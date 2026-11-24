package main

// The NETWORK SHARES section of the Disks page: the saved shares
// ($XDG_CONFIG_HOME/isotope/shares.json, pkg/shares) and the mounted ones,
// "Connect to server…" for an smb:// or nfs:// address, Mount, Unmount and
// Forget. isotope-updated mounts (MountShare, polkit
// io.kairos.isotope-update.shares: a local, active session); the user name,
// domain and password go to it through a pipe fd. A remembered password is
// in the user's keyring (Secret Service), never in the file and never in
// the daemon.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"
	"github.com/godbus/dbus/v5"

	"github.com/kairos-community/isotope/pkg/guikit"
	"github.com/kairos-community/isotope/pkg/i18n"
	"github.com/kairos-community/isotope/pkg/shares"
)

// MountedShare is one of the daemon's MountedShares.
type MountedShare struct {
	MountPoint, FSType, Source string
}

// ShareStore is the user's saved shares and the keyring that holds their
// passwords.
type ShareStore interface {
	Saved() ([]shares.Saved, error)
	SetSaved([]shares.Saved) error
	shares.Keyring
}

// fileShares is ShareStore over shares.json and the Secret Service.
type fileShares struct {
	path    string
	keyring shares.Keyring // nil without a session bus
}

func newFileShares() (*fileShares, error) {
	p, err := shares.Path()
	if err != nil {
		return nil, err
	}
	s := &fileShares{path: p}
	if c, err := dbus.SessionBus(); err == nil {
		s.keyring = shares.SecretService{Conn: c}
	}
	return s, nil
}

func (s *fileShares) Saved() ([]shares.Saved, error)  { return shares.Load(s.path) }
func (s *fileShares) SetSaved(l []shares.Saved) error { return shares.Store(s.path, l) }

var errNoKeyring = errors.New("the keyring (Secret Service) is not available")

func (s *fileShares) Password(url, user string) (string, bool, error) {
	if s.keyring == nil {
		return "", false, nil
	}
	return s.keyring.Password(url, user)
}

func (s *fileShares) StorePassword(url, user, pw string) error {
	if s.keyring == nil {
		return errNoKeyring
	}
	return s.keyring.StorePassword(url, user, pw)
}

func (s *fileShares) ForgetPassword(url, user string) error {
	if s.keyring == nil {
		return nil
	}
	return s.keyring.ForgetPassword(url, user)
}

// shareTimeout bounds MountShare: the daemon allows 60 s per mount call,
// and tries two NFS versions.
const shareTimeout = 150 * time.Second

func (d *busDaemon) MountShare(url string, o shares.Options, c shares.Credentials) (string, error) {
	opts := map[string]dbus.Variant{}
	if o.Name != "" {
		opts["name"] = dbus.MakeVariant(o.Name)
	}
	if o.ReadOnly {
		opts["read_only"] = dbus.MakeVariant(true)
	}
	var point string
	err := withSecrets(c.Lines(), func(fd dbus.UnixFD) error {
		ctx, cancel := context.WithTimeout(context.Background(), shareTimeout)
		defer cancel()
		return d.callCtx(ctx, 0, "MountShare", []any{&point}, url, opts, fd)
	})
	return point, err
}

func (d *busDaemon) UnmountShare(point string) error {
	return d.callTimeout(longTimeout, "UnmountShare", nil, point)
}

func (d *busDaemon) MountedShares() ([]MountedShare, error) {
	var list []map[string]dbus.Variant
	if err := d.call(0, "MountedShares", []any{&list}); err != nil {
		return nil, err
	}
	out := make([]MountedShare, 0, len(list))
	for _, m := range list {
		out = append(out, MountedShare{MountPoint: str(m, "mountpoint"), FSType: str(m, "fstype"), Source: str(m, "source")})
	}
	return out, nil
}

// nfsAdminText says why NFS is for administrators (polkit
// io.kairos.isotope-update.shares-nfs).
var nfsAdminText = i18n.Mark("Only an administrator can mount an NFS share. NFS lets in this computer by its address, not you, so it could show you other people's files on the server. Ask an administrator, or use SMB.")

// describeShareErr is Describe for the share calls: their NotAuthorized
// is about the session, or about NFS, not the admin group.
func describeShareErr(err error) string {
	if errName(err) == "NotAuthorized" {
		if strings.Contains(err.Error(), "Flatpak") {
			return err.Error()
		}
		if strings.Contains(err.Error(), "NFS") {
			return i18n.T(nfsAdminText)
		}
		return i18n.T("Only the person at this computer's screen can mount a network share.")
	}
	return Describe(err)
}

// ---- the section's view ----

// shareRow is one line of the section: a saved share (Saved >= 0), mounted
// or not, or a mounted share that is not saved.
type shareRow struct {
	Key, Title, Sub string
	Saved           int // index in the saved list, -1 for none
	MountPoint      string
}

// shareRows joins the saved list and the mounted shares: the saved ones
// first, in their order, then the mounted ones nobody saved.
func shareRows(saved []shares.Saved, mounted []MountedShare) []shareRow {
	used := make([]bool, len(mounted))
	var out []shareRow
	for i, s := range saved {
		r := shareRow{Key: "saved:" + s.URL, Title: s.Name, Saved: i}
		if l, err := shares.Parse(s.URL); err == nil {
			for j, m := range mounted {
				if ml, ok := shares.FromMount(m.FSType, m.Source); ok && !used[j] && ml.Same(l) {
					used[j], r.MountPoint = true, m.MountPoint
					break
				}
			}
		}
		parts := []string{s.URL}
		if s.User != "" {
			who := s.User
			if s.Domain != "" {
				who = s.Domain + "\\" + s.User
			}
			// TRANSLATORS: %s is the user name the share is mounted as.
			parts = append(parts, i18n.Tf("as %s", who))
		}
		if r.MountPoint != "" {
			parts = append(parts, i18n.Tf("Mounted at %s", r.MountPoint))
		} else {
			parts = append(parts, i18n.T("Not mounted"))
		}
		if s.AutoMount {
			parts = append(parts, i18n.T("Mounts at login"))
		}
		r.Sub = strings.Join(parts, " · ")
		out = append(out, r)
	}
	for j, m := range mounted {
		if used[j] {
			continue
		}
		addr := m.Source
		if l, ok := shares.FromMount(m.FSType, m.Source); ok {
			addr = l.String()
		}
		out = append(out, shareRow{Key: "mounted:" + m.MountPoint, Title: filepath.Base(m.MountPoint), Saved: -1,
			MountPoint: m.MountPoint, Sub: addr + " · " + i18n.Tf("Mounted at %s", m.MountPoint) + " · " + i18n.T("Not saved")})
	}
	return out
}

type shareClicks struct{ mount, unmount, forget widget.Clickable }

func (p *disksPage) shClicks(key string) *shareClicks {
	k, ok := p.shareClicks[key]
	if !ok {
		k = &shareClicks{}
		p.shareClicks[key] = k
	}
	return k
}

// loadShares reads the saved list and the mounted shares.
func (p *disksPage) loadShares() {
	u, d, st := p.u, p.u.hooks.Daemon, p.u.hooks.Shares
	if d == nil {
		return
	}
	u.async(func() {
		var saved []shares.Saved
		var serr error
		if st != nil {
			saved, serr = st.Saved()
		}
		mounted, merr := d.MountedShares()
		u.mu.Lock()
		defer u.mu.Unlock()
		p.shLoaded = true
		p.shSaved, p.shMounted, p.shErr = saved, mounted, ""
		switch {
		case merr != nil:
			p.shErr = describeShareErr(merr)
		case serr != nil:
			p.shErr = i18n.Tf("Could not read the saved shares: %s", serr.Error())
		}
	})
}

// shareDo runs one share change off the UI thread, tells its error, and
// reads the section again.
func (p *disksPage) shareDo(working, failed string, f func(Daemon, ShareStore) error) {
	u, d, st := p.u, p.u.hooks.Daemon, p.u.hooks.Shares
	if d == nil {
		return
	}
	u.mu.Lock()
	p.shWorking = working
	u.mu.Unlock()
	u.async(func() {
		err := f(d, st)
		u.mu.Lock()
		p.shWorking = ""
		u.mu.Unlock()
		if err != nil {
			u.tell(failed, describeShareErr(err))
		}
		p.loadShares()
	})
}

func (p *disksPage) sharesNow() ([]shares.Saved, []MountedShare) {
	p.u.mu.Lock()
	defer p.u.mu.Unlock()
	return append([]shares.Saved(nil), p.shSaved...), append([]MountedShare(nil), p.shMounted...)
}

// mountSaved mounts a saved share with its password from the keyring. A
// share with a user and no saved password opens the connect dialog to ask
// for it.
func (p *disksPage) mountSaved(s shares.Saved) {
	u, st := p.u, p.u.hooks.Shares
	if l, err := shares.Parse(s.URL); err == nil && l.Scheme == shares.NFS && !u.sys.Admin {
		u.tell(i18n.Tf("Could not mount %s", s.Name), i18n.T(nfsAdminText))
		return
	}
	u.async(func() {
		c := shares.Credentials{User: s.User, Domain: s.Domain}
		if s.User != "" {
			pw, found := "", false
			if st != nil {
				pw, found, _ = st.Password(s.URL, s.User)
			}
			if !found {
				p.connectDialog(&s)
				return
			}
			c.Password = pw
		}
		p.shareDo(i18n.Tf("Mounting %s…", s.Name), i18n.Tf("Could not mount %s", s.Name), func(d Daemon, _ ShareStore) error {
			_, err := d.MountShare(s.URL, shares.Options{Name: s.Name}, c)
			return err
		})
	})
}

func (p *disksPage) unmountShare(r shareRow) {
	p.shareDo(i18n.Tf("Unmounting %s…", r.Title), i18n.Tf("Could not unmount %s", r.Title), func(d Daemon, _ ShareStore) error {
		return d.UnmountShare(r.MountPoint)
	})
}

func (p *disksPage) forgetShare(s shares.Saved) {
	p.u.ask(i18n.Tf("Forget %s?", s.Name), i18n.T("It is removed from the list, with its saved password. A mounted share stays mounted until you unmount it."),
		choice{Label: i18n.T("Cancel"), Kind: guikit.ButtonSecondary},
		choice{Label: i18n.T("Forget"), Kind: guikit.ButtonDanger, Do: func() {
			p.shareDo(i18n.Tf("Forgetting %s…", s.Name), i18n.Tf("Could not forget %s", s.Name), func(_ Daemon, st ShareStore) error {
				if st == nil {
					return nil
				}
				l, err := shares.Parse(s.URL)
				if err != nil {
					return err
				}
				list, err := st.Saved()
				if err != nil {
					return err
				}
				if err := st.SetSaved(shares.Remove(list, l)); err != nil {
					return err
				}
				if s.User != "" {
					return st.ForgetPassword(s.URL, s.User)
				}
				return nil
			})
		}})
}

// updateShares handles the section's clicks. u.mu is not held.
func (p *disksPage) updateShares(gtx layout.Context) {
	if p.connect.Clicked(gtx) {
		p.connectDialog(nil)
	}
	saved, mounted := p.sharesNow()
	for _, r := range shareRows(saved, mounted) {
		k := p.shClicks(r.Key)
		if k.mount.Clicked(gtx) && r.Saved >= 0 && r.MountPoint == "" {
			p.mountSaved(saved[r.Saved])
		}
		if k.unmount.Clicked(gtx) && r.MountPoint != "" {
			p.unmountShare(r)
		}
		if k.forget.Clicked(gtx) && r.Saved >= 0 {
			p.forgetShare(saved[r.Saved])
		}
	}
}

// sharesSection is the NETWORK SHARES heading and its panel.
func (p *disksPage) sharesSection() layout.Widget {
	u := p.u
	u.mu.Lock()
	loaded, errText, working := p.shLoaded, p.shErr, p.shWorking
	u.mu.Unlock()
	saved, mounted := p.sharesNow()
	rows := shareRows(saved, mounted)
	off := working != ""
	return func(gtx layout.Context) layout.Dimensions {
		var c col
		c.add(u.caps(i18n.T("NETWORK SHARES")))
		c.gap(10)
		if u.hooks.Daemon == nil {
			c.add(u.errorBox(i18n.T("The update service (isotope-updated), which mounts network shares, is not available.")))
			return c.layout(gtx)
		}
		c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
			var in col
			switch {
			case errText != "":
				in.add(u.dim(errText))
				in.gap(10)
			case !loaded:
				in.add(u.dim(i18n.T("Loading…")))
				in.gap(10)
			case len(rows) == 0:
				in.add(u.dim(i18n.T("No saved shares. Connect to an SMB (Windows, Samba, a NAS) or NFS server.")))
				in.gap(10)
			}
			if working != "" {
				in.add(u.spinnerLine(working))
				in.gap(10)
			}
			for _, r := range rows {
				k := p.shClicks(r.Key)
				var as []guikit.Action
				if r.MountPoint != "" {
					as = append(as, guikit.Action{Click: &k.unmount, Kind: guikit.ButtonSecondary, Label: i18n.T("Unmount"), Disabled: off})
				} else {
					as = append(as, guikit.Action{Click: &k.mount, Kind: guikit.ButtonSecondary, Label: i18n.T("Mount"), Disabled: off})
				}
				if r.Saved >= 0 {
					as = append(as, guikit.Action{Click: &k.forget, Kind: guikit.ButtonGhost, Label: i18n.T("Forget"), Disabled: off})
				}
				in.add(u.row(r.Title, r.Sub, u.buttons(as...)))
				in.gap(10)
			}
			in.add(u.buttons(guikit.Action{Click: &p.connect, Kind: guikit.ButtonSecondary, Label: i18n.T("Connect to server…"), Disabled: off}))
			return in.layout(gtx)
		}))
		return c.layout(gtx)
	}
}

// ---- Connect to server ----

// connectForm is the "Connect to server" dialog's state.
type connectForm struct {
	url, user, domain, password *guikit.Field
	save, auto, savePw          bool
	saveClick, autoClick        widget.Clickable
	savePwClick                 widget.Clickable
	// name is the folder name of a saved share being mounted again.
	name    string
	focused bool
	// admin: the user may mount NFS (polkit shares-nfs).
	admin bool
}

var errNFSNeedsAdmin = errors.New("only an administrator can mount an NFS share")

// location is the parsed address, or its error ("" text is no error yet).
func (f *connectForm) location() (shares.Location, error) {
	return shares.Parse(f.url.Editor.Text())
}

func (f *connectForm) nfs() bool {
	l, err := f.location()
	return err == nil && l.Scheme == shares.NFS
}

// check is the form's first problem, nil when it can connect.
func (f *connectForm) check() error {
	l, err := f.location()
	if err != nil {
		return err
	}
	if l.Scheme == shares.NFS {
		if !f.admin {
			return errNFSNeedsAdmin
		}
		return nil
	}
	user := strings.TrimSpace(f.user.Editor.Text())
	if err := shares.CheckCredential("user name", user); err != nil {
		return err
	}
	if err := shares.CheckCredential("domain", strings.TrimSpace(f.domain.Editor.Text())); err != nil {
		return err
	}
	if user == "" && (f.password.Editor.Text() != "" || strings.TrimSpace(f.domain.Editor.Text()) != "") {
		return errors.New(i18n.T("a password or a domain needs a user name"))
	}
	return nil
}

// request is what the form asks for: the share to save and the password.
func (f *connectForm) request() (shares.Saved, string, error) {
	if err := f.check(); err != nil {
		return shares.Saved{}, "", err
	}
	l, _ := f.location()
	s := shares.Saved{URL: l.String(), Name: f.name, AutoMount: f.save && f.auto}
	if s.Name == "" {
		s.Name = l.DefaultName()
	}
	pw := ""
	if l.Scheme == shares.SMB {
		s.User = strings.TrimSpace(f.user.Editor.Text())
		s.Domain = strings.TrimSpace(f.domain.Editor.Text())
		pw = f.password.Editor.Text()
	}
	return s, pw, nil
}

func newConnectForm(s *shares.Saved, admin bool) *connectForm {
	f := &connectForm{url: guikit.NewField(true), user: guikit.NewField(true), domain: guikit.NewField(true),
		password: guikit.NewSecret(), save: true, savePw: true, admin: admin}
	if s != nil {
		f.url.Editor.SetText(s.URL)
		f.user.Editor.SetText(s.User)
		f.domain.Editor.SetText(s.Domain)
		f.name, f.auto = s.Name, s.AutoMount
	}
	return f
}

// connectDialog asks for a server address and its login, mounts it, and
// saves it when asked. With s it asks again for a saved share whose
// password is not in the keyring. Safe from any goroutine.
func (p *disksPage) connectDialog(s *shares.Saved) {
	u := p.u
	f := newConnectForm(s, u.sys.Admin)
	u.mu.Lock()
	p.connForm = f
	u.mu.Unlock()
	var dlg *dialog
	title, body := i18n.T("Connect to server"), i18n.T("Mount a shared folder of another computer. It shows in your files under /run/media.")
	if s != nil {
		title, body = i18n.Tf("Mount %s", s.Name), i18n.Tf("Type the password for %s.", s.URL)
	}
	dlg = newDialog(title, body, func(gtx layout.Context) layout.Dimensions {
		return p.connectFormView(gtx, f, dlg)
	}, 1,
		choice{Label: i18n.T("Cancel"), Kind: guikit.ButtonSecondary},
		choice{Label: i18n.T("Connect"), Kind: guikit.ButtonPrimary, Enabled: func() bool { return f.check() == nil }, Do: func() {
			req, pw, err := f.request()
			if err != nil {
				return
			}
			remember, keepPw := f.save, f.save && f.savePw && pw != ""
			f.password.Editor.SetText("")
			p.shareDo(i18n.Tf("Connecting to %s…", req.URL), i18n.Tf("Could not connect to %s", req.URL), func(d Daemon, st ShareStore) error {
				_, err := d.MountShare(req.URL, shares.Options{Name: req.Name}, shares.Credentials{User: req.User, Domain: req.Domain, Password: pw})
				if err != nil {
					return err
				}
				if !remember || st == nil {
					return nil
				}
				list, err := st.Saved()
				if err != nil {
					return err
				}
				if err := st.SetSaved(shares.Put(list, req)); err != nil {
					return err
				}
				if keepPw {
					if err := st.StorePassword(req.URL, req.User, pw); err != nil {
						return errors.New(i18n.Tf("the share is mounted and saved, but its password could not be saved in the keyring: %s", err.Error()))
					}
				}
				return nil
			})
		}})
	u.open(dlg)
}

func (p *disksPage) connectFormView(gtx layout.Context, f *connectForm, dlg *dialog) layout.Dimensions {
	u := p.u
	for _, fl := range []*guikit.Field{f.url, f.user, f.domain, f.password} {
		if fl.Submitted(gtx) {
			u.pick(dlg, 1) // ignored while Connect is off
		}
	}
	if f.saveClick.Clicked(gtx) {
		f.save = !f.save
	}
	if f.autoClick.Clicked(gtx) {
		f.auto = !f.auto
	}
	if f.savePwClick.Clicked(gtx) {
		f.savePw = !f.savePw
	}
	if !f.focused {
		f.focused = true
		target := &f.url.Editor
		if f.url.Editor.Text() != "" {
			target = &f.password.Editor
		}
		gtx.Execute(focus(target))
	}
	f.url.Err, f.user.Err, f.domain.Err = "", "", ""
	if strings.TrimSpace(f.url.Editor.Text()) != "" {
		if _, err := f.location(); err != nil {
			f.url.Err = upperFirst(err.Error()) + "."
		}
	}
	if err := shares.CheckCredential("user name", strings.TrimSpace(f.user.Editor.Text())); err != nil {
		f.user.Err = upperFirst(err.Error()) + "."
	}
	if err := shares.CheckCredential("domain", strings.TrimSpace(f.domain.Editor.Text())); err != nil {
		f.domain.Err = upperFirst(err.Error()) + "."
	}
	nfs := f.nfs()
	input := func(title string, fl *guikit.Field, hint string) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			return u.Labelled(gtx, title, fl, hint, func(gtx layout.Context) layout.Dimensions {
				return u.Input(gtx, fl, guikit.StBody, 38, "", nil)
			})
		}
	}
	var c col
	c.add(input(i18n.T("Server address"), f.url, i18n.T("smb://server/share or nfs://server/export")))
	switch {
	case nfs && !f.admin:
		c.gap(12)
		c.add(u.notice(i18n.T(nfsAdminText)))
		return c.layout(gtx)
	case nfs:
		c.gap(12)
		c.add(u.dim(i18n.T("NFS needs no password. Isotope uses NFS version 4.1 or later. Files show the owners the server has, which may not be you.")))
	default:
		c.gap(12)
		c.add(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{}.Layout(gtx,
				layout.Flexed(0.55, input(i18n.T("User"), f.user, i18n.T("Empty for a guest."))),
				layout.Rigid(layout.Spacer{Width: 12}.Layout),
				layout.Flexed(0.45, input(i18n.T("Domain"), f.domain, i18n.T("Optional."))))
		})
		c.gap(12)
		c.add(input(i18n.T("Password"), f.password, ""))
	}
	c.gap(16)
	c.add(checkRow(u, &f.saveClick, f.save, i18n.T("Remember this share on this page")))
	if f.save {
		c.gap(10)
		c.add(checkRow(u, &f.autoClick, f.auto, i18n.T("Mount it at login")))
		if !nfs {
			c.gap(10)
			c.add(checkRow(u, &f.savePwClick, f.savePw, i18n.T("Remember the password in your keyring")))
		}
	}
	return c.layout(gtx)
}

// checkRow is a checkbox and its label on one line; a click on either
// toggles it.
func checkRow(u *UI, click *widget.Clickable, on bool, label string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return click.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return guikit.Checkbox(gtx, on) }),
				layout.Rigid(layout.Spacer{Width: 10}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.Label(gtx, guikit.StBody, guikit.ColText, label)
				}))
		})
	}
}

// upperFirst makes the first letter a capital.
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

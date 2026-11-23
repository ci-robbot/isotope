package main

import (
	"errors"
	"image"
	"strings"
	"sync"
	"testing"

	"github.com/kairos-community/isotope/pkg/shares"
)

// fakeShareStore is the saved list and the keyring, in memory.
type fakeShareStore struct {
	mu    sync.Mutex
	saved []shares.Saved
	pw    map[string]string // url|user -> password
	err   map[string]error
}

func newFakeShareStore() *fakeShareStore {
	return &fakeShareStore{pw: map[string]string{}, err: map[string]error{}}
}

func (s *fakeShareStore) Saved() ([]shares.Saved, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]shares.Saved(nil), s.saved...), s.err["Saved"]
}
func (s *fakeShareStore) SetSaved(l []shares.Saved) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.err["SetSaved"]; err != nil {
		return err
	}
	s.saved = append([]shares.Saved(nil), l...)
	return nil
}
func (s *fakeShareStore) Password(url, user string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pw, ok := s.pw[url+"|"+user]
	return pw, ok, nil
}
func (s *fakeShareStore) StorePassword(url, user, pw string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.err["StorePassword"]; err != nil {
		return err
	}
	s.pw[url+"|"+user] = pw
	return nil
}
func (s *fakeShareStore) ForgetPassword(url, user string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pw, url+"|"+user)
	return nil
}

func (s *fakeShareStore) passwords() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for k, v := range s.pw {
		out[k] = v
	}
	return out
}

// sharesUI is the Disks page with the fake udisks2, the fake daemon and a
// share store, its shares read.
func sharesUI(t *testing.T) (*UI, *fakeDaemon, *fakeShareStore, *disksPage) {
	t.Helper()
	return sharesUIAs(t, false)
}

// sharesUIAs is sharesUI for an administrator or not.
func sharesUIAs(t *testing.T, admin bool) (*UI, *fakeDaemon, *fakeShareStore, *disksPage) {
	t.Helper()
	u, d, _ := testUI(t, admin)
	f := newFakeDisks()
	st := newFakeShareStore()
	u.hooks.Disks, u.hooks.Shares = f, st
	p := u.view(PageDisks).(*disksPage)
	p.setRoots(func() ([]string, error) { return f.roots, nil })
	return u, d, st, p
}

func showShares(t *testing.T, u *UI, p *disksPage) {
	t.Helper()
	u.mu.Lock()
	p.shLoaded = false
	u.mu.Unlock()
	u.show(PageDisks)
	eventually(t, "the shares", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return p.shLoaded && p.loaded })
}

func rowsOf(p *disksPage) []shareRow {
	saved, mounted := p.sharesNow()
	return shareRows(saved, mounted)
}

func TestShareRowsJoinSavedAndMounted(t *testing.T) {
	saved := []shares.Saved{
		{URL: "smb://nas/media", Name: "media", User: "anna", Domain: "HOME", AutoMount: true},
		{URL: "nfs://srv/data", Name: "data"},
	}
	mounted := []MountedShare{
		{MountPoint: "/run/media/anna/other", FSType: "cifs", Source: "//pc/other"},
		{MountPoint: "/run/media/anna/media", FSType: "cifs", Source: "//NAS/media"},
	}
	rows := shareRows(saved, mounted)
	want := []shareRow{
		{Key: "saved:smb://nas/media", Title: "media", Saved: 0, MountPoint: "/run/media/anna/media",
			Sub: `smb://nas/media · as HOME\anna · Mounted at /run/media/anna/media · Mounts at login`},
		{Key: "saved:nfs://srv/data", Title: "data", Saved: 1, Sub: "nfs://srv/data · Not mounted"},
		{Key: "mounted:/run/media/anna/other", Title: "other", Saved: -1, MountPoint: "/run/media/anna/other",
			Sub: "smb://pc/other · Mounted at /run/media/anna/other · Not saved"},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows %+v", rows)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d\n%+v\nwant\n%+v", i, rows[i], want[i])
		}
	}
}

func TestConnectMountsSavesAndKeepsThePasswordInTheKeyring(t *testing.T) {
	u, d, st, p := sharesUI(t)
	showShares(t, u, p)
	p.connectDialog(nil)
	q := waitQuestion(t, u, "Connect to server")
	f := p.connForm
	if q.choices[1].enabled() {
		t.Fatal("Connect is on with an empty form")
	}
	f.url.Editor.SetText("smb://anna@nas/media")
	if q.choices[1].enabled() {
		t.Fatal("Connect is on with a user in the address")
	}
	f.url.Editor.SetText("smb://nas/media")
	f.password.Editor.SetText("s3cret")
	if q.choices[1].enabled() {
		t.Fatal("Connect is on with a password and no user")
	}
	f.user.Editor.SetText("anna")
	f.domain.Editor.SetText("HOME")
	f.auto = true
	if !q.choices[1].enabled() {
		t.Fatalf("Connect is off: %v", f.check())
	}
	u.pick(q, 1)
	eventually(t, "the mount", func() bool { return contains(d.called(), "MountShare smb://nas/media media") })
	eventually(t, "the saved share", func() bool { l, _ := st.Saved(); return len(l) == 1 })
	l, _ := st.Saved()
	if l[0] != (shares.Saved{URL: "smb://nas/media", Name: "media", User: "anna", Domain: "HOME", AutoMount: true}) {
		t.Fatalf("saved %+v", l[0])
	}
	if pw := st.passwords()["smb://nas/media|anna"]; pw != "s3cret" {
		t.Fatalf("keyring %v", st.passwords())
	}
	d.mu.Lock()
	c := d.shareCreds[0]
	d.mu.Unlock()
	if c != (shares.Credentials{User: "anna", Domain: "HOME", Password: "s3cret"}) {
		t.Fatalf("credentials %+v", c)
	}
	for _, call := range d.called() {
		if strings.Contains(call, "s3cret") {
			t.Fatalf("a call string holds the password: %q", call)
		}
	}
	if f.password.Editor.Text() != "" {
		t.Error("the typed password stays in the form")
	}
	eventually(t, "the mounted row", func() bool {
		rs := rowsOf(p)
		return len(rs) == 1 && rs[0].MountPoint == "/run/media/anna/media"
	})
}

func TestConnectWithoutRememberSavesNothing(t *testing.T) {
	u, d, st, p := sharesUIAs(t, true)
	showShares(t, u, p)
	p.connectDialog(nil)
	q := waitQuestion(t, u, "Connect to server")
	f := p.connForm
	f.url.Editor.SetText("nfs://srv/export/data")
	if !f.nfs() {
		t.Fatal("an nfs:// address is NFS")
	}
	f.user.Editor.SetText("ignored") // hidden for NFS, and not sent
	f.save = false
	u.pick(q, 1)
	eventually(t, "the mount", func() bool { return contains(d.called(), "MountShare nfs://srv/export/data data") })
	d.mu.Lock()
	c := d.shareCreds[0]
	d.mu.Unlock()
	if c != (shares.Credentials{}) {
		t.Fatalf("NFS got credentials %+v", c)
	}
	eventually(t, "the reload", func() bool { return len(rowsOf(p)) == 1 })
	if l, _ := st.Saved(); len(l) != 0 {
		t.Fatalf("saved %+v", l)
	}
}

func TestMountSavedUsesTheKeyringOrAsks(t *testing.T) {
	u, d, st, p := sharesUI(t)
	st.saved = []shares.Saved{
		{URL: "smb://nas/media", Name: "media", User: "anna"},
		{URL: "smb://nas/music", Name: "music", User: "anna"},
		{URL: "smb://nas/public", Name: "public"},
	}
	st.pw["smb://nas/media|anna"] = "s3cret"
	showShares(t, u, p)
	rows := rowsOf(p)
	if len(rows) != 3 {
		t.Fatalf("rows %+v", rows)
	}
	p.mountSaved(st.saved[0])
	eventually(t, "media", func() bool { return contains(d.called(), "MountShare smb://nas/media media") })
	p.mountSaved(st.saved[2])
	eventually(t, "public", func() bool { return contains(d.called(), "MountShare smb://nas/public public") })
	// No password in the keyring: the dialog asks, with the share filled in.
	p.mountSaved(st.saved[1])
	q := waitQuestion(t, u, "Mount music")
	if contains(d.called(), "MountShare smb://nas/music music") {
		t.Fatal("mounted without a password")
	}
	u.mu.Lock()
	f := p.connForm
	u.mu.Unlock()
	if f.url.Editor.Text() != "smb://nas/music" || f.user.Editor.Text() != "anna" || f.name != "music" {
		t.Fatalf("the form is %q %q %q", f.url.Editor.Text(), f.user.Editor.Text(), f.name)
	}
	f.password.Editor.SetText("pw2")
	u.pick(q, 1)
	eventually(t, "music", func() bool { return contains(d.called(), "MountShare smb://nas/music music") })
	eventually(t, "music's password", func() bool { return st.passwords()["smb://nas/music|anna"] == "pw2" })
	d.mu.Lock()
	creds := append([]shares.Credentials(nil), d.shareCreds...)
	d.mu.Unlock()
	if creds[0].Password != "s3cret" || creds[1] != (shares.Credentials{}) {
		t.Fatalf("credentials %+v", creds)
	}
}

func TestUnmountAndForget(t *testing.T) {
	u, d, st, p := sharesUI(t)
	st.saved = []shares.Saved{{URL: "smb://nas/media", Name: "media", User: "anna"}}
	st.pw["smb://nas/media|anna"] = "s3cret"
	d.mounted = []MountedShare{{MountPoint: "/run/media/anna/media", FSType: "cifs", Source: "//nas/media"}}
	showShares(t, u, p)
	r := rowsOf(p)[0]
	p.unmountShare(r)
	eventually(t, "the unmount", func() bool { return contains(d.called(), "UnmountShare /run/media/anna/media") })
	eventually(t, "the row", func() bool { rs := rowsOf(p); return len(rs) == 1 && rs[0].MountPoint == "" })
	p.forgetShare(st.saved[0])
	q := waitQuestion(t, u, "Forget media?")
	u.pick(q, 1)
	eventually(t, "the forget", func() bool { l, _ := st.Saved(); return len(l) == 0 })
	eventually(t, "the password", func() bool { return len(st.passwords()) == 0 })
}

func TestShareErrorsAreTold(t *testing.T) {
	u, d, _, p := sharesUI(t)
	showShares(t, u, p)
	d.mu.Lock()
	d.err = map[string]error{"MountShare": &DaemonError{Name: "NotAuthorized", Msg: "not authorized"}}
	d.mu.Unlock()
	p.connectDialog(nil)
	q := waitQuestion(t, u, "Connect to server")
	p.connForm.url.Editor.SetText("smb://nas/media")
	u.pick(q, 1)
	q = waitQuestion(t, u, "Could not connect to smb://nas/media")
	if !strings.Contains(q.body, "person at this computer's screen") {
		t.Fatalf("body %q", q.body)
	}
	d.mu.Lock()
	d.err = map[string]error{"MountedShares": errors.New("boom")}
	d.mu.Unlock()
	showShares(t, u, p)
	u.mu.Lock()
	e := p.shErr
	u.mu.Unlock()
	if !strings.Contains(e, "boom") {
		t.Fatalf("shErr %q", e)
	}
	u.Layout(gtxOf(1100, 720))
}

func TestSharesSectionDrawsAtBothSizes(t *testing.T) {
	u, d, st, p := sharesUI(t)
	st.saved = []shares.Saved{{URL: "smb://nas/media", Name: "media", User: "anna", AutoMount: true}}
	d.mounted = []MountedShare{{MountPoint: "/run/media/anna/media", FSType: "cifs", Source: "//nas/media"}}
	showShares(t, u, p)
	for _, sz := range []image.Point{{1100, 720}, {700, 500}} {
		if dims := u.Layout(gtxOf(sz.X, sz.Y)); dims.Size != sz {
			t.Errorf("at %v: %v", sz, dims.Size)
		}
	}
	p.connectDialog(nil)
	waitQuestion(t, u, "Connect to server")
	for _, sz := range []image.Point{{1100, 720}, {700, 500}} {
		u.Layout(gtxOf(sz.X, sz.Y))
	}
	// Without a share store and without the daemon.
	u.hooks.Shares = nil
	showShares(t, u, p)
	u.Layout(gtxOf(1100, 720))
	u2, _, _ := testUI(t, true)
	u2.hooks.Daemon = nil
	u2.show(PageDisks)
	u2.Layout(gtxOf(1100, 720))
}

// An NTFS partition of a USB disk is a filesystem like any other: the page
// offers Mount (udisks2 mounts it with the kernel's ntfs3 driver,
// /etc/udisks2/mount_options.conf).
func TestDisksPageMountsNTFS(t *testing.T) {
	u, f, p := disksUI(t, false)
	bd := "/org/freedesktop/UDisks2/block_devices/"
	f.mu.Lock()
	f.drives = append(f.drives, Drive{ID: "/org/freedesktop/UDisks2/drives/WD_Passport", Vendor: "WD", Model: "My Passport", Size: 1e12, Removable: true, Ejectable: true,
		Blocks: []Block{
			{ID: bd + "sdc", Device: "/dev/sdc", Size: 1e12},
			{ID: bd + "sdc1", Device: "/dev/sdc1", Label: "Windows", FSType: "ntfs", UUID: "0123456789ABCDEF", Size: 1e12, IsFilesystem: true, IsPartition: true},
		}})
	f.mu.Unlock()
	p.load()
	eventually(t, "the NTFS disk", func() bool { return len(p.snapshot()) == 3 })
	wd := p.driveOf(t, "/org/freedesktop/UDisks2/drives/WD_Passport")
	if got := labels(p.blockActions(wd, wd.Blocks[1])); strings.Join(got, ",") != "Mount" {
		t.Fatalf("NTFS actions %q", got)
	}
	if !usableForBackupOn(wd.Blocks[1], f.roots) {
		t.Error("an NTFS disk is usable for a backup")
	}
	p.mount(bd + "sdc1")
	eventually(t, "Mount", func() bool { return contains(f.called(), "Mount "+bd+"sdc1") })
	u.Layout(gtxOf(1100, 720))
}

// NFS is for administrators: the dialog says why and Connect stays off, and
// a saved NFS share says so instead of calling the daemon.
func TestNFSNeedsAnAdministrator(t *testing.T) {
	u, d, st, p := sharesUI(t)
	st.saved = []shares.Saved{{URL: "nfs://srv/data", Name: "data"}}
	showShares(t, u, p)
	p.connectDialog(nil)
	q := waitQuestion(t, u, "Connect to server")
	p.connForm.url.Editor.SetText("nfs://srv/export")
	if q.choices[1].enabled() {
		t.Fatal("Connect is on for NFS and a non-admin")
	}
	if err := p.connForm.check(); err != errNFSNeedsAdmin {
		t.Fatalf("check = %v", err)
	}
	u.Layout(gtxOf(1100, 720))
	p.connForm.url.Editor.SetText("smb://nas/media")
	if !q.choices[1].enabled() {
		t.Fatal("Connect is off for SMB")
	}
	u.closeDialog()
	p.mountSaved(st.saved[0])
	q = waitQuestion(t, u, "Could not mount data")
	if q.body != nfsAdminText {
		t.Fatalf("body %q", q.body)
	}
	if contains(d.called(), "MountShare nfs://srv/data data") {
		t.Fatal("the daemon was called")
	}
	// The daemon's own refusal reads the same.
	err := &DaemonError{Name: "NotAuthorized", Msg: "not authorized: only an administrator can mount an NFS share, because NFS trusts this computer, not the user"}
	if describeShareErr(err) != nfsAdminText {
		t.Fatalf("describeShareErr = %q", describeShareErr(err))
	}
}

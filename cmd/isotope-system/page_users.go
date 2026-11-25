package main

import (
	"regexp"
	"strings"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/guikit"
	"github.com/kairos-community/isotope/pkg/i18n"
)

func init() { pageMakers[PageUsers] = func(u *UI) pageView { return newUsersPage(u) } }

var (
	onlyAdmin = i18n.Mark("Only an administrator can add, remove or change other users.")
	nameRule  = i18n.Mark("Use lowercase letters, digits, - and _. Start with a letter or _. At most 32 characters.")
)

const maxPassword = 256 // bytes; the daemon refuses more

var userNameRE = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// passwordProblem says why the daemon would refuse pw, "" when it would not.
func passwordProblem(pw string) string {
	switch {
	case len(pw) > maxPassword:
		return i18n.T("A password has at most 256 bytes.")
	case strings.ContainsAny(pw, "\r\n"):
		return i18n.T("A password cannot have a line break.")
	}
	return ""
}

// usersPage: the users of this machine, changed through isotope-updated
// (Users, AddUser, RemoveUser, SetPassword, SetAdmin), which writes /etc
// and /oem together.
type usersPage struct {
	u    *UI
	list widget.List

	// Under u.mu.
	users   []User
	loaded  bool
	loadErr string

	add addForm
	pw  pwForm
	rm  rmForm

	addBtn widget.Clickable
	rows   map[string]*userClicks

	// fp is the FINGERPRINTS section, the user's own (fprint_section.go).
	fp *fingerSection
}

type userClicks struct{ pw, admin, rm widget.Clickable }

type addForm struct {
	name, full, pw, pw2 *guikit.Field
	admin               bool
	adminClick          widget.Clickable
	focused             bool
	taken               func(name string) bool
}

type pwForm struct {
	cur, newPw, again *guikit.Field
	own               bool
	focused           bool
}

type rmForm struct {
	keep      bool
	keepClick widget.Clickable
}

func newUsersPage(u *UI) *usersPage {
	p := &usersPage{u: u, rows: map[string]*userClicks{}}
	p.add = addForm{name: guikit.NewField(false), full: guikit.NewField(false), pw: guikit.NewSecret(), pw2: guikit.NewSecret()}
	p.add.taken = p.exists
	p.pw = pwForm{cur: guikit.NewSecret(), newPw: guikit.NewSecret(), again: guikit.NewSecret()}
	p.fp = newFingerSection(u)
	return p
}

func (p *usersPage) exists(name string) bool {
	p.u.mu.Lock()
	defer p.u.mu.Unlock()
	for _, x := range p.users {
		if x.Name == name {
			return true
		}
	}
	return false
}

func (p *usersPage) Shown() {
	p.load()
	p.fp.load()
}

func (p *usersPage) load() {
	u, d := p.u, p.u.hooks.Daemon
	if d == nil {
		return
	}
	u.async(func() {
		list, err := d.Users()
		u.mu.Lock()
		defer u.mu.Unlock()
		if err != nil {
			p.loadErr = i18n.Tf("Could not read the users: %s", Describe(err))
			return
		}
		p.users, p.loaded, p.loadErr = list, true, ""
	})
}

func (p *usersPage) clicks(name string) *userClicks {
	k, ok := p.rows[name]
	if !ok {
		k = &userClicks{}
		p.rows[name] = k
	}
	return k
}

func (p *usersPage) snapshot() []User {
	p.u.mu.Lock()
	defer p.u.mu.Unlock()
	return append([]User(nil), p.users...)
}

// change runs one daemon call, then shows its refusal or reloads the list.
func (p *usersPage) change(failed string, call func(Daemon) error) {
	u, d := p.u, p.u.hooks.Daemon
	if d == nil {
		return
	}
	u.async(func() {
		if err := call(d); err != nil {
			u.tell(failed, Describe(err))
		}
		p.load()
	})
}

func (p *usersPage) self(name string) bool { return name == p.u.sys.User }

// ---- the add form ----

func (f *addForm) nameErr() string {
	n := f.name.Editor.Text()
	switch {
	case n == "":
		return ""
	case !userNameRE.MatchString(n):
		return i18n.T(nameRule)
	case f.taken != nil && f.taken(n):
		return i18n.T("This name is already used.")
	}
	return ""
}

func (f *addForm) pwErr() string {
	if e := passwordProblem(f.pw.Editor.Text()); e != "" {
		return e
	}
	if f.pw2.Editor.Text() != "" && f.pw.Editor.Text() != f.pw2.Editor.Text() {
		return i18n.T("The passwords are not the same.")
	}
	return ""
}

func (f *addForm) valid() bool {
	n, pw := f.name.Editor.Text(), f.pw.Editor.Text()
	return userNameRE.MatchString(n) && f.nameErr() == "" && pw != "" && pw == f.pw2.Editor.Text() && passwordProblem(pw) == ""
}

func (f *addForm) clear() {
	for _, x := range []*guikit.Field{f.name, f.full, f.pw, f.pw2} {
		x.Editor.SetText("")
		x.Err = ""
	}
	f.admin, f.focused = false, false
}

func (p *usersPage) openAdd() {
	u := p.u
	if !u.sys.Admin {
		return
	}
	p.add.clear()
	u.askForm(i18n.T("Add a user"), "", p.addFormView, 0,
		choice{Label: i18n.T("Cancel"), Kind: guikit.ButtonSecondary, Do: p.add.clear},
		choice{Label: i18n.T("Add"), Kind: guikit.ButtonPrimary, Enabled: p.add.valid, Do: func() {
			name, full, pw, admin := p.add.name.Editor.Text(), strings.TrimSpace(p.add.full.Editor.Text()), p.add.pw.Editor.Text(), p.add.admin
			p.add.clear()
			p.change(i18n.Tf("Could not add %s", name), func(d Daemon) error { return d.AddUser(name, full, admin, pw) })
		}})
}

func (p *usersPage) addFormView(gtx layout.Context) layout.Dimensions {
	u, f := p.u, &p.add
	if f.adminClick.Clicked(gtx) {
		f.admin = !f.admin
	}
	if !f.focused {
		f.focused = true
		gtx.Execute(focus(&f.name.Editor))
	}
	f.name.Err, f.pw2.Err = f.nameErr(), f.pwErr()
	return fieldsCol(gtx, u,
		fieldRow{title: i18n.T("Name"), f: f.name, hint: i18n.T("The name to log in with.")},
		fieldRow{title: i18n.T("Full name"), f: f.full},
		fieldRow{title: i18n.T("Password"), f: f.pw},
		fieldRow{title: i18n.T("Password again"), f: f.pw2},
		fieldRow{check: func(gtx layout.Context) layout.Dimensions {
			return u.CheckCard(gtx, &f.adminClick, f.admin, i18n.T("Administrator"), i18n.T("Can change the system and the other users."))
		}},
	)
}

type fieldRow struct {
	title string
	f     *guikit.Field
	hint  string
	check layout.Widget
}

func fieldsCol(gtx layout.Context, u *UI, rows ...fieldRow) layout.Dimensions {
	var c col
	for i, r := range rows {
		if i > 0 {
			c.gap(12)
		}
		if r.check != nil {
			c.add(r.check)
			continue
		}
		c.add(func(gtx layout.Context) layout.Dimensions {
			return u.Labelled(gtx, r.title, r.f, r.hint, func(gtx layout.Context) layout.Dimensions {
				return u.Input(gtx, r.f, guikit.StBody, 38, "", nil)
			})
		})
	}
	return c.layout(gtx)
}

// ---- change a password ----

func (f *pwForm) valid() bool {
	n := f.newPw.Editor.Text()
	if n == "" || n != f.again.Editor.Text() || passwordProblem(n) != "" {
		return false
	}
	return !f.own || f.cur.Editor.Text() != ""
}

func (f *pwForm) clear() {
	for _, x := range []*guikit.Field{f.cur, f.newPw, f.again} {
		x.Editor.SetText("")
		x.Err = ""
	}
	f.focused = false
}

func (p *usersPage) openPassword(name string) {
	u := p.u
	own := p.self(name)
	if !own && !u.sys.Admin {
		return
	}
	p.pw.clear()
	p.pw.own = own
	title := i18n.Tf("Change the password of %s", name)
	if own {
		title = i18n.T("Change your password")
	}
	u.askForm(title, "", p.pwFormView, 0,
		choice{Label: i18n.T("Cancel"), Kind: guikit.ButtonSecondary, Do: p.pw.clear},
		choice{Label: i18n.T("Change password"), Kind: guikit.ButtonPrimary, Enabled: p.pw.valid, Do: func() {
			cur, pw := p.pw.cur.Editor.Text(), p.pw.newPw.Editor.Text()
			if !own {
				cur = ""
			}
			p.pw.clear()
			p.change(i18n.T("Could not change the password"), func(d Daemon) error { return d.SetPassword(name, cur, pw) })
		}})
}

func (p *usersPage) pwFormView(gtx layout.Context) layout.Dimensions {
	f := &p.pw
	if !f.focused {
		f.focused = true
		first := f.newPw
		if f.own {
			first = f.cur
		}
		gtx.Execute(focus(&first.Editor))
	}
	f.newPw.Err, f.again.Err = passwordProblem(f.newPw.Editor.Text()), ""
	if f.again.Editor.Text() != "" && f.again.Editor.Text() != f.newPw.Editor.Text() {
		f.again.Err = i18n.T("The passwords are not the same.")
	}
	rows := []fieldRow{}
	if f.own {
		rows = append(rows, fieldRow{title: i18n.T("Current password"), f: f.cur})
	}
	rows = append(rows, fieldRow{title: i18n.T("New password"), f: f.newPw}, fieldRow{title: i18n.T("New password again"), f: f.again})
	return fieldsCol(gtx, p.u, rows...)
}

// ---- remove, and the administrator switch ----

func (p *usersPage) openRemove(name string) {
	u := p.u
	if !u.sys.Admin || p.self(name) {
		return
	}
	p.rm.keep = true
	u.askForm(i18n.Tf("Remove %s?", name), i18n.T("The user can no longer log in."), func(gtx layout.Context) layout.Dimensions {
		if p.rm.keepClick.Clicked(gtx) {
			p.rm.keep = !p.rm.keep
		}
		return u.CheckCard(gtx, &p.rm.keepClick, p.rm.keep, i18n.T("Keep the home folder"), i18n.Tf("Leave /home/%s and its files on the disk.", name))
	}, 0,
		choice{Label: i18n.T("Cancel"), Kind: guikit.ButtonSecondary},
		choice{Label: i18n.Tf("Remove %s", name), Kind: guikit.ButtonDanger, Do: func() {
			keep := p.rm.keep
			p.change(i18n.Tf("Could not remove %s", name), func(d Daemon) error { return d.RemoveUser(name, keep) })
		}})
}

func (p *usersPage) setAdmin(name string, on bool) {
	u := p.u
	if !u.sys.Admin {
		return
	}
	do := func() {
		p.change(i18n.T("Could not change the administrators"), func(d Daemon) error { return d.SetAdmin(name, on) })
	}
	if on {
		do()
		return
	}
	body := i18n.T("They can no longer change the system or the other users.")
	if p.self(name) {
		body = i18n.T("You will no longer be able to change this system.")
	}
	u.askForm(i18n.Tf("Remove %s from the administrators?", name), body, nil, 0,
		choice{Label: i18n.T("Cancel"), Kind: guikit.ButtonSecondary},
		choice{Label: i18n.T("Remove from administrators"), Kind: guikit.ButtonDanger, Do: do})
}

// ---- the page ----

// actionsOf are the buttons of a row. A non-admin gets only their own
// password.
func (p *usersPage) actionsOf(x User) []guikit.Action {
	k := p.clicks(x.Name)
	own := p.self(x.Name)
	admin := p.u.sys.Admin
	var as []guikit.Action
	switch {
	case own:
		as = append(as, guikit.Action{Click: &k.pw, Kind: guikit.ButtonSecondary, Label: i18n.T("Change your password")})
	case admin:
		as = append(as, guikit.Action{Click: &k.pw, Kind: guikit.ButtonSecondary, Label: i18n.T("Change password")})
	}
	if admin {
		label := i18n.T("Make administrator")
		if x.Admin {
			label = i18n.T("Remove administrator")
		}
		as = append(as,
			guikit.Action{Click: &k.admin, Kind: guikit.ButtonSecondary, Label: label},
			guikit.Action{Click: &k.rm, Kind: guikit.ButtonGhost, Label: i18n.T("Remove…"), Disabled: own})
	}
	return as
}

// rowTexts are the rows as "title|sub|You", for the tests.
func (p *usersPage) rowTexts() []string {
	var out []string
	for _, x := range p.snapshot() {
		t, s := p.rowText(x)
		you := ""
		if p.self(x.Name) {
			you = "You"
		}
		out = append(out, t+"|"+s+"|"+you)
	}
	return out
}

func (p *usersPage) rowText(x User) (title, sub string) {
	title, sub = x.FullName, x.Name
	if title == "" {
		title = x.Name
	}
	if x.Admin {
		sub += " · " + i18n.T("Administrator")
	}
	return
}

func (p *usersPage) Update(gtx layout.Context) {
	p.fp.update(gtx)
	if p.addBtn.Clicked(gtx) {
		p.openAdd()
	}
	for _, x := range p.snapshot() {
		k, name, admin := p.clicks(x.Name), x.Name, x.Admin
		if k.pw.Clicked(gtx) {
			p.openPassword(name)
		}
		if k.admin.Clicked(gtx) {
			p.setAdmin(name, !admin)
		}
		if k.rm.Clicked(gtx) {
			p.openRemove(name)
		}
	}
}

func (p *usersPage) Footer() (leading, trailing []guikit.Action) {
	return nil, []guikit.Action{{Click: &p.addBtn, Kind: guikit.ButtonPrimary, Label: i18n.T("Add user…"), Disabled: !p.u.sys.Admin || p.u.hooks.Daemon == nil}}
}

func (p *usersPage) Body(gtx layout.Context) layout.Dimensions {
	u := p.u
	u.mu.Lock()
	loaded, loadErr := p.loaded, p.loadErr
	u.mu.Unlock()
	users := p.snapshot()
	return u.scrolled(gtx, &p.list, func(gtx layout.Context) layout.Dimensions {
		var c col
		c.add(u.Heading(i18n.T("Users"), i18n.T("Add and remove the people who use this computer, change their passwords and who is an administrator, and add your fingerprints.")))
		c.gap(20)
		if u.sys.Release.Flavor == "cosmic" {
			c.add(u.notice(i18n.T("On Isotope, change users here. A change made in COSMIC Settings may not survive a restart.")))
			c.gap(14)
		}
		if !u.sys.Admin {
			c.add(u.notice(i18n.T(onlyAdmin)))
			c.gap(14)
		}
		switch {
		case u.hooks.Daemon == nil:
			c.add(u.errorBox(i18n.T("The update service (isotope-updated) is not available.")))
		case loadErr != "":
			c.add(u.errorBox(loadErr))
		case !loaded:
			c.add(u.spinnerLine(i18n.T("Reading the users…")))
		default:
			for i, x := range users {
				if i > 0 {
					c.gap(10)
				}
				c.add(p.userRow(x))
			}
		}
		// The user's own fingerprints, which need fprintd, not the daemon.
		c.gap(26)
		c.add(p.fp.body())
		return c.layout(gtx)
	})
}

func (p *usersPage) userRow(x User) layout.Widget {
	u := p.u
	title, sub := p.rowText(x)
	as := p.actionsOf(x)
	own := p.self(x.Name)
	return u.panel(func(gtx layout.Context) layout.Dimensions {
		var c col
		c.add(u.row(title, sub, func(gtx layout.Context) layout.Dimensions {
			if !own {
				return layout.Dimensions{}
			}
			return u.Label(gtx, guikit.StSmall, u.Colors().Text2, i18n.T("You"))
		}))
		if len(as) > 0 {
			c.gap(10)
			c.add(func(gtx layout.Context) layout.Dimensions { return wrapButtons(gtx, u, as) })
		}
		return c.layout(gtx)
	})
}

// wrapButtons lays the buttons in a row, or one per line when they do not
// fit the width.
func wrapButtons(gtx layout.Context, u *UI, as []guikit.Action) layout.Dimensions {
	if gtx.Constraints.Max.X < 520 && len(as) > 1 {
		var c col
		for i, a := range as {
			if i > 0 {
				c.gap(8)
			}
			c.add(u.buttons(a))
		}
		return c.layout(gtx)
	}
	return u.buttons(as...)(gtx)
}

package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"
	"gopkg.in/yaml.v3"
)

// Users. On Kairos /etc is rebuilt at every boot, so a user that only
// useradd knows is gone after a reboot. Each change is written first to
// usersOEM, a yip file that the boot applies again, and then applied live.
// If the live apply fails, the file gets its previous bytes back.
//
// yip (v1.27.1) creates a user of a users: map that does not exist yet with
// its passwd, gecos, shell and groups. A user that exists already (the
// installer's user, from /oem/90_custom.yaml, which runs before this file)
// gets only its passwd. So the installer's user is changed with an override:
// a passwd-only users: entry and boot commands.

// Variables so tests can point them elsewhere.
var (
	passwdFile           = "/etc/passwd"
	groupFile            = "/etc/group"
	shadowFile           = "/etc/shadow"
	usersOEM             = "/oem/95_isotope_users.yaml"
	usersRun   cmdRunner = execCmd // useradd, usermod, userdel, gpasswd, chpasswd, openssl
	loggedIn             = logindHasUser
)

// usersMu serialises every change to the users.
var usersMu sync.Mutex

const (
	adminGroup = "admin"
	userShell  = "/bin/bash"
	minUserUID = 1000
	maxUserUID = 59999
)

// userGroups are the supplemental groups of a new user: the installer's
// desktopGroups without admin. Keep in step with pkg/installer/config.go:48.
var userGroups = []string{"audio", "video", "render", "input", "bluetooth", "seat", "docker"}

// userNameRule is the installer's rule (pkg/installer/wizard.go:45).
var userNameRule = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

var (
	errLastAdmin     = errors.New("this is the last administrator: make another user an administrator first")
	errSystemAccount = errors.New("this is a system account: Isotope does not change it")
	errNoSuchUser    = errors.New("there is no such user")
	errUserExists    = errors.New("a user or a group with this name exists already")
	errSelfRemove    = errors.New("you cannot remove the account that you use")
	errLoggedIn      = errors.New("this user is logged in: the user must log out first")
	errBadUserName   = errors.New("a user name must be 1-32 characters: lowercase letters, digits, - or _, starting with a letter or _")
	errBadFullName   = errors.New("a full name must be at most 64 characters, without ':', ',' or a line break")
	errEmptyPassword = errors.New("the password is empty")
	errBadPassword   = errors.New("a password must be at most 256 bytes, without a line break")
	errWrongPassword = errors.New("the current password is not correct")
	errUnverifiable  = errors.New("the current password is stored in a form that Isotope cannot check: an administrator must set a new password")
)

// userInfo is one user that a person can log in as.
type userInfo struct {
	Name, FullName, Home string
	UID                  uint32
	Admin                bool
}

// passwdEntry is one line of /etc/passwd.
type passwdEntry struct {
	name, gecos, home string
	uid               uint32
}

func isUserUID(uid uint32) bool { return uid >= minUserUID && uid <= maxUserUID }

func readPasswd() ([]passwdEntry, error) {
	b, err := os.ReadFile(passwdFile)
	if err != nil {
		return nil, err
	}
	var out []passwdEntry
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Split(l, ":")
		if len(f) != 7 || strings.HasPrefix(l, "#") {
			continue
		}
		uid, err := strconv.ParseUint(f[2], 10, 32)
		if err != nil {
			continue
		}
		out = append(out, passwdEntry{name: f[0], gecos: f[4], home: f[5], uid: uint32(uid)})
	}
	return out, nil
}

// readGroups returns the members of each group in groupFile.
func readGroups() (map[string][]string, error) {
	b, err := os.ReadFile(groupFile)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Split(l, ":")
		if len(f) != 4 || strings.HasPrefix(l, "#") {
			continue
		}
		var members []string
		for _, m := range strings.Split(f[3], ",") {
			if m != "" {
				members = append(members, m)
			}
		}
		out[f[0]] = members
	}
	return out, nil
}

// listUsers returns the users with a uid from 1000 to 59999, in the order
// of passwdFile.
func listUsers() ([]userInfo, error) {
	pw, err := readPasswd()
	if err != nil {
		return nil, err
	}
	groups, err := readGroups()
	if err != nil {
		return nil, err
	}
	var out []userInfo
	for _, e := range pw {
		if !isUserUID(e.uid) {
			continue
		}
		out = append(out, userInfo{
			Name:     e.name,
			FullName: strings.SplitN(e.gecos, ",", 2)[0],
			Home:     e.home,
			UID:      e.uid,
			Admin:    contains(groups[adminGroup], e.name),
		})
	}
	return out, nil
}

// userSnapshot is what a guard needs to know about the system.
type userSnapshot struct {
	passwd []passwdEntry
	groups map[string][]string
}

func loadSnapshot() (userSnapshot, error) {
	pw, err := readPasswd()
	if err != nil {
		return userSnapshot{}, err
	}
	g, err := readGroups()
	if err != nil {
		return userSnapshot{}, err
	}
	return userSnapshot{passwd: pw, groups: g}, nil
}

func (s userSnapshot) find(name string) (passwdEntry, bool) {
	for _, e := range s.passwd {
		if e.name == name {
			return e, true
		}
	}
	return passwdEntry{}, false
}

// target finds a user that Isotope may change.
func (s userSnapshot) target(name string) (passwdEntry, error) {
	e, ok := s.find(name)
	if !ok {
		return e, errNoSuchUser
	}
	if !isUserUID(e.uid) {
		return e, errSystemAccount
	}
	return e, nil
}

func (s userSnapshot) isAdmin(name string) bool { return contains(s.groups[adminGroup], name) }

// adminsWithout counts the admins with a user uid, name not counted.
func (s userSnapshot) adminsWithout(name string) int {
	n := 0
	for _, m := range s.groups[adminGroup] {
		if m == name {
			continue
		}
		if e, ok := s.find(m); ok && isUserUID(e.uid) {
			n++
		}
	}
	return n
}

// checkFullName refuses what would break /etc/passwd or the gecos field.
func checkFullName(s string) error {
	if utf8.RuneCountInString(s) > 64 || !utf8.ValidString(s) || strings.ContainsAny(s, ":,") {
		return errBadFullName
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return errBadFullName
		}
	}
	return nil
}

// maxPasswordLen is what `openssl passwd -stdin` reads of a line. It drops
// the rest without a word, while PAM hashes the whole password.
const maxPasswordLen = 256

// checkPasswordInput refuses a password that openssl would read cut short.
func checkPasswordInput(plain string) error {
	if len(plain) > maxPasswordLen || strings.ContainsAny(plain, "\r\n") {
		return errBadPassword
	}
	return nil
}

// hashPassword returns the SHA-512 crypt hash of plain. The password goes
// to openssl on stdin, never in argv.
func hashPassword(plain string) (string, error) {
	if plain == "" {
		return "", errEmptyPassword
	}
	if err := checkPasswordInput(plain); err != nil {
		return "", err
	}
	out, err := usersRun("openssl", []string{"passwd", "-6", "-stdin"}, []byte(plain+"\n"))
	if err != nil {
		return "", err
	}
	h := strings.TrimSpace(string(out))
	if !strings.HasPrefix(h, "$6$") || strings.ContainsAny(h, ": \n") {
		return "", errors.New("openssl did not return a SHA-512 crypt hash")
	}
	return h, nil
}

var cryptSalt = regexp.MustCompile(`^[./0-9A-Za-z]{1,16}$`)

// verifyPassword checks plain against the shadow hash of name. Only a
// plain SHA-512 crypt hash ($6$salt$hash) can be checked; any other form
// gives errUnverifiable.
func verifyPassword(name, plain string) error {
	b, err := os.ReadFile(shadowFile)
	if err != nil {
		return err
	}
	var hash string
	found := false
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Split(l, ":")
		if len(f) >= 2 && f[0] == name {
			hash, found = f[1], true
			break
		}
	}
	if !found {
		return errNoSuchUser
	}
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != "" || parts[1] != "6" || !cryptSalt.MatchString(parts[2]) {
		return errUnverifiable
	}
	if err := checkPasswordInput(plain); err != nil {
		return err
	}
	out, err := usersRun("openssl", []string{"passwd", "-6", "-salt", parts[2], "-stdin"}, []byte(plain+"\n"))
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare(bytes.TrimSpace(out), []byte(hash)) != 1 {
		return errWrongPassword
	}
	return nil
}

// usersState is the daemon's own state, the isotope_users key of usersOEM.
type usersState struct {
	Own       map[string]ownUser      `yaml:"own"`
	Overrides map[string]overrideUser `yaml:"overrides"`
}

// ownUser is a user that Isotope created.
type ownUser struct {
	Gecos  string `yaml:"gecos"`
	Passwd string `yaml:"passwd"`
	Admin  bool   `yaml:"admin"`
}

// overrideUser is a change to a user that Isotope did not create. A nil or
// empty field is not changed.
type overrideUser struct {
	Gecos   *string `yaml:"gecos,omitempty"`
	Passwd  string  `yaml:"passwd,omitempty"`
	Admin   *bool   `yaml:"admin,omitempty"`
	Removed bool    `yaml:"removed,omitempty"`
}

// oemFile is usersOEM as yip reads it.
type oemFile struct {
	Name         string     `yaml:"name"`
	IsotopeUsers usersState `yaml:"isotope_users"`
	Stages       oemStages  `yaml:"stages"`
}

type oemStages struct {
	Boot []oemStage `yaml:"boot,omitempty"`
}

type oemStage struct {
	Name     string             `yaml:"name"`
	Users    map[string]yipUser `yaml:"users,omitempty"`
	Commands []string           `yaml:"commands,omitempty"`
}

type yipUser struct {
	Passwd string   `yaml:"passwd"`
	Gecos  string   `yaml:"gecos,omitempty"`
	Shell  string   `yaml:"shell,omitempty"`
	Groups []string `yaml:"groups,omitempty"`
}

const usersOEMHeader = "# Written by isotope-updated (Isotope System, isotope users). Do not edit.\n"

// loadUsersState reads the isotope_users key of usersOEM. No file is an
// empty state.
func loadUsersState() (usersState, error) {
	b, err := os.ReadFile(usersOEM)
	if errors.Is(err, os.ErrNotExist) {
		return usersState{}, nil
	}
	if err != nil {
		return usersState{}, err
	}
	var f struct {
		IsotopeUsers usersState `yaml:"isotope_users"`
	}
	if err := yaml.Unmarshal(b, &f); err != nil {
		return usersState{}, fmt.Errorf("%s: %w", usersOEM, err)
	}
	return f.IsotopeUsers, nil
}

// shellQuote quotes s for sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellName gives a user name for a boot command, quoted when needed.
func shellName(s string) string {
	if userNameRule.MatchString(s) {
		return s
	}
	return shellQuote(s)
}

// newUserGroups is userGroups, plus admin for an admin, without the
// groups that this system does not have.
func newUserGroups(have map[string][]string, admin bool) []string {
	want := userGroups
	if admin {
		want = append(append([]string(nil), userGroups...), adminGroup)
	}
	var out []string
	for _, g := range want {
		if _, ok := have[g]; ok {
			out = append(out, g)
		}
	}
	return out
}

// renderUsersOEM writes the state and the yip stages made from it.
func renderUsersOEM(st usersState, have map[string][]string) ([]byte, error) {
	users := map[string]yipUser{}
	for name, u := range st.Own {
		users[name] = yipUser{Passwd: u.Passwd, Gecos: u.Gecos, Shell: userShell, Groups: newUserGroups(have, u.Admin)}
	}
	var cmds []string
	for _, name := range slices.Sorted(maps.Keys(st.Overrides)) {
		o := st.Overrides[name]
		if o.Removed {
			cmds = append(cmds, "userdel "+shellName(name))
			continue
		}
		if o.Passwd != "" {
			users[name] = yipUser{Passwd: o.Passwd}
		}
		if o.Gecos != nil {
			cmds = append(cmds, "usermod -c "+shellQuote(*o.Gecos)+" "+shellName(name))
		}
		if o.Admin != nil {
			op := "-d"
			if *o.Admin {
				op = "-a"
			}
			cmds = append(cmds, "gpasswd "+op+" "+shellName(name)+" "+adminGroup)
		}
	}
	f := oemFile{Name: "Isotope users", IsotopeUsers: st}
	if len(users) > 0 {
		f.Stages.Boot = append(f.Stages.Boot, oemStage{Name: "Isotope users", Users: users})
	}
	if len(cmds) > 0 {
		f.Stages.Boot = append(f.Stages.Boot, oemStage{Name: "Isotope user changes", Commands: cmds})
	}
	var buf bytes.Buffer
	buf.WriteString(usersOEMHeader)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeUsersState writes usersOEM (0600) through a temporary file.
func writeUsersState(st usersState) error {
	groups, err := readGroups()
	if err != nil {
		return err
	}
	b, err := renderUsersOEM(st, groups)
	if err != nil {
		return err
	}
	return writeFileAtomic(usersOEM, b)
}

func writeFileAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// changeUsers writes the new state to usersOEM, then runs apply. If apply
// fails, usersOEM gets its previous bytes back (or is removed, when there
// was none). The caller holds usersMu.
func changeUsers(st usersState, apply func() error) error {
	prev, err := os.ReadFile(usersOEM)
	existed := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := writeUsersState(st); err != nil {
		return err
	}
	if err := apply(); err != nil {
		var rerr error
		if existed {
			rerr = writeFileAtomic(usersOEM, prev)
		} else {
			rerr = os.Remove(usersOEM)
		}
		if rerr != nil {
			return fmt.Errorf("%w (and %s was not restored: %v)", err, usersOEM, rerr)
		}
		return err
	}
	return nil
}

// override returns the override of name, made empty when there is none.
func (st *usersState) override(name string) overrideUser {
	if st.Overrides == nil {
		st.Overrides = map[string]overrideUser{}
	}
	return st.Overrides[name]
}

func chpasswd(name, hash string) error {
	_, err := usersRun("chpasswd", []string{"-e"}, []byte(name+":"+hash+"\n"))
	return err
}

// addUser creates name, with a home folder, and records it in usersOEM.
func addUser(name, fullName string, admin bool, password string) error {
	if !userNameRule.MatchString(name) {
		return errBadUserName
	}
	if err := checkFullName(fullName); err != nil {
		return err
	}
	if password == "" {
		return errEmptyPassword
	}
	usersMu.Lock()
	defer usersMu.Unlock()
	if restoring {
		return errRestoring
	}

	snap, err := loadSnapshot()
	if err != nil {
		return err
	}
	st, err := loadUsersState()
	if err != nil {
		return err
	}
	_, inPasswd := snap.find(name)
	_, isGroup := snap.groups[name]
	_, isOwn := st.Own[name]
	_, isOverride := st.Overrides[name]
	if inPasswd || isGroup || isOwn || isOverride {
		return errUserExists
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	if st.Own == nil {
		st.Own = map[string]ownUser{}
	}
	st.Own[name] = ownUser{Gecos: fullName, Passwd: hash, Admin: admin}

	return changeUsers(st, func() error {
		args := []string{"-m", "-c", fullName, "-s", userShell}
		if gs := newUserGroups(snap.groups, admin); len(gs) > 0 {
			args = append(args, "-G", strings.Join(gs, ","))
		}
		if _, err := usersRun("useradd", append(args, name), nil); err != nil {
			return err
		}
		if err := chpasswd(name, hash); err != nil {
			// A user without its password would be lost at reboot anyway.
			if _, derr := usersRun("userdel", []string{"-r", name}, nil); derr != nil {
				return fmt.Errorf("%w (and the new user was not removed: %v)", err, derr)
			}
			return err
		}
		return nil
	})
}

// removeUser deletes name, and its home folder unless keepHome. caller is
// the user who asks.
func removeUser(name string, keepHome bool, caller string) error {
	usersMu.Lock()
	defer usersMu.Unlock()
	if restoring {
		return errRestoring
	}

	snap, err := loadSnapshot()
	if err != nil {
		return err
	}
	if _, err := snap.target(name); err != nil {
		return err
	}
	if name == caller {
		return errSelfRemove
	}
	if snap.isAdmin(name) && snap.adminsWithout(name) == 0 {
		return errLastAdmin
	}
	in, err := loggedIn(name)
	if err != nil {
		return fmt.Errorf("cannot tell whether %s is logged in: %w", name, err)
	}
	if in {
		return errLoggedIn
	}
	st, err := loadUsersState()
	if err != nil {
		return err
	}
	if _, ok := st.Own[name]; ok {
		delete(st.Own, name)
	} else {
		if st.Overrides == nil {
			st.Overrides = map[string]overrideUser{}
		}
		st.Overrides[name] = overrideUser{Removed: true}
	}
	return changeUsers(st, func() error {
		args := []string{name}
		if !keepHome {
			args = []string{"-r", name}
		}
		_, err := usersRun("userdel", args, nil)
		return err
	})
}

// setPassword sets the password of name.
func setPassword(name, password string) error {
	if password == "" {
		return errEmptyPassword
	}
	usersMu.Lock()
	defer usersMu.Unlock()
	if restoring {
		return errRestoring
	}

	snap, err := loadSnapshot()
	if err != nil {
		return err
	}
	if _, err := snap.target(name); err != nil {
		return err
	}
	st, err := loadUsersState()
	if err != nil {
		return err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	if u, ok := st.Own[name]; ok {
		u.Passwd = hash
		st.Own[name] = u
	} else {
		o := st.override(name)
		o.Passwd = hash
		st.Overrides[name] = o
	}
	return changeUsers(st, func() error { return chpasswd(name, hash) })
}

// setAdmin adds name to the admin group, or removes it.
func setAdmin(name string, on bool) error {
	usersMu.Lock()
	defer usersMu.Unlock()
	if restoring {
		return errRestoring
	}

	snap, err := loadSnapshot()
	if err != nil {
		return err
	}
	if _, err := snap.target(name); err != nil {
		return err
	}
	if _, ok := snap.groups[adminGroup]; !ok {
		return errors.New("this system has no admin group")
	}
	is := snap.isAdmin(name)
	if !on && is && snap.adminsWithout(name) == 0 {
		return errLastAdmin
	}
	st, err := loadUsersState()
	if err != nil {
		return err
	}
	if u, ok := st.Own[name]; ok {
		u.Admin = on
		st.Own[name] = u
	} else {
		o := st.override(name)
		o.Admin = &on
		st.Overrides[name] = o
	}
	return changeUsers(st, func() error {
		if is == on {
			return nil
		}
		op := "-d"
		if on {
			op = "-a"
		}
		_, err := usersRun("gpasswd", []string{op, name, adminGroup}, nil)
		return err
	})
}

// logindHasUser asks logind whether name has a session, or lingers.
func logindHasUser(name string) (bool, error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), polkitTimeout)
	defer cancel()
	var users []struct {
		UID  uint32
		Name string
		Path dbus.ObjectPath
	}
	err = conn.Object("org.freedesktop.login1", "/org/freedesktop/login1").
		CallWithContext(ctx, "org.freedesktop.login1.Manager.ListUsers", 0).Store(&users)
	if err != nil {
		return false, fmt.Errorf("logind: %w", err)
	}
	for _, u := range users {
		if u.Name == name {
			return true, nil
		}
	}
	return false, nil
}

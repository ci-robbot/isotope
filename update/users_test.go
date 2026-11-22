package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// usersCall is one command the users model ran.
type usersCall struct {
	name  string
	args  []string
	stdin string
}

func (c usersCall) String() string { return c.name + " " + strings.Join(c.args, " ") }

// fakeUsers is a small system: useradd, userdel and gpasswd edit the
// passwd and group files, openssl gives a fake but deterministic hash.
type fakeUsers struct {
	t      *testing.T
	calls  []usersCall
	failOn string // a command name that fails
}

func (f *fakeUsers) run(name string, args []string, stdin []byte) ([]byte, error) {
	f.calls = append(f.calls, usersCall{name, append([]string(nil), args...), string(stdin)})
	if name == f.failOn {
		return nil, fmt.Errorf("%s failed", name)
	}
	switch name {
	case "openssl":
		salt := "fakesalt"
		for i, a := range args {
			if a == "-salt" && i+1 < len(args) {
				salt = args[i+1]
			}
		}
		return []byte(fakeHash(salt, strings.TrimSuffix(string(stdin), "\n")) + "\n"), nil
	case "useradd":
		u := args[len(args)-1]
		var gecos, groups string
		for i, a := range args {
			switch a {
			case "-c":
				gecos = args[i+1]
			case "-G":
				groups = args[i+1]
			}
		}
		uid := 1000
		for _, l := range readLines(f.t, passwdFile) {
			fs := strings.Split(l, ":")
			if n, _ := strconv.Atoi(fs[2]); n >= uid && n < 60000 {
				uid = n + 1
			}
		}
		appendLine(f.t, passwdFile, fmt.Sprintf("%s:x:%d:%d:%s:/home/%s:/bin/bash", u, uid, uid, gecos, u))
		appendLine(f.t, groupFile, fmt.Sprintf("%s:x:%d:", u, uid))
		for _, g := range strings.Split(groups, ",") {
			if g != "" {
				editMembers(f.t, g, u, true)
			}
		}
	case "userdel":
		u := args[len(args)-1]
		var keep []string
		for _, l := range readLines(f.t, passwdFile) {
			if !strings.HasPrefix(l, u+":") {
				keep = append(keep, l)
			}
		}
		writeLines(f.t, passwdFile, keep)
		for _, l := range readLines(f.t, groupFile) {
			editMembers(f.t, strings.SplitN(l, ":", 2)[0], u, false)
		}
	case "gpasswd":
		editMembers(f.t, args[2], args[1], args[0] == "-a")
	}
	return nil, nil
}

func fakeHash(salt, plain string) string {
	sum := sha256.Sum256([]byte(salt + "\x00" + plain))
	return "$6$" + salt + "$" + hex.EncodeToString(sum[:])
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func writeLines(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	writeLines(t, path, append(readLines(t, path), line))
}

func editMembers(t *testing.T, group, user string, add bool) {
	t.Helper()
	lines := readLines(t, groupFile)
	for i, l := range lines {
		fs := strings.Split(l, ":")
		if fs[0] != group {
			continue
		}
		var members []string
		for _, m := range strings.Split(fs[3], ",") {
			if m != "" && m != user {
				members = append(members, m)
			}
		}
		if add {
			members = append(members, user)
		}
		fs[3] = strings.Join(members, ",")
		lines[i] = strings.Join(fs, ":")
	}
	writeLines(t, groupFile, lines)
}

// usersFixture is an installed system: anna is the installer's user and
// the only admin. The group file has no bluetooth and no docker.
func usersFixture(t *testing.T) *fakeUsers {
	t.Helper()
	dir := t.TempDir()
	oldP, oldG, oldS, oldO, oldR, oldL := passwdFile, groupFile, shadowFile, usersOEM, usersRun, loggedIn
	t.Cleanup(func() {
		passwdFile, groupFile, shadowFile, usersOEM, usersRun, loggedIn = oldP, oldG, oldS, oldO, oldR, oldL
	})
	passwdFile = filepath.Join(dir, "passwd")
	groupFile = filepath.Join(dir, "group")
	shadowFile = filepath.Join(dir, "shadow")
	usersOEM = filepath.Join(dir, "oem", "95_isotope_users.yaml")
	if err := os.MkdirAll(filepath.Dir(usersOEM), 0o755); err != nil {
		t.Fatal(err)
	}
	writeLines(t, passwdFile, []string{
		"root:x:0:0:root:/root:/bin/bash",
		"daemon:x:2:2:daemon:/sbin:/sbin/nologin",
		"svc:x:999:999:Service:/:/sbin/nologin",
		"anna:x:1000:1003:Anna,,,:/home/anna:/bin/bash",
		"nobody:x:65534:65534:nobody:/:/sbin/nologin",
	})
	writeLines(t, groupFile, []string{
		"root:x:0:",
		"admin:x:900:anna",
		"audio:x:995:anna",
		"video:x:983:anna",
		"render:x:986:anna",
		"input:x:990:anna",
		"seat:x:1001:anna",
		"anna:x:1003:",
	})
	writeLines(t, shadowFile, []string{
		"root:!:20000::::::",
		"anna:" + fakeHash("annasalt", "annapass") + ":20000:0:99999:7:::",
	})
	f := &fakeUsers{t: t}
	usersRun = f.run
	loggedIn = func(string) (bool, error) { return false, nil }
	return f
}

// oemState reads the daemon's own key back from the OEM file.
func oemState(t *testing.T) usersState {
	t.Helper()
	st, err := loadUsersState()
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// oemDoc parses the whole OEM file, as yip sees it.
type oemDoc struct {
	Name   string `yaml:"name"`
	Stages struct {
		Boot []struct {
			Name     string `yaml:"name"`
			Users    map[string]map[string]any
			Commands []string
		} `yaml:"boot"`
	} `yaml:"stages"`
}

func readOEM(t *testing.T) (oemDoc, string) {
	t.Helper()
	b, err := os.ReadFile(usersOEM)
	if err != nil {
		t.Fatal(err)
	}
	var d oemDoc
	if err := yaml.Unmarshal(b, &d); err != nil {
		t.Fatalf("OEM file does not parse: %v\n%s", err, b)
	}
	return d, string(b)
}

func (d oemDoc) users() map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, s := range d.Stages.Boot {
		for k, v := range s.Users {
			out[k] = v
		}
	}
	return out
}

func (d oemDoc) commands() []string {
	var out []string
	for _, s := range d.Stages.Boot {
		out = append(out, s.Commands...)
	}
	return out
}

func (f *fakeUsers) names() []string {
	var out []string
	for _, c := range f.calls {
		out = append(out, c.String())
	}
	return out
}

func (f *fakeUsers) mutations() []string {
	var out []string
	for _, c := range f.calls {
		if c.name != "openssl" {
			out = append(out, c.String())
		}
	}
	return out
}

func TestListUsersSkipsSystemAccountsAndMarksAdmins(t *testing.T) {
	usersFixture(t)
	writeLines(t, passwdFile, []string{
		"root:x:0:0:root:/root:/bin/bash",
		"daemon:x:2:2:daemon:/sbin:/sbin/nologin",
		"anna:x:1000:1003:Anna Bell,Room 1,,:/home/anna:/bin/bash",
		"bob:x:1001:1004:Bob:/home/bob:/bin/bash",
		"nobody:x:65534:65534:nobody:/:/sbin/nologin",
	})
	writeLines(t, groupFile, []string{"admin:x:900:anna", "bob:x:1004:"})
	got, err := listUsers()
	if err != nil {
		t.Fatal(err)
	}
	want := []userInfo{
		{Name: "anna", FullName: "Anna Bell", Home: "/home/anna", UID: 1000, Admin: true},
		{Name: "bob", FullName: "Bob", Home: "/home/bob", UID: 1001, Admin: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("listUsers = %+v, want %+v", got, want)
	}
}

func TestAddUserWritesTheOEMFileThenRunsUseradd(t *testing.T) {
	f := usersFixture(t)
	if err := addUser("bob", "Bob", true, "s3cret pass"); err != nil {
		t.Fatal(err)
	}
	hash := fakeHash("fakesalt", "s3cret pass")

	st := oemState(t)
	if got, want := st.Own["bob"], (ownUser{Gecos: "Bob", Passwd: hash, Admin: true}); got != want {
		t.Fatalf("own.bob = %+v, want %+v", got, want)
	}
	d, raw := readOEM(t)
	if d.Name != "Isotope users" {
		t.Errorf("name = %q", d.Name)
	}
	if !strings.HasPrefix(raw, "# Written by isotope-updated") {
		t.Errorf("no header comment:\n%s", raw)
	}
	bob := d.users()["bob"]
	if bob["passwd"] != hash || bob["gecos"] != "Bob" || bob["shell"] != "/bin/bash" {
		t.Errorf("stage entry for bob = %v", bob)
	}
	var groups []string
	for _, g := range bob["groups"].([]any) {
		groups = append(groups, g.(string))
	}
	if want := []string{"audio", "video", "render", "input", "seat", "admin"}; !reflect.DeepEqual(groups, want) {
		t.Errorf("groups = %v, want %v (bluetooth and docker are not in the group file)", groups, want)
	}

	fi, err := os.Stat(usersOEM)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("OEM file mode = %v, want 0600", fi.Mode().Perm())
	}

	muts := f.mutations()
	want := []string{
		"useradd -m -c Bob -s /bin/bash -G audio,video,render,input,seat,admin bob",
		"chpasswd -e",
	}
	if !reflect.DeepEqual(muts, want) {
		t.Fatalf("commands = %q, want %q", muts, want)
	}
	for _, c := range f.calls {
		if c.name == "chpasswd" && c.stdin != "bob:"+hash+"\n" {
			t.Errorf("chpasswd stdin = %q", c.stdin)
		}
		for _, a := range c.args {
			if strings.Contains(a, "s3cret") || strings.Contains(a, hash) {
				t.Errorf("argv of %s holds a secret: %q", c.name, a)
			}
		}
	}
}

func TestAddUserRefusesBadNamesAndExisting(t *testing.T) {
	f := usersFixture(t)
	cases := []struct {
		name, full, pass string
		want             error
	}{
		{"Bob", "Bob", "p", errBadUserName},
		{"1x", "x", "p", errBadUserName},
		{"a b", "x", "p", errBadUserName},
		{"", "x", "p", errBadUserName},
		{strings.Repeat("a", 33), "x", "p", errBadUserName},
		{"anna", "Anna", "p", errUserExists},
		{"svc", "S", "p", errUserExists},
		{"audio", "Audio", "p", errUserExists}, // a group with that name
		{"bob", "Bob:x", "p", errBadFullName},
		{"bob", "Bob\nx", "p", errBadFullName},
		{"bob", strings.Repeat("b", 65), "p", errBadFullName},
		{"bob", "Bob", "", errEmptyPassword},
	}
	for _, c := range cases {
		if err := addUser(c.name, c.full, false, c.pass); !errors.Is(err, c.want) {
			t.Errorf("addUser(%q, %q) = %v, want %v", c.name, c.full, err, c.want)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("commands ran: %q", f.names())
	}
	if _, err := os.Stat(usersOEM); !os.IsNotExist(err) {
		t.Errorf("OEM file written: %v", err)
	}
}

func TestFailedLiveApplyRestoresTheOEMFile(t *testing.T) {
	f := usersFixture(t)
	if err := addUser("bob", "Bob", false, "pw"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(usersOEM)
	if err != nil {
		t.Fatal(err)
	}
	f.failOn = "useradd"
	if err := addUser("carl", "Carl", false, "pw"); err == nil {
		t.Fatal("addUser succeeded with a failing useradd")
	}
	after, err := os.ReadFile(usersOEM)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("OEM file not restored:\n%s\nwant:\n%s", after, before)
	}

	// With no file before, a failure leaves no file.
	if err := os.Remove(usersOEM); err != nil {
		t.Fatal(err)
	}
	if err := addUser("dave", "Dave", false, "pw"); err == nil {
		t.Fatal("addUser succeeded with a failing useradd")
	}
	if _, err := os.Stat(usersOEM); !os.IsNotExist(err) {
		t.Fatalf("OEM file left behind: %v", err)
	}
}

func TestFailedChpasswdRemovesTheNewUser(t *testing.T) {
	f := usersFixture(t)
	f.failOn = "chpasswd"
	if err := addUser("bob", "Bob", false, "pw"); err == nil {
		t.Fatal("addUser succeeded with a failing chpasswd")
	}
	if got := f.mutations(); got[len(got)-1] != "userdel -r bob" {
		t.Errorf("commands = %q, want a last userdel -r bob", got)
	}
	if _, err := os.Stat(usersOEM); !os.IsNotExist(err) {
		t.Errorf("OEM file left behind: %v", err)
	}
}

func TestRemoveUserGuards(t *testing.T) {
	f := usersFixture(t)
	cases := []struct {
		name string
		want error
	}{
		{"root", errSystemAccount},
		{"svc", errSystemAccount},
		{"nobody", errSystemAccount},
		{"zed", errNoSuchUser},
		{"anna", errLastAdmin},
	}
	for _, c := range cases {
		if err := removeUser(c.name, true, "other"); !errors.Is(err, c.want) {
			t.Errorf("removeUser(%q) = %v, want %v", c.name, err, c.want)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("commands ran: %q", f.names())
	}
	if _, err := os.Stat(usersOEM); !os.IsNotExist(err) {
		t.Errorf("OEM file written: %v", err)
	}
}

func TestRemoveUserRefusesSelfAndLoggedIn(t *testing.T) {
	f := usersFixture(t)
	if err := addUser("bob", "Bob", true, "pw"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(usersOEM)
	f.calls = nil

	if err := removeUser("bob", true, "bob"); !errors.Is(err, errSelfRemove) {
		t.Errorf("removeUser(self) = %v, want errSelfRemove", err)
	}
	loggedIn = func(name string) (bool, error) { return name == "bob", nil }
	if err := removeUser("bob", true, "anna"); !errors.Is(err, errLoggedIn) {
		t.Errorf("removeUser(logged in) = %v, want errLoggedIn", err)
	}
	loggedIn = func(string) (bool, error) { return false, errors.New("no logind") }
	if err := removeUser("bob", true, "anna"); err == nil {
		t.Error("removeUser went on without knowing whether bob is logged in")
	}
	if len(f.calls) != 0 {
		t.Errorf("commands ran: %q", f.names())
	}
	after, _ := os.ReadFile(usersOEM)
	if string(after) != string(before) {
		t.Error("OEM file changed")
	}
}

func TestRemoveOwnUserDropsItsEntry(t *testing.T) {
	for _, keep := range []bool{true, false} {
		t.Run(fmt.Sprint("keepHome=", keep), func(t *testing.T) {
			f := usersFixture(t)
			if err := addUser("bob", "Bob", false, "pw"); err != nil {
				t.Fatal(err)
			}
			f.calls = nil
			if err := removeUser("bob", keep, "anna"); err != nil {
				t.Fatal(err)
			}
			if _, ok := oemState(t).Own["bob"]; ok {
				t.Error("own.bob still in the OEM file")
			}
			d, _ := readOEM(t)
			if _, ok := d.users()["bob"]; ok {
				t.Error("stage entry for bob still in the OEM file")
			}
			want := "userdel -r bob"
			if keep {
				want = "userdel bob"
			}
			if got := f.mutations(); !reflect.DeepEqual(got, []string{want}) {
				t.Errorf("commands = %q, want %q", got, want)
			}
		})
	}
}

func TestRemoveInstallerUserIsAnOverride(t *testing.T) {
	f := usersFixture(t)
	if err := addUser("bob", "Bob", true, "pw"); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	if err := removeUser("anna", false, "bob"); err != nil {
		t.Fatal(err)
	}
	if got := f.mutations(); !reflect.DeepEqual(got, []string{"userdel -r anna"}) {
		t.Errorf("commands = %q", got)
	}
	if !oemState(t).Overrides["anna"].Removed {
		t.Error("overrides.anna.removed is not set")
	}
	d, _ := readOEM(t)
	if _, ok := d.users()["anna"]; ok {
		t.Error("a users: entry for anna")
	}
	if cmds := d.commands(); !reflect.DeepEqual(cmds, []string{"userdel anna"}) {
		t.Errorf("boot commands = %q, want [userdel anna]", cmds)
	}
	// The name stays taken: the installer's file creates anna again at boot.
	if err := addUser("anna", "Anna", false, "pw"); !errors.Is(err, errUserExists) {
		t.Errorf("addUser(anna) after remove = %v, want errUserExists", err)
	}
}

func TestSetAdminOffRefusesTheLastAdmin(t *testing.T) {
	f := usersFixture(t)
	if err := setAdmin("anna", false); !errors.Is(err, errLastAdmin) {
		t.Fatalf("setAdmin(anna, off) = %v, want errLastAdmin", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("commands ran: %q", f.names())
	}
	// A system account in admin does not count.
	appendLine(t, passwdFile, "bob:x:1001:1004:Bob:/home/bob:/bin/bash")
	editMembers(t, "admin", "svc", true)
	if err := setAdmin("anna", false); !errors.Is(err, errLastAdmin) {
		t.Fatalf("setAdmin(anna, off) with svc in admin = %v, want errLastAdmin", err)
	}
	if err := setAdmin("svc", true); !errors.Is(err, errSystemAccount) {
		t.Errorf("setAdmin(svc) = %v, want errSystemAccount", err)
	}
	if err := setAdmin("zed", true); !errors.Is(err, errNoSuchUser) {
		t.Errorf("setAdmin(zed) = %v, want errNoSuchUser", err)
	}
}

func TestSetAdminOnInstallerUserAddsACommand(t *testing.T) {
	f := usersFixture(t)
	if err := addUser("bob", "Bob", true, "pw"); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	if err := setAdmin("anna", false); err != nil {
		t.Fatal(err)
	}
	d, _ := readOEM(t)
	if got := f.mutations(); !reflect.DeepEqual(got, []string{"gpasswd -d anna admin"}) {
		t.Errorf("live = %q", got)
	}
	if cmds := d.commands(); !reflect.DeepEqual(cmds, []string{"gpasswd -d anna admin"}) {
		t.Errorf("boot commands = %q", cmds)
	}
	if _, ok := d.users()["anna"]; ok {
		t.Error("a users: entry for anna")
	}

	f.calls = nil
	if err := setAdmin("anna", true); err != nil {
		t.Fatal(err)
	}
	d, _ = readOEM(t)
	if got := f.mutations(); !reflect.DeepEqual(got, []string{"gpasswd -a anna admin"}) {
		t.Errorf("live = %q", got)
	}
	if cmds := d.commands(); !reflect.DeepEqual(cmds, []string{"gpasswd -a anna admin"}) {
		t.Errorf("boot commands = %q", cmds)
	}

	// Own users carry admin in their groups, not in a command.
	f.calls = nil
	if err := setAdmin("bob", false); err != nil {
		t.Fatal(err)
	}
	if got := f.mutations(); !reflect.DeepEqual(got, []string{"gpasswd -d bob admin"}) {
		t.Errorf("live = %q", got)
	}
	d, _ = readOEM(t)
	if gs := fmt.Sprint(d.users()["bob"]["groups"]); strings.Contains(gs, "admin") {
		t.Errorf("bob groups = %s, want no admin", gs)
	}
	if oemState(t).Own["bob"].Admin {
		t.Error("own.bob.admin is still true")
	}
}

func TestSetPasswordOfInstallerUserIsAPasswdOnlyEntry(t *testing.T) {
	f := usersFixture(t)
	if err := setPassword("anna", "new pass"); err != nil {
		t.Fatal(err)
	}
	hash := fakeHash("fakesalt", "new pass")
	d, _ := readOEM(t)
	if got := d.users()["anna"]; !reflect.DeepEqual(got, map[string]any{"passwd": hash}) {
		t.Errorf("users: anna = %v, want passwd only", got)
	}
	if cmds := d.commands(); len(cmds) != 0 {
		t.Errorf("boot commands = %q, want none", cmds)
	}
	if got := f.mutations(); !reflect.DeepEqual(got, []string{"chpasswd -e"}) {
		t.Errorf("live = %q", got)
	}
	for _, c := range f.calls {
		if c.name == "chpasswd" && c.stdin != "anna:"+hash+"\n" {
			t.Errorf("chpasswd stdin = %q", c.stdin)
		}
	}
	if err := setPassword("anna", ""); !errors.Is(err, errEmptyPassword) {
		t.Errorf("empty password = %v", err)
	}
	if err := setPassword("root", "x"); !errors.Is(err, errSystemAccount) {
		t.Errorf("root = %v", err)
	}
	if err := setPassword("zed", "x"); !errors.Is(err, errNoSuchUser) {
		t.Errorf("zed = %v", err)
	}
}

func TestSetPasswordOfOwnUserUpdatesItsEntry(t *testing.T) {
	usersFixture(t)
	if err := addUser("bob", "Bob", false, "old"); err != nil {
		t.Fatal(err)
	}
	if err := setPassword("bob", "new"); err != nil {
		t.Fatal(err)
	}
	if got := oemState(t).Own["bob"].Passwd; got != fakeHash("fakesalt", "new") {
		t.Errorf("own.bob.passwd = %q", got)
	}
}

func TestVerifyPassword(t *testing.T) {
	f := usersFixture(t)
	if err := verifyPassword("anna", "annapass"); err != nil {
		t.Errorf("right password = %v", err)
	}
	if err := verifyPassword("anna", "nope"); !errors.Is(err, errWrongPassword) {
		t.Errorf("wrong password = %v", err)
	}
	for _, c := range f.calls {
		if c.String() != "openssl passwd -6 -salt annasalt -stdin" {
			t.Errorf("ran %q", c)
		}
	}
	for _, h := range []string{"$y$j9T$abc$def", "$6$rounds=5000$salt$hash", "!", "*", ""} {
		writeLines(t, shadowFile, []string{"anna:" + h + ":20000::::::"})
		if err := verifyPassword("anna", "annapass"); !errors.Is(err, errUnverifiable) {
			t.Errorf("hash %q = %v, want errUnverifiable", h, err)
		}
	}
	if err := verifyPassword("zed", "x"); !errors.Is(err, errNoSuchUser) {
		t.Errorf("unknown user = %v", err)
	}
}

func TestRenderedFileRoundTrips(t *testing.T) {
	usersFixture(t)
	yes, no := true, false
	obrien := "Anna O'Brien"
	st := usersState{
		Own: map[string]ownUser{
			"bob":  {Gecos: "Bob", Passwd: "$6$s$h", Admin: true},
			"carl": {Gecos: "Carl \"C\" Ü", Passwd: "$6$t$i"},
		},
		Overrides: map[string]overrideUser{
			"anna": {Gecos: &obrien, Passwd: "$6$u$j", Admin: &no},
			"dora": {Admin: &yes},
			"eve":  {Removed: true},
		},
	}
	if err := writeUsersState(st); err != nil {
		t.Fatal(err)
	}
	if got := oemState(t); !reflect.DeepEqual(got, st) {
		t.Fatalf("round trip = %+v, want %+v", got, st)
	}
	d, _ := readOEM(t)
	want := []string{
		`usermod -c 'Anna O'\''Brien' anna`,
		"gpasswd -d anna admin",
		"gpasswd -a dora admin",
		"userdel eve",
	}
	if cmds := d.commands(); !reflect.DeepEqual(cmds, want) {
		t.Errorf("boot commands = %q, want %q", cmds, want)
	}
	if got := d.users()["anna"]; !reflect.DeepEqual(got, map[string]any{"passwd": "$6$u$j"}) {
		t.Errorf("users: anna = %v", got)
	}
	if _, ok := d.users()["dora"]; ok {
		t.Error("a users: entry for dora, who has no password override")
	}
	if got := d.users()["carl"]["gecos"]; got != "Carl \"C\" Ü" {
		t.Errorf("carl gecos = %v", got)
	}
	// The users stage comes before the commands stage.
	if n := len(d.Stages.Boot); n != 2 || d.Stages.Boot[0].Name != "Isotope users" || d.Stages.Boot[1].Name != "Isotope user changes" {
		t.Errorf("boot stages = %+v", d.Stages.Boot)
	}
}

func TestPasswordLengthLimit(t *testing.T) {
	f := usersFixture(t)
	if err := addUser("bob", "Bob", false, strings.Repeat("p", 256)); err != nil {
		t.Fatalf("256 bytes = %v, want accepted", err)
	}
	f.calls = nil
	long := strings.Repeat("p", 257)
	if err := addUser("carl", "Carl", false, long); !errors.Is(err, errBadPassword) {
		t.Errorf("addUser 257 bytes = %v, want errBadPassword", err)
	}
	if err := setPassword("bob", long); !errors.Is(err, errBadPassword) {
		t.Errorf("setPassword 257 bytes = %v, want errBadPassword", err)
	}
	if err := setPassword("bob", "a\nb"); !errors.Is(err, errBadPassword) {
		t.Errorf("setPassword with a line break = %v, want errBadPassword", err)
	}
	if err := verifyPassword("anna", long); !errors.Is(err, errBadPassword) {
		t.Errorf("verifyPassword 257 bytes = %v, want errBadPassword", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("commands ran: %q", f.names())
	}
	if _, ok := oemState(t).Own["carl"]; ok {
		t.Error("carl written to the OEM file")
	}
}

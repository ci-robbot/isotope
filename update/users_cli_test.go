package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

type fakeUsersClient struct {
	rows    []UserRow
	added   []string
	removed []string
	passwds []string
	admins  []string
}

func (f *fakeUsersClient) Users() ([]UserRow, error) { return f.rows, nil }
func (f *fakeUsersClient) AddUser(name, full string, admin bool, pw string) error {
	f.added = append(f.added, strings.Join([]string{name, full, boolStr(admin), pw}, "|"))
	return nil
}
func (f *fakeUsersClient) RemoveUser(name string, keepHome bool) error {
	f.removed = append(f.removed, name+"|"+boolStr(keepHome))
	return nil
}
func (f *fakeUsersClient) SetPassword(name, cur, pw string) error {
	f.passwds = append(f.passwds, strings.Join([]string{name, cur, pw}, "|"))
	return nil
}
func (f *fakeUsersClient) SetAdmin(name string, on bool) error {
	f.admins = append(f.admins, name+"|"+boolStr(on))
	return nil
}
func (f *fakeUsersClient) Close() {}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func usersEnv(t *testing.T, fc *fakeUsersClient, ft *fakeTerm) {
	t.Helper()
	oldD, oldT, oldC, oldS := dialUsers, newTerminal, currentUserName, stdinIsTerminal
	stdinIsTerminal = func(io.Reader) bool { return true }
	dialUsers = func() (usersClient, error) { return fc, nil }
	newTerminal = func(io.Writer) (terminal, error) { return ft, nil }
	currentUserName = func() string { return "anna" }
	t.Cleanup(func() { dialUsers, newTerminal, currentUserName, stdinIsTerminal = oldD, oldT, oldC, oldS })
}

func runUsers(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := cmdUsers(args, strings.NewReader(""), &out, &errb)
	return code, out.String(), errb.String()
}

func TestUsersListPrintsAColumnPerField(t *testing.T) {
	fc := &fakeUsersClient{rows: []UserRow{
		{Name: "anna", FullName: "Anna Rossi", Admin: true},
		{Name: "bob", FullName: "", Admin: false},
	}}
	usersEnv(t, fc, &fakeTerm{})
	code, out, _ := runUsers()
	if code != exitOK {
		t.Fatalf("exit %d", code)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("want header and 2 rows, got %q", out)
	}
	for i, want := range [][]string{{"NAME", "FULL NAME", "ADMIN"}, {"anna", "Anna", "Rossi", "yes"}, {"bob", "no"}} {
		if got := strings.Fields(lines[i]); strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("line %d: %q, want fields %v", i, lines[i], want)
		}
	}
}

func TestUsersAddAsksTwiceAndRefusesAMismatch(t *testing.T) {
	fc := &fakeUsersClient{}
	usersEnv(t, fc, &fakeTerm{secrets: []string{"a", "b"}})
	code, _, errs := runUsers("add", "carl")
	if code != exitError || !strings.Contains(errs, "The passwords do not match") {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	if len(fc.added) != 0 {
		t.Fatalf("AddUser called: %v", fc.added)
	}
	usersEnv(t, fc, &fakeTerm{secrets: []string{"pw", "pw"}})
	if code, _, errs := runUsers("add", "carl", "--full-name", "Carl C", "--admin"); code != exitOK {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if len(fc.added) != 1 || fc.added[0] != "carl|Carl C|true|pw" {
		t.Fatalf("added %v", fc.added)
	}
}

func TestUsersPasswdForYourselfAsksTheCurrentOne(t *testing.T) {
	fc := &fakeUsersClient{}
	ft := &fakeTerm{secrets: []string{"old", "new", "new"}}
	usersEnv(t, fc, ft)
	if code, _, errs := runUsers("passwd"); code != exitOK {
		t.Fatalf("exit %d: %s", code, errs)
	}
	want := []string{"Current password: ", "New password: ", "Again: "}
	if strings.Join(ft.prompts, "|") != strings.Join(want, "|") {
		t.Fatalf("prompts %q", ft.prompts)
	}
	if len(fc.passwds) != 1 || fc.passwds[0] != "anna|old|new" {
		t.Fatalf("passwds %v", fc.passwds)
	}
	// Another user: no current password.
	ft2 := &fakeTerm{secrets: []string{"x", "x"}}
	usersEnv(t, fc, ft2)
	if code, _, _ := runUsers("passwd", "bob"); code != exitOK {
		t.Fatal("exit")
	}
	if len(ft2.prompts) != 2 || fc.passwds[1] != "bob||x" {
		t.Fatalf("prompts %q passwds %v", ft2.prompts, fc.passwds)
	}
}

func TestUsersRemoveKeepsTheHomeByDefault(t *testing.T) {
	fc := &fakeUsersClient{}
	usersEnv(t, fc, &fakeTerm{})
	if code, _, e := runUsers("remove", "bob"); code != exitOK {
		t.Fatalf("exit %d: %s", code, e)
	}
	if code, _, e := runUsers("remove", "bob", "--delete-home"); code != exitOK {
		t.Fatalf("exit %d: %s", code, e)
	}
	if len(fc.removed) != 2 || fc.removed[0] != "bob|true" || fc.removed[1] != "bob|false" {
		t.Fatalf("removed %v", fc.removed)
	}
}

func TestUsersAdmin(t *testing.T) {
	fc := &fakeUsersClient{}
	usersEnv(t, fc, &fakeTerm{})
	for _, v := range []string{"on", "off"} {
		if code, _, e := runUsers("admin", "bob", v); code != exitOK {
			t.Fatalf("exit %d: %s", code, e)
		}
	}
	if code, _, _ := runUsers("admin", "bob", "maybe"); code != exitUsage {
		t.Fatalf("exit %d", code)
	}
	if len(fc.admins) != 2 || fc.admins[0] != "bob|true" || fc.admins[1] != "bob|false" {
		t.Fatalf("admins %v", fc.admins)
	}
}

func TestUsersUsage(t *testing.T) {
	usersEnv(t, &fakeUsersClient{}, &fakeTerm{})
	for _, args := range [][]string{{"frobnicate"}, {"add"}, {"remove"}, {"admin", "bob"}} {
		if code, _, _ := runUsers(args...); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, exitUsage)
		}
	}
}

// runUsersStdin runs with stdin as a pipe, not a terminal.
func runUsersStdin(in string, args ...string) (int, string, string) {
	stdinIsTerminal = func(io.Reader) bool { return false }
	var out, errb bytes.Buffer
	code := cmdUsers(args, strings.NewReader(in), &out, &errb)
	return code, out.String(), errb.String()
}

func TestUsersAddReadsTwoLinesFromStdin(t *testing.T) {
	fc := &fakeUsersClient{}
	usersEnv(t, fc, &fakeTerm{})
	if code, _, e := runUsersStdin("pw\npw\n", "add", "carl"); code != exitOK {
		t.Fatalf("exit %d: %s", code, e)
	}
	if len(fc.added) != 1 || fc.added[0] != "carl||false|pw" {
		t.Fatalf("added %v", fc.added)
	}
}

func TestUsersAddMismatchOnStdin(t *testing.T) {
	fc := &fakeUsersClient{}
	usersEnv(t, fc, &fakeTerm{})
	code, _, e := runUsersStdin("a\nb\n", "add", "carl")
	if code != exitError || !strings.Contains(e, "The passwords do not match") || len(fc.added) != 0 {
		t.Fatalf("exit %d, stderr %q, added %v", code, e, fc.added)
	}
	if code, _, _ := runUsersStdin("only-one\n", "add", "carl"); code != exitError {
		t.Fatalf("a missing line: exit %d", code)
	}
}

func TestUsersPasswdForYourselfOnStdin(t *testing.T) {
	fc := &fakeUsersClient{}
	usersEnv(t, fc, &fakeTerm{})
	if code, _, e := runUsersStdin("old\nnew\nnew\n", "passwd"); code != exitOK {
		t.Fatalf("exit %d: %s", code, e)
	}
	if len(fc.passwds) != 1 || fc.passwds[0] != "anna|old|new" {
		t.Fatalf("passwds %v", fc.passwds)
	}
}

func TestUsersPasswdWithNoCallerName(t *testing.T) {
	fc := &fakeUsersClient{}
	usersEnv(t, fc, &fakeTerm{})
	currentUserName = func() string { return "" }
	if code, _, e := runUsers("passwd"); code != exitError || !strings.Contains(e, "give a NAME") || len(fc.passwds) != 0 {
		t.Fatalf("exit %d, stderr %q", code, e)
	}
}

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"strings"

	"github.com/godbus/dbus/v5"
)

const usersUsage = `Usage: isotope users [command]

Manage the user accounts of this computer. Runs as root through the update
service. Changes are kept across restarts.

Commands:
  (none)                      list the users: NAME, FULL NAME, ADMIN
  add NAME [--full-name TEXT] [--admin]
                              add a user; asks for the password twice
  remove NAME [--delete-home] remove a user; the home folder stays unless
                              --delete-home is given
  passwd [NAME]               change a password; NAME is you if left out,
                              and then the current password is asked
  admin NAME on|off           give or take the administrator right

Passwords are asked on the terminal. When the input is not a terminal
(a pipe, cloud-init, ssh without a terminal), each password prompt reads one
line from it: add reads the password and the same again; passwd for
yourself reads the current password, the new one and the new one again.
`

// UserRow is one user as the service lists it.
type UserRow struct {
	Name, FullName, Home string
	UID                  uint32
	Admin                bool
}

// usersClient is the CLI's view of the users half of the service.
type usersClient interface {
	Users() ([]UserRow, error)
	AddUser(name, fullName string, admin bool, password string) error
	RemoveUser(name string, keepHome bool) error
	SetPassword(name, current, password string) error
	SetAdmin(name string, on bool) error
	Close()
}

var dialUsers = func() (usersClient, error) { return dial() }

// stdinIsTerminal tells if r is a terminal. A variable for the tests.
var stdinIsTerminal = func(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// lineTerminal reads one line per prompt from a pipe, with no echo handling.
type lineTerminal struct{ r *bufio.Reader }

func (t *lineTerminal) Line(string) (string, error) {
	s, err := t.r.ReadString('\n')
	if err != nil && s == "" {
		if err == io.EOF {
			err = errors.New("not enough lines on the input")
		}
		return "", err
	}
	return strings.TrimRight(s, "\r\n"), nil
}

func (t *lineTerminal) Secret(p string) (string, error) { return t.Line(p) }

// usersTerminal is the terminal when stdin is one and can be opened; else
// the lines of stdin.
func usersTerminal(stdin io.Reader, stdout io.Writer) terminal {
	if stdinIsTerminal(stdin) {
		if t, err := newTerminal(stdout); err == nil {
			return t
		}
	}
	return &lineTerminal{r: bufio.NewReader(stdin)}
}

// currentUserName is the name of the user who runs the command. A variable
// for the tests.
var currentUserName = func() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	return u.Username
}

func (c *client) Users() ([]UserRow, error) {
	var ms []map[string]dbus.Variant
	if err := c.call("Users", []any{&ms}); err != nil {
		return nil, err
	}
	rows := make([]UserRow, 0, len(ms))
	for _, m := range ms {
		uid, _ := m["uid"].Value().(uint32)
		rows = append(rows, UserRow{
			Name: str(m, "name"), FullName: str(m, "full_name"),
			Home: str(m, "home"), UID: uid, Admin: boolean(m, "admin"),
		})
	}
	return rows, nil
}

func (c *client) AddUser(name, fullName string, admin bool, password string) error {
	return withSecrets([]string{password}, func(fd dbus.UnixFD) error {
		return c.call("AddUser", nil, name, fullName, admin, fd)
	})
}

func (c *client) RemoveUser(name string, keepHome bool) error {
	return c.call("RemoveUser", nil, name, keepHome)
}

// SetPassword sends the current password first, then the new one.
func (c *client) SetPassword(name, current, password string) error {
	return withSecrets([]string{current, password}, func(fd dbus.UnixFD) error {
		return c.call("SetPassword", nil, name, fd)
	})
}

func (c *client) SetAdmin(name string, on bool) error {
	return c.call("SetAdmin", nil, name, on)
}

func describeUsersError(err error) string {
	if daemonErrName(err) == "NotAuthorized" {
		return "Only an administrator can change the users of this system."
	}
	return describeDaemonError(err)
}

func usersBad(stderr io.Writer, msg string) int {
	fmt.Fprintf(stderr, "isotope users: %s\n\n%s", msg, usersUsage)
	return exitUsage
}

// askNewPassword asks for a password twice.
func askNewPassword(t terminal, first string, stderr io.Writer) (string, bool) {
	a, err := t.Secret(first)
	if err != nil {
		return "", false
	}
	b, err := t.Secret("Again: ")
	if err != nil {
		return "", false
	}
	if a != b {
		fmt.Fprintln(stderr, "isotope users: The passwords do not match; nothing changed")
		return "", false
	}
	return a, true
}

func cmdUsers(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	sub := ""
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	var pos []string
	var fullName string
	var admin, deleteHome bool
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case sub == "add" && a == "--full-name":
			if i+1 >= len(args) {
				return usersBad(stderr, "--full-name needs a value")
			}
			i++
			fullName = args[i]
		case sub == "add" && strings.HasPrefix(a, "--full-name="):
			fullName = strings.TrimPrefix(a, "--full-name=")
		case sub == "add" && a == "--admin":
			admin = true
		case sub == "remove" && a == "--delete-home":
			deleteHome = true
		case strings.HasPrefix(a, "-"):
			return usersBad(stderr, fmt.Sprintf("unknown option %q", a))
		default:
			pos = append(pos, a)
		}
	}
	switch sub {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usersUsage)
		return exitOK
	case "":
		if len(pos) != 0 {
			return usersBad(stderr, "unexpected argument")
		}
	case "add", "remove":
		if len(pos) != 1 {
			return usersBad(stderr, sub+" needs one NAME")
		}
	case "passwd":
		if len(pos) > 1 {
			return usersBad(stderr, "passwd takes at most one NAME")
		}
	case "admin":
		if len(pos) != 2 || (pos[1] != "on" && pos[1] != "off") {
			return usersBad(stderr, "admin needs NAME and on or off")
		}
	default:
		return usersBad(stderr, fmt.Sprintf("unknown command %q", sub))
	}

	// Ask before dialing: a refused password needs no service.
	var secrets []string
	switch sub {
	case "add", "passwd":
		t := usersTerminal(stdin, stdout)
		var err error
		if sub == "add" {
			pw, ok := askNewPassword(t, "Password: ", stderr)
			if !ok {
				return exitError
			}
			secrets = []string{pw}
			break
		}
		me := currentUserName()
		if me == "" && len(pos) == 0 {
			fmt.Fprintln(stderr, "isotope users: cannot tell which user you are; give a NAME")
			return exitError
		}
		self := len(pos) == 0 || pos[0] == me
		cur := ""
		if self {
			if cur, err = t.Secret("Current password: "); err != nil {
				return exitError
			}
		}
		pw, ok := askNewPassword(t, "New password: ", stderr)
		if !ok {
			return exitError
		}
		secrets = []string{cur, pw}
	}

	c, err := dialUsers()
	if err != nil {
		fmt.Fprintf(stderr, "isotope users: cannot reach the update service: %v\n", err)
		return exitError
	}
	defer c.Close()
	fail := func(err error) int {
		fmt.Fprintf(stderr, "isotope users: %s\n", describeUsersError(err))
		return exitError
	}
	switch sub {
	case "":
		rows, err := c.Users()
		if err != nil {
			return fail(err)
		}
		w := len("NAME")
		for _, r := range rows {
			w = max(w, len(r.Name))
		}
		fw := len("FULL NAME")
		for _, r := range rows {
			fw = max(fw, len(r.FullName))
		}
		fmt.Fprintf(stdout, "%-*s  %-*s  %s\n", w, "NAME", fw, "FULL NAME", "ADMIN")
		for _, r := range rows {
			adm := "no"
			if r.Admin {
				adm = "yes"
			}
			fmt.Fprintf(stdout, "%-*s  %-*s  %s\n", w, r.Name, fw, r.FullName, adm)
		}
	case "add":
		if err := c.AddUser(pos[0], fullName, admin, secrets[0]); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "Added %s.\n", pos[0])
	case "remove":
		if err := c.RemoveUser(pos[0], !deleteHome); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "Removed %s.\n", pos[0])
	case "passwd":
		name := currentUserName()
		if len(pos) == 1 {
			name = pos[0]
		}
		if name == "" {
			fmt.Fprintln(stderr, "isotope users: cannot tell which user you are; give a NAME")
			return exitError
		}
		if err := c.SetPassword(name, secrets[0], secrets[1]); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "Changed the password of %s.\n", name)
	case "admin":
		if err := c.SetAdmin(pos[0], pos[1] == "on"); err != nil {
			return fail(err)
		}
		state := "no longer"
		if pos[1] == "on" {
			state = "now"
		}
		fmt.Fprintf(stdout, "%s is %s an administrator.\n", pos[0], state)
	}
	return exitOK
}

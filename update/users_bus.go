package main

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// The users methods. Users is open to every caller, as /etc/passwd is.
// The others need actionUsers, except SetPassword for the caller's own
// account. Passwords travel as a pipe fd, as the recovery secrets do.

// userName names the user with uid. A variable so the tests can fake it.
var userName = func(uid uint32) (string, error) {
	u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
	if err != nil {
		return "", err
	}
	return u.Username, nil
}

// errUnverifiableSelf is errUnverifiable for a user who changes their own
// password: passwd asks PAM, which can check any hash.
var errUnverifiableSelf = unverifiableSelf{}

type unverifiableSelf struct{}

func (unverifiableSelf) Error() string {
	return "the current password is stored in a form that Isotope cannot check: change it with passwd in a terminal, or ask an administrator to set a new password"
}

func (unverifiableSelf) Unwrap() error { return errUnverifiable }

// callerName is the user name of the process behind sender. A caller
// without a name is refused: removeUser and SetPassword compare it with a
// user name, and an empty name must never match or miss by accident.
func (s *service) callerName(sender dbus.Sender, method string) (string, *dbus.Error) {
	if s.uidOf == nil {
		fmt.Fprintf(os.Stderr, "isotope: refusing %s for %s: no way to look up the caller's user\n", method, sender)
		return "", errNotAuthorized
	}
	uid, err := s.uidOf(sender)
	if err != nil {
		fmt.Fprintf(os.Stderr, "isotope: refusing %s for %s: %v\n", method, sender, err)
		return "", errNotAuthorized
	}
	name, err := userName(uid)
	if err == nil && name == "" {
		err = fmt.Errorf("uid %d has no user name", uid)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "isotope: refusing %s for %s: %v\n", method, sender, err)
		return "", errNotAuthorized
	}
	return name, nil
}

func (a busAPI) Users() ([]map[string]dbus.Variant, *dbus.Error) {
	done, derr := a.s.enter()
	if derr != nil {
		return nil, derr
	}
	defer done()
	usersMu.Lock()
	us, err := listUsers()
	usersMu.Unlock()
	if err != nil {
		return nil, busError(err)
	}
	out := make([]map[string]dbus.Variant, 0, len(us))
	for _, u := range us {
		out = append(out, map[string]dbus.Variant{
			"name":      dbus.MakeVariant(u.Name),
			"full_name": dbus.MakeVariant(u.FullName),
			"uid":       dbus.MakeVariant(u.UID),
			"admin":     dbus.MakeVariant(u.Admin),
			"home":      dbus.MakeVariant(u.Home),
		})
	}
	return out, nil
}

// AddUser reads one line from fd: the password.
func (a busAPI) AddUser(sender dbus.Sender, name, fullName string, admin bool, fd dbus.UnixFD) *dbus.Error {
	done, derr := a.s.enter()
	if derr != nil {
		closeFD(fd)
		return derr
	}
	defer done()
	if err := a.s.authorize(sender, actionUsers); err != nil {
		closeFD(fd)
		return err
	}
	if derr := a.s.notRestoring(); derr != nil {
		closeFD(fd)
		return derr
	}
	secrets, err := readSecrets(fd)
	if err != nil {
		return busError(err)
	}
	if len(secrets) != 1 {
		return busError(errBadPassword)
	}
	return busError(addUser(name, fullName, admin, secrets[0]))
}

func (a busAPI) RemoveUser(sender dbus.Sender, name string, keepHome bool) *dbus.Error {
	done, derr := a.s.enter()
	if derr != nil {
		return derr
	}
	defer done()
	if err := a.s.authorize(sender, actionUsers); err != nil {
		return err
	}
	if derr := a.s.notRestoring(); derr != nil {
		return derr
	}
	caller, derr := a.s.callerName(sender, "RemoveUser")
	if derr != nil {
		return derr
	}
	return busError(removeUser(name, keepHome, caller))
}

// SetPassword reads two lines from fd: the current password (may be empty)
// and the new one. A user who changes their own password needs no polkit
// answer but must give the current password, an administrator too: the
// session may be someone else's at the keyboard. For another user's
// password polkit decides and the first line is ignored.
func (a busAPI) SetPassword(sender dbus.Sender, name string, fd dbus.UnixFD) *dbus.Error {
	done, derr := a.s.enter()
	if derr != nil {
		closeFD(fd)
		return derr
	}
	defer done()
	caller, derr := a.s.callerName(sender, "SetPassword")
	if derr != nil {
		closeFD(fd)
		return derr
	}
	// This is the one method that reads an argument before polkit: name,
	// and only to compare it with the caller's own user name.
	self := name == caller
	if self {
		derr = a.s.notSandboxed(sender, "SetPassword (own password)")
	} else {
		derr = a.s.authorize(sender, actionUsers)
	}
	if derr == nil {
		derr = a.s.notRestoring()
	}
	if derr != nil {
		closeFD(fd)
		return derr
	}
	secrets, err := readSecrets(fd)
	if err != nil {
		return busError(err)
	}
	if len(secrets) < 2 {
		return busError(errors.New("want two lines: the current password and the new one"))
	}
	if len(secrets) > 2 {
		return busError(errBadPassword)
	}
	if self {
		if err := verifySelf(name, secrets[0]); err != nil {
			if errors.Is(err, errUnverifiable) {
				err = errUnverifiableSelf
			}
			return busError(err)
		}
	}
	return busError(setPassword(name, secrets[1]))
}

// wrongPasswordWait runs after a wrong current password, so that
// SetPassword cannot be used to guess a password fast. A variable so the
// tests do not wait.
var wrongPasswordWait = func() { time.Sleep(2 * time.Second) }

// selfVerify holds one *sync.Mutex per user name: one check of a user's
// current password at a time, so parallel calls do not get around the wait.
var selfVerify sync.Map

// verifySelf is verifyPassword for a user who changes their own password,
// one check at a time per user, with a wait after a wrong password.
func verifySelf(name, plain string) error {
	m, _ := selfVerify.LoadOrStore(name, new(sync.Mutex))
	mu := m.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	err := verifyPassword(name, plain)
	if errors.Is(err, errWrongPassword) {
		wrongPasswordWait()
	}
	return err
}

func (a busAPI) SetAdmin(sender dbus.Sender, name string, on bool) *dbus.Error {
	done, derr := a.s.enter()
	if derr != nil {
		return derr
	}
	defer done()
	if err := a.s.authorize(sender, actionUsers); err != nil {
		return err
	}
	if derr := a.s.notRestoring(); derr != nil {
		return derr
	}
	return busError(setAdmin(name, on))
}

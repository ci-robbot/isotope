package shares

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/godbus/dbus/v5"
)

// SecretService is Keyring over the freedesktop Secret Service on the
// session bus (gnome-keyring on every Isotope desktop). The secret travels
// with the "plain" algorithm: the session bus is the user's own, as
// libsecret's fallback. Items carry the attributes xdg:schema, url and user,
// and go to the default collection, the login keyring that
// pam_gnome_keyring unlocks at login. A locked item is not unlocked here:
// that needs a prompt, and a missing password is then asked for on the
// page.
type SecretService struct{ Conn *dbus.Conn }

// Schema is the xdg:schema attribute of Isotope's share passwords.
const Schema = "io.kairos.isotope.NetworkShare"

const (
	ssName       = "org.freedesktop.secrets"
	ssPath       = dbus.ObjectPath("/org/freedesktop/secrets")
	ssDefault    = dbus.ObjectPath("/org/freedesktop/secrets/aliases/default")
	ssService    = "org.freedesktop.Secret.Service"
	ssItem       = "org.freedesktop.Secret.Item"
	ssCollection = "org.freedesktop.Secret.Collection"
	ssTimeout    = 10 * time.Second
)

// secret is the Secret Service's Secret struct, (oayays).
type secret struct {
	Session     dbus.ObjectPath
	Parameters  []byte
	Value       []byte
	ContentType string
}

var errLocked = errors.New("the keyring is locked")

func attrs(url, user string) map[string]string {
	return map[string]string{"xdg:schema": Schema, "url": url, "user": user}
}

func (k SecretService) call(obj dbus.ObjectPath, method string, out []any, args ...any) error {
	ctx, cancel := context.WithTimeout(context.Background(), ssTimeout)
	defer cancel()
	c := k.Conn.Object(ssName, obj).CallWithContext(ctx, method, 0, args...)
	if c.Err != nil {
		return c.Err
	}
	if len(out) > 0 {
		return c.Store(out...)
	}
	return nil
}

func (k SecretService) session() (dbus.ObjectPath, error) {
	var out dbus.Variant
	var path dbus.ObjectPath
	err := k.call(ssPath, ssService+".OpenSession", []any{&out, &path}, "plain", dbus.MakeVariant(""))
	return path, err
}

func (k SecretService) closeSession(s dbus.ObjectPath) {
	_ = k.call(s, "org.freedesktop.Secret.Session.Close", nil)
}

// search returns the unlocked items, and errLocked when only locked ones
// match.
func (k SecretService) search(url, user string) ([]dbus.ObjectPath, error) {
	var unlocked, locked []dbus.ObjectPath
	if err := k.call(ssPath, ssService+".SearchItems", []any{&unlocked, &locked}, attrs(url, user)); err != nil {
		return nil, err
	}
	if len(unlocked) == 0 && len(locked) > 0 {
		return nil, errLocked
	}
	return unlocked, nil
}

func (k SecretService) Password(url, user string) (string, bool, error) {
	items, err := k.search(url, user)
	if err != nil || len(items) == 0 {
		return "", false, err
	}
	s, err := k.session()
	if err != nil {
		return "", false, err
	}
	defer k.closeSession(s)
	var sec secret
	if err := k.call(items[0], ssItem+".GetSecret", []any{&sec}, s); err != nil {
		return "", false, err
	}
	pw := string(sec.Value)
	clear(sec.Value)
	return pw, true, nil
}

func (k SecretService) StorePassword(url, user, password string) error {
	s, err := k.session()
	if err != nil {
		return err
	}
	defer k.closeSession(s)
	props := map[string]dbus.Variant{
		ssItem + ".Label":      dbus.MakeVariant("Network share " + url + " (" + user + ")"),
		ssItem + ".Attributes": dbus.MakeVariant(attrs(url, user)),
	}
	v := []byte(password)
	defer clear(v)
	var item, prompt dbus.ObjectPath
	err = k.call(ssDefault, ssCollection+".CreateItem", []any{&item, &prompt},
		props, secret{Session: s, Value: v, ContentType: "text/plain"}, true)
	if err != nil {
		return err
	}
	if prompt != "/" && prompt != "" {
		_ = k.call(prompt, "org.freedesktop.Secret.Prompt.Dismiss", nil)
		return fmt.Errorf("%w: the password was not saved", errLocked)
	}
	return nil
}

func (k SecretService) ForgetPassword(url, user string) error {
	items, err := k.search(url, user)
	if err != nil {
		return err
	}
	for _, it := range items {
		var prompt dbus.ObjectPath
		if err := k.call(it, ssItem+".Delete", []any{&prompt}); err != nil {
			return err
		}
	}
	return nil
}

package main

import (
	"math/rand/v2"
	"strings"

	"gioui.org/layout"

	"github.com/kairos-community/isotope/pkg/guikit"
	"github.com/kairos-community/isotope/pkg/i18n"
	"github.com/kairos-community/isotope/pkg/recoverykey"
)

// keyCheck is the state of a key dialog: the key it shows once, the group
// it asks back (1-based) and the field for it.
type keyCheck struct {
	secret  string
	group   int
	typed   *guikit.Field
	focused bool
}

// ok tells if the typed group matches.
func (k *keyCheck) ok() bool {
	return recoverykey.ConfirmsGroup(k.secret, k.group, k.typed.Editor.Text())
}

// clear drops the key and what was typed.
func (k *keyCheck) clear() {
	k.secret = ""
	k.typed.Editor.SetText("")
	k.typed.Err = ""
}

// keyDialog shows a secret once (recovery key, backup password): the key in
// mono, two groups per line (recoverykey.Lines), "I saved it", then a field
// asking group n (1-based, random in 2..7) typed back; done runs when the
// typed group matches (recoverykey.ConfirmsGroup). Cancel at either step
// runs nothing.
func (u *UI) keyDialog(title, intro, key string, done func()) {
	k := &keyCheck{secret: recoverykey.Canonical(key), group: 2 + rand.IntN(6), typed: guikit.NewField(true)}
	if k.secret == "" {
		return
	}
	lines := recoverykey.Lines(k.secret)
	show := newDialog(title, intro, func(gtx layout.Context) layout.Dimensions {
		var c col
		c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
			var rows col
			for i, l := range lines {
				if i > 0 {
					rows.gap(4)
				}
				rows.add(func(gtx layout.Context) layout.Dimensions {
					return u.Label(gtx, guikit.StMono, guikit.ColText, l)
				})
			}
			return rows.layout(gtx)
		}))
		c.gap(8)
		c.add(u.dim(i18n.Tf("The letters are only these: %s", strings.Join(strings.Split(recoverykey.Alphabet, ""), " "))))
		return c.layout(gtx)
	}, 1,
		choice{Label: i18n.T("Cancel"), Kind: guikit.ButtonSecondary, Do: k.clear},
		choice{Label: i18n.T("I saved it"), Kind: guikit.ButtonPrimary, Do: func() { u.askKeyGroup(title, k, done) }})
	show.key = k
	u.open(show)
}

// askKeyGroup is the key dialog's second step. Confirm stays off until the
// group matches, and Cancel has the focus: done is what changes things.
func (u *UI) askKeyGroup(title string, k *keyCheck, done func()) {
	var d *dialog
	confirm := func() {
		if !k.ok() {
			return
		}
		k.clear()
		if done != nil {
			done()
		}
	}
	d = newDialog(title, i18n.Tf("To check you saved it, type group %d of 8 of the new key.", k.group), func(gtx layout.Context) layout.Dimensions {
		if k.typed.Submitted(gtx) && k.ok() {
			u.pick(d, len(d.choices)-1)
		}
		if !k.focused {
			k.focused = true
			gtx.Execute(focus(&k.typed.Editor))
		}
		k.typed.Err = ""
		if t := strings.NewReplacer("-", "", " ", "").Replace(k.typed.Editor.Text()); len(t) >= 8 && !k.ok() {
			k.typed.Err = i18n.T("That is not it. Check what you wrote down.")
		}
		return u.Labelled(gtx, i18n.Tf("Group %d", k.group), k.typed, i18n.T("8 letters."), func(gtx layout.Context) layout.Dimensions {
			return u.Input(gtx, k.typed, guikit.StMono, 38, "", nil)
		})
	}, 0,
		choice{Label: i18n.T("Cancel"), Kind: guikit.ButtonSecondary, Do: k.clear},
		choice{Label: i18n.T("Confirm"), Kind: guikit.ButtonPrimary, Enabled: k.ok, Do: confirm})
	d.key = k
	u.open(d)
}

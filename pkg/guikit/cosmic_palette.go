package guikit

import (
	"image/color"

	"github.com/kairos-community/isotope/pkg/cosmictheme"
)

// CosmicPalette is the palette of a COSMIC theme, so a window looks like
// COSMIC Settings beside it. A key the theme lacks keeps the role from
// IsotopePalette in the theme's mode. See the design note
// docs/superpowers/specs/2026-11-25-isotope-system-native-look-design.md for
// the field behind each role.
func CosmicPalette(t cosmictheme.Theme) Palette {
	p := IsotopePalette(t.Dark)
	if t.Have[cosmictheme.KeyBackground] {
		bg := t.Background
		p.Window, p.Sidebar, p.Footer = On(p.Window, bg.Base), On(p.Window, bg.Base), On(p.Window, bg.Base)
		p.Selected = On(p.Sidebar, bg.Component.Selected)
		p.Text = On(p.Window, bg.On)
		// libcosmic's caption and secondary text: the text colour, fainter.
		p.Text2 = Over(p.Window, opaque(bg.On), 0.85)
		p.Dim = Over(p.Window, opaque(bg.On), 0.7)
		p.Ring = Over(p.Window, opaque(bg.On), 0.45)
		p.KeyBox = On(p.Window, bg.Component.Base)
	}
	if t.Have[cosmictheme.KeyPrimary] {
		pr := t.Primary
		p.Group = On(p.Window, pr.Base)
		p.GroupBorder = Alpha(p.Group, 0)
		p.Hover = On(p.Group, pr.Component.Hover)
		p.Divider = On(p.Group, pr.Divider)
		p.Field = On(p.Group, pr.Component.Base)
	}
	if t.Have[cosmictheme.KeyButton] {
		b := t.Button
		p.Button, p.OnButton = On(p.Group, b.Base), On(p.Group, b.On)
		p.Disabled = On(p.Group, b.Disabled)
		p.OnDisabled = On(p.Disabled, b.OnDisabled)
		p.ButtonBorder = Alpha(p.Button, 0)
	}
	if t.Have[cosmictheme.KeyAccentButton] {
		p.Suggested, p.OnSuggested = On(p.Window, t.AccentButton.Base), On(p.Window, t.AccentButton.On)
	}
	if t.Have[cosmictheme.KeyAccent] {
		a := t.Accent
		p.Accent, p.OnAccent = On(p.Window, a.Base), On(p.Window, a.On)
		p.Focus = On(p.Window, a.Focus)
		p.FocusSoft = Over(p.Window, p.Focus, 0.25)
		p.Link, p.Glow = p.Accent, p.Accent
	}
	if t.Have[cosmictheme.KeyDestructive] {
		d := t.Destructive
		p.Destructive, p.OnDestructive = On(p.Window, d.Base), On(p.Window, d.On)
		p.Error, p.ErrorText = p.Destructive, p.Destructive
	}
	if t.Have[cosmictheme.KeyWarning] {
		p.Warning = On(p.Window, t.Warning.Base)
	}
	if t.Have[cosmictheme.KeySuccess] {
		p.Success = On(p.Window, t.Success.Base)
		p.Check = p.Success
	}
	if t.Have[cosmictheme.KeyShade] {
		p.Shade = t.Shade
	}
	if t.Have[cosmictheme.KeyCornerRadii] {
		p.RadiusS, p.RadiusM = t.RadiusS, t.RadiusM
	}
	return p
}

func opaque(c color.NRGBA) color.NRGBA { c.A = 0xff; return c }

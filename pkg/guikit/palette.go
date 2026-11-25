package guikit

import "image/color"

// The colours of the graphical installer design note's "Visual system"
// table, which are theme/palette.toml's night palette, the same as the
// splash and GRUB.
var (
	ColDesk1     = RGB(0x2e2c17) // the brand glow at the top of the desktop
	ColDesk2     = RGB(0x141411)
	ColDesk3     = RGB(0x0c0d0b)
	ColWindow    = RGB(0x1d1d1a)
	ColSidebar   = RGB(0x181915)
	ColFooter    = RGB(0x1b1b18)
	ColField     = RGB(0x2a2b25)
	ColAccent    = RGB(0xe4de48) // brand: the installer is seen before any theme
	ColOnAccent  = RGB(0x141411)
	ColGlow      = RGB(0xf9f1af)
	ColCheck     = RGB(0x9f9615)
	ColText      = RGB(0xf4f5f2)
	ColText2     = RGB(0xced0c8)
	ColDim       = RGB(0x909287)
	ColRing      = RGB(0x595b52)
	ColError     = RGB(0xff7172)
	ColErrorText = RGB(0xffaaa9)
	ColDisabled  = RGB(0x393a34)
	ColKeyBox    = RGB(0x141411)
	ColWarn      = RGB(0xea8a18) // the palette's amber: a warning, not an error
)

// FocusRing is the accent ring of the mocks, shown around a focused input.
var FocusRing = Over(ColWindow, ColAccent, 0.18)

// FocusRingStrong marks the focused button or card. It is light, so it
// shows around the lime button too. The inputs' faint ring sits next to a
// solid accent border; a button has none, and at 18% the ring did not show
// on the footer at all.
var FocusRingStrong = Over(ColWindow, ColText, 0.7)

// RGB is the opaque colour 0xRRGGBB.
func RGB(v uint32) color.NRGBA {
	return color.NRGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 0xff}
}

// Alpha is c at opacity a (0..1), as the mocks' rgba() values.
func Alpha(c color.NRGBA, a float32) color.NRGBA {
	c.A = uint8(a*255 + 0.5)
	return c
}

// Over is c at opacity a laid on bg, mixed in sRGB as a browser does, so the
// mocks' rgba() values come out as drawn there. Gio blends in linear light,
// which makes a faint overlay much brighter, so translucent fills are mixed
// here into opaque colours instead.
func Over(bg, c color.NRGBA, a float32) color.NRGBA {
	m := func(x, y uint8) uint8 { return uint8(float32(x)*(1-a) + float32(y)*a + 0.5) }
	return color.NRGBA{R: m(bg.R, c.R), G: m(bg.G, c.G), B: m(bg.B, c.B), A: 0xff}
}

// Palette is the colours and corner radii a window draws with. The
// installer and Isotope Welcome draw with InstallerPalette, the night
// colours above; Isotope System draws with its desktop's: CosmicPalette on
// COSMIC, IsotopePalette elsewhere. Every widget of Theme takes its colours
// from Theme.Colors.
type Palette struct {
	// Dark is true for light text on dark surfaces.
	Dark bool
	// Window is the page; Sidebar the navigation column and header bar;
	// Footer the bar under a page.
	Window, Sidebar, Footer color.NRGBA
	// Group is a section's box of rows (a panel, a card); GroupBorder its
	// outline, transparent for none.
	Group, GroupBorder color.NRGBA
	// Hover is a row under the pointer, Selected the current row of a
	// list, Divider a line between rows.
	Hover, Selected, Divider color.NRGBA
	// Field is a text input's box.
	Field color.NRGBA
	// Text is the main text, Text2 a paragraph, Dim a caption or a hint.
	Text, Text2, Dim color.NRGBA
	// Ring is the outline of an unchecked radio or checkbox.
	Ring color.NRGBA
	// Disabled and OnDisabled are a disabled button and its label.
	Disabled, OnDisabled color.NRGBA
	// Button is a standard button, OnButton its label, ButtonBorder its
	// outline (transparent for none).
	Button, OnButton, ButtonBorder color.NRGBA
	// Suggested is the one suggested action of a view.
	Suggested, OnSuggested color.NRGBA
	// Accent is a selection, a switch that is on, a link's underline.
	Accent, OnAccent color.NRGBA
	// Focus is the keyboard focus ring; FocusSoft the faint one around a
	// focused input.
	Focus, FocusSoft color.NRGBA
	// Link is a text button.
	Link color.NRGBA
	// Destructive is a button that removes or erases.
	Destructive, OnDestructive color.NRGBA
	// Error is an error's colour, ErrorText an error's words.
	Error, ErrorText color.NRGBA
	// Warning and Success are a warning's and a done step's colour; Check
	// the fill of a done check.
	Warning, Success, Check color.NRGBA
	// Glow is the light end of a progress bar.
	Glow color.NRGBA
	// KeyBox is a key cap.
	KeyBox color.NRGBA
	// Shade dims the window under a dialog.
	Shade color.NRGBA
	// RadiusS is the corner of buttons, fields and groups; RadiusM of a
	// dialog. In dp.
	RadiusS, RadiusM float32
}

// InstallerPalette is the night palette of the installer's mocks, the Col
// values above.
var InstallerPalette = Palette{
	Dark:   true,
	Window: ColWindow, Sidebar: ColSidebar, Footer: ColFooter,
	Group: ColSidebar, GroupBorder: Over(ColSidebar, ColText, 0.06),
	Hover: Over(ColSidebar, ColText, 0.05), Selected: Over(ColSidebar, ColAccent, 0.12),
	Divider: Over(ColSidebar, ColText, 0.06),
	Field:   ColField,
	Text:    ColText, Text2: ColText2, Dim: ColDim,
	Ring:     ColRing,
	Disabled: ColDisabled, OnDisabled: ColDim,
	Button: ColField, OnButton: ColText, ButtonBorder: Over(ColFooter, ColText, 0.1),
	Suggested: ColAccent, OnSuggested: ColOnAccent,
	Accent: ColAccent, OnAccent: ColOnAccent,
	Focus: FocusRingStrong, FocusSoft: FocusRing,
	Link:        ColGlow,
	Destructive: ColError, OnDestructive: ColOnAccent,
	Error: ColError, ErrorText: ColErrorText,
	Warning: ColWarn, Success: ColCheck, Check: ColCheck,
	Glow:    ColAccent,
	KeyBox:  ColKeyBox,
	Shade:   Alpha(ColKeyBox, 0.72),
	RadiusS: 8, RadiusM: 14,
}

// IsotopePalette is Isotope's theme (theme/palette.toml) as a palette: the
// night roles when dark, else the day ones. It is laid out as COSMIC lays
// out a theme built from the same palette: the window and sidebar on base,
// groups on surface.
func IsotopePalette(dark bool) Palette {
	r := isotopeNight
	if !dark {
		r = isotopeDay
	}
	base, surface := RGB(r.base), RGB(r.surface)
	text := RGB(r.text)
	return Palette{
		Dark:   dark,
		Window: base, Sidebar: base, Footer: base,
		Group: surface, GroupBorder: color.NRGBA{},
		Hover: Over(surface, text, 0.06), Selected: RGB(r.overlay),
		Divider: RGB(r.border),
		Field:   RGB(r.overlay),
		Text:    text, Text2: RGB(r.subtext), Dim: RGB(r.dim),
		Ring:     RGB(r.muted),
		Disabled: RGB(r.overlay), OnDisabled: RGB(r.dim),
		Button: RGB(r.overlay), OnButton: text, ButtonBorder: color.NRGBA{},
		Suggested: RGB(r.accent), OnSuggested: RGB(r.on_accent),
		Accent: RGB(r.accent), OnAccent: RGB(r.on_accent),
		Focus: RGB(r.accent), FocusSoft: Over(base, RGB(r.accent), 0.25),
		Link:        RGB(r.accent),
		Destructive: RGB(r.error), OnDestructive: RGB(r.on_accent),
		Error: RGB(r.error), ErrorText: RGB(r.error),
		Warning: RGB(r.warning), Success: RGB(r.success), Check: RGB(r.success),
		Glow:    RGB(r.accent),
		KeyBox:  RGB(r.crust),
		Shade:   Alpha(RGB(0x000000), 0.32),
		RadiusS: 8, RadiusM: 16,
	}
}

// On is c laid on bg by its own alpha: a COSMIC colour that is
// translucent (a button is a veil over the surface) as the opaque colour it
// shows there. An opaque c is itself.
func On(bg, c color.NRGBA) color.NRGBA {
	if c.A == 0xff {
		return c
	}
	a := float32(c.A) / 255
	c.A = 0xff
	return Over(bg, c, a)
}

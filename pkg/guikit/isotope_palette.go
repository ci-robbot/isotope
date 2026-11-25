package guikit

// Isotope's palette (theme/palette.toml), night and day, for the tools that
// follow the desktop's dark or light mode where there is no COSMIC theme to
// read. tools/gen-theme.py writes the values; edit the palette, not this file.

// isotopeRoles is the palette roles a tool draws with.
type isotopeRoles struct {
	crust, base, surface, overlay, border, muted, dim, subtext, text uint32
	accent, success, warning, error, on_accent                       uint32
}

var isotopeNight = isotopeRoles{
	crust:     0x0c0d0b,
	base:      0x141411,
	surface:   0x1d1d1a,
	overlay:   0x2a2b25,
	border:    0x393a34,
	muted:     0x595b52,
	dim:       0x909287,
	subtext:   0xced0c8,
	text:      0xf4f5f2,
	accent:    0xe9e7c1,
	success:   0x37be95,
	warning:   0xea8a18,
	error:     0xff7172,
	on_accent: 0x141411,
}

var isotopeDay = isotopeRoles{
	crust:     0xd8d9d3,
	base:      0xf4f5f2,
	surface:   0xffffff,
	overlay:   0xe9eae6,
	border:    0xcbcdc6,
	muted:     0x76786d,
	dim:       0x61635a,
	subtext:   0x4a4c43,
	text:      0x1c1d19,
	accent:    0x3e3e32,
	success:   0x167a69,
	warning:   0x832d00,
	error:     0xb00e50,
	on_accent: 0xffffff,
}

// Package cosmictheme reads the theme COSMIC is running: the colours,
// corner radii and dark or light mode that cosmic-settings computes and
// cosmic-config stores, one RON file per key. It reads only the keys
// Isotope's tools draw with, and only the RON they are written in.
//
// The layout is cosmic-theme's at the libcosmic rev Isotope builds
// (tools/isotope-cosmic-theme): com.system76.CosmicTheme.Mode/v1/is_dark,
// then com.system76.CosmicTheme.Dark/v2/<key> or .Light/v2/<key>. The user's
// directory ($XDG_CONFIG_HOME/cosmic) holds what the user changed; the rest
// comes from the system's (/usr/share/cosmic), key by key.
package cosmictheme

import (
	"fmt"
	"image/color"
	"path/filepath"
	"strings"
)

// SystemDir is where the image keeps COSMIC's defaults.
const SystemDir = "/usr/share/cosmic"

const (
	modeDir  = "com.system76.CosmicTheme.Mode/v1"
	darkDir  = "com.system76.CosmicTheme.Dark/v2"
	lightDir = "com.system76.CosmicTheme.Light/v2"
)

// Component is cosmic-theme's Component: the colours of a widget in its
// states.
type Component struct {
	Base, Hover, Pressed, Selected, SelectedText, Focus color.NRGBA
	Divider, On, Disabled, OnDisabled, Border           color.NRGBA
}

// Container is cosmic-theme's Container: a surface, the widgets on it, its
// divider and its text colour.
type Container struct {
	Base      color.NRGBA
	Component Component
	Divider   color.NRGBA
	On        color.NRGBA
}

// Theme is the part of a COSMIC theme the tools use.
type Theme struct {
	Dark                 bool
	Background, Primary  Container
	Accent, AccentButton Component
	Button, Destructive  Component
	Warning, Success     Component
	Shade                color.NRGBA
	RadiusS, RadiusM     float32
	// Have says which keys were read: a key missing from every directory,
	// or not readable, is false and its fields are zero.
	Have map[string]bool
}

// The keys Load reads.
const (
	KeyBackground   = "background"
	KeyPrimary      = "primary"
	KeyAccent       = "accent"
	KeyAccentButton = "accent_button"
	KeyButton       = "button"
	KeyDestructive  = "destructive"
	KeyWarning      = "warning"
	KeySuccess      = "success"
	KeyShade        = "shade"
	KeyCornerRadii  = "corner_radii"
)

// Dirs is the user's COSMIC config directory, then the system's.
func Dirs(getenv func(string) string, home string) []string {
	cfg := getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	return []string{filepath.Join(cfg, "cosmic"), SystemDir}
}

// WatchDirs is the directories whose changes change the theme: the mode and
// both themes in the first (the user's) directory of dirs.
func WatchDirs(dirs []string) []string {
	if len(dirs) == 0 {
		return nil
	}
	return []string{
		filepath.Join(dirs[0], modeDir),
		filepath.Join(dirs[0], darkDir),
		filepath.Join(dirs[0], lightDir),
	}
}

// first is the parsed value of rel in the first directory where it exists
// and parses.
func first(dirs []string, read func(string) ([]byte, error), rel string, use func(value) error) bool {
	for _, d := range dirs {
		b, err := read(filepath.Join(d, rel))
		if err != nil {
			continue
		}
		v, err := parse(b)
		if err != nil {
			continue
		}
		if use(v) == nil {
			return true
		}
	}
	return false
}

// IsDark is COSMIC's mode: true unless is_dark says false. Isotope ships
// dark as the default, and so does COSMIC.
func IsDark(dirs []string, read func(string) ([]byte, error)) bool {
	dark := true
	first(dirs, read, filepath.Join(modeDir, "is_dark"), func(v value) error {
		if v.b == nil {
			return fmt.Errorf("not a boolean")
		}
		dark = *v.b
		return nil
	})
	return dark
}

// Load reads the running theme from dirs (Dirs), in the mode IsDark says.
func Load(dirs []string, read func(string) ([]byte, error)) Theme {
	t := Theme{Dark: IsDark(dirs, read), Have: map[string]bool{}}
	dir := darkDir
	if !t.Dark {
		dir = lightDir
	}
	key := func(k string, use func(value) error) {
		t.Have[k] = first(dirs, read, filepath.Join(dir, k), use)
	}
	container := func(dst *Container) func(value) error {
		return func(v value) error {
			c, err := readContainer(v)
			if err == nil {
				*dst = c
			}
			return err
		}
	}
	component := func(dst *Component) func(value) error {
		return func(v value) error {
			c, err := readComponent(v)
			if err == nil {
				*dst = c
			}
			return err
		}
	}
	key(KeyBackground, container(&t.Background))
	key(KeyPrimary, container(&t.Primary))
	key(KeyAccent, component(&t.Accent))
	key(KeyAccentButton, component(&t.AccentButton))
	key(KeyButton, component(&t.Button))
	key(KeyDestructive, component(&t.Destructive))
	key(KeyWarning, component(&t.Warning))
	key(KeySuccess, component(&t.Success))
	key(KeyShade, func(v value) error {
		c, err := v.colour()
		if err == nil {
			t.Shade = c
		}
		return err
	})
	key(KeyCornerRadii, func(v value) error {
		s, err := radius(v, "radius_s")
		if err != nil {
			return err
		}
		m, err := radius(v, "radius_m")
		if err != nil {
			return err
		}
		t.RadiusS, t.RadiusM = s, m
		return nil
	})
	return t
}

// radius is the first corner of a (f, f, f, f) radius field.
func radius(v value, name string) (float32, error) {
	f, err := v.field(name)
	if err != nil {
		return 0, err
	}
	if len(f.items) == 0 || f.items[0].num == nil {
		return 0, fmt.Errorf("%s is not four numbers", name)
	}
	return float32(*f.items[0].num), nil
}

func readComponent(v value) (Component, error) {
	var c Component
	var errs []string
	for name, dst := range map[string]*color.NRGBA{
		"base": &c.Base, "hover": &c.Hover, "pressed": &c.Pressed, "selected": &c.Selected,
		"selected_text": &c.SelectedText, "focus": &c.Focus, "divider": &c.Divider, "on": &c.On,
		"disabled": &c.Disabled, "on_disabled": &c.OnDisabled, "border": &c.Border,
	} {
		col, err := v.colourField(name)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		*dst = col
	}
	if len(errs) > 0 {
		return Component{}, fmt.Errorf("component: %s", strings.Join(errs, "; "))
	}
	return c, nil
}

func readContainer(v value) (Container, error) {
	var c Container
	var err error
	if c.Base, err = v.colourField("base"); err != nil {
		return Container{}, err
	}
	if c.Divider, err = v.colourField("divider"); err != nil {
		return Container{}, err
	}
	if c.On, err = v.colourField("on"); err != nil {
		return Container{}, err
	}
	comp, err := v.field("component")
	if err != nil {
		return Container{}, err
	}
	if c.Component, err = readComponent(comp); err != nil {
		return Container{}, err
	}
	return c, nil
}

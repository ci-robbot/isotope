# Isotope System: a native window

## Intent

Isotope System must feel like a normal app of the desktop it runs on: a
window like COSMIC Settings, a file manager or a terminal. Today it feels
like a web page in a window. The maintainer's words: "I want it to feel a
native app of the OS, not a page with a background and like a web app", and
"we open as a full page with background, etc, that doesn't make much sense".

The installer keeps its look. It is a full-screen, branded flow in cage, seen
before any desktop exists, and the frame suits it there. Only Isotope System
changes.

## What is wrong today

Isotope System uses the installer's frame, `guikit.Window` and
`Theme.NavSidebar` (`pkg/guikit/window.go`, `nav.go`):

- `Window.Layout` paints a fake desktop inside the app's own window: a dark
  base with a radial glow at the top.
- When the window is 1000x700 dp or larger (it opens at 1100x720), the
  content is a 920x620 rounded card with a drop shadow in the middle, with a
  border of fake desktop round it. See `cmd/isotope-system/testdata/printers-admin.png`.
- The sidebar is headed by the Isotope mark and "Isotope System", as an app
  landing page is.
- The colours are a fixed night palette (`pkg/guikit/palette.go`): no light
  mode and no desktop accent colour.
- On COSMIC the window has no title bar at all. cosmic-comp asks clients to
  draw their own (client-side decorations), Gio never asks for server-side
  ones, and Isotope System, unlike Isotope Welcome, draws no `guikit.TitleBar`.
  The window cannot be moved or closed with the mouse.

## Goals

- The window is the app. No fake desktop, no glow, no floating card, no
  shadow drawn by the app. The content fills the window at every size.
- It opens as a normal window of a sensible size, never larger than the
  screen, and can be resized, maximized and tiled like any other.
- On COSMIC: a header bar in the style of COSMIC Settings (title, window
  buttons on the side COSMIC uses, a button that shows and hides the
  navigation list on a narrow window), and the colours, accent, corner radii
  and dark or light mode of the running COSMIC theme. A change of theme while
  the window is open applies without a restart.
- On sway, i3 and niri: the window manager draws the title bar (or none, as
  it does for every window). The app follows the system's dark or light
  setting.
- Pages read as settings pages: a page title, sections of grouped list rows,
  buttons in the rows. Not cards on a dark canvas.
- The same features on every page. This change is about the frame and the
  look; no page loses or gains a function.

## Non-goals

- A new toolkit. Isotope System stays Go and Gio (see Approaches).
- Screen reader support. Gio windows are not read by Orca today; this change
  does not fix that.
- GTK theme support on sway, i3 and niri (reading a GTK theme's colours).
  Those desktops run Isotope's own palette; the app uses it too.
- Changing the installer or Isotope Welcome. They keep `guikit.Window`.

## Anti-goals

These would make it a failure even if it works:

- Any part of the window that looks like a desktop or a web page: a
  background picture, a glow, a centred card, a shadow inside the window.
- A COSMIC window that cannot be moved, resized from its edges or closed with
  the mouse.
- Colours that disagree with COSMIC Settings open next to it (dark app on a
  light desktop, another accent).
- Text or a control unreadable in light mode.
- A regression in the installer's or Welcome's look (their golden screenshots
  must not change).

## Approaches considered

1. **Keep Gio; a native frame and a themeable palette (chosen).** Isotope
   System gets its own frame; `guikit` gets a palette value instead of fixed
   colours, filled from the COSMIC theme on COSMIC and from Isotope's day or
   night palette elsewhere. One codebase on four desktops; the page logic and
   its tests stay. Chosen by the maintainer: "let's try with 1 first".
2. **Rewrite in libcosmic (Rust).** COSMIC Settings' own widgets, and screen
   readers through AccessKit. A rewrite of about 40 pages and their D-Bus
   clients, built through the COSMIC prebuild pipeline; months of work; on
   sway, i3 and niri it would look like a COSMIC app. Kept as a later option
   if 1 is not native enough.
3. **GTK4 / libadwaita.** Looks like GNOME, which none of the four desktops
   are, and adds GTK4 to the image. Dropped.

## The design

### Window and size

- No `guikit.Window`. The frame fills the window: sidebar on the left, page
  on the right, from edge to edge.
- Opening size: 960x680 dp, cut to the output with `guikit.FitWindowSize`
  (as Isotope Welcome and the installer window do), so it is never larger
  than the screen. Minimum size 640x480 dp.
- `app.Decorated(true)` stays. On COSMIC the app draws the header bar
  itself (below) and Gio's own decoration is off, as Welcome does today.
- The sway, i3 and niri window rules that float it stay: it is a settings
  window, as COSMIC Settings floats on COSMIC.

### Header bar (COSMIC only)

When `XDG_CURRENT_DESKTOP` is COSMIC:

- A header bar of COSMIC's height for the user's density (`header_size` and
  `interface_density` in `com.system76.CosmicTk`; 40 dp for Standard, as
  `guikit.TitleBarHeight` today).
- On it: the window buttons (close, maximize, minimize) on the side
  `guikit.ControlsLeft` says, a "show navigation" button next to them
  (shown when the window is narrower than 760 dp, where the sidebar hides),
  and the page's title in the middle. Dragging the bar moves the window,
  double-click maximizes, as `guikit.TitleBar` already does.
- The edges resize the window (Gio's `system.ActionResize*` on a few dp of
  border), so it behaves like a COSMIC window.
- The header bar takes the theme's `background` colour, the same as the
  sidebar, so the top left reads as one surface, as in COSMIC Settings.

Elsewhere there is no header bar; the window manager's title bar (or none)
is what other apps get.

### Sidebar

- A plain navigation list, as in COSMIC Settings: one row per page with an
  icon and its name, the selected row highlighted in the theme's selected
  colour, no Isotope mark and no "Isotope System" heading (the title bar
  names the app).
- One small line icon per page, drawn in Go like guikit's existing icons
  (`pkg/guikit/draw.go`), in the palette's text colour, so they follow dark
  and light mode, need no SVG library or new dependency, and render the
  same in CI as on a desktop. Not loaded from the icon theme: guikit has no
  SVG reader, the CI runner has no Conflux, and the rule against copying
  Conflux's SVGs into the tree stands. (Builder check; my call.)
- The version line at the bottom ("Isotope v1.0.0 · sway") moves to About.
- On a window narrower than 760 dp the sidebar hides; on COSMIC the header
  bar's button shows it over the page; elsewhere a button at the top of the
  page does.

### Pages

- A page starts with its title in a large heading, then sections: a small
  heading (in the sentence case COSMIC uses, not the capitals of today, e.g.
  "Found on the network and USB") and a group of list rows in one rounded
  container of the theme's `primary.component` colour, rows split by a thin
  divider, as COSMIC Settings' `settings::section`.
- A row has a title, an optional subtitle in the dim colour, and its
  controls on the right (buttons, a switch, a status word). Today's
  stacked buttons under a row move to the right of the row; a row with more
  than two actions puts the rest in a "⋯" menu. This is the biggest change
  per page; the page code keeps its data and handlers.
- Buttons: the standard button is the theme's button colour; the one
  suggested action per dialog or section is the accent colour; destructive
  actions (Remove, Reset, Format) use the theme's `destructive` colour.
- Dialogs (confirmations, forms) stay inside the window as today, in the
  same palette, with a scrim of the theme's shade over the page.
- Page width: the content column is at most 720 dp wide and centred in the
  page area, as COSMIC Settings does; wider windows add margin, not wider
  rows.

### Colours: a palette that follows the desktop

`guikit` gets a `Palette` value (window, sidebar/header, component, divider,
text, dim text, accent, on-accent, destructive, warning, success, focus
ring, radii) carried by `guikit.Theme`. Today's package-level `Col*` values
stay as the installer's palette (`InstallerPalette`), so the installer and
Welcome do not change. Isotope System's widgets read the palette from the
theme, never the `Col*` values.

Where the palette comes from:

- **COSMIC:** the running theme. Mode from
  `com.system76.CosmicTheme.Mode/v1/is_dark` (user file over
  `/usr/share/cosmic`; dark when absent, COSMIC's and Isotope's default), then
  the keys of `com.system76.CosmicTheme.Dark/v1` or `.Light/v1`, again the
  user's directory over the system one. Each key is one RON file holding
  libcosmic's own types: a *container* (`background`, `primary`) is
  `(base, component, divider, on, small_widget)`, a *component*
  (`component` inside a container, and the top-level `accent`,
  `destructive`, `warning`, `success`, `button`, `accent_button`) is
  `(base, hover, pressed, selected, selected_text, focus, divider, on,
  disabled, on_disabled, border, disabled_border)`, each colour
  `(red, green, blue, alpha)` floats; `shade` is one colour;
  `corner_radii` is `(radius_0, radius_xs, radius_s, radius_m, radius_l,
  radius_xl)`, each four floats. The palette takes:

  | Palette role | COSMIC field |
  |---|---|
  | sidebar, header bar | `background.base` |
  | page | `background.base` |
  | section group (row container) | `primary.base` |
  | row hover / pressed | `primary.component.hover` / `.pressed` |
  | selected sidebar row | `background.component.selected` |
  | divider | `primary.divider` |
  | text | `primary.on` (rows), `background.on` (sidebar, page title) |
  | dim text | `on` with alpha 0.7, as libcosmic's caption text |
  | standard button | `button.base`, text `button.on` |
  | suggested button | `accent_button.base`, text `accent_button.on` |
  | accent (switches, focus ring, links) | `accent.base`, `accent.focus` |
  | destructive | `destructive.base`, text `destructive.on` |
  | warning / success text | `warning.base` / `success.base` |
  | dialog scrim | `shade` |
  | radii | `radius_s` (buttons, rows' group), `radius_m` (dialogs) |

  The reader handles only these shapes (named fields, nested parentheses,
  floats; no other RON), in `pkg/guikit` or `pkg/cosmictheme`. A key that is
  missing or does not parse falls back to the Isotope palette for that mode,
  key by key. `is_high_contrast` is not read: COSMIC already computes
  high-contrast colours into these keys, and the app uses what COSMIC
  computed. (Builder check; my call.) Test data: the `CosmicTheme.Dark`,
  `.Light` and `.Mode` directories from `/usr/share/cosmic` of an Isotope
  0.2.5 COSMIC system (or the published `cosmic-v0.2.5` image), committed
  under the reader's `testdata/`.
- **sway, i3, niri:** Isotope's palette (`theme/palette.toml`, night and
  `day.*`), compiled in by `tools/gen-theme.py` as Go constants, so a
  palette change reaches the app like every other config. Dark or light from
  the portal: `org.freedesktop.portal.Settings.ReadOne("org.freedesktop.appearance",
  "color-scheme")`, 1 dark, 2 light, 0 (no preference) dark; no portal: dark.
- **Live changes:** on COSMIC the app watches the Mode directory and the
  active theme's directory (inotify on the directories, as
  `isotope wallpaper-follow` does: cosmic-config replaces files by rename) and
  redraws with the new palette. Elsewhere it subscribes to the portal's
  `SettingChanged` for `color-scheme`.
- Contrast: every text on its background must reach 4.5:1 in both modes
  with Isotope's palette (a test, as `gen-theme.py --check` has a contrast
  floor). With a user's own COSMIC theme the app uses what COSMIC computed;
  it does not second-guess it.

### The small `isotope-system ask` windows

`isotope-system ask vpn-secret` and `ask bluetooth-file` (opened by the
tray) take the same palette, follow the same live changes and, on COSMIC,
get the header bar (title and close only), so none of them stays in the
night look on a light COSMIC desktop. They keep their size and content.
(Builder check; my call.)

### Fonts

The interface font and size of COSMIC (`interface_font` in
`com.system76.CosmicTk`, "Inter Variable" on Isotope) when the family is one
guikit embeds; otherwise guikit's Inter. Urbanist (the installer's display
face) is not used in Isotope System: page titles are Inter, semibold, as
COSMIC's.

## Tests

- New golden screenshots for every Isotope System page and the two `ask`
  windows, in both modes, at the
  opening size and at a narrow size (sidebar hidden), in the same style as
  today's `cmd/isotope-system/testdata/*.png`, rewritten with `-update`. The
  installer's and Welcome's goldens must not change (CI fails if they do).
- The RON reader: the real theme files (dark and light), a missing key, a
  malformed key, the user's directory over the system one.
- The palette choice: COSMIC dark, COSMIC light, a user theme, sway with
  each portal answer, no portal.
- The contrast floor on the Isotope palette in both modes.
- The frame: no pixel outside the window's content colours at any size (no
  glow, no shadow), the sidebar hides below 760 dp, the header bar only on
  COSMIC.
- The user guide's screenshots (`site/guide/*.md` pointing at the goldens)
  follow automatically; any label that moves (the version line) is updated
  in the guide in the same change.
- Manual, on the design note's list: a real COSMIC desktop (move, resize,
  maximize, theme switch while open, light mode) and sway, niri and i3
  (dark and light).

## Calls made after the builder check

These were settled without the maintainer; check them while reading:

- Sidebar icons are drawn in Go, not loaded from the icon theme.
- The COSMIC field for each palette role (the table above).
- `is_high_contrast` is not read; COSMIC's computed colours are trusted.
- The `ask` windows get the new look too.

## What is left to the builder

- The icon drawn for each page.
- How the "⋯" overflow menu is drawn, within the palette.
- Splitting the work into phases (for example: frame and palette first with
  pages unchanged, then the pages one by one), as long as each merged phase
  ships no fake desktop and passes its goldens.

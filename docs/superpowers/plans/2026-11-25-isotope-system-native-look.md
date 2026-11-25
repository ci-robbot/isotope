# Isotope System native look: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Isotope System opens as an ordinary window of its desktop: no fake
desktop or floating card, COSMIC's header bar and theme colours on COSMIC,
Isotope's day or night palette elsewhere, a plain navigation sidebar and
settings-style pages.

**Architecture:** `pkg/cosmictheme` reads the COSMIC theme files (a small
RON subset, no Gio). `pkg/guikit` gets a `Palette` value on `Theme`; every
guikit widget draws from it, and a nil palette is today's installer colours,
so the installer and Welcome do not change. Isotope System picks its palette
at start (COSMIC theme, or Isotope day/night from the portal), follows
changes live, and lays out its own frame: header bar (COSMIC only), sidebar,
page.

**Tech Stack:** Go, Gio v0.10.3, godbus, golang.org/x/sys/unix (inotify).

**Spec:** `docs/superpowers/specs/2026-11-25-isotope-system-native-look-design.md`

## Global Constraints

- The installer (`cmd/isotope-install-gui`) and Welcome (`cmd/isotope-welcome`) goldens must not change.
- No new module dependency.
- COSMIC theme keys live in `com.system76.CosmicTheme.{Dark,Light}/v2/<key>`, colours as `"#RRGGBBAA"` strings (checked against files written by `tools/isotope-cosmic-theme`; the spec said `v1` and float tuples, which is the older format). The mode is `com.system76.CosmicTheme.Mode/v1/is_dark`. User dir `$XDG_CONFIG_HOME/cosmic` (or `~/.config/cosmic`) over `/usr/share/cosmic`, key by key.
- Opening size 960x680 dp through `guikit.FitWindowSize`; minimum 640x480 dp.
- Sidebar hides below 760 dp of window width.
- Page content column at most 720 dp, centred.
- Section headings in sentence case; the `.po` catalogs keep their translations (`go run ./tools/i18n check` passes).
- Every text on its background in `IsotopePalette(dark)` and `IsotopePalette(light)` reaches 4.5:1 (dim text 4.5:1 on the window and on a group).

## Review Focus

1. A COSMIC user theme with only some keys written (COSMIC Settings writes only what changed): the missing keys come from the system dir, then Isotope's palette; never a black or transparent fill.
2. Translucent COSMIC colours (`button.base` is `#9F9F9B40`): composed over the surface they sit on, not painted with alpha in linear light.
3. Light mode: no text or icon keeps a night colour (a `guikit.Col*` left in Isotope System).
4. A theme switch while a dialog is open: the dialog redraws in the new palette, no crash on a palette swap mid-frame.
5. A narrow window (640 dp): the sidebar hides, the toggle shows it over the page, picking a page hides it again.

Each has its test in the owning task.

---

## Phase 1: frame, palette, header bar, sidebar

### Task 1: `pkg/cosmictheme`, the COSMIC theme reader

**Files:**
- Create: `pkg/cosmictheme/ron.go`, `pkg/cosmictheme/theme.go`, `pkg/cosmictheme/theme_test.go`
- Create: `pkg/cosmictheme/testdata/usr/share/cosmic/...` (the keys below, Dark and Light, from `tools/isotope-cosmic-theme`'s output, plus `Mode/v1/is_dark`)

**Interfaces:**
- Produces:
  - `type Component struct{ Base, Hover, Pressed, Selected, SelectedText, Focus, Divider, On, Disabled, OnDisabled, Border color.NRGBA }`
  - `type Container struct{ Base color.NRGBA; Component Component; Divider, On color.NRGBA }`
  - `type Theme struct{ Dark bool; Background, Primary Container; Accent, AccentButton, Button, Destructive, Warning, Success Component; Shade color.NRGBA; RadiusS, RadiusM float32; Have map[string]bool }`; `Have[key]` is true for each key read.
  - `func Dirs(getenv func(string) string, home string) []string`: user dir then `/usr/share/cosmic`.
  - `func Load(dirs []string, read func(string) ([]byte, error)) Theme`
  - `func IsDark(dirs []string, read func(string) ([]byte, error)) bool` (absent: true)
  - `func WatchDirs(dirs []string) []string`: the Mode dir and both theme dirs under the first (user) dir.

- [ ] Step 1: tests `TestLoadIsotopeDark`, `TestLoadIsotopeLight` (accent `#E9E7C1FF` / light `primary.base #FFFFFFFF`, `button.base` alpha 0x40, `RadiusS` 8, `RadiusM` 16), `TestUserKeyOverridesSystem` (user `accent` only; other keys from the system dir), `TestMalformedKeyIsSkipped` (`Have["accent"]` false, others true), `TestIsDarkDefaultsToTrue`, `TestParseLegacyFloatColour` (`(red: 1.0, green: 0.5, blue: 0.0, alpha: 1.0)`).
- [ ] Step 2: run `go test ./pkg/cosmictheme/` → fails (no package).
- [ ] Step 3: implement a recursive-descent reader for: `( name: value, ... )`, `( v, v )`, `"string"`, numbers, `true/false`, `Some(x)`, `None`, trailing commas. Colours: a `"#RRGGBB[AA]"` string or a struct of float `red/green/blue/alpha`.
- [ ] Step 4: tests pass.
- [ ] Step 5: commit `feat(cosmictheme): read the running COSMIC theme`.

### Task 2: `guikit.Palette`, and guikit drawn from it

**Files:**
- Modify: `pkg/guikit/palette.go`, `widgets.go`, `window.go`, `nav.go`, `titlebar.go`, `draw.go`, `text.go`
- Create: `pkg/guikit/isotope_palette.go` (generated values), `pkg/guikit/palette_test.go`
- Modify: `tools/gen-theme.py` (a RULES entry for `isotope_palette.go`)

**Interfaces:**
- Produces:
  - `type Palette struct{ Dark bool; Window, Sidebar, Group, Hover, Selected, Divider, Field, Footer, Text, Text2, Dim, Ring, Disabled, OnDisabled, Button, OnButton, ButtonBorder, Suggested, OnSuggested, Accent, OnAccent, Focus, Link, Destructive, OnDestructive, Error, ErrorText, Warning, Success, Check, KeyBox, Shade color.NRGBA; RadiusS, RadiusM float32 }`
  - `var InstallerPalette Palette` (exactly today's `Col*` and derived values)
  - `func IsotopePalette(dark bool) Palette` (from `theme/palette.toml` night/day)
  - `func CosmicPalette(t cosmictheme.Theme) Palette` (spec table; missing key → `IsotopePalette(t.Dark)` for that role; translucent colours composed with `Over` on their surface)
  - `Theme.Pal *Palette`; `func (th *Theme) Colors() *Palette` (nil → `&InstallerPalette`)
  - `Theme.TitleStyle Style` (zero → `StTitle`) used by `Heading` and `Confirm`'s title.
  - Theme methods drawing with the palette: `th.Radio`, `th.Checkbox`, `th.Panel`, `th.Spinner`, `th.ProgressBar`, `th.WarnBox` (= `Warning`). Free functions `Radio`, `Checkbox`, `Panel`, `Spinner`, `ProgressBar` keep their signatures and use `InstallerPalette`.

- [ ] Step 1: tests `TestInstallerPaletteIsTheColValues`, `TestIsotopePaletteContrast` (Text, Text2, Dim on Window and Group; OnSuggested on Suggested; OnDestructive on Destructive; both modes, ≥ 4.5), `TestCosmicPaletteFromIsotopeTheme` (dark: Window `#141411`, Group `#1D1D1A`, Suggested `#E9E7C1`; Button opaque), `TestCosmicPaletteFallsBackPerKey` (theme with only `accent` → rest equals `IsotopePalette(true)` except Accent).
- [ ] Step 2: run → fail.
- [ ] Step 3: implement; replace every `Col*` read inside guikit widgets with `th.Colors()`.
- [ ] Step 4: `go test ./pkg/guikit/` passes; installer and Welcome screenshot tests pass **without** `-update`; `python3 tools/gen-theme.py --check` passes.
- [ ] Step 5: commit `feat(guikit): draw from a palette value`.

### Task 3: native frame widgets in guikit

**Files:**
- Create: `pkg/guikit/native.go` (HeaderBar, NavList), `pkg/guikit/icons.go` (page icons), `pkg/guikit/native_test.go`

**Interfaces:**
- Produces:
  - `func HeaderHeight(getenv func(string) string, home string, read func(string) ([]byte, error)) float32`: `com.system76.CosmicTk/v1/header_size` Compact 36, Standard 40, Spacious 48 (default 40).
  - `type HeaderBar struct{ Title string; Left, Maximized, ShowNavButton, CloseOnly bool; Height float32 }`; `func (h *HeaderBar) Update(gtx) (system.Action, bool /*nav toggled*/)`; `func (h *HeaderBar) Layout(gtx, th) layout.Dimensions`.
  - `type NavEntry struct{ Name string; Icon IconName }`; `type NavList struct{ Items []NavEntry; Current int }` with `Clicked(gtx) int`, `Row(i) *widget.Clickable`, `func (th *Theme) NavList(gtx, *NavList) layout.Dimensions` (240 dp wide, rows 40 dp, icon 16 dp, selected row `Selected` fill and `Text`, others `Text`, hover `Hover`).
  - `type IconName string` and `func Icon(gtx, name IconName, size float32, c color.NRGBA) layout.Dimensions` for: apps, updates, desktop, extensions, backup, security, network, bluetooth, printers, users, disks, compat, about, menu.

- [ ] Step 1: tests `TestHeaderHeightFromDensity`, `TestNavListClickPicksRow`, `TestEveryIconDraws` (non-empty dims, ops recorded, for every IconName).
- [ ] Step 2: run → fail.
- [ ] Step 3: implement.
- [ ] Step 4: pass.
- [ ] Step 5: commit `feat(guikit): a header bar and a navigation list for settings windows`.

### Task 4: Isotope System in the native frame

**Files:**
- Create: `cmd/isotope-system/theme.go` (palette source + live updates), `cmd/isotope-system/theme_test.go`
- Modify: `cmd/isotope-system/ui.go`, `main.go`, `ask.go`, and every file that reads `guikit.Col*` (`page_apps.go`, `fprint_section.go`, `page_printers.go`, `page_network.go`, `firewall.go`, `shares.go`, `page_users.go`, `page_updates.go`, `page_other.go`, `page_disks.go`, `page_backup.go`, `keydialog.go`, `backup_setup.go`, `apps_test.go`, `dialog.go`)
- Modify: the 40 `u.caps(...)` strings to sentence case; `pkg/i18n/po/isotope-system/*.po` msgids (translations sentence-cased the same way).

**Interfaces:**
- Consumes: Tasks 1–3.
- Produces:
  - `type paletteSource struct{ cosmic bool; dirs []string; read func(string)([]byte,error); scheme func() (uint32, bool) }`, `func (s paletteSource) palette() guikit.Palette`
  - `func watchCosmic(dirs []string, changed func()) error` (inotify, re-adds a lost watch), `func watchPortal(conn *dbus.Conn, changed func(uint32))`
  - `func (u *UI) SetPalette(p guikit.Palette)` (any goroutine; applied at the next frame)
  - `UI.bar *guikit.HeaderBar` (nil off COSMIC), `UI.navOpen bool`, `Hooks.Window func(system.Action)`.

- [ ] Step 1: tests `TestPaletteSourceCosmic` (testdata dirs → Window `#141411`), `TestPaletteSourcePortal` (scheme 1 dark, 2 light, 0 dark, none dark), `TestNoColLeftInIsotopeSystem` is **not** written (source-parsing tests are rejected here); instead `TestLightPaletteHasNoNightPixels` renders Updates in light mode and checks no pixel equals `ColWindow`/`ColSidebar`/`ColDesk*`. `TestFrameHasNoDesktop` (960x680 render: corner pixels equal the palette's Sidebar/Window, not `ColDesk*`). `TestNarrowHidesSidebar` (640 wide: no nav row at x<240; toggle opens it; a pick closes it). `TestHeaderBarOnlyOnCosmic`. `TestPaletteSwapUnderDialog` (SetPalette while a dialog is up, two frames, no panic, scrim colour from new Shade).
- [ ] Step 2: run → fail.
- [ ] Step 3: implement: drop `guikit.Window`; header bar on COSMIC with nav toggle below 760 dp; `u.scrolled` centres a 720 dp column; `Theme.TitleStyle` Inter SemiBold 24; `caps` draws `StBody600` in `Text`; `panel` is a `Group` box with `RadiusS`; main.go sizes the window with `FitWindowSize(…, 960, 680)` (COSMIC screen from `cosmic-randr list --kdl`), `app.MinSize(640,480)`, `app.Decorated(!cosmic)`, forwards `ConfigEvent` maximized state and the bar's actions to `win.Perform`. `ask` windows: same palette and live updates; on COSMIC a close-only bar.
- [ ] Step 4: `go test ./cmd/isotope-system/` with `-update` for the goldens, then without; `go run ./tools/i18n check`.
- [ ] Step 5: commit `feat(system): open as a native window that follows the desktop's theme`.

### Task 5: goldens for both modes, docs

**Files:**
- Create: `cmd/isotope-system/native_screenshot_test.go`
- Modify: `cmd/isotope-system/testdata/*.png` (rewritten), `site/guide/*.md` lines naming the sidebar version line, AGENTS.md row for Isotope System if it describes the frame.

- [ ] Step 1: `TestNativePages`: every `Pages` entry × {dark, light} at 960x680 → `testdata/native-<page>-<mode>.png`; `native-narrow-dark.png` at 640x480; `native-cosmic-header-light.png`.
- [ ] Step 2: write them with `-update`; look at them.
- [ ] Step 3: re-run without `-update`: pass.
- [ ] Step 4: commit `test(system): goldens of the native window in both modes`.

## Phase 2: settings rows (one PR per few pages)

### Task 6: grouped rows with dividers and controls on the right

**Files:**
- Modify: `cmd/isotope-system/ui.go` (`group(rows ...layout.Widget)`, `actionRow(title, sub string, actions []guikit.Action)`, an overflow `⋯` menu for more than two actions), then each page file in turn: printers, network, bluetooth, users, disks, backup, updates, extensions, desktop, security, compat, apps, about.

- [ ] Step 1 (per page): adapt the page's golden test expectations; replace "panel with rows and a gap of 12, buttons under the row" by `u.group(...)` with `u.actionRow(...)`.
- [ ] Step 2: `go test ./cmd/isotope-system/ -update`, look at the PNG, re-run without.
- [ ] Step 3: commit per page group `feat(system): settings rows on the <page> page`.

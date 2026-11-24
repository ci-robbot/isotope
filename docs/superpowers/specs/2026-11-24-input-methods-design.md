# Input methods (fcitx5 extension)

Issue kairos-community/isotope#136. Status: implemented on
`feat/input-methods`.

## Intent

A user can type Chinese, Japanese and Korean (and other scripts that need an
input method) on all four desktops: sway, i3, niri and COSMIC, in the
image's own apps and in Flatpak apps. It is an extension,
`isotope ext install fcitx5`, so a system without it is exactly as before:
the image only gets hooks that do nothing while `/usr/bin/fcitx5` is
missing.

## Findings

- The Wayland compositors: sway 1.10.1 has `zwp_input_method_v2` with
  popups (`sway/input/text_input.c`, `text_input_popup.h`); niri uses
  smithay's `InputMethodHandler` with popup surfaces
  (`src/handlers/mod.rs`); cosmic-comp creates `InputMethodManagerState`
  and `VirtualKeyboardManagerState` for every client that is not sandboxed,
  and `TextInputManagerState` (text-input-v3) for all
  (`src/state.rs`). So a host input method that speaks
  input-method-v2 works on all three, and apps that speak text-input-v3
  reach it through the compositor.
- i3 is X11 (XLibre): no compositor in between, so the input method must be
  an XIM server, or apps must load a toolkit IM module that talks to it
  directly.
- The image's toolkits: GTK 3 only (`gtk3`, Wayland only, on sway, niri and
  COSMIC; `gtk3-x11` on i3). No GTK 4, no Qt in the image except Noctalia's
  Qt 6 on niri (Wayland). libcosmic/iced (COSMIC), foot (sway, niri) and
  Isotope's Gio apps use text-input-v3 on Wayland. GTK 3 on Wayland uses its
  built-in `wayland` IM module (text-input-v3) unless `GTK_IM_MODULE` says
  otherwise. GTK 3's `GTK_IM_MODULE` is a colon-separated list
  (`gtkimmodule.c`, `g_strsplit (envvar, ":", 0)`), so `fcitx:xim` takes the
  first that exists.
- Flatpak apps bring their own toolkits. On Wayland, GTK 3/4 and Qt 6 use
  text-input-v3 through the compositor; on X11 they need XIM or their
  runtime's fcitx IM module, which talks to `org.freedesktop.portal.Fcitx`
  on the session bus (fcitx5's D-Bus frontend owns it).
- The Hadron base has neither libstdc++ nor GLib nor any drawing library;
  every desktop image adds libstdc++, libgcc_s, expat, libxml2 and GTK 3's
  stack (GLib, pixman, libpng, FreeType, HarfBuzz, FriBidi, Fontconfig,
  gdk-pixbuf), Wayland's client libraries and libxkbcommon. Cairo differs:
  sway, niri and COSMIC build it without XCB, i3 with Xlib and XCB. Only i3
  has the xcb utility libraries and libxkbcommon-x11.
- fcitx5 5.1.22 and later need librsvg for the classic UI (SVG themes),
  and fcitx5-chinese-addons 5.1.15 needs fcitx5 5.1.22. librsvg is in the
  niri image only.
- Kairos merges only `/usr/bin`, `/usr/sbin`, `/usr/lib`, `/usr/share`,
  `/usr/include` and `/usr/src`. anthy reads its dictionary path from
  `$sysconfdir/anthy-unicode.conf`; fcitx5 installs an XDG autostart file
  and an Xwayland session script in `/etc`.
- fcitx5 has no setting UI without Qt (fcitx5-configtool is Qt). Without a
  profile it builds a default group from the locale: a user with an English
  locale gets only the keyboard. Its D-Bus controller
  (`org.fcitx.Fcitx.Controller1` on `/controller`) has
  `InputMethodGroupInfo`, `SetInputMethodGroupInfo` (which saves the
  profile) and `AvailableInputMethods`.

## Decisions

### 1. fcitx5, not ibus

ibus's Wayland frontend speaks `zwp_input_method_v1`, which only GNOME's
and KDE's compositors offer; sway, niri and cosmic-comp offer
`zwp_input_method_v2`, which fcitx5's `waylandim` speaks. fcitx5 also has
an XIM server (xcb-imdkit) for i3 and for X11 apps under Xwayland, and the
D-Bus frontend Flatpak runtimes' fcitx modules use. One framework covers
the four desktops and both kinds of apps.

### 2. Engines: Pinyin, Anthy, Hangul

- Chinese: fcitx5-chinese-addons (Pinyin, Shuangpin, the table IMs) on
  libime, with libime's language model and dictionaries built from the
  upstream `_dict` tarballs (no download at build time). Without cloud
  Pinyin (it sends what you type to a web service), OpenCC (chttrans keeps
  its built-in table) and the Qt GUI.
- Japanese: fcitx5-anthy on anthy-unicode. Mozc is the better engine but
  builds only with Bazel; it can be a later extension.
- Korean: fcitx5-hangul on libhangul.

### 3. A self-contained extension

Everything the extension needs that is not on every desktop image lives in
`/usr/lib/isotope-ext/fcitx5` behind a RUNPATH: Cairo with XCB (so one
build works on the Wayland desktops, whose Cairo has none, and on i3),
Pango on it, librsvg (built with Rust and cargo-c, as the root Dockerfile's
mesa-rust stage builds Rust), libxcb and the xcb utilities, libX11 (only for
libxkbfile), libxkbfile, libxkbcommon-x11, xcb-imdkit and fcitx5's own
libraries and addons. The libraries the extension takes from the image are
listed in `extensions/fcitx5/image-libs.txt`; the build refuses any other
NEEDED entry, and `check-input-methods.sh IMAGE` checks that every desktop
image has each one, so an image change that drops one goes red in the image
job, not on a user's machine. The image's libraries are built in the
extension's build stage at the image's versions (same ARGs and Renovate
rules), to link against and as the test stage's stand-in.

anthy's configuration file is `/usr/lib/isotope-ext/fcitx5/etc/anthy-unicode.conf`
(its sysconfdir), the ISO 639-3 names are in
`/usr/share/isotope-ext/fcitx5/iso-codes` (COSMIC's image has iso-codes at
the standard path, which an extension must not overlay), nothing goes to
`/etc`, and the build refuses a program not named `fcitx5*` or
`anthy-*unicode*` and anything in `/usr/lib` outside its directory (Cairo's
and Pango's tools are in the images).

### 4. Starting it: the session, only when installed

fcitx5 must start inside the session (it needs `WAYLAND_DISPLAY` or
`DISPLAY`). The extension cannot add autostart files (`/etc/xdg/autostart`
is not merged, and sway, i3 and niri do not read it). So every session runs
`isotope input-method start`: `config.d/60-input-method.conf` on sway,
`config.d/50-input-method.conf` on i3, a `spawn-at-startup` line on niri and
`/etc/xdg/autostart/isotope-input-method.desktop` on COSMIC. Without
`/usr/bin/fcitx5` it exits 0 at once and prints nothing. With it, it gives
a user who has no fcitx5 profile one, hands the IM variables to
`dbus-update-activation-environment --systemd` (the portals' file chooser
is D-Bus activated), and runs `fcitx5 -d -r`.

The first profile: the keyboard layout (`XKB_DEFAULT_LAYOUT` and
`_VARIANT`, else `/etc/isotope/keyboard`, else `us`; fcitx5 on
input-method-v2 sends keys with its own keymap, so this must be the
session's), then Pinyin, Anthy or Hangul for a `zh`, `ja` or `ko` locale,
and all three otherwise: someone who installs the extension wants at least
one, and the tray icon switches between them.

### 5. The variables: only when merged

`/etc/profile.d/isotope-input-method.sh` sets nothing unless
`/usr/bin/fcitx5` is executable. With it:

| | sway, niri, COSMIC (Wayland) | i3 (X11) |
|---|---|---|
| `XMODIFIERS` | `@im=fcitx` (X11 apps under Xwayland, XIM) | `@im=fcitx` |
| `GTK_IM_MODULE` | unset: GTK uses text-input-v3 | `fcitx:xim`: a Flatpak runtime's fcitx module, else XIM (the image's GTK 3 has no fcitx module) |
| `QT_IM_MODULE` | unset | `fcitx` |
| `QT_IM_MODULES` | `wayland;fcitx` (Qt 6.8.2+: text-input-v3 first) | unset |
| `SDL_IM_MODULE` | `fcitx` | `fcitx` |

A value already set (from `~/.profile`) wins, so a user can choose another
framework. The file is sourced by login shells, and by sway's and i3's
`start-desktop` and `isotope-niri-session` themselves (ly sources
`/etc/profile` only for sh and bash users; niri-session's `$SHELL -l` skips
it for zsh). The variant is read from `SESSION=` in `/etc/isotope/session`.

No GTK IM module ships: the Wayland desktops do not need one, and on i3 XIM
serves the image's GTK 3 (the file chooser of the portals). A GTK module
would need a GTK build in the extension and `GTK_IM_MODULE_FILE`, which
would also reach Flatpak apps and hide their runtime's modules.

### 6. Choosing the input methods: `isotope input-method`

`isotope input-method` (`status`, `set NAME...`) is the setting, since no
configuration tool ships. While fcitx5 runs, `set` calls
`SetInputMethodGroupInfo` on the current group (fcitx5 saves the profile;
writing the file under a running fcitx5 would be overwritten when it
exits), checking names against `AvailableInputMethods`; otherwise it writes
`$XDG_CONFIG_HOME/fcitx5/profile` by a rename, checking names against
`/usr/share/fcitx5/inputmethod/*.conf`. The keyboard entry stays first.
Without the extension both say how to install it. An Isotope System page is
not part of this change: the extension's catalog entry, "Input methods",
is the Install line (Extensions page, and the Apps page's search).

### 7. Not on the agent or the server

The agent cannot install extensions (no `isotope-ext`), so Dockerfile.agent
removes the i3 line and the profile.d file and checks that no session line
or variable is left. The server has no session; `server-config` takes
neither.

## Non-goals

On-screen keyboard; handwriting; Mozc; a graphical configuration tool (Qt);
cloud Pinyin; GTK or Qt IM modules in the image.

## Tests

- The extension's Dockerfile: the build stage refuses a NEEDED library that
  is neither in `image-libs.txt` nor the extension's, a missing RUNPATH, a
  program or `/usr/lib` file outside its names, a leftover testing addon,
  and a component without a licence. The `test` stage, on Hadron: no file
  over the base, nothing outside the merged hierarchies, every library and
  symbol found by ldd (with a stand-in of the image's libraries), anthy's
  dictionary path, libime's and libhangul's data, then fcitx5 on a session
  bus: it owns `org.fcitx.Fcitx5`, lists keyboard-us, pinyin, shuangpin,
  anthy and hangul, and has the classicui, wayland, waylandim, xcb, xim,
  dbusfrontend and notificationitem addons.
- `cmd/isotope/inputmethod_test.go`: `start` without fcitx5 does nothing;
  the first profile per locale and layout; an existing profile is kept;
  only set variables go to D-Bus; `set` offline, its refusals, and over a
  fake controller on a private `dbus-daemon`; `status`; a profile in the
  shape fcitx5 writes.
- `test/input-method/check-input-methods.sh --repo` (CI): the profile.d file
  run in Alpine (nothing without fcitx5; the Wayland and the i3 sets; a
  user's value wins), each session's line, the CLI, the extension's files,
  the agent's removals, the server left out.
- `check-input-methods.sh IMAGE` (desktop-images.yml, release.yml, also the
  server and the agent): the hooks, that they do nothing without the
  extension, every library of `image-libs.txt` in each desktop image, and
  none of the extension's paths taken.

## Manual checks (a booted desktop with the extension)

1. On each of sway, i3, niri and COSMIC, after `isotope ext install fcitx5`
   and a new login: the fcitx5 icon is in the tray; `isotope input-method`
   says running and lists the keyboard and the three engines.
2. Ctrl+Space in foot (sway, niri), cosmic-term (COSMIC) and st (i3),
   then Pinyin `nihao` gives 你好 with the candidate window at the cursor.
3. Anthy: `nihongo` and Space gives 日本語; Hangul: `gksrmf` gives 한글.
4. A Flatpak GTK app (gedit or Firefox) and a Flatpak Qt app, on a Wayland
   desktop and on i3; the portal file chooser on i3 (XIM).
5. An X11 app under Xwayland on sway (XIM through `XMODIFIERS`).
6. `isotope input-method set hangul` while running: the tray menu changes
   at once and the choice survives a new login.
7. `isotope ext remove fcitx5` and a new login: no fcitx5, and `env` has
   none of the IM variables.

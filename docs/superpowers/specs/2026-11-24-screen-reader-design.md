# Screen reader (extension) — design

Issue #136 item: "Screen reader: Orca with speech and the AT-SPI stack, as an
Isotope extension."

## Intent

A blind or low-vision user can turn on a screen reader with the key GNOME
uses (Super+Alt+S) or from the Isotope menu, and hear the desktop and their
apps read aloud. The screen reader is an extension, so the image does not
grow for everyone.

## What the image had (measured in the Dockerfile at 291fc6d)

- No Python, no PyGObject, no GObject Introspection anywhere (every stage
  builds with introspection off), so no typelib.
- at-spi2-core 2.54 (the accessibility bus, the registry, libatspi, built
  without X11) only in `i3-rootfs`. COSMIC, niri and sway had no
  accessibility bus at all.
- GTK 3.24.43 Wayland-only on sway, COSMIC and niri (`gtk3`), X11 on i3
  (`gtk3-x11`); ATK 2.38. The Wayland GTK 3 is not linked to the ATK bridge.
- libpipewire on every desktop; libpulse only on COSMIC; ALSA's default is
  not routed to PipeWire.
- Hadron's base has no Python (only its debug image); its toolchain has one.

## Where it works

Orca 46+ reads the keyboard through libatspi's `AtspiDevice`. On Wayland,
from at-spi2-core 2.56, that is the a11y-manager device, which asks the
compositor's `org.freedesktop.a11y.KeyboardMonitor` (well-known name
`org.freedesktop.a11y.Manager`) to grab Orca's keys.

| Desktop | Keyboard monitor | Decision |
|---|---|---|
| COSMIC (epoch 1.11.0) | cosmic-comp `src/dbus/a11y_keyboard_monitor.rs`, allows `org.gnome.Orca.KeyboardMonitor` | Ship. Super+Alt+S is already COSMIC's `System(ScreenReader)`; cosmic-session starts `/usr/bin/orca` when `ScreenReaderEnabled` turns on |
| niri 26.04 | `src/dbus/freedesktop_a11y.rs` (default `dbus` feature, which Isotope builds); niri's own UI is exposed through AccessKit | Ship. Isotope binds Super+Alt+S and restores at login |
| sway 1.10 | none | Not shipped. Orca would read focus changes but take no command (no Orca key, no flat review, no browse mode); claiming support would be false |
| i3 (X11) | not needed on X11, but the image's at-spi2-core has no X11 device and the registry has no X11 backend | Not shipped. Needs an X11 at-spi2-core (libXtst, libXi) in the i3 image: see Open questions |

The switch refuses on sway and i3 with a sentence; the menu shows no entry
and no key is bound there.

## Decisions

1. **Image: the accessibility bus on COSMIC and niri.** `COPY --from=at-spi2-core`
   in `cosmic-rootfs` and `niri-rootfs` (the same stage i3 copies). It is the
   bus every accessible app talks to, and COSMIC's own Super+Alt+S, its
   Accessibility page and cosmic-session's Orca launch need it even before
   Orca is installed. Putting it in the image, not the extension, means the
   extension never carries a file that i3's image already has (`isotope ext`
   refuses that). No stage the COSMIC prebuilt halves reach changes: the four
   keys are unchanged.
2. **Extension: everything Python and speech, in one private prefix.**
   `/usr/lib/isotope-ext/screen-reader` holds CPython 3.12 (shared, RUNPATH),
   gobject-introspection 1.82 (the series of the image's GLib 2.82), PyGObject
   3.48 (the last on libgirepository-1.0), libatspi 2.58 with its typelibs and
   Python overrides, dasbus, libsndfile, eSpeak NG 1.52, speech-dispatcher
   0.12.1 and Orca 50.3. Everything is built from source on Hadron's
   toolchain, as the modemmanager extension is. `/usr/bin/orca` is a link to
   Orca's launcher, which cosmic-session runs; Orca's own `orca.service` is in
   `/usr/lib/systemd/user`, which the switch starts on niri.
3. **GTK's typelibs without building GTK.** Orca imports Gtk and Gdk 3. The
   GTK stack is the image's; building it again in the extension only to run
   the scanner would double the build. The typelibs are compiled with
   `g-ir-compiler` from gtk-rs/gir-files (a pinned tag), which name the
   libraries by soname. GTK 3 is ABI-frozen; a function a newer release added
   that the image's library lacks fails only when called. Atspi and Atk come
   from at-spi2-core's own scanner run.
4. **A contract with the image.** `extensions/screen-reader/image-libs.txt`
   lists what the extension takes from the image: `linked` (GLib, libffi,
   zlib, libdbus, libpipewire) and `typelib` (GTK's stack). The build stage
   fails on any other library; the test stage uses the build's copies of
   `linked` as stand-ins on the Hadron base; `check-screen-reader.sh IMAGE`
   proves COSMIC and niri have all of them. The `--repo` check holds GLib,
   PCRE2, PipeWire and libsndfile to the image's versions.
5. **libatspi 2.58 in the extension, 2.54 bus in the image.** The client side
   needs 2.56+ for the a11y-manager device; the bus protocol has not changed,
   so the image's bus stays.
6. **Speech through PipeWire.** speech-dispatcher 0.12 has a PipeWire output,
   and every desktop has libpipewire; ALSA's default would open the card
   under PipeWire, and libpulse is only on COSMIC. `AudioOutputMethod
   "pipewire"` and `DefaultModule espeak-ng` are set in its read-only
   configuration; `spd-conf` writes the user's override.
7. **One setting.** The switch uses the bus's `ScreenReaderEnabled`
   (at-spi-bus-launcher keeps it in GSettings, with GLib's keyfile backend),
   the same property COSMIC's toggles set and that Chromium and Firefox read.
   On COSMIC that is all (cosmic-session starts Orca); on niri the switch also
   starts or stops `orca.service`, and `restore` at login starts it when the
   setting is on.
8. **Bytecode once.** The sysext is read-only, so Python cannot cache
   bytecode at run time: the standard library and every package are compiled
   at build time with `--invalidation-mode unchecked-hash` (file times in the
   sysext may change).
9. **The extension's version is Orca's** (`SCREEN_READER_VERSION`).

## Size

Measured from the extension build log (`du -sh /output/usr` in the build
stage) and the `.sysext.raw` that extensions.yml makes: see the PR body.
The large parts are CPython's standard library (~35 MB uncompressed with
bytecode), eSpeak NG's data (~20 MB, every language) and Orca (~10 MB).

## Non-goals

- Braille (brltty, liblouis): not trivial on musl; Orca runs without it.
- A magnifier.
- Making Isotope's own Gio windows (Isotope System, Welcome, the installer)
  and the image's Wayland GTK 3 apps accessible: Gio has no AT-SPI support,
  and the Wayland GTK 3 has no ATK bridge.

## Tests

- The extension's `test` stage (extensions.yml): no file over the base, only
  the merged hierarchies, every ELF's libraries found (the image's as
  stand-ins), `sys.prefix`, Atspi importable, every typelib Orca loads
  resolved through GIRepository (Gtk, Gdk, Pango, PangoCairo, GdkPixbuf,
  HarfBuzz, Atk, Atspi) with `libgtk-3.so.0` as Gtk's library, dasbus,
  speechd's spawn command, Orca's package and prefix, `/usr/bin/orca`,
  `orca.service`, eSpeak NG speaking English and German into WAV files, more
  than 80 voices, speech-dispatcher's configuration.
- `test/accessibility/check-screen-reader.sh --repo` (ci.yml): the switch on
  each desktop with fake `busctl` and `systemctl` (on, off, toggle, restore,
  status, a unit the user manager has not loaded, no extension, sway and i3
  refused), keys, sheets, menu entry, the bus in the stages, version pins,
  the agent's removal.
- `check-screen-reader.sh IMAGE` (desktop-images.yml and release.yml, also
  the agent).

## Manual checks (need a booted desktop)

1. COSMIC: `isotope ext install screen-reader`, then Super+Alt+S: Orca
   speaks "Screen reader on"; Tab through COSMIC Settings and Firefox
   (Flatpak), and Orca reads each control; Super+Alt+S again stops it.
   Same from COSMIC Settings > Accessibility and from the Isotope menu.
2. COSMIC and niri: Orca's own keys work (Insert or Caps Lock as the Orca
   modifier, e.g. Orca+H for learn mode, Orca+S to toggle speech): this is
   the compositor's keyboard monitor at work.
3. niri: the same with Super+Alt+S; log out and in with it on, and Orca
   starts again; `systemctl --user status orca.service`.
4. Speech: the voice follows the system language where eSpeak NG has it;
   `spd-say hello` speaks; speech comes out of the PipeWire default sink and
   follows a Bluetooth headset.
5. Orca's preferences (Orca+Space) open, and a change survives a restart of
   Orca (GSettings keyfile backend).
6. Without the extension: Super+Alt+S on niri and the menu entry say how to
   install it.
7. sway and i3: no menu entry; `isotope-screen-reader on` refuses.
8. The size of the installed sysext.

## Open questions

- i3: worth building at-spi2-core with X11 (libXtst, libXi) for the i3 image
  so Orca works there too?
- sway: support would need a keyboard monitor in sway (none upstream).
- Should Isotope System or Isotope Welcome's Accessibility page offer the
  screen reader (install and turn on) for a first boot without sight?

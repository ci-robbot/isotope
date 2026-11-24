# Translations: gettext catalogs, Italian and Spanish

Issue: kairos-community/isotope#136, item "Translations". The maintainer asked
for Italian and Spanish first.

## Intent

Isotope System, Isotope Welcome, the installer (graphical and text) and the
tray speak the user's language. The catalogs are ordinary gettext PO files,
so a translator can use any PO editor, and adding a language is a file and
two commands. Nothing needs cgo or the host's gettext tools.

## Decisions

1. **An in-repo pure-Go runtime, `pkg/i18n`.** A message is its English
   source string (gettext's msgid). `T`, `Tf` (Sprintf of the translated
   format), `X` (msgctxt, for a short word with two meanings), `N` and `Nf`
   (ngettext) and `Mark` (gettext_noop, for package-level tables translated
   where they are shown). A missing, empty or fuzzy translation falls back
   to English. Third-party libraries were not taken: the parser, the
   Plural-Forms evaluator and the lookup are about 700 lines with tests
   (`pkg/i18n/gettext`), the tool needs the same parser and a writer, and
   the repo avoids dependencies the musl image builds have to fetch.
   `golang.org/x/text/message` uses its own catalog format, not PO, and
   `leonelquinteros/gotext` has no writer for a msgmerge.
2. **Off until a program turns it on.** Every call returns English until
   `i18n.Init()` (or `SetLanguage`) runs. The GUIs call `Init` in `main`;
   in `cmd/isotope` only the `tray` subcommand does, so the CLI, its
   `--help` and its errors stay English, even where they share a function
   with the tray. Tests that do not call it see the source strings, so no
   existing test or golden changed.
3. **Locale from the environment, in gettext's order.** `LANGUAGE` (a list,
   honoured only when the locale is set and is not C or POSIX), then the
   first of `LC_ALL`, `LC_MESSAGES`, `LANG`; each name tried as
   `ll_CC@mod`, `ll_CC`, `ll@mod`, `ll` without its encoding
   (`i18n.Candidates`). English (`en*`) or no catalog is English.
4. **Catalogs embedded with `go:embed`**, in `pkg/i18n/po/<domain>/<lang>.po`
   next to each domain's template `<domain>.pot`. Five domains, one per
   surface plus the shared widgets: `isotope-system`, `isotope-welcome`,
   `isotope-installer` (`cmd/isotope-install-gui`, `cmd/isotope-install-ui`,
   `pkg/installer`), `isotope-tray` (`cmd/isotope`) and `guikit`. At run
   time the domains of a language are merged into one table: a msgid means
   one thing everywhere, and the check fails when two domains translate it
   differently. Every binary carries every domain (about 200 KB per
   language); splitting them per binary would save little and need a
   build step. The `.pot` files are not embedded.
5. **The pipeline is Go (`tools/i18n`), not xgettext/msgmerge.** `go run
   ./tools/i18n update` walks the Go AST of each domain's directories (not
   `_test.go`, not `testdata`) for calls of the package's functions, with
   literal arguments (or `+` of literals); a non-literal argument to
   anything but `T`/`X` is an error. It writes the template (file
   references without line numbers, so moving code does not make the
   template stale; `#. TRANSLATORS:` comments from the line above; the
   `go-format` flag on `Tf`/`Nf` messages), then merges it into each
   language of `po/LINGUAS`: translations kept, new messages empty, removed
   ones dropped, a message whose plural changed marked fuzzy. Lines are not
   wrapped, so a diff is one line per change. `go run ./tools/i18n check`
   (CI, through `test/i18n/check-i18n.sh --repo`) fails on a stale template
   or catalog, an empty or fuzzy message in a `LINGUAS` language, a
   translation whose Go verbs differ from the source's (same argument, same
   verb letter; a plural form may drop the count, "Un aggiornamento"), a
   wrong number of plural forms, a msgid translated two ways, and a Go file
   outside every domain that imports `pkg/i18n` (its strings would never
   reach a catalog). `go run ./tools/i18n list PATH...` prints what a file
   marks.
6. **Format strings.** A sentence built from pieces became one format
   string. Strings with two or more verbs that a translation may reorder
   use indexed verbs (`%[1]s`); a plural whose singular drops the count
   formats only the arguments it uses (`Nf` drops the extras, so English
   "One update" works too).
7. **What is not translated.** Log lines, the `isotope` CLI and its
   `--help`, identifiers, D-Bus and JSON keys, paths, commands, product
   names, and data: keyboard layout descriptions (xkeyboard-config's),
   time zone names, a locale's own name on the Language page, app names and
   summaries from Flathub, the daemon's own error and detail texts (the
   root `isotope-updated` has no session language; its sentences show as it
   sends them), and the text of `pkg/*` packages other than `guikit` and
   `installer` (pkg/shares' notifications, pkg/flatpak's errors). The
   shortcut sheet is generated from the configs and stays English.
8. **Plural rules** come from each catalog's `Plural-Forms` header,
   evaluated by a small C-expression parser (`n`, integers, arithmetic,
   comparisons, `!`, `&&`, `||`, `?:`); Italian and Spanish are
   `nplurals=2; plural=(n != 1);`. `tools/i18n` knows the header of common
   languages for a new catalog and refuses one it does not know.
9. **The language reaches the next session through `LANG`, which already
   existed.** The Welcome page's Language step (`SetLocale`) and the
   installer (`Choice.Locale`, the live session's language) write
   `/etc/locale.conf`; `/etc/profile.d/isotope-locale.sh` exports `LANG`
   from it for the login shell; sway's and i3's `start-desktop` source it
   and push `LANG` to the user manager and D-Bus activation; niri and
   COSMIC import the login environment. So the tray (started by the
   session), Isotope Welcome (`--login`), Isotope System (from the menu, the
   tray, the launcher) start with it, and `Init` picks the catalog. The
   live installer window also takes the `lang` of the live welcome's
   `live-choices.json` (`SetLanguage`), since `sudo` may not carry `LANG`.
   The first-login Welcome does not switch language in the middle of its
   flow (the Ready page already says to log out to use it); the live
   welcome does, since it applies the live session's language at once
   (its `SessionLocale` hook also calls `SetLanguage`).
10. **Layout.** Italian and Spanish run 20-30% longer. Goldens in `it`
    (and `es` for Isotope System) hold the longer text in the widgets; see
    "Tests". Most widgets already wrap. One did not: the installer's summary
    rows cut their value to one line and gave the name column a fixed
    height, so "Attiva, passphrase a ogni avvio · chiave di ripristino
    salvata" lost its end at 1024x768. The value now takes two lines and
    the name column only a fixed width; the English goldens are unchanged.
11. **Translations are written by hand** with the glossary below.
    Register: Italian "tu" and imperative buttons ("Installa"), as GNOME and
    KDE Italian; Spanish infinitive buttons ("Instalar") and "usted" in
    sentences ("Reinicie el equipo"), as GNOME and KDE Spanish.

## Glossary

| English | Italiano | Español |
|---|---|---|
| Updates / Update | Aggiornamenti / Aggiorna | Actualizaciones / Actualizar |
| Install / Remove | Installa / Rimuovi | Instalar / Quitar |
| Cancel / Close / Back / Continue | Annulla / Chiudi / Indietro / Continua | Cancelar / Cerrar / Atrás / Continuar |
| Restart / Restart to finish | Riavvia / Riavvia per completare | Reiniciar / Reinicie para terminar |
| Settings | Impostazioni | Configuración |
| computer | computer | equipo |
| app | app (f., invariable) | aplicación |
| Extensions | Estensioni | Extensiones |
| Desktop & image | Desktop e immagine | Escritorio e imagen |
| Network / Printers / Users / Disks | Rete / Stampanti / Utenti / Dischi | Red / Impresoras / Usuarios / Discos |
| Backup / Restore | Backup / Ripristina | Copia de seguridad / Restaurar |
| Security / About / Compatibility | Sicurezza / Informazioni / Compatibilità | Seguridad / Acerca de / Compatibilidad |
| Firewall | Firewall | Cortafuegos |
| Recovery key | Chiave di ripristino | Clave de recuperación |
| passphrase / password | passphrase / password | frase de contraseña / contraseña |
| administrator | amministratore | administrador |
| encrypt / encryption | cifra / cifratura | cifrar / cifrado |
| Keyboard layout / Time zone / Language | Layout della tastiera / Fuso orario / Lingua | Distribución del teclado / Zona horaria / Idioma |
| Turn on / Turn off | Attiva / Disattiva | Activar / Desactivar |
| Connect / Disconnect / Forget / Pair | Connetti / Disconnetti / Dimentica / Associa | Conectar / Desconectar / Olvidar / Emparejar |
| Mount / Unmount / Eject / Format | Monta / Smonta / Espelli / Formatta | Montar / Desmontar / Expulsar / Formatear |
| network share | condivisione di rete | recurso compartido de red |
| live system | sistema live | sistema en vivo |
| Remote desktop | Desktop remoto | Escritorio remoto |
| fingerprint | impronta digitale | huella digital |

Never translated: Isotope, Isotope System, Isotope Welcome, Kairos, Hadron,
Flathub, Flatpak, COSMIC, sway, i3, niri, Noctalia, NetworkManager,
WireGuard, Wi-Fi, Bluetooth, USB, TPM, LUKS, Secure Boot, Trusted Boot,
key names.

## Non-goals

- Translating the `isotope` CLI, the shell menus (`isotope-menu`,
  `isotope-pick` lists), the shortcut sheet, COSMIC's own apps, the
  login screens, the boot splash and the text that the root daemon
  composes. Each is its own catalog later, if wanted.
- Right-to-left layout and scripts the interface font (Inter) does not
  cover: guikit loads no system fonts, so a CJK catalog would need fonts
  first.
- A language switch inside a running window.

## Tests

- `pkg/i18n/gettext`: the PO parser and writer (round trip, escapes,
  multi-line strings, plurals, obsolete entries, errors), Plural-Forms for
  one-, two-, three- and four-form languages, Go verb comparison.
- `pkg/i18n`: lookups, contexts, plurals with the catalog's rule, fuzzy and
  empty entries falling back, the gettext order of `LANGUAGE`, `LC_ALL`,
  `LC_MESSAGES` and `LANG`, the shipped catalogs parsing with a plural rule
  for `it` and `es`, and `LANG=it_IT.UTF-8`/`es_ES.UTF-8` (what the
  Welcome page and the installer write) selecting them.
- `tools/i18n`: update then check on a temporary tree (template contents,
  translator notes, merge keeping translations, stale template, missing
  translations, wrong verbs, conflicting translations across domains, a
  stray import, non-literal arguments).
- `cmd/isotope-install-gui`: the live window takes the language of the
  live session's choices.
- Screenshot goldens (`TestI18nScreenshots` in each package; Ubuntu 24.04's
  Mesa, as the other goldens; `-update` rewrites them):
  `cmd/isotope-system/testdata/i18n-{it,es}-updates-1100x720.png`,
  `cmd/isotope-welcome/testdata/i18n-it-{1-welcome,ready-language-changed}-*.png`,
  `cmd/isotope-install-gui/testdata/i18n-it-{1-welcome,3-account,5-encryption,7-summary}-*.png`,
  at 1440x900 and 1024x768. The English goldens are unchanged.
- `test/i18n/check-i18n.sh --repo` (CI): `tools/i18n check`, every program's
  `Init`, the LANG chain (the profile script run against a temporary
  `locale.conf`, the session files that pass `LANG` on), the Go stages'
  `COPY pkg/i18n/`. `IMAGE` (desktop-images.yml, release.yml, also the
  server and the agent): each program carries both catalogs.

## Manual checks on a booted desktop

- Choose Italiano on the Welcome page's Language step, log out and in:
  the tray menu, Isotope System and `isotope welcome` are in Italian on
  sway, i3, niri and COSMIC. Same for Español.
- On the live desktop, choose Italiano in the live welcome and install:
  the installer window is in Italian, and the installed system's first
  login is in Italian.
- A tray notification (an available update) is in the session's language.
- Nothing truncates on a 1024x768 screen at scale 1 in either language.

# User guide

Part of kairos-community/isotope#136.

## Intent

Isotope has a README for people who build it and a home page that sells it,
but nothing that tells a user how to install it and do things with it. This
adds a user guide to the website: task-oriented pages, in plain short
sentences, that cover what is on main today.

## Decisions

- **Source: one Markdown file per page in `site/guide/`.** Markdown reads on
  GitHub as well, and a feature PR can edit it next to the code. The pages
  are rendered by `site/guide.py`, which `site/build.py` calls: pure Python
  stdlib, as build.py is. A small Markdown subset (headings, paragraphs,
  one-level lists, tables, code blocks, `>` notes, `code`, **bold**, links,
  images, and `[[Super+Escape]]` for keys) is enough for task pages; a full
  Markdown library would be a dependency for features the guide does not
  use. Nested lists are not supported on purpose: a step with sub-steps is
  two lists or a table, which also reads better.
- **One page per task or per Isotope System page**, in the sidebar's order
  of the app, plus install, first login, desktops and keys, network shares,
  rollback and help. `index.md` links every page and its link order is the
  navigation order, so there is one list to keep.
- **The build fails on a broken link or image** (a missing page, a file not
  in the repository, a page `index.md` does not link). `site/check.py`
  checks the built guide with the rest of the site (local links and
  `#fragments` were already checked for every HTML file) and adds guide
  rules: `guide/index.html` exists, the home page links `guide/`, every guide
  page is linked from the guide's index, no link still points at a `.md`.
- **Screenshots are the existing goldens** (`cmd/*/testdata/*.png`), named by
  their path in the Markdown, so they render on GitHub and follow the UI
  when the goldens change. pages.yml now also runs on a change to
  `cmd/isotope-system/testdata/*.png`. No screenshots of real hardware.
- **The guide describes main**, not the latest release (v0.1.0 is older than
  most of what the guide covers). Its first page says so and points to the
  release notes. The home page's "Programs built for glibc: Coming soon"
  card is left as it is: it describes the release.
- **Labels are the code's own.** Every button, section and message named in
  the guide was read from the Go sources (`cmd/isotope-system`,
  `cmd/isotope-welcome`, `cmd/isotope-install-gui`) or the scripts
  (`isotope-menu`, the shortcut sheets), not only from AGENTS.md. Section
  titles are written in capitals, as the pages show them.
- **Help entry and About page.** The Isotope menu's Help entry opens the
  shortcut sheet (`isotope-help`), which is useful but did not lead to any
  documentation: it now prints "User guide: <URL>" under its title. The
  About page only had "What's new in this release": it now also has a
  "User guide" button that runs `xdg-open` on the guide's URL. The URL is a
  constant (`userGuideURL`) next to the shell copy, held equal by a test, the
  same way the extension catalog URL is fixed.
- **AGENTS.md rule:** a feature PR updates the guide page it affects, in the
  same change. Features still being built (fingerprint, smart cards, input
  methods, screen reader, remote desktop) are not documented until they land.

## Non-goals

- Translations.
- Screenshots of real hardware.
- Documenting the server edition and the agent image: the guide is for the
  desktop user. The server keeps the README.
- Advanced mode (installing any OCI image) and other developer paths.

## Tests

- `site/test_guide.py` (`python3 -m unittest discover -s site -p 'test_*.py'`,
  run by pages.yml before the build): the inline and block renderer, page
  order and prev/next, an orphan page and broken links failing the build,
  the real guide building and passing `check.py`, and `check.py`'s guide
  rules on a broken site.
- `python3 site/build.py --offline` then `python3 site/check.py _site`.
- `cmd/isotope-system/about_test.go`: the About footer has "User guide" (and
  the release notes when the release names them), and the click runs
  `xdg-open URL`.
- `test/extensions/check-extensions-menu.sh` (CI): the shortcut sheet's
  second line names the guide, and it is the About page's `userGuideURL`.
  (A Go test cannot read `isotope-help`: `check-go-stage-copies.sh` then
  wants the `system-build` stage to copy it.)

## Manual checks (a booted desktop)

- The About page's "User guide" button opens the browser on each variant
  (xdg-open needs a default browser; on a fresh install that is the one
  Isotope Welcome installed).
- The Isotope menu's Help entry shows the guide's address on the second line
  of the sheet, on each variant.
- Read the guide against a booted desktop once: the labels were taken from
  the code, but the flows (the order of dialogs) were read, not clicked.

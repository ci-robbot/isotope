# Isotope's website on GitHub Pages

## Why

Isotope has no page of its own. People land on the README, which is long and
written for people who already know what Kairos and Hadron are. The maintainer
asked for "a cool page, show the project, explain what it is, offer download
buttons".

The Pages site of kairos-community/isotope already serves one file:
`extensions.json`, the system extension catalog. Installed systems read it at
`https://kairos-community.github.io/isotope/extensions.json`
(`rootfs/common/etc/isotope/extensions.conf`, `cmd/isotope/ext.go`). A Pages
deployment replaces the whole site, so the website and the catalog must be one
artifact. Losing the catalog breaks `isotope ext` on every installed machine.

## Goals

- One page that says what Isotope is (an immutable, A/B Linux built on Hadron,
  by Kairos), shows the four desktops and the features that ship, and offers
  the downloads.
- Download buttons for the latest release: four desktops in the full and the vm
  line, and Isotope Server. Each says which ISO it is, its size, and links its
  checksum. A short "which one do I pick?" guide.
- No version in the source. The links come from the latest release at build
  time, with the release page as the fallback.
- One Pages build for the site and `extensions.json`, with the deploy retry.
  A site change cannot drop the catalog, and a catalog change cannot drop the
  site.

## Non-goals

- A docs site. "Docs" links to the README and docs/DEVELOPMENT.md on GitHub.
- A light theme. Isotope is dark first; the page follows the night palette.
- The agent appliance and the Trusted Boot ISOs as buttons. They are linked
  through the release page, as the README does.

## Structure

`site/` holds the source; `site/build.py` (Python standard library only)
writes the finished site to a directory, `_site/` by default.

| Section | Content |
|---|---|
| Header | The mark and wordmark, links to Desktops, Features, Download, Docs, GitHub |
| Hero | The night wallpaper ("Hydrogen"), the plain pitch, Download and GitHub buttons, the version the buttons point at. The mark plays the boot splash's sequence once: the core lights, the shell takes the light, the sixth side lifts |
| How updates work | The A/B sequence in four real steps, with the `isotope` commands |
| Desktops | COSMIC, niri + Noctalia, Sway, i3: a small drawing of how each arranges windows, who it is for, `isotope switch` |
| Features | System extensions, disk encryption (installer screenshot), backups, the firewall, the live desktop (live welcome screenshot), glibc programs (coming soon: Flatpak and Distrobox today) |
| Isotope Server | What it has, in one short block |
| Download | The picker (real computer or VM; which desktop), then the matrix of ISOs: rows are the desktops and Server, columns are the full and vm lines |
| Footer | GitHub, docs, releases, the Apache-2.0 licence (applets GPL-3.0), "Isotope, built on Hadron, by Kairos" |

Copy keeps to what AGENTS.md and the README say ships. No other distribution is
named.

## The look

- Colours: `build.py` reads `theme/palette.toml` and writes the `[night]` roles
  as CSS custom properties, so the site cannot drift from the desktop. Brand
  yellow (`brand`, `brand_glow`) only for what names Isotope and the primary
  action; the theme ivory (`accent`) for links and focus; graphite neutrals.
- Type: the faces the GUI tools ship (`pkg/guikit/fonts`): Urbanist SemiBold
  for display (the wordmark's face), Inter for text, JetBrains Mono for
  commands. All three are under the SIL Open Font Licence, which allows web
  embedding. `build.py` takes the Inter and JetBrains Mono `woff2` files from
  the release zips the Dockerfile pins (same version, same SHA-256), and falls
  back to the TTFs in `pkg/guikit/fonts` when the download fails. Urbanist is
  the TTF in `pkg/guikit/fonts` (43 kB). The licence files ship next to them.
- Structure from the mark: the hexagon and its lifted sixth side are the one
  motif. No card grid, no numbered labels except on the real update sequence.
- Screenshots: the GUI screenshot goldens in `cmd/*/testdata/`, copied at build
  time, so they follow the tools.
- One motion: the hero mark. `prefers-reduced-motion` shows it still.
- Accessibility: palette contrast (text, subtext and dim on base all pass AA for
  their sizes), visible focus rings in `accent`, skip link, alt text, the picker
  is radio buttons in fieldsets, the page works without JavaScript.

## Download links

`build.py` calls the GitHub API for `releases/latest` (with `GITHUB_TOKEN` when
set). For each `*.iso.build.json` asset of that release it reads the manifest
(`scripts/release-manifest.sh`): `desktop_flavor`, `gpu`, `trusted_boot`, `iso`,
`iso_sha256`. It keeps sway, i3, niri, cosmic (full and vm) and server, without
Trusted Boot, and joins each with its ISO asset (URL, size) and its `.sha256`
asset. If a manifest cannot be read, the asset name
`isotope-<flavor>[-vm]-<tag>-amd64.iso` is parsed instead. The page shows the
tag, the date, the size and the first characters of the SHA-256.

When the API fails, or a release has no ISO for a cell, the build still
succeeds: that button links to the release page (`releases/latest`) and says
"Get it from the release page". `site/app.js` adds nothing to the links; it only
runs the picker.

A new release must refresh the page. The release job publishes the release with
`GITHUB_TOKEN`, which starts no workflow, so `pages.yml` runs on
`workflow_run` of "Release ISOs" when it succeeds.

## The Pages pipeline

A new `.github/workflows/pages.yml`:

- Triggers: push to main touching `site/**`, `theme/palette.toml`, the
  screenshots or the workflow; `workflow_run` of "Release ISOs"; `workflow_call`
  (from extensions.yml); `workflow_dispatch`; `pull_request` touching the same
  paths (build and check only).
- Job `build`: `site/build.py --out _site`, then `scripts/extensions-catalog.sh
  _site/extensions.json`. When the catalog script fails and the caller did not
  ask for a fresh catalog (`fresh-catalog: true`, extensions.yml does), it
  fetches the live `extensions.json` and validates it, so a site deploy during a
  registry outage keeps the catalog that is there. Then `site/check.py _site`:
  every local link, image and anchor resolves, and `extensions.json` is at the
  root and is a catalog. Upload the Pages artifact (not on a pull request).
- Job `deploy`: `deploy-pages`, with the 30 s wait and one retry, in the
  `pages` concurrency group, without cancelling a deploy in progress.

extensions.yml's `catalog` job becomes a call to `pages.yml` with
`fresh-catalog: true`. It keeps `if: !cancelled() && not a pull request`, so the
catalog is still rebuilt when one extension failed. Every deploy now builds
both halves from the same commit, so whichever runs last publishes a complete
site.

## Checks

- `pages.yml` on pull requests: the build, the link check, the catalog check
  (the catalog script reads the public registry, so it works on a PR).
- `actionlint` on the changed workflows.

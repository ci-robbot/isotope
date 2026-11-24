# Isotope system extensions

Each directory here is one system extension: software that runs on the host
but is not in the image, merged over `/usr` when a user installs it with
`isotope ext` (or the Extensions entry of the Isotope menu). Isotope builds
them on the same Hadron release as the image and publishes them in its own
catalog, which `/etc/isotope/extensions.conf` names before hadron-layers. So
a name published here wins over the same name in hadron-layers.

## What a directory holds

```
extensions/<name>/
  Dockerfile       stages `default` (the files) and `test` (offline checks)
  extension.json   {"title": "...", "description": "..."} for the menu
```

- `<name>` is what users install: `isotope ext install <name>`.
- `ARG HADRON_VERSION` has no default. The workflow passes the image's pin
  (`scripts/bump-hadron.sh --current`). Build on
  `ghcr.io/kairos-io/hadron-toolchain:${HADRON_VERSION}`.
- `ARG <NAME>_VERSION=<version>` is the extension's version (`htop` reads
  `HTOP_VERSION`, `foo-bar` reads `FOO_BAR_VERSION`). It must start with a
  digit, and it needs a `# renovate:` comment above it
  (`scripts/check-renovate-pins.sh`).
- The `default` stage is `FROM scratch` with only files under `/usr`. A
  system extension merges nothing else, and on Kairos not even all of `/usr`:
  only `/usr/bin`, `/usr/sbin`, `/usr/lib`, `/usr/share`, `/usr/include` and
  `/usr/src` (`SYSTEMD_SYSEXT_HIERARCHIES` in kairos-init's `99_sysext.yaml`).
  A helper that would go to `/usr/libexec` goes under `/usr/lib`
  (`--libexecdir=lib/...`).
- A library the extension needs and the image may already have (or may get
  later) does not go into `/usr/lib`: link it statically, or ship it in a
  directory of the extension's own, such as `/usr/lib/isotope-ext/<name>`,
  found through a RUNPATH (`modemmanager/` does).
- A service is enabled from `/usr`: a link in
  `/usr/lib/systemd/system/multi-user.target.wants/` (or `sockets.target`,
  `timers.target`), with the unit file next to it in
  `/usr/lib/systemd/system`. After `isotope ext` merges an extension it
  reloads systemd, the D-Bus configuration, the udev rules and polkit
  (when the extension has files in `/usr/share/polkit-1`), and starts
  the units linked there whose unit file the extension ships and the base
  does not have; before it disables or removes a merged one, it stops those
  same units. A link to a unit of the base is never acted on.
- The `test` stage runs on `ghcr.io/kairos-io/hadron:${HADRON_VERSION}`. It
  must fail when a file of the extension already exists in the base. A file
  over the base's copy is what `isotope ext` refuses, and in the worst case it
  replaces a library the desktop uses. Link the extension's own libraries
  statically, as `htop/` does with ncurses.

Build and test one locally:

```
docker build --build-arg HADRON_VERSION=$(scripts/bump-hadron.sh --current) --target test extensions/htop
```

## How it is published

`.github/workflows/extensions.yml`, on every change here that reaches `main`:

1. builds the `test` stage, then `default`;
2. makes `<name>.sysext.raw` with AuroraBoot (`auroraboot sysext`);
3. pushes it to `ghcr.io/kairos-community/isotope:sysext-<name>-<version>-amd64`
   (the image's own public repository, so nothing new has to be made public);
4. writes the catalog with `scripts/extensions-catalog.sh`, which lists every
   `sysext-*` tag of the repository pinned by digest, and deploys it to
   <https://kairos-community.github.io/isotope/extensions.json>.

A pull request runs steps 1 and 2 only.

The extensions are unsigned, like hadron-layers'. They merge because the image
sets `extensions.ignore_signatures`, which a Trusted Boot image does not set.
So a Trusted Boot system cannot use them.

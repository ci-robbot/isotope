# Programs built for glibc

Tracking issue: kairos-community/isotope#136, batch 2, the first item.

## Why

Isotope is a musl system. A program that a vendor ships for "Linux" is almost
always built against glibc, and today it fails with a puzzling
`not found` (the kernel cannot find its loader,
`/lib64/ld-linux-x86-64.so.2`). Flatpak covers the apps on Flathub, and
Distrobox is already in the image, but the user has to know about both and set
a box up by hand. The goal is that a glibc program or AppImage runs when the
user starts it, from a terminal or the file manager, with no setup.

## Goals

- A glibc program started in any way (`./tool`, a double-click, a `.desktop`
  file, a script) runs, in a default Ubuntu LTS Distrobox that Isotope creates
  on first use.
- An AppImage runs the same way.
- `isotope glibc` manages the box and runs a program in it explicitly.
- When it cannot run (root, the live system, no Docker, the server), the user
  reads one clear sentence that says why and what to do, never `not found`.
- On all four desktop variants.

## Non-goals

- Running glibc programs natively on musl (gcompat): rejected, see Approaches.
- 32-bit (i386) glibc programs.
- System services or root running glibc programs.
- Choosing the box's distro (the maintainer picked Ubuntu LTS).

## Anti-goals

- A glibc program that half-runs on the host and fails later in an odd way.
- A loop: the box has its own glibc loader, so a program inside the box must
  never come back to Isotope's stand-in.
- Breaking a statically linked program, which runs on the host today.

## Approaches

The maintainer picked A.

- **A. A stand-in glibc loader plus binfmt for AppImages (picked).** Every
  dynamically linked x86-64 glibc program names `/lib64/ld-linux-x86-64.so.2`
  as its ELF interpreter. Isotope has no such file (`/lib64` is a link to
  `/usr/lib`, which holds only musl's `ld-musl-x86_64.so.1`). A small static
  program installed there is what the kernel starts for such a program. It
  hands the program to the box. AppImages have their own magic (`AI\x02` at
  offset 8), so a `binfmt.d` rule handles them. This is native-feeling and
  fully compatible. The cost is a slow first run (the box is created), about
  half a second on later runs, and that a tool which probes for that path to
  ask "is this glibc?" gets the wrong answer.
- **B. gcompat**, a glibc compatibility shim on musl: no container and no delay,
  but only simple programs work. Programs that use newer glibc symbols, NSS or
  locales fail in odd ways, which is the anti-goal above.
- **C. Explicit only:** `isotope glibc run`, an AppImage handler and a message.
  Nothing is automatic.

## The design

### The stand-in loader

- `tools/isotope-glibc-loader.c`, built static (musl) in a Dockerfile stage like
  `isotope-gpu-probe`, installed as `/usr/lib/ld-linux-x86-64.so.2` (reached
  as `/lib64/ld-linux-x86-64.so.2` through the `/lib64` link) on the four desktop
  variants.
- **When the kernel starts it as an interpreter:**
  - It finds the program path from `getauxval(AT_EXECFN)` and keeps argv as given.
  - It execs `/usr/bin/isotope glibc run --loader -- <absolute program path> <argv[1..]>`, keeping argv[0] for the program.
- **When it is run directly** (`/lib64/ld-linux-x86-64.so.2 ./prog args`, as
  some scripts do): AT_EXECFN is the loader itself, so `argv[1]` is the program.
- **When it cannot hand off** (a setuid or setgid program, which starts the
  loader with `AT_SECURE`: setuid and setgid glibc programs are not handed to
  the box, because `isotope` would run the caller's `distrobox` from `PATH`
  with the program's user or group; uid 0, the live system marker
  `/run/isotope/live-desktop`, no `/usr/bin/isotope`, or
  `ISOTOPE_GLIBC_LOADER_ACTIVE` already set, which would mean a loop):
  - It prints to stderr: "`<prog>` is a program built for glibc. Isotope runs
    these in a glibc container for your user: run it as a normal user, or see
    `isotope glibc --help`." The wording for each case is the builder's call.
    It must say why, and what to do.
  - It exits 127.
- **Size and safety:** it is small and links nothing. It never writes to
  disk and never runs a shell.

### AppImages

An AppImage can be built for glibc, for musl, or fully static, so it is not
sent to the box blindly. (The maintainer raised this: "what if AppImages
support musl?")

- **The binfmt rule.** `/usr/lib/binfmt.d/isotope-appimage.conf` registers the
  AppImage type 2 magic (offset 8, `AI\x02`) to `/usr/libexec/isotope-appimage`,
  which runs `isotope glibc run --appimage -- <path> <args>`. Type 1 AppImages
  are out of scope.
- **Inside containers, AppImages run through their own runtime, as before.**
  binfmt_misc is not per mount namespace: rootful Docker, a Distrobox and the
  `isotope-glibc` box see the host's rule. So the handler is a small static C
  program, `tools/isotope-appimage.c` (built `-static-pie` against musl like the
  loader, in the Dockerfile `appimage-handler` stage). `F` makes the kernel run
  the host's handler everywhere, but the AppImage's path and `/usr/bin/isotope`
  are resolved in the caller's namespace.
  - On the host it execs `isotope glibc run --appimage -- <path> <args>`. "The
    host" is: `/run/systemd/system` is a directory (booted with systemd as
    init) and `/usr/bin/isotope` is an executable regular file. Docker
    containers and Distrobox boxes have no `/run/systemd/system` unless they
    were created to run systemd (`--init`). A systemd container that also has
    Isotope's `isotope` passes; `isotope glibc run` then refuses there with its
    own sentence (no Docker, or live), which is acceptable. A device and inode
    comparison with `/proc/self/exe` is not used: a systemd-sysext overlay
    merged over `/usr` reports another device, so it would say "not the host"
    on an installed Isotope after any extension change.
  - Anywhere else it copies the AppImage into a `memfd_create` file, clears the
    magic (bytes 8 to 10, in the ELF `e_ident` padding, which the kernel
    ignores), and `fexecve`s the copy with the original arguments and
    environment. The copy has no magic, so the rule does not match it again
    (no loop). The runtime reads its squashfs from `/proc/self/exe`, which is
    the copy and holds the whole file. This is what AppImageLauncher's
    binfmt-bypass does. The runtime needs FUSE there, or
    `--appimage-extract-and-run`, as before this rule existed.
    - The runtime sets `APPIMAGE` itself from `/proc/self/exe`, so the app
      sees the memfd's name there (self-updaters and a portable `.home` are
      affected, inside containers only). `TARGET_APPIMAGE` would fix it, but
      the runtime never unsets it, so a nested AppImage would run the wrong
      file. The handler sets neither.
    - With `vm.memfd_noexec=2` (per PID namespace, so a container can set it)
      an executable memfd is refused; the handler then says so and points to
      `--appimage-extract-and-run`.
  - It refuses with a sentence and exit 127 when it runs setuid or setgid
    (`AT_SECURE`, or an effective user or group that is not the real one).
- **Extract, do not mount.** An AppImage is an ELF runtime followed by a
  squashfs image.
  - `isotope glibc` reads the squashfs itself, with a pure-Go squashfs reader in
    `cmd/isotope`. The offset is the end of the runtime ELF's section headers,
    as the AppImage spec defines it.
  - It extracts the image once into
    `$XDG_CACHE_HOME/isotope/appimages/<sha256 of the file>/`, then runs `AppRun`
    from there with `APPDIR`, `APPIMAGE` and `ARGV0` set, as the AppImage
    runtime would.
  - This needs no FUSE. It never runs the AppImage's own runtime, so it cannot
    re-trigger the binfmt rule. Later starts reuse the cache. (Only on the
    host: in a container the handler runs the runtime itself, see the rule.)
  - The cache is pruned on `isotope glibc reset`, and an entry not used for
    30 days is removed on the next run.
- **Decide by what is inside.**
  - Find the program `AppRun` starts: the target of a symlink, `AppRun` itself
    if it is an ELF, or, when `AppRun` is a script, the ELF files under
    `usr/bin` and `usr/lib`.
  - If any of them names `ld-linux-x86-64.so.2` as its ELF interpreter, the
    AppImage runs in the box. The cache is under `$HOME`, which the box shares.
  - Otherwise (musl or static), it runs natively on the host, with no box and no
    Docker.
  - `isotope glibc run --native|--box FILE` overrides the choice.
- **Plain programs follow the same rule.** A musl or static binary names no
  glibc loader, so it never reaches the stand-in and runs natively, as today.

### `isotope glibc` (in `cmd/isotope`, pure Go)

- `isotope glibc run [--loader|--appimage] [--] FILE [ARGS...]`:
  1. Refuse as root, on live, or without Docker, with the same sentences as the
     loader.
  2. Create the box if it is missing (`distrobox create --yes --name isotope-glibc
     --image <image>`).
     - On a terminal it prints progress.
     - With no terminal (a double-click) it sends a desktop notification at the
       start ("Setting up the glibc environment, first run only…") and at the end.
       A failure says what failed.
  3. Run the program with `distrobox enter isotope-glibc -- bash -c 'exec -a "$0" "$@"' <argv0> <path> <args>`
     (bash: see "Calls made after the builder check", item 6).
     - It keeps the working directory and the environment that distrobox passes.
     - It sets `ISOTOPE_GLIBC_LOADER_ACTIVE=1`.
     - It maps a path the box cannot see: distrobox shares `$HOME` and mounts the
       host root at `/run/host`, so a path outside the shared ones gets
       `/run/host` in front. The builder checks which paths distrobox 1.8 shares.
     - It passes stdin, stdout, stderr and the exit status through, and the
       signals as distrobox does.
  4. With `--appimage`, it does the extraction and the native-or-box choice
     described under AppImages first. A native AppImage needs no Docker, so the
     refusals for "no Docker" and "live" do not apply to it.
- `isotope glibc create`, `isotope glibc enter` (a shell in the box),
  `isotope glibc status` (exists, image, size) and `isotope glibc reset`
  (remove and create again, after a confirmation on a terminal).
  `status --json` gives `exists`, `image`, `size_bytes`, `auto`, `error` and
  `busy` (being created or reset now) for Isotope System; `create --progress`
  and `reset --progress` print the progress on stderr as on a terminal, and
  notify only their end, and only when the window that started them is gone
  (their parent changed). `reset` exits 75 when the box is in use, before
  any other check.
- **The image is Ubuntu LTS:**
  - `docker.io/library/ubuntu:26.04` today.
  - It is pinned in one place that Renovate updates within the LTS line,
    following the Dockerfile ARG pattern, and written to
    `/usr/share/isotope/glibc.conf`.
  - Moving to a new LTS is a deliberate change. An existing box keeps its image
    until `reset`.
- **One box per user,** created by that user (the user is in `docker`, as the
  installer sets up). Docker runs only on the installed system
  (`92_docker.yaml`), so live is refused with a clear sentence.

### The Compatibility page in Isotope System

Added at the maintainer's request after this design. Isotope System has a
Compatibility page (`isotope system compatibility`) on every desktop; on the
live system it says "Not available on the live system. Install Isotope
first." Its first section, Glibc compatibility, runs `isotope glibc` as the
user, so the setting file, the box and the cache keep one implementation:

- a switch, "Run programs built for other Linux systems automatically":
  `isotope glibc auto on|off`;
- "Glibc environment": Not set up yet, or Ready with its image and the disk
  space it uses, from `isotope glibc status --json`;
- "Set up now" (`create --progress`) and "Reset…" after a confirmation
  (`reset --yes --progress`), with a spinner and the CLI's lines. The CLI's
  "in use, close those programs" refusal (exit 75) is a message on the
  page, not an error dialog. Both run in their own session and write to a
  file, not a pipe, so closing the window does not stop them half way, and
  their end is then a notification.

### Where it ships

- The four desktop variants.
- Not the server: it has no Distrobox. Its glibc programs keep failing, but the
  server ships no stand-in loader, so nothing changes there.
- Not the agent: Dockerfile.agent removes it.
- The AGENTS.md feature table gets a "Programs built for glibc" row.

### The help page

- A short page explains when to use Flatpak (apps on Flathub), `isotope glibc`
  (a downloaded program or AppImage) or Distrobox directly (a full development
  environment).
- It ships in the image and is linked from `isotope glibc --help` and the README.
  The builder picks the format; the existing `isotope-help` or a doc file are
  both fine. Built: `docs/glibc-programs.md`, copied in `desktop-default` to
  `/usr/share/doc/isotope/glibc-programs.md`; `--help` names that path first
  and the GitHub page second. The agent image removes it.

## Tests

- **Go tests** (`cmd/isotope`):
  - the refusals (setuid or setgid, root, live, no Docker);
  - the path mapping;
  - the create, then run, command lines against a fake `distrobox`;
  - the notification without a terminal;
  - for AppImages: the squashfs offset, the extraction and the cache, and the
    choice made on test AppImages (a glibc one goes to the box; a musl one, a
    static one and a script `AppRun` over musl ELFs run natively);
  - the loop guard.
- **The loader:**
  - a C test, or a shell test on a built loader, for AT_EXECFN against direct
    invocation;
  - argv passed through;
  - each refusal message and its exit code 127, a setgid program
    (`AT_SECURE`) included.
- **The AppImage handler** (`test/glibc/test-appimage.sh`): on the host it
  reaches a fake `isotope`; elsewhere it runs a fake AppImage (an ELF stub
  that writes its argv and the size and magic bytes of `/proc/self/exe`) once,
  with its argv, from a whole copy without the magic; its refusals (not an
  AppImage, setgid).
- **Image check** `test/glibc/check-glibc.sh`:
  - the loader is at `/lib64/ld-linux-x86-64.so.2` and is static;
  - the binfmt rule is in place;
  - `glibc.conf` names an Ubuntu LTS image;
  - a real glibc binary (copied from an Ubuntu image), started on the built
    image with a fake `isotope`, reaches `isotope glibc run` with its path and
    arguments;
  - the agent and the server do not have the loader.
- **Not testable in CI:** a real box with Docker inside the image (the CI images
  run no Docker daemon), and a double-click in each file manager. These go in
  the PR's manual test list.

## Calls made after the builder check

These are the designer's calls, not the maintainer's. Each one can be changed.

1. **What reaches the loader.**
   - Only an exec of a dynamically linked glibc x86-64 ELF reaches the loader.
     Docker's bundle is static. Flatpak and Docker containers have their own
     `/lib64` inside their namespaces. So on the host this is in practice a
     program the user downloaded: a tarball's binary, an installer, or a
     glibc `node` from nvm.
   - Each of these fails today. After this change it runs in the box.
   - A user can opt out: `isotope glibc auto off` writes
     `$XDG_CONFIG_HOME/isotope/glibc` (`auto=off`). The loader then prints its
     message instead of handing off. `ISOTOPE_GLIBC_AUTO=0` does the same for
     one command.
   - There is no allowlist.
   - The image checks confirm that no file the image ships names
     `ld-linux-x86-64.so.2` as its interpreter: `scanelf`, or a `readelf` loop
     over `/usr`.
2. **First run, failures and concurrency.**
   - `isotope glibc run` waits up to 30 s for `/run/docker.sock`.
   - It says "log out and back in" when the user is in `docker` in
     `/etc/group` but not in the current session.
   - Creating the box holds a lock, `$XDG_RUNTIME_DIR/isotope/glibc.lock`, so
     two programs started together create it once and the second one waits.
   - A failed create removes the half-made container. The next run tries
     again.
   - Offline: one sentence saying the first run needs the network.
   - A box whose container is gone (pruned) is recreated with the same
     first-run notice.
   - Creating the box has no time limit, because the image pull and
     distrobox's first-start package setup can take minutes. It shows progress
     on a terminal and two notifications without one.
3. **The image setting.**
   - `ARG GLIBC_BOX_IMAGE=docker.io/library/ubuntu:26.04` in the Dockerfile,
     under a `# renovate: datasource=docker depName=docker.io/library/ubuntu`
     comment.
   - A `renovate.json` package rule allows only `\d\d\.04` tags with an even
     year, so Renovate stays on LTS.
   - The `desktop-default` stage writes it to `/usr/share/isotope/glibc.conf`
     as `image=<ref>`. The Go code reads that key and falls back to the same
     default when the file is missing.
4. **binfmt** (see AppImages for what the handler does).
   - The rule is `:isotope-appimage:M:8:\x41\x49\x02::/usr/libexec/isotope-appimage:F`.
   - `F` opens the handler when the rule is registered, so the host's handler
     runs from any mount namespace. What the handler then opens or execs is
     resolved in the caller's namespace, which is why it is a static program
     that runs the AppImage's own runtime in a container (see AppImages).
     There is no `P` or `C`: no setuid.
   - The plan makes sure `systemd-binfmt.service` is enabled and loads
     `/usr/lib/binfmt.d`, and checks it in the image check (the unit is
     enabled and the rule file is in place). A booted check is in the PR's
     manual list.
5. **Notifications** go through `cmd/isotope`'s own D-Bus `Notify`, not
   `notify-send`, which the image does not ship. Every variant runs a
   notification server.
6. **argv[0] and paths.**
   - The program runs through `bash -c 'exec -a "$0" "$@"'` inside the box, so
     it keeps its argv[0]. It is bash, not sh, because Ubuntu's `/bin/sh` is
     dash, whose `exec` has no `-a` ("exec: -a: not found"). bash is in every
     Ubuntu image (it is Essential).
   - Distrobox 1.8.2.5 (checked in its source; citations in
     `cmd/isotope/glibc.go`) shares `$HOME`, `/tmp`, `/media`, `/mnt`,
     `/run/media` and `/var/mnt` at the same path. It passes the working
     directory and the environment through `distrobox enter`. It has no
     option that fails on a missing container: it asks on stdin, or
     non-interactively creates one from its own default image. So
     `isotope glibc` sets `DBX_NON_INTERACTIVE=0` and
     `DBX_CONTAINER_MANAGER=docker` for every distrobox call, checks the
     container with `docker inspect`, and holds the box lock (shared while a
     program runs, exclusive to create or reset) from the check until the
     program exits.
   - Distrobox shares `$HOME` (the same home: no separate `--home`) and mounts
     the host root at `/run/host`. The builder checks which of `/tmp`, `/mnt`,
     `/media` and `/run/media` distrobox 1.8 shares.
   - Any other path gets `/run/host` in front. There is no copying.
7. **Scope of "in any way".** It covers what the kernel starts: a glibc ELF or
   an AppImage with its execute bit set. A downloaded file without the execute
   bit never reaches the kernel. That is the file manager's job (it asks to make
   it executable), and it is out of scope.
8. **The test binary.** CI pulls the pinned Ubuntu image and copies
   `/usr/bin/true` (a glibc ELF) out of it for `check-glibc.sh`.

## Left to the builder

- The exact sentences, within the "says why and what to do" rule.
- Where the help page lives.
- How the box's first-run notification is sent (`notify-send` exists on every
  variant).
- Whether type 1 AppImages are worth a rule.

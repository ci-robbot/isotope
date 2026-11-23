# Programs built for glibc

Isotope is built on musl, a small C library. Most Linux programs that you
download are built for glibc, the C library of Ubuntu, Fedora and Debian.
Such a program cannot run on musl directly.

On the four desktops (sway, i3, COSMIC and niri), Isotope runs these programs
for you in a container: an Ubuntu LTS [Distrobox](https://distrobox.it/)
named `isotope-glibc`. You start the program the usual way, from a terminal or
from the file manager. The server and the agent appliance do not have this.

## Which one to use

| You want | Use |
|---|---|
| A desktop app that is on [Flathub](https://flathub.org/) (a browser, an office suite, a chat app, a game) | **Flatpak**. Install it from Isotope Welcome's Apps page (`isotope welcome apps`) or with `flatpak install flathub APP`. It gets updates, a sandbox and the desktop's file dialogs. |
| One program that you downloaded: a tarball's binary, an installer, an AppImage, a glibc `node` from nvm | **Start it as usual.** Isotope sends it to `isotope glibc`. You can also run `isotope glibc run -- ./program ARGS`. |
| A full development environment: install packages with `apt` or `dnf`, build software, keep several distributions | **Distrobox** itself: `distrobox create --name dev --image docker.io/library/fedora:latest`, then `distrobox enter dev`. You control the image and its packages. |

Use the glibc container for programs, not as your development environment.
`isotope glibc reset` removes it and creates it again, with everything that
was installed in it.

## How it works

- A program built for glibc asks for glibc's loader,
  `/lib64/ld-linux-x86-64.so.2`. On Isotope, that file is a small stand-in.
  It gives the program to `isotope glibc run`, which runs it in the
  container.
- An AppImage with its execute bit set is sent to `isotope glibc run
  --appimage`. Isotope unpacks it once into `~/.cache/isotope/appimages`. It
  then runs it directly when nothing in it needs glibc, and in the container
  when something does. `--appimage-extract` and `--appimage-help` are passed
  to the app as ordinary arguments, because Isotope, not the AppImage's
  runtime, opens the file. Unpacked AppImages not used for 30 days are
  removed.
- Inside a container (Docker, a Distrobox of your own, or `isotope glibc
  enter`), an AppImage runs through its own runtime, as on any other Linux
  system: it needs FUSE in that container, or `--appimage-extract-and-run`.
- The first run creates the container. This needs the network and can take a
  few minutes. Later runs start at once.
- The container shares your home folder, `/tmp`, `/media`, `/mnt`,
  `/run/media` and `/var/mnt`. Other paths of your system are under
  `/run/host` in the container.

## What it needs

- The installed system. The live system on the ISO has no Docker, so it has
  no container.
- A normal user in the `docker` group. The installer adds the user that it
  makes. If you were added later, log out and in again.
- The root user is refused: run the program as a normal user.
- A setuid or setgid program built for glibc is refused: it is not handed to
  the container with someone else's user or group.

When a program cannot be sent to the container, it stops with a sentence that
says why and what to do, and the exit status 127.

## Settings and commands

Isotope System has a Compatibility page (`isotope system compatibility`) with
the same settings: whether programs are sent to the container automatically,
the container's state, "Set up now" and "Reset…".

```sh
isotope glibc status          # whether the container exists, its image and size
isotope glibc create          # create the container now
isotope glibc enter           # a shell in the container
isotope glibc reset           # remove the container and create it again
isotope glibc auto off        # do not send programs automatically any more
ISOTOPE_GLIBC_AUTO=0 ./tool   # the same, for one command
isotope glibc --help          # every command and option
```

The container's image is an Ubuntu LTS release, which the system names in
`/usr/share/isotope/glibc.conf`. A new Isotope release can move to the next
LTS. A container that exists keeps its image until `isotope glibc reset`.

# Network shares and NTFS disks

Issue kairos-community/isotope#136, batch 4. Status: implemented on
`feat/network-shares`.

## Goal

- Mount an SMB (`smb://`) or NFS (`nfs://`) share from the Disks page of
  Isotope System ("Connect to server…"), and keep a list of saved shares that
  mount again at login.
- Mount NTFS disks through the kernel's ntfs3 driver from udisks2, and show
  them as mountable on the Disks page.
- Not on the server (no Disks page) and not on the agent.

## What the base gives

- `ghcr.io/kairos-io/hadron:main` has `mount.cifs` (cifs-utils,
  kairos-io/hadron#893), `mount.nfs` and `mount.nfs4`, and the kernel
  modules `fs/smb/client/cifs.ko`, `fs/nfs/nfsv4.ko` and `fs/ntfs3/ntfs3.ko`.
  Isotope adds no package for either protocol.
- udisks2 2.12's built-in mount options already contain
  `ntfs_drivers=ntfs3,ntfs` and `ntfs:ntfs3_defaults=uid=$UID,gid=$GID`, so
  NTFS mounts with ntfs3 with no build option. The image still ships
  `/etc/udisks2/mount_options.conf`, which pins `ntfs_drivers=ntfs3` (the
  legacy `ntfs` driver left the kernel in 6.9, so the fallback can only fail)
  and adds `windows_names` (no file names that Windows cannot open). The mount
  call names `ntfs3`, and the kernel loads the module through its `fs-ntfs3`
  alias.

## Decisions

### 1. Mounting is a daemon method

Mounting a CIFS or NFS share needs root. `isotope-updated` gets three methods:

| Method | Arguments | Notes |
|---|---|---|
| `MountShare` | `url s, options a{sv}, secrets h` → `mountpoint s` | options: `name` (s), `read_only` (b). secrets: three lines, user, domain, password |
| `UnmountShare` | `mountpoint s` | only a share under the caller's own `/run/media/<user>` |
| `MountedShares` | → `aa{sv}` (`mountpoint`, `fstype`, `source`) | the caller's own shares, read from the mount table |

The polkit action is `io.kairos.isotope-update.shares`: YES for a local,
active session and NO for every other session, as udisks2's removable-disk
rule. An NFS share also needs a second action,
`io.kairos.isotope-update.shares-nfs`: YES only for a local, active member of
`admin`. NFS with AUTH_SYS (`sec=sys`, the default) trusts this computer by
its address: the kernel mounts from a reserved port, and then sends the
user's local uid as it is. An export that trusts this machine (common in labs
and offices) would let any desktop user in, as whoever has their uid number
on the server. Before this feature only an administrator could attach such
an export, and it stays so. SMB has its own credentials and stays open to a
local, active session. A second polkit action, not a group check in the
daemon, keeps the decision in the rules file with the others, where an
administrator can change it. The daemon asks for it after `shares`, reading
only the URL's scheme first. The page says why when a user who is not an
administrator types an `nfs://` address (Connect stays off), and a saved NFS
share says the same instead of calling the daemon.

Both rules are in `50-isotope-update.rules`, before the admin rule that
answers for every other `io.kairos.isotope-update.*` action, so the order is
the file's own and does not depend on the order of two files. A Flatpak
caller is refused before polkit is asked, as for every other mutating method.

Mount points are `/run/media/<user>/<name>`, where udisks2 puts disks. The
daemon makes `/run/media/<user>` as udisks2 does (root, 0750, the user's
group), and refuses a `/run/media/<user>` that is a link or is not owned by
root: the user cannot then swap the mount point for a link between the mkdir
and the mount. A name that is taken gets a number (`name-2`), as udisks2 does.

Options are always `nosuid,nodev,noexec`.

- CIFS: `uid=`, `gid=`, `forceuid`, `forcegid`, `file_mode=0600`,
  `dir_mode=0700`, so the files are the caller's and no other local user's.
  The SMB dialect is negotiated by the kernel (3.1.1 down to 2.1).
- NFS: only NFSv4, `vers=4.2`, and `vers=4.1` when the server does not do
  4.2. File owners are the server's numbers: a user whose uid differs from
  the server's sees other owners. This is a known limitation (no idmapping,
  no Kerberos).

The daemon runs `mount` and `umount` with an environment of `PATH` only, so
`mount.cifs` never reads a `USER` or `PASSWD` variable from the daemon's
environment. Each call has a 60 s limit.

A share that the caller already has mounted is not mounted twice:
`MountShare` returns its mount point. Login and a click can then both ask.

Limits. Every mount is a systemd `.mount` unit, and unlike udisks2 nothing
else bounds them, so a user has at most 16 shares mounted; the 17th is
refused ("unmount one first"). Each user has one `MountShare` in progress at
a time; a second is refused at once rather than queued. No lock is held
across the mount call itself (the busy flag is per user, under a short
lock), so a slow or dead server keeps only its own user waiting:
`UnmountShare`, `MountedShares` and other users' mounts go on.

A credentials file that a killed daemon left in `/run/isotope-update/shares`
(the RuntimeDirectory is preserved) is removed when the daemon starts.

### 2. Credentials

- No password on a command line, in D-Bus strings or in `/oem`.
- The page sends user, domain and password through a pipe fd, as `AddUser`
  and `BackupInit` do.
- The daemon writes them to a 0600 file in `/run/isotope-update/shares/`
  (directory 0700) for `mount.cifs credentials=`, and removes it when the
  mount call returns. No user: the `guest` option.
- An NFS share takes no credentials; the daemon refuses them.
- Remembering a password is the session's job: the page stores it in the
  user's keyring through the Secret Service (gnome-keyring, on all four
  variants), with the attributes `xdg:schema=io.kairos.isotope.NetworkShare`,
  `url` and `user`. The daemon never stores a password.

### 2b. Unmount at logout

Shares do not outlive the session. The kernel would keep the user's SMB
session and password, and an NFSv4 mount is `hard`: a laptop that leaves the
network keeps a dead mount, and every stat of it hangs. Windows mapped drives
and GVfs disconnect at logoff too.

`isotope-shares-logout@.service` (instance: the uid) is wanted by every
`user@UID.service` through `user@.service.d/50-isotope-shares-logout.conf`,
and has `BindsTo=user@%i.service`. It does nothing at start; its
`ExecStop`, `isotope-update daemon --unmount-shares %i`, runs as root when
the user's last session ends and logind stops the user manager. It unmounts
every cifs, smb3, nfs and nfs4 mount right under that user's
`/run/media/<user>`, lazily (`umount -l`), and NFS also forced (`-f -l`), so
a share in use or a server that is gone cannot hang the logout, then removes
the folders. It asks no polkit: the session is no longer active, and the
`shares` rule would say no. The tray is not used: it is killed at logout.
Not on the server (`server-config` takes no such file) and not on the agent
(Dockerfile.agent removes both files).

### 3. Saved shares are the user's file, not the daemon's

The task named `SavedShares`, `SaveShare` and `ForgetShare` daemon methods
and also put the list in `$XDG_CONFIG_HOME/isotope/shares.json`. These two
cannot both hold safely: the daemon is root, does not know the caller's
`XDG_CONFIG_HOME`, and root writing into a home directory that the user
controls is a classic link attack. So the list is read and written by the
user's own processes (Isotope System and the tray) through `pkg/shares`, and
the daemon only mounts. `shares.json` holds `url`, `name`, `user`, `domain`
and `auto_mount`, never a secret; it is written 0600 through a temporary file
and a rename.

The saved list is matched to the mounted shares by protocol, host, share and
path (`shares.Location.Same`), not by the URL text.

### 4. Mount at login: the tray

The tray (`isotope-update tray`) already starts with every session on all four
variants, after the login keyring is unlocked by `pam_gnome_keyring`, in a
session that polkit sees as local and active, and it is the process that
already hosts the session's agents. At start it reads `shares.json`, takes
each password from the keyring, and asks the daemon to mount every share with
`auto_mount`. The network may still be coming up, so a share that fails for a
network reason (timeout, host down or unreachable, connection refused, a name
that does not resolve: `shares.Transient`) is tried again, up to five times,
20 s apart. Any other failure stops at once, above all a wrong password
(`error(13)`, `Permission denied`, `STATUS_LOGON_FAILURE`): five tries of an
old password at each login can lock a domain account. A keyring that is not
up yet is asked again in the next round. When a share's last try fails, the
tray sends one notification with the share's name and the reason ("The server
refused the user name or password.", "Its password is not in your
keyring.") and points to the Disks page. A systemd user service was the
other choice; it starts for SSH logins too (where polkit says no), and its
order against the keyring unlock and the session's activation is not
fixed. The tray does not run on the live desktop, so nothing mounts by itself
there.

### 5. The firewall

`/usr/share/isotope/firewall.nft` has one chain, `input`, policy drop. There
is no output chain, so connections to SMB (445) and NFS (2049) go out, and
their replies come back through `ct state established,related accept`.
NFSv4.0 and NFSv3 need the server to open connections to the client
(callbacks, NLM), which the input policy drops; NFSv4.1 and 4.2 use the
client's own connection for callbacks. So only 4.2 and 4.1 are used.

### 6. NTFS on the Disks page

udisks2 shows an NTFS partition as a filesystem with `IdType` `ntfs`; the
page offers Mount for it like any other filesystem of a removable disk. A
test holds that. A real NTFS mount needs a loop device and `mkfs.ntfs`
(ntfs-3g), which neither the Hadron image nor the CI job has; the image
check (`test/shares/check-shares.sh IMAGE`) checks the driver and the
configuration, and the real mount is on the manual list below.

### 7. Not on the server, no CLI

The server has no Disks page and its administrator uses `mount`. The daemon
methods exist there too (one binary), and work for a local console session,
but nothing calls them. No `isotope shares` command: the CLI has no keyring
and no saved list to use, and `mount -t cifs` is what a server administrator
types.

### 8. Agent

The agent removes the update service with its polkit action and rule file
(and so the shares action and rule), `/etc/udisks2` (and so
`mount_options.conf`), Isotope System, and the tray. `Dockerfile.agent` checks
that no shares action is left. `mount.cifs` and `mount.nfs` are Hadron's and
stay.

## Tests

- `pkg/shares`: URL parsing and refusals, the saved file, the login
  auto-mount loop with a fake keyring and mounter.
- `cmd/isotope`: the daemon methods with fake `mount` and `umount` commands
  on a temporary `PATH`: arguments, options, the credentials file (0600,
  gone after the call), the environment, guest, NFS 4.2 then 4.1, read-only,
  the mount point rules, unmount of another user's share, the mount table,
  the NFS action, the cap of 16, one mount at a time per user with no lock
  across the mount, and the logout unmount (`-l`, NFS `-f -l`, no polkit).
- `pkg/shares`: which errors are tried again, one try for a wrong password,
  the keyring asked again, one notification per share that failed.
- `cmd/isotope-system`: the Disks page's shares section and the "Connect to
  server…" dialog against a fake daemon and store; golden screenshots of the
  dialog and the list (`cmd/isotope-system/testdata/shares-*.png`).
- `test/polkit/check-polkit-rules.sh`: the shares action.
- `test/shares/check-shares.sh --repo` (CI) and `IMAGE` (the built
  images): the tools, the modules, the udisks2 configuration, the polkit
  action; on the agent, that none of it is left that should not be.

## Manual list

- Mount a Samba share with a user and a password, and as a guest; check the
  owner of the files and that `/run/isotope-update/shares` is empty after.
- Mount an NFSv4.2 export and an NFSv4.1-only export, as an administrator;
  as another user, check that the page says NFS is for administrators.
- Log out with an SMB and an NFS share mounted (and once with the NFS server
  unplugged): `findmnt` shows neither, and the logout does not hang.
- Change a saved share's password on the server, log in: one notification,
  and the server's log shows one failed login, not five.
- Save a share with "Mount at login" and the password, log out and in.
- Plug in a USB disk with an NTFS partition, mount it from the Disks page,
  write a file, and check `findmnt` says `ntfs3`.

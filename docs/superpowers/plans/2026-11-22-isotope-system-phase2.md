# Isotope System, phase 2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Finish Isotope System: Users and Disks pages, backup setup, export and restore, the new recovery key and reseal dialogs, an update history, and (only on a HIDE verdict) COSMIC Settings without its Users page. All in one PR.

**Architecture:** Each privileged job is a new method of `io.kairos.IsotopeUpdate1` in `isotope-updated` (`update/`). The method sits behind polkit and reuses the CLI's code path (`backup.go`, `restore.go`, `backup_export.go`). Secrets reach it only through a pipe fd. The app (`system/`) gets a matching client and one page per job. Disks talk to udisks2 straight from the app, as the user, under an extended polkit rule. No daemon is involved there.

**Tech Stack:** Go 1.25, godbus/dbus v5, Gio (gioui.org v0.10.3) over `guikit/`, polkit JS rules, yip v1.27.1 (kairos-init:master, the monorepo `go.mod`), restic 0.19.1, udisks2 2.12, gopkg.in/yaml.v3 (new in `update/`).

**Spec:** `docs/superpowers/specs/2026-11-21-isotope-system-app-design.md` (Backup phase 2, Security, Users (phase 2), Disks (phase 2), Privilege model, Testing, Phases, Decisions). Backup follows `docs/superpowers/specs/2026-11-20-isotope-welcome-design.md` (The Backup page, Disks, Remote targets, Daemon API). The implementer reads both specs and `AGENTS.md`.

## Global Constraints

- **Secrets.** Never send a secret (a password, the backup password, a recovery key, an S3 key) as a D-Bus string, in either direction. Secrets travel as a pipe fd (type `h`): newline-separated lines, at most 4096 bytes. The daemon reads them with `readSecrets` (`update/recovery_bus.go`), and the client writes them with `withSecrets` (`update/recovery_cli.go`, twinned in `system/`).
- **Command lines and logs.** Never put a secret or a password hash in argv or in a log line. Pass it on stdin (`openssl passwd -6 -stdin`, `chpasswd -e`).
- **Order of checks.** Every mutating daemon method calls `a.s.enter()`, then `a.s.authorize(sender, action)` before it reads any argument. The one exception is `SetPassword` for the caller's own account (Task 4). A fd argument is closed on every refusal path (`closeFD`).
- **Polkit actions.** `io.kairos.isotope-update.users` (new) and `io.kairos.isotope-update.restore` (new) are added to `rootfs/usr/share/polkit-1/actions/io.kairos.isotope-update.policy` with `allow_any`, `allow_inactive` and `allow_active` all `no`, like the others. `50-isotope-update.rules` already answers both.
- **Flatpak callers.** They are refused, as today (`authorize` does it).
- **Persistent state.** It goes under `/usr/local` or `/oem`, never `/var/lib`: Kairos rebuilds `/etc` and `/var` at every boot.
- **Users.** A user change is applied now and written to `/oem/95_isotope_users.yaml` (0600). Write the OEM file first, then apply live. If the live apply fails, write back the file's previous bytes.
- **Admin checks in the app.** `System.Admin` only starts a page read-only. Every button that changes the system is off for a non-admin, with the text "Only an administrator can …". The daemon decides again on every call.
- **Names.** "Isotope System", `isotope system [PAGE]`, page ids `users`, `disks`. Never "Settings", never "Hadron".
- **No terminal.** Never `sudo`, `pkexec` or a terminal from the app.
- **Variants.** Every variant (sway, i3, cosmic, niri) gets every page. The server gets the daemon methods and the `isotope users` CLI, not the app. The agent image gets neither the app nor the update service.
- **Commits.** Conventional, signed off, with the trailer: `git commit -s -m "feat(scope): …" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"`.
- **`update/` tests.** Run on the host: `cd update && go test ./...`. Bus tests need a `dbus-daemon` and skip without one.
- **`system/` tests.** They need cgo headers, so run them in a container. This plan calls the command `SYSTEST`:
  `docker run --rm -v "$PWD:/src" -w /src/system golang:1.25-alpine sh -c 'apk add -q gcc musl-dev pkgconf wayland-dev libxkbcommon-dev mesa-dev vulkan-headers && CGO_ENABLED=1 go vet -tags nox11,novulkan ./... && CGO_ENABLED=1 go test -race -tags nox11,novulkan ./...'`
- **`guikit/` tests.** Same container, `-w /src/guikit`.
- **gofmt.** It is clean in every touched module (CI checks it).

## Review Focus

1. **Self-password.** A user who changes their own password from a session that someone else opened must give the current password. A hash the daemon cannot verify (`$y$`, `rounds=`) must refuse with a sentence, not lock the user out. Pinned in Task 4: `TestSetPasswordSelfNeedsTheCurrentPassword`, `TestSetPasswordSelfRefusesAnUnverifiableHash`.
2. **Removing a logged-in user, or yourself.** Removing a user who is logged in, or the account you use, must refuse with a sentence and change nothing. Pinned in Task 3: `TestRemoveUserRefusesSelfAndLoggedIn`.
3. **Formatting the wrong disk.** A drive with any system partition (`COS_*`, `*_LUKS`, `HintSystem`, `HintIgnore`, the drive that holds `/`) must never offer Format. Format of a mounted disk must unmount first and stop if the unmount fails. Pinned in Task 10: `TestIsSystemDevice`, `TestSystemDriveIfAnyBlockIs`. Pinned in Task 13: `TestFormatUnmountsFirstAndStopsOnFailure`.
4. **A USB backup disk that is not plugged in.** A `uuid:` target with the disk missing must show "Plug in <label> to back up" and leave Back up now off. The scheduled run must skip it, never write to a bare mount point on the internal disk. Pinned in Task 6: `TestUUIDTargetNotPresentIsSkipped`, `TestUUIDTargetUnmountsWhatItMounted`.
5. **Restore on a machine in use.** `Restore` must refuse before it reads the backup when the machine is not new. The page must not offer it, and must point to `isotope restore --only` instead. Pinned in Task 7: `TestRestoreRefusesAMachineInUse`. Pinned in Task 14: `TestRestoreHiddenOnAMachineInUse`.

---

## File map

| File | Responsibility |
|---|---|
| `guikit/window.go` | `ConfirmForm`: a dialog with a form between the body and the buttons (Confirm calls it with nil) |
| `system/dialog.go` | dialog gains a form widget, a focus index and per-choice enabled checks |
| `update/recoverykey/` (new package) | the recovery key format: generate, canonical form, display lines, group check. Shared by `update/` and `system/` |
| `update/users.go` | users model: `/etc/passwd` and `/etc/group`, guards, password hashing, the OEM file, live apply |
| `update/users_bus.go` | `Users`, `AddUser`, `RemoveUser`, `SetPassword`, `SetAdmin` on the bus; caller uid |
| `update/users_cli.go` | `isotope users` |
| `update/history.go` | the install history file and `UpdateHistory` |
| `update/backup_target.go` | `uuid:` targets, per-target env, SFTP key and host keys, `BackupInit`/`BackupSSHKey`/`BackupHostKey` |
| `update/backup_jobs.go` | `BackupList`, `BackupExport`, `BackupCancel`, `Restore`, `machineIsNew`, `StateRestore` |
| `system/bus.go` | client of every new daemon method |
| `system/udisks.go`, `system/disks_model.go` | udisks2 client; `isSystemDevice`, drive rows |
| `system/page_users.go`, `page_disks.go`, `page_backup.go`, `page_security.go`, `page_updates.go` | the pages |
| `system/keydialog.go` | the shown-once key dialog (recovery key, backup password) |
| `rootfs/usr/share/polkit-1/rules.d/50-isotope-udisks.rules` | modify-device for local active admins |
| `test/polkit/check-polkit-rules.sh`, `test/polkit/rules-harness.js` | runs the real polkit rule files against a case table |
| `test/system/check-system-image.sh` | a built image has the app, the desktop file, the window rules, the menu and the tools |
| `Dockerfile`, `Dockerfile.agent` | exfatprogs stage; agent removals; (conditional) cosmic-settings features |

---

### Task 1: guikit `ConfirmForm` and form dialogs in `system/`

**Files:**
- Modify: `guikit/window.go` (`Confirm` at :382)
- Test: `guikit/guikit_test.go`
- Modify: `system/dialog.go`
- Test: `system/ui_test.go`

**Interfaces:**
- Produces (guikit): `func (th *Theme) ConfirmForm(gtx layout.Context, title, body string, form layout.Widget, actions []Action) layout.Dimensions`. `Confirm(gtx, title, body, actions)` becomes `ConfirmForm(gtx, title, body, nil, actions)`. When the form is not nil, it is drawn between the body and the buttons, 18 dp below the body, and the box is 520 dp wide in place of 460.
- Produces (system):
  ```go
  type choice struct {
      Label   string
      Kind    guikit.ButtonKind
      Do      func()
      Enabled func() bool // nil: always enabled
  }
  // askForm is ask with a form and the index of the choice that gets the focus.
  func (u *UI) askForm(title, body string, form layout.Widget, focus int, choices ...choice)
  ```
  `ask` keeps its behaviour: the last choice gets the focus. `dialog` gains `form layout.Widget` and `focus int`. `pick` ignores a choice whose `Enabled` returns false.

- [ ] **Step 1: Write the failing tests**

```go
// guikit/guikit_test.go
func TestConfirmFormDrawsTheForm(t *testing.T) {
    th := NewTheme()
    drawn := false
    form := func(gtx layout.Context) layout.Dimensions { drawn = true; return layout.Dimensions{Size: image.Pt(10, 40)} }
    var c widget.Clickable
    th.ConfirmForm(testContext(1000, 700), "T", "B", form, []Action{{Click: &c, Label: "OK"}})
    if !drawn { t.Fatal("the form was not drawn") }
}

// system/ui_test.go
func TestAskFormFocusesTheGivenChoice(t *testing.T) {
    u, _, _ := testUI(t, true)
    u.askForm("Erase?", "body", nil, 0, choice{Label: "Cancel"}, choice{Label: "Erase and format", Kind: guikit.ButtonDanger})
    d := waitQuestion(t, u, "Erase?")
    if d.focus != 0 { t.Fatalf("focus %d, want 0 (Cancel)", d.focus) }
}
func TestPickIgnoresADisabledChoice(t *testing.T) {
    u, _, _ := testUI(t, true)
    ran := false
    u.askForm("Q", "", nil, 0, choice{Label: "Cancel"}, choice{Label: "Go", Do: func() { ran = true }, Enabled: func() bool { return false }})
    d := waitQuestion(t, u, "Q")
    u.pick(d, 1)
    if ran || u.question() == nil { t.Fatal("a disabled choice was picked") }
}
```

- [ ] **Step 2: Run them and see them fail.** `SYSTEST` (and the guikit container run). Expected: compile errors, because `ConfirmForm`, `askForm` and `focus` are not defined.
- [ ] **Step 3: Implement `ConfirmForm`, `askForm`, the `focus` field and `Enabled`.** In `dialog.update`, focus `&d.clicks[d.focus]` the first time, and draw a disabled choice as `Action{Disabled: true}`.
- [ ] **Step 4: Run the tests again.** `SYSTEST` and the guikit run. Expected: PASS. Welcome still builds: `docker run … -w /src/welcome … go vet -tags nox11,novulkan ./...`.
- [ ] **Step 5: Commit** `feat(guikit): a confirm dialog with a form, for Isotope System's dialogs`.

---

### Task 2: `update/recoverykey`, the shared key format

**Files:**
- Create: `update/recoverykey/recoverykey.go`, `update/recoverykey/recoverykey_test.go`
- Modify: `update/recovery.go:470-512` (`generateRecoveryKey`, `newRecoveryKey`, `canonicalRecoveryKey`, `modhexAlphabet`, `recoveryKeyShape` become thin calls into the package), `update/recovery_cli.go:146-180` (`recoveryKeyLines`, `confirmsGroup`)

**Interfaces:**
- Produces:
  ```go
  package recoverykey
  func Generate(r io.Reader) (string, error)   // 8 groups of 8 modhex, dash-separated
  func New() (string, error)                   // Generate(crypto/rand.Reader)
  func Canonical(s string) string              // "" when not a key
  func Lines(key string) []string              // two groups per line: "aaaaaaaa-bbbbbbbb"
  func ConfirmsGroup(key string, group int, typed string) bool // group is 1-based; spaces and case ignored
  ```
- Consumed by Task 12 (the recovery key dialog) and Task 14 (the backup password dialog). `update/backup.go` `newBackupKey` stays `= newRecoveryKey`.

- [ ] **Step 1: Move the existing tests.** The tests of these functions in `update/recovery_test.go` and `update/recovery_cli` tests move into `recoverykey_test.go`, with the same cases. Add `TestLinesTwoGroupsPerLine`: 4 lines of 17 characters.
- [ ] **Step 2: Run them and see them fail.** `cd update && go test ./recoverykey/`. Expected: FAIL, because the package has no code yet.
- [ ] **Step 3: Implement the package.** The old names in `package main` call into it, so no call site changes.
- [ ] **Step 4: Run the tests again.** `cd update && go test ./...`. Expected: PASS.
- [ ] **Step 5: Commit** `refactor(update): the recovery key format as an importable package`.

---

### Task 3: Users in the daemon, the model and the OEM file

**Files:**
- Create: `update/users.go`, `update/users_test.go`
- Modify: `update/go.mod`, `update/go.sum` (add `gopkg.in/yaml.v3 v3.0.1`)

**Interfaces:**
- Produces (package `main`, used by Tasks 4 and 5):
  ```go
  var (
      passwdFile = "/etc/passwd"
      groupFile  = "/etc/group"
      shadowFile = "/etc/shadow"
      usersOEM   = "/oem/95_isotope_users.yaml"
      usersRun   cmdRunner = execCmd // useradd, usermod, userdel, gpasswd, chpasswd, openssl
      loggedIn   = func(name string) (bool, error) { /* logind ListUsers over the system bus */ }
  )
  type userInfo struct {
      Name, FullName, Home string
      UID                  uint32
      Admin                bool
  }
  func listUsers() ([]userInfo, error)                  // uid 1000..59999, from passwdFile and groupFile
  func hashPassword(plain string) (string, error)       // openssl passwd -6 -stdin
  func verifyPassword(name, plain string) error         // nil, errWrongPassword, errUnverifiable
  func addUser(name, fullName string, admin bool, password string) error
  func removeUser(name string, keepHome bool, caller string) error
  func setPassword(name, password string) error
  func setAdmin(name string, on bool) error
  var errLastAdmin, errSystemAccount, errNoSuchUser, errUserExists, errSelfRemove,
      errLoggedIn, errBadUserName, errBadFullName, errEmptyPassword,
      errWrongPassword, errUnverifiable error
  ```
- `busError` in `dbus.go` maps every `err…` above to `Refused`.

**What yip v1.27.1 does with this file.** This was read in `pkg/plugins/user.go` and `pkg/executor/default.go`. It decides the format:
- Files in one directory run in lexical order, each one chained after the one before. So `/oem/95_…` runs after the installer's `/oem/90_custom.yaml`.
- A user in a `users:` map that does not exist yet is created with `passwd` (stored as given, so a `$6$` hash), `gecos`, `shell` and the private group. Then the user is added to each listed group that exists. `/etc` is rebuilt at every boot, so this runs at every boot.
- A user that already exists, such as the installer's user created by the earlier file, gets **only** its `passwd` set. Groups and gecos are ignored.
- Groups are only ever added, never removed.

So the file holds two kinds of entries:
- Our own users, as full `users:` entries.
- Overrides of users we did not create (the installer's user): the password in a `users:` entry with `passwd` only, and the rest as boot `commands` after it (`usermod -c`, `gpasswd -a|-d NAME admin`, `userdel NAME`).

The daemon's own state is the top-level key `isotope_users`. yip ignores keys it does not know. The daemon reads only that key and renders `stages:` from it. Format:

```yaml
# Written by isotope-updated (Isotope System, isotope users). Do not edit.
name: "Isotope users"
isotope_users:
  own:
    bob: {gecos: "Bob", passwd: "$6$…", admin: false}
  overrides:
    anna: {gecos: "Anna B", passwd: "$6$…", admin: false, removed: false}
stages:
  boot:
    - name: "Isotope users"
      users:
        bob: {passwd: "$6$…", gecos: "Bob", shell: /bin/bash, groups: [audio, video, render, input, bluetooth, seat, docker]}
        anna: {passwd: "$6$…"}
    - name: "Isotope user changes"
      commands:
        - usermod -c 'Anna B' anna
        - gpasswd -d anna admin
```

- [ ] **Step 1: Write the failing tests.** Each test points the variables above into `t.TempDir()`, with a fake `usersRun` that records the name, args and stdin, and a fake `loggedIn`.

```go
func TestListUsersSkipsSystemAccountsAndMarksAdmins(t *testing.T) // passwd: root:0, daemon:2, anna:1000, bob:1001, nobody:65534; group admin:x:..:anna → anna Admin, bob not, 2 rows, FullName from gecos field 1
func TestAddUserWritesTheOEMFileThenRunsUseradd(t *testing.T)    // file has own.bob and the stage entry with groups incl. admin when admin=true; groups not in groupFile are dropped; useradd args: -m -c Bob -s /bin/bash -G <groups> bob; then chpasswd -e with stdin "bob:$6$…\n"; no argv contains the password or the hash; file mode 0600
func TestAddUserRefusesBadNamesAndExisting(t *testing.T)          // "Bob", "1x", "a b", 33 chars → errBadUserName; "anna" (exists) → errUserExists; full name with ':' or newline or > 64 chars → errBadFullName; empty password → errEmptyPassword; nothing written, nothing run
func TestFailedLiveApplyRestoresTheOEMFile(t *testing.T)          // usersRun fails on useradd → the OEM file has its previous bytes
func TestRemoveUserGuards(t *testing.T)                           // root, uid 999 → errSystemAccount; the last admin → errLastAdmin; unknown → errNoSuchUser
func TestRemoveUserRefusesSelfAndLoggedIn(t *testing.T)           // caller == name → errSelfRemove; loggedIn true → errLoggedIn; nothing run
func TestRemoveOwnUserDropsItsEntry(t *testing.T)                 // own.bob gone; userdel bob (keepHome) / userdel -r bob (not)
func TestRemoveInstallerUserIsAnOverride(t *testing.T)            // overrides.anna.removed; commands contain "userdel anna"; no users: entry for anna
func TestSetAdminOffRefusesTheLastAdmin(t *testing.T)
func TestSetAdminOnInstallerUserAddsACommand(t *testing.T)        // "gpasswd -a anna admin" live and in commands; off → "gpasswd -d anna admin"
func TestSetPasswordOfInstallerUserIsAPasswdOnlyEntry(t *testing.T) // users: anna: {passwd: $6$…} and nothing else for anna
func TestVerifyPassword(t *testing.T)                             // shadow "$6$salt$hash": runs openssl passwd -6 -salt salt -stdin, compares → nil / errWrongPassword; "$y$…" or "$6$rounds=…" → errUnverifiable
func TestRenderedFileRoundTrips(t *testing.T)                     // load(render(state)) == state; gecos "O'Brien" is quoted 'O'\''Brien' in commands
```

- [ ] **Step 2: Run them and see them fail.** `cd update && go test -run 'Users|User|Password|Rendered' .`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement `users.go`.**
  - The name rule is the installer's `^[a-z_][a-z0-9_-]{0,31}$` (`install-ui/core/wizard.go:45`).
  - The group list is the installer's `desktopGroups` without `admin`: `audio, video, render, input, bluetooth, seat, docker`. Add `admin` when the user is an admin. Keep only the groups in `groupFile`, because the server has fewer. Add a comment that names `install-ui/core/config.go:48` as the list to keep in step.
  - A `usersMu sync.Mutex` serialises every change.
  - "Last admin" means the count of `admin` members with uid ≥ 1000 after the change is 0.
  - Run the commands as `useradd`, `usermod -c`, `gpasswd -a|-d`, `userdel [-r]`, and `chpasswd -e` with `name:hash` on stdin.
- [ ] **Step 4: Run the tests again.** `cd update && go test ./...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(update): users that survive a reboot, in /oem/95_isotope_users.yaml`.

---

### Task 4: Users on the bus

**Files:**
- Create: `update/users_bus.go`, `update/users_bus_test.go`
- Modify: `update/dbus.go` (constant `actionUsers = "io.kairos.isotope-update.users"`, introspection XML, `busError` cases, `service.uidOf`, `serve` sets it)
- Modify: `rootfs/usr/share/polkit-1/actions/io.kairos.isotope-update.policy` (action `io.kairos.isotope-update.users`, message "Authentication is required to manage the users of this computer")
- Modify: `rootfs/usr/share/polkit-1/rules.d/50-isotope-update.rules` (comment only: "… manage users …")

**Interfaces (D-Bus, exact):**
```xml
<method name="Users"><arg name="users" type="aa{sv}" direction="out"/></method>
<method name="AddUser">
  <arg name="name" type="s" direction="in"/>
  <arg name="full_name" type="s" direction="in"/>
  <arg name="admin" type="b" direction="in"/>
  <arg name="password" type="h" direction="in"/>
</method>
<method name="RemoveUser">
  <arg name="name" type="s" direction="in"/>
  <arg name="keep_home" type="b" direction="in"/>
</method>
<method name="SetPassword">
  <arg name="name" type="s" direction="in"/>
  <arg name="secrets" type="h" direction="in"/>
</method>
<method name="SetAdmin">
  <arg name="name" type="s" direction="in"/>
  <arg name="on" type="b" direction="in"/>
</method>
```
- `Users` takes no polkit check. Each dict has `name` s, `full_name` s, `uid` u, `admin` b, `home` s.
- The `AddUser` fd has one line, the password.
- The `SetPassword` fd has two lines: the current password (may be empty) and the new one.
- Go: `func (a busAPI) Users() ([]map[string]dbus.Variant, *dbus.Error)`, `AddUser(sender dbus.Sender, name, fullName string, admin bool, fd dbus.UnixFD) *dbus.Error`, `RemoveUser(sender dbus.Sender, name string, keepHome bool) *dbus.Error`, `SetPassword(sender dbus.Sender, name string, fd dbus.UnixFD) *dbus.Error`, `SetAdmin(sender dbus.Sender, name string, on bool) *dbus.Error`.
- `service` gains `uidOf func(dbus.Sender) (uint32, error)` (`org.freedesktop.DBus.GetConnectionUnixUser`) and the package variable `userName = func(uid uint32) (string, error)` (os/user), which tests fake.

**SetPassword rule.** This is the design's "own password: the user", made safe:
- When the caller's user name equals `name`: no polkit check, but the sandbox check (split `authorize` into `notSandboxed(sender) *dbus.Error` plus the polkit part). Line 1 of the fd must verify (`verifyPassword`). An admin changing their own password also gives the current one.
- Otherwise: `authorize(sender, actionUsers)` first, and line 1 is ignored.
- This is the one method that reads an argument (`name`) before polkit, and only to compare it to the caller's own name. Say so in a comment.

- [ ] **Step 1: Write the failing tests.** Use the `startService`/`fakeAuth` fixtures, `users.go` faked as in Task 3, and `userName` faked so that the test process's uid maps to `anna`.

```go
func TestUsersNeedsNoPolkit(t *testing.T)                     // 0 questions; dicts carry name/full_name/uid(uint32)/admin/home
func TestUserMethodsAskTheUsersAction(t *testing.T)           // AddUser, RemoveUser, SetAdmin, SetPassword("bob") with allow=false → NotAuthorized; every question is actionUsers; nothing run
func TestAddUserReadsThePasswordFromTheFd(t *testing.T)       // fd "s3cret\n" → hashPassword stdin "s3cret"; the D-Bus message has no string arg containing it
func TestSetPasswordSelfNeedsTheCurrentPassword(t *testing.T) // caller anna, name anna, allow=false: wrong current → Refused (errWrongPassword), right → chpasswd ran; polkit never asked
func TestSetPasswordSelfRefusesAnUnverifiableHash(t *testing.T) // shadow "$y$…" → Refused with "change it with passwd in a terminal"
func TestUserGuardsAreRefused(t *testing.T)                   // last admin → error name Refused, message is errLastAdmin's sentence
func TestUserMethodsCloseTheFdWhenRefused(t *testing.T)       // as TestRecovery*… does for its fds
```

- [ ] **Step 2: Run them and see them fail.** `cd update && go test -run 'User|SetPassword' .`. Expected: FAIL.
- [ ] **Step 3: Implement the methods, the policy entry and the introspection.** `test/update/check-tray-wiring.sh` gains `check "the polkit policy declares the users action"`, the same shape as the recovery line, and the same for `restore` (Task 7 adds that action; add both lines here).
- [ ] **Step 4: Run the tests again.** `cd update && go test ./...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(update): manage users over D-Bus, behind the users action`.

---

### Task 5: `isotope users` CLI

**Why a CLI.** The design's non-goals say "Every page has a CLI equivalent; the CLI stays the reference", and AGENTS.md says a new user-facing command is a subcommand of `isotope`. The server edition has no app, and `useradd` alone is lost at reboot, so without the CLI the server has no lasting way to add a user.

**Files:**
- Create: `update/users_cli.go`, `update/users_cli_test.go`
- Modify: `update/main.go` (`case "users": return cmdUsers(args, stdin, stdout, stderr)`, usage text), `update/client.go` (client methods)

**Interfaces:**
- CLI:
  ```
  isotope users                       list: NAME  FULL NAME  ADMIN
  isotope users add NAME [--full-name TEXT] [--admin]   asks the password twice
  isotope users remove NAME [--delete-home]
  isotope users passwd [NAME]         NAME defaults to the caller; asks the current one for yourself
  isotope users admin NAME on|off
  ```
- Client: `func (c *client) Users() ([]UserRow, error)`, `AddUser(name, fullName string, admin bool, password string) error`, `RemoveUser(name string, keepHome bool) error`, `SetPassword(name, current, password string) error`, `SetAdmin(name string, on bool) error`. Secrets go through `withSecrets`. Prompts go through the existing `terminal` interface (`ttyTerminal.Secret`).
- `var dialUsers = func() (usersClient, error)`, so the tests fake it, as `dialRecovery` does.

- [ ] **Step 1: Write the failing tests.**

```go
func TestUsersListPrintsAColumnPerField(t *testing.T)
func TestUsersAddAsksTwiceAndRefusesAMismatch(t *testing.T)   // "a" then "b" → exit 1, "The passwords do not match", no AddUser call
func TestUsersPasswdForYourselfAsksTheCurrentOne(t *testing.T) // current user anna (fake), NAME omitted → prompts "Current password", "New password", "Again"
func TestUsersRemoveKeepsTheHomeByDefault(t *testing.T)       // RemoveUser("bob", true); with --delete-home → false
func TestUsersUsage(t *testing.T)                             // unknown verb → exit 2
```

- [ ] **Step 2: Run them and see them fail.** `cd update && go test -run TestUsers .`. Expected: FAIL.
- [ ] **Step 3: Implement it.** Daemon errors print through `describeDaemonError`.
- [ ] **Step 4: Run the tests again.** `cd update && go test ./...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(update): isotope users`.

---

### Task 6: Backup targets: USB disk by UUID, remote credentials, SFTP keys, `BackupInit`

**Files:**
- Create: `update/backup_target.go`, `update/backup_target_test.go`
- Modify: `update/backup.go`:
  - `backupConfig` gains `Label string`, saved as `label=`.
  - `normalizeTarget` accepts `uuid:`.
  - `targetPresent`: a `uuid:` target is present when `/dev/disk/by-uuid/<UUID>` exists.
  - `resticCmd` becomes `resticCmdEnv(p, target, passFile, envFile string, args ...string)`, with `resticCmd` calling it with `p.envFile()`.
  - `runConfiguredBackup`, `backupList` and `backupInit` resolve the target through `resolveTarget`.
- Modify: `update/restore.go` (`restorer` gains `envFile string`; `restic()` and `resolveSnapshot` use `resticCmdEnv`), `update/backup_export.go` (no change in behaviour)
- Modify: `update/backup_bus.go` (`BackupStatus` gains `label` s and `kind` s, one of `disk`, `dir`, `rest`, `s3`, `sftp`, `other`, `""`)
- Modify: `update/dbus.go` (introspection)

**Interfaces:**
- Target forms, stored in `config` as `target=`:
  - `uuid:<UUID>/<path>`: a filesystem by UUID and a folder in it (new);
  - `/abs/dir`;
  - `rest:https://host[:port]/path`;
  - `s3:https://endpoint/bucket/path`;
  - `sftp://user@host:port//abs/path`, or `sftp:user@host:/path` when the port is 22.
- Go:
  ```go
  func parseUUIDTarget(t string) (uuid, sub string, ok bool)
  // resolveTarget turns a target into what restic opens. A uuid: target is
  // looked up in mountinfo; when it is not mounted, it is mounted through
  // udisks2 (as root) and release unmounts it again.
  func resolveTarget(target string) (repo string, release func(), err error)
  var mountByUUID = func(uuid string) (dir string, unmount func(), err error) // org.freedesktop.UDisks2 Filesystem.Mount/Unmount
  func sshDir(p backupPaths) string // p.State + "/ssh": id_ed25519, id_ed25519.pub, known_hosts
  func sftpArgs(p backupPaths) []string // ["-o", "sftp.args=-i <key> -o UserKnownHostsFile=<known_hosts> -o StrictHostKeyChecking=yes -o BatchMode=yes"], added by resticCmdEnv for sftp targets
  func ensureSSHKey(p backupPaths) (pub string, err error) // ssh-keygen -t ed25519 -N "" -C isotope-backup@<hostname>, once
  func scanHostKey(p backupPaths, host string, port uint32) (fingerprints []string, err error) // ssh-keyscan -p, kept in p.Run/hostkeys-<host>-<port>; ssh-keygen -lf → "SHA256:…"
  func acceptHostKey(p backupPaths, host string, port uint32, fingerprint string) error // appends the scanned line whose fingerprint matches to known_hosts
  func initTarget(p backupPaths, target string, opt initOptions, password string, env []string) error
  type initOptions struct{ Containers, Existing bool; Label, HostKey string; Port uint32 }
  var allowedEnv = []string{"RESTIC_REST_USERNAME", "RESTIC_REST_PASSWORD", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_DEFAULT_REGION"}
  ```
- D-Bus (exact), all under `actionBackup`:
  ```xml
  <method name="BackupInit">
    <arg name="target" type="s" direction="in"/>
    <arg name="options" type="a{sv}" direction="in"/>
    <arg name="secrets" type="h" direction="in"/>
  </method>
  <method name="BackupSSHKey"><arg name="public_key" type="s" direction="out"/></method>
  <method name="BackupHostKey">
    <arg name="host" type="s" direction="in"/>
    <arg name="port" type="u" direction="in"/>
    <arg name="fingerprints" type="as" direction="out"/>
  </method>
  ```
  - `options` keys: `containers` b, `existing` b, `label` s, `host_key` s, `port` u.
  - The `secrets` fd: line 1 is the backup password. For a new backup the app generates it with `recoverykey.New` and has it confirmed before the call. Lines 2 and on are `NAME=value`, with NAME in `allowedEnv`. Any other name is refused.
  - `BackupInit` returns nothing. The password never comes back over the bus. See the decisions in the report.

`initTarget` is the CLI's `backupInit` with the questions answered:
- The env lines go to `p.Run/env.new` (`writeSecret`, 0600) and become `p.envFile()` only after `restic init` (or `cat config` when `existing`) succeeds. The same goes for the password (`password.new`).
- An SFTP target runs `acceptHostKey` first and is refused without `host_key`.
- A `uuid:` target creates `<mount>/<path>` after `checkOnDisk(<mount>)`.
- The CLI's `backupInit` keeps its prompts and calls `initTarget` for the work.

- [ ] **Step 1: Write the failing tests.** Fake `sysCommand` and `mountByUUID`, and point `mountinfoPath` at a fixture.

```go
func TestNormalizeTargetAcceptsUUID(t *testing.T)               // "uuid:1234-ABCD/isotope-backup" kept; "uuid:" or "uuid:x/../y" refused
func TestResolveTargetFindsAMountedUUID(t *testing.T)           // mountinfo has /dev/sdb1 at /run/media/anna/USB, by-uuid link → repo /run/media/anna/USB/isotope-backup, release no-op
func TestUUIDTargetUnmountsWhatItMounted(t *testing.T)          // not in mountinfo → mountByUUID called, release calls its unmount once
func TestUUIDTargetNotPresentIsSkipped(t *testing.T)            // scheduled run with no by-uuid link → skip "… is not plugged in", mountByUUID never called, restic never run
func TestInitTargetWritesEnvOnlyAfterRestic(t *testing.T)       // restic init fails → no p.envFile(), no p.password(); succeeds → both 0600, config target/label set
func TestInitTargetRefusesUnknownEnv(t *testing.T)              // "PATH=/tmp" → error, nothing written
func TestSFTPNeedsAConfirmedHostKey(t *testing.T)               // no host_key → refused; wrong fingerprint → refused; right → known_hosts has the "[host]:2222 ssh-ed25519 …" line
func TestResticCmdAddsSFTPArgs(t *testing.T)                    // sftp target → args contain "-o" "sftp.args=-i …/ssh/id_ed25519 … StrictHostKeyChecking=yes …"
func TestBusBackupInitReadsTheFd(t *testing.T)                  // password and env from the fd; allow=false → NotAuthorized and fd closed
func TestBackupStatusSaysKindAndLabel(t *testing.T)
```

- [ ] **Step 2: Run them and see them fail.** `cd update && go test -run 'Target|SFTP|ResticCmd|BackupInit|BackupStatusSays' .`. Expected: FAIL.
- [ ] **Step 3: Implement it.** The CLI's `isotope backup status` prints `Target: <label> (USB disk)` for a `uuid:` target.
- [ ] **Step 4: Run the tests again.** `cd update && go test ./...`. Expected: PASS. Every existing backup and restore test still passes.
- [ ] **Step 5: Commit** `feat(update): back up to a USB disk by UUID or a remote target, set up over D-Bus`.

---

### Task 7: Backup jobs: list, export, cancel, restore

**Files:**
- Create: `update/backup_jobs.go`, `update/backup_jobs_test.go`
- Modify: `update/daemon.go`:
  - `StateRestore State = "restore"`.
  - `busyLocked`, `Cancel`, `Reboot` and `RebootPrevious` treat it like `StateBackup`.
  - The `backup` field becomes `backup func(ctx context.Context, progress func(string)) (string, error)`.
  - New `export` and `restore` fields of the same shape, so the tests fake them.
- Modify: `update/backup.go`: `runConfiguredBackup` and `backupOnce` take a `ctx`. restic runs through `runCtx(ctx, cmd) error`, which kills the process on `ctx.Done()`.
- Modify: `update/backup_bus.go`: `BackupStatus` gains `can_restore` b and `restore_refusal` s.
- Modify: `update/trayapp.go:344` `busyState`, `update/menu.go` `statusLine` (`StateRestore`: "Restoring a backup…").
- Modify: `update/dbus.go` (`actionRestore = "io.kairos.isotope-update.restore"`, introspection), the polkit `.policy` (action `restore`).

**Interfaces (D-Bus, exact):**
```xml
<method name="BackupList">
  <arg name="source" type="s" direction="in"/>
  <arg name="options" type="a{sv}" direction="in"/>
  <arg name="secrets" type="h" direction="in"/>
  <arg name="snapshots" type="aa{sv}" direction="out"/>
</method>
<method name="BackupExport"><arg name="path" type="s" direction="in"/></method>
<method name="BackupCancel"/>
<method name="Restore">
  <arg name="source" type="s" direction="in"/>
  <arg name="snapshot" type="s" direction="in"/>
  <arg name="host" type="s" direction="in"/>
  <arg name="options" type="a{sv}" direction="in"/>
  <arg name="secrets" type="h" direction="in"/>
</method>
```

**BackupList** (`actionBackup`):
- `source` "" means the configured target with the stored password and env. The fd may then be empty.
- Otherwise `source` is a target string or an export file path, and the fd is as in `BackupInit`.
- `options` has `host_key` and `port` for SFTP.
- It returns snapshot dicts with `id` s (short id), `time` x (unix), `host` s and `size` t (restic's `summary.total_bytes_processed`, 0 when missing), newest first.
- It is synchronous, and the client calls it with a 120 s timeout.

**BackupExport** (`actionBackup`):
- A job in the slot, `StateBackup`, detail "Exporting to <file>". It runs `exportTo` with `passStdin=false`.
- Without `p.password()` it is refused: "Set up a backup first; the file is encrypted with its password".
- The file is chowned to the caller's uid and primary gid (`service.uidOf`), so the user can copy it.
- The outcome goes in `backup_result`/`backup_detail`, as for `BackupNow`.

**BackupCancel** (`actionBackup`): cancels a running backup or export job. restic is killed, and the result is `failed` with "Cancelled". It returns `NotCancellable` for `StateRestore`.

**Restore** (`actionRestore`):
- It is refused first unless `machineIsNew`. The refusal is `Refused` with "This machine is already in use; restore parts of a backup with isotope restore --only".
- Then it is a job in the slot, `StateRestore`. It runs `restorer.run(source, true)` with:
  - `r.in = strings.NewReader(password + "\n")`, so the CLI's own password read is reused;
  - `r.snap = snapshot` ("" means "latest") and `r.host = host`;
  - `r.envFile` = a 0600 file in `p.Run` built from the fd's env lines, removed at the end;
  - stdout and stderr through `lineWriter`.
- It ends in the restart, as the CLI does.

**machineIsNew:**
```go
// machineIsNew is the Welcome design's rule: one human user (uid ≥ 1000),
// no backup target and no backup ever, no restore pending or finished, and
// that user's welcome stamp not written. reason is the first rule that fails.
func machineIsNew(p backupPaths) (ok bool, reason string)
```
- The stamps are `<home>/.local/state/isotope/welcome`, `…/isotope/first-run-apps`, `…/isotope/welcomed` and their `hadron/` copies.
- `can_restore` and `restore_refusal` in `BackupStatus` come from it.

- [ ] **Step 1: Write the failing tests.**

```go
func TestBackupListOfTheConfiguredTarget(t *testing.T)   // fake restic "snapshots --json --tag isotope" output → 2 dicts newest first, size from summary
func TestBackupListOfAnExportFile(t *testing.T)          // a tar from packDir is unpacked under p.State/work-*, and removed after
func TestBackupExportChownsToTheCaller(t *testing.T)     // the job's file owner uid = the fake caller uid (chown faked through a variable)
func TestBackupExportWithoutAPasswordIsRefused(t *testing.T)
func TestBackupCancelStopsTheJob(t *testing.T)           // job blocks on ctx; BackupCancel → result failed "Cancelled", state back to prev
func TestRestoreRefusesAMachineInUse(t *testing.T)       // two users → Refused; the fake restore func never ran; nothing read from the fd beyond closing it
func TestRestoreRunsAsAJob(t *testing.T)                 // machine new → StateRestore, progress lines, restore func got source/snapshot/host/password
func TestRestoreIsNotCancellable(t *testing.T)
func TestMachineIsNew(t *testing.T)                      // one rule broken per case → ok false with that reason
func TestRestoreAsksTheRestoreAction(t *testing.T)       // question is actionRestore; BackupList/Export/Cancel ask actionBackup
func TestTrayKnowsTheRestoreState(t *testing.T)          // busyState(StateRestore) true, statusLine says "Restoring a backup…"
```

- [ ] **Step 2: Run them and see them fail.** `cd update && go test -run 'BackupList|BackupExport|BackupCancel|Restore|MachineIsNew|TrayKnows' .`. Expected: FAIL.
- [ ] **Step 3: Implement it.**
- [ ] **Step 4: Run the tests again.** `cd update && go test ./...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(update): list, export and restore backups over D-Bus`.

---

### Task 8: Update history in the daemon

**Where the data comes from.** The daemon records each install itself when the job ends, in `/usr/local/isotope-update/history`. The alternatives do not work:
- The kairos-agent upgrade log is prose for people. It names an `oci:` source, not a release.
- The boot entries name only two slots (active and passive), with no dates.
- The daemon's current state is not a history at all.

The daemon already holds the ref, digest, version (from the inspect) and outcome at that moment. Every install, from the app, the CLI or the tray, goes through it. `/usr/local` survives reboots, and `/var` does not. Installs made before this change are not in the file. The page then still shows the installed release and the boot entry names, as phase 1 does.

**Files:**
- Create: `update/history.go`, `update/history_test.go`
- Modify: `update/daemon.go` (`run`: append at the end of every Apply job; `RebootPrevious`: append a `rollback` entry after `backend.RebootPrevious()` succeeds), `update/dbus.go` (method, introspection)

**Interfaces:**
```go
var historyFile = "/usr/local/isotope-update/history" // one line per entry, tab-separated: unix time, kind, version, ref, digest, result
type historyEntry struct {
    Time                         int64
    Kind                         string // "install" | "rollback"
    Version, Ref, Digest, Result string // Result: "installed" | "failed" | "cancelled" | "" for rollback
}
func appendHistory(e historyEntry) error // keeps the newest 200 lines, writeRunFile-style atomic rename
func readHistory() ([]historyEntry, error) // newest first; a malformed line is skipped
```
```xml
<method name="UpdateHistory"><arg name="entries" type="aa{sv}" direction="out"/></method>
```
- No polkit check.
- Each dict has `time` x, `kind` s, `version` s, `ref` s, `digest` s, `result` s.

- [ ] **Step 1: Write the failing tests.**

```go
func TestApplyRecordsTheOutcome(t *testing.T)     // fakeBackend success → one "install … installed" line; failure → "failed"; Cancel during inspecting → "cancelled"
func TestRollbackIsRecorded(t *testing.T)
func TestHistoryKeepsTheNewest200(t *testing.T)
func TestHistorySkipsMalformedLines(t *testing.T)
func TestBusUpdateHistory(t *testing.T)           // no polkit question; dicts newest first
```

- [ ] **Step 2: Run them and see them fail.** `cd update && go test -run 'History|Records|Rollback' .`. Expected: FAIL.
- [ ] **Step 3: Implement it.** A failed write is logged to stderr and never fails the job.
- [ ] **Step 4: Run the tests again.** `cd update && go test ./...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(update): keep a history of installs`.

---

### Task 9: The app's client of the new daemon methods

**Files:**
- Modify: `system/bus.go` (types, `Daemon` interface, `busDaemon` methods, `withSecrets` twin, `Status.Busy` and `StateRestore`)
- Modify: `system/ui_test.go` (`fakeDaemon` implements the new methods and records their calls)
- Create: `system/bus_test.go` (dict decoding)

**Interfaces (system/, Go):**
```go
const StateRestore = "restore"

type User struct {
    Name, FullName, Home string
    UID                  uint32
    Admin                bool
}
type Credentials struct {
    Password string
    Env      []string // NAME=value
}
type TargetOptions struct {
    Containers, Existing bool
    Label, HostKey       string
    Port                 uint32
}
type Snapshot struct {
    ID, Host string
    Time     int64
    Size     uint64
}
type HistoryEntry struct {
    Time                                int64
    Kind, Version, Ref, Digest, Result string
}

// BackupStatus gains: Label, Kind, RestoreRefusal string; CanRestore bool.

// Added to the Daemon interface:
Users() ([]User, error)
AddUser(name, fullName string, admin bool, password string) error
RemoveUser(name string, keepHome bool) error
SetPassword(name, current, password string) error
SetAdmin(name string, on bool) error
BackupInit(target string, opt TargetOptions, cred Credentials) error
BackupSSHKey() (string, error)
BackupHostKey(host string, port uint32) ([]string, error)
BackupList(source string, opt TargetOptions, cred Credentials) ([]Snapshot, error)
BackupExport(path string) error
BackupCancel() error
Restore(source, snapshot, host string, opt TargetOptions, cred Credentials) error
RecoveryNewKey(current, newKey string) error // already on the daemon (update/recovery_bus.go); new in the app
RecoveryReseal(key string) error             // idem
UpdateHistory() ([]HistoryEntry, error)
```
- `optionsDict(TargetOptions) map[string]dbus.Variant` omits zero values.
- `BackupInit`, `BackupList` and `BackupHostKey` call with a 120 s context. The others keep the default.
- `Describe` maps the `Refused` error name to the daemon's own sentence, unchanged.

- [ ] **Step 1: Write the failing tests.**

```go
func TestUserFromDict(t *testing.T)
func TestSnapshotsFromDicts(t *testing.T)
func TestHistoryFromDicts(t *testing.T)
func TestOptionsDictOmitsZeroValues(t *testing.T)
func TestWithSecretsJoinsLines(t *testing.T) // reads the pipe: "pw\nAWS_ACCESS_KEY_ID=x\n"
func TestBusyIncludesRestore(t *testing.T)
```

- [ ] **Step 2: Run them and see them fail.** `SYSTEST`. Expected: FAIL.
- [ ] **Step 3: Implement the client.** Extend `fakeDaemon` with fields `users []User`, `snaps []Snapshot`, `history []HistoryEntry`, `sshKey string`, `hostKeys []string`, `err map[string]error` (per method), and record every call as `"Method arg…"`. Secrets are recorded as `"<secret>"`, so a test can assert that they never show in a call string.
- [ ] **Step 4: Run the tests again.** `SYSTEST`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(system): client of the users, backup, restore and history methods`.

---

### Task 10: udisks2 client and the system-device rule

**Files:**
- Create: `system/udisks.go` (client over `org.freedesktop.UDisks2`), `system/disks_model.go`, `system/disks_model_test.go`
- Modify: `system/ui.go` (`Hooks.Disks Disks`), `system/main.go` (dial it; nil when udisks2 is missing), `system/sysinfo.go` (`System.User string`, the current user name), `system/ui_test.go` (`fakeDisks`)

**Interfaces:**
```go
type Block struct {
    ID                       string // object path
    Device, Label, FSType, UUID string
    Size, Used               uint64 // Used from statfs of the first mount point, 0 when not mounted
    MountPoints              []string
    HintSystem, HintIgnore   bool
    IsFilesystem, IsPartition, IsCrypto bool
}
type Drive struct {
    ID                                    string // object path
    Model, Vendor, Serial                 string
    Size                                  uint64
    Removable, Ejectable, CanPowerOff     bool
    Blocks                                []Block // whole disk first, then partitions by number
}
type Disks interface {
    Drives() ([]Drive, error)
    Mount(block string) (mountPoint string, err error)  // Filesystem.Mount, options {}
    Unmount(block string) error                         // Filesystem.Unmount
    Eject(drive string) error                           // Drive.Eject
    PowerOff(drive string) error                        // Drive.PowerOff
    Format(drive, fsType, label string) error           // see Task 13
    Watch() (changed <-chan struct{}, stop func(), err error) // ObjectManager InterfacesAdded/Removed
}
func isSystemDevice(b Block, rootDevices []string) bool
func systemDrive(d Drive, rootDevices []string) bool // true if any block is a system device
func usableForBackup(b Block) bool                   // a filesystem of ext4, exfat, vfat, ntfs, btrfs or xfs, with a UUID, not a system device
var statfs = func(path string) (used uint64, err error)
var rootDevices = func() []string // the devices behind / and /oem, /usr/local from /proc/self/mountinfo
```
`isSystemDevice` returns true for any of:
- a label `COS_*`;
- a label ending `_LUKS`;
- `HintSystem` or `HintIgnore`;
- a device in `rootDevices`.

- [ ] **Step 1: Write the failing tests.**

```go
func TestIsSystemDevice(t *testing.T) // table: COS_PERSISTENT, COS_LIVE, COS_OEM_LUKS, HintSystem, HintIgnore, "/dev/sda2" in rootDevices → true; "USB" exfat removable → false; "" label ext4 → false
func TestSystemDriveIfAnyBlockIs(t *testing.T) // a USB drive with one COS_STATE partition (Kairos on a stick) → systemDrive true
func TestUsableForBackup(t *testing.T)         // exfat with UUID → true; swap, crypto_LUKS, no UUID, system → false
func TestDrivesFromManagedObjects(t *testing.T) // a fixture map[dbus.ObjectPath]map[string]map[string]dbus.Variant (Drive + Block + Partition + Filesystem ifaces) → Drives with blocks grouped by the Block's "Drive" property; MountPoints decoded from aay
```
The decoding is `drivesFrom(objs map[dbus.ObjectPath]map[string]map[string]dbus.Variant) []Drive`, a pure function that the client calls.

- [ ] **Step 2: Run them and see them fail.** `SYSTEST`. Expected: FAIL.
- [ ] **Step 3: Implement the client.** `fakeDisks` in `ui_test.go` holds `drives []Drive`, `calls []string`, `errs map[string]error`, and its `Format` records `"Format <drive> <fs> <label>"`.
- [ ] **Step 4: Run the tests again.** `SYSTEST`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(system): udisks2 client and the rule that keeps system disks read-only`.

---

### Task 11: The Users page

**Files:**
- Create: `system/page_users.go`
- Modify: `system/page_other.go` (remove the `PageUsers` later-page maker), `system/args.go` (`later` drops `PageUsers`)
- Test: `system/ui_test.go`

**Behaviour** (design, "Users (phase 2)"):
- **The list** (`Users`): full name, then the user name and "Administrator" as dim text, "You" on the caller's own row.
- **Add user…**, for admins: a form dialog (`askForm`) with Name, Full name, Password, Password again, and an "Administrator" check. Add stays disabled until the name matches `^[a-z_][a-z0-9_-]{0,31}$`, the passwords match and are not empty. The name field says the rule under it when it does not match.
- **Per row, for admins:**
  - Change password (a form: new password twice; for your own row also "Current password");
  - Administrator on or off (`SetAdmin`; turning it off asks "Remove <name> from the administrators?");
  - Remove…, a dialog with the check "Keep the home folder" (on by default) and the Danger button "Remove <name>". Off on your own row.
- **For a non-admin:** only "Change your password" on their own row. Everything else is off, with "Only an administrator can add, remove or change other users."
- **COSMIC.** A line under the heading says that on Isotope, users are changed here, because changes made elsewhere may not survive a restart.

- [ ] **Step 1: Write the failing tests.**

```go
func TestUsersPageAddsAUser(t *testing.T)            // fill the form fields, pick "Add" → fake call "AddUser bob Bob true <secret>"
func TestUsersPageAddIsOffUntilTheFormIsValid(t *testing.T) // mismatched passwords → choice Enabled false
func TestUsersPageChangesYourOwnPassword(t *testing.T) // non-admin; own row; dialog has a Current password field; call "SetPassword anna <secret> <secret>"
func TestUsersPageRemoveKeepsTheHomeByDefault(t *testing.T) // "RemoveUser bob true"
func TestUsersPageNonAdminSeesNoAdminButtons(t *testing.T) // Footer() returns no Add action enabled; the row actions for others are absent
func TestUsersPageShowsTheDaemonsRefusal(t *testing.T) // fake err "this is the last administrator…" on SetAdmin → tell() with that sentence
```
`TestEveryPageDraws` already draws every page at 1100x720 and 700x500, for admin and non-admin. It now covers Users with a `fakeDaemon.users` of three rows.

- [ ] **Step 2: Run them and see them fail.** `SYSTEST`. Expected: FAIL.
- [ ] **Step 3: Implement the page.**
- [ ] **Step 4: Run the tests again.** `SYSTEST`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(system): the Users page`.

---

### Task 12: The Security page: new recovery key and reseal

**Files:**
- Create: `system/keydialog.go` (shared with Task 14)
- Modify: `system/page_security.go`, `system/go.mod` (nothing to add: `update` is already required with a `replace`)
- Test: `system/ui_test.go`

**Interfaces:**
```go
// keyDialog shows a secret once (recovery key, backup password): the key in
// mono, two groups per line (recoverykey.Lines), "I saved it", then a field
// asking group n (1-based, random in 2..7) typed back; done runs when the
// typed group matches (recoverykey.ConfirmsGroup). Cancel at either step
// runs nothing.
func (u *UI) keyDialog(title, intro, key string, done func())
```
**Flow** (the CLI's `recoveryNewKey`, `update/recovery_cli.go:321`):
1. If the edition is Trusted Boot and the TPM unlocks every container (`tpmWorksEverywhere` rules, mirrored as `tpmUnlocksAll(RecoveryStatus) bool`), the current secret is "".
2. Otherwise a form asks for "The disk passphrase or the current recovery key" (a secret field).
3. The app generates the key with `recoverykey.New()`.
4. `keyDialog` shows it ("Write it down and keep it away from this computer. It is shown only now.").
5. `RecoveryNewKey(current, key)` runs off the UI goroutine, with a spinner.
6. The page reloads `RecoveryStatus`.

**Reseal:**
- Shown only on Trusted Boot when some container has `TPM > 0 && !TPMWorks`, as "Seal to this computer's TPM again".
- When the TPM already works everywhere, the page says "The TPM unlocks the disk; nothing to reseal" and shows no button.
- Otherwise a form asks for the recovery key. `RecoveryReseal(key)`.
- The terminal hint `isotope recovery new-key` is replaced by these buttons. Non-admins keep the notice.

- [ ] **Step 1: Write the failing tests.**

```go
func TestNewRecoveryKeyAsksTheCurrentSecretThenConfirms(t *testing.T) // passphrase disk: form → key dialog → type the asked group → call "RecoveryNewKey <secret> <secret>"; the generated key matches recoverykey.Canonical
func TestNewRecoveryKeyWrongGroupDoesNothing(t *testing.T)
func TestNewRecoveryKeySkipsTheSecretWhenTheTPMUnlocks(t *testing.T) // Trusted Boot, TPMWorks everywhere → no secret form
func TestResealOnlyWhenTheTPMFails(t *testing.T)
```

- [ ] **Step 2: Run them and see them fail.** `SYSTEST`. Expected: FAIL.
- [ ] **Step 3: Implement it.**
- [ ] **Step 4: Run the tests again.** `SYSTEST`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(system): replace the recovery key and reseal from the Security page`.

---

### Task 13: Disks: the polkit rule, exFAT tools and the Disks page

**Files:**
- Modify: `rootfs/usr/share/polkit-1/rules.d/50-isotope-udisks.rules`
- Create: `test/polkit/rules-harness.js`, `test/polkit/check-polkit-rules.sh`
- Modify: `.github/workflows/ci.yml` (step "polkit rules" runs `test/polkit/check-polkit-rules.sh`)
- Modify: `test/boot-ux/check-boot-units.sh:137` (also assert `modify-device"` and `isInGroup("admin")` in the rule; assert `mkfs.exfat` and `exfatlabel` run, `--version`)
- Modify: `Dockerfile` (new `exfatprogs` stage after `libblockdev`; copied in `desktop-default`), `Dockerfile.agent` (remove `/usr/sbin/mkfs.exfat /usr/sbin/fsck.exfat /usr/sbin/tune.exfat /usr/sbin/exfatlabel /usr/sbin/dump.exfat /usr/sbin/exfat2img` and assert that they are gone)
- Create: `system/page_disks.go`
- Modify: `system/udisks.go` (`Format`), `system/page_other.go` and `system/args.go` (drop the Disks later page)
- Test: `system/ui_test.go`

**The rule.** Its text follows the design ("Disks (phase 2)"): local, active and in `admin` gives YES for `org.freedesktop.udisks2.modify-device`, and any other subject gets NO.
```js
// Formatting a removable disk (Isotope System's Disks page): an
// administrator at the screen, without a password; nobody else, and never
// from an inactive or remote session. System disks ask for
// modify-device-system, which no Isotope rule grants.
polkit.addRule(function(action, subject) {
    if (action.id == "org.freedesktop.udisks2.modify-device") {
        return (subject.local && subject.active && subject.isInGroup("admin"))
            ? polkit.Result.YES : polkit.Result.NO;
    }
});
```
The existing rule already grants `filesystem-mount` to every local active subject, which covers the mount after a format. That line is unchanged.

**The harness** (`rules-harness.js`, plain node with no packages):
- It defines `polkit = {Result: {YES:"yes", NO:"no", AUTH_ADMIN:"auth_admin"}, addRule(f) {rules.push(f)}}`.
- It `vm.runInThisContext`s each rules file given on the command line.
- It runs a case table `[{action, local, active, groups, want}]`. Each case calls the rules in order, takes the first non-undefined result, and `undefined` counts as "fallthrough". A mismatch prints FAIL and exits 1.

Cases:

| File | Action | Subject | Want |
|---|---|---|---|
| udisks | `modify-device` | admin, local, active | yes |
| udisks | `modify-device` | admin, remote | no |
| udisks | `modify-device` | admin, inactive | no |
| udisks | `modify-device` | non-admin, local, active | no |
| udisks | `modify-device-system` | admin, local, active | fallthrough |
| udisks | `filesystem-mount` | any local active | yes |
| udisks | `filesystem-mount-system` | admin, local, active | fallthrough |
| udisks | `filesystem-unmount-others` | admin, local, active | fallthrough |
| update | `io.kairos.isotope-update.users` | admin / not | yes / no |
| update | `io.kairos.isotope-update.restore` | admin / not | yes / no |

`check-polkit-rules.sh` runs `node` when it is on PATH, and otherwise `docker run --rm -v repo:/src:ro node:22-alpine node …`.

**exfatprogs:**
- Source: `# renovate: datasource=github-releases depName=exfatprogs/exfatprogs`, `ARG EXFATPROGS_VERSION=` the latest 1.2.x release.
- Build: autotools, `--prefix=/usr --sbindir=/usr/sbin`, `DESTDIR=/exfatprogs`.
- The last `RUN` line runs `/exfatprogs/usr/sbin/mkfs.exfat -V`.

**Format** (`udisks.go`):
1. Unmount every mounted filesystem of the drive. Stop at the first failure and return it.
2. `Block.Format("dos", {"erase": none})` on the whole disk.
3. `PartitionTable.CreatePartitionAndFormat(0, 0, type, "", {}, fsType, {"label": label, "take-ownership": true})`. The type is `"0x07"` for exfat and `"0x83"` for ext4. Owner and group are the user (the `take-ownership` option, udisks2 ≥ 2.7.2), so an ext4 disk is writable without root.
- The label limit is 11 characters for exfat and 16 for ext4. The dialog enforces it.

**The page:**
- One card per drive: model, size, "Removable" or not; then each partition with its label, filesystem, size and "X used of Y".
- A drive where `systemDrive` is true says "Isotope's system disk" and has no buttons.
- Removable drives get Mount, Unmount, Eject and Power off for everyone. Format… is for admins only; non-admins get "Only an administrator can format a disk."
- The page reloads on `Watch` events.

**Format dialog** (the design's text):
- Title: "Erase and format <model>?"
- Body: `"<model>, <size>, <device>."`, then one line per partition, `"<label or 'No label'> (<fs>), <used> used of <size>"`, then, when mounted, `"It is unmounted first."`, then `"Everything on this disk will be erased. This cannot be undone."`.
- Form: filesystem cards "exFAT: works on Windows, macOS and Linux" (default) and "ext4: Linux only"; a Label field.
- Choices: `Cancel` (focus, index 0) and `Erase and format` (`ButtonDanger`, enabled when the label fits).

- [ ] **Step 1: Write the failing tests.**

```go
func TestDisksPageSystemDriveHasNoButtons(t *testing.T)
func TestDisksPageFormatDialogNamesTheDiskAndFocusesCancel(t *testing.T) // body contains model, size, device, each partition line, "cannot be undone"; focus 0; choices[1].Label == "Erase and format"
func TestFormatUnmountsFirstAndStopsOnFailure(t *testing.T) // fakeDisks errs["Unmount"] → no "Format" call, tell() shows the error
func TestDisksPageNonAdminCannotFormat(t *testing.T)
func TestDisksPageMountEjectPowerOff(t *testing.T) // calls recorded with the block/drive ids
```
Also write the harness and run `test/polkit/check-polkit-rules.sh` against the current rule. Expected: the `modify-device` admin case FAILs.

- [ ] **Step 2: Run them and see them fail.** `SYSTEST`; `test/polkit/check-polkit-rules.sh`.
- [ ] **Step 3: Implement the rule, the stage, the Dockerfile.agent removal, `Format` and the page.**
- [ ] **Step 4: Run the checks again.** `SYSTEST` passes; `test/polkit/check-polkit-rules.sh` prints only `ok:` lines. `docker buildx build --target exfatprogs -f Dockerfile .` builds.
- [ ] **Step 5: Commit** `feat(disks): the Disks page, and admins may format a removable disk`.

---

### Task 14: The Backup page, phase 2

**Files:**
- Modify: `system/page_backup.go` (split the setup flow into `system/backup_setup.go`)
- Test: `system/ui_test.go`, `system/model_test.go` (`BackupViewOf` gains Plug in / label lines)

**Behaviour** (Welcome design, "The Backup page", "Disks", "Remote targets"):
- **No target** ("This machine has no backup yet"), for admins:
  - "Back up this machine": a choice between "A USB disk" and "A remote server". "Include Docker and Podman storage" is off by default.
  - **USB:** the list of `usableForBackup` filesystems on removable drives (label, size, free), with "Plug in a USB disk" when there are none (refresh on `Disks.Watch`). The chosen one is mounted when needed (`Disks.Mount`). A Folder field defaults to `isotope-backup`. The target is `"uuid:" + UUID + "/" + folder` and the label is the filesystem label.
  - **Remote:** three cards.
    - REST: URL, user, password. The target is `rest:<url>`, with env `RESTIC_REST_USERNAME`/`RESTIC_REST_PASSWORD`.
    - S3: endpoint, bucket, path, access key ID, secret key, region. The target is `s3:<endpoint>/<bucket>/<path>`, with env `AWS_*`.
    - SFTP: `user@host`, port (22), path. `BackupSSHKey` shows the public key with Copy (the clipboard through Gio's `clipboard.WriteCmd`) and "Add this key to the server's authorized_keys, then Connect". Connect calls `BackupHostKey` and asks "Is this the server's key? SHA256:…". Yes passes `HostKey`.
  - **Continue** offers "Create a new backup" or "Use an existing backup".
    - New: `recoverykey.New()` gives the password, and `keyDialog("The password of this backup", …)` must confirm it before `BackupInit` runs.
    - Existing: a password field; `Existing: true`.
- **With a target:** phase 1's status, schedule and Back up now, plus:
  - "Plug in <label> to back up", with Back up now off, when `!Present`;
  - "Cancel" while this window's backup or export runs (`BackupCancel`);
  - "Export to a file…": a removable filesystem plus a file name, default `isotope-<hostname>-<YYYY-MM-DD>.isotope-backup`, then `BackupExport(mountPoint + "/" + name)`;
  - **Backups**: `BackupList("", …)`, newest first, rows "date · host · size".
- **Restore.** Shown only when `CanRestore`, as "Restore this machine from a backup…": the snapshot chosen, then confirmation text taken from the Welcome design's step 4. `Restore(source, snapshot, host, …)`. Otherwise one line: "Restoring over a machine in use is done in a terminal: isotope restore --only homes,apps,extensions" (and `RestoreRefusal` as dim text).
- **Non-admin:** the phase 1 notice. No setup, no list.

- [ ] **Step 1: Write the failing tests.**

```go
func TestBackupSetupUSBInitsByUUIDAfterTheKeyIsConfirmed(t *testing.T) // fakeDisks with an exfat "USB" UUID 1234-ABCD not mounted → Mount call, then key dialog, then "BackupInit uuid:1234-ABCD/isotope-backup {label:USB} <secret>"; no BackupInit before the group is typed
func TestBackupSetupExistingAsksThePassword(t *testing.T)               // Existing true, password from the field
func TestBackupSetupS3SendsEnvLinesNotStrings(t *testing.T)            // the BackupInit call string has "<secret>" and no "AWS_SECRET" value
func TestBackupSetupSFTPConfirmsTheHostKey(t *testing.T)               // BackupHostKey → question with the fingerprint → BackupInit with HostKey set
func TestBackupPlugInDisablesBackUpNow(t *testing.T)                   // Present false → the "Back up now" Action Disabled, text "Plug in USB to back up"
func TestBackupListShowsNewestFirst(t *testing.T)
func TestBackupExportPathOnTheChosenDisk(t *testing.T)
func TestRestoreHiddenOnAMachineInUse(t *testing.T)                    // CanRestore false → no restore action; the --only hint is shown
func TestRestoreOfferedOnANewMachine(t *testing.T)                     // CanRestore true → confirm → "Restore  <id> <host> …"
```

- [ ] **Step 2: Run them and see them fail.** `SYSTEST`. Expected: FAIL.
- [ ] **Step 3: Implement it.**
- [ ] **Step 4: Run the tests again.** `SYSTEST`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(system): set up a backup, list, export and restore from the Backup page`.

---

### Task 15: Update history on the Updates page

**Files:**
- Modify: `system/page_updates.go`, `system/model.go` (`HistoryLines(h []HistoryEntry, installed string, now time.Time) []string`)
- Test: `system/model_test.go`, `system/ui_test.go`

**Behaviour:**
- A "HISTORY" section under the status lists the newest 10 entries:
  - "v1.1.0 installed · 3 days ago";
  - "v1.2.0 failed · yesterday";
  - "Restarted into the previous system · 2 hours ago".
- The entry whose version equals `Status.Installed` and is the newest `installed` one gets "(running)".
- With an empty history the page keeps phase 1's line: the installed release and the boot entry names.
- The section reloads when an install job ends (`onJobEnd`).

- [ ] **Step 1: Write the failing tests.**

```go
func TestHistoryLines(t *testing.T)            // table incl. failed, cancelled, rollback, "(running)" on the right row only
func TestUpdatesPageShowsHistory(t *testing.T) // fakeDaemon.history set → the page loads it on Shown (call "UpdateHistory")
```

- [ ] **Step 2: Run them and see them fail.** `SYSTEST`. Expected: FAIL.
- [ ] **Step 3: Implement it.**
- [ ] **Step 4: Run the tests again.** `SYSTEST`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(system): the install history on the Updates page`.

---

### Task 16: Wiring, docs and the server

**Files:**
- Modify: `AGENTS.md` (`CLAUDE.md` is a symlink to it):
  - **Isotope System row:** Users and Disks are pages now (drop "not there yet (phase 2)"). Backup sets up USB and remote targets, lists, exports and restores (on a new machine). Security replaces the recovery key and reseals. Updates shows the install history. Users go through the daemon (`Users`, `AddUser`, `RemoveUser`, `SetPassword`, `SetAdmin`, polkit `io.kairos.isotope-update.users`; a user may change their own password with the current one) and `/oem/95_isotope_users.yaml`. Disks go through udisks2 as the user. Tests: `test/system/check-system-image.sh`, `test/polkit/check-polkit-rules.sh`.
  - **Backup and restore row:** "setting up a target and restoring stay with the CLI" becomes the phase 2 text: `BackupInit`/`BackupList`/`BackupExport`/`BackupCancel`/`Restore` (polkit `.backup`, `.restore`; Restore only on a new machine), `uuid:` targets mounted through udisks2 by the scheduled run, `/usr/local/isotope-backup/env` and `ssh/`.
  - **Removable disks row:** `modify-device` for a local, active admin (Format in Isotope System), NO for everyone else; exfatprogs (Dockerfile `exfatprogs` stage) for exFAT; the agent removes it too.
  - **Disk encryption row:** "replacing the key there is phase 2 of its design, the CLI does it today" becomes "the Security page replaces the key and reseals (the key generated in the app, shown once, confirmed by one group, sent through a pipe fd)".
  - **Server edition table:** the "It has" `isotope` cell gains `isotope users` (users kept in `/oem/95_isotope_users.yaml`).
- Modify: `test/server/check-server.sh`: `isotope users --help` exits 0; `openssl`, `useradd`, `usermod`, `userdel`, `gpasswd` and `chpasswd` are on PATH.
- Modify: `test/system/check-system-menu.sh`: the policy declares the `users` and `restore` actions.
- No new Isotope menu or tray entries. The design's menu decision lists exactly System, Update, Extensions, Backup and Recovery key, and Users and Disks are reached through "System". The server-config file list needs no change: `COPY rootfs/usr/share/polkit-1/ /usr/share/polkit-1/` already carries the policy file with the new actions, and the server has no udisks2, so its rule is inert there as today. `Dockerfile.agent` already removes the update service, its policy and rules, and `isotope-system`. Task 13 adds its exfatprogs removal.

- [ ] **Step 1: Write the failing checks.** The `check-server.sh` and `check-system-menu.sh` lines above. Run `test/system/check-system-menu.sh`. Expected: the users/restore lines FAIL before Tasks 4 and 7 merge, and PASS after.
- [ ] **Step 2: Edit `AGENTS.md`.** `git diff AGENTS.md` shows the four rows and the server cell changed and no other row.
- [ ] **Step 3: Run the checks again.** `test/system/check-system-menu.sh`. Expected: all `ok:`.
- [ ] **Step 4: Commit** `docs: Isotope System phase 2 in AGENTS.md; server checks for isotope users`.

---

### Task 17: `test/system/check-system-image.sh`

**Files:**
- Create: `test/system/check-system-image.sh`, written like `test/welcome/check-welcome-image.sh`: `IMAGE FLAVOR`, FLAVOR is sway, i3, cosmic, niri or agent.
- Modify: `.github/workflows/desktop-images.yml` (after "Check Isotope Welcome"), `.github/workflows/release.yml` (same place, `if: matrix.desktop != 'server'`), `.github/workflows/agent-gates.yml` (`… agent-desktop:dev agent`)

**Checks:**

| Check | Condition |
|---|---|
| `/usr/bin/isotope-system` is the Gio program | `ldd` lists libwayland-client; no "not found"; i3 links libX11, the others do not |
| `isotope-system --nonsense` | exits 2 |
| Desktop file | `/usr/share/applications/isotope-system.desktop` exists with `Exec=isotope system` |
| Window rule | on sway, i3 and niri, one in the variant's config (`isotope-system` as `app_id`/`class`/`app-id`); none needed on COSMIC |
| Isotope menu | lists "System" and calls `isotope system` |
| Daemon interface | `isotope-updated`'s introspection names `AddUser`, `BackupInit`, `Restore`, `UpdateHistory` (`strings /usr/bin/isotope \| grep -q`) |
| Policy | declares `users` and `restore` |
| Tools on PATH | `useradd`, `usermod`, `userdel`, `gpasswd`, `chpasswd`, `openssl`, `ssh-keyscan`, `ssh-keygen`, `mkfs.exfat`, `mkfs.ext4` |
| agent | no `isotope-system`, no desktop file, no `mkfs.exfat` |

- [ ] **Step 1: Write the script.** Run it against an image built before this change (`cosmic-desktop:look` on the dev box). Expected: FAIL on `mkfs.exfat` and on the new method names.
- [ ] **Step 2: Wire the workflows.**
- [ ] **Step 3: Run it again** against an image built from this branch: `make build DESKTOP=sway` (or the CI build), then `test/system/check-system-image.sh sway-desktop:dev sway`. Expected: all PASS. If no image can be built locally, say so in the PR and leave it to CI.
- [ ] **Step 4: Commit** `test(system): check Isotope System in a built image`.

---

### Task 18 (conditional): COSMIC Settings without its Users page

**Run this task only if** the booted test of the design ("Users (phase 2)", steps 1 to 4) gave the verdict **HIDE**. If the verdict is KEEP, or is not in yet, skip this task. Then say in the PR that COSMIC Settings' Users page is still shown, and why.

**Files:**
- Modify: `Dockerfile` (the `cosmic-apps` stage, the `cosmic-build cosmic-settings` RUN at :3559)
- Modify: `test/look/check-look.sh` (COSMIC only)
- Modify: `AGENTS.md` (Isotope System row: "COSMIC Settings' Users page is not built: users change in Isotope System")

**The exact Dockerfile change** (the design's feature list, which matches `cosmic-settings/Cargo.toml` at `epoch-1.11.0`: `default = [a11y, avif, linux, single-instance, wgpu, systemd]` and `linux` = the pages + `cosmic-comp-config`, `xdg-portal`, `wayland`):
```dockerfile
# COSMIC Settings without its Users page: accountsservice writes only /etc,
# which Kairos rebuilds at every boot, so users added there are lost (booted
# test, docs/superpowers/specs/2026-11-21-isotope-system-app-design.md,
# "Users (phase 2)"). Isotope System's Users page is the one place users
# change. The list is the epoch's default features without page-users;
# check it again against cosmic-settings/Cargo.toml at every new epoch.
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked --mount=type=cache,id=cosmic-target,target=/cargo-target,sharing=locked \
    COSMIC_CARGO_ARGS="-p cosmic-settings --no-default-features --features a11y,avif,single-instance,wgpu,systemd,cosmic-comp-config,page-accessibility,page-about,page-bluetooth,page-date,page-default-apps,page-display,page-input,page-legacy-applications,page-networking,page-power,page-region,page-sound,page-window-management,page-workspaces,xdg-portal,wayland" \
    cosmic-build cosmic-settings && \
    rm -f "$COSMIC_OUT/usr/share/applications/com.system76.CosmicSettings.Users.desktop" \
          "$COSMIC_OUT/usr/share/polkit-1/actions/com.system76.CosmicSettings.Users.policy" && \
    ! test -e "$COSMIC_OUT/usr/share/applications/com.system76.CosmicSettings.Users.desktop" && \
    ! test -e "$COSMIC_OUT/usr/share/polkit-1/actions/com.system76.CosmicSettings.Users.policy" && \
    rm -rf /cargo-target/*
```
- The justfile's `install` recipe only copies the built binary, without a rebuild, so the features hold (checked: `install` has no dependency, `cosmic-settings/justfile` at `epoch-1.11.0`).
- `cosmic-settings.rules` (locale, hostname, ModemManager) stays installed.

- [ ] **Step 1: Write the failing check.** `check-look.sh` for FLAVOR=cosmic: `! test -e /usr/share/applications/com.system76.CosmicSettings.Users.desktop && ! test -e /usr/share/polkit-1/actions/com.system76.CosmicSettings.Users.policy`. Run it on a current cosmic image. Expected: FAIL.
- [ ] **Step 2: Make the Dockerfile change.** Build `--target cosmic-apps`. Expected: the RUN succeeds, and its `! test -e` lines prove the files are gone.
- [ ] **Step 3: Run the check again.** Expected: PASS on the new cosmic image.
- [ ] **Step 4: Commit** `feat(cosmic): build COSMIC Settings without its Users page`.

---

## Task order and dependencies

1 → 2 → 3 → 4 → 5 (the users daemon and CLI); 6 → 7 → 8 (the backup and history daemon); 9 (the client, after 4, 7 and 8) → 10 → 11, 12, 13, 14, 15 (the pages; 13 and 14 need 10) → 16 → 17 → 18 (conditional). Every task ends with its own tests green.

### Task 19: Welcome's "Restore from a backup"

The maintainer asked for all of phase 2 in one PR. The System design says that Welcome keeps "Restore from a backup" on its first page and that it calls the same methods as the Backup page. This task adds that path.

**Files:** `welcome/page_welcome.go`, the Welcome restore steps (new file(s) in `welcome/`), Welcome's daemon client, tests next to them. Read `docs/superpowers/specs/2026-11-20-isotope-welcome-design.md` §1 "Welcome" and the restore steps it links to.

**Interfaces:**
- Consumes: `Restore`, `BackupList`, `BackupSSHKey`, `BackupHostKey` and the target and secrets format of Tasks 6–7. Use the exact D-Bus signatures from their Interfaces blocks. The machine-is-new rule is `machineIsNew` in the daemon (Task 7). Welcome asks the daemon; it must not re-implement the rule. If no read-only method exposes it, add `MachineIsNew() (b)` (no polkit) in this task, with a test.
- Produces: nothing for later tasks.

- [ ] **Tests first.**
  - The choice is shown only to an admin on a new machine, and hidden otherwise.
  - The restore steps:
    1. choose a target (USB disk or remote, the same fields as System's Backup page);
    2. the backup password through the fd;
    3. pick the backup from `BackupList`;
    4. confirm;
    5. `Restore`, which ends the flow with a "restarting" screen.
  - The daemon's refusal text is shown as it comes.
  - Use Welcome's existing fake-daemon test style.
- [ ] **Implement.** Reuse Welcome's existing form widgets and `guikit`, without importing `system/`. If a piece of UI is needed by both, put it in `guikit/` and say so.
- [ ] **Run the tests.** `welcome/` tests, in the same container as `SYSTEST` with `-w /src/welcome`, and with the Dockerfile's tags.
- [ ] **Commit.**

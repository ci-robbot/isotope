package main

// isotope glibc: programs built for glibc, run in a per-user Ubuntu LTS
// Distrobox. Isotope is a musl system. The stand-in loader
// (tools/isotope-glibc-loader.c, installed as /lib64/ld-linux-x86-64.so.2)
// hands every glibc program the kernel starts to `isotope glibc run
// --loader`, which creates the box on the first run and runs the program in
// it. See docs/superpowers/specs/2026-11-23-glibc-programs-design.md.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	glibcBox          = "isotope-glibc"
	glibcDefaultImage = "docker.io/library/ubuntu:26.04"
	// glibcGuardEnv is set for every program run in the box. The stand-in
	// loader refuses when it is set, so a glibc program in the box that
	// somehow reaches the host's loader cannot loop. The box has its own
	// real loader, so this should never happen.
	glibcGuardEnv = "ISOTOPE_GLIBC_LOADER_ACTIVE"
	// glibcArgv0Env is the program's original argv[0], from the loader.
	glibcArgv0Env = "ISOTOPE_GLIBC_ARGV0"
	glibcAutoEnv  = "ISOTOPE_GLIBC_AUTO"
	// glibcExitCannotRun is what the loader exits with when it cannot hand
	// off, and what `run` exits with when the program could not be started:
	// the shell's "command not found or not executable".
	glibcExitCannotRun = 127
	// glibcExitInUse is reset's refusal while programs run in the box
	// (EX_TEMPFAIL: try again later). Isotope System's Compatibility page
	// branches on it (cmd/isotope-system/compat.go); keep the number.
	glibcExitInUse = 75
)

const glibcUsage = `Usage: isotope glibc [command]

Isotope is built on musl, so a program built for glibc (a downloaded tarball's
binary, an installer, a glibc node) cannot run on it directly. Isotope runs
such programs in a container for your user, an Ubuntu LTS Distrobox named
isotope-glibc: starting the program the usual way is enough. The first run
creates the container, which needs the network and can take a few minutes.
The container shares your home folder; other paths are reached through
/run/host.

Commands:
  run [--] FILE [ARGS...]   run FILE in the container
      --appimage               FILE is an AppImage: unpack it once into
                               ~/.cache/isotope/appimages, then run it on this
                               system when nothing in it is built for glibc,
                               in the container otherwise
      --native, --box          run the AppImage on this system, or in the
                               container, whatever it contains
  create                    create the container now
      --progress               print the progress on stderr, as on a
                               terminal, and send no notification
  pull                      download the image the container is made from, without
                            creating the container
      --progress               as for create
  enter                     open a shell in the container
  status                    whether the container exists, its image and size
      --json                   the same as JSON: exists, image, size_bytes,
                               image_present, image_size_bytes, auto, error, busy
  reset                     remove the container and create it again, and
                            the unpacked AppImages
      --yes                    do not ask for confirmation
      --progress               as for create
                            It exits with 75 when the container is in use:
                            close the programs that run in it, then try again.
  auto on|off               whether programs built for glibc are sent to the
                            container when you start them (default on);
                            ISOTOPE_GLIBC_AUTO=0 turns it off for one command

Example: isotope glibc run -- ./tool --version

Use Flatpak for apps on Flathub, and Distrobox itself for a full development
environment. The container runs on Docker, so it needs the installed system
(not the live one), a normal user and membership of the docker group.
Isotope System's Compatibility page (isotope system compatibility) has the
same settings. When to use Flatpak, isotope glibc or Distrobox:
/usr/share/doc/isotope/glibc-programs.md on this system, or
https://github.com/kairos-community/isotope/blob/main/docs/glibc-programs.md
`

// runOpts says how `isotope glibc run` was started.
type runOpts struct {
	Loader   bool // started by the stand-in loader: auto=off applies
	AppImage bool // FILE is an AppImage: extract it, then run it on the host or in the box
	Native   bool // run the AppImage on the host, whatever it contains
	Box      bool // run the AppImage in the box, whatever it contains
}

// glibcSys is everything `isotope glibc` reads from the system, so the
// tests can replace it.
type glibcSys struct {
	euid       func() int
	uid        func() int
	egid       func() int
	gid        func() int
	userName   func() string
	getgroups  func() ([]int, error)
	groupFile  string
	liveMarker string
	dockerSock string
	confFile   string
	sockWait   time.Duration // how long to wait for dockerSock
	poll       time.Duration // how often to look at the socket and the lock
	lockWait   time.Duration // how long to wait for the create lock; 0 is no limit
	isTerminal func() bool
	// progress is create's and reset's --progress: talk as on a terminal
	// (Isotope System reads the lines), and notify only the end, and only
	// when the window that started it is gone: startPPID is the parent at
	// the start, and a new parent (the orphan's reaper) means it closed.
	progress  bool
	startPPID int
	getppid   func() int
	notify    func(summary, body string) error
	// exec runs the final distrobox command. In production it replaces the
	// process, so the program gets the terminal, the signals and the exit
	// status as distrobox passes them; it returns only on failure.
	exec   func(path string, argv, env []string) (int, error)
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

var glibc = glibcSys{
	euid: os.Geteuid,
	uid:  os.Getuid,
	egid: os.Getegid,
	gid:  os.Getgid,
	userName: func() string {
		if u, err := user.Current(); err == nil {
			return u.Username
		}
		return os.Getenv("USER")
	},
	getgroups: func() ([]int, error) {
		g, err := os.Getgroups()
		return append(g, os.Getgid()), err
	},
	groupFile:  "/etc/group",
	liveMarker: "/run/isotope/live-desktop",
	dockerSock: "/run/docker.sock",
	confFile:   "/usr/share/isotope/glibc.conf",
	sockWait:   30 * time.Second,
	poll:       250 * time.Millisecond,
	isTerminal: func() bool {
		_, err := unix.IoctlGetTermios(2, unix.TCGETS)
		return err == nil
	},
	getppid: os.Getppid,
	notify:  Notify,
	exec: func(path string, argv, env []string) (int, error) {
		return glibcExitCannotRun, unix.Exec(path, argv, env)
	},
	stdin:  os.Stdin,
	stdout: os.Stdout,
	stderr: os.Stderr,
}

// cmdGlibc is `isotope glibc`.
func cmdGlibc(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	glibc.stdin, glibc.stdout, glibc.stderr = stdin, stdout, stderr
	glibc.progress = false
	if len(args) == 0 {
		fmt.Fprint(stdout, glibcUsage)
		return exitOK
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "run":
		return cmdGlibcRun(rest)
	case "create":
		for _, a := range rest {
			if a != "--progress" {
				fmt.Fprintf(stderr, "isotope glibc create: unknown argument %q\n\n%s", a, glibcUsage)
				return exitUsage
			}
			glibcSetProgress()
		}
		lock, code := glibcPrepare()
		if code != exitOK {
			return code
		}
		lock.release()
		fmt.Fprintf(stdout, "The glibc container %s is ready.\n", glibcBox)
		return exitOK
	case "pull":
		for _, a := range rest {
			if a != "--progress" {
				fmt.Fprintf(stderr, "isotope glibc pull: unknown argument %q\n\n%s", a, glibcUsage)
				return exitUsage
			}
			glibcSetProgress()
		}
		return glibcPull()
	case "enter":
		lock, code := glibcPrepare()
		if code != exitOK {
			return code
		}
		return glibcEnter(lock, nil)
	case "status":
		if len(rest) == 1 && rest[0] == "--json" {
			return glibcStatusAsJSON()
		}
		if len(rest) > 0 {
			fmt.Fprintf(stderr, "isotope glibc status: unknown argument %q\n\n%s", rest[0], glibcUsage)
			return exitUsage
		}
		return glibcStatus()
	case "reset":
		return glibcReset(rest)
	case "auto":
		return glibcAuto(rest)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, glibcUsage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "isotope glibc: unknown command %q\n\n%s", sub, glibcUsage)
		return exitUsage
	}
}

func cmdGlibcRun(args []string) int {
	var opts runOpts
flags:
	for len(args) > 0 {
		switch a := args[0]; a {
		case "--loader":
			opts.Loader = true
		case "--appimage":
			opts.AppImage = true
		case "--native":
			opts.Native = true
		case "--box":
			opts.Box = true
		case "-h", "--help":
			fmt.Fprint(glibc.stdout, glibcUsage)
			return exitOK
		case "--":
			args = args[1:]
			break flags
		default:
			if strings.HasPrefix(a, "-") && a != "-" {
				fmt.Fprintf(glibc.stderr, "isotope glibc run: unknown flag %q\n\n%s", a, glibcUsage)
				return exitUsage
			}
			break flags
		}
		args = args[1:]
	}
	if opts.Native && opts.Box {
		fmt.Fprintf(glibc.stderr, "isotope glibc run: say --native or --box, not both\n\n%s", glibcUsage)
		return exitUsage
	}
	if len(args) == 0 {
		fmt.Fprintf(glibc.stderr, "isotope glibc run: name the program to run\n\n%s", glibcUsage)
		return exitUsage
	}
	return glibcRun(opts, args[0], args[1:])
}

// glibcRun runs file with args in the box, creating the box first when it
// is missing. It returns the program's exit status, or 127 when the program
// could not be started. An AppImage (--appimage, --native, --box) is
// extracted first and may run on the host instead; see appimage.go.
func glibcRun(opts runOpts, file string, args []string) int {
	if opts.AppImage || opts.Native || opts.Box {
		return appimageRun(opts, file, args)
	}
	prog := file
	if strings.Contains(prog, "/") && !filepath.IsAbs(prog) {
		if abs, err := filepath.Abs(prog); err == nil {
			prog = abs
		}
	}
	if why, todo := glibcRefusal(opts.Loader); why != "" {
		glibcFail(refusalText(prog, why, todo), "Cannot run "+filepath.Base(prog))
		return glibcExitCannotRun
	}
	argv0 := os.Getenv(glibcArgv0Env)
	if argv0 == "" {
		argv0 = filepath.Base(prog)
	}
	home, _ := os.UserHomeDir()
	return glibcBoxRun(boxPath(home, prog), argv0, nil, args)
}

// glibcBoxRun runs prog (a path as the box sees it) with argv0 and args in
// the box, after the command pre (such as env with variables), creating the
// box first when it is missing. The refusals are the caller's.
func glibcBoxRun(prog, argv0 string, pre, args []string) int {
	lock, err := glibcEnsureBox()
	if err != nil {
		glibcFail(err.Error(), "The glibc environment could not be set up")
		return glibcExitCannotRun
	}
	// Ubuntu's sh is dash, whose exec has no -a; bash is in every Ubuntu
	// image (it is Essential), and its `exec -a` keeps the program's
	// argv[0]. "$@" passes the arguments through unchanged.
	cmd := append(append([]string{}, pre...), "bash", "-c", `exec -a "$0" "$@"`, argv0, prog)
	return glibcEnter(lock, append(cmd, args...))
}

// glibcEnter execs `distrobox enter isotope-glibc [-- cmd...]`.
//
// What distrobox-enter 1.8.2.5 does with it (github.com/89luca89/distrobox,
// tag 1.8.2.5, distrobox-enter):
//   - the working directory: lines 413-417 keep $PWD when it is under the
//     container's $HOME, and put /run/host in front of it otherwise, so the
//     program starts in the same directory;
//   - the environment: lines 441-443 pass every variable of its own
//     environment with `--env`, except those whose value has `"`, a backquote
//     or `$`, and HOME, PATH, PWD, SHELL, HOST(NAME), XDG_*_DIRS and a few
//     more. So ISOTOPE_GLIBC_LOADER_ACTIVE=1 reaches the program without
//     --additional-flags;
//   - the arguments: line 788 is `exec "$@"`, so they reach `docker
//     exec` unchanged;
//   - a missing container: lines 659-699 ask "Create it now, out of image
//     ...?" on stdin and create it from distrobox's own default image. That
//     is why glibcEnsureBox checks with `docker inspect` first, under the
//     box lock, which this process keeps until the program exits.
//
// lock is the shared box lock from glibcEnsureBox; it is released when the
// program exits.
func glibcEnter(lock *glibcBoxLock, cmd []string) int {
	path, err := exec.LookPath("distrobox")
	if err != nil {
		lock.release()
		fmt.Fprintln(glibc.stderr, "isotope glibc: distrobox is not installed.")
		return glibcExitCannotRun
	}
	argv := []string{"distrobox", "enter", glibcBox}
	if len(cmd) > 0 {
		argv = append(append(argv, "--"), cmd...)
	}
	lock.keepAcrossExec()
	code, err := glibc.exec(path, argv, append(glibcDistroboxEnv(), glibcGuardEnv+"=1"))
	lock.release() // reached only in tests, or when the exec failed
	if err != nil {
		glibcFail(fmt.Sprintf("isotope glibc: could not start distrobox: %v", err), "The glibc environment could not be started")
		return glibcExitCannotRun
	}
	return code
}

// glibcDistroboxEnv is the environment of every distrobox call: this
// process's, without the loader's variables and without the user's DBX_*
// settings, which could change the box's home, image or container manager
// under the path mapping. DBX_CONTAINER_MANAGER=docker makes distrobox use
// the manager that glibcBoxExists asked. DBX_NON_INTERACTIVE=0 beats a
// `non_interactive=1` in a distrobox config file (distrobox-enter applies
// the environment after the config files, line 153), so a missing box makes
// distrobox ask, never create one silently from its default image; the lock
// keeps the box from going missing in the first place.
func glibcDistroboxEnv() []string {
	env := make([]string, 0, len(os.Environ())+3)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, glibcArgv0Env+"=") || strings.HasPrefix(e, glibcGuardEnv+"=") || strings.HasPrefix(e, "DBX_") {
			continue
		}
		env = append(env, e)
	}
	return append(env, "DBX_NON_INTERACTIVE=0", "DBX_CONTAINER_MANAGER=docker")
}

// glibcBoxShared are the host paths the box sees at the same path, besides
// $HOME. From distrobox 1.8.2.5 (github.com/89luca89/distrobox, tag
// 1.8.2.5): distrobox-create line 730 mounts /tmp and line 734 the user's
// home at the same path; distrobox-init lines 35-50 (HOST_MOUNTS) bind
// /media, /mnt, /run/media and /var/mnt from /run/host when the host has
// them. Everything else is under /run/host: distrobox-create line 765 mounts
// the host's / there on Docker.
var glibcBoxShared = []string{"/tmp", "/mnt", "/media", "/run/media", "/var/mnt"}

// boxPath is where the box sees the host path p. A name without a slash is
// a command looked up in the box's PATH and is left as it is.
func boxPath(home, p string) string {
	if !strings.Contains(p, "/") {
		return p
	}
	p = filepath.Clean(p)
	under := func(dir string) bool {
		dir = filepath.Clean(dir)
		return dir != "/" && (p == dir || strings.HasPrefix(p, dir+"/"))
	}
	if home != "" && under(home) {
		return p
	}
	for _, d := range glibcBoxShared {
		if under(d) {
			return p
		}
	}
	return "/run/host" + p
}

// refusalText is a refusal in the loader's words: why, and what to do.
func refusalText(prog, why, todo string) string {
	if prog == "" {
		return fmt.Sprintf("Isotope runs programs built for glibc in a glibc container for your user, but %s. %s See `isotope glibc --help`.", why, todo)
	}
	return fmt.Sprintf("%s is a program built for glibc. Isotope runs these in a glibc container for your user, but %s. %s See `isotope glibc --help`.", prog, why, todo)
}

// glibcTalks is true when someone reads stderr: a terminal, or Isotope
// System through --progress. Otherwise the news goes out as notifications.
func glibcTalks() bool {
	return glibc.progress || glibc.isTerminal()
}

// glibcSetProgress is --progress.
func glibcSetProgress() {
	glibc.progress = true
	glibc.startPPID = glibc.getppid()
}

// glibcReaderGone is true when a --progress job outlived the window that
// reads it: its parent changed. Isotope System starts the job in its own
// session so that closing the window does not stop it; the end then goes
// out as a notification, or nobody would learn it.
func glibcReaderGone() bool {
	return glibc.progress && glibc.getppid() != glibc.startPPID
}

// glibcFail prints msg, and without a terminal (a double-click) also sends
// it as a notification, since nobody would see stderr.
func glibcFail(msg, summary string) {
	fmt.Fprintln(glibc.stderr, msg)
	if !glibcTalks() || glibcReaderGone() {
		_ = glibc.notify(summary, msg)
	}
}

// glibcRefusal says why the box cannot be used now, and what to do, or
// returns "" when it can. loader applies the user's auto=off.
func glibcRefusal(loader bool) (why, todo string) {
	// A setuid or setgid caller: isotope looks up distrobox and docker in
	// PATH and reads HOME, all the caller's, so it must not run them with
	// someone else's user or group.
	if glibc.euid() != glibc.uid() || glibc.egid() != glibc.gid() {
		return "it runs setuid or setgid, and setuid and setgid programs are not handed to the container",
			"Run it as the user and group it belongs to."
	}
	if glibc.euid() == 0 {
		return "this is the root user, which has no container", "Run it as a normal user."
	}
	if loader && glibcAutoOff() {
		return "running them automatically is turned off for you",
			"Run `isotope glibc run -- <program>` yourself, or turn it on with `isotope glibc auto on`."
	}
	if _, err := os.Stat(glibc.liveMarker); err == nil {
		return "this is the live system, which has no container and nowhere to keep one",
			"Install Isotope, then run it as a normal user."
	}
	if _, err := exec.LookPath("distrobox"); err != nil {
		return "Distrobox is not installed on this system",
			"Isotope's desktops have it; the server edition does not."
	}
	if !glibcWaitSocket() {
		return fmt.Sprintf("Docker is not running (%s did not appear in %s)", glibc.dockerSock, glibc.sockWait.Round(time.Second)),
			"Check `systemctl status docker`, then try again."
	}
	return glibcDockerGroup()
}

func glibcWaitSocket() bool {
	deadline := time.Now().Add(glibc.sockWait)
	said := false
	for {
		if _, err := os.Stat(glibc.dockerSock); err == nil {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		if !said && glibcTalks() {
			fmt.Fprintln(glibc.stderr, "Waiting for Docker to start...")
			said = true
		}
		time.Sleep(glibc.poll)
	}
}

// glibcDockerGroup compares this session's groups with the group file: a
// user added to docker after logging in has it in the file but not in the
// session, and must log out and back in.
func glibcDockerGroup() (why, todo string) {
	b, err := os.ReadFile(glibc.groupFile)
	if err != nil {
		return "", "" // let Docker answer
	}
	user := glibc.userName()
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(line, ":")
		if len(f) < 4 || f[0] != "docker" {
			continue
		}
		gid, err := strconv.Atoi(f[2])
		if err != nil {
			return "", ""
		}
		if groups, err := glibc.getgroups(); err == nil {
			for _, g := range groups {
				if g == gid {
					return "", ""
				}
			}
		}
		for _, m := range strings.Split(f[3], ",") {
			if strings.TrimSpace(m) == user {
				return "this session started before you were added to the docker group, which it needs",
					"Log out and back in, then try again."
			}
		}
		return "you are not in the docker group, which it needs",
			fmt.Sprintf("Ask an administrator to add you (`sudo usermod -aG docker %s`), then log out and back in.", user)
	}
	return "this system has no docker group", "Docker comes with Isotope's desktops; reinstall or update Isotope."
}

// glibcPrepare is the refusals and the box for create and enter. It
// returns with the box lock held shared.
func glibcPrepare() (*glibcBoxLock, int) {
	if why, todo := glibcRefusal(false); why != "" {
		glibcFail(refusalText("", why, todo), "The glibc environment cannot be used")
		return nil, exitError
	}
	lock, err := glibcEnsureBox()
	if err != nil {
		glibcFail(err.Error(), "The glibc environment could not be set up")
		return nil, exitError
	}
	return lock, exitOK
}

// glibcImage is the image a new box is created from: image= in the conf
// file the image ships, or the same default when the file is missing.
func glibcImage() string {
	if v := readKeyFile(glibc.confFile)["image"]; v != "" {
		return v
	}
	return glibcDefaultImage
}

// readKeyFile reads key=value lines; # starts a comment line.
func readKeyFile(path string) map[string]string {
	m := map[string]string{}
	b, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m
}

func glibcConfigFile() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "isotope", "glibc")
}

// glibcAutoOff is the user's opt-out, read the way the loader reads it.
func glibcAutoOff() bool {
	return os.Getenv(glibcAutoEnv) == "0" || glibcAutoSetOff()
}

// glibcAutoSetOff is the saved setting alone, without ISOTOPE_GLIBC_AUTO,
// which is for one command. A missing file is on.
func glibcAutoSetOff() bool {
	return readKeyFile(glibcConfigFile())["auto"] == "off"
}

func glibcAuto(args []string) int {
	if len(args) == 0 {
		state := "on"
		if glibcAutoOff() {
			state = "off"
		}
		fmt.Fprintf(glibc.stdout, "Programs built for glibc go to the container automatically: %s\n", state)
		return exitOK
	}
	if len(args) != 1 || (args[0] != "on" && args[0] != "off") {
		fmt.Fprintf(glibc.stderr, "isotope glibc auto: say on or off\n\n%s", glibcUsage)
		return exitUsage
	}
	path := glibcConfigFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintln(glibc.stderr, "isotope glibc:", err)
		return exitError
	}
	if err := os.WriteFile(path, []byte("auto="+args[0]+"\n"), 0o644); err != nil {
		fmt.Fprintln(glibc.stderr, "isotope glibc:", err)
		return exitError
	}
	if args[0] == "off" {
		fmt.Fprintln(glibc.stdout, "Programs built for glibc no longer go to the container when you start them. `isotope glibc run` still works.")
	} else {
		fmt.Fprintln(glibc.stdout, "Programs built for glibc go to the container when you start them.")
	}
	return exitOK
}

// docker runs the docker CLI and returns its combined output.
func glibcDocker(args ...string) (string, error) {
	out, err := exec.Command("docker", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// glibcBoxExists asks Docker whether the box's container is there. A
// pruned box is simply missing, and is created again.
func glibcBoxExists() (bool, error) {
	out, err := glibcDocker("inspect", "--type", "container", glibcBox)
	if err == nil {
		return true, nil
	}
	if strings.Contains(out, "No such") {
		return false, nil
	}
	return false, fmt.Errorf("Docker did not answer about the glibc container: %s", lastLines(out, 3))
}

// The box lock, $XDG_RUNTIME_DIR/isotope/glibc.lock, is a flock:
//   - shared while a program runs: it is taken before the box is checked and
//     handed to distrobox across the exec, so it is held until the program
//     exits;
//   - exclusive while the box is created (including distrobox's first-start
//     setup) or removed by reset.
//
// So two programs started together on a fresh account create the box once,
// a program started while the box is still being set up waits for the setup
// instead of entering a half-made box, and reset cannot remove the box
// between a run's check and its enter (or under a running program).
// distrobox-enter has no option that fails on a missing container (it asks,
// lines 659-699, or creates one from its own default image when
// non-interactive), so this lock is what closes that window.
type glibcBoxLock struct{ f *os.File }

func glibcLockPath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		// No runtime dir (su, ssh without logind): /run/user/<uid> may not
		// exist and cannot be made, so use the user's cache.
		if c, err := os.UserCacheDir(); err == nil {
			dir = c
		} else {
			dir = os.TempDir()
		}
	}
	return filepath.Join(dir, "isotope", "glibc.lock")
}

func glibcOpenLock() (*glibcBoxLock, error) {
	path := glibcLockPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("the glibc container's lock: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("the glibc container's lock: %w", err)
	}
	return &glibcBoxLock{f: f}, nil
}

// take takes the lock shared (unix.LOCK_SH) or exclusive (unix.LOCK_EX).
// It drops what it held first: flock's conversion is not atomic anyway,
// and two holders that both convert would otherwise wait for each other.
// With wait false it tries once; otherwise it waits up to glibc.lockWait
// (0 is no limit), saying so once on a terminal.
func (l *glibcBoxLock) take(how int, wait bool) error {
	_ = unix.Flock(int(l.f.Fd()), unix.LOCK_UN)
	var deadline time.Time
	if glibc.lockWait > 0 {
		deadline = time.Now().Add(glibc.lockWait)
	}
	said := false
	for {
		err := unix.Flock(int(l.f.Fd()), how|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			return fmt.Errorf("the glibc container's lock: %w", err)
		}
		if !wait || (!deadline.IsZero() && !time.Now().Before(deadline)) {
			return errGlibcBusy
		}
		if !said && glibcTalks() {
			if how == unix.LOCK_SH {
				fmt.Fprintln(glibc.stderr, "Waiting for the glibc container, which another program is setting up...")
			} else {
				fmt.Fprintln(glibc.stderr, "Waiting for the glibc container, which other programs are using...")
			}
			said = true
		}
		time.Sleep(glibc.poll)
	}
}

var errGlibcBusy = errors.New("the glibc container is busy: another program is setting it up or running in it")

// keepAcrossExec lets the lock's fd survive the exec of distrobox, so the
// shared lock lasts as long as the program.
func (l *glibcBoxLock) keepAcrossExec() {
	_, _ = unix.FcntlInt(l.f.Fd(), unix.F_SETFD, 0)
}

func (l *glibcBoxLock) release() {
	_ = unix.Flock(int(l.f.Fd()), unix.LOCK_UN)
	l.f.Close()
}

// glibcEnsureBox returns with the box there and the lock held shared,
// creating the box (under the exclusive lock) when it is missing.
func glibcEnsureBox() (*glibcBoxLock, error) {
	l, err := glibcOpenLock()
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		if err := l.take(unix.LOCK_SH, true); err != nil {
			l.release()
			return nil, err
		}
		ok, err := glibcBoxExists()
		if err != nil {
			l.release()
			return nil, err
		}
		if ok {
			return l, nil
		}
		if attempt == 2 {
			l.release()
			return nil, errors.New("The glibc container was created but Docker does not find it. Run `isotope glibc status`, then try again.")
		}
		if err := l.take(unix.LOCK_EX, true); err != nil {
			l.release()
			return nil, err
		}
		// Another program may have created it while we waited.
		if ok, err = glibcBoxExists(); err == nil && !ok {
			err = glibcCreate()
		}
		if err != nil {
			l.release()
			return nil, err
		}
	}
}

// glibcCreate creates the box and starts it once, which runs distrobox's
// first-start setup (packages, the user), so the "ready" notice comes when
// the box is ready. There is no time limit: the image pull and the setup can
// take minutes. A failure removes the half-made container, so the next run
// tries again.
func glibcCreate() error {
	image := glibcImage()
	term := glibcTalks()
	const summary = "Setting up the glibc environment, first run only"
	if term {
		fmt.Fprintf(glibc.stderr, "%s: creating the container %s from %s. This can take a few minutes.\n", summary, glibcBox, image)
	} else {
		_ = glibc.notify(summary, "Isotope is creating a container to run programs built for glibc. This can take a few minutes; the program starts when it is done.")
	}
	steps := [][]string{
		{"create", "--yes", "--name", glibcBox, "--image", image},
		{"enter", glibcBox, "--", "true"},
	}
	for _, step := range steps {
		var tail bytes.Buffer
		c := exec.Command("distrobox", step...)
		c.Env = glibcDistroboxEnv()
		if term {
			c.Stdout = io.MultiWriter(glibc.stderr, &tail)
			c.Stderr = c.Stdout
		} else {
			c.Stdout, c.Stderr = &tail, &tail
		}
		if err := c.Run(); err != nil {
			_, _ = glibcDocker("rm", "--force", glibcBox)
			return glibcCreateError(image, tail.String(), err)
		}
	}
	if term {
		fmt.Fprintln(glibc.stderr, "The glibc environment is ready.")
	}
	if !term || glibcReaderGone() {
		_ = glibc.notify("The glibc environment is ready", "Programs built for glibc now run in it.")
	}
	return nil
}

func glibcCreateError(image, out string, err error) error {
	lower := strings.ToLower(out)
	for _, s := range []string{"dial tcp", "no such host", "network is unreachable", "tls handshake timeout",
		"i/o timeout", "temporary failure in name resolution", "connection refused", "could not resolve"} {
		if strings.Contains(lower, s) {
			return fmt.Errorf("The glibc container could not be created: the first run downloads %s, which needs the network. Connect to the network, then try again.", image)
		}
	}
	detail := lastLines(out, 3)
	if detail == "" {
		detail = err.Error()
	}
	return fmt.Errorf("The glibc container could not be created: %s. Try again; `isotope glibc create` shows the whole output.", detail)
}

// lastLines is the last n non-empty lines of s, joined with "; ".
func lastLines(s string, n int) string {
	var lines []string
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "; ")
}

func glibcStatus() int {
	fmt.Fprintf(glibc.stdout, "Container:  %s\n", glibcBox)
	ok, err := glibcBoxExists()
	switch {
	case err != nil:
		fmt.Fprintf(glibc.stdout, "State:      unknown: %v\n", err)
	case !ok:
		fmt.Fprintln(glibc.stdout, "State:      not created yet; the first program built for glibc creates it")
	default:
		image, _ := glibcDocker("inspect", "--type", "container", "--format", "{{.Config.Image}}", glibcBox)
		size, _ := glibcDocker("ps", "--all", "--size", "--filter", "name=^"+glibcBox+"$", "--format", "{{.Size}}")
		fmt.Fprintln(glibc.stdout, "State:      created")
		fmt.Fprintf(glibc.stdout, "Image:      %s\n", image)
		if size != "" {
			fmt.Fprintf(glibc.stdout, "Size:       %s\n", size)
		}
	}
	fmt.Fprintf(glibc.stdout, "New image:  %s (used when the container is created or reset)\n", glibcImage())
	auto := "on"
	if glibcAutoOff() {
		auto = "off"
	}
	fmt.Fprintf(glibc.stdout, "Automatic:  %s\n", auto)
	return exitOK
}

// glibcImageTimeout bounds `docker image inspect` in status --json.
const glibcImageTimeout = 10 * time.Second

// glibcImageSize asks Docker whether image is on this computer, and its
// size.
func glibcImageSize(image string) (present bool, size int64) {
	ctx, cancel := context.WithTimeout(context.Background(), glibcImageTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Size}}", image)
	c.WaitDelay = time.Second
	out, err := c.Output()
	if err != nil {
		return false, 0
	}
	size, _ = strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return true, size
}

// glibcPull is `isotope glibc pull`: the image, without the container. It
// holds the box lock shared, so it waits for a setup or reset in progress.
func glibcPull() int {
	if why, todo := glibcRefusal(false); why != "" {
		glibcFail(refusalText("", why, todo), "The glibc environment cannot be used")
		return exitError
	}
	lock, err := glibcOpenLock()
	if err == nil {
		if err = lock.take(unix.LOCK_SH, true); err != nil {
			lock.release()
		}
	}
	if err != nil {
		glibcFail(err.Error(), "The glibc image could not be downloaded")
		return exitError
	}
	defer lock.release()
	image := glibcImage()
	term := glibcTalks()
	if term {
		fmt.Fprintf(glibc.stderr, "Downloading %s. This can take a few minutes.\n", image)
	}
	var tail bytes.Buffer
	c := exec.Command("docker", "pull", image)
	if term {
		c.Stdout = io.MultiWriter(glibc.stderr, &tail)
		c.Stderr = c.Stdout
	} else {
		c.Stdout, c.Stderr = &tail, &tail
	}
	if err := c.Run(); err != nil {
		e := glibcCreateError(image, tail.String(), err)
		msg := strings.Replace(e.Error(), "container could not be created", "image could not be downloaded", 1)
		if !strings.Contains(msg, "image could not") {
			msg = fmt.Sprintf("The glibc image could not be downloaded: %s.", lastLines(tail.String(), 3))
		}
		glibcFail(msg, "The glibc image could not be downloaded")
		return exitError
	}
	if term {
		fmt.Fprintln(glibc.stderr, "The glibc image is downloaded.")
	}
	if !term || glibcReaderGone() {
		_ = glibc.notify("The glibc image is downloaded", image)
	}
	fmt.Fprintf(glibc.stdout, "The image %s is on this computer.\n", image)
	return exitOK
}

// glibcStatusJSON is `isotope glibc status --json`, which Isotope System's
// Compatibility page reads. Keep the names: the page depends on them.
//   - exists: the container is there;
//   - image: its image, or the one a new container is made from;
//   - size_bytes: the disk space it uses, its image included (Docker's
//     SizeRootFs), 0 without a container;
//   - image_present: the image a new container is made from (`image`
//     when there is no container) is in Docker, from `docker image inspect`;
//   - image_size_bytes: its size, 0 when it is not there;
//   - auto: the saved setting of `isotope glibc auto` (a missing file is
//     on; ISOTOPE_GLIBC_AUTO, which is for one command, is left out);
//   - error: why Docker could not say, as a sentence, or "";
//   - busy: the container is being created or reset now (its lock is held
//     exclusive), by another window or a program's first run.
type glibcStatusJSON struct {
	Exists    bool   `json:"exists"`
	Image     string `json:"image"`
	SizeBytes int64  `json:"size_bytes"`
	Auto      bool   `json:"auto"`
	Error     string `json:"error"`
	Busy      bool   `json:"busy"`

	ImagePresent   bool  `json:"image_present"`
	ImageSizeBytes int64 `json:"image_size_bytes"`
}

func glibcStatusAsJSON() int {
	st := glibcStatusJSON{Image: glibcImage(), Auto: !glibcAutoSetOff()}
	if l, err := glibcOpenLock(); err == nil {
		st.Busy = errors.Is(l.take(unix.LOCK_SH, false), errGlibcBusy)
		l.release()
	}
	ok, err := glibcBoxExists()
	switch {
	case err != nil:
		st.Error = err.Error()
		// The usual cause has a better sentence: the docker group.
		if why, todo := glibcDockerGroup(); why != "" {
			st.Error = refusalText("", why, todo)
		}
	case ok:
		st.Exists = true
		out, err := glibcDocker("inspect", "--type", "container", "--size", "--format", "{{.Config.Image}} {{.SizeRootFs}}", glibcBox)
		if f := strings.Fields(out); err == nil && len(f) == 2 {
			st.Image = f[0]
			st.SizeBytes, _ = strconv.ParseInt(f[1], 10, 64)
		}
	}
	if st.Error == "" {
		st.ImagePresent, st.ImageSizeBytes = glibcImageSize(st.Image)
	}
	b, _ := json.Marshal(st)
	fmt.Fprintln(glibc.stdout, string(b))
	return exitOK
}

// glibcInUse is reset's refusal while the box is in use: exit 75.
func glibcInUse() int {
	glibcFail("The glibc container is in use: a program built for glibc is running in it, or it is being set up. Close those programs, then try again.", "The glibc environment could not be reset")
	return glibcExitInUse
}

// glibcReset removes the box and creates it again, after a confirmation on
// a terminal. Programs installed in the box are lost; the home folder is
// shared, not in the box, so it is untouched.
func glibcReset(args []string) int {
	yes := false
	for _, a := range args {
		switch a {
		case "--yes", "-y":
			yes = true
		case "--progress":
			glibcSetProgress()
		default:
			fmt.Fprintf(glibc.stderr, "isotope glibc reset: unknown argument %q\n\n%s", a, glibcUsage)
			return exitUsage
		}
	}
	// A container in use is refused first: the other checks (Docker's
	// socket can take 30 s) cannot change that answer, and the lock is
	// taken again below, after the confirmation.
	if l, err := glibcOpenLock(); err == nil {
		err = l.take(unix.LOCK_EX, false)
		l.release()
		if errors.Is(err, errGlibcBusy) {
			return glibcInUse()
		}
	}
	if why, todo := glibcRefusal(false); why != "" {
		glibcFail(refusalText("", why, todo), "The glibc environment cannot be used")
		return exitError
	}
	if !yes && glibc.isTerminal() {
		fmt.Fprintf(glibc.stdout, "Remove the container %s and create it again from %s? What you installed in it is lost; your home folder is not touched. [y/N] ", glibcBox, glibcImage())
		answer, _ := bufio.NewReader(glibc.stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			fmt.Fprintln(glibc.stdout, "Nothing changed.")
			return exitError
		}
	}
	// Exclusive, without waiting: a program running in the box holds the
	// lock shared, and removing the box would kill it.
	lock, err := glibcOpenLock()
	if err == nil {
		if err = lock.take(unix.LOCK_EX, false); err != nil {
			lock.release()
		}
	}
	if errors.Is(err, errGlibcBusy) {
		return glibcInUse()
	}
	if err != nil {
		glibcFail(err.Error(), "The glibc environment could not be reset")
		return exitError
	}
	defer lock.release()
	// The extracted AppImages go too; an AppImage running on the host
	// holds its entry's lock and keeps it.
	appimagePrune(appimageCacheDir(), 0)
	out, err := glibcDocker("rm", "--force", glibcBox)
	if err != nil && !strings.Contains(out, "No such") {
		glibcFail(fmt.Sprintf("The glibc container could not be removed: %s", lastLines(out, 3)), "The glibc environment could not be reset")
		return exitError
	}
	// Created again under the same exclusive lock, so no run enters the
	// missing box in between.
	if err := glibcCreate(); err != nil {
		glibcFail(err.Error(), "The glibc environment could not be reset")
		return exitError
	}
	fmt.Fprintf(glibc.stdout, "The glibc container %s was created again.\n", glibcBox)
	return exitOK
}

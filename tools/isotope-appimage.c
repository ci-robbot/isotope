/*
 * isotope-appimage: the binfmt_misc handler for type 2 AppImages.
 *
 * Installed as /usr/libexec/isotope-appimage and registered by
 * /usr/lib/binfmt.d/isotope-appimage.conf for the magic "AI\x02" at offset
 * 8. The rule has the F flag, so the kernel opens this file once, in the
 * host's mount namespace, when the rule is registered: the handler that runs
 * is always the host's, in every container. The rule has no P flag, so the
 * kernel starts it as
 *
 *   isotope-appimage <the AppImage's path as given to execve> <argv[1..]>
 *
 * The rule applies in every mount namespace (rootful Docker and Distrobox see
 * the host's binfmt_misc table), but the path it gets, and /usr/bin/isotope,
 * are resolved in the caller's namespace. So:
 *
 *  - On the host it execs
 *      /usr/bin/isotope glibc run --appimage -- <path> <argv[1..]>
 *    which unpacks the AppImage and runs it on the host or in the glibc
 *    container. "On the host" means: /run/systemd/system is a directory
 *    (the machine was booted with systemd as init) and /usr/bin/isotope is
 *    an executable regular file. Docker containers and Distrobox boxes have
 *    no /run/systemd/system unless they were created to run systemd (an
 *    --init box). A systemd container that also has Isotope's isotope
 *    binary passes the test; `isotope glibc run` then refuses there with
 *    its own sentence (no Docker, or the live system), which is acceptable.
 *    A device and inode test against /proc/self/exe is not used: once a
 *    systemd-sysext overlay is merged over /usr, the path reports the
 *    overlay's device and never matches the file the rule opened.
 *
 *  - Anywhere else (a Docker container, a Distrobox, the isotope-glibc box),
 *    it runs the AppImage's own runtime, as before this rule existed: it
 *    copies the file into a memfd, clears the magic (bytes 8 to 10, in the
 *    ELF e_ident padding, which the kernel ignores) and fexecve()s the memfd
 *    with the original arguments and environment. Without the magic the rule
 *    does not match again, so it does not loop. The runtime reads its
 *    squashfs from /proc/self/exe, which is the memfd and holds the whole
 *    file. The runtime sets APPIMAGE itself, from that link, so the app sees
 *    the memfd's name there (TARGET_APPIMAGE would fix that, but the runtime
 *    never unsets it, so a nested AppImage would run the wrong file).
 *
 * It refuses with a sentence and exit 127 when it runs setuid or setgid
 * (AT_SECURE, or an effective user or group that is not the real one): a
 * privileged caller's environment is not trusted. It is static, links no
 * library, runs no shell and never looks anything up in PATH. Built with
 * -DTESTING, the env var ISOTOPE_APPIMAGE_TEST_ROOT prefixes the two paths it
 * looks at (/usr/bin/isotope and /run/systemd/system); a production build has
 * no such hook.
 */
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/auxv.h>
#include <sys/mman.h>
#include <sys/sendfile.h>
#include <sys/stat.h>
#include <unistd.h>

#ifndef MFD_EXEC
#define MFD_EXEC 0x0010U
#endif

#define ISOTOPE_PATH "/usr/bin/isotope"
#define SYSTEMD_PATH "/run/systemd/system"

/* vm.memfd_noexec=2 (per PID namespace, so a container can set it) refuses
 * an executable memfd, or the exec of one. */
#define NOEXEC_WHY "this system does not allow running a program from memory (vm.memfd_noexec is 2)"
#define NOEXEC_TODO "Run it with --appimage-extract-and-run, or set vm.memfd_noexec to 0 or 1."

extern char **environ;

static const char *rooted(const char *path, char *buf, size_t n)
{
#ifdef TESTING
	const char *root = getenv("ISOTOPE_APPIMAGE_TEST_ROOT");
	if (root && *root) {
		snprintf(buf, n, "%s%s", root, path);
		return buf;
	}
#endif
	(void)buf;
	(void)n;
	return path;
}

static void refuse(const char *prog, const char *why, const char *todo)
{
	fprintf(stderr, "%s is an AppImage. Isotope could not start it: %s. %s "
		"See `isotope glibc --help`.\n", prog, why, todo);
	exit(127);
}

/* True on the host: booted with systemd, and iso is an executable regular
 * file (see the top of the file). */
static int on_host(const char *iso)
{
	char buf[PATH_MAX];
	struct stat st;

	if (stat(rooted(SYSTEMD_PATH, buf, sizeof buf), &st) != 0 || !S_ISDIR(st.st_mode))
		return 0;
	return stat(iso, &st) == 0 && S_ISREG(st.st_mode) && access(iso, X_OK) == 0;
}

/* Copies all of in to out, from offset 0. */
static int copy_all(int in, int out, off_t size)
{
	char buf[65536];
	off_t done = 0;
	ssize_t n;

	while (done < size) {
		n = sendfile(out, in, NULL, (size_t)(size - done) > 0x7ffff000 ? 0x7ffff000 : (size_t)(size - done));
		if (n < 0 && errno == EINTR)
			continue;
		if (n <= 0)
			break;
		done += n;
	}
	if (done == size)
		return 0;
	/* sendfile refused (or the file changed): copy by hand. */
	if (lseek(in, 0, SEEK_SET) < 0 || lseek(out, 0, SEEK_SET) < 0 || ftruncate(out, 0) < 0)
		return -1;
	for (;;) {
		n = read(in, buf, sizeof buf);
		if (n < 0 && errno == EINTR)
			continue;
		if (n < 0)
			return -1;
		if (n == 0)
			return 0;
		for (ssize_t w = 0; w < n;) {
			ssize_t m = write(out, buf + w, (size_t)(n - w));
			if (m < 0 && errno == EINTR)
				continue;
			if (m <= 0)
				return -1;
			w += m;
		}
	}
}

/* Runs the AppImage's own runtime from a memfd copy without the magic. */
static void run_runtime(const char *prog, char **argv)
{
	static const char magic[3] = { 'A', 'I', 2 }, zero[3] = { 0, 0, 0 };
	char got[3];
	struct stat st;
	int in, mfd;

	in = open(prog, O_RDONLY | O_CLOEXEC);
	if (in < 0)
		refuse(prog, strerror(errno), "Check that the file is there and that you can read it.");
	if (fstat(in, &st) != 0 || !S_ISREG(st.st_mode))
		refuse(prog, "it is not a regular file", "Give the path of the AppImage file.");
	if (pread(in, got, sizeof got, 8) != (ssize_t)sizeof got || memcmp(got, magic, sizeof magic) != 0)
		refuse(prog, "it is not a type 2 AppImage", "Run it with the program it was made for.");

	mfd = memfd_create("isotope-appimage", MFD_CLOEXEC | MFD_EXEC);
	if (mfd < 0 && errno == EINVAL) /* a kernel before 6.3 has no MFD_EXEC */
		mfd = memfd_create("isotope-appimage", MFD_CLOEXEC);
	if (mfd < 0 && (errno == EACCES || errno == EPERM))
		refuse(prog, NOEXEC_WHY, NOEXEC_TODO);
	if (mfd < 0)
		refuse(prog, strerror(errno), "Free some memory, or run it with --appimage-extract-and-run.");
	if (copy_all(in, mfd, st.st_size) != 0)
		refuse(prog, "it could not be copied into memory", "Free some memory, or run it with --appimage-extract-and-run.");
	close(in);
	if (pwrite(mfd, zero, sizeof zero, 8) != (ssize_t)sizeof zero)
		refuse(prog, "its copy in memory could not be changed", "Run it with --appimage-extract-and-run.");

	fexecve(mfd, argv, environ);
	if (errno == EACCES || errno == EPERM)
		refuse(prog, NOEXEC_WHY, NOEXEC_TODO);
	refuse(prog, strerror(errno), "Run it with --appimage-extract-and-run.");
}

int main(int argc, char **argv)
{
	char buf[PATH_MAX];
	const char *prog, *iso;
	char **nargv;
	int i;

	if (argc < 2) {
		fprintf(stderr,
			"usage: %s <AppImage> [arguments]\n"
			"This is Isotope's handler for AppImages, which the kernel starts. "
			"See `isotope glibc --help`.\n", argc > 0 ? argv[0] : "isotope-appimage");
		return 127;
	}
	prog = argv[1];

	/* Before anything reads the environment or a path. */
	if (getauxval(AT_SECURE) || geteuid() != getuid() || getegid() != getgid())
		refuse(prog, "it runs setuid or setgid, and Isotope does not start setuid or setgid programs for you",
		       "Run it as the user and group it belongs to.");

	iso = rooted(ISOTOPE_PATH, buf, sizeof buf);
	if (on_host(iso)) {
		/* isotope glibc run --appimage -- <prog> <args...> NULL */
		nargv = calloc((size_t)argc + 5, sizeof *nargv);
		if (!nargv)
			refuse(prog, "there is no memory left", "Free some memory and try again.");
		nargv[0] = (char *)iso;
		nargv[1] = "glibc";
		nargv[2] = "run";
		nargv[3] = "--appimage";
		nargv[4] = "--";
		for (i = 1; i < argc; i++)
			nargv[4 + i] = argv[i];
		execv(iso, nargv);
		fprintf(stderr, "%s is an AppImage. Isotope could not start %s (%s). "
			"See `isotope glibc --help`.\n", prog, ISOTOPE_PATH, strerror(errno));
		return 127;
	}

	/* The original arguments: argv[0] is the AppImage's path, as the kernel
	 * gives no other (no P flag). */
	run_runtime(prog, argv + 1);
	return 127;
}

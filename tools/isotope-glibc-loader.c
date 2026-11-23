/*
 * isotope-glibc-loader: the stand-in for glibc's dynamic loader.
 *
 * Installed as /usr/lib/ld-linux-x86-64.so.2 (reached as
 * /lib64/ld-linux-x86-64.so.2). Isotope is a musl system, so a program built
 * for glibc names that path as its interpreter and fails to start. The
 * kernel runs this file in its place, and it hands the program to
 * `isotope glibc run`, which runs it in a glibc container for the user:
 *
 *   /usr/bin/isotope glibc run --loader -- <absolute program path> <argv[1..]>
 *
 * with ISOTOPE_GLIBC_ARGV0=<the program's original argv[0]> in the
 * environment. When the kernel starts the program, getauxval(AT_EXECFN) is
 * its path and argv is as the caller gave it. When someone runs the loader
 * directly (`ld-linux-x86-64.so.2 ./prog args`), the loader is the main
 * program, argv[1] is the program and the rest are its arguments.
 *
 * It cannot hand off a setuid or setgid program (AT_SECURE), for root, on
 * the live system, when the user turned the feature off, when it is already
 * running inside the hand-off (a loop) or when /usr/bin/isotope is missing. It then says why and what to do on
 * stderr and exits 127.
 *
 * It is static, links no library, runs no shell, never looks isotope up in
 * PATH and never writes to disk. Built with -DTESTING, the env var
 * ISOTOPE_GLIBC_LOADER_TEST_ROOT prefixes the two paths it reads (the live
 * marker and isotope); a production build has no such hook.
 */
#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/auxv.h>
#include <unistd.h>

#define ISOTOPE_PATH "/usr/bin/isotope"
#define LIVE_MARKER "/run/isotope/live-desktop"
#define GUARD_ENV "ISOTOPE_GLIBC_LOADER_ACTIVE"
#define AUTO_ENV "ISOTOPE_GLIBC_AUTO"
#define ARGV0_ENV "ISOTOPE_GLIBC_ARGV0"

extern char **environ;
extern void _start(void);

static const char *rooted(const char *path, char *buf, size_t n)
{
#ifdef TESTING
	const char *root = getenv("ISOTOPE_GLIBC_LOADER_TEST_ROOT");
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
	fprintf(stderr,
		"%s is a program built for glibc. Isotope runs these in a glibc "
		"container for your user, but %s. %s "
		"See `isotope glibc --help`.\n",
		prog, why, todo);
	exit(127);
}

/* True when the opt-out file says auto=off. A missing or unreadable file, or
 * any other content, means on. Only the key "auto" is read. */
static int config_auto_off(void)
{
	char path[PATH_MAX], buf[4096];
	const char *xdg = getenv("XDG_CONFIG_HOME"), *home = getenv("HOME");
	int fd, off = 0;
	ssize_t n;
	char *line, *next;

	if (xdg && *xdg)
		snprintf(path, sizeof path, "%s/isotope/glibc", xdg);
	else if (home && *home)
		snprintf(path, sizeof path, "%s/.config/isotope/glibc", home);
	else
		return 0;
	fd = open(path, O_RDONLY | O_CLOEXEC);
	if (fd < 0)
		return 0;
	n = read(fd, buf, sizeof buf - 1);
	close(fd);
	if (n <= 0)
		return 0;
	buf[n] = 0;
	for (line = buf; line; line = next) {
		char *eq, *k, *v, *e;
		next = strchr(line, '\n');
		if (next)
			*next++ = 0;
		while (*line == ' ' || *line == '\t')
			line++;
		if (*line == '#')
			continue;
		eq = strchr(line, '=');
		if (!eq)
			continue;
		*eq = 0;
		k = line;
		v = eq + 1;
		for (e = eq; e > k && (e[-1] == ' ' || e[-1] == '\t'); e--)
			e[-1] = 0;
		while (*v == ' ' || *v == '\t')
			v++;
		for (e = v + strlen(v); e > v && (e[-1] == ' ' || e[-1] == '\t' || e[-1] == '\r'); e--)
			e[-1] = 0;
		if (strcmp(k, "auto") == 0)
			off = strcmp(v, "off") == 0;
	}
	return off;
}

int main(int argc, char **argv)
{
	char pbuf[PATH_MAX], cwd[PATH_MAX], abs[PATH_MAX], tbuf[PATH_MAX];
	const char *prog, *argv0, *a;
	char **args;
	int nargs, direct, i, n;

	/* Run directly, the loader is the main program: the auxv entry point is
	 * its own. Started by the kernel as an interpreter, it is the program's. */
	direct = getauxval(AT_ENTRY) == (unsigned long)(void *)&_start;
	if (direct) {
		if (argc < 2) {
			fprintf(stderr,
				"usage: %s <program built for glibc> [arguments]\n"
				"This is Isotope's stand-in for glibc's loader. "
				"See `isotope glibc --help`.\n", argv[0]);
			return 127;
		}
		prog = argv[1];
		argv0 = argv[1];
		args = argv + 2;
		nargs = argc - 2;
	} else {
		prog = (const char *)getauxval(AT_EXECFN);
		if (!prog || !*prog)
			prog = argc > 0 ? argv[0] : "this program";
		argv0 = argc > 0 ? argv[0] : prog;
		args = argv + 1;
		nargs = argc > 0 ? argc - 1 : 0;
	}

	/* Started setuid or setgid, the loader runs with the program's
	 * credentials, and isotope would run a distrobox and docker of the
	 * caller's choosing (PATH, HOME) with them. glibc's ld.so has a secure
	 * mode for this; the stand-in refuses before it reads the environment. */
	if (getauxval(AT_SECURE) || geteuid() != getuid() || getegid() != getgid())
		refuse(prog, "it is setuid or setgid, and setuid and setgid glibc programs are not handed to the container",
		       "Run it on a glibc system, or in a glibc container of your own.");

	/* An absolute path against the cwd, symlinks kept as they are. */
	if (prog[0] != '/') {
		const char *p = prog;
		while (p[0] == '.' && p[1] == '/')
			p += 2;
		if (!getcwd(cwd, sizeof cwd))
			refuse(prog, "its path cannot be made absolute (the current directory is not readable)",
			       "Run it from a directory you can read, or give its full path.");
		n = snprintf(abs, sizeof abs, "%s%s%s", cwd,
			     strcmp(cwd, "/") ? "/" : "", p);
		if (n < 0 || (size_t)n >= sizeof abs)
			refuse(prog, "its path is too long", "Move it to a shorter path.");
		prog = abs;
	}

	if (getenv(GUARD_ENV))
		refuse(prog, "it was already handed to the container, so it was not handed over again (that would loop)",
		       "The container may be missing a glibc loader; check `isotope glibc --help`.");
	if (geteuid() == 0 || getuid() == 0)
		refuse(prog, "this is the root user, which has no container",
		       "Run it as a normal user.");
	a = getenv(AUTO_ENV);
	if ((a && strcmp(a, "0") == 0) || config_auto_off())
		refuse(prog, "running them automatically is turned off for you",
		       "Run `isotope glibc run -- <program>` yourself, or turn it on with `isotope glibc auto on`.");
	if (access(rooted(LIVE_MARKER, tbuf, sizeof tbuf), F_OK) == 0)
		refuse(prog, "this is the live system, which has no container and nowhere to keep one",
		       "Install Isotope, then run it as a normal user.");
	const char *iso = rooted(ISOTOPE_PATH, pbuf, sizeof pbuf);
	if (access(iso, X_OK) != 0)
		refuse(prog, ISOTOPE_PATH " is missing",
		       "Reinstall the isotope tool, or run the program in a glibc container yourself.");

	/* argv: isotope glibc run --loader -- <prog> <args...> NULL */
	char **nargv = calloc((size_t)nargs + 7, sizeof *nargv);
	size_t ne = 0;
	while (environ && environ[ne])
		ne++;
	char **nenv = calloc(ne + 2, sizeof *nenv);
	char *ev = malloc(strlen(ARGV0_ENV) + strlen(argv0) + 2);
	if (!nargv || !nenv || !ev)
		refuse(prog, "there is no memory left", "Free some memory and try again.");
	sprintf(ev, "%s=%s", ARGV0_ENV, argv0);
	nargv[0] = (char *)iso;
	nargv[1] = "glibc";
	nargv[2] = "run";
	nargv[3] = "--loader";
	nargv[4] = "--";
	nargv[5] = (char *)prog;
	for (i = 0; i < nargs; i++)
		nargv[6 + i] = args[i];
	n = 0;
	for (size_t j = 0; j < ne; j++)
		if (strncmp(environ[j], ARGV0_ENV "=", strlen(ARGV0_ENV) + 1) != 0)
			nenv[n++] = environ[j];
	nenv[n++] = ev;

	execve(iso, nargv, nenv);
	fprintf(stderr,
		"%s is a program built for glibc. Isotope could not start %s (%s). "
		"See `isotope glibc --help`.\n",
		prog, ISOTOPE_PATH, strerror(errno));
	return 127;
}

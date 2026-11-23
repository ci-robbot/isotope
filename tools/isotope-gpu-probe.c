/*
 * isotope-gpu-probe: can Mesa draw on this machine's GPU in hardware?
 *
 * Mesa's loader picks a driver from the GPU's PCI id. When the image has no
 * such driver (an old Radeon on an image without r600, any real GPU on the vm
 * image line) or the driver fails, Mesa does not fall back to software by
 * itself: EGL fails and the compositor exits, or draws only a cursor. The
 * sessions and the installer run this probe first, and start the compositor
 * with LIBGL_ALWAYS_SOFTWARE=1 and GBM_ALWAYS_SOFTWARE=1 (llvmpipe, and
 * kms_swrast for GBM) when it says no.
 *
 * It opens the primary DRM device, the one wlroots and smithay render on:
 * the firmware's boot VGA card, else the card with the lowest number. When
 * that is only the firmware's framebuffer (simpledrm, efifb) and the kernel
 * command line has no nomodeset, the real driver may still be loading (the
 * live installer starts early): it waits for udev (`udevadm settle`, at most
 * 5 seconds) and looks once more. It prefers the card's render node,
 * makes a GBM device on it, an EGL display on the GBM platform, and a
 * surfaceless OpenGL ES 2 context, and reads GL_RENDERER: what a wlroots,
 * smithay or cosmic-comp compositor does at start.
 *
 * Output is one line on stdout:
 *   exit 0: the renderer, a hardware driver works ("AMD CEDAR (...)")
 *   exit 1: "<class> <kernel driver>: <reason>", no hardware driver works
 *           ("hardware radeon: no Mesa driver for this GPU (EGL failed)"), the
 *           renderer is llvmpipe or softpipe ("virtual virtio_gpu: software
 *           (llvmpipe)"), or the driver crashed or hung (it runs in a child
 *           process, with a 5 second limit). The class says what the GPU is:
 *             hardware  a real GPU, whose Mesa driver is missing or fails
 *             virtual   a virtual machine's GPU without 3D (virtio_gpu,
 *                       qxl, bochs, cirrus, vmwgfx, vboxvideo, hyperv_drm),
 *                       or the firmware framebuffer in a virtual machine:
 *                       software is normal there
 *             firmware  only the firmware's framebuffer (simpledrm, efifb)
 *                       on real hardware: the kernel driver is off
 *                       (nomodeset) or not loaded yet
 *   exit 2: "<reason>", no DRM device to test, or none this user may open
 * Mesa's own messages ("r600: driver missing") go to /dev/null: the probe
 * runs before a login session and must not print on the console. -v keeps
 * them, for debugging.
 *
 * libgbm, libEGL and libGLESv2 are dlopened, not linked, so the probe builds
 * with libc alone and runs with whichever EGL the image has: glvnd's on the
 * Wayland variants, Mesa's own on i3.
 */
#define _GNU_SOURCE
#include <dirent.h>
#include <dlfcn.h>
#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/wait.h>
#include <unistd.h>

typedef void *EGLDisplay;
typedef void *EGLConfig;
typedef void *EGLContext;
typedef void *EGLSurface;
typedef int32_t EGLint;
typedef unsigned int EGLenum;
typedef unsigned int EGLBoolean;

#define EGL_NONE 0x3038
#define EGL_OPENGL_ES_API 0x30A0
#define EGL_CONTEXT_CLIENT_VERSION 0x3098
#define EGL_PLATFORM_GBM_KHR 0x31D7
#define GL_RENDERER 0x1F01

typedef void *(*GbmCreateDeviceFn)(int);
typedef void *(*GetProcAddressFn)(const char *);
typedef EGLDisplay (*GetPlatformDisplayFn)(EGLenum, void *, const void *);
typedef EGLBoolean (*InitializeFn)(EGLDisplay, EGLint *, EGLint *);
typedef EGLBoolean (*BindAPIFn)(EGLenum);
typedef EGLContext (*CreateContextFn)(EGLDisplay, EGLConfig, EGLContext, const EGLint *);
typedef EGLBoolean (*MakeCurrentFn)(EGLDisplay, EGLSurface, EGLSurface, EGLContext);
typedef const unsigned char *(*GetStringFn)(unsigned int);

#define SYS_DRM "/sys/class/drm"

static char kdriver[64] = "unknown";
static const char *gpu_class = "hardware";

/* no: print "<kernel driver>: <reason>" and exit 1 */
static int no(const char *reason)
{
	printf("%s %s: %s\n", gpu_class, kdriver, reason);
	return 1;
}

/* copy copies src into dst, cut to fit. */
static void copy(char *dst, size_t len, const char *src)
{
	size_t n = strnlen(src, len - 1);
	memcpy(dst, src, n);
	dst[n] = 0;
}

static int read_line(const char *path, char *buf, size_t len)
{
	FILE *f = fopen(path, "r");
	if (!f)
		return -1;
	if (!fgets(buf, (int)len, f)) {
		fclose(f);
		return -1;
	}
	fclose(f);
	buf[strcspn(buf, "\n")] = 0;
	return 0;
}

/* is_card says whether a /sys/class/drm entry is a card ("card0"), not a
 * connector ("card0-eDP-1") or a render node. */
static int is_card(const char *name)
{
	const char *p;
	if (strncmp(name, "card", 4) != 0 || !name[4])
		return 0;
	for (p = name + 4; *p; p++)
		if (*p < '0' || *p > '9')
			return 0;
	return 1;
}

static int boot_vga(const char *card)
{
	char path[PATH_MAX], v[8];
	snprintf(path, sizeof path, SYS_DRM "/%s/device/boot_vga", card);
	return read_line(path, v, sizeof v) == 0 && strcmp(v, "1") == 0;
}

/* primary_card writes the name of the card to test into out: the boot VGA
 * card, else the card with the lowest number, which is the first card in
 * udev's order. wlroots (wlr_session_find_gpus) and smithay (primary_gpu)
 * pick the same way: the boot VGA card, else the first card, whatever is
 * connected. */
static int primary_card(char *out, size_t len)
{
	DIR *d = opendir(SYS_DRM);
	struct dirent *e;
	int best = -1, best_num = INT_MAX;
	if (!d)
		return -1;
	while ((e = readdir(d))) {
		int score, num;
		if (!is_card(e->d_name))
			continue;
		num = atoi(e->d_name + 4);
		score = boot_vga(e->d_name);
		if (score > best || (score == best && num < best_num)) {
			best = score;
			best_num = num;
			copy(out, len, e->d_name);
		}
	}
	closedir(d);
	return best < 0 ? -1 : 0;
}

static void kernel_driver(const char *card)
{
	char path[PATH_MAX], target[PATH_MAX];
	ssize_t n;
	snprintf(path, sizeof path, SYS_DRM "/%s/device/driver", card);
	n = readlink(path, target, sizeof target - 1);
	if (n > 0) {
		const char *base;
		target[n] = 0;
		base = strrchr(target, '/');
		copy(kdriver, sizeof kdriver, base ? base + 1 : target);
	}
}

static int in_list(const char *name, const char *const *list)
{
	for (; *list; list++)
		if (strcmp(name, *list) == 0)
			return 1;
	return 0;
}

/* in_vm says whether this runs in a virtual machine: the CPU's hypervisor
 * flag (x86), or a Xen hypervisor. */
static int in_vm(void)
{
	FILE *f = fopen("/proc/cpuinfo", "r");
	char line[4096];
	int vm = 0;
	if (f) {
		while (!vm && fgets(line, sizeof line, f))
			vm = strncmp(line, "flags", 5) == 0 && strstr(line, " hypervisor") != NULL;
		fclose(f);
	}
	return vm || access("/sys/hypervisor/type", F_OK) == 0;
}

/* classify sets gpu_class from the kernel driver. */
static void classify(void)
{
	static const char *const virtual_gpus[] = {
		"virtio_gpu", "virtio-gpu", "qxl", "bochs", "bochs-drm", "cirrus",
		"cirrus-qemu", "vmwgfx", "vboxvideo", "hyperv_drm", "hyperv_fb",
		"xen-fbfront", NULL};
	static const char *const firmware_fbs[] = {
		"simple-framebuffer", "simpledrm", "efifb", "efidrm", "vesafb",
		"vesadrm", "ofdrm", NULL};
	if (in_list(kdriver, virtual_gpus))
		gpu_class = "virtual";
	else if (in_list(kdriver, firmware_fbs))
		gpu_class = in_vm() ? "virtual" : "firmware";
}

/* cmdline_has says whether the kernel command line has the word w. */
static int cmdline_has(const char *w)
{
	char buf[4096], *tok, *save;
	FILE *f = fopen("/proc/cmdline", "r");
	size_t n;
	if (!f)
		return 0;
	n = fread(buf, 1, sizeof buf - 1, f);
	fclose(f);
	buf[n] = 0;
	for (tok = strtok_r(buf, " \t\n", &save); tok; tok = strtok_r(NULL, " \t\n", &save))
		if (strcmp(tok, w) == 0)
			return 1;
	return 0;
}

/* udev_settle runs `udevadm settle --timeout=5`, quietly, and waits. */
static void udev_settle(void)
{
	pid_t pid = fork();
	int status;
	if (pid == 0) {
		int null = open("/dev/null", O_RDWR | O_CLOEXEC);
		if (null >= 0) {
			dup2(null, 1);
			dup2(null, 2);
		}
		execlp("udevadm", "udevadm", "settle", "--timeout=5", (char *)NULL);
		_exit(127);
	}
	if (pid > 0)
		while (waitpid(pid, &status, 0) < 0 && errno == EINTR)
			;
}

/* find_card picks the card, its kernel driver and its class. */
static int find_card(char *card, size_t len)
{
	if (primary_card(card, len) < 0)
		return -1;
	copy(kdriver, sizeof kdriver, "unknown");
	gpu_class = "hardware";
	kernel_driver(card);
	classify();
	return 0;
}

/* open_device opens the card's render node, or the card node when it has
 * none (simpledrm, some virtual GPUs) or the render node cannot be opened. */
static int open_device(const char *card)
{
	char path[PATH_MAX];
	DIR *d;
	struct dirent *e;
	int fd = -1;
	snprintf(path, sizeof path, SYS_DRM "/%s/device/drm", card);
	if ((d = opendir(path))) {
		while (fd < 0 && (e = readdir(d))) {
			if (strncmp(e->d_name, "renderD", 7) != 0)
				continue;
			snprintf(path, sizeof path, "/dev/dri/%s", e->d_name);
			fd = open(path, O_RDWR | O_CLOEXEC);
		}
		closedir(d);
	}
	if (fd < 0) {
		snprintf(path, sizeof path, "/dev/dri/%s", card);
		fd = open(path, O_RDWR | O_CLOEXEC);
	}
	return fd;
}

/* gl_test runs in the child: GBM, EGL, a context, GL_RENDERER. It prints the
 * one line and returns the exit status. */
static int gl_test(int fd)
{
	void *gbm_lib, *egl_lib, *gbm;
	GbmCreateDeviceFn gbm_create_device;
	GetProcAddressFn get_proc;
	GetPlatformDisplayFn get_display;
	InitializeFn initialize;
	BindAPIFn bind_api;
	CreateContextFn create_context;
	MakeCurrentFn make_current;
	GetStringFn get_string;
	EGLDisplay dpy;
	EGLContext ctx;
	EGLint major, minor;
	static const EGLint ctx_attribs[] = {EGL_CONTEXT_CLIENT_VERSION, 2, EGL_NONE};
	const char *renderer;

	gbm_lib = dlopen("libgbm.so.1", RTLD_NOW | RTLD_LOCAL);
	egl_lib = dlopen("libEGL.so.1", RTLD_NOW | RTLD_GLOBAL);
	if (!gbm_lib || !egl_lib)
		return no("no Mesa GBM or EGL library");
	gbm_create_device = (GbmCreateDeviceFn)dlsym(gbm_lib, "gbm_create_device");
	get_proc = (GetProcAddressFn)dlsym(egl_lib, "eglGetProcAddress");
	if (!gbm_create_device || !get_proc)
		return no("no Mesa GBM or EGL library");

	gbm = gbm_create_device(fd);
	if (!gbm)
		return no("no Mesa driver for this GPU (GBM failed)");

	get_display = (GetPlatformDisplayFn)get_proc("eglGetPlatformDisplayEXT");
	if (!get_display)
		get_display = (GetPlatformDisplayFn)get_proc("eglGetPlatformDisplay");
	initialize = (InitializeFn)get_proc("eglInitialize");
	bind_api = (BindAPIFn)get_proc("eglBindAPI");
	create_context = (CreateContextFn)get_proc("eglCreateContext");
	make_current = (MakeCurrentFn)get_proc("eglMakeCurrent");
	if (!get_display || !initialize || !bind_api || !create_context || !make_current)
		return no("the EGL library lacks a function");

	dpy = get_display(EGL_PLATFORM_GBM_KHR, gbm, NULL);
	if (!dpy || !initialize(dpy, &major, &minor))
		return no("no Mesa driver for this GPU (EGL failed)");
	if (!bind_api(EGL_OPENGL_ES_API))
		return no("no OpenGL ES (EGL failed)");
	/* EGL_KHR_no_config_context and EGL_KHR_surfaceless_context, which
	 * Mesa has on every platform. */
	ctx = create_context(dpy, NULL, NULL, ctx_attribs);
	if (!ctx || !make_current(dpy, NULL, NULL, ctx))
		return no("no OpenGL ES 2 context (EGL failed)");

	get_string = (GetStringFn)get_proc("glGetString");
	if (!get_string) {
		void *gles = dlopen("libGLESv2.so.2", RTLD_NOW | RTLD_LOCAL);
		if (gles)
			get_string = (GetStringFn)dlsym(gles, "glGetString");
	}
	if (!get_string)
		return no("no glGetString");
	renderer = (const char *)get_string(GL_RENDERER);
	if (!renderer)
		return no("the driver has no renderer name");
	if (strstr(renderer, "llvmpipe"))
		return no("software (llvmpipe)");
	if (strstr(renderer, "softpipe"))
		return no("software (softpipe)");
	printf("%s\n", renderer);
	/* No teardown: the process ends here, and a driver's teardown is one
	 * more place to crash. */
	return 0;
}

static volatile sig_atomic_t timed_out;

static void on_alarm(int sig)
{
	(void)sig;
	timed_out = 1;
}

int main(int argc, char **argv)
{
	int verbose = argc > 1 && strcmp(argv[1], "-v") == 0;
	char card[256];
	int fd, status;
	pid_t child;
	struct sigaction sa;

	if (argc > 1 && !verbose) {
		fprintf(stderr, "usage: isotope-gpu-probe [-v]\n");
		return 2;
	}
	setvbuf(stdout, NULL, _IOLBF, 0);
	/* A caller that ignores SIGCHLD would make the kernel reap the test
	 * process, and waitpid fail: a working GPU would look broken. */
	signal(SIGCHLD, SIG_DFL);

	if (find_card(card, sizeof card) < 0) {
		printf("no DRM device\n");
		return 2;
	}
	/* Only the firmware's framebuffer on real hardware, with no
	 * nomodeset: the GPU's own driver may still be loading. Wait for
	 * udev once, then look again. */
	if (strcmp(gpu_class, "firmware") == 0 && !cmdline_has("nomodeset")) {
		udev_settle();
		if (find_card(card, sizeof card) < 0) {
			printf("no DRM device\n");
			return 2;
		}
	}
	fd = open_device(card);
	if (fd < 0) {
		printf("%s: cannot open %s: %s\n", kdriver, card, strerror(errno));
		return 2;
	}

	/* The driver is loaded in a child, so a driver that crashes or hangs
	 * gives an answer too: a broken driver is one more reason for
	 * software rendering. */
	fflush(stdout);
	child = fork();
	if (child < 0) {
		printf("%s: cannot fork: %s\n", kdriver, strerror(errno));
		return 2;
	}
	if (child == 0) {
		/* The question is what the hardware does: the software flags
		 * an earlier fallback left in the environment (EGL's and
		 * GBM's) must not answer it. */
		unsetenv("LIBGL_ALWAYS_SOFTWARE");
		unsetenv("GBM_ALWAYS_SOFTWARE");
		setenv("MESA_SHADER_CACHE_DISABLE", "true", 1);
		if (!verbose) {
			int null = open("/dev/null", O_WRONLY | O_CLOEXEC);
			setenv("EGL_LOG_LEVEL", "fatal", 1);
			setenv("MESA_DEBUG", "silent", 1);
			setenv("MESA_LOG_LEVEL", "error", 1);
			if (null >= 0) {
				dup2(null, 2);
				close(null);
			}
		}
		_exit(gl_test(fd));
	}

	memset(&sa, 0, sizeof sa);
	sa.sa_handler = on_alarm; /* no SA_RESTART: waitpid returns EINTR */
	sigaction(SIGALRM, &sa, NULL);
	alarm(5);
	while (waitpid(child, &status, 0) < 0) {
		if (errno != EINTR)
			return no("lost the test process");
		if (timed_out) {
			kill(child, SIGKILL);
			waitpid(child, &status, 0);
			return no("the graphics driver did not answer in time");
		}
	}
	alarm(0);
	if (WIFEXITED(status))
		return WEXITSTATUS(status);
	{
		char reason[64];
		snprintf(reason, sizeof reason, "the graphics driver crashed (signal %d)",
			 WIFSIGNALED(status) ? WTERMSIG(status) : 0);
		return no(reason);
	}
}

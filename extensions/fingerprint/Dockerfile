# Fingerprint sign-in, as an Isotope system extension: fprintd and libfprint.
# See extensions/README.md for how an extension is built and published, and
# docs/superpowers/specs/2026-11-24-fingerprint-design.md for the design.
#
# What it brings: fprintd (the D-Bus service net.reactivated.Fprint, started
# by D-Bus through fprintd.service), libfprint with its default drivers and
# their udev rules, the fprintd-enroll, -list, -verify and -delete tools,
# fprintd's polkit actions and D-Bus policy, and the PAM module
# pam_fprintd.so.
#
# The PAM side: the image's lock screens, sudo and polkit carry their
# fingerprint lines already, each one starting with "-", which makes PAM
# skip a line whose module is missing. This extension brings the modules,
# so the lines only work while it is merged:
#   /usr/lib/security/pam_fprintd.so       fprintd's module
#   /usr/lib/security/pam_isotope_unix.so  a link to the image's pam_unix.so,
#                                          for swaylock and i3lock, which read
#                                          the password before PAM runs: their
#                                          services check that password first
#                                          and ask for a finger only when it is
#                                          wrong (or a single key was typed)
# The login screens never ask for a finger: they keep the password, which
# unlocks the login keyring (gnome-keyring).
#
# Its own libraries (libfprint, libgusb, libusb, json-glib, pixman, libgudev,
# libpolkit-gobject) and the fprintd daemon live in
# /usr/lib/isotope-ext/fingerprint, found through a RUNPATH: the desktops
# have some of them in /usr/lib, and a second copy there is what
# `isotope ext` refuses. /usr/libexec is not used: Kairos merges only
# /usr/bin, /usr/sbin, /usr/lib, /usr/share, /usr/include and /usr/src
# (SYSTEMD_SYSEXT_HIERARCHIES). /etc is not part of a system extension, so
# fprintd runs without /etc/fprintd.conf and takes its built-in default, the
# file storage in /var/lib/fprint, which the image keeps on the persistent
# partition (94_isotope_state.yaml). GLib, libsystemd, libudev, libpam and
# OpenSSL's libcrypto come from the image; the build stage fails if an ELF
# file needs anything else.

# Set by .github/workflows/extensions.yml from scripts/bump-hadron.sh, so the
# extension builds on the same Hadron release as the image.
ARG HADRON_VERSION

FROM ghcr.io/kairos-io/hadron-toolchain:${HADRON_VERSION} AS build
ENV PKG_CONFIG_PATH=/usr/lib/isotope-ext/fingerprint/pkgconfig
RUN pip3 install meson ninja packaging
WORKDIR /build

# Build-only dependencies, installed into this stage's root and never into
# /output: libucontext (the toolchain's libsystemd and libudev link it; the
# Hadron base ships it), PCRE2 and GLib at the versions the image builds
# (the image's own copy is what runs), and gettext's msgfmt, which fprintd's
# polkit policy and translations are made with. Same versions and Renovate
# rules as the Dockerfile at the root, so one Renovate PR moves both.
# renovate: datasource=github-tags depName=kaniini/libucontext extractVersion=^libucontext-(?<version>\d+(\.\d+)*)$
ARG LIBUCONTEXT_VERSION=1.5.1
RUN curl --retry-all-errors -fL --retry 5 https://github.com/kaniini/libucontext/archive/refs/tags/libucontext-${LIBUCONTEXT_VERSION}.tar.gz -o u.tar.gz && \
    tar -xf u.tar.gz && rm u.tar.gz && cd libucontext-* && \
    make -j"$(nproc)" ARCH=x86_64 && make ARCH=x86_64 install
# renovate: datasource=github-tags depName=PCRE2Project/pcre2 extractVersion=^pcre2-(?<version>\d+(\.\d+)*)$
ARG PCRE2_VERSION=10.44
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/PCRE2Project/pcre2/releases/download/pcre2-${PCRE2_VERSION}/pcre2-${PCRE2_VERSION}.tar.gz -o p.tar.gz && \
    tar -xzf p.tar.gz && rm p.tar.gz && cd pcre2-* && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-dependency-tracking && make -j"$(nproc)" && make install
# renovate: datasource=gitlab-tags depName=GNOME/glib registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG GLIB_VERSION=2.82.5
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.gnome.org/sources/glib/${GLIB_VERSION%.*}/glib-${GLIB_VERSION}.tar.xz -o g.tar.xz && \
    tar -xf g.tar.xz && rm g.tar.xz && cd glib-* && \
    meson setup buildDir ${COMMON_MESON_FLAGS} \
      -Dnls=disabled -Dtests=false -Dlibmount=disabled -Dselinux=disabled \
      -Dman-pages=disabled -Dintrospection=disabled -Dglib_debug=disabled \
      -Ddtrace=disabled -Dsysprof=disabled && \
    ninja -C buildDir install
ARG GETTEXT_VERSION=0.22.5
RUN for m in https://mirrors.kernel.org/gnu https://ftpmirror.gnu.org https://ftp.gnu.org/gnu; do curl -fL --connect-timeout 30 --retry 3 --retry-delay 3 --retry-all-errors "$m/gettext/gettext-${GETTEXT_VERSION}.tar.xz" -o gt.tar.xz && break; rm -f gt.tar.xz; done && test -s gt.tar.xz && \
    tar -xf gt.tar.xz && rm gt.tar.xz && cd gettext-${GETTEXT_VERSION} && \
    ./configure --prefix=/usr --disable-java --disable-csharp --disable-libasprintf \
      --disable-openmp --disable-acl --disable-curses --without-emacs --without-git \
      --disable-static --enable-shared && \
    make -j"$(nproc)" && make install && msgfmt --version >/dev/null
# For the test stage only, never the extension: a stand-in for the image's
# GLib.
RUN mkdir -p /glib-runtime/usr/lib && \
    cp -a /usr/lib/libglib-2.0.so.0* /usr/lib/libgobject-2.0.so.0* /usr/lib/libgio-2.0.so.0* \
      /usr/lib/libgmodule-2.0.so.0* /usr/lib/libpcre2-8.so.0* /usr/lib/libffi.so.* /glib-runtime/usr/lib/

# The extension's libraries: built shared into the private directory, with
# its RUNPATH, and installed twice: into / for the next builds and into
# /output for the extension. MESON_PRIVATE is the options they share.
ENV LDFLAGS="-Wl,-rpath,/usr/lib/isotope-ext/fingerprint"
ENV MESON_PRIVATE="--prefix=/usr --libdir=lib/isotope-ext/fingerprint --libexecdir=lib/isotope-ext/fingerprint --bindir=bin --sbindir=bin --buildtype=minsize -Dstrip=true"

# renovate: datasource=github-releases depName=libusb/libusb extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG LIBUSB_VERSION=1.0.30
RUN curl --retry-all-errors -fL --retry 5 https://github.com/libusb/libusb/releases/download/v${LIBUSB_VERSION}/libusb-${LIBUSB_VERSION}.tar.bz2 -o u.tar.bz2 && \
    tar -xjf u.tar.bz2 && rm u.tar.bz2 && mv libusb-${LIBUSB_VERSION} libusb-src && cd libusb-src && \
    cp /usr/share/automake-*/config.sub /usr/share/automake-*/config.guess . && \
    ./configure ${COMMON_CONFIGURE_ARGS} --libdir=/usr/lib/isotope-ext/fingerprint --enable-udev && \
    make -j"$(nproc)" && make install && make DESTDIR=/output install

# renovate: datasource=gitlab-tags depName=GNOME/json-glib registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG JSONGLIB_VERSION=1.10.8
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://download.gnome.org/sources/json-glib/${JSONGLIB_VERSION%.*}/json-glib-${JSONGLIB_VERSION}.tar.xz -o j.tar.xz && \
    tar -xf j.tar.xz && rm j.tar.xz && mv json-glib-* json-glib-src && cd json-glib-src && \
    meson setup buildDir ${MESON_PRIVATE} -Dintrospection=disabled -Ddocumentation=disabled -Dgtk_doc=disabled -Dman=false -Dtests=false && \
    ninja -C buildDir install && DESTDIR=/output ninja -C buildDir install

# libgusb: libfprint's USB layer. No tools, tests, docs, VAPI or
# introspection.
# renovate: datasource=github-releases depName=hughsie/libgusb
ARG LIBGUSB_VERSION=0.4.9
RUN curl --retry-all-errors -fL --retry 5 https://github.com/hughsie/libgusb/releases/download/${LIBGUSB_VERSION}/libgusb-${LIBGUSB_VERSION}.tar.xz -o gusb.tar.xz && \
    tar -xf gusb.tar.xz && rm gusb.tar.xz && mv libgusb-${LIBGUSB_VERSION} libgusb-src && cd libgusb-src && \
    meson setup buildDir ${MESON_PRIVATE} -Dtests=false -Dvapi=false -Ddocs=false -Dintrospection=false -Dumockdev=disabled && \
    ninja -C buildDir install && DESTDIR=/output ninja -C buildDir install

# pixman: the image drivers' (aes3500, aes4000) image resizing.
# renovate: datasource=gitlab-tags depName=pixman/pixman registryUrl=https://gitlab.freedesktop.org extractVersion=^pixman-(?<version>\d+(\.\d+)*)$
ARG PIXMAN_VERSION=0.46.0
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://www.cairographics.org/releases/pixman-${PIXMAN_VERSION}.tar.gz -o pixman.tar.gz && \
    tar -xzf pixman.tar.gz && rm pixman.tar.gz && mv pixman-${PIXMAN_VERSION} pixman-src && cd pixman-src && \
    meson setup buildDir ${MESON_PRIVATE} -Dtests=disabled -Ddemos=disabled -Dgtk=disabled -Dlibpng=disabled -Dopenmp=disabled && \
    ninja -C buildDir install && DESTDIR=/output ninja -C buildDir install

# libgudev: the SPI driver (elanspi) finds its reader through udev.
# renovate: datasource=gitlab-tags depName=GNOME/libgudev registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG LIBGUDEV_VERSION=238
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.gnome.org/sources/libgudev/${LIBGUDEV_VERSION}/libgudev-${LIBGUDEV_VERSION}.tar.xz -o g.tar.xz && \
    tar -xf g.tar.xz && rm g.tar.xz && mv libgudev-* libgudev-src && cd libgudev-src && \
    meson setup buildDir ${MESON_PRIVATE} -Dtests=disabled -Dintrospection=disabled -Dvapi=disabled -Dgtk_doc=false && \
    ninja -C buildDir install && DESTDIR=/output ninja -C buildDir install

# libpolkit-gobject-1, which fprintd asks polkit with: the libraries only
# (no polkitd, no helper), the image's polkit version. Only the library
# goes into the extension; polkitd and its rules are the image's.
# renovate: datasource=github-tags depName=polkit-org/polkit extractVersion=^(?<version>\d+(\.\d+)*)$
ARG POLKIT_VERSION=126
RUN curl --retry-all-errors -fL --retry 5 https://github.com/polkit-org/polkit/archive/refs/tags/${POLKIT_VERSION}.tar.gz -o pk.tar.gz && \
    tar -xzf pk.tar.gz && rm pk.tar.gz && mv polkit-${POLKIT_VERSION} polkit-src && cd polkit-src && \
    meson setup buildDir ${MESON_PRIVATE} -Dlibs-only=true -Dsession_tracking=logind \
      -Dauthfw=pam -Dpam_include=system-auth -Dgettext=false \
      -Dintrospection=false -Dman=false -Dgtk_doc=false -Dexamples=false -Dtests=false && \
    ninja -C buildDir install && \
    cp -a /usr/lib/isotope-ext/fingerprint/libpolkit-gobject-1.so* /output/usr/lib/isotope-ext/fingerprint/

# libfprint with its default drivers (every USB and SPI reader it supports,
# no virtual test drivers), the udev rules of the SPI reader, and none of
# its tests, docs, examples or introspection. The udev hwdb that lets the
# readers autosuspend is systemd's own (v248 and later). Without
# introspection, libfprint 1.94.100's tests/meson.build iterates a dict with
# one variable, which meson refuses; the sed names the second one (a no-op
# once upstream fixes the line).
# renovate: datasource=gitlab-tags depName=libfprint/libfprint registryUrl=https://gitlab.freedesktop.org extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG LIBFPRINT_VERSION=1.94.100
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://gitlab.freedesktop.org/libfprint/libfprint/-/archive/v${LIBFPRINT_VERSION}/libfprint-v${LIBFPRINT_VERSION}.tar.gz -o lf.tar.gz && \
    tar -xzf lf.tar.gz && rm lf.tar.gz && mv libfprint-v${LIBFPRINT_VERSION} libfprint-src && cd libfprint-src && \
    sed -i 's/^    foreach driver_test: drivers_tests$/    foreach driver_test, _args: drivers_tests/' tests/meson.build && \
    meson setup buildDir ${MESON_PRIVATE} -Ddrivers=default -Dintrospection=false -Ddoc=false \
      -Dgtk-examples=false -Dinstalled-tests=false -Dudev_rules=enabled -Dudev_rules_dir=/usr/lib/udev/rules.d && \
    ninja -C buildDir install && DESTDIR=/output ninja -C buildDir install

# fprintd: the daemon into the private directory (D-Bus and the unit name
# it there), the tools into /usr/bin, pam_fprintd.so into
# /usr/lib/security, the D-Bus files and the polkit policy into /usr/share.
# The extension's version is fprintd's.
# renovate: datasource=gitlab-tags depName=libfprint/fprintd registryUrl=https://gitlab.freedesktop.org extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG FINGERPRINT_VERSION=1.94.5
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://gitlab.freedesktop.org/libfprint/fprintd/-/archive/v${FINGERPRINT_VERSION}/fprintd-v${FINGERPRINT_VERSION}.tar.gz -o fd.tar.gz && \
    tar -xzf fd.tar.gz && rm fd.tar.gz && mv fprintd-v${FINGERPRINT_VERSION} fprintd-src && cd fprintd-src && \
    meson setup buildDir ${MESON_PRIVATE} --sysconfdir=/etc --localstatedir=/var \
      -Dpam=true -Dman=false -Dgtk_doc=false -Dsystemd=true \
      -Dsystemd_system_unit_dir=/usr/lib/systemd/system \
      -Ddbus_service_dir=/usr/share/dbus-1/system-services \
      -Dpam_modules_dir=/usr/lib/security && \
    DESTDIR=/output ninja -C buildDir install

# The PAM alias for the lock screens that read the password first (see the
# top of this file): a relative link to the image's own pam_unix.so, so the
# password check is the image's, at the image's version.
RUN ln -s pam_unix.so /output/usr/lib/security/pam_isotope_unix.so

# Licences, in a directory of the extension's own (the image has some of
# these libraries with their own licence directories), then everything a
# system extension must not carry or does not need: /etc (not merged;
# fprintd.conf's only setting is the default), headers, pkg-config files,
# the translations, man pages, D-Bus interface XML and AppStream data.
RUN set -eux; \
    for p in libusb-src:libusb json-glib-src:json-glib libgusb-src:libgusb pixman-src:pixman libgudev-src:libgudev \
             polkit-src:polkit libfprint-src:libfprint fprintd-src:fprintd; do \
      src=${p%%:*}; name=${p#*:}; mkdir -p /output/usr/share/licenses/fingerprint-extension/$name; \
      lic=$(ls -d $src/COPYING* $src/LICENSE $src/LICENSE.* $src/LICENSES/* 2>/dev/null || true); \
      [ -n "$lic" ] || { echo "no licence in $src" >&2; exit 1; }; \
      cp -r $lic /output/usr/share/licenses/fingerprint-extension/$name/; \
    done; \
    rm -rf /output/etc /output/usr/include /output/usr/share/man /output/usr/share/doc \
      /output/usr/share/gtk-doc /output/usr/share/locale /output/usr/share/metainfo \
      /output/usr/share/dbus-1/interfaces /output/usr/share/installed-tests \
      /output/usr/lib/isotope-ext/fingerprint/pkgconfig /output/usr/lib/isotope-ext/fingerprint/installed-tests \
      /output/usr/bin/json-glib-* /output/usr/bin/gusbcmd /output/usr/libexec; \
    find /output -name '*.la' -delete -o -name '*.a' -delete; \
    find /output/usr/lib/isotope-ext/fingerprint -maxdepth 1 -name '*.so' -type l -delete; \
    rmdir /output/usr/share/dbus-1 2>/dev/null || true
# Every ELF file needs only the C library, GLib, libsystemd, libudev,
# libpam, libcrypto and libraries of the extension itself, and the programs
# carry the RUNPATH that finds them.
RUN set -eu; \
    allowed=" libc.so libglib-2.0.so.0 libgobject-2.0.so.0 libgio-2.0.so.0 libgmodule-2.0.so.0 libsystemd.so.0 libudev.so.1 libpam.so.0 libcrypto.so.3 "; \
    bad=; n=0; \
    for f in $(find /output -type f); do \
      [ "$(head -c 4 "$f" | od -An -c | tr -d ' ')" = '177ELF' ] || continue; n=$((n+1)); \
      for lib in $(readelf -d "$f" | sed -n 's/.*(NEEDED).*\[\(.*\)\]/\1/p'); do \
        case "$allowed" in *" $lib "*) continue ;; esac; \
        [ -e "/output/usr/lib/isotope-ext/fingerprint/$lib" ] && continue; \
        bad="$bad $f:$lib"; \
      done; \
    done; \
    echo "$n ELF files"; [ "$n" -gt 8 ]; \
    if [ -n "$bad" ]; then echo "libraries the image does not have:$bad" >&2; exit 1; fi; \
    for f in /output/usr/lib/isotope-ext/fingerprint/fprintd /output/usr/bin/fprintd-enroll /output/usr/lib/isotope-ext/fingerprint/libfprint-2.so.2; do \
      readelf -d "$f" | grep -E 'R(UN)?PATH' | grep -q /usr/lib/isotope-ext/fingerprint || { echo "$f has no RUNPATH" >&2; exit 1; }; \
    done; \
    test -f /output/usr/lib/security/pam_fprintd.so; \
    test "$(readlink /output/usr/lib/security/pam_isotope_unix.so)" = pam_unix.so; \
    ! readelf -d /output/usr/lib/security/pam_fprintd.so | grep -q 'libglib'

# What the extension contains: everything under /usr of this stage.
FROM scratch AS default
COPY --from=build /output/usr /usr

# Checks the workflow runs before it publishes anything. Kept offline.
FROM ghcr.io/kairos-io/hadron:${HADRON_VERSION} AS test
COPY --from=default / /extension/
# A system extension is merged over /usr. It must not carry a file the base
# already has, or it would replace the base's copy.
RUN set -eu; \
    clash=$(cd /extension && find . ! -type d | while read -r f; do [ ! -e "/${f#./}" ] && [ ! -L "/${f#./}" ] || echo "${f#./}"; done); \
    if [ -n "$clash" ]; then echo "the extension overlays base files: $clash" >&2; exit 1; fi
# Kairos merges only these hierarchies (SYSTEMD_SYSEXT_HIERARCHIES in
# kairos-init's 99_sysext.yaml); a file anywhere else is never seen.
RUN set -eu; \
    outside=$(cd /extension && find . ! -type d | sed 's#^\./##' | grep -v -E '^usr/(bin|sbin|lib|share|include|src)/' || true); \
    if [ -n "$outside" ]; then echo "files outside the merged hierarchies: $outside" >&2; exit 1; fi
RUN cd /extension && find . -type f | while read -r f; do \
      [ "$(head -c 4 "$f" | od -An -c | tr -d ' ')" = '177ELF' ] && echo "/${f#./}"; done >/tmp/elf; \
    test -s /tmp/elf
# The image's GLib, which every Isotope desktop has, without replacing a
# file.
COPY --from=build /glib-runtime/ /glib-runtime/
RUN cp -an /glib-runtime/. / && rm -rf /glib-runtime
RUN cp -a /extension/. / && rm -rf /extension
# Every library each ELF file needs is found, and the PAM alias resolves to
# the base's pam_unix.so.
RUN set -eu; while read -r f; do \
      ldd "$f" >/tmp/ldd 2>&1 || { echo "ldd $f failed" >&2; cat /tmp/ldd >&2; exit 1; }; \
      if grep -qE 'not found|Error loading|Error relocating' /tmp/ldd; then echo "$f:" >&2; cat /tmp/ldd >&2; exit 1; fi; \
    done </tmp/elf; \
    test -f /usr/lib/security/pam_isotope_unix.so; \
    [ "$(readlink -f /usr/lib/security/pam_isotope_unix.so)" = "$(readlink -f /usr/lib/security/pam_unix.so)" ]
# The tools start (enroll's and verify's --help need no daemon; list and
# delete reach the bus first, and list runs below), the unit and the D-Bus files
# name the daemon where it is, and the polkit policy has fprintd's three
# actions.
RUN set -eux; \
    for t in enroll verify; do fprintd-$t --help >/dev/null 2>&1 || fprintd-$t --help; done; \
    test -x /usr/bin/fprintd-list; test -x /usr/bin/fprintd-delete; \
    d=/usr/lib/isotope-ext/fingerprint/fprintd; test -x $d; \
    grep -qx "ExecStart=$d" /usr/lib/systemd/system/fprintd.service; \
    grep -qx "Exec=$d" /usr/share/dbus-1/system-services/net.reactivated.Fprint.service; \
    grep -qx 'SystemdService=fprintd.service' /usr/share/dbus-1/system-services/net.reactivated.Fprint.service; \
    grep -q 'net.reactivated.Fprint' /usr/share/dbus-1/system.d/net.reactivated.Fprint.conf; \
    for a in verify enroll setusername; do grep -q "net.reactivated.fprint.device.$a" /usr/share/polkit-1/actions/net.reactivated.fprint.device.policy; done; \
    ls /usr/lib/isotope-ext/fingerprint/libfprint-2.so.2 >/dev/null; \
    ls /usr/lib/udev/rules.d/70-libfprint-2.rules >/dev/null
# fprintd on a system bus in this container: it owns its name, says there
# is no reader (no USB device here), and fprintd-list answers for root.
RUN set -u; \
    fail() { echo "FAIL: $*" >&2; echo "--- fprintd's log:" >&2; cat /tmp/fprintd.log >&2 2>/dev/null || true; exit 1; }; \
    owned() { busctl --system status net.reactivated.Fprint >/dev/null 2>&1; }; \
    mkdir -p /run/dbus /var/lib/fprint; dbus-daemon --system --fork || fail "dbus-daemon did not start"; \
    G_MESSAGES_DEBUG=all /usr/lib/isotope-ext/fingerprint/fprintd --no-timeout >/tmp/fprintd.log 2>&1 & pid=$!; \
    i=0; until owned; do \
      kill -0 "$pid" 2>/dev/null || fail "fprintd exited"; \
      i=$((i+1)); [ "$i" -le 120 ] || fail "fprintd did not own its name in 60 s"; sleep 0.5; \
    done; \
    busctl --system call net.reactivated.Fprint /net/reactivated/Fprint/Manager net.reactivated.Fprint.Manager GetDevices >/tmp/devices 2>&1 || fail "GetDevices: $(cat /tmp/devices)"; \
    cat /tmp/devices; grep -qx 'ao 0' /tmp/devices || fail "a reader in a container?"; \
    fprintd-list root >/tmp/list 2>&1; cat /tmp/list; \
    grep -qi 'no devices available' /tmp/list || fail "fprintd-list root"; \
    kill "$pid"; wait "$pid" 2>/dev/null || true; \
    echo "fprintd answers on the system bus"

# ModemManager, the mobile broadband (cellular modem) service, as an Isotope
# system extension. See extensions/README.md for how an extension is built and
# published, and docs/superpowers/specs/2026-11-24-modemmanager-design.md.
#
# The image already has NetworkManager's WWAN plugin and libmm-glib (the
# Dockerfile's networkmanager and mm-glib stages); this extension adds the
# daemon that plugin talks to: ModemManager and its plugins, mmcli, libqmi
# (qmicli, qmi-proxy), libmbim (mbimcli, mbim-proxy), libqrtr-glib, the D-Bus
# policy and activation file, ModemManager.service, the udev rules, and the
# mobile-broadband-provider-info database NetworkManager reads the APN from.
#
# Its own libraries (libmm-glib, libqmi-glib, libmbim-glib, libqrtr-glib,
# libgudev, ModemManager's plugins) and the two proxies live in
# /usr/lib/isotope-ext/modemmanager, found through a RUNPATH: the image has
# its own libmm-glib.so.0 in /usr/lib and the desktops libgudev-1.0.so.0, and
# a second copy there is what `isotope ext` refuses. /usr/libexec is not used:
# Kairos merges only /usr/bin, /usr/sbin, /usr/lib, /usr/share, /usr/include
# and /usr/src (SYSTEMD_SYSEXT_HIERARCHIES). GLib, libsystemd and libudev come
# from the image; the build stage fails if an ELF file needs anything else.

# Set by .github/workflows/extensions.yml from scripts/bump-hadron.sh, so the
# extension builds on the same Hadron release as the image.
ARG HADRON_VERSION

FROM ghcr.io/kairos-io/hadron-toolchain:${HADRON_VERSION} AS build
ENV PKG_CONFIG_PATH=/usr/lib/isotope-ext/modemmanager/pkgconfig
RUN pip3 install meson ninja packaging
WORKDIR /build

# Build-only dependencies, installed into this stage's root and never into
# /output: libucontext (the toolchain's libsystemd and libudev link it; the
# Hadron base ships it), PCRE2 and GLib at the versions the image builds
# (the image's own copy is what runs). Same versions and Renovate rules as
# the Dockerfile at the root, so one Renovate PR moves both.
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
# For the test stage only, never the extension: a stand-in for the image's
# GLib, gdbus (to watch the bus as another user), and a stub that owns
# ModemManager's name and sends a signal (test/mm-signal-stub.c).
COPY test/ /build/test/
RUN mkdir -p /glib-runtime/usr/lib /glib-runtime/usr/bin && \
    cp -a /usr/lib/libglib-2.0.so.0* /usr/lib/libgobject-2.0.so.0* /usr/lib/libgio-2.0.so.0* \
      /usr/lib/libgmodule-2.0.so.0* /usr/lib/libpcre2-8.so.0* /usr/lib/libffi.so.* /glib-runtime/usr/lib/ && \
    cp -a /usr/bin/gdbus /glib-runtime/usr/bin/ && \
    cc -O2 -Wall -o /mm-signal-stub /build/test/mm-signal-stub.c $(pkg-config --cflags --libs gio-2.0)

# The extension's libraries: built shared into the private directory, with
# its RUNPATH, and installed twice: into / for the next builds and into
# /output for the extension. MESON_PRIVATE is the options they share.
ENV LDFLAGS="-Wl,-rpath,/usr/lib/isotope-ext/modemmanager"
ENV MESON_PRIVATE="--prefix=/usr --libdir=lib/isotope-ext/modemmanager --libexecdir=lib/isotope-ext/modemmanager --bindir=bin --sbindir=bin --buildtype=minsize -Dstrip=true"

# renovate: datasource=gitlab-tags depName=GNOME/libgudev registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG LIBGUDEV_VERSION=238
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.gnome.org/sources/libgudev/${LIBGUDEV_VERSION}/libgudev-${LIBGUDEV_VERSION}.tar.xz -o g.tar.xz && \
    tar -xf g.tar.xz && rm g.tar.xz && cd libgudev-* && \
    meson setup buildDir ${MESON_PRIVATE} -Dtests=disabled -Dintrospection=disabled -Dvapi=disabled -Dgtk_doc=false && \
    ninja -C buildDir install && DESTDIR=/output ninja -C buildDir install

# renovate: datasource=gitlab-tags depName=mobile-broadband/libqrtr-glib registryUrl=https://gitlab.freedesktop.org extractVersion=^(?<version>\d+\.\d+\.\d+)$
ARG LIBQRTR_GLIB_VERSION=1.2.2
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://gitlab.freedesktop.org/mobile-broadband/libqrtr-glib/-/archive/${LIBQRTR_GLIB_VERSION}/libqrtr-glib-${LIBQRTR_GLIB_VERSION}.tar.gz -o q.tar.gz && \
    tar -xzf q.tar.gz && rm q.tar.gz && cd libqrtr-glib-* && \
    meson setup buildDir ${MESON_PRIVATE} -Dintrospection=false -Dgtk_doc=false && \
    ninja -C buildDir install && DESTDIR=/output ninja -C buildDir install

# renovate: datasource=gitlab-tags depName=mobile-broadband/libmbim registryUrl=https://gitlab.freedesktop.org extractVersion=^(?<version>\d+\.\d+\.\d+)$
ARG LIBMBIM_VERSION=1.32.0
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://gitlab.freedesktop.org/mobile-broadband/libmbim/-/archive/${LIBMBIM_VERSION}/libmbim-${LIBMBIM_VERSION}.tar.gz -o m.tar.gz && \
    tar -xzf m.tar.gz && rm m.tar.gz && cd libmbim-* && \
    meson setup buildDir ${MESON_PRIVATE} -Dintrospection=false -Dman=false -Dbash_completion=false -Dgtk_doc=false && \
    ninja -C buildDir install && DESTDIR=/output ninja -C buildDir install

# No qmi-firmware-update (and so no udev support in libqmi): flashing a modem
# is not this extension's job.
# renovate: datasource=gitlab-tags depName=mobile-broadband/libqmi registryUrl=https://gitlab.freedesktop.org extractVersion=^(?<version>\d+\.\d+\.\d+)$
ARG LIBQMI_VERSION=1.36.0
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://gitlab.freedesktop.org/mobile-broadband/libqmi/-/archive/${LIBQMI_VERSION}/libqmi-${LIBQMI_VERSION}.tar.gz -o q.tar.gz && \
    tar -xzf q.tar.gz && rm q.tar.gz && cd libqmi-* && \
    meson setup buildDir ${MESON_PRIVATE} -Dfirmware_update=false -Dudev=false \
      -Dintrospection=false -Dman=false -Dbash_completion=false -Dgtk_doc=false && \
    ninja -C buildDir install && DESTDIR=/output ninja -C buildDir install

# ModemManager without polkit: its D-Bus policy (files/) lets only root
# (NetworkManager) and the admin group call it. No man pages, bash
# completion, introspection, tests or examples.
# renovate: datasource=gitlab-tags depName=mobile-broadband/ModemManager registryUrl=https://gitlab.freedesktop.org extractVersion=^(?<version>\d+\.\d+\.\d+)$
ARG MODEMMANAGER_VERSION=1.24.2
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://gitlab.freedesktop.org/mobile-broadband/ModemManager/-/archive/${MODEMMANAGER_VERSION}/ModemManager-${MODEMMANAGER_VERSION}.tar.gz -o mm.tar.gz && \
    tar -xzf mm.tar.gz && rm mm.tar.gz && mv ModemManager-* mm-src && cd mm-src && \
    meson setup buildDir ${MESON_PRIVATE} \
      -Dudevdir=/usr/lib/udev -Ddbus_policy_dir=/usr/share/dbus-1/system.d \
      -Dsystemdsystemunitdir=/usr/lib/systemd/system \
      -Dpolkit=no -Dqmi=true -Dmbim=true -Dqrtr=true \
      -Dsystemd_suspend_resume=true -Dsystemd_journal=true \
      -Dintrospection=false -Dvapi=false -Dman=false -Dgtk_doc=false \
      -Dbash_completion=false -Dtests=false -Dexamples=false && \
    DESTDIR=/output ninja -C buildDir install

# The provider database NetworkManager reads the APN from
# (gsm.auto-config), at the path it is built with.
# renovate: datasource=gitlab-tags depName=GNOME/mobile-broadband-provider-info registryUrl=https://gitlab.gnome.org
ARG MOBILE_BROADBAND_PROVIDER_INFO_VERSION=20261111
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.gnome.org/sources/mobile-broadband-provider-info/${MOBILE_BROADBAND_PROVIDER_INFO_VERSION}/mobile-broadband-provider-info-${MOBILE_BROADBAND_PROVIDER_INFO_VERSION}.tar.xz -o p.tar.xz && \
    tar -xf p.tar.xz && rm p.tar.xz && cd mobile-broadband-provider-info-* && \
    install -Dm644 serviceproviders.xml /output/usr/share/mobile-broadband-provider-info/serviceproviders.xml && \
    install -Dm644 serviceproviders.2.dtd /output/usr/share/mobile-broadband-provider-info/serviceproviders.2.dtd && \
    install -Dm644 COPYING /output/usr/share/licenses/modemmanager-extension/mobile-broadband-provider-info/COPYING

# Isotope's D-Bus policy and the unit's drop-in replace and extend upstream's.
COPY files/ /output/

# Licences (in a directory of the extension's own: the image has
# /usr/share/licenses/ModemManager for its libmm-glib), then the unit enabled
# from /usr (a sysext cannot write the links in /etc that `systemctl enable`
# would): the multi-user.target link, which is what starts ModemManager at
# boot (NetworkManager never activates it on a systemd system), and the
# alias the D-Bus activation file names, so mmcli and Noctalia can start it.
# Then everything a system extension must not carry.
RUN set -eux; \
    for p in mm-src:ModemManager libqmi-*:libqmi libmbim-*:libmbim libqrtr-glib-*:libqrtr-glib libgudev-*:libgudev; do \
      src=${p%%:*}; name=${p#*:}; mkdir -p /output/usr/share/licenses/modemmanager-extension/$name; \
      cp $src/COPYING* /output/usr/share/licenses/modemmanager-extension/$name/; \
    done; \
    u=/output/usr/lib/systemd/system; \
    mkdir -p $u/multi-user.target.wants; \
    ln -sf ../ModemManager.service $u/multi-user.target.wants/ModemManager.service; \
    ln -sf ModemManager.service $u/dbus-org.freedesktop.ModemManager1.service; \
    rm -rf /output/etc /output/usr/include /output/usr/share/man /output/usr/share/doc \
      /output/usr/share/gtk-doc /output/usr/share/locale /output/usr/share/bash-completion \
      /output/usr/share/dbus-1/interfaces /output/usr/lib/isotope-ext/modemmanager/pkgconfig; \
    find /output -name '*.la' -delete -o -name '*.a' -delete
# Every ELF file needs only the C library, GLib, libsystemd, libudev and
# libraries of the extension itself, and carries the RUNPATH that finds them.
RUN set -eu; \
    allowed=" libc.so libglib-2.0.so.0 libgobject-2.0.so.0 libgio-2.0.so.0 libgmodule-2.0.so.0 libsystemd.so.0 libudev.so.1 "; \
    bad=; n=0; \
    for f in $(find /output -type f); do \
      [ "$(head -c 4 "$f" | od -An -c | tr -d ' ')" = '177ELF' ] || continue; n=$((n+1)); \
      for lib in $(readelf -d "$f" | sed -n 's/.*(NEEDED).*\[\(.*\)\]/\1/p'); do \
        case "$allowed" in *" $lib "*) continue ;; esac; \
        [ -e "/output/usr/lib/isotope-ext/modemmanager/$lib" ] && continue; \
        bad="$bad $f:$lib"; \
      done; \
    done; \
    echo "$n ELF files"; [ "$n" -gt 5 ]; \
    if [ -n "$bad" ]; then echo "libraries the image does not have:$bad" >&2; exit 1; fi; \
    readelf -d /output/usr/bin/ModemManager | grep -E 'R(UN)?PATH' | grep -q /usr/lib/isotope-ext/modemmanager; \
    readelf -d /output/usr/bin/mmcli | grep -E 'R(UN)?PATH' | grep -q /usr/lib/isotope-ext/modemmanager; \
    test -x /output/usr/lib/isotope-ext/modemmanager/qmi-proxy; \
    test -x /output/usr/lib/isotope-ext/modemmanager/mbim-proxy

# What the extension contains: everything under /usr of this stage.
FROM scratch AS default
COPY --from=build /output/usr /usr

# Checks the workflow runs before it publishes anything. Kept offline.
FROM ghcr.io/kairos-io/hadron:${HADRON_VERSION} AS test
COPY --from=default / /extension/
# A system extension is merged over /usr. It must not carry a file the base
# already has, or it would replace the base's copy.
RUN set -eu; \
    clash=$(cd /extension && find . ! -type d | while read -r f; do [ ! -e "/${f#./}" ] || echo "${f#./}"; done); \
    if [ -n "$clash" ]; then echo "the extension overlays base files: $clash" >&2; exit 1; fi
# Kairos merges only these hierarchies (SYSTEMD_SYSEXT_HIERARCHIES in
# kairos-init's 99_sysext.yaml); a file anywhere else is never seen.
RUN set -eu; \
    outside=$(cd /extension && find . ! -type d | sed 's#^\./##' | grep -v -E '^usr/(bin|sbin|lib|share|include|src)/' || true); \
    if [ -n "$outside" ]; then echo "files outside the merged hierarchies: $outside" >&2; exit 1; fi
RUN cd /extension && find . -type f | while read -r f; do \
      [ "$(head -c 4 "$f" | od -An -c | tr -d ' ')" = '177ELF' ] && echo "/${f#./}"; done >/tmp/elf; \
    test -s /tmp/elf
# The image's GLib, which every Isotope image has, without replacing a file.
COPY --from=build /glib-runtime/ /glib-runtime/
COPY --from=build /mm-signal-stub /usr/local/bin/mm-signal-stub
RUN cp -an /glib-runtime/. / && rm -rf /glib-runtime
RUN cp -a /extension/. / && rm -rf /extension
RUN set -eux; ModemManager --version; mmcli --version; qmicli --version; mbimcli --version
RUN set -eu; while read -r f; do \
      ldd "$f" >/tmp/ldd 2>&1 || { echo "ldd $f failed" >&2; cat /tmp/ldd >&2; exit 1; }; \
      if grep -q 'not found' /tmp/ldd; then echo "$f:" >&2; cat /tmp/ldd >&2; exit 1; fi; \
    done </tmp/elf
# The unit, its drop-in and the links that enable it from /usr.
RUN set -eux; \
    u=/usr/lib/systemd/system; \
    grep -qx 'ExecStart=/usr/bin/ModemManager' $u/ModemManager.service; \
    test -f $u/multi-user.target.wants/ModemManager.service; \
    [ "$(readlink $u/dbus-org.freedesktop.ModemManager1.service)" = ModemManager.service ]; \
    grep -qx 'SystemdService=dbus-org.freedesktop.ModemManager1.service' /usr/share/dbus-1/system-services/org.freedesktop.ModemManager1.service; \
    grep -q 'udevadm trigger' $u/ModemManager.service.d/10-isotope.conf; \
    ls /usr/lib/udev/rules.d/80-mm-candidate.rules /usr/lib/udev/rules.d/77-mm-*.rules >/dev/null; \
    test -s /usr/share/mobile-broadband-provider-info/serviceproviders.xml; \
    grep -q '<serviceproviders' /usr/share/mobile-broadband-provider-info/serviceproviders.xml
# On a system bus in this container: ModemManager owns its name, root and a
# member of admin may call it and nobody else may; then, with a stub that
# owns the name and sends a signal, a member of admin receives
# ModemManager's signals and another user does not (an SMS arrives in one).
# ModemManager's log is printed on every failure. gid 900 is the admin
# group's on Kairos (kairos-init); Hadron's 990 is input.
RUN set -u; \
    fail() { echo "FAIL: $*" >&2; echo "--- ModemManager's log:" >&2; cat /tmp/mm.log >&2 2>/dev/null || true; exit 1; }; \
    owned() { busctl --system status org.freedesktop.ModemManager1 >/dev/null 2>&1; }; \
    printf 'tester:x:1000:1000::/tmp:/bin/sh\n' >>/etc/passwd; \
    printf 'tester:x:1000:\nadmin:x:900:tester\n' >>/etc/group; \
    mkdir -p /run/dbus; dbus-daemon --system --fork || fail "dbus-daemon did not start"; \
    ModemManager --debug >/tmp/mm.log 2>&1 & mm=$!; \
    i=0; until owned; do \
      kill -0 "$mm" 2>/dev/null || fail "ModemManager exited"; \
      i=$((i+1)); [ "$i" -le 120 ] || fail "ModemManager did not own its name in 60 s"; sleep 0.5; \
    done; \
    mmcli -L >/tmp/root.out 2>&1; cat /tmp/root.out; \
    grep -q 'No modems were found' /tmp/root.out || fail "mmcli -L as root"; \
    su tester -s /bin/sh -c 'mmcli -L' >/tmp/tester.out 2>&1; cat /tmp/tester.out; \
    grep -q 'No modems were found' /tmp/tester.out || fail "mmcli -L as a member of admin"; \
    if su nobody -s /bin/sh -c 'mmcli -L' >/tmp/nobody.out 2>&1; then cat /tmp/nobody.out; fail "mmcli -L as nobody was allowed"; fi; \
    cat /tmp/nobody.out; grep -qiE 'denied|not allowed|rejected' /tmp/nobody.out || fail "mmcli -L as nobody did not fail on the policy"; \
    kill "$mm"; wait "$mm" 2>/dev/null || true; \
    i=0; while owned; do i=$((i+1)); [ "$i" -le 50 ] || fail "ModemManager did not stop"; sleep 0.2; done; \
    mm-signal-stub & stub=$!; \
    i=0; until owned; do i=$((i+1)); [ "$i" -le 50 ] || fail "the stub did not own the name"; sleep 0.2; done; \
    su tester -s /bin/sh -c 'timeout 3 gdbus monitor --system --dest org.freedesktop.ModemManager1' >/tmp/tester.sig 2>&1; \
    su nobody -s /bin/sh -c 'timeout 3 gdbus monitor --system --dest org.freedesktop.ModemManager1' >/tmp/nobody.sig 2>&1; \
    kill "$stub"; \
    grep -q IsotopeTest /tmp/tester.sig || { cat /tmp/tester.sig; fail "a member of admin did not receive ModemManager's signals"; }; \
    if grep -q IsotopeTest /tmp/nobody.sig; then cat /tmp/nobody.sig; fail "nobody received ModemManager's signals"; fi; \
    echo "policy: root and admin call and receive; nobody does neither"

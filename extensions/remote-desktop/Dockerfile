# Remote desktop: wayvnc, a VNC server for wlroots and other compositors
# with the screencopy and virtual input protocols (Sway and niri), as an
# Isotope system extension. See extensions/README.md for how an extension is
# built and published, and docs/superpowers/specs/2026-11-24-remote-desktop-design.md.
#
# The extension is the server only: wayvnc and wayvncctl in /usr/bin, and
# their libraries (neatvnc, aml, GnuTLS with Nettle, GMP and libtasn1 for
# VeNCrypt and RSA-AES, Jansson, libjpeg-turbo's TurboJPEG) in
# /usr/lib/isotope-ext/remote-desktop, found through a RUNPATH, so no file
# lands over one of the image's. The image runs it: `isotope remote-desktop`
# and the user unit isotope-remote-desktop.service, never enabled, started
# per user from Isotope System's Network page. The image's own libraries
# (the C library, zlib, pixman, libdrm, libxkbcommon, libwayland-client) are
# the only others it needs; the build stage fails if an ELF file needs
# anything else.

# Set by .github/workflows/extensions.yml from scripts/bump-hadron.sh, so the
# extension builds on the same Hadron release as the image.
ARG HADRON_VERSION

FROM ghcr.io/kairos-io/hadron-toolchain:${HADRON_VERSION} AS build
ENV PKG_CONFIG_PATH=/usr/lib/isotope-ext/remote-desktop/pkgconfig
RUN pip3 install meson ninja packaging
WORKDIR /build

# Build-only: the image's own Wayland, pixman, libdrm and libxkbcommon, at
# the versions the Dockerfile at the root builds (the same Renovate rules,
# so one Renovate run moves both). Installed into this stage's root and never
# into /output: the image's copies are what run.
# renovate: datasource=gitlab-tags depName=wayland/wayland registryUrl=https://gitlab.freedesktop.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG WAYLAND_VERSION=1.25.0
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://gitlab.freedesktop.org/wayland/wayland/-/archive/${WAYLAND_VERSION}/wayland-${WAYLAND_VERSION}.tar -o w.tar && \
    tar -xf w.tar && rm w.tar && cd wayland-* && \
    meson setup buildDir ${COMMON_MESON_FLAGS} -Dtests=false -Ddocumentation=false -Dscanner=true -Dlibraries=true && \
    ninja -C buildDir install
# renovate: datasource=gitlab-tags depName=pixman/pixman registryUrl=https://gitlab.freedesktop.org extractVersion=^pixman-(?<version>\d+(\.\d+)*)$
ARG PIXMAN_VERSION=0.46.0
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://www.cairographics.org/releases/pixman-${PIXMAN_VERSION}.tar.gz -o p.tar.gz && \
    tar -xzf p.tar.gz && rm p.tar.gz && cd pixman-* && \
    meson setup buildDir ${COMMON_MESON_FLAGS} -Dtests=disabled -Ddemos=disabled && ninja -C buildDir install
# renovate: datasource=gitlab-tags depName=mesa/drm registryUrl=https://gitlab.freedesktop.org extractVersion=^libdrm-(?<version>\d+(\.\d+)*)$
ARG LIBDRM_VERSION=2.4.133
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://dri.freedesktop.org/libdrm/libdrm-${LIBDRM_VERSION}.tar.xz -o d.tar.xz && \
    tar -xf d.tar.xz && rm d.tar.xz && cd libdrm-* && \
    meson setup buildDir ${COMMON_MESON_FLAGS} -Dtests=false && ninja -C buildDir install
# renovate: datasource=github-tags depName=xkbcommon/libxkbcommon extractVersion=^xkbcommon-(?<version>\d+(\.\d+)*)$
ARG LIBXKBCOMMON_VERSION=1.13.0
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/xkbcommon/libxkbcommon/archive/refs/tags/xkbcommon-${LIBXKBCOMMON_VERSION}.tar.gz -o x.tar.gz && \
    tar -xzf x.tar.gz && rm x.tar.gz && cd libxkbcommon-* && \
    meson setup buildDir ${COMMON_MESON_FLAGS} -Denable-x11=false -Denable-xkbregistry=false \
      -Denable-tools=false -Denable-docs=false -Denable-bash-completion=false && \
    ninja -C buildDir install
# What the test stage puts in for the image's libraries, without replacing
# a file the Hadron base has (libffi: wayland-client needs it).
RUN mkdir -p /image-runtime/usr/lib && \
    cp -a /usr/lib/libpixman-1.so.0* /usr/lib/libdrm.so.2* /usr/lib/libwayland-client.so.0* \
      /usr/lib/libxkbcommon.so.0* /usr/lib/libffi.so.* /image-runtime/usr/lib/

# The extension's libraries: shared, in the private directory, with its
# RUNPATH; installed twice, into / for the next builds and into /output for
# the extension. Autotools take CONF_PRIVATE, meson MESON_PRIVATE.
ENV LDFLAGS="-L/usr/lib/isotope-ext/remote-desktop -Wl,-rpath,/usr/lib/isotope-ext/remote-desktop"
ENV CONF_PRIVATE="--prefix=/usr --libdir=/usr/lib/isotope-ext/remote-desktop --bindir=/usr/bin --disable-static --enable-shared"
ENV MESON_PRIVATE="--prefix=/usr --libdir=lib/isotope-ext/remote-desktop --bindir=bin --buildtype=minsize -Dstrip=true -Ddefault_library=shared"

# GMP, for Nettle's public-key half (RSA-AES, and TLS). Tarball-only
# releases (scripts/check-renovate-pins.sh lists GMP as updated by hand).
ARG GMP_VERSION=6.3.0
RUN for m in https://mirrors.kernel.org/gnu https://ftpmirror.gnu.org https://ftp.gnu.org/gnu; do curl -fL --connect-timeout 30 --retry 3 --retry-delay 3 --retry-all-errors "$m/gmp/gmp-${GMP_VERSION}.tar.xz" -o g.tar.xz && break; rm -f g.tar.xz; done && \
    test -s g.tar.xz && tar -xf g.tar.xz && rm g.tar.xz && mv gmp-* src-gmp && cd src-gmp && \
    CFLAGS="-O2 -std=gnu17" ./configure ${CONF_PRIVATE} && \
    make -j"$(nproc)" && make install && make DESTDIR=/output install

# Nettle 3.10 (GnuTLS 3.8 also takes 4.0; neatvnc's Nettle code is written
# for the 3.x API).
# renovate: datasource=github-tags depName=gnutls/nettle extractVersion=^nettle_(?<version>\d+(\.\d+)*)_release_\d+$
ARG NETTLE_VERSION=3.10.2
RUN for m in https://mirrors.kernel.org/gnu https://ftpmirror.gnu.org https://ftp.gnu.org/gnu; do curl -fL --connect-timeout 30 --retry 3 --retry-delay 3 --retry-all-errors "$m/nettle/nettle-${NETTLE_VERSION}.tar.gz" -o n.tar.gz && break; rm -f n.tar.gz; done && \
    test -s n.tar.gz && tar -xzf n.tar.gz && rm n.tar.gz && mv nettle-* src-nettle && cd src-nettle && \
    ./configure ${CONF_PRIVATE} --disable-documentation --disable-openssl && \
    make -j"$(nproc)" && make install && make DESTDIR=/output install && \
    test -e /usr/lib/isotope-ext/remote-desktop/libhogweed.so

# renovate: datasource=gitlab-tags depName=gnutls/libtasn1 extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG LIBTASN1_VERSION=4.20.0
RUN for m in https://mirrors.kernel.org/gnu https://ftpmirror.gnu.org https://ftp.gnu.org/gnu; do curl -fL --connect-timeout 30 --retry 3 --retry-delay 3 --retry-all-errors "$m/libtasn1/libtasn1-${LIBTASN1_VERSION}.tar.gz" -o t.tar.gz && break; rm -f t.tar.gz; done && \
    test -s t.tar.gz && tar -xzf t.tar.gz && rm t.tar.gz && mv libtasn1-* src-libtasn1 && cd src-libtasn1 && \
    ./configure ${CONF_PRIVATE} --disable-doc --disable-valgrind-tests && \
    make -j"$(nproc)" && make install && make DESTDIR=/output install

# GnuTLS for VeNCrypt (X509Plain: TLS, then the user name and password).
# No tools, p11-kit, TPM, IDN or certificate compression; libunistring is
# the bundled copy.
# renovate: datasource=gitlab-tags depName=gnutls/gnutls extractVersion=^(?<version>\d+\.\d+\.\d+)$
ARG GNUTLS_VERSION=3.8.14
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://www.gnupg.org/ftp/gcrypt/gnutls/v${GNUTLS_VERSION%.*}/gnutls-${GNUTLS_VERSION}.tar.xz -o gt.tar.xz && \
    tar -xf gt.tar.xz && rm gt.tar.xz && mv gnutls-* src-gnutls && cd src-gnutls && \
    ./configure ${CONF_PRIVATE} --disable-doc --disable-manpages --disable-tools --disable-tests \
      --disable-full-test-suite --disable-cxx --disable-nls --disable-libdane --disable-guile \
      --without-p11-kit --without-tpm --without-tpm2 --without-idn --without-zlib --without-brotli \
      --without-zstd --without-leancrypto --with-included-unistring && \
    make -j"$(nproc)" && make install && make DESTDIR=/output install && \
    test -e /usr/lib/isotope-ext/remote-desktop/libgnutls.so.30

# renovate: datasource=github-releases depName=akheron/jansson extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG JANSSON_VERSION=2.15.1
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/akheron/jansson/releases/download/v${JANSSON_VERSION}/jansson-${JANSSON_VERSION}.tar.gz -o j.tar.gz && \
    tar -xzf j.tar.gz && rm j.tar.gz && mv jansson-* src-jansson && cd src-jansson && \
    ./configure ${CONF_PRIVATE} && make -j"$(nproc)" && make install && make DESTDIR=/output install

# TurboJPEG, for the Tight encoding's JPEG (much less bandwidth for photos
# and video). No SIMD: the toolchain has no NASM.
# renovate: datasource=github-releases depName=libjpeg-turbo/libjpeg-turbo extractVersion=^(?<version>\d+\.\d+\.\d+)$
ARG LIBJPEG_TURBO_VERSION=3.2.1
RUN curl --retry-all-errors -fL --retry 5 https://github.com/libjpeg-turbo/libjpeg-turbo/releases/download/${LIBJPEG_TURBO_VERSION}/libjpeg-turbo-${LIBJPEG_TURBO_VERSION}.tar.gz -o jt.tar.gz && \
    tar -xzf jt.tar.gz && rm jt.tar.gz && mv libjpeg-turbo-${LIBJPEG_TURBO_VERSION} src-libjpeg-turbo && cd src-libjpeg-turbo && \
    cmake -B build -DCMAKE_INSTALL_PREFIX=/usr -DCMAKE_INSTALL_LIBDIR=lib/isotope-ext/remote-desktop \
      -DCMAKE_INSTALL_RPATH=/usr/lib/isotope-ext/remote-desktop -DCMAKE_BUILD_TYPE=Release \
      -DENABLE_STATIC=OFF -DWITH_SIMD=OFF -DWITH_TURBOJPEG=ON && \
    cmake --build build -j"$(nproc)" && cmake --install build && DESTDIR=/output cmake --install build && \
    test -e /usr/lib/isotope-ext/remote-desktop/libturbojpeg.so.0

# renovate: datasource=github-tags depName=any1/aml extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG AML_VERSION=1.0.0
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/any1/aml/archive/refs/tags/v${AML_VERSION}.tar.gz -o a.tar.gz && \
    tar -xzf a.tar.gz && rm a.tar.gz && mv aml-* src-aml && cd src-aml && \
    meson setup buildDir ${MESON_PRIVATE} -Dexamples=false && \
    ninja -C buildDir install && DESTDIR=/output ninja -C buildDir install

# neatvnc with every security type it has (VeNCrypt with GnuTLS; RSA-AES
# with Nettle) and JPEG; no GBM or H.264 (they need Mesa and FFmpeg here).
# renovate: datasource=github-tags depName=any1/neatvnc extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG NEATVNC_VERSION=1.0.4
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/any1/neatvnc/archive/refs/tags/v${NEATVNC_VERSION}.tar.gz -o nv.tar.gz && \
    tar -xzf nv.tar.gz && rm nv.tar.gz && mv neatvnc-* src-neatvnc && cd src-neatvnc && \
    meson setup buildDir ${MESON_PRIVATE} -Dtls=enabled -Dnettle=enabled -Djpeg=enabled \
      -Dgbm=disabled -Dh264=disabled -Dtests=false -Dexamples=false -Dbenchmarks=false && \
    ninja -C buildDir install && DESTDIR=/output ninja -C buildDir install

# wayvnc itself: the extension's version. No PAM (the password is its own,
# never the login password), no GPU capture (GBM), no man pages.
# renovate: datasource=github-tags depName=any1/wayvnc extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG REMOTE_DESKTOP_VERSION=0.10.2
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/any1/wayvnc/archive/refs/tags/v${REMOTE_DESKTOP_VERSION}.tar.gz -o wv.tar.gz && \
    tar -xzf wv.tar.gz && rm wv.tar.gz && mv wayvnc-* src-wayvnc && cd src-wayvnc && \
    meson setup buildDir ${MESON_PRIVATE} -Dpam=disabled -Dscreencopy-dmabuf=disabled -Dman-pages=disabled -Dtests=false && \
    DESTDIR=/output ninja -C buildDir install

# For the test stage only, never the extension: a VNC client that checks the
# security types and logs in through VeNCrypt (test/rd-probe.c).
COPY test/ /build/test/
RUN cc -O2 -Wall -Wextra -Werror ${LDFLAGS} -o /rd-probe /build/test/rd-probe.c $(pkg-config --cflags --libs gnutls)

# Licences, in a directory of the extension's own (the image has some of
# these libraries' licences under their own names). Then everything a system
# extension must not carry, and every program but wayvnc and wayvncctl
# (libtasn1's and libjpeg-turbo's tools, Nettle's), and libjpeg, which
# TurboJPEG does not need.
RUN set -eux; \
    for src in src-*; do \
      name=${src#src-}; d=/output/usr/share/licenses/remote-desktop-extension/$name; mkdir -p "$d"; \
      lic=$(ls -d $src/COPYING* $src/LICENSE* $src/LICENSES $src/README.ijg 2>/dev/null || true); \
      [ -n "$lic" ] || { echo "no licence in $src" >&2; exit 1; }; \
      cp -r $lic "$d"/; \
    done; \
    find /output/usr/bin -mindepth 1 ! -name wayvnc ! -name wayvncctl -exec rm -rf {} +; \
    rm -rf /output/etc /output/usr/include /output/usr/share/man /output/usr/share/doc /output/usr/share/info \
      /output/usr/share/locale /output/usr/share/aclocal /output/usr/lib/isotope-ext/remote-desktop/pkgconfig \
      /output/usr/lib/isotope-ext/remote-desktop/cmake /output/usr/lib/isotope-ext/remote-desktop/libjpeg.so*; \
    find /output -name '*.la' -delete -o -name '*.a' -delete; \
    ls /output/usr/bin; [ "$(ls /output/usr/bin | tr '\n' ' ')" = "wayvnc wayvncctl " ]
# Every ELF file needs only the image's libraries and the extension's own,
# carries the RUNPATH that finds them, and nothing is left unstripped.
RUN set -eu; \
    allowed=" libc.so libz.so.1 libpixman-1.so.0 libdrm.so.2 libxkbcommon.so.0 libwayland-client.so.0 "; \
    bad=; n=0; \
    for f in $(find /output -type f); do \
      [ "$(head -c 4 "$f" | od -An -c | tr -d ' ')" = '177ELF' ] || continue; n=$((n+1)); \
      for lib in $(readelf -d "$f" | sed -n 's/.*(NEEDED).*\[\(.*\)\]/\1/p'); do \
        case "$allowed" in *" $lib "*) continue ;; esac; \
        [ -e "/output/usr/lib/isotope-ext/remote-desktop/$lib" ] && continue; \
        bad="$bad $f:$lib"; \
      done; \
    done; \
    echo "$n ELF files"; [ "$n" -ge 10 ]; \
    if [ -n "$bad" ]; then echo "libraries the image does not have:$bad" >&2; exit 1; fi; \
    for b in wayvnc wayvncctl; do \
      readelf -d /output/usr/bin/$b | grep -E 'R(UN)?PATH' | grep -q /usr/lib/isotope-ext/remote-desktop; \
    done; \
    readelf -d /output/usr/lib/isotope-ext/remote-desktop/libneatvnc.so.* | grep -E 'R(UN)?PATH' | grep -q /usr/lib/isotope-ext/remote-desktop

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
# No unit, D-Bus file or udev rule: the image has the user unit, and the
# server runs only when a user turns it on.
RUN set -eu; \
    extra=$(cd /extension && find usr/lib/systemd usr/share/dbus-1 usr/lib/udev usr/share/polkit-1 -type f 2>/dev/null || true); \
    if [ -n "$extra" ]; then echo "the extension starts nothing by itself, yet carries: $extra" >&2; exit 1; fi
RUN cd /extension && find . -type f | while read -r f; do \
      [ "$(head -c 4 "$f" | od -An -c | tr -d ' ')" = '177ELF' ] && echo "/${f#./}"; done >/tmp/elf; \
    test -s /tmp/elf
# The image's libraries, which every Sway and niri image has, without
# replacing a file of the base.
COPY --from=build /image-runtime/ /image-runtime/
COPY --from=build /rd-probe /usr/local/bin/rd-probe
RUN cp -an /image-runtime/. / && rm -rf /image-runtime
RUN cp -a /extension/. / && rm -rf /extension
# Every library each ELF file needs is found.
RUN set -eu; while read -r f; do \
      ldd "$f" >/tmp/ldd 2>&1 || { echo "ldd $f failed" >&2; cat /tmp/ldd >&2; exit 1; }; \
      if grep -qE 'not found|Error' /tmp/ldd; then echo "$f:" >&2; cat /tmp/ldd >&2; exit 1; fi; \
    done </tmp/elf
RUN set -eux; \
    wayvnc --help >/tmp/help 2>&1 || true; grep -q -- '--detached' /tmp/help; \
    wayvncctl --help >/tmp/help 2>&1 || true; grep -qi 'usage' /tmp/help
# The server a viewer meets, with no compositor (--detached): the
# configuration `isotope remote-desktop` writes, on keys in the formats it
# writes. It offers only VeNCrypt and RSA-AES (never None, the DES "VNC
# password" or Apple DH), VeNCrypt only as X509Plain, and lets in the user
# name and password it was given and nobody else. A configuration without a
# password does not start.
RUN set -u; \
    fail() { echo "FAIL: $*" >&2; echo "--- wayvnc's log:" >&2; cat /tmp/wayvnc.log >&2 2>/dev/null || true; exit 1; }; \
    probe=/usr/local/bin/rd-probe; \
    d=/tmp/rd; mkdir -p -m 700 $d /tmp/xdg; export XDG_RUNTIME_DIR=/tmp/xdg; \
    $probe gen $d || fail "making the keys"; \
    printf '%s\n' use_relative_paths=true address=127.0.0.1 port=5900 enable_auth=true username=tester \
      'password=correct horse' rsa_private_key_file=rsa-key.pem private_key_file=tls-key.pem \
      certificate_file=tls-cert.pem >$d/wayvnc.conf; \
    grep -v '^password=' $d/wayvnc.conf >$d/nopass.conf; \
    wayvnc --detached --config=$d/nopass.conf >/tmp/nopass.log 2>&1 & np=$!; \
    i=0; while kill -0 $np 2>/dev/null; do \
      i=$((i+1)); if [ $i -gt 50 ]; then kill $np; cat /tmp/nopass.log; fail "wayvnc started without a password"; fi; sleep 0.2; \
    done; \
    if wait $np; then cat /tmp/nopass.log; fail "wayvnc exited 0 without a password"; fi; \
    grep -qi 'password' /tmp/nopass.log || { cat /tmp/nopass.log; fail "no word about the missing password"; }; \
    wayvnc --detached --config=$d/wayvnc.conf --log-level=info >/tmp/wayvnc.log 2>&1 & wv=$!; \
    i=0; until $probe types 127.0.0.1 5900 >/tmp/types 2>/dev/null; do \
      kill -0 $wv 2>/dev/null || fail "wayvnc exited"; \
      i=$((i+1)); [ $i -le 50 ] || fail "wayvnc did not listen in 10 s"; sleep 0.2; \
    done; \
    echo "security types: $(tr '\n' ' ' </tmp/types)"; \
    grep -qx 19 /tmp/types || fail "no VeNCrypt (19)"; \
    grep -qxE '5|129' /tmp/types || fail "no RSA-AES (5 or 129)"; \
    if grep -qxE '1|2|30' /tmp/types; then fail "an unencrypted or broken type is offered"; fi; \
    $probe vencrypt 127.0.0.1 5900 tester 'wrong horse' >/tmp/bad; rc=$?; cat /tmp/bad; \
    [ $rc -eq 1 ] || fail "a wrong password was not refused ($rc)"; \
    grep -qx 'subtype 262' /tmp/bad || fail "no X509Plain"; \
    if grep -E '^subtype ' /tmp/bad | grep -qvx 'subtype 262'; then fail "a VeNCrypt subtype other than X509Plain"; fi; \
    $probe vencrypt 127.0.0.1 5900 someone 'correct horse' >/tmp/bad; rc=$?; cat /tmp/bad; \
    [ $rc -eq 1 ] || fail "a wrong user name was not refused ($rc)"; \
    $probe vencrypt 127.0.0.1 5900 tester 'correct horse' >/tmp/good; rc=$?; cat /tmp/good; \
    [ $rc -eq 0 ] || fail "the right user name and password were refused ($rc)"; \
    kill $wv 2>/dev/null; wait $wv 2>/dev/null || true; \
    echo "remote desktop: encrypted types only; the right login in, a wrong one out"

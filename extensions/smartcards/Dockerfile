# Smart cards and security keys as an Isotope system extension: the PC/SC
# daemon and its CCID reader driver, OpenSC with its PKCS#11 module, and
# GnuPG's scdaemon. See extensions/README.md for how an extension is built
# and published, and docs/superpowers/specs/2026-11-24-smartcards-design.md.
#
# What it carries:
# - pcsc-lite: pcscd (socket-activated: pcscd.socket is enabled from /usr,
#   pcscd.service exits when idle), libpcsclite.so.1 and
#   libpcsclite_real.so.1 in /usr/lib, where every PC/SC client looks for
#   them (OpenSC, scdaemon, and other programs open them by that name), and
#   polkit: its two actions and Isotope's rule, so the user of the active
#   local session (and root) may use the readers and nobody else.
# - CCID: the driver for USB smart card readers and USB tokens (YubiKey,
#   Nitrokey, most readers), /usr/lib/pcsc/drivers/ifd-ccid.bundle, with
#   libusb linked in statically.
# - OpenSC: its tools (opensc-tool, pkcs11-tool, pkcs15-tool, piv-tool,
#   openpgp-tool, ...) and its PKCS#11 module, /usr/lib/pkcs11/opensc-pkcs11.so,
#   registered with p11-kit (/usr/share/p11-kit/modules/opensc.module, which
#   the desktops' p11-kit reads and a system without p11-kit ignores). Its own
#   libraries live in /usr/lib/isotope-ext/smartcards, behind a RUNPATH.
# - GnuPG's scdaemon (/usr/lib/gnupg/scdaemon, where the image's gpg-agent
#   starts it: the image builds GnuPG with --libexecdir=/usr/lib/gnupg, which
#   Kairos merges) and gpg-card, at the image's GnuPG version, with the
#   GnuPG libraries linked in statically. The image's gpg and gpg-agent are
#   built with card support (the Dockerfile's gnupg stage) and leave only
#   scdaemon out.
#
# FIDO2/U2F security keys need none of this: the Hadron base's udev rules
# (60-fido-id.rules, 70-uaccess.rules) give the active session's user the
# hidraw device. test/smartcards/check-smartcards.sh checks that on the image.
#
# GLib, polkit's library, libsystemd, libudev, OpenSSL's libcrypto and zlib
# come from the image; the build stage fails if an ELF file needs anything
# else.

# Set by .github/workflows/extensions.yml from scripts/bump-hadron.sh, so the
# extension builds on the same Hadron release as the image.
ARG HADRON_VERSION

FROM ghcr.io/kairos-io/hadron-toolchain:${HADRON_VERSION} AS build
RUN pip3 install meson ninja packaging
WORKDIR /build
# No LTO: the static libraries below (libusb and the GnuPG stack) would hold
# LTO bytecode, which a link without the linker plugin cannot read.
ENV CFLAGS="-Os -pipe -fomit-frame-pointer -ffunction-sections -fdata-sections"
ENV LDFLAGS="-Wl,--gc-sections -Wl,--as-needed"
# Static libraries for this stage only, never installed into /output. PIC:
# libusb goes into the CCID driver, a shared module, and the GnuPG libraries
# into position-independent programs.
ENV CONFIGURE_STATIC="--prefix=/usr --host=x86_64-hadron-linux-musl --build=x86_64-hadron-linux-musl --enable-static --disable-shared --with-pic"

# Build-only dependencies, installed into this stage's root and never into
# /output: libucontext (the toolchain's libsystemd links it; the Hadron base
# ships it), PCRE2 and GLib at the versions the image builds, and polkit's
# client library (the image's own copies are what run). Same versions and
# Renovate rules as the Dockerfile at the root, so one Renovate PR moves both.
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
# polkit's libraries only (libs-only: no polkitd, no JavaScript engine), so
# pcscd links libpolkit-gobject-1.so.0; the image's polkit is the one that
# runs. Its policy file and translations are left out: they are polkitd's,
# and merging them needs gettext's msgfmt, which this stage has not.
# renovate: datasource=github-tags depName=polkit-org/polkit extractVersion=^(?<version>\d+(\.\d+)*)$
ARG POLKIT_VERSION=126
RUN curl --retry-all-errors -fL --retry 5 https://github.com/polkit-org/polkit/archive/refs/tags/${POLKIT_VERSION}.tar.gz -o p.tar.gz && \
    tar -xzf p.tar.gz && rm p.tar.gz && mv polkit-* polkit-src && cd polkit-src && \
    grep -qx "subdir('actions')" meson.build && grep -qx "subdir('po')" meson.build && \
    sed -i -e "/^subdir('actions')$/d" -e "/^subdir('po')$/d" meson.build && \
    meson setup buildDir ${COMMON_MESON_FLAGS} -Dlibs-only=true \
      -Dsession_tracking=logind -Dauthfw=pam -Dgettext=false \
      -Dintrospection=false -Dman=false -Dgtk_doc=false -Dexamples=false -Dtests=false && \
    ninja -C buildDir install && test -e /usr/lib/libpolkit-gobject-1.so.0
# The toolchain has libsystemd.pc but no systemd.pc, which pcsc-lite reads
# the unit and sysusers directories from.
RUN printf 'prefix=/usr\nsystemdsystemunitdir=${prefix}/lib/systemd/system\nsystemduserunitdir=${prefix}/lib/systemd/user\nsysusersdir=${prefix}/lib/sysusers.d\n\nName: systemd\nDescription: systemd\nVersion: 257\n' \
      > /usr/lib/pkgconfig/systemd.pc
# libusb, static, for the CCID driver: the image has a shared libusb only on
# some images (CUPS, fwupd), and a copy in /usr/lib is what `isotope ext`
# refuses. Without udev: it finds devices through netlink, so the static
# library needs nothing but the C library.
# renovate: datasource=github-releases depName=libusb/libusb extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG LIBUSB_VERSION=1.0.30
RUN curl --retry-all-errors -fL --retry 5 https://github.com/libusb/libusb/releases/download/v${LIBUSB_VERSION}/libusb-${LIBUSB_VERSION}.tar.bz2 -o u.tar.bz2 && \
    tar -xjf u.tar.bz2 && rm u.tar.bz2 && mv libusb-${LIBUSB_VERSION} libusb-src && cd libusb-src && \
    cp /usr/share/automake-*/config.sub /usr/share/automake-*/config.guess . && \
    ./configure ${CONFIGURE_STATIC} --disable-udev && \
    make -j"$(nproc)" && make install && test -e /usr/lib/libusb-1.0.a && ! test -e /usr/lib/libusb-1.0.so

# pcsc-lite: pcscd and the client library. Installed into / for the next
# builds and into /output. pcscd goes to /usr/bin, as the modemmanager
# extension's daemon does. No serial readers (they need a hand-written
# /etc/reader.conf.d); USB readers through libudev hotplug.
# renovate: datasource=github-tags depName=LudovicRousseau/PCSC
ARG SMARTCARDS_VERSION=2.5.4
ARG PCSC_LITE_SHA256=23b1c644ab4f13269d3a9ff26734f473f622deff6ed60492171a9a25f603f078
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://pcsclite.apdu.fr/files/pcsc-lite-${SMARTCARDS_VERSION}.tar.xz -o p.tar.xz && \
    echo "${PCSC_LITE_SHA256}  p.tar.xz" | sha256sum -c - && \
    tar -xf p.tar.xz && rm p.tar.xz && mv pcsc-lite-${SMARTCARDS_VERSION} pcsc-lite-src && cd pcsc-lite-src && \
    meson setup buildDir ${COMMON_MESON_FLAGS} --bindir=bin --sbindir=bin --sysconfdir=/etc \
      -Dlibsystemd=true -Dlibudev=true -Dlibusb=false -Dpolkit=true -Dusb=true -Dserial=false \
      -Dsystemdunit=system -Dipcdir=/run/pcscd -Dusbdropdir=/usr/lib/pcsc/drivers && \
    ninja -C buildDir install && DESTDIR=/output ninja -C buildDir install

# CCID, the USB reader driver pcscd loads. No udev rule: upstream's gives the
# reader to the pcscd group, and pcscd runs as root here (see the drop-ins
# in files/). systemd's own rules already give the active session's user
# a CCID device (ID_SMARTCARD_READER, uaccess).
# renovate: datasource=github-tags depName=LudovicRousseau/CCID
ARG CCID_VERSION=1.8.4
ARG CCID_SHA256=4ff98151a7feb828a711e2f9d68c6b6065a97c597a81c2cbbd11d7f7edcfe743
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://ccid.apdu.fr/files/ccid-${CCID_VERSION}.tar.xz -o c.tar.xz && \
    echo "${CCID_SHA256}  c.tar.xz" | sha256sum -c - && \
    tar -xf c.tar.xz && rm c.tar.xz && mv ccid-${CCID_VERSION} ccid-src && cd ccid-src && \
    meson setup buildDir ${COMMON_MESON_FLAGS} -Dudev-rules=false -Dserial=false -Dpcsclite=true && \
    DESTDIR=/output ninja -C buildDir install

# OpenSC: the card drivers, the tools and the PKCS#11 module. Its libraries
# (libopensc, libsmm-local, the PKCS#11 module and pkcs11-spy) go to the
# extension's own directory. No readline, notifications (GIO), OpenPACE,
# manuals or tests; --disable-strict, because upstream's strict mode is
# -Werror. OpenSC runs without /etc/opensc.conf (a sysext cannot ship /etc).
# renovate: datasource=github-releases depName=OpenSC/OpenSC
ARG OPENSC_VERSION=0.27.1
ARG OPENSC_SHA256=976f4a23eaf3397a1a2c3a7aac80bf971a8c3d829c9a79f06145bfaeeae5eca7
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/OpenSC/OpenSC/releases/download/${OPENSC_VERSION}/opensc-${OPENSC_VERSION}.tar.gz -o o.tar.gz && \
    echo "${OPENSC_SHA256}  o.tar.gz" | sha256sum -c - && \
    tar -xzf o.tar.gz && rm o.tar.gz && mv opensc-${OPENSC_VERSION} opensc-src && cd opensc-src && \
    LDFLAGS="${LDFLAGS} -Wl,-rpath,/usr/lib/isotope-ext/smartcards" \
    ./configure --prefix=/usr --libdir=/usr/lib/isotope-ext/smartcards --sysconfdir=/etc \
      --host=x86_64-hadron-linux-musl --build=x86_64-hadron-linux-musl \
      --enable-shared --disable-static --disable-dependency-tracking --disable-strict \
      --enable-pcsc --with-pcsc-provider=libpcsclite.so.1 --enable-openssl --enable-zlib \
      --disable-readline --disable-openpace --disable-notify --disable-man --disable-doc \
      --disable-tests --disable-cmocka --disable-autostart-items --disable-dnie-ui && \
    make -j"$(nproc)" && make DESTDIR=/output install

# The GnuPG libraries scdaemon and gpg-card link, built static: the server
# image has no GnuPG at all, and the desktops' shared copies may move to
# other versions than these. Same versions and checksums as the Dockerfile's
# GnuPG stages (test/smartcards/check-smartcards.sh --repo compares them);
# updated by hand with them (scripts/check-renovate-pins.sh).
ARG LIBGPGERROR_VERSION=1.56
ARG LIBGPGERROR_SHA256=82c3d2deb4ad96ad3925d6f9f124fe7205716055ab50e291116ef27975d169c0
ARG LIBASSUAN_VERSION=3.0.2
ARG LIBASSUAN_SHA256=d2931cdad266e633510f9970e1a2f346055e351bb19f9b78912475b8074c36f6
ARG LIBGCRYPT_VERSION=1.12.2
ARG LIBGCRYPT_SHA256=7ce33c2492221a0436f96a8500215e9f3e3dcb5fd26a757cd415e7a843babd5e
ARG LIBKSBA_VERSION=1.8.0
ARG LIBKSBA_SHA256=296b9db9095749f2aa104202d7ab7fd09ad10710e00780a709c9754b1a1d9292
ARG NPTH_VERSION=1.8
ARG NPTH_SHA256=8bd24b4f23a3065d6e5b26e98aba9ce783ea4fd781069c1b35d149694e90ca3e
ARG GNUPG_VERSION=2.4.9
ARG GNUPG_SHA256=dd17ab2e9a04fd79d39d853f599cbc852062ddb9ab52a4ddeb4176fd8b302964
RUN set -eu; \
    get() { curl -fL --retry 5 --retry-delay 3 --retry-all-errors "https://gnupg.org/ftp/gcrypt/$1/$1-$2.tar.bz2" -o "$1.tar.bz2"; \
      echo "$3  $1.tar.bz2" | sha256sum -c -; tar -xf "$1.tar.bz2"; rm "$1.tar.bz2"; mv "$1-$2" "$1-src"; }; \
    get libgpg-error "${LIBGPGERROR_VERSION}" "${LIBGPGERROR_SHA256}"; \
    get libassuan "${LIBASSUAN_VERSION}" "${LIBASSUAN_SHA256}"; \
    get libgcrypt "${LIBGCRYPT_VERSION}" "${LIBGCRYPT_SHA256}"; \
    get libksba "${LIBKSBA_VERSION}" "${LIBKSBA_SHA256}"; \
    get npth "${NPTH_VERSION}" "${NPTH_SHA256}"; \
    get gnupg "${GNUPG_VERSION}" "${GNUPG_SHA256}"
RUN cd libgpg-error-src && ./configure ${CONFIGURE_STATIC} --disable-doc --disable-tests --disable-nls \
      --enable-install-gpg-error-config && make -j"$(nproc)" && make install
RUN cd libassuan-src && ./configure ${CONFIGURE_STATIC} --disable-doc && make -j"$(nproc)" && make install
RUN cd libgcrypt-src && ./configure ${CONFIGURE_STATIC} --disable-doc && make -j"$(nproc)" && make install
RUN cd libksba-src && ./configure ${CONFIGURE_STATIC} --disable-doc && make -j"$(nproc)" && make install
RUN cd npth-src && ./configure ${CONFIGURE_STATIC} --disable-tests && make -j"$(nproc)" && make install
# scdaemon talks to the readers through pcscd only (no CCID driver of its
# own, which would take the reader from pcscd and from OpenSC). On a C
# library other than glibc it opens "libpcsclite.so", the development link
# no system ships; here it opens libpcsclite.so.1, as on glibc. Only
# scdaemon and gpg-card are taken; gpg and gpg-agent are the image's.
RUN cd gnupg-src && \
    grep -qx '#define DEFAULT_PCSC_DRIVER "libpcsclite.so"' scd/scdaemon.c && \
    sed -i 's|^#define DEFAULT_PCSC_DRIVER "libpcsclite.so"$|#define DEFAULT_PCSC_DRIVER "libpcsclite.so.1"|' scd/scdaemon.c && \
    [ "$(grep -c '^#define DEFAULT_PCSC_DRIVER "libpcsclite.so.1"$' scd/scdaemon.c)" = 2 ] && \
    ./configure --prefix=/usr --libexecdir=/usr/lib/gnupg \
      --host=x86_64-hadron-linux-musl --build=x86_64-hadron-linux-musl \
      --disable-gpgsm --disable-dirmngr --disable-keyboxd --disable-tpm2d --disable-wks-tools \
      --disable-gpgtar --disable-doc --disable-nls --disable-ldap --disable-libdns --disable-bzip2 \
      --disable-sqlite --disable-ntbtls --disable-gnutls --disable-ccid-driver --disable-tests \
      --without-readline && \
    make -j"$(nproc)" && \
    install -Dm755 scd/scdaemon /output/usr/lib/gnupg/scdaemon && \
    install -Dm755 tools/gpg-card /output/usr/bin/gpg-card && \
    strip /output/usr/lib/gnupg/scdaemon /output/usr/bin/gpg-card

# Isotope's drop-ins (pcscd as root with no capabilities), the polkit rule
# and the p11-kit module file.
COPY files/ /output/

# Licences (the static GnuPG libraries and libusb included, since they are
# linked in), the PKCS#11 module where programs look for it, pcscd.socket
# enabled from /usr (a sysext cannot write the links in /etc that `systemctl
# enable` would; `isotope ext` starts it after the merge), then everything a
# system extension must not carry: /etc, headers, pkg-config files, static
# libraries, manuals, pcsc-lite's spy (a Python debugging tool), the
# sysusers file of the pcscd user (not used: see the drop-ins), the AppStream
# file, OpenSC's own pkcs11 link directory and pkcs11-register (it writes
# host paths into browsers' NSS databases; Isotope's browsers are Flatpaks).
RUN set -eux; \
    L=/output/usr/share/licenses/smartcards-extension; \
    for p in pcsc-lite-src:pcsc-lite ccid-src:ccid opensc-src:opensc gnupg-src:gnupg \
             libgpg-error-src:libgpg-error libassuan-src:libassuan libgcrypt-src:libgcrypt \
             libksba-src:libksba npth-src:npth libusb-src:libusb; do \
      src=${p%%:*}; name=${p#*:}; mkdir -p $L/$name; \
      lic=$(ls -d $src/COPYING* $src/LICENSE* $src/LICENSES/* 2>/dev/null || true); \
      [ -n "$lic" ] || { echo "no licence in $src" >&2; exit 1; }; \
      cp -r $lic $L/$name/; \
    done; \
    mkdir -p /output/usr/lib/pkcs11; \
    ln -s ../isotope-ext/smartcards/opensc-pkcs11.so /output/usr/lib/pkcs11/opensc-pkcs11.so; \
    u=/output/usr/lib/systemd/system; \
    mkdir -p $u/sockets.target.wants; \
    ln -s ../pcscd.socket $u/sockets.target.wants/pcscd.socket; \
    rm -rf /output/etc /output/usr/include /output/usr/share/man /output/usr/share/doc \
      /output/usr/share/metainfo /output/usr/share/bash-completion /output/usr/lib/sysusers.d \
      /output/usr/lib/pkgconfig /output/usr/lib/isotope-ext/smartcards/pkgconfig \
      /output/usr/lib/isotope-ext/smartcards/pkcs11 /output/usr/bin/pcsc-spy \
      /output/usr/bin/pkcs11-register /output/usr/share/applications; \
    rm -f /output/usr/lib/libpcscspy.so* /output/usr/lib/libpcsclite.so \
      /output/usr/lib/libpcsclite_real.so /output/usr/lib/isotope-ext/smartcards/onepin-opensc-pkcs11.so \
      /output/usr/lib/isotope-ext/smartcards/libopensc.so; \
    find /output -name '*.la' -delete -o -name '*.a' -delete
# Every ELF file needs only the C library, GLib, polkit's client library,
# libsystemd, libudev, OpenSSL's libcrypto, zlib and libraries of the
# extension itself; the OpenSC programs carry the RUNPATH that finds them;
# pcscd links polkit (its access control) and libsystemd (socket
# activation); scdaemon opens libpcsclite.so.1 and has no CCID driver of its
# own (no libusb); nothing of GnuPG's libraries is linked shared.
RUN set -eu; \
    allowed=" libc.so libglib-2.0.so.0 libgobject-2.0.so.0 libgio-2.0.so.0 libpolkit-gobject-1.so.0 libsystemd.so.0 libudev.so.1 libcrypto.so.3 libz.so.1 libpcsclite.so.1 "; \
    bad=; n=0; \
    for f in $(find /output -type f); do \
      [ "$(head -c 4 "$f" | od -An -c | tr -d ' ')" = '177ELF' ] || continue; n=$((n+1)); \
      for lib in $(readelf -d "$f" | sed -n 's/.*(NEEDED).*\[\(.*\)\]/\1/p'); do \
        case "$allowed" in *" $lib "*) continue ;; esac; \
        [ -e "/output/usr/lib/isotope-ext/smartcards/$lib" ] && continue; \
        bad="$bad $f:$lib"; \
      done; \
    done; \
    echo "$n ELF files"; [ "$n" -gt 15 ]; \
    if [ -n "$bad" ]; then echo "libraries the image does not have:$bad" >&2; exit 1; fi; \
    for b in opensc-tool pkcs11-tool pkcs15-tool piv-tool openpgp-tool; do \
      readelf -d /output/usr/bin/$b | grep -E 'R(UN)?PATH' | grep -q /usr/lib/isotope-ext/smartcards || { echo "$b has no RUNPATH" >&2; exit 1; }; \
    done; \
    readelf -d /output/usr/lib/isotope-ext/smartcards/opensc-pkcs11.so | grep -E 'R(UN)?PATH' | grep -q /usr/lib/isotope-ext/smartcards; \
    readelf -d /output/usr/bin/pcscd | grep -q 'NEEDED.*libpolkit-gobject-1.so.0'; \
    readelf -d /output/usr/bin/pcscd | grep -q 'NEEDED.*libsystemd.so.0'; \
    ! readelf -d /output/usr/lib/gnupg/scdaemon | grep -E 'NEEDED.*(gcrypt|gpg-error|assuan|ksba|npth|usb)' || exit 1; \
    ! readelf -d /output/usr/bin/gpg-card | grep -E 'NEEDED.*(gcrypt|gpg-error|assuan|ksba|npth)' || exit 1; \
    grep -q 'libpcsclite.so.1' /output/usr/lib/gnupg/scdaemon; \
    ! grep -q 'libusb_' /output/usr/lib/gnupg/scdaemon || exit 1; \
    test -f /output/usr/lib/pcsc/drivers/ifd-ccid.bundle/Contents/Info.plist; \
    test -f /output/usr/lib/pcsc/drivers/ifd-ccid.bundle/Contents/Linux/libccid.so; \
    ! readelf -d /output/usr/lib/pcsc/drivers/ifd-ccid.bundle/Contents/Linux/libccid.so | grep -q 'NEEDED.*libusb' || exit 1

# What the extension contains: everything under /usr of this stage.
FROM scratch AS default
COPY --from=build /output/usr /usr

# For the test stage only, never the extension: a stand-in for the image's
# GLib and polkit client library, which every Isotope image has and the
# Hadron base has not.
FROM build AS image-runtime
RUN mkdir -p /image-runtime/usr/lib && \
    cp -a /usr/lib/libglib-2.0.so.0* /usr/lib/libgobject-2.0.so.0* /usr/lib/libgio-2.0.so.0* \
      /usr/lib/libgmodule-2.0.so.0* /usr/lib/libpcre2-8.so.0* /usr/lib/libffi.so.* \
      /usr/lib/libpolkit-gobject-1.so.0* /image-runtime/usr/lib/

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
COPY --from=image-runtime /image-runtime/ /image-runtime/
RUN cp -an /image-runtime/. / && rm -rf /image-runtime
RUN cp -a /extension/. / && rm -rf /extension
# OpenSC's tools exit 2 on --help; they run against pcscd below.
RUN set -eux; pcscd --version; \
    /usr/lib/gnupg/scdaemon --version | grep -F 'scdaemon (GnuPG) 2.4.9'; gpg-card --version | grep -F 'gpg-card (GnuPG) 2.4.9'
# Every library each ELF file needs is found. The CCID driver's log and
# PC/SC symbols come from the pcscd program that loads it, so for it musl's
# ldd also prints "Error relocating ...: symbol not found" and exits 1;
# those lines are left out, a missing library is not.
RUN set -eu; while read -r f; do \
      case "$f" in \
        /usr/lib/pcsc/drivers/*) \
          ldd "$f" 2>&1 | grep -v '^Error relocating .*: symbol not found$' >/tmp/ldd || true ;; \
        *) ldd "$f" >/tmp/ldd 2>&1 || { echo "ldd $f failed" >&2; cat /tmp/ldd >&2; exit 1; } ;; \
      esac; \
      if grep -qE 'not found|Error loading' /tmp/ldd; then echo "$f:" >&2; cat /tmp/ldd >&2; exit 1; fi; \
    done </tmp/elf
# The units, the drop-ins, the link that enables the socket from /usr, the
# polkit files and the p11-kit module.
RUN set -eux; \
    u=/usr/lib/systemd/system; \
    grep -q '^ExecStart=/usr/bin/pcscd --foreground --auto-exit' $u/pcscd.service; \
    grep -qx 'ListenStream=/run/pcscd/pcscd.comm' $u/pcscd.socket; \
    [ "$(readlink $u/sockets.target.wants/pcscd.socket)" = ../pcscd.socket ]; \
    grep -qx 'User=' $u/pcscd.service.d/10-isotope.conf; \
    grep -qx 'SocketUser=' $u/pcscd.socket.d/10-isotope.conf; \
    grep -qx 'SocketGroup=' $u/pcscd.socket.d/10-isotope.conf; \
    grep -q 'org.debian.pcsc-lite.access_pcsc' /usr/share/polkit-1/actions/org.debian.pcsc-lite.policy; \
    grep -q 'org.debian.pcsc-lite.access_card' /usr/share/polkit-1/actions/org.debian.pcsc-lite.policy; \
    test -f /usr/share/polkit-1/rules.d/50-isotope-pcscd.rules; \
    m=$(sed -n 's/^module: *//p' /usr/share/p11-kit/modules/opensc.module); test -f "$m"; \
    test -e /usr/lib/libpcsclite.so.1 && test -e /usr/lib/libpcsclite_real.so.1; \
    test -s /usr/share/licenses/smartcards-extension/pcsc-lite/COPYING
# pcscd on this container, without polkit (no polkitd here; the rule is
# checked by test/polkit/check-polkit-rules.sh): every client finds it
# through the socket and reports no reader, which shows each one loaded
# libpcsclite.so.1 and reached the daemon. pcscd's log is printed on every
# failure.
RUN set -u; \
    fail() { echo "FAIL: $*" >&2; echo "--- pcscd's log:" >&2; cat /tmp/pcscd.log >&2 2>/dev/null || true; exit 1; }; \
    mkdir -p /run/pcscd; \
    pcscd --foreground --disable-polkit --debug >/tmp/pcscd.log 2>&1 & p=$!; \
    i=0; until [ -S /run/pcscd/pcscd.comm ]; do \
      kill -0 "$p" 2>/dev/null || fail "pcscd exited"; \
      i=$((i+1)); [ "$i" -le 100 ] || fail "pcscd made no socket in 20 s"; sleep 0.2; \
    done; \
    opensc-tool --info >/tmp/info.out 2>&1; cat /tmp/info.out; \
    grep -q 'pcsc(libpcsclite.so.1)' /tmp/info.out || fail "opensc-tool --info: no PC/SC through libpcsclite.so.1"; \
    opensc-tool --list-readers >/tmp/readers.out 2>&1; cat /tmp/readers.out; \
    grep -q 'No smart card readers found' /tmp/readers.out || fail "opensc-tool --list-readers"; \
    pkcs11-tool --module /usr/lib/pkcs11/opensc-pkcs11.so --list-slots >/tmp/slots.out 2>&1; \
    cat /tmp/slots.out; grep -q 'Available slots' /tmp/slots.out || fail "pkcs11-tool --list-slots"; \
    printf 'GETINFO version\nSERIALNO\nBYE\n' | /usr/lib/gnupg/scdaemon --server --log-file /tmp/scd.log --debug-level guru >/tmp/scd.out 2>&1; \
    cat /tmp/scd.out; \
    grep -q '^D 2.4.9' /tmp/scd.out || fail "scdaemon GETINFO version"; \
    if grep -q 'failed to open driver' /tmp/scd.log; then cat /tmp/scd.log; fail "scdaemon could not open libpcsclite.so.1"; fi; \
    if grep -qE 'pcsc_establish_context failed|PC/SC .* not available' /tmp/scd.log; then cat /tmp/scd.log; fail "scdaemon did not reach pcscd"; fi; \
    grep -q '^ERR ' /tmp/scd.out || fail "scdaemon SERIALNO found a card where there is none"; \
    kill "$p"; wait "$p" 2>/dev/null || true; \
    echo "pcscd, OpenSC and scdaemon talk over the socket"

# fcitx5, the input method framework, with Pinyin (Chinese), Anthy
# (Japanese) and Hangul (Korean), as an Isotope system extension: typing the
# scripts that need an input method on sway, i3, niri and COSMIC. See
# extensions/README.md for how an extension is built and published, and
# docs/superpowers/specs/2026-11-24-input-methods-design.md for the choices.
#
# What it has: fcitx5 with its Wayland input method frontend
# (zwp_input_method_v2, which sway, niri and cosmic-comp offer), its X11 XIM
# frontend (i3, and X11 apps under Xwayland), its D-Bus frontend (the IM
# modules in Flatpak runtimes, org.freedesktop.portal.Fcitx), the classic UI
# (the candidate window and the tray icon), fcitx5-chinese-addons (Pinyin,
# Shuangpin, tables) on libime, fcitx5-anthy on anthy-unicode and
# fcitx5-hangul on libhangul. No configuration tool (it is Qt): the session
# gives a new user a profile, and `isotope input-method set` changes it.
#
# The image has the hooks only (an /etc/profile.d file, a line in every
# session), which do nothing without /usr/bin/fcitx5.
#
# Libraries: the ones in image-libs.txt come from the image (every desktop
# has them; check-input-methods.sh IMAGE proves it); every other one, Cairo
# with XCB, Pango, librsvg, libxcb, libX11 and fcitx5's own, lives in
# /usr/lib/isotope-ext/fcitx5 behind a RUNPATH. Kairos merges only /usr/bin,
# /usr/sbin, /usr/lib, /usr/share, /usr/include and /usr/src, so helpers go
# under that directory too, and anthy's configuration file (which would be
# /etc/anthy-unicode.conf) as well.

# Set by .github/workflows/extensions.yml from scripts/bump-hadron.sh, so the
# extension builds on the same Hadron release as the image.
ARG HADRON_VERSION

# Rust for librsvg (fcitx5's classic UI draws SVG themes with it): build time
# only, as in the root Dockerfile.
FROM rust:1.97.0-alpine3.22 AS rust-toolchain

FROM ghcr.io/kairos-io/hadron-toolchain:${HADRON_VERSION} AS build
RUN pip3 install meson ninja packaging
WORKDIR /build
COPY build/licence.sh /usr/local/bin/licence
COPY image-libs.txt /image-libs.txt
RUN chmod +x /usr/local/bin/licence

# ---------------------------------------------------------------------------
# Build tools, never in the extension.

# gettext's msgfmt: fcitx5 and its engines need it (find_package(Gettext
# REQUIRED)) and their translations matter here. Installed under /opt with
# only its programs linked into /usr/local/bin: on musl gettext installs a
# libintl of its own, and CMake would link it (libintl.so.8 is in no image)
# if it were on a path CMake searches.
ARG GETTEXT_VERSION=0.22.5
RUN for m in https://mirrors.kernel.org/gnu https://ftpmirror.gnu.org https://ftp.gnu.org/gnu; do curl -fL --connect-timeout 30 --retry 3 --retry-delay 3 --retry-all-errors "$m/gettext/gettext-${GETTEXT_VERSION}.tar.xz" -o g.tar.xz && break; rm -f g.tar.xz; done && test -s g.tar.xz && tar -xf g.tar.xz && rm g.tar.xz && \
    cd gettext-${GETTEXT_VERSION} && \
    ./configure --prefix=/opt/gettext --disable-java --disable-csharp --disable-libasprintf \
      --disable-openmp --disable-acl --disable-curses --without-emacs --without-git \
      --disable-static --enable-shared && \
    make -j"$(nproc)" && make install && \
    for t in msgfmt msgmerge xgettext msginit msgcat msgconv msguniq autopoint; do ln -s /opt/gettext/bin/$t /usr/local/bin/$t; done && \
    msgfmt --version | head -n 1 && cd .. && rm -rf gettext-${GETTEXT_VERSION}

# gperf: fontconfig's build needs it.
ARG GPERF_VERSION=3.1
RUN for m in https://mirrors.kernel.org/gnu https://ftpmirror.gnu.org https://ftp.gnu.org/gnu; do curl -fL --connect-timeout 30 --retry 3 --retry-delay 3 --retry-all-errors "$m/gperf/gperf-${GPERF_VERSION}.tar.gz" -o gperf.tar.gz && break; rm -f gperf.tar.gz; done && test -s gperf.tar.gz && tar -xzf gperf.tar.gz && rm gperf.tar.gz && \
    cd gperf-${GPERF_VERSION} && ./configure ${COMMON_CONFIGURE_ARGS} --disable-dependency-tracking && \
    make -j"$(nproc)" CFLAGS="-std=gnu17 -O2 -g" && make install && cd .. && rm -rf gperf-${GPERF_VERSION}

# libucontext: the toolchain's libsystemd needs libucontext.so.1, which the
# toolchain does not have, so every program linked against it (fcitx5's
# sd-bus and sd-event, and the tools libime and fcitx5 run during their
# builds) fails to link without it. Into /usr/lib of this stage only, as
# in the root Dockerfile and the smartcards extension: the Hadron base
# ships its own copy, which is what runs. Same version and Renovate rule as
# the root Dockerfile.
# renovate: datasource=github-tags depName=kaniini/libucontext extractVersion=^libucontext-(?<version>\d+(\.\d+)*)$
ARG LIBUCONTEXT_VERSION=1.5.1
RUN curl --retry-all-errors -fL --retry 5 https://github.com/kaniini/libucontext/archive/refs/tags/libucontext-${LIBUCONTEXT_VERSION}.tar.gz -o u.tar.gz && \
    tar -xf u.tar.gz && rm u.tar.gz && cd libucontext-libucontext-${LIBUCONTEXT_VERSION} && \
    make -j"$(nproc)" ARCH=x86_64 && make ARCH=x86_64 install && cd .. && rm -rf libucontext-libucontext-${LIBUCONTEXT_VERSION} && \
    test -e /usr/lib/libucontext.so.1

# The toolchain compiles C with -flto (CFLAGS) and sets AR=gcc-ar and
# RANLIB=gcc-ranlib, which Autotools and Meson use. CMake does not read AR
# or RANLIB: it finds plain ar, which cannot index LTO objects ("plugin
# needed to handle lto object"), and fcitx5's static Wayland protocol
# libraries come out with no symbols. CMAKE_PRIVATE below names gcc-ar and
# gcc-ranlib by these paths.
RUN for t in gcc-ar gcc-ranlib; do [ -x /usr/bin/$t ] || ln -s "$(command -v $t)" /usr/bin/$t; done && \
    /usr/bin/gcc-ar --version | head -n 1

# KDE's extra-cmake-modules: fcitx5, xcb-imdkit, libime and the engines
# build with it.
# renovate: datasource=github-tags depName=KDE/extra-cmake-modules extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG ECM_VERSION=6.32.0
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/KDE/extra-cmake-modules/archive/refs/tags/v${ECM_VERSION}.tar.gz -o ecm.tar.gz && \
    tar -xzf ecm.tar.gz && rm ecm.tar.gz && cd extra-cmake-modules-${ECM_VERSION} && \
    cmake -G Ninja -B b -DCMAKE_INSTALL_PREFIX=/usr -DBUILD_TESTING=OFF -DBUILD_DOC=OFF \
      -DBUILD_HTML_DOCS=OFF -DBUILD_MAN_DOCS=OFF -DBUILD_QTHELP_DOCS=OFF && \
    cmake --install b && cd .. && rm -rf extra-cmake-modules-${ECM_VERSION}

# nlohmann/json (header only): fcitx5's keyboard engine. Its MIT licence
# ships, since the code is compiled in.
# renovate: datasource=github-releases depName=nlohmann/json extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG NLOHMANN_JSON_VERSION=3.12.0
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/nlohmann/json/releases/download/v${NLOHMANN_JSON_VERSION}/json.tar.xz -o json.tar.xz && \
    tar -xf json.tar.xz && rm json.tar.xz && mv json nlohmann-json && cd nlohmann-json && \
    cmake -G Ninja -B b -DCMAKE_INSTALL_PREFIX=/usr -DJSON_BuildTests=OFF -DJSON_Install=ON && \
    cmake --install b && licence nlohmann-json . && cd .. && rm -rf nlohmann-json

# Boost: libime needs Boost.Iostreams (linked in statically, built with
# -fPIC and no compression filters), fcitx5-chinese-addons the headers.
# renovate: datasource=github-releases depName=boostorg/boost extractVersion=^boost-(?<version>\d+\.\d+\.\d+)$
ARG BOOST_VERSION=1.92.0
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/boostorg/boost/releases/download/boost-${BOOST_VERSION}/boost-${BOOST_VERSION}-b2-nodocs.tar.xz -o boost.tar.xz && \
    tar -xf boost.tar.xz && rm boost.tar.xz && cd boost-${BOOST_VERSION} && \
    ./bootstrap.sh --prefix=/usr --with-libraries=iostreams && \
    ./b2 -j"$(nproc)" install --prefix=/usr link=static runtime-link=shared variant=release \
      threading=multi cxxflags=-fPIC cflags=-fPIC -sNO_COMPRESSION=1 -sNO_BZIP2=1 -sNO_ZLIB=1 -sNO_LZMA=1 -sNO_ZSTD=1 && \
    test -f /usr/lib/libboost_iostreams.a && licence boost . && cd .. && rm -rf boost-${BOOST_VERSION}

# ---------------------------------------------------------------------------
# The image's libraries, at the image's versions (the same ARGs and Renovate
# rules as the root Dockerfile, so one Renovate PR moves both). They are
# installed here to build against; the image's own copies are what runs.
# The test stage gets them as a stand-in for the image (/image-runtime).

# renovate: datasource=github-tags depName=PCRE2Project/pcre2 extractVersion=^pcre2-(?<version>\d+(\.\d+)*)$
ARG PCRE2_VERSION=10.44
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/PCRE2Project/pcre2/releases/download/pcre2-${PCRE2_VERSION}/pcre2-${PCRE2_VERSION}.tar.gz -o p.tar.gz && \
    tar -xzf p.tar.gz && rm p.tar.gz && cd pcre2-${PCRE2_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-dependency-tracking --enable-pcre2-16 --enable-pcre2-32 && \
    make -j"$(nproc)" && make install && cd .. && rm -rf pcre2-${PCRE2_VERSION}
# renovate: datasource=gitlab-tags depName=GNOME/glib registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG GLIB_VERSION=2.82.5
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.gnome.org/sources/glib/${GLIB_VERSION%.*}/glib-${GLIB_VERSION}.tar.xz -o g.tar.xz && \
    tar -xf g.tar.xz && rm g.tar.xz && cd glib-${GLIB_VERSION} && \
    meson setup b ${COMMON_MESON_FLAGS} \
      -Dnls=disabled -Dtests=false -Dlibmount=disabled -Dselinux=disabled \
      -Dman-pages=disabled -Dintrospection=disabled -Dglib_debug=disabled \
      -Ddtrace=disabled -Dsysprof=disabled && \
    ninja -C b install && cd .. && rm -rf glib-${GLIB_VERSION}
# renovate: datasource=github-tags depName=pnggroup/libpng extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG LIBPNG_VERSION=1.6.44
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.sourceforge.net/libpng/libpng-${LIBPNG_VERSION}.tar.xz -o png.tar.xz && \
    tar -xf png.tar.xz && rm png.tar.xz && cd libpng-${LIBPNG_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-dependency-tracking && \
    sed -i 's/^pnglibconf\.h: pnglibconf\.out$/pnglibconf.h:/' Makefile && \
    cp scripts/pnglibconf.h.prebuilt pnglibconf.h && \
    make -j"$(nproc)" && make install && cd .. && rm -rf libpng-${LIBPNG_VERSION}
ARG FREETYPE_VERSION=2.13.3
ARG FREETYPE_SHA256=0550350666d427c74daeb85d5ac7bb353acba5f76956395995311a9c6f063289
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors \
      https://download.savannah.gnu.org/releases/freetype/freetype-${FREETYPE_VERSION}.tar.xz -o freetype.tar.xz \
    || curl -fL --retry 5 --retry-delay 3 --retry-all-errors \
      https://downloads.sourceforge.net/freetype/freetype-${FREETYPE_VERSION}.tar.xz -o freetype.tar.xz && \
    echo "${FREETYPE_SHA256}  freetype.tar.xz" | sha256sum -c - && \
    tar -xf freetype.tar.xz && rm freetype.tar.xz && cd freetype-${FREETYPE_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-dependency-tracking --with-harfbuzz=no --with-png=yes --with-brotli=no --with-bzip2=no --with-zlib=yes && \
    make -j"$(nproc)" && make install && cd .. && rm -rf freetype-${FREETYPE_VERSION}
# renovate: datasource=github-tags depName=harfbuzz/harfbuzz extractVersion=^(?<version>\d+(\.\d+)*)$
ARG HARFBUZZ_VERSION=10.1.0
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/harfbuzz/harfbuzz/releases/download/${HARFBUZZ_VERSION}/harfbuzz-${HARFBUZZ_VERSION}.tar.xz -o hb.tar.xz && \
    tar -xf hb.tar.xz && rm hb.tar.xz && cd harfbuzz-${HARFBUZZ_VERSION} && \
    meson setup b ${COMMON_MESON_FLAGS} -Dtests=disabled -Ddocs=disabled -Dintrospection=disabled -Dglib=enabled -Dfreetype=enabled -Dcairo=disabled -Dchafa=disabled && \
    ninja -C b install && cd .. && rm -rf harfbuzz-${HARFBUZZ_VERSION}
# renovate: datasource=github-tags depName=fribidi/fribidi extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG FRIBIDI_VERSION=1.0.16
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/fribidi/fribidi/releases/download/v${FRIBIDI_VERSION}/fribidi-${FRIBIDI_VERSION}.tar.xz -o f.tar.xz && \
    tar -xf f.tar.xz && rm f.tar.xz && cd fribidi-${FRIBIDI_VERSION} && \
    meson setup b ${COMMON_MESON_FLAGS} -Dtests=false -Ddocs=false -Dbin=false && \
    ninja -C b install && cd .. && rm -rf fribidi-${FRIBIDI_VERSION}
# renovate: datasource=gitlab-tags depName=fontconfig/fontconfig registryUrl=https://gitlab.freedesktop.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG FONTCONFIG_VERSION=2.15.0
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://www.freedesktop.org/software/fontconfig/release/fontconfig-${FONTCONFIG_VERSION}.tar.xz -o fc.tar.xz && \
    tar -xf fc.tar.xz && rm fc.tar.xz && cd fontconfig-${FONTCONFIG_VERSION} && \
    meson setup b ${COMMON_MESON_FLAGS} -Dtests=disabled -Dtools=disabled -Ddoc=disabled -Dnls=disabled -Dcache-build=disabled && \
    ninja -C b install && cd .. && rm -rf fontconfig-${FONTCONFIG_VERSION}
# renovate: datasource=gitlab-tags depName=pixman/pixman registryUrl=https://gitlab.freedesktop.org extractVersion=^pixman-(?<version>\d+(\.\d+)*)$
ARG PIXMAN_VERSION=0.46.0
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://www.cairographics.org/releases/pixman-${PIXMAN_VERSION}.tar.gz -o px.tar.gz && \
    tar -xzf px.tar.gz && rm px.tar.gz && cd pixman-${PIXMAN_VERSION} && \
    meson setup b ${COMMON_MESON_FLAGS} && ninja -C b install && cd .. && rm -rf pixman-${PIXMAN_VERSION}
# renovate: datasource=gitlab-tags depName=GNOME/gdk-pixbuf registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG GDKPIXBUF_VERSION=2.42.12
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.gnome.org/sources/gdk-pixbuf/${GDKPIXBUF_VERSION%.*}/gdk-pixbuf-${GDKPIXBUF_VERSION}.tar.xz -o gp.tar.xz && \
    tar -xf gp.tar.xz && rm gp.tar.xz && cd gdk-pixbuf-${GDKPIXBUF_VERSION} && \
    meson setup b ${COMMON_MESON_FLAGS} -Dtiff=disabled -Djpeg=disabled -Dothers=disabled \
      -Dintrospection=disabled -Dman=false -Ddocs=false -Dinstalled_tests=false -Dgio_sniffing=false && \
    ninja -C b install && cd .. && rm -rf gdk-pixbuf-${GDKPIXBUF_VERSION}
# renovate: datasource=gitlab-tags depName=wayland/wayland registryUrl=https://gitlab.freedesktop.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG WAYLAND_VERSION=1.25.0
# renovate: datasource=gitlab-tags depName=wayland/wayland-protocols registryUrl=https://gitlab.freedesktop.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG WAYLAND_PROTOCOLS_VERSION=1.49
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://gitlab.freedesktop.org/wayland/wayland/-/archive/${WAYLAND_VERSION}/wayland-${WAYLAND_VERSION}.tar -o w.tar && \
    tar -xf w.tar && rm w.tar && cd wayland-${WAYLAND_VERSION} && \
    meson setup b ${COMMON_MESON_FLAGS} -Dtests=false -Ddocumentation=false -Dscanner=true -Dlibraries=true && \
    ninja -C b install && cd .. && rm -rf wayland-${WAYLAND_VERSION} && \
    curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://gitlab.freedesktop.org/wayland/wayland-protocols/-/archive/${WAYLAND_PROTOCOLS_VERSION}/wayland-protocols-${WAYLAND_PROTOCOLS_VERSION}.tar -o wp.tar && \
    tar -xf wp.tar && rm wp.tar && cd wayland-protocols-${WAYLAND_PROTOCOLS_VERSION} && \
    meson setup b ${COMMON_MESON_FLAGS} -Dtests=false && ninja -C b install && cd .. && rm -rf wayland-protocols-${WAYLAND_PROTOCOLS_VERSION}
# The keyboard layouts (data only): fcitx5 reads /usr/share/X11/xkb, which
# every desktop image has; the test stage needs a copy.
# renovate: datasource=gitlab-tags depName=xkeyboard-config/xkeyboard-config registryUrl=https://gitlab.freedesktop.org extractVersion=^xkeyboard-config-(?<version>\d+(\.\d+)*)$
ARG XKEYBOARD_CONFIG_VERSION=2.44
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://gitlab.freedesktop.org/xkeyboard-config/xkeyboard-config/-/archive/xkeyboard-config-${XKEYBOARD_CONFIG_VERSION}/xkeyboard-config-xkeyboard-config-${XKEYBOARD_CONFIG_VERSION}.tar.gz -o xkb.tar.gz && \
    tar -xf xkb.tar.gz && rm xkb.tar.gz && cd xkeyboard-config-xkeyboard-config-${XKEYBOARD_CONFIG_VERSION} && \
    meson setup b ${COMMON_MESON_FLAGS} && ninja -C b install && DESTDIR=/image-runtime ninja -C b install && \
    cd .. && rm -rf xkeyboard-config-xkeyboard-config-${XKEYBOARD_CONFIG_VERSION} && \
    test -f /usr/share/X11/xkb/rules/evdev.xml

# ---------------------------------------------------------------------------
# The extension's own libraries and programs: built into
# /usr/lib/isotope-ext/fcitx5 with that RUNPATH, and installed twice, into /
# for the next builds and into /output for the extension. fcitx5's and
# libime's CMake packages land in its cmake/ directory, which
# CMAKE_PREFIX_PATH names (CMake finds <prefix>/<name>*/ there).
ENV P=/usr/lib/isotope-ext/fcitx5
ENV PKG_CONFIG_PATH=/usr/lib/isotope-ext/fcitx5/pkgconfig
ENV MESON_PRIVATE="--prefix=/usr --libdir=lib/isotope-ext/fcitx5 --libexecdir=lib/isotope-ext/fcitx5/libexec --buildtype=minsize -Dstrip=true"
ENV CMAKE_PRIVATE="-G Ninja -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/usr -DCMAKE_INSTALL_LIBDIR=lib/isotope-ext/fcitx5 -DCMAKE_INSTALL_LIBEXECDIR=lib/isotope-ext/fcitx5/libexec -DCMAKE_INSTALL_SYSCONFDIR=/etc -DCMAKE_INSTALL_RPATH=/usr/lib/isotope-ext/fcitx5 -DCMAKE_PREFIX_PATH=/usr/lib/isotope-ext/fcitx5/cmake -DBUILD_TESTING=OFF -DCMAKE_AR=/usr/bin/gcc-ar -DCMAKE_RANLIB=/usr/bin/gcc-ranlib"

# X11: the protocol headers and xtrans to build with (never shipped), then
# libXau, libxcb, the xcb utilities, libX11 (for libxkbfile) and
# libxkbfile. Same versions as the i3 image's.
# renovate: datasource=gitlab-tags depName=xorg/proto/xorgproto registryUrl=https://gitlab.freedesktop.org extractVersion=^xorgproto-(?<version>\d+(\.\d+)*)$
ARG XORGPROTO_VERSION=2025.1
# renovate: datasource=gitlab-tags depName=xorg/proto/xcbproto registryUrl=https://gitlab.freedesktop.org extractVersion=^xcb-proto-(?<version>\d+(\.\d+)*)$
ARG XCB_VERSION=1.17.0
# renovate: datasource=gitlab-tags depName=xorg/lib/libxtrans registryUrl=https://gitlab.freedesktop.org extractVersion=^xtrans-(?<version>\d+(\.\d+)*)$
ARG XTRANS_VERSION=1.6.0
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://github.com/X11Libre/mirror.fdo.xorgproto/archive/refs/tags/xorgproto-${XORGPROTO_VERSION}.tar.gz -o xp.tar.gz && \
    tar -xf xp.tar.gz && rm xp.tar.gz && cd mirror.fdo.xorgproto-* && \
    meson setup b ${COMMON_MESON_FLAGS} && ninja -C b install && cd .. && rm -rf mirror.fdo.xorgproto-* && \
    curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://xcb.freedesktop.org/dist/xcb-proto-${XCB_VERSION}.tar.gz -o xcbp.tar.gz && \
    tar -xzf xcbp.tar.gz && rm xcbp.tar.gz && cd xcb-proto-${XCB_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --build=${BUILD} --disable-dependency-tracking && \
    make -j"$(nproc)" && make install && cd .. && rm -rf xcb-proto-${XCB_VERSION} && \
    curl --retry-all-errors -fL --retry 5 https://xorg.freedesktop.org/archive/individual/lib/xtrans-${XTRANS_VERSION}.tar.xz -o xt.tar.xz && \
    tar -xf xt.tar.xz && rm xt.tar.xz && cd xtrans-${XTRANS_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-docs && make install && cd .. && rm -rf xtrans-${XTRANS_VERSION}
# renovate: datasource=gitlab-tags depName=xorg/lib/libxau registryUrl=https://gitlab.freedesktop.org extractVersion=^libXau-(?<version>\d+(\.\d+)*)$
ARG LIBXAU_VERSION=1.0.12
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://gitlab.freedesktop.org/xorg/lib/libxau/-/archive/libXau-${LIBXAU_VERSION}/libxau-libXau-${LIBXAU_VERSION}.tar.gz -o xau.tar.gz && \
    tar -xf xau.tar.gz && rm xau.tar.gz && cd libxau-libXau-${LIBXAU_VERSION} && \
    LDFLAGS="$LDFLAGS -Wl,-rpath,$P" meson setup b ${MESON_PRIVATE} && \
    ninja -C b install && DESTDIR=/output ninja -C b install && licence libXau . && cd .. && rm -rf libxau-libXau-${LIBXAU_VERSION}
# renovate: datasource=gitlab-tags depName=xorg/lib/libxcb registryUrl=https://gitlab.freedesktop.org extractVersion=^libxcb-(?<version>\d+(\.\d+)*)$
ARG LIBXCB_VERSION=1.17.0
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://xcb.freedesktop.org/dist/libxcb-${LIBXCB_VERSION}.tar.gz -o xcb.tar.gz && \
    tar -xzf xcb.tar.gz && rm xcb.tar.gz && cd libxcb-${LIBXCB_VERSION} && \
    LDFLAGS="$LDFLAGS -Wl,-rpath,$P" ./configure ${COMMON_CONFIGURE_ARGS} --build=${BUILD} --libdir=$P --disable-dependency-tracking --disable-devel-docs && \
    make -j"$(nproc)" && make install && make install DESTDIR=/output && licence libxcb . && cd .. && rm -rf libxcb-${LIBXCB_VERSION}
# renovate: datasource=gitlab-tags depName=xorg/lib/libxcb-util registryUrl=https://gitlab.freedesktop.org extractVersion=^xcb-util-(?<version>\d+(\.\d+)*)$
ARG XCB_UTIL_VERSION=0.4.1
# renovate: datasource=gitlab-tags depName=xorg/lib/libxcb-keysyms registryUrl=https://gitlab.freedesktop.org extractVersion=^xcb-util-keysyms-(?<version>\d+(\.\d+)*)$
ARG XCB_UTIL_KEYSYMS_VERSION=0.4.1
# renovate: datasource=gitlab-tags depName=xorg/lib/libxcb-wm registryUrl=https://gitlab.freedesktop.org extractVersion=^xcb-util-wm-(?<version>\d+(\.\d+)*)$
ARG XCB_UTIL_WM_VERSION=0.4.2
RUN set -e; for p in xcb-util:${XCB_UTIL_VERSION} xcb-util-keysyms:${XCB_UTIL_KEYSYMS_VERSION} xcb-util-wm:${XCB_UTIL_WM_VERSION}; do \
      n=${p%%:*}; v=${p#*:}; \
      curl --retry-all-errors -fL --retry 5 https://xcb.freedesktop.org/dist/$n-$v.tar.xz -o $n.tar.xz && \
      tar -xf $n.tar.xz && rm $n.tar.xz && cd $n-$v && \
      LDFLAGS="$LDFLAGS -Wl,-rpath,$P" ./configure ${COMMON_CONFIGURE_ARGS} --libdir=$P --disable-static && \
      make -j"$(nproc)" && make install && make install DESTDIR=/output && licence $n . && cd .. && rm -rf $n-$v; \
    done
# renovate: datasource=gitlab-tags depName=xorg/lib/libx11 registryUrl=https://gitlab.freedesktop.org extractVersion=^libX11-(?<version>\d+(\.\d+)*)$
ARG LIBX11_VERSION=1.8.12
# renovate: datasource=gitlab-tags depName=xorg/lib/libxkbfile registryUrl=https://gitlab.freedesktop.org extractVersion=^libxkbfile-(?<version>\d+(\.\d+)*)$
ARG LIBXKBFILE_VERSION=1.1.3
RUN curl --retry-all-errors -fL --retry 5 https://xorg.freedesktop.org/archive/individual/lib/libX11-${LIBX11_VERSION}.tar.xz -o x11.tar.xz && \
    tar -xf x11.tar.xz && rm x11.tar.xz && cd libX11-${LIBX11_VERSION} && \
    LDFLAGS="$LDFLAGS -Wl,-rpath,$P" ./configure ${COMMON_CONFIGURE_ARGS} --libdir=$P --disable-specs --disable-static && \
    make -j"$(nproc)" && make install && make install DESTDIR=/output && licence libX11 . && cd .. && rm -rf libX11-${LIBX11_VERSION} && \
    curl --retry-all-errors -fL --retry 5 https://xorg.freedesktop.org/archive/individual/lib/libxkbfile-${LIBXKBFILE_VERSION}.tar.xz -o xkbfile.tar.xz && \
    tar -xf xkbfile.tar.xz && rm xkbfile.tar.xz && cd libxkbfile-${LIBXKBFILE_VERSION} && \
    LDFLAGS="$LDFLAGS -Wl,-rpath,$P" ./configure ${COMMON_CONFIGURE_ARGS} --libdir=$P --disable-static && \
    make -j"$(nproc)" && make install && make install DESTDIR=/output && licence libxkbfile . && cd .. && rm -rf libxkbfile-${LIBXKBFILE_VERSION}

# libxkbcommon with its X11 part. libxkbcommon.so.0 itself is the image's
# (it stays out of /output: the stand-in copy is for the test stage only);
# libxkbcommon-x11.so.0, which the Wayland desktops do not have, ships.
# renovate: datasource=github-tags depName=xkbcommon/libxkbcommon extractVersion=^xkbcommon-(?<version>\d+(\.\d+)*)$
ARG LIBXKBCOMMON_VERSION=1.13.0
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/xkbcommon/libxkbcommon/archive/refs/tags/xkbcommon-${LIBXKBCOMMON_VERSION}.tar.gz -o xkbc.tar.gz && \
    tar -xf xkbc.tar.gz && rm xkbc.tar.gz && cd libxkbcommon-xkbcommon-${LIBXKBCOMMON_VERSION} && \
    LDFLAGS="$LDFLAGS -Wl,-rpath,$P" meson setup b ${MESON_PRIVATE} -Denable-x11=true -Denable-wayland=false \
      -Denable-xkbregistry=false -Denable-bash-completion=false -Denable-tools=false -Denable-docs=false \
      -Dxkb-config-root=/usr/share/X11/xkb -Dx-locale-root=/usr/share/X11/locale && \
    ninja -C b install && DESTDIR=/xkbc ninja -C b install && \
    mkdir -p /output$P /image-runtime/usr/lib && \
    cp -a /xkbc$P/libxkbcommon-x11.so* /output$P/ && cp -a /xkbc$P/libxkbcommon.so.0* /image-runtime/usr/lib/ && \
    licence libxkbcommon . && rm -rf /xkbc && cd .. && rm -rf libxkbcommon-xkbcommon-${LIBXKBCOMMON_VERSION}

# Cairo with its XCB backend (fcitx5's X11 UI needs cairo-xcb; the Wayland
# desktops' Cairo is built without it), and Pango on it. Same versions as
# the image's.
# renovate: datasource=gitlab-tags depName=cairo/cairo registryUrl=https://gitlab.freedesktop.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG CAIRO_VERSION=1.18.4
# renovate: datasource=gitlab-tags depName=GNOME/pango registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG PANGO_VERSION=1.54.0
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://cairographics.org/releases/cairo-${CAIRO_VERSION}.tar.xz -o cairo.tar.xz && \
    tar -xf cairo.tar.xz && rm cairo.tar.xz && cd cairo-${CAIRO_VERSION} && \
    LDFLAGS="$LDFLAGS -Wl,-rpath,$P" meson setup b ${MESON_PRIVATE} -Dtests=disabled -Dglib=enabled -Dspectre=disabled \
      -Dfreetype=enabled -Dfontconfig=enabled -Dpng=enabled -Dxlib=disabled -Dxcb=enabled && \
    ninja -C b install && DESTDIR=/output ninja -C b install && licence cairo . && cd .. && rm -rf cairo-${CAIRO_VERSION} && \
    pkg-config --exists cairo-xcb && \
    curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.gnome.org/sources/pango/${PANGO_VERSION%.*}/pango-${PANGO_VERSION}.tar.xz -o pango.tar.xz && \
    tar -xf pango.tar.xz && rm pango.tar.xz && cd pango-${PANGO_VERSION} && \
    LDFLAGS="$LDFLAGS -Wl,-rpath,$P" meson setup b ${MESON_PRIVATE} -Dintrospection=disabled -Dgtk_doc=false -Dfontconfig=enabled -Dfreetype=enabled -Dcairo=enabled && \
    ninja -C b install && DESTDIR=/output ninja -C b install && licence pango . && cd .. && rm -rf pango-${PANGO_VERSION}

# librsvg (Rust, through cargo-c): fcitx5 5.1.22 and later need it for the
# classic UI. Built against Hadron's musl with crt-static off and with the
# private RUNPATH. Unlike the root Dockerfile's mesa-rust stage, the Rust
# image's self-contained libc.a stays: cargo-c builds with an explicit
# target, so the build scripts link statically against it (without it they
# fail with "cannot find -lc"); the library itself links Hadron's libc.so,
# which the last check below proves.
# renovate: datasource=github-releases depName=lu-zero/cargo-c extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG CARGO_C_VERSION=0.10.28
# renovate: datasource=gitlab-tags depName=GNOME/librsvg registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG LIBRSVG_VERSION=2.63.4
COPY --from=rust-toolchain /usr/local/cargo/ /usr/local/cargo/
COPY --from=rust-toolchain /usr/local/rustup/ /usr/local/rustup/
RUN set -eu; export CARGO_HOME=/usr/local/cargo RUSTUP_HOME=/usr/local/rustup PATH=/usr/local/cargo/bin:$PATH \
      RUSTFLAGS="-C target-feature=-crt-static -C link-arg=-Wl,-rpath,$P" CARGO_INCREMENTAL=0; \
    curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/lu-zero/cargo-c/releases/download/v${CARGO_C_VERSION}/cargo-c-x86_64-unknown-linux-musl.tar.gz -o cargo-c.tar.gz; \
    tar -xzf cargo-c.tar.gz -C /usr/local/cargo/bin && rm cargo-c.tar.gz; \
    curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.gnome.org/sources/librsvg/${LIBRSVG_VERSION%.*}/librsvg-${LIBRSVG_VERSION}.tar.xz -o rsvg.tar.xz; \
    tar -xf rsvg.tar.xz && rm rsvg.tar.xz && cd librsvg-${LIBRSVG_VERSION}; \
    LDFLAGS="$LDFLAGS -Wl,-rpath,$P" meson setup b ${MESON_PRIVATE} -Dintrospection=disabled -Dpixbuf=enabled \
      -Dpixbuf-loader=disabled -Drsvg-convert=disabled -Ddocs=disabled -Dvala=disabled -Dtests=false -Davif=disabled; \
    ninja -C b install && DESTDIR=/output ninja -C b install && licence librsvg .; \
    cd .. && rm -rf librsvg-${LIBRSVG_VERSION} /usr/local/cargo/registry /usr/local/cargo/git; \
    pkg-config --exists librsvg-2.0; readelf -d $P/librsvg-2.so.2 | grep -q 'NEEDED.*\[libc\.so\]'

# xcb-imdkit: fcitx5's XIM server.
# renovate: datasource=github-tags depName=fcitx/xcb-imdkit extractVersion=^(?<version>\d+(\.\d+)*)$
ARG XCB_IMDKIT_VERSION=1.0.9
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.fcitx-im.org/fcitx5/xcb-imdkit/xcb-imdkit-${XCB_IMDKIT_VERSION}.tar.zst -o imdkit.tar.zst && \
    zstd -dc imdkit.tar.zst | tar -x && rm imdkit.tar.zst && cd xcb-imdkit-${XCB_IMDKIT_VERSION} && \
    cmake ${CMAKE_PRIVATE} -B b -DUSE_SYSTEM_UTHASH=OFF && cmake --build b && \
    cmake --install b --strip && DESTDIR=/output cmake --install b --strip && licence xcb-imdkit . && cd .. && rm -rf xcb-imdkit-${XCB_IMDKIT_VERSION}

# The ISO 639-3 language names fcitx5's keyboard engine reads (one file of
# iso-codes, in a directory of the extension's own).
# renovate: datasource=gitlab-tags depName=iso-codes-team/iso-codes registryUrl=https://salsa.debian.org extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG ISO_CODES_VERSION=4.20.1
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://salsa.debian.org/iso-codes-team/iso-codes/-/archive/v${ISO_CODES_VERSION}/iso-codes-v${ISO_CODES_VERSION}.tar.gz -o iso.tar.gz && \
    tar -xzf iso.tar.gz && rm iso.tar.gz && cd iso-codes-v${ISO_CODES_VERSION} && \
    install -Dm644 data/iso_639-3.json /usr/share/isotope-ext/fcitx5/iso-codes/json/iso_639-3.json && \
    install -Dm644 data/iso_639-3.json /output/usr/share/isotope-ext/fcitx5/iso-codes/json/iso_639-3.json && \
    licence iso-codes . && cd .. && rm -rf iso-codes-v${ISO_CODES_VERSION}

# ---------------------------------------------------------------------------
# fcitx5 and the engines, from the upstream release tarballs (the _dict ones
# carry the data the build would otherwise download). The four are one
# Renovate group: fcitx5-chinese-addons names the fcitx5 release it needs.

# fcitx5: sd-bus and sd-event (Hadron's libsystemd), no enchant, no libuuid,
# no autostart file (/etc is not merged; the session starts it), Plasma's
# protocols from its own copy. The testing addons are built because the
# engines' CMake files ask for them, and left out of the extension below.
# renovate: datasource=github-tags depName=fcitx/fcitx5 extractVersion=^(?<version>\d+(\.\d+)*)$
ARG FCITX5_VERSION=5.1.23
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.fcitx-im.org/fcitx5/fcitx5/fcitx5-${FCITX5_VERSION}_dict.tar.zst -o fcitx5.tar.zst && \
    zstd -dc fcitx5.tar.zst | tar -x && rm fcitx5.tar.zst && cd fcitx5-${FCITX5_VERSION} && \
    cmake ${CMAKE_PRIVATE} -B b -DENABLE_TEST=OFF -DENABLE_COVERAGE=OFF -DENABLE_DOC=OFF \
      -DENABLE_ENCHANT=OFF -DENABLE_LIBUUID=OFF -DENABLE_XDGAUTOSTART=OFF \
      -DUSE_SYSTEM_PLASMA_WAYLAND_PROTOCOLS=OFF -DEVENT_LOOP_BACKEND=systemd \
      -DISOCODES_ISO639_JSON=/usr/share/isotope-ext/fcitx5/iso-codes/json/iso_639-3.json \
      -DXKEYBOARDCONFIG_XKBBASE=/usr/share/X11/xkb -DXKEYBOARDCONFIG_DATADIR=/usr/share && \
    cmake --build b && cmake --install b --strip && DESTDIR=/output cmake --install b --strip && \
    licence fcitx5 . && licence yoga third_party/yoga && cd .. && rm -rf fcitx5-${FCITX5_VERSION} && \
    test -x /usr/bin/fcitx5 && test -f $P/cmake/Fcitx5Core/Fcitx5CoreConfig.cmake

# libime (the Pinyin and table engines' library, its language model and
# dictionaries). Its tools build the data, then leave the extension.
# renovate: datasource=github-tags depName=fcitx/libime extractVersion=^(?<version>\d+(\.\d+)*)$
ARG LIBIME_VERSION=1.1.17
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.fcitx-im.org/fcitx5/libime/libime-${LIBIME_VERSION}_dict.tar.zst -o libime.tar.zst && \
    zstd -dc libime.tar.zst | tar -x && rm libime.tar.zst && cd libime-${LIBIME_VERSION} && \
    cmake ${CMAKE_PRIVATE} -B b -DENABLE_TEST=OFF -DENABLE_DOC=OFF -DENABLE_DATA=ON -DENABLE_TOOLS=ON && \
    cmake --build b && cmake --install b --strip && DESTDIR=/output cmake --install b --strip && \
    licence libime . && cd .. && rm -rf libime-${LIBIME_VERSION} && \
    test -n "$(ls -d $P/cmake/LibIME*)"

# renovate: datasource=github-tags depName=fcitx/fcitx5-chinese-addons extractVersion=^(?<version>\d+(\.\d+)*)$
ARG FCITX5_CHINESE_ADDONS_VERSION=5.1.15
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.fcitx-im.org/fcitx5/fcitx5-chinese-addons/fcitx5-chinese-addons-${FCITX5_CHINESE_ADDONS_VERSION}_dict.tar.zst -o ca.tar.zst && \
    zstd -dc ca.tar.zst | tar -x && rm ca.tar.zst && cd fcitx5-chinese-addons-${FCITX5_CHINESE_ADDONS_VERSION} && \
    cmake ${CMAKE_PRIVATE} -B b -DENABLE_TEST=OFF -DENABLE_GUI=OFF -DENABLE_BROWSER=OFF \
      -DENABLE_CLOUDPINYIN=OFF -DENABLE_OPENCC=OFF -DENABLE_TOOLS=OFF -DENABLE_DATA=ON && \
    cmake --build b && cmake --install b --strip && DESTDIR=/output cmake --install b --strip && \
    licence fcitx5-chinese-addons . && cd .. && rm -rf fcitx5-chinese-addons-${FCITX5_CHINESE_ADDONS_VERSION}

# Korean: libhangul and fcitx5-hangul.
# renovate: datasource=github-releases depName=libhangul/libhangul extractVersion=^libhangul-(?<version>\d+(\.\d+)*)$
ARG LIBHANGUL_VERSION=0.2.0
# renovate: datasource=github-tags depName=fcitx/fcitx5-hangul extractVersion=^(?<version>\d+(\.\d+)*)$
ARG FCITX5_HANGUL_VERSION=5.1.11
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/libhangul/libhangul/releases/download/libhangul-${LIBHANGUL_VERSION}/libhangul-${LIBHANGUL_VERSION}.tar.gz -o hangul.tar.gz && \
    tar -xzf hangul.tar.gz && rm hangul.tar.gz && cd libhangul-${LIBHANGUL_VERSION} && \
    { [ -x configure ] || autoreconf -fi; } && \
    LDFLAGS="$LDFLAGS -Wl,-rpath,$P" ./configure ${COMMON_CONFIGURE_ARGS} --libdir=$P --disable-dependency-tracking && \
    make -j"$(nproc)" && make install && make install DESTDIR=/output && licence libhangul . && cd .. && rm -rf libhangul-${LIBHANGUL_VERSION} && \
    curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.fcitx-im.org/fcitx5/fcitx5-hangul/fcitx5-hangul-${FCITX5_HANGUL_VERSION}.tar.zst -o fh.tar.zst && \
    zstd -dc fh.tar.zst | tar -x && rm fh.tar.zst && cd fcitx5-hangul-${FCITX5_HANGUL_VERSION} && \
    cmake ${CMAKE_PRIVATE} -B b -DENABLE_TEST=OFF && cmake --build b && \
    cmake --install b --strip && DESTDIR=/output cmake --install b --strip && licence fcitx5-hangul . && cd .. && rm -rf fcitx5-hangul-${FCITX5_HANGUL_VERSION}

# Japanese: anthy-unicode and fcitx5-anthy. anthy reads its dictionary path
# from a configuration file in its sysconfdir, which is the extension's own
# directory, since /etc is not merged. (Mozc needs Bazel and is not here.)
# renovate: datasource=github-releases depName=fujiwarat/anthy-unicode
ARG ANTHY_UNICODE_VERSION=1.0.0.20260213
# renovate: datasource=github-tags depName=fcitx/fcitx5-anthy extractVersion=^(?<version>\d+(\.\d+)*)$
ARG FCITX5_ANTHY_VERSION=5.1.11
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/fujiwarat/anthy-unicode/releases/download/${ANTHY_UNICODE_VERSION}/anthy-unicode-${ANTHY_UNICODE_VERSION}.tar.gz -o anthy.tar.gz && \
    tar -xzf anthy.tar.gz && rm anthy.tar.gz && cd anthy-unicode-${ANTHY_UNICODE_VERSION} && \
    { [ -x configure ] || autoreconf -fi; } && \
    LDFLAGS="$LDFLAGS -Wl,-rpath,$P" ./configure ${COMMON_CONFIGURE_ARGS} --libdir=$P --sysconfdir=$P/etc --disable-dependency-tracking && \
    make -j1 && make install && make install DESTDIR=/output && licence anthy-unicode . && cd .. && rm -rf anthy-unicode-${ANTHY_UNICODE_VERSION} && \
    curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.fcitx-im.org/fcitx5/fcitx5-anthy/fcitx5-anthy-${FCITX5_ANTHY_VERSION}.tar.zst -o fa.tar.zst && \
    zstd -dc fa.tar.zst | tar -x && rm fa.tar.zst && cd fcitx5-anthy-${FCITX5_ANTHY_VERSION} && \
    cmake ${CMAKE_PRIVATE} -B b -DENABLE_TEST=OFF && cmake --build b && \
    cmake --install b --strip && DESTDIR=/output cmake --install b --strip && licence fcitx5-anthy . && cd .. && rm -rf fcitx5-anthy-${FCITX5_ANTHY_VERSION}

# What a system extension must not carry: /etc, headers, pkg-config and
# CMake files, static and libtool archives, man pages and docs, libime's
# and libhangul's command-line tools, X11's locale and error databases (i3
# has its own at the same paths), fcitx5's testing addons (the modules and
# their addon files in share/fcitx5/testing) and fcitx5-configtool with its
# launcher entry (a wrapper that only starts a Qt or KDE configuration tool,
# and none is here). libxcb-xtest is libxcb's XTest library, not a test. Then the
# stand-in of the image's libraries for the test stage.
RUN set -eux; \
    rm -rf /output/etc /output/usr/include /output/usr/share/man /output/usr/share/doc \
      /output/usr/share/gtk-doc /output/usr/share/aclocal /output/usr/share/pkgconfig /output/usr/share/X11 \
      /output/usr/share/emacs /output/usr/share/bash-completion \
      /output$P/pkgconfig /output$P/cmake /output$P/X11/Xcms.txt; \
    rm -f /output/usr/bin/libime_* /output/usr/bin/hangul /output/usr/bin/cairo-trace /output/usr/bin/pango-*; \
    rm -rf /output$P/cairo; \
    find /output -name '*.la' -delete -o -name '*.a' -delete; \
    rm -f /output$P/fcitx5/*test*.so /output/usr/share/fcitx5/addon/test*.conf; \
    rm -rf /output/usr/share/fcitx5/testing; \
    rm -f /output/usr/bin/fcitx5-configtool /output/usr/share/applications/fcitx5-configtool.desktop; \
    if find /output$P/fcitx5 /output/usr/share/fcitx5 -name '*test*' | grep .; then echo "testing addons are left" >&2; exit 1; fi; \
    mkdir -p /image-runtime/usr/lib /image-runtime/usr/bin; \
    grep -v -e '^#' -e '^$' -e '^libc.so$' /image-libs.txt | while read -r lib; do \
      cp -a /usr/lib/$lib* /image-runtime/usr/lib/ 2>/dev/null || [ -e /image-runtime/usr/lib/$lib ] || { echo "no $lib to stand in for the image" >&2; exit 1; }; \
    done; \
    cp -a /usr/lib/libpcre2-8.so.0* /usr/lib/libffi.so.* /image-runtime/usr/lib/; \
    cp -a /usr/bin/gdbus /image-runtime/usr/bin/; \
    mkdir -p /image-runtime/etc && cp -a /etc/fonts /image-runtime/etc/ 2>/dev/null || true; \
    mkdir -p /image-runtime/usr/share && cp -a /usr/share/glib-2.0 /image-runtime/usr/share/ 2>/dev/null || true

# Every ELF file needs only the libraries in image-libs.txt and the
# extension's own, and carries the RUNPATH that finds them.
RUN set -eu; \
    allowed=" $(grep -v -e '^#' -e '^$' /image-libs.txt | tr '\n' ' ') "; \
    bad=; n=0; \
    for f in $(find /output -type f); do \
      [ "$(head -c 4 "$f" | od -An -c | tr -d ' ')" = '177ELF' ] || continue; n=$((n+1)); \
      for lib in $(readelf -d "$f" | sed -n 's/.*(NEEDED).*\[\(.*\)\]/\1/p'); do \
        case "$allowed" in *" $lib "*) continue ;; esac; \
        [ -e "/output$P/$lib" ] && continue; \
        bad="$bad $f:$lib"; \
      done; \
    done; \
    echo "$n ELF files"; [ "$n" -gt 30 ]; \
    if [ -n "$bad" ]; then echo "libraries neither the image nor the extension has:$bad" >&2; exit 1; fi; \
    for g in /output/usr/bin/fcitx5 '/output$P/libFcitx5Core.so.*' '/output$P/fcitx5/*classicui*.so' \
             '/output$P/fcitx5/*pinyin.so' '/output$P/fcitx5/*anthy*.so' '/output$P/fcitx5/*hangul*.so' \
             '/output$P/librsvg-2.so.*' '/output$P/libcairo.so.*'; do \
      g=$(eval echo "$g"); found=; \
      for f in $g; do [ -f "$f" ] && [ ! -L "$f" ] || continue; found=1; \
        readelf -d "$f" | grep -E 'R(UN)?PATH' | grep -q "$P" || { echo "$f has no RUNPATH to $P" >&2; exit 1; }; \
      done; \
      [ -n "$found" ] || { echo "nothing matches $g" >&2; exit 1; }; \
    done; \
    other=$(ls /output/usr/bin | grep -v -E '^(fcitx5.*|anthy-.*unicode.*)$' || true); \
    if [ -n "$other" ]; then echo "programs that are not fcitx5's or anthy's (the image may have them): $other" >&2; exit 1; fi; \
    other=$(ls /output/usr/lib | grep -v -x isotope-ext || true); \
    if [ -n "$other" ]; then echo "files in /usr/lib outside isotope-ext/fcitx5: $other" >&2; exit 1; fi; \
    test -f /output$P/etc/anthy-unicode.conf; \
    test -f /output/usr/share/fcitx5/inputmethod/pinyin.conf; \
    test -f /output/usr/share/fcitx5/inputmethod/anthy.conf; \
    test -f /output/usr/share/fcitx5/inputmethod/hangul.conf; \
    ls /output/usr/share/licenses/fcitx5-extension

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
# The image's libraries (image-libs.txt), GLib's gdbus and the keyboard
# layouts, without replacing a file of the base.
COPY --from=build /image-runtime/ /image-runtime/
RUN cp -an /image-runtime/. / && rm -rf /image-runtime
RUN cp -a /extension/. / && rm -rf /extension
# Every library each ELF file needs is found, and every symbol: fcitx5's
# addons link libFcitx5Core themselves, so nothing is left for the program
# that loads them to provide.
RUN set -eu; while read -r f; do \
      ldd "$f" >/tmp/ldd 2>&1 || { echo "ldd $f failed" >&2; cat /tmp/ldd >&2; exit 1; }; \
      if grep -qE 'not found|Error' /tmp/ldd; then echo "$f:" >&2; cat /tmp/ldd >&2; exit 1; fi; \
    done </tmp/elf
# anthy finds its dictionary through its own configuration file.
RUN set -eux; \
    conf=/usr/lib/isotope-ext/fcitx5/etc/anthy-unicode.conf; cat $conf; \
    dic=$(sed -n 's/^DIC_FILE[[:space:]]\{1,\}//p' $conf); test -s "$dic"; \
    test -s /usr/share/isotope-ext/fcitx5/iso-codes/json/iso_639-3.json; \
    test -n "$(ls /usr/share/libime)"; test -n "$(ls /usr/lib/isotope-ext/fcitx5/libime)"; \
    test -n "$(ls /usr/share/libhangul)"
# fcitx5 on a session bus with no display (its Wayland and X11 frontends
# find none and wait): it owns org.fcitx.Fcitx5, and its controller lists
# the keyboard and the three engines. fcitx5's log is printed on failure.
RUN set -u; \
    fail() { echo "FAIL: $*" >&2; echo "--- fcitx5's log:" >&2; cat /tmp/fcitx5.log >&2 2>/dev/null || true; exit 1; }; \
    export HOME=/tmp/home XDG_RUNTIME_DIR=/tmp/run LANG=C.UTF-8; mkdir -p $HOME $XDG_RUNTIME_DIR; chmod 700 $XDG_RUNTIME_DIR; \
    DBUS_SESSION_BUS_ADDRESS=$(dbus-daemon --session --fork --print-address=1) || fail "no session bus"; \
    export DBUS_SESSION_BUS_ADDRESS; \
    fcitx5 --verbose='*=4' >/tmp/fcitx5.log 2>&1 & pid=$!; \
    i=0; until gdbus call --session --dest org.freedesktop.DBus --object-path /org/freedesktop/DBus \
        --method org.freedesktop.DBus.NameHasOwner org.fcitx.Fcitx5 2>/dev/null | grep -q true; do \
      kill -0 "$pid" 2>/dev/null || fail "fcitx5 exited"; \
      i=$((i+1)); [ "$i" -le 120 ] || fail "fcitx5 did not own its name in 60 s"; sleep 0.5; \
    done; \
    gdbus call --session --dest org.fcitx.Fcitx5 --object-path /controller \
      --method org.fcitx.Fcitx.Controller1.AvailableInputMethods >/tmp/ims 2>&1 || { cat /tmp/ims; fail "AvailableInputMethods"; }; \
    for im in keyboard-us pinyin shuangpin anthy hangul; do \
      grep -q "'$im'" /tmp/ims || { cat /tmp/ims; fail "fcitx5 does not list $im"; }; \
    done; \
    gdbus call --session --dest org.fcitx.Fcitx5 --object-path /controller \
      --method org.fcitx.Fcitx.Controller1.GetAddons >/tmp/addons 2>&1 || fail "GetAddons"; \
    for a in classicui wayland waylandim xcb xim dbusfrontend pinyin anthy hangul notificationitem; do \
      grep -q "'$a'" /tmp/addons || { cat /tmp/addons; fail "fcitx5 has no $a addon"; }; \
    done; \
    kill "$pid"; wait "$pid" 2>/dev/null || true; \
    echo "fcitx5 runs and lists keyboard-us, pinyin, shuangpin, anthy and hangul"

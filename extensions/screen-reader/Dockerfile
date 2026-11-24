# The Orca screen reader with speech, as an Isotope system extension. See
# extensions/README.md for how an extension is built and published, and
# docs/superpowers/specs/2026-11-24-screen-reader-design.md.
#
# Orca is Python over GObject Introspection, and the image has neither
# Python, PyGObject nor a typelib. So this extension carries, all in
# /usr/lib/isotope-ext/screen-reader (the prefix P below; Kairos does not
# merge /usr/libexec), behind a RUNPATH:
#   - CPython (shared, with its standard library precompiled), PyGObject and
#     gobject-introspection's libgirepository and typelibs (GLib, GObject,
#     Gio, GModule, cairo, freetype2, xlib, ...);
#   - libatspi from at-spi2-core 2.58 with its Atspi and Atk typelibs and
#     its Python overrides: Orca grabs the keyboard on Wayland through
#     libatspi's a11y-manager device (org.freedesktop.a11y.KeyboardMonitor,
#     which cosmic-comp and niri serve), added in at-spi2-core 2.56; the
#     image's at-spi2-core (2.54, the a11y bus) stays the bus;
#   - typelibs for GTK 3, GDK 3, Pango, PangoCairo, GdkPixbuf and HarfBuzz,
#     compiled from gtk-rs/gir-files (GIR files generated from those
#     libraries' releases): GTK and its stack are the image's own libraries,
#     so this extension does not build them, only describes them;
#   - dasbus (Orca's D-Bus service), speech-dispatcher with its espeak-ng
#     output module and PipeWire audio, its Python client (speechd),
#     eSpeak NG with all its languages, and libsndfile;
#   - Orca itself, with /usr/bin/orca (what cosmic-session starts) and
#     orca.service (what isotope-screen-reader starts on niri).
# What it takes from the image is listed in image-libs.txt; the build stage
# fails if an ELF file needs any other library that is not its own.

# Set by .github/workflows/extensions.yml from scripts/bump-hadron.sh, so the
# extension builds on the same Hadron release as the image.
ARG HADRON_VERSION

FROM ghcr.io/kairos-io/hadron-toolchain:${HADRON_VERSION} AS build
ENV P=/usr/lib/isotope-ext/screen-reader
ENV PKG_CONFIG_PATH=/usr/lib/isotope-ext/screen-reader/lib/pkgconfig:/usr/lib/isotope-ext/screen-reader/share/pkgconfig
RUN pip3 install meson ninja packaging
WORKDIR /build

# Build-only dependencies, installed into this stage's root and never into
# /output: libucontext (the toolchain's libsystemd links it), PCRE2 and GLib
# at the versions the image builds (the image's own copy is what runs; same
# versions and Renovate rules as the Dockerfile at the root, so one Renovate
# PR moves both), and gettext's msgfmt and xgettext, which Orca,
# at-spi2-core and speech-dispatcher's configure need. gettext goes to
# /opt/gettext, off the default library path, so no build links its libintl
# (musl has gettext in its libc).
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
RUN for m in https://mirrors.kernel.org/gnu https://ftpmirror.gnu.org https://ftp.gnu.org/gnu; do curl -fL --connect-timeout 30 --retry 3 --retry-delay 3 --retry-all-errors "$m/gettext/gettext-${GETTEXT_VERSION}.tar.xz" -o gt.tar.xz && break; rm -f gt.tar.xz; done && \
    test -s gt.tar.xz && tar -xf gt.tar.xz && rm gt.tar.xz && cd gettext-${GETTEXT_VERSION} && \
    ./configure --prefix=/opt/gettext --disable-java --disable-csharp --disable-libasprintf \
      --disable-openmp --disable-acl --disable-curses --without-emacs --without-git \
      --disable-static --enable-shared && \
    make -j"$(nproc)" && make install && cd .. && rm -rf gettext-${GETTEXT_VERSION} && \
    /opt/gettext/bin/msgfmt --version >/dev/null
ENV PATH=/usr/lib/isotope-ext/screen-reader/bin:$PATH:/opt/gettext/bin

# Everything below lands in P, and every library and program there finds the
# others through this RUNPATH.
ENV LDFLAGS="-Wl,-rpath,/usr/lib/isotope-ext/screen-reader/lib"
ENV MESON_PRIVATE="--prefix=/usr/lib/isotope-ext/screen-reader --libdir=lib --libexecdir=lib --buildtype=minsize -Dstrip=true"

# CPython 3.12 (PyGObject 3.48 below is tested up to 3.12), shared, with
# pip only to install setuptools for gobject-introspection's scanner; both
# are left out of the extension. -O2 without the toolchain's LTO flags.
# renovate: datasource=github-tags depName=python/cpython extractVersion=^v(?<version>3\.12\.\d+)$
ARG PYTHON_VERSION=3.12.16
# renovate: datasource=pypi depName=setuptools
ARG SETUPTOOLS_VERSION=75.8.0
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://www.python.org/ftp/python/${PYTHON_VERSION}/Python-${PYTHON_VERSION}.tar.xz -o py.tar.xz && \
    tar -xf py.tar.xz && rm py.tar.xz && mv Python-${PYTHON_VERSION} python-src && cd python-src && \
    ./configure --prefix=$P --enable-shared --without-static-libpython --with-ensurepip=install \
      --disable-test-modules CFLAGS="-O2 -pipe" LDFLAGS="$LDFLAGS" && \
    make -j"$(nproc)" && make install && \
    $P/bin/python3 -m pip install --no-cache-dir "setuptools==${SETUPTOOLS_VERSION}" && \
    $P/bin/python3 -c 'import ctypes, zlib, pyexpat, json; print("python ok")'

# gobject-introspection 1.82, the series that matches the image's GLib 2.82:
# libgirepository-1.0 (what PyGObject 3.48 loads typelibs with), the
# scanner at-spi2-core needs, the compiler for the GIR files, and the
# typelibs of GLib, GObject, Gio, GModule and the fundamental ones. With a
# GLib of 2.80 or later, gobject-introspection builds GLib's GIR files and
# typelibs but leaves installing them to GLib, which the image builds
# without introspection: they are installed from its build tree here.
# renovate: datasource=gitlab-tags depName=GNOME/gobject-introspection registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>1\.82\.\d+)$
ARG GOBJECT_INTROSPECTION_VERSION=1.82.0
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.gnome.org/sources/gobject-introspection/${GOBJECT_INTROSPECTION_VERSION%.*}/gobject-introspection-${GOBJECT_INTROSPECTION_VERSION}.tar.xz -o gi.tar.xz && \
    tar -xf gi.tar.xz && rm gi.tar.xz && mv gobject-introspection-${GOBJECT_INTROSPECTION_VERSION} gi-src && cd gi-src && \
    meson setup buildDir ${MESON_PRIVATE} -Dpython=$P/bin/python3 -Dcairo=disabled -Ddoctool=disabled -Dgtk_doc=false && \
    ninja -C buildDir install && \
    for g in GLib-2.0 GObject-2.0 GModule-2.0 Gio-2.0; do \
      install -Dm644 buildDir/gir/$g.gir $P/share/gir-1.0/$g.gir && \
      install -Dm644 buildDir/gir/$g.typelib $P/lib/girepository-1.0/$g.typelib || exit 1; \
    done && \
    for t in GLib-2.0 GObject-2.0 Gio-2.0 GModule-2.0 cairo-1.0 freetype2-2.0 xlib-2.0; do \
      test -s $P/lib/girepository-1.0/$t.typelib || { echo "no $t.typelib" >&2; exit 1; }; done

# at-spi2-core 2.58: libatspi and the Atspi and Atk typelibs. No X11 (the
# a11y-manager device is what Orca uses on Wayland), no systemd. Its bus
# launcher, registry, ATK and bridge are built but not shipped: the image's
# are the ones that run.
# renovate: datasource=gitlab-tags depName=GNOME/at-spi2-core registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG ATSPI_VERSION=2.58.9
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.gnome.org/sources/at-spi2-core/${ATSPI_VERSION%.*}/at-spi2-core-${ATSPI_VERSION}.tar.xz -o a.tar.xz && \
    tar -xf a.tar.xz && rm a.tar.xz && mv at-spi2-core-${ATSPI_VERSION} atspi-src && cd atspi-src && \
    meson setup buildDir ${MESON_PRIVATE} -Dintrospection=enabled -Dx11=disabled -Duse_systemd=false \
      -Ddocs=false -Dgtk2_atk_adaptor=false -Ddefault_bus=dbus-daemon -Ddbus_daemon=/usr/bin/dbus-daemon \
      -Dpython=$P/bin/python3 && \
    ninja -C buildDir install && \
    test -s $P/lib/girepository-1.0/Atspi-2.0.typelib && test -s $P/lib/girepository-1.0/Atk-1.0.typelib && \
    grep -q a11y_manager $P/lib/libatspi.so.0 || { echo "libatspi has no a11y-manager device" >&2; exit 1; }

# PyGObject 3.48, the last series on libgirepository-1.0. No pycairo: Orca
# uses cairo only for the optional on-screen highlight.
# renovate: datasource=gitlab-tags depName=GNOME/pygobject registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>3\.48\.\d+)$
ARG PYGOBJECT_VERSION=3.48.2
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.gnome.org/sources/pygobject/${PYGOBJECT_VERSION%.*}/pygobject-${PYGOBJECT_VERSION}.tar.xz -o pg.tar.xz && \
    tar -xf pg.tar.xz && rm pg.tar.xz && mv pygobject-${PYGOBJECT_VERSION} pygobject-src && cd pygobject-src && \
    meson setup buildDir ${MESON_PRIVATE} -Dpython=$P/bin/python3 -Dpycairo=disabled -Dtests=false && \
    ninja -C buildDir install && \
    $P/bin/python3 -c 'import gi; gi.require_version("Atspi", "2.0"); from gi.repository import GLib, Gio, Atspi; print("gi ok", GLib.MAJOR_VERSION, GLib.MINOR_VERSION)'

# The GTK stack's typelibs, compiled from the GIR files gtk-rs keeps (one
# tag: GTK 3.24, GDK 3, Pango, PangoCairo, GdkPixbuf, HarfBuzz). Each names
# its library by soname, which the image has (image-libs.txt). A function a
# newer release added and the image's library lacks only fails when called.
# Newer scanners write the basic type gconstpointer, which this
# g-ir-compiler (1.82) does not know: it is gpointer in a typelib (the C
# type, kept in c:type, only adds const).
# renovate: datasource=github-tags depName=gtk-rs/gir-files
ARG GIR_FILES_VERSION=0.22.6
RUN mkdir -p /build/gir && cd /build/gir && \
    for g in HarfBuzz-0.0 GdkPixbuf-2.0 Pango-1.0 PangoCairo-1.0 Gdk-3.0 Gtk-3.0; do \
      curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://raw.githubusercontent.com/gtk-rs/gir-files/${GIR_FILES_VERSION}/$g.gir -o $g.gir && \
      sed -i 's/ name="gconstpointer"/ name="gpointer"/g' $g.gir && \
      g-ir-compiler --includedir=/build/gir --includedir=$P/share/gir-1.0 $g.gir -o $P/lib/girepository-1.0/$g.typelib || exit 1; \
    done && \
    $P/bin/python3 -c 'import gi; from gi.repository import GIRepository as R; r = R.Repository.get_default(); [r.require(n, v, 0) for n, v in (("Gtk", "3.0"), ("Gdk", "3.0"), ("Pango", "1.0"), ("GdkPixbuf", "2.0"), ("Atk", "1.0"))]; print("typelibs ok", r.get_shared_library("Gtk"))'

# dasbus: Orca's D-Bus service. Pure Python, copied as a package.
# renovate: datasource=github-tags depName=dasbus-project/dasbus extractVersion=^v(?<version>.*)$
ARG DASBUS_VERSION=1.7
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/dasbus-project/dasbus/archive/refs/tags/v${DASBUS_VERSION}.tar.gz -o d.tar.gz && \
    tar -xzf d.tar.gz && rm d.tar.gz && mv dasbus-${DASBUS_VERSION} dasbus-src && \
    cp -r dasbus-src/dasbus $P/lib/python3.12/site-packages/dasbus && \
    $P/bin/python3 -c 'import dasbus.connection'

# libsndfile, which speech-dispatcher needs (sound icons). Shared, in P: the
# image has it only on COSMIC. Same version and options as the image's.
# renovate: datasource=github-tags depName=libsndfile/libsndfile extractVersion=^(?<version>\d+(\.\d+)*)$
ARG LIBSNDFILE_VERSION=1.2.2
RUN curl --retry-all-errors -fL --retry 5 https://github.com/libsndfile/libsndfile/releases/download/${LIBSNDFILE_VERSION}/libsndfile-${LIBSNDFILE_VERSION}.tar.xz -o s.tar.xz && \
    tar -xf s.tar.xz && rm s.tar.xz && mv libsndfile-${LIBSNDFILE_VERSION} sndfile-src && cd sndfile-src && \
    cmake -B build -G Ninja -DCMAKE_INSTALL_PREFIX=$P -DCMAKE_INSTALL_LIBDIR=lib -DCMAKE_BUILD_TYPE=MinSizeRel \
      -DCMAKE_POLICY_VERSION_MINIMUM=3.5 -DBUILD_SHARED_LIBS=ON -DENABLE_EXTERNAL_LIBS=OFF -DENABLE_MPEG=OFF \
      -DBUILD_PROGRAMS=OFF -DBUILD_EXAMPLES=OFF -DBUILD_TESTING=OFF -DENABLE_CPACK=OFF && \
    cmake --build build && cmake --install build

# dotconf, speech-dispatcher's configuration parser: static, build-only.
# renovate: datasource=github-tags depName=williamh/dotconf extractVersion=^v(?<version>.*)$
ARG DOTCONF_VERSION=1.4.1
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/williamh/dotconf/archive/refs/tags/v${DOTCONF_VERSION}.tar.gz -o dc.tar.gz && \
    tar -xzf dc.tar.gz && rm dc.tar.gz && mv dotconf-${DOTCONF_VERSION} dotconf-src && cd dotconf-src && \
    autoreconf -fi && \
    ./configure --prefix=/usr --libdir=/usr/lib --disable-shared --enable-static --with-pic CFLAGS="-O2 -fPIC" LDFLAGS= && \
    make -j"$(nproc)" -C src && make -C src install && install -Dm644 dotconf.pc /usr/lib/pkgconfig/dotconf.pc && \
    test -f /usr/lib/libdotconf.a

# libpipewire, build-only, for speech-dispatcher's PipeWire output: the
# image's own libpipewire-0.3.so.0 (every desktop has it) is what runs. Same
# version as the image; only the client library, every plugin off.
# renovate: datasource=gitlab-tags depName=pipewire/pipewire registryUrl=https://gitlab.freedesktop.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG PIPEWIRE_VERSION=1.2.7
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://gitlab.freedesktop.org/pipewire/pipewire/-/archive/${PIPEWIRE_VERSION}/pipewire-${PIPEWIRE_VERSION}.tar.gz -o pw.tar.gz && \
    tar -xzf pw.tar.gz && rm pw.tar.gz && mv pipewire-${PIPEWIRE_VERSION} pipewire-src && cd pipewire-src && \
    meson setup buildDir ${COMMON_MESON_FLAGS} --auto-features=disabled \
      -Dexamples=disabled -Dtests=disabled -Dsystemd-user-service=disabled -Dpipewire-jack=disabled \
      -Dpipewire-v4l2=disabled -Dspa-plugins=disabled -Daudiomixer=disabled -Daudioconvert=disabled \
      -Dcontrol=disabled -Daudiotestsrc=disabled -Dsupport=disabled -Ddbus=disabled \
      -Dvideoconvert=disabled -Dvideotestsrc=disabled -Dflatpak=disabled \
      -Dsession-managers=[] -Dlegacy-rtkit=false && \
    ninja -C buildDir install && pkg-config --exists libpipewire-0.3

# eSpeak NG, every language. No pcaudiolib (speech-dispatcher plays the
# audio), no sonic, no MBROLA, no speech-player (C++). The data is compiled
# with the binary just built, one job at a time (the dictionaries' rules
# race in parallel).
# renovate: datasource=github-releases depName=espeak-ng/espeak-ng
ARG ESPEAK_NG_VERSION=1.52.0
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/espeak-ng/espeak-ng/archive/refs/tags/${ESPEAK_NG_VERSION}.tar.gz -o e.tar.gz && \
    tar -xzf e.tar.gz && rm e.tar.gz && mv espeak-ng-${ESPEAK_NG_VERSION} espeak-src && cd espeak-src && \
    mkdir -p m4 && touch AUTHORS && ln -sf ChangeLog.md NEWS && ln -sf README.md README && autoreconf -fi && \
    ./configure --prefix=$P --libdir=$P/lib --disable-static --without-pcaudiolib --without-sonic \
      --without-mbrola --without-speechplayer && \
    make -j"$(nproc)" src/espeak-ng && make -j1 && make install && \
    test -d $P/share/espeak-ng-data/lang && $P/bin/espeak-ng --version

# speech-dispatcher with its eSpeak NG module, PipeWire audio and the Python
# client Orca uses (speechd), which starts the server itself
# (SPD_SPAWN_CMD). Its configuration is read from P/etc, which the user's
# ~/.config/speech-dispatcher overrides (spd-conf). No other synthesizer,
# no ltdl, no systemd units (the client starts it), no translations
# (--disable-nls keeps libintl out). src/common includes sndfile.h without
# SNDFILE_CFLAGS, so P/include is on the include path.
# renovate: datasource=github-releases depName=brailcom/speechd
ARG SPEECH_DISPATCHER_VERSION=0.12.1
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/brailcom/speechd/releases/download/${SPEECH_DISPATCHER_VERSION}/speech-dispatcher-${SPEECH_DISPATCHER_VERSION}.tar.gz -o sd.tar.gz && \
    tar -xzf sd.tar.gz && rm sd.tar.gz && mv speech-dispatcher-${SPEECH_DISPATCHER_VERSION} speechd-src && cd speechd-src && \
    PYTHON=$P/bin/python3 CPPFLAGS="-I$P/include" ./configure --prefix=$P --libdir=$P/lib --libexecdir=$P/lib --sysconfdir=$P/etc \
      --disable-static --disable-nls --disable-ltdl --disable-doc --enable-python \
      --with-espeak-ng --without-espeak --without-flite --without-ibmtts --without-voxin --without-ivona \
      --without-pico --without-baratinoo --without-kali \
      --with-pipewire --without-pulse --without-libao --without-alsa --without-oss --without-nas \
      --with-default-audio-method=pipewire \
      --with-systemdsystemunitdir=no --with-systemduserunitdir=no && \
    make -j"$(nproc)" && make install && \
    c=$P/etc/speech-dispatcher/speechd.conf && \
    sed -i -e 's/^# *AudioOutputMethod "pipewire"$/AudioOutputMethod "pipewire"/' \
      -e 's/^# *DefaultModule espeak-ng$/DefaultModule espeak-ng/' "$c" && \
    grep -qx 'AudioOutputMethod "pipewire"' "$c" && grep -qx 'DefaultModule espeak-ng' "$c" && \
    test -x $P/lib/speech-dispatcher-modules/sd_espeak-ng && \
    $P/bin/python3 -c 'import os, speechd.paths as p; print(p.SPD_SPAWN_CMD); assert os.access(p.SPD_SPAWN_CMD, os.X_OK)'

# Orca. Its configure checks GTK by importing it in Python; GTK is the
# image's and not in this stage, so that check is turned off (the typelibs
# are checked above). Its help (Yelp pages, which need itstool and Yelp)
# and its icon cache step (gtk-update-icon-cache) are left out. The GSettings
# schema is compiled into P/share, which Orca's launcher puts first in
# XDG_DATA_DIRS. The extension's version is Orca's.
# renovate: datasource=gitlab-tags depName=GNOME/orca registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>\d+\.\d+)$
ARG SCREEN_READER_VERSION=50.3
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.gnome.org/sources/orca/${SCREEN_READER_VERSION%.*}/orca-${SCREEN_READER_VERSION}.tar.xz -o o.tar.xz && \
    tar -xf o.tar.xz && rm o.tar.xz && mv orca-${SCREEN_READER_VERSION} orca-src && cd orca-src && \
    sed -i -e 's/^if gtk_test.returncode() != 0$/if false/' \
      -e "/^subdir('help')$/d" \
      -e 's/gtk_update_icon_cache: true/gtk_update_icon_cache: false/' meson.build && \
    grep -qx 'if false' meson.build && ! grep -qx "subdir('help')" meson.build && \
    rm -f $P/share/glib-2.0/schemas/org.a11y.*.gschema.xml && \
    meson setup buildDir --prefix=$P --libdir=lib --buildtype=plain && \
    ninja -C buildDir install && \
    test -s $P/share/glib-2.0/schemas/gschemas.compiled && \
    head -n 1 $P/bin/orca | grep -q "^#!$P/bin/python3" && \
    $P/bin/python3 -c 'import orca.orca_platform as p; print("orca", p.version)'

# What the extension ships: P without the build tools, headers, static and
# libtool files, the services the image runs itself (at-spi2-core's bus and
# registry), ATK and its bridge (the image's), pip and setuptools, the
# standard library's tests, IDLE, Tk and ensurepip, the typelibs of
# libraries Orca never loads (D-Bus GLib, GL, Vulkan, X extensions), and what a system
# extension must not carry. Then the Python modules that link a library the
# image may not have (readline, curses, sqlite, ssl, ...; Orca uses none),
# found by their DT_NEEDED. The standard library and every package are
# compiled to bytecode once, checked by hash rather than by file time (the
# sysext is read-only and its times may change), so Orca starts fast.
RUN set -eu; \
    mkdir -p /output/usr/lib/isotope-ext /output/usr/bin /output/usr/lib/systemd/user; \
    cp -a $P /output/usr/lib/isotope-ext/; \
    O=/output$P; S=$O/lib/python3.12; \
    u=$(ls $O/lib/systemd/user/orca.service /usr/lib/systemd/user/orca.service 2>/dev/null | head -n 1); \
    [ -n "$u" ] || { echo "Orca installed no orca.service" >&2; exit 1; }; \
    cp "$u" /output/usr/lib/systemd/user/orca.service; \
    ln -s ../lib/isotope-ext/screen-reader/bin/orca /output/usr/bin/orca; \
    cd $O; \
    rm -rf include lib/pkgconfig share/pkgconfig share/man share/doc share/info share/aclocal \
      share/gir-1.0 share/gobject-introspection-1.0 share/gtk-doc share/vim share/applications \
      share/dbus-1 share/defaults share/icons/hicolor/icon-theme.cache etc/xdg lib/systemd lib/gobject-introspection lib/cmake \
      lib/at-spi-bus-launcher lib/at-spi2-registryd lib/libatk-1.0.so* lib/libatk-bridge-2.0.so* \
      lib/gtk-2.0 lib/gtk-3.0 lib/gnome-settings-daemon-3.0 \
      $S/config-3.12-* $S/test $S/idlelib $S/tkinter $S/turtledemo $S/ensurepip $S/lib2to3 $S/pydoc_data \
      $S/site-packages/pip $S/site-packages/pip-* $S/site-packages/setuptools $S/site-packages/setuptools-* \
      $S/site-packages/_distutils_hack $S/site-packages/distutils-precedence.pth $S/site-packages/pkg_resources; \
    rm -f bin/g-ir-* bin/idle3* bin/pydoc3* bin/pip* bin/python3*-config bin/2to3*; \
    for t in DBus-1.0 DBusGLib-1.0 fontconfig-2.0 GL-1.0 libxml2-2.0 xft-2.0 xfixes-4.0 xrandr-1.3 Vulkan-1.0 win32-1.0; do \
      rm -f lib/girepository-1.0/$t.typelib; done; \
    find share/locale -name 'atk10.mo' -delete -o -name 'at-spi2-core.mo' -delete 2>/dev/null || true; \
    find . \( -name '*.la' -o -name '*.a' \) -delete; \
    for m in $S/lib-dynload/*.so; do \
      for lib in $(readelf -d "$m" | sed -n 's/.*(NEEDED).*\[\(.*\)\]/\1/p'); do \
        case " libc.so libffi.so.8 libz.so.1 libpython3.12.so.1.0 " in *" $lib "*) ;; \
          *) echo "dropping ${m##*/}: needs $lib"; rm -f "$m"; break ;; esac; \
      done; \
    done; \
    PYTHONPATH= $O/bin/python3 -c 'import ctypes, json, struct, math, select, subprocess, socket, array, fcntl, unicodedata, zlib, pyexpat, hashlib' || \
      { echo "a module Orca needs is gone" >&2; exit 1; }; \
    find $O -name __pycache__ -type d -prune -exec rm -rf {} +; \
    PYTHONHASHSEED=0 $P/bin/python3 -m compileall -q -j0 --invalidation-mode unchecked-hash \
      -d $P/lib/python3.12 -s $S -o 0 $S >/dev/null; \
    find $O -name '*.opt-*.pyc' -delete; \
    echo "size of the extension:"; du -sh /output/usr; du -sh $O/lib/python3.12 $O/share/espeak-ng-data $O/lib/girepository-1.0

# Licences, in a directory of the extension's own.
RUN set -eux; \
    L=/output/usr/share/licenses/screen-reader-extension; \
    for p in python-src:python gi-src:gobject-introspection atspi-src:at-spi2-core pygobject-src:pygobject \
             dasbus-src:dasbus sndfile-src:libsndfile espeak-src:espeak-ng speechd-src:speech-dispatcher orca-src:orca; do \
      src=/build/${p%%:*}; name=${p#*:}; mkdir -p $L/$name; \
      lic=$(ls -d $src/COPYING* $src/LICENSE* $src/LICENSES/* 2>/dev/null || true); \
      [ -n "$lic" ] || { echo "no licence in $src" >&2; exit 1; }; \
      cp -r $lic $L/$name/; \
    done; \
    printf '%s\n' \
      "The typelibs Gtk-3.0, Gdk-3.0, Pango-1.0, PangoCairo-1.0, GdkPixbuf-2.0 and HarfBuzz-0.0" \
      "are compiled from GIR files of github.com/gtk-rs/gir-files, which describe GTK, Pango," \
      "GdkPixbuf (LGPL-2.1-or-later) and HarfBuzz (MIT); those libraries are the image's own," \
      "with their licences there." > $L/gir-files.txt

# Every ELF file needs only the C library, the image libraries named in
# image-libs.txt ("linked") and libraries of the extension itself, and
# every typelib names only libraries of the image list or the extension.
COPY image-libs.txt /build/image-libs.txt
RUN set -eu; \
    allowed=" libc.so $(sed -n 's/^linked //p' /build/image-libs.txt | tr '\n' ' ') "; \
    loaded=" $(sed -n 's/^\(linked\|typelib\) //p' /build/image-libs.txt | tr '\n' ' ') "; \
    bad=; n=0; \
    for f in $(find /output -type f); do \
      [ "$(head -c 4 "$f" | od -An -c | tr -d ' ')" = '177ELF' ] || continue; n=$((n+1)); \
      for lib in $(readelf -d "$f" | sed -n 's/.*(NEEDED).*\[\(.*\)\]/\1/p'); do \
        case "$allowed" in *" $lib "*) continue ;; esac; \
        [ -e "/output$P/lib/$lib" ] && continue; \
        bad="$bad $f:$lib"; \
      done; \
    done; \
    echo "$n ELF files"; [ "$n" -gt 20 ]; \
    if [ -n "$bad" ]; then echo "libraries the image does not have:$bad" >&2; exit 1; fi; \
    for t in /output$P/lib/girepository-1.0/*.typelib; do \
      for lib in $(grep -ao 'lib[A-Za-z0-9_+-]*\.so\.[0-9]*' "$t" | sort -u); do \
        case "$loaded" in *" $lib "*) continue ;; esac; \
        [ -e "/output$P/lib/$lib" ] && continue; \
        bad="$bad ${t##*/}:$lib"; \
      done; \
    done; \
    if [ -n "$bad" ]; then echo "typelibs name libraries the image does not have:$bad" >&2; exit 1; fi; \
    for f in bin/python3.12 lib/libgirepository-1.0.so.1 lib/libatspi.so.0 bin/speech-dispatcher; do \
      readelf -d /output$P/$f | grep -E 'R(UN)?PATH' | grep -q $P/lib || { echo "$f has no RUNPATH to $P/lib" >&2; exit 1; }; \
    done
# The image's libraries for the test stage, which runs on the Hadron base:
# the build's own copies of the "linked" list and what they need.
RUN mkdir -p /image-runtime/usr/lib && \
    for l in $(sed -n 's/^linked //p' /build/image-libs.txt) libpcre2-8.so.0; do \
      cp -a /usr/lib/$l* /image-runtime/usr/lib/ 2>/dev/null || cp -aL /lib/$l /image-runtime/usr/lib/ || { echo "no $l in the build stage" >&2; exit 1; }; \
    done

# What the extension contains: everything under /usr of this stage.
FROM scratch AS default
COPY --from=build /output/usr /usr

# Checks the workflow runs before it publishes anything. Kept offline.
FROM ghcr.io/kairos-io/hadron:${HADRON_VERSION} AS test
ENV P=/usr/lib/isotope-ext/screen-reader
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
# The image's libraries, which every desktop image has, without replacing a
# file of the base.
COPY --from=build /image-runtime/ /image-runtime/
RUN cp -an /image-runtime/. / && rm -rf /image-runtime
RUN cp -a /extension/. / && rm -rf /extension
# Every library each ELF file needs is found. Python's extension modules
# (PyGObject's included) take Python's symbols from the interpreter that
# loads them, and speech-dispatcher's audio plugins theirs from the module
# that loads them, so for those musl's ldd also prints "Error relocating
# ...: symbol not found" and exits 1; those lines are left out, a missing
# library is not.
RUN set -eu; while read -r f; do \
      case "$f" in \
        $P/lib/python3.12/*.so|$P/lib/speech-dispatcher/*.so) \
          ldd "$f" 2>&1 | grep -v '^Error relocating .*: symbol not found$' >/tmp/ldd || true ;; \
        *) ldd "$f" >/tmp/ldd 2>&1 || { echo "ldd $f failed" >&2; cat /tmp/ldd >&2; exit 1; } ;; \
      esac; \
      if grep -qE 'not found|Error loading' /tmp/ldd; then echo "$f:" >&2; cat /tmp/ldd >&2; exit 1; fi; \
    done </tmp/elf
# The interpreter, GObject Introspection, libatspi and every typelib Orca
# loads (resolved without their libraries, which the image has), dasbus,
# the speech client, and Orca's own package.
RUN set -eux; \
    $P/bin/python3 -c 'import sys; assert sys.prefix == "/usr/lib/isotope-ext/screen-reader", sys.prefix; import ctypes, json, zlib, pyexpat, xml.dom.minidom'; \
    $P/bin/python3 -c 'import gi; gi.require_version("Atspi", "2.0"); from gi.repository import GLib, Gio, Atspi; Atspi.Device; print("atspi", Atspi.get_version() if hasattr(Atspi, "get_version") else "?")'; \
    $P/bin/python3 -c 'from gi.repository import GIRepository as R; r = R.Repository.get_default(); [r.require(n, v, 0) for n, v in (("Gtk", "3.0"), ("Gdk", "3.0"), ("Pango", "1.0"), ("PangoCairo", "1.0"), ("GdkPixbuf", "2.0"), ("HarfBuzz", "0.0"), ("Atk", "1.0"), ("Atspi", "2.0"))]; assert r.get_shared_library("Gtk") == "libgtk-3.so.0", r.get_shared_library("Gtk")'; \
    $P/bin/python3 -c 'import dasbus.connection, dasbus.server.interface'; \
    $P/bin/python3 -c 'import os, speechd, speechd.paths as p; assert os.access(p.SPD_SPAWN_CMD, os.X_OK), p.SPD_SPAWN_CMD'; \
    $P/bin/python3 -c 'import orca.orca_platform as p; assert p.prefix == "/usr/lib/isotope-ext/screen-reader", p.prefix; print("orca", p.version)'; \
    test -x /usr/bin/orca; [ "$(readlink -f /usr/bin/orca)" = $P/bin/orca ]; \
    test -s $P/share/glib-2.0/schemas/gschemas.compiled; \
    grep -qx 'ExecStart=orca --replace' /usr/lib/systemd/user/orca.service; \
    test -n "$(find $P/lib/python3.12/site-packages/orca -name '*.pyc' | head -n 1)"
# Speech: eSpeak NG speaks into a WAV file in every language it has, and
# speech-dispatcher's configuration plays through PipeWire with eSpeak NG.
# Real sound needs a desktop session (the design note's manual list).
RUN set -eux; \
    $P/bin/espeak-ng -v en -w /tmp/en.wav "Isotope screen reader"; [ "$(stat -c %s /tmp/en.wav)" -gt 10000 ]; \
    $P/bin/espeak-ng -v de -w /tmp/de.wav "Bildschirmleser"; [ "$(stat -c %s /tmp/de.wav)" -gt 10000 ]; \
    [ "$($P/bin/espeak-ng --voices | wc -l)" -gt 80 ]; \
    $P/bin/speech-dispatcher --version | head -n 1; \
    grep -qx 'AudioOutputMethod "pipewire"' $P/etc/speech-dispatcher/speechd.conf; \
    grep -qx 'DefaultModule espeak-ng' $P/etc/speech-dispatcher/speechd.conf; \
    test -x $P/lib/speech-dispatcher-modules/sd_espeak-ng

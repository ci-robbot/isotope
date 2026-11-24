# Isotope environment: Sway/Wayland or i3/XLibre.
#
# Builds a complete desktop on top of the Hadron base image. DESKTOP=sway keeps
# the existing Wayland/Sway stack; DESKTOP=i3 selects XLibre and i3. BuildKit
# only evaluates the selected desktop rootfs, so the other graphics stack is
# pruned from both the build graph and the resulting image.
# Everything is built from source against the Hadron musl toolchain.
#
# Build:    docker build -t sway-desktop:dev .
# XLibre:   docker build --build-arg DESKTOP=i3 -t i3-desktop:dev .
# Test:     test/run.sh   (full build -> boot -> assert loop)
#
# Milestone M1: Sway compositor under systemd-logind, rendering on tty1 for an
# ly-authenticated user, with a terminal (foot).

# Hadron release the desktop is built on. The base image and the toolchain MUST
# come from the same release (same musl/GCC ABI), so both derive from this one
# value. scripts/bump-hadron.sh rewrites it here, in Dockerfile.agent and in the
# Makefile; .github/workflows/follow-hadron.yml runs that when Hadron tags a
# new release.
ARG HADRON_VERSION=v0.6.7
# TODO: back to ghcr.io/kairos-io/hadron:${HADRON_VERSION} when the Hadron
# release with nftables (kairos-io/hadron#893) is out. The firewall needs nft;
# HADRON_VERSION stays the release for the toolchain and the prebuild keys.
ARG BASE_IMAGE=ghcr.io/kairos-io/hadron:main
# smartmontools comes from its hadron-layers layer (statically linked, no shared
# library a base or image already has), copied into every desktop image and the
# server: see the smartmontools stage.
# renovate: datasource=docker depName=ghcr.io/kairos-io/hadron-layers/smartmontools
ARG SMARTMONTOOLS_LAYER_VERSION=7.5
# The QEMU guest agent comes from its hadron-layers layer (statically linked,
# files under /usr only), on the vm image line only: see the guest-tools stages.
# renovate: datasource=docker depName=ghcr.io/kairos-io/hadron-layers/qemu-guest-agent
ARG QEMU_GUEST_AGENT_LAYER_VERSION=11.1.3
# The image of the glibc container (`isotope glibc`, the Distrobox named
# isotope-glibc), written to /usr/share/isotope/glibc.conf in desktop-default.
# cmd/isotope/glibc.go's glibcDefaultImage falls back to the same value.
# Ubuntu LTS only: renovate.json allows even-year .04 tags, and the next LTS
# is a major update that waits on the dependency dashboard. A box that exists
# keeps its image until `isotope glibc reset`.
# renovate: datasource=docker depName=docker.io/library/ubuntu
ARG GLIBC_BOX_IMAGE=docker.io/library/ubuntu:26.04
# Select the display server + window manager pair: sway, i3, cosmic or niri.
# server builds the server edition instead, with no desktop (server-default).
ARG DESKTOP=sway
# GPU=vm (default): software/virtual GL only (llvmpipe, virgl, softpipe, svga)
#   and Vulkan on the CPU or through virtio-gpu (lavapipe, venus). llvmpipe
#   links libLLVM, so the llvm stage below is built (or taken from
#   LLVM_IMAGE), but not the SPIRV stages nor Rust.
# GPU=full: also hardware GL (iris, crocus, i915, radeonsi, r600, r300,
#   nouveau, zink), Vulkan (radv, anv, hasvk, NVK) and VA-API, which adds the
#   SPIRV stack below and Rust (for NVK). See the mesa stage.
ARG GPU=vm
# kairos-init image for the bootable layer (final `kairos` stage). kairos-init
# now lives in the kairos-io/kairos monorepo, whose master pipeline promotes
# this tag only after its own image tests pass. Global ARG so it is usable in
# that stage's FROM.
ARG KAIROS_INIT_IMAGE=ghcr.io/kairos-io/kairos/kairos-init:master

FROM ghcr.io/kairos-io/hadron-toolchain:${HADRON_VERSION} AS toolchain

# ---------------------------------------------------------------------------
# Boot splash (splash/). Installed as /usr/bin/kairos-splash: kairos-init leaves
# an existing binary at that path alone and runs it in both halves of the boot
# (its 50kairos-splash dracut module bakes it into the initramfs, and
# kairos-splash.service runs it before the login screen). It draws straight
# into the framebuffer, falling back to a text animation without one.
#
# `make test` checks the password agent protocol (agent.c). The smoke tests
# run the whole program four times: the non-tty fallback, one rendered
# frame, one with the passphrase panel and one with the "Press D" hint
# (which must differ from the plain frame), each a PPM of exactly the
# requested size.
# ---------------------------------------------------------------------------
FROM toolchain AS isotope-splash
COPY splash/ /build/splash/
RUN cd /build/splash && make clean && make && make test && \
    ./isotope-splash | grep -q ISOTOPE && \
    ./isotope-splash --render=/tmp/frame.ppm --size=640x400 --at=2 && \
    [ "$(stat -c %s /tmp/frame.ppm)" -eq $((15 + 640 * 400 * 3)) ] && \
    ./isotope-splash --render=/tmp/prompt.ppm --size=640x400 --at=2 --prompt=retry && \
    [ "$(stat -c %s /tmp/prompt.ppm)" -eq $((15 + 640 * 400 * 3)) ] && \
    ./isotope-splash --render=/tmp/hint.ppm --size=640x400 --at=2 --hint && \
    [ "$(stat -c %s /tmp/hint.ppm)" -eq $((15 + 640 * 400 * 3)) ] && ! cmp -s /tmp/frame.ppm /tmp/hint.ppm


FROM toolchain AS wayland
# renovate: datasource=gitlab-tags depName=wayland/wayland registryUrl=https://gitlab.freedesktop.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG WAYLAND_VERSION=1.25.0
# renovate: datasource=gitlab-tags depName=wayland/wayland-protocols registryUrl=https://gitlab.freedesktop.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG WAYLAND_PROTOCOLS_VERSION=1.49
RUN mkdir -p /wayland
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://gitlab.freedesktop.org/wayland/wayland/-/archive/${WAYLAND_VERSION}/wayland-${WAYLAND_VERSION}.tar -o wayland.tar && tar -xf wayland.tar && rm wayland.tar && mv wayland-* wayland-src
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://gitlab.freedesktop.org/wayland/wayland-protocols/-/archive/${WAYLAND_PROTOCOLS_VERSION}/wayland-protocols-${WAYLAND_PROTOCOLS_VERSION}.tar -o wayland-protocols.tar && tar -xf wayland-protocols.tar && rm wayland-protocols.tar && mv wayland-protocols-* wayland-protocols-src
RUN pip3 install meson ninja
WORKDIR /build/wayland-src
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dtests=false -Ddocumentation=false -Dscanner=true -Dlibraries=true
RUN DESTDIR=/wayland ninja -C buildDir install
RUN ninja -C buildDir install
WORKDIR /build/wayland-protocols-src
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dtests=false
RUN DESTDIR=/wayland ninja -C buildDir install


# libpciaccess: libdrm_intel needs it, and the X server (x11-libs takes it
# from here). It is in /libdrm too, so every image that has libdrm_intel has it.
FROM toolchain AS libpciaccess
# renovate: datasource=gitlab-tags depName=xorg/lib/libpciaccess registryUrl=https://gitlab.freedesktop.org extractVersion=^libpciaccess-(?<version>\d+(\.\d+)*)$
ARG LIBPCIACCESS_VERSION=0.18.1
RUN pip3 install meson ninja
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://xorg.freedesktop.org/archive/individual/lib/libpciaccess-${LIBPCIACCESS_VERSION}.tar.xz -o libpciaccess.tar.xz && \
    tar -xf libpciaccess.tar.xz && cd libpciaccess-${LIBPCIACCESS_VERSION} && \
    meson setup buildDir ${COMMON_MESON_FLAGS} -Dzlib=disabled && \
    DESTDIR=/libpciaccess ninja -C buildDir install


# libdrm with every x86 driver API on, not left to "auto": Mesa's i915
# driver (GPU=full) links libdrm_intel, which needs libpciaccess; radeonsi and
# radv link libdrm_amdgpu (2.4.133 or later since Mesa 26); and a missing
# dependency must fail here, not drop the API in silence.
FROM toolchain AS libdrm
COPY --from=libpciaccess /libpciaccess /
COPY --from=libpciaccess /libpciaccess /libdrm
# renovate: datasource=gitlab-tags depName=mesa/drm registryUrl=https://gitlab.freedesktop.org extractVersion=^libdrm-(?<version>\d+(\.\d+)*)$
ARG LIBDRM_VERSION=2.4.133
RUN mkdir -p /libdrm
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://dri.freedesktop.org/libdrm/libdrm-${LIBDRM_VERSION}.tar.xz -o libdrm.tar.xz && tar -xf libdrm.tar.xz && rm libdrm.tar.xz && mv libdrm-* libdrm-src
WORKDIR /build/libdrm-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dtests=false -Dintel=enabled -Dradeon=enabled \
      -Damdgpu=enabled -Dnouveau=enabled -Dvmwgfx=enabled
RUN DESTDIR=/libdrm ninja -C buildDir install && \
    for l in intel.so.1 radeon.so.1 amdgpu.so.1 nouveau.so.2; do test -e /libdrm/usr/lib/libdrm_$l || exit 1; done


# systemd/libudev in the Hadron musl toolchain is linked against libucontext.
# Build it explicitly so every stage using those toolchain libraries can link
# reliably, and copy it into both desktop runtime roots below.
FROM toolchain AS libucontext
# renovate: datasource=github-tags depName=kaniini/libucontext extractVersion=^libucontext-(?<version>\d+(\.\d+)*)$
ARG LIBUCONTEXT_VERSION=1.5.1
RUN mkdir -p /libucontext
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/kaniini/libucontext/archive/refs/tags/libucontext-${LIBUCONTEXT_VERSION}.tar.gz -o libucontext.tar.gz && \
    tar -xf libucontext.tar.gz && mv libucontext-* src
WORKDIR /build/src
RUN make -j"$(nproc)" ARCH=x86_64
RUN make ARCH=x86_64 DESTDIR=/libucontext install


# ===========================================================================
# LLVM / SPIRV stack for Mesa: llvmpipe (every GPU mode) and iris + radeonsi
# (GPU=full; r600, r300 and i915 use the same LLVM, see the mesa stage).
#
# Kept ENTIRELY inside this example so the main Hadron toolchain Dockerfile is
# never touched: these stages build LLVM/clang/libclc + SPIRV on top of the
# published hadron-toolchain (same musl ABI — no Alpine cross-mix). Every build
# uses the llvm stage, because Mesa's llvmpipe links libLLVM; without it, a VM
# without 3D or any GPU Mesa has no driver for falls back to softpipe, which
# takes seconds per frame. The SPIRV stages are compiled ONLY for GPU=full; the
# `llvm-stack` indirection at the bottom makes the GPU=vm build prune them
# (BuildKit only builds referenced stages). CI and release builds take the llvm
# stage from a prebuilt image (LLVM_IMAGE in the Makefile).
#
# Validated on hadron-toolchain (musl / GCC 15): LLVM 20.1.8 (LLVM 18 fails on
# GCC 15), amdgcn libclc, SPIRV-Tools, and llvm-spirv all compile; Mesa 26.3
# links iris/radeonsi against them.
# ===========================================================================

FROM toolchain AS spirv-tools
# renovate: datasource=github-tags depName=KhronosGroup/SPIRV-Headers versioning=regex:^vulkan-sdk-(?<major>\d+)\.(?<minor>\d+)\.(?<patch>\d+)\.(?<build>\d+)$
ARG SPIRV_HEADERS_VERSION=vulkan-sdk-1.4.309.0
# renovate: datasource=github-tags depName=KhronosGroup/SPIRV-Tools versioning=regex:^vulkan-sdk-(?<major>\d+)\.(?<minor>\d+)\.(?<patch>\d+)\.(?<build>\d+)$
ARG SPIRV_TOOLS_VERSION=vulkan-sdk-1.4.309.0
RUN pip3 install ninja
WORKDIR /b
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/KhronosGroup/SPIRV-Headers/archive/refs/tags/${SPIRV_HEADERS_VERSION}.tar.gz -o sh.tgz && \
    curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/KhronosGroup/SPIRV-Tools/archive/refs/tags/${SPIRV_TOOLS_VERSION}.tar.gz -o st.tgz && \
    tar -xf sh.tgz && mv SPIRV-Headers-* spirv-headers && \
    tar -xf st.tgz && mv SPIRV-Tools-* spirv-tools && \
    cp -a spirv-headers spirv-tools/external/spirv-headers
RUN cd spirv-headers && cmake -G Ninja -B build -DCMAKE_INSTALL_PREFIX=/usr -DCMAKE_INSTALL_LIBDIR=lib && \
    DESTDIR=/spirv-tools ninja -C build install
RUN cd spirv-tools && cmake -G Ninja -B build -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/usr -DCMAKE_INSTALL_LIBDIR=lib \
      -DSPIRV_SKIP_TESTS=ON -DBUILD_SHARED_LIBS=ON \
      -DSPIRV-Headers_SOURCE_DIR=/b/spirv-tools/external/spirv-headers && \
    ninja -C build -j$(nproc) && DESTDIR=/spirv-tools ninja -C build install

# LLVM + clang + amdgcn libclc. LTO off (brutal for LLVM); shared libLLVM dylib;
# in-tree SPIRV backend kept so clang can emit SPIR-V. The spirv libclc targets
# need llvm-spirv at configure time (circular dep on LLVM), so libclc builds the
# amdgcn targets only — enough for radeonsi + Mesa's libclc dependency. The
# AMDGPU target also holds the R600 processors, which r600 asks LLVM for.
FROM toolchain AS llvm
# renovate: datasource=github-tags depName=llvm/llvm-project extractVersion=^llvmorg-(?<version>\d+(\.\d+)*)$
ARG LLVM_VERSION=20.1.8
RUN pip3 install ninja
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/llvm/llvm-project/releases/download/llvmorg-${LLVM_VERSION}/llvm-project-${LLVM_VERSION}.src.tar.xz -o llvm.tar.xz && \
    tar -xf llvm.tar.xz && rm llvm.tar.xz && mv llvm-project-* llvm-project
WORKDIR /build/llvm-project
RUN cmake -G Ninja -S llvm -B build \
      -DCMAKE_BUILD_TYPE=Release -DCMAKE_C_FLAGS="-O2 -pipe" -DCMAKE_CXX_FLAGS="-O2 -pipe" \
      -DCMAKE_INSTALL_PREFIX=/usr -DLLVM_LIBDIR_SUFFIX="" \
      -DLLVM_TARGETS_TO_BUILD="X86;AMDGPU;SPIRV" \
      -DLLVM_ENABLE_PROJECTS="clang;libclc" \
      -DLIBCLC_TARGETS_TO_BUILD="amdgcn--;amdgcn--amdhsa" \
      -DLLVM_BUILD_LLVM_DYLIB=ON -DLLVM_LINK_LLVM_DYLIB=ON \
      -DLLVM_ENABLE_ZSTD=OFF -DLLVM_ENABLE_LIBXML2=OFF -DLLVM_ENABLE_TERMINFO=OFF \
      -DLLVM_INCLUDE_TESTS=OFF -DLLVM_INCLUDE_EXAMPLES=OFF -DLLVM_INCLUDE_BENCHMARKS=OFF \
      -DCLANG_ENABLE_STATIC_ANALYZER=OFF -DCLANG_ENABLE_ARCMT=OFF
RUN ninja -C build -j$(nproc) && DESTDIR=/llvm ninja -C build install

# Only the /llvm install tree, for CI: release.yml builds this target once per
# LLVM key (`make print-llvm-tag`) and pushes it, and later builds swap it in
# for the llvm stage (`make LLVM_IMAGE=...`), which skips the 3-hour compile.
FROM scratch AS llvm-dist
COPY --from=llvm /llvm /llvm

# SPIRV-LLVM-Translator (llvm-spirv). Its cmake otherwise git-clones SPIRV-Headers
# at a pinned commit; with no git/network in the build, feed it a local checkout
# at exactly that commit (its sources use newer SPIR-V caps than the spirv-tools
# headers tag).
FROM toolchain AS spirv-llvm-translator
# renovate: datasource=github-tags depName=KhronosGroup/SPIRV-LLVM-Translator extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG SPIRV_LLVM_TRANSLATOR_VERSION=20.1.3
ARG SPIRV_HEADERS_TRANSLATOR_COMMIT=0e710677989b4326ac974fd80c5308191ed80965
RUN pip3 install ninja
COPY --from=llvm /llvm /llvm
RUN rsync -aHAX --keep-dirlinks /llvm/. /
WORKDIR /b
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/KhronosGroup/SPIRV-Headers/archive/${SPIRV_HEADERS_TRANSLATOR_COMMIT}.tar.gz -o sh.tgz && \
    tar -xf sh.tgz && mv SPIRV-Headers-* spirv-headers && \
    curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/KhronosGroup/SPIRV-LLVM-Translator/archive/refs/tags/v${SPIRV_LLVM_TRANSLATOR_VERSION}.tar.gz -o tr.tgz && \
    tar -xf tr.tgz && mv SPIRV-LLVM-Translator-* tr
RUN cd tr && cmake -G Ninja -B build -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/usr -DCMAKE_INSTALL_LIBDIR=lib \
      -DLLVM_DIR=/usr/lib/cmake/llvm -DBUILD_SHARED_LIBS=OFF \
      -DLLVM_EXTERNAL_SPIRV_HEADERS_SOURCE_DIR=/b/spirv-headers && \
    ninja -C build -j$(nproc) && DESTDIR=/translator ninja -C build install

# Merge the three installs into one /llvm-out tree (GPU=full path).
FROM toolchain AS llvm-stack-full
COPY --from=spirv-tools /spirv-tools /s
COPY --from=llvm /llvm /l
COPY --from=spirv-llvm-translator /translator /t
RUN mkdir -p /llvm-out && \
    rsync -aHAX /l/. /llvm-out && \
    rsync -aHAX /s/. /llvm-out && \
    rsync -aHAX /t/. /llvm-out

# LLVM only (GPU=vm path): what llvmpipe needs. Selecting this prunes the
# SPIRV-Tools and llvm-spirv stages above.
FROM toolchain AS llvm-stack-vm
COPY --from=llvm /llvm /llvm-out

# Pick the stack by GPU flag. For vm this resolves to the LLVM-only stage, so
# the SPIRV stages are never referenced and BuildKit skips them.
FROM llvm-stack-${GPU} AS llvm-stack


# libglvnd, the GL vendor-neutral dispatch libraries: libEGL.so.1,
# libGLESv1_CM.so.1, libGLESv2.so.2 and libOpenGL.so.0, which forward to
# Mesa's libEGL_mesa.so.0 through /usr/share/glvnd/egl_vendor.d/50_mesa.json.
# libOpenGL.so.0 is the desktop GL library without GLX. libepoxy dlopens it
# for the GL entry points of a desktop GL context made through EGL, and
# aborts with "Couldn't open libGL.so.1 or libOpenGL.so.0" when it is
# missing: that is Xwayland's glamor on a real GPU, which then leaves the
# COSMIC greeter as a cursor on a black screen. The Wayland images have no
# GLX, so glvnd is built without X11 and GLX. The tag tarball is checked
# against a SHA256 computed when pinned; a version bump must update
# LIBGLVND_SHA256 too. test/graphics/check-desktop-gl.sh checks the result.
FROM toolchain AS libglvnd
# renovate: datasource=gitlab-tags depName=glvnd/libglvnd registryUrl=https://gitlab.freedesktop.org extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG LIBGLVND_VERSION=1.7.0
ARG LIBGLVND_SHA256=2b6e15b06aafb4c0b6e2348124808cbd9b291c647299eaaba2e3202f51ff2f3d
RUN mkdir -p /libglvnd
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://gitlab.freedesktop.org/glvnd/libglvnd/-/archive/v${LIBGLVND_VERSION}/libglvnd-v${LIBGLVND_VERSION}.tar.gz -o libglvnd.tar.gz && \
    echo "${LIBGLVND_SHA256}  libglvnd.tar.gz" | sha256sum -c - && \
    tar -xf libglvnd.tar.gz && rm libglvnd.tar.gz && mv libglvnd-* libglvnd-src
WORKDIR /build/libglvnd-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dx11=disabled -Dglx=disabled \
      -Degl=true -Dgles1=true -Dgles2=true -Dheaders=true
RUN DESTDIR=/libglvnd ninja -C buildDir install


# Rust compiler from the pinned Alpine image: build time only, nothing it links
# ships. Used by Mesa's NVK (GPU=full), the i3 screenshot tools and the COSMIC
# stages, the same way Dockerfile.agent builds cua-driver. It is declared
# before all of them because BuildKit resolves --from only against stages
# defined above the consumer.
FROM rust:1.97.0-alpine3.22 AS rust-toolchain


# Mesa's Rust tools, for NVK (the nouveau Vulkan driver, GPU=full): rustc,
# bindgen and cbindgen. Built here against Hadron's musl with crt-static off,
# so bindgen can dlopen the llvm stage's libclang (the Alpine image's
# binaries are static and cannot). Mesa refuses bindgen 0.72.0, 0.73.0 and
# 0.73.1 (meson.build), so check its list before a bump. The toolchain's own
# static musl (self-contained/libc.a) is removed: Mesa links NVK's Rust
# library with -L to that directory (for libunwind.a), so the linker took
# libc.a from there before the system libc.so and put a second, never
# initialized musl into libvulkan_nouveau.so, which crashed in malloc as soon
# as the loader opened it. Build time only:
# nothing here ships. GPU=vm builds no Rust driver and takes the empty
# mesa-rust-vm stage, so it never pulls the Rust image.
FROM toolchain AS mesa-rust-full
COPY --from=rust-toolchain /usr/local/cargo/ /usr/local/cargo/
COPY --from=rust-toolchain /usr/local/rustup/ /usr/local/rustup/
ENV CARGO_HOME=/usr/local/cargo \
    RUSTUP_HOME=/usr/local/rustup \
    PATH=/usr/local/cargo/bin:${PATH} \
    RUSTFLAGS="-C target-feature=-crt-static" \
    CARGO_INCREMENTAL=0
# renovate: datasource=crate depName=bindgen-cli
ARG BINDGEN_VERSION=0.72.1
# renovate: datasource=crate depName=cbindgen
ARG CBINDGEN_VERSION=0.29.4
RUN cargo install bindgen-cli --version ${BINDGEN_VERSION} --locked && \
    cargo install cbindgen --version ${CBINDGEN_VERSION} --locked && \
    rm -rf /usr/local/cargo/registry /usr/local/cargo/git && \
    rm /usr/local/rustup/toolchains/*/lib/rustlib/x86_64-unknown-linux-musl/lib/self-contained/libc.a && \
    mkdir -p /mesa-rust/usr/local && mv /usr/local/cargo /usr/local/rustup /mesa-rust/usr/local/

FROM toolchain AS mesa-rust-vm
RUN mkdir -p /mesa-rust/usr/local

FROM mesa-rust-${GPU} AS mesa-rust


# glslangValidator: Mesa compiles the ray-tracing BVH shaders of radv, anv
# and lavapipe with it at build time (it wants 12.2 or later). Build time only:
# only the binary goes to the Mesa stages, nothing ships. No SPIRV-Tools
# optimizer (ENABLE_OPT=OFF): Mesa does not ask glslang to optimize.
FROM toolchain AS glslang
# renovate: datasource=github-tags depName=KhronosGroup/glslang versioning=regex:^vulkan-sdk-(?<major>\d+)\.(?<minor>\d+)\.(?<patch>\d+)\.(?<build>\d+)$
ARG GLSLANG_VERSION=vulkan-sdk-1.4.363.0
RUN pip3 install ninja
WORKDIR /b
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/KhronosGroup/glslang/archive/refs/tags/${GLSLANG_VERSION}.tar.gz -o g.tgz && \
    tar -xf g.tgz && mv glslang-* glslang
RUN cd glslang && cmake -G Ninja -B build -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/usr -DCMAKE_INSTALL_LIBDIR=lib \
      -DENABLE_OPT=OFF -DGLSLANG_TESTS=OFF -DBUILD_SHARED_LIBS=OFF && \
    ninja -C build -j$(nproc) && DESTDIR=/b/out ninja -C build install && \
    mkdir -p /glslang/usr/bin && cp -a /b/out/usr/bin/glslang* /glslang/usr/bin/ && \
    /glslang/usr/bin/glslangValidator --version


# The Vulkan loader (libvulkan.so.1), which finds Mesa's drivers through the
# ICD files in /usr/share/vulkan/icd.d. Without it no native Vulkan app and
# no zink (GL on Vulkan) works; Flatpak apps bring their own. The headers
# and the loader are one Vulkan SDK release; the headers are only for the
# loader's build, and only the runtime library goes to the images (no
# headers, pkg-config or CMake files: nothing on the image builds against
# them, and the C++ headers alone are over 20 MB). The WSI options only add
# header-level code, the loader links nothing for them: the Wayland images
# get Wayland (Mesa's Wayland build has no X11 WSI either), i3 gets XCB, Xlib
# and Wayland (vulkan-loader-x11, after the X11 libraries). Built without LTO,
# like Mesa: the loader's asm_offset step reads offsets out of a compiled
# object, which an LTO object does not have.
FROM toolchain AS vulkan-src
# renovate: datasource=github-tags depName=KhronosGroup/Vulkan-Headers versioning=regex:^vulkan-sdk-(?<major>\d+)\.(?<minor>\d+)\.(?<patch>\d+)\.(?<build>\d+)$
ARG VULKAN_HEADERS_VERSION=vulkan-sdk-1.4.363.0
# renovate: datasource=github-tags depName=KhronosGroup/Vulkan-Loader versioning=regex:^vulkan-sdk-(?<major>\d+)\.(?<minor>\d+)\.(?<patch>\d+)\.(?<build>\d+)$
ARG VULKAN_LOADER_VERSION=vulkan-sdk-1.4.363.0
RUN pip3 install ninja
WORKDIR /b
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/KhronosGroup/Vulkan-Headers/archive/refs/tags/${VULKAN_HEADERS_VERSION}.tar.gz -o h.tgz && \
    curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/KhronosGroup/Vulkan-Loader/archive/refs/tags/${VULKAN_LOADER_VERSION}.tar.gz -o l.tgz && \
    tar -xf h.tgz && mv Vulkan-Headers-* headers && tar -xf l.tgz && mv Vulkan-Loader-* loader
RUN cmake -G Ninja -S headers -B headers/build -DCMAKE_INSTALL_PREFIX=/usr && \
    ninja -C headers/build install
ENV VULKAN_LOADER_CMAKE="-G Ninja -S loader -B loader/build -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/usr -DCMAKE_INSTALL_LIBDIR=lib -DCMAKE_INSTALL_SYSCONFDIR=/etc -DBUILD_TESTS=OFF"

FROM vulkan-src AS vulkan-loader
RUN export CFLAGS="$(printf '%s' "$CFLAGS" | sed 's/-flto[^ ]*//g')" LDFLAGS="$(printf '%s' "$LDFLAGS" | sed 's/-flto[^ ]*//g')" && \
    cmake ${VULKAN_LOADER_CMAKE} -DBUILD_WSI_XCB_SUPPORT=OFF -DBUILD_WSI_XLIB_SUPPORT=OFF -DBUILD_WSI_WAYLAND_SUPPORT=ON && \
    ninja -C loader/build && DESTDIR=/vulkan-loader ninja -C loader/build install && \
    rm -rf /vulkan-loader/usr/include /vulkan-loader/usr/lib/pkgconfig /vulkan-loader/usr/lib/cmake && \
    test -e /vulkan-loader/usr/lib/libvulkan.so.1 && ! test -e /vulkan-loader/usr/include


# libva, the VA-API library that video players and browsers load Mesa's video
# drivers through (radeonsi, r600, nouveau and virgl: /usr/lib/dri/*_drv_video.so,
# GPU=full). DRM and Wayland only: libva's X11 part needs libX11, which the
# Wayland images do not have, and apps on i3 use the DRM display too. Intel's
# VA-API driver is intel-media-driver, built in its own stage below.
# GPU=vm has no VA-API and takes the empty libva-vm stage. /libva keeps the
# headers and pkg-config file for Mesa's build; the Mesa stages copy only the
# libraries into the images.
FROM toolchain AS libva-full
COPY --from=libdrm /libdrm /
COPY --from=wayland /wayland /
# renovate: datasource=github-releases depName=intel/libva
ARG LIBVA_VERSION=2.24.1
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/intel/libva/archive/refs/tags/${LIBVA_VERSION}.tar.gz -o libva.tar.gz && \
    tar -xf libva.tar.gz && mv libva-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Ddisable_drm=false -Dwith_x11=no -Dwith_glx=no -Dwith_wayland=yes -Denable_docs=false && \
    DESTDIR=/libva ninja -C buildDir install && \
    test -e /libva/usr/lib/libva.so.2 && test -e /libva/usr/lib/libva-drm.so.2 && test -e /libva/usr/lib/libva-wayland.so.2

FROM toolchain AS libva-vm
RUN mkdir -p /libva

FROM libva-${GPU} AS libva

# Intel's VA-API driver, intel-media-driver (iHD_drv_video.so), and the
# graphics memory library it links (gmmlib, libigdgmm). Both are C++ and link
# the libstdc++ the desktop images ship, like Mesa's megadriver. GPU=full only:
# GPU=vm has no VA-API and takes the empty stage. /gmmlib keeps the headers and
# pkg-config file for the driver's build; /intel-media-driver holds only the
# two runtime files, which desktop-default copies into the image (not into
# /mesa: that would change the Mesa stages' cache keys and make every Mesa
# build wait for this compile).
FROM toolchain AS gmmlib-full
# gmmlib publishes tags, not releases.
# renovate: datasource=github-tags depName=intel/gmmlib extractVersion=^intel-gmmlib-(?<version>\d+(\.\d+)*)$
ARG GMMLIB_VERSION=22.10.2
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/intel/gmmlib/archive/refs/tags/intel-gmmlib-${GMMLIB_VERSION}.tar.gz -o gmmlib.tar.gz && \
    tar -xf gmmlib.tar.gz && mv gmmlib-* src
WORKDIR /build/src
RUN pip3 install ninja && \
    export CFLAGS="$(printf '%s' "$CFLAGS" | sed 's/-flto[^ ]*//g')" && export CXXFLAGS="$CFLAGS" LDFLAGS="$(printf '%s' "$LDFLAGS" | sed 's/-flto[^ ]*//g')" && \
    cmake -G Ninja -S . -B build -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/usr -DCMAKE_INSTALL_LIBDIR=lib -DRUN_TEST_SUITE=OFF && \
    DESTDIR=/gmmlib ninja -C build install && \
    install -Dm644 LICENSE.md /gmmlib/usr/share/licenses/gmmlib/LICENSE.md && \
    test -e /gmmlib/usr/lib/libigdgmm.so.12

FROM toolchain AS gmmlib-vm
RUN mkdir -p /gmmlib

FROM gmmlib-${GPU} AS gmmlib

FROM toolchain AS intel-media-driver-full
COPY --from=libdrm /libdrm /
COPY --from=libva /libva /
COPY --from=gmmlib /gmmlib /
# renovate: datasource=github-releases depName=intel/media-driver extractVersion=^intel-media-(?<version>\d+(\.\d+)*)$
ARG INTEL_MEDIA_DRIVER_VERSION=26.3.5
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/intel/media-driver/archive/refs/tags/intel-media-${INTEL_MEDIA_DRIVER_VERSION}.tar.gz -o md.tar.gz && \
    tar -xf md.tar.gz && mv media-driver-* src
WORKDIR /build/src
RUN pip3 install ninja && \
    export CFLAGS="$(printf '%s' "$CFLAGS" | sed 's/-flto[^ ]*//g')" && export CXXFLAGS="$CFLAGS" LDFLAGS="$(printf '%s' "$LDFLAGS" | sed 's/-flto[^ ]*//g')" && \
    cmake -G Ninja -S . -B build -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/usr -DCMAKE_INSTALL_LIBDIR=lib \
      -DLIBVA_DRIVERS_PATH=/usr/lib/dri -DENABLE_PRODUCTION_KMD=ON -DBUILD_CMRTLIB=OFF -DMEDIA_RUN_TEST_SUITE=OFF \
      -DMEDIA_BUILD_FATAL_WARNINGS=OFF -DINSTALL_DRIVER_SYSCONF=OFF && \
    ninja -C build iHD_drv_video
# Only the two runtime files and the licences (media-driver: MIT, with BSD-3
# for the WebM parts; gmmlib: MIT); /usr/lib/libigdgmm.so* is gmmlib's install,
# copied in above with the headers and pkg-config file that stay behind.
RUN mkdir -p /intel-media-driver/usr/lib/dri && \
    install -m755 build/media_driver/iHD_drv_video.so /intel-media-driver/usr/lib/dri/ && \
    cp -a /usr/lib/libigdgmm.so* /intel-media-driver/usr/lib/ && \
    install -Dm644 -t /intel-media-driver/usr/share/licenses/intel-media-driver LICENSE.md third-party-programs.txt && \
    install -Dm644 /usr/share/licenses/gmmlib/LICENSE.md /intel-media-driver/usr/share/licenses/gmmlib/LICENSE.md && \
    strip --strip-unneeded /intel-media-driver/usr/lib/dri/iHD_drv_video.so /intel-media-driver/usr/lib/libigdgmm.so.12.* && \
    test -e /intel-media-driver/usr/lib/dri/iHD_drv_video.so && \
    ! find /intel-media-driver -name '*.h' -o -name '*.pc' -o -name '*.cmake' | grep -q .

FROM toolchain AS intel-media-driver-vm
RUN mkdir -p /intel-media-driver

FROM intel-media-driver-${GPU} AS intel-media-driver

# Guest tools, GPU=vm only: qemu-ga, qemu-guest-agent.service and the udev rule
# that starts the unit when the host presents the org.qemu.guest_agent.0 virtio
# port. The unit is not enabled (no wants link): the port exists only in a QEMU
# VM, so on real hardware the agent never starts. GPU=full (real hardware) gets
# none of it. Copied in desktop-default, so the agent image (built on i3 vm)
# has it too; the server has no vm line.
FROM ghcr.io/kairos-io/hadron-layers/qemu-guest-agent:${QEMU_GUEST_AGENT_LAYER_VERSION} AS guest-tools-vm

FROM scratch AS guest-tools-full

FROM guest-tools-${GPU} AS guest-tools


FROM toolchain AS mesa
COPY --from=libdrm /libdrm /
# Mesa is a glvnd vendor: it installs libEGL_mesa.so.0 and the vendor file,
# and glvnd's libraries go into /mesa with it, so every stage and image that
# takes /mesa gets the whole GL stack. The Vulkan loader goes with it too.
COPY --from=libglvnd /libglvnd /
COPY --from=libglvnd /libglvnd /mesa
COPY --from=vulkan-loader /vulkan-loader /mesa
# LLVM stack: LLVM for GPU=vm, LLVM + libclc + SPIRV for GPU=full.
COPY --from=llvm-stack /llvm-out /llvm-out
RUN rsync -aHAX --keep-dirlinks /llvm-out/. / && rm -rf /llvm-out
COPY --from=wayland /wayland /
COPY --from=glslang /glslang /
# Rust, bindgen and cbindgen for NVK (GPU=full; empty for GPU=vm).
COPY --from=mesa-rust /mesa-rust/ /
ENV CARGO_HOME=/usr/local/cargo \
    RUSTUP_HOME=/usr/local/rustup \
    PATH=/usr/local/cargo/bin:${PATH}
COPY --from=libva /libva /libva-out
# renovate: datasource=gitlab-tags depName=mesa/mesa registryUrl=https://gitlab.freedesktop.org extractVersion=^mesa-(?<version>\d+(\.\d+)*)$
ARG MESA_VERSION=26.3.1
RUN mkdir -p /mesa
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://archive.mesa3d.org/mesa-${MESA_VERSION}.tar.xz -o mesa.tar.xz && tar -xf mesa.tar.xz && rm mesa.tar.xz && mv mesa-* mesa-src
WORKDIR /build/mesa-src
RUN pip3 install meson ninja setuptools packaging mako pyyaml
# venus (the virtio Vulkan driver) needs venus-protocol at exactly the version
# Mesa's subprojects/venus-protocol.wrap names, a git wrap; the toolchain has
# no git, so the tag's tarball goes into the directory that wrap names. A Mesa
# bump that changes the wrap must change this line and the checksum.
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://gitlab.freedesktop.org/virgl/venus-protocol/-/archive/v1.1.3/venus-protocol-v1.1.3.tar.gz -o venus.tgz && \
    echo "c5a362ca28e0a35042db1baeadaf9183500b216841e49d72714b80bae50b4445  venus.tgz" | sha256sum -c - && \
    grep -qx 'directory = venus-protocol-1.1.3' subprojects/venus-protocol.wrap && \
    grep -qx 'revision = v1.1.3' subprojects/venus-protocol.wrap && \
    tar -xzf venus.tgz -C subprojects && mv subprojects/venus-protocol-v1.1.3 subprojects/venus-protocol-1.1.3 && rm venus.tgz
# Every mode builds llvmpipe, the software rasterizer Mesa picks for any
# display it has no GPU driver for: a VM without 3D (virtio-gpu, QXL, -vga
# std) or, with GPU=vm, real hardware. softpipe stays as a fallback that
# GALLIUM_DRIVER=softpipe selects, for when llvmpipe itself is suspect.
# GPU=vm (default): software/virtual GL only (llvmpipe/virgl/softpipe/svga),
#   and Vulkan on the CPU (lavapipe) and through virtio-gpu to the host
#   (venus). No zink, no VA-API: a VM gets nothing from them.
# GPU=full: also real hardware, the x86 set the big distributions (Fedora,
# Debian, Arch, Alpine, Chimera) ship:
#   - Intel: iris (gen 8 and later, Broadwell on), crocus (gen 4 to 7.5: GMA
#     X4500, Ironlake, Sandy Bridge, Ivy Bridge, Haswell, Bay Trail,
#     2008-2014) and i915 (gen 2 and 3: GMA 900 to 3150, 2004-2010; GL 2.1,
#     links libdrm_intel). crocus shares Intel's compiler with iris (the elk
#     back end, built in-tree). Vulkan: anv (gen 9 on) and hasvk (gen 7 and 8).
#   - AMD: radeonsi (GCN and later), r600 (TeraScale, HD 2000 to HD 6000 and
#     the Llano, Trinity, Richland and Brazos APUs, 2007-2013) and r300 (R300
#     to R500, X300 to X1950, 2002-2008; GL 2.1). r600 compiles its shaders
#     with its own NIR back end (sfn); with -Dllvm=enabled Mesa adds LLVM's
#     `amdgpu` and `asmparser` components for it, and LLVM's AMDGPU target
#     (built in the llvm stage) holds the R600 processors too, so no other
#     LLVM target is needed. r300 and i915 need LLVM's draw module for their
#     vertex shaders, which llvmpipe needs too. Vulkan: radv. radeonsi and
#     radv need libdrm_amdgpu 2.4.133 or later (Mesa 26).
#   - NVIDIA: nouveau. Without it, a compositor on an NVIDIA GPU has KMS but
#     no GL or GBM driver for the render node, and shows only the cursor.
#     Vulkan: NVK (Turing and later), which is Rust and needs mesa-rust.
#   - zink, GL on any of these Vulkan drivers (MESA_LOADER_DRIVER_OVERRIDE=zink).
#   - The device-select Vulkan layer, which puts the GPU that drives the
#     display first on a machine with two (as Fedora and Debian ship it).
#   - VA-API video decode and encode (radeonsi, r600, nouveau, virgl) with
#     every codec, as Debian, Arch, Alpine and Chimera build it; Fedora leaves
#     out the patented ones.
#   iris and radeonsi link against the LLVM/libclc/SPIRV stack built by the
#   stages above (rsync'd into /usr). The 2.1-era GPUs (r300, i915) may still
#   be too weak for a compositor: isotope-gpu-probe and the sessions' software
#   fallback cover a GPU whose driver fails, as they cover one with no driver
#   at all. Pair with --build-arg FIRMWARE=true so the i915/amdgpu/radeon
#   blobs and nouveau's GSP firmware are present.
# Mesa 26 refuses LTO (meson.build: "Building Mesa with LTO is not
# supported"); the toolchain's CFLAGS and LDFLAGS carry -flto=auto, which
# that check does not see, so it is taken out here.
# NVK's Rust code is a static library linked into libvulkan_nouveau.so; it
# must use the system's musl. rust_args turns rustc's crt-static default for
# the musl target off, and mesa-rust removes the toolchain's static libc.a
# (see there). The install step below fails when NVK defines malloc, which
# means it has its own copy of musl and crashes the first app that loads it.
# NVK also links Rust's static LLVM libunwind; --exclude-libs keeps its
# _Unwind_* symbols out of the dynamic table, so C++ code in the same process
# keeps libgcc_s's unwinder, and the install step checks that too.
# Mesa 26 has no "wayland" EGL native platform any more, so the Wayland
# build's EGL_DEFAULT_DISPLAY is surfaceless (it was wayland with 25.3). Apps
# that pass a wl_display or use eglGetPlatformDisplay, as the toolkits do,
# are not affected.
ARG GPU=vm
# The example's libLLVM is built without RTTI, so Mesa must disable it too
# (cpp_rtti=false).
RUN if [ "$GPU" = "full" ]; then \
      DRV="iris,crocus,i915,radeonsi,r600,r300,nouveau,zink,llvmpipe,softpipe,virgl,svga"; \
      VK="amd,intel,intel_hasvk,nouveau,swrast,virtio"; VKLAYERS="device-select"; VA=enabled; \
      RUSTARGS="-Drust_args=-Ctarget-feature=-crt-static"; \
      rsync -aHAX --keep-dirlinks /libva-out/. /; \
    else \
      DRV="llvmpipe,softpipe,virgl,svga"; \
      VK="swrast,virtio"; VKLAYERS=""; VA=disabled; RUSTARGS=""; \
    fi; \
    export CFLAGS="$(printf '%s' "$CFLAGS" | sed 's/-flto[^ ]*//g')" \
           CXXFLAGS="$(printf '%s' "${CXXFLAGS:-}" | sed 's/-flto[^ ]*//g')" \
           LDFLAGS="$(printf '%s' "$LDFLAGS" | sed 's/-flto[^ ]*//g') -Wl,--exclude-libs,libunwind.a"; \
    meson setup buildDir ${COMMON_MESON_FLAGS} ${RUSTARGS} -Dplatforms=wayland \
    -Dgallium-drivers="$DRV" \
    -Dvulkan-drivers="$VK" \
    -Dvulkan-layers="$VKLAYERS" \
    -Dgallium-va="$VA" \
    -Dvideo-codecs=all \
    -Dglx=disabled \
    -Dglvnd=enabled \
    -Dopengl=true \
    -Dgles1=enabled \
    -Dgles2=enabled \
    -Degl=enabled \
    -Dllvm=enabled \
    -Dcpp_rtti=false \
    -Dbuild-tests=false
RUN DESTDIR=/mesa ninja -C buildDir install && \
    if [ -e /mesa/usr/lib/libvulkan_nouveau.so ]; then \
      nm -D --defined-only /mesa/usr/lib/libvulkan_nouveau.so >/tmp/nvk.syms && \
      ! grep -qw -e malloc -e _Unwind_RaiseException /tmp/nvk.syms; \
    fi
# The megadriver links libLLVM at runtime (llvmpipe in every mode, lavapipe
# too); with GPU=full it also links libelf (radeonsi, NVK), the shared
# SPIRV-Tools libraries (iris and the Vulkan drivers) and libdrm_intel (i915;
# from /libdrm with its libpciaccess), and the VA drivers link libva. They
# come from the LLVM stack, the toolchain and the libva stage and aren't
# otherwise in the OS image, so bundle them: only the shared libraries, not
# the LLVM install (libLLVM is ~125MB, the cost of llvmpipe). libdrm's
# amdgpu, radeon, nouveau and intel libraries come with /libdrm.
RUN mkdir -p /mesa/usr/lib && \
    cp -a /usr/lib/libLLVM.so* /mesa/usr/lib/ && \
    if [ "$GPU" = "full" ]; then \
      cp -a /usr/lib/libelf.so* /usr/lib/libSPIRV-Tools*.so* /mesa/usr/lib/ && \
      rsync -aHAX --keep-dirlinks --exclude=/usr/include --exclude=/usr/lib/pkgconfig /libva-out/. /mesa/ ; \
    fi && \
    rm -rf /libva-out


FROM toolchain AS xkeyboard-config
# renovate: datasource=gitlab-tags depName=xkeyboard-config/xkeyboard-config registryUrl=https://gitlab.freedesktop.org extractVersion=^xkeyboard-config-(?<version>\d+(\.\d+)*)$
ARG XKEYBOARD_CONFIG_VERSION=2.44
RUN mkdir -p /xkeyboard-config
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://gitlab.freedesktop.org/xkeyboard-config/xkeyboard-config/-/archive/xkeyboard-config-${XKEYBOARD_CONFIG_VERSION}/xkeyboard-config-xkeyboard-config-${XKEYBOARD_CONFIG_VERSION}.tar.gz -o xkeyboard-config.tar.gz && tar -xf xkeyboard-config.tar.gz && rm xkeyboard-config.tar.gz && mv xkeyboard-config-xkeyboard-config-* xkeyboard-config-src
WORKDIR /build/xkeyboard-config-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS}
RUN DESTDIR=/xkeyboard-config ninja -C buildDir install


FROM toolchain AS libxkb
COPY --from=wayland /wayland /
COPY --from=xkeyboard-config /xkeyboard-config /
# renovate: datasource=github-tags depName=xkbcommon/libxkbcommon extractVersion=^xkbcommon-(?<version>\d+(\.\d+)*)$
ARG LIBXKBCOMMON_VERSION=1.13.0
RUN mkdir -p /libxkb
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://github.com/xkbcommon/libxkbcommon/archive/refs/tags/xkbcommon-${LIBXKBCOMMON_VERSION}.tar.gz -o libxkbcommon.tar.gz && tar -xzf libxkbcommon.tar.gz && rm libxkbcommon.tar.gz && mv libxkbcommon-* libxkbcommon-src
WORKDIR /build/libxkbcommon-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Denable-x11=false -Denable-xkbregistry=true -Denable-bash-completion=false
RUN DESTDIR=/libxkb ninja -C buildDir install


FROM toolchain AS pixman
# renovate: datasource=gitlab-tags depName=pixman/pixman registryUrl=https://gitlab.freedesktop.org extractVersion=^pixman-(?<version>\d+(\.\d+)*)$
ARG PIXMAN_VERSION=0.46.0
RUN mkdir -p /pixman
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://www.cairographics.org/releases/pixman-${PIXMAN_VERSION}.tar.gz -o pixman.tar.gz && tar -xzf pixman.tar.gz && rm pixman.tar.gz && mv pixman-* pixman-src
WORKDIR /build/pixman-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS}
RUN DESTDIR=/pixman ninja -C buildDir install


FROM toolchain AS libevdev
# renovate: datasource=gitlab-tags depName=libevdev/libevdev registryUrl=https://gitlab.freedesktop.org extractVersion=^libevdev-(?<version>\d+(\.\d+)*)$
ARG LIBEVDEV_VERSION=1.13.5
RUN mkdir -p /libevdev
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://gitlab.freedesktop.org/libevdev/libevdev/-/archive/libevdev-${LIBEVDEV_VERSION}/libevdev-libevdev-${LIBEVDEV_VERSION}.tar.gz -o libevdev.tar.gz && tar -xzf libevdev.tar.gz && rm libevdev.tar.gz && mv libevdev-* libevdev-src
WORKDIR /build/libevdev-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dtests=disabled -Ddocumentation=disabled -Dtools=disabled
RUN DESTDIR=/libevdev ninja -C buildDir install


FROM toolchain AS libinput
COPY --from=libucontext /libucontext /
COPY --from=libevdev /libevdev /
# renovate: datasource=gitlab-tags depName=libinput/libinput registryUrl=https://gitlab.freedesktop.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG LIBINPUT_VERSION=1.30.0
RUN mkdir -p /libinput
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://gitlab.freedesktop.org/libinput/libinput/-/archive/${LIBINPUT_VERSION}/libinput-${LIBINPUT_VERSION}.tar.gz -o libinput.tar.gz && tar -xf libinput.tar.gz && rm libinput.tar.gz && mv libinput-* libinput-src
WORKDIR /build/libinput-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dmtdev=false -Dlibwacom=false -Dtests=false -Ddebug-gui=false
RUN DESTDIR=/libinput ninja -C buildDir install


FROM toolchain AS hwdata
# renovate: datasource=github-tags depName=vcrhonek/hwdata extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG HWDATA_VERSION=0.401
RUN mkdir -p /hwdata
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://github.com/vcrhonek/hwdata/archive/v${HWDATA_VERSION}/hwdata-${HWDATA_VERSION}.tar.gz -o hwdata.tar.xz && tar -xf hwdata.tar.xz && rm hwdata.tar.xz && mv hwdata-* hwdata-src
WORKDIR /build/hwdata-src
RUN ./configure ${COMMON_CONFIGURE_ARGS} --disable-dependency-tracking
RUN make -j$(nproc) && make install DESTDIR=/hwdata


FROM toolchain AS libdisplay-info
COPY --from=hwdata /hwdata /
# renovate: datasource=gitlab-tags depName=emersion/libdisplay-info registryUrl=https://gitlab.freedesktop.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG LIBDISPLAY_INFO_VERSION=0.3.0
RUN mkdir -p /libdisplay-info
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://gitlab.freedesktop.org/emersion/libdisplay-info/-/releases/${LIBDISPLAY_INFO_VERSION}/downloads/libdisplay-info-${LIBDISPLAY_INFO_VERSION}.tar.xz -o libdisplay-info.tar.xz && tar -xf libdisplay-info.tar.xz && rm libdisplay-info.tar.xz && mv libdisplay-info-* libdisplay-info-src
WORKDIR /build/libdisplay-info-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS}
RUN DESTDIR=/libdisplay-info ninja -C buildDir install


# ===========================================================================
# Shared desktop libraries
# ===========================================================================

# PCRE2 — required by glib
FROM toolchain AS pcre2
# renovate: datasource=github-tags depName=PCRE2Project/pcre2 extractVersion=^pcre2-(?<version>\d+(\.\d+)*)$
ARG PCRE2_VERSION=10.44
RUN mkdir -p /pcre2
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://github.com/PCRE2Project/pcre2/releases/download/pcre2-${PCRE2_VERSION}/pcre2-${PCRE2_VERSION}.tar.gz -o pcre2.tar.gz && tar -xzf pcre2.tar.gz && rm pcre2.tar.gz && mv pcre2-* pcre2-src
WORKDIR /build/pcre2-src
RUN ./configure ${COMMON_CONFIGURE_ARGS} --disable-dependency-tracking --enable-pcre2-16 --enable-pcre2-32
RUN make -j$(nproc) && make install DESTDIR=/pcre2


# libpng — needed by grim (PNG screenshots)
FROM toolchain AS libpng
# renovate: datasource=github-tags depName=pnggroup/libpng extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG LIBPNG_VERSION=1.6.44
RUN mkdir -p /libpng
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://download.sourceforge.net/libpng/libpng-${LIBPNG_VERSION}.tar.xz -o libpng.tar.xz && tar -xf libpng.tar.xz && rm libpng.tar.xz && mv libpng-* libpng-src
WORKDIR /build/libpng-src
RUN ./configure ${COMMON_CONFIGURE_ARGS} --disable-dependency-tracking
# The Isotope cross-style target tuple causes libpng's generated DFA feature
# header to disable every read/write API. Use libpng's shipped, standard
# feature profile so Cairo, grim, and gdk-pixbuf get the normal PNG surface.
RUN sed -i 's/^pnglibconf\.h: pnglibconf\.out$/pnglibconf.h:/' Makefile && \
    cp scripts/pnglibconf.h.prebuilt pnglibconf.h && \
    make -j$(nproc) && make install DESTDIR=/libpng


# GLib — libffi/expat/zlib already provided by the toolchain
FROM toolchain AS glib2
COPY --from=pcre2 /pcre2 /
# renovate: datasource=gitlab-tags depName=GNOME/glib registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG GLIB_VERSION=2.82.5
RUN mkdir -p /glib2
WORKDIR /build
RUN GLIB_MAJOR="${GLIB_VERSION%.*}" && curl --retry 5 --retry-delay 3 --retry-all-errors -L https://download.gnome.org/sources/glib/${GLIB_MAJOR}/glib-${GLIB_VERSION}.tar.xz -o glib.tar.xz && tar -xf glib.tar.xz && rm glib.tar.xz && mv glib-* glib-src
WORKDIR /build/glib-src
RUN pip3 install meson ninja packaging
RUN meson setup buildDir ${COMMON_MESON_FLAGS} \
    -Dnls=disabled -Dtests=false -Dlibmount=disabled -Dselinux=disabled \
    -Dman-pages=disabled -Dintrospection=disabled -Dglib_debug=disabled \
    -Ddtrace=disabled -Dsysprof=disabled
RUN DESTDIR=/glib2 ninja -C buildDir install


# FreeType — built without harfbuzz (broken circular dep) and brotli. With
# libpng: Noto Color Emoji (the emoji stage) stores its glyphs as PNG images
# (CBDT), and a FreeType without PNG support cannot load them, so every app
# that draws text through FreeType (GTK, Pango, foot, fuzzel, dmenu, dunst)
# would show nothing for an emoji. libfreetype then links libpng16, so every
# stage that copies /freetype copies /libpng too (freetype2.pc names it), and
# every image already ships libpng.
FROM toolchain AS freetype
COPY --from=libpng /libpng /
RUN mkdir -p /freetype
WORKDIR /build
ARG FREETYPE_VERSION=2.13.3
ARG FREETYPE_SHA256=0550350666d427c74daeb85d5ac7bb353acba5f76956395995311a9c6f063289
# `-f` so a Savannah 5xx is not written to disk as the tarball: bare `curl -L`
# saved the HTML error page and the failure only surfaced later as the
# unhelpful `tar: invalid tar magic`. Savannah stays first as the canonical
# host but has returned 502 for days at a stretch, so SourceForge -- FreeType's
# other upstream distribution point -- is the fallback. The pinned checksum
# means neither host has to be trusted, matching how npth and sqlite are
# fetched in this Dockerfile.
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors \
      https://download.savannah.gnu.org/releases/freetype/freetype-${FREETYPE_VERSION}.tar.xz \
      -o freetype.tar.xz \
    || curl -fL --retry 5 --retry-delay 3 --retry-all-errors \
      https://downloads.sourceforge.net/freetype/freetype-${FREETYPE_VERSION}.tar.xz \
      -o freetype.tar.xz && \
    echo "${FREETYPE_SHA256}  freetype.tar.xz" | sha256sum -c - && \
    tar -xf freetype.tar.xz && rm freetype.tar.xz && mv freetype-* freetype-src
WORKDIR /build/freetype-src
RUN ./configure ${COMMON_CONFIGURE_ARGS} --disable-dependency-tracking --with-harfbuzz=no --with-png=yes --with-brotli=no --with-bzip2=no --with-zlib=yes
RUN make -j$(nproc) && make install DESTDIR=/freetype


# HarfBuzz — needs freetype + glib
FROM toolchain AS harfbuzz
COPY --from=freetype /freetype /
COPY --from=libpng /libpng /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
# renovate: datasource=github-tags depName=harfbuzz/harfbuzz extractVersion=^(?<version>\d+(\.\d+)*)$
ARG HARFBUZZ_VERSION=10.1.0
RUN mkdir -p /harfbuzz
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://github.com/harfbuzz/harfbuzz/releases/download/${HARFBUZZ_VERSION}/harfbuzz-${HARFBUZZ_VERSION}.tar.xz -o harfbuzz.tar.xz && tar -xf harfbuzz.tar.xz && rm harfbuzz.tar.xz && mv harfbuzz-* harfbuzz-src
WORKDIR /build/harfbuzz-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dtests=disabled -Ddocs=disabled -Dintrospection=disabled -Dglib=enabled -Dfreetype=enabled -Dcairo=disabled -Dchafa=disabled
RUN DESTDIR=/harfbuzz ninja -C buildDir install


# FriBidi — bidirectional text, needed by pango
FROM toolchain AS fribidi
# renovate: datasource=github-tags depName=fribidi/fribidi extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG FRIBIDI_VERSION=1.0.16
RUN mkdir -p /fribidi
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://github.com/fribidi/fribidi/releases/download/v${FRIBIDI_VERSION}/fribidi-${FRIBIDI_VERSION}.tar.xz -o fribidi.tar.xz && tar -xf fribidi.tar.xz && rm fribidi.tar.xz && mv fribidi-* fribidi-src
WORKDIR /build/fribidi-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dtests=false -Ddocs=false -Dbin=false
RUN DESTDIR=/fribidi ninja -C buildDir install


# gperf — build-time tool required by fontconfig
FROM toolchain AS gperf
ARG GPERF_VERSION=3.1
RUN mkdir -p /gperf
WORKDIR /build
RUN for m in https://mirrors.kernel.org/gnu https://ftpmirror.gnu.org https://ftp.gnu.org/gnu; do curl -fL --connect-timeout 30 --retry 3 --retry-delay 3 --retry-all-errors "$m/gperf/gperf-${GPERF_VERSION}.tar.gz" -o gperf.tar.gz && break; rm -f gperf.tar.gz; done && test -s gperf.tar.gz && tar -xzf gperf.tar.gz && rm gperf.tar.gz && mv gperf-* gperf-src
WORKDIR /build/gperf-src
# gperf 3.1 ships an ancient K&R getopt.c; the toolchain GCC defaults to C23
# where `()` means `(void)`, so force the older language semantics.
RUN ./configure ${COMMON_CONFIGURE_ARGS} --disable-dependency-tracking
RUN make -j$(nproc) CFLAGS="-std=gnu17 -O2 -g" && make install DESTDIR=/gperf


# Fontconfig (+ DejaVu fonts) — expat from toolchain, freetype built above
FROM toolchain AS fontconfig
COPY --from=freetype /freetype /
COPY --from=libpng /libpng /
COPY --from=gperf /gperf /
# renovate: datasource=gitlab-tags depName=fontconfig/fontconfig registryUrl=https://gitlab.freedesktop.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG FONTCONFIG_VERSION=2.15.0
RUN mkdir -p /fontconfig
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://www.freedesktop.org/software/fontconfig/release/fontconfig-${FONTCONFIG_VERSION}.tar.xz -o fontconfig.tar.xz && tar -xf fontconfig.tar.xz && rm fontconfig.tar.xz && mv fontconfig-* fontconfig-src
WORKDIR /build/fontconfig-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dtests=disabled -Dtools=disabled -Ddoc=disabled -Dnls=disabled -Dcache-build=disabled
RUN DESTDIR=/fontconfig ninja -C buildDir install
# Bundle DejaVu fonts
ARG DEJAVU_VERSION=2.37
RUN mkdir -p /fontconfig/usr/share/fonts/dejavu
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://github.com/dejavu-fonts/dejavu-fonts/releases/download/version_2_37/dejavu-fonts-ttf-${DEJAVU_VERSION}.tar.bz2 -o dejavu.tar.bz2 && \
    tar -xjf dejavu.tar.bz2 && \
    cp dejavu-fonts-ttf-*/ttf/*.ttf /fontconfig/usr/share/fonts/dejavu/ && \
    rm -rf dejavu.tar.bz2 dejavu-fonts-ttf-*


# Cairo — built with the FreeType + fontconfig backends (cairo-ft), required by
# pango. Defined here so freetype/fontconfig stages precede it.
FROM toolchain AS cairo
COPY --from=pixman /pixman /
COPY --from=freetype /freetype /
COPY --from=fontconfig /fontconfig /
COPY --from=libpng /libpng /
COPY --from=pcre2 /pcre2 /
COPY --from=glib2 /glib2 /
# 1.18.4 fixes the COLRv1 / FreeType 2.13.x cairo-ft detection bug.
# renovate: datasource=gitlab-tags depName=cairo/cairo registryUrl=https://gitlab.freedesktop.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG CAIRO_VERSION=1.18.4
RUN mkdir -p /cairo
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://cairographics.org/releases/cairo-${CAIRO_VERSION}.tar.xz -o cairo.tar.xz && tar -xf cairo.tar.xz && rm cairo.tar.xz && mv cairo-* cairo-src
WORKDIR /build/cairo-src
RUN pip3 install meson ninja
# PNG enabled so cairo_image_surface_create_from_png is available (swaybar/swaybg).
# glib enabled so cairo-gobject is built (required by GTK3 / waybar).
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dtests=disabled -Dglib=enabled -Dspectre=disabled \
    -Dfreetype=enabled -Dfontconfig=enabled -Dpng=enabled -Dxlib=disabled -Dxcb=disabled
RUN DESTDIR=/cairo ninja -C buildDir install


# Pango
FROM toolchain AS pango
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=harfbuzz /harfbuzz /
COPY --from=freetype /freetype /
COPY --from=fontconfig /fontconfig /
COPY --from=fribidi /fribidi /
COPY --from=cairo /cairo /
COPY --from=pixman /pixman /
COPY --from=libpng /libpng /
# renovate: datasource=gitlab-tags depName=GNOME/pango registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG PANGO_VERSION=1.54.0
RUN mkdir -p /pango
WORKDIR /build
RUN PANGO_MAJOR="${PANGO_VERSION%.*}" && curl --retry 5 --retry-delay 3 --retry-all-errors -L https://download.gnome.org/sources/pango/${PANGO_MAJOR}/pango-${PANGO_VERSION}.tar.xz -o pango.tar.xz && tar -xf pango.tar.xz && rm pango.tar.xz && mv pango-* pango-src
WORKDIR /build/pango-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dintrospection=disabled -Dgtk_doc=false -Dfontconfig=enabled -Dfreetype=enabled -Dcairo=enabled
RUN DESTDIR=/pango ninja -C buildDir install


# ===========================================================================
# GTK3 stack (for waybar). Wayland-only, NO GObject introspection (keeps it
# tractable on musl). Network/audio/bluetooth are handled by waybar's native
# modules + fuzzel menus over nmcli/wpctl/bluetoothctl — no nm-applet/pavucontrol.
# ===========================================================================

# libepoxy — GL/EGL dispatch used by GTK's GL renderer.
FROM toolchain AS libepoxy
COPY --from=mesa /mesa /
# renovate: datasource=github-tags depName=anholt/libepoxy extractVersion=^(?<version>\d+(\.\d+)*)$
ARG LIBEPOXY_VERSION=1.5.10
RUN mkdir -p /libepoxy
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/anholt/libepoxy/archive/refs/tags/${LIBEPOXY_VERSION}.tar.gz -o epoxy.tar.gz && tar -xf epoxy.tar.gz && rm epoxy.tar.gz && mv libepoxy-* epoxy-src
WORKDIR /build/epoxy-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dglx=no -Dx11=false -Degl=yes -Dtests=false
RUN DESTDIR=/libepoxy ninja -C buildDir install

# gdk-pixbuf — image loading (PNG only is plenty for the bar/applets).
FROM toolchain AS gdk-pixbuf
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=libpng /libpng /
# renovate: datasource=gitlab-tags depName=GNOME/gdk-pixbuf registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG GDKPIXBUF_VERSION=2.42.12
RUN mkdir -p /gdk-pixbuf
WORKDIR /build
RUN GP_MAJOR="${GDKPIXBUF_VERSION%.*}" && curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.gnome.org/sources/gdk-pixbuf/${GP_MAJOR}/gdk-pixbuf-${GDKPIXBUF_VERSION}.tar.xz -o gp.tar.xz && tar -xf gp.tar.xz && rm gp.tar.xz && mv gdk-pixbuf-* gp-src
WORKDIR /build/gp-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dtiff=disabled -Djpeg=disabled -Dothers=disabled \
    -Dintrospection=disabled -Dman=false -Ddocs=false -Dinstalled_tests=false -Dgio_sniffing=false
RUN DESTDIR=/gdk-pixbuf ninja -C buildDir install

# atk — accessibility toolkit (GTK links it; the at-spi2 bridge stays runtime-optional).
FROM toolchain AS atk
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
ARG ATK_VERSION=2.38.0
RUN mkdir -p /atk
WORKDIR /build
RUN ATK_MAJOR="${ATK_VERSION%.*}" && curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.gnome.org/sources/atk/${ATK_MAJOR}/atk-${ATK_VERSION}.tar.xz -o atk.tar.xz && tar -xf atk.tar.xz && rm atk.tar.xz && mv atk-* atk-src
WORKDIR /build/atk-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dintrospection=false
RUN DESTDIR=/atk ninja -C buildDir install

# gettext's tools (msgfmt, autopoint): polkit and accountsservice merge
# translations into their policy files at build time even with translations
# off, and Avahi's autoreconf needs autopoint and gettext's m4 macros.
# Build-only; nothing from this stage ships. Defined here, before the
# printing stages that GTK copies.
FROM toolchain AS gettext-tools
ARG GETTEXT_VERSION=0.22.5
WORKDIR /build
RUN for m in https://mirrors.kernel.org/gnu https://ftpmirror.gnu.org https://ftp.gnu.org/gnu; do curl -fL --connect-timeout 30 --retry 3 --retry-delay 3 --retry-all-errors "$m/gettext/gettext-${GETTEXT_VERSION}.tar.xz" -o g.tar.xz && break; rm -f g.tar.xz; done && test -s g.tar.xz && tar -xf g.tar.xz && \
    cd gettext-${GETTEXT_VERSION} && \
    ./configure --prefix=/usr --disable-java --disable-csharp --disable-libasprintf \
      --disable-openmp --disable-acl --disable-curses --without-emacs --without-git \
      --disable-static --enable-shared && \
    make -j"$(nproc)" && make install DESTDIR=/gettext-tools

# ---------------------------------------------------------------------------
# Printing, part one: what GTK's CUPS print backend links (libcups) and what
# CUPS needs to find printers (Avahi) and to talk to USB printers (libusb).
# Defined before gtk3, which copies cups. The rest (the filters, Ghostscript,
# cups-browsed, ipp-usb) and the `printing` stage that desktop-default copies
# are after lcms2, below. None of these stages is reached by the COSMIC
# prebuilt halves (tools/stage-key.py), so they do not rekey them. See
# docs/superpowers/specs/2026-11-24-printing-design.md.
# ---------------------------------------------------------------------------

# libusb: CUPS's usb backend and ipp-usb.
FROM toolchain AS libusb
COPY --from=libucontext /libucontext /
# renovate: datasource=github-releases depName=libusb/libusb extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG LIBUSB_VERSION=1.0.30
RUN mkdir -p /libusb
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/libusb/libusb/releases/download/v${LIBUSB_VERSION}/libusb-${LIBUSB_VERSION}.tar.bz2 -o u.tar.bz2 && \
    tar -xjf u.tar.bz2 && mv libusb-${LIBUSB_VERSION} src
WORKDIR /build/src
RUN cp /usr/share/automake-*/config.sub /usr/share/automake-*/config.guess . && \
    ./configure ${COMMON_CONFIGURE_ARGS} --enable-udev && \
    make -j"$(nproc)" && make DESTDIR=/libusb install && \
    L=/libusb/usr/share/licenses/libusb && install -d "$L" && cp -p $(ls -d LICENSE* COPYING* NOTICE* 2>/dev/null) "$L"/ && ls "$L"/* >/dev/null && \
    test -e /libusb/usr/lib/libusb-1.0.so.0

# libdaemon: avahi-daemon is not built without it. Its last release is from
# 2012 (a tarball only, so its checksum is pinned and the version is updated
# by hand); its config.sub predates musl, so automake's replaces it.
FROM toolchain AS libdaemon
ARG LIBDAEMON_VERSION=0.14
ARG LIBDAEMON_SHA256=fd23eb5f6f986dcc7e708307355ba3289abe03cc381fc47a80bca4a50aa6b834
RUN mkdir -p /libdaemon
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://0pointer.de/lennart/projects/libdaemon/libdaemon-${LIBDAEMON_VERSION}.tar.gz -o d.tar.gz && \
    echo "${LIBDAEMON_SHA256}  d.tar.gz" | sha256sum -c - && \
    tar -xzf d.tar.gz && mv libdaemon-${LIBDAEMON_VERSION} src
WORKDIR /build/src
RUN cp /usr/share/automake-*/config.sub /usr/share/automake-*/config.guess . && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-lynx --disable-examples && \
    make -j"$(nproc)" && make DESTDIR=/libdaemon install && \
    L=/libdaemon/usr/share/licenses/libdaemon && install -d "$L" && cp -p $(ls -d LICENSE* COPYING* NOTICE* 2>/dev/null) "$L"/ && ls "$L"/* >/dev/null && \
    test -e /libdaemon/usr/lib/libdaemon.so.0

# Avahi: mDNS/DNS-SD. avahi-daemon on the system bus; libavahi-client for
# CUPS, cups-browsed and ipp-usb; libavahi-glib for cups-browsed. Isotope
# System's Printers page browses through its D-Bus API. 0.9-rc5, as Alpine
# ships it: 0.8 (2020) has a dozen CVEs fixed only on the 0.9 line. A git
# tag, so autoreconf (with gettext-tools' autopoint). No nss-mdns: musl has
# no NSS (see the design note). No Python, Qt, GTK, GObject, gdbm,
# libevent, autoipd, compat libraries or man pages.
FROM toolchain AS avahi
COPY --from=libucontext /libucontext /
COPY --from=pcre2 /pcre2 /
COPY --from=glib2 /glib2 /
COPY --from=gettext-tools /gettext-tools /
COPY --from=libdaemon /libdaemon /
# renovate: datasource=github-tags depName=avahi/avahi extractVersion=^v(?<version>\d+\.\d+(\.\d+)?(-rc\d+)?)$
ARG AVAHI_VERSION=0.9-rc5
RUN mkdir -p /avahi
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/avahi/avahi/archive/refs/tags/v${AVAHI_VERSION}.tar.gz -o a.tar.gz && \
    tar -xzf a.tar.gz && mv avahi-${AVAHI_VERSION} src
WORKDIR /build/src
RUN autoreconf -vif
# --disable-stack-protector: Avahi's -fstack-protector links libssp.so.0, a
# toolchain library the images do not have (musl's libc has the stack
# protector itself), and cupsd then fails to load libavahi-common.
RUN ./configure ${COMMON_CONFIGURE_ARGS} --sysconfdir=/etc --localstatedir=/var --runstatedir=/run \
      --with-distro=none --disable-nls --enable-glib --disable-gobject --disable-libevent \
      --disable-stack-protector \
      --disable-qt3 --disable-qt4 --disable-qt5 --disable-gtk --disable-gtk3 \
      --disable-python --disable-pygobject --disable-python-dbus --disable-mono --disable-monodoc \
      --disable-doxygen-doc --disable-manpages --disable-xmltoman --disable-gdbm --disable-dbm \
      --disable-autoipd --disable-compat-libdns_sd --disable-compat-howl --disable-tests \
      --with-xml=expat --with-avahi-user=avahi --with-avahi-group=avahi \
      --with-dbus-sys=/usr/share/dbus-1/system.d \
      --with-systemdsystemunitdir=/usr/lib/systemd/system
RUN make -j"$(nproc)" && make DESTDIR=/avahi install && \
    L=/avahi/usr/share/licenses/avahi && install -d "$L" && cp -p $(ls -d LICENSE* COPYING* NOTICE* 2>/dev/null) "$L"/ && ls "$L"/* >/dev/null && \
    test -x /avahi/usr/sbin/avahi-daemon && \
    test -e /avahi/usr/lib/libavahi-client.so.3 && test -e /avahi/usr/lib/libavahi-glib.so.1 && \
    ! readelf -d /avahi/usr/lib/libavahi-*.so.* /avahi/usr/sbin/avahi-daemon | grep -q 'NEEDED.*libssp' && \
    test -f /avahi/usr/lib/systemd/system/avahi-daemon.service && \
    test -f /avahi/usr/lib/systemd/system/avahi-daemon.socket && \
    test -f /avahi/usr/share/dbus-1/system.d/avahi-dbus.conf && \
    rm -rf /avahi/run

# CUPS: the scheduler (cupsd, socket-activated by systemd), lpadmin and the
# other tools, libcups, the ipp, ipps, dnssd and usb backends. OpenSSL for
# ipps, Avahi for dnssd:// URIs and DNS-SD, PAM for the web interface's admin
# pages. Isotope's settings are rootfs/common/etc/cups (ServerRoot
# /var/lib/cups); the lpd server units are not wanted.
FROM toolchain AS cups
COPY --from=libucontext /libucontext /
COPY --from=libusb /libusb /
COPY --from=avahi /avahi /
# renovate: datasource=github-releases depName=OpenPrinting/cups extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG CUPS_VERSION=2.4.20
RUN mkdir -p /cups
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/OpenPrinting/cups/releases/download/v${CUPS_VERSION}/cups-${CUPS_VERSION}-source.tar.gz -o c.tar.gz && \
    tar -xzf c.tar.gz && mv cups-${CUPS_VERSION} src
WORKDIR /build/src
RUN ./configure --prefix=/usr --libdir=/usr/lib --sysconfdir=/etc --localstatedir=/var \
      --with-rundir=/run/cups --with-logdir=/var/log/cups --with-docdir=/usr/share/cups/doc \
      --with-domainsocket=/run/cups/cups.sock --with-cups-user=lp --with-cups-group=lp \
      --with-system-groups=root --with-tls=openssl --with-dnssd=avahi \
      --enable-libusb --enable-dbus --with-dbusdir=/usr/share/dbus-1 \
      --with-ondemand=systemd --with-systemd=/usr/lib/systemd/system \
      --enable-pam --disable-libpaper --disable-static --without-rcdir
RUN make -j"$(nproc)" && make BUILDROOT=/cups install && \
    L=/cups/usr/share/licenses/cups && install -d "$L" && cp -p $(ls -d LICENSE* COPYING* NOTICE* 2>/dev/null) "$L"/ && ls "$L"/* >/dev/null && \
    rm -rf /cups/run /cups/usr/lib/systemd/system/cups-lpd* && \
    test -x /cups/usr/sbin/cupsd && test -x /cups/usr/sbin/lpadmin && \
    test -e /cups/usr/lib/libcups.so.2 && test -x /cups/usr/lib/cups/backend/dnssd && \
    test -x /cups/usr/lib/cups/backend/ipp && test -e /cups/usr/lib/cups/backend/usb && \
    test -f /cups/usr/lib/systemd/system/cups.service && test -f /cups/usr/lib/systemd/system/cups.socket && \
    grep -q '^ListenStream=/run/cups/cups.sock' /cups/usr/lib/systemd/system/cups.socket

# GTK3 itself — wayland-only. With the CUPS print backend
# (libprintbackend-cups.so), so the print dialog lists the CUPS queues and
# the IPP printers on the network: print_backends is also compiled into
# libgtk as the default gtk-print-backends setting, so the module alone
# would not be loaded. gtk3 is not reached by the COSMIC prebuilt halves.
FROM toolchain AS gtk3
COPY --from=cups /cups /
COPY --from=avahi /avahi /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=cairo /cairo /
COPY --from=pango /pango /
COPY --from=harfbuzz /harfbuzz /
COPY --from=fribidi /fribidi /
COPY --from=freetype /freetype /
COPY --from=fontconfig /fontconfig /
COPY --from=libpng /libpng /
COPY --from=pixman /pixman /
COPY --from=gdk-pixbuf /gdk-pixbuf /
COPY --from=atk /atk /
COPY --from=libepoxy /libepoxy /
COPY --from=wayland /wayland /
COPY --from=libxkb /libxkb /
COPY --from=mesa /mesa /
# renovate: datasource=gitlab-tags depName=GNOME/gtk registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG GTK3_VERSION=3.24.43
RUN mkdir -p /gtk3
WORKDIR /build
RUN GTK_MAJOR="${GTK3_VERSION%.*}" && curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.gnome.org/sources/gtk+/${GTK_MAJOR}/gtk+-${GTK3_VERSION}.tar.xz -o gtk.tar.xz && tar -xf gtk.tar.xz && rm gtk.tar.xz && mv gtk+-* gtk-src
WORKDIR /build/gtk-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} \
    -Dx11_backend=false -Dwayland_backend=true -Dbroadway_backend=false \
    -Dintrospection=false -Dgtk_doc=false -Dman=false -Ddemos=false -Dexamples=false -Dtests=false \
    -Dprint_backends=file,cups -Dcolord=no
RUN DESTDIR=/gtk3 ninja -C buildDir install && \
    test -f /gtk3/usr/lib/gtk-3.0/3.0.0/printbackends/libprintbackend-cups.so

# --- gtkmm3 C++ wrappers (waybar links these) ------------------------------
FROM toolchain AS libsigcpp
# renovate: datasource=github-tags depName=libsigcplusplus/libsigcplusplus extractVersion=^(?<version>\d+(\.\d+)*)$
ARG LIBSIGCPP_VERSION=2.12.1
RUN mkdir -p /libsigcpp
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.gnome.org/sources/libsigc++/${LIBSIGCPP_VERSION%.*}/libsigc++-${LIBSIGCPP_VERSION}.tar.xz -o s.tar.xz && tar -xf s.tar.xz && rm s.tar.xz && mv libsigc++-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dbuild-documentation=false -Dbuild-examples=false
RUN DESTDIR=/libsigcpp ninja -C buildDir install

FROM toolchain AS glibmm
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=libsigcpp /libsigcpp /
# renovate: datasource=gitlab-tags depName=GNOME/glibmm registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG GLIBMM_VERSION=2.66.7
RUN mkdir -p /glibmm
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.gnome.org/sources/glibmm/${GLIBMM_VERSION%.*}/glibmm-${GLIBMM_VERSION}.tar.xz -o g.tar.xz && tar -xf g.tar.xz && rm g.tar.xz && mv glibmm-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dbuild-documentation=false
RUN DESTDIR=/glibmm ninja -C buildDir install

FROM toolchain AS cairomm
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=cairo /cairo /
COPY --from=freetype /freetype /
COPY --from=fontconfig /fontconfig /
COPY --from=libpng /libpng /
COPY --from=pixman /pixman /
COPY --from=libsigcpp /libsigcpp /
# renovate: datasource=gitlab-tags depName=cairo/cairomm registryUrl=https://gitlab.freedesktop.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG CAIROMM_VERSION=1.14.5
RUN mkdir -p /cairomm
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://gitlab.freedesktop.org/cairo/cairomm/-/archive/${CAIROMM_VERSION}/cairomm-${CAIROMM_VERSION}.tar.gz -o c.tar.gz && tar -xf c.tar.gz && rm c.tar.gz && mv cairomm-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dbuild-documentation=false -Dbuild-tests=false
RUN DESTDIR=/cairomm ninja -C buildDir install

FROM toolchain AS pangomm
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=cairo /cairo /
COPY --from=pango /pango /
COPY --from=harfbuzz /harfbuzz /
COPY --from=fribidi /fribidi /
COPY --from=freetype /freetype /
COPY --from=fontconfig /fontconfig /
COPY --from=libpng /libpng /
COPY --from=pixman /pixman /
COPY --from=libsigcpp /libsigcpp /
COPY --from=glibmm /glibmm /
COPY --from=cairomm /cairomm /
# renovate: datasource=gitlab-tags depName=GNOME/pangomm registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG PANGOMM_VERSION=2.46.4
RUN mkdir -p /pangomm
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.gnome.org/sources/pangomm/${PANGOMM_VERSION%.*}/pangomm-${PANGOMM_VERSION}.tar.xz -o p.tar.xz && tar -xf p.tar.xz && rm p.tar.xz && mv pangomm-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dbuild-documentation=false
RUN DESTDIR=/pangomm ninja -C buildDir install

FROM toolchain AS atkmm
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=atk /atk /
COPY --from=libsigcpp /libsigcpp /
COPY --from=glibmm /glibmm /
# renovate: datasource=gitlab-tags depName=GNOME/atkmm registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG ATKMM_VERSION=2.28.4
RUN mkdir -p /atkmm
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.gnome.org/sources/atkmm/${ATKMM_VERSION%.*}/atkmm-${ATKMM_VERSION}.tar.xz -o a.tar.xz && tar -xf a.tar.xz && rm a.tar.xz && mv atkmm-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dbuild-documentation=false
RUN DESTDIR=/atkmm ninja -C buildDir install

FROM toolchain AS gtkmm3
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=cairo /cairo /
COPY --from=pango /pango /
COPY --from=harfbuzz /harfbuzz /
COPY --from=fribidi /fribidi /
COPY --from=freetype /freetype /
COPY --from=fontconfig /fontconfig /
COPY --from=libpng /libpng /
COPY --from=pixman /pixman /
COPY --from=gdk-pixbuf /gdk-pixbuf /
COPY --from=atk /atk /
COPY --from=libepoxy /libepoxy /
COPY --from=wayland /wayland /
COPY --from=libxkb /libxkb /
COPY --from=mesa /mesa /
COPY --from=gtk3 /gtk3 /
COPY --from=libsigcpp /libsigcpp /
COPY --from=glibmm /glibmm /
COPY --from=cairomm /cairomm /
COPY --from=pangomm /pangomm /
COPY --from=atkmm /atkmm /
# renovate: datasource=gitlab-tags depName=GNOME/gtkmm registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG GTKMM_VERSION=3.24.9
RUN mkdir -p /gtkmm3
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.gnome.org/sources/gtkmm/${GTKMM_VERSION%.*}/gtkmm-${GTKMM_VERSION}.tar.xz -o gm.tar.xz && tar -xf gm.tar.xz && rm gm.tar.xz && mv gtkmm-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dbuild-documentation=false -Dbuild-demos=false -Dbuild-tests=false
RUN DESTDIR=/gtkmm3 ninja -C buildDir install

# --- waybar support libraries ----------------------------------------------
FROM toolchain AS gtk-layer-shell
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=cairo /cairo /
COPY --from=pango /pango /
COPY --from=harfbuzz /harfbuzz /
COPY --from=fribidi /fribidi /
COPY --from=freetype /freetype /
COPY --from=fontconfig /fontconfig /
COPY --from=libpng /libpng /
COPY --from=pixman /pixman /
COPY --from=gdk-pixbuf /gdk-pixbuf /
COPY --from=atk /atk /
COPY --from=libepoxy /libepoxy /
COPY --from=wayland /wayland /
COPY --from=libxkb /libxkb /
COPY --from=mesa /mesa /
COPY --from=gtk3 /gtk3 /
# renovate: datasource=github-tags depName=wmww/gtk-layer-shell extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG GTKLAYERSHELL_VERSION=0.8.2
RUN mkdir -p /gtk-layer-shell
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://github.com/wmww/gtk-layer-shell/archive/refs/tags/v${GTKLAYERSHELL_VERSION}.tar.gz -o g.tar.gz && tar -xf g.tar.gz && rm g.tar.gz && mv gtk-layer-shell-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dintrospection=false -Dvapi=false -Dexamples=false -Ddocs=false -Dtests=false
RUN DESTDIR=/gtk-layer-shell ninja -C buildDir install

FROM toolchain AS jsoncpp
# renovate: datasource=github-tags depName=open-source-parsers/jsoncpp extractVersion=^(?<version>\d+(\.\d+)*)$
ARG JSONCPP_VERSION=1.9.6
RUN mkdir -p /jsoncpp
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://github.com/open-source-parsers/jsoncpp/archive/refs/tags/${JSONCPP_VERSION}.tar.gz -o j.tar.gz && tar -xf j.tar.gz && rm j.tar.gz && mv jsoncpp-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dtests=false
RUN DESTDIR=/jsoncpp ninja -C buildDir install

FROM toolchain AS libfmt
# renovate: datasource=github-tags depName=fmtlib/fmt extractVersion=^(?<version>\d+(\.\d+)*)$
ARG FMT_VERSION=10.2.1
RUN mkdir -p /libfmt
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://github.com/fmtlib/fmt/archive/refs/tags/${FMT_VERSION}.tar.gz -o f.tar.gz && tar -xf f.tar.gz && rm f.tar.gz && mv fmt-* src
WORKDIR /build/src
RUN cmake -B buildDir -DCMAKE_INSTALL_PREFIX=/usr -DCMAKE_INSTALL_LIBDIR=lib -DCMAKE_BUILD_TYPE=MinSizeRel -DBUILD_SHARED_LIBS=ON -DFMT_TEST=OFF -DFMT_DOC=OFF
RUN cmake --build buildDir -j"$(nproc)" && DESTDIR=/libfmt cmake --install buildDir

FROM toolchain AS spdlog
COPY --from=libfmt /libfmt /
# renovate: datasource=github-tags depName=gabime/spdlog extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG SPDLOG_VERSION=1.13.0
RUN mkdir -p /spdlog
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://github.com/gabime/spdlog/archive/refs/tags/v${SPDLOG_VERSION}.tar.gz -o s.tar.gz && tar -xf s.tar.gz && rm s.tar.gz && mv spdlog-* src
WORKDIR /build/src
RUN cmake -B buildDir -DCMAKE_INSTALL_PREFIX=/usr -DCMAKE_INSTALL_LIBDIR=lib -DCMAKE_BUILD_TYPE=MinSizeRel -DBUILD_SHARED_LIBS=ON -DSPDLOG_FMT_EXTERNAL=ON -DSPDLOG_BUILD_EXAMPLE=OFF -DSPDLOG_BUILD_TESTS=OFF
RUN cmake --build buildDir -j"$(nproc)" && DESTDIR=/spdlog cmake --install buildDir

# Howard Hinnant date — waybar's clock module timezone support (header + tz lib).
# Provided as a system dep so waybar's meson doesn't reach out to wrapdb.
FROM toolchain AS cpp-date
# renovate: datasource=github-tags depName=HowardHinnant/date extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG DATE_VERSION=3.0.1
RUN mkdir -p /cpp-date
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://github.com/HowardHinnant/date/archive/refs/tags/v${DATE_VERSION}.tar.gz -o d.tar.gz && tar -xf d.tar.gz && rm d.tar.gz && mv date-* src
WORKDIR /build/src
RUN cmake -B buildDir -DCMAKE_INSTALL_PREFIX=/usr -DCMAKE_INSTALL_LIBDIR=lib -DCMAKE_BUILD_TYPE=MinSizeRel -DBUILD_SHARED_LIBS=ON -DBUILD_TZ_LIB=ON -DUSE_SYSTEM_TZ_DB=ON
RUN cmake --build buildDir -j"$(nproc)" && DESTDIR=/cpp-date cmake --install buildDir

# libdbusmenu-gtk3 — backend for waybar's SNI system tray (hosts external
# applet icons). The musl-from-source toolchain has no intltool/gettext, so we
# neuter the configure-time intltool version check (it only gates translation
# catalogs we don't ship) and build just the two library subdirs, skipping the
# po/ translation dir that actually needs intltool-merge at make time.
#
# The source and these stubs are shared by two builds: libdbusmenu, against
# the Wayland-only gtk3 (waybar on Sway), and libdbusmenu-gtk3-x11, against
# gtk3-x11 (snixembed on i3).
FROM toolchain AS libdbusmenu-src
ARG LIBDBUSMENU_VERSION=16.04.0
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://launchpad.net/libdbusmenu/16.04/${LIBDBUSMENU_VERSION}/+download/libdbusmenu-${LIBDBUSMENU_VERSION}.tar.gz -o d.tar.gz && tar -xf d.tar.gz && rm d.tar.gz && mv libdbusmenu-* src
WORKDIR /build/src
# Stub intltool: configure checks for intltool-update/merge/extract in PATH and
# parses --version. We don't build translations (only the two lib subdirs), so
# stubs that report a recent version and no-op are enough to get past configure.
RUN printf '#!/bin/sh\n[ "$1" = "--version" ] && echo "intltool-update (intltool) 0.51.0"\nexit 0\n' > /usr/bin/intltool-update \
    && printf '#!/bin/sh\nexit 0\n' > /usr/bin/intltool-merge \
    && printf '#!/bin/sh\nexit 0\n' > /usr/bin/intltool-extract \
    && chmod +x /usr/bin/intltool-update /usr/bin/intltool-merge /usr/bin/intltool-extract
# intltool also insists on GNU gettext tools; the toolchain has none. Stub the
# few configure probes for. po/ (which would actually run them) is not built.
RUN for t in msgfmt gmsgfmt xgettext msgmerge msgcat msguniq msginit msgconv msgen msgfilter; do \
        printf '#!/bin/sh\ncase "$1" in --version) echo "%s (GNU gettext-tools) 0.21" ;; esac\nexit 0\n' "$t" > /usr/bin/$t \
        && chmod +x /usr/bin/$t; \
    done
# intltool's configure probe `perl -e "require XML::Parser"`; provide a stub
# module so it resolves (only po/ would actually parse XML, and we skip po/).
RUN mkdir -p /usr/share/perl5/vendor_perl/XML \
    && printf 'package XML::Parser;\n$VERSION = "2.46";\n1;\n' > /usr/share/perl5/vendor_perl/XML/Parser.pm
# Disable the tests path leaves AM_CONDITIONALs (HAVE_VALGRIND, ...) defined only
# in that branch, so configure's "conditional X was never defined" sanity guards
# abort. Neuter those guards — the lib subdirs we build don't use the conditionals.
RUN sed -i 's/if test -z "${[A-Z_]*_TRUE}" && test -z "${[A-Z_]*_FALSE}"; then/if false; then/g' configure

FROM libdbusmenu-src AS libdbusmenu
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=cairo /cairo /
COPY --from=pango /pango /
COPY --from=harfbuzz /harfbuzz /
COPY --from=fribidi /fribidi /
COPY --from=freetype /freetype /
COPY --from=fontconfig /fontconfig /
COPY --from=libpng /libpng /
COPY --from=pixman /pixman /
COPY --from=gdk-pixbuf /gdk-pixbuf /
COPY --from=atk /atk /
COPY --from=libepoxy /libepoxy /
COPY --from=wayland /wayland /
COPY --from=libxkb /libxkb /
COPY --from=mesa /mesa /
COPY --from=gtk3 /gtk3 /
RUN mkdir -p /libdbusmenu
RUN ./configure ${COMMON_CONFIGURE_ARGS} --with-gtk=3 --disable-introspection --disable-vala --disable-gtk-doc --disable-dumper --disable-tests --disable-static --disable-nls
# This 2016 codebase uses G_TYPE_INSTANCE_GET_PRIVATE, now a deprecation that
# -Werror promotes to a hard error against modern GLib. Strip -Werror.
RUN find . -name Makefile -exec sed -i 's/-Werror//g' {} +
# Build/install only the libraries (libdbusmenu-glib + libdbusmenu-gtk); the
# po/, docs/ and tools/ subdirs need intltool/gtk-doc we don't have.
RUN make -C libdbusmenu-glib -j"$(nproc)" && make -C libdbusmenu-gtk -j"$(nproc)" \
    && make -C libdbusmenu-glib DESTDIR=/libdbusmenu install \
    && make -C libdbusmenu-gtk  DESTDIR=/libdbusmenu install

# wlroots — compositor library (shared with doom)
# libseat — built with the systemd-logind backend (libsystemd from toolchain).
# MUST be defined before wlroots: wlroots links it for DRM session management,
# and without it at build time wlroots disables the session backend entirely
# ("Cannot create session: disabled at compile-time"), so sway can never open
# DRM and gets bounced straight back to the login manager.
FROM toolchain AS libseat
COPY --from=libucontext /libucontext /
# renovate: datasource=github-tags depName=kennylevinsen/seatd
ARG SEATD_VERSION=0.9.1
RUN mkdir -p /libseat
WORKDIR /build
RUN curl -f --retry 5 --retry-delay 3 --retry-all-errors -L https://github.com/kennylevinsen/seatd/archive/refs/tags/${SEATD_VERSION}.tar.gz -o seatd.tar.gz && tar -xf seatd.tar.gz && rm seatd.tar.gz && mv seatd-* seatd-src
WORKDIR /build/seatd-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} \
    -Dlibseat-logind=systemd -Dlibseat-seatd=enabled -Dlibseat-builtin=enabled -Dserver=enabled -Dexamples=disabled -Dman-pages=disabled
RUN DESTDIR=/libseat ninja -C buildDir install


# The wlroots source, on its own so cage (the graphical installer's
# compositor) can build it as a subproject without the sway stack.
FROM toolchain AS wlroots-src
# renovate: datasource=gitlab-tags depName=wlroots/wlroots registryUrl=https://gitlab.freedesktop.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG WLROOTS_VERSION=0.18.2
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://gitlab.freedesktop.org/wlroots/wlroots/-/archive/${WLROOTS_VERSION}/wlroots-${WLROOTS_VERSION}.tar -o wlroots.tar && tar -xf wlroots.tar && rm wlroots.tar && mv wlroots-* wlroots-src


FROM toolchain AS wlroots
COPY --from=libucontext /libucontext /
RUN mkdir -p /wlroots
COPY --from=libseat /libseat /
COPY --from=wayland /wayland /
COPY --from=libdrm /libdrm /
COPY --from=libxkb /libxkb /
COPY --from=pixman /pixman /
COPY --from=libinput /libinput /
COPY --from=libevdev /libevdev /
COPY --from=mesa /mesa /
COPY --from=hwdata /hwdata /
COPY --from=libdisplay-info /libdisplay-info /
COPY --from=wlroots-src /build/wlroots-src /build/wlroots-src
WORKDIR /build/wlroots-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dexamples=false -Dwerror=false
RUN DESTDIR=/wlroots ninja -C buildDir install


# Sway
FROM toolchain AS sway
COPY --from=libucontext /libucontext /
COPY --from=wayland /wayland /
COPY --from=libdrm /libdrm /
COPY --from=libxkb /libxkb /
COPY --from=pixman /pixman /
COPY --from=libinput /libinput /
COPY --from=libevdev /libevdev /
COPY --from=mesa /mesa /
COPY --from=wlroots /wlroots /
COPY --from=libseat /libseat /
COPY --from=cairo /cairo /
COPY --from=pango /pango /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=harfbuzz /harfbuzz /
COPY --from=freetype /freetype /
COPY --from=fontconfig /fontconfig /
COPY --from=fribidi /fribidi /
COPY --from=hwdata /hwdata /
COPY --from=libdisplay-info /libdisplay-info /
COPY --from=libpng /libpng /
# renovate: datasource=github-tags depName=swaywm/sway extractVersion=^(?<version>\d+(\.\d+)*)$
ARG SWAY_VERSION=1.10.1
RUN mkdir -p /sway
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://github.com/swaywm/sway/releases/download/${SWAY_VERSION}/sway-${SWAY_VERSION}.tar.gz -o sway.tar.gz && tar -xzf sway.tar.gz && rm sway.tar.gz && mv sway-* sway-src
WORKDIR /build/sway-src
RUN pip3 install meson ninja
# swaybar/swaynag image loading uses cairo PNG (libpng now available).
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dman-pages=disabled -Dgdk-pixbuf=disabled -Dtray=disabled -Dswaybar=true -Dswaynag=true -Dwerror=false
RUN DESTDIR=/sway ninja -C buildDir install


# tllist — foot dependency (header-only-ish)
FROM toolchain AS tllist
# renovate: datasource=gitea-tags depName=dnkl/tllist registryUrl=https://codeberg.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG TLLIST_VERSION=1.1.0
RUN mkdir -p /tllist
WORKDIR /build
RUN curl -f --connect-timeout 30 --speed-limit 1024 --speed-time 60 --retry 5 --retry-delay 3 --retry-all-errors -L https://codeberg.org/dnkl/tllist/releases/download/${TLLIST_VERSION}/tllist-${TLLIST_VERSION}.tar.gz -o tllist.tar.gz && tar -xzf tllist.tar.gz && rm tllist.tar.gz && mv tllist* tllist-src
WORKDIR /build/tllist-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS}
RUN DESTDIR=/tllist ninja -C buildDir install


# fcft — font loading/glyph rasterization for foot (shaping disabled)
FROM toolchain AS fcft
COPY --from=freetype /freetype /
COPY --from=libpng /libpng /
COPY --from=fontconfig /fontconfig /
COPY --from=pixman /pixman /
COPY --from=harfbuzz /harfbuzz /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=tllist /tllist /
# renovate: datasource=gitea-tags depName=dnkl/fcft registryUrl=https://codeberg.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG FCFT_VERSION=3.3.1
RUN mkdir -p /fcft
WORKDIR /build
RUN curl -f --connect-timeout 30 --speed-limit 1024 --speed-time 60 --retry 5 --retry-delay 3 --retry-all-errors -L https://codeberg.org/dnkl/fcft/releases/download/${FCFT_VERSION}/fcft-${FCFT_VERSION}.tar.gz -o fcft.tar.gz && tar -xzf fcft.tar.gz && rm fcft.tar.gz && mv fcft* fcft-src
WORKDIR /build/fcft-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dtest-text-shaping=false -Dgrapheme-shaping=disabled -Drun-shaping=disabled -Ddocs=disabled
RUN DESTDIR=/fcft ninja -C buildDir install


# foot — Wayland terminal emulator
FROM toolchain AS foot
COPY --from=wayland /wayland /
COPY --from=libxkb /libxkb /
COPY --from=pixman /pixman /
COPY --from=freetype /freetype /
COPY --from=libpng /libpng /
COPY --from=fontconfig /fontconfig /
COPY --from=fcft /fcft /
COPY --from=tllist /tllist /
COPY --from=harfbuzz /harfbuzz /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
# renovate: datasource=gitea-tags depName=dnkl/foot registryUrl=https://codeberg.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG FOOT_VERSION=1.20.2
RUN mkdir -p /foot
WORKDIR /build
RUN curl -f --connect-timeout 30 --speed-limit 1024 --speed-time 60 --retry 5 --retry-delay 3 --retry-all-errors -L https://codeberg.org/dnkl/foot/releases/download/${FOOT_VERSION}/foot-${FOOT_VERSION}.tar.gz -o foot.tar.gz && tar -xzf foot.tar.gz && rm foot.tar.gz && mv foot* foot-src
WORKDIR /build/foot-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Ddocs=disabled -Dgrapheme-clustering=disabled -Dthemes=false -Dterminfo=disabled -Dwerror=false
RUN DESTDIR=/foot ninja -C buildDir install


# grim — Wayland screenshot tool (used by the test harness and desktop)
FROM toolchain AS grim
COPY --from=wayland /wayland /
COPY --from=pixman /pixman /
COPY --from=libpng /libpng /
# renovate: datasource=gitlab-tags depName=emersion/grim registryUrl=https://gitlab.freedesktop.org extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG GRIM_VERSION=1.4.1
RUN mkdir -p /grim
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://gitlab.freedesktop.org/emersion/grim/-/archive/v${GRIM_VERSION}/grim-v${GRIM_VERSION}.tar.gz -o grim.tar.gz && tar -xzf grim.tar.gz && rm grim.tar.gz && mv grim-* grim-src
WORKDIR /build/grim-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Djpeg=disabled -Dman-pages=disabled
RUN DESTDIR=/grim ninja -C buildDir install


# ===========================================================================
# M2: networking — NetworkManager + wpa_supplicant + wifi
# ===========================================================================

# libnl — netlink library, needed by wpa_supplicant/hostapd (nl80211)
FROM toolchain AS libnl
ARG LIBNL_VERSION=3.11.0
RUN mkdir -p /libnl
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://github.com/thom311/libnl/releases/download/libnl3_11_0/libnl-${LIBNL_VERSION}.tar.gz -o libnl.tar.gz && tar -xzf libnl.tar.gz && rm libnl.tar.gz && mv libnl-* libnl-src
WORKDIR /build/libnl-src
RUN ./configure ${COMMON_CONFIGURE_ARGS} --disable-dependency-tracking
RUN make -j$(nproc) && make install DESTDIR=/libnl


# wpa_supplicant — wifi backend for NetworkManager (D-Bus control interface)
FROM toolchain AS wpa-supplicant
COPY --from=libnl /libnl /
ARG WPA_VERSION=2.11
RUN mkdir -p /wpa
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://w1.fi/releases/wpa_supplicant-${WPA_VERSION}.tar.gz -o wpa.tar.gz && tar -xzf wpa.tar.gz && rm wpa.tar.gz && mv wpa_supplicant-* wpa-src
WORKDIR /build/wpa-src/wpa_supplicant
RUN printf '%s\n' \
    'CONFIG_DRIVER_NL80211=y' \
    'CONFIG_LIBNL32=y' \
    'CONFIG_CTRL_IFACE=y' \
    'CONFIG_CTRL_IFACE_DBUS_NEW=y' \
    'CONFIG_CTRL_IFACE_DBUS_INTRO=y' \
    'CONFIG_BACKEND=file' \
    'CONFIG_TLS=openssl' \
    'CONFIG_IEEE80211W=y' \
    'CONFIG_IEEE80211AC=y' \
    'CONFIG_AP=y' \
    'CONFIG_WPS=y' \
    'CONFIG_EAP=y' \
    'CONFIG_PKCS12=y' \
    'CONFIG_DEBUG_FILE=y' \
    > .config
# Toolchain OpenSSL is built with OPENSSL_NO_MD4; drop the (unused for WPA2-PSK)
# EVP_md4 reference so the crypto backend compiles and links.
RUN sed -i 's/EVP_md4()/NULL/g' ../src/crypto/crypto_openssl.c
# Install to /usr/bin (the base image is usrmerged: /usr/sbin -> /usr/bin).
RUN make -j$(nproc) BINDIR=/usr/bin
RUN make install DESTDIR=/wpa BINDIR=/usr/bin
# D-Bus + systemd service files so NetworkManager can use wpa_supplicant.
# Written explicitly (the upstream .in templates use @BINDIR@ placeholders).
RUN mkdir -p /wpa/usr/share/dbus-1/system-services /wpa/usr/share/dbus-1/system.d /wpa/usr/lib/systemd/system && \
    cp dbus/dbus-wpa_supplicant.conf /wpa/usr/share/dbus-1/system.d/wpa_supplicant.conf && \
    printf '%s\n' \
      '[D-BUS Service]' \
      'Name=fi.w1.wpa_supplicant1' \
      'Exec=/usr/bin/wpa_supplicant -u -O /run/wpa_supplicant' \
      'User=root' \
      'SystemdService=wpa_supplicant.service' \
      > /wpa/usr/share/dbus-1/system-services/fi.w1.wpa_supplicant1.service && \
    printf '%s\n' \
      '[Unit]' \
      'Description=WPA supplicant' \
      'Before=network.target' \
      'After=dbus.service' \
      'Wants=network.target' \
      '[Service]' \
      'Type=dbus' \
      'BusName=fi.w1.wpa_supplicant1' \
      'ExecStart=/usr/bin/wpa_supplicant -u -O /run/wpa_supplicant' \
      'ExecReload=/bin/kill -HUP $MAINPID' \
      '[Install]' \
      'WantedBy=multi-user.target' \
      'Alias=dbus-fi.w1.wpa_supplicant1.service' \
      > /wpa/usr/lib/systemd/system/wpa_supplicant.service


# hostapd — test-only AP for exercising wifi association against mac80211_hwsim
FROM toolchain AS hostapd
COPY --from=libnl /libnl /
ARG HOSTAPD_VERSION=2.11
RUN mkdir -p /hostapd
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://w1.fi/releases/hostapd-${HOSTAPD_VERSION}.tar.gz -o hostapd.tar.gz && tar -xzf hostapd.tar.gz && rm hostapd.tar.gz && mv hostapd-* hostapd-src
WORKDIR /build/hostapd-src/hostapd
RUN printf '%s\n' \
    'CONFIG_DRIVER_NL80211=y' \
    'CONFIG_LIBNL32=y' \
    'CONFIG_IEEE80211N=y' \
    'CONFIG_IEEE80211AC=y' \
    > .config
RUN sed -i 's/EVP_md4()/NULL/g' ../src/crypto/crypto_openssl.c
RUN make -j$(nproc) BINDIR=/usr/bin
RUN make install DESTDIR=/hostapd BINDIR=/usr/bin


# libndp — IPv6 router/neighbour discovery, required by NetworkManager
FROM toolchain AS libndp
# renovate: datasource=github-tags depName=jpirko/libndp extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG LIBNDP_VERSION=1.9
RUN mkdir -p /libndp
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://github.com/jpirko/libndp/archive/refs/tags/v${LIBNDP_VERSION}.tar.gz -o libndp.tar.gz && tar -xzf libndp.tar.gz && rm libndp.tar.gz && mv libndp-* libndp-src
WORKDIR /build/libndp-src
RUN ./autogen.sh 2>/dev/null || true
# libndp 1.9 has musl pointer-type mismatches that GCC 14+ treats as errors.
RUN ./configure ${COMMON_CONFIGURE_ARGS} --disable-dependency-tracking \
    CFLAGS="-O2 -g -Wno-error=incompatible-pointer-types -Wno-error=int-conversion"
RUN make -j$(nproc) && make install DESTDIR=/libndp


# wireless-regdb — regulatory database (loaded by cfg80211 from /lib/firmware)
FROM toolchain AS wireless-regdb
ARG WIRELESS_REGDB_VERSION=2024.10.07
# Hadron points both /lib/firmware and /usr/lib/firmware at this persistent,
# canonical directory. Writing the target directly avoids replacing either
# symlink when the scratch rootfs is overlaid onto the base image.
RUN mkdir -p /wireless-regdb/usr/local/lib/firmware
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://www.kernel.org/pub/software/network/wireless-regdb/wireless-regdb-${WIRELESS_REGDB_VERSION}.tar.xz -o regdb.tar.xz && tar -xf regdb.tar.xz && rm regdb.tar.xz && mv wireless-regdb-* regdb-src
RUN cp regdb-src/regulatory.db regdb-src/regulatory.db.p7s /wireless-regdb/usr/local/lib/firmware/


# ncurses — provides the termcap library (libtinfo) that the toolchain's
# libreadline needs; without it nmcli fails to link (undefined tgetent, tputs…).
FROM toolchain AS ncurses
ARG NCURSES_VERSION=6.5
RUN mkdir -p /ncurses
WORKDIR /build
RUN for m in https://mirrors.kernel.org/gnu https://ftpmirror.gnu.org https://ftp.gnu.org/gnu; do curl -fL --connect-timeout 30 --retry 3 --retry-delay 3 --retry-all-errors "$m/ncurses/ncurses-${NCURSES_VERSION}.tar.gz" -o ncurses.tar.gz && break; rm -f ncurses.tar.gz; done && test -s ncurses.tar.gz && tar -xzf ncurses.tar.gz && rm ncurses.tar.gz && mv ncurses-* ncurses-src
WORKDIR /build/ncurses-src
RUN ./configure ${COMMON_CONFIGURE_ARGS} --with-shared --enable-termcap --disable-widec \
    --without-debug --without-ada --without-cxx --without-cxx-binding \
    --without-tests --disable-stripping --without-manpages
# install.libs installs just the libraries/headers (libncurses); the full
# `install` cross-runs `tic` to build the terminfo DB, which fails here and is
# not needed (we only want the termcap symbols for readline).
RUN make -j$(nproc) libs && make install.libs install.includes DESTDIR=/ncurses


# S-Lang — the terminal library newt draws with. Only the library is kept:
# slsh and the S-Lang modules are not needed by nmtui. jedsoft.org publishes
# no checksums, so the tarball is checked against a SHA256 computed when
# pinned; a version bump must update SLANG_SHA256 too. The make is serial:
# the slang makefiles are not safe to run in parallel.
FROM toolchain AS slang
ARG SLANG_VERSION=2.3.3
ARG SLANG_SHA256=f9145054ae131973c61208ea82486d5dd10e3c5cdad23b7c4a0617743c8f5a18
RUN mkdir -p /slang
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://www.jedsoft.org/releases/slang/slang-${SLANG_VERSION}.tar.bz2 -o slang.tar.bz2 && \
    echo "${SLANG_SHA256}  slang.tar.bz2" | sha256sum -c - && \
    tar -xjf slang.tar.bz2 && rm slang.tar.bz2 && mv slang-* slang-src
WORKDIR /build/slang-src
RUN ./configure ${COMMON_CONFIGURE_ARGS} --without-png --without-pcre --without-onig --without-z --without-iconv
RUN make -C src elf && make -C src install-elf DESTDIR=/slang && make install-pkgconfig DESTDIR=/slang && \
    rm -rf /slang/usr/share/doc


# newt — the text widget library nmtui is built on. Upstream (gitlab.com/newt-ui)
# no longer uploads release tarballs, so this takes the tagged source from the
# GitHub mirror (mlichvar/newt, the maintainer's), checked against a SHA256
# computed when pinned. Tags are r0-52-25 for 0.52.25. Only the shared library
# is built: whiptail needs popt and nmtui does not need whiptail.
FROM toolchain AS newt
COPY --from=slang /slang /
ARG NEWT_VERSION=0.52.25
ARG NEWT_SHA256=6984c9bd754be2fbcf896a0a8bd5b659cd231935a3cce7b8554bfc25ceac91e7
RUN mkdir -p /newt
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://github.com/mlichvar/newt/archive/refs/tags/r$(echo ${NEWT_VERSION} | tr . -).tar.gz -o newt.tar.gz && \
    echo "${NEWT_SHA256}  newt.tar.gz" | sha256sum -c - && \
    tar -xzf newt.tar.gz && rm newt.tar.gz && mv newt-* newt-src
WORKDIR /build/newt-src
# The git tree has no configure script; autogen.sh would also build the
# translations, which need gettext tools and are off here (--disable-nls).
RUN autoheader && autoconf
RUN ./configure ${COMMON_CONFIGURE_ARGS} --without-python --without-tcl --disable-nls
RUN make -j$(nproc) sharedlib && make install-sh DESTDIR=/newt


# libxslt — provides xsltproc, a build-time requirement of NetworkManager
FROM toolchain AS libxslt
# renovate: datasource=gitlab-tags depName=GNOME/libxslt registryUrl=https://gitlab.gnome.org extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG LIBXSLT_VERSION=1.1.42
RUN mkdir -p /libxslt
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://download.gnome.org/sources/libxslt/${LIBXSLT_VERSION%.*}/libxslt-${LIBXSLT_VERSION}.tar.xz -o libxslt.tar.xz && tar -xf libxslt.tar.xz && rm libxslt.tar.xz && mv libxslt-* libxslt-src
WORKDIR /build/libxslt-src
RUN ./configure ${COMMON_CONFIGURE_ARGS} --disable-dependency-tracking --without-python
RUN make -j$(nproc) && make install DESTDIR=/libxslt


# libmm-glib, ModemManager's client library, for NetworkManager's WWAN device
# plugin. ModemManager itself is the `modemmanager` extension
# (extensions/modemmanager), which links its own copy from a private
# directory; this one is only what NetworkManager loads. Built from
# ModemManager's source without QMI, MBIM, udev, polkit or systemd: none of
# them changes the client library. /mm-glib-dev is the whole install, for the
# networkmanager stage's build; that stage copies libmm-glib.so.0 into the
# images.
FROM toolchain AS mm-glib
COPY --from=libucontext /libucontext /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
# xsltproc: include/meson.build makes ModemManager-names.h with it.
COPY --from=libxslt /libxslt /
# renovate: datasource=gitlab-tags depName=mobile-broadband/ModemManager registryUrl=https://gitlab.freedesktop.org extractVersion=^(?<version>\d+\.\d+\.\d+)$
ARG MODEMMANAGER_VERSION=1.24.2
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://gitlab.freedesktop.org/mobile-broadband/ModemManager/-/archive/${MODEMMANAGER_VERSION}/ModemManager-${MODEMMANAGER_VERSION}.tar.gz -o mm.tar.gz && \
    tar -xzf mm.tar.gz && rm mm.tar.gz && mv ModemManager-* mm-src
WORKDIR /build/mm-src
RUN pip3 install meson ninja packaging
RUN meson setup buildDir ${COMMON_MESON_FLAGS} \
    -Dudev=false -Dudevdir=/usr/lib/udev -Dmbim=false -Dqmi=false -Dqrtr=false \
    -Dpolkit=no -Dsystemdsystemunitdir=no -Dsystemd_suspend_resume=false -Dsystemd_journal=false \
    -Dintrospection=false -Dvapi=false -Dman=false -Dgtk_doc=false -Dbash_completion=false \
    -Dtests=false -Dexamples=false
RUN DESTDIR=/mm-glib-dev ninja -C buildDir install && \
    install -Dm644 COPYING.LIB /mm-glib-dev/usr/share/licenses/ModemManager/COPYING.LIB && \
    test -e /mm-glib-dev/usr/lib/libmm-glib.so.0 && test -e /mm-glib-dev/usr/lib/pkgconfig/mm-glib.pc


# NetworkManager. With ModemManager support (the WWAN device plugin and
# libnm-wwan, which link libmm-glib): the binary exports only the internal
# symbols the plugins of its own build use (tools/create-exports-NetworkManager.sh),
# so the plugin cannot come from another stage. This stage is not reached by
# the COSMIC prebuilt keys (cosmic-shell, cosmic-apps), so it changes in place.
# libmm-glib.so.0 goes into /networkmanager, so every image that copies
# NetworkManager has what its plugin loads. Without the `modemmanager`
# extension there is no ModemManager on the bus and no modem device. No pppd:
# modems that only work over a PPP serial link do not connect.
FROM toolchain AS networkmanager
COPY --from=libucontext /libucontext /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=mm-glib /mm-glib-dev /
COPY --from=libndp /libndp /
COPY --from=libnl /libnl /
COPY --from=libxslt /libxslt /
COPY --from=ncurses /ncurses /
COPY --from=slang /slang /
COPY --from=newt /newt /
# The toolchain's libreadline needs termcap symbols from libncurses; make the
# readline pkg-config dependency pull it in so nmcli links cleanly.
RUN sed -i 's/-lreadline/-lreadline -lncurses/' /usr/lib/pkgconfig/readline.pc /usr/lib64/pkgconfig/readline.pc 2>/dev/null || true
# renovate: datasource=gitlab-tags depName=NetworkManager/NetworkManager registryUrl=https://gitlab.freedesktop.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG NM_VERSION=1.50.0
RUN mkdir -p /networkmanager
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://download.gnome.org/sources/NetworkManager/${NM_VERSION%.*}/NetworkManager-${NM_VERSION}.tar.xz -o nm.tar.xz && tar -xf nm.tar.xz && rm nm.tar.xz && mv NetworkManager-* nm-src
WORKDIR /build/nm-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} --sbindir=bin \
    -Dpolkit=false -Dwifi=true -Diwd=false -Dwext=false \
    -Dnmcli=true -Dnmtui=true -Dmodem_manager=true -Dppp=false -Dovs=false \
    -Dmobile_broadband_provider_info_database=/usr/share/mobile-broadband-provider-info/serviceproviders.xml \
    -Dintrospection=false -Ddocs=false -Dtests=no -Dlibpsl=false -Dqt=false \
    -Dselinux=false -Dvapi=false -Difcfg_rh=false -Dlibaudit=no \
    -Dsession_tracking=systemd -Dsuspend_resume=systemd -Dsystemd_journal=true \
    -Dcrypto=null -Dconfig_dns_rc_manager_default=symlink \
    -Dc_link_args=-lncurses
RUN DESTDIR=/networkmanager ninja -C buildDir install
RUN mkdir -p /networkmanager/usr/share/licenses/ModemManager && \
    cp -a /usr/lib/libmm-glib.so.0* /networkmanager/usr/lib/ && \
    cp /usr/share/licenses/ModemManager/COPYING.LIB /networkmanager/usr/share/licenses/ModemManager/ && \
    test -e /networkmanager/usr/lib/libmm-glib.so.0 && \
    test -e /networkmanager/usr/lib/NetworkManager/${NM_VERSION}/libnm-device-plugin-wwan.so && \
    test -e /networkmanager/usr/lib/NetworkManager/${NM_VERSION}/libnm-wwan.so


# OpenConnect, the company VPN client (Cisco AnyConnect, Palo Alto
# GlobalProtect, Fortinet, Juniper and Pulse): libopenconnect and the
# `openconnect` CLI, on the toolchain's OpenSSL and libxml2. The tray's
# NetworkManager secret agent runs `openconnect --authenticate` as the user
# to get the login cookie, and nm-openconnect-service runs it as the tunnel.
# The optional extras are off: libproxy, stoken (RSA SecurID), libpcsclite
# (YubiKey OATH), libpskc and GSSAPI. Runtime files only; the stage checks it.
# Its own stage, copied next to the networkmanager stage, so that stage and
# the COSMIC prebuilt keys that may reach it stay as they are.
FROM toolchain AS openconnect
# renovate: datasource=gitlab-tags depName=openconnect/openconnect extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG OPENCONNECT_VERSION=9.21
ARG OPENCONNECT_SHA256=5b32369467db6e5f317aa1ed12cfcbb81ed00bdbc765450b6bfcbdc300944a58
RUN mkdir -p /openconnect
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://www.infradead.org/openconnect/download/openconnect-${OPENCONNECT_VERSION}.tar.gz -o oc.tar.gz && \
    echo "${OPENCONNECT_SHA256}  oc.tar.gz" | sha256sum -c - && \
    tar -xzf oc.tar.gz && rm oc.tar.gz && mv openconnect-* openconnect-src
WORKDIR /build/openconnect-src
# The toolchain's OpenSSL has no RC4. It is only for --allow-insecure-crypto
# (3DES and RC4, for very old servers), which then reports itself unavailable.
RUN sed -i 's/EVP_rc4()/NULL/' openssl.c && ! grep -q 'EVP_rc4' openssl.c
RUN ./configure ${COMMON_CONFIGURE_ARGS} --sbindir=/usr/bin \
      --without-gnutls --with-vpnc-script=/usr/share/vpnc-scripts/vpnc-script \
      --without-libproxy --without-stoken --without-libpcsclite --without-libpskc \
      --without-gssapi --without-java --with-builtin-json --disable-nls
# /openconnect-dev is the whole install, for nm-openconnect's build (its
# libnm plugin includes openconnect.h); /openconnect is what the images get.
RUN make -j$(nproc) && make install DESTDIR=/openconnect-dev
RUN mkdir -p /openconnect/usr/bin /openconnect/usr/lib /openconnect/usr/share/licenses/openconnect && \
    cp -a /openconnect-dev/usr/bin/openconnect /openconnect/usr/bin/ && \
    cp -a /openconnect-dev/usr/lib/libopenconnect.so.* /openconnect/usr/lib/ && \
    cp COPYING.LGPL /openconnect/usr/share/licenses/openconnect/ && \
    LD_LIBRARY_PATH=/openconnect/usr/lib /openconnect/usr/bin/openconnect --version 2>&1 | tee /tmp/v && \
    grep -q 'Using OpenSSL' /tmp/v && \
    ! find /openconnect \( -name '*.h' -o -name '*.pc' -o -name '*.cmake' -o -name '*.a' -o -name '*.la' \) | grep -q .


# vpnc-script, the script `openconnect` runs from the terminal to set up the
# tunnel's addresses, routes and DNS (NetworkManager uses its own helper).
# vpnc-scripts has no release tags: bump VPNC_SCRIPTS_COMMIT and the SHA256
# together.
FROM toolchain AS vpnc-script
ARG VPNC_SCRIPTS_COMMIT=ce9e961bd0f6b867e1c7c35f78f6fb973f6ff101
ARG VPNC_SCRIPT_SHA256=f0c4d936a382f07711263242699b5e2d85d1ace37136bb78785d352997c17742
RUN mkdir -p /vpnc-script/usr/share/vpnc-scripts && \
    curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://gitlab.com/openconnect/vpnc-scripts/-/raw/${VPNC_SCRIPTS_COMMIT}/vpnc-script -o /vpnc-script/usr/share/vpnc-scripts/vpnc-script && \
    echo "${VPNC_SCRIPT_SHA256}  /vpnc-script/usr/share/vpnc-scripts/vpnc-script" | sha256sum -c - && \
    chmod 0755 /vpnc-script/usr/share/vpnc-scripts/vpnc-script


# NetworkManager-openconnect: the VPN service plugin (nm-openconnect-service,
# its helper, the D-Bus policy and the .name file). No GNOME editor and no
# GTK auth dialog: the tray's secret agent (cmd/isotope/openconnect.go) does the
# auth dialog's job and returns the cookie, gateway and gwcert secrets.
# configure checks for WebKitGTK even without the auth dialog that uses it;
# setting WEBKIT_CFLAGS/LIBS skips that check (nothing built here reads them).
# It also wants file(1), which the toolchain lacks and the build never runs.
# The service runs openconnect as the nm-openconnect user, which
# /usr/lib/sysusers.d/nm-openconnect.conf creates (the kairos stage).
FROM toolchain AS nm-openconnect
COPY --from=libucontext /libucontext /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=networkmanager /networkmanager /
COPY --from=openconnect /openconnect-dev /
# renovate: datasource=gitlab-tags depName=GNOME/NetworkManager-openconnect registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG NM_OPENCONNECT_VERSION=1.2.10
ARG NM_OPENCONNECT_SHA256=844b6bf64ecadb97b4a68c776db89aa5e6ee7e59bd24b0180228406863136464
RUN mkdir -p /nm-openconnect
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://download.gnome.org/sources/NetworkManager-openconnect/${NM_OPENCONNECT_VERSION%.*}/NetworkManager-openconnect-${NM_OPENCONNECT_VERSION}.tar.xz -o nmoc.tar.xz && \
    echo "${NM_OPENCONNECT_SHA256}  nmoc.tar.xz" | sha256sum -c - && \
    tar -xf nmoc.tar.xz && rm nmoc.tar.xz && mv NetworkManager-openconnect-* nmoc-src
WORKDIR /build/nmoc-src
# The toolchain has no intltool or gettext; configure only probes them (no
# translations are built: po/ is left out of SUBDIRS).
RUN printf '#!/bin/sh\n[ "$1" = "--version" ] && echo "intltool-update (intltool) 0.51.0"\nexit 0\n' > /usr/bin/intltool-update \
    && printf '#!/bin/sh\nexit 0\n' > /usr/bin/intltool-merge \
    && printf '#!/bin/sh\nexit 0\n' > /usr/bin/intltool-extract \
    && chmod +x /usr/bin/intltool-update /usr/bin/intltool-merge /usr/bin/intltool-extract \
    && for t in msgfmt gmsgfmt xgettext msgmerge; do \
        printf '#!/bin/sh\ncase "$1" in --version) echo "%s (GNU gettext-tools) 0.21" ;; esac\nexit 0\n' "$t" > /usr/bin/$t && chmod +x /usr/bin/$t; \
    done \
    && mkdir -p /usr/share/perl5/vendor_perl/XML \
    && printf 'package XML::Parser;\n$VERSION = "2.46";\n1;\n' > /usr/share/perl5/vendor_perl/XML/Parser.pm
RUN sed -i 's/^SUBDIRS = \(.*\) po$/SUBDIRS = \1/' Makefile.in && grep -q '^SUBDIRS = \.$' Makefile.in && \
    ./configure ${COMMON_CONFIGURE_ARGS} --sysconfdir=/etc --localstatedir=/var \
      --without-gnome --without-authdlg --without-gtk4 --without-libnm-glib --disable-nls \
      WEBKIT_CFLAGS=-DISOTOPE_NO_WEBKIT WEBKIT_LIBS=-DISOTOPE_NO_WEBKIT ac_cv_prog_has_file=yes
RUN make -j$(nproc) && make install DESTDIR=/nm-openconnect
RUN rm -rf /nm-openconnect/usr/include /nm-openconnect/usr/lib/pkgconfig /nm-openconnect/usr/share/locale \
      /nm-openconnect/usr/share/metainfo /nm-openconnect/usr/share/doc && \
    find /nm-openconnect -name '*.la' -delete && \
    mkdir -p /nm-openconnect/usr/lib/sysusers.d /nm-openconnect/usr/share/licenses/NetworkManager-openconnect && \
    printf 'u nm-openconnect - "NetworkManager OpenConnect" - -\n' > /nm-openconnect/usr/lib/sysusers.d/nm-openconnect.conf && \
    cp COPYING /nm-openconnect/usr/share/licenses/NetworkManager-openconnect/ && \
    test -x /nm-openconnect/usr/libexec/nm-openconnect-service && \
    test -x /nm-openconnect/usr/libexec/nm-openconnect-service-openconnect-helper && \
    grep -q '^program=/usr/libexec/nm-openconnect-service$' /nm-openconnect/usr/lib/NetworkManager/VPN/nm-openconnect-service.name && \
    ! test -e /nm-openconnect/usr/libexec/nm-openconnect-auth-dialog && \
    ! find /nm-openconnect \( -name '*.h' -o -name '*.pc' -o -name '*.cmake' -o -name '*.a' \) | grep -q .


# ===========================================================================
# M3: audio — PipeWire + WirePlumber
# ===========================================================================

# ALSA library
FROM toolchain AS alsa-lib
# renovate: datasource=github-tags depName=alsa-project/alsa-lib extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG ALSA_VERSION=1.2.12
RUN mkdir -p /alsa-lib
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://www.alsa-project.org/files/pub/lib/alsa-lib-${ALSA_VERSION}.tar.bz2 -o alsa-lib.tar.bz2 && tar -xjf alsa-lib.tar.bz2 && rm alsa-lib.tar.bz2 && mv alsa-lib-* alsa-lib-src
WORKDIR /build/alsa-lib-src
RUN ./configure ${COMMON_CONFIGURE_ARGS} --disable-dependency-tracking --disable-python
RUN make -j$(nproc) && make install DESTDIR=/alsa-lib


# ALSA UCM configuration (data files describing sound cards)
FROM toolchain AS alsa-ucm-conf
# renovate: datasource=github-tags depName=alsa-project/alsa-ucm-conf extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG ALSA_UCM_VERSION=1.2.12
RUN mkdir -p /alsa-ucm-conf
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://www.alsa-project.org/files/pub/lib/alsa-ucm-conf-${ALSA_UCM_VERSION}.tar.bz2 -o ucm.tar.bz2 && tar -xjf ucm.tar.bz2 && rm ucm.tar.bz2 && mv alsa-ucm-conf-* ucm-src
RUN mkdir -p /alsa-ucm-conf/usr/share/alsa && cp -a ucm-src/ucm2 /alsa-ucm-conf/usr/share/alsa/


# sbc — Bluetooth A2DP audio codec (used by PipeWire's bluez5 plugin)
FROM toolchain AS sbc
ARG SBC_VERSION=2.0
RUN mkdir -p /sbc
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://www.kernel.org/pub/linux/bluetooth/sbc-${SBC_VERSION}.tar.xz -o sbc.tar.xz && tar -xf sbc.tar.xz && rm sbc.tar.xz && mv sbc-* sbc-src
WORKDIR /build/sbc-src
RUN ./configure ${COMMON_CONFIGURE_ARGS} --disable-dependency-tracking --disable-tester
RUN make -j$(nproc) && make install DESTDIR=/sbc


# Bluetooth audio codecs for PipeWire's bluez5 plugin: aptX, LDAC, AAC, LC3 and
# Opus. Each stage installs the full build output to /<name>-dev (headers and
# pkg-config files, only for building the pipewire-codecs stage) and the
# runtime files to /<name> (the stripped shared library and its licence, the
# only tree an image copies). A stage fails if a header or .pc file leaks
# into /<name>.
#
# libfreeaptx — aptX and aptX HD (LGPL-2.1+)
FROM toolchain AS libfreeaptx
# renovate: datasource=github-tags depName=iamthehorker/libfreeaptx extractVersion=^(?<version>\d+(\.\d+)*)$
ARG LIBFREEAPTX_VERSION=0.2.2
RUN mkdir -p /libfreeaptx /libfreeaptx-dev
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/iamthehorker/libfreeaptx/archive/refs/tags/${LIBFREEAPTX_VERSION}.tar.gz -o a.tar.gz && tar -xzf a.tar.gz && rm a.tar.gz && mv libfreeaptx-* src
WORKDIR /build/src
RUN make -j$(nproc) CC=gcc PREFIX=/usr LIBDIR=lib && make install CC=gcc PREFIX=/usr LIBDIR=lib DESTDIR=/libfreeaptx-dev && \
    install -d /libfreeaptx/usr/lib && cp -a /libfreeaptx-dev/usr/lib/libfreeaptx.so* /libfreeaptx/usr/lib/ && \
    find /libfreeaptx/usr/lib -type f -exec strip --strip-unneeded {} + && \
    install -Dm0644 COPYING /libfreeaptx/usr/share/licenses/libfreeaptx/COPYING && \
    ! find /libfreeaptx \( -name '*.h' -o -name '*.pc' -o -name '*.cmake' -o -name '*.a' \) | grep -q . && \
    ls /libfreeaptx/usr/lib/libfreeaptx.so.*


# libldac — LDAC encoder and adaptive bitrate library (Apache-2.0). EHfive/ldacBT
# is the CMake build of Android's libldac; its source is a git submodule, which
# a tag tarball leaves empty, so the pinned submodule commit comes from
# android.googlesource.com. Bump LDAC_COMMIT with the ldacBT tag (the commit the
# tag's `libldac` submodule points to).
FROM toolchain AS libldac
# renovate: datasource=github-tags depName=EHfive/ldacBT extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG LDACBT_VERSION=2.0.2.6
ARG LDAC_COMMIT=82b6a1abee84787b8fa167efe20290073f60db2d
RUN mkdir -p /libldac /libldac-dev
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/EHfive/ldacBT/archive/refs/tags/v${LDACBT_VERSION}.tar.gz -o a.tar.gz && tar -xzf a.tar.gz && rm a.tar.gz && mv ldacBT-* src && \
    curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://android.googlesource.com/platform/external/libldac/+archive/${LDAC_COMMIT}.tar.gz -o l.tar.gz && tar -xzf l.tar.gz -C src/libldac && rm l.tar.gz
WORKDIR /build/src
RUN cmake -G "Unix Makefiles" -B build -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/usr -DINSTALL_LIBDIR=/usr/lib && \
    cmake --build build -j$(nproc) && DESTDIR=/libldac-dev cmake --install build && \
    install -d /libldac/usr/lib && cp -a /libldac-dev/usr/lib/libldacBT_enc.so* /libldac-dev/usr/lib/libldacBT_abr.so* /libldac/usr/lib/ && \
    find /libldac/usr/lib -type f -exec strip --strip-unneeded {} + && \
    install -Dm0644 LICENSE /libldac/usr/share/licenses/libldac/LICENSE && \
    install -Dm0644 libldac/NOTICE /libldac/usr/share/licenses/libldac/NOTICE && \
    ! find /libldac \( -name '*.h' -o -name '*.pc' -o -name '*.cmake' -o -name '*.a' \) | grep -q . && \
    ls /libldac/usr/lib/libldacBT_enc.so.* /libldac/usr/lib/libldacBT_abr.so.*


# liblc3 — LC3 and LC3plus-free low complexity codec for LE Audio (Apache-2.0)
FROM toolchain AS liblc3
# renovate: datasource=github-tags depName=google/liblc3 extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG LIBLC3_VERSION=1.1.3
RUN mkdir -p /liblc3 /liblc3-dev
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/google/liblc3/archive/refs/tags/v${LIBLC3_VERSION}.tar.gz -o a.tar.gz && tar -xzf a.tar.gz && rm a.tar.gz && mv liblc3-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dtools=false -Dpython=false && \
    DESTDIR=/liblc3-dev ninja -C buildDir install && \
    install -d /liblc3/usr/lib && cp -a /liblc3-dev/usr/lib/liblc3.so* /liblc3/usr/lib/ && \
    find /liblc3/usr/lib -type f -exec strip --strip-unneeded {} + && \
    install -Dm0644 LICENSE /liblc3/usr/share/licenses/liblc3/LICENSE && \
    ! find /liblc3 \( -name '*.h' -o -name '*.pc' -o -name '*.cmake' -o -name '*.a' \) | grep -q . && \
    ls /liblc3/usr/lib/liblc3.so.*


# fdk-aac — AAC encoder and decoder for the AAC Bluetooth codec. This is the
# fdk-aac-stripped source (the Android tree of the Fraunhofer FDK AAC codec,
# which Fedora ships as fdk-aac-free), not mstorsjo/fdk-aac or a distro's
# "nonfree" build. Its licence is the Android one: it lets us redistribute the
# binary with the licence text, and the text is what /usr/share/licenses
# carries (NOTICE). The PipeWire plugin is the only linker; the library itself
# is a separate shared object, so the licence does not reach PipeWire's own
# MIT licence. The licence grants no patent rights: AAC patent licensing is the
# user's matter, as in every distro that ships this library.
FROM toolchain AS fdk-aac
# renovate: datasource=gitlab-tags depName=wtaymans/fdk-aac-stripped registryUrl=https://gitlab.freedesktop.org extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG FDK_AAC_VERSION=2.0.0
RUN mkdir -p /fdk-aac /fdk-aac-dev
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://gitlab.freedesktop.org/wtaymans/fdk-aac-stripped/-/archive/v${FDK_AAC_VERSION}/fdk-aac-stripped-v${FDK_AAC_VERSION}.tar.gz -o a.tar.gz && tar -xzf a.tar.gz && rm a.tar.gz && mv fdk-aac-stripped-* src
WORKDIR /build/src
RUN autoreconf -fiv && ./configure ${COMMON_CONFIGURE_ARGS} --disable-dependency-tracking --disable-example
RUN make -j$(nproc) && make install DESTDIR=/fdk-aac-dev && \
    install -d /fdk-aac/usr/lib && cp -a /fdk-aac-dev/usr/lib/libfdk-aac.so* /fdk-aac/usr/lib/ && \
    find /fdk-aac/usr/lib -type f -exec strip --strip-unneeded {} + && \
    install -Dm0644 NOTICE /fdk-aac/usr/share/licenses/fdk-aac/NOTICE && \
    ! find /fdk-aac \( -name '*.h' -o -name '*.pc' -o -name '*.cmake' -o -name '*.a' -o -name '*.la' \) | grep -q . && \
    ls /fdk-aac/usr/lib/libfdk-aac.so.*


# opus — Opus codec for the Bluetooth Opus codec and PipeWire's RTP modules (BSD-3-Clause).
# The release tarball, not the GitHub tag: it carries the generated DNN data and package_version that a tag lacks.
FROM toolchain AS opus
# renovate: datasource=github-tags depName=xiph/opus extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG OPUS_VERSION=1.6.1
RUN mkdir -p /opus /opus-dev
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://downloads.xiph.org/releases/opus/opus-${OPUS_VERSION}.tar.gz -o a.tar.gz && tar -xzf a.tar.gz && rm a.tar.gz && mv opus-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Ddocs=disabled -Dtests=disabled -Dextra-programs=disabled && \
    DESTDIR=/opus-dev ninja -C buildDir install && \
    install -d /opus/usr/lib && cp -a /opus-dev/usr/lib/libopus.so* /opus/usr/lib/ && \
    find /opus/usr/lib -type f -exec strip --strip-unneeded {} + && \
    install -Dm0644 COPYING /opus/usr/share/licenses/opus/COPYING && \
    ! find /opus \( -name '*.h' -o -name '*.pc' -o -name '*.cmake' -o -name '*.a' \) | grep -q . && \
    ls /opus/usr/lib/libopus.so.*


# BlueZ — bluetoothd, bluetoothctl, btvirt (virtual HCI for the test)
FROM toolchain AS bluez
COPY --from=libucontext /libucontext /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=ncurses /ncurses /
RUN sed -i 's/-lreadline/-lreadline -lncurses/' /usr/lib/pkgconfig/readline.pc /usr/lib64/pkgconfig/readline.pc 2>/dev/null || true
# renovate: datasource=github-tags depName=bluez/bluez extractVersion=^(?<version>\d+(\.\d+)*)$
ARG BLUEZ_VERSION=5.79
RUN mkdir -p /bluez
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://www.kernel.org/pub/linux/bluetooth/bluez-${BLUEZ_VERSION}.tar.xz -o bluez.tar.xz && tar -xf bluez.tar.xz && rm bluez.tar.xz && mv bluez-* bluez-src
WORKDIR /build/bluez-src
# MAX_INPUT is gated behind _GNU_SOURCE in musl's <limits.h>; define it directly.
RUN ./configure ${COMMON_CONFIGURE_ARGS} --disable-dependency-tracking \
    --enable-client --enable-tools --enable-deprecated --enable-library \
    --enable-systemd --enable-udev --disable-obex --disable-mesh --disable-cups \
    --disable-manpages --enable-testing --disable-nfc --disable-sap --disable-midi \
    --with-dbusconfdir=/usr/share --with-systemdsystemunitdir=/usr/lib/systemd/system \
    --with-systemduserunitdir=/usr/lib/systemd/user \
    CFLAGS="-O2 -g -DMAX_INPUT=255" LIBS="-lncurses"
RUN make -j$(nproc) && make install DESTDIR=/bluez
# btvirt is a tool not installed by default; place it on PATH for the test
RUN cp -f tools/btvirt /bluez/usr/bin/ 2>/dev/null || cp -f emulator/btvirt /bluez/usr/bin/ 2>/dev/null || true


# PipeWire
FROM toolchain AS pipewire
COPY --from=libucontext /libucontext /
COPY --from=alsa-lib /alsa-lib /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=sbc /sbc /
COPY --from=bluez /bluez /
# renovate: datasource=gitlab-tags depName=pipewire/pipewire registryUrl=https://gitlab.freedesktop.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG PIPEWIRE_VERSION=1.2.7
RUN mkdir -p /pipewire
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://gitlab.freedesktop.org/pipewire/pipewire/-/archive/${PIPEWIRE_VERSION}/pipewire-${PIPEWIRE_VERSION}.tar.gz -o pipewire.tar.gz && tar -xzf pipewire.tar.gz && rm pipewire.tar.gz && mv pipewire-* pipewire-src
WORKDIR /build/pipewire-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} \
    -Dalsa=enabled -Dpipewire-alsa=enabled \
    -Ddbus=enabled -Dsystemd=enabled -Dsystemd-user-service=enabled \
    -Dbluez5=enabled -Djack=disabled -Dvulkan=disabled -Dv4l2=disabled \
    -Dlibcamera=disabled -Dexamples=disabled -Dtests=disabled -Dman=disabled \
    -Dgstreamer=disabled -Dsndfile=disabled -Dreadline=disabled -Draop=disabled \
    -Dlibpulse=disabled -Davahi=disabled -Dx11=disabled -Dlegacy-rtkit=true \
    -Dudev=enabled -Dsession-managers=[]
RUN DESTDIR=/pipewire ninja -C buildDir install


# PipeWire with the Bluetooth codecs. The pipewire stage stays as it is, because
# the COSMIC prebuilt halves are looked up by the key of every stage they reach
# and cosmic-build reaches pipewire: new codec flags there would rekey them
# and cost a 2.5 h rebuild of each. This stage reconfigures the same source and
# build directory with the codec options and installs the result; the images
# copy it over /pipewire. What it ships is the runtime files: no headers,
# pkg-config or CMake files.
FROM toolchain AS pipewire-codecs
COPY --from=libucontext /libucontext /
COPY --from=alsa-lib /alsa-lib /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=sbc /sbc /
COPY --from=bluez /bluez /
COPY --from=libfreeaptx /libfreeaptx-dev /
COPY --from=libldac /libldac-dev /
COPY --from=liblc3 /liblc3-dev /
COPY --from=fdk-aac /fdk-aac-dev /
COPY --from=opus /opus-dev /
COPY --from=pipewire /build/pipewire-src /build/pipewire-src
RUN mkdir -p /pipewire-codecs
WORKDIR /build/pipewire-src
RUN pip3 install meson ninja
RUN meson configure buildDir \
    -Dbluez5-codec-aptx=enabled -Dbluez5-codec-ldac=enabled -Dbluez5-codec-lc3=enabled \
    -Dbluez5-codec-aac=enabled -Dbluez5-codec-opus=enabled -Dbluez5-codec-lc3plus=disabled \
    -Dopus=enabled
RUN DESTDIR=/pipewire-codecs ninja -C buildDir install
RUN rm -rf /pipewire-codecs/usr/include /pipewire-codecs/usr/lib/pkgconfig /pipewire-codecs/usr/lib/cmake && \
    d=/pipewire-codecs/usr/lib/spa-0.2/bluez5 && ls $d && \
    for c in sbc aac aptx ldac lc3 opus; do test -e $d/libspa-codec-bluez5-$c.so || { echo "missing codec plugin $c"; exit 1; }; done && \
    ! find /pipewire-codecs \( -name '*.h' -o -name '*.pc' -o -name '*.cmake' \) | grep -q .


# WirePlumber (session manager; uses its bundled Lua)
FROM toolchain AS wireplumber
COPY --from=libucontext /libucontext /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=pipewire /pipewire /
COPY --from=alsa-lib /alsa-lib /
# renovate: datasource=gitlab-tags depName=pipewire/wireplumber registryUrl=https://gitlab.freedesktop.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG WIREPLUMBER_VERSION=0.5.7
RUN mkdir -p /wireplumber
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://gitlab.freedesktop.org/pipewire/wireplumber/-/archive/${WIREPLUMBER_VERSION}/wireplumber-${WIREPLUMBER_VERSION}.tar.gz -o wireplumber.tar.gz && tar -xzf wireplumber.tar.gz && rm wireplumber.tar.gz && mv wireplumber-* wireplumber-src
WORKDIR /build/wireplumber-src
RUN pip3 install meson ninja
# Pre-seed meson's wrap cache with lua (the wrap fetches it from www.lua.org,
# which is frequently unreachable). MacPorts mirrors the identical tarball
# (sha 164c7849…), and meson verifies the hash so the patch still applies.
RUN mkdir -p subprojects/packagecache && \
    curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://distfiles.macports.org/lua/lua-5.4.4.tar.gz -o subprojects/packagecache/lua-5.4.4.tar.gz
RUN meson setup buildDir ${COMMON_MESON_FLAGS} \
    -Dsystem-lua=false -Dintrospection=disabled -Ddoc=disabled -Dtests=false \
    -Delogind=disabled -Dsystemd=enabled -Dsystemd-user-service=true -Dsystemd-system-service=false
RUN DESTDIR=/wireplumber ninja -C buildDir install

# --- waybar — the status bar / panel ---------------------------------------
# Native modules drive NetworkManager / WirePlumber / BlueZ directly (clickable
# wifi, volume, bluetooth), plus an SNI system tray (libdbusmenu) for external
# applet icons. Click actions reuse the variant-aware Isotope network/audio menus.
# Defined after wireplumber/pipewire so every COPY --from resolves backwards.
FROM toolchain AS waybar
COPY --from=libucontext /libucontext /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=cairo /cairo /
COPY --from=pango /pango /
COPY --from=harfbuzz /harfbuzz /
COPY --from=fribidi /fribidi /
COPY --from=freetype /freetype /
COPY --from=fontconfig /fontconfig /
COPY --from=libpng /libpng /
COPY --from=pixman /pixman /
COPY --from=gdk-pixbuf /gdk-pixbuf /
COPY --from=atk /atk /
COPY --from=libepoxy /libepoxy /
COPY --from=wayland /wayland /
COPY --from=libxkb /libxkb /
COPY --from=mesa /mesa /
COPY --from=gtk3 /gtk3 /
COPY --from=libsigcpp /libsigcpp /
COPY --from=glibmm /glibmm /
COPY --from=cairomm /cairomm /
COPY --from=pangomm /pangomm /
COPY --from=atkmm /atkmm /
COPY --from=gtkmm3 /gtkmm3 /
COPY --from=gtk-layer-shell /gtk-layer-shell /
COPY --from=jsoncpp /jsoncpp /
COPY --from=libfmt /libfmt /
COPY --from=spdlog /spdlog /
COPY --from=libnl /libnl /
COPY --from=cpp-date /cpp-date /
COPY --from=libdbusmenu /libdbusmenu /
COPY --from=pipewire /pipewire /
COPY --from=wireplumber /wireplumber /
# renovate: datasource=github-tags depName=Alexays/Waybar extractVersion=^(?<version>\d+(\.\d+)*)$
ARG WAYBAR_VERSION=0.10.4
RUN mkdir -p /waybar
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://github.com/Alexays/Waybar/archive/refs/tags/${WAYBAR_VERSION}.tar.gz -o w.tar.gz && tar -xf w.tar.gz && rm w.tar.gz && mv Waybar-* src
WORKDIR /build/src
RUN pip3 install meson ninja
# Enable the modules the desktop uses (tray=dbusmenu-gtk, network=libnl,
# audio=wireplumber); gtk-layer-shell is a hard dep. Disable the rest so meson
# doesn't reach for libs we don't ship (pulse/jack/mpd/sndio/cava).
RUN meson setup buildDir ${COMMON_MESON_FLAGS} \
      -Dlibcxx=false -Ddbusmenu-gtk=enabled -Dlibnl=enabled \
      -Dwireplumber=enabled -Dpulseaudio=disabled \
      -Dmpris=disabled -Djack=disabled -Dmpd=disabled -Dsndio=disabled \
      -Dcava=disabled -Dexperimental=false -Dman-pages=disabled -Dtests=disabled
RUN DESTDIR=/waybar ninja -C buildDir install

# Nerd Font symbols — waybar/foot glyphs (wifi/bt/volume icons). Symbols-only
# subset keeps it small; installed to /usr/share/fonts and picked up by fc-cache.
FROM toolchain AS nerdfont
# renovate: datasource=github-tags depName=ryanoasis/nerd-fonts extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG NERDFONT_VERSION=3.1.1
RUN mkdir -p /nerdfont/usr/share/fonts/nerd-fonts
WORKDIR /tmp
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://github.com/ryanoasis/nerd-fonts/releases/download/v${NERDFONT_VERSION}/NerdFontsSymbolsOnly.tar.xz -o n.tar.xz \
    && tar -xf n.tar.xz -C /nerdfont/usr/share/fonts/nerd-fonts SymbolsNerdFont-Regular.ttf SymbolsNerdFontMono-Regular.ttf \
    && rm n.tar.xz

# The Isotope typefaces: Inter for the interface, JetBrains Mono for terminals,
# both SIL OFL 1.1, with their licences. Data only: the variable TTFs (one file
# covers every weight), no static cuts, no web fonts. rootfs/common/etc/fonts/conf.d/
# 60-isotope.conf makes them the sans-serif and monospace defaults; DejaVu and
# the Nerd Font symbols stay as fallbacks. Neither project publishes checksums,
# so the release zips are checked against SHA256s computed when pinned: a
# Renovate bump fails here until the PR updates the matching *_SHA256 too.
# The toolchain has no unzip; Python's zipfile module extracts the zips.
FROM toolchain AS isotope-fonts
# renovate: datasource=github-releases depName=rsms/inter extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG INTER_VERSION=4.1
ARG INTER_SHA256=9883fdd4a49d4fb66bd8177ba6625ef9a64aa45899767dde3d36aa425756b11e
# renovate: datasource=github-releases depName=JetBrains/JetBrainsMono extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG JETBRAINS_MONO_VERSION=2.304
ARG JETBRAINS_MONO_SHA256=6f6376c6ed2960ea8a963cd7387ec9d76e3f629125bc33d1fdcd7eb7012f7bbf
WORKDIR /tmp
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/rsms/inter/releases/download/v${INTER_VERSION}/Inter-${INTER_VERSION}.zip -o inter.zip && \
    echo "${INTER_SHA256}  inter.zip" | sha256sum -c - && \
    curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/JetBrains/JetBrainsMono/releases/download/v${JETBRAINS_MONO_VERSION}/JetBrainsMono-${JETBRAINS_MONO_VERSION}.zip -o jbm.zip && \
    echo "${JETBRAINS_MONO_SHA256}  jbm.zip" | sha256sum -c - && \
    python3 -m zipfile -e inter.zip inter && python3 -m zipfile -e jbm.zip jbm && \
    f=/isotope-fonts/usr/share/fonts l=/isotope-fonts/usr/share/licenses && \
    install -Dm0644 -t $f/inter inter/InterVariable.ttf inter/InterVariable-Italic.ttf && \
    install -Dm0644 -t $l/inter inter/LICENSE.txt && \
    install -Dm0644 -t $f/jetbrains-mono 'jbm/fonts/variable/JetBrainsMono[wght].ttf' 'jbm/fonts/variable/JetBrainsMono-Italic[wght].ttf' && \
    install -Dm0644 -t $l/jetbrains-mono jbm/OFL.txt && \
    rm -rf inter.zip jbm.zip inter jbm

# Emoji, on every variant: the Noto Color Emoji font (SIL OFL 1.1) and the
# list that isotope-emoji, the emoji picker, shows. Data only. 60-isotope.conf
# adds the font as the last fallback of the generic families and as the
# "emoji" family. The font's glyphs are PNG images (CBDT), which is why the
# freetype stage is built with libpng.
# noto-emoji publishes no font in its release assets, so the font and its
# licence come from the tagged tree. The list is made from Unicode's
# emoji-test.txt by tools/gen-emoji-list.sh: one "<emoji> <name>" line per
# fully-qualified emoji, in /usr/share/isotope/emoji.txt. Nothing publishes
# checksums, so all three files are checked against SHA256s computed when
# pinned: a Renovate bump of NOTO_EMOJI_VERSION fails here until the PR
# updates NOTO_EMOJI_SHA256 too. Unicode has no tags Renovate can read; bump
# UNICODE_EMOJI_VERSION by hand with the font, once a year when Unicode
# releases (the font must have glyphs for the emoji the list shows).
FROM toolchain AS emoji
# renovate: datasource=github-releases depName=googlefonts/noto-emoji extractVersion=^v(?<version>\d+\.\d+)$
ARG NOTO_EMOJI_VERSION=2.051
ARG NOTO_EMOJI_SHA256=72a635cb3d2f3524c51620cdde406b217204e8a6a06c6a096ff8ed4b5fd6e27b
ARG NOTO_EMOJI_LICENSE_SHA256=6a73f9541c2de74158c0e7cf6b0a58ef774f5a780bf191f2d7ec9cc53efe2bf2
ARG UNICODE_EMOJI_VERSION=17.0.0
ARG UNICODE_EMOJI_SHA256=1d8a944f88d7952f7ef7c5167fef3c67995bcae24543949710231b03a201acda
COPY tools/gen-emoji-list.sh /build/gen-emoji-list.sh
WORKDIR /tmp
RUN n=https://raw.githubusercontent.com/googlefonts/noto-emoji/v${NOTO_EMOJI_VERSION} && \
    curl --retry 5 --retry-delay 3 --retry-all-errors -fL $n/fonts/NotoColorEmoji.ttf -o NotoColorEmoji.ttf && \
    echo "${NOTO_EMOJI_SHA256}  NotoColorEmoji.ttf" | sha256sum -c - && \
    curl --retry 5 --retry-delay 3 --retry-all-errors -fL $n/fonts/LICENSE -o OFL.txt && \
    echo "${NOTO_EMOJI_LICENSE_SHA256}  OFL.txt" | sha256sum -c - && \
    curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://www.unicode.org/Public/${UNICODE_EMOJI_VERSION}/emoji/emoji-test.txt -o emoji-test.txt && \
    echo "${UNICODE_EMOJI_SHA256}  emoji-test.txt" | sha256sum -c - && \
    install -Dm0644 -t /emoji/usr/share/fonts/noto-emoji NotoColorEmoji.ttf && \
    install -Dm0644 -t /emoji/usr/share/licenses/noto-emoji OFL.txt && \
    mkdir -p /emoji/usr/share/isotope && \
    sh /build/gen-emoji-list.sh emoji-test.txt > /emoji/usr/share/isotope/emoji.txt && \
    # A format change in emoji-test.txt must fail here, not ship an empty
    # picker: Unicode 17 has 3944 fully-qualified emoji.
    [ "$(wc -l < /emoji/usr/share/isotope/emoji.txt)" -gt 3000 ] && \
    grep -qx '😀 grinning face' /emoji/usr/share/isotope/emoji.txt && \
    rm -f NotoColorEmoji.ttf OFL.txt emoji-test.txt

# The desktop wallpaper is the release's (Isotope 1, Hydrogen), committed in
# rootfs/common/usr/share/backgrounds/isotope/ and reached through the stable name
# isotope-desktop-night.png; tools/wallpapers/ renders it and the build
# wallpaper a development build points that link at instead. Drawn here at
# build time, from theme/palette.toml by tools/gen-wallpaper.py (stdlib
# Python, fixed dither seed, no network): the tiles, the fallback, and the
# plain night image swaylock uses; niri and COSMIC, whose shells have a light
# mode, also get the day tiles. The 4K images are scaled down to fit smaller
# screens.
FROM toolchain AS isotope-wallpapers
COPY theme/palette.toml /build/theme/palette.toml
COPY tools/gen-wallpaper.py /build/tools/gen-wallpaper.py
RUN python3 /build/tools/gen-wallpaper.py /isotope-wallpapers/usr/share/backgrounds/isotope \
      isotope-tiles-night-3840x2160 isotope-plain-night-1920x1080

FROM toolchain AS isotope-wallpapers-day
COPY theme/palette.toml /build/theme/palette.toml
COPY tools/gen-wallpaper.py /build/tools/gen-wallpaper.py
RUN python3 /build/tools/gen-wallpaper.py /isotope-wallpapers-day/usr/share/backgrounds/isotope \
      isotope-tiles-day-3840x2160

# The COSMIC login screen's soft wallpapers: isotope-desktop-{night,day}-blur.png,
# a Gaussian blur (sigma 40 px at 4K) of the two desktop wallpapers at a
# quarter of their size, which the cosmic-greeter patch shows in place of the
# sharp picture (patches/cosmic/cosmic-greeter). gen-wallpaper.py --blur, stdlib
# only, about 3 s each. A development build makes its own in build-wallpaper.
FROM toolchain AS isotope-wallpapers-blur
COPY tools/gen-wallpaper.py /build/tools/gen-wallpaper.py
COPY rootfs/common/usr/share/backgrounds/isotope/ /build/wallpapers/
RUN for mode in night day; do \
      python3 /build/tools/gen-wallpaper.py --blur /build/wallpapers/isotope-desktop-$mode.png \
        /isotope-wallpapers-blur/usr/share/backgrounds/isotope/isotope-desktop-$mode-blur.png || exit 1; \
    done

# --- docker (static bundle) — rootful OCI containers ------------------------
# Docker's daemon + CLI + containerd + runc ship as fully-static Go binaries in
# the official static bundle (a musl-friendly, no-libc-dependency pattern).
# dockerd spawns its own containerd child when --containerd is not given, so a
# single docker.service is enough. The base image already provides iptables-nft
# and the kernel ships nf_conntrack/bridge/br_netfilter, so docker's bridge+NAT
# networking works without vendoring a separate network stack.
FROM toolchain AS docker
# renovate: datasource=github-tags depName=docker/cli extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG DOCKER_VERSION=29.5.3
RUN mkdir -p /docker/usr/bin
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://download.docker.com/linux/static/stable/x86_64/docker-${DOCKER_VERSION}.tgz -o d.tgz \
    && tar -xf d.tgz && rm d.tgz \
    && cp -a docker/dockerd docker/docker docker/containerd docker/containerd-shim-runc-v2 docker/runc docker/docker-init docker/docker-proxy docker/ctr /docker/usr/bin/ \
    && rm -rf docker

# --- distrobox — mutable dev-environment containers on docker ---------------
# distrobox is pure POSIX shell (no toolchain). Its install script lays the
# distrobox/distrobox-* scripts into <prefix>/bin and shared data into
# <prefix>/share. It drives the `docker` CLI directly as its container backend.
FROM toolchain AS distrobox
# renovate: datasource=github-tags depName=89luca89/distrobox extractVersion=^(?<version>\d+(\.\d+)*)$
ARG DISTROBOX_VERSION=1.8.2.5
RUN mkdir -p /distrobox/usr
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://github.com/89luca89/distrobox/archive/refs/tags/${DISTROBOX_VERSION}.tar.gz -o db.tgz \
    && tar -xf db.tgz && rm db.tgz && mv distrobox-* src
WORKDIR /build/src
RUN ./install --prefix /distrobox/usr

# --- flatpak support libraries ---------------------------------------------
# libarchive — OCI/bundle handling for ostree + flatpak (zlib/lzma/zstd/openssl
# all come from the toolchain).
FROM toolchain AS libarchive
# renovate: datasource=github-tags depName=libarchive/libarchive extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG LIBARCHIVE_VERSION=3.8.7
RUN mkdir -p /libarchive
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://github.com/libarchive/libarchive/releases/download/v${LIBARCHIVE_VERSION}/libarchive-${LIBARCHIVE_VERSION}.tar.gz -o a.tar.gz && tar -xf a.tar.gz && rm a.tar.gz && mv libarchive-* src
WORKDIR /build/src
RUN ./configure ${COMMON_CONFIGURE_ARGS} --disable-bsdtar --disable-bsdcpio --disable-bsdcat --without-xml2 --without-expat
RUN make -j"$(nproc)" && make DESTDIR=/libarchive install

FROM toolchain AS json-glib
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
# renovate: datasource=gitlab-tags depName=GNOME/json-glib registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG JSONGLIB_VERSION=1.10.8
RUN mkdir -p /json-glib
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://download.gnome.org/sources/json-glib/${JSONGLIB_VERSION%.*}/json-glib-${JSONGLIB_VERSION}.tar.xz -o j.tar.xz && tar -xf j.tar.xz && rm j.tar.xz && mv json-glib-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dintrospection=disabled -Ddocumentation=disabled -Dgtk_doc=disabled -Dman=false -Dtests=false
RUN DESTDIR=/json-glib ninja -C buildDir install

FROM toolchain AS bubblewrap
# renovate: datasource=github-tags depName=containers/bubblewrap extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG BWRAP_VERSION=0.11.2
RUN mkdir -p /bubblewrap
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://github.com/containers/bubblewrap/releases/download/v${BWRAP_VERSION}/bubblewrap-${BWRAP_VERSION}.tar.xz -o b.tar.xz && tar -xf b.tar.xz && rm b.tar.xz && mv bubblewrap-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dman=disabled -Dtests=false -Dselinux=disabled -Drequire_userns=false
RUN DESTDIR=/bubblewrap ninja -C buildDir install

FROM toolchain AS xdg-dbus-proxy
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
# renovate: datasource=github-tags depName=flatpak/xdg-dbus-proxy extractVersion=^(?<version>\d+(\.\d+)*)$
ARG XDP_VERSION=0.1.7
RUN mkdir -p /xdg-dbus-proxy
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://github.com/flatpak/xdg-dbus-proxy/releases/download/${XDP_VERSION}/xdg-dbus-proxy-${XDP_VERSION}.tar.xz -o x.tar.xz && tar -xf x.tar.xz && rm x.tar.xz && mv xdg-dbus-proxy-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dman=disabled
RUN DESTDIR=/xdg-dbus-proxy ninja -C buildDir install

# GPG support — flatpak 1.16 hard-requires libgpgme at build; runtime
# verification is supplied by signed-flatpak-runtime.
FROM toolchain AS libgpg-error
ARG LIBGPGERROR_VERSION=1.56
ARG LIBGPGERROR_SHA256=82c3d2deb4ad96ad3925d6f9f124fe7205716055ab50e291116ef27975d169c0
RUN mkdir -p /libgpg-error
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors \
      https://gnupg.org/ftp/gcrypt/libgpg-error/libgpg-error-${LIBGPGERROR_VERSION}.tar.bz2 \
      -o source.tar.bz2 && \
    echo "${LIBGPGERROR_SHA256}  source.tar.bz2" | sha256sum -c - && \
    tar -xf source.tar.bz2 && rm source.tar.bz2 && mv libgpg-error-* src
WORKDIR /build/src
RUN ./configure ${COMMON_CONFIGURE_ARGS} --disable-doc --disable-tests \
      --enable-install-gpg-error-config
RUN make -j"$(nproc)" && make DESTDIR=/libgpg-error install

FROM toolchain AS libassuan
COPY --from=libgpg-error /libgpg-error /
ARG LIBASSUAN_VERSION=3.0.2
RUN mkdir -p /libassuan
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://www.gnupg.org/ftp/gcrypt/libassuan/libassuan-${LIBASSUAN_VERSION}.tar.bz2 -o a.tar.bz2 && tar -xf a.tar.bz2 && rm a.tar.bz2 && mv libassuan-* src
WORKDIR /build/src
RUN ./configure ${COMMON_CONFIGURE_ARGS} --disable-doc
RUN make -j"$(nproc)" && make DESTDIR=/libassuan install

FROM toolchain AS gpgme
COPY --from=libgpg-error /libgpg-error /
COPY --from=libassuan /libassuan /
ARG GPGME_VERSION=1.24.3
RUN mkdir -p /gpgme
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://www.gnupg.org/ftp/gcrypt/gpgme/gpgme-${GPGME_VERSION}.tar.bz2 -o g.tar.bz2 && tar -xf g.tar.bz2 && rm g.tar.bz2 && mv gpgme-* src
WORKDIR /build/src
# musl lacks the glibc transitional ino64_t/off64_t types gpgme's posix-io.c
# uses; _LARGEFILE64_SOURCE makes musl expose them as off_t aliases.
RUN CPPFLAGS="-D_LARGEFILE64_SOURCE" ./configure ${COMMON_CONFIGURE_ARGS} --disable-gpg-test --disable-gpgsm-test --disable-g13-test --enable-languages=cpp --disable-doc
RUN make -j"$(nproc)" && make DESTDIR=/gpgme install

FROM toolchain AS libgcrypt
COPY --from=libgpg-error /libgpg-error /
ARG LIBGCRYPT_VERSION=1.12.2
ARG LIBGCRYPT_SHA256=7ce33c2492221a0436f96a8500215e9f3e3dcb5fd26a757cd415e7a843babd5e
RUN mkdir -p /libgcrypt
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors \
      https://gnupg.org/ftp/gcrypt/libgcrypt/libgcrypt-${LIBGCRYPT_VERSION}.tar.bz2 \
      -o source.tar.bz2 && \
    echo "${LIBGCRYPT_SHA256}  source.tar.bz2" | sha256sum -c - && \
    tar -xf source.tar.bz2 && rm source.tar.bz2 && mv libgcrypt-* src
WORKDIR /build/src
RUN ./configure ${COMMON_CONFIGURE_ARGS} --disable-doc
RUN make -j"$(nproc)" && make DESTDIR=/libgcrypt install

FROM toolchain AS libksba
COPY --from=libgpg-error /libgpg-error /
ARG LIBKSBA_VERSION=1.8.0
ARG LIBKSBA_SHA256=296b9db9095749f2aa104202d7ab7fd09ad10710e00780a709c9754b1a1d9292
RUN mkdir -p /libksba
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors \
      https://gnupg.org/ftp/gcrypt/libksba/libksba-${LIBKSBA_VERSION}.tar.bz2 \
      -o source.tar.bz2 && \
    echo "${LIBKSBA_SHA256}  source.tar.bz2" | sha256sum -c - && \
    tar -xf source.tar.bz2 && rm source.tar.bz2 && mv libksba-* src
WORKDIR /build/src
RUN ./configure ${COMMON_CONFIGURE_ARGS} --disable-doc
RUN make -j"$(nproc)" && make DESTDIR=/libksba install

FROM toolchain AS npth
ARG NPTH_VERSION=1.8
ARG NPTH_SHA256=8bd24b4f23a3065d6e5b26e98aba9ce783ea4fd781069c1b35d149694e90ca3e
RUN mkdir -p /npth
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors \
      https://gnupg.org/ftp/gcrypt/npth/npth-${NPTH_VERSION}.tar.bz2 \
      -o source.tar.bz2 && \
    echo "${NPTH_SHA256}  source.tar.bz2" | sha256sum -c - && \
    tar -xf source.tar.bz2 && rm source.tar.bz2 && mv npth-* src
WORKDIR /build/src
RUN ./configure ${COMMON_CONFIGURE_ARGS} --disable-tests
RUN make -j"$(nproc)" && make DESTDIR=/npth install

FROM toolchain AS sqlite-gnupg
ARG SQLITE_VERSION=3490200
ARG SQLITE_SHA256=5c6d8697e8a32a1512a9be5ad2b2e7a891241c334f56f8b0fb4fc6051e1652e8
RUN mkdir -p /sqlite-gnupg
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors \
      https://sqlite.org/2025/sqlite-autoconf-${SQLITE_VERSION}.tar.gz \
      -o source.tar.gz && \
    echo "${SQLITE_SHA256}  source.tar.gz" | sha256sum -c - && \
    tar -xf source.tar.gz && rm source.tar.gz && mv sqlite-autoconf-* src
WORKDIR /build/src
RUN CFLAGS="-O2 -pipe -flto" ./configure --prefix=/usr \
      --host=x86_64-hadron-linux-musl --build=x86_64-hadron-linux-musl \
      --enable-shared --disable-static --disable-readline --disable-static-shell
RUN make -j"$(nproc)" && make DESTDIR=/sqlite-gnupg install

FROM toolchain AS ntbtls
COPY --from=libgpg-error /libgpg-error /
COPY --from=libgcrypt /libgcrypt /
COPY --from=libksba /libksba /
ARG NTBTLS_VERSION=0.3.2
ARG NTBTLS_SHA256=bdfcb99024acec9c6c4b998ad63bb3921df4cfee4a772ad6c0ca324dbbf2b07c
RUN mkdir -p /ntbtls
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors \
      https://gnupg.org/ftp/gcrypt/ntbtls/ntbtls-${NTBTLS_VERSION}.tar.bz2 \
      -o source.tar.bz2 && \
    echo "${NTBTLS_SHA256}  source.tar.bz2" | sha256sum -c - && \
    tar -xf source.tar.bz2 && rm source.tar.bz2 && mv ntbtls-* src
WORKDIR /build/src
RUN ./configure ${COMMON_CONFIGURE_ARGS}
RUN make -j"$(nproc)" && make DESTDIR=/ntbtls install

FROM toolchain AS gnupg
COPY --from=libgpg-error /libgpg-error /
COPY --from=libassuan /libassuan /
COPY --from=libgcrypt /libgcrypt /
COPY --from=libksba /libksba /
COPY --from=npth /npth /
COPY --from=ntbtls /ntbtls /
COPY --from=sqlite-gnupg /sqlite-gnupg /
ARG GNUPG_VERSION=2.4.9
ARG GNUPG_SHA256=dd17ab2e9a04fd79d39d853f599cbc852062ddb9ab52a4ddeb4176fd8b302964
RUN mkdir -p /gnupg
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors \
      https://gnupg.org/ftp/gcrypt/gnupg/gnupg-${GNUPG_VERSION}.tar.bz2 \
      -o source.tar.bz2 && \
    echo "${GNUPG_SHA256}  source.tar.bz2" | sha256sum -c - && \
    tar -xf source.tar.bz2 && rm source.tar.bz2 && mv gnupg-* src
WORKDIR /build/src
# Card support stays on (gpg --card-status, --card-edit and keytocard, and
# gpg-agent's SCD command, which gpg-card uses) and scdaemon is built, then
# left out: the `smartcards` extension (extensions/smartcards) brings it, at
# the same version, to /usr/lib/gnupg/scdaemon, where this gpg-agent starts
# it (--libexecdir below; Kairos merges /usr/lib, not /usr/libexec). Without
# the extension, a card command says the card is not available. No CCID
# driver of scdaemon's own: readers go through pcscd. This stage is not
# reached by the COSMIC prebuilt keys.
RUN ./configure ${COMMON_CONFIGURE_ARGS} --libexecdir=/usr/lib/gnupg \
      --disable-gpgsm --disable-wks-tools --disable-ccid-driver --disable-tpm2d \
      --disable-doc --disable-nls --disable-ldap --disable-libdns \
      --disable-bzip2
RUN make -j"$(nproc)" && make DESTDIR=/gnupg install && \
    rm -f /gnupg/usr/bin/gpg-card /gnupg/usr/bin/gpg-wks-client \
      /gnupg/usr/lib/gnupg/gpg-wks-client /gnupg/usr/lib/gnupg/scdaemon
RUN test -x /gnupg/usr/bin/gpg && test -x /gnupg/usr/bin/gpgv && \
    test -x /gnupg/usr/bin/gpgconf && test -x /gnupg/usr/bin/gpg-agent && \
    test -x /gnupg/usr/bin/dirmngr && \
    test -x /gnupg/usr/lib/gnupg/keyboxd && \
    test ! -e /gnupg/usr/bin/gpgsm && test ! -e /gnupg/usr/bin/gpg-card && \
    test ! -e /gnupg/usr/bin/gpg-wks-client && \
    test ! -e /gnupg/usr/lib/gnupg/gpg-wks-client && \
    test ! -e /gnupg/usr/lib/gnupg/scdaemon && \
    test ! -e /gnupg/usr/lib/gnupg/tpm2daemon && \
    /gnupg/usr/bin/gpg --dump-options | grep -qx -- '--card-status'

FROM toolchain AS signed-flatpak-runtime
COPY --from=libgpg-error /libgpg-error /
COPY --from=libassuan /libassuan /
COPY --from=libgcrypt /libgcrypt /
COPY --from=libksba /libksba /
COPY --from=npth /npth /
COPY --from=ntbtls /ntbtls /
COPY --from=sqlite-gnupg /sqlite-gnupg /
COPY --from=gnupg /gnupg /
COPY --from=gnupg /gnupg /signed-flatpak-runtime
RUN mkdir -p /signed-flatpak-runtime/usr/lib \
      /signed-flatpak-runtime/usr/share/isotope && \
    cp -a /usr/lib/libgcrypt.so* /signed-flatpak-runtime/usr/lib/ && \
    cp -a /usr/lib/libksba.so* /signed-flatpak-runtime/usr/lib/ && \
    cp -a /usr/lib/libnpth.so* /signed-flatpak-runtime/usr/lib/ && \
    cp -a /usr/lib/libntbtls.so* /signed-flatpak-runtime/usr/lib/ && \
    cp -a /usr/lib/libsqlite3.so* /signed-flatpak-runtime/usr/lib/ && \
    rm -rf /signed-flatpak-runtime/usr/share/doc \
      /signed-flatpak-runtime/usr/share/info \
      /signed-flatpak-runtime/usr/share/man \
      /signed-flatpak-runtime/usr/share/locale
ARG FLATHUB_DESCRIPTOR_SHA256=3371dd250e61d9e1633630073fefda153cd4426f72f4afa0c3373ae2e8fea03a
ARG FLATHUB_PRIMARY_FINGERPRINT=6E5C05D979C76DAF93C081354184DD4D907A7CAE
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors \
      https://dl.flathub.org/repo/flathub.flatpakrepo \
      -o /signed-flatpak-runtime/usr/share/isotope/flathub.flatpakrepo && \
    echo "${FLATHUB_DESCRIPTOR_SHA256}  /signed-flatpak-runtime/usr/share/isotope/flathub.flatpakrepo" | \
      sha256sum -c - && \
    test "$(grep -c '^GPGKey=' \
      /signed-flatpak-runtime/usr/share/isotope/flathub.flatpakrepo)" -eq 1 && \
    sed -n 's/^GPGKey=//p' \
      /signed-flatpak-runtime/usr/share/isotope/flathub.flatpakrepo | \
      base64 -d > /signed-flatpak-runtime/usr/share/isotope/flathub.gpg && \
    install -d -m700 /tmp/gnupg-home && \
    test "$(GNUPGHOME=/tmp/gnupg-home gpg --batch --with-colons --show-keys \
      /signed-flatpak-runtime/usr/share/isotope/flathub.gpg 2>/dev/null | \
      awk -F: '$1 == "fpr" {print $10; exit}')" = \
      "${FLATHUB_PRIMARY_FINGERPRINT}"
RUN : > /signed-flatpak-runtime/usr/share/isotope/gnupg-runtime-size && \
    bytes="$(du -sb /signed-flatpak-runtime | awk '{print $1}')" && \
    printf '%s\n' "$bytes" > \
      /signed-flatpak-runtime/usr/share/isotope/gnupg-runtime-size && \
    bytes="$(du -sb /signed-flatpak-runtime | awk '{print $1}')" && \
    test "$bytes" -le 20971520 && \
    printf '%s\n' "$bytes" > \
      /signed-flatpak-runtime/usr/share/isotope/gnupg-runtime-size

# AppStream trio — flatpak 1.16 hard-requires libappstream at build.
FROM toolchain AS libyaml
# renovate: datasource=github-tags depName=yaml/libyaml extractVersion=^(?<version>\d+(\.\d+)*)$
ARG LIBYAML_VERSION=0.2.5
RUN mkdir -p /libyaml
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://github.com/yaml/libyaml/releases/download/${LIBYAML_VERSION}/yaml-${LIBYAML_VERSION}.tar.gz -o y.tar.gz && tar -xf y.tar.gz && rm y.tar.gz && mv yaml-* src
WORKDIR /build/src
RUN ./configure ${COMMON_CONFIGURE_ARGS}
RUN make -j"$(nproc)" && make DESTDIR=/libyaml install

FROM toolchain AS libxmlb
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
# renovate: datasource=github-tags depName=hughsie/libxmlb extractVersion=^(?<version>\d+(\.\d+)*)$
ARG LIBXMLB_VERSION=0.3.22
RUN mkdir -p /libxmlb
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://github.com/hughsie/libxmlb/releases/download/${LIBXMLB_VERSION}/libxmlb-${LIBXMLB_VERSION}.tar.xz -o x.tar.xz && tar -xf x.tar.xz && rm x.tar.xz && mv libxmlb-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dintrospection=false -Dgtkdoc=false -Dtests=false -Dstemmer=false -Dcli=false
RUN DESTDIR=/libxmlb ninja -C buildDir install

FROM toolchain AS appstream
COPY --from=gperf /gperf /
COPY --from=libxslt /libxslt /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=libyaml /libyaml /
COPY --from=libxmlb /libxmlb /
# renovate: datasource=github-tags depName=ximion/appstream extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG APPSTREAM_VERSION=1.0.4
RUN mkdir -p /appstream
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://www.freedesktop.org/software/appstream/releases/AppStream-${APPSTREAM_VERSION}.tar.xz -o as.tar.xz && tar -xf as.tar.xz && rm as.tar.xz && mv AppStream-* src
WORKDIR /build/src
RUN pip3 install meson ninja
# No gettext in the toolchain: meson's i18n.gettext() returns void and the po/
# build aborts. Stub the gettext tools so it resolves (translations are no-ops).
RUN for t in xgettext msgmerge msginit msgcat msgconv msguniq; do \
        printf '#!/bin/sh\ncase "$1" in --version) echo "%s (GNU gettext-tools) 0.21" ;; esac\nexit 0\n' "$t" > /usr/bin/$t && chmod +x /usr/bin/$t; \
    done
# msgfmt must emit a *valid* (empty) .mo so meson's po install step finds the
# files it declared; a no-op would leave them missing.
RUN printf '#!/bin/sh\ncase "$1" in --version) echo "msgfmt (GNU gettext-tools) 0.21"; exit 0;; esac\nout=""\nwhile [ $# -gt 0 ]; do case "$1" in -o) shift; out="$1";; --output-file=*) out="${1#*=}";; esac; shift; done\n[ -n "$out" ] && python3 -c "import struct,sys;open(sys.argv[1],\"wb\").write(struct.pack(\"<7I\",0x950412de,0,0,28,28,0,28))" "$out"\nexit 0\n' > /usr/bin/msgfmt && chmod +x /usr/bin/msgfmt
# itstool only translates appstream-cli's own metainfo (not used by libappstream
# that flatpak links). Stub it to copy the untranslated -j source to -o output.
RUN printf '#!/bin/sh\nsrc=""; out=""\nwhile [ $# -gt 0 ]; do case "$1" in -j) shift; src="$1";; -o) shift; out="$1";; esac; shift; done\n[ -n "$out" ] && { [ -n "$src" ] && cp "$src" "$out" || : > "$out"; }\nexit 0\n' > /usr/bin/itstool && chmod +x /usr/bin/itstool
# docs/ (man pages needing DocBook XSL), data/ (appstream-cli's translated
# metainfo) and po/ (translations needing real gettext) are all unused by
# libappstream — the only thing flatpak links — and each trips the missing
# gettext/docbook toolchain, so drop all three subdirs.
RUN : > docs/meson.build && : > data/meson.build && : > po/meson.build
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dstemming=false -Dsystemd=false -Dvapi=false -Dqt=false -Dcompose=false -Dgir=false -Dsvg-support=false -Ddocs=false -Dapidocs=false -Dinstall-docs=false -Dapt-support=false
RUN DESTDIR=/appstream ninja -C buildDir install

# libfuse3 — flatpak requires fuse (revokefs-fuse + the document portal).
FROM toolchain AS libfuse3
# renovate: datasource=github-tags depName=libfuse/libfuse extractVersion=^fuse-(?<version>\d+(\.\d+)*)$
ARG LIBFUSE_VERSION=3.16.2
RUN mkdir -p /libfuse3
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://github.com/libfuse/libfuse/releases/download/fuse-${LIBFUSE_VERSION}/fuse-${LIBFUSE_VERSION}.tar.gz -o f.tar.gz && tar -xf f.tar.gz && rm f.tar.gz && mv fuse-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dexamples=false -Dtests=false -Duseroot=false
RUN DESTDIR=/libfuse3 ninja -C buildDir install
# mount.fuse3 installs to /usr/sbin, but the image's /usr/sbin is a symlink to
# bin — a real sbin dir here breaks the COPY into downstream stages. Relocate.
RUN if [ -d /libfuse3/usr/sbin ]; then mkdir -p /libfuse3/usr/bin && cp -a /libfuse3/usr/sbin/. /libfuse3/usr/bin/ && rm -rf /libfuse3/usr/sbin; fi

# e2fsprogs — ostree hard-requires libe2p (ext2 attribute flags). Build only the
# libraries; reuse the toolchain's libuuid/libblkid instead of e2fsprogs' own.
FROM toolchain AS e2fsprogs
# renovate: datasource=github-tags depName=tytso/e2fsprogs extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG E2FSPROGS_VERSION=1.47.2
RUN mkdir -p /e2fsprogs
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://mirrors.edge.kernel.org/pub/linux/kernel/people/tytso/e2fsprogs/v${E2FSPROGS_VERSION}/e2fsprogs-${E2FSPROGS_VERSION}.tar.gz -o e.tar.gz && tar -xf e.tar.gz && rm e.tar.gz && mv e2fsprogs-* src
WORKDIR /build/src
RUN ./configure ${COMMON_CONFIGURE_ARGS} --enable-elf-shlibs --disable-nls --disable-libuuid --disable-libblkid --disable-fuse2fs --disable-fsck
RUN make -j"$(nproc)" libs && make install-libs DESTDIR=/e2fsprogs

# --- ostree — the content store flatpak deploys apps from -------------------
# libcurl/openssl/zlib/lzma/libmount/libsystemd all come from the toolchain.
FROM toolchain AS ostree
COPY --from=libucontext /libucontext /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=libarchive /libarchive /
COPY --from=libgpg-error /libgpg-error /
COPY --from=libassuan /libassuan /
COPY --from=gpgme /gpgme /
COPY --from=e2fsprogs /e2fsprogs /
# renovate: datasource=github-tags depName=ostreedev/ostree extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG OSTREE_VERSION=2026.1
RUN mkdir -p /ostree
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://github.com/ostreedev/ostree/releases/download/v${OSTREE_VERSION}/libostree-${OSTREE_VERSION}.tar.xz -o ot.tar.xz && tar -xf ot.tar.xz && rm ot.tar.xz && mv libostree-* src
WORKDIR /build/src
RUN ./configure ${COMMON_CONFIGURE_ARGS} \
      --with-gpgme --with-curl --without-soup --with-crypto=openssl \
      --with-libarchive --with-libsystemd --disable-rofiles-fuse \
      --disable-introspection --disable-gtk-doc --disable-man --without-selinux
RUN make -j"$(nproc)" && make DESTDIR=/ostree install

# --- flatpak ---------------------------------------------------------------
# Sandboxed app runtime. Uses our system bubblewrap / xdg-dbus-proxy /
# fusermount3 (no bundled copies). libseccomp/libcap/libcurl/libxml2/libsystemd
# come from the toolchain. The GnuPG engine and trust bootstrap for runtime
# verification are supplied by signed-flatpak-runtime.
FROM toolchain AS flatpak
COPY --from=libucontext /libucontext /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=libpng /libpng /
COPY --from=gdk-pixbuf /gdk-pixbuf /
COPY --from=libarchive /libarchive /
COPY --from=json-glib /json-glib /
COPY --from=libgpg-error /libgpg-error /
COPY --from=libassuan /libassuan /
COPY --from=gpgme /gpgme /
COPY --from=libyaml /libyaml /
COPY --from=libxmlb /libxmlb /
COPY --from=appstream /appstream /
COPY --from=libfuse3 /libfuse3 /
COPY --from=bubblewrap /bubblewrap /
COPY --from=xdg-dbus-proxy /xdg-dbus-proxy /
COPY --from=ostree /ostree /
# renovate: datasource=github-tags depName=flatpak/flatpak extractVersion=^(?<version>\d+(\.\d+)*)$
ARG FLATPAK_VERSION=1.16.6
RUN mkdir -p /flatpak
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://github.com/flatpak/flatpak/releases/download/${FLATPAK_VERSION}/flatpak-${FLATPAK_VERSION}.tar.xz -o fp.tar.xz && tar -xf fp.tar.xz && rm fp.tar.xz && mv flatpak-* src
WORKDIR /build/src
RUN pip3 install meson ninja pyparsing
# --libexecdir=lib keeps flatpak's helpers (session-helper, portal, ...) in
# /usr/lib (pure image content) rather than /usr/libexec, which Kairos lists in
# PERSISTENT_STATE_PATHS and could shadow on the installed system.
RUN meson setup buildDir ${COMMON_MESON_FLAGS} --libexecdir=lib \
      -Dhttp_backend=curl -Dgir=disabled -Dgtkdoc=disabled -Ddocbook_docs=disabled \
      -Dman=disabled -Dseccomp=enabled -Dselinux_module=disabled -Dmalcontent=disabled \
      -Ddconf=disabled -Dsystem_helper=disabled -Dtests=false -Dinstalled_tests=false \
      -Dwayland_security_context=disabled -Dxauth=disabled \
      -Dsystem_bubblewrap=bwrap -Dsystem_dbus_proxy=xdg-dbus-proxy -Dsystem_fusermount=fusermount3
RUN DESTDIR=/flatpak ninja -C buildDir install

# --- shared-mime-info — the XDG MIME database -------------------------------
# Without /usr/share/mime, GLib's g_content_type_guess() can't identify files,
# so libxmlb (and thus appstream / `flatpak search`) fails to recognise the
# gzipped Flathub catalog and never decompresses it. Build the database and the
# update-mime-database tool; compile the binary cache at build time.
FROM toolchain AS shared-mime-info
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
# renovate: datasource=gitlab-tags depName=xdg/shared-mime-info registryUrl=https://gitlab.freedesktop.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG SMI_VERSION=2.4
RUN mkdir -p /shared-mime-info
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://gitlab.freedesktop.org/xdg/shared-mime-info/-/archive/${SMI_VERSION}/shared-mime-info-${SMI_VERSION}.tar.gz -o smi.tar.gz && tar -xf smi.tar.gz && rm smi.tar.gz && mv shared-mime-info-* src
WORKDIR /build/src
RUN pip3 install meson ninja
# No gettext/itstool/xmlto. Stub them: msgfmt must report a GNU version (meson's
# i18n.merge_file checks) and, in --xml mode, pass the template through so the
# (untranslated) freedesktop.org.xml is still produced.
RUN for t in xgettext msgmerge itstool xmlto; do printf '#!/bin/sh\nexit 0\n' > /usr/bin/$t && chmod +x /usr/bin/$t; done
RUN cat > /usr/bin/msgfmt <<'MFEOF' && chmod +x /usr/bin/msgfmt
#!/bin/sh
template=""; out=""
while [ $# -gt 0 ]; do
    case "$1" in
        --version) echo "msgfmt (GNU gettext-tools) 0.21"; exit 0 ;;
        --template) shift; template="$1" ;;
        --template=*) template="${1#*=}" ;;
        -o) shift; out="$1" ;;
        --output-file=*) out="${1#*=}" ;;
    esac
    shift
done
[ -n "$out" ] && { [ -n "$template" ] && cp "$template" "$out" || python3 -c "import struct,sys;open(sys.argv[1],'wb').write(struct.pack('<7I',0x950412de,0,0,28,28,0,28))" "$out"; }
exit 0
MFEOF
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dbuild-tools=true -Dupdate-mimedb=false -Dbuild-translations=false -Dbuild-tests=false
RUN DESTDIR=/shared-mime-info ninja -C buildDir install
# Compile the binary mime cache into the DESTDIR (the in-tree update-mimedb
# post-install step is skipped above because it isn't DESTDIR-aware).
RUN /shared-mime-info/usr/bin/update-mime-database /shared-mime-info/usr/share/mime 2>&1 | tail -1; \
    ls -la /shared-mime-info/usr/share/mime/mime.cache


# ===========================================================================
# M5: desktop polish — wallpaper, notifications, launcher, clipboard, etc.
# ===========================================================================

# swaybg — wallpaper daemon
FROM toolchain AS swaybg
COPY --from=wayland /wayland /
COPY --from=cairo /cairo /
COPY --from=pixman /pixman /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=freetype /freetype /
COPY --from=fontconfig /fontconfig /
COPY --from=libpng /libpng /
# renovate: datasource=github-tags depName=swaywm/swaybg extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG SWAYBG_VERSION=1.2.1
RUN mkdir -p /swaybg
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://github.com/swaywm/swaybg/releases/download/v${SWAYBG_VERSION}/swaybg-${SWAYBG_VERSION}.tar.gz -o swaybg.tar.gz && tar -xzf swaybg.tar.gz && rm swaybg.tar.gz && mv swaybg-* swaybg-src
WORKDIR /build/swaybg-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dman-pages=disabled -Dgdk-pixbuf=disabled -Dwerror=false
RUN DESTDIR=/swaybg ninja -C buildDir install


# mako — notification daemon
FROM toolchain AS mako
COPY --from=libucontext /libucontext /
COPY --from=wayland /wayland /
COPY --from=cairo /cairo /
COPY --from=pango /pango /
COPY --from=pixman /pixman /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=harfbuzz /harfbuzz /
COPY --from=freetype /freetype /
COPY --from=fontconfig /fontconfig /
COPY --from=fribidi /fribidi /
COPY --from=libpng /libpng /
# renovate: datasource=github-tags depName=emersion/mako extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG MAKO_VERSION=1.9.0
RUN mkdir -p /mako
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://github.com/emersion/mako/releases/download/v${MAKO_VERSION}/mako-${MAKO_VERSION}.tar.gz -o mako.tar.gz && tar -xzf mako.tar.gz && rm mako.tar.gz && mv mako-* mako-src
WORKDIR /build/mako-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dman-pages=disabled -Dsd-bus-provider=libsystemd
RUN DESTDIR=/mako ninja -C buildDir install


# fuzzel — application launcher
FROM toolchain AS fuzzel
COPY --from=wayland /wayland /
COPY --from=libxkb /libxkb /
COPY --from=pixman /pixman /
COPY --from=freetype /freetype /
COPY --from=fontconfig /fontconfig /
COPY --from=fcft /fcft /
COPY --from=tllist /tllist /
COPY --from=harfbuzz /harfbuzz /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=libpng /libpng /
# renovate: datasource=gitea-tags depName=dnkl/fuzzel registryUrl=https://codeberg.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG FUZZEL_VERSION=1.11.0
RUN mkdir -p /fuzzel
WORKDIR /build
RUN curl -f --connect-timeout 30 --speed-limit 1024 --speed-time 60 --retry 5 --retry-delay 3 --retry-all-errors -L https://codeberg.org/dnkl/fuzzel/releases/download/${FUZZEL_VERSION}/fuzzel-${FUZZEL_VERSION}.tar.gz -o fuzzel.tar.gz && tar -xzf fuzzel.tar.gz && rm fuzzel.tar.gz && mv fuzzel* fuzzel-src
WORKDIR /build/fuzzel-src
RUN pip3 install meson ninja
# Drop the man-page subdir (needs scdoc, which we don't build).
RUN sed -i "/subdir('doc')/d" meson.build
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Denable-cairo=disabled -Dpng-backend=libpng -Dsvg-backend=none -Dwerror=false
RUN DESTDIR=/fuzzel ninja -C buildDir install


# wl-clipboard — clipboard utilities. 2.3.0 or later: `wl-paste --watch` sets
# CLIPBOARD_STATE=sensitive for a copy marked x-kde-passwordManagerHint, which
# is how cliphist knows not to record it.
FROM toolchain AS wl-clipboard
COPY --from=wayland /wayland /
# renovate: datasource=github-tags depName=bugaevc/wl-clipboard extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG WLCLIP_VERSION=2.3.0
RUN mkdir -p /wl-clipboard
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://github.com/bugaevc/wl-clipboard/archive/refs/tags/v${WLCLIP_VERSION}.tar.gz -o wlclip.tar.gz && tar -xzf wlclip.tar.gz && rm wlclip.tar.gz && mv wl-clipboard-* wlclip-src
WORKDIR /build/wlclip-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS}
RUN DESTDIR=/wl-clipboard ninja -C buildDir install


# cliphist — the clipboard history of Sway and COSMIC (isotope-clipboard).
# `wl-paste --watch cliphist store` records each copy. Static Go binary
# (CGO_ENABLED=0), like isotope-update, so it runs on musl with no runtime
# linkage. niri needs none: Noctalia keeps its own history.
FROM golang:1.25.0-alpine3.22 AS cliphist
# renovate: datasource=github-tags depName=sentriz/cliphist extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG CLIPHIST_VERSION=0.7.0
WORKDIR /src
RUN wget -qO- https://github.com/sentriz/cliphist/archive/refs/tags/v${CLIPHIST_VERSION}.tar.gz | tar -xz --strip-components=1 && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /cliphist/usr/bin/cliphist . && \
    [ "$(/cliphist/usr/bin/cliphist version 2>&1 | awk '$1 == "version" { print $2 }')" = "${CLIPHIST_VERSION}" ]


# restic — the backup engine of `isotope backup` and `isotope restore`:
# encrypted, deduplicated snapshots to a directory (a USB disk), SFTP, a REST
# server or S3. Static Go binary, like cliphist. In every desktop and the
# server; Dockerfile.agent removes it. restic 0.19 needs Go 1.25.8 or later,
# newer than the other Go stages.
FROM golang:1.25.8-alpine3.22 AS restic
# renovate: datasource=github-tags depName=restic/restic extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG RESTIC_VERSION=0.19.1
WORKDIR /src
RUN wget -qO- https://github.com/restic/restic/archive/refs/tags/v${RESTIC_VERSION}.tar.gz | tar -xz --strip-components=1 && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /restic/usr/bin/restic ./cmd/restic && \
    [ "$(/restic/usr/bin/restic version | awk '{ print $2 }')" = "${RESTIC_VERSION}" ]


# bootvine (codeberg.org/wvhulle/bootvine, GPL-3.0-or-later): a live tree of
# the boot's systemd jobs, the units that failed and why, and the slowest
# ones, on tty12. The splash starts it in the initramfs when D is pressed or
# isotope.bootvine is on the kernel command line (splash/README.md); the
# 52isotope-bootvine dracut module puts it in the initramfs, not enabled.
# Pinned to a commit, not a release: v0.1.4 talks to systemd through libdbus
# and libsystemd, and only later commits replaced both with pure Rust (zbus,
# sdjournal), which is what lets it build as one static musl binary here.
# Its rust-toolchain.toml asks for a nightly it does not need; stable builds it.
FROM rust:1.96-alpine AS bootvine
ARG BOOTVINE_COMMIT=c1e99c33bf1654849b1c42926c5e93e9ce61afa0
RUN apk add --no-cache musl-dev git
WORKDIR /src
RUN git init -q . && git remote add origin https://codeberg.org/wvhulle/bootvine.git && \
    git fetch -q --depth 1 origin "${BOOTVINE_COMMIT}" && git checkout -q FETCH_HEAD && \
    [ "$(git rev-parse HEAD)" = "${BOOTVINE_COMMIT}" ] && rm rust-toolchain.toml && \
    cargo build --release --locked -p bootvine && \
    install -Dm755 target/release/bootvine /bootvine/usr/bin/bootvine && \
    install -Dm644 bootvine/system-files/systemd/bootvine.service /bootvine/usr/lib/systemd/system/bootvine.service && \
    install -Dm644 LICENSE /bootvine/usr/share/licenses/bootvine/LICENSE && \
    /bootvine/usr/bin/bootvine --version


# slurp — region selection (pairs with grim)
FROM toolchain AS slurp
COPY --from=wayland /wayland /
COPY --from=cairo /cairo /
COPY --from=pixman /pixman /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=freetype /freetype /
COPY --from=fontconfig /fontconfig /
COPY --from=libpng /libpng /
COPY --from=libxkb /libxkb /
# renovate: datasource=github-tags depName=emersion/slurp extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG SLURP_VERSION=1.5.0
RUN mkdir -p /slurp
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://github.com/emersion/slurp/releases/download/v${SLURP_VERSION}/slurp-${SLURP_VERSION}.tar.gz -o slurp.tar.gz && tar -xzf slurp.tar.gz && rm slurp.tar.gz && mv slurp-* slurp-src
WORKDIR /build/slurp-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dman-pages=disabled
RUN DESTDIR=/slurp ninja -C buildDir install


# swayidle — idle management
FROM toolchain AS swayidle
COPY --from=libucontext /libucontext /
COPY --from=wayland /wayland /
# renovate: datasource=github-tags depName=swaywm/swayidle extractVersion=^(?<version>\d+(\.\d+)*)$
ARG SWAYIDLE_VERSION=1.8.0
RUN mkdir -p /swayidle
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://github.com/swaywm/swayidle/releases/download/${SWAYIDLE_VERSION}/swayidle-${SWAYIDLE_VERSION}.tar.gz -o swayidle.tar.gz && tar -xzf swayidle.tar.gz && rm swayidle.tar.gz && mv swayidle-* swayidle-src
WORKDIR /build/swayidle-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dman-pages=disabled -Dlogind=enabled
RUN DESTDIR=/swayidle ninja -C buildDir install


# swaylock — screen locker, run by the Super+Alt+L binding and by swayidle on
# idle and before sleep. Built against PAM: it checks the password through the
# service in rootfs/sway/etc/pam.d/swaylock (pam_unix's setuid unix_chkpwd
# helper reads /etc/shadow for it), so the binary itself is not setuid.
# Colours come from rootfs/sway/etc/swaylock/config.
FROM toolchain AS swaylock
COPY --from=wayland /wayland /
COPY --from=libxkb /libxkb /
COPY --from=cairo /cairo /
COPY --from=pixman /pixman /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=freetype /freetype /
COPY --from=fontconfig /fontconfig /
COPY --from=libpng /libpng /
# renovate: datasource=github-tags depName=swaywm/swaylock extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG SWAYLOCK_VERSION=1.8.6
RUN mkdir -p /swaylock
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/swaywm/swaylock/releases/download/v${SWAYLOCK_VERSION}/swaylock-${SWAYLOCK_VERSION}.tar.gz -o swaylock.tar.gz && tar -xzf swaylock.tar.gz && rm swaylock.tar.gz && mv swaylock-* swaylock-src
WORKDIR /build/swaylock-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dpam=enabled -Dgdk-pixbuf=disabled -Dman-pages=disabled \
      -Dzsh-completions=false -Dbash-completions=false -Dfish-completions=false
RUN DESTDIR=/swaylock ninja -C buildDir install


# wlsunset — the sway night light, started by isotope-night-light. It warms the
# screen through wlr-gamma-control, at fixed times (Hadron has no location).
# niri does not need it: Noctalia has its own night light.
FROM toolchain AS wlsunset
COPY --from=wayland /wayland /
# renovate: datasource=github-tags depName=kennylevinsen/wlsunset extractVersion=^(?<version>\d+(\.\d+)*)$
ARG WLSUNSET_VERSION=0.4.0
RUN mkdir -p /wlsunset
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/kennylevinsen/wlsunset/archive/refs/tags/${WLSUNSET_VERSION}.tar.gz -o wlsunset.tar.gz && tar -xzf wlsunset.tar.gz && rm wlsunset.tar.gz && mv wlsunset-* wlsunset-src
WORKDIR /build/wlsunset-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dman-pages=disabled
RUN DESTDIR=/wlsunset ninja -C buildDir install


# X protocol libs — ly links libxcb (X11 session support) even in a Wayland setup
FROM toolchain AS xorgproto
# renovate: datasource=gitlab-tags depName=xorg/proto/xorgproto registryUrl=https://gitlab.freedesktop.org extractVersion=^xorgproto-(?<version>\d+(\.\d+)*)$
ARG XORGPROTO_VERSION=2025.1
RUN mkdir -p /xorgproto
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://github.com/X11Libre/mirror.fdo.xorgproto/archive/refs/tags/xorgproto-${XORGPROTO_VERSION}.tar.gz -o xorgproto.tar.gz && tar -xf xorgproto.tar.gz && rm xorgproto.tar.gz && mv mirror.fdo.xorgproto-* xorgproto-src
WORKDIR /build/xorgproto-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS}
RUN DESTDIR=/xorgproto ninja -C buildDir install

FROM toolchain AS libxau
COPY --from=xorgproto /xorgproto /
# renovate: datasource=gitlab-tags depName=xorg/lib/libxau registryUrl=https://gitlab.freedesktop.org extractVersion=^libXau-(?<version>\d+(\.\d+)*)$
ARG LIBXAU_VERSION=1.0.12
RUN mkdir -p /libxau
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://gitlab.freedesktop.org/xorg/lib/libxau/-/archive/libXau-${LIBXAU_VERSION}/libxau-libXau-${LIBXAU_VERSION}.tar.gz -o libxau.tar.gz && tar -xf libxau.tar.gz && rm libxau.tar.gz && mv libxau-libXau-* libxau-src
WORKDIR /build/libxau-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS}
RUN DESTDIR=/libxau ninja -C buildDir install

FROM toolchain AS xcbproto
# renovate: datasource=gitlab-tags depName=xorg/proto/xcbproto registryUrl=https://gitlab.freedesktop.org extractVersion=^xcb-proto-(?<version>\d+(\.\d+)*)$
ARG XCB_VERSION=1.17.0
RUN mkdir -p /xcbproto
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://xcb.freedesktop.org/dist/xcb-proto-${XCB_VERSION}.tar.gz -o xcb-proto.tar.gz && tar -xzf xcb-proto.tar.gz && rm xcb-proto.tar.gz && mv xcb-proto-* xcb-src
WORKDIR /build/xcb-src
RUN ./configure ${COMMON_CONFIGURE_ARGS} --build=${BUILD} --disable-dependency-tracking
RUN make -j$(nproc) && make install DESTDIR=/xcbproto

FROM toolchain AS libxcb
COPY --from=xorgproto /xorgproto /
COPY --from=xcbproto /xcbproto /
COPY --from=libxau /libxau /
# renovate: datasource=gitlab-tags depName=xorg/lib/libxcb registryUrl=https://gitlab.freedesktop.org extractVersion=^libxcb-(?<version>\d+(\.\d+)*)$
ARG LIBXCB_VERSION=1.17.0
RUN mkdir -p /libxcb
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://xcb.freedesktop.org/dist/libxcb-${LIBXCB_VERSION}.tar.gz -o libxcb.tar.gz && tar -xzf libxcb.tar.gz && rm libxcb.tar.gz && mv libxcb-* libxcb-src
WORKDIR /build/libxcb-src
RUN ./configure ${COMMON_CONFIGURE_ARGS} --build=${BUILD} --disable-dependency-tracking
RUN make -j$(nproc) && make install DESTDIR=/libxcb


# ===========================================================================
# XLibre + i3 display stack (selected with DESKTOP=i3)
#
# XLibre intentionally remains isolated from the Sway graph. The i3-rootfs
# stage below is the only consumer, so BuildKit prunes this entire section from
# the default DESKTOP=sway build.
# ===========================================================================

# X11 client/server libraries. Keep these in one cached stage: each package is
# installed both into the live builder prefix (for the next package) and into
# /x11-libs (the rootfs artifact). Protocol headers/libxcb come from the shared
# stages above because ly also uses them in the Sway image.
FROM toolchain AS x11-libs
COPY --from=xorgproto /xorgproto /
COPY --from=xcbproto /xcbproto /
COPY --from=libxau /libxau /
COPY --from=libxcb /libxcb /
COPY --from=freetype /freetype /
COPY --from=libpng /libpng /
COPY --from=fontconfig /fontconfig /
RUN mkdir -p /x11-libs
WORKDIR /build

# renovate: datasource=gitlab-tags depName=xorg/lib/libxtrans registryUrl=https://gitlab.freedesktop.org extractVersion=^xtrans-(?<version>\d+(\.\d+)*)$
ARG XTRANS_VERSION=1.6.0
RUN curl --retry-all-errors -fL --retry 5 https://xorg.freedesktop.org/archive/individual/lib/xtrans-${XTRANS_VERSION}.tar.xz -o xtrans.tar.xz && \
    tar -xf xtrans.tar.xz && cd xtrans-${XTRANS_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-docs && \
    make -j"$(nproc)" && make install && make install DESTDIR=/x11-libs

# renovate: datasource=gitlab-tags depName=xorg/lib/libx11 registryUrl=https://gitlab.freedesktop.org extractVersion=^libX11-(?<version>\d+(\.\d+)*)$
ARG LIBX11_VERSION=1.8.12
RUN curl --retry-all-errors -fL --retry 5 https://xorg.freedesktop.org/archive/individual/lib/libX11-${LIBX11_VERSION}.tar.xz -o libX11.tar.xz && \
    tar -xf libX11.tar.xz && cd libX11-${LIBX11_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-specs --disable-static && \
    make -j"$(nproc)" && make install && make install DESTDIR=/x11-libs

# renovate: datasource=gitlab-tags depName=xorg/lib/libxext registryUrl=https://gitlab.freedesktop.org extractVersion=^libXext-(?<version>\d+(\.\d+)*)$
ARG LIBXEXT_VERSION=1.3.6
RUN curl --retry-all-errors -fL --retry 5 https://xorg.freedesktop.org/archive/individual/lib/libXext-${LIBXEXT_VERSION}.tar.xz -o libXext.tar.xz && \
    tar -xf libXext.tar.xz && cd libXext-${LIBXEXT_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-specs --disable-static && \
    make -j"$(nproc)" && make install && make install DESTDIR=/x11-libs

# renovate: datasource=gitlab-tags depName=xorg/lib/libxrender registryUrl=https://gitlab.freedesktop.org extractVersion=^libXrender-(?<version>\d+(\.\d+)*)$
ARG LIBXRENDER_VERSION=0.9.12
RUN curl --retry-all-errors -fL --retry 5 https://xorg.freedesktop.org/archive/individual/lib/libXrender-${LIBXRENDER_VERSION}.tar.xz -o libXrender.tar.xz && \
    tar -xf libXrender.tar.xz && cd libXrender-${LIBXRENDER_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-static && \
    make -j"$(nproc)" && make install && make install DESTDIR=/x11-libs

# renovate: datasource=gitlab-tags depName=xorg/lib/libxfixes registryUrl=https://gitlab.freedesktop.org extractVersion=^libXfixes-(?<version>\d+(\.\d+)*)$
ARG LIBXFIXES_VERSION=6.0.1
RUN curl --retry-all-errors -fL --retry 5 https://xorg.freedesktop.org/archive/individual/lib/libXfixes-${LIBXFIXES_VERSION}.tar.xz -o libXfixes.tar.xz && \
    tar -xf libXfixes.tar.xz && cd libXfixes-${LIBXFIXES_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-static && \
    make -j"$(nproc)" && make install && make install DESTDIR=/x11-libs

# renovate: datasource=gitlab-tags depName=xorg/lib/libxdamage registryUrl=https://gitlab.freedesktop.org extractVersion=^libXdamage-(?<version>\d+(\.\d+)*)$
ARG LIBXDAMAGE_VERSION=1.1.6
RUN curl --retry-all-errors -fL --retry 5 https://xorg.freedesktop.org/archive/individual/lib/libXdamage-${LIBXDAMAGE_VERSION}.tar.xz -o libXdamage.tar.xz && \
    tar -xf libXdamage.tar.xz && cd libXdamage-${LIBXDAMAGE_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-static && \
    make -j"$(nproc)" && make install && make install DESTDIR=/x11-libs

# renovate: datasource=gitlab-tags depName=xorg/lib/libxcomposite registryUrl=https://gitlab.freedesktop.org extractVersion=^libXcomposite-(?<version>\d+(\.\d+)*)$
ARG LIBXCOMPOSITE_VERSION=0.4.6
RUN curl --retry-all-errors -fL --retry 5 https://xorg.freedesktop.org/archive/individual/lib/libXcomposite-${LIBXCOMPOSITE_VERSION}.tar.xz -o libXcomposite.tar.xz && \
    tar -xf libXcomposite.tar.xz && cd libXcomposite-${LIBXCOMPOSITE_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-static && \
    make -j"$(nproc)" && make install && make install DESTDIR=/x11-libs

# renovate: datasource=gitlab-tags depName=xorg/lib/libxrandr registryUrl=https://gitlab.freedesktop.org extractVersion=^libXrandr-(?<version>\d+(\.\d+)*)$
ARG LIBXRANDR_VERSION=1.5.4
RUN curl --retry-all-errors -fL --retry 5 https://xorg.freedesktop.org/archive/individual/lib/libXrandr-${LIBXRANDR_VERSION}.tar.xz -o libXrandr.tar.xz && \
    tar -xf libXrandr.tar.xz && cd libXrandr-${LIBXRANDR_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-static && \
    make -j"$(nproc)" && make install && make install DESTDIR=/x11-libs

# renovate: datasource=gitlab-tags depName=xorg/lib/libxi registryUrl=https://gitlab.freedesktop.org extractVersion=^libXi-(?<version>\d+(\.\d+)*)$
ARG LIBXI_VERSION=1.8.2
RUN curl --retry-all-errors -fL --retry 5 https://xorg.freedesktop.org/archive/individual/lib/libXi-${LIBXI_VERSION}.tar.xz -o libXi.tar.xz && \
    tar -xf libXi.tar.xz && cd libXi-${LIBXI_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-static && \
    make -j"$(nproc)" && make install && make install DESTDIR=/x11-libs

# renovate: datasource=gitlab-tags depName=xorg/lib/libxinerama registryUrl=https://gitlab.freedesktop.org extractVersion=^libXinerama-(?<version>\d+(\.\d+)*)$
ARG LIBXINERAMA_VERSION=1.1.5
RUN curl --retry-all-errors -fL --retry 5 https://xorg.freedesktop.org/archive/individual/lib/libXinerama-${LIBXINERAMA_VERSION}.tar.xz -o libXinerama.tar.xz && \
    tar -xf libXinerama.tar.xz && cd libXinerama-${LIBXINERAMA_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-static && \
    make -j"$(nproc)" && make install && make install DESTDIR=/x11-libs

# renovate: datasource=gitlab-tags depName=xorg/lib/libxcursor registryUrl=https://gitlab.freedesktop.org extractVersion=^libXcursor-(?<version>\d+(\.\d+)*)$
ARG LIBXCURSOR_VERSION=1.2.3
RUN curl --retry-all-errors -fL --retry 5 https://xorg.freedesktop.org/archive/individual/lib/libXcursor-${LIBXCURSOR_VERSION}.tar.xz -o libXcursor.tar.xz && \
    tar -xf libXcursor.tar.xz && cd libXcursor-${LIBXCURSOR_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-static && \
    make -j"$(nproc)" && make install && make install DESTDIR=/x11-libs

# renovate: datasource=gitlab-tags depName=xorg/lib/libxft registryUrl=https://gitlab.freedesktop.org extractVersion=^libXft-(?<version>\d+(\.\d+)*)$
ARG LIBXFT_VERSION=2.3.9
RUN curl --retry-all-errors -fL --retry 5 https://xorg.freedesktop.org/archive/individual/lib/libXft-${LIBXFT_VERSION}.tar.xz -o libXft.tar.xz && \
    tar -xf libXft.tar.xz && cd libXft-${LIBXFT_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-static && \
    make -j"$(nproc)" && make install && make install DESTDIR=/x11-libs

# renovate: datasource=gitlab-tags depName=xorg/lib/libxscrnsaver registryUrl=https://gitlab.freedesktop.org extractVersion=^libXScrnSaver-(?<version>\d+(\.\d+)*)$
ARG LIBXSS_VERSION=1.2.4
RUN curl --retry-all-errors -fL --retry 5 https://xorg.freedesktop.org/archive/individual/lib/libXScrnSaver-${LIBXSS_VERSION}.tar.xz -o libXScrnSaver.tar.xz && \
    tar -xf libXScrnSaver.tar.xz && cd libXScrnSaver-${LIBXSS_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-static && \
    make -j"$(nproc)" && make install && make install DESTDIR=/x11-libs

# XLibre's mandatory font/XKB support libraries and the runtime xkb compiler.
# renovate: datasource=gitlab-tags depName=xorg/lib/libfontenc registryUrl=https://gitlab.freedesktop.org extractVersion=^libfontenc-(?<version>\d+(\.\d+)*)$
ARG LIBFONTENC_VERSION=1.1.8
RUN curl --retry-all-errors -fL --retry 5 https://xorg.freedesktop.org/archive/individual/lib/libfontenc-${LIBFONTENC_VERSION}.tar.xz -o libfontenc.tar.xz && \
    tar -xf libfontenc.tar.xz && cd libfontenc-${LIBFONTENC_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-static && \
    make -j"$(nproc)" && make install && make install DESTDIR=/x11-libs

# renovate: datasource=gitlab-tags depName=xorg/lib/libxfont registryUrl=https://gitlab.freedesktop.org extractVersion=^libXfont2-(?<version>\d+(\.\d+)*)$
ARG LIBXFONT2_VERSION=2.0.7
RUN curl --retry-all-errors -fL --retry 5 https://xorg.freedesktop.org/archive/individual/lib/libXfont2-${LIBXFONT2_VERSION}.tar.xz -o libXfont2.tar.xz && \
    tar -xf libXfont2.tar.xz && cd libXfont2-${LIBXFONT2_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-devel-docs --disable-static && \
    make -j"$(nproc)" && make install && make install DESTDIR=/x11-libs

# renovate: datasource=gitlab-tags depName=xorg/lib/libxkbfile registryUrl=https://gitlab.freedesktop.org extractVersion=^libxkbfile-(?<version>\d+(\.\d+)*)$
ARG LIBXKBFILE_VERSION=1.1.3
RUN curl --retry-all-errors -fL --retry 5 https://xorg.freedesktop.org/archive/individual/lib/libxkbfile-${LIBXKBFILE_VERSION}.tar.xz -o libxkbfile.tar.xz && \
    tar -xf libxkbfile.tar.xz && cd libxkbfile-${LIBXKBFILE_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-static && \
    make -j"$(nproc)" && make install && make install DESTDIR=/x11-libs

# renovate: datasource=gitlab-tags depName=xorg/app/xkbcomp registryUrl=https://gitlab.freedesktop.org extractVersion=^xkbcomp-(?<version>\d+(\.\d+)*)$
ARG XKBCOMP_VERSION=1.4.7
RUN curl --retry-all-errors -fL --retry 5 https://xorg.freedesktop.org/archive/individual/app/xkbcomp-${XKBCOMP_VERSION}.tar.xz -o xkbcomp.tar.xz && \
    tar -xf xkbcomp.tar.xz && cd xkbcomp-${XKBCOMP_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} && \
    make -j"$(nproc)" && make install && make install DESTDIR=/x11-libs

# DRM/modesetting support used by the XLibre server.
RUN pip3 install meson ninja
# renovate: datasource=gitlab-tags depName=xorg/lib/libxcvt registryUrl=https://gitlab.freedesktop.org extractVersion=^libxcvt-(?<version>\d+(\.\d+)*)$
ARG LIBXCVT_VERSION=0.1.3
RUN curl --retry-all-errors -fL --retry 5 https://xorg.freedesktop.org/archive/individual/lib/libxcvt-${LIBXCVT_VERSION}.tar.xz -o libxcvt.tar.xz && \
    tar -xf libxcvt.tar.xz && cd libxcvt-${LIBXCVT_VERSION} && \
    meson setup buildDir ${COMMON_MESON_FLAGS} && \
    ninja -C buildDir install && DESTDIR=/x11-libs ninja -C buildDir install

# libpciaccess comes from its own stage, shared with libdrm.
COPY --from=libpciaccess /libpciaccess /
COPY --from=libpciaccess /libpciaccess /x11-libs

# renovate: datasource=gitlab-tags depName=xorg/lib/libxshmfence registryUrl=https://gitlab.freedesktop.org extractVersion=^libxshmfence-(?<version>\d+(\.\d+)*)$
ARG LIBXSHMFENCE_VERSION=1.3.3
RUN curl --retry-all-errors -fL --retry 5 https://xorg.freedesktop.org/archive/individual/lib/libxshmfence-${LIBXSHMFENCE_VERSION}.tar.xz -o libxshmfence.tar.xz && \
    tar -xf libxshmfence.tar.xz && cd libxshmfence-${LIBXSHMFENCE_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-static && \
    make -j"$(nproc)" && make install && make install DESTDIR=/x11-libs

# i3's XCB utility dependencies.
# renovate: datasource=gitlab-tags depName=xorg/lib/libxcb-util registryUrl=https://gitlab.freedesktop.org extractVersion=^xcb-util-(?<version>\d+(\.\d+)*)$
ARG XCB_UTIL_VERSION=0.4.1
RUN curl --retry-all-errors -fL --retry 5 https://xcb.freedesktop.org/dist/xcb-util-${XCB_UTIL_VERSION}.tar.xz -o xcb-util.tar.xz && \
    tar -xf xcb-util.tar.xz && cd xcb-util-${XCB_UTIL_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-static && \
    make -j"$(nproc)" && make install && make install DESTDIR=/x11-libs

# renovate: datasource=gitlab-tags depName=xorg/lib/libxcb-image registryUrl=https://gitlab.freedesktop.org extractVersion=^xcb-util-image-(?<version>\d+(\.\d+)*)$
ARG XCB_UTIL_IMAGE_VERSION=0.4.1
RUN curl --retry-all-errors -fL --retry 5 https://xcb.freedesktop.org/dist/xcb-util-image-${XCB_UTIL_IMAGE_VERSION}.tar.xz -o xcb-image.tar.xz && \
    tar -xf xcb-image.tar.xz && cd xcb-util-image-${XCB_UTIL_IMAGE_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-static && \
    make -j"$(nproc)" && make install && make install DESTDIR=/x11-libs

# renovate: datasource=gitlab-tags depName=xorg/lib/libxcb-render-util registryUrl=https://gitlab.freedesktop.org extractVersion=^xcb-util-renderutil-(?<version>\d+(\.\d+)*)$
ARG XCB_UTIL_RENDERUTIL_VERSION=0.3.10
RUN curl --retry-all-errors -fL --retry 5 https://xcb.freedesktop.org/dist/xcb-util-renderutil-${XCB_UTIL_RENDERUTIL_VERSION}.tar.xz -o xcb-renderutil.tar.xz && \
    tar -xf xcb-renderutil.tar.xz && cd xcb-util-renderutil-${XCB_UTIL_RENDERUTIL_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-static && \
    make -j"$(nproc)" && make install && make install DESTDIR=/x11-libs

# renovate: datasource=gitlab-tags depName=xorg/lib/libxcb-cursor registryUrl=https://gitlab.freedesktop.org extractVersion=^xcb-util-cursor-(?<version>\d+(\.\d+)*)$
ARG XCB_UTIL_CURSOR_VERSION=0.1.5
RUN curl --retry-all-errors -fL --retry 5 https://xcb.freedesktop.org/dist/xcb-util-cursor-${XCB_UTIL_CURSOR_VERSION}.tar.xz -o xcb-cursor.tar.xz && \
    tar -xf xcb-cursor.tar.xz && cd xcb-util-cursor-${XCB_UTIL_CURSOR_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-static && \
    make -j"$(nproc)" && make install && make install DESTDIR=/x11-libs

# renovate: datasource=gitlab-tags depName=xorg/lib/libxcb-keysyms registryUrl=https://gitlab.freedesktop.org extractVersion=^xcb-util-keysyms-(?<version>\d+(\.\d+)*)$
ARG XCB_UTIL_KEYSYMS_VERSION=0.4.1
RUN curl --retry-all-errors -fL --retry 5 https://xcb.freedesktop.org/dist/xcb-util-keysyms-${XCB_UTIL_KEYSYMS_VERSION}.tar.xz -o xcb-keysyms.tar.xz && \
    tar -xf xcb-keysyms.tar.xz && cd xcb-util-keysyms-${XCB_UTIL_KEYSYMS_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-static && \
    make -j"$(nproc)" && make install && make install DESTDIR=/x11-libs

# renovate: datasource=gitlab-tags depName=xorg/lib/libxcb-wm registryUrl=https://gitlab.freedesktop.org extractVersion=^xcb-util-wm-(?<version>\d+(\.\d+)*)$
ARG XCB_UTIL_WM_VERSION=0.4.2
RUN curl --retry-all-errors -fL --retry 5 https://xcb.freedesktop.org/dist/xcb-util-wm-${XCB_UTIL_WM_VERSION}.tar.xz -o xcb-wm.tar.xz && \
    tar -xf xcb-wm.tar.xz && cd xcb-util-wm-${XCB_UTIL_WM_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-static && \
    make -j"$(nproc)" && make install && make install DESTDIR=/x11-libs

# xcb-util-xrm uses the BSD queue macros. musl does not ship sys/queue.h, so
# install the header-only compatibility pair from libbsd for this build graph.
# renovate: datasource=gitlab-tags depName=libbsd/libbsd registryUrl=https://gitlab.freedesktop.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG LIBBSD_HEADERS_VERSION=0.12.2
RUN curl --retry-all-errors -fL --retry 5 https://libbsd.freedesktop.org/releases/libbsd-${LIBBSD_HEADERS_VERSION}.tar.xz -o libbsd.tar.xz && \
    tar -xf libbsd.tar.xz && \
    mkdir -p /usr/include/sys /usr/include/bsd/sys /x11-libs/usr/include/sys /x11-libs/usr/include/bsd/sys && \
    cp libbsd-${LIBBSD_HEADERS_VERSION}/include/bsd/sys/queue.h /usr/include/sys/queue.h && \
    cp libbsd-${LIBBSD_HEADERS_VERSION}/include/bsd/sys/cdefs.h /usr/include/bsd/sys/cdefs.h && \
    cp /usr/include/sys/queue.h /x11-libs/usr/include/sys/queue.h && \
    cp /usr/include/bsd/sys/cdefs.h /x11-libs/usr/include/bsd/sys/cdefs.h

# renovate: datasource=github-tags depName=Airblader/xcb-util-xrm extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG XCB_UTIL_XRM_VERSION=1.3
RUN curl --retry-all-errors -fL --retry 5 https://github.com/Airblader/xcb-util-xrm/releases/download/v${XCB_UTIL_XRM_VERSION}/xcb-util-xrm-${XCB_UTIL_XRM_VERSION}.tar.gz -o xcb-xrm.tar.gz && \
    tar -xf xcb-xrm.tar.gz && cd xcb-util-xrm-${XCB_UTIL_XRM_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-static && \
    make -j"$(nproc)" && make install && make install DESTDIR=/x11-libs

ARG STARTUP_NOTIFICATION_VERSION=0.12
RUN curl --retry-all-errors -fL --retry 5 https://www.freedesktop.org/software/startup-notification/releases/startup-notification-${STARTUP_NOTIFICATION_VERSION}.tar.gz -o startup-notification.tar.gz && \
    tar -xf startup-notification.tar.gz && cd startup-notification-${STARTUP_NOTIFICATION_VERSION} && \
    ./configure --quiet --prefix=/usr --enable-shared --disable-static && \
    make -j"$(nproc)" && make install && make install DESTDIR=/x11-libs

ARG LIBEV_VERSION=4.33
RUN curl --retry-all-errors -fL --retry 5 https://dist.schmorp.de/libev/libev-${LIBEV_VERSION}.tar.gz -o libev.tar.gz && \
    tar -xf libev.tar.gz && cd libev-${LIBEV_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-static && \
    make -j"$(nproc)" && make install && make install DESTDIR=/x11-libs

# renovate: datasource=github-tags depName=lloyd/yajl extractVersion=^(?<version>\d+(\.\d+)*)$
ARG YAJL_VERSION=2.1.0
RUN curl --retry-all-errors -fL --retry 5 https://github.com/lloyd/yajl/archive/refs/tags/${YAJL_VERSION}.tar.gz -o yajl.tar.gz && \
    tar -xf yajl.tar.gz && cd yajl-${YAJL_VERSION} && \
    sed -i '/ADD_SUBDIRECTORY(test)/,$d' CMakeLists.txt && \
    cmake -G Ninja -B buildDir -DCMAKE_POLICY_VERSION_MINIMUM=3.5 -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/usr -DCMAKE_INSTALL_LIBDIR=lib -DBUILD_SHARED_LIBS=ON && \
    ninja -C buildDir && ninja -C buildDir install && DESTDIR=/x11-libs ninja -C buildDir install

# renovate: datasource=gitlab-tags depName=xorg/lib/libxxf86vm registryUrl=https://gitlab.freedesktop.org extractVersion=^libXxf86vm-(?<version>\d+(\.\d+)*)$
ARG LIBXXF86VM_VERSION=1.1.7
RUN curl --retry-all-errors -fL --retry 5 https://xorg.freedesktop.org/archive/individual/lib/libXxf86vm-${LIBXXF86VM_VERSION}.tar.xz -o libXxf86vm.tar.xz && \
    tar -xf libXxf86vm.tar.xz && cd libXxf86vm-${LIBXXF86VM_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-static && \
    make -j"$(nproc)" && make install && make install DESTDIR=/x11-libs


# libxkbcommon with its X11/XCB bridge (the Sway variant keeps the lean,
# Wayland-only build from the earlier libxkb stage).
FROM toolchain AS libxkb-x11
COPY --from=xkeyboard-config /xkeyboard-config /
COPY --from=xorgproto /xorgproto /
COPY --from=xcbproto /xcbproto /
COPY --from=libxau /libxau /
COPY --from=libxcb /libxcb /
# renovate: datasource=github-tags depName=xkbcommon/libxkbcommon extractVersion=^xkbcommon-(?<version>\d+(\.\d+)*)$
ARG LIBXKBCOMMON_VERSION=1.13.0
RUN mkdir -p /libxkb-x11
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/xkbcommon/libxkbcommon/archive/refs/tags/xkbcommon-${LIBXKBCOMMON_VERSION}.tar.gz -o libxkbcommon.tar.gz && \
    tar -xf libxkbcommon.tar.gz && mv libxkbcommon-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Denable-x11=true -Denable-wayland=false -Denable-xkbregistry=true -Denable-bash-completion=false
RUN DESTDIR=/libxkb-x11 ninja -C buildDir install


# The Vulkan loader for i3 (see vulkan-src): XCB, Xlib and Wayland WSI. The X11
# libraries are only needed for their headers; the loader links none of them.
FROM vulkan-src AS vulkan-loader-x11
COPY --from=xorgproto /xorgproto /
COPY --from=xcbproto /xcbproto /
COPY --from=libxau /libxau /
COPY --from=libxcb /libxcb /
COPY --from=x11-libs /x11-libs /
RUN export CFLAGS="$(printf '%s' "$CFLAGS" | sed 's/-flto[^ ]*//g')" LDFLAGS="$(printf '%s' "$LDFLAGS" | sed 's/-flto[^ ]*//g')" && \
    cmake ${VULKAN_LOADER_CMAKE} -DBUILD_WSI_XCB_SUPPORT=ON -DBUILD_WSI_XLIB_SUPPORT=ON -DBUILD_WSI_WAYLAND_SUPPORT=ON && \
    ninja -C loader/build && DESTDIR=/vulkan-loader ninja -C loader/build install && \
    rm -rf /vulkan-loader/usr/include /vulkan-loader/usr/lib/pkgconfig /vulkan-loader/usr/lib/cmake && \
    test -e /vulkan-loader/usr/lib/libvulkan.so.1 && ! test -e /vulkan-loader/usr/include


# Mesa rebuilt for the X11 platform and DRI GLX. GPU retains the same vm/full
# semantics as the Sway Mesa stage (the same GL, Vulkan and VA-API drivers;
# see the comment there for each driver and for LTO), and every mode builds
# llvmpipe. The Vulkan drivers get the X11 WSI here too.
FROM toolchain AS mesa-x11
COPY --from=libdrm /libdrm /
COPY --from=llvm-stack /llvm-out /llvm-out
RUN rsync -aHAX --keep-dirlinks /llvm-out/. / && rm -rf /llvm-out
COPY --from=xorgproto /xorgproto /
COPY --from=xcbproto /xcbproto /
COPY --from=libxau /libxau /
COPY --from=libxcb /libxcb /
COPY --from=x11-libs /x11-libs /
# The Wayland platform too: the graphical installer (cage and
# isotope-install-gui) is a Wayland client on every variant, i3 included.
COPY --from=wayland /wayland /
COPY --from=glslang /glslang /
COPY --from=mesa-rust /mesa-rust/ /
ENV CARGO_HOME=/usr/local/cargo \
    RUSTUP_HOME=/usr/local/rustup \
    PATH=/usr/local/cargo/bin:${PATH}
COPY --from=libva /libva /libva-out
COPY --from=vulkan-loader-x11 /vulkan-loader /mesa-x11
# renovate: datasource=gitlab-tags depName=mesa/mesa registryUrl=https://gitlab.freedesktop.org extractVersion=^mesa-(?<version>\d+(\.\d+)*)$
ARG MESA_VERSION=26.3.1
ARG GPU=vm
RUN mkdir -p /mesa-x11
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://archive.mesa3d.org/mesa-${MESA_VERSION}.tar.xz -o mesa.tar.xz && \
    tar -xf mesa.tar.xz && mv mesa-* src
WORKDIR /build/src
RUN pip3 install meson ninja setuptools packaging mako pyyaml
# venus-protocol without git, as in the Sway Mesa stage.
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://gitlab.freedesktop.org/virgl/venus-protocol/-/archive/v1.1.3/venus-protocol-v1.1.3.tar.gz -o venus.tgz && \
    echo "c5a362ca28e0a35042db1baeadaf9183500b216841e49d72714b80bae50b4445  venus.tgz" | sha256sum -c - && \
    grep -qx 'directory = venus-protocol-1.1.3' subprojects/venus-protocol.wrap && \
    grep -qx 'revision = v1.1.3' subprojects/venus-protocol.wrap && \
    tar -xzf venus.tgz -C subprojects && mv subprojects/venus-protocol-v1.1.3 subprojects/venus-protocol-1.1.3 && rm venus.tgz
RUN if [ "$GPU" = "full" ]; then \
      DRV="iris,crocus,i915,radeonsi,r600,r300,nouveau,zink,llvmpipe,softpipe,virgl,svga"; \
      VK="amd,intel,intel_hasvk,nouveau,swrast,virtio"; VKLAYERS="device-select"; VA=enabled; \
      RUSTARGS="-Drust_args=-Ctarget-feature=-crt-static"; \
      rsync -aHAX --keep-dirlinks /libva-out/. /; \
    else \
      DRV="llvmpipe,softpipe,virgl,svga"; \
      VK="swrast,virtio"; VKLAYERS=""; VA=disabled; RUSTARGS=""; \
    fi; \
    export CFLAGS="$(printf '%s' "$CFLAGS" | sed 's/-flto[^ ]*//g')" \
           CXXFLAGS="$(printf '%s' "${CXXFLAGS:-}" | sed 's/-flto[^ ]*//g')" \
           LDFLAGS="$(printf '%s' "$LDFLAGS" | sed 's/-flto[^ ]*//g') -Wl,--exclude-libs,libunwind.a"; \
    meson setup buildDir ${COMMON_MESON_FLAGS} ${RUSTARGS} -Dplatforms=x11,wayland \
      -Dgallium-drivers="$DRV" -Dvulkan-drivers="$VK" -Dvulkan-layers="$VKLAYERS" \
      -Dgallium-va="$VA" -Dvideo-codecs=all \
      -Dglx=dri -Dopengl=true -Dgles1=enabled -Dgles2=enabled \
      -Degl=enabled -Dllvm=enabled -Dcpp_rtti=false -Dbuild-tests=false
RUN DESTDIR=/mesa-x11 ninja -C buildDir install && \
    if [ -e /mesa-x11/usr/lib/libvulkan_nouveau.so ]; then \
      nm -D --defined-only /mesa-x11/usr/lib/libvulkan_nouveau.so >/tmp/nvk.syms && \
      ! grep -qw -e malloc -e _Unwind_RaiseException /tmp/nvk.syms; \
    fi
# The same bundled libraries as the Sway Mesa stage.
RUN mkdir -p /mesa-x11/usr/lib && cp -a /usr/lib/libLLVM.so* /mesa-x11/usr/lib/ && \
    if [ "$GPU" = "full" ]; then \
      cp -a /usr/lib/libelf.so* /usr/lib/libSPIRV-Tools*.so* /mesa-x11/usr/lib/ && \
      rsync -aHAX --keep-dirlinks --exclude=/usr/include --exclude=/usr/lib/pkgconfig /libva-out/. /mesa-x11/ ; \
    fi && \
    rm -rf /libva-out


FROM toolchain AS libepoxy-x11
COPY --from=xorgproto /xorgproto /
COPY --from=xcbproto /xcbproto /
COPY --from=libxau /libxau /
COPY --from=libxcb /libxcb /
COPY --from=x11-libs /x11-libs /
COPY --from=mesa-x11 /mesa-x11 /
# renovate: datasource=github-tags depName=anholt/libepoxy extractVersion=^(?<version>\d+(\.\d+)*)$
ARG LIBEPOXY_VERSION=1.5.10
RUN mkdir -p /libepoxy-x11
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/anholt/libepoxy/archive/refs/tags/${LIBEPOXY_VERSION}.tar.gz -o epoxy.tar.gz && \
    tar -xf epoxy.tar.gz && mv libepoxy-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dglx=yes -Dx11=true -Degl=yes -Dtests=false
RUN DESTDIR=/libepoxy-x11 ninja -C buildDir install


# X11-enabled Cairo/Pango pair used by i3 and dunst. Keeping these separate
# avoids adding libX11 dependencies to Sway's Cairo runtime.
FROM toolchain AS cairo-x11
COPY --from=xorgproto /xorgproto /
COPY --from=xcbproto /xcbproto /
COPY --from=libxau /libxau /
COPY --from=libxcb /libxcb /
COPY --from=x11-libs /x11-libs /
COPY --from=pixman /pixman /
COPY --from=freetype /freetype /
COPY --from=fontconfig /fontconfig /
COPY --from=libpng /libpng /
COPY --from=pcre2 /pcre2 /
COPY --from=glib2 /glib2 /
# renovate: datasource=gitlab-tags depName=cairo/cairo registryUrl=https://gitlab.freedesktop.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG CAIRO_VERSION=1.18.4
RUN mkdir -p /cairo-x11
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://cairographics.org/releases/cairo-${CAIRO_VERSION}.tar.xz -o cairo.tar.xz && \
    tar -xf cairo.tar.xz && mv cairo-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dtests=disabled -Dglib=enabled -Dspectre=disabled \
    -Dfreetype=enabled -Dfontconfig=enabled -Dpng=enabled -Dxlib=enabled -Dxcb=enabled
RUN DESTDIR=/cairo-x11 ninja -C buildDir install

FROM toolchain AS pango-x11
COPY --from=xorgproto /xorgproto /
COPY --from=xcbproto /xcbproto /
COPY --from=libxau /libxau /
COPY --from=libxcb /libxcb /
COPY --from=x11-libs /x11-libs /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=harfbuzz /harfbuzz /
COPY --from=freetype /freetype /
COPY --from=fontconfig /fontconfig /
COPY --from=fribidi /fribidi /
COPY --from=cairo-x11 /cairo-x11 /
COPY --from=pixman /pixman /
COPY --from=libpng /libpng /
# renovate: datasource=gitlab-tags depName=GNOME/pango registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG PANGO_VERSION=1.54.0
RUN mkdir -p /pango-x11
WORKDIR /build
RUN PANGO_MAJOR="${PANGO_VERSION%.*}" && curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.gnome.org/sources/pango/${PANGO_MAJOR}/pango-${PANGO_VERSION}.tar.xz -o pango.tar.xz && \
    tar -xf pango.tar.xz && mv pango-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dintrospection=disabled -Dgtk_doc=false -Dfontconfig=enabled -Dfreetype=enabled -Dcairo=enabled
RUN DESTDIR=/pango-x11 ninja -C buildDir install


# XLibre X server. 25.2.0 is the current stable branch and includes the Xorg
# modesetting DDX; only the external libinput DDX is needed below.
FROM toolchain AS xlibre
COPY --from=xorgproto /xorgproto /
COPY --from=xcbproto /xcbproto /
COPY --from=libxau /libxau /
COPY --from=libxcb /libxcb /
COPY --from=freetype /freetype /
COPY --from=libpng /libpng /
COPY --from=libucontext /libucontext /
COPY --from=x11-libs /x11-libs /
COPY --from=xkeyboard-config /xkeyboard-config /
COPY --from=pixman /pixman /
COPY --from=libdrm /libdrm /
COPY --from=mesa-x11 /mesa-x11 /
COPY --from=libepoxy-x11 /libepoxy-x11 /
COPY --from=libseat /libseat /
# renovate: datasource=github-tags depName=X11Libre/xserver extractVersion=^xlibre-xserver-(?<version>\d+(\.\d+)*)$
ARG XLIBRE_VERSION=25.2.0
RUN mkdir -p /xlibre
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/X11Libre/xserver/archive/refs/tags/xlibre-xserver-${XLIBRE_VERSION}.tar.gz -o xlibre.tar.gz && \
    tar -xf xlibre.tar.gz && mv xserver-* src && \
    sed -i '/#include <stddef.h>/a #include <sys/random.h>' src/os/osdep.h && \
    sed -i 's/while (pos < len)/while (pos < nbytes)/; s/getrandom(buf + pos/getrandom((unsigned char *)buf + pos/' src/os/osdep.h
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} \
      -Dxorg=true -Dxorg-sdk=true -Dxnest=false -Dxephyr=false -Dxfbdev=false -Dxvfb=true \
      -Dglamor=true -Dgbm=true -Dglx=true -Dglx_dri=true \
      -Ddri1=false -Ddri2=true -Ddri3=true -Ddrm=true \
      -Dxdmcp=false -Dxdm-auth-1=false -Dint10=false -Dagp=false -Ddga=false \
      -Dudev=true -Dudev_kms=true -Dsystemd_notify=false -Dsystemd_logind=false -Dseatd_libseat=true \
      -Dsuid_wrapper=false -Dlisten_tcp=false -Dsha1=libcrypto -Dxselinux=false \
      -Dxkb_dir=/usr/share/X11/xkb -Dxkb_output_dir=/var/lib/xkb -Dxkb_bin_dir=/usr/bin \
      -Dxf86-input-inputtest=false -Dtests=false -Ddocs=false -Ddevel-docs=false
RUN DESTDIR=/xlibre ninja -C buildDir install


FROM toolchain AS xlibre-input-libinput
COPY --from=xorgproto /xorgproto /
COPY --from=libevdev /libevdev /
COPY --from=libinput /libinput /
COPY --from=libucontext /libucontext /
COPY --from=pixman /pixman /
COPY --from=freetype /freetype /
COPY --from=libpng /libpng /
COPY --from=x11-libs /x11-libs /
COPY --from=xlibre /xlibre /
# renovate: datasource=github-tags depName=X11Libre/xf86-input-libinput extractVersion=^xlibre-xf86-input-libinput-(?<version>\d+(\.\d+)*)$
ARG XLIBRE_INPUT_LIBINPUT_VERSION=25.0.1
RUN mkdir -p /xlibre-input-libinput
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/X11Libre/xf86-input-libinput/archive/refs/tags/xlibre-xf86-input-libinput-${XLIBRE_INPUT_LIBINPUT_VERSION}.tar.gz -o driver.tar.gz && \
    tar -xf driver.tar.gz && mv xf86-input-libinput-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS}
RUN DESTDIR=/xlibre-input-libinput ninja -C buildDir install


FROM toolchain AS i3
COPY --from=xorgproto /xorgproto /
COPY --from=xcbproto /xcbproto /
COPY --from=libxau /libxau /
COPY --from=libxcb /libxcb /
COPY --from=x11-libs /x11-libs /
COPY --from=libxkb-x11 /libxkb-x11 /
COPY --from=cairo-x11 /cairo-x11 /
COPY --from=pango-x11 /pango-x11 /
COPY --from=pixman /pixman /
COPY --from=pcre2 /pcre2 /
COPY --from=glib2 /glib2 /
COPY --from=harfbuzz /harfbuzz /
COPY --from=fribidi /fribidi /
COPY --from=freetype /freetype /
COPY --from=fontconfig /fontconfig /
COPY --from=libpng /libpng /
# renovate: datasource=github-tags depName=i3/i3 extractVersion=^(?<version>\d+(\.\d+)*)$
ARG I3_VERSION=4.25.1
RUN mkdir -p /i3
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://i3wm.org/downloads/i3-${I3_VERSION}.tar.xz -o i3.tar.xz && \
    tar -xf i3.tar.xz && mv i3-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Ddocs=false -Dmans=false
RUN DESTDIR=/i3 ninja -C buildDir install


# Native X11 desktop tools: xauth for Ly's X session cookie, st terminal, dmenu
# launcher, xsel clipboard and clipnotify for the clipboard history. These
# replace foot/fuzzel/wl-clipboard/cliphist; the isotope-xroot stage below
# replaces swaybg.
FROM toolchain AS x11-desktop-tools
COPY --from=xorgproto /xorgproto /
COPY --from=xcbproto /xcbproto /
COPY --from=libxau /libxau /
COPY --from=libxcb /libxcb /
COPY --from=x11-libs /x11-libs /
COPY --from=freetype /freetype /
COPY --from=libpng /libpng /
COPY --from=fontconfig /fontconfig /
RUN mkdir -p /x11-desktop-tools/usr/bin
WORKDIR /build
# renovate: datasource=gitlab-tags depName=xorg/app/xauth registryUrl=https://gitlab.freedesktop.org extractVersion=^xauth-(?<version>\d+(\.\d+)*)$
ARG XAUTH_VERSION=1.1.5
# xauth only uses XmuGetHostname from libXmuu; that function is a thin
# gethostname(2) wrapper. Use libc directly instead of pulling libXt and the
# rest of libXmu's legacy widget dependency chain into this small image.
RUN curl --retry-all-errors -fL --retry 5 https://xorg.freedesktop.org/archive/individual/app/xauth-${XAUTH_VERSION}.tar.xz -o xauth.tar.xz && \
    tar -xf xauth.tar.xz && cd xauth-${XAUTH_VERSION} && \
    sed -i -e 's|#include <X11/Xmu/SysUtil.h>|#include <unistd.h>|' \
           -e 's/XmuGetHostname/gethostname/' \
           -e '/(void) gethostname (buf, maxlen);/a\    buf[maxlen - 1] = '\''\\0'\'';' parsedpy.c && \
    XAUTH_CFLAGS="-I/usr/include $(pkg-config --cflags x11 xau xext)" \
    XAUTH_LIBS="$(pkg-config --libs x11 xau xext)" \
    ./configure ${COMMON_CONFIGURE_ARGS} && \
    make -j"$(nproc)" && make install DESTDIR=/x11-desktop-tools

# xrdb loads ~/.Xresources into the X server: ly's setup.sh runs
# `xrdb -merge ~/.Xresources` at every i3 login, and the Welcome window's
# Accessibility page sets the display scale there (Xft.dpi). Without a cpp on
# the image xrdb reads the file as it is (no #include or #define). Like
# xauth, it takes only XmuGetHostname from libXmuu; use gethostname(2).
# renovate: datasource=gitlab-tags depName=xorg/app/xrdb registryUrl=https://gitlab.freedesktop.org extractVersion=^xrdb-(?<version>\d+(\.\d+)*)$
ARG XRDB_VERSION=1.2.3
RUN curl --retry-all-errors -fL --retry 5 https://xorg.freedesktop.org/archive/individual/app/xrdb-${XRDB_VERSION}.tar.xz -o xrdb.tar.xz && \
    tar -xf xrdb.tar.xz && cd xrdb-${XRDB_VERSION} && \
    sed -i -e 's|#include <X11/Xmu/SysUtil.h>|#include <unistd.h>|' \
           -e 's/XmuGetHostname(client, MAXHOSTNAMELEN);/gethostname(client, MAXHOSTNAMELEN);\n    client[MAXHOSTNAMELEN - 1] = 0;/' xrdb.c && \
    grep -q 'client\[MAXHOSTNAMELEN - 1\] = 0;' xrdb.c && \
    XRDB_CFLAGS="-I/usr/include $(pkg-config --cflags x11)" \
    XRDB_LIBS="$(pkg-config --libs x11)" \
    ./configure ${COMMON_CONFIGURE_ARGS} && \
    make -j"$(nproc)" && make install DESTDIR=/x11-desktop-tools

# renovate: datasource=git-tags depName=https://git.suckless.org/st extractVersion=^(?<version>\d+(\.\d+)*)$
ARG ST_VERSION=0.9.3
RUN curl --retry-all-errors -fL --retry 5 https://dl.suckless.org/st/st-${ST_VERSION}.tar.gz -o st.tar.gz && \
    tar -xf st.tar.gz && cd st-${ST_VERSION} && \
    sed -i -e 's|^PREFIX = .*|PREFIX = /usr|' -e 's|^X11INC = .*|X11INC = /usr/include|' -e 's|^X11LIB = .*|X11LIB = /usr/lib|' config.mk && \
    sed -i -e 's|Liberation Mono:pixelsize=12:antialias=true:autohint=true|JetBrains Mono:pixelsize=15:antialias=true:autohint=true|' \
           -e 's|static int borderpx = 2;|static int borderpx = 10;|' \
           -e 's|char \*termname = "st-256color";|char *termname = "xterm-256color";|' \
           -e 's|"gray90", /\* default foreground colour \*/|"#f4f5f2", /* default foreground colour */|' \
           -e 's|"black", /\* default background colour \*/|"#141411", /* default background colour */|' config.def.h && \
    grep -q '"JetBrains Mono:pixelsize=15:' config.def.h && \
    make -j"$(nproc)" CC=cc && install -m0755 st /x11-desktop-tools/usr/bin/st

# renovate: datasource=git-tags depName=https://git.suckless.org/dmenu extractVersion=^(?<version>\d+(\.\d+)*)$
ARG DMENU_VERSION=5.3
RUN curl --retry-all-errors -fL --retry 5 https://dl.suckless.org/tools/dmenu-${DMENU_VERSION}.tar.gz -o dmenu.tar.gz && \
    tar -xf dmenu.tar.gz && cd dmenu-${DMENU_VERSION} && \
    sed -i -e 's|^PREFIX = .*|PREFIX = /usr|' -e 's|^X11INC = .*|X11INC = /usr/include|' -e 's|^X11LIB = .*|X11LIB = /usr/lib|' config.mk && \
    sed -i -e 's|"monospace:size=10"|"Inter Variable:size=11"|' config.def.h && \
    grep -q '"Inter Variable:size=11"' config.def.h && \
    make -j"$(nproc)" CC=cc && make install CC=cc DESTDIR=/x11-desktop-tools

# renovate: datasource=github-tags depName=kfish/xsel extractVersion=^(?<version>\d+(\.\d+)*)$
ARG XSEL_VERSION=1.2.1
RUN curl --retry-all-errors -fL --retry 5 https://github.com/kfish/xsel/archive/refs/tags/${XSEL_VERSION}.tar.gz -o xsel.tar.gz && \
    tar -xf xsel.tar.gz && cd xsel-${XSEL_VERSION} && \
    autoreconf -fi && ./configure ${COMMON_CONFIGURE_ARGS} && \
    make -j"$(nproc)" && make install DESTDIR=/x11-desktop-tools

# xclip puts the i3 screenshots on the clipboard. xsel above cannot: it only
# offers text targets, and a pasted screenshot needs image/png (`xclip -t`).
# Like xauth, xclip only takes two atom macros from libXmu; define them with
# XInternAtom instead of pulling libXmu, libXt, libSM and libICE into the image.
# The Xmu header also brought in Xresource.h and string.h, so include those.
# renovate: datasource=github-tags depName=astrand/xclip extractVersion=^(?<version>\d+(\.\d+)*)$
ARG XCLIP_VERSION=0.13
RUN curl --retry-all-errors -fL --retry 5 https://github.com/astrand/xclip/archive/refs/tags/${XCLIP_VERSION}.tar.gz -o xclip.tar.gz && \
    tar -xf xclip.tar.gz && cd xclip-${XCLIP_VERSION} && \
    sed -i -e '/X11\/Xmu\/Atoms.h/d' -e '/X11\/Intrinsic.h/d' -e '/AC_CHECK_LIB(Xmu/d' configure.ac && \
    sed -i 's/ -lXmu//' Makefile.in && \
    sed -i 's|^#include <X11/Xmu/Atoms.h>$|#include <X11/Xresource.h>\n#include <string.h>\n#define XA_CLIPBOARD(d) XInternAtom((d), "CLIPBOARD", False)\n#define XA_UTF8_STRING(d) XInternAtom((d), "UTF8_STRING", False)|' xclip.c && \
    autoreconf -fi && ./configure ${COMMON_CONFIGURE_ARGS} && \
    make -j"$(nproc)" && install -m0755 xclip /x11-desktop-tools/usr/bin/xclip

# xsct is the i3 night light: isotope-night-light runs it every minute, and it
# sets the X RandR gamma once and exits. One C file against libX11 and
# libXrandr, which the i3 image already has.
# renovate: datasource=github-tags depName=faf0/sct extractVersion=^(?<version>\d+(\.\d+)*)$
ARG XSCT_VERSION=2.3
RUN curl --retry-all-errors -fL --retry 5 https://github.com/faf0/sct/archive/refs/tags/${XSCT_VERSION}.tar.gz -o sct.tar.gz && \
    tar -xf sct.tar.gz && cd sct-${XSCT_VERSION} && \
    make CC=cc LDFLAGS=-s && install -m0755 xsct /x11-desktop-tools/usr/bin/xsct
# setxkbmap switches the i3 keyboard layout at once: the live welcome runs it
# when the user picks a layout. Against libxkbfile, libX11 and libXrandr from
# x11-libs; only the binary is shipped (no man page). Built here, not next to
# xkbcomp in x11-libs, because x11-libs also goes into the Wayland variants.
# renovate: datasource=gitlab-tags depName=xorg/app/setxkbmap registryUrl=https://gitlab.freedesktop.org extractVersion=^setxkbmap-(?<version>\d+(\.\d+)*)$
ARG SETXKBMAP_VERSION=1.3.5
RUN curl --retry-all-errors -fL --retry 5 https://xorg.freedesktop.org/archive/individual/app/setxkbmap-${SETXKBMAP_VERSION}.tar.xz -o setxkbmap.tar.xz && \
    tar -xf setxkbmap.tar.xz && cd setxkbmap-${SETXKBMAP_VERSION} && \
    ./configure ${COMMON_CONFIGURE_ARGS} && \
    make -j"$(nproc)" && install -s -m0755 setxkbmap /x11-desktop-tools/usr/bin/setxkbmap
# clipnotify waits for the next clipboard change (XFixes) and exits; the
# isotope-clipboard daemon runs it in a loop to record the i3 clipboard
# history, reading the text with xsel and the types with xclip. One C file.
# Not clipmenu: its C rewrite has no release tag to pin, and its last tagged
# release is a bash script.
# renovate: datasource=github-tags depName=cdown/clipnotify extractVersion=^(?<version>\d+(\.\d+)*)$
ARG CLIPNOTIFY_VERSION=1.0.2
RUN curl --retry-all-errors -fL --retry 5 https://github.com/cdown/clipnotify/archive/refs/tags/${CLIPNOTIFY_VERSION}.tar.gz -o clipnotify.tar.gz && \
    tar -xf clipnotify.tar.gz && cd clipnotify-${CLIPNOTIFY_VERSION} && \
    cc ${CFLAGS:-} clipnotify.c -o clipnotify ${LDFLAGS:-} -lX11 -lXfixes && \
    install -m0755 clipnotify /x11-desktop-tools/usr/bin/clipnotify


# isotope-xroot: the i3 wallpaper. Paints a PNG scaled to fill each monitor on
# the X root window, with cairo (already in the i3 image for i3 and dunst).
FROM toolchain AS isotope-xroot
COPY --from=xorgproto /xorgproto /
COPY --from=xcbproto /xcbproto /
COPY --from=libxau /libxau /
COPY --from=libxcb /libxcb /
COPY --from=x11-libs /x11-libs /
COPY --from=pixman /pixman /
COPY --from=freetype /freetype /
COPY --from=fontconfig /fontconfig /
COPY --from=libpng /libpng /
COPY --from=cairo-x11 /cairo-x11 /
COPY tools/isotope-xroot.c /build/isotope-xroot.c
RUN mkdir -p /isotope-xroot/usr/bin && \
    cc -O2 -Wall -Wextra -Werror /build/isotope-xroot.c $(pkg-config --cflags --libs cairo-xlib cairo-png xrandr x11) -lm \
      -o /isotope-xroot/usr/bin/isotope-xroot


# X11 notification daemon replacing mako.
FROM toolchain AS dunst
COPY --from=libucontext /libucontext /
COPY --from=xorgproto /xorgproto /
COPY --from=xcbproto /xcbproto /
COPY --from=libxau /libxau /
COPY --from=libxcb /libxcb /
COPY --from=x11-libs /x11-libs /
COPY --from=cairo-x11 /cairo-x11 /
COPY --from=pango-x11 /pango-x11 /
COPY --from=pixman /pixman /
COPY --from=pcre2 /pcre2 /
COPY --from=glib2 /glib2 /
COPY --from=harfbuzz /harfbuzz /
COPY --from=fribidi /fribidi /
COPY --from=freetype /freetype /
COPY --from=fontconfig /fontconfig /
COPY --from=libpng /libpng /
COPY --from=gdk-pixbuf /gdk-pixbuf /
# renovate: datasource=github-tags depName=dunst-project/dunst extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG DUNST_VERSION=1.13.0
RUN mkdir -p /dunst
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/dunst-project/dunst/archive/refs/tags/v${DUNST_VERSION}.tar.gz -o dunst.tar.gz && \
    tar -xf dunst.tar.gz && mv dunst-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dx11=enabled -Dwayland=disabled -Ddunstify=disabled -Dsystemd=enabled -Ddocs=disabled
RUN DESTDIR=/dunst ninja -C buildDir install


# i3lock — the i3 screen locker, run by Super+Alt+L and by xss-lock. Every
# library it needs is already in the i3 image (XCB utils, xcb-xrm and libev
# from x11-libs, xkbcommon-x11, X11 Cairo). It checks the password through PAM
# (rootfs/i3/etc/pam.d/i3lock), so the binary is not setuid.
FROM toolchain AS i3lock
COPY --from=xorgproto /xorgproto /
COPY --from=xcbproto /xcbproto /
COPY --from=libxau /libxau /
COPY --from=libxcb /libxcb /
COPY --from=x11-libs /x11-libs /
COPY --from=libxkb-x11 /libxkb-x11 /
COPY --from=cairo-x11 /cairo-x11 /
COPY --from=pixman /pixman /
COPY --from=pcre2 /pcre2 /
COPY --from=glib2 /glib2 /
COPY --from=freetype /freetype /
COPY --from=fontconfig /fontconfig /
COPY --from=libpng /libpng /
# renovate: datasource=github-tags depName=i3/i3lock extractVersion=^(?<version>\d+(\.\d+)*)$
ARG I3LOCK_VERSION=2.16
RUN mkdir -p /i3lock
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/i3/i3lock/archive/refs/tags/${I3LOCK_VERSION}.tar.gz -o i3lock.tar.gz && \
    tar -xf i3lock.tar.gz && mv i3lock-* src
WORKDIR /build/src
# i3lock hides its unlock indicator until the first key press, so a locked
# screen is a flat colour that looks like a sleeping display, and it sizes the
# ring by DPI, which is 96 on a laptop panel and a 4K monitor alike, so it is
# oversized on one and tiny on the other. The patch draws the ring from the
# start and sizes it with the same whole-number scale the boot splash uses.
# i3lock has no option for either. `patch` fails the build if a release moves
# the code.
COPY patches/i3lock-indicator.patch /build/i3lock-indicator.patch
RUN patch -p1 < /build/i3lock-indicator.patch
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS}
RUN DESTDIR=/i3lock ninja -C buildDir install


# xss-lock — runs i3lock when the X screen saver activates (idle) and when
# logind asks: before suspend (holding a sleep delay lock until the screen is
# locked) and on `loginctl lock-session`. It needs only GLib/GIO and XCB.
FROM toolchain AS xss-lock
COPY --from=xorgproto /xorgproto /
COPY --from=xcbproto /xcbproto /
COPY --from=libxau /libxau /
COPY --from=libxcb /libxcb /
COPY --from=x11-libs /x11-libs /
COPY --from=pcre2 /pcre2 /
COPY --from=glib2 /glib2 /
# renovate: datasource=github-tags depName=xdbob/xss-lock extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG XSS_LOCK_VERSION=0.3.0
RUN mkdir -p /xss-lock
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/xdbob/xss-lock/archive/refs/tags/v${XSS_LOCK_VERSION}.tar.gz -o xss-lock.tar.gz && \
    tar -xf xss-lock.tar.gz && mv xss-lock-* src
WORKDIR /build/src
RUN pip3 install ninja
RUN cmake -G Ninja -B buildDir -DCMAKE_POLICY_VERSION_MINIMUM=3.5 -DCMAKE_BUILD_TYPE=MinSizeRel -DCMAKE_INSTALL_PREFIX=/usr && \
    ninja -C buildDir && DESTDIR=/xss-lock ninja -C buildDir install && \
    rm -rf /xss-lock/usr/share


# i3 screenshots: shotgun captures the screen or an area to PNG, hacksaw lets
# the user drag out that area. They replace grim and slurp. Both are small Rust
# programs: shotgun speaks X11 through pure-Rust x11rb, so it links only musl,
# and hacksaw links libxcb's shape and xkb extensions, already in the image.
# maim and slop do the same job but would add libjpeg, libwebp, glm and C++ libs
# to the image. RUSTFLAGS links musl dynamically, like the COSMIC binaries.
FROM toolchain AS x11-screenshot
COPY --from=xorgproto /xorgproto /
COPY --from=xcbproto /xcbproto /
COPY --from=libxau /libxau /
COPY --from=libxcb /libxcb /
COPY --from=rust-toolchain /usr/local/cargo/ /usr/local/cargo/
COPY --from=rust-toolchain /usr/local/rustup/ /usr/local/rustup/
ENV CARGO_HOME=/usr/local/cargo \
    RUSTUP_HOME=/usr/local/rustup \
    PATH=/usr/local/cargo/bin:${PATH} \
    RUSTFLAGS="-C target-feature=-crt-static" \
    CARGO_INCREMENTAL=0
# renovate: datasource=crate depName=shotgun
ARG SHOTGUN_VERSION=2.5.1
# renovate: datasource=crate depName=hacksaw
ARG HACKSAW_VERSION=1.0.4
RUN mkdir -p /x11-screenshot/usr/bin
RUN cargo install shotgun --version ${SHOTGUN_VERSION} --locked --root /build/cargo && \
    cargo install hacksaw --version ${HACKSAW_VERSION} --locked --root /build/cargo && \
    install -m0755 /build/cargo/bin/shotgun /build/cargo/bin/hacksaw /x11-screenshot/usr/bin/


# ===========================================================================
# Login manager — ly (TUI display manager). Built with the prebuilt musl Zig
# toolchain (Hadron's toolchain has no Zig). ly authenticates a user via PAM,
# creates the logind session, and launches either Wayland directly or XLibre
# plus the selected X session.
# ===========================================================================
FROM toolchain AS ly
COPY --from=libxcb /libxcb /
COPY --from=libxau /libxau /
COPY --from=xorgproto /xorgproto /
ARG ZIG_VERSION=0.16.0
# ly 1.4.x needs Zig 0.16.x.
# renovate: datasource=github-tags depName=fairyglade/ly extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG LY_VERSION=1.4.1
RUN mkdir -p /ly
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://ziglang.org/download/${ZIG_VERSION}/zig-x86_64-linux-${ZIG_VERSION}.tar.xz -o zig.tar.xz && tar -xf zig.tar.xz && rm zig.tar.xz && mv zig-x86_64-linux-* /opt/zig
ENV PATH=/opt/zig:$PATH
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -L https://github.com/fairyglade/ly/archive/refs/tags/v${LY_VERSION}.tar.gz -o ly.tar.gz && tar -xzf ly.tar.gz && rm ly.tar.gz && mv ly-* ly-src
WORKDIR /build/ly-src
# -Dcpu=baseline: Zig builds for the CPU of the build machine unless told
# otherwise. ly built on a runner with AVX-512 died with SIGILL ("trap invalid
# opcode") 8 ms after start on every CPU without it (AMD EPYC 7763 in CI), so
# the desktop never came up there. Build for the generic x86-64 baseline, like
# the C and Rust code in this image.
RUN zig build -Dcpu=baseline
RUN zig build installexe -Dcpu=baseline -Dinit_system=systemd -Ddest_directory=/ly


# ===========================================================================
# COSMIC desktop (DESKTOP=cosmic). See
# docs/superpowers/specs/2026-11-03-cosmic-desktop-design.md.
#
# Every COSMIC component is Rust, built from its epoch tag against Hadron's
# musl and the libraries this Dockerfile builds. Rust itself comes from the
# pinned Alpine image (only the toolchain; nothing it links ships), the same
# way Dockerfile.agent builds cua-driver. libclang for bindgen comes from the
# llvm stage and is used at build time only. BuildKit only evaluates these
# stages for DESKTOP=cosmic. The rust-toolchain stage is declared above the
# Mesa stages, which build NVK with it (GPU=full), and the XLibre section,
# whose i3 screenshot tools use it too.
# ===========================================================================

# duktape: the small JavaScript engine polkit evaluates its rules with (the
# alternative is mozjs, a browser engine's worth of build).
FROM toolchain AS duktape
# renovate: datasource=github-tags depName=svaarala/duktape extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG DUKTAPE_VERSION=2.7.0
RUN mkdir -p /duktape
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://duktape.org/duktape-${DUKTAPE_VERSION}.tar.xz -o d.tar.xz && tar -xf d.tar.xz && mv duktape-* src
WORKDIR /build/src
RUN make -f Makefile.sharedlibrary INSTALL_PREFIX=/usr CC=cc && \
    make -f Makefile.sharedlibrary INSTALL_PREFIX=/duktape/usr install && \
    mkdir -p /duktape/usr/lib/pkgconfig && \
    printf 'prefix=/usr\nlibdir=${prefix}/lib\nincludedir=${prefix}/include\n\nName: duktape\nDescription: Embeddable Javascript engine\nVersion: %s\nLibs: -L${libdir} -lduktape\nLibs.private: -lm\nCflags: -I${includedir}\n' "${DUKTAPE_VERSION}" \
      > /duktape/usr/lib/pkgconfig/duktape.pc

# polkit: lets an unprivileged desktop session ask for privileged actions
# (date and time, language, users) with a password prompt. cosmic-osd is the
# agent that shows the prompt. Without it, COSMIC Settings' system panels
# silently fail. --libexecdir=lib for the same reason as
# flatpak: /usr/libexec is a persistent path on Kairos.
FROM toolchain AS polkit
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=duktape /duktape /
COPY --from=gettext-tools /gettext-tools /
COPY --from=libucontext /libucontext /
# renovate: datasource=github-tags depName=polkit-org/polkit extractVersion=^(?<version>\d+(\.\d+)*)$
ARG POLKIT_VERSION=126
RUN mkdir -p /polkit
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/polkit-org/polkit/archive/refs/tags/${POLKIT_VERSION}.tar.gz -o p.tar.gz && tar -xzf p.tar.gz && mv polkit-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} --libexecdir=lib \
      -Dsession_tracking=logind -Dsystemdsystemunitdir=/usr/lib/systemd/system \
      -Dauthfw=pam -Dpam_include=system-auth -Dgettext=false \
      -Dintrospection=false -Dman=false -Dgtk_doc=false -Dexamples=false -Dtests=false \
      -Dpolkitd_user=polkitd -Dprivileged_group=admin
RUN DESTDIR=/polkit ninja -C buildDir install

# accountsservice: the user-account D-Bus service COSMIC Settings' Users page
# and the greeter's user list talk to.
FROM toolchain AS accountsservice
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=duktape /duktape /
COPY --from=polkit /polkit /
COPY --from=gettext-tools /gettext-tools /
COPY --from=libucontext /libucontext /
# The GitLab release Alpine builds: it carries the musl fallbacks (shadow
# entries without fgetspent_r, the wtmp path) that the last tarball lacks.
# Its version script reads the version from the source directory's name.
# renovate: datasource=gitlab-tags depName=accountsservice/accountsservice registryUrl=https://gitlab.freedesktop.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG ACCOUNTSSERVICE_VERSION=26.27.3
RUN mkdir -p /accountsservice
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://gitlab.freedesktop.org/accountsservice/accountsservice/-/archive/${ACCOUNTSSERVICE_VERSION}/accountsservice-${ACCOUNTSSERVICE_VERSION}.tar.gz -o a.tar.gz && tar -xzf a.tar.gz
WORKDIR /build/accountsservice-${ACCOUNTSSERVICE_VERSION}
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} --libexecdir=lib \
      -Dsystemdsystemunitdir=/usr/lib/systemd/system -Delogind=false -Dlibxcrypt=false \
      -Dintrospection=false -Dvapi=false -Ddocbook=false -Dgtk_doc=false -Dtests=false \
      -Dadmin_group=admin -Dwtmpfile=/var/log/wtmp
RUN DESTDIR=/accountsservice ninja -C buildDir install

# libgudev: GObject bindings for libudev (the toolchain's), which UPower and
# power-profiles-daemon find their devices with. Its tests need umockdev, which
# is not here, so meson leaves them out. libucontext, as in the polkit and bluez
# stages: the toolchain's libsystemd (which libudev pulls in) links it.
FROM toolchain AS libgudev
COPY --from=libucontext /libucontext /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
# renovate: datasource=gitlab-tags depName=GNOME/libgudev registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG LIBGUDEV_VERSION=238
RUN mkdir -p /libgudev
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.gnome.org/sources/libgudev/${LIBGUDEV_VERSION}/libgudev-${LIBGUDEV_VERSION}.tar.xz -o g.tar.xz && tar -xf g.tar.xz && rm g.tar.xz && mv libgudev-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dtests=disabled -Dintrospection=disabled -Dvapi=disabled -Dgtk_doc=false
RUN DESTDIR=/libgudev ninja -C buildDir install

# UPower: the org.freedesktop.UPower system service that reports the battery
# and the AC adapter from /sys/class/power_supply. COSMIC's battery applet and
# Noctalia show the battery only through it (sway's waybar and i3's
# isotope-status read /sys themselves). It installs its D-Bus system policy
# and activation file, upower.service, its udev rules and hwdb, and its polkit
# action. --libexecdir=lib for the same reason as polkit (upowerd lands in
# /usr/lib). The tests are not run: the integration tests need a running
# daemon and python-dbusmock, and introspection is off, so meson skips them.
FROM toolchain AS upower
COPY --from=libucontext /libucontext /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=duktape /duktape /
COPY --from=polkit /polkit /
COPY --from=gettext-tools /gettext-tools /
COPY --from=libgudev /libgudev /
# renovate: datasource=gitlab-tags depName=upower/upower registryUrl=https://gitlab.freedesktop.org extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG UPOWER_VERSION=1.91.5
RUN mkdir -p /upower
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://gitlab.freedesktop.org/upower/upower/-/archive/v${UPOWER_VERSION}/upower-v${UPOWER_VERSION}.tar.gz -o u.tar.gz && tar -xzf u.tar.gz && rm u.tar.gz && mv upower-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} --libexecdir=lib \
      -Dos_backend=linux -Dpolkit=enabled -Didevice=disabled -Dintrospection=disabled \
      -Dman=false -Dgtk-doc=false -Dinstalled_tests=false -Dzshcompletiondir=no \
      -Dsystemdsystemunitdir=/usr/lib/systemd/system
RUN DESTDIR=/upower ninja -C buildDir install && \
    test -x /upower/usr/lib/upowerd && \
    test -f /upower/usr/lib/systemd/system/upower.service && \
    test -f /upower/usr/share/dbus-1/system.d/org.freedesktop.UPower.conf && \
    test -f /upower/usr/share/dbus-1/system-services/org.freedesktop.UPower.service && \
    test -f /upower/usr/share/polkit-1/actions/org.freedesktop.upower.policy

# power-profiles-daemon: the power profile switch (performance, balanced,
# power saver) of COSMIC's battery applet and Noctalia, on the system bus as
# org.freedesktop.UPower.PowerProfiles and net.hadess.PowerProfiles.
# powerprofilesctl is a Python script that needs PyGObject, which the image
# does not have, so it is removed; the desktops use the D-Bus interface. The
# tests (python-dbusmock, umockdev) are off.
FROM toolchain AS power-profiles-daemon
COPY --from=libucontext /libucontext /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=duktape /duktape /
COPY --from=polkit /polkit /
COPY --from=libgudev /libgudev /
COPY --from=upower /upower /
# renovate: datasource=gitlab-tags depName=upower/power-profiles-daemon registryUrl=https://gitlab.freedesktop.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG PPD_VERSION=0.30
RUN mkdir -p /power-profiles-daemon
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://gitlab.freedesktop.org/upower/power-profiles-daemon/-/archive/${PPD_VERSION}/power-profiles-daemon-${PPD_VERSION}.tar.gz -o p.tar.gz && tar -xzf p.tar.gz && rm p.tar.gz && mv power-profiles-daemon-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} --libexecdir=lib \
      -Dtests=false -Dmanpage=disabled -Dbashcomp=disabled -Dpylint=disabled -Dgtk_doc=false \
      -Dsystemdsystemunitdir=/usr/lib/systemd/system
RUN DESTDIR=/power-profiles-daemon ninja -C buildDir install && \
    rm -f /power-profiles-daemon/usr/bin/powerprofilesctl && \
    test -x /power-profiles-daemon/usr/lib/power-profiles-daemon && \
    test -f /power-profiles-daemon/usr/lib/systemd/system/power-profiles-daemon.service && \
    test -f /power-profiles-daemon/usr/share/dbus-1/system.d/org.freedesktop.UPower.PowerProfiles.conf && \
    test -f /power-profiles-daemon/usr/share/polkit-1/actions/power-profiles-daemon.policy

# libsndfile + libpulse: the PulseAudio client library the audio applet, OSD
# and settings link against. Only the client: PipeWire's pipewire-pulse is the
# server.
FROM toolchain AS libsndfile
# renovate: datasource=github-tags depName=libsndfile/libsndfile extractVersion=^(?<version>\d+(\.\d+)*)$
ARG LIBSNDFILE_VERSION=1.2.2
RUN mkdir -p /libsndfile
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/libsndfile/libsndfile/releases/download/${LIBSNDFILE_VERSION}/libsndfile-${LIBSNDFILE_VERSION}.tar.xz -o s.tar.xz && tar -xf s.tar.xz && mv libsndfile-* src
WORKDIR /build/src
RUN pip3 install ninja
RUN cmake -B build -G Ninja -DCMAKE_INSTALL_PREFIX=/usr -DCMAKE_INSTALL_LIBDIR=lib -DCMAKE_BUILD_TYPE=MinSizeRel -DCMAKE_POLICY_VERSION_MINIMUM=3.5 \
      -DBUILD_SHARED_LIBS=ON -DENABLE_EXTERNAL_LIBS=OFF -DENABLE_MPEG=OFF \
      -DBUILD_PROGRAMS=OFF -DBUILD_EXAMPLES=OFF -DBUILD_TESTING=OFF -DENABLE_CPACK=OFF && \
    cmake --build build && DESTDIR=/libsndfile cmake --install build

FROM toolchain AS libpulse
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=libsndfile /libsndfile /
# renovate: datasource=gitlab-tags depName=pulseaudio/pulseaudio registryUrl=https://gitlab.freedesktop.org extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG PULSEAUDIO_VERSION=17.0
RUN mkdir -p /libpulse
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://www.freedesktop.org/software/pulseaudio/releases/pulseaudio-${PULSEAUDIO_VERSION}.tar.xz -o p.tar.xz && tar -xf p.tar.xz && mv pulseaudio-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} \
      -Ddaemon=false -Dclient=true -Ddoxygen=false -Dman=false -Dtests=false -Ddatabase=simple \
      -Dalsa=disabled -Dasyncns=disabled -Davahi=disabled -Dbluez5=disabled -Ddbus=enabled \
      -Dfftw=disabled -Dglib=enabled -Dgsettings=disabled -Dgtk=disabled -Djack=disabled \
      -Dlirc=disabled -Dopenssl=disabled -Dorc=disabled -Dsamplerate=disabled -Dsoxr=disabled \
      -Dspeex=disabled -Dsystemd=disabled -Dudev=disabled -Dx11=disabled -Dgstreamer=disabled \
      -Dwebrtc-aec=disabled -Dtcpwrap=disabled -Dvalgrind=disabled -Delogind=disabled \
      -Doss-output=disabled -Dconsolekit=disabled -Dbashcompletiondir=no -Dzshcompletiondir=no
RUN DESTDIR=/libpulse ninja -C buildDir install

# dav1d: AV1 decoding for the AVIF wallpapers COSMIC ships. No assembly, so no
# nasm in the toolchain; the C path is plenty for a few wallpapers.
FROM toolchain AS dav1d
# renovate: datasource=gitlab-tags depName=videolan/dav1d registryUrl=https://code.videolan.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG DAV1D_VERSION=1.5.1
RUN mkdir -p /dav1d
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://downloads.videolan.org/pub/videolan/dav1d/${DAV1D_VERSION}/dav1d-${DAV1D_VERSION}.tar.xz -o d.tar.xz && tar -xf d.tar.xz && mv dav1d-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Denable_asm=false -Denable_tools=false -Denable_tests=false -Denable_examples=false
RUN DESTDIR=/dav1d ninja -C buildDir install

# iso-codes: only the JSON tables and the .pc file COSMIC Settings' region page
# reads. Its build wants gettext for the translations, which this toolchain does
# not have and nothing here needs.
FROM toolchain AS iso-codes
# renovate: datasource=gitlab-tags depName=iso-codes-team/iso-codes registryUrl=https://salsa.debian.org extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG ISO_CODES_VERSION=4.18.0
RUN mkdir -p /iso-codes/usr/share/iso-codes/json /iso-codes/usr/share/pkgconfig
WORKDIR /build
# salsa makes the archive on request and can answer 200 with a truncated body,
# which curl does not retry, so download until the archive reads to the end.
RUN for i in 1 2 3 4 5; do \
      curl --retry-all-errors -fL --retry 5 https://salsa.debian.org/iso-codes-team/iso-codes/-/archive/v${ISO_CODES_VERSION}/iso-codes-v${ISO_CODES_VERSION}.tar.gz -o i.tar.gz && \
        tar -tzf i.tar.gz > /dev/null && break; \
      [ "$i" = 5 ] && exit 1; sleep 10; \
    done && tar -xzf i.tar.gz && \
    cp iso-codes-*/data/*.json /iso-codes/usr/share/iso-codes/json/ && \
    printf 'prefix=/usr\ndatarootdir=${prefix}/share\ndatadir=${datarootdir}\ndomains=iso_639-2 iso_639-3 iso_639-5 iso_3166-1 iso_3166-2 iso_3166-3 iso_4217 iso_15924\n\nName: iso-codes\nDescription: ISO country, subdivision, language, currency and script codes and their translations\nVersion: %s\n' "${ISO_CODES_VERSION}" \
      > /iso-codes/usr/share/pkgconfig/iso-codes.pc

# GStreamer core + base libraries: xdg-desktop-portal-cosmic records the screen
# with it for screen sharing. Plugins beyond the base libraries are off.
FROM toolchain AS gstreamer
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
# renovate: datasource=gitlab-tags depName=gstreamer/gstreamer registryUrl=https://gitlab.freedesktop.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG GSTREAMER_VERSION=1.26.7
RUN mkdir -p /gstreamer
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://gstreamer.freedesktop.org/src/gstreamer/gstreamer-${GSTREAMER_VERSION}.tar.xz -o g.tar.xz && tar -xf g.tar.xz && \
    curl --retry-all-errors -fL --retry 5 https://gstreamer.freedesktop.org/src/gst-plugins-base/gst-plugins-base-${GSTREAMER_VERSION}.tar.xz -o b.tar.xz && tar -xf b.tar.xz
RUN pip3 install meson ninja
WORKDIR /build/gstreamer-${GSTREAMER_VERSION}
RUN meson setup buildDir ${COMMON_MESON_FLAGS} --libexecdir=lib \
      -Dintrospection=disabled -Dnls=disabled -Dtests=disabled -Dexamples=disabled \
      -Dbenchmarks=disabled -Dtools=disabled -Ddoc=disabled -Dptp-helper=disabled \
      -Dlibunwind=disabled -Dlibdw=disabled -Dbash-completion=disabled -Dcheck=enabled && \
    ninja -C buildDir install && DESTDIR=/gstreamer ninja -C buildDir install
WORKDIR /build/gst-plugins-base-${GSTREAMER_VERSION}
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dauto_features=disabled \
      -Dintrospection=disabled -Dnls=disabled -Dtests=disabled -Dexamples=disabled \
      -Dtools=disabled -Ddoc=disabled -Dapp=enabled -Dvideoconvertscale=enabled \
      -Daudioconvert=enabled -Dvideorate=enabled -Daudioresample=enabled && \
    DESTDIR=/gstreamer ninja -C buildDir install

# xdg-desktop-portal: the broker Flatpak apps open files, take screenshots and
# share the screen through. xdg-desktop-portal-cosmic is its COSMIC backend.
FROM toolchain AS xdg-desktop-portal
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=json-glib /json-glib /
COPY --from=libfuse3 /libfuse3 /
COPY --from=pipewire /pipewire /
COPY --from=gdk-pixbuf /gdk-pixbuf /
COPY --from=libpng /libpng /
COPY --from=flatpak /flatpak /
COPY --from=libarchive /libarchive /
COPY --from=libxmlb /libxmlb /
COPY --from=appstream /appstream /
COPY --from=libyaml /libyaml /
COPY --from=ostree /ostree /
COPY --from=gpgme /gpgme /
COPY --from=libassuan /libassuan /
COPY --from=libgpg-error /libgpg-error /
COPY --from=bubblewrap /bubblewrap /
COPY --from=gstreamer /gstreamer /
# Hadron's libsystemd links libucontext; the linker needs it to resolve it.
COPY --from=libucontext /libucontext /
# renovate: datasource=github-tags depName=flatpak/xdg-desktop-portal extractVersion=^(?<version>\d+(\.\d+)*)$
ARG XDG_DESKTOP_PORTAL_VERSION=1.20.3
RUN mkdir -p /xdg-desktop-portal
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/flatpak/xdg-desktop-portal/releases/download/${XDG_DESKTOP_PORTAL_VERSION}/xdg-desktop-portal-${XDG_DESKTOP_PORTAL_VERSION}.tar.xz -o x.tar.xz && tar -xf x.tar.xz && mv xdg-desktop-portal-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} --libexecdir=lib \
      -Dsystemd=enabled -Ddocumentation=disabled -Dman-pages=disabled -Dtests=disabled \
      -Dgeoclue=disabled -Dsandboxed-image-validation=disabled -Dsandboxed-sound-validation=disabled \
      -Dflatpak-interfaces=enabled
RUN DESTDIR=/xdg-desktop-portal ninja -C buildDir install

# Xwayland: X11 apps (several popular Flatpaks still are) on the COSMIC
# compositor. XLibre does not ship it, so this is the upstream X.org release,
# reusing the X11 libraries built for the i3 variant.
FROM toolchain AS xwayland
COPY --from=wayland /wayland /
COPY --from=libdrm /libdrm /
COPY --from=mesa /mesa /
COPY --from=libepoxy /libepoxy /
COPY --from=pixman /pixman /
COPY --from=xorgproto /xorgproto /
COPY --from=libxau /libxau /
COPY --from=xcbproto /xcbproto /
COPY --from=libxcb /libxcb /
COPY --from=x11-libs /x11-libs /
COPY --from=freetype /freetype /
COPY --from=libpng /libpng /
COPY --from=libucontext /libucontext /
# renovate: datasource=gitlab-tags depName=xorg/xserver registryUrl=https://gitlab.freedesktop.org extractVersion=^xwayland-(?<version>\d+(\.\d+)*)$
ARG XWAYLAND_VERSION=24.1.8
RUN mkdir -p /xwayland
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://xorg.freedesktop.org/archive/individual/xserver/xwayland-${XWAYLAND_VERSION}.tar.xz -o x.tar.xz && tar -xf x.tar.xz && mv xwayland-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} \
      -Dxvfb=false -Dxdmcp=false -Dxdm-auth-1=false -Dxcsecurity=true -Ddri3=true -Dglamor=true -Dglx=false \
      -Dsha1=libcrypto -Dsecure-rpc=false -Dlibdecor=false \
      -Dxwayland_ei=false -Ddocs=false -Ddevel-docs=false -Dlisten_tcp=false \
      -Dxkb_dir=/usr/share/X11/xkb -Dxkb_output_dir=/var/lib/xkb -Dxkb_bin_dir=/usr/bin
RUN DESTDIR=/xwayland ninja -C buildDir install

# The shared COSMIC build environment: every library a component links
# against, Rust, just, and libclang.
FROM toolchain AS cosmic-build
RUN rmdir /usr/lib/firmware 2>/dev/null || true
COPY --from=wayland /wayland /
COPY --from=libdrm /libdrm /
COPY --from=libucontext /libucontext /
COPY --from=mesa /mesa /
COPY --from=xkeyboard-config /xkeyboard-config /
COPY --from=libxkb /libxkb /
COPY --from=pixman /pixman /
COPY --from=libevdev /libevdev /
COPY --from=libinput /libinput /
COPY --from=hwdata /hwdata /
COPY --from=libdisplay-info /libdisplay-info /
COPY --from=libseat /libseat /
COPY --from=pcre2 /pcre2 /
COPY --from=glib2 /glib2 /
COPY --from=freetype /freetype /
COPY --from=harfbuzz /harfbuzz /
COPY --from=fribidi /fribidi /
COPY --from=fontconfig /fontconfig /
COPY --from=libpng /libpng /
COPY --from=xorgproto /xorgproto /
COPY --from=libxau /libxau /
COPY --from=xcbproto /xcbproto /
COPY --from=libxcb /libxcb /
COPY --from=alsa-lib /alsa-lib /
COPY --from=pipewire /pipewire /
COPY --from=libsndfile /libsndfile /
COPY --from=libpulse /libpulse /
COPY --from=dav1d /dav1d /
COPY --from=iso-codes /iso-codes /
COPY --from=gstreamer /gstreamer /
COPY --from=xdg-desktop-portal /xdg-desktop-portal /
COPY --from=json-glib /json-glib /
COPY --from=libfuse3 /libfuse3 /
COPY --from=libgpg-error /libgpg-error /
COPY --from=libassuan /libassuan /
COPY --from=gpgme /gpgme /
COPY --from=libyaml /libyaml /
COPY --from=libxmlb /libxmlb /
COPY --from=appstream /appstream /
COPY --from=libarchive /libarchive /
COPY --from=ostree /ostree /
COPY --from=flatpak /flatpak /
COPY --from=llvm /llvm /
COPY --from=rust-toolchain /usr/local/cargo/ /usr/local/cargo/
COPY --from=rust-toolchain /usr/local/rustup/ /usr/local/rustup/
COPY --from=rust-toolchain /sbin/ldconfig /sbin/ldconfig
RUN sed -i '$a exit 0' /sbin/ldconfig && ldconfig
ENV CARGO_HOME=/usr/local/cargo \
    RUSTUP_HOME=/usr/local/rustup \
    PATH=/usr/local/cargo/bin:${PATH} \
    RUSTFLAGS="-C target-feature=-crt-static" \
    PKG_CONFIG_PATH=/usr/lib/pkgconfig:/usr/share/pkgconfig \
    LIBCLANG_PATH=/usr/lib \
    CFLAGS="-O2 -pipe" CXXFLAGS="-O2 -pipe" LDFLAGS="-Wl,--as-needed" \
    CARGO_PROFILE_RELEASE_LTO=thin \
    CARGO_INCREMENTAL=0
# renovate: datasource=crate depName=just
ARG JUST_VERSION=1.43.0
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked \
    cargo install just --version ${JUST_VERSION} --locked --root /usr/local
COPY tools/cosmic-build.sh /usr/local/bin/cosmic-build
COPY patches/cosmic /usr/share/isotope-patches
WORKDIR /build

# greetd, the login daemon cosmic-greeter runs under, and pop-launcher, the
# search backend behind the COSMIC launcher.
FROM cosmic-build AS greetd
# renovate: datasource=github-tags depName=kennylevinsen/greetd
ARG GREETD_VERSION=0.10.3
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked \
    curl --retry-all-errors -fsSL --retry 5 https://github.com/kennylevinsen/greetd/archive/refs/tags/${GREETD_VERSION}.tar.gz | tar -xz && \
    cd greetd-${GREETD_VERSION} && cargo build --release --locked && \
    install -Dm755 target/release/greetd /greetd/usr/bin/greetd && \
    install -Dm755 target/release/agreety /greetd/usr/bin/agreety && \
    ! ldd /greetd/usr/bin/greetd | grep 'not found'

FROM cosmic-build AS pop-launcher
# renovate: datasource=github-tags depName=pop-os/cosmic-session extractVersion=^epoch-(?<version>\d+(\.\d+)*)$
ARG COSMIC_VERSION=1.11.0
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked \
    curl --retry-all-errors -fsSL --retry 5 https://github.com/pop-os/launcher/archive/refs/tags/epoch-${COSMIC_VERSION}.tar.gz | tar -xz && \
    cd launcher-epoch-${COSMIC_VERSION} && just build-release --locked && \
    just rootdir=/pop-launcher install && \
    ! ldd /pop-launcher/usr/bin/pop-launcher | grep 'not found'

# The COSMIC components, one RUN each so a failure re-runs only that one. They
# build in a cargo target dir on a cache mount, so nothing but the installed
# files reaches a layer, and each step empties it when done: the components'
# release profiles differ (panic strategy, LTO), so little compiled code is
# shared between them, and keeping it would grow by gigabytes per component.
#
# They are split in two stages, the shell (cosmic-shell) and the applications
# (cosmic-apps), of about two and a half hours each on a CI runner. One job
# building both ran past GitHub's six-hour limit. release.yml builds each half
# in its own job and pushes it as the cosmic-shell-dist or cosmic-apps-dist
# image, tagged by `make print-stage-key`; the cosmic build then swaps them in
# (`make COSMIC_SHELL_IMAGE=... COSMIC_APPS_IMAGE=...`).
FROM cosmic-build AS cosmic-shell
# renovate: datasource=github-tags depName=pop-os/cosmic-session extractVersion=^epoch-(?<version>\d+(\.\d+)*)$
ARG COSMIC_VERSION=1.11.0
ENV COSMIC_VERSION=${COSMIC_VERSION} COSMIC_OUT=/cosmic
ENV CARGO_TARGET_DIR=/cargo-target
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked --mount=type=cache,id=cosmic-target,target=/cargo-target,sharing=locked cosmic-build cosmic-comp
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked --mount=type=cache,id=cosmic-target,target=/cargo-target,sharing=locked cosmic-build cosmic-session
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked --mount=type=cache,id=cosmic-target,target=/cargo-target,sharing=locked cosmic-build cosmic-settings-daemon
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked --mount=type=cache,id=cosmic-target,target=/cargo-target,sharing=locked cosmic-build cosmic-panel && rm -rf /cargo-target/*
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked --mount=type=cache,id=cosmic-target,target=/cargo-target,sharing=locked cosmic-build cosmic-applets && rm -rf /cargo-target/*
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked --mount=type=cache,id=cosmic-target,target=/cargo-target,sharing=locked cosmic-build cosmic-bg && rm -rf /cargo-target/*
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked --mount=type=cache,id=cosmic-target,target=/cargo-target,sharing=locked cosmic-build cosmic-osd && rm -rf /cargo-target/*
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked --mount=type=cache,id=cosmic-target,target=/cargo-target,sharing=locked cosmic-build cosmic-notifications && rm -rf /cargo-target/*
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked --mount=type=cache,id=cosmic-target,target=/cargo-target,sharing=locked cosmic-build cosmic-launcher && rm -rf /cargo-target/*
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked --mount=type=cache,id=cosmic-target,target=/cargo-target,sharing=locked cosmic-build cosmic-app-library && rm -rf /cargo-target/*
# cosmic-workspaces installs with make, not just, and builds only its binary.
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked --mount=type=cache,id=cosmic-target,target=/cargo-target,sharing=locked \
    COSMIC_CARGO_ARGS="--bin cosmic-workspaces" \
    COSMIC_INSTALL_CMD='make install DESTDIR="$COSMIC_OUT" CARGO_TARGET_DIR="$CARGO_TARGET_DIR"' \
    cosmic-build cosmic-workspaces-epoch && rm -rf /cargo-target/*
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked --mount=type=cache,id=cosmic-target,target=/cargo-target,sharing=locked cosmic-build cosmic-idle && rm -rf /cargo-target/*
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked --mount=type=cache,id=cosmic-target,target=/cargo-target,sharing=locked cosmic-build cosmic-randr && rm -rf /cargo-target/*
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked --mount=type=cache,id=cosmic-target,target=/cargo-target,sharing=locked cosmic-build cosmic-screenshot && rm -rf /cargo-target/*
FROM cosmic-build AS cosmic-apps
# renovate: datasource=github-tags depName=pop-os/cosmic-session extractVersion=^epoch-(?<version>\d+(\.\d+)*)$
ARG COSMIC_VERSION=1.11.0
ENV COSMIC_VERSION=${COSMIC_VERSION} COSMIC_OUT=/cosmic
ENV CARGO_TARGET_DIR=/cargo-target
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked --mount=type=cache,id=cosmic-target,target=/cargo-target,sharing=locked cosmic-build xdg-desktop-portal-cosmic && rm -rf /cargo-target/*
# COSMIC Settings without its Users page: accountsservice writes only /etc,
# which Kairos rebuilds at every boot, so users added there are lost (booted
# test, docs/superpowers/specs/2026-11-21-isotope-system-app-design.md,
# "Users (phase 2)"). Isotope System's Users page is the one place users
# change. The list is the epoch's default features without page-users;
# check it again against cosmic-settings/Cargo.toml at every new epoch.
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked --mount=type=cache,id=cosmic-target,target=/cargo-target,sharing=locked \
    COSMIC_CARGO_ARGS="-p cosmic-settings --no-default-features --features a11y,avif,single-instance,wgpu,systemd,cosmic-comp-config,page-accessibility,page-about,page-bluetooth,page-date,page-default-apps,page-display,page-input,page-legacy-applications,page-networking,page-power,page-region,page-sound,page-window-management,page-workspaces,xdg-portal,wayland" \
    cosmic-build cosmic-settings && \
    rm -f "$COSMIC_OUT/usr/share/applications/com.system76.CosmicSettings.Users.desktop" \
          "$COSMIC_OUT/usr/share/polkit-1/actions/com.system76.CosmicSettings.Users.policy" && \
    ! test -e "$COSMIC_OUT/usr/share/applications/com.system76.CosmicSettings.Users.desktop" && \
    ! test -e "$COSMIC_OUT/usr/share/polkit-1/actions/com.system76.CosmicSettings.Users.policy" && \
    rm -rf /cargo-target/*
# cosmic-files is built with upstream's build-release recipe, which runs one
# cargo invocation per package. One invocation for all three packages unifies
# the applet's desktop-applet feature into the cosmic-files app, which then
# opens a Desktop tab and a full-screen layer surface on every output. The
# log string checked here is compiled only with desktop-applet: the app must
# not have it, and the applet must, so the check cannot pass on a binary
# without the string.
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked --mount=type=cache,id=cosmic-target,target=/cargo-target,sharing=locked COSMIC_BUILD_RECIPE="build-release --locked" cosmic-build cosmic-files && \
    ! grep -qa 'already had surface ID' /cosmic/usr/bin/cosmic-files && \
    grep -qa 'already had surface ID' /cosmic/usr/bin/cosmic-files-applet && \
    rm -rf /cargo-target/*
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked --mount=type=cache,id=cosmic-target,target=/cargo-target,sharing=locked cosmic-build cosmic-term && rm -rf /cargo-target/*
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked --mount=type=cache,id=cosmic-target,target=/cargo-target,sharing=locked cosmic-build cosmic-edit && rm -rf /cargo-target/*
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked --mount=type=cache,id=cosmic-target,target=/cargo-target,sharing=locked cosmic-build cosmic-store && rm -rf /cargo-target/*
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked --mount=type=cache,id=cosmic-target,target=/cargo-target,sharing=locked COSMIC_CARGO_ARGS="--all" cosmic-build cosmic-greeter && rm -rf /cargo-target/*
# Data-only components: the icon theme (just), the wallpapers (make), and the
# panel layouts of cosmic-initial-setup (/usr/share/cosmic-layouts), which
# Isotope Welcome's Layout page reads. cosmic-initial-setup itself (its binary,
# its autostart file) is not installed: Isotope Welcome is COSMIC's first-login
# flow, as on the other variants.
RUN mkdir -p /build/icons /build/wallpapers /build/is && \
    curl --retry-all-errors -fsSL --retry 5 https://github.com/pop-os/cosmic-icons/archive/refs/tags/epoch-${COSMIC_VERSION}.tar.gz | tar -xz --strip-components=1 -C /build/icons && \
    curl --retry-all-errors -fsSL --retry 5 https://github.com/pop-os/cosmic-wallpapers/archive/refs/tags/epoch-${COSMIC_VERSION}.tar.gz | tar -xz --strip-components=1 -C /build/wallpapers && \
    curl --retry-all-errors -fsSL --retry 5 https://github.com/pop-os/cosmic-initial-setup/archive/refs/tags/epoch-${COSMIC_VERSION}.tar.gz | tar -xz --strip-components=1 -C /build/is && \
    (cd /build/icons && just rootdir=/cosmic install) && \
    (cd /build/wallpapers && make install DESTDIR=/cosmic prefix=/usr) && \
    (cd /build/is/res/layouts && find . -type f -exec install -Dm0644 '{}' '/cosmic/usr/share/cosmic-layouts/{}' \;) && \
    test -d /cosmic/usr/share/cosmic-layouts/bottom-panel && \
    rm -rf /build/icons /build/wallpapers /build/is

# Only the installed trees, for CI to push (see cosmic-shell above).
FROM scratch AS cosmic-shell-dist
COPY --from=cosmic-shell /cosmic /cosmic

FROM scratch AS cosmic-apps-dist
COPY --from=cosmic-apps /cosmic /cosmic

# Both halves in one tree. The applications are copied last, as they were
# installed last when one stage built everything.
FROM scratch AS cosmic
COPY --from=cosmic-shell /cosmic /cosmic
COPY --from=cosmic-apps /cosmic /cosmic

# musl-locales: the `locale` command musl lacks. COSMIC Settings builds its
# language list from `locale -a`; without it the list is empty. Alpine's
# patch makes `locale -a` list every language, since musl has no compiled
# locales to enumerate (its C library messages stay English; app UIs are
# translated).
# Each name gets the .UTF-8 suffix glibc systems report, which is what
# COSMIC's language lists filter on; musl accepts any locale name.
FROM toolchain AS musl-locales
COPY --from=gettext-tools /gettext-tools /
# renovate: datasource=gitlab-tags depName=adelie/musl-locales registryUrl=https://git.adelielinux.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG MUSL_LOCALES_VERSION=0.1.0
ARG APORTS_COMMIT=65780305dd2517157357121ccddbe95c84e6d5a4
WORKDIR /build
# git.adelielinux.org refuses connections from GitHub's runners (a release
# build waited 14 minutes for it). Alpine mirrors the same tarball (identical
# sha512, see aports main/musl-locales/APKBUILD) in its distfiles.
RUN { curl --connect-timeout 20 --retry 2 --retry-all-errors -fsSL -o musl-locales.tar.bz2 \
        https://git.adelielinux.org/adelie/musl-locales/-/archive/${MUSL_LOCALES_VERSION}/musl-locales-${MUSL_LOCALES_VERSION}.tar.bz2 || \
      curl --retry-all-errors -fsSL --retry 5 -o musl-locales.tar.bz2 \
        https://distfiles.alpinelinux.org/distfiles/v3.22/musl-locales-${MUSL_LOCALES_VERSION}.tar.bz2; } && \
    tar -xjf musl-locales.tar.bz2 && rm musl-locales.tar.bz2 && \
    cd musl-locales-${MUSL_LOCALES_VERSION} && \
    curl --retry-all-errors -fsSL --retry 5 https://raw.githubusercontent.com/alpinelinux/aports/${APORTS_COMMIT}/main/musl-locales/add-all-available-locales.patch | patch -p1 && \
    sed -i '/printf("a_DJ\\n/s/\\n/.UTF-8\\n/g' locale.c && grep -q 'de_DE.UTF-8' locale.c && \
    pip3 install ninja && \
    cmake -B build -G Ninja -DCMAKE_BUILD_TYPE=MinSizeRel -DCMAKE_INSTALL_PREFIX=/usr -DCMAKE_POLICY_VERSION_MINIMUM=3.5 && \
    cmake --build build && DESTDIR=/musl-locales cmake --install build && \
    /musl-locales/usr/bin/locale -a | grep -qx de_DE.UTF-8

# hicolor: the fallback icon theme every other theme inherits from, and where
# applications install their own icons. Only its index.theme (and the empty
# size directories it declares); without it no app icon resolves.
FROM toolchain AS hicolor-icon-theme
# renovate: datasource=gitlab-tags depName=xdg/default-icon-theme registryUrl=https://gitlab.freedesktop.org extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG HICOLOR_VERSION=0.18
WORKDIR /build
RUN curl --retry-all-errors -fsSL --retry 5 https://icon-theme.freedesktop.org/releases/hicolor-icon-theme-${HICOLOR_VERSION}.tar.xz | tar -xJ && \
    cd hicolor-icon-theme-${HICOLOR_VERSION} && pip3 install meson ninja && \
    meson setup build --prefix=/usr && DESTDIR=/hicolor-icon-theme ninja -C build install && \
    test -f /hicolor-icon-theme/usr/share/icons/hicolor/index.theme

# Conflux, the Isotope icon theme (GPL-3.0 icons, data only), on every variant.
# Only what its index.theme lists is installed (index.theme and the directories
# in Directories= that exist, with the @2x links of ScaledDirectories=), not the whole
# repo: no Workspace/, images/, preview.png, update.sh or .github/. Its LICENSE
# and CREDITS.md go to /usr/share/licenses/conflux with a SOURCE file (upstream
# URL and pinned commit). The icons are unmodified; Isotope adds alias symlinks
# (theme/conflux-links.txt, installed to /usr/share/isotope), the apps/48 link,
# and rewrites the Inherits line of index.theme.
# That Inherits line names Adwaita and breeze, which the image does not have, so
# it inherits Cosmic (pop-os/cosmic-icons, installed here the way its justfile
# does, as the toolchain has no `just`) and hicolor. Conflux names few Flathub
# and COSMIC app IDs: theme/conflux-links.txt links those IDs to the Conflux
# icon that fits. Apps with none are in theme/conflux-missing.txt.
# Conflux ships a few dangling links (ProtonVPN status icons); they are removed.
# mako globs <icon-path>/*/<number>/<name>.*, so apps/48 points at apps/scalable.
# `find -L -type l` lists only broken links, and BusyBox find lacks -xtype.
FROM toolchain AS icon-themes
# Conflux has no release tags.
ARG CONFLUX_COMMIT=85b0ef6dc59e4d459364337a92a14f604a2baa82
# renovate: datasource=github-tags depName=pop-os/cosmic-session extractVersion=^epoch-(?<version>\d+(\.\d+)*)$
ARG COSMIC_VERSION=1.11.0
COPY theme/conflux-links.txt theme/conflux-missing.txt /build/
RUN i=/icon-themes/usr/share/icons && c=$i/Conflux && s=/build/conflux && l=/icon-themes/usr/share/licenses/conflux && \
    mkdir -p $c $s $l $i/Cosmic /build/cosmic-icons /icon-themes/usr/share/isotope && \
    curl --retry-all-errors -fsSL --retry 5 https://github.com/MoshiurRahmanAdib/Conflux-Icon-Theme/archive/${CONFLUX_COMMIT}.tar.gz | tar -xz --strip-components=1 -C $s && \
    install -m0644 $s/index.theme $c/index.theme && \
    for d in $(sed -n 's/^Directories=//p' $s/index.theme | tr ',' ' '); do \
      [ -d $s/$d ] || continue; mkdir -p $c/$(dirname $d) && cp -a $s/$d $c/$d || exit 1; \
    done && \
    for x in $(sed -n 's/^ScaledDirectories=//p' $s/index.theme | tr ',' ' ' | tr ' ' '\n' | cut -d/ -f1 | sort -u); do \
      test -d $c/${x%@2x} && ln -s ${x%@2x} $c/$x || exit 1; \
    done && \
    rm -f $c/apps/scalable/optimize.sh && \
    install -m0644 $s/LICENSE $s/CREDITS.md $l/ && \
    printf '%s\n' \
      'Conflux icon theme, by MoshiurRahmanAdib, licensed GPL-3.0 (see LICENSE; credits in CREDITS.md).' \
      'Upstream: https://github.com/MoshiurRahmanAdib/Conflux-Icon-Theme' \
      "Commit:   ${CONFLUX_COMMIT}" \
      'The icons are installed unmodified. Isotope adds alias symlinks only (listed in' \
      '/usr/share/isotope/conflux-links.txt), the apps/48 link, and sets the Inherits line' \
      'of index.theme to Cosmic,hicolor. Dangling upstream links and the helper script' \
      'apps/scalable/optimize.sh are not installed.' > $l/SOURCE && \
    rm -rf $s && \
    sed -i 's/^Inherits=.*/Inherits=Cosmic,hicolor/' $c/index.theme && \
    curl --retry-all-errors -fsSL --retry 5 https://github.com/pop-os/cosmic-icons/archive/refs/tags/epoch-${COSMIC_VERSION}.tar.gz | tar -xz --strip-components=1 -C /build/cosmic-icons && \
    (cd /build/cosmic-icons && \
     for d in freedesktop extra; do (cd $d && find ./scalable -type f -exec install -Dm0644 '{}' "$i/Cosmic/{}" \;); done; \
     install -Dm0644 index.theme $i/Cosmic/index.theme) && \
    grep -v '^#' /build/conflux-links.txt | while read -r id name; do \
      [ -n "$id" ] || continue; n=0; \
      for d in apps/scalable apps@2x/scalable; do \
        if [ -e "$c/$d/$name.svg" ]; then ln -sf "$name.svg" "$c/$d/$id.svg"; n=$((n+1)); fi; \
      done; \
      [ $n -gt 0 ] || { echo "conflux-links.txt: no $name.svg in Conflux for $id" >&2; exit 1; }; \
    done && \
    ln -s scalable $c/apps/48 && \
    install -Dm0644 /build/conflux-links.txt /build/conflux-missing.txt /icon-themes/usr/share/isotope/ && \
    rm -rf /build/cosmic-icons && \
    find -L $c -type l -exec rm -f {} + && \
    grep -qx 'Inherits=Cosmic,hicolor' $c/index.theme && \
    test -f $i/Cosmic/index.theme && \
    grep -q 'GNU GENERAL PUBLIC LICENSE' $l/LICENSE && grep -q "$CONFLUX_COMMIT" $l/SOURCE && \
    for n in Workspace images preview.png update.sh .github README.md CONTRIBUTING.md .gitignore; do test ! -e $c/$n || exit 1; done && \
    test -e $c/apps/scalable/org.videolan.VLC.svg && \
    test -e $c/apps/48/org.videolan.VLC.svg && \
    test -z "$(find -L $c -type l)"

# The UI fonts libcosmic defaults to (Open Sans, Noto Sans Mono), installed
# system-wide so Flatpak and Xwayland apps match the shell. Pinned commits.
FROM toolchain AS cosmic-fonts
ARG GOOGLE_FONTS_COMMIT=8b0a1d0f5983c89bc2b93f1b5fb55f9e252744b5
ARG NOTO_FONTS_COMMIT=8b36ddb234b06c091320e28985a3c96b48ce2596
RUN d=/cosmic-fonts/usr/share/fonts/cosmic && mkdir -p $d && \
    for f in "OpenSans%5Bwdth,wght%5D.ttf" "OpenSans-Italic%5Bwdth,wght%5D.ttf"; do \
      curl --retry-all-errors -fsSL --retry 5 "https://raw.githubusercontent.com/google/fonts/${GOOGLE_FONTS_COMMIT}/ofl/opensans/$f" -o "$d/$(printf %s "$f" | sed 's/%5B/[/;s/%5D/]/')" || exit 1; \
    done && \
    for f in NotoSansMono-Regular.ttf NotoSansMono-Bold.ttf; do \
      curl --retry-all-errors -fsSL --retry 5 "https://raw.githubusercontent.com/notofonts/notofonts.github.io/${NOTO_FONTS_COMMIT}/fonts/NotoSansMono/hinted/ttf/$f" -o "$d/$f" || exit 1; \
    done

# ===========================================================================
# niri + Noctalia desktop (DESKTOP=niri). See
# docs/superpowers/specs/2026-11-04-niri-noctalia-design.md.
#
# niri is a scrollable-tiling Wayland compositor (Rust); Noctalia is the shell
# around it: bar, dock, launcher, control center, notifications, lock screen,
# wallpaper and the polkit agent (C++23, no Qt or GTK). Both build from tagged
# releases against Hadron's musl. niri reuses COSMIC's Rust build environment;
# Noctalia's libraries below are plain C/C++ and build on the toolchain.
# BuildKit only evaluates these stages for DESKTOP=niri.
# ===========================================================================

# Header-only: stb's image resize and write headers, from a pinned commit.
# Upstream has since moved the resize header under deprecated/.
FROM toolchain AS stb
ARG STB_COMMIT=d14d46f6b44456eb88beac58cba833a4135d4c89
RUN mkdir -p /stb/usr/include/stb && \
    for h in deprecated/stb_image_resize2.h stb_image_write.h; do \
      curl --retry-all-errors -fsSL --retry 5 "https://raw.githubusercontent.com/nothings/stb/${STB_COMMIT}/$h" -o "/stb/usr/include/stb/${h##*/}" || exit 1; \
    done

FROM toolchain AS nlohmann-json
# renovate: datasource=github-tags depName=nlohmann/json extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG NLOHMANN_JSON_VERSION=3.12.0
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/nlohmann/json/releases/download/v${NLOHMANN_JSON_VERSION}/json.tar.xz -o j.tar.xz && tar -xf j.tar.xz && mv json src
WORKDIR /build/src
RUN cmake -B build -DCMAKE_INSTALL_PREFIX=/usr -DJSON_BuildTests=OFF -DJSON_Install=ON && \
    DESTDIR=/nlohmann-json cmake --install build

FROM toolchain AS tomlplusplus
# renovate: datasource=github-tags depName=marzer/tomlplusplus extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG TOMLPLUSPLUS_VERSION=3.4.0
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/marzer/tomlplusplus/archive/refs/tags/v${TOMLPLUSPLUS_VERSION}.tar.gz -o t.tar.gz && tar -xzf t.tar.gz && mv tomlplusplus-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dbuild_tests=false -Dbuild_examples=false && \
    DESTDIR=/tomlplusplus ninja -C buildDir install

FROM toolchain AS md4c
# renovate: datasource=github-tags depName=mity/md4c extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG MD4C_VERSION=0.6.0
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/mity/md4c/archive/refs/tags/v${MD4C_VERSION}.tar.gz -o m.tar.gz && tar -xzf m.tar.gz && mv md4c-* src
WORKDIR /build/src
RUN cmake -B build -DCMAKE_INSTALL_PREFIX=/usr -DCMAKE_INSTALL_LIBDIR=lib -DCMAKE_BUILD_TYPE=MinSizeRel -DBUILD_SHARED_LIBS=ON && \
    cmake --build build && DESTDIR=/md4c cmake --install build

FROM toolchain AS libsodium
# renovate: datasource=github-tags depName=jedisct1/libsodium extractVersion=^(?<version>\d+(\.\d+)*)-RELEASE$
ARG LIBSODIUM_VERSION=1.0.22
RUN mkdir -p /libsodium
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/jedisct1/libsodium/releases/download/${LIBSODIUM_VERSION}-RELEASE/libsodium-${LIBSODIUM_VERSION}.tar.gz -o s.tar.gz && tar -xzf s.tar.gz && mv libsodium-* src
WORKDIR /build/src
RUN ./configure ${COMMON_CONFIGURE_ARGS} && make -j"$(nproc)" && make DESTDIR=/libsodium install

# sdbus-c++: the C++ D-Bus binding Noctalia talks to system services with.
# Hadron's libsystemd links libucontext, so the linker needs it too.
FROM toolchain AS sdbus-cpp
COPY --from=libucontext /libucontext /
# renovate: datasource=github-tags depName=Kistler-Group/sdbus-cpp extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG SDBUS_CPP_VERSION=2.3.1
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/Kistler-Group/sdbus-cpp/archive/refs/tags/v${SDBUS_CPP_VERSION}.tar.gz -o s.tar.gz && tar -xzf s.tar.gz && mv sdbus-cpp-* src
WORKDIR /build/src
RUN cmake -B build -DCMAKE_INSTALL_PREFIX=/usr -DCMAKE_INSTALL_LIBDIR=lib -DCMAKE_BUILD_TYPE=MinSizeRel \
      -DSDBUSCPP_BUILD_DOCS=OFF -DSDBUSCPP_BUILD_TESTS=OFF -DSDBUSCPP_BUILD_CODEGEN=OFF -DSDBUSCPP_BUILD_EXAMPLES=OFF && \
    cmake --build build && DESTDIR=/sdbus-cpp cmake --install build

# libical: calendar events (the clock panel's agenda, CalDAV). System time
# zone data, no GLib binding.
FROM toolchain AS libical
# renovate: datasource=github-tags depName=libical/libical extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG LIBICAL_VERSION=4.0.7
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/libical/libical/archive/refs/tags/v${LIBICAL_VERSION}.tar.gz -o i.tar.gz && tar -xzf i.tar.gz && mv libical-* src
WORKDIR /build/src
RUN pip3 install ninja
RUN cmake -B build -G Ninja -DCMAKE_INSTALL_PREFIX=/usr -DCMAKE_INSTALL_LIBDIR=lib -DCMAKE_BUILD_TYPE=MinSizeRel \
      -DLIBICAL_GLIB=OFF -DLIBICAL_GOBJECT_INTROSPECTION=OFF -DLIBICAL_JAVA_BINDINGS=OFF -DLIBICAL_CXX_BINDINGS=OFF \
      -DLIBICAL_BUILD_DOCS=OFF -DLIBICAL_BUILD_TESTING=OFF -DLIBICAL_BUILD_EXAMPLES=OFF -DLIBICAL_STATIC=OFF \
      -DCMAKE_DISABLE_FIND_PACKAGE_ICU=ON -DCMAKE_DISABLE_FIND_PACKAGE_BerkeleyDB=ON && \
    cmake --build build && DESTDIR=/libical cmake --install build

# libical 3.0.x for obexd. BlueZ's dummy phonebook plugin links libicalvcal
# (vCard parsing), which libical 4 removed, so obexd cannot build against the
# libical stage above. The renovate pin stays on the 3 series.
FROM toolchain AS libical3
# renovate: datasource=github-tags depName=libical/libical extractVersion=^v(?<version>\d+(\.\d+)*)$ versioning=regex:^(?<major>3)\.(?<minor>\d+)\.(?<patch>\d+)$
ARG LIBICAL3_VERSION=3.0.20
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/libical/libical/archive/refs/tags/v${LIBICAL3_VERSION}.tar.gz -o i.tar.gz && tar -xzf i.tar.gz && mv libical-* src
WORKDIR /build/src
RUN pip3 install ninja
RUN cmake -B build -G Ninja -DCMAKE_INSTALL_PREFIX=/usr -DCMAKE_INSTALL_LIBDIR=lib -DCMAKE_BUILD_TYPE=MinSizeRel \
      -DICAL_GLIB=OFF -DGOBJECT_INTROSPECTION=OFF -DWITH_CXX_BINDINGS=OFF -DICAL_BUILD_DOCS=OFF \
      -DLIBICAL_BUILD_TESTING=OFF -DSHARED_ONLY=ON \
      -DCMAKE_DISABLE_FIND_PACKAGE_ICU=ON -DCMAKE_DISABLE_FIND_PACKAGE_BerkeleyDB=ON && \
    cmake --build build && DESTDIR=/libical3 cmake --install build && ls /libical3/usr/lib/libicalvcal.so

# libical 3, runtime files only, for obexd (BlueZ's file transfer service, which
# reads calendar and contact objects). The libical3 stage keeps its headers,
# pkg-config and CMake files because other stages build against them; the
# images get this stage instead.
FROM libical3 AS libical-runtime
RUN mkdir -p /libical-runtime/usr/lib /libical-runtime/usr/share/licenses/libical && \
    cp -a /libical3/usr/lib/libical*.so.* /libical-runtime/usr/lib/ && \
    cp -a /build/src/LICENSE* /libical-runtime/usr/share/licenses/libical/ && \
    ls /libical-runtime/usr/lib/libical.so.* && ls /libical-runtime/usr/share/licenses/libical/* && \
    ! find /libical-runtime \( -name '*.h' -o -name '*.pc' -o -name '*.cmake' -o -name '*.a' \) | grep -q .

# BlueZ with OBEX (obexd and its obex.service user unit) and the LE Audio
# profiles. The bluez stage stays unchanged because cosmic-build reaches it and
# a change there would rekey the COSMIC prebuilt halves. This stage reconfigures
# the same source with obex on and installs the result; the images copy it over
# /bluez. LE Audio (bap, bass, vcp, mcp, csip, micp, ccp) is on by default in
# 5.79: configure only has --disable-* options for them.
FROM toolchain AS bluez-full
COPY --from=libucontext /libucontext /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=ncurses /ncurses /
COPY --from=libical3 /libical3 /
COPY --from=bluez /build/bluez-src /build/bluez-src
RUN mkdir -p /bluez-full
WORKDIR /build/bluez-src
RUN make distclean >/dev/null 2>&1 || true
RUN ./configure ${COMMON_CONFIGURE_ARGS} --disable-dependency-tracking \
    --enable-client --enable-tools --enable-deprecated --enable-library \
    --enable-systemd --enable-udev --enable-obex --disable-mesh --disable-cups \
    --disable-manpages --enable-testing --disable-nfc --disable-sap --disable-midi \
    --with-dbusconfdir=/usr/share --with-systemdsystemunitdir=/usr/lib/systemd/system \
    --with-systemduserunitdir=/usr/lib/systemd/user \
    CFLAGS="-O2 -g -DMAX_INPUT=255" LIBS="-lncurses"
RUN make -j$(nproc) && make install DESTDIR=/bluez-full
RUN cp -f tools/btvirt /bluez-full/usr/bin/ 2>/dev/null || cp -f emulator/btvirt /bluez-full/usr/bin/ 2>/dev/null || true
# The D-Bus activation file asks for SystemdService=dbus-org.bluez.obex.service;
# BlueZ installs that link in the unit directory, so no per-user enable is needed.
# Fail the stage when OBEX, that link or an LE Audio plugin is missing.
RUN test -x /bluez-full/usr/libexec/bluetooth/obexd && \
    test -f /bluez-full/usr/lib/systemd/user/obex.service && \
    test -f /bluez-full/usr/share/dbus-1/services/org.bluez.obex.service && \
    [ "$(readlink /bluez-full/usr/lib/systemd/user/dbus-org.bluez.obex.service)" = obex.service ] && \
    for p in bap bass vcp mcp csip micp ccp; do \
      grep -aq "__bluetooth_builtin_$p" /bluez-full/usr/libexec/bluetooth/bluetoothd || { echo "LE Audio profile $p not built"; exit 1; }; \
    done


FROM toolchain AS libwebp
# renovate: datasource=github-tags depName=webmproject/libwebp extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG LIBWEBP_VERSION=1.6.0
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://storage.googleapis.com/downloads.webmproject.org/releases/webp/libwebp-${LIBWEBP_VERSION}.tar.gz -o w.tar.gz && tar -xzf w.tar.gz && mv libwebp-* src
WORKDIR /build/src
RUN cmake -B build -DCMAKE_INSTALL_PREFIX=/usr -DCMAKE_INSTALL_LIBDIR=lib -DCMAKE_BUILD_TYPE=MinSizeRel -DBUILD_SHARED_LIBS=ON \
      -DWEBP_BUILD_ANIM_UTILS=OFF -DWEBP_BUILD_CWEBP=OFF -DWEBP_BUILD_DWEBP=OFF -DWEBP_BUILD_GIF2WEBP=OFF \
      -DWEBP_BUILD_IMG2WEBP=OFF -DWEBP_BUILD_VWEBP=OFF -DWEBP_BUILD_WEBPINFO=OFF -DWEBP_BUILD_WEBPMUX=OFF \
      -DWEBP_BUILD_EXTRAS=OFF && \
    cmake --build build && DESTDIR=/libwebp cmake --install build

# libjxl (JPEG XL) and the three libraries it needs, all from the system rather
# than its git submodules, which a release tarball does not carry.
FROM toolchain AS brotli
# renovate: datasource=github-tags depName=google/brotli extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG BROTLI_VERSION=1.2.1
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/google/brotli/archive/refs/tags/v${BROTLI_VERSION}.tar.gz -o b.tar.gz && tar -xzf b.tar.gz && mv brotli-* src
WORKDIR /build/src
RUN cmake -B build -DCMAKE_INSTALL_PREFIX=/usr -DCMAKE_INSTALL_LIBDIR=lib -DCMAKE_BUILD_TYPE=MinSizeRel -DBUILD_SHARED_LIBS=ON -DBROTLI_DISABLE_TESTS=ON && \
    cmake --build build && DESTDIR=/brotli cmake --install build

FROM toolchain AS highway
# renovate: datasource=github-tags depName=google/highway extractVersion=^(?<version>\d+(\.\d+)*)$
ARG HIGHWAY_VERSION=1.4.0
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/google/highway/archive/refs/tags/${HIGHWAY_VERSION}.tar.gz -o h.tar.gz && tar -xzf h.tar.gz && mv highway-* src
WORKDIR /build/src
RUN cmake -B build -DCMAKE_INSTALL_PREFIX=/usr -DCMAKE_INSTALL_LIBDIR=lib -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=ON \
      -DBUILD_TESTING=OFF -DHWY_ENABLE_TESTS=OFF -DHWY_ENABLE_EXAMPLES=OFF -DHWY_ENABLE_CONTRIB=OFF -DHWY_ENABLE_INSTALL=ON && \
    cmake --build build && DESTDIR=/highway cmake --install build

FROM toolchain AS lcms2
# renovate: datasource=github-tags depName=mm2/Little-CMS extractVersion=^lcms(?<version>\d+(\.\d+)*)$
ARG LCMS2_VERSION=2.20
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/mm2/Little-CMS/releases/download/lcms${LCMS2_VERSION}/lcms2-${LCMS2_VERSION}.tar.gz -o l.tar.gz && tar -xzf l.tar.gz && mv lcms2-* src
WORKDIR /build/src
RUN ./configure ${COMMON_CONFIGURE_ARGS} && make -j"$(nproc)" && make DESTDIR=/lcms2 install

# ---------------------------------------------------------------------------
# Printing, part two: the filters that turn a document into what a driverless
# printer takes (PDF, PWG raster, Apple raster), cups-browsed, ipp-usb, and
# the `printing` stage that desktop-default copies (part one, Avahi and CUPS,
# is before gtk3). Each stage puts its licence in
# /usr/share/licenses/<name>. See
# docs/superpowers/specs/2026-11-24-printing-design.md.
# ---------------------------------------------------------------------------

# libjpeg-turbo: QPDF needs it. No SIMD: the toolchain has no NASM, and a
# printer's JPEG decode is not where printing spends its time.
FROM toolchain AS libjpeg-turbo
# renovate: datasource=github-releases depName=libjpeg-turbo/libjpeg-turbo extractVersion=^(?<version>\d+\.\d+\.\d+)$
ARG LIBJPEG_TURBO_VERSION=3.2.1
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/libjpeg-turbo/libjpeg-turbo/releases/download/${LIBJPEG_TURBO_VERSION}/libjpeg-turbo-${LIBJPEG_TURBO_VERSION}.tar.gz -o j.tar.gz && \
    tar -xzf j.tar.gz && mv libjpeg-turbo-${LIBJPEG_TURBO_VERSION} src
WORKDIR /build/src
RUN cmake -B build -DCMAKE_INSTALL_PREFIX=/usr -DCMAKE_INSTALL_LIBDIR=lib -DCMAKE_BUILD_TYPE=Release \
      -DENABLE_STATIC=OFF -DWITH_SIMD=OFF -DWITH_TURBOJPEG=OFF && \
    cmake --build build -j"$(nproc)" && DESTDIR=/libjpeg-turbo cmake --install build && \
    L=/libjpeg-turbo/usr/share/licenses/libjpeg-turbo && install -d "$L" && cp -p $(ls -d LICENSE* COPYING* NOTICE* 2>/dev/null) "$L"/ && ls "$L"/* >/dev/null && \
    test -e /libjpeg-turbo/usr/lib/libjpeg.so.62

# QPDF: libcupsfilters' PDF library (pdftopdf, bannertopdf). C++; OpenSSL for
# its crypto. Only libqpdf ships.
FROM toolchain AS qpdf
COPY --from=libjpeg-turbo /libjpeg-turbo /
# renovate: datasource=github-releases depName=qpdf/qpdf extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG QPDF_VERSION=12.4.2
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/qpdf/qpdf/releases/download/v${QPDF_VERSION}/qpdf-${QPDF_VERSION}.tar.gz -o q.tar.gz && \
    tar -xzf q.tar.gz && mv qpdf-${QPDF_VERSION} src
WORKDIR /build/src
RUN cmake -B build -DCMAKE_INSTALL_PREFIX=/usr -DCMAKE_INSTALL_LIBDIR=lib -DCMAKE_BUILD_TYPE=MinSizeRel \
      -DBUILD_STATIC_LIBS=OFF -DBUILD_DOC=OFF -DINSTALL_MANUAL=OFF -DINSTALL_EXAMPLES=OFF \
      -DREQUIRE_CRYPTO_OPENSSL=ON -DUSE_IMPLICIT_CRYPTO=OFF && \
    cmake --build build -j"$(nproc)" && DESTDIR=/qpdf cmake --install build && \
    L=/qpdf/usr/share/licenses/qpdf && install -d "$L" && cp -p $(ls -d LICENSE* COPYING* NOTICE* 2>/dev/null) "$L"/ && ls "$L"/* >/dev/null && \
    ls /qpdf/usr/lib/libqpdf.so.* >/dev/null

# Ghostscript: renders a PDF to PWG raster or Apple raster for the printers
# that take no PDF (most cheap inkjets), through its cups, pwgraster and
# appleraster devices (gdevcups, linked to libcups). The one renderer: no
# Poppler, no MuPDF. Its own copies of its third-party libraries, linked in,
# and its init files compiled into the binary. AGPL-3.0. The release tags
# spell the version without dots (gs10080), which Renovate cannot map, so the
# version and the tarball's checksum are updated by hand. C17: GCC 15 defaults
# to C23, which some of the bundled sources do not build with.
FROM toolchain AS ghostscript
COPY --from=libucontext /libucontext /
COPY --from=avahi /avahi /
COPY --from=cups /cups /
ARG GHOSTSCRIPT_VERSION=10.08.0
ARG GHOSTSCRIPT_SHA256=c20492bc8ebb96c87fa2e52a0926e1cda8cde95d66145e018ac713fed5da38cf
WORKDIR /build
RUN tag="gs$(echo "${GHOSTSCRIPT_VERSION}" | tr -d .)" && \
    curl --retry-all-errors -fL --retry 5 https://github.com/ArtifexSoftware/ghostpdl-downloads/releases/download/${tag}/ghostscript-${GHOSTSCRIPT_VERSION}.tar.xz -o gs.tar.xz && \
    echo "${GHOSTSCRIPT_SHA256}  gs.tar.xz" | sha256sum -c - && \
    tar -xJf gs.tar.xz && mv ghostscript-${GHOSTSCRIPT_VERSION} src
WORKDIR /build/src
RUN CFLAGS="${CFLAGS} -std=gnu17" ./configure --prefix=/usr --sysconfdir=/etc \
      --without-x --disable-gtk --disable-dbus --disable-fontconfig --without-libpaper \
      --without-tesseract --without-ijs --without-versioned-path --enable-cups && \
    make -j"$(nproc)" && make DESTDIR=/ghostscript install && \
    rm -rf /ghostscript/usr/share/doc /ghostscript/usr/share/man /ghostscript/usr/share/ghostscript/doc && \
    L=/ghostscript/usr/share/licenses/ghostscript && install -d "$L" && cp -p $(ls -d LICENSE* COPYING* NOTICE* 2>/dev/null) "$L"/ && ls "$L"/* >/dev/null && \
    devs="$(/ghostscript/usr/bin/gs -h)" && \
    for d in cups pwgraster appleraster pdfwrite ps2write; do \
      echo "$devs" | grep -qw "$d" || { echo "ghostscript: no $d device" >&2; exit 1; }; \
    done

# libcupsfilters 2.1: the filter functions. Not 2.2.x: its release notes say
# 2.2.0 does not build and 2.2.1 has regressions from the move to PDFio, and
# send users to 2.1.2 (renovate.json holds it below 2.2). Ghostscript renders;
# no Poppler, no MuPDF, no TIFF, JPEG XL or EXIF input.
FROM toolchain AS libcupsfilters
COPY --from=libucontext /libucontext /
COPY --from=avahi /avahi /
COPY --from=cups /cups /
COPY --from=libjpeg-turbo /libjpeg-turbo /
COPY --from=qpdf /qpdf /
COPY --from=libpng /libpng /
COPY --from=freetype /freetype /
COPY --from=fontconfig /fontconfig /
COPY --from=lcms2 /lcms2 /
# renovate: datasource=github-releases depName=OpenPrinting/libcupsfilters extractVersion=^(?<version>\d+(\.\d+)*)$
ARG LIBCUPSFILTERS_VERSION=2.1.2
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/OpenPrinting/libcupsfilters/releases/download/${LIBCUPSFILTERS_VERSION}/libcupsfilters-${LIBCUPSFILTERS_VERSION}.tar.xz -o l.tar.xz && \
    tar -xJf l.tar.xz && mv libcupsfilters-${LIBCUPSFILTERS_VERSION} src
WORKDIR /build/src
RUN cp /usr/share/automake-*/config.sub /usr/share/automake-*/config.guess . && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-poppler --disable-mutool \
      --with-gs-path=/usr/bin/gs --with-ippfind-path=/usr/bin/ippfind \
      --without-tiff --without-jpegxl --disable-exif --disable-werror && \
    make -j"$(nproc)" && make DESTDIR=/libcupsfilters install && \
    L=/libcupsfilters/usr/share/licenses/libcupsfilters && install -d "$L" && cp -p $(ls -d LICENSE* COPYING* NOTICE* 2>/dev/null) "$L"/ && ls "$L"/* >/dev/null && \
    ls /libcupsfilters/usr/lib/libcupsfilters.so.* >/dev/null

# libppd: the PPD library cups-filters and cups-browsed link. Ghostscript is
# here so configure finds its ps2write device.
FROM toolchain AS libppd
COPY --from=libucontext /libucontext /
COPY --from=avahi /avahi /
COPY --from=cups /cups /
COPY --from=ghostscript /ghostscript /
COPY --from=libjpeg-turbo /libjpeg-turbo /
COPY --from=qpdf /qpdf /
COPY --from=libpng /libpng /
COPY --from=freetype /freetype /
COPY --from=fontconfig /fontconfig /
COPY --from=lcms2 /lcms2 /
COPY --from=libcupsfilters /libcupsfilters /
# renovate: datasource=github-releases depName=OpenPrinting/libppd extractVersion=^(?<version>\d+(\.\d+)*)$
ARG LIBPPD_VERSION=2.1.1
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/OpenPrinting/libppd/releases/download/${LIBPPD_VERSION}/libppd-${LIBPPD_VERSION}.tar.xz -o l.tar.xz && \
    tar -xJf l.tar.xz && mv libppd-${LIBPPD_VERSION} src
WORKDIR /build/src
RUN cp /usr/share/automake-*/config.sub /usr/share/automake-*/config.guess . && \
    ./configure ${COMMON_CONFIGURE_ARGS} --with-pdftops=gs --disable-pdftops --disable-mutool \
      --disable-pdftocairo --disable-acroread --with-ippfind-path=/usr/bin/ippfind --disable-werror && \
    make -j"$(nproc)" && make DESTDIR=/libppd install && \
    L=/libppd/usr/share/licenses/libppd && install -d "$L" && cp -p $(ls -d LICENSE* COPYING* NOTICE* 2>/dev/null) "$L"/ && ls "$L"/* >/dev/null && \
    ls /libppd/usr/lib/libppd.so.* >/dev/null

# cups-filters 2: the CUPS filters (the universal filter that runs
# libcupsfilters' functions) and their MIME conversion rules, and the
# driverless PPD generator. No Foomatic (it is for drivers).
FROM toolchain AS cups-filters
COPY --from=libucontext /libucontext /
COPY --from=avahi /avahi /
COPY --from=cups /cups /
COPY --from=ghostscript /ghostscript /
COPY --from=libjpeg-turbo /libjpeg-turbo /
COPY --from=qpdf /qpdf /
COPY --from=libpng /libpng /
COPY --from=freetype /freetype /
COPY --from=fontconfig /fontconfig /
COPY --from=lcms2 /lcms2 /
COPY --from=libcupsfilters /libcupsfilters /
COPY --from=libppd /libppd /
# renovate: datasource=github-releases depName=OpenPrinting/cups-filters extractVersion=^(?<version>\d+(\.\d+)*)$
ARG CUPS_FILTERS_VERSION=2.0.1
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/OpenPrinting/cups-filters/releases/download/${CUPS_FILTERS_VERSION}/cups-filters-${CUPS_FILTERS_VERSION}.tar.xz -o f.tar.xz && \
    tar -xJf f.tar.xz && mv cups-filters-${CUPS_FILTERS_VERSION} src
WORKDIR /build/src
RUN cp /usr/share/automake-*/config.sub /usr/share/automake-*/config.guess . && \
    ./configure ${COMMON_CONFIGURE_ARGS} --disable-poppler --disable-mutool --disable-foomatic \
      --with-ippfind-path=/usr/bin/ippfind --with-shell=/bin/sh --disable-werror && \
    make -j"$(nproc)" && make DESTDIR=/cups-filters install && \
    L=/cups-filters/usr/share/licenses/cups-filters && install -d "$L" && cp -p $(ls -d LICENSE* COPYING* NOTICE* 2>/dev/null) "$L"/ && ls "$L"/* >/dev/null && \
    test -x /cups-filters/usr/lib/cups/filter/universal && \
    ls /cups-filters/usr/share/cups/mime/*.convs >/dev/null

# cups-browsed: makes a queue for each driverless printer Avahi finds
# (rootfs/common/etc/cups/cups-browsed.conf). Its systemd unit is not
# installed by make; it is copied from the source.
FROM toolchain AS cups-browsed
COPY --from=libucontext /libucontext /
COPY --from=pcre2 /pcre2 /
COPY --from=glib2 /glib2 /
COPY --from=avahi /avahi /
COPY --from=cups /cups /
COPY --from=libjpeg-turbo /libjpeg-turbo /
COPY --from=qpdf /qpdf /
COPY --from=libpng /libpng /
COPY --from=freetype /freetype /
COPY --from=fontconfig /fontconfig /
COPY --from=lcms2 /lcms2 /
COPY --from=libcupsfilters /libcupsfilters /
COPY --from=libppd /libppd /
# renovate: datasource=github-releases depName=OpenPrinting/cups-browsed extractVersion=^(?<version>\d+(\.\d+)*)$
ARG CUPS_BROWSED_VERSION=2.1.1
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/OpenPrinting/cups-browsed/releases/download/${CUPS_BROWSED_VERSION}/cups-browsed-${CUPS_BROWSED_VERSION}.tar.xz -o b.tar.xz && \
    tar -xJf b.tar.xz && mv cups-browsed-${CUPS_BROWSED_VERSION} src
WORKDIR /build/src
RUN cp /usr/share/automake-*/config.sub /usr/share/automake-*/config.guess . && \
    ./configure ${COMMON_CONFIGURE_ARGS} --with-browseremoteprotocols=dnssd --without-rcdir --disable-werror && \
    make -j"$(nproc)" && make DESTDIR=/cups-browsed install && \
    install -Dm0644 daemon/cups-browsed.service /cups-browsed/usr/lib/systemd/system/cups-browsed.service && \
    grep -qx 'ExecStart=/usr/sbin/cups-browsed' /cups-browsed/usr/lib/systemd/system/cups-browsed.service && \
    L=/cups-browsed/usr/share/licenses/cups-browsed && install -d "$L" && cp -p $(ls -d LICENSE* COPYING* NOTICE* 2>/dev/null) "$L"/ && ls "$L"/* >/dev/null && \
    test -x /cups-browsed/usr/sbin/cups-browsed

# ipp-usb: an IPP-over-USB printer (most USB printers since about 2015)
# becomes an IPP printer on localhost, published through Avahi on loopback,
# so it prints driverless too. Go with cgo (libusb, libavahi-client); its
# dependencies are vendored. Its udev rule starts ipp-usb.service when such a
# printer is plugged in, which is upstream's design: `ipp-usb udev` exits
# when the last one is gone, so the unit is not linked into a target.
FROM toolchain AS ipp-usb
COPY --from=libucontext /libucontext /
COPY --from=libusb /libusb /
COPY --from=avahi /avahi /
COPY --from=golang:1.25.0-alpine3.22 /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:$PATH GOTOOLCHAIN=local
# renovate: datasource=github-tags depName=OpenPrinting/ipp-usb extractVersion=^(?<version>\d+(\.\d+)*)$
ARG IPP_USB_VERSION=0.9.34
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/OpenPrinting/ipp-usb/archive/refs/tags/${IPP_USB_VERSION}.tar.gz -o i.tar.gz && \
    tar -xzf i.tar.gz && mv ipp-usb-${IPP_USB_VERSION} src
WORKDIR /build/src
RUN CGO_ENABLED=1 go build -mod=vendor -trimpath -ldflags="-s -w" -tags nethttpomithttp2 -o ipp-usb . && \
    install -Dm0755 ipp-usb /ipp-usb/usr/sbin/ipp-usb && \
    install -Dm0644 systemd-udev/71-ipp-usb.rules /ipp-usb/usr/lib/udev/rules.d/71-ipp-usb.rules && \
    install -Dm0644 systemd-udev/ipp-usb.service /ipp-usb/usr/lib/systemd/system/ipp-usb.service && \
    sed -i 's#^ExecStart=/sbin/ipp-usb#ExecStart=/usr/sbin/ipp-usb#' /ipp-usb/usr/lib/systemd/system/ipp-usb.service && \
    grep -qx 'ExecStart=/usr/sbin/ipp-usb udev' /ipp-usb/usr/lib/systemd/system/ipp-usb.service && \
    install -Dm0644 ipp-usb.conf /ipp-usb/etc/ipp-usb/ipp-usb.conf && \
    install -d /ipp-usb/usr/share/ipp-usb/quirks && cp -p ipp-usb-quirks/* /ipp-usb/usr/share/ipp-usb/quirks/ && \
    L=/ipp-usb/usr/share/licenses/ipp-usb && install -d "$L" && cp -p $(ls -d LICENSE* COPYING* NOTICE* 2>/dev/null) "$L"/ && ls "$L"/* >/dev/null

# The printing payload: the runtime files of the stages above and of part one
# (Avahi, CUPS, libusb, libdaemon), with no headers, pkg-config, static
# libraries, man pages or docs, stripped. desktop-default copies it; the
# server does not. /usr/share/isotope/printing.files lists every file, so
# Dockerfile.agent can remove exactly these.
FROM toolchain AS printing-files
COPY --from=libusb /libusb /p/
COPY --from=libdaemon /libdaemon /p/
COPY --from=avahi /avahi /p/
COPY --from=cups /cups /p/
COPY --from=ghostscript /ghostscript /p/
COPY --from=libjpeg-turbo /libjpeg-turbo /p/
COPY --from=qpdf /qpdf /p/
COPY --from=lcms2 /lcms2 /p/
COPY --from=libcupsfilters /libcupsfilters /p/
COPY --from=libppd /libppd /p/
COPY --from=cups-filters /cups-filters /p/
COPY --from=cups-browsed /cups-browsed /p/
COPY --from=ipp-usb /ipp-usb /p/
WORKDIR /p
RUN rm -rf usr/include usr/lib/pkgconfig usr/share/pkgconfig usr/lib/cmake usr/share/man usr/share/doc usr/share/info \
      usr/share/locale usr/share/aclocal usr/share/gtk-doc usr/bin/cups-config \
      usr/bin/cjpeg usr/bin/djpeg usr/bin/jpegtran usr/bin/rdjpgcom usr/bin/wrjpgcom usr/bin/tjbench \
      usr/bin/qpdf usr/bin/fix-qdf usr/bin/zlib-flate \
      usr/bin/jpgicc usr/bin/linkicc usr/bin/psicc usr/bin/tificc usr/bin/transicc \
      run var/run && \
    # Avahi's example services announce SSH and SFTP on the network.
    rm -f etc/avahi/services/ssh.service etc/avahi/services/sftp-ssh.service && \
    find . \( -name '*.a' -o -name '*.la' \) -delete && \
    for f in $(find usr -type f \( -name '*.so*' -o -perm -u+x \)); do \
      case "$(head -c 4 "$f" | od -An -c | tr -d ' ')" in *ELF*) strip --strip-unneeded "$f" ;; esac; \
    done && \
    mkdir -p var/lib/cups usr/share/isotope && \
    # The list leaves out lcms2 and libjpeg: general libraries another
    # component of an image may link (niri's has lcms2), which the agent's
    # removal must not take away.
    { find . ! -type d | sed 's#^\.##' | grep -v -e '^/usr/lib/liblcms2\.so' -e '^/usr/lib/libjpeg\.so'; \
      echo /usr/share/isotope/printing.files; } | sort -u > usr/share/isotope/printing.files && \
    for f in usr/sbin/cupsd usr/sbin/lpadmin usr/sbin/cups-browsed usr/sbin/avahi-daemon usr/sbin/ipp-usb usr/bin/gs \
             usr/lib/cups/filter/universal usr/lib/cups/backend/dnssd usr/lib/cups/backend/ipp \
             usr/share/cups/data/testprint; do \
      test -e "$f" || { echo "printing: $f is missing" >&2; exit 1; }; \
    done && \
    # No headers or pkg-config files are left. The .h files under ppdc/ are
    # not C headers: they are the PPD compiler's includes (hp.h, epson.h ...)
    # that CUPS's .drv driver files read at run time.
    left=$(find . \( -name '*.h' -o -name '*.pc' \) ! -path '*/ppdc/*') && \
    { [ -z "$left" ] || { echo "printing: development files left: $left" >&2; exit 1; }; }

FROM scratch AS printing
COPY --from=printing-files /p/ /

FROM toolchain AS libjxl
COPY --from=brotli /brotli /
COPY --from=highway /highway /
COPY --from=lcms2 /lcms2 /
# renovate: datasource=github-tags depName=libjxl/libjxl extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG LIBJXL_VERSION=0.12.0
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/libjxl/libjxl/archive/refs/tags/v${LIBJXL_VERSION}.tar.gz -o j.tar.gz && tar -xzf j.tar.gz && mv libjxl-* src
WORKDIR /build/src
RUN pip3 install ninja
RUN cmake -B build -G Ninja -DCMAKE_INSTALL_PREFIX=/usr -DCMAKE_INSTALL_LIBDIR=lib -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=ON \
      -DBUILD_TESTING=OFF -DJPEGXL_ENABLE_TOOLS=OFF -DJPEGXL_ENABLE_DOXYGEN=OFF -DJPEGXL_ENABLE_MANPAGES=OFF \
      -DJPEGXL_ENABLE_BENCHMARK=OFF -DJPEGXL_ENABLE_EXAMPLES=OFF -DJPEGXL_ENABLE_JNI=OFF -DJPEGXL_ENABLE_SJPEG=OFF \
      -DJPEGXL_ENABLE_OPENEXR=OFF -DJPEGXL_ENABLE_SKCMS=OFF -DJPEGXL_ENABLE_JPEGLI=OFF -DJPEGXL_ENABLE_FUZZERS=OFF \
      -DJPEGXL_FORCE_SYSTEM_BROTLI=ON -DJPEGXL_FORCE_SYSTEM_HWY=ON -DJPEGXL_FORCE_SYSTEM_LCMS2=ON && \
    cmake --build build && DESTDIR=/libjxl cmake --install build

# libqalculate: the launcher's calculator (arithmetic, units, currencies). Its
# number types sit on GMP and MPFR. 5.13.1 is the newest release with a
# generated source tarball.
FROM toolchain AS gmp
ARG GMP_VERSION=6.3.0
WORKDIR /build
RUN for m in https://mirrors.kernel.org/gnu https://ftpmirror.gnu.org https://ftp.gnu.org/gnu; do curl -fL --connect-timeout 30 --retry 3 --retry-delay 3 --retry-all-errors "$m/gmp/gmp-${GMP_VERSION}.tar.xz" -o g.tar.xz && break; rm -f g.tar.xz; done && test -s g.tar.xz && tar -xf g.tar.xz && mv gmp-* src
WORKDIR /build/src
# GMP's configure probes predate C23, where `void g()` means no arguments.
RUN CFLAGS="-O2 -std=gnu17" ./configure --prefix=/usr --enable-cxx --enable-shared --disable-static && \
    make -j"$(nproc)" && make DESTDIR=/gmp install

FROM toolchain AS mpfr
COPY --from=gmp /gmp /
ARG MPFR_VERSION=4.2.2
WORKDIR /build
RUN for m in https://mirrors.kernel.org/gnu https://ftpmirror.gnu.org https://ftp.gnu.org/gnu; do curl -fL --connect-timeout 30 --retry 3 --retry-delay 3 --retry-all-errors "$m/mpfr/mpfr-${MPFR_VERSION}.tar.xz" -o m.tar.xz && break; rm -f m.tar.xz; done && test -s m.tar.xz && tar -xf m.tar.xz && mv mpfr-* src
WORKDIR /build/src
# Not COMMON_CONFIGURE_ARGS: MPFR rejects --enable-lto for a shared library.
RUN ./configure --prefix=/usr --enable-shared --disable-static && make -j"$(nproc)" && make DESTDIR=/mpfr install

# libbytesize: byte-size arithmetic (GMP, MPFR, PCRE2) that libblockdev's mdraid
# plugin needs. No Python bindings, no docs. msgfmt for its translations comes
# from gettext-tools.
FROM toolchain AS libbytesize
COPY --from=gmp /gmp /
COPY --from=mpfr /mpfr /
COPY --from=pcre2 /pcre2 /
COPY --from=gettext-tools /gettext-tools /
# renovate: datasource=github-releases depName=storaged-project/libbytesize
ARG LIBBYTESIZE_VERSION=2.12
RUN mkdir -p /libbytesize
WORKDIR /build
RUN curl -f --retry 5 --retry-delay 3 --retry-all-errors -L https://github.com/storaged-project/libbytesize/releases/download/${LIBBYTESIZE_VERSION}/libbytesize-${LIBBYTESIZE_VERSION}.tar.gz -o b.tar.gz && tar -xzf b.tar.gz && rm b.tar.gz && mv libbytesize-* src
WORKDIR /build/src
RUN ./configure ${COMMON_CONFIGURE_ARGS} --without-python3 --without-gtk-doc --without-tools
RUN make -j"$(nproc)" && make install DESTDIR=/libbytesize

# keyutils: libblockdev's crypto plugin includes <keyutils.h> (kernel keyring
# calls for LUKS keys). Build-time only: the image uses Hadron's libkeyutils.so.1.
FROM toolchain AS keyutils
# renovate: datasource=git-tags depName=https://git.kernel.org/pub/scm/linux/kernel/git/dhowells/keyutils.git extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG KEYUTILS_VERSION=1.6.3
RUN mkdir -p /keyutils
WORKDIR /build
RUN curl -f --retry 5 --retry-delay 3 --retry-all-errors -L https://git.kernel.org/pub/scm/linux/kernel/git/dhowells/keyutils.git/snapshot/keyutils-${KEYUTILS_VERSION}.tar.gz -o k.tar.gz && tar -xzf k.tar.gz && rm k.tar.gz && mv keyutils-* src
WORKDIR /build/src
RUN make -j"$(nproc)" LIBDIR=/usr/lib USRLIBDIR=/usr/lib NO_ARLIB=1 NO_SOLIB=0 && \
    make install DESTDIR=/keyutils LIBDIR=/usr/lib USRLIBDIR=/usr/lib NO_ARLIB=1 && \
    rm -rf /keyutils/usr/share/man /keyutils/usr/sbin /keyutils/usr/bin /keyutils/bin /keyutils/sbin /keyutils/etc /keyutils/lib /keyutils/usr/share/keyutils && \
    test -f /keyutils/usr/include/keyutils.h

# libcryptsetup (build-time only, for headers: the image uses Hadron's own
# libcryptsetup.so.12, so keep this version in step with Hadron's): libblockdev's
# crypto plugin (LUKS) links it, and udisks2
# requires that plugin. Library only: no cryptsetup binaries, no veritysetup,
# integritysetup or LUKS2 reencrypt, no ssh-token. The toolchain has the
# devmapper, json-c, openssl, uuid and blkid it needs.
FROM toolchain AS cryptsetup
# renovate: datasource=github-tags depName=mbroz/cryptsetup extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG CRYPTSETUP_VERSION=2.8.8
RUN mkdir -p /cryptsetup
WORKDIR /build
RUN curl -f --retry 5 --retry-delay 3 --retry-all-errors -L https://www.kernel.org/pub/linux/utils/cryptsetup/v$(echo ${CRYPTSETUP_VERSION} | cut -d. -f1,2)/cryptsetup-${CRYPTSETUP_VERSION}.tar.xz -o c.tar.xz && tar -xf c.tar.xz && rm c.tar.xz && mv cryptsetup-* src
WORKDIR /build/src
# configure insists on popt, which only the command-line tools use; they are
# off, so the check is answered instead of building popt.
RUN ac_cv_lib_popt_poptConfigFileToString=yes ./configure ${COMMON_CONFIGURE_ARGS} \
      --disable-cryptsetup --disable-veritysetup --disable-integritysetup \
      --disable-ssh-token --disable-asciidoc --disable-nls --disable-selinux \
      --with-crypto_backend=openssl
RUN make -j"$(nproc)" && make install DESTDIR=/cryptsetup

# libnvme: libblockdev's nvme plugin, which udisks2 requires. Library only;
# no Python/swig bindings, no D-Bus support, no tests or docs.
FROM toolchain AS libnvme
# renovate: datasource=github-releases depName=linux-nvme/libnvme extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG LIBNVME_VERSION=1.16.2
RUN mkdir -p /libnvme
WORKDIR /build
RUN curl -f --retry 5 --retry-delay 3 --retry-all-errors -L https://github.com/linux-nvme/libnvme/archive/refs/tags/v${LIBNVME_VERSION}.tar.gz -o n.tar.gz && tar -xzf n.tar.gz && rm n.tar.gz && mv libnvme-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dpython=disabled -Dtests=false -Dexamples=false -Dliburing=disabled \
      -Dlibdbus=disabled -Dkeyutils=disabled -Dopenssl=disabled -Djson-c=enabled
RUN DESTDIR=/libnvme ninja -C buildDir install

# libblockdev: the storage library udisks2 calls for every disk operation. Only
# the plugins udisks2's configure requires: fs, loop, mdraid, part, swap, crypto
# (libcryptsetup, keyutils) and nvme (libnvme). The optional ones are off: lvm,
# lvm_dbus, btrfs, dm, mpath, nvdimm, smart and the tools (which pull in parted),
# plus escrow, the Python bindings, GObject introspection and docs. The plugins
# call the mkfs.* and mdadm binaries at run time; those are not built here.
FROM toolchain AS libblockdev
COPY --from=libucontext /libucontext /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=gmp /gmp /
COPY --from=mpfr /mpfr /
COPY --from=libbytesize /libbytesize /
COPY --from=keyutils /keyutils /
COPY --from=cryptsetup /cryptsetup /
COPY --from=libnvme /libnvme /
COPY --from=e2fsprogs /e2fsprogs /
COPY --from=gettext-tools /gettext-tools /
# renovate: datasource=github-releases depName=storaged-project/libblockdev
ARG LIBBLOCKDEV_VERSION=3.6.0
RUN mkdir -p /libblockdev
WORKDIR /build
RUN curl -f --retry 5 --retry-delay 3 --retry-all-errors -L https://github.com/storaged-project/libblockdev/releases/download/${LIBBLOCKDEV_VERSION}/libblockdev-${LIBBLOCKDEV_VERSION}.tar.gz -o b.tar.gz && tar -xzf b.tar.gz && rm b.tar.gz && mv libblockdev-* src
WORKDIR /build/src
RUN ./configure ${COMMON_CONFIGURE_ARGS} --sysconfdir=/etc \
      --without-btrfs --without-lvm --without-lvm_dbus --without-dm --without-mpath \
      --without-nvdimm --without-smart --without-smartmontools --without-tools \
      --without-escrow --without-python3 --without-gtk-doc --without-s390 --disable-introspection
RUN make -j"$(nproc)" && make install DESTDIR=/libblockdev && \
    test -f /libblockdev/usr/lib/libblockdev.so && \
    test -f /libblockdev/usr/lib/libbd_crypto.so && \
    test -f /libblockdev/usr/lib/libbd_nvme.so && \
    test -f /libblockdev/usr/lib/libbd_fs.so

# exfatprogs: mkfs.exfat, fsck.exfat, tune.exfat, exfatlabel, dump.exfat and
# exfat2img. libblockdev's fs plugin runs mkfs.exfat when udisks2 formats a
# disk as exFAT (the Disks page of Isotope System), and exfatlabel to read or
# set its label. No libraries. desktop-default copies the binaries to /usr/bin
# (the base image's /usr/sbin is a link to it). -V prints the version and
# exits 1, so the check reads what it prints.
FROM toolchain AS exfatprogs
# renovate: datasource=github-releases depName=exfatprogs/exfatprogs
ARG EXFATPROGS_VERSION=1.2.9
RUN mkdir -p /exfatprogs
WORKDIR /build
RUN curl -f --retry 5 --retry-delay 3 --retry-all-errors -L https://github.com/exfatprogs/exfatprogs/releases/download/${EXFATPROGS_VERSION}/exfatprogs-${EXFATPROGS_VERSION}.tar.xz -o x.tar.xz && tar -xf x.tar.xz && rm x.tar.xz && mv exfatprogs-* src
WORKDIR /build/src
RUN ./configure ${COMMON_CONFIGURE_ARGS} --prefix=/usr --sbindir=/usr/sbin
RUN make -j"$(nproc)" && make install DESTDIR=/exfatprogs && rm -rf /exfatprogs/usr/share && \
    for b in mkfs fsck tune dump; do test -x /exfatprogs/usr/sbin/$b.exfat; done && \
    test -x /exfatprogs/usr/sbin/exfatlabel && test -x /exfatprogs/usr/sbin/exfat2img && \
    /exfatprogs/usr/sbin/exfatlabel -V 2>&1 | grep -q "version : ${EXFATPROGS_VERSION}" && \
    /exfatprogs/usr/sbin/mkfs.exfat -V 2>&1 | grep -q "version : ${EXFATPROGS_VERSION}"

# udisks2: the org.freedesktop.UDisks2 system service that mounts and unmounts
# removable disks for an unprivileged session (udisksctl, and the file manager
# and Isotope's Backup page through D-Bus). It installs its D-Bus policy and
# activation file, udisks2.service, its udev rules and its polkit actions.
# --libexecdir=/usr/lib puts udisksd in /usr/lib/udisks2 (not in /usr/libexec,
# a persistent path on Kairos). The daemon needs the libblockdev plugins above;
# LVM2, iSCSI, Btrfs, LibStorageMgmt, zram and SMART modules are off. The unit,
# polkit rule and udev rule that enable it on the image are added elsewhere.
# There is no daemon in the build, so `udisksctl --version` exits with "Error
# connecting to the udisks daemon" after it has loaded; the check wants that
# message, and ldd to find every library.
FROM toolchain AS udisks2
COPY --from=libucontext /libucontext /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=duktape /duktape /
COPY --from=polkit /polkit /
COPY --from=gettext-tools /gettext-tools /
COPY --from=libgudev /libgudev /
COPY --from=gmp /gmp /
COPY --from=mpfr /mpfr /
COPY --from=libbytesize /libbytesize /
COPY --from=keyutils /keyutils /
COPY --from=cryptsetup /cryptsetup /
COPY --from=libnvme /libnvme /
COPY --from=e2fsprogs /e2fsprogs /
COPY --from=libblockdev /libblockdev /
# renovate: datasource=github-releases depName=storaged-project/udisks extractVersion=^udisks-(?<version>\d+(\.\d+)*)$
ARG UDISKS2_VERSION=2.12.0
RUN mkdir -p /udisks2
WORKDIR /build
RUN curl -f --retry 5 --retry-delay 3 --retry-all-errors -L https://github.com/storaged-project/udisks/releases/download/udisks-${UDISKS2_VERSION}/udisks-${UDISKS2_VERSION}.tar.bz2 -o u.tar.bz2 && tar -xjf u.tar.bz2 && rm u.tar.bz2 && mv udisks-* src
WORKDIR /build/src
RUN ./configure ${COMMON_CONFIGURE_ARGS} --sysconfdir=/etc --localstatedir=/var --libexecdir=/usr/lib \
      --with-systemdsystemunitdir=/usr/lib/systemd/system --with-udevdir=/usr/lib/udev \
      --with-tmpfilesdir=/usr/lib/tmpfiles.d --with-modloaddir=/usr/lib/modules-load.d \
      --with-modprobedir=/usr/lib/modprobe.d \
      --disable-lvm2 --disable-btrfs --disable-iscsi --disable-lsm --disable-zram --disable-smart \
      --disable-introspection --disable-gtk-doc --disable-man
RUN make -j"$(nproc)" && make install DESTDIR=/udisks2 && \
    test -x /udisks2/usr/lib/udisks2/udisksd && \
    test -f /udisks2/usr/lib/systemd/system/udisks2.service && \
    test -f /udisks2/usr/share/dbus-1/system.d/org.freedesktop.UDisks2.conf && \
    test -f /udisks2/usr/share/dbus-1/system-services/org.freedesktop.UDisks2.service && \
    test -f /udisks2/usr/share/polkit-1/actions/org.freedesktop.UDisks2.policy && \
    rm -rf /udisks2/usr/share/gtk-doc && \
    ! LD_LIBRARY_PATH=/udisks2/usr/lib ldd /udisks2/usr/lib/udisks2/udisksd /udisks2/usr/bin/udisksctl | grep 'not found' && \
    LD_LIBRARY_PATH=/udisks2/usr/lib /udisks2/usr/bin/udisksctl --version 2>&1 | grep -q 'udisks daemon'

# Firmware updates: fwupd with the LVFS, on the desktops and the server (see
# docs/superpowers/specs/2026-11-24-firmware-updates-design.md). New stages
# only: the ones they copy from (glib2, pcre2, libxmlb, polkit, gettext-tools,
# hwdata, sqlite-gnupg) are reached by cosmic-build and stay unchanged, so the
# COSMIC prebuilt keys do not move. The images take fwupd-runtime only.
#
# libusb: the printing stage `libusb` above (1.0.30, with udev); fwupd needs
# libusb_init_context, 1.0.27 or newer.
#
# gnu-efi: the EFI headers, crt0 and linker script fwupd-efi builds against.
# Build time only. ARCH=x86_64: the toolchain sets ARCH=x86-64, which gnu-efi
# does not know. Its makefiles pass LDFLAGS to ld directly, so the
# toolchain's -Wl, and -flto flags are not given to it.
FROM toolchain AS gnu-efi
# renovate: datasource=github-tags depName=ncroxon/gnu-efi extractVersion=^(?<version>\d+(\.\d+)*)$
ARG GNU_EFI_VERSION=4.0.4
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://github.com/ncroxon/gnu-efi/archive/refs/tags/${GNU_EFI_VERSION}.tar.gz -o g.tar.gz && tar -xzf g.tar.gz && rm g.tar.gz && mv gnu-efi-* src
WORKDIR /build/src
RUN env -u LDFLAGS -u CFLAGS make -j1 ARCH=x86_64 PREFIX=/usr && \
    env -u LDFLAGS -u CFLAGS make ARCH=x86_64 PREFIX=/usr INSTALLROOT=/gnu-efi install && \
    test -f /gnu-efi/usr/lib/pkgconfig/gnu-efi.pc && test -d /gnu-efi/usr/include/efi

# fwupd-efi: fwupdx64.efi, the EFI program that hands a UEFI capsule to the
# firmware at the restart after an update. In /usr/lib/fwupd/efi (libexecdir
# lib: /usr/libexec is a persistent path on Kairos, see flatpak), where the
# fwupd stage looks for it. Unsigned: the standard edition boots with Secure
# Boot off; Trusted Boot images turn the capsule plugin off (kairos stage).
FROM toolchain AS fwupd-efi
COPY --from=gnu-efi /gnu-efi /
# renovate: datasource=github-tags depName=fwupd/fwupd-efi extractVersion=^(?<version>\d+(\.\d+)*)$
ARG FWUPD_EFI_VERSION=1.9
RUN mkdir -p /fwupd-efi
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://github.com/fwupd/fwupd-efi/archive/refs/tags/${FWUPD_EFI_VERSION}.tar.gz -o e.tar.gz && tar -xzf e.tar.gz && rm e.tar.gz && mv fwupd-efi-* src
WORKDIR /build/src
RUN pip3 install meson ninja pefile
RUN env -u LDFLAGS -u CFLAGS meson setup buildDir --prefix=/usr --libdir=lib --libexecdir=lib --buildtype=release \
      -Defi_sbat_distro_id=isotope -Defi_sbat_distro_summary=Isotope \
      -Defi_sbat_distro_pkgname=fwupd-efi -Defi_sbat_distro_version=${FWUPD_EFI_VERSION} \
      -Defi_sbat_distro_url=https://github.com/kairos-community/isotope
RUN DESTDIR=/fwupd-efi ninja -C buildDir install && \
    test -f /fwupd-efi/usr/lib/fwupd/efi/fwupdx64.efi && \
    rm -rf /fwupd-efi/usr/lib/debug && \
    install -Dm644 COPYING /fwupd-efi/usr/share/licenses/fwupd-efi/COPYING

# fwupd: the daemon (D-Bus activated, org.freedesktop.fwupd), fwupdmgr and
# fwupdtool, the LVFS remote, its units (fwupd-refresh.timer refreshes the
# metadata), its D-Bus policy, polkit actions and rule, and its sysusers file
# (the fwupd-refresh user). fwupd 2.1 has libjcat built in and checks the
# LVFS signatures with OpenSSL (Hadron's), and finds the ESP through udisks2.
# libcurl, OpenSSL, liblzma, zlib, libsystemd and libblkid come from the
# toolchain and are in the Hadron base. Off: docs, man pages, introspection,
# tests, the capsule splash (it needs Python's PIL to build), ModemManager,
# passim, libdrm, libmnl, the completions and the firmware packager.
FROM toolchain AS fwupd
COPY --from=libucontext /libucontext /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=duktape /duktape /
COPY --from=polkit /polkit /
COPY --from=gettext-tools /gettext-tools /
COPY --from=hwdata /hwdata /
COPY --from=libxmlb /libxmlb /
COPY --from=sqlite-gnupg /sqlite-gnupg /
COPY --from=libusb /libusb /
# renovate: datasource=github-releases depName=fwupd/fwupd extractVersion=^(?<version>\d+(\.\d+)*)$
ARG FWUPD_VERSION=2.1.10
RUN mkdir -p /fwupd
WORKDIR /build
RUN curl -fL --retry 5 --retry-delay 3 --retry-all-errors https://github.com/fwupd/fwupd/releases/download/${FWUPD_VERSION}/fwupd-${FWUPD_VERSION}.tar.xz -o f.tar.xz && tar -xf f.tar.xz && rm f.tar.xz && mv fwupd-* src
WORKDIR /build/src
RUN pip3 install meson ninja jinja2
RUN meson setup buildDir ${COMMON_MESON_FLAGS} --libexecdir=lib \
      -Dbuild=all -Ddocs=disabled -Dman=false -Dintrospection=disabled -Dtests=false \
      -Dumockdev_tests=disabled -Dvalgrind=disabled -Dmetainfo=false \
      -Dbash_completion=false -Dfish_completion=false -Dfirmware-packager=false \
      -Dgnutls=disabled -Dopenssl=enabled -Dpolkit=enabled -Dsystemd=enabled -Dlogind=enabled \
      -Dlibdrm=disabled -Dlibmnl=disabled -Dpassim=disabled -Dplugin_modem_manager=disabled \
      -Dplugin_uefi_capsule_splash=false -Defi_binary=false -Defi_os_dir=isotope \
      -Dlvfs=true -Dsupported_build=enabled -Dvendor_ids_dir=/usr/share/hwdata \
      -Dpython=/usr/bin/python3
RUN DESTDIR=/fwupd ninja -C buildDir install && \
    test -x /fwupd/usr/lib/fwupd/fwupd && test -x /fwupd/usr/bin/fwupdmgr && \
    test -f /fwupd/usr/lib/systemd/system/fwupd.service && \
    test -f /fwupd/usr/lib/systemd/system/fwupd-refresh.timer && \
    test -f /fwupd/usr/lib/systemd/system/fwupd-refresh.service && \
    test -f /fwupd/usr/lib/sysusers.d/fwupd.conf && \
    test -f /fwupd/usr/share/dbus-1/system.d/org.freedesktop.fwupd.conf && \
    test -f /fwupd/usr/share/dbus-1/system-services/org.freedesktop.fwupd.service && \
    test -f /fwupd/usr/share/polkit-1/actions/org.freedesktop.fwupd.policy && \
    test -f /fwupd/etc/fwupd/remotes.d/lvfs.conf && \
    grep -qx 'Enabled=true' /fwupd/etc/fwupd/remotes.d/lvfs.conf && \
    install -Dm644 COPYING /fwupd/usr/share/licenses/fwupd/COPYING

# fwupd's runtime files (its private libraries are in
# /usr/lib/fwupd-<version>, through RUNPATH), and the libraries it needs that
# the images may not have: libusb, libsqlite3 and libxmlb, as the fwupd stage
# copied them from their stages. libsqlite3 includes the bare libsqlite3.so:
# sqlite-gnupg builds it without a soname, so fwupd's libraries need it by
# that name, and the server has no other copy (the desktops copy the same libxmlb for
# Flatpak; the server has none). fwupdx64.efi from fwupd-efi. No headers,
# pkg-config, static libraries or installed tests; the licences of fwupd,
# fwupd-efi and libusb (LGPL-2.1+) in /usr/share/licenses. Fails when a library the
# daemon or fwupdmgr needs is not in the base, the toolchain's runtime copies
# or here.
FROM fwupd AS fwupd-runtime
COPY --from=fwupd-efi /fwupd-efi /fwupd
RUN rm -rf /fwupd/usr/include /fwupd/usr/lib/pkgconfig /fwupd/usr/share/installed-tests \
      /fwupd/usr/lib/installed-tests /fwupd/usr/share/fwupd/host-emulate.d \
      /fwupd/etc/fwupd/remotes.d/fwupd-tests.conf /fwupd/usr/share/doc /fwupd/etc/grub.d && \
    find /fwupd -name '*.a' -delete && \
    mkdir -p /fwupd-runtime && cp -a /fwupd/. /fwupd-runtime/ && \
    cp -a /usr/lib/libusb-1.0.so.* /usr/lib/libsqlite3.so /usr/lib/libsqlite3.so.* /usr/lib/libxmlb.so.* /fwupd-runtime/usr/lib/ && \
    mkdir -p /fwupd-runtime/usr/share/licenses && cp -a /usr/share/licenses/libusb /fwupd-runtime/usr/share/licenses/ && \
    for l in fwupd fwupd-efi libusb; do test -s /fwupd-runtime/usr/share/licenses/$l/COPYING || exit 1; done && \
    test -f /fwupd-runtime/usr/lib/fwupd/efi/fwupdx64.efi && \
    ! find /fwupd-runtime \( -name '*.h' -o -name '*.pc' -o -name '*.a' \) | grep -q . && \
    priv=$(echo /fwupd-runtime/usr/lib/fwupd-[0-9]*) && test -f "$priv/libfwupdengine.so" && \
    ! LD_LIBRARY_PATH="/fwupd-runtime/usr/lib:$priv" \
      ldd /fwupd-runtime/usr/lib/fwupd/fwupd /fwupd-runtime/usr/bin/fwupdmgr /fwupd-runtime/usr/bin/fwupdtool 2>&1 | grep -E 'not found|Error'

FROM toolchain AS libqalculate
COPY --from=gmp /gmp /
COPY --from=mpfr /mpfr /
COPY --from=gettext-tools /gettext-tools /
# renovate: datasource=github-tags depName=Qalculate/libqalculate extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG LIBQALCULATE_VERSION=5.13.1
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/Qalculate/libqalculate/releases/download/v${LIBQALCULATE_VERSION}/libqalculate-${LIBQALCULATE_VERSION}.tar.gz -o q.tar.gz && tar -xzf q.tar.gz && mv libqalculate-* src
WORKDIR /build/src
RUN ./configure --prefix=/usr --enable-shared --disable-static --disable-textport --without-icu --without-readline --disable-nls && \
    make -j"$(nproc)" && make DESTDIR=/libqalculate install

# libsecret: the Secret Service client library (stored credentials).
FROM toolchain AS libsecret
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=libgpg-error /libgpg-error /
COPY --from=libgcrypt /libgcrypt /
# renovate: datasource=gitlab-tags depName=GNOME/libsecret registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG LIBSECRET_VERSION=0.21.8
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://download.gnome.org/sources/libsecret/${LIBSECRET_VERSION%.*}/libsecret-${LIBSECRET_VERSION}.tar.xz -o s.tar.xz && tar -xf s.tar.xz && mv libsecret-* src
WORKDIR /build/src
RUN pip3 install meson ninja
# secret-file-collection.c calls open() relying on glibc to pull in fcntl.h;
# fixed on libsecret's main branch, not yet in a release.
RUN sed -i 's|^#include "config.h"|&\n#include <fcntl.h>|' libsecret/secret-file-collection.c
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dmanpage=false -Dvapi=false -Dgtk_doc=false -Dintrospection=false \
      -Dbash_completion=disabled -Dtpm2=false -Dpam=false -Dcrypto=libgcrypt && \
    DESTDIR=/libsecret ninja -C buildDir install

# gnome-keyring: the Secret Service (org.freedesktop.secrets) on every variant,
# so Electron and Chromium apps, Flatpak ones included, keep passwords in an
# encrypted keyring instead of a plain text file. pam_gnome_keyring.so unlocks
# the "login" keyring with the login password: ly's and greetd's PAM services
# already call it as an optional module. It needs gcr 3 (gck-1, gcr-base-3),
# which needs p11-kit. No gcr-prompter (that is GTK): a keyring that the login
# did not unlock cannot be unlocked by a dialog.
FROM toolchain AS gnome-keyring
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=libgpg-error /libgpg-error /
COPY --from=libgcrypt /libgcrypt /
COPY --from=gettext-tools /gettext-tools /
# Hadron's libsystemd links libucontext (musl has no getcontext).
COPY --from=libucontext /libucontext /
# renovate: datasource=github-releases depName=p11-glue/p11-kit
ARG P11_KIT_VERSION=0.26.5
# renovate: datasource=gitlab-tags depName=GNOME/gcr registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>3\.\d+(\.\d+)*)$
ARG GCR3_VERSION=3.41.2
# renovate: datasource=gitlab-tags depName=GNOME/gnome-keyring registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG GNOME_KEYRING_VERSION=51.1
RUN pip3 install meson ninja
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/p11-glue/p11-kit/releases/download/${P11_KIT_VERSION}/p11-kit-${P11_KIT_VERSION}.tar.xz -o p.tar.xz && \
    curl --retry-all-errors -fL --retry 5 https://download.gnome.org/sources/gcr/${GCR3_VERSION%.*}/gcr-${GCR3_VERSION}.tar.xz -o g.tar.xz && \
    curl --retry-all-errors -fL --retry 5 https://download.gnome.org/sources/gnome-keyring/${GNOME_KEYRING_VERSION%.*}/gnome-keyring-${GNOME_KEYRING_VERSION}.tar.xz -o k.tar.xz && \
    for f in p g k; do tar -xf $f.tar.xz && rm $f.tar.xz; done
# Each project installs into the build root too, so the next one finds it.
# DESTDIR=/ for that: without a DESTDIR gcr's post-install script runs
# gtk-update-icon-cache, which is not here.
# p11-kit without its trust module: Hadron's CA bundle comes from the base
# image, and gcr only needs the PKCS#11 module loader.
RUN cd p11-kit-* && \
    meson setup buildDir ${COMMON_MESON_FLAGS} -Dtrust_module=disabled -Dlibffi=disabled \
      -Dsystemd=disabled -Dbash_completion=disabled -Dzsh_completion=disabled \
      -Dgtk_doc=false -Dman=false -Dnls=false -Dtest=false && \
    DESTDIR=/gnome-keyring ninja -C buildDir install && DESTDIR=/ ninja -C buildDir install
# gpg_path is only a string gcr would run for OpenPGP keys; the keyring never
# does, and Hadron's gpg is not in this stage. gcr 3.41.2 asks for the path of
# ssh-add and ssh-agent even with ssh_agent=false, so build-only stubs stand in.
# musl declares getpass() (console-interaction.c) only under _GNU_SOURCE.
RUN printf '#!/bin/sh\nexit 1\n' > /usr/local/bin/ssh-add && cp /usr/local/bin/ssh-add /usr/local/bin/ssh-agent && \
    chmod +x /usr/local/bin/ssh-add /usr/local/bin/ssh-agent && \
    cd gcr-* && \
    CFLAGS="${CFLAGS:-} -D_GNU_SOURCE" meson setup buildDir ${COMMON_MESON_FLAGS} -Dgtk=false -Dintrospection=false \
      -Dgtk_doc=false -Dssh_agent=false -Dsystemd=disabled -Dgpg_path=/usr/bin/gpg && \
    DESTDIR=/gnome-keyring ninja -C buildDir install && DESTDIR=/ ninja -C buildDir install
# The toolchain has libsystemd.pc but no systemd.pc, which gnome-keyring reads
# the user unit directory from.
RUN printf 'prefix=/usr\nsystemduserunitdir=${prefix}/lib/systemd/user\n\nName: systemd\nDescription: systemd\nVersion: 257\n' \
      > /usr/lib/pkgconfig/systemd.pc
RUN cd gnome-keyring-* && \
    meson setup buildDir ${COMMON_MESON_FLAGS} -Dpam=true -Dssh-agent=false -Dsystemd=enabled \
      -Dselinux=disabled -Dlibcap-ng=disabled -Ddebug-mode=false -Dmanpage=false && \
    DESTDIR=/gnome-keyring ninja -C buildDir install
# Headers, pkg-config files and the test-only modules (devel/) are not
# shipped. gcr installs D-Bus activation files for gcr-prompter even when it
# does not build it (-Dgtk=false).
RUN rm -rf /gnome-keyring/usr/include /gnome-keyring/usr/lib/pkgconfig \
      /gnome-keyring/usr/share/gtk-doc /gnome-keyring/usr/share/vala /gnome-keyring/usr/share/GConf \
      /gnome-keyring/usr/share/dbus-1/services/org.gnome.keyring.SystemPrompter.service \
      /gnome-keyring/usr/share/dbus-1/services/org.gnome.keyring.PrivatePrompter.service \
      /gnome-keyring/usr/lib/gnome-keyring/devel && \
    test -x /gnome-keyring/usr/bin/gnome-keyring-daemon && \
    test -f /gnome-keyring/usr/lib/security/pam_gnome_keyring.so && \
    test -f /gnome-keyring/usr/lib/systemd/user/gnome-keyring-daemon.socket && \
    test -f /gnome-keyring/usr/share/dbus-1/services/org.freedesktop.secrets.service

# niri's build environment: COSMIC's (Rust, libclang, the Wayland/input/GPU
# stack, PipeWire) plus cairo and pango, which niri draws its overlays with.
FROM cosmic-build AS niri-build
COPY --from=pixman /pixman /
COPY --from=cairo /cairo /
COPY --from=pango /pango /
COPY --from=x11-libs /x11-libs /
COPY --from=xwayland /xwayland /
RUN ldconfig

# librsvg (SVG icons and wallpapers) is Rust behind a C API, built with
# cargo-c. cargo-c itself is compiled here, against Hadron's libraries.
FROM niri-build AS librsvg
# renovate: datasource=crate depName=cargo-c
ARG CARGO_C_VERSION=0.10.26
# renovate: datasource=gitlab-tags depName=GNOME/librsvg registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG LIBRSVG_VERSION=2.63.4
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked \
    cargo install cargo-c --version ${CARGO_C_VERSION} --locked --features vendored-openssl --root /usr/local
RUN curl --retry-all-errors -fL --retry 5 https://download.gnome.org/sources/librsvg/${LIBRSVG_VERSION%.*}/librsvg-${LIBRSVG_VERSION}.tar.xz -o r.tar.xz && tar -xf r.tar.xz && mv librsvg-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked \
    meson setup buildDir ${COMMON_MESON_FLAGS} -Dintrospection=disabled -Dpixbuf=disabled -Dpixbuf-loader=disabled \
      -Drsvg-convert=disabled -Ddocs=disabled -Dvala=disabled -Dtests=false -Davif=disabled && \
    DESTDIR=/librsvg ninja -C buildDir install && \
    rm -rf buildDir && \
    ! ldd /librsvg/usr/lib/librsvg-2.so.2 | grep 'not found'

# niri's release profile keeps line-table debug info (~130 MB binary); the
# installed copy is stripped.
# rootfs/niri's niri.desktop replaces the one installed here: it starts
# isotope-niri-session (the GPU check, then niri-session).
FROM niri-build AS niri
# renovate: datasource=github-tags depName=YaLTeR/niri extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG NIRI_VERSION=26.04
ENV CARGO_TARGET_DIR=/cargo-target
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked \
    --mount=type=cache,id=niri-target,target=/cargo-target,sharing=locked \
    curl --retry-all-errors -fsSL --retry 5 https://github.com/YaLTeR/niri/archive/refs/tags/v${NIRI_VERSION}.tar.gz | tar -xz && \
    cd niri-${NIRI_VERSION} && \
    cargo build --release --locked && \
    install -s -Dm755 /cargo-target/release/niri /niri/usr/bin/niri && \
    install -Dm755 resources/niri-session /niri/usr/bin/niri-session && \
    install -Dm644 resources/niri.desktop /niri/usr/share/wayland-sessions/niri.desktop && \
    install -Dm644 resources/niri-portals.conf /niri/usr/share/xdg-desktop-portal/niri-portals.conf && \
    install -Dm644 resources/niri.service /niri/usr/lib/systemd/user/niri.service && \
    install -Dm644 resources/niri-shutdown.target /niri/usr/lib/systemd/user/niri-shutdown.target && \
    rm -rf /cargo-target/* && \
    ! ldd /niri/usr/bin/niri | grep 'not found' && \
    /niri/usr/bin/niri --version

# xwayland-satellite: runs Xwayland as an ordinary Wayland client, so X11 apps
# work under niri. niri starts it on demand when it is on PATH.
FROM niri-build AS xwayland-satellite
# renovate: datasource=github-tags depName=Supreeeme/xwayland-satellite extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG XWAYLAND_SATELLITE_VERSION=0.8.3
ENV CARGO_TARGET_DIR=/cargo-target
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked \
    --mount=type=cache,id=niri-target,target=/cargo-target,sharing=locked \
    curl --retry-all-errors -fsSL --retry 5 https://github.com/Supreeeme/xwayland-satellite/archive/refs/tags/v${XWAYLAND_SATELLITE_VERSION}.tar.gz | tar -xz && \
    cd xwayland-satellite-${XWAYLAND_SATELLITE_VERSION} && \
    cargo build --release --locked -F systemd && \
    install -s -Dm755 /cargo-target/release/xwayland-satellite /xwayland-satellite/usr/bin/xwayland-satellite && \
    rm -rf /cargo-target/* && \
    ! ldd /xwayland-satellite/usr/bin/xwayland-satellite | grep 'not found'

# Noctalia, the desktop shell. Its runtime assets (fonts, translations,
# templates, sounds) install under /usr/share/noctalia and must ship with it.
FROM toolchain AS noctalia
COPY --from=wayland /wayland /
COPY --from=libdrm /libdrm /
COPY --from=libucontext /libucontext /
COPY --from=mesa /mesa /
COPY --from=libxkb /libxkb /
COPY --from=xkeyboard-config /xkeyboard-config /
COPY --from=pcre2 /pcre2 /
COPY --from=glib2 /glib2 /
COPY --from=freetype /freetype /
COPY --from=harfbuzz /harfbuzz /
COPY --from=fribidi /fribidi /
COPY --from=fontconfig /fontconfig /
COPY --from=libpng /libpng /
COPY --from=pixman /pixman /
COPY --from=cairo /cairo /
COPY --from=pango /pango /
COPY --from=libepoxy /libepoxy /
COPY --from=duktape /duktape /
COPY --from=polkit /polkit /
COPY --from=alsa-lib /alsa-lib /
COPY --from=pipewire /pipewire /
COPY --from=wireplumber /wireplumber /
COPY --from=libsndfile /libsndfile /
COPY --from=libgpg-error /libgpg-error /
COPY --from=libgcrypt /libgcrypt /
COPY --from=libsecret /libsecret /
COPY --from=libsodium /libsodium /
COPY --from=sdbus-cpp /sdbus-cpp /
COPY --from=librsvg /librsvg /
COPY --from=gmp /gmp /
COPY --from=mpfr /mpfr /
COPY --from=libqalculate /libqalculate /
COPY --from=md4c /md4c /
COPY --from=nlohmann-json /nlohmann-json /
COPY --from=tomlplusplus /tomlplusplus /
COPY --from=libical /libical /
COPY --from=stb /stb /
COPY --from=libwebp /libwebp /
COPY --from=brotli /brotli /
COPY --from=highway /highway /
COPY --from=lcms2 /lcms2 /
COPY --from=libjxl /libjxl /
# renovate: datasource=github-tags depName=noctalia-dev/noctalia extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG NOCTALIA_VERSION=5.4.1
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/noctalia-dev/noctalia/archive/refs/tags/v${NOCTALIA_VERSION}.tar.gz -o n.tar.gz && tar -xzf n.tar.gz && mv noctalia-${NOCTALIA_VERSION} src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir --prefix=/usr --libdir=lib --buildtype=release -Dstrip=true -Dtests=disabled -Djemalloc=disabled && \
    ninja -C buildDir && DESTDIR=/noctalia ninja -C buildDir install
RUN ! ldd /noctalia/usr/bin/noctalia | grep 'not found' && \
    test -f /noctalia/usr/share/noctalia/assets/emoji.json

# The Adwaita cursor theme, cursors only (not the icon set), registered as the
# system default. Without a cursor theme sway, i3, niri and GTK apps fall back
# to a bare built-in arrow and no resize or text cursors.
FROM toolchain AS adwaita-cursors
# renovate: datasource=gitlab-tags depName=GNOME/adwaita-icon-theme registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG ADWAITA_ICON_THEME_VERSION=51.0
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://download.gnome.org/sources/adwaita-icon-theme/${ADWAITA_ICON_THEME_VERSION%%.*}/adwaita-icon-theme-${ADWAITA_ICON_THEME_VERSION}.tar.xz -o a.tar.xz && tar -xf a.tar.xz && mv adwaita-icon-theme-* src
RUN d=/adwaita-cursors/usr/share/icons && mkdir -p $d/Adwaita $d/default && \
    cp -a src/Adwaita/cursors $d/Adwaita/cursors && \
    printf '[Icon Theme]\nName=Adwaita\nComment=The Adwaita cursor theme\n' > $d/Adwaita/index.theme && \
    printf '[Icon Theme]\nInherits=Adwaita\n' > $d/default/index.theme && \
    test -e $d/Adwaita/cursors/default

# GSettings schemas for the GTK portal's Settings interface, which is how
# Flatpak apps learn the colour scheme (org.freedesktop.appearance
# color-scheme) and, for GTK3 apps, the theme and fonts. xdg-desktop-portal-gtk
# reads them from org.gnome.desktop.interface. Hadron's defaults are a vendor
# override: dark colour scheme, Adwaita-dark for GTK3, and the Isotope fonts
# (the isotope-fonts stage) by their real family names, because a Flatpak sees
# the host's font files but not its fontconfig aliases. GTK3 has the dark
# Adwaita built in only as a variant, so the theme named Adwaita-dark is a
# one-line stylesheet that imports it. COSMIC adds its own override
# (rootfs/cosmic/usr/share/glib-2.0/schemas/91_isotope-cosmic.gschema.override)
# that puts GTK's window buttons on the left, as its patched libcosmic does.
#
# No dconf: GLib then uses its keyfile backend, which reads the compiled schemas
# with this override (so every app and portal sees the dark defaults) and keeps
# user changes in ~/.config/glib-2.0/settings/keyfile. dconf would only add a
# daemon, a GIO module and its cache in every image. On COSMIC that keyfile is
# where cosmic-settings-daemon writes its `gsettings set ...
# button-layout` (at start and when the CosmicTk window buttons change), and
# that user value beats the schema default. So the override alone does not keep
# the buttons on the left: patches/cosmic/cosmic-settings-daemon makes the
# daemon write the left layout when window_controls_left is true. The default
# stage compiles the schemas (glib-compile-schemas); installs under DESTDIR
# skip it.
FROM toolchain AS gsettings-desktop-schemas
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
# renovate: datasource=gitlab-tags depName=GNOME/gsettings-desktop-schemas registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG GSETTINGS_SCHEMAS_VERSION=47.1
RUN mkdir -p /gsettings-desktop-schemas
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.gnome.org/sources/gsettings-desktop-schemas/${GSETTINGS_SCHEMAS_VERSION%.*}/gsettings-desktop-schemas-${GSETTINGS_SCHEMAS_VERSION}.tar.xz -o s.tar.xz && tar -xf s.tar.xz && rm s.tar.xz && mv gsettings-desktop-schemas-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dintrospection=false && \
    DESTDIR=/gsettings-desktop-schemas ninja -C buildDir install
RUN d=/gsettings-desktop-schemas/usr/share && \
    printf '%s\n' '# Isotope defaults: dark apps and the Isotope fonts, including Flatpak apps through the portal.' \
      '[org.gnome.desktop.interface]' "color-scheme='prefer-dark'" "gtk-theme='Adwaita-dark'" \
      "font-name='Inter Variable 10.5'" "icon-theme='Conflux'" "document-font-name='Inter Variable 10.5'" \
      "monospace-font-name='JetBrains Mono 10.5'" \
      > $d/glib-2.0/schemas/90_isotope.gschema.override && \
    mkdir -p $d/themes/Adwaita-dark/gtk-3.0 && \
    printf '@import url("resource:///org/gtk/libgtk/theme/Adwaita/gtk-contained-dark.css");\n' \
      > $d/themes/Adwaita-dark/gtk-3.0/gtk.css && \
    cp -a $d/glib-2.0/schemas /tmp/schemas && glib-compile-schemas --strict /tmp/schemas && \
    test "$(gsettings --schemadir /tmp/schemas get org.gnome.desktop.interface color-scheme 2>/dev/null)" = "'prefer-dark'" && \
    test "$(gsettings --schemadir /tmp/schemas get org.gnome.desktop.interface font-name 2>/dev/null)" = "'Inter Variable 10.5'" && \
    test "$(gsettings --schemadir /tmp/schemas get org.gnome.desktop.interface icon-theme 2>/dev/null)" = "'Conflux'" && \
    rm -rf /tmp/schemas

# xdg-desktop-portal-gtk: the file chooser (and app chooser, print, settings)
# Flatpak apps open through the portal, on Sway and niri (i3 has its own X11
# build below). niri's portals.conf prefers gnome, then gtk; the GNOME backend
# needs GTK4 and mutter interfaces, so gtk it is. Its Settings interface serves
# the dark colour scheme from the gsettings-desktop-schemas stage.
FROM toolchain AS xdg-desktop-portal-gtk
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=cairo /cairo /
COPY --from=pango /pango /
COPY --from=harfbuzz /harfbuzz /
COPY --from=fribidi /fribidi /
COPY --from=freetype /freetype /
COPY --from=fontconfig /fontconfig /
COPY --from=libpng /libpng /
COPY --from=pixman /pixman /
COPY --from=gdk-pixbuf /gdk-pixbuf /
COPY --from=atk /atk /
COPY --from=libepoxy /libepoxy /
COPY --from=wayland /wayland /
COPY --from=libxkb /libxkb /
COPY --from=mesa /mesa /
COPY --from=libdrm /libdrm /
COPY --from=gtk3 /gtk3 /
COPY --from=xdg-desktop-portal /xdg-desktop-portal /
COPY --from=gettext-tools /gettext-tools /
COPY --from=gsettings-desktop-schemas /gsettings-desktop-schemas /
# renovate: datasource=github-tags depName=flatpak/xdg-desktop-portal-gtk extractVersion=^(?<version>\d+(\.\d+)*)$
ARG XDG_DESKTOP_PORTAL_GTK_VERSION=1.15.3
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/flatpak/xdg-desktop-portal-gtk/releases/download/${XDG_DESKTOP_PORTAL_GTK_VERSION}/xdg-desktop-portal-gtk-${XDG_DESKTOP_PORTAL_GTK_VERSION}.tar.xz -o x.tar.xz && tar -xf x.tar.xz && mv xdg-desktop-portal-gtk-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} --libexecdir=lib -Dwallpaper=disabled -Dsettings=enabled -Dlockdown=disabled && \
    DESTDIR=/xdg-desktop-portal-gtk ninja -C buildDir install && \
    ! ldd /xdg-desktop-portal-gtk/usr/lib/xdg-desktop-portal-gtk | grep 'not found'

# inih: the INI parser xdg-desktop-portal-wlr reads its config with.
FROM toolchain AS inih
# renovate: datasource=github-tags depName=benhoyt/inih extractVersion=^r(?<version>\d+)$
ARG INIH_VERSION=62
RUN mkdir -p /inih
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/benhoyt/inih/archive/refs/tags/r${INIH_VERSION}.tar.gz -o inih.tar.gz && tar -xf inih.tar.gz && rm inih.tar.gz && mv inih-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Ddefault_library=shared -Ddistro_install=true && \
    DESTDIR=/inih ninja -C buildDir install

# xdg-desktop-portal-wlr: the Screenshot and ScreenCast backends on Sway. It
# captures through wlroots' screencopy protocol and streams through PipeWire;
# a screen share asks which output with slurp.
FROM toolchain AS xdg-desktop-portal-wlr
COPY --from=libucontext /libucontext /
COPY --from=wayland /wayland /
COPY --from=libdrm /libdrm /
COPY --from=mesa /mesa /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=alsa-lib /alsa-lib /
COPY --from=pipewire /pipewire /
COPY --from=inih /inih /
# renovate: datasource=github-tags depName=emersion/xdg-desktop-portal-wlr extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG XDG_DESKTOP_PORTAL_WLR_VERSION=0.8.4
RUN mkdir -p /xdg-desktop-portal-wlr
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://github.com/emersion/xdg-desktop-portal-wlr/releases/download/v${XDG_DESKTOP_PORTAL_WLR_VERSION}/xdg-desktop-portal-wlr-${XDG_DESKTOP_PORTAL_WLR_VERSION}.tar.gz -o x.tar.gz && tar -xf x.tar.gz && rm x.tar.gz && mv xdg-desktop-portal-wlr-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} --libexecdir=lib -Dsd-bus-provider=libsystemd -Dsystemd=enabled -Dman-pages=disabled && \
    DESTDIR=/xdg-desktop-portal-wlr ninja -C buildDir install && \
    ! ldd /xdg-desktop-portal-wlr/usr/lib/xdg-desktop-portal-wlr | grep 'not found'

# at-spi2-core: ATK and the AT-SPI bridge. GTK3's X11 backend requires the
# bridge (the Wayland build above does not). Same version as Dockerfile.agent,
# whose X11-enabled build overwrites these libraries in the agent image; this
# one leaves X11 off so the i3 image needs no libXtst. --libexecdir=lib for
# the same reason as flatpak: /usr/libexec is a persistent path on Kairos.
FROM toolchain AS at-spi2-core
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=libucontext /libucontext /
# renovate: datasource=gitlab-tags depName=GNOME/at-spi2-core registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG ATSPI_VERSION=2.54.0
RUN mkdir -p /at-spi2-core
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.gnome.org/sources/at-spi2-core/${ATSPI_VERSION%.*}/at-spi2-core-${ATSPI_VERSION}.tar.xz -o a.tar.xz && tar -xf a.tar.xz && rm a.tar.xz && mv at-spi2-core-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} --libexecdir=lib \
      -Ddefault_bus=dbus-daemon -Ddbus_daemon=/usr/bin/dbus-daemon -Duse_systemd=true \
      -Dgtk2_atk_adaptor=false -Ddocs=false -Dintrospection=disabled -Dx11=disabled && \
    DESTDIR=/at-spi2-core ninja -C buildDir install

# GTK3 with the X11 backend, for the i3 image only: xdg-desktop-portal-gtk
# and snixembed need a GTK that can open a window on XLibre. The Wayland variants keep the
# Wayland-only gtk3 stage, so they gain no X11 libraries.
# The X11 GTK for i3, with the CUPS print backend as gtk3 above.
FROM toolchain AS gtk3-x11
COPY --from=cups /cups /
COPY --from=avahi /avahi /
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=xorgproto /xorgproto /
COPY --from=xcbproto /xcbproto /
COPY --from=libxau /libxau /
COPY --from=libxcb /libxcb /
COPY --from=x11-libs /x11-libs /
COPY --from=cairo-x11 /cairo-x11 /
COPY --from=pango-x11 /pango-x11 /
COPY --from=harfbuzz /harfbuzz /
COPY --from=fribidi /fribidi /
COPY --from=freetype /freetype /
COPY --from=fontconfig /fontconfig /
COPY --from=libpng /libpng /
COPY --from=pixman /pixman /
COPY --from=gdk-pixbuf /gdk-pixbuf /
COPY --from=at-spi2-core /at-spi2-core /
COPY --from=libdrm /libdrm /
COPY --from=mesa-x11 /mesa-x11 /
COPY --from=libepoxy-x11 /libepoxy-x11 /
COPY --from=libucontext /libucontext /
# renovate: datasource=gitlab-tags depName=GNOME/gtk registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG GTK3_VERSION=3.24.43
RUN mkdir -p /gtk3-x11
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.gnome.org/sources/gtk+/${GTK3_VERSION%.*}/gtk+-${GTK3_VERSION}.tar.xz -o gtk.tar.xz && tar -xf gtk.tar.xz && rm gtk.tar.xz && mv gtk+-* gtk-src
WORKDIR /build/gtk-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} \
    -Dx11_backend=true -Dwayland_backend=false -Dbroadway_backend=false \
    -Dintrospection=false -Dgtk_doc=false -Dman=false -Ddemos=false -Dexamples=false -Dtests=false \
    -Dprint_backends=file,cups -Dcolord=no
RUN DESTDIR=/gtk3-x11 ninja -C buildDir install && \
    test -f /gtk3-x11/usr/lib/gtk-3.0/3.0.0/printbackends/libprintbackend-cups.so

# xdg-desktop-portal-gtk again, built against gtk3-x11 for i3. Same source and
# options as the stage above.
FROM toolchain AS xdg-desktop-portal-gtk-x11
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=xorgproto /xorgproto /
COPY --from=xcbproto /xcbproto /
COPY --from=libxau /libxau /
COPY --from=libxcb /libxcb /
COPY --from=x11-libs /x11-libs /
COPY --from=cairo-x11 /cairo-x11 /
COPY --from=pango-x11 /pango-x11 /
COPY --from=harfbuzz /harfbuzz /
COPY --from=fribidi /fribidi /
COPY --from=freetype /freetype /
COPY --from=fontconfig /fontconfig /
COPY --from=libpng /libpng /
COPY --from=pixman /pixman /
COPY --from=gdk-pixbuf /gdk-pixbuf /
COPY --from=at-spi2-core /at-spi2-core /
COPY --from=libdrm /libdrm /
COPY --from=mesa-x11 /mesa-x11 /
COPY --from=libepoxy-x11 /libepoxy-x11 /
COPY --from=libucontext /libucontext /
COPY --from=gtk3-x11 /gtk3-x11 /
COPY --from=xdg-desktop-portal /xdg-desktop-portal /
COPY --from=gettext-tools /gettext-tools /
COPY --from=gsettings-desktop-schemas /gsettings-desktop-schemas /
# renovate: datasource=github-tags depName=flatpak/xdg-desktop-portal-gtk extractVersion=^(?<version>\d+(\.\d+)*)$
ARG XDG_DESKTOP_PORTAL_GTK_VERSION=1.15.3
WORKDIR /build
RUN curl --retry-all-errors -fL --retry 5 https://github.com/flatpak/xdg-desktop-portal-gtk/releases/download/${XDG_DESKTOP_PORTAL_GTK_VERSION}/xdg-desktop-portal-gtk-${XDG_DESKTOP_PORTAL_GTK_VERSION}.tar.xz -o x.tar.xz && tar -xf x.tar.xz && mv xdg-desktop-portal-gtk-* src
WORKDIR /build/src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} --libexecdir=lib -Dwallpaper=disabled -Dsettings=enabled -Dlockdown=disabled && \
    DESTDIR=/xdg-desktop-portal-gtk-x11 ninja -C buildDir install && \
    ! ldd /xdg-desktop-portal-gtk-x11/usr/lib/xdg-desktop-portal-gtk | grep 'not found'

# libdbusmenu-gtk3 again, against gtk3-x11 for snixembed on i3. Same source,
# stubs and options as the libdbusmenu stage.
FROM libdbusmenu-src AS libdbusmenu-gtk3-x11
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=xorgproto /xorgproto /
COPY --from=xcbproto /xcbproto /
COPY --from=libxau /libxau /
COPY --from=libxcb /libxcb /
COPY --from=x11-libs /x11-libs /
COPY --from=cairo-x11 /cairo-x11 /
COPY --from=pango-x11 /pango-x11 /
COPY --from=harfbuzz /harfbuzz /
COPY --from=fribidi /fribidi /
COPY --from=freetype /freetype /
COPY --from=fontconfig /fontconfig /
COPY --from=libpng /libpng /
COPY --from=pixman /pixman /
COPY --from=gdk-pixbuf /gdk-pixbuf /
COPY --from=at-spi2-core /at-spi2-core /
COPY --from=libdrm /libdrm /
COPY --from=mesa-x11 /mesa-x11 /
COPY --from=libepoxy-x11 /libepoxy-x11 /
COPY --from=libucontext /libucontext /
COPY --from=gtk3-x11 /gtk3-x11 /
RUN mkdir -p /libdbusmenu-gtk3-x11
RUN ./configure ${COMMON_CONFIGURE_ARGS} --with-gtk=3 --disable-introspection --disable-vala --disable-gtk-doc --disable-dumper --disable-tests --disable-static --disable-nls
RUN find . -name Makefile -exec sed -i 's/-Werror//g' {} +
RUN make -C libdbusmenu-glib -j"$(nproc)" && make -C libdbusmenu-gtk -j"$(nproc)" \
    && make -C libdbusmenu-glib DESTDIR=/libdbusmenu-gtk3-x11 install \
    && make -C libdbusmenu-gtk  DESTDIR=/libdbusmenu-gtk3-x11 install

# Vala compiler, to build snixembed. Build-time only: no rootfs copies this
# stage. The release tarball carries valac's own sources already translated to
# C, so it needs no earlier valac; valadoc is off (it needs graphviz).
# GI_GIRDIR is only where vapigen would look for .gir files; there is no
# GObject Introspection here, and configure fails without a value.
FROM toolchain AS vala
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=libucontext /libucontext /
# renovate: datasource=gitlab-tags depName=GNOME/vala registryUrl=https://gitlab.gnome.org extractVersion=^(?<version>\d+(\.\d+)*)$
ARG VALA_VERSION=0.56.19
RUN mkdir -p /vala
WORKDIR /build
RUN curl --retry 5 --retry-delay 3 --retry-all-errors -fL https://download.gnome.org/sources/vala/${VALA_VERSION%.*}/vala-${VALA_VERSION}.tar.xz -o vala.tar.xz && tar -xf vala.tar.xz && rm vala.tar.xz && mv vala-* src
WORKDIR /build/src
RUN ./configure ${COMMON_CONFIGURE_ARGS} --disable-valadoc GI_GIRDIR=/usr/share/gir-1.0 && \
    make -j"$(nproc)" && make install DESTDIR=/vala

# snixembed: shows StatusNotifierItems (the update tray's icon) as XEmbed tray
# icons, the only kind i3bar hosts. It draws them with GTK3's GtkStatusIcon,
# which exists only in the X11 backend, so it builds against gtk3-x11 and
# libdbusmenu-gtk3-x11 rather than the Wayland gtk3. Upstream is on sourcehut
# only (no GitHub mirror), so the archive comes from git.sr.ht. When git.sr.ht
# refuses the connection (it did for a whole agent gate run), the same archive
# comes from Debian's pool: snixembed_<version>.orig.tar.gz is byte-identical
# to the sourcehut tarball (same sha256 for 0.3.3).
#
# libdbusmenu ships its Vala bindings only when built with GObject
# Introspection, which Hadron does not build. snixembed uses one thing from
# them, DbusmenuGtk.Menu's constructor, so the two bindings it names
# (--pkg Dbusmenu-0.4 DbusmenuGtk3-0.4) are written here: an empty one and one
# declaring that class. The C code still includes libdbusmenu's real header
# and links its library, so a wrong function name fails the build.
FROM toolchain AS snixembed
COPY --from=glib2 /glib2 /
COPY --from=pcre2 /pcre2 /
COPY --from=xorgproto /xorgproto /
COPY --from=xcbproto /xcbproto /
COPY --from=libxau /libxau /
COPY --from=libxcb /libxcb /
COPY --from=x11-libs /x11-libs /
COPY --from=cairo-x11 /cairo-x11 /
COPY --from=pango-x11 /pango-x11 /
COPY --from=harfbuzz /harfbuzz /
COPY --from=fribidi /fribidi /
COPY --from=freetype /freetype /
COPY --from=fontconfig /fontconfig /
COPY --from=libpng /libpng /
COPY --from=pixman /pixman /
COPY --from=gdk-pixbuf /gdk-pixbuf /
COPY --from=at-spi2-core /at-spi2-core /
COPY --from=libdrm /libdrm /
COPY --from=mesa-x11 /mesa-x11 /
COPY --from=libepoxy-x11 /libepoxy-x11 /
COPY --from=libucontext /libucontext /
COPY --from=gtk3-x11 /gtk3-x11 /
COPY --from=libdbusmenu-gtk3-x11 /libdbusmenu-gtk3-x11 /
COPY --from=vala /vala /
# renovate: datasource=git-tags depName=https://git.sr.ht/~steef/snixembed extractVersion=^(?<version>\d+(\.\d+)*)$
ARG SNIXEMBED_VERSION=0.3.3
RUN mkdir -p /snixembed
WORKDIR /build
RUN { curl --connect-timeout 20 --retry 5 --retry-delay 3 --retry-all-errors -fL -o s.tar.gz \
        https://git.sr.ht/~steef/snixembed/archive/${SNIXEMBED_VERSION}.tar.gz || \
      curl --retry 5 --retry-delay 3 --retry-all-errors -fL -o s.tar.gz \
        https://deb.debian.org/debian/pool/main/s/snixembed/snixembed_${SNIXEMBED_VERSION}.orig.tar.gz; } && \
    tar -xzf s.tar.gz && rm s.tar.gz && mv snixembed-* src
WORKDIR /build/src
RUN v=/usr/share/vala/vapi && mkdir -p $v && \
    printf '%s\n' '/* Empty: snixembed uses nothing from libdbusmenu-glib directly. */' \
      'namespace Dbusmenu {}' > $v/Dbusmenu-0.4.vapi && \
    printf '%s\n' '/* The part of libdbusmenu-gtk3 snixembed uses (see the Dockerfile). */' \
      '[CCode (cprefix = "DbusmenuGtk", lower_case_cprefix = "dbusmenu_gtk_")]' \
      'namespace DbusmenuGtk {' \
      '	[CCode (cheader_filename = "libdbusmenu-gtk/menu.h", type_id = "dbusmenu_gtkmenu_get_type ()")]' \
      '	public class Menu : Gtk.Menu {' \
      '		[CCode (cname = "dbusmenu_gtkmenu_new", has_construct_function = false)]' \
      '		public Menu (string dbus_name, string dbus_object);' \
      '	}' \
      '}' > $v/DbusmenuGtk3-0.4.vapi
RUN make && make install DESTDIR=/snixembed PREFIX=/usr && \
    rm -rf /snixembed/usr/share/man && \
    /snixembed/usr/bin/snixembed --version | grep -qx "version: ${SNIXEMBED_VERSION}" && \
    ! ldd /snixembed/usr/bin/snixembed | grep 'not found'

# ===========================================================================
# M6: real-hardware firmware (the full GPU line)
#
# Real GPU, Wi-Fi, Bluetooth and audio hardware needs firmware blobs, and the
# CPU needs its microcode updates. This is OFF by default (the vm line and the
# QEMU test path need no blobs); the full line builds with
# --build-arg FIRMWARE=true. Not tested in CI: QEMU has none of the hardware.
#
# It is a chosen part of linux-firmware, not all of it (about 960 MB with
# zstd): what common laptops and desktops need to show a display and to have
# Wi-Fi, Bluetooth, wired Ethernet and sound.
#   GPU: amdgpu, radeon (GCN 1 and 2, older Radeons), i915, xe (Intel from
#     Lunar Lake on), and nouveau's GSP firmware (Turing and later need it to
#     drive the display; nouveau uses GSP by default). Only the GSP-RM release
#     the kernel prefers (NOUVEAU_GSP_RM); no pre-Turing nvidia blobs, which
#     only accelerate, and Mesa here has no NVIDIA driver.
#   Wi-Fi and Bluetooth: Intel (iwlwifi, the 2 newest APIs of each card; ibt),
#     the Qualcomm laptop chips (ath10k QCA6174/QCA9377/QCA988X, ath11k
#     WCN6855/QCA6390/QCA2066, ath12k WCN7850/QCC2072, ath9k_htc, qca),
#     Realtek (rtw88, rtw89, rtlwifi, rtl_bt), MediaTek (MT7921/22/25/27,
#     MT76x0/x2/7601U/7615/7663), Broadcom (brcmfmac and the cypress files it
#     links to, and the two brcmsmac files that the bcma-bus chips of some
#     2010-2012 laptops, such as MacBook Airs, load), Marvell PCIe (Surface).
#   Ethernet: rtl_nic (Realtek r8169).
#   Sound: the laptop speaker amplifiers (cirrus cs35l*, ti/audio tas2781)
#     here, and Intel's SOF DSP firmware from sof-bin below.
#   Microcode: amd-ucode here, intel-ucode below.
# Left out: router and access-point chips (mt7915/16/86/96, ath11k IPQ/QCN,
# ath12k QCN9274/IPQ), Arm SoC and Chromebook firmware, Marvell switch and
# SDIO firmware, server NICs and HBAs, TV tuners, NPUs.
#
# The blobs go to /usr/lib/firmware, never /usr/local/lib/firmware. In Hadron
# (kairos-io/kairos#4290, in v0.6.4) the kernel finds /usr/local/lib/firmware
# only through /usr/lib/firmware/updates, and on an installed system
# COS_PERSISTENT is mounted over /usr/local, which hides every blob the image
# put there. Only the live ISO could load them.
#
# The server edition (DESKTOP=server) takes another set: no GPU, Wi-Fi,
# Bluetooth or audio firmware, and no SOF. It takes the firmware of the
# Ethernet drivers in the kernel instead, from the 1 GbE Realtek and Broadcom
# chips to the 100 GbE Intel (the ice DDP package), Broadcom NetXtreme II,
# QLogic FastLinQ (qed), Chelsio and Netronome cards, and both microcodes.
# Left out: Mellanox Spectrum switch ASICs, Omni-Path, PCMCIA and the storage
# HBAs (qla2xxx, bfa, isci and the old SCSI cards). --require covers this set
# the same way.
#
# tools/install-firmware.sh installs the files the way upstream's
# copy-firmware.sh --zstd does: compressed with zstd (Hadron's kernel has
# CONFIG_FW_LOADER_COMPRESS_ZSTD), with every link WHENCE declares to them.
# Drivers often ask for the link: iwlwifi asks for iwlwifi-<card>.ucode at the
# top of the directory, which links into intel/iwlwifi/. --require checks every
# firmware name the base image's kernel modules declare against the result, so
# a missing link or an API that --newest dropped fails the build.
#
# The microcode stays uncompressed: dracut concatenates it into the early cpio
# of the initramfs (kernel/x86/microcode/), and the kernel loads it only there
# (CONFIG_MICROCODE_LATE_LOADING is off).
# ===========================================================================
FROM ${BASE_IMAGE} AS hadron-kernel

# smartmontools (smartctl, smartd, the drive database and smartd.service) from
# its hadron-layers layer, files under /usr only. smartd stays off: the preset
# in rootfs/common/usr/lib/systemd/system-preset/ disables it, and it is not linked
# into any target. Copied in desktop-default and server-default.
FROM ghcr.io/kairos-io/hadron-layers/smartmontools:${SMARTMONTOOLS_LAYER_VERSION} AS smartmontools

FROM alpine:3 AS firmware
ARG FIRMWARE=false
ARG DESKTOP
# The tarballs are checked against pinned SHA256s, so these are updated by
# hand (scripts/check-renovate-pins.sh). linux-firmware: take both values from
# https://cdn.kernel.org/pub/linux/kernel/firmware/sha256sums.asc.
ARG LINUX_FIRMWARE_VERSION=20261019
ARG LINUX_FIRMWARE_SHA256=a10d1ab5baf2c74ba92e8c7cf313e691696c26bddfa6df00db795e48d70cd4ca
# The newest GSP-RM release the kernel's nouveau declares; checked below.
ARG NOUVEAU_GSP_RM=570.144
# https://github.com/thesofproject/sof-bin/releases
ARG SOF_BIN_VERSION=2026.09.2
ARG SOF_BIN_SHA256=5f7bc30b2b5c1e6b0da55a33fc44e2b7f56ae9b06952edcde614f51ef6b943e1
# https://github.com/intel/Intel-Linux-Processor-Microcode-Data-Files/tags
ARG INTEL_UCODE_VERSION=20261110
ARG INTEL_UCODE_SHA256=989a5cd345808babee491da69ed80ae5741697bc87743a71d0ba611f3fd6afa5
RUN mkdir -p /firmware/usr/lib/firmware && \
    if [ "$FIRMWARE" = "true" ]; then apk add --no-cache curl tar xz zstd coreutils; fi
COPY tools/install-firmware.sh /usr/local/bin/install-firmware
RUN --mount=type=bind,from=hadron-kernel,source=/usr/lib/modules,target=/kmods \
    if [ "$FIRMWARE" = "true" ]; then \
      set -e && cd /tmp && \
      find /kmods -name '*.ko*' | while read -r m; do \
        case "$m" in *.zst) zstd -dcq "$m" ;; *.xz) xz -dc "$m" ;; *) cat "$m" ;; esac | \
          tr '\0' '\n' | sed -n 's/^firmware=//p'; \
      done | sort -u > kernel-firmware.txt && \
      if [ "$DESKTOP" = server ]; then \
      newest="" && \
      patterns="intel/ice/ddp/ e100/ rtl_nic/ tigon/ bnx2/ bnx2x/ qed/ phanfw.bin \
        cxgb3/ cxgb4/ netronome/ myri10ge_ ctfw- ct2fw- tehuti/ \
        3com/typhoon adaptec/starfire kaweth/ \
        amd-ucode/" ; \
      else \
      gsp="$(sed -n 's|^nvidia/tu102/gsp/gsp-\(.*\)\.bin$|\1|p' kernel-firmware.txt | sort -V | tail -n 1)" && \
      if [ "$gsp" != "$NOUVEAU_GSP_RM" ]; then \
        echo "nouveau prefers GSP-RM '$gsp', set NOUVEAU_GSP_RM to it" >&2; exit 1; \
      fi && \
      newest="--newest intel/iwlwifi/=2" && \
      patterns="amdgpu/ radeon/ i915/ xe/ nvidia/*/gsp/*-${NOUVEAU_GSP_RM} \
        intel/iwlwifi/ intel/ibt- \
        ath9k_htc/ ath10k/QCA6174/ ath10k/QCA9377/ ath10k/QCA988X/ \
        ath11k/WCN6855/ ath11k/QCA6390/ ath11k/QCA2066/ \
        ath12k/WCN7850/ ath12k/QCC2072/ qca/ \
        rtw88/ rtw89/ rtlwifi/ rtl_bt/ rtl_nic/ \
        mediatek/WIFI_ mediatek/BT_RAM_CODE_ mediatek/mt7925/ mediatek/mt7927/ mediatek/mt76 \
        brcm/brcmfmac brcm/bcm43xx cypress/ mrvl/pcie \
        cirrus/ ti/audio/ \
        amd-ucode/" ; \
      fi && \
      top="linux-firmware-${LINUX_FIRMWARE_VERSION}" && \
      curl -fL --retry 5 --retry-delay 3 --retry-all-errors \
        https://cdn.kernel.org/pub/linux/kernel/firmware/${top}.tar.xz -o lf.tar.xz && \
      echo "${LINUX_FIRMWARE_SHA256}  lf.tar.xz" | sha256sum -c - && \
      set -f && \
      tar -xJf lf.tar.xz --wildcards "$top/WHENCE" \
        $(for p in $patterns; do printf '%s/%s* ' "$top" "$p"; done) && \
      rm lf.tar.xz && \
      install-firmware $newest --require kernel-firmware.txt \
        "$top" /firmware/usr/lib/firmware $patterns && \
      set +f && \
      rm -rf "$top" kernel-firmware.txt ; \
    fi
COPY tools/install-sof.sh /usr/local/bin/install-sof
RUN if [ "$FIRMWARE" = "true" ] && [ "$DESKTOP" != server ]; then \
      set -e && cd /tmp && \
      curl -fL --retry 5 --retry-delay 3 --retry-all-errors \
        https://github.com/thesofproject/sof-bin/releases/download/v${SOF_BIN_VERSION}/sof-bin-${SOF_BIN_VERSION}.tar.gz \
        -o sof.tar.gz && \
      echo "${SOF_BIN_SHA256}  sof.tar.gz" | sha256sum -c - && \
      tar -xzf sof.tar.gz && rm sof.tar.gz && \
      install-sof "sof-bin-${SOF_BIN_VERSION}" /firmware/usr/lib/firmware && \
      test -e /firmware/usr/lib/firmware/intel/sof-ipc4/mtl/intel-signed/sof-mtl.ri.zst && \
      rm -rf "sof-bin-${SOF_BIN_VERSION}" ; \
    fi
RUN if [ "$FIRMWARE" = "true" ]; then \
      set -e && cd /tmp && \
      curl -fL --retry 5 --retry-delay 3 --retry-all-errors \
        https://github.com/intel/Intel-Linux-Processor-Microcode-Data-Files/archive/refs/tags/microcode-${INTEL_UCODE_VERSION}.tar.gz \
        -o ucode.tar.gz && \
      echo "${INTEL_UCODE_SHA256}  ucode.tar.gz" | sha256sum -c - && \
      tar -xzf ucode.tar.gz && rm ucode.tar.gz && \
      mkdir -p /firmware/usr/lib/firmware/intel-ucode && \
      cp Intel-Linux-Processor-Microcode-Data-Files-microcode-${INTEL_UCODE_VERSION}/intel-ucode/* \
        /firmware/usr/lib/firmware/intel-ucode/ && \
      test "$(ls /firmware/usr/lib/firmware/intel-ucode | wc -l)" -gt 100 && \
      test -s /firmware/usr/lib/firmware/amd-ucode/microcode_amd_fam19h.bin && \
      rm -rf Intel-Linux-Processor-Microcode-Data-Files-microcode-${INTEL_UCODE_VERSION} ; \
    fi

# ===========================================================================
# Final assembly
# ===========================================================================

FROM scratch AS sway-rootfs
COPY --from=wayland /wayland /
COPY --from=libdrm /libdrm /
COPY --from=libucontext /libucontext /
COPY --from=mesa /mesa /
COPY --from=xkeyboard-config /xkeyboard-config /
COPY --from=libxkb /libxkb /
COPY --from=pixman /pixman /
COPY --from=libevdev /libevdev /
COPY --from=libinput /libinput /
COPY --from=hwdata /hwdata /
COPY --from=libdisplay-info /libdisplay-info /
COPY --from=cairo /cairo /
COPY --from=wlroots /wlroots /
COPY --from=pcre2 /pcre2 /
COPY --from=glib2 /glib2 /
COPY --from=freetype /freetype /
COPY --from=harfbuzz /harfbuzz /
COPY --from=fribidi /fribidi /
COPY --from=fontconfig /fontconfig /
COPY --from=pango /pango /
COPY --from=libseat /libseat /
COPY --from=sway /sway /
COPY --from=tllist /tllist /
COPY --from=fcft /fcft /
COPY --from=foot /foot /
COPY --from=libpng /libpng /
COPY --from=grim /grim /
# M2: networking
COPY --from=libnl /libnl /
COPY --from=wpa-supplicant /wpa /
COPY --from=hostapd /hostapd /
COPY --from=libndp /libndp /
COPY --from=networkmanager /networkmanager /
# Company VPN (OpenConnect): its own stages, so networkmanager keeps its key
COPY --from=openconnect /openconnect /
COPY --from=nm-openconnect /nm-openconnect /
COPY --from=vpnc-script /vpnc-script /
COPY --from=wireless-regdb /wireless-regdb /
COPY --from=ncurses /ncurses /
COPY --from=slang /slang /
COPY --from=newt /newt /
# M3: audio
COPY --from=alsa-lib /alsa-lib /
COPY --from=alsa-ucm-conf /alsa-ucm-conf /
COPY --from=pipewire /pipewire /
COPY --from=wireplumber /wireplumber /
# M4: bluetooth, with the codecs (aptX, LDAC, AAC, LC3, Opus)
COPY --from=sbc /sbc /
COPY --from=libfreeaptx /libfreeaptx /
COPY --from=libldac /libldac /
COPY --from=liblc3 /liblc3 /
COPY --from=fdk-aac /fdk-aac /
COPY --from=opus /opus /
COPY --from=pipewire-codecs /pipewire-codecs /
COPY --from=bluez /bluez /
# OBEX and LE Audio: over the bluez build, which stays as the COSMIC prebuilt keys need it
COPY --from=bluez-full /bluez-full /
COPY --from=libical-runtime /libical-runtime /
# M5: desktop polish
COPY --from=swaybg /swaybg /
COPY --from=mako /mako /
COPY --from=fuzzel /fuzzel /
COPY --from=wl-clipboard /wl-clipboard /
COPY --from=cliphist /cliphist /
COPY --from=slurp /slurp /
COPY --from=swayidle /swayidle /
COPY --from=swaylock /swaylock /
COPY --from=wlsunset /wlsunset /
# waybar — status bar + its GTK3/gtkmm/tray runtime stack and Nerd Font glyphs
COPY --from=gtk3 /gtk3 /
COPY --from=gdk-pixbuf /gdk-pixbuf /
COPY --from=atk /atk /
COPY --from=libepoxy /libepoxy /
COPY --from=libsigcpp /libsigcpp /
COPY --from=glibmm /glibmm /
COPY --from=cairomm /cairomm /
COPY --from=pangomm /pangomm /
COPY --from=atkmm /atkmm /
COPY --from=gtkmm3 /gtkmm3 /
COPY --from=gtk-layer-shell /gtk-layer-shell /
COPY --from=jsoncpp /jsoncpp /
COPY --from=libfmt /libfmt /
COPY --from=spdlog /spdlog /
COPY --from=cpp-date /cpp-date /
COPY --from=libdbusmenu /libdbusmenu /
COPY --from=waybar /waybar /
COPY --from=nerdfont /nerdfont /
COPY --from=isotope-fonts /isotope-fonts /
COPY --from=emoji /emoji /
COPY --from=isotope-wallpapers /isotope-wallpapers /
# Fallback icon theme and the Adwaita cursors (system default via
# /usr/share/icons/default), as on niri.
COPY --from=hicolor-icon-theme /hicolor-icon-theme /
COPY --from=icon-themes /icon-themes /
COPY --from=adwaita-cursors /adwaita-cursors /
# Login manager (ly) + its X protocol libs
COPY --from=libxau /libxau /
COPY --from=libxcb /libxcb /
COPY --from=ly /ly /
# Portals, so Flatpak apps get file dialogs and the dark colour scheme (gtk)
# and screenshots and screen sharing (wlr). sway-portals.conf picks them.
COPY --from=gstreamer /gstreamer /
COPY --from=inih /inih /
COPY --from=xdg-desktop-portal /xdg-desktop-portal /
COPY --from=xdg-desktop-portal-gtk /xdg-desktop-portal-gtk /
COPY --from=xdg-desktop-portal-wlr /xdg-desktop-portal-wlr /
COPY --from=gsettings-desktop-schemas /gsettings-desktop-schemas /
# polkit, which answers isotope-updated's "may this user install updates?"
COPY --from=duktape /duktape /
COPY --from=polkit /polkit /


# XLibre/i3 root. This mirrors the shared networking/audio/container-facing
# runtime from sway-rootfs but swaps every display-specific artifact.
FROM scratch AS i3-rootfs
# The Wayland client and server libraries and libdisplay-info are for the
# graphical installer (cage); the i3 session itself does not use them.
COPY --from=wayland /wayland /
COPY --from=libdisplay-info /libdisplay-info /
COPY --from=libdrm /libdrm /
COPY --from=libucontext /libucontext /
COPY --from=mesa-x11 /mesa-x11 /
COPY --from=xkeyboard-config /xkeyboard-config /
COPY --from=libxkb-x11 /libxkb-x11 /
COPY --from=pixman /pixman /
COPY --from=libevdev /libevdev /
COPY --from=libinput /libinput /
COPY --from=hwdata /hwdata /
COPY --from=cairo-x11 /cairo-x11 /
COPY --from=pcre2 /pcre2 /
COPY --from=glib2 /glib2 /
COPY --from=freetype /freetype /
COPY --from=harfbuzz /harfbuzz /
COPY --from=fribidi /fribidi /
COPY --from=fontconfig /fontconfig /
COPY --from=pango-x11 /pango-x11 /
COPY --from=libpng /libpng /
COPY --from=gdk-pixbuf /gdk-pixbuf /
COPY --from=libseat /libseat /
# XLibre server, input DDX, client libraries, and i3-native desktop tools.
COPY --from=xorgproto /xorgproto /
COPY --from=libxau /libxau /
COPY --from=libxcb /libxcb /
COPY --from=x11-libs /x11-libs /
COPY --from=libepoxy-x11 /libepoxy-x11 /
COPY --from=xlibre /xlibre /
COPY --from=xlibre-input-libinput /xlibre-input-libinput /
COPY --from=i3 /i3 /
COPY --from=x11-desktop-tools /x11-desktop-tools /
COPY --from=isotope-xroot /isotope-xroot /
COPY --from=dunst /dunst /
COPY --from=i3lock /i3lock /
COPY --from=xss-lock /xss-lock /
COPY --from=x11-screenshot /x11-screenshot /
# M2: networking
COPY --from=libnl /libnl /
COPY --from=wpa-supplicant /wpa /
COPY --from=hostapd /hostapd /
COPY --from=libndp /libndp /
COPY --from=networkmanager /networkmanager /
# Company VPN (OpenConnect): its own stages, so networkmanager keeps its key
COPY --from=openconnect /openconnect /
COPY --from=nm-openconnect /nm-openconnect /
COPY --from=vpnc-script /vpnc-script /
COPY --from=wireless-regdb /wireless-regdb /
COPY --from=ncurses /ncurses /
COPY --from=slang /slang /
COPY --from=newt /newt /
# M3: audio
COPY --from=alsa-lib /alsa-lib /
COPY --from=alsa-ucm-conf /alsa-ucm-conf /
COPY --from=pipewire /pipewire /
COPY --from=wireplumber /wireplumber /
# M4: bluetooth, with the codecs (aptX, LDAC, AAC, LC3, Opus)
COPY --from=sbc /sbc /
COPY --from=libfreeaptx /libfreeaptx /
COPY --from=libldac /libldac /
COPY --from=liblc3 /liblc3 /
COPY --from=fdk-aac /fdk-aac /
COPY --from=opus /opus /
COPY --from=pipewire-codecs /pipewire-codecs /
COPY --from=bluez /bluez /
# OBEX and LE Audio: over the bluez build, which stays as the COSMIC prebuilt keys need it
COPY --from=bluez-full /bluez-full /
COPY --from=libical-runtime /libical-runtime /
# Shared visual assets + login manager.
COPY --from=nerdfont /nerdfont /
COPY --from=isotope-fonts /isotope-fonts /
COPY --from=emoji /emoji /
COPY --from=isotope-wallpapers /isotope-wallpapers /
COPY --from=hicolor-icon-theme /hicolor-icon-theme /
COPY --from=icon-themes /icon-themes /
COPY --from=adwaita-cursors /adwaita-cursors /
COPY --from=ly /ly /
# Portals, so Flatpak apps get file dialogs and the dark colour scheme. The GTK
# backend needs a GTK3 with the X11 backend (and its AT-SPI bridge) here.
COPY --from=gstreamer /gstreamer /
COPY --from=at-spi2-core /at-spi2-core /
COPY --from=gtk3-x11 /gtk3-x11 /
COPY --from=xdg-desktop-portal /xdg-desktop-portal /
COPY --from=xdg-desktop-portal-gtk-x11 /xdg-desktop-portal-gtk-x11 /
COPY --from=gsettings-desktop-schemas /gsettings-desktop-schemas /
# polkit, which answers isotope-updated's "may this user install updates?", and
# the update tray's host: snixembed shows its StatusNotifierItem in i3bar,
# which hosts only XEmbed icons. It uses the same GTK3 X11 build as the portal.
COPY --from=duktape /duktape /
COPY --from=polkit /polkit /
COPY --from=libdbusmenu-gtk3-x11 /libdbusmenu-gtk3-x11 /
COPY --from=snixembed /snixembed /


# COSMIC root. Shares the networking/audio/bluetooth runtime with the other
# variants; the desktop itself is the cosmic stage's output plus the services it
# needs (polkit, accountsservice, the portal, greetd, Xwayland).
FROM scratch AS cosmic-rootfs
COPY --from=wayland /wayland /
COPY --from=libdrm /libdrm /
COPY --from=libucontext /libucontext /
COPY --from=mesa /mesa /
COPY --from=xkeyboard-config /xkeyboard-config /
COPY --from=libxkb /libxkb /
COPY --from=pixman /pixman /
COPY --from=libevdev /libevdev /
COPY --from=libinput /libinput /
COPY --from=hwdata /hwdata /
COPY --from=libdisplay-info /libdisplay-info /
COPY --from=libseat /libseat /
COPY --from=pcre2 /pcre2 /
COPY --from=glib2 /glib2 /
COPY --from=freetype /freetype /
COPY --from=harfbuzz /harfbuzz /
COPY --from=fribidi /fribidi /
COPY --from=fontconfig /fontconfig /
COPY --from=libpng /libpng /
COPY --from=gdk-pixbuf /gdk-pixbuf /
COPY --from=libepoxy /libepoxy /
# Xwayland and the X11 libraries it (and xkbcomp) need.
COPY --from=xorgproto /xorgproto /
COPY --from=libxau /libxau /
COPY --from=libxcb /libxcb /
COPY --from=x11-libs /x11-libs /
COPY --from=xwayland /xwayland /
# M2: networking
COPY --from=libnl /libnl /
COPY --from=wpa-supplicant /wpa /
COPY --from=hostapd /hostapd /
COPY --from=libndp /libndp /
COPY --from=networkmanager /networkmanager /
# Company VPN (OpenConnect): its own stages, so networkmanager keeps its key
COPY --from=openconnect /openconnect /
COPY --from=nm-openconnect /nm-openconnect /
COPY --from=vpnc-script /vpnc-script /
COPY --from=wireless-regdb /wireless-regdb /
COPY --from=ncurses /ncurses /
COPY --from=slang /slang /
COPY --from=newt /newt /
# M3: audio
COPY --from=alsa-lib /alsa-lib /
COPY --from=alsa-ucm-conf /alsa-ucm-conf /
COPY --from=pipewire /pipewire /
COPY --from=wireplumber /wireplumber /
COPY --from=libsndfile /libsndfile /
COPY --from=libpulse /libpulse /
# M4: bluetooth, with the codecs (aptX, LDAC, AAC, LC3, Opus)
COPY --from=sbc /sbc /
COPY --from=libfreeaptx /libfreeaptx /
COPY --from=libldac /libldac /
COPY --from=liblc3 /liblc3 /
COPY --from=fdk-aac /fdk-aac /
COPY --from=opus /opus /
COPY --from=pipewire-codecs /pipewire-codecs /
COPY --from=bluez /bluez /
# OBEX and LE Audio: over the bluez build, which stays as the COSMIC prebuilt keys need it
COPY --from=bluez-full /bluez-full /
COPY --from=libical-runtime /libical-runtime /
# COSMIC and the services it talks to.
COPY --from=duktape /duktape /
COPY --from=polkit /polkit /
COPY --from=accountsservice /accountsservice /
COPY --from=dav1d /dav1d /
COPY --from=iso-codes /iso-codes /
COPY --from=gstreamer /gstreamer /
COPY --from=json-glib /json-glib /
COPY --from=xdg-desktop-portal /xdg-desktop-portal /
COPY --from=greetd /greetd /
COPY --from=pop-launcher /pop-launcher /
COPY --from=cosmic /cosmic /
# fuzzel and the font library it links: the picker of the Isotope menu
# (isotope-pick) and the emoji picker.
COPY --from=fcft /fcft /
COPY --from=fuzzel /fuzzel /
# wl-clipboard: wl-copy puts the emoji picker's choice on the clipboard
# (isotope-emoji), and wl-paste --watch feeds cliphist the clipboard history
# (isotope-clipboard). cosmic-comp offers the data-control protocols to every
# client that is not in a sandbox, so wl-paste needs no setting.
COPY --from=wl-clipboard /wl-clipboard /
COPY --from=cliphist /cliphist /
COPY --from=cosmic-fonts /cosmic-fonts /
# gsettings-desktop-schemas, for the GTK window-button override in
# rootfs/cosmic/usr/share/glib-2.0/schemas (and Isotope's shared override).
COPY --from=gsettings-desktop-schemas /gsettings-desktop-schemas /
# xdg-desktop-portal-gtk, as the second Settings backend after COSMIC's: it
# serves org.gnome.desktop.wm.preferences button-layout and
# org.gnome.desktop.interface icon-theme (the two keys xdg-desktop-portal-cosmic
# lacks), so Flatpak GTK apps follow the Isotope look. rootfs/cosmic's
# cosmic-portals.conf lists it after cosmic, so the colour scheme still comes
# from COSMIC. It needs the GTK3 stack, which COSMIC's own apps do not.
COPY --from=cairo /cairo /
COPY --from=pango /pango /
COPY --from=atk /atk /
COPY --from=gtk3 /gtk3 /
# The accessibility bus (at-spi2-core: at-spi-bus-launcher, the registry,
# libatspi), which the screen-reader extension's Orca talks to; COSMIC's
# Super+Alt+S, its Accessibility settings and cosmic-session's Orca launch
# need it too. i3 has it already for its X11 GTK. See
# docs/superpowers/specs/2026-11-24-screen-reader-design.md.
COPY --from=at-spi2-core /at-spi2-core /
COPY --from=xdg-desktop-portal-gtk /xdg-desktop-portal-gtk /
COPY --from=hicolor-icon-theme /hicolor-icon-theme /
COPY --from=icon-themes /icon-themes /
COPY --from=musl-locales /musl-locales /
COPY --from=nerdfont /nerdfont /
COPY --from=isotope-fonts /isotope-fonts /
COPY --from=emoji /emoji /
COPY --from=isotope-wallpapers /isotope-wallpapers /
COPY --from=isotope-wallpapers-day /isotope-wallpapers-day /


FROM scratch AS niri-rootfs
COPY --from=wayland /wayland /
COPY --from=libdrm /libdrm /
COPY --from=libucontext /libucontext /
COPY --from=mesa /mesa /
COPY --from=xkeyboard-config /xkeyboard-config /
COPY --from=libxkb /libxkb /
COPY --from=pixman /pixman /
COPY --from=libevdev /libevdev /
COPY --from=libinput /libinput /
COPY --from=hwdata /hwdata /
COPY --from=libdisplay-info /libdisplay-info /
COPY --from=libseat /libseat /
COPY --from=pcre2 /pcre2 /
COPY --from=glib2 /glib2 /
COPY --from=freetype /freetype /
COPY --from=harfbuzz /harfbuzz /
COPY --from=fribidi /fribidi /
COPY --from=fontconfig /fontconfig /
COPY --from=libpng /libpng /
COPY --from=cairo /cairo /
COPY --from=pango /pango /
COPY --from=gdk-pixbuf /gdk-pixbuf /
COPY --from=atk /atk /
COPY --from=libepoxy /libepoxy /
COPY --from=gtk3 /gtk3 /
# The accessibility bus (at-spi2-core: at-spi-bus-launcher, the registry,
# libatspi), which the screen-reader extension's Orca talks to; COSMIC's
# Super+Alt+S, its Accessibility settings and cosmic-session's Orca launch
# need it too. i3 has it already for its X11 GTK. See
# docs/superpowers/specs/2026-11-24-screen-reader-design.md.
COPY --from=at-spi2-core /at-spi2-core /
# Xwayland (run by xwayland-satellite) and the X11 libraries it needs.
COPY --from=xorgproto /xorgproto /
COPY --from=libxau /libxau /
COPY --from=libxcb /libxcb /
COPY --from=x11-libs /x11-libs /
COPY --from=xwayland /xwayland /
COPY --from=xwayland-satellite /xwayland-satellite /
# M2: networking
COPY --from=libnl /libnl /
COPY --from=wpa-supplicant /wpa /
COPY --from=hostapd /hostapd /
COPY --from=libndp /libndp /
COPY --from=networkmanager /networkmanager /
# Company VPN (OpenConnect): its own stages, so networkmanager keeps its key
COPY --from=openconnect /openconnect /
COPY --from=nm-openconnect /nm-openconnect /
COPY --from=vpnc-script /vpnc-script /
COPY --from=wireless-regdb /wireless-regdb /
COPY --from=ncurses /ncurses /
COPY --from=slang /slang /
COPY --from=newt /newt /
# M3: audio
COPY --from=alsa-lib /alsa-lib /
COPY --from=alsa-ucm-conf /alsa-ucm-conf /
COPY --from=pipewire /pipewire /
COPY --from=wireplumber /wireplumber /
COPY --from=libsndfile /libsndfile /
# M4: bluetooth, with the codecs (aptX, LDAC, AAC, LC3, Opus)
COPY --from=sbc /sbc /
COPY --from=libfreeaptx /libfreeaptx /
COPY --from=libldac /libldac /
COPY --from=liblc3 /liblc3 /
COPY --from=fdk-aac /fdk-aac /
COPY --from=opus /opus /
COPY --from=pipewire-codecs /pipewire-codecs /
COPY --from=bluez /bluez /
# OBEX and LE Audio: over the bluez build, which stays as the COSMIC prebuilt keys need it
COPY --from=bluez-full /bluez-full /
COPY --from=libical-runtime /libical-runtime /
# niri, Noctalia and what Noctalia links.
COPY --from=niri /niri /
COPY --from=duktape /duktape /
COPY --from=polkit /polkit /
COPY --from=libgcrypt /libgcrypt /
COPY --from=libsecret /libsecret /
COPY --from=libsodium /libsodium /
COPY --from=sdbus-cpp /sdbus-cpp /
COPY --from=librsvg /librsvg /
COPY --from=gmp /gmp /
COPY --from=mpfr /mpfr /
COPY --from=libqalculate /libqalculate /
COPY --from=md4c /md4c /
COPY --from=tomlplusplus /tomlplusplus /
COPY --from=libical /libical /
COPY --from=libwebp /libwebp /
COPY --from=brotli /brotli /
COPY --from=highway /highway /
COPY --from=lcms2 /lcms2 /
COPY --from=libjxl /libjxl /
COPY --from=noctalia /noctalia /
# Portals, so Flatpak apps get file dialogs.
COPY --from=json-glib /json-glib /
COPY --from=gstreamer /gstreamer /
COPY --from=xdg-desktop-portal /xdg-desktop-portal /
COPY --from=xdg-desktop-portal-gtk /xdg-desktop-portal-gtk /
COPY --from=gsettings-desktop-schemas /gsettings-desktop-schemas /
# Terminal, clipboard, fonts, icons, login screen.
COPY --from=tllist /tllist /
COPY --from=fcft /fcft /
COPY --from=foot /foot /
# fuzzel: the picker of the Isotope menu (isotope-pick).
COPY --from=fuzzel /fuzzel /
COPY --from=wl-clipboard /wl-clipboard /
COPY --from=cosmic-fonts /cosmic-fonts /
COPY --from=nerdfont /nerdfont /
COPY --from=isotope-fonts /isotope-fonts /
COPY --from=emoji /emoji /
COPY --from=isotope-wallpapers /isotope-wallpapers /
COPY --from=isotope-wallpapers-day /isotope-wallpapers-day /
COPY --from=hicolor-icon-theme /hicolor-icon-theme /
COPY --from=icon-themes /icon-themes /
COPY --from=adwaita-cursors /adwaita-cursors /
COPY --from=ly /ly /


# Static configuration is also selected as a rootfs stage. rootfs/common/ is shared;
# the overlay contains only files for the chosen session, so the i3 image never
# advertises a broken Sway session (and vice versa).
FROM scratch AS common-config
COPY rootfs/common/ /

FROM common-config AS sway-config
COPY rootfs/sway/ /

FROM common-config AS i3-config
COPY rootfs/i3/ /

# The Isotope theme as COSMIC's system defaults: the builder and the computed
# theme, dark and light, written by libcosmic's own cosmic-theme crate from
# theme/cosmic-theme.json, so the numbers are COSMIC's and not ours. The crate
# sits at tools/isotope-cosmic-theme because its tests include_str the JSON as
# ../../theme/cosmic-theme.json. The build fails if the computed accent differs
# from the one in the JSON.
FROM cosmic-build AS cosmic-theme
WORKDIR /src
COPY tools/isotope-cosmic-theme tools/isotope-cosmic-theme
COPY theme/cosmic-theme.json theme/cosmic-theme.json
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked --mount=type=cache,id=cosmic-theme-target,target=/cargo-target,sharing=locked \
    cd tools/isotope-cosmic-theme && export CARGO_TARGET_DIR=/cargo-target && \
    cargo test --locked && \
    cargo run --release --locked -- ../../theme/cosmic-theme.json /cosmic-theme/usr/share/cosmic && \
    test -s /cosmic-theme/usr/share/cosmic/com.system76.CosmicTheme.Dark/v2/accent && \
    test -s /cosmic-theme/usr/share/cosmic/com.system76.CosmicTheme.Light/v2/accent && \
    test "$(cat /cosmic-theme/usr/share/cosmic/com.system76.CosmicTheme.Mode/v1/is_dark)" = true

# Isotope's own COSMIC panel applets: applets/, a Cargo workspace (see
# applets/Cargo.toml and applets/justfile): the Isotope mark and the focused
# app at the left of the top bar (isotope-applet-mark, panel ID
# io.kairos.Isotope.Mark) and the quick settings at the right
# (isotope-applet-quick, io.kairos.Isotope.QuickSettings). Built like a stock
# COSMIC component, through cosmic-build's directory mode, so the libcosmic
# patches apply to them and every installed ELF is ldd-checked. Their
# libcosmic is the rev cosmic-applets pins (test/cosmic-build/
# test-cosmic-build-patches.sh checks that rev is one the patches are kept
# for). It builds its dependency graph itself: in release CI
# the COSMIC halves come prebuilt, and locally the neighbouring stages empty
# the shared cosmic-target cache after each component.
# The unit tests run first, in their own target dir; one of them compares the
# mark's icon with docs/assets/isotope-mark.svg at its path in the repo, so
# that file is copied to the same place relative to applets/.
FROM cosmic-build AS isotope-applets
# renovate: datasource=github-tags depName=pop-os/cosmic-session extractVersion=^epoch-(?<version>\d+(\.\d+)*)$
ARG COSMIC_VERSION=1.11.0
ENV COSMIC_VERSION=${COSMIC_VERSION} COSMIC_OUT=/cosmic
ENV CARGO_TARGET_DIR=/cargo-target
COPY applets /src/applets
COPY docs/assets/isotope-mark.svg /src/docs/assets/isotope-mark.svg
RUN --mount=type=cache,id=cosmic-cargo-registry,target=/usr/local/cargo/registry,sharing=locked --mount=type=cache,id=cosmic-target,target=/cargo-target,sharing=locked \
    (cd /src/applets && CARGO_TARGET_DIR=/cargo-target/applets-test cargo test --locked) && \
    cosmic-build /src/applets && rm -rf /cargo-target/* && \
    test -x /cosmic/usr/bin/isotope-applet-mark && \
    grep -qa window_controls_left /cosmic/usr/bin/isotope-applet-mark && \
    test -f /cosmic/usr/share/applications/io.kairos.Isotope.Mark.desktop && \
    test -f /cosmic/usr/share/icons/hicolor/scalable/apps/io.kairos.Isotope.Mark-symbolic.svg && \
    test -x /cosmic/usr/bin/isotope-applet-quick && \
    grep -qa window_controls_left /cosmic/usr/bin/isotope-applet-quick && \
    test -f /cosmic/usr/share/applications/io.kairos.Isotope.QuickSettings.desktop && \
    test -f /cosmic/usr/share/icons/hicolor/scalable/apps/io.kairos.Isotope.QuickSettings-symbolic.svg

FROM common-config AS cosmic-config
COPY rootfs/cosmic/ /
# Isotope's own applets (the isotope-applets stage), next to COSMIC's.
COPY --from=isotope-applets /cosmic/ /
# The Isotope panel defaults live once, in the Isotope layout (the Layout page
# installs that dir into a user's config); the system defaults are a copy of
# it, so the two cannot drift. No RUN: this stage has no shell. Each COPY
# below is one config dir of the layout (a new one needs a line here);
# test/look/check-look.sh diffs them against the layout.
COPY rootfs/cosmic/usr/share/cosmic-layouts/isotope/com.system76.CosmicPanel /usr/share/cosmic/com.system76.CosmicPanel
COPY rootfs/cosmic/usr/share/cosmic-layouts/isotope/com.system76.CosmicPanel.Panel /usr/share/cosmic/com.system76.CosmicPanel.Panel
COPY rootfs/cosmic/usr/share/cosmic-layouts/isotope/com.system76.CosmicPanel.Dock /usr/share/cosmic/com.system76.CosmicPanel.Dock
COPY --from=cosmic-theme /cosmic-theme/ /
COPY --from=isotope-wallpapers-blur /isotope-wallpapers-blur/ /

FROM common-config AS niri-config
COPY rootfs/niri/ /

# ---------------------------------------------------------------------------
# Server edition (DESKTOP=server): no display server, compositor, login
# manager, Mesa, fonts, Flatpak, audio or desktop session. It is not a
# desktop variant: its image is the server-default stage below, which never
# names a desktop stage, so BuildKit builds none of them. What it keeps from
# the desktop build is the network stack (NetworkManager, nmcli and nmtui), polkit
# for the update service, and the boot, installer and update parts of
# rootfs/common/, taken file by file because most of rootfs/common/ is desktop scripts.
# ---------------------------------------------------------------------------
FROM scratch AS server-rootfs
COPY --from=libucontext /libucontext /
COPY --from=pcre2 /pcre2 /
COPY --from=glib2 /glib2 /
COPY --from=libnl /libnl /
COPY --from=libndp /libndp /
COPY --from=ncurses /ncurses /
COPY --from=slang /slang /
COPY --from=newt /newt /
COPY --from=networkmanager /networkmanager /
COPY --from=duktape /duktape /
COPY --from=polkit /polkit /

FROM scratch AS server-config
# The disk passphrase prompt in the initramfs (isotope-unlock, and the splash
# answering the password agent), the console font and the GRUB theme.
COPY rootfs/common/etc/dracut.conf.d/ /etc/dracut.conf.d/
COPY rootfs/common/usr/lib/dracut/ /usr/lib/dracut/
COPY rootfs/common/etc/vconsole.conf /etc/vconsole.conf
COPY rootfs/common/etc/kairos/ /etc/kairos/
COPY rootfs/common/usr/libexec/isotope-grub-theme /usr/libexec/isotope-grub-theme
COPY rootfs/common/system/oem/08_isotope_rename.yaml rootfs/common/system/oem/09_isotope_grub_theme.yaml rootfs/common/system/oem/10_isotope_grub_env.yaml rootfs/common/system/oem/11_isotope_efi_boot_entry.yaml /system/oem/
# The installer, and system extensions.
COPY rootfs/common/system/oem/90_desktop_installer.yaml rootfs/common/system/oem/93_isotope_extensions.yaml /system/oem/
COPY rootfs/common/etc/isotope/extensions.conf /etc/isotope/extensions.conf
# The update service, its D-Bus activation and its polkit policy.
COPY rootfs/common/usr/lib/systemd/system/isotope-updated.service /usr/lib/systemd/system/isotope-updated.service
# Kernel errors off the boot screen: the command line's loglevel= applied again
# in the real root (isotope-console-level.service).
COPY rootfs/common/usr/lib/systemd/system/isotope-console-level.service /usr/lib/systemd/system/isotope-console-level.service
COPY rootfs/common/usr/libexec/isotope-console-level /usr/libexec/isotope-console-level
# A serial getty only on a port with a UART behind it, and the tss user for the
# TPM udev rules.
COPY rootfs/common/usr/lib/systemd/system/serial-getty@.service.d/ /usr/lib/systemd/system/serial-getty@.service.d/
COPY rootfs/common/usr/libexec/isotope-serial-present /usr/libexec/isotope-serial-present
COPY rootfs/common/usr/lib/sysusers.d/tpm2-tss.conf /usr/lib/sysusers.d/tpm2-tss.conf
COPY rootfs/common/etc/systemd/system/sysinit.target.wants/ /etc/systemd/system/sysinit.target.wants/
COPY rootfs/common/usr/share/dbus-1/ /usr/share/dbus-1/
COPY rootfs/common/usr/share/polkit-1/ /usr/share/polkit-1/
# smartd stays off (smartmontools comes from its layer, copied in server-default).
COPY rootfs/common/usr/lib/systemd/system-preset/80-isotope-smartd.preset /usr/lib/systemd/system-preset/80-isotope-smartd.preset
# Backups: the daily timer of `isotope backup --scheduled`, and the one-shot
# after a whole restore.
COPY rootfs/common/usr/lib/systemd/system/isotope-backup.service rootfs/common/usr/lib/systemd/system/isotope-backup.timer rootfs/common/usr/lib/systemd/system/isotope-restore-finish.service /usr/lib/systemd/system/
# Automatic updates: the daily timer of `isotope update --auto` (it does
# nothing until `isotope update auto on`).
COPY rootfs/common/usr/lib/systemd/system/isotope-autoupdate.service rootfs/common/usr/lib/systemd/system/isotope-autoupdate.timer /usr/lib/systemd/system/
# Firmware: the LVFS metadata refresh only on an installed system, daily.
# The polkit rule (50-isotope-fwupd.rules) comes with the polkit-1 copy above.
COPY rootfs/common/usr/lib/systemd/system/fwupd-refresh.timer.d/ /usr/lib/systemd/system/fwupd-refresh.timer.d/
COPY rootfs/common/usr/lib/systemd/system/fwupd-refresh.service.d/ /usr/lib/systemd/system/fwupd-refresh.service.d/
# Docker: its units, and the boot stage that persists /var/lib/docker and
# starts the daemon on the installed system only.
COPY rootfs/common/usr/lib/systemd/system/docker.service rootfs/common/usr/lib/systemd/system/docker.socket /usr/lib/systemd/system/
COPY rootfs/common/system/oem/92_docker.yaml /system/oem/92_docker.yaml
COPY rootfs/common/system/oem/94_isotope_state.yaml /system/oem/94_isotope_state.yaml
# The firewall: its unit (linked into sysinit.target by the
# sysinit.target.wants copy above), the loader, the base ruleset and the
# default allowed list (SSH).
COPY rootfs/common/usr/lib/systemd/system/isotope-firewall.service /usr/lib/systemd/system/isotope-firewall.service
COPY rootfs/common/usr/libexec/isotope-firewall /usr/libexec/isotope-firewall
COPY rootfs/common/usr/share/isotope/firewall.nft rootfs/common/usr/share/isotope/firewall-default.nft /usr/share/isotope/
COPY rootfs/server/ /

FROM ${DESKTOP}-rootfs AS desktop-rootfs
FROM ${DESKTOP}-config AS desktop-config


# ---------------------------------------------------------------------------
# Installer TUI. A static CGO_ENABLED=0 Go binary, so it runs on musl without
# any runtime linkage. Replaces the POSIX-sh wizard: it collects the same
# answers, writes the same cloud-config, then runs `kairos-agent install` as a
# subprocess and renders a branded progress screen instead of raw log spew.
#
# Must be declared before `default` (the stage that COPYies it in), not merely
# before `kairos`: BuildKit resolves --from against stages defined ABOVE the
# consuming stage.
#
# Deliberately NOT in the Makefile's --no-cache-filter list. That list exists
# because the kairos stage's `dracut -f` bakes the splash branding and every
# other initramfs input behind BuildKit's back, and because it follows the
# moving kairos-init:master tag. Here the cache key is honest: the source COPYs are
# content-addressed, so any source change invalidates the build, and the output
# is COPYied straight into the image rather than swallowed by a later step. Busting
# it every build would re-run `go mod download` (network) for no correctness gain.
# ---------------------------------------------------------------------------
FROM golang:1.25.0-alpine3.22 AS install-ui-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/isotope-install-ui/ cmd/isotope-install-ui/
COPY pkg/installer/ pkg/installer/
COPY pkg/i18n/ pkg/i18n/
# The trailing check is a smoke test: the binary must run and reject an unknown
# flag with exit 2, so a binary that segfaults on startup fails the build here
# rather than on tty1 during an install. Braced so a FAILED `go build` still
# fails the RUN -- written as `... && bin --nonsense; [ $? -eq 2 ]` the RUN's
# status would be the test's alone, and a build that happened to exit 2 would
# "pass" having produced no binary at all.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/isotope-install-ui ./cmd/isotope-install-ui \
    && { /out/isotope-install-ui --nonsense 2>/dev/null; [ $? -eq 2 ]; }


# ---------------------------------------------------------------------------
# The graphical installer of the desktop ISOs: isotope-install-gui
# (cmd/isotope-install-gui, Gio over pkg/installer) inside cage, a kiosk Wayland compositor.
# isotope-install-ui starts it and keeps the text installer as the fallback;
# see docs/superpowers/specs/2026-11-17-graphical-installer-design.md. Only the
# desktop images get it (desktop-default); the server keeps the text installer.
#
# gl is the variant's Mesa: the Wayland one on sway, cosmic and niri, the X11
# one on i3, which mesa-x11 builds with the Wayland platform too for this.
# Both are built against the same libraries, with the same sonames, so the
# build here only needs headers and link-time libraries from one of them.
# ---------------------------------------------------------------------------
FROM scratch AS gl-sway
COPY --from=mesa /mesa /
FROM scratch AS gl-cosmic
COPY --from=mesa /mesa /
FROM scratch AS gl-niri
COPY --from=mesa /mesa /
FROM scratch AS gl-i3
COPY --from=mesa-x11 /mesa-x11 /
# mesa-x11's egl.pc requires the X11 and xcb libraries, so pkg-config needs
# their .pc files (and the linker their libraries) to build against it.
COPY --from=xorgproto /xorgproto /
COPY --from=xcbproto /xcbproto /
COPY --from=libxau /libxau /
COPY --from=libxcb /libxcb /
COPY --from=x11-libs /x11-libs /
FROM scratch AS gl-server
FROM gl-${DESKTOP} AS gl

# isotope-gpu-probe: can Mesa draw on this GPU in hardware? The sessions
# (/usr/libexec/isotope-gpu-env) and the installer run it before a
# compositor starts, and fall back to software rendering when it says no. It
# dlopens libgbm and libEGL, so it builds with libc alone and runs with
# glvnd's EGL (sway, COSMIC, niri) and Mesa's own (i3).
FROM toolchain AS isotope-gpu-probe
COPY tools/isotope-gpu-probe.c /build/isotope-gpu-probe.c
RUN mkdir -p /isotope-gpu-probe/usr/bin && \
    cc -O2 -Wall -Wextra -Werror /build/isotope-gpu-probe.c -o /isotope-gpu-probe/usr/bin/isotope-gpu-probe && \
    ! ldd /isotope-gpu-probe/usr/bin/isotope-gpu-probe | grep 'not found'

# isotope-glibc-loader: the stand-in for glibc's dynamic loader. A program
# built for glibc names /lib64/ld-linux-x86-64.so.2 as its interpreter; the
# kernel runs this file and it hands the program to `isotope glibc run`. It
# must be static with no PT_INTERP and link no library, so it builds against
# Alpine's musl-dev (Hadron's toolchain has no libc.a). Static-pie, so it loads
# at a free address even under a non-PIE program. The test hook is compiled in
# only with -DTESTING, so the production binary must not contain it.
FROM alpine:3.22 AS glibc-loader
RUN apk add --no-cache gcc musl-dev binutils
COPY tools/isotope-glibc-loader.c /build/isotope-glibc-loader.c
RUN mkdir -p /glibc-loader/usr/lib && \
    cc -static-pie -O2 -Wall -Wextra -Werror /build/isotope-glibc-loader.c -o /glibc-loader/usr/lib/ld-linux-x86-64.so.2 && \
    ! readelf -lW /glibc-loader/usr/lib/ld-linux-x86-64.so.2 | grep -q INTERP && \
    ! grep -q ISOTOPE_GLIBC_LOADER_TEST_ROOT /glibc-loader/usr/lib/ld-linux-x86-64.so.2

# isotope-appimage: the binfmt_misc handler for type 2 AppImages
# (rootfs/common/usr/lib/binfmt.d/isotope-appimage.conf). On the host it hands
# the AppImage to `isotope glibc run --appimage`; in a container, where the
# rule applies too, it runs the AppImage's own runtime from a memfd copy
# without the magic. Built like the loader: static-pie against musl, no
# PT_INTERP, and no test hook in this build.
FROM alpine:3.22 AS appimage-handler
RUN apk add --no-cache gcc musl-dev binutils
COPY tools/isotope-appimage.c /build/isotope-appimage.c
RUN mkdir -p /appimage-handler/usr/libexec && \
    cc -static-pie -O2 -Wall -Wextra -Werror /build/isotope-appimage.c -o /appimage-handler/usr/libexec/isotope-appimage && \
    ! readelf -lW /appimage-handler/usr/libexec/isotope-appimage | grep -q INTERP && \
    ! grep -q ISOTOPE_APPIMAGE_TEST_ROOT /appimage-handler/usr/libexec/isotope-appimage

# cage, with wlroots linked in from source (a meson subproject), so it needs no
# libwlroots on cosmic, niri and i3, whose desktops do not use wlroots. cage's
# releases follow wlroots series (0.2.0 is the wlroots 0.18 one); the
# "sway and wlroots" group in renovate.json moves them together.
FROM toolchain AS cage
COPY --from=libucontext /libucontext /
COPY --from=libseat /libseat /
COPY --from=wayland /wayland /
COPY --from=libdrm /libdrm /
COPY --from=libxkb /libxkb /
COPY --from=pixman /pixman /
COPY --from=libinput /libinput /
COPY --from=libevdev /libevdev /
COPY --from=gl / /
COPY --from=hwdata /hwdata /
COPY --from=libdisplay-info /libdisplay-info /
# renovate: datasource=github-tags depName=cage-kiosk/cage extractVersion=^v(?<version>\d+(\.\d+)*)$
ARG CAGE_VERSION=0.2.0
RUN mkdir -p /cage
WORKDIR /build
RUN curl -f --retry 5 --retry-delay 3 --retry-all-errors -L https://github.com/cage-kiosk/cage/archive/refs/tags/v${CAGE_VERSION}.tar.gz -o cage.tar.gz && tar -xf cage.tar.gz && rm cage.tar.gz && mv cage-* cage-src
COPY --from=wlroots-src /build/wlroots-src /build/cage-src/subprojects/wlroots
WORKDIR /build/cage-src
RUN pip3 install meson ninja
RUN meson setup buildDir ${COMMON_MESON_FLAGS} -Dman-pages=disabled \
      -Dwlroots:default_library=static -Dwlroots:examples=false -Dwlroots:werror=false \
      -Dwlroots:xwayland=disabled
RUN DESTDIR=/cage-root ninja -C buildDir install && \
    install -Dm0755 /cage-root/usr/bin/cage /cage/usr/bin/cage && \
    /cage/usr/bin/cage -v

# isotope-install-gui needs cgo (libwayland-client, libEGL, libxkbcommon), so
# it is built here with the Hadron toolchain, not in golang:alpine like the
# static text installer. Go itself is copied from the same golang image. The
# tests run without the screenshots, which need a GPU context; CI runs them
# (.github/workflows/ci.yml).
# Two builds, as Isotope Welcome's below: -tags nox11 for cage (a Wayland
# compositor, on every variant, i3 included) and for the live sessions of
# sway, COSMIC and niri; -tags novulkan with X11 for i3's live X11 session,
# where `isotope-install-gui --window` runs as a window. install-gui-${DESKTOP}
# picks: i3 gets both, the X11 one as isotope-install-gui-x11.
FROM toolchain AS install-gui-build
COPY --from=wayland /wayland /
COPY --from=libxkb /libxkb /
COPY --from=libdrm /libdrm /
COPY --from=gl / /
COPY --from=xorgproto /xorgproto /
COPY --from=xcbproto /xcbproto /
COPY --from=libxau /libxau /
COPY --from=libxcb /libxcb /
COPY --from=x11-libs /x11-libs /
COPY --from=libxkb-x11 /libxkb-x11 /
COPY --from=golang:1.25.0-alpine3.22 /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:$PATH GOTOOLCHAIN=local
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY pkg/installer/ pkg/installer/
COPY pkg/guikit/ pkg/guikit/
COPY pkg/i18n/ pkg/i18n/
COPY cmd/isotope-install-gui/ cmd/isotope-install-gui/
RUN CGO_ENABLED=1 ISOTOPE_GUI_SKIP_SCREENSHOTS=1 go test -tags nox11,novulkan ./cmd/isotope-install-gui && \
    CGO_ENABLED=1 go build -tags nox11,novulkan -trimpath -ldflags="-s -w" -o /out/wayland/isotope-install-gui ./cmd/isotope-install-gui && \
    { /out/wayland/isotope-install-gui --nonsense 2>/dev/null; [ $? -eq 2 ]; } && \
    CGO_ENABLED=1 go build -tags novulkan -trimpath -ldflags="-s -w" -o /out/x11/isotope-install-gui ./cmd/isotope-install-gui && \
    { /out/x11/isotope-install-gui --nonsense 2>/dev/null; [ $? -eq 2 ]; }

FROM scratch AS install-gui-sway
COPY --from=install-gui-build /out/wayland/isotope-install-gui /usr/bin/isotope-install-gui
FROM scratch AS install-gui-cosmic
COPY --from=install-gui-build /out/wayland/isotope-install-gui /usr/bin/isotope-install-gui
FROM scratch AS install-gui-niri
COPY --from=install-gui-build /out/wayland/isotope-install-gui /usr/bin/isotope-install-gui
# i3: cage (the "Install Isotope" boot entry) is Wayland and takes the
# Wayland build; the live X11 session's window takes the X11 one.
# isotope-live-install-run picks by the session (X11: DISPLAY without
# WAYLAND_DISPLAY).
FROM scratch AS install-gui-i3
COPY --from=install-gui-build /out/wayland/isotope-install-gui /usr/bin/isotope-install-gui
COPY --from=install-gui-build /out/x11/isotope-install-gui /usr/bin/isotope-install-gui-x11
# The server has no graphical installer; the stage only keeps
# install-gui-${DESKTOP} resolvable, as welcome-server does.
FROM scratch AS install-gui-server
FROM install-gui-${DESKTOP} AS install-gui

# Isotope Welcome (cmd/isotope-welcome), the graphical first-login flow, built like the
# installer above. Two builds of the same source: Gio's X11 backend links
# libX11, libX11-xcb, libXcursor, libXfixes and libxkbcommon-x11, which sway,
# COSMIC and niri do not ship, so they get the -tags nox11 build, and i3 (which
# has the X11 libraries, and the Wayland ones for the installer) the build
# with X11. Both are built whatever DESKTOP is; welcome-${DESKTOP} below
# picks one, as gl-${DESKTOP} does. The X11 headers and libraries are here
# only to link the second build. CI runs the screenshot tests.
FROM toolchain AS welcome-build
COPY --from=wayland /wayland /
COPY --from=libxkb /libxkb /
COPY --from=libdrm /libdrm /
COPY --from=gl / /
COPY --from=xorgproto /xorgproto /
COPY --from=xcbproto /xcbproto /
COPY --from=libxau /libxau /
COPY --from=libxcb /libxcb /
COPY --from=x11-libs /x11-libs /
COPY --from=libxkb-x11 /libxkb-x11 /
COPY --from=golang:1.25.0-alpine3.22 /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:$PATH GOTOOLCHAIN=local
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
# The live page takes the keyboard list and checks from pkg/installer.
COPY pkg/installer/ pkg/installer/
COPY pkg/guikit/ pkg/guikit/
COPY pkg/i18n/ pkg/i18n/
COPY cmd/isotope-welcome/ cmd/isotope-welcome/
# The tests read the app list and the shortcut sheets from the rootfs trees.
COPY rootfs/common/usr/share/isotope/first-run-apps.list rootfs/common/usr/share/isotope/
COPY rootfs/sway/usr/share/isotope/shortcuts.txt rootfs/sway/usr/share/isotope/
COPY rootfs/i3/usr/share/isotope/shortcuts.txt rootfs/i3/usr/share/isotope/
COPY rootfs/cosmic/usr/share/isotope/shortcuts.txt rootfs/cosmic/usr/share/isotope/
COPY rootfs/niri/usr/share/isotope/shortcuts.txt rootfs/niri/usr/share/isotope/
# ...and niri's config, which must include the live keyboard file.
COPY rootfs/niri/etc/niri/config.kdl rootfs/niri/etc/niri/
RUN CGO_ENABLED=1 ISOTOPE_GUI_SKIP_SCREENSHOTS=1 go test -tags nox11,novulkan ./cmd/isotope-welcome && \
    CGO_ENABLED=1 go build -tags nox11,novulkan -trimpath -ldflags="-s -w" -o /out/wayland/isotope-welcome ./cmd/isotope-welcome && \
    { /out/wayland/isotope-welcome --nonsense 2>/dev/null; [ $? -eq 2 ]; } && \
    CGO_ENABLED=1 go build -tags novulkan -trimpath -ldflags="-s -w" -o /out/x11/isotope-welcome ./cmd/isotope-welcome && \
    { /out/x11/isotope-welcome --nonsense 2>/dev/null; [ $? -eq 2 ]; }

FROM scratch AS welcome-sway
COPY --from=welcome-build /out/wayland/isotope-welcome /usr/bin/isotope-welcome
FROM scratch AS welcome-cosmic
COPY --from=welcome-build /out/wayland/isotope-welcome /usr/bin/isotope-welcome
FROM scratch AS welcome-niri
COPY --from=welcome-build /out/wayland/isotope-welcome /usr/bin/isotope-welcome
FROM scratch AS welcome-i3
COPY --from=welcome-build /out/x11/isotope-welcome /usr/bin/isotope-welcome
# The server has no desktop and takes no welcome; the stage only keeps
# welcome-${DESKTOP} resolvable, as gl-server does.
FROM scratch AS welcome-server
FROM welcome-${DESKTOP} AS welcome

# Isotope System (cmd/isotope-system), the window for the system's jobs (updates,
# desktop, extensions, backup, the disk's keys, network), built like Isotope
# Welcome: the same two builds, system-${DESKTOP} picks one. It imports
# pkg/isorelease, pkg/recoverykey, pkg/firewall, pkg/shares, pkg/fwupd,
# pkg/flatpak and pkg/remotedesktop, shared with the isotope CLI.
FROM welcome-build AS system-build
COPY pkg/isorelease/ pkg/isorelease/
COPY pkg/recoverykey/ pkg/recoverykey/
COPY pkg/firewall/ pkg/firewall/
COPY pkg/shares/ pkg/shares/
COPY pkg/printing/ pkg/printing/
COPY pkg/fwupd/ pkg/fwupd/
COPY pkg/flatpak/ pkg/flatpak/
COPY pkg/remotedesktop/ pkg/remotedesktop/
COPY cmd/isotope-system/ cmd/isotope-system/
RUN CGO_ENABLED=1 ISOTOPE_GUI_SKIP_SCREENSHOTS=1 go test -tags nox11,novulkan ./cmd/isotope-system && \
    CGO_ENABLED=1 go build -tags nox11,novulkan -trimpath -ldflags="-s -w" -o /out/wayland/isotope-system ./cmd/isotope-system && \
    { /out/wayland/isotope-system --nonsense 2>/dev/null; [ $? -eq 2 ]; } && \
    CGO_ENABLED=1 go build -tags novulkan -trimpath -ldflags="-s -w" -o /out/x11/isotope-system ./cmd/isotope-system && \
    { /out/x11/isotope-system --nonsense 2>/dev/null; [ $? -eq 2 ]; }

FROM scratch AS system-sway
COPY --from=system-build /out/wayland/isotope-system /usr/bin/isotope-system
FROM scratch AS system-cosmic
COPY --from=system-build /out/wayland/isotope-system /usr/bin/isotope-system
FROM scratch AS system-niri
COPY --from=system-build /out/wayland/isotope-system /usr/bin/isotope-system
FROM scratch AS system-i3
COPY --from=system-build /out/x11/isotope-system /usr/bin/isotope-system
# The server has no desktop and takes no system app.
FROM scratch AS system-server
FROM system-${DESKTOP} AS system


# ---------------------------------------------------------------------------
# isotope-update: release checker, desktop notifier, `kairos-agent upgrade`
# wrapper and the root update service on the system bus (isotope-updated). Static
# (CGO_ENABLED=0), like the installer TUI, and for the same reasons kept out of
# the --no-cache-filter list. Not stdlib-only any more: the D-Bus service needs
# godbus, downloaded in its own layer so a source change does not refetch it.
# The unit tests run here so a broken updater fails the image build, not a
# user's upgrade; the ones that need a dbus-daemon skip, as this stage has none.
# ---------------------------------------------------------------------------
FROM golang:1.25.0-alpine3.22 AS update-build
# The glibc tests run the program as the box does, through bash -c 'exec -a ...'.
RUN apk add --no-cache bash
# The tree sits at /src as in the repo, because the locale tests read the
# installer's golden files at ../../pkg/installer/testdata: both writers of
# /oem/91_isotope_locale.yaml are held to the same bytes.
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY pkg/installer/testdata/oem-locale-de_DE.golden pkg/installer/testdata/locale.conf-de_DE.golden pkg/installer/testdata/
# TestFirewallDefaultsMatchTheImage holds pkg/firewall's defaults to the shipped file.
COPY rootfs/common/usr/share/isotope/firewall-default.nft rootfs/common/usr/share/isotope/
# TestShippedCatalogsConfIsTheDefault reads the shipped catalog list.
COPY rootfs/common/etc/isotope/extensions.conf rootfs/common/etc/isotope/
# TestReadPicks reads Isotope Welcome's app list, the Apps page's picks.
COPY rootfs/common/usr/share/isotope/first-run-apps.list rootfs/common/usr/share/isotope/
COPY pkg/isorelease/ pkg/isorelease/
COPY pkg/recoverykey/ pkg/recoverykey/
COPY pkg/firewall/ pkg/firewall/
COPY pkg/shares/ pkg/shares/
COPY pkg/printing/ pkg/printing/
COPY pkg/fwupd/ pkg/fwupd/
COPY pkg/flatpak/ pkg/flatpak/
COPY pkg/remotedesktop/ pkg/remotedesktop/
COPY pkg/i18n/ pkg/i18n/
COPY cmd/isotope/ cmd/isotope/
RUN CGO_ENABLED=0 go test ./cmd/isotope/... ./pkg/isorelease/... ./pkg/recoverykey/... ./pkg/shares/... ./pkg/printing/... ./pkg/fwupd/... ./pkg/flatpak/... ./pkg/remotedesktop/... \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/isotope ./cmd/isotope \
    && { /out/isotope --nonsense >/dev/null 2>&1; [ $? -eq 2 ]; }


FROM ${BASE_IMAGE} AS desktop-default
ARG BASE_IMAGE
# The firewall (isotope-firewall.service) needs nft from the base. Hadron
# v0.6.7 and hadron-trusted:v0.6.7 have none; stop here, before the long
# build, rather than ship a unit that fails at every boot.
RUN test -x /usr/sbin/nft || { echo "${BASE_IMAGE} has no /usr/sbin/nft: the firewall needs a Hadron base with nftables (kairos-io/hadron#893)" >&2; exit 1; }
# Built desktop stack
COPY --from=desktop-rootfs / /
# Intel's VA-API driver and libigdgmm (GPU=full; empty on vm). Copied here, not
# into /mesa, so the Mesa stages and the COSMIC builds that reach them keep
# their cache keys; all four desktops (and the agent, built on the i3 image)
# take it from this one place.
COPY --from=intel-media-driver /intel-media-driver /
# QEMU guest agent (GPU=vm; empty on full). Not enabled: the udev rule starts it.
COPY --from=guest-tools / /
# Runtime libraries provided by the toolchain but not the base image
COPY --from=toolchain /usr/lib/libffi.so* /usr/lib/
COPY --from=toolchain /usr/lib/libexpat.so* /usr/lib/
COPY --from=toolchain /usr/lib/libjson-c.so* /usr/lib/
COPY --from=toolchain /usr/lib/libstdc++.so.6* /usr/lib/
COPY --from=toolchain /usr/lib/libgcc_s.so* /usr/lib/
COPY --from=toolchain /usr/lib/libreadline.so* /usr/lib/
# libxml2 — pulled by libxkbregistry (waybar links it; sway uses libxkbcommon
# and never loads it, which is why this only surfaces with waybar present).
COPY --from=toolchain /usr/lib/libxml2.so* /usr/lib/
# Flatpak + its from-source stack (libcurl/seccomp/zstd/lzma/cap/mount/crypto
# already ship in the base image; glib2/gdk-pixbuf/libxml2 are pulled in above
# by the waybar stack).
COPY --from=libgpg-error /libgpg-error /
COPY --from=libassuan /libassuan /
COPY --from=gpgme /gpgme /
COPY --from=signed-flatpak-runtime /signed-flatpak-runtime /
COPY --from=libyaml /libyaml /
COPY --from=libxmlb /libxmlb /
COPY --from=appstream /appstream /
COPY --from=libarchive /libarchive /
COPY --from=json-glib /json-glib /
COPY --from=libfuse3 /libfuse3 /
# Only libe2p (ostree's libe2p dep) — NOT the rest of e2fsprogs, which would
# clobber the base image's libext2fs and break the installer's own mkfs.ext4.
COPY --from=e2fsprogs /e2fsprogs/usr/lib/libe2p.so* /usr/lib/
COPY --from=bubblewrap /bubblewrap /
COPY --from=xdg-dbus-proxy /xdg-dbus-proxy /
COPY --from=ostree /ostree /
COPY --from=flatpak /flatpak /
# The Secret Service (gnome-keyring, with gcr 3 and p11-kit), on every variant.
COPY --from=libgcrypt /libgcrypt /
COPY --from=gnome-keyring /gnome-keyring /
# Battery and power profiles on the system bus (UPower, power-profiles-daemon),
# on every variant: COSMIC's battery applet and Noctalia read the battery only
# from UPower. polkit, which both ask, comes with every variant's rootfs.
COPY --from=libgudev /libgudev /
COPY --from=upower /upower /
COPY --from=power-profiles-daemon /power-profiles-daemon /
# Removable disks (udisks2 with libblockdev): mounts a USB or NVMe disk for the
# unprivileged session through D-Bus and polkit. libblockdev's mdraid plugin needs
# libbytesize, which needs GMP and MPFR; its crypto plugin needs libcryptsetup and
# keyutils, its nvme plugin libnvme. libext2fs, libdevmapper, libfdisk and libblkid
# are in the base image, libe2p, glib2 and pcre2 are copied above.
COPY --from=gmp /gmp /
COPY --from=mpfr /mpfr /
COPY --from=libbytesize /libbytesize /
# libcryptsetup and libkeyutils are not copied: Hadron already ships them and
# dracut unlocks COS_PERSISTENT_LUKS with them at boot, so libbd_crypto links
# the base image's copies (the cryptsetup and keyutils stages are build-time
# headers only; CRYPTSETUP_VERSION must follow Hadron's).
COPY --from=libnvme /libnvme /
COPY --from=libblockdev /libblockdev /
COPY --from=udisks2 /udisks2 /
# exFAT for the Disks page's Format (libblockdev runs mkfs.exfat).
COPY --from=exfatprogs /exfatprogs/usr/sbin/ /usr/bin/
# Firmware updates (fwupd, fwupdmgr, the LVFS remote, fwupdx64.efi). fwupd
# finds the ESP through udisks2, above, for UEFI capsules.
COPY --from=fwupd-runtime /fwupd-runtime /
# XDG MIME database — required for GLib content-type detection, which appstream/
# libxmlb rely on to decompress the gzipped Flathub catalog (fixes flatpak search).
COPY --from=shared-mime-info /shared-mime-info /
# Docker (static bundle): dockerd/docker/containerd/runc + helpers -> /usr/bin.
COPY --from=docker /docker /
# Distrobox (POSIX shell) — mutable dev containers on docker.
COPY --from=distrobox /distrobox /
# M6: optional real-hardware firmware (empty unless FIRMWARE=true)
COPY --from=firmware /firmware /
# Disk health: smartctl and smartd (off) from the hadron-layers layer.
COPY --from=smartmontools / /
# Printing: CUPS, cups-filters, cups-browsed, Ghostscript, Avahi and ipp-usb
# (the printing stage). Before the session config, whose /etc/cups files
# replace the upstream ones. Avahi keeps upstream's avahi-daemon.conf.
COPY --from=printing / /
# Shared + selected session config / launch layer
COPY --from=desktop-config / /
# The boot splash, at the path kairos-init's units and dracut module exec. It
# must be in place before the kairos stage runs kairos-init, which then keeps it
# instead of linking the stock `kairos splash` there.
COPY --from=isotope-splash /build/splash/isotope-splash /usr/bin/kairos-splash
# bootvine, which the splash hands the screen to on D (52isotope-bootvine).
COPY --from=bootvine /bootvine/ /

# System setup. NOTE: no user is created here — the desktop user is defined at
# install time via a Kairos cloud-config (see cloud-config.yaml) and lives on
# the persistent /home. We only ensure the groups it will join exist, enable
# the system services, and configure the ly display manager on tty1.
RUN chmod 1777 /tmp; \
    ldconfig 2>/dev/null || true; \
    # Register the bundled fonts for the selected bar, terminal, and launcher.
    fc-cache -f 2>/dev/null || true; \
    # Compile the GSettings schemas the variant ships (GTK's, and
    # gsettings-desktop-schemas with Isotope's dark override; COSMIC adds its
    # window-button override from rootfs/cosmic): the
    # stages install them under DESTDIR, where meson skips this, and GLib
    # aborts a GTK app that opens a file chooser with no compiled schemas. On
    # i3 the AT-SPI bus launcher, which GTK starts for snixembed, aborts too.
    if [ -d /usr/share/glib-2.0/schemas ]; then glib-compile-schemas /usr/share/glib-2.0/schemas || exit 1; fi; \
    for g in audio video render input bluetooth seat docker; do groupadd -f "$g"; done; \
    mkdir -p /var/lib/docker /var/lib/xkb; \
    # The selected session launcher lives in /usr/bin (NOT /usr/local): Kairos mounts /usr/local
    # from the persistent partition on the installed system, which shadows
    # anything baked into the image there — ly would exec a missing launcher and
    # bounce straight back to the login screen. The installer TUI lives in
    # /usr/bin for the same reason (it is COPYied in after this block, already
    # executable, so the glob below deliberately does not have to reach it).
    chmod +x /usr/bin/isotope-*; \
    if [ -e /usr/bin/start-desktop ]; then chmod +x /usr/bin/start-desktop; fi; \
    if [ -e /usr/lib/systemd/system/ly@.service ]; then \
    # ly runs on tty1 via the ly@tty1 instance of the ly@.service template (ly
    # 1.3.x dropped the plain ly.service and the config `tty` option — the tty is
    # the systemd instance). It authenticates the cloud-config user and launches
    # the selected Sway or i3 session via its desktop entry.
    # Isotope colours for ly, in eight-colour mode. The Linux console has 16
    # colours: it maps ly's 24-bit values to the nearest of them by hue, so
    # the accent came out cyan and the navy background black. Instead ly asks
    # for the console's own colours, which the kernel command line sets to the
    # Isotope palette (vt.default_* in grubmenu.cfg): the default background is
    # colour 0 (base), bright white is 15 (text), bright yellow is 11 (brand:
    # the login box is a brand moment, like the splash), red is 1 (error).
    # Not bright blue: that is the terminal's own blue. The values are
    # 0xSSRRGGBB: styling 0x40 is TB_BRIGHT, 0x01 is bold, and the low byte is
    # a termbox colour (2 red, 4 yellow, 8 white). No animation: decision 8 dropped the "Black Hole" one, whose
    # licence is not stated.
    sed -i \
      -e 's/^animation = .*/animation = none/' \
      -e 's/^full_color = .*/full_color = false/' \
      -e 's/^bg = .*/bg = 0x00000000/' \
      -e 's/^fg = .*/fg = 0x40000008/' \
      -e 's/^border_fg = .*/border_fg = 0x40000004/' \
      -e 's/^error_fg = .*/error_fg = 0x01000002/' \
      -e 's/^error_bg = .*/error_bg = 0x00000000/' \
      -e 's/^box_title = .*/box_title = Isotope/' \
      -e 's/^clock = .*/clock = %H:%M/' \
      /etc/ly/config.ini 2>/dev/null || true; \
    # XLibre's libseat integration is initialized before its later automatic
    # controlling-TTY detection. Rootless X sessions therefore must pass
    # -keeptty explicitly; otherwise libseat is disabled and XLibre falls back
    # to opening /dev/ttyN itself, which fails for the authenticated user.
    if [ -x /usr/bin/Xorg ]; then \
      sed -i 's|^x_cmd = .*|x_cmd = /usr/bin/X -keeptty|' /etc/ly/config.ini; \
    fi; \
    # ly's unit ships only `Alias=display-manager.service` (no WantedBy=), so a
    # plain `systemctl enable` never pulls it into a target. And Kairos forces
    # `systemctl set-default multi-user.target` at boot, so graphical.target
    # (which Wants=display-manager.service) is never reached. Net result: ly never
    # starts and boot stops at a login-less multi-user state. Wire ly straight
    # into multi-user.target — it's a VT TUI with no graphical prerequisites.
    systemctl enable ly@tty1.service 2>/dev/null || true; \
    mkdir -p /etc/systemd/system/multi-user.target.wants; \
    ln -sf /usr/lib/systemd/system/ly@.service /etc/systemd/system/multi-user.target.wants/ly@tty1.service; \
    else \
    # COSMIC: greetd runs cosmic-greeter on tty1 instead of ly (rootfs/cosmic/).
    # The greeter runs as its own system user, which needs the seat groups for
    # its compositor.
      useradd -r -M -d /var/lib/cosmic-greeter -s /usr/sbin/nologin -G video,render,input,seat cosmic-greeter; \
      mkdir -p /var/lib/cosmic-greeter && chown cosmic-greeter: /var/lib/cosmic-greeter; \
      # cosmic-osd (the password prompt) execs the polkit helper at Debian's
      # path, compiled in; exec follows the link to the setuid target.
      mkdir -p /usr/libexec && ln -sf /usr/lib/polkit-1/polkit-agent-helper-1 /usr/libexec/polkit-agent-helper-1; \
      # COSMIC's play/pause, next and previous keys run playerctl, which Hadron
      # does not ship; isotope-media-key does the same over MPRIS with busctl.
      # The build fails if upstream renames these actions, so the keys cannot
      # silently stop working.
      f=/usr/share/cosmic/com.system76.CosmicSettings.Shortcuts/v1/system_actions; \
      sed -i -E 's/"playerctl (play-pause|next|previous)"/"isotope-media-key \1"/' "$f" || exit 1; \
      grep -q '"isotope-media-key play-pause"' "$f" && ! grep -q playerctl "$f" || exit 1; \
      # Super+Escape opens the Isotope menu, as in every variant. COSMIC binds
      # it to the screen lock, which moves to Super+Alt+L, the lock key of the
      # other variants (Super+Alt+Right still moves to the output on the right).
      # The build fails if upstream changes either line.
      # Super+; opens the emoji picker, as in every variant. COSMIC binds
      # nothing there; the binding is added on the line after the menu's,
      # and the build fails if Super+; is bound twice.
      f=/usr/share/cosmic/com.system76.CosmicSettings.Shortcuts/v1/defaults; \
      # Super+Ctrl+V opens the clipboard history, as in every variant. COSMIC
      # binds nothing to it; the build fails if upstream starts to.
      ! grep -qF '(modifiers: [Super, Ctrl], key: "v")' "$f" || exit 1; \
      sed -i \
        -e 's/(modifiers: \[Super\], key: "Escape"): System(LockScreen),/(modifiers: [Super], key: "Escape"): Spawn("isotope-menu"),\n    (modifiers: [Super], key: "semicolon"): Spawn("isotope-emoji"),/' \
        -e 's/(modifiers: \[Super, Alt\], key: "l"): SwitchOutput(Right),/(modifiers: [Super, Alt], key: "l"): System(LockScreen),/' \
        -e '/(modifiers: \[Super\], key: "Escape"): Spawn("isotope-menu"),/a\    (modifiers: [Super, Ctrl], key: "v"): Spawn("isotope-clipboard"),' \
        "$f" || exit 1; \
      grep -qF '(modifiers: [Super], key: "Escape"): Spawn("isotope-menu"),' "$f" || exit 1; \
      grep -qF '(modifiers: [Super, Alt], key: "l"): System(LockScreen),' "$f" || exit 1; \
      # Super+Alt+BackSpace turns do not disturb on and off, as in the other
      # variants (isotope-notify-key). COSMIC has no command to close a
      # notification, so the other notification keys are not bound here.
      sed -i 's/\((modifiers: \[Super\], key: "Escape"): Spawn("isotope-menu"),\)/\1\n    (modifiers: [Super, Alt], key: "BackSpace"): Spawn("isotope-notify-key dnd"),/' "$f" || exit 1; \
      grep -qF '(modifiers: [Super, Alt], key: "BackSpace"): Spawn("isotope-notify-key dnd"),' "$f" || exit 1; \
      [ "$(grep -cF '(modifiers: [Super], key: "semicolon")' "$f")" -eq 1 ] || exit 1; \
      grep -qF '(modifiers: [Super], key: "semicolon"): Spawn("isotope-emoji"),' "$f" || exit 1; \
      [ "$(grep -cF '(modifiers: [Super, Ctrl], key: "v"): Spawn("isotope-clipboard"),' "$f")" -eq 1 ] || exit 1; \
      mkdir -p /etc/systemd/system/multi-user.target.wants; \
      for u in cosmic-greeter cosmic-greeter-daemon accounts-daemon; do \
        ln -sf /usr/lib/systemd/system/$u.service /etc/systemd/system/multi-user.target.wants/$u.service; \
      done; \
    fi; \
    # polkit (Sway, i3, COSMIC and niri): polkitd runs the daemon, which D-Bus
    # starts on the first request (polkit.service needs no enable link). The agent helper is
    # what checks a password for the prompt, so it must be setuid root, and the
    # rules directories must be readable by polkitd only.
    if [ -d /usr/lib/polkit-1 ]; then \
      useradd -r -M -d / -s /usr/sbin/nologin polkitd; \
      chmod 4755 /usr/lib/polkit-1/polkit-agent-helper-1; \
      for d in /etc/polkit-1/rules.d /usr/share/polkit-1/rules.d; do \
        mkdir -p "$d" && chown polkitd:root "$d" && chmod 700 "$d"; \
      done; \
    fi; \
    # pam_unix checks a password through this helper when the caller is not
    # root, which is how every screen locker unlocks (Noctalia's, COSMIC's).
    # Without setuid it cannot read /etc/shadow and every unlock fails with
    # "cannot retrieve authentication info".
    if [ -e /usr/sbin/unix_chkpwd ]; then chmod 4755 /usr/sbin/unix_chkpwd; fi; \
    # getty@tty1 stays enabled: ly@tty1 and cosmic-greeter declare
    # Conflicts=getty@tty1, so the login screen replaces it at boot, and the
    # installer's boot masks it in /run (Kairos' 52_installer.yaml). A static
    # mask here left tty1 with no login at all when the login screen does not
    # run, and broke Kairos' own `systemctl enable getty@tty1` at every boot.
    # NetworkManager is the network manager. systemd-networkd is disabled in
    # the kairos stage, after kairos-init, which enables it.
    systemctl enable NetworkManager.service wpa_supplicant.service 2>/dev/null || true; \
    test -L /etc/systemd/system/network-online.target.wants/NetworkManager-wait-online.service || exit 1; \
    # M3: PipeWire + WirePlumber as per-user services
    systemctl --global enable pipewire.socket pipewire-pulse.socket wireplumber.service 2>/dev/null || true; \
    # The keyring's control socket, so pam_gnome_keyring (ly, greetd) unlocks
    # the login keyring in a daemon the user's systemd owns.
    systemctl --global enable gnome-keyring-daemon.socket; \
    test -L /etc/systemd/user/sockets.target.wants/gnome-keyring-daemon.socket || exit 1; \
    # M4: Bluetooth daemon. `enable` only links it into bluetooth.target.wants,
    # but Kairos forces multi-user.target (bluetooth.target is never reached), so
    # like ly it must be pulled into multi-user.target directly or bluetoothd
    # never starts and bluetoothctl hangs on "waiting for bluetoothd".
    systemctl enable bluetooth.service 2>/dev/null || true; \
    ln -sf /usr/lib/systemd/system/bluetooth.service /etc/systemd/system/multi-user.target.wants/bluetooth.service; \
    # UPower and power-profiles-daemon. Both are D-Bus activated too, but their
    # units say WantedBy=graphical.target, which Kairos never reaches, so like
    # bluetooth they are pulled into multi-user.target and run from boot.
    systemctl enable upower.service power-profiles-daemon.service 2>/dev/null || true; \
    for u in upower power-profiles-daemon; do \
      ln -sf /usr/lib/systemd/system/$u.service /etc/systemd/system/multi-user.target.wants/$u.service; \
    done; \
    # udisks2 mounts removable disks for the session. Same reason: its unit says
    # WantedBy=graphical.target.
    systemctl enable udisks2.service 2>/dev/null || true; \
    ln -sf /usr/lib/systemd/system/udisks2.service /etc/systemd/system/multi-user.target.wants/udisks2.service; \
    # Docker must stay disabled in the baked filesystem: the live installer's
    # /var is itself an overlayfs, so starting dockerd there would make overlay2
    # repeatedly fail its nested-overlay check. The guarded 92_docker.yaml boot
    # stage enables and starts Docker only on the installed system, after Kairos
    # has bind-mounted persistent /var/lib/docker.
    systemctl disable docker.socket docker.service 2>/dev/null || true; \
    rm -f /etc/systemd/system/sockets.target.wants/docker.socket \
      /etc/systemd/system/multi-user.target.wants/docker.service; \
    # Flatpak's fusermount helpers must be setuid-root (revokefs-fuse + the
    # document portal). Docker is rootful, so no newuidmap/newgidmap setuid here.
    for h in /usr/bin/fusermount3 /usr/bin/fusermount; do \
        [ -e "$h" ] && chmod u+s "$h" || true; \
    done; \
    # Per-user setup (Flathub remote) runs late on the booted system via a
    # systemd oneshot. Like ly/bluetooth it must be pulled into
    # multi-user.target directly (Kairos forces that target).
    chmod +x /usr/bin/isotope-user-setup; \
    systemctl enable isotope-user-setup.service 2>/dev/null || true; \
    ln -sf /usr/lib/systemd/system/isotope-user-setup.service /etc/systemd/system/multi-user.target.wants/isotope-user-setup.service

# Installer TUI. /usr/bin, not /usr/local/bin: /usr/local is shadowed by an empty
# overlay at runtime on Kairos, so a binary there is invisible to the installed
# system. (The shell installer it replaces lived in /usr/local/bin and only
# worked because the installer runs in the live environment.)
COPY --from=install-ui-build /out/isotope-install-ui /usr/bin/isotope-install-ui
# The graphical installer: isotope-install-ui runs it inside cage when both are
# here. The server image has neither and keeps the text installer. The
# variant's build (install-gui-${DESKTOP}); i3 also has isotope-install-gui-x11.
COPY --from=install-gui / /
COPY --from=cage /cage/usr/bin/cage /usr/bin/cage
# The GPU probe, for the sessions' and the installer's software fallback
# (rootfs/common/usr/libexec/isotope-gpu-env).
COPY --from=isotope-gpu-probe /isotope-gpu-probe /
# Programs built for glibc. The stand-in loader (tools/isotope-glibc-loader.c)
# at /usr/lib/ld-linux-x86-64.so.2, which is also /lib64's (a link to usr/lib
# on Hadron), hands such a program to `isotope glibc run`; glibc.conf names the
# container's image. AppImages reach /usr/libexec/isotope-appimage
# (tools/isotope-appimage.c, the appimage-handler stage) through the binfmt
# rule in rootfs/common/usr/lib/binfmt.d/isotope-appimage.conf, which
# systemd-binfmt.service registers at boot. Hadron's systemd already links that
# unit and proc-sys-fs-binfmt_misc.automount into sysinit.target (statically,
# under /usr/lib), and binfmt_misc is a module the automount loads; stop here
# if a base ever drops one of them. Not on the server (server-default copies
# none of this) and not on the agent (Dockerfile.agent removes it).
# The help page ships too, so `isotope glibc --help` can name it offline.
# test/glibc/check-glibc.sh checks the image.
COPY --from=glibc-loader /glibc-loader /
COPY --from=appimage-handler /appimage-handler /
COPY docs/glibc-programs.md /usr/share/doc/isotope/glibc-programs.md
ARG GLIBC_BOX_IMAGE
RUN printf 'image=%s\n' "${GLIBC_BOX_IMAGE}" > /usr/share/isotope/glibc.conf && \
    test "$(readlink /lib64)" = usr/lib && \
    test -L /usr/lib/systemd/system/sysinit.target.wants/systemd-binfmt.service && \
    test -L /usr/lib/systemd/system/sysinit.target.wants/proc-sys-fs-binfmt_misc.automount && \
    { ls /usr/lib/modules/*/kernel/fs/binfmt_misc.ko* >/dev/null 2>&1 || \
      grep -qs '/binfmt_misc.ko$' /usr/lib/modules/*/modules.builtin; } && \
    test -f /usr/lib/binfmt.d/isotope-appimage.conf && \
    test -x /usr/libexec/isotope-appimage && test -f /usr/share/doc/isotope/glibc-programs.md || \
    { echo "${BASE_IMAGE}: the glibc loader's /lib64 link, systemd-binfmt.service, the binfmt_misc automount or module, or the AppImage rule is missing" >&2; exit 1; }
# Isotope Welcome, the variant's build (welcome-${DESKTOP}). The sessions
# start `isotope-welcome --login`; Dockerfile.agent removes it.
COPY --from=welcome / /
# Isotope System, the variant's build (system-${DESKTOP}). The tray and
# the Isotope menu open its pages; Dockerfile.agent removes it.
COPY --from=system / /
# libX11's Compose tables, data only. libxkbcommon reads them for the locale,
# and Gio stops ("xkb_compose_table_new_from_locale failed") without them;
# they also make dead keys work in the GUI. Only i3 ships libX11 itself.
COPY --from=x11-libs /x11-libs/usr/share/X11/locale /usr/share/X11/locale
# The isotope command: updates, system extensions, the disk's recovery key,
# backups. /usr/bin for the same /usr/local reason. isotope-update,
# isotope-ext and isotope-recovery are its older names, kept as links: run as
# isotope-ext it is `isotope ext`, as isotope-recovery `isotope recovery`.
COPY --from=update-build /out/isotope /usr/bin/isotope
RUN ln -sf isotope /usr/bin/isotope-update
RUN ln -sf isotope /usr/bin/isotope-ext
RUN ln -sf isotope /usr/bin/isotope-recovery
# Backups: restic, the daily timer that runs `isotope backup --scheduled`
# (it does nothing until `isotope backup schedule` turns it on), and the
# one-shot that installs the apps and extensions after a whole restore.
COPY --from=restic /restic/ /
RUN systemctl enable isotope-backup.timer isotope-restore-finish.service && \
    test -L /etc/systemd/system/timers.target.wants/isotope-backup.timer && \
    test -L /etc/systemd/system/multi-user.target.wants/isotope-restore-finish.service
# Automatic updates: the daily timer of `isotope update --auto`, which does
# nothing until `isotope update auto on` turns them on.
RUN systemctl enable isotope-autoupdate.timer && \
    test -L /etc/systemd/system/timers.target.wants/isotope-autoupdate.timer
# Printing's users and services. lp runs CUPS's filters and backends, avahi
# runs avahi-daemon; the base image has the lp group but no lp user. The
# printing stage was built where lp is another gid, so CUPS's directories get
# the image's lp group. cups.service and cups-browsed.service are linked into
# multi-user.target (cups-browsed keeps a connection to cupsd, so cupsd runs
# from boot) and the sockets into sockets.target, as the other services here;
# avahi-daemon also gets its D-Bus activation alias. ipp-usb is started by
# its udev rule. They run on the live desktop too: printing from the live
# session touches nothing on the disk. /var/lib/cups is CUPS's ServerRoot
# (rootfs/common/etc/cups/cups-files.conf), kept on the persistent partition
# by 94_isotope_state.yaml.
# cups-files.conf names the admin group as SystemGroup, and cupsd refuses to
# start on a group that does not exist, so the image has it, as the server's
# does (the installer and the live boot add their user to it). GID 900, the
# one kairos-init's 10_accounting.yaml gives it at every boot.
RUN set -e; \
    groupadd -f -g 900 admin; \
    groupadd -f lp; \
    getent passwd lp >/dev/null || useradd -r -M -d /var/spool/cups -g lp -s /usr/sbin/nologin -c "Printing" lp; \
    groupadd -rf avahi; \
    getent passwd avahi >/dev/null || useradd -r -M -d /run/avahi-daemon -g avahi -s /usr/sbin/nologin -c "Avahi mDNS/DNS-SD" avahi; \
    mkdir -p /var/lib/cups /var/spool/cups/tmp /var/cache/cups /etc/cups/ppd /etc/cups/ssl; \
    chgrp -R lp /etc/cups /var/spool/cups /var/cache/cups; \
    chown root:lp /var/lib/cups; chmod 0755 /var/lib/cups; chmod 0710 /var/spool/cups; \
    chmod 0640 /etc/cups/cupsd.conf /etc/cups/cups-files.conf; \
    systemctl enable cups.socket cups.service cups-browsed.service avahi-daemon.socket avahi-daemon.service 2>/dev/null || true; \
    mkdir -p /etc/systemd/system/multi-user.target.wants /etc/systemd/system/sockets.target.wants; \
    for u in cups cups-browsed avahi-daemon; do \
      ln -sf /usr/lib/systemd/system/$u.service /etc/systemd/system/multi-user.target.wants/$u.service; \
    done; \
    for u in cups avahi-daemon; do \
      ln -sf /usr/lib/systemd/system/$u.socket /etc/systemd/system/sockets.target.wants/$u.socket; \
    done; \
    ln -sf /usr/lib/systemd/system/avahi-daemon.service /etc/systemd/system/dbus-org.freedesktop.Avahi.service; \
    for l in multi-user.target.wants/cups.service multi-user.target.wants/cups-browsed.service \
             multi-user.target.wants/avahi-daemon.service sockets.target.wants/cups.socket \
             sockets.target.wants/avahi-daemon.socket dbus-org.freedesktop.Avahi.service; do \
      test -e /etc/systemd/system/$l || { echo "printing: /etc/systemd/system/$l does not resolve" >&2; exit 1; }; \
    done; \
    grep -qx 'ServerRoot /var/lib/cups' /etc/cups/cups-files.conf; \
    cupsd -t -c /etc/cups/cupsd.conf -s /etc/cups/cups-files.conf
# Firmware: the LVFS metadata refresh, daily on an installed system
# (fwupd-refresh.timer.d/10-isotope.conf). fwupd itself is D-Bus activated.
RUN systemctl enable fwupd-refresh.timer && \
    test -L /etc/systemd/system/timers.target.wants/fwupd-refresh.timer && \
    test -f /usr/lib/systemd/system/fwupd-refresh.timer.d/10-isotope.conf
# Fingerprint (the fingerprint system extension, extensions/fingerprint): a
# finger may authorize sudo and polkit (their password prompts), as it
# unlocks the lock screens (rootfs/<variant>/etc/pam.d). The line goes first
# in Hadron's sudo service and polkit's polkit-1 (/usr/lib/pam.d, unless an
# /etc copy wins), and starts with "-": PAM skips it while pam_fprintd.so is
# missing, which it is until the extension is merged. A user with no finger
# enrolled, or a machine with no reader, gets the password prompt at once.
# The login screens never get it (the login keyring needs the password).
# test/fingerprint/check-fingerprint.sh runs every PAM service of the image
# with and without the module.
RUN set -e; \
    line='-auth      sufficient   pam_fprintd.so  timeout=10'; \
    for svc in sudo polkit-1; do \
      f=/etc/pam.d/$svc; [ -f "$f" ] || f=/usr/lib/pam.d/$svc; \
      [ -f "$f" ] || { echo "fingerprint: no PAM service $svc" >&2; exit 1; }; \
      grep -q pam_fprintd "$f" && continue; \
      awk -v l="$line" 'NR == 1 && /^#%PAM/ { print; print l; next } NR == 1 { print l } { print }' "$f" >"$f.new"; \
      cat "$f.new" >"$f"; rm -f "$f.new"; \
      test "$(grep -m1 -E '^-?auth' "$f")" = "$line"; \
    done


# The server edition's image. Keep the binaries at the end in step with
# desktop-default above.
FROM ${BASE_IMAGE} AS server-default
ARG BASE_IMAGE
# The firewall (isotope-firewall.service) needs nft from the base. Hadron
# v0.6.7 and hadron-trusted:v0.6.7 have none; stop here, before the long
# build, rather than ship a unit that fails at every boot.
RUN test -x /usr/sbin/nft || { echo "${BASE_IMAGE} has no /usr/sbin/nft: the firewall needs a Hadron base with nftables (kairos-io/hadron#893)" >&2; exit 1; }
COPY --from=server-rootfs / /
COPY --from=toolchain /usr/lib/libffi.so* /usr/lib/
COPY --from=toolchain /usr/lib/libexpat.so* /usr/lib/
COPY --from=toolchain /usr/lib/libstdc++.so.6* /usr/lib/
COPY --from=toolchain /usr/lib/libgcc_s.so* /usr/lib/
COPY --from=toolchain /usr/lib/libreadline.so* /usr/lib/
# Network card firmware and CPU microcode (empty unless FIRMWARE=true).
COPY --from=firmware /firmware /
# Disk health: smartctl and smartd (off) from the hadron-layers layer.
COPY --from=smartmontools / /
# Docker, the same static bundle as the desktop (no Distrobox).
COPY --from=docker /docker /
COPY --from=server-config / /
# The splash is kept for one job: in the initramfs it asks for the disk
# passphrase (51isotope-askpass), and falls back to text without a framebuffer.
COPY --from=isotope-splash /build/splash/isotope-splash /usr/bin/kairos-splash
COPY --from=bootvine /bootvine/ /
# The installer puts its user in the desktop groups (pkg/installer/config.go);
# a group that does not exist would stop the user from being created.
# NetworkManager starts at boot; nmcli changes need root (sudo), see
# rootfs/server/etc/NetworkManager/NetworkManager.conf. polkit answers the
# update service only (50-isotope-update.rules: the admin group, no prompt),
# so it works the same from SSH and from the console. Docker stays disabled
# in the image, as on the desktop: the live ISO's /var is an overlayfs, where
# overlay2 fails its nested-overlay check. 92_docker.yaml starts it on the
# installed system, on the persistent /var/lib/docker.
RUN chmod 1777 /tmp; \
    ldconfig 2>/dev/null || true; \
    groupadd -f -g 900 admin; \
    for g in audio video render input bluetooth seat docker; do groupadd -f "$g"; done; \
    mkdir -p /var/lib/docker; \
    systemctl disable docker.socket docker.service 2>/dev/null || true; \
    rm -f /etc/systemd/system/sockets.target.wants/docker.socket \
      /etc/systemd/system/multi-user.target.wants/docker.service; \
    useradd -r -M -d / -s /usr/sbin/nologin polkitd; \
    chmod 4755 /usr/lib/polkit-1/polkit-agent-helper-1; \
    for d in /etc/polkit-1/rules.d /usr/share/polkit-1/rules.d; do \
      mkdir -p "$d" && chown polkitd:root "$d" && chmod 700 "$d"; \
    done; \
    # NetworkManager only, as on the desktop. systemd-networkd is disabled in
    # the kairos stage, after kairos-init, which enables it.
    systemctl enable NetworkManager.service; \
    test -L /etc/systemd/system/multi-user.target.wants/NetworkManager.service && \
    test -L /etc/systemd/system/network-online.target.wants/NetworkManager-wait-online.service
COPY --from=install-ui-build /out/isotope-install-ui /usr/bin/isotope-install-ui
COPY --from=update-build /out/isotope /usr/bin/isotope
RUN ln -sf isotope /usr/bin/isotope-update
RUN ln -sf isotope /usr/bin/isotope-ext
RUN ln -sf isotope /usr/bin/isotope-recovery
COPY --from=restic /restic/ /
RUN systemctl enable isotope-backup.timer isotope-restore-finish.service && \
    test -L /etc/systemd/system/timers.target.wants/isotope-backup.timer && \
    test -L /etc/systemd/system/multi-user.target.wants/isotope-restore-finish.service
# Automatic updates: the daily timer of `isotope update --auto`, which does
# nothing until `isotope update auto on` turns them on.
RUN systemctl enable isotope-autoupdate.timer && \
    test -L /etc/systemd/system/timers.target.wants/isotope-autoupdate.timer
# Firmware: fwupd and fwupdmgr, the daily LVFS metadata refresh. No
# udisks2 here, so no ESP for UEFI capsules: the capsule plugin is off and
# the other plugins (disks, USB, Thunderbolt, a BMC through Redfish) work.
COPY --from=fwupd-runtime /fwupd-runtime /
RUN systemctl enable fwupd-refresh.timer && \
    test -L /etc/systemd/system/timers.target.wants/fwupd-refresh.timer && \
    test -f /usr/lib/systemd/system/fwupd-refresh.timer.d/10-isotope.conf && \
    sed -i '/^\[fwupd\]$/a DisabledPlugins=uefi_capsule' /etc/fwupd/fwupd.conf && \
    [ "$(grep -c '^DisabledPlugins=uefi_capsule$' /etc/fwupd/fwupd.conf)" = 1 ]

# The image the kairos stage turns bootable: the desktop for sway, i3, cosmic
# and niri, the server edition for server.
FROM desktop-default AS sway-default
FROM desktop-default AS i3-default
FROM desktop-default AS cosmic-default
# The compiled schemas carry COSMIC's own override (GTK's window buttons on the
# left). This proves the default is compiled, not that an app follows it:
# cosmic-settings-daemon writes a user value over it, which its Isotope patch
# keeps on the left (test/look/check-look.sh greps the binary). Host GTK apps
# read the value directly; Flatpak GTK apps read it through
# xdg-desktop-portal-gtk, the second Settings backend (cosmic-portals.conf).
RUN test "$(gsettings get org.gnome.desktop.wm.preferences button-layout)" = "'close,minimize,maximize:'"
FROM desktop-default AS niri-default
FROM ${DESKTOP}-default AS default

# A development build's wallpaper: drawn from the build's commit by
# tools/wallpapers/build_wallpaper.py, and the stable names
# isotope-desktop-{night,day}.png pointed at it, in place of the release's
# Hydrogen. A release build passes no WALLPAPER_COMMIT and this stage is
# empty, so the link keeps pointing at Hydrogen. Only the pictures are made
# here (numpy and SciPy wheels need glibc, which the Hadron base does not
# have), so any Python image will do; the libraries are pinned in
# tools/wallpapers/requirements.txt.
FROM python:3.12-slim-bookworm AS build-wallpaper
ARG WALLPAPER_COMMIT=
COPY tools/wallpapers /w
COPY tools/gen-wallpaper.py /w/gen-wallpaper.py
RUN mkdir -p /out/usr/share/backgrounds/isotope && \
    if [ -n "${WALLPAPER_COMMIT}" ]; then \
      pip install --no-cache-dir -r /w/requirements.txt && \
      d=/out/usr/share/backgrounds/isotope && short="$(printf %s "${WALLPAPER_COMMIT}" | cut -c1-12)" && \
      for mode in night day; do \
        python3 /w/build_wallpaper.py --seed "${WALLPAPER_COMMIT}" --mode "$mode" --out "$d" && \
        ln -sf "isotope-build-${short}-${mode}-3840x2160.png" "$d/isotope-desktop-${mode}.png" && \
        python3 /w/gen-wallpaper.py --blur "$d/isotope-desktop-${mode}.png" "$d/isotope-desktop-${mode}-blur.png"; \
      done && ls -l "$d"; \
    fi


# ===========================================================================
# Kairos init layer — makes the image bootable/installable.
#
# Folds in what used to be a separately-fetched Kairos Dockerfile
# (kairos-io/kairos images/Dockerfile): bind-mount the kairos-init tool and run
# its `install` then `init` stages on top of the desktop image. This is the
# default build target, so a single `docker build .` produces an image
# AuroraBoot can turn straight into an ISO. Build `--target default` for the
# bare desktop image without this layer.
# ===========================================================================
FROM ${KAIROS_INIT_IMAGE} AS kairos-init

FROM default AS kairos
ARG MODEL=generic
# The development build's wallpaper, when there is one (see build-wallpaper).
COPY --from=build-wallpaper /out/ /
ARG TRUSTED_BOOT=false
ARG VERSION=v0.0.0
ARG FIPS=no-fips
# The splash shows the release. kairos-init's dracut module copies every file in
# /etc/kairos/branding/splash into the initramfs, so writing it here, before
# kairos-init builds the initramfs, gives both halves of the boot the same one.
RUN mkdir -p /etc/kairos/branding/splash && printf '%s\n' "${VERSION}" > /etc/kairos/branding/splash/version
# The tss user and group for the TPM udev rules (/usr/lib/sysusers.d/tpm2-tss.conf)
# in the image's /etc/passwd, so they do not depend on systemd-sysusers.service
# running at boot. Every edition passes here.
RUN systemd-sysusers tpm2-tss.conf && \
    grep -q '^tss:' /etc/passwd && grep -q '^tss:' /etc/group
# The nm-openconnect user, which NetworkManager-openconnect runs the tunnel's
# openconnect as (the desktops only; the server has no OpenConnect).
RUN [ ! -f /usr/lib/sysusers.d/nm-openconnect.conf ] || \
    { systemd-sysusers nm-openconnect.conf && grep -q '^nm-openconnect:' /etc/passwd; }
# The fwupd-refresh user, which fwupd-refresh.service (the daily LVFS
# metadata refresh) runs as. The agent has no fwupd.
RUN [ ! -f /usr/lib/sysusers.d/fwupd.conf ] || \
    { systemd-sysusers fwupd.conf && grep -q '^fwupd-refresh:' /etc/passwd; }
# UEFI capsules need fwupdx64.efi to start at the next boot, and on a Trusted
# Boot image Secure Boot is always on and the helper is not signed with
# Isotope's db key; the ESP is also mounted read-only at /efi. So the capsule
# plugin is off there, and every other device still updates. The server
# turns it off already (no udisks2); the line is added once. See
# docs/superpowers/specs/2026-11-24-firmware-updates-design.md.
RUN [ "${TRUSTED_BOOT}" = false ] || [ ! -f /etc/fwupd/fwupd.conf ] || \
    { grep -qx 'DisabledPlugins=uefi_capsule' /etc/fwupd/fwupd.conf || \
        sed -i '/^\[fwupd\]$/a DisabledPlugins=uefi_capsule' /etc/fwupd/fwupd.conf; \
      [ "$(grep -c '^DisabledPlugins=uefi_capsule$' /etc/fwupd/fwupd.conf)" = 1 ]; }
# System extensions from the catalogs are unsigned; the normal install merges
# them anyway (rootfs/common/etc/kairos/90_isotope_extensions.yaml and
# rootfs/common/system/oem/93_isotope_extensions.yaml). A Trusted Boot image keeps
# signatures on: it gets neither file.
RUN [ "${TRUSTED_BOOT}" = false ] || \
    rm -f /etc/kairos/90_isotope_extensions.yaml /system/oem/93_isotope_extensions.yaml
RUN --mount=type=bind,from=kairos-init,src=/kairos-init,dst=/kairos-init \
    fips_flag=""; [ "$FIPS" = "fips" ] && fips_flag="--fips"; \
    /kairos-init -l debug -s install -m "${MODEL}" -t "${TRUSTED_BOOT}" --version "${VERSION}" $fips_flag && \
    /kairos-init -l debug -s init    -m "${MODEL}" -t "${TRUSTED_BOOT}" --version "${VERSION}" $fips_flag
# NetworkManager is the only network manager. kairos-init's "Enable services
# for Hadron" step runs `systemctl enable systemd-networkd`, so this must come
# after it: a disable in the edition stages was undone here. With both enabled,
# two DHCP clients ran on the wired card, and systemd-networkd-wait-online
# (which cannot see Wi-Fi) held network-online.target for its 120 s timeout on
# every Wi-Fi or unplugged boot. NetworkManager-wait-online stays the one
# network-online waiter. test/boot-ux/check-boot-units.sh checks the result.
RUN systemctl disable systemd-networkd.service systemd-networkd.socket \
      systemd-networkd-wait-online.service 2>/dev/null || true; \
    ! ls /etc/systemd/system/*.wants/systemd-networkd* 2>/dev/null | grep -q .
# No default `kairos` user on an installed system. kairos-init's
# 10_accounting.yaml creates a locked one on every boot, and COSMIC's greeter
# lists it as "Created by entities". Keep it on the live ISO and in UKI install
# mode, where it gets the password "kairos". The script fails the build if
# kairos-init changes that step; test/accounting/check-default-user.sh checks
# the result.
RUN --mount=type=bind,source=scripts/kairos-user-live-only.sh,target=/tmp/kairos-user-live-only.sh \
    sh /tmp/kairos-user-live-only.sh /system/oem/10_accounting.yaml
# Brand the GRUB boot-menu entry. kairos-agent reads GRUB_ENTRY_NAME from
# /etc/kairos-release (which kairos-init just generated) when install.grub-entry-
# name is unset, preferring it over /etc/os-release. The OS identity fields in
# /etc/os-release are intentionally left untouched.
RUN echo 'GRUB_ENTRY_NAME="isotope"' >> /etc/kairos-release
# Release identity for isotope-update. FLAVOR and GPU name the image line this
# system upgrades along: isotope-update pulls ${IMAGE_REPO}:${FLAVOR}-<tag> for
# GPU=full and ${IMAGE_REPO}:${FLAVOR}-vm-<tag> for GPU=vm, which release.yml
# pushes for every published release. Dockerfile.agent rewrites FLAVOR to
# `agent` for the appliance, which has the one line agent-<tag>. VERSION is the
# release tag, so a dev build (v0.0.0) never sorts as newer than a real
# release.
ARG IMAGE_REPO=ghcr.io/kairos-community/isotope
ARG HADRON_VERSION
ARG DESKTOP
ARG GPU
ARG FIRMWARE=false
RUN case "${GPU}" in full|vm) ;; *) echo "GPU must be full or vm, not '${GPU}'" >&2; exit 1 ;; esac && \
    printf 'FLAVOR=%s\nGPU=%s\nVERSION=%s\nHADRON_VERSION=%s\nIMAGE_REPO=%s\nGITHUB_REPO=%s\nTRUSTED_BOOT=%s\n' \
      "${DESKTOP}" "${GPU}" "${VERSION}" "${HADRON_VERSION}" "${IMAGE_REPO}" kairos-community/isotope "${TRUSTED_BOOT}" \
      > /etc/isotope/release
# The same identity as OCI labels, so isotope-update can tell an Isotope
# image from any other one without pulling it (the registry config blob carries
# them). Kept in this last stage, after every expensive one, because the value
# changes per release and a LABEL ahead of them would invalidate their cache.
# The keys must match the constants in cmd/isotope/policy.go.
LABEL io.kairos.isotope.flavor="${DESKTOP}" \
      io.kairos.isotope.version="${VERSION}" \
      io.kairos.isotope.hadron-version="${HADRON_VERSION}" \
      io.kairos.isotope.gpu="${GPU}" \
      io.kairos.isotope.firmware="${FIRMWARE}"
# Isotope GRUB menu colours — text-mode fallback. The main menu renders from
# /etc/cos/grub.cfg (copied to COS_STATE at install) before grubmenu.cfg switches
# to the gfxterm theme, and these 16-colour accents also apply if gfxterm/the
# theme ever fail to load. Inserted after `loadfont unicode`, before the menu.
# A Trusted Boot image has no GRUB: kairos-init writes no /etc/cos/grub.cfg
# with -t true, and systemd-boot boots the signed UKIs directly.
RUN [ "${TRUSTED_BOOT}" = true ] || sed -i '/^loadfont unicode/a set color_normal=light-gray/black\nset color_highlight=black/light-green\nset menu_color_normal=light-green/black\nset menu_color_highlight=black/light-green' /etc/cos/grub.cfg
# The Isotope gfxterm theme with a background image. kairos-init's branding stage
# writes its own /etc/kairos/branding/grubmenu.cfg (bundled.ExtraGrubCfg),
# clobbering the overlay copy, so re-apply ours AFTER kairos-init: it switches the
# installed menu to gfxterm and `set theme=` to themes/isotope/theme.txt. 08_grub
# copies this to COS_STATE/grubmenu at install; 09_isotope_grub_theme.yaml (oem)
# runs /usr/libexec/isotope-grub-theme, which puts the theme dir, the font and the
# EFI grub modules on STATE so gfxmenu/tga/the theme all resolve there.
# (isotope-theme/ isn't written by kairos-init, so its COPY at line ~1982 survives.)
COPY rootfs/common/etc/kairos/branding/grubmenu.cfg /etc/kairos/branding/grubmenu.cfg
# Console font. kairos-init may regenerate /etc/vconsole.conf, so re-apply ours
# after it — same treatment as /etc/issue and /etc/motd below.
COPY rootfs/common/etc/vconsole.conf /etc/vconsole.conf
# Per-variant GRUB theme subtitle. theme.txt ships in the COMMON overlay with an
# @VARIANT_SUBTITLE@ placeholder; the variant overlay supplies DESKTOP_NAME and
# DISPLAY_NAME via /etc/isotope/session. Lowercased to match the theme's
# typographic style ("i3 · xlibre · kairos").
#
# The chain is a single && list ending in `! grep`, so ANY link failing (missing
# session file, empty names, sed error) or the placeholder surviving fails the
# build: a theme with a literal @VARIANT_SUBTITLE@ would render that text on the
# boot menu.
RUN . /etc/isotope/session && \
    [ -n "$DESKTOP_NAME" ] && [ -n "$DISPLAY_NAME" ] && \
    subtitle="$(printf '%s · %s · kairos' "$DESKTOP_NAME" "$DISPLAY_NAME" | tr '[:upper:]' '[:lower:]')" && \
    sed -i "s|@VARIANT_SUBTITLE@|${subtitle}|" /etc/kairos/branding/isotope-theme/theme.txt && \
    ! grep -q '@VARIANT_SUBTITLE@' /etc/kairos/branding/isotope-theme/theme.txt
# kairos-init regenerates /etc/motd (and may touch /etc/issue); re-apply the
# branded console banners from the selected desktop overlay on top. From the
# build context, not the desktop-config stage: on COSMIC that stage reaches
# isotope-applets, cosmic-build and LLVM, and release.yml's Trusted Boot job
# (make UKI_FROM=..., which rebuilds only this stage, with no prebuilt LLVM
# image) would compile LLVM and time out.
COPY rootfs/${DESKTOP}/etc/issue rootfs/${DESKTOP}/etc/motd /etc/

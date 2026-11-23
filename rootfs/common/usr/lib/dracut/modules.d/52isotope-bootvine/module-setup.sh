#!/bin/bash
# Isotope: bootvine in the initramfs, for the splash to hand the screen to.
# See splash/README.md ("Boot details").
#
# bootvine's own 90bootvine module enables bootvine.service, so it would draw
# on every boot. Here the unit is installed and left disabled: kairos-splash
# starts it when D is pressed or isotope.bootvine is on the kernel command
# line. Once started it survives switch-root by itself (an @ argv[0]) and
# follows the rest of the boot.

check() {
    require_binaries /usr/bin/bootvine || return 1
    return 0
}

depends() {
    echo systemd
    return 0
}

install() {
    declare systemdsystemunitdir=${systemdsystemunitdir}
    inst_multiple /usr/bin/bootvine
    inst_simple "${systemdsystemunitdir}/bootvine.service"
}

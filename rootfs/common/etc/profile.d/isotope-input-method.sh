# Input method variables, only while the fcitx5 system extension is merged
# (isotope ext install fcitx5). Without /usr/bin/fcitx5 this file sets
# nothing, so a system without the extension is as before.
#
# Wayland (sway, niri, COSMIC): GTK 3 and 4 and Qt 6 talk to fcitx5 through
# the compositor (text-input-v3 to zwp_input_method_v2), so GTK_IM_MODULE
# and QT_IM_MODULE stay unset. QT_IM_MODULES (Qt 6.8.2 and later) tries
# that first and fcitx5's own Qt module (in Flatpak runtimes) second.
# XMODIFIERS is for X11 apps under Xwayland (XIM), SDL_IM_MODULE for SDL
# games, which talk to fcitx5 over D-Bus.
#
# i3 (X11): no compositor in between. GTK_IM_MODULE=fcitx:xim takes the
# fcitx module where an app has one (Flatpak runtimes ship it) and XIM
# otherwise (the image's own GTK 3, which has no fcitx module); Qt takes
# fcitx's module.
#
# Sourced by login shells (ly's session setup, niri-session, greetd's
# /etc/profile for COSMIC); sway's and i3's start-desktop and
# isotope-niri-session source it themselves. A value already set (from
# ~/.profile) wins, so a user can pick another input method framework.
# `isotope input-method start`, which every session runs, hands these to
# D-Bus activated services. See
# docs/superpowers/specs/2026-11-24-input-methods-design.md.
if [ -x /usr/bin/fcitx5 ]; then
    _him_session=
    if [ -r /etc/isotope/session ]; then
        while IFS='=' read -r _him_key _him_value; do
            [ "$_him_key" = SESSION ] && _him_session=$_him_value
        done < /etc/isotope/session
    fi
    export XMODIFIERS="${XMODIFIERS:-@im=fcitx}"
    export SDL_IM_MODULE="${SDL_IM_MODULE:-fcitx}"
    if [ "$_him_session" = i3 ]; then
        export GTK_IM_MODULE="${GTK_IM_MODULE:-fcitx:xim}"
        export QT_IM_MODULE="${QT_IM_MODULE:-fcitx}"
    else
        export QT_IM_MODULES="${QT_IM_MODULES:-wayland;fcitx}"
    fi
    unset _him_session _him_key _him_value
fi

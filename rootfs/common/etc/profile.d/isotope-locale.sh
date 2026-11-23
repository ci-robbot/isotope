# System language chosen on the Welcome page (SetLocale in isotope-updated).
#
# isotope-updated writes /etc/locale.conf (LANG=...) and an /oem cloud-config
# that writes it again at every boot, because /etc is rebuilt from the image.
# Nothing in this repo reads /etc/locale.conf into a session: ly's and greetd's
# PAM stacks include Hadron's own `login` service (not in this repo, so whether
# it has pam_env with that file is unverified), and start-desktop sets no LANG.
# So export it here, as isotope-keyboard.sh does for the layout.
#
# Sourced by login shells (ly's session setup, niri-session, start-cosmic);
# sway's and i3's start-desktop source this file themselves. It is
# unconditional, not "unless already set": the system language is for every
# user, and ~/.profile runs after this file, so a user can still override it.
# The file is only read, never run. A quoted value (LANG="de_DE.UTF-8") is
# ignored on purpose: isotope-updated never writes one, and the check below
# accepts only plain locale-name characters.
if [ -r /etc/locale.conf ]; then
    while IFS='=' read -r _hl_key _hl_value; do
        if [ "$_hl_key" = LANG ]; then
            case "$_hl_value" in
                '' | *[!A-Za-z0-9_.@-]*) ;;
                *) export LANG="$_hl_value" ;;
            esac
        fi
    done < /etc/locale.conf
    unset _hl_key _hl_value
fi

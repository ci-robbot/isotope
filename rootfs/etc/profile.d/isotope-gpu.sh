# Software rendering when Mesa has no working driver for the GPU: see
# /usr/libexec/isotope-gpu-env.
#
# greetd sources /etc/profile with /bin/sh before the commands it runs
# (COSMIC's session and login screen), whatever the user's shell; ly's session
# setup does for sh, bash and fish users. sway's and i3's start-desktop,
# isotope-niri-session and isotope-greeter-start source the hook themselves
# too, for the shells that skip this file.
#
# Not over SSH (a remote login starts no compositor on this GPU), and not for
# root: `sudo -i` and `su -` start login shells that need no GPU, and the
# installer runs the probe itself.
if [ -z "${SSH_CONNECTION:-}" ] && [ "$(id -u)" != 0 ] && [ -r /usr/libexec/isotope-gpu-env ]; then
    . /usr/libexec/isotope-gpu-env
fi

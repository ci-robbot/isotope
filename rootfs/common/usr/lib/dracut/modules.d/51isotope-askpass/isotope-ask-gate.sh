#!/bin/sh
# ExecStartPre of systemd-ask-password-console.service in the initramfs.
#
# systemd starts the console agent when a question appears in
# /run/systemd/ask-password. Two agents answering the same question would put
# two prompts on one screen, so exactly one of them asks:
#
#   - kairos-splash has a framebuffer and the keyboard: it wrote its PID to
#     /run/isotope-ask/splash-agent and draws the prompt. This gate waits, and
#     the console agent starts only after the splash is gone (switch-root,
#     emergency, a crash). A question still open then is asked on the console.
#   - kairos-splash has no framebuffer, did not start, or was told not to
#     (isotope.splash.agent=0): no PID file, or a dead PID. The gate stops the
#     splash, which puts tty1 back in text mode, and the console agent asks.
#   - bootvine runs (the splash handed it the screen on D, or
#     isotope.bootvine): it is a password agent too and asks on tty12, the
#     console it shows. The gate waits while it runs, as for the splash.
#
# The splash unit is Type=notify and sends READY=1 only after that decision,
# and this unit is After= it, so the PID file is never looked at too early.

pidfile=/run/isotope-ask/splash-agent

# switch-root stops this unit while the gate is still waiting. That is the
# normal end of a boot where the splash answered, not a failure.
trap 'exit 0' TERM

splash_answers() {
    [ -r "$pidfile" ] || return 1
    read -r pid < "$pidfile" || return 1
    [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null
}

if splash_answers; then
    echo "isotope-ask-gate: kairos-splash (PID $pid) answers password questions"
    while splash_answers; do
        sleep 1
    done
    echo "isotope-ask-gate: kairos-splash is gone, the console agent takes over"
fi

# bootvine.service is active from the moment the splash's `systemctl start`
# returns, and the splash removes its PID file only after that.
if systemctl -q is-active bootvine.service 2>/dev/null; then
    echo "isotope-ask-gate: bootvine answers password questions"
    while systemctl -q is-active bootvine.service 2>/dev/null; do
        sleep 1
    done
    echo "isotope-ask-gate: bootvine is gone, the console agent takes over"
fi

if systemctl -q is-active kairos-splash.service 2>/dev/null; then
    systemctl stop kairos-splash.service
fi
exit 0

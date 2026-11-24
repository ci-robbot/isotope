#!/bin/sh
# pam-matrix.sh FLAVOR: runs as root inside a built image, started by
# test/fingerprint/check-fingerprint.sh IMAGE with pam-check and the
# pam_fprintd stand-in in /probe (Dockerfile.pam). It changes the throwaway
# container only.
#
# 1. Every PAM service of the image (/etc/pam.d, then /usr/lib/pam.d) names
#    only modules that exist and includes only services that exist, except
#    on the lines that start with "-", which PAM skips when the module is
#    missing.
# 2. As the image is (no fingerprint extension): the right password opens
#    every service a person types a password into, a wrong one opens none.
# 3. With the extension's two modules in place (the stand-in answering "no
#    reader", as pam_fprintd does without a reader or an enrolled finger):
#    the same.
# 4. With the stand-in answering "a finger matched" and a wrong password:
#    sudo, polkit-1 and the lock screens open, the login services do not
#    and never ask the module. swaylock and i3lock check a typed password
#    before the finger.
# On the server and the agent no service takes a finger.
set -u
flavor="$1"
P=/probe
fail=0
pass() { echo "PASS: $*"; }
bad() { echo "FAIL: $*"; fail=1; }

# --- 1. modules and includes --------------------------------------------------
svcfile() { # svcfile NAME: the file PAM reads for the service, or nothing
	if [ -f "/etc/pam.d/$1" ]; then echo "/etc/pam.d/$1"; elif [ -f "/usr/lib/pam.d/$1" ]; then echo "/usr/lib/pam.d/$1"; fi
}
modfile() { # modfile MODULE: the module's path, or nothing
	case "$1" in
	/*) [ -e "$1" ] && echo "$1" ;;
	*) for d in /usr/lib/security /lib/security; do [ -e "$d/$1" ] && { echo "$d/$1"; return; }; done ;;
	esac
}
services=$(for f in /etc/pam.d/* /usr/lib/pam.d/*; do [ -f "$f" ] && basename "$f"; done | sort -u)
for s in $services; do
	f=$(svcfile "$s")
	[ -n "$f" ] || continue
	# type, control (a [..] control is one word once its spaces go), module
	sed -e 's/#.*//' -e ':a' -e 's/\(\[[^]]*\) /\1/' -e 'ta' "$f" | while read -r type control module _; do
		[ -n "$type" ] || continue
		case "$type" in @include) continue ;; -*) continue ;; esac
		case "$control" in
		include | substack)
			[ -n "$(svcfile "$module")" ] || echo "$f: $type $control $module (no such service)" ;;
		*)
			[ -n "$(modfile "$module")" ] || echo "$f: $type $control $module (no such module)" ;;
		esac
	done
done >/tmp/broken
if [ -s /tmp/broken ]; then
	bad "every PAM service names modules and services that exist:"; sed 's/^/    /' /tmp/broken
else
	pass "every PAM service of the image names modules and services that exist (not counting \"-\" lines)"
fi
check_dash() { # every line naming the fingerprint modules starts with "-"
	grep -rHs -e pam_fprintd -e pam_isotope_unix /etc/pam.d /usr/lib/pam.d | grep -v ':[[:space:]]*-' | grep -v ':[[:space:]]*#'
}
if [ -n "$(check_dash)" ]; then
	bad "a fingerprint line without \"-\": $(check_dash)"
else
	pass "every fingerprint line starts with \"-\", so a missing module is skipped"
fi

# --- the user and the services ------------------------------------------------
pw='Isotope-fp-check-1'
shell=$(grep -E '^/' /etc/shells 2>/dev/null | while read -r s; do [ -x "$s" ] && { echo "$s"; break; }; done)
[ -n "$shell" ] || shell=/bin/sh
useradd -m -s "$shell" fptest >/dev/null 2>&1 || true
printf 'fptest:%s\n' "$pw" | chpasswd || { bad "chpasswd"; exit 1; }

# The services a person types a password into, and who runs each one: the
# lock screens run as the user, the others as root (setuid sudo, polkit's
# setuid helper, the login screens, sshd).
user_svcs="swaylock i3lock cosmic-greeter"
all="login system-login system-auth sshd sudo polkit-1 ly cosmic-greeter-login $user_svcs"
present=
for s in $all; do [ -n "$(svcfile "$s")" ] && present="$present $s"; done
finger_svcs="sudo polkit-1 $user_svcs"
case "$flavor" in server | agent) finger_svcs= ;; esac

auth() { # auth SERVICE PASSWORD: 0 when PAM opens SERVICE for fptest
	rm -rf /run/faillock /var/run/faillock
	case " $user_svcs " in
	*" $1 "*) printf '%s\n' "$2" | su -s /bin/sh fptest -c "$P/pam-check $1 fptest" 2>>/tmp/pam.log ;;
	*) printf '%s\n' "$2" | "$P/pam-check" "$1" fptest 2>>/tmp/pam.log ;;
	esac
}
calls() { wc -l </run/fp-stub.calls | tr -d ' '; }
# The lock screens run as the user, so the stand-in writes there as fptest.
reset_calls() { : >/run/fp-stub.calls; chmod 666 /run/fp-stub.calls; }
is_finger() { case " $finger_svcs " in *" $1 "*) return 0 ;; esac; return 1; }

passwords() { # passwords WHEN: the right password opens, a wrong one does not
	for s in $present; do
		if auth "$s" "$pw"; then pass "$1: $s opens with the password"; else bad "$1: $s refuses the right password"; fi
		if auth "$s" wrong-password; then bad "$1: $s opens with a wrong password"; else pass "$1: $s refuses a wrong password"; fi
	done
}

# --- 2. without the extension -------------------------------------------------
if [ -n "$(modfile pam_fprintd.so)" ] || [ -n "$(modfile pam_isotope_unix.so)" ]; then
	bad "the image itself has pam_fprintd.so or pam_isotope_unix.so (they belong to the extension)"
fi
passwords "without the extension"

# --- 3. the extension merged, no reader or no finger ---------------------------
cp "$P/pam_fprintd.so" /usr/lib/security/pam_fprintd.so
ln -sf pam_unix.so /usr/lib/security/pam_isotope_unix.so
echo unavail >/run/fp-stub
passwords "with the extension and no reader"

# --- 4. a finger that matches ---------------------------------------------------
echo match >/run/fp-stub
for s in $present; do
	reset_calls
	if is_finger "$s"; then
		if auth "$s" wrong-password && [ "$(calls)" -ge 1 ]; then pass "a finger opens $s"; else bad "a finger does not open $s"; fi
	else
		if auth "$s" wrong-password; then bad "a finger opens $s, which keeps the password"; else pass "a finger does not open $s"; fi
		if [ "$(calls)" = 0 ]; then pass "$s never asks for a finger"; else bad "$s asks for a finger"; fi
	fi
done
# swaylock and i3lock: the typed password is checked before the finger.
for s in swaylock i3lock; do
	case " $present " in *" $s "*) ;; *) continue ;; esac
	is_finger "$s" || continue
	reset_calls
	if auth "$s" "$pw" && [ "$(calls)" = 0 ]; then pass "$s checks a typed password before the finger"; else bad "$s asks for the finger before it checks the typed password"; fi
done

if [ "$fail" != 0 ]; then
	echo "--- PAM's answers:"; cat /tmp/pam.log
fi
exit "$fail"

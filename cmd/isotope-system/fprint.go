package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
)

// Fingerprints: fprintd (the fingerprint system extension) for the user's
// own fingers, over its D-Bus API as the user. fprintd asks polkit: listing
// needs net.reactivated.fprint.device.verify (yes for an active local
// session), adding and removing net.reactivated.fprint.device.enroll
// (auth_self_keep: the user's own password, asked through the page's polkit
// agent, polkit_agent.go). See
// docs/superpowers/specs/2026-11-24-fingerprint-design.md.

// Fingerprints is fprintd as the Users page needs it.
type Fingerprints interface {
	// State says whether fprintd is installed, the reader and the user's
	// enrolled fingers.
	State() (FingerState, error)
	// Enroll adds finger (an fprintd finger name), calling progress with
	// each step until it is done. Cancelling ctx stops it. polkit asks the
	// user's password through ask.
	Enroll(ctx context.Context, finger string, ask asker, progress func(EnrollStep)) error
	// Delete removes one of the user's enrolled fingers; polkit asks the
	// user's password through ask.
	Delete(ctx context.Context, finger string, ask asker) error
}

// FingerState is what the page shows.
type FingerState struct {
	// Installed: fprintd can be started (the extension is merged).
	Installed bool
	// NeedsRestart: fprintd is installed but polkit does not know its
	// actions yet (the extension was installed since polkit started), so
	// nothing can be added until the next start.
	NeedsRestart bool
	// Reader is the reader's name, "" when there is none.
	Reader string
	// Swipe: the reader is swiped, not touched.
	Swipe bool
	// Stages is how many scans one enrollment takes (0: unknown).
	Stages int
	// Enrolled are the user's enrolled fingers, in fingerNames' order.
	Enrolled []string
}

// EnrollStep is one status of an enrollment.
type EnrollStep struct {
	// Passed is how many scans were good so far.
	Passed int
	// Message says what to do now, "" for nothing new.
	Message string
	// Done: the enrollment ended (Err says how).
	Done bool
}

// fingerNames are fprintd's finger names, left hand then right hand.
var fingerNames = []string{
	"left-thumb", "left-index-finger", "left-middle-finger", "left-ring-finger", "left-little-finger",
	"right-thumb", "right-index-finger", "right-middle-finger", "right-ring-finger", "right-little-finger",
}

// fingerLabel is "Right index finger" for "right-index-finger".
func fingerLabel(name string) string {
	s := strings.ReplaceAll(name, "-", " ")
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// fingerShort is "Index finger" for "right-index-finger": the finger
// without its hand.
func fingerShort(name string) string {
	_, rest, ok := strings.Cut(name, "-")
	if !ok {
		rest = name
	}
	return fingerLabel(rest)
}

// fingerPhrase is "your right index finger", for the instructions.
func fingerPhrase(name string) string {
	return "your " + strings.ReplaceAll(name, "-", " ")
}

// sortFingers puts known names in fingerNames' order, unknown ones last.
func sortFingers(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range fingerNames {
		for _, x := range in {
			if x == n && !seen[n] {
				out = append(out, n)
				seen[n] = true
			}
		}
	}
	for _, x := range in {
		if !seen[x] && x != "" {
			out = append(out, x)
			seen[x] = true
		}
	}
	return out
}

// enrollMessage is the line to show for an fprintd EnrollStatus result,
// and whether the result ends the enrollment and with which error.
func enrollMessage(result string, swipe bool) (msg string, done bool, err error) {
	verb := "Touch"
	if swipe {
		verb = "Swipe"
	}
	switch result {
	case "enroll-stage-passed":
		return "Good. Lift your finger, then " + strings.ToLower(verb) + " the reader again.", false, nil
	case "enroll-completed":
		return "", true, nil
	case "enroll-retry-scan":
		return "That scan did not work. " + verb + " the reader again.", false, nil
	case "enroll-swipe-too-short":
		return "The swipe was too short. Swipe again.", false, nil
	case "enroll-finger-not-centered":
		return "Put your finger in the middle of the reader and try again.", false, nil
	case "enroll-remove-and-retry":
		return "Lift your finger, then try again.", false, nil
	case "enroll-too-fast":
		return "Too fast. Wait a moment, then try again.", false, nil
	case "enroll-duplicate":
		return "", true, errors.New("this finger is already enrolled, for you or another user")
	case "enroll-data-full":
		return "", true, errors.New("the reader has no room for another fingerprint")
	case "enroll-disconnected":
		return "", true, errors.New("the reader was disconnected")
	case "enroll-failed":
		return "", true, errors.New("the reader could not enroll this finger: try again")
	}
	return "", true, fmt.Errorf("the reader reported %s", result)
}

// ---- fprintd over D-Bus ----

const (
	fprintName       = "net.reactivated.Fprint"
	fprintManager    = "/net/reactivated/Fprint/Manager"
	fprintManagerIf  = "net.reactivated.Fprint.Manager"
	fprintDeviceIf   = "net.reactivated.Fprint.Device"
	fprintNoDevice   = "net.reactivated.Fprint.Error.NoSuchDevice"
	fprintNoPrints   = "net.reactivated.Fprint.Error.NoEnrolledPrints"
	polkitName       = "org.freedesktop.PolicyKit1"
	polkitAuthority  = "/org/freedesktop/PolicyKit1/Authority"
	polkitAuthIf     = "org.freedesktop.PolicyKit1.Authority"
	fprintEnrollPerm = "net.reactivated.fprint.device.enroll"
)

// fprintd is Fingerprints on a bus (the system bus; a private one in the
// tests).
type fprintd struct {
	conn *dbus.Conn
	// polkit: whether polkit is there, to ask for fprintd's actions and to
	// register the page's agent with (the tests' bus has no polkit).
	polkit bool
	// user and uid are whose password the agent asks for.
	user string
	uid  uint32
	mu   sync.Mutex // one claim at a time
}

func newFprintd(conn *dbus.Conn, user string, uid uint32) *fprintd {
	return &fprintd{conn: conn, polkit: true, user: user, uid: uid}
}

// withAgent runs do with the page's polkit agent registered for this
// process (polkit_agent.go). When it cannot be registered, the session's
// agent, if there is one, answers instead.
func (f *fprintd) withAgent(ask asker, do func() error) error {
	if f.polkit && ask != nil {
		stop, err := startPolkitAgent(f.conn, f.user, f.uid, ask)
		if err != nil {
			log.Printf("fingerprint: %v", err)
		}
		defer stop()
	}
	return do()
}

func dbusErrName(err error) string {
	var e dbus.Error
	if errors.As(err, &e) {
		return e.Name
	}
	var pe *dbus.Error
	if errors.As(err, &pe) {
		return pe.Name
	}
	return ""
}

// call is a method call that lets polkit ask the user (fprintd passes the
// flag on to polkit).
func call(o dbus.BusObject, method string, args ...any) *dbus.Call {
	return o.Call(method, dbus.FlagAllowInteractiveAuthorization, args...)
}

func (f *fprintd) installed() bool {
	var names []string
	if err := f.conn.BusObject().Call("org.freedesktop.DBus.ListActivatableNames", 0).Store(&names); err == nil {
		for _, n := range names {
			if n == fprintName {
				return true
			}
		}
	}
	var has bool
	_ = f.conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, fprintName).Store(&has)
	return has
}

// polkitKnows says whether polkit has fprintd's enroll action.
func (f *fprintd) polkitKnows() bool {
	var actions []struct {
		ID, Description, Message, Vendor, VendorURL, Icon string
		Any, Inactive, Active                             uint32
		Annotations                                       map[string]string
	}
	err := f.conn.Object(polkitName, polkitAuthority).Call(polkitAuthIf+".EnumerateActions", 0, "").Store(&actions)
	if err != nil {
		return true // cannot tell: do not ask for a restart
	}
	for _, a := range actions {
		if a.ID == fprintEnrollPerm {
			return true
		}
	}
	return false
}

func (f *fprintd) device() (dbus.BusObject, error) {
	var p dbus.ObjectPath
	err := f.conn.Object(fprintName, fprintManager).Call(fprintManagerIf+".GetDefaultDevice", 0).Store(&p)
	if err != nil {
		return nil, err
	}
	return f.conn.Object(fprintName, p), nil
}

func (f *fprintd) State() (FingerState, error) {
	var st FingerState
	if !f.installed() {
		return st, nil
	}
	st.Installed = true
	if f.polkit {
		st.NeedsRestart = !f.polkitKnows()
	}
	dev, err := f.device()
	if dbusErrName(err) == fprintNoDevice {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if v, err := dev.GetProperty(fprintDeviceIf + ".name"); err == nil {
		st.Reader, _ = v.Value().(string)
	}
	if st.Reader == "" {
		st.Reader = "Fingerprint reader"
	}
	if v, err := dev.GetProperty(fprintDeviceIf + ".scan-type"); err == nil {
		s, _ := v.Value().(string)
		st.Swipe = s == "swipe"
	}
	if v, err := dev.GetProperty(fprintDeviceIf + ".num-enroll-stages"); err == nil {
		n, _ := v.Value().(int32)
		st.Stages = int(n)
	}
	var fingers []string
	err = call(dev, fprintDeviceIf+".ListEnrolledFingers", "").Store(&fingers)
	if err != nil && dbusErrName(err) != fprintNoPrints {
		return st, err
	}
	st.Enrolled = sortFingers(fingers)
	return st, nil
}

// claimed runs do with the reader claimed for the user, and releases it.
func (f *fprintd) claimed(do func(dev dbus.BusObject) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	dev, err := f.device()
	if err != nil {
		return err
	}
	if err := call(dev, fprintDeviceIf+".Claim", "").Err; err != nil {
		return err
	}
	defer dev.Call(fprintDeviceIf+".Release", 0)
	return do(dev)
}

func (f *fprintd) Delete(ctx context.Context, finger string, ask asker) error {
	return f.withAgent(ask, func() error {
		return f.claimed(func(dev dbus.BusObject) error {
			return call(dev, fprintDeviceIf+".DeleteEnrolledFinger", finger).Err
		})
	})
}

func (f *fprintd) Enroll(ctx context.Context, finger string, ask asker, progress func(EnrollStep)) error {
	return f.withAgent(ask, func() error { return f.enroll(ctx, finger, progress) })
}

func (f *fprintd) enroll(ctx context.Context, finger string, progress func(EnrollStep)) error {
	swipe := false
	if dev, err := f.device(); err == nil {
		if v, err := dev.GetProperty(fprintDeviceIf + ".scan-type"); err == nil {
			s, _ := v.Value().(string)
			swipe = s == "swipe"
		}
	}
	return f.claimed(func(dev dbus.BusObject) error {
		// fprintd's own unique name: EnrollStatus from anyone else is
		// ignored.
		var owner string
		if err := f.conn.BusObject().Call("org.freedesktop.DBus.GetNameOwner", 0, fprintName).Store(&owner); err != nil {
			return err
		}
		opts := []dbus.MatchOption{dbus.WithMatchObjectPath(dev.Path()), dbus.WithMatchInterface(fprintDeviceIf), dbus.WithMatchMember("EnrollStatus")}
		if err := f.conn.AddMatchSignal(opts...); err != nil {
			return err
		}
		defer f.conn.RemoveMatchSignal(opts...)
		sig := make(chan *dbus.Signal, 16)
		f.conn.Signal(sig)
		defer f.conn.RemoveSignal(sig)

		if err := call(dev, fprintDeviceIf+".EnrollStart", finger).Err; err != nil {
			return err
		}
		defer dev.Call(fprintDeviceIf+".EnrollStop", 0)
		passed := 0
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case s := <-sig:
				if s == nil || s.Sender != owner || s.Path != dev.Path() || s.Name != fprintDeviceIf+".EnrollStatus" || len(s.Body) < 2 {
					continue
				}
				result, _ := s.Body[0].(string)
				if result == "enroll-stage-passed" {
					passed++
				}
				msg, done, err := enrollMessage(result, swipe)
				progress(EnrollStep{Passed: passed, Message: msg, Done: done})
				if done {
					return err
				}
			}
		}
	})
}

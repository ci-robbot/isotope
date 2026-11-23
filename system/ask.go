package main

import (
	"encoding/json"
	"fmt"
	"image"
	"io"
	"os"
	"strconv"
	"strings"

	"gioui.org/app"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/kairos-community/isotope/guikit"
)

// `isotope-system ask KIND ...` is one small dialog in a window of its own.
// The tray runs it to ask a question on behalf of a NetworkManager or OBEX
// agent. The answer is JSON on stdout. Exit 0 is an answer; exit 1 is Cancel
// or a closed window.

const askUsage = `usage: isotope-system ask vpn-secret --name NAME --fields password[,otp] [--message TEXT]
       isotope-system ask bluetooth-file --device NAME --file NAME --size BYTES`

// askSpec is a parsed `ask` command line.
type askSpec struct {
	kind    string // "vpn-secret" or "bluetooth-file"
	name    string // the connection
	message string
	fields  []string
	device  string
	file    string
	size    uint64
}

// parseAsk reads the arguments after "ask".
func parseAsk(args []string) (askSpec, error) {
	if len(args) == 0 {
		return askSpec{}, fmt.Errorf("no kind of question")
	}
	s := askSpec{kind: args[0]}
	if s.kind != "vpn-secret" && s.kind != "bluetooth-file" {
		return s, fmt.Errorf("%q is not a kind of question", s.kind)
	}
	rest := args[1:]
	for len(rest) > 0 {
		if len(rest) < 2 || !strings.HasPrefix(rest[0], "--") {
			return s, fmt.Errorf("bad argument %q", rest[0])
		}
		flag, val := rest[0], rest[1]
		rest = rest[2:]
		switch {
		case flag == "--name" && s.kind == "vpn-secret":
			s.name = val
		case flag == "--message" && s.kind == "vpn-secret":
			s.message = val
		case flag == "--fields" && s.kind == "vpn-secret":
			for _, f := range strings.Split(val, ",") {
				if f = strings.TrimSpace(f); f != "" {
					s.fields = append(s.fields, f)
				}
			}
		case flag == "--device" && s.kind == "bluetooth-file":
			s.device = val
		case flag == "--file" && s.kind == "bluetooth-file":
			s.file = val
		case flag == "--size" && s.kind == "bluetooth-file":
			n, err := strconv.ParseUint(val, 10, 64)
			if err != nil {
				return s, fmt.Errorf("--size %q is not a number", val)
			}
			s.size = n
		default:
			return s, fmt.Errorf("%s does not take %s", s.kind, flag)
		}
	}
	if s.kind == "vpn-secret" && len(s.fields) == 0 {
		return s, fmt.Errorf("vpn-secret needs --fields")
	}
	if s.kind == "bluetooth-file" && s.file == "" {
		return s, fmt.Errorf("bluetooth-file needs --file")
	}
	return s, nil
}

// askField is one typed field of a vpn-secret dialog.
type askField struct {
	name  string
	label string
	f     *guikit.Field
}

// askModel is the dialog and what it prints. finish runs once with the exit
// code, after the answer is written.
type askModel struct {
	spec   askSpec
	u      *UI
	d      *dialog
	fields []*askField
	out    io.Writer
	finish func(code int)
}

// askLabels are the words for the fields NetworkManager's secrets use.
var askLabels = map[string]string{
	"password":           "Password",
	"secondary_password": "Second password",
	"otp":                "One-time code",
	"private-key":        "Private key",
}

// askVisible are the fields shown as typed: a one-time code is no secret.
var askVisible = map[string]bool{"otp": true}

func newAskModel(u *UI, spec askSpec, out io.Writer, finish func(int)) *askModel {
	m := &askModel{spec: spec, u: u, out: out, finish: finish}
	if spec.kind == "bluetooth-file" {
		m.d = m.fileDialog()
	} else {
		m.d = m.secretDialog()
	}
	return m
}

// answer prints v as JSON and finishes with exit code 0.
func (m *askModel) answer(v any) {
	b, _ := json.Marshal(v)
	fmt.Fprintf(m.out, "%s\n", b)
	m.finish(0)
}

func (m *askModel) secretDialog() *dialog {
	for _, name := range m.spec.fields {
		f := guikit.NewSecret()
		if askVisible[name] {
			f = guikit.NewField(true)
		}
		label := askLabels[name]
		if label == "" {
			label = name
		}
		m.fields = append(m.fields, &askField{name: name, label: label, f: f})
	}
	ready := func() bool {
		for _, f := range m.fields {
			if f.f.Editor.Text() == "" {
				return false
			}
		}
		return true
	}
	title := "Connect to " + m.spec.name
	if m.spec.name == "" {
		title = "Connect to the VPN"
	}
	body := m.spec.message
	if body == "" {
		body = "The VPN asks for this to connect."
	}
	focused := false
	var d *dialog
	d = newDialog(title, body, func(gtx layout.Context) layout.Dimensions {
		var c col
		for i, f := range m.fields {
			if f.f.Submitted(gtx) && ready() {
				m.u.pick(d, 1)
			}
			if i > 0 {
				c.gap(8)
			}
			c.add(func(gtx layout.Context) layout.Dimensions {
				return m.u.Labelled(gtx, f.label, f.f, "", func(gtx layout.Context) layout.Dimensions {
					return m.u.Input(gtx, f.f, guikit.StBody, 38, "", nil)
				})
			})
		}
		if !focused && len(m.fields) > 0 {
			focused = true
			gtx.Execute(focus(&m.fields[0].f.Editor))
		}
		return c.layout(gtx)
	}, 1,
		choice{Label: "Cancel", Kind: guikit.ButtonSecondary, Do: func() { m.finish(1) }},
		choice{Label: "Connect", Kind: guikit.ButtonPrimary, Enabled: ready, Do: func() {
			got := map[string]string{}
			for _, f := range m.fields {
				got[f.name] = f.f.Editor.Text()
			}
			m.answer(got)
		}})
	return d
}

func (m *askModel) fileDialog() *dialog {
	who := m.spec.device
	if who == "" {
		who = "A Bluetooth device"
	}
	body := fmt.Sprintf("%s wants to send you %s", who, m.spec.file)
	if m.spec.size > 0 {
		body += " (" + humanSize(m.spec.size) + ")"
	}
	body += ". The file goes to your Downloads folder."
	return newDialog("Receive a file?", body, nil, 0,
		choice{Label: "Decline", Kind: guikit.ButtonSecondary, Do: func() { m.answer(map[string]bool{"accept": false}) }},
		choice{Label: "Accept", Kind: guikit.ButtonPrimary, Do: func() { m.answer(map[string]bool{"accept": true}) }})
}

// layout draws one frame of the ask window.
func (m *askModel) layout(gtx layout.Context) layout.Dimensions {
	u := m.u
	gtx = guikit.Scaled(gtx, u.scale)
	guikit.FillRect(gtx, image.Rectangle{Max: gtx.Constraints.Max}, guikit.ColWindow)
	// Esc picks the dialog's first choice, as in every dialog.
	u.mu.Lock()
	u.dlg = m.d // pick clears it; no Invalidate, or every frame asks for the next
	u.mu.Unlock()
	guikit.WindowKeys(gtx, &u.scale, func() { u.pick(m.d, m.d.cancel) })
	m.d.update(gtx, u)
	m.d.layout(gtx, u)
	return layout.Dimensions{Size: gtx.Constraints.Max}
}

// runAsk is `isotope-system ask ...`.
func runAsk(args []string) int {
	spec, err := parseAsk(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "isotope-system: %v\n%s\n", err, askUsage)
		return 2
	}
	win := new(app.Window)
	win.Option(app.Title("Isotope"), app.Size(unit.Dp(560), unit.Dp(360)), app.Decorated(true))
	u := &UI{Theme: &guikit.Theme{Shaper: guikit.NewShaper()}, scale: 1, hooks: Hooks{Invalidate: win.Invalidate}}
	m := newAskModel(u, spec, os.Stdout, func(code int) { os.Exit(code) })
	go func() {
		var ops op.Ops
		for {
			switch e := win.Event().(type) {
			case app.DestroyEvent:
				os.Exit(1)
			case app.FrameEvent:
				gtx := app.NewContext(&ops, e)
				m.layout(gtx)
				e.Frame(gtx.Ops)
			}
		}
	}()
	app.Main()
	return 1
}

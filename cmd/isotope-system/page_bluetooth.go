package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/guikit"
)

func init() {
	pageMakers[PageBluetooth] = func(u *UI) pageView {
		return &btPage{u: u, clicks: map[string]*widget.Clickable{}, profiles: map[string][]Profile{}}
	}
}

// logf writes a line to stderr, as main does.
func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "isotope-system: "+format+"\n", args...)
}

// scanTime is how long "Search for devices" scans.
const scanTime = 30 * time.Second

// btPage: the adapter on and off, the devices BlueZ knows with connect,
// pair and forget, their battery, and the codec of an audio device.
type btPage struct {
	u    *UI
	list widget.List

	// Under u.mu.
	st       BTState
	loaded   bool
	loading  bool
	err      string
	working  string
	sending  btSend               // the file going out now
	profiles map[string][]Profile // by device path, connected audio devices

	prompt *btPrompt
	// poll is how often a running scan refreshes the list; window is how
	// long a scan lasts (scanTime). The tests shorten both.
	poll, window time.Duration

	clicks                 map[string]*widget.Clickable
	power, search, refresh widget.Clickable
}

// btSend is a file going to one device, for its row.
type btSend struct {
	Dev         string // device path; "" when nothing is sent
	Sent, Total int64
}

// btSendText is the row's line for a send: a percentage, or the bytes sent
// when the size is unknown.
func btSendText(sent, total int64) string {
	if sent < 0 {
		return "Waiting for the device to accept…"
	}
	if total > 0 {
		return fmt.Sprintf("Sending… %d%%", min(sent*100/total, 100))
	}
	return "Sending… " + byteSize(sent) + " sent"
}

// byteSize is n as KB, MB or GB, in plain words.
func byteSize(n int64) string {
	const k = 1000
	switch {
	case n < k:
		return fmt.Sprintf("%d B", n)
	case n < k*k:
		return fmt.Sprintf("%.0f KB", float64(n)/k)
	case n < k*k*k:
		return fmt.Sprintf("%.1f MB", float64(n)/(k*k))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(k*k*k))
}

// click is the button for key (a device path and a label).
func (p *btPage) click(key string) *widget.Clickable {
	c, ok := p.clicks[key]
	if !ok {
		c = new(widget.Clickable)
		p.clicks[key] = c
	}
	return c
}

// btRow is a device as the page shows it.
type btRow struct {
	Dev     BTDevice
	Sub     string
	Actions []string  // button labels, in order
	Codecs  []Profile // the sound-quality choice; empty for none
}

// btView is what the page says, as strings, so a test reads it.
type btView struct {
	Headline string
	Rows     []btRow
}

var btKinds = map[string]string{
	"audio-headset": "Headset", "audio-headphones": "Headphones", "audio-card": "Speaker",
	"input-keyboard": "Keyboard", "input-mouse": "Mouse", "input-gaming": "Game controller",
	"input-tablet": "Tablet", "phone": "Phone", "computer": "Computer", "printer": "Printer",
}

func btKind(icon string) string {
	if k, ok := btKinds[icon]; ok {
		return k
	}
	return ""
}

func isAudio(d BTDevice) bool { return strings.HasPrefix(d.Icon, "audio-") }

func btSub(d BTDevice) string {
	var parts []string
	if k := btKind(d.Icon); k != "" {
		parts = append(parts, k)
	}
	switch {
	case d.Connected:
		parts = append(parts, "Connected")
	case d.Paired:
		parts = append(parts, "Paired")
	default:
		parts = append(parts, "Not paired")
	}
	if d.Battery >= 0 {
		parts = append(parts, fmt.Sprintf("Battery %d%%", d.Battery))
	}
	return strings.Join(parts, " · ")
}

// btActions are a device's buttons. "Send file…" is on every paired
// device: obexd opens its own link to the device, so it needs no
// connection first.
func btActions(d BTDevice) []string {
	switch {
	case d.Connected:
		return []string{"Send file…", "Disconnect", "Forget"}
	case d.Paired:
		return []string{"Connect", "Send file…", "Forget"}
	}
	return []string{"Pair"}
}

func btViewOf(have bool, st BTState, profiles map[string][]Profile) btView {
	switch {
	case !have || !st.Adapter:
		return btView{Headline: "No Bluetooth adapter"}
	case !st.Powered:
		return btView{Headline: "Bluetooth is off"}
	}
	v := btView{Headline: "Bluetooth is on"}
	if st.Discovering {
		v.Headline += " · searching for devices"
	}
	for _, d := range st.Devices {
		r := btRow{Dev: d, Sub: btSub(d), Actions: btActions(d)}
		if d.Connected {
			// One codec is no choice.
			if ps := profiles[d.Path]; len(ps) > 1 {
				r.Codecs = ps
			}
		}
		v.Rows = append(v.Rows, r)
	}
	return v
}

func (p *btPage) Shown() {
	if bt := p.u.hooks.Bluetooth; bt != nil && p.prompt == nil {
		p.prompt = newBTPrompt(p.u)
		bt.SetPairPrompt(p.prompt)
	}
	if p.poll == 0 {
		p.poll = 2 * time.Second
	}
	if p.window == 0 {
		p.window = scanTime
	}
	p.load()
}

// fetch reads the state and the codecs of the connected audio devices.
func (p *btPage) fetch() {
	u, bt := p.u, p.u.hooks.Bluetooth
	st, err := bt.State()
	profiles := map[string][]Profile{}
	if err == nil {
		for _, d := range st.Devices {
			if d.Connected && isAudio(d) && d.Address != "" {
				ps, perr := Profiles(d.Address)
				if perr != nil {
					logf("sound quality of %s: %v", d.Name, perr)
				}
				if len(ps) > 0 {
					profiles[d.Path] = ps
				}
			}
		}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if err != nil {
		p.err = "Bluetooth is not available: " + err.Error()
		return
	}
	p.st, p.profiles, p.err, p.loaded = st, profiles, "", true
}

func (p *btPage) load() {
	u := p.u
	if u.hooks.Bluetooth == nil {
		return
	}
	u.mu.Lock()
	if p.loading {
		u.mu.Unlock()
		return
	}
	p.loading = true
	u.mu.Unlock()
	u.async(func() {
		p.fetch()
		u.mu.Lock()
		p.loading = false
		u.mu.Unlock()
	})
}

// do runs a BlueZ change and reloads.
func (p *btPage) do(working string, f func(Bluetooth) error) {
	u, bt := p.u, p.u.hooks.Bluetooth
	if bt == nil {
		return
	}
	u.mu.Lock()
	p.working = working
	u.mu.Unlock()
	u.async(func() {
		err := f(bt)
		u.mu.Lock()
		p.working = ""
		u.mu.Unlock()
		if err != nil {
			u.tell("Bluetooth", err.Error())
		}
		p.load()
	})
}

// scan searches for 30 seconds and refreshes the list while it runs.
func (p *btPage) scan() {
	u, bt := p.u, p.u.hooks.Bluetooth
	if bt == nil {
		return
	}
	u.async(func() {
		if err := bt.Discover(p.window); err != nil {
			u.tell("Bluetooth", err.Error())
			return
		}
		deadline := time.Now().Add(p.window + p.window/6)
		for time.Now().Before(deadline) {
			p.fetch()
			u.hooks.Invalidate()
			u.mu.Lock()
			here := u.page == PageBluetooth
			u.mu.Unlock()
			if !here {
				return
			}
			// Keep going to the deadline: BlueZ may report Discovering a
			// moment after StartDiscovery returns.
			time.Sleep(p.poll)
		}
	})
}

// pair pairs in its own goroutine (do), because Pair blocks until the user
// has answered the prompts, which open dialogs on this window.
func (p *btPage) pair(d BTDevice) {
	p.do("Pairing with "+d.Name+"…", func(bt Bluetooth) error {
		if err := bt.Pair(d.Path); err != nil {
			return err
		}
		// A headset often connects by itself; ask anyway. The pairing
		// worked, so a failure here is not an error for the user.
		if err := bt.Connect(d.Path); err != nil {
			logf("connect after pairing %s: %v", d.Name, err)
		}
		return nil
	})
}

func (p *btPage) forget(d BTDevice) {
	p.u.ask("Forget "+d.Name+"?", "This computer no longer knows the device. To use it again, pair it again.",
		choice{Label: "Cancel", Kind: guikit.ButtonSecondary},
		choice{Label: "Forget", Kind: guikit.ButtonDanger, Do: func() {
			p.do("Forgetting "+d.Name+"…", func(bt Bluetooth) error { return bt.Forget(d.Path) })
		}})
}

// sendPath is the absolute path of the file typed, "" unless it is an
// existing regular file. A leading ~/ is the home directory; a relative path
// is relative to the home directory too, since the window has no working
// directory the user knows.
func sendPath(typed string) string {
	t := strings.TrimSpace(typed)
	if t == "" {
		return ""
	}
	if t == "~" || strings.HasPrefix(t, "~/") || !filepath.IsAbs(t) {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		t = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(t, "~"), "/"))
	}
	t = filepath.Clean(t)
	if fi, err := os.Stat(t); err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	return t
}

// send sends the file at path to d.
func (p *btPage) send(d BTDevice, path string) {
	u := p.u
	p.do("Sending "+filepath.Base(path)+" to "+d.Name+"…", func(bt Bluetooth) error {
		u.mu.Lock()
		p.sending = btSend{Dev: d.Path}
		u.mu.Unlock()
		defer func() {
			u.mu.Lock()
			p.sending = btSend{}
			u.mu.Unlock()
		}()
		err := bt.SendFile(d.Path, path, func(sent, total int64) {
			u.mu.Lock()
			p.sending = btSend{Dev: d.Path, Sent: sent, Total: total}
			u.mu.Unlock()
			u.hooks.Invalidate()
		})
		if errors.Is(err, errSendCanceled) {
			return nil // the user asked for it
		}
		return err
	})
}

// sendFile asks for the file's path, when there is no file picker, and
// sends it.
func (p *btPage) sendFile(d BTDevice) {
	u := p.u
	f := guikit.NewField(true)
	focused := false
	var dlg *dialog
	path := func() string { return sendPath(f.Editor.Text()) }
	send := func() {
		if path := path(); path != "" {
			p.send(d, path)
		}
	}
	dlg = newDialog("Send a file to "+d.Name, "", func(gtx layout.Context) layout.Dimensions {
		if f.Submitted(gtx) {
			u.pick(dlg, 1) // ignored while Send is off
		}
		if !focused {
			focused = true
			gtx.Execute(focus(&f.Editor))
		}
		return u.Labelled(gtx, "File", f, "The path of a file, for example ~/Pictures/photo.jpg.", func(gtx layout.Context) layout.Dimensions {
			return u.Input(gtx, f, guikit.StBody, 38, "", nil)
		})
	}, 1,
		choice{Label: "Cancel", Kind: guikit.ButtonSecondary},
		choice{Label: "Send", Kind: guikit.ButtonPrimary, Enabled: func() bool { return path() != "" }, Do: send})
	u.open(dlg)
}

func (p *btPage) Update(gtx layout.Context) {
	u := p.u
	u.mu.Lock()
	st, have, busy := p.st, u.hooks.Bluetooth != nil, p.working != ""
	view := btViewOf(have && p.loaded, st, p.profiles)
	u.mu.Unlock()
	if p.refresh.Clicked(gtx) {
		p.load()
	}
	if p.power.Clicked(gtx) {
		on := !st.Powered
		p.do("Turning Bluetooth "+map[bool]string{true: "on", false: "off"}[on]+"…", func(bt Bluetooth) error { return bt.SetPowered(on) })
	}
	if p.search.Clicked(gtx) && !busy {
		p.scan()
	}
	if p.click("send|cancel").Clicked(gtx) && have {
		u.hooks.Bluetooth.CancelSend()
	}
	for _, r := range view.Rows {
		d := r.Dev
		for _, a := range r.Actions {
			if !p.click(d.Path+"|"+a).Clicked(gtx) || busy {
				continue
			}
			switch a {
			case "Pair":
				p.pair(d)
			case "Connect":
				p.do("Connecting to "+d.Name+"…", func(bt Bluetooth) error { return bt.Connect(d.Path) })
			case "Disconnect":
				p.do("Disconnecting from "+d.Name+"…", func(bt Bluetooth) error { return bt.Disconnect(d.Path) })
			case "Forget":
				p.forget(d)
			case "Send file…":
				u.chooseFile(FileRequest{Title: "Send a file to " + d.Name, Accept: "Send"},
					func(path string) { p.send(d, path) }, func() { p.sendFile(d) })
			}
		}
		for _, c := range r.Codecs {
			if p.click(d.Path+"|codec|"+c.Name).Clicked(gtx) && !busy && !c.Active {
				p.setCodec(d, c)
			}
		}
	}
}

func (p *btPage) setCodec(d BTDevice, c Profile) {
	u := p.u
	u.mu.Lock()
	p.working = "Switching " + d.Name + " to " + c.Label + "…"
	u.mu.Unlock()
	u.async(func() {
		err := SetProfile(d.Address, c.Name)
		u.mu.Lock()
		p.working = ""
		u.mu.Unlock()
		if err != nil {
			u.tell("Sound quality", err.Error())
		}
		p.load()
	})
}

func (p *btPage) Footer() (leading, trailing []guikit.Action) {
	p.u.mu.Lock()
	loading := p.loading
	p.u.mu.Unlock()
	return nil, []guikit.Action{{Click: &p.refresh, Kind: guikit.ButtonSecondary, Label: "Refresh", Disabled: loading || p.u.hooks.Bluetooth == nil}}
}

func (p *btPage) Body(gtx layout.Context) layout.Dimensions {
	u := p.u
	u.mu.Lock()
	st, err, working, loaded, sending := p.st, p.err, p.working, p.loaded, p.sending
	view := btViewOf(u.hooks.Bluetooth != nil && loaded, st, p.profiles)
	u.mu.Unlock()
	return u.scrolled(gtx, &p.list, func(gtx layout.Context) layout.Dimensions {
		var c col
		c.add(u.Heading("Bluetooth", "Pair and connect headphones, speakers, keyboards and other devices."))
		c.gap(24)
		if err != "" {
			c.add(u.errorBox(err))
			return c.layout(gtx)
		}
		if u.hooks.Bluetooth != nil && !loaded {
			c.add(u.spinnerLine("Looking for the adapter…"))
			return c.layout(gtx)
		}
		power := "Turn off"
		if !st.Powered {
			power = "Turn on"
		}
		busy := working != ""
		acts := []guikit.Action{{Click: &p.power, Kind: guikit.ButtonSecondary, Label: power, Disabled: busy}}
		if st.Powered {
			acts = append(acts, guikit.Action{Click: &p.search, Kind: guikit.ButtonSecondary, Label: "Search for devices", Disabled: busy || st.Discovering})
		}
		if !st.Adapter || u.hooks.Bluetooth == nil {
			c.add(u.lead(view.Headline))
			c.gap(6)
			c.add(u.dim("This computer has no Bluetooth adapter, or it is switched off by a key or by rfkill."))
			return c.layout(gtx)
		}
		c.add(u.row(view.Headline, "", u.buttons(acts...)))
		c.gap(6)
		c.add(u.dim("Files sent to this computer are saved in Downloads."))
		if busy {
			c.gap(12)
			c.add(u.spinnerLine(working))
		}
		if len(view.Rows) == 0 {
			if st.Powered {
				c.gap(18)
				c.add(u.dim("No device yet. Put the device in pairing mode, then choose Search for devices."))
			}
			return c.layout(gtx)
		}
		c.gap(24)
		c.add(u.caps("DEVICES"))
		c.gap(10)
		c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
			var rows col
			for i, r := range view.Rows {
				if i > 0 {
					rows.gap(14)
				}
				var as []guikit.Action
				for _, a := range r.Actions {
					k := guikit.ButtonSecondary
					if a == "Forget" {
						k = guikit.ButtonGhost
					}
					as = append(as, guikit.Action{Click: p.click(r.Dev.Path + "|" + a), Kind: k, Label: a, Disabled: busy})
				}
				sub := r.Sub
				if sending.Dev == r.Dev.Path {
					sub = btSendText(sending.Sent, sending.Total)
					as = []guikit.Action{{Click: p.click("send|cancel"), Kind: guikit.ButtonSecondary, Label: "Cancel"}}
				}
				rows.add(u.row(r.Dev.Name, sub, u.buttons(as...)))
				if len(r.Codecs) > 0 {
					rows.gap(8)
					var cs []guikit.Action
					for _, cd := range r.Codecs {
						k := guikit.ButtonSecondary
						if cd.Active {
							k = guikit.ButtonPrimary
						}
						cs = append(cs, guikit.Action{Click: p.click(r.Dev.Path + "|codec|" + cd.Name), Kind: k, Label: cd.Label, Disabled: busy})
					}
					rows.add(u.row("Sound quality", "A higher quality uses more battery and needs a good signal.", u.buttons(cs...)))
				}
			}
			return rows.layout(gtx)
		}))
		return c.layout(gtx)
	})
}

// btPrompt is the page's PairPrompt. BlueZ calls it on godbus goroutines;
// each question opens a dialog and blocks its goroutine until the user
// answers or Cancel runs. Nothing here blocks the UI goroutine.
type btPrompt struct {
	u *UI

	mu    sync.Mutex
	gen   uint64          // counts Cancels; a question that began before one never opens
	cur   *dialog         // the dialog on screen for the current question
	reply func(any, bool) // answers the blocked goroutine; nil for Display
}

func newBTPrompt(u *UI) *btPrompt { return &btPrompt{u: u} }

type btReply struct {
	v  any
	ok bool
}

// ask opens the dialog build returns and waits for its answer. build gets
// the function the dialog's buttons answer with (the first answer counts).
func (b *btPrompt) ask(build func(answer func(any, bool)) *dialog) (any, bool) {
	b.Cancel() // a question never outlives the next one
	b.mu.Lock()
	gen := b.gen
	b.mu.Unlock()
	ch := make(chan btReply, 1)
	var once sync.Once
	answer := func(v any, ok bool) { once.Do(func() { ch <- btReply{v, ok} }) }
	d := build(answer)
	b.mu.Lock()
	if b.gen != gen {
		// A Cancel came while the dialog was built: it never opens.
		b.mu.Unlock()
		return nil, false
	}
	b.cur, b.reply = d, answer
	b.u.open(d)
	b.mu.Unlock()
	r := <-ch
	b.mu.Lock()
	if b.cur == d {
		b.cur, b.reply = nil, nil
	}
	b.mu.Unlock()
	return r.v, r.ok
}

// Cancel closes the open pairing dialog and unblocks its question with a
// refusal. BlueZ calls it, and Pair calls it again when it returns, so it
// is safe to call twice.
func (b *btPrompt) Cancel() {
	b.mu.Lock()
	d, reply := b.cur, b.reply
	b.cur, b.reply = nil, nil
	b.gen++
	b.mu.Unlock()
	if d != nil {
		b.u.closeIf(d)
	}
	if reply != nil {
		reply(nil, false)
	}
}

// yesNo is a two-button question.
func (b *btPrompt) yesNo(title, body, no, yes string) bool {
	_, ok := b.ask(func(answer func(any, bool)) *dialog {
		return newDialog(title, body, nil, 1,
			choice{Label: no, Kind: guikit.ButtonSecondary, Do: func() { answer(nil, false) }},
			choice{Label: yes, Kind: guikit.ButtonPrimary, Do: func() { answer(true, true) }})
	})
	return ok
}

func (b *btPrompt) Confirm(dev BTDevice, passkey uint32) bool {
	return b.yesNo("Pair with "+dev.Name+"?",
		fmt.Sprintf("Check that %s shows this code: %03d %03d", dev.Name, passkey/1000, passkey%1000), "No", "Yes")
}

func (b *btPrompt) Authorize(dev BTDevice) bool {
	return b.yesNo("Pair with "+dev.Name+"?", dev.Name+" wants to pair with this computer.", "No", "Yes")
}

// typed asks for text in a field; valid tells when Pair is allowed.
func (b *btPrompt) typed(dev BTDevice, body, label, hint string, valid func(string) bool) (string, bool) {
	v, ok := b.ask(func(answer func(any, bool)) *dialog {
		f := guikit.NewField(true)
		focused := false
		var d *dialog
		text := func() string { return strings.TrimSpace(f.Editor.Text()) }
		d = newDialog("Pair with "+dev.Name, body, func(gtx layout.Context) layout.Dimensions {
			if f.Submitted(gtx) && valid(text()) {
				b.u.pick(d, 1)
			}
			if !focused {
				focused = true
				gtx.Execute(focus(&f.Editor))
			}
			return b.u.Labelled(gtx, label, f, hint, func(gtx layout.Context) layout.Dimensions {
				return b.u.Input(gtx, f, guikit.StBody, 38, "", nil)
			})
		}, 1,
			choice{Label: "Cancel", Kind: guikit.ButtonSecondary, Do: func() { answer(nil, false) }},
			choice{Label: "Pair", Kind: guikit.ButtonPrimary, Enabled: func() bool { return valid(text()) },
				Do: func() { answer(text(), true) }})
		return d
	})
	s, _ := v.(string)
	return s, ok
}

func (b *btPrompt) PinCode(dev BTDevice) (string, bool) {
	return b.typed(dev, "Type the PIN of "+dev.Name+". It is often 0000 or 1234.", "PIN", "Up to 16 characters.",
		func(s string) bool { return s != "" && len(s) <= 16 })
}

func (b *btPrompt) Passkey(dev BTDevice) (uint32, bool) {
	s, ok := b.typed(dev, "Type the 6-digit code that "+dev.Name+" shows.", "Code", "Up to 6 digits.", func(s string) bool {
		n, err := strconv.ParseUint(s, 10, 32)
		return err == nil && n <= 999999
	})
	n, _ := strconv.ParseUint(s, 10, 32)
	return uint32(n), ok
}

// Display shows the code to type on the device. It does not wait; an empty
// code removes the dialog.
func (b *btPrompt) Display(dev BTDevice, code string) {
	b.Cancel()
	if code == "" {
		return
	}
	b.mu.Lock()
	gen := b.gen
	b.mu.Unlock()
	d := newDialog("Pair with "+dev.Name, "Type this code on "+dev.Name+", then press Enter: "+code, nil, 0,
		choice{Label: "OK", Kind: guikit.ButtonPrimary})
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.gen != gen {
		return
	}
	b.cur = d
	b.u.open(d)
}

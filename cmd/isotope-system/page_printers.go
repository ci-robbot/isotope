package main

import (
	"context"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/guikit"
	"github.com/kairos-community/isotope/pkg/i18n"
	"github.com/kairos-community/isotope/pkg/printing"
)

func init() {
	pageMakers[PagePrinters] = func(u *UI) pageView {
		return &printersPage{u: u, rows: map[string]*printerClicks{}, adds: map[string]*widget.Clickable{}, findTime: findTime}
	}
}

// findTime is how long "Search for printers" waits for answers. Avahi
// usually says it has them all much sooner.
const findTime = 8 * time.Second

var printersOnlyAdmin = i18n.Mark("Only an administrator can add or remove printers or choose the default. Anyone can print a test page.")

// printersPage: the CUPS queues with their state, the test page, the
// default and Remove; and the driverless printers Avahi finds, with Add.
// Reads as the user (cups.go, avahi.go); changes through isotope-updated.
type printersPage struct {
	u    *UI
	list widget.List

	// Under u.mu.
	printers  []Printer
	loaded    bool
	loadErr   string
	found     []FoundPrinter
	searching bool
	searched  bool
	findErr   string
	working   string

	findTime time.Duration
	rows     map[string]*printerClicks
	adds     map[string]*widget.Clickable
	search   widget.Clickable
}

type printerClicks struct{ test, def, rm widget.Clickable }

// printerRow is one queue as the page shows it.
type printerRow struct {
	P          Printer
	Title, Sub string
	// Badge is "Default" for the default printer.
	Badge string
	// CanTest, CanDefault and CanRemove: which buttons the row has.
	CanTest, CanDefault, CanRemove bool
}

// printerState is the queue's state in words.
func printerState(p Printer) string {
	var s string
	switch p.State {
	case printerProcessing:
		s = i18n.X("printer", "Printing")
	case printerStopped:
		s = i18n.X("printer", "Stopped")
	default:
		s = i18n.X("printer", "Ready")
	}
	if p.StateMessage != "" && p.State != printerIdle {
		s += ": " + p.StateMessage
	}
	if !p.Accepting {
		s += " · " + i18n.T("not accepting jobs")
	}
	return s
}

func joinDots(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " · ")
}

// printerRows is the queues as rows: what each says and which buttons it
// has. canChange is an administrator with the daemon there.
func printerRows(ps []Printer, canChange bool) []printerRow {
	var rows []printerRow
	for _, p := range ps {
		auto := ""
		if p.Automatic() {
			auto = i18n.T("Set up automatically while it is plugged in")
		}
		r := printerRow{P: p, Title: p.Title(), Sub: joinDots(p.Model, p.Location, printerState(p), auto)}
		if p.Default {
			r.Badge = i18n.X("printer", "Default")
		}
		r.CanTest = p.Accepting
		r.CanDefault = canChange && !p.Default
		// cups-browsed would make an automatic queue again at once.
		r.CanRemove = canChange && !p.Automatic()
		rows = append(rows, r)
	}
	return rows
}

// foundRow is one printer the search found.
type foundRow struct {
	F          FoundPrinter
	Title, Sub string
	// Status replaces the Add button: "Added" when a queue has it already,
	// "Needs a driver" when it takes no format CUPS can make.
	Status string
	CanAdd bool
}

// hasQueue says whether a queue already prints to f: the same URI, the
// same printer UUID in a dnssd:// URI, the same address and port (the
// queues cups-browsed makes, and ones added by hand), or the queue name it
// would get.
func hasQueue(f FoundPrinter, ps []Printer) bool {
	for _, p := range ps {
		switch {
		case p.DeviceURI == f.URI, p.Name == f.Queue:
			return true
		case f.UUID != "" && strings.Contains(strings.ToLower(p.DeviceURI), "uuid="+strings.ToLower(f.UUID)):
			return true
		case f.Address != "" && sameAddress(p.DeviceURI, f.Address, f.Port):
			return true
		}
	}
	return false
}

// sameAddress says whether an ipp:// or ipps:// device URI points at addr
// and port (631 when the URI has none).
func sameAddress(uri, addr string, port uint16) bool {
	u, err := url.Parse(uri)
	if err != nil || (u.Scheme != "ipp" && u.Scheme != "ipps") {
		return false
	}
	p := u.Port()
	if p == "" {
		p = "631"
	}
	a, b := net.ParseIP(u.Hostname()), net.ParseIP(addr)
	same := u.Hostname() == addr || (a != nil && b != nil && a.Equal(b))
	return same && p == strconv.Itoa(int(port))
}

func foundRows(fs []FoundPrinter, ps []Printer, canChange bool) []foundRow {
	var rows []foundRow
	for _, f := range fs {
		where := i18n.T("On the network")
		if f.USB {
			where = "USB"
		}
		r := foundRow{F: f, Title: f.Service, Sub: joinDots(f.Model, f.Location, where)}
		switch {
		case hasQueue(f, ps):
			r.Status = i18n.X("printer", "Added")
		case !f.Driverless:
			r.Status = i18n.T("Needs a driver")
		default:
			r.CanAdd = canChange
		}
		rows = append(rows, r)
	}
	return rows
}

// uniqueQueue is name, or name_2, name_3... when a queue has that name.
func uniqueQueue(name string, ps []Printer) string {
	taken := map[string]bool{}
	for _, p := range ps {
		taken[p.Name] = true
	}
	if !taken[name] {
		return name
	}
	for i := 2; ; i++ {
		suffix := "_" + strconv.Itoa(i)
		base := name
		if len(base)+len(suffix) > printing.MaxName {
			base = base[:printing.MaxName-len(suffix)]
		}
		if !taken[base+suffix] {
			return base + suffix
		}
	}
}

// canChange: an administrator, and isotope-updated there to ask.
func (p *printersPage) canChange() bool { return p.u.sys.Admin && p.u.hooks.Daemon != nil }

func (p *printersPage) Shown() {
	p.load()
	p.u.mu.Lock()
	first := !p.searched && !p.searching
	p.u.mu.Unlock()
	if first {
		p.find()
	}
}

// load reads the queues.
func (p *printersPage) load() {
	u, pr := p.u, p.u.hooks.Printing
	if pr == nil {
		return
	}
	u.async(func() {
		list, err := pr.Printers()
		u.mu.Lock()
		defer u.mu.Unlock()
		p.loaded = true
		if err != nil {
			p.loadErr = err.Error()
			return
		}
		p.printers, p.loadErr = list, ""
	})
}

// find searches the network for findTime.
func (p *printersPage) find() {
	u, pr := p.u, p.u.hooks.Printing
	if pr == nil {
		return
	}
	u.mu.Lock()
	if p.searching {
		u.mu.Unlock()
		return
	}
	p.searching = true
	u.mu.Unlock()
	u.async(func() {
		ctx, cancel := context.WithTimeout(context.Background(), p.findTime)
		defer cancel()
		found, err := pr.Find(ctx)
		u.mu.Lock()
		defer u.mu.Unlock()
		p.searching, p.searched = false, true
		if err != nil {
			p.findErr = err.Error()
			return
		}
		p.found, p.findErr = found, ""
	})
}

// change runs one daemon call with a working line, then reloads the
// queues or tells why it failed.
func (p *printersPage) change(working, failed string, call func(Daemon) error) {
	u, d := p.u, p.u.hooks.Daemon
	if d == nil || !u.sys.Admin {
		return
	}
	u.mu.Lock()
	p.working = working
	u.mu.Unlock()
	u.async(func() {
		err := call(d)
		u.mu.Lock()
		p.working = ""
		u.mu.Unlock()
		if err != nil {
			u.tell(failed, Describe(err))
		}
		p.load()
	})
}

func (p *printersPage) add(f FoundPrinter) {
	p.u.mu.Lock()
	name := uniqueQueue(f.Queue, p.printers)
	p.u.mu.Unlock()
	info := f.Service
	if printing.CheckText(info) != nil {
		info = ""
	}
	loc := f.Location
	if printing.CheckText(loc) != nil {
		loc = ""
	}
	p.change(i18n.Tf("Adding %s…", f.Service), i18n.Tf("Could not add %s", f.Service),
		func(d Daemon) error { return d.AddPrinter(name, f.URI, info, loc) })
}

func (p *printersPage) testPage(pr Printer) {
	u, cups := p.u, p.u.hooks.Printing
	if cups == nil {
		return
	}
	u.async(func() {
		id, err := cups.PrintTestPage(pr.Name)
		if err != nil {
			u.tell(i18n.T("Could not print a test page"), err.Error())
			return
		}
		u.tell(i18n.T("Test page sent"), i18n.Tf("Job %d is on its way to %s.", id, pr.Title()))
	})
}

func (p *printersPage) clicks(name string) *printerClicks {
	k, ok := p.rows[name]
	if !ok {
		k = &printerClicks{}
		p.rows[name] = k
	}
	return k
}

func (p *printersPage) addClick(uri string) *widget.Clickable {
	k, ok := p.adds[uri]
	if !ok {
		k = &widget.Clickable{}
		p.adds[uri] = k
	}
	return k
}

type printersSnap struct {
	printers                    []Printer
	found                       []FoundPrinter
	loaded, searching, searched bool
	loadErr, findErr, working   string
}

func (p *printersPage) snap() printersSnap {
	p.u.mu.Lock()
	defer p.u.mu.Unlock()
	return printersSnap{printers: append([]Printer(nil), p.printers...), found: append([]FoundPrinter(nil), p.found...),
		loaded: p.loaded, searching: p.searching, searched: p.searched,
		loadErr: p.loadErr, findErr: p.findErr, working: p.working}
}

func (p *printersPage) Update(gtx layout.Context) {
	u := p.u
	s := p.snap()
	if s.working != "" {
		return
	}
	if p.search.Clicked(gtx) {
		p.find()
	}
	for _, r := range printerRows(s.printers, p.canChange()) {
		r := r
		k := p.clicks(r.P.Name)
		if k.test.Clicked(gtx) && r.CanTest {
			p.testPage(r.P)
		}
		if k.def.Clicked(gtx) && r.CanDefault {
			p.change(i18n.Tf("Making %s the default…", r.Title), i18n.T("Could not change the default printer"),
				func(d Daemon) error { return d.SetDefaultPrinter(r.P.Name) })
		}
		if k.rm.Clicked(gtx) && r.CanRemove {
			u.ask(i18n.Tf("Remove %s?", r.Title), i18n.T("The printer is removed for every user of this computer, with the jobs waiting for it. It can be added again from the printers found below."),
				choice{Label: i18n.T("Cancel"), Kind: guikit.ButtonSecondary},
				choice{Label: i18n.T("Remove"), Kind: guikit.ButtonDanger, Do: func() {
					p.change(i18n.Tf("Removing %s…", r.Title), i18n.Tf("Could not remove %s", r.Title),
						func(d Daemon) error { return d.RemovePrinter(r.P.Name) })
				}})
		}
	}
	for _, r := range foundRows(s.found, s.printers, p.canChange()) {
		if p.addClick(r.F.URI).Clicked(gtx) && r.CanAdd {
			p.add(r.F)
		}
	}
}

func (p *printersPage) Footer() (leading, trailing []guikit.Action) { return nil, nil }

func (p *printersPage) Body(gtx layout.Context) layout.Dimensions {
	u := p.u
	s := p.snap()
	busy := s.working != ""
	return u.scrolled(gtx, &p.list, func(gtx layout.Context) layout.Dimensions {
		var c col
		c.add(u.Heading(i18n.T("Printers"), i18n.T("Printers that work without a driver: on the network (IPP Everywhere, AirPrint, Mopria) or on USB.")))
		c.gap(20)
		if u.hooks.Printing == nil {
			c.add(u.errorBox(i18n.T("Printing is not available on this system.")))
			return c.layout(gtx)
		}
		if !u.sys.Admin {
			c.add(u.notice(i18n.T(printersOnlyAdmin)))
			c.gap(14)
		} else if u.hooks.Daemon == nil {
			c.add(u.errorBox(i18n.T("The update service (isotope-updated) is not available: printers cannot be added or removed.")))
			c.gap(14)
		}
		if busy {
			c.add(u.spinnerLine(s.working))
			c.gap(14)
		}
		c.add(u.caps(i18n.T("Printers")))
		c.gap(10)
		switch {
		case s.loadErr != "":
			c.add(u.errorBox(capitalize(s.loadErr) + "."))
		case !s.loaded:
			c.add(u.spinnerLine(i18n.T("Reading the printers…")))
		case len(s.printers) == 0:
			c.add(u.panel(u.dim(i18n.T("No printer yet. Add one of the printers found below."))))
		default:
			c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
				var rows col
				for i, r := range printerRows(s.printers, p.canChange()) {
					if i > 0 {
						rows.gap(12)
					}
					rows.add(p.printerRow(r, busy))
				}
				return rows.layout(gtx)
			}))
		}
		c.gap(24)
		c.add(u.caps(i18n.T("Found on the network and USB")))
		c.gap(10)
		c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
			var rows col
			found := foundRows(s.found, s.printers, p.canChange())
			switch {
			case s.searching:
				rows.add(u.spinnerLine(i18n.T("Searching for printers…")))
			case s.findErr != "":
				rows.add(u.errorBox(capitalize(s.findErr) + "."))
			case s.searched && len(found) == 0:
				rows.add(u.dim(i18n.T("No printer found. Turn the printer on and connect it to this network, or plug it in with USB, then search again.")))
			}
			for _, r := range found {
				rows.gap(12)
				rows.add(p.foundRow(r, busy))
			}
			rows.gap(12)
			rows.add(u.buttons(guikit.Action{Click: &p.search, Kind: guikit.ButtonSecondary, Label: i18n.T("Search again"), Disabled: s.searching || busy}))
			return rows.layout(gtx)
		}))
		return c.layout(gtx)
	})
}

func (p *printersPage) printerRow(r printerRow, busy bool) layout.Widget {
	u := p.u
	k := p.clicks(r.P.Name)
	var as []guikit.Action
	if r.CanTest {
		as = append(as, guikit.Action{Click: &k.test, Kind: guikit.ButtonSecondary, Label: i18n.T("Print test page"), Disabled: busy})
	}
	if r.CanDefault {
		as = append(as, guikit.Action{Click: &k.def, Kind: guikit.ButtonSecondary, Label: i18n.T("Make default"), Disabled: busy})
	}
	if r.CanRemove {
		as = append(as, guikit.Action{Click: &k.rm, Kind: guikit.ButtonGhost, Label: i18n.T("Remove…"), Disabled: busy})
	}
	return func(gtx layout.Context) layout.Dimensions {
		var c col
		c.add(u.row(r.Title, r.Sub, func(gtx layout.Context) layout.Dimensions {
			if r.Badge == "" {
				return layout.Dimensions{}
			}
			return u.Label(gtx, guikit.StSmall, u.Colors().Accent, r.Badge)
		}))
		if len(as) > 0 {
			c.gap(8)
			c.add(func(gtx layout.Context) layout.Dimensions { return wrapButtons(gtx, u, as) })
		}
		return c.layout(gtx)
	}
}

func (p *printersPage) foundRow(r foundRow, busy bool) layout.Widget {
	u := p.u
	var right layout.Widget
	switch {
	case r.Status != "":
		status := r.Status
		right = func(gtx layout.Context) layout.Dimensions {
			return u.Label(gtx, guikit.StSmall, u.Colors().Dim, status)
		}
	case r.CanAdd:
		right = u.buttons(guikit.Action{Click: p.addClick(r.F.URI), Kind: guikit.ButtonPrimary, Label: i18n.T("Add"), Disabled: busy})
	}
	return u.row(r.Title, r.Sub, right)
}

// capitalize starts an error's sentence with a capital.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

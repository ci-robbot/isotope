package flatpak

import (
	"bufio"
	"compress/gzip"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// App is one app of the catalog.
type App struct {
	ID, Name, Summary, Description string
	Developer, License             string
	// Categories are the freedesktop categories it lists.
	Categories []string
	Keywords   []string
	// Icon is the 64x64 icon's file, "" when the catalog has none cached.
	Icon     string
	Verified bool
	// Release is the newest release: its version and what it changed.
	Release Release
	// Ref is Flathub's ref of the app.
	Ref Ref
	// Runtime is the runtime the app uses, ID/ARCH/BRANCH.
	Runtime string
}

// Release is one entry of an app's release notes.
type Release struct {
	Version     string
	Time        int64
	Description string
}

// Catalog is Flathub's appstream data as flatpak keeps it for the user:
// the desktop and console apps (not the runtimes and add-ons), by ID.
type Catalog struct {
	Apps []App
	byID map[string]int
}

// Get is the app with id.
func (c *Catalog) Get(id string) (App, bool) {
	if c == nil {
		return App{}, false
	}
	i, ok := c.byID[id]
	if !ok {
		return App{}, false
	}
	return c.Apps[i], true
}

// LoadCatalog reads dir's appstream.xml.gz (or appstream.xml) and resolves
// the icons under dir/icons/64x64.
func LoadCatalog(dir string) (*Catalog, error) {
	var r io.Reader
	f, err := os.Open(filepath.Join(dir, "appstream.xml.gz"))
	if err == nil {
		defer f.Close()
		gz, err := gzip.NewReader(bufio.NewReader(f))
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		r = gz
	} else {
		f, err = os.Open(filepath.Join(dir, "appstream.xml"))
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = bufio.NewReader(f)
	}
	return ParseCatalog(r, filepath.Join(dir, "icons", "64x64"))
}

// The XML of one <component>. Translations (xml:lang) are skipped.
type xmlText struct {
	Lang string `xml:"http://www.w3.org/XML/1998/namespace lang,attr"`
	Text string `xml:",chardata"`
}

type xmlInner struct {
	Lang  string `xml:"http://www.w3.org/XML/1998/namespace lang,attr"`
	Inner []byte `xml:",innerxml"`
}

type xmlIcon struct {
	Type   string `xml:"type,attr"`
	Width  string `xml:"width,attr"`
	Scale  string `xml:"scale,attr"`
	Name   string `xml:",chardata"`
	Height string `xml:"height,attr"`
}

type xmlRelease struct {
	Version     string     `xml:"version,attr"`
	Timestamp   string     `xml:"timestamp,attr"`
	Type        string     `xml:"type,attr"`
	Description []xmlInner `xml:"description"`
}

type xmlValue struct {
	Key   string `xml:"key,attr"`
	Value string `xml:",chardata"`
}

type xmlComponent struct {
	Type          string       `xml:"type,attr"`
	ID            string       `xml:"id"`
	Names         []xmlText    `xml:"name"`
	Summaries     []xmlText    `xml:"summary"`
	Descriptions  []xmlInner   `xml:"description"`
	DeveloperName []xmlText    `xml:"developer_name"`
	Developer     []xmlText    `xml:"developer>name"`
	License       string       `xml:"project_license"`
	Icons         []xmlIcon    `xml:"icon"`
	Categories    []string     `xml:"categories>category"`
	Keywords      []xmlText    `xml:"keywords>keyword"`
	Releases      []xmlRelease `xml:"releases>release"`
	Bundles       []struct {
		Type    string `xml:"type,attr"`
		Runtime string `xml:"runtime,attr"`
		Text    string `xml:",chardata"`
	} `xml:"bundle"`
	Custom   []xmlValue `xml:"custom>value"`
	Metadata []xmlValue `xml:"metadata>value"`
}

// untranslated is the first text without xml:lang.
func untranslated(ts []xmlText) string {
	for _, t := range ts {
		if t.Lang == "" {
			return strings.TrimSpace(t.Text)
		}
	}
	return ""
}

// ParseCatalog reads an appstream catalog. iconDir is where the cached
// 64x64 icons are.
func ParseCatalog(r io.Reader, iconDir string) (*Catalog, error) {
	d := xml.NewDecoder(r)
	c := &Catalog{byID: map[string]int{}}
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "component" {
			continue
		}
		var x xmlComponent
		if err := d.DecodeElement(&x, &se); err != nil {
			return nil, err
		}
		app, ok := appOf(x, iconDir)
		if !ok {
			continue
		}
		if i, dup := c.byID[app.ID]; dup {
			c.Apps[i] = app
			continue
		}
		c.byID[app.ID] = len(c.Apps)
		c.Apps = append(c.Apps, app)
	}
	return c, nil
}

func appOf(x xmlComponent, iconDir string) (App, bool) {
	switch x.Type {
	case "desktop-application", "desktop", "console-application":
	default:
		return App{}, false
	}
	var a App
	for _, b := range x.Bundles {
		if ref, ok := ParseRef(b.Text); ok && ref.IsApp() && (b.Type == "" || b.Type == "flatpak") {
			a.Ref, a.Runtime = ref, b.Runtime
			break
		}
	}
	if a.Ref.ID == "" {
		return App{}, false
	}
	a.ID = a.Ref.ID
	a.Name = untranslated(x.Names)
	if a.Name == "" {
		a.Name = a.ID
	}
	a.Summary = untranslated(x.Summaries)
	for _, dsc := range x.Descriptions {
		if dsc.Lang == "" {
			a.Description = descriptionText(dsc.Inner)
			break
		}
	}
	a.Developer = untranslated(x.Developer)
	if a.Developer == "" {
		a.Developer = untranslated(x.DeveloperName)
	}
	a.License = strings.TrimSpace(x.License)
	a.Categories = x.Categories
	for _, k := range x.Keywords {
		if k.Lang == "" {
			a.Keywords = append(a.Keywords, strings.TrimSpace(k.Text))
		}
	}
	for _, ic := range x.Icons {
		if ic.Type == "cached" && ic.Width == "64" && ic.Scale == "" && strings.TrimSpace(ic.Name) != "" {
			a.Icon = filepath.Join(iconDir, filepath.Base(strings.TrimSpace(ic.Name)))
			break
		}
	}
	for _, v := range append(x.Custom, x.Metadata...) {
		if v.Key == "flathub::verification::verified" && strings.TrimSpace(v.Value) == "true" {
			a.Verified = true
		}
	}
	a.Release = newestRelease(x.Releases)
	return a, true
}

// newestRelease is the newest stable release (by timestamp; the catalog
// lists the newest first, but not always).
func newestRelease(rs []xmlRelease) Release {
	var best Release
	found := false
	for _, r := range rs {
		if r.Type == "development" || r.Type == "snapshot" {
			continue
		}
		t, _ := strconv.ParseInt(r.Timestamp, 10, 64)
		if found && t <= best.Time {
			continue
		}
		best = Release{Version: r.Version, Time: t}
		for _, dsc := range r.Description {
			if dsc.Lang == "" {
				best.Description = descriptionText(dsc.Inner)
				break
			}
		}
		found = true
	}
	return best
}

// descriptionText is an appstream description as plain text: paragraphs
// split by a blank line, list items as "• item". Translated paragraphs
// (xml:lang) are skipped.
func descriptionText(inner []byte) string {
	d := xml.NewDecoder(strings.NewReader("<d>" + string(inner) + "</d>"))
	d.Strict = false
	var paras []string
	var cur strings.Builder
	skip := 0
	flush := func(prefix string) {
		t := strings.Join(strings.Fields(cur.String()), " ")
		cur.Reset()
		if t != "" {
			paras = append(paras, prefix+t)
		}
	}
	var stack []string
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if skip > 0 {
				skip++
				continue
			}
			for _, a := range t.Attr {
				if a.Name.Local == "lang" {
					skip = 1
				}
			}
			if skip == 0 {
				stack = append(stack, t.Name.Local)
			}
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			if len(stack) == 0 {
				continue
			}
			switch stack[len(stack)-1] {
			case "p":
				flush("")
			case "li":
				flush("• ")
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if skip == 0 {
				cur.Write(t)
			}
		}
	}
	flush("")
	var b strings.Builder
	for i, p := range paras {
		if i > 0 {
			if strings.HasPrefix(p, "• ") && strings.HasPrefix(paras[i-1], "• ") {
				b.WriteString("\n")
			} else {
				b.WriteString("\n\n")
			}
		}
		b.WriteString(p)
	}
	return b.String()
}

// The category chips of the Browse tab, and the freedesktop main
// categories each one shows.
var Categories = []struct {
	Name string
	Of   []string
}{
	{"Office", []string{"Office"}},
	{"Graphics", []string{"Graphics"}},
	{"Audio & video", []string{"AudioVideo", "Audio", "Video"}},
	{"Internet", []string{"Network"}},
	{"Development", []string{"Development"}},
	{"Games", []string{"Game"}},
	{"Education", []string{"Education", "Science"}},
	{"Utilities", []string{"Utility", "System"}},
}

// InCategory lists the apps of the chip name, verified first, then by name.
func (c *Catalog) InCategory(name string) []App {
	if c == nil {
		return nil
	}
	var of []string
	for _, cat := range Categories {
		if cat.Name == name {
			of = cat.Of
		}
	}
	var out []App
	for _, a := range c.Apps {
		for _, x := range a.Categories {
			if has(of, x) {
				out = append(out, a)
				break
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Verified != out[j].Verified {
			return out[i].Verified
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// Search finds the apps whose name, ID, summary or keywords contain every
// word of q, case-insensitively. Name matches come first: a name that is
// q, then one that starts with it, then one that contains it, then the
// rest; ties go to verified apps, then by name.
func (c *Catalog) Search(q string) []App {
	if c == nil {
		return nil
	}
	words := strings.FieldsFunc(strings.ToLower(q), func(r rune) bool { return unicode.IsSpace(r) })
	if len(words) == 0 {
		return nil
	}
	ql := strings.Join(words, " ")
	type hit struct {
		a    App
		rank int
	}
	var hits []hit
	for _, a := range c.Apps {
		name := strings.ToLower(a.Name)
		hay := name + "\n" + strings.ToLower(a.ID) + "\n" + strings.ToLower(a.Summary) + "\n" + strings.ToLower(strings.Join(a.Keywords, "\n"))
		all := true
		for _, w := range words {
			if !strings.Contains(hay, w) {
				all = false
				break
			}
		}
		if !all {
			continue
		}
		rank := 4
		switch {
		case name == ql:
			rank = 0
		case strings.HasPrefix(name, ql):
			rank = 1
		case strings.Contains(name, ql):
			rank = 2
		case strings.Contains(strings.ToLower(a.ID), ql):
			rank = 3
		}
		hits = append(hits, hit{a, rank})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].rank != hits[j].rank {
			return hits[i].rank < hits[j].rank
		}
		if hits[i].a.Verified != hits[j].a.Verified {
			return hits[i].a.Verified
		}
		return strings.ToLower(hits[i].a.Name) < strings.ToLower(hits[j].a.Name)
	})
	out := make([]App, len(hits))
	for i, h := range hits {
		out[i] = h.a
	}
	return out
}

// ReadPicks reads the IDs of Isotope Welcome's list of apps
// (/usr/share/isotope/first-run-apps.list: group|ID|label|on or off), in
// order: the Browse tab's "Isotope picks".
func ReadPicks(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var ids []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if f := strings.Split(l, "|"); len(f) == 4 && strings.TrimSpace(f[1]) != "" {
			ids = append(ids, strings.TrimSpace(f[1]))
		}
	}
	return ids, sc.Err()
}

// PicksFile is Isotope Welcome's list of apps.
const PicksFile = "/usr/share/isotope/first-run-apps.list"

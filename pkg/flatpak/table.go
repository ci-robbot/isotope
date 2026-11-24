package flatpak

import (
	"strconv"
	"strings"
)

// Ref is a flatpak ref: app/ID/ARCH/BRANCH or runtime/ID/ARCH/BRANCH.
type Ref struct {
	Kind, ID, Arch, Branch string
}

// The two kinds of ref.
const (
	KindApp     = "app"
	KindRuntime = "runtime"
)

// ParseRef reads a full ref (app/ID/ARCH/BRANCH). ok is false for anything
// else.
func ParseRef(s string) (Ref, bool) {
	f := strings.Split(strings.TrimSpace(s), "/")
	if len(f) != 4 || (f[0] != KindApp && f[0] != KindRuntime) || f[1] == "" {
		return Ref{}, false
	}
	return Ref{Kind: f[0], ID: f[1], Arch: f[2], Branch: f[3]}, true
}

// String is the full ref.
func (r Ref) String() string { return r.Kind + "/" + r.ID + "/" + r.Arch + "/" + r.Branch }

// Partial is ID/ARCH/BRANCH, how `flatpak list` names an app's runtime.
func (r Ref) Partial() string { return r.ID + "/" + r.Arch + "/" + r.Branch }

// IsApp says whether r is an app.
func (r Ref) IsApp() bool { return r.Kind == KindApp }

// rows splits flatpak's plain table output (no terminal: no header, one
// row per line, the cells separated by tabs) into rows of n cells. flatpak
// leaves out a trailing empty cell, so short rows are padded.
func rows(out []byte, n int) [][]string {
	var rs [][]string
	for _, l := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		cells := strings.Split(l, "\t")
		for len(cells) < n {
			cells = append(cells, "")
		}
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		rs = append(rs, cells[:n])
	}
	return rs
}

// ParseSize reads a size as GLib's g_format_size writes it ("944.3 kB",
// "1.5 MB", "117 bytes"; flatpak's tables put a no-break space before the
// unit) into bytes. ok is false when s is no size.
func ParseSize(s string) (int64, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, " ", " "))
	num, unit, found := strings.Cut(s, " ")
	if !found {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(num, ",", "."), 64)
	if err != nil || v < 0 {
		return 0, false
	}
	mult := map[string]float64{
		"byte": 1, "bytes": 1, "B": 1,
		"kB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12, "PB": 1e15,
		"KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40,
	}[strings.TrimSpace(unit)]
	if mult == 0 {
		return 0, false
	}
	return int64(v*mult + 0.5), true
}

// FormatSize writes bytes as GLib does (decimal units, one decimal), with a
// plain space: "1.2 GB", "340 MB" (whole numbers lose the ".0").
func FormatSize(n int64) string {
	if n < 1000 {
		if n == 1 {
			return "1 byte"
		}
		return strconv.FormatInt(n, 10) + " bytes"
	}
	units := []string{"kB", "MB", "GB", "TB", "PB"}
	v := float64(n)
	u := ""
	for _, x := range units {
		v /= 1000
		u = x
		if v < 1000 {
			break
		}
	}
	s := strconv.FormatFloat(v, 'f', 1, 64)
	s = strings.TrimSuffix(s, ".0")
	return s + " " + u
}

// options reads flatpak's Options column ("current,eol=REASON,eol-rebase=REF").
// The end-of-life reason can hold commas, so it runs to the next known key.
func options(s string) (eol, rebase string) {
	if i := strings.Index(s, "eol-rebase="); i >= 0 {
		rebase = s[i+len("eol-rebase="):]
		if j := strings.IndexByte(rebase, ','); j >= 0 {
			rebase = rebase[:j]
		}
		s = s[:i]
	}
	if i := strings.Index(s, "eol="); i >= 0 {
		eol = strings.TrimSuffix(strings.TrimSpace(s[i+len("eol="):]), ",")
	}
	return eol, rebase
}

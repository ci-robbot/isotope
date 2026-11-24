package flatpak

import (
	"regexp"
	"strconv"
	"strings"
)

// Progress is how far a job is.
type Progress struct {
	// Percent of the whole job, 0 to 100; -1 before the first figure.
	Percent int
	// Step and Steps: the operation flatpak is on, of how many (1 of 1
	// for a single install).
	Step, Steps int
	// Line is flatpak's last line, without its bar.
	Line string
}

// ProgressReader follows flatpak's plain progress lines. With a terminal
// flatpak redraws a table; without one (FLATPAK_FANCY_OUTPUT=0, or output
// to a file) it prints one line per change: "Installing 1/2…", then
// "Installing 1/2… ███▍   36%  905.4 kB/s", ..., "Installation complete.".
// Every operation goes from 0 to 100%, so the whole job is
// ((step-1)*100 + percent) / steps.
type ProgressReader struct {
	p Progress
}

var (
	stepRe    = regexp.MustCompile(`^(\S+?)(?: (\d+)/(\d+))?…`)
	percentRe = regexp.MustCompile(`(\d{1,3})%`)
)

// NewProgressReader starts at no figure.
func NewProgressReader() *ProgressReader {
	return &ProgressReader{p: Progress{Percent: -1, Step: 1, Steps: 1}}
}

// Line reads one line and returns the progress after it.
func (r *ProgressReader) Line(l string) Progress {
	l = strings.TrimSpace(l)
	if l == "" {
		return r.p
	}
	if m := stepRe.FindStringSubmatch(l); m != nil && isVerb(m[1]) {
		if m[2] != "" {
			r.p.Step, _ = strconv.Atoi(m[2])
			r.p.Steps, _ = strconv.Atoi(m[3])
		}
		pct := 0
		if mm := percentRe.FindStringSubmatch(l); mm != nil {
			pct, _ = strconv.Atoi(mm[1])
		}
		pct = min(max(pct, 0), 100)
		steps := max(r.p.Steps, 1)
		step := min(max(r.p.Step, 1), steps)
		whole := ((step-1)*100 + pct) / steps
		if whole > r.p.Percent {
			r.p.Percent = whole
		}
		r.p.Line = m[0]
		return r.p
	}
	r.p.Line = l
	return r.p
}

// isVerb: the words flatpak's plain transaction starts an operation's
// lines with (in English; a translated flatpak still has the figures).
func isVerb(w string) bool {
	switch w {
	case "Installing", "Updating", "Uninstalling":
		return true
	}
	// Any single word followed by "…" and a figure counts: a translation.
	return !strings.ContainsAny(w, ":.")
}

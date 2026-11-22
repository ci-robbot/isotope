package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// historyFile holds one line per install or rollback, tab-separated: unix
// time, kind, version, ref, digest, result. It lives under /usr/local because
// Kairos rebuilds /var at every boot. Any user may read it through D-Bus, so
// it holds no secret: a ref carries no credentials (ParseRef refuses them).
var historyFile = "/usr/local/isotope-update/history"

const (
	historyMax      = 200     // lines kept
	historyFieldMax = 512     // bytes in one field
	historyReadMax  = 4 << 20 // bytes read from the file
)

type historyEntry struct {
	Time int64
	// Kind is "install" or "rollback".
	Kind string
	// Result is "installed", "failed" or "cancelled"; "" for a rollback.
	Version, Ref, Digest, Result string
}

var historyMu sync.Mutex

// historyField makes s safe for one tab-separated field: no control
// character, at most historyFieldMax bytes.
func historyField(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if len(s) > historyFieldMax {
		s = strings.ToValidUTF8(s[:historyFieldMax], "")
	}
	return s
}

func (e historyEntry) line() string {
	return strings.Join([]string{
		strconv.FormatInt(e.Time, 10),
		historyField(e.Kind), historyField(e.Version), historyField(e.Ref),
		historyField(e.Digest), historyField(e.Result),
	}, "\t")
}

// readHistoryLines returns the well-formed entries, oldest first. A missing
// file is an empty history.
func readHistoryLines() ([]historyEntry, error) {
	f, err := os.Open(historyFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, historyReadMax))
	if err != nil {
		return nil, err
	}
	var out []historyEntry
	for _, l := range strings.Split(string(raw), "\n") {
		p := strings.Split(l, "\t")
		if len(p) != 6 {
			continue
		}
		t, err := strconv.ParseInt(p[0], 10, 64)
		if err != nil {
			continue
		}
		out = append(out, historyEntry{Time: t, Kind: p[1], Version: p[2], Ref: p[3], Digest: p[4], Result: p[5]})
	}
	if len(out) > historyMax {
		out = out[len(out)-historyMax:]
	}
	return out, nil
}

// appendHistory adds e and keeps the newest historyMax lines. The file is
// replaced by an atomic rename.
func appendHistory(e historyEntry) error {
	historyMu.Lock()
	defer historyMu.Unlock()
	old, err := readHistoryLines()
	if err != nil {
		return err
	}
	old = append(old, e)
	if len(old) > historyMax {
		old = old[len(old)-historyMax:]
	}
	var b strings.Builder
	for _, x := range old {
		b.WriteString(x.line())
		b.WriteByte('\n')
	}
	return writeRunFile(historyFile, b.String())
}

// readHistory returns the entries, newest first. A malformed line is skipped.
func readHistory() ([]historyEntry, error) {
	historyMu.Lock()
	defer historyMu.Unlock()
	h, err := readHistoryLines()
	for i, j := 0, len(h)-1; i < j; i, j = i+1, j-1 {
		h[i], h[j] = h[j], h[i]
	}
	return h, err
}

// recordHistory writes e. A failed write is logged and never fails the job.
func recordHistory(e historyEntry) {
	e.Time = time.Now().Unix()
	if err := appendHistory(e); err != nil {
		fmt.Fprintln(os.Stderr, "isotope: could not write the update history:", err)
	}
}

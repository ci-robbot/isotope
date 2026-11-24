package main

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kairos-community/isotope/pkg/flatpak"
	"github.com/kairos-community/isotope/pkg/i18n"
)

// The tray says when app updates are waiting: with automatic app updates
// off, one notification per set of pending updates; with them on, only
// for the updates the automatic run holds back because they ask for more
// permissions. Its button opens Isotope System's Apps page on the Updates
// tab. The set already announced is $XDG_STATE_HOME/isotope-update/
// app-updates-notified (flatpak.SetKey). A failure is logged, never a
// notification. A metered connection skips the look: it downloads
// Flathub's summary.

// The first look waits for the session and the network, as firmware's.
var (
	appsFirstCheck = 7 * time.Minute
	appsInterval   = 6 * time.Hour
)

// appsWatch is the watcher's seams.
type appsWatch struct {
	updates func(context.Context) ([]flatpak.Update, error)
	review  func(context.Context, []flatpak.Update) []flatpak.Pending
	auto    func() bool
	metered func() bool
	notify  func(summary, body, label string, invoked func()) error
	open    func(page string) error
	record  string
	log     *log.Logger
}

func appsRecord() string { return filepath.Join(stateDir(), "app-updates-notified") }

// watchApps runs the watcher for the session; it never returns.
func watchApps(logger *log.Logger, open func(page string) error) {
	c := flatpak.New()
	if !c.Available() {
		return
	}
	w := appsWatch{
		updates: c.Updates,
		review:  c.Review,
		auto:    flatpak.AutoEnabled,
		metered: func() bool {
			env, done, err := appsEnv()
			if err != nil {
				return false
			}
			defer done()
			m, err := env.Metered()
			return err == nil && m
		},
		notify: NotifyAction,
		open:   open,
		record: appsRecord(),
		log:    logger,
	}
	w.run(context.Background(), appsFirstCheck, appsInterval)
}

func (w appsWatch) run(ctx context.Context, first, every time.Duration) {
	t := time.NewTimer(first)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.once(ctx)
			t.Reset(every)
		}
	}
}

// once looks for updates and announces a set not announced before.
func (w appsWatch) once(ctx context.Context) {
	if w.metered != nil && w.metered() {
		return
	}
	ups, err := w.updates(ctx)
	if err != nil {
		if !errors.Is(err, flatpak.ErrNoFlatpak) {
			w.log.Printf("app updates: %v", err)
		}
		return
	}
	auto := w.auto()
	set := ups
	var held []flatpak.Pending
	if auto {
		_, held = flatpak.Split(w.review(ctx, ups))
		set = nil
		for _, h := range held {
			set = append(set, h.Update)
		}
	}
	if len(set) == 0 {
		return
	}
	key := flatpak.SetKey(set)
	if b, err := os.ReadFile(w.record); err == nil && strings.TrimSpace(string(b)) == key {
		return
	}
	summary, body := appsNotice(set, auto)
	err = w.notify(summary, body, i18n.T("Open Isotope System"), func() {
		if err := w.open("apps updates"); err != nil {
			w.log.Printf("opening Isotope System: %v", err)
		}
	})
	if err != nil {
		w.log.Printf("showing a notification: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(w.record), 0o755); err == nil {
		err = os.WriteFile(w.record, []byte(key+"\n"), 0o644)
		if err != nil {
			w.log.Printf("recording the app updates announced: %v", err)
		}
	}
}

// appsNotice is the notification's words: "3 app updates" and their
// names, or, with automatic updates on, the ones that need an OK.
func appsNotice(set []flatpak.Update, auto bool) (summary, body string) {
	names := make([]string, 0, len(set))
	for _, u := range set {
		names = append(names, orID(u.Name, u.Ref.ID))
	}
	list := names[0]
	switch {
	case len(names) == 2:
		list = i18n.Tf("%[1]s and %[2]s", names[0], names[1])
	case len(names) > 2:
		list = i18n.Nf("%[1]s, %[2]s and %[3]d more", "%[1]s, %[2]s and %[3]d more", len(names)-2, names[0], names[1], len(names)-2)
	}
	n := len(set)
	if auto {
		return i18n.Nf("An app update needs your OK", "%d app updates need your OK", n, n),
			i18n.Nf("%s asks for more permissions. Isotope System's Apps page shows what it asks for.",
				"%s ask for more permissions. Isotope System's Apps page shows what they ask for.", n, list)
	}
	return i18n.Nf("%d app update", "%d app updates", n, n),
		i18n.Nf("%s can be updated. Isotope System's Apps page installs it.",
			"%s can be updated. Isotope System's Apps page installs them.", n, list)
}

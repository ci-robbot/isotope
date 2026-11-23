package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/kairos-community/isotope/pkg/installer"
)

// --- wizard flow -----------------------------------------------------------

// keys drives a wizard through a sequence of keystrokes. Runes are typed one
// character at a time; anything else is a named key ("enter", "esc", "down").
func keys(t *testing.T, m wizardModel, seq ...string) wizardModel {
	t.Helper()
	var tm tea.Model = m
	for _, s := range seq {
		var msg tea.KeyMsg
		switch s {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "up":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "ctrl+c":
			msg = tea.KeyMsg{Type: tea.KeyCtrlC}
		default:
			for _, r := range s {
				tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			}
			continue
		}
		tm, _ = tm.Update(msg)
	}
	return tm.(wizardModel)
}

func testDisks() []installer.Disk {
	return []installer.Disk{{Path: "/dev/sda", SizeGB: 120}, {Path: "/dev/vdb", SizeGB: 500}}
}

// clear replaces whatever the step seeded with the given text.
func clear(t *testing.T, m wizardModel, text string) wizardModel {
	t.Helper()
	m.in.SetValue(text)
	return m
}

// atHostname accepts the default keyboard and timezone and returns the
// wizard on the hostname step with an empty field.
func atHostname(t *testing.T, disks []installer.Disk) wizardModel {
	t.Helper()
	m := keys(t, newWizard(disks), "enter", "enter")
	if m.Page != installer.PageHostname {
		t.Fatalf("step = %v after accepting keyboard and timezone, want installer.PageHostname", m.Page)
	}
	return clear(t, m, "")
}

func TestWizardHappyPathCollectsEveryField(t *testing.T) {
	m := atHostname(t, testDisks())
	m = keys(t, m, "box", "enter", "ada", "enter", "hunter2", "enter", "adalovelace", "enter")

	if m.Page != installer.PageDisk {
		t.Fatalf("step = %v, want installer.PageDisk", m.Page)
	}
	m = keys(t, m, "down", "enter")
	if m.Page != installer.PageEncrypt {
		t.Fatalf("step = %v, want installer.PageEncrypt", m.Page)
	}
	m = keys(t, m, "enter")
	if m.Page != installer.PageReview {
		t.Fatalf("step = %v, want installer.PageReview", m.Page)
	}
	want := installer.Choice{Hostname: "box", Username: "ada", Password: "hunter2", Github: "adalovelace", Disk: "/dev/vdb",
		KeyboardLayout: "us", Timezone: "UTC"}
	if m.Choice != want {
		t.Errorf("choice = %+v, want %+v", m.Choice, want)
	}
	if m.done {
		t.Error("wizard reported done before the review gate was confirmed")
	}

	m = keys(t, m, "enter")
	if !m.done {
		t.Error("enter on the review screen should finish the wizard")
	}
}

// TestWizardHostnameDefaults pins that an emptied hostname field falls back to
// the shell installer's default rather than writing an empty hostname.
func TestWizardHostnameDefaults(t *testing.T) {
	m := atHostname(t, testDisks())
	m = keys(t, m, "enter")
	if m.Choice.Hostname != installer.DefaultHostname {
		t.Errorf("Hostname = %q, want %q", m.Choice.Hostname, installer.DefaultHostname)
	}
}

// TestWizardEscRestoresPreviousValue is the whole point of back-navigation: a
// user who mistypes their username and goes back must find their hostname
// still there, not a blank field.
func TestWizardEscRestoresPreviousValue(t *testing.T) {
	m := atHostname(t, testDisks())
	m = keys(t, m, "box", "enter", "ada", "enter") // now on password
	m = keys(t, m, "esc")                          // back to username
	if m.Page != installer.PageUser {
		t.Fatalf("step = %v, want installer.PageUser", m.Page)
	}
	if got := m.in.Value(); got != "ada" {
		t.Errorf("username field = %q, want %q", got, "ada")
	}
	m = keys(t, m, "esc") // back to hostname
	if m.Page != installer.PageHostname {
		t.Fatalf("step = %v, want installer.PageHostname", m.Page)
	}
	if got := m.in.Value(); got != "box" {
		t.Errorf("hostname field = %q, want %q", got, "box")
	}
	m = keys(t, m, "esc", "esc") // timezone, then keyboard
	if m.Page != installer.PageKeyboard {
		t.Fatalf("step = %v, want installer.PageKeyboard", m.Page)
	}
	// Esc on the first step must not underflow past installer.PageKeyboard.
	m = keys(t, m, "esc")
	if m.Page != installer.PageKeyboard {
		t.Errorf("step = %v after esc on the first step, want installer.PageKeyboard", m.Page)
	}
}

func TestWizardEscFromReviewGoesBackToDisk(t *testing.T) {
	m := atHostname(t, testDisks())
	m = keys(t, m, "box", "enter", "ada", "enter", "pw", "enter", "enter", "enter", "enter")
	if m.Page != installer.PageReview {
		t.Fatalf("step = %v, want installer.PageReview", m.Page)
	}
	m = keys(t, m, "esc")
	if m.Page != installer.PageEncrypt {
		t.Errorf("step = %v, want installer.PageEncrypt", m.Page)
	}
	m = keys(t, m, "esc")
	if m.Page != installer.PageDisk {
		t.Errorf("step = %v, want installer.PageDisk", m.Page)
	}
	if m.done {
		t.Error("esc from review must not finish the wizard")
	}
}

func TestWizardPasswordIsMasked(t *testing.T) {
	m := atHostname(t, testDisks())
	m = keys(t, m, "enter", "ada", "enter", "hunter2")
	if m.Page != installer.PagePass {
		t.Fatalf("step = %v, want installer.PagePass", m.Page)
	}
	if strings.Contains(m.View(), "hunter2") {
		t.Errorf("password is echoed in plaintext:\n%s", m.View())
	}
	if m.in.Value() != "hunter2" {
		t.Errorf("password value = %q, want hunter2", m.in.Value())
	}
}

func TestWizardUsernameValidation(t *testing.T) {
	cases := []struct {
		name  string
		input string
		ok    bool
	}{
		{"plain", "ada", true},
		{"digits and dash", "ada-l0velace", true},
		{"leading underscore", "_ada", true},
		{"empty", "", false},
		{"newline breaks the save.ini block scalar", "ada\nsession_index = 9", false},
		{"space", "ada lovelace", false},
		{"uppercase", "Ada", false},
		{"leading digit", "1ada", false},
		{"colon", "ada:x", false},
		{"too long", strings.Repeat("a", 33), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := atHostname(t, testDisks())
			m = keys(t, m, "enter") // past hostname, now on username
			m = clear(t, m, tc.input)
			m = keys(t, m, "enter")

			if tc.ok {
				if m.Page != installer.PagePass {
					t.Fatalf("step = %v, want installer.PagePass (input %q rejected: %q)", m.Page, tc.input, m.err)
				}
				if m.Choice.Username != tc.input {
					t.Errorf("Username = %q, want %q", m.Choice.Username, tc.input)
				}
				return
			}
			if m.Page != installer.PageUser {
				t.Fatalf("step = %v, want to stay on installer.PageUser for input %q", m.Page, tc.input)
			}
			if m.err == "" {
				t.Errorf("no on-screen error shown for invalid username %q", tc.input)
			}
			if m.Choice.Username != "" {
				t.Errorf("invalid username %q was stored anyway", m.Choice.Username)
			}
			if !strings.Contains(m.View(), m.err) {
				t.Errorf("error %q is not rendered in the view:\n%s", m.err, m.View())
			}
		})
	}
}

func TestWizardPasswordIsRequired(t *testing.T) {
	m := atHostname(t, testDisks())
	m = keys(t, m, "enter", "ada", "enter")
	m = clear(t, m, "")
	m = keys(t, m, "enter")
	if m.Page != installer.PagePass {
		t.Fatalf("step = %v, want to stay on installer.PagePass", m.Page)
	}
	if m.err == "" {
		t.Error("no on-screen error shown for an empty password")
	}
}

func TestWizardGithubIsOptional(t *testing.T) {
	m := atHostname(t, testDisks())
	m = keys(t, m, "enter", "ada", "enter", "pw", "enter")
	if m.Page != installer.PageGithub {
		t.Fatalf("step = %v, want installer.PageGithub", m.Page)
	}
	m = keys(t, m, "enter")
	if m.Page != installer.PageDisk {
		t.Fatalf("step = %v, want installer.PageDisk after skipping github", m.Page)
	}
	if m.Choice.Github != "" {
		t.Errorf("Github = %q, want empty", m.Choice.Github)
	}
}

// TestWizardGithubValidation is the interactive half of the injection fix.
// ISOTOPE_GITHUB is not the only way a handle reaches the renderer — anything
// typed here does too, and this step used to store its value unchecked.
func TestWizardGithubValidation(t *testing.T) {
	cases := []struct {
		name  string
		input string
		ok    bool
	}{
		{"ordinary handle", "adalovelace", true},
		{"hyphen and digits", "ada-l0velace", true},
		{"empty stays optional", "", true},
		{"39 chars", strings.Repeat("a", 39), true},
		// textinput flattens the newlines to spaces, so what commitField is
		// actually asked to accept here is the flattened payload — still an
		// injection attempt, still rejected, and rejected by our check rather
		// than by the widget's incidental scrubbing.
		{"flattened injection payload", "ada stages:   network:     - name: pwn", false},
		{"leading hyphen", "-ada", false},
		{"trailing hyphen", "ada-", false},
		{"space", "ada lovelace", false},
		{"underscore", "ada_lovelace", false},
		{"40 chars", strings.Repeat("a", 40), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := atHostname(t, testDisks())
			m = keys(t, m, "enter", "ada", "enter", "pw", "enter")
			if m.Page != installer.PageGithub {
				t.Fatalf("step = %v, want installer.PageGithub", m.Page)
			}
			m = clear(t, m, tc.input)
			m = keys(t, m, "enter")

			if tc.ok {
				if m.Page != installer.PageDisk {
					t.Fatalf("step = %v, want installer.PageDisk (input %q rejected: %q)", m.Page, tc.input, m.err)
				}
				if m.Choice.Github != tc.input {
					t.Errorf("Github = %q, want %q", m.Choice.Github, tc.input)
				}
				return
			}
			if m.Page != installer.PageGithub {
				t.Fatalf("step = %v, want to stay on installer.PageGithub for input %q", m.Page, tc.input)
			}
			if m.err == "" {
				t.Errorf("no on-screen error shown for invalid github handle %q", tc.input)
			}
			if m.Choice.Github != "" {
				t.Errorf("invalid github handle %q was stored anyway", m.Choice.Github)
			}
			if !strings.Contains(m.View(), m.err) {
				t.Errorf("error %q is not rendered in the view:\n%s", m.err, m.View())
			}
		})
	}
}

// TestWizardNoDisksHalts: with nothing installable there is no safe next step,
// so the wizard must say so rather than advance to an empty picker whose enter
// key would index an empty slice.
func TestWizardNoDisksHalts(t *testing.T) {
	m := atHostname(t, nil)
	m = keys(t, m, "enter", "ada", "enter", "pw", "enter", "enter")
	if m.Page != installer.PageGithub {
		t.Fatalf("step = %v, want to stay on installer.PageGithub when no disk exists", m.Page)
	}
	if m.err == "" {
		t.Error("no on-screen error shown when no disk is installable")
	}
}

func TestWizardDiskSelectionClampsAtBothEnds(t *testing.T) {
	m := atHostname(t, testDisks())
	m = keys(t, m, "enter", "ada", "enter", "pw", "enter", "enter")
	if m.Page != installer.PageDisk {
		t.Fatalf("step = %v, want installer.PageDisk", m.Page)
	}
	m = keys(t, m, "up", "up") // already at the top
	if m.diskIdx != 0 {
		t.Errorf("diskIdx = %d after up at the top, want 0", m.diskIdx)
	}
	m = keys(t, m, "down", "down", "down") // only two disks
	if m.diskIdx != 1 {
		t.Errorf("diskIdx = %d after running off the bottom, want 1", m.diskIdx)
	}
	m = keys(t, m, "enter")
	if m.Choice.Disk != "/dev/vdb" {
		t.Errorf("Disk = %q, want /dev/vdb", m.Choice.Disk)
	}
}

// TestWizardReviewShowsTheTarget: the review gate is the last point before a
// disk is erased, so it must name the disk that is about to go.
func TestWizardReviewShowsTheTarget(t *testing.T) {
	m := atHostname(t, testDisks())
	m = keys(t, m, "box", "enter", "ada", "enter", "pw", "enter", "adalovelace", "enter", "enter", "enter")
	view := m.View()
	for _, want := range []string{"/dev/sda", "box", "ada", "github:adalovelace", "ERASES"} {
		if !strings.Contains(view, want) {
			t.Errorf("review view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "pw") {
		t.Errorf("review view leaks the password:\n%s", view)
	}
	// The target must appear in the labelled summary row, not only inside the
	// warning sentence — the summary is what a user scans before committing.
	var found bool
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "disk") && strings.Contains(line, "/dev/sda") {
			found = true
		}
	}
	if !found {
		t.Errorf("review view has no 'disk' summary row naming /dev/sda:\n%s", view)
	}
}

// TestWizardReviewOnlyConfirmsOnEnter: the review screen is the last gate
// before a disk is erased, so only the documented key may pass it. A stray
// keypress — someone leaning on the keyboard, a VT emitting junk — must not
// start an irreversible install.
func TestWizardReviewOnlyConfirmsOnEnter(t *testing.T) {
	base := atHostname(t, testDisks())
	base = keys(t, base, "box", "enter", "ada", "enter", "pw", "enter", "adalovelace", "enter", "enter", "enter")
	if base.Page != installer.PageReview {
		t.Fatalf("step = %v, want installer.PageReview", base.Page)
	}
	for _, k := range []string{"x", "y", "down", "up"} {
		if m := keys(t, base, k); m.done {
			t.Errorf("key %q confirmed the review gate; only enter may", k)
		}
	}
	if !keys(t, base, "enter").done {
		t.Error("enter did not confirm the review gate")
	}
}

func TestWizardCtrlCAborts(t *testing.T) {
	m := atHostname(t, testDisks())
	m = keys(t, m, "ctrl+c")
	if !m.aborted {
		t.Error("ctrl+c should abort the wizard")
	}
	if m.done {
		t.Error("an aborted wizard must not report done")
	}
}

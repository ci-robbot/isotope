package installer

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// The hand-off between isotope-install-ui and the graphical installer
// (install-gui), which it runs inside cage. Both read these, so they live
// here.

// GUIRelaunchExit is the graphical installer's exit status for "start me
// again with the keyboard layout in GUIStatePath": cage's keymap is fixed when
// it starts, so a new layout needs a new cage.
const GUIRelaunchExit = 75

// GUIShellExit is the graphical installer's exit status for "give me the
// rescue shell": its "Open a terminal" on an image without foot. The text
// installer's rescue shell, /bin/sh on tty1, takes over.
const GUIShellExit = 76

// RunDir is the installer's directory in /run: the passphrase hand-off, the
// graphical installer's state and its markers. ISOTOPE_INSTALL_RUN_DIR moves
// the markers for tests.
func RunDir() string {
	if d := os.Getenv("ISOTOPE_INSTALL_RUN_DIR"); d != "" {
		return d
	}
	return "/run/isotope-install"
}

// GUIStatePath is where the graphical installer keeps its answers across a
// relaunch. Never a password, a passphrase or a recovery key.
func GUIStatePath() string { return filepath.Join(RunDir(), "state.json") }

// GUIStartedPath exists once the graphical installer has drawn its first
// frame: cage, the GPU and the fonts work. Before it exists, a cage that
// exits means the text installer takes over with nothing lost.
func GUIStartedPath() string { return filepath.Join(RunDir(), "gui-started") }

// InstallRunningPath exists while the graphical installer's install runs, so
// the text installer never starts a second one over it.
func InstallRunningPath() string { return filepath.Join(RunDir(), "install-running") }

// GUIState is the graphical installer's answers so far, saved for a
// relaunch. Next is the page to open: "keyboard" or "account".
type GUIState struct {
	Next        string `json:"next"`
	Layout      string `json:"layout"`
	Variant     string `json:"variant,omitempty"`
	Timezone    string `json:"timezone,omitempty"`
	Search      string `json:"search,omitempty"`
	Hostname    string `json:"hostname,omitempty"`
	Username    string `json:"username,omitempty"`
	Github      string `json:"github,omitempty"`
	Disk        string `json:"disk,omitempty"`
	Encrypt     bool   `json:"encrypt,omitempty"`
	RecoveryKey bool   `json:"recovery_key,omitempty"`
}

// ReadGUIState reads the saved state. A layout that is not a valid XKB name
// is dropped, since it goes into cage's environment.
func ReadGUIState(path string) (GUIState, error) {
	var s GUIState
	b, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return GUIState{}, err
	}
	if ValidateKeyboard(s.Layout, s.Variant) != nil {
		s.Layout, s.Variant = "", ""
	}
	return s, nil
}

// WriteGUIState saves s, readable by root only.
func WriteGUIState(path string, s GUIState) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

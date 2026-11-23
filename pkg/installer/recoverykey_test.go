package installer

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var recoveryKeyShape = regexp.MustCompile(`^([cbdefghijklnrtuv]{8}-){7}[cbdefghijklnrtuv]{8}$`)

func TestGenerateRecoveryKey(t *testing.T) {
	seq := make([]byte, 32)
	for i := range seq {
		seq[i] = byte(i * 8)
	}
	got, err := GenerateRecoveryKey(bytes.NewReader(seq))
	if err != nil {
		t.Fatal(err)
	}
	// 0x00 0x08 0x10 0x18 -> cc cj bc bj: high nibble then low nibble.
	if want := "cccjbcbj"; !strings.HasPrefix(got, want+"-") {
		t.Errorf("first group = %q, want %q", got[:8], want)
	}
	if !recoveryKeyShape.MatchString(got) {
		t.Errorf("%q is not 8 groups of 8 modhex characters", got)
	}

	a, err := newRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := newRecoveryKey()
	if a == b || !recoveryKeyShape.MatchString(a) {
		t.Errorf("two keys from crypto/rand: %q and %q", a, b)
	}
	if _, err := GenerateRecoveryKey(bytes.NewReader(make([]byte, 31))); err == nil {
		t.Error("a short random source must fail, not pad the key")
	}
}

func TestConfirmsRecoveryKey(t *testing.T) {
	key, _ := GenerateRecoveryKey(bytes.NewReader(bytes.Repeat([]byte{0x12, 0x34, 0x56, 0x78}, 8)))
	groups := RecoveryKeyGroups(key)
	for _, typed := range []string{groups[3], strings.ToUpper(groups[3]), " " + groups[3] + " ", key, strings.ReplaceAll(key, "-", "")} {
		if !confirmsRecoveryKey(key, 3, typed) {
			t.Errorf("%q should confirm group 4", typed)
		}
	}
	other, _ := GenerateRecoveryKey(rand.Reader)
	for _, typed := range []string{"", RecoveryKeyGroups(other)[3], groups[3][:7]} {
		if confirmsRecoveryKey(key, 3, typed) {
			t.Errorf("%q should not confirm group 4", typed)
		}
	}
}

func TestRecoveryKeyLines(t *testing.T) {
	key, _ := GenerateRecoveryKey(bytes.NewReader(bytes.Repeat([]byte{0x01, 0x23, 0x45, 0x67}, 8)))
	lines := RecoveryKeyLines(key)
	if len(lines) != 6 {
		t.Fatalf("%d lines, want 4 pairs, a blank and the key: %q", len(lines), lines)
	}
	g := RecoveryKeyGroups(key)
	if want := "3  " + spaced(g[2]) + "    " + spaced(g[3]) + "  4"; lines[1] != want {
		t.Errorf("line 2 = %q, want %q", lines[1], want)
	}
	if lines[5] != key {
		t.Errorf("last line = %q", lines[5])
	}
	for _, l := range lines {
		if n := len([]rune(l)); n > 76 {
			t.Errorf("line is %d columns: %q", n, l)
		}
	}
}

// fakeTB answers like a Trusted Boot target disk: two TPM-sealed
// containers, each getting one new keyslot when luksAddKey runs.
type fakeTB struct {
	calls  []call
	added  map[string]bool
	failOn string
	keyAt  string // what the key file held when luksAddKey ran
}

func (f *fakeTB) run(name string, args []string, stdin []byte) ([]byte, error) {
	f.calls = append(f.calls, call{name: name, args: args, stdin: string(stdin)})
	if f.failOn != "" && len(args) > 0 && args[0] == f.failOn {
		return nil, errors.New("boom")
	}
	switch {
	case name == "lsblk":
		return []byte(`NAME="/dev/vda" FSTYPE="" LABEL=""
NAME="/dev/vda1" FSTYPE="vfat" LABEL="COS_GRUB"
NAME="/dev/vda3" FSTYPE="crypto_LUKS" LABEL="COS_PERSISTENT_LUKS"
NAME="/dev/vda2" FSTYPE="crypto_LUKS" LABEL="COS_OEM_LUKS"
`), nil
	case name == "cryptsetup" && args[0] == "luksDump":
		dev := args[len(args)-1]
		slots := `"1":{}`
		if f.added[dev] {
			slots += `,"0":{}`
		}
		return []byte(`{"keyslots":{` + slots + `},"tokens":{"0":{"type":"systemd-tpm2","keyslots":["1"]}}}`), nil
	case name == "cryptsetup" && args[0] == "luksAddKey":
		dev, file := args[len(args)-2], args[len(args)-1]
		data, _ := os.ReadFile(file)
		f.keyAt = string(data)
		f.added[dev] = true
	}
	return nil, nil
}

func TestEnrollRecoveryKey(t *testing.T) {
	old := recoveryKeyFile
	recoveryKeyFile = filepath.Join(t.TempDir(), "isotope-install", "recovery-key")
	t.Cleanup(func() { recoveryKeyFile = old })
	key, _ := newRecoveryKey()

	f := &fakeTB{added: map[string]bool{}}
	var logs []string
	if err := enrollRecoveryKey(f.run, "/dev/vda", key, func(l string) { logs = append(logs, l) }); err != nil {
		t.Fatal(err)
	}
	if f.keyAt != key {
		t.Errorf("luksAddKey read %q from the key file", f.keyAt)
	}
	if _, err := os.Stat(recoveryKeyFile); !os.IsNotExist(err) {
		t.Error("the key file is still there after the enrollment")
	}
	var adds, imports, tests int
	for _, c := range f.calls {
		joined := strings.Join(c.args, " ")
		if strings.Contains(joined, key) {
			t.Errorf("%s %v carries the key in its arguments", c.name, c.args)
		}
		switch c.args[0] {
		case "luksAddKey":
			adds++
			for _, want := range []string{"--token-type systemd-tpm2", "--pbkdf pbkdf2", "--pbkdf-force-iterations 1000"} {
				if !strings.Contains(joined, want) {
					t.Errorf("luksAddKey %q lacks %q", joined, want)
				}
			}
		case "token":
			imports++
			var tok struct {
				Type     string   `json:"type"`
				Keyslots []string `json:"keyslots"`
			}
			if err := json.Unmarshal([]byte(c.stdin), &tok); err != nil || tok.Type != "systemd-recovery" || len(tok.Keyslots) != 1 || tok.Keyslots[0] != "0" {
				t.Errorf("token import %q", c.stdin)
			}
		case "open":
			tests++
			if c.stdin != key || !strings.Contains(joined, "--disable-external-tokens --key-slot 0") {
				t.Errorf("key test %q with stdin of %d bytes", joined, len(c.stdin))
			}
		}
	}
	if adds != 2 || imports != 2 || tests != 2 {
		t.Errorf("luksAddKey %d, token import %d, key test %d; want 2 each (OEM and persistent)", adds, imports, tests)
	}
	for _, l := range logs {
		if strings.Contains(l, RecoveryKeyGroups(key)[0]) {
			t.Errorf("log line carries the key: %q", l)
		}
	}
}

func TestEnrollRecoveryKeyFailures(t *testing.T) {
	old := recoveryKeyFile
	recoveryKeyFile = filepath.Join(t.TempDir(), "isotope-install", "recovery-key")
	t.Cleanup(func() { recoveryKeyFile = old })
	key, _ := newRecoveryKey()
	for _, step := range []string{"luksAddKey", "token", "open"} {
		f := &fakeTB{added: map[string]bool{}, failOn: step}
		if err := enrollRecoveryKey(f.run, "/dev/vda", key, func(string) {}); err == nil {
			t.Errorf("%s failed, yet the enrollment succeeded", step)
		}
		if _, err := os.Stat(recoveryKeyFile); !os.IsNotExist(err) {
			t.Errorf("%s failed and the key file was left behind", step)
		}
	}
}

func TestEncryptionStepsRecoveryKey(t *testing.T) {
	pre, cleanup, post := EncryptionSteps(&Choice{RecoveryKey: true, RecoveryKeyValue: "x", Disk: "/dev/vda"})
	if pre == nil || cleanup == nil || post == nil {
		t.Fatal("a recovery key install needs the manual install and the post step")
	}
	if pre, _, _ := EncryptionSteps(&Choice{Disk: "/dev/vda"}); pre != nil {
		t.Error("a TPM-only install needs no post step")
	}
}

func TestEnrollRecoveryKeyWithThePassphrase(t *testing.T) {
	old := recoveryKeyFile
	recoveryKeyFile = filepath.Join(t.TempDir(), "isotope-install", "recovery-key")
	t.Cleanup(func() { recoveryKeyFile = old })
	key, _ := newRecoveryKey()
	f := &fakeTB{added: map[string]bool{}}
	pass := "/run/isotope-install/passphrase"
	if err := enrollRecoveryKeyWith(f.run, "/dev/vda", key, &pass, func(string) {}); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if c.args[0] == "luksAddKey" {
			j := strings.Join(c.args, " ")
			if !strings.Contains(j, "--key-file "+pass) || strings.Contains(j, "--token-type") {
				t.Errorf("luksAddKey %q", j)
			}
		}
	}
}

package main

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// localeEnv points SetLocale at a temporary root and a fake `locale -a`.
func localeEnv(t *testing.T, out string, runErr error) (conf, oem string) {
	conf, oem, _ = localeEnvFake(t, out, runErr)
	return conf, oem
}

// localeFake is the output of the fake `locale -a`. The service reads it
// on its own goroutines, so a test changes it with set, not by setting
// localeList again.
type localeFake struct {
	mu  sync.Mutex
	out string
	err error
}

func (l *localeFake) set(out string, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.out, l.err = out, err
}

func localeEnvFake(t *testing.T, out string, runErr error) (conf, oem string, fake *localeFake) {
	t.Helper()
	dir := t.TempDir()
	conf = filepath.Join(dir, "etc", "locale.conf")
	oem = filepath.Join(dir, "oem", "91_isotope_locale.yaml")
	fake = &localeFake{out: out, err: runErr}
	oc, oo, ol := localeConf, localeOEM, localeList
	localeConf, localeOEM = conf, oem
	localeList = func(name string, args []string, _ []byte) ([]byte, error) {
		if name != "locale" || len(args) != 1 || args[0] != "-a" {
			t.Errorf("ran %s %v", name, args)
		}
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return []byte(fake.out), fake.err
	}
	t.Cleanup(func() { localeConf, localeOEM, localeList = oc, oo, ol })
	return conf, oem, fake
}

func localeDaemon(t *testing.T) *Daemon {
	t.Helper()
	daemonEnv(t, "v0.0.5")
	return NewDaemon(&fakeBackend{})
}

const fakeLocales = "C\nPOSIX\nde_DE.UTF-8\nen_US.UTF-8\n"

func noFile(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s was written", p)
		}
	}
}

func TestSetLocaleRefusedWithoutAction(t *testing.T) {
	conf, oem := localeEnv(t, fakeLocales, nil)
	d := localeDaemon(t)
	auth := &fakeAuth{allow: false}
	f := startService(t, d, auth)
	c := f.call("SetLocale", "de_DE.UTF-8")
	if got := errName(c.Err); got != busInterface+".Error.NotAuthorized" {
		t.Fatalf("got %s (%v), want NotAuthorized", got, c.Err)
	}
	noFile(t, conf, oem)
	me := f.client.Names()[0]
	if q := auth.questions(); len(q) != 1 || q[0] != me+" "+actionSettings {
		t.Fatalf("asked %q, want the settings action", q)
	}
}

func TestSetLocaleRefusesUnknown(t *testing.T) {
	conf, oem, fake := localeEnvFake(t, fakeLocales, nil)
	f := startService(t, localeDaemon(t), &fakeAuth{allow: true})
	if c := f.call("SetLocale", "xx_YY.UTF-8"); c.Err == nil {
		t.Fatal("an unknown locale was accepted")
	}
	// A locale `locale -a` does not list is refused even when well formed;
	// so is a failing `locale -a`.
	noFile(t, conf, oem)
	fake.set("", errors.New("no locale command"))
	if c := f.call("SetLocale", "de_DE.UTF-8"); c.Err == nil {
		t.Fatal("accepted with no locale list")
	}
}

func TestSetLocaleRefusesBadChars(t *testing.T) {
	conf, oem := localeEnv(t, fakeLocales+"de_DE.UTF-8\nevil\n", nil)
	f := startService(t, localeDaemon(t), &fakeAuth{allow: true})
	for _, bad := range []string{"de_DE.UTF-8\nevil", "de DE", "", "de_DE.UTF-8;rm", "../x", "é"} {
		if c := f.call("SetLocale", bad); c.Err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	noFile(t, conf, oem)
}

func TestSetLocaleWritesBoth(t *testing.T) {
	conf, oem := localeEnv(t, fakeLocales, nil)
	f := startService(t, localeDaemon(t), &fakeAuth{allow: true})
	if c := f.call("SetLocale", "de_DE.UTF-8"); c.Err != nil {
		t.Fatal(c.Err)
	}
	b, err := os.ReadFile(conf)
	if err != nil || string(b) != "LANG=de_DE.UTF-8\n" {
		t.Fatalf("locale.conf %q, %v", b, err)
	}
	want := `name: "Isotope system locale"
stages:
  initramfs:
    - name: "Write /etc/locale.conf"
      files:
        - path: /etc/locale.conf
          permissions: 0644
          content: |
            LANG=de_DE.UTF-8
`
	b, err = os.ReadFile(oem)
	if err != nil || string(b) != want {
		t.Fatalf("oem file %q, %v", b, err)
	}
	for _, p := range []string{conf, oem} {
		if st, _ := os.Stat(p); st.Mode().Perm() != 0o644 {
			t.Errorf("%s mode %v", p, st.Mode())
		}
	}
	// A second call replaces both files.
	if c := f.call("SetLocale", "en_US.UTF-8"); c.Err != nil {
		t.Fatal(c.Err)
	}
	if b, _ := os.ReadFile(conf); string(b) != "LANG=en_US.UTF-8\n" {
		t.Fatalf("locale.conf %q", b)
	}
}

func TestSetLocaleRefusesFlatpakCaller(t *testing.T) {
	conf, oem := localeEnv(t, fakeLocales, nil)
	d := localeDaemon(t)
	fakeProc(t, uint32(os.Getpid()), true)
	auth := &fakeAuth{allow: true}
	f := startService(t, d, auth)
	c := f.call("SetLocale", "de_DE.UTF-8")
	if got := errName(c.Err); got != busInterface+".Error.NotAuthorized" {
		t.Fatalf("from a sandbox: %s", got)
	}
	if len(auth.questions()) != 0 {
		t.Fatal("polkit was asked for a sandboxed caller")
	}
	noFile(t, conf, oem)
}

// The installer (pkg/installer) writes this same file; both are tested
// against one golden file.
func TestOemLocaleMatchesTheInstaller(t *testing.T) {
	want, err := os.ReadFile("../../pkg/installer/testdata/oem-locale-de_DE.golden")
	if err != nil {
		t.Fatal(err)
	}
	if got := oemLocale("de_DE.UTF-8"); got != string(want) {
		t.Errorf("oemLocale differs from the installer's golden file:\n%s", got)
	}
}

// setLocale itself, run on temporary paths, writes the bytes the installer's
// golden files hold: /etc/locale.conf and the /oem file.
func TestSetLocaleWritesTheInstallersBytes(t *testing.T) {
	dir := t.TempDir()
	oldConf, oldOEM, oldList := localeConf, localeOEM, localeList
	t.Cleanup(func() { localeConf, localeOEM, localeList = oldConf, oldOEM, oldList })
	localeConf, localeOEM = dir+"/locale.conf", dir+"/91_isotope_locale.yaml"
	localeList = cmdRunner(func(string, []string, []byte) ([]byte, error) { return []byte("de_DE.UTF-8\n"), nil })
	if err := setLocale("de_DE.UTF-8"); err != nil {
		t.Fatal(err)
	}
	for file, golden := range map[string]string{
		localeConf: "../../pkg/installer/testdata/locale.conf-de_DE.golden",
		localeOEM:  "../../pkg/installer/testdata/oem-locale-de_DE.golden",
	} {
		got, _ := os.ReadFile(file)
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("%s = %q, want %q", file, got, want)
		}
	}
}

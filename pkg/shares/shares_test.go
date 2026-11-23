package shares

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in                        string
		scheme, host, share, path string
		port                      uint16
		str, source, fstype, name string
	}{
		{in: "smb://nas/media", scheme: SMB, host: "nas", share: "media",
			str: "smb://nas/media", source: "//nas/media", fstype: "cifs", name: "media"},
		{in: " SMB://NAS.local:4455/Media/Films/2024/ ", scheme: SMB, host: "NAS.local", port: 4455, share: "Media", path: "/Films/2024",
			str: "smb://NAS.local:4455/Media/Films/2024", source: "//NAS.local/Media/Films/2024", fstype: "cifs", name: "2024"},
		{in: "smb://my_box/a%20b", scheme: SMB, host: "my_box", share: "a b",
			str: "smb://my_box/a%20b", source: "//my_box/a b", fstype: "cifs", name: "a b"},
		{in: "nfs://10.0.0.2/srv/data", scheme: NFS, host: "10.0.0.2", path: "/srv/data",
			str: "nfs://10.0.0.2/srv/data", source: "10.0.0.2:/srv/data", fstype: "nfs4", name: "data"},
		{in: "nfs://[fd00::1]:2050/", scheme: NFS, host: "fd00::1", port: 2050, path: "/",
			str: "nfs://[fd00::1]:2050/", source: "[fd00::1]:/", fstype: "nfs4", name: "fd00::1"},
		{in: "nfs://server", scheme: NFS, host: "server", path: "/",
			str: "nfs://server/", source: "server:/", fstype: "nfs4", name: "server"},
	}
	for _, c := range cases {
		l, err := Parse(c.in)
		if err != nil {
			t.Errorf("Parse(%q): %v", c.in, err)
			continue
		}
		want := Location{Scheme: c.scheme, Host: c.host, Port: c.port, Share: c.share, Path: c.path}
		if l != want {
			t.Errorf("Parse(%q) = %+v, want %+v", c.in, l, want)
		}
		if l.String() != c.str || l.Source() != c.source || l.FSType() != c.fstype || l.DefaultName() != c.name {
			t.Errorf("%q: String %q Source %q FSType %q DefaultName %q", c.in, l.String(), l.Source(), l.FSType(), l.DefaultName())
		}
		again, err := Parse(l.String())
		if err != nil || again != l {
			t.Errorf("%q: String does not parse back: %+v, %v", c.in, again, err)
		}
	}
}

func TestParseRefuses(t *testing.T) {
	for _, in := range []string{
		"", "nas/media", "ftp://nas/x", "smb:nas", "smb://nas", "smb://nas/",
		"smb://anna@nas/media", "smb://anna:pw@nas/media", "smb://nas/media?x=1", "smb://nas/media#a",
		"smb://-oexec/x", "smb://.nas/x", "smb://na s/x", "smb://nas:0/x", "smb://nas:70000/x", "smb://nas:/x",
		"smb://nas/a,uid=0", "smb://nas/a/../b", "smb://nas/a%5Cb", "smb://nas/a%0Ab", "nfs:///srv",
		"smb://" + strings.Repeat("a", 300) + "/x", "smb://nas/" + strings.Repeat("a", 1100),
	} {
		if l, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) = %+v, want an error", in, l)
		}
	}
}

func TestSameAndFromMount(t *testing.T) {
	a, _ := Parse("smb://NAS:4455/media/films/")
	b, _ := Parse("smb://nas/media/films")
	if !a.Same(b) {
		t.Error("the port and the case of the host should not count")
	}
	c, _ := Parse("smb://nas/media")
	if a.Same(c) {
		t.Error("another path is another share")
	}
	m, ok := FromMount("cifs", "//nas/media/films")
	if !ok || !m.Same(b) {
		t.Errorf("FromMount cifs = %+v %v", m, ok)
	}
	n, _ := Parse("nfs://[fd00::1]/srv/data")
	m, ok = FromMount("nfs4", "[fd00::1]:/srv/data")
	if !ok || !m.Same(n) {
		t.Errorf("FromMount nfs4 = %+v %v", m, ok)
	}
	for _, x := range [][2]string{{"ext4", "/dev/sda1"}, {"cifs", "nas/x"}, {"nfs4", "nas"}, {"cifs", "//nas"}} {
		if _, ok := FromMount(x[0], x[1]); ok {
			t.Errorf("FromMount(%q, %q) should be no share", x[0], x[1])
		}
	}
}

func TestCheckNameAndCredential(t *testing.T) {
	for _, n := range []string{"media", "Films 2024", "naïve"} {
		if err := CheckName(n); err != nil {
			t.Errorf("CheckName(%q): %v", n, err)
		}
	}
	for _, n := range []string{"", ".", "..", ".hidden", "-x", "a/b", "a\\b", "a\nb", strings.Repeat("x", 65)} {
		if CheckName(n) == nil {
			t.Errorf("CheckName(%q) should refuse", n)
		}
	}
	for _, s := range []string{"", "anna", "ACME", "anna.b@corp"} {
		if err := CheckCredential("user name", s); err != nil {
			t.Errorf("CheckCredential(%q): %v", s, err)
		}
	}
	for _, s := range []string{"a,b", "a=b", "a\nb", strings.Repeat("x", 257)} {
		if CheckCredential("user name", s) == nil {
			t.Errorf("CheckCredential(%q) should refuse", s)
		}
	}
}

func TestSavedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "isotope", "shares.json")
	list, err := Load(path)
	if err != nil || len(list) != 0 {
		t.Fatalf("a missing file is an empty list: %v %v", list, err)
	}
	list = Put(list, Saved{URL: "smb://nas/media", Name: "media", User: "anna", AutoMount: true})
	list = Put(list, Saved{URL: "nfs://srv/data", Name: "data"})
	list = Put(list, Saved{URL: "smb://NAS/media/", Name: "Media", User: "anna", Domain: "HOME"})
	if len(list) != 2 || list[0].Name != "Media" {
		t.Fatalf("Put replaces the same share: %+v", list)
	}
	if err := Store(path, list); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the file is 0600: %v %v", fi.Mode(), err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "password") {
		t.Errorf("the file must hold no password: %s", b)
	}
	got, err := Load(path)
	if err != nil || !reflect.DeepEqual(got, list) {
		t.Fatalf("Load = %+v %v, want %+v", got, err, list)
	}
	l, _ := Parse("smb://nas/media")
	got = Remove(got, l)
	if len(got) != 1 || got[0].URL != "nfs://srv/data" || Find(got, l) != -1 {
		t.Fatalf("Remove: %+v", got)
	}
	// A bad entry is left out, the others stay.
	os.WriteFile(path, []byte(`{"shares":[{"url":"smb://nas/a,uid=0","name":"x"},{"url":"nfs://srv/x","name":"x","user":"anna"},{"url":"nfs://srv/ok","name":"ok"}]}`), 0o600)
	got, err = Load(path)
	if err != nil || len(got) != 1 || got[0].Name != "ok" {
		t.Fatalf("Load with bad entries = %+v %v", got, err)
	}
	os.WriteFile(path, []byte(`{`), 0o600)
	if _, err := Load(path); err == nil {
		t.Error("a broken file is an error")
	}
}

func TestPathFollowsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/x/conf")
	if p, _ := Path(); p != "/x/conf/isotope/shares.json" {
		t.Errorf("Path = %q", p)
	}
	t.Setenv("XDG_CONFIG_HOME", "relative")
	t.Setenv("HOME", "/home/anna")
	if p, _ := Path(); p != "/home/anna/.config/isotope/shares.json" {
		t.Errorf("Path = %q", p)
	}
}

type fakeKeyring struct {
	pw       map[string]string
	errsLeft map[string]int // url -> keyring errors before it answers
}

func (k *fakeKeyring) Password(url, user string) (string, bool, error) {
	if k.errsLeft[url] > 0 {
		k.errsLeft[url]--
		return "", false, errors.New("org.freedesktop.secrets was not provided by any .service files")
	}
	pw, ok := k.pw[url+"|"+user]
	return pw, ok, nil
}
func (k *fakeKeyring) StorePassword(url, user, pw string) error { k.pw[url+"|"+user] = pw; return nil }
func (k *fakeKeyring) ForgetPassword(url, user string) error    { delete(k.pw, url+"|"+user); return nil }

type fakeMounter struct {
	calls []string
	fail  map[string][]string // url -> the errors of its next calls
}

func (m *fakeMounter) MountShare(url string, o Options, c Credentials) (string, error) {
	m.calls = append(m.calls, url+" "+o.Name+" "+strings.Join(c.Lines(), "|"))
	if f := m.fail[url]; len(f) > 0 {
		m.fail[url] = f[1:]
		return "", errors.New(f[0])
	}
	return "/run/media/anna/" + o.Name, nil
}

func (m *fakeMounter) count(url string) int {
	n := 0
	for _, c := range m.calls {
		if strings.HasPrefix(c, url+" ") {
			n++
		}
	}
	return n
}

func TestTransientAndAuthFailure(t *testing.T) {
	for _, msg := range []string{
		"mount error(112): Host is down", "mount error(113): No route to host", "mount error(111): could not connect: Connection refused",
		"mount.nfs4: Connection timed out", "mount did not finish in 1m0s", "mount error: could not resolve address for nas: Unknown error",
		"mount.nfs4: Failed to resolve server nas: Name or service not known", "mount error(101): Network is unreachable",
	} {
		if !Transient(errors.New(msg)) {
			t.Errorf("%q should be tried again", msg)
		}
	}
	for _, msg := range []string{
		"mount error(13): Permission denied", "STATUS_LOGON_FAILURE", "mount.nfs4: access denied by server while mounting srv:/x",
		"the path cannot hold a comma", "not authorized: only the person at the screen", "mount error(2): No such file or directory",
		"mount error(13): Permission denied (timed out?)",
	} {
		if Transient(errors.New(msg)) {
			t.Errorf("%q must not be tried again", msg)
		}
	}
	if !AuthFailure(errors.New("mount error(13): Permission denied")) || AuthFailure(errors.New("host is down")) {
		t.Error("AuthFailure")
	}
}

func TestAutoMount(t *testing.T) {
	kr := &fakeKeyring{pw: map[string]string{"smb://nas/media|anna": "s3cret", "smb://nas/wrong|anna": "old", "smb://nas/late|anna": "pw"},
		errsLeft: map[string]int{"smb://nas/late": 1}}
	m := &fakeMounter{fail: map[string][]string{
		"nfs://srv/data":  {"mount.nfs4: Connection timed out", "mount error(113): No route to host"},
		"nfs://srv/never": {"host is down", "host is down", "host is down", "host is down"},
		"smb://nas/wrong": {"mount error(13): Permission denied", "mount error(13): Permission denied"},
	}}
	var slept []time.Duration
	var logs []string
	var notes []string
	a := AutoMount{Mounter: m, Keyring: kr, Tries: 3, Wait: 20 * time.Second,
		Sleep:  func(d time.Duration) { slept = append(slept, d) },
		Log:    func(f string, args ...any) { logs = append(logs, f) },
		Notify: func(summary, body string) { notes = append(notes, summary+": "+body) }}
	got := a.Run([]Saved{
		{URL: "smb://nas/media", Name: "media", User: "anna", Domain: "HOME", AutoMount: true},
		{URL: "smb://nas/nopw", Name: "nopw", User: "anna", AutoMount: true},
		{URL: "smb://nas/late", Name: "late", User: "anna", AutoMount: true},
		{URL: "smb://nas/wrong", Name: "wrong", User: "anna", AutoMount: true},
		{URL: "smb://nas/guest", Name: "guest", AutoMount: true},
		{URL: "smb://nas/manual", Name: "manual", User: "anna"},
		{URL: "nfs://srv/data", Name: "data", AutoMount: true},
		{URL: "nfs://srv/never", Name: "never", AutoMount: true},
	})
	want := map[string]string{"smb://nas/media": "/run/media/anna/media", "smb://nas/guest": "/run/media/anna/guest",
		"nfs://srv/data": "/run/media/anna/data", "smb://nas/late": "/run/media/anna/late"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mounted %v, want %v", got, want)
	}
	if m.calls[0] != "smb://nas/media media anna|HOME|s3cret" {
		t.Errorf("calls %q", m.calls)
	}
	// A wrong password is sent once, never again.
	if n := m.count("smb://nas/wrong"); n != 1 {
		t.Errorf("the wrong password was sent %d times", n)
	}
	// A network failure is tried again, up to Tries.
	if m.count("nfs://srv/data") != 3 || m.count("nfs://srv/never") != 3 || m.count("smb://nas/nopw") != 0 || m.count("smb://nas/manual") != 0 {
		t.Errorf("calls %q", m.calls)
	}
	// The keyring that was not up at the first round is asked again.
	if m.count("smb://nas/late") != 1 {
		t.Errorf("late: %q", m.calls)
	}
	if len(slept) != 2 || slept[0] != 20*time.Second {
		t.Errorf("slept %v", slept)
	}
	wantNotes := []string{
		"Could not mount nopw: Its password is not in your keyring. Open Isotope System's Disks page to mount it.",
		"Could not mount wrong: The server refused the user name or password. Open Isotope System's Disks page to mount it.",
		"Could not mount never: Host is down. Open Isotope System's Disks page to mount it.",
	}
	if strings.Join(notes, "\n") != strings.Join(wantNotes, "\n") {
		t.Errorf("notifications\n%s\nwant\n%s", strings.Join(notes, "\n"), strings.Join(wantNotes, "\n"))
	}
	if len(logs) == 0 {
		t.Error("the failures are logged")
	}
}

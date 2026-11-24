package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestUnmeteredIsTheRefreshCondition(t *testing.T) {
	old := meteredCheck
	t.Cleanup(func() { meteredCheck = old })
	for _, c := range []struct {
		name    string
		metered bool
		err     error
		code    int
		say     string
	}{
		{"not metered", false, nil, 0, ""},
		{"metered", true, nil, 1, "is metered"},
		{"unknown", false, errors.New("NetworkManager timed out"), 1, "cannot tell"},
	} {
		meteredCheck = func() (bool, error) { return c.metered, c.err }
		var out bytes.Buffer
		if got := cmdUnmetered(&out); got != c.code || !strings.Contains(out.String(), c.say) {
			t.Errorf("%s: exit %d %q, want %d %q", c.name, got, out.String(), c.code, c.say)
		}
	}
}

// The refresh service runs the condition through the binary's real path.
func TestRefreshServiceSkipsAMeteredConnection(t *testing.T) {
	b, err := os.ReadFile("../../rootfs/common/usr/lib/systemd/system/fwupd-refresh.service.d/10-isotope.conf")
	if err != nil {
		t.Skip("the drop-in is not in this tree:", err)
	}
	if !strings.Contains(string(b), "\nExecCondition=/usr/bin/isotope unmetered\n") {
		t.Fatalf("no metered condition in the drop-in:\n%s", b)
	}
}

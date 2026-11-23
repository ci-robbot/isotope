package isorelease

import "testing"

// The tags release.yml pushes; Isotope System computes the same ones.
func TestLineTag(t *testing.T) {
	for _, c := range []struct{ flavor, line, want string }{
		{"sway", GPUFull, "sway-v1.0.0"},
		{"sway", GPUVM, "sway-vm-v1.0.0"},
		{"cosmic", LineUKI, "cosmic-uki-v1.0.0"},
		{FlavorServer, GPUVM, "server-v1.0.0"},
		{FlavorServer, LineUKI, "server-uki-v1.0.0"},
		{FlavorAgent, LineUKI, "agent-v1.0.0"},
	} {
		if got := LineTag(c.flavor, c.line, "v1.0.0"); got != c.want {
			t.Errorf("%s %s: %q, want %q", c.flavor, c.line, got, c.want)
		}
	}
}

func TestIsTag(t *testing.T) {
	for s, want := range map[string]bool{"v1.2.3": true, "v1.2.3-rc1": true, "1.2.3": true, "latest": false, "v1.2": false} {
		if IsTag(s) != want {
			t.Errorf("IsTag(%q) = %v", s, !want)
		}
	}
}

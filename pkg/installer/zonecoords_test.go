package installer

import (
	"math"
	"strings"
	"testing"
)

const zoneTab = `# tzdb timezone descriptions
IT	+4154+01229	Europe/Rome
FR,MC	+4852+00220	Europe/Paris
US	+404251-0740023	America/New_York	Eastern (most areas)
AU	-3352+15113	Australia/Sydney	New South Wales (most areas)
XX	garbage	Nowhere/Bad
`

func TestParseZoneTab(t *testing.T) {
	c := ParseZoneTab(strings.NewReader(zoneTab))
	if len(c) != 4 {
		t.Fatalf("%d zones: %v", len(c), c)
	}
	near := func(a, b float64) bool { return math.Abs(a-b) < 0.01 }
	if r := c["Europe/Rome"]; !near(r.Lat, 41.9) || !near(r.Lon, 12.4833) {
		t.Errorf("Rome %+v", r)
	}
	if n := c["America/New_York"]; !near(n.Lat, 40.7142) || !near(n.Lon, -74.0064) {
		t.Errorf("New York %+v", n)
	}
	if s := c["Australia/Sydney"]; s.Lat > 0 {
		t.Errorf("Sydney %+v", s)
	}
}

func TestNearestZone(t *testing.T) {
	c := ParseZoneTab(strings.NewReader(zoneTab))
	zones := []string{"UTC", "Europe/Rome", "Europe/Paris", "America/New_York", "Australia/Sydney"}
	for _, tc := range []struct {
		p    Coord
		want string
	}{
		{Coord{45.46, 9.19}, "Europe/Rome"},        // Milan
		{Coord{50.85, 4.35}, "Europe/Paris"},       // Brussels
		{Coord{42.36, -71.06}, "America/New_York"}, // Boston
		{Coord{-37.8, 144.96}, "Australia/Sydney"}, // Melbourne
	} {
		if got := NearestZone(c, zones, tc.p); got != tc.want {
			t.Errorf("NearestZone(%+v) = %q, want %q", tc.p, got, tc.want)
		}
	}
	if got := NearestZone(c, []string{"UTC"}, Coord{}); got != "" {
		t.Errorf("no coordinates: %q", got)
	}
}

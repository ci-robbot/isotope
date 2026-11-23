package installer

import (
	"bufio"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Coord is a point on Earth, in degrees: north and east are positive.
type Coord struct{ Lat, Lon float64 }

// ParseZoneTab reads the coordinates of each zone's main city from tzdata's
// zone1970.tab or zone.tab (ISO 6709: ±DDMM±DDDMM or ±DDMMSS±DDDMMSS).
func ParseZoneTab(r io.Reader) map[string]Coord {
	out := map[string]Coord{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 3 {
			continue
		}
		if c, ok := parseISO6709(f[1]); ok {
			out[f[2]] = c
		}
	}
	return out
}

func parseISO6709(s string) (Coord, bool) {
	// The longitude starts at the second sign.
	i := strings.IndexAny(s[1:], "+-")
	if i < 0 || (s[0] != '+' && s[0] != '-') {
		return Coord{}, false
	}
	lat, ok1 := dms(s[:i+1], 2)
	lon, ok2 := dms(s[i+1:], 3)
	return Coord{lat, lon}, ok1 && ok2
}

// dms reads ±D..MM[SS] with deg degree digits.
func dms(s string, deg int) (float64, bool) {
	sign := 1.0
	if s[0] == '-' {
		sign = -1
	}
	s = s[1:]
	if len(s) != deg+2 && len(s) != deg+4 {
		return 0, false
	}
	d, err1 := strconv.Atoi(s[:deg])
	m, err2 := strconv.Atoi(s[deg : deg+2])
	sec := 0
	var err3 error
	if len(s) == deg+4 {
		sec, err3 = strconv.Atoi(s[deg+2:])
	}
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, false
	}
	return sign * (float64(d) + float64(m)/60 + float64(sec)/3600), true
}

// ZoneCoords are the main cities of this system's zones, from the tzdata
// tables in the zoneinfo directory, or none.
func ZoneCoords() map[string]Coord { return loadZoneCoords(zoneinfoDir) }

func loadZoneCoords(dir string) map[string]Coord {
	out := map[string]Coord{}
	for _, tab := range []string{"zone.tab", "zone1970.tab"} {
		f, err := os.Open(filepath.Join(dir, tab))
		if err != nil {
			continue
		}
		for z, c := range ParseZoneTab(f) {
			out[z] = c
		}
		f.Close()
	}
	return out
}

// NearestZone is the zone of zones whose city is nearest to p (great-circle
// distance), or "" when none of them has coordinates.
func NearestZone(coords map[string]Coord, zones []string, p Coord) string {
	best, bestD := "", math.Inf(1)
	for _, z := range zones {
		c, ok := coords[z]
		if !ok {
			continue
		}
		if d := distance(p, c); d < bestD {
			best, bestD = z, d
		}
	}
	return best
}

// distance is the central angle between a and b, in radians.
func distance(a, b Coord) float64 {
	rad := math.Pi / 180
	la1, la2 := a.Lat*rad, b.Lat*rad
	dla, dlo := la2-la1, (b.Lon-a.Lon)*rad
	h := math.Sin(dla/2)*math.Sin(dla/2) + math.Cos(la1)*math.Cos(la2)*math.Sin(dlo/2)*math.Sin(dlo/2)
	return 2 * math.Asin(math.Min(1, math.Sqrt(h)))
}

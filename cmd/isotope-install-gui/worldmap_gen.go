//go:build ignore

// worldmap_gen writes worldmap.txt, the land outlines the time zone map
// draws, from Natural Earth's 1:110m land polygons (public domain):
//
//	curl -fsSLO https://raw.githubusercontent.com/nvkelso/natural-earth-vector/master/geojson/ne_110m_land.geojson
//	go run worldmap_gen.go ne_110m_land.geojson > worldmap.txt
//
// Each ring is simplified (Douglas-Peucker, 0.35 degrees), rings smaller
// than about two square degrees are dropped, and the points are rounded to
// a tenth of a degree. One ring per line: "lon,lat lon,lat ...".
package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
)

type pt struct{ x, y float64 }

func main() {
	b, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	var fc struct {
		Features []struct {
			Geometry struct {
				Type        string          `json:"type"`
				Coordinates json.RawMessage `json:"coordinates"`
			} `json:"geometry"`
		} `json:"features"`
	}
	if err := json.Unmarshal(b, &fc); err != nil {
		panic(err)
	}
	var rings [][]pt
	for _, f := range fc.Features {
		var polys [][][][2]float64
		switch f.Geometry.Type {
		case "Polygon":
			var p [][][2]float64
			if err := json.Unmarshal(f.Geometry.Coordinates, &p); err != nil {
				panic(err)
			}
			polys = append(polys, p)
		case "MultiPolygon":
			if err := json.Unmarshal(f.Geometry.Coordinates, &polys); err != nil {
				panic(err)
			}
		}
		for _, poly := range polys {
			if len(poly) == 0 {
				continue
			}
			// The outer ring only: 1:110m land has no lakes worth a hole.
			var r []pt
			for _, c := range poly[0] {
				r = append(r, pt{c[0], c[1]})
			}
			rings = append(rings, r)
		}
	}
	fmt.Println("# Natural Earth 1:110m land (public domain), simplified by worldmap_gen.go.")
	for _, r := range rings {
		s := simplify(r, 0.35)
		if len(s) < 4 || math.Abs(area(s)) < 2 {
			continue
		}
		var parts []string
		for _, p := range s {
			parts = append(parts, fmt.Sprintf("%g,%g", math.Round(p.x*10)/10, math.Round(p.y*10)/10))
		}
		fmt.Println(strings.Join(parts, " "))
	}
}

func area(r []pt) float64 {
	a := 0.0
	for i := range r {
		j := (i + 1) % len(r)
		a += r[i].x*r[j].y - r[j].x*r[i].y
	}
	return a / 2
}

func simplify(r []pt, eps float64) []pt {
	if len(r) < 3 {
		return r
	}
	keep := make([]bool, len(r))
	keep[0], keep[len(r)-1] = true, true
	var dp func(a, b int)
	dp = func(a, b int) {
		best, bi := 0.0, -1
		for i := a + 1; i < b; i++ {
			if d := segDist(r[i], r[a], r[b]); d > best {
				best, bi = d, i
			}
		}
		if bi >= 0 && best > eps {
			keep[bi] = true
			dp(a, bi)
			dp(bi, b)
		}
	}
	dp(0, len(r)-1)
	var out []pt
	for i, k := range keep {
		if k {
			out = append(out, r[i])
		}
	}
	return out
}

func segDist(p, a, b pt) float64 {
	dx, dy := b.x-a.x, b.y-a.y
	l := dx*dx + dy*dy
	if l == 0 {
		return math.Hypot(p.x-a.x, p.y-a.y)
	}
	t := math.Max(0, math.Min(1, ((p.x-a.x)*dx+(p.y-a.y)*dy)/l))
	return math.Hypot(p.x-a.x-t*dx, p.y-a.y-t*dy)
}

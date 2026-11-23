package main

import (
	_ "embed"
	"image"
	"strconv"
	"strings"
	"time"

	"gioui.org/f32"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/kairos-community/isotope/pkg/guikit"
	"github.com/kairos-community/isotope/pkg/installer"
)

// The time zone map: Natural Earth's 1:110m land (public domain),
// simplified by worldmap_gen.go, drawn equirectangular (a degree is a
// degree in both directions, as the mocks' 360x180 view box), with the
// chosen zone's band of longitudes lit and a dot on its main city. A click
// picks the zone whose city is nearest.
//
//go:embed worldmap.txt
var worldmapData string

var landRings = func() [][]f32.Point {
	var rings [][]f32.Point
	for _, line := range strings.Split(worldmapData, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var r []f32.Point
		for _, p := range strings.Fields(line) {
			lon, lat, ok := strings.Cut(p, ",")
			if !ok {
				continue
			}
			x, err1 := strconv.ParseFloat(lon, 32)
			y, err2 := strconv.ParseFloat(lat, 32)
			if err1 == nil && err2 == nil {
				r = append(r, f32.Pt(float32(x), float32(y)))
			}
		}
		if len(r) > 2 {
			rings = append(rings, r)
		}
	}
	return rings
}()

// mapPoint is where lon/lat falls in a map of size sz.
func mapPoint(sz image.Point, lon, lat float64) f32.Point {
	return f32.Pt(float32((lon+180)/360)*float32(sz.X), float32((90-lat)/180)*float32(sz.Y))
}

// mapCoord is the lon/lat under p in a map of size sz.
func mapCoord(sz image.Point, p f32.Point) installer.Coord {
	return installer.Coord{
		Lat: 90 - float64(p.Y)/float64(sz.Y)*180,
		Lon: float64(p.X)/float64(sz.X)*360 - 180,
	}
}

// worldMap draws the map filling the constraints.
func (u *UI) worldMap(gtx layout.Context) layout.Dimensions {
	sz := gtx.Constraints.Max
	u.mapSize = sz
	defer clip.Rect(image.Rectangle{Max: sz}).Push(gtx.Ops).Pop()
	for _, r := range landRings {
		var p clip.Path
		p.Begin(gtx.Ops)
		for i, c := range r {
			q := mapPoint(sz, float64(c.X), float64(c.Y))
			if i == 0 {
				p.MoveTo(q)
			} else {
				p.LineTo(q)
			}
		}
		p.Close()
		paint.FillShape(gtx.Ops, guikit.ColField, clip.Outline{Path: p.End()}.Op())
	}
	// The grid every 30 degrees of longitude and 45 of latitude.
	grid := guikit.Over(guikit.ColSidebar, guikit.ColText, 0.05)
	for lon := -120.0; lon <= 120; lon += 60 {
		x := int(mapPoint(sz, lon, 0).X)
		guikit.FillRect(gtx, image.Rect(x, 0, x+1, sz.Y), grid)
	}
	for lat := -45.0; lat <= 45; lat += 45 {
		y := int(mapPoint(sz, 0, lat).Y)
		guikit.FillRect(gtx, image.Rect(0, y, sz.X, y+1), grid)
	}
	if u.tzSel >= 0 {
		zone := u.w.Zones[u.tzSel]
		// The band: the 15 degrees of longitude of the zone's UTC offset.
		if loc, err := time.LoadLocation(zone); err == nil {
			_, off := u.now().In(loc).Zone()
			center := float64(off) / 3600 * 15
			x0 := int(mapPoint(sz, center-7.5, 0).X)
			x1 := int(mapPoint(sz, center+7.5, 0).X)
			guikit.FillRect(gtx, image.Rect(x0, 0, x1, sz.Y), guikit.Over(guikit.ColSidebar, guikit.ColAccent, 0.14))
		}
		if c, ok := u.zoneCoords[zone]; ok {
			p := mapPoint(sz, c.Lon, c.Lat)
			ring, dot := guikit.Dp(gtx, 9), guikit.Dp(gtx, 4)
			cx, cy := int(p.X), int(p.Y)
			guikit.FillRRect(gtx, image.Rect(cx-ring, cy-ring, cx+ring, cy+ring), ring, guikit.Over(guikit.ColSidebar, guikit.ColAccent, 0.25))
			guikit.FillRRect(gtx, image.Rect(cx-dot, cy-dot, cx+dot, cy+dot), dot, guikit.ColGlow)
		}
	}
	pointer.CursorPointer.Add(gtx.Ops)
	u.mapClick.Add(gtx.Ops)
	return layout.Dimensions{Size: sz}
}

// clickMap picks the zone nearest to the click.
func (u *UI) clickMap(p image.Point) {
	if u.mapSize.X == 0 || u.mapSize.Y == 0 {
		return
	}
	c := mapCoord(u.mapSize, f32.Pt(float32(p.X), float32(p.Y)))
	if z := installer.NearestZone(u.zoneCoords, u.w.Zones, c); z != "" {
		for i, name := range u.w.Zones {
			if name == z {
				u.pickZone(i)
			}
		}
	}
}

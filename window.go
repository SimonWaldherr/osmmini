package osmmini

import "math"

type CoordWindow struct {
	MinLat float64 `json:"min_lat"`
	MaxLat float64 `json:"max_lat"`
	MinLon float64 `json:"min_lon"`
	MaxLon float64 `json:"max_lon"`
}

func (w CoordWindow) Valid() bool {
	if w.MinLat >= w.MaxLat || w.MinLon >= w.MaxLon {
		return false
	}
	if w.MinLat < -90 || w.MaxLat > 90 || w.MinLon < -180 || w.MaxLon > 180 {
		return false
	}
	return true
}

func (w CoordWindow) Center() Coord {
	return Coord{
		Lat: (w.MinLat + w.MaxLat) / 2,
		Lon: (w.MinLon + w.MaxLon) / 2,
	}
}

func (w CoordWindow) Contains(c Coord) bool {
	return c.Lat >= w.MinLat && c.Lat <= w.MaxLat && c.Lon >= w.MinLon && c.Lon <= w.MaxLon
}

// ExpandMeters expands the window in all directions by approx. meters.
func (w CoordWindow) ExpandMeters(m float64) CoordWindow {
	if m <= 0 {
		return w
	}
	c := w.Center()
	latDeg := m / 111320.0
	cos := math.Cos(deg2rad(c.Lat))
	lonDen := 111320.0 * cos
	lonDeg := latDeg
	if lonDen > 1e-9 {
		lonDeg = m / lonDen
	}
	out := CoordWindow{
		MinLat: w.MinLat - latDeg,
		MaxLat: w.MaxLat + latDeg,
		MinLon: w.MinLon - lonDeg,
		MaxLon: w.MaxLon + lonDeg,
	}
	// clamp
	if out.MinLat < -90 {
		out.MinLat = -90
	}
	if out.MaxLat > 90 {
		out.MaxLat = 90
	}
	if out.MinLon < -180 {
		out.MinLon = -180
	}
	if out.MaxLon > 180 {
		out.MaxLon = 180
	}
	return out
}

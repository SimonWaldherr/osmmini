package osmmini

import "math"

// Ring is a closed sequence of coordinates (first and last point coincide,
// per the GeoJSON LinearRing convention).
type Ring []Coord

// Polygon is one exterior ring plus zero or more interior holes.
type Polygon struct {
	Outer Ring
	Holes []Ring
}

// Geometry is one or more polygons. A plain GeoJSON Polygon and a
// MultiPolygon share this one runtime shape (len(Polygons) == 1 for the
// former), so callers never need to special-case either.
type Geometry struct {
	Polygons []Polygon
}

// Territory is a named geographic area plus arbitrary business metadata
// (e.g. vehicle, depot, employee) copied verbatim from its source data.
type Territory struct {
	ID         string
	Name       string
	Layer      string
	Geometry   Geometry
	Properties map[string]any
}

// boundaryEpsilonDeg is the point-on-edge tolerance used by Contains so a
// point that lies exactly on a territory boundary is matched deterministically
// instead of depending on float rounding.
const boundaryEpsilonDeg = 1e-9

// Contains reports whether c lies within g. A point exactly on an edge
// (within boundaryEpsilonDeg) counts as contained.
func (g Geometry) Contains(c Coord) bool {
	for _, poly := range g.Polygons {
		if poly.contains(c) {
			return true
		}
	}
	return false
}

func (p Polygon) contains(c Coord) bool {
	if pointOnRing(p.Outer, c) {
		return true
	}
	for _, h := range p.Holes {
		if pointOnRing(h, c) {
			return true
		}
	}
	inside := rayCast(p.Outer, c)
	for _, h := range p.Holes {
		if rayCast(h, c) {
			inside = !inside
		}
	}
	return inside
}

// rayCast is the standard even-odd (PNPOLY) point-in-ring test.
func rayCast(ring Ring, c Coord) bool {
	n := len(ring)
	if n < 3 {
		return false
	}
	inside := false
	j := n - 1
	for i := 0; i < n; i++ {
		yi, xi := ring[i].Lat, ring[i].Lon
		yj, xj := ring[j].Lat, ring[j].Lon
		if (yi > c.Lat) != (yj > c.Lat) {
			xIntersect := (xj-xi)*(c.Lat-yi)/(yj-yi) + xi
			if c.Lon < xIntersect {
				inside = !inside
			}
		}
		j = i
	}
	return inside
}

func pointOnRing(ring Ring, c Coord) bool {
	n := len(ring)
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		dLat := ring[j].Lat - ring[i].Lat
		dLon := ring[j].Lon - ring[i].Lon
		lenSq := dLat*dLat + dLon*dLon
		var t float64
		if lenSq > 1e-20 {
			t = ((c.Lat-ring[i].Lat)*dLat + (c.Lon-ring[i].Lon)*dLon) / lenSq
			if t < 0 {
				t = 0
			} else if t > 1 {
				t = 1
			}
		}
		projLat := ring[i].Lat + t*dLat
		projLon := ring[i].Lon + t*dLon
		ddLat := c.Lat - projLat
		ddLon := c.Lon - projLon
		if ddLat*ddLat+ddLon*ddLon <= boundaryEpsilonDeg*boundaryEpsilonDeg {
			return true
		}
	}
	return false
}

// BBox returns the bounding box of g in WGS84 coordinates.
func (g Geometry) BBox() CoordWindow {
	w := CoordWindow{MinLat: math.Inf(1), MinLon: math.Inf(1), MaxLat: math.Inf(-1), MaxLon: math.Inf(-1)}
	for _, poly := range g.Polygons {
		for _, c := range poly.Outer {
			if c.Lat < w.MinLat {
				w.MinLat = c.Lat
			}
			if c.Lat > w.MaxLat {
				w.MaxLat = c.Lat
			}
			if c.Lon < w.MinLon {
				w.MinLon = c.Lon
			}
			if c.Lon > w.MaxLon {
				w.MaxLon = c.Lon
			}
		}
	}
	return w
}

// Centroid returns the unweighted average of g's outer-ring vertices. It is
// a cheap approximation (not a true area centroid), adequate for rough
// distance/proximity use such as the "nearest territory" assignment policy.
func (g Geometry) Centroid() Coord {
	var sumLat, sumLon float64
	var n int
	for _, poly := range g.Polygons {
		for _, c := range poly.Outer {
			sumLat += c.Lat
			sumLon += c.Lon
			n++
		}
	}
	if n == 0 {
		return Coord{}
	}
	return Coord{Lat: sumLat / float64(n), Lon: sumLon / float64(n)}
}

// DistanceToBoundaryMeters returns the approximate minimum distance from c
// to g's boundary (0 when c is inside or on the boundary is not implied —
// callers that only care about "inside" should use Contains).
func (g Geometry) DistanceToBoundaryMeters(c Coord) float64 {
	best := math.Inf(1)
	for _, poly := range g.Polygons {
		if d := ringMinDistMeters(poly.Outer, c); d < best {
			best = d
		}
		for _, h := range poly.Holes {
			if d := ringMinDistMeters(h, c); d < best {
				best = d
			}
		}
	}
	return best
}

// distToSegmentMeters approximates the distance in meters from c to segment
// a-b using an equirectangular projection local to c (the same approximation
// CoordWindow.ExpandMeters uses elsewhere in this package) -- adequate at
// territory scale, not for antimeridian-spanning or polar geometry.
func distToSegmentMeters(a, b, c Coord) float64 {
	cosLat := math.Cos(deg2rad(c.Lat))
	x := func(p Coord) float64 { return p.Lon * 111320 * cosLat }
	y := func(p Coord) float64 { return p.Lat * 111320 }
	ax, ay := x(a), y(a)
	bx, by := x(b), y(b)
	px, py := x(c), y(c)
	dx, dy := bx-ax, by-ay
	lenSq := dx*dx + dy*dy
	t := 0.0
	if lenSq > 1e-9 {
		t = ((px-ax)*dx + (py-ay)*dy) / lenSq
		if t < 0 {
			t = 0
		} else if t > 1 {
			t = 1
		}
	}
	projX, projY := ax+t*dx, ay+t*dy
	return math.Hypot(px-projX, py-projY)
}

func ringMinDistMeters(ring Ring, c Coord) float64 {
	best := math.Inf(1)
	n := len(ring)
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		if d := distToSegmentMeters(ring[i], ring[j], c); d < best {
			best = d
		}
	}
	return best
}

// ringToRingMinDistMeters approximates the minimum distance between two ring
// boundaries by checking each ring's vertices against the other ring's
// edges. This is exact whenever the closest approach lands on a vertex of
// either ring (the common case for adjacent admin/delivery polygons); it can
// slightly overestimate the true minimum for two segments that pass closest
// at a skew angle away from any vertex, which is an acceptable trade-off for
// a neighbor-detection heuristic.
func ringToRingMinDistMeters(a, b Ring) float64 {
	best := math.Inf(1)
	for _, p := range a {
		if d := ringMinDistMeters(b, p); d < best {
			best = d
		}
	}
	for _, p := range b {
		if d := ringMinDistMeters(a, p); d < best {
			best = d
		}
	}
	return best
}

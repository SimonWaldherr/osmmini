package osmmini

import "math"

// Tags represents OSM tags as a simple string map.
type Tags map[string]string

// Coord holds WGS84 coordinates.
type Coord struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

// CoordWindow is a bounding box in WGS84 coordinates.
type CoordWindow struct {
	MinLat float64 `json:"min_lat"`
	MaxLat float64 `json:"max_lat"`
	MinLon float64 `json:"min_lon"`
	MaxLon float64 `json:"max_lon"`
}

// Valid reports whether the window is well-formed.
func (w CoordWindow) Valid() bool {
	if w.MinLat >= w.MaxLat || w.MinLon >= w.MaxLon {
		return false
	}
	if w.MinLat < -90 || w.MaxLat > 90 || w.MinLon < -180 || w.MaxLon > 180 {
		return false
	}
	return true
}

// Center returns the geographic centre of the window.
func (w CoordWindow) Center() Coord {
	return Coord{
		Lat: (w.MinLat + w.MaxLat) / 2,
		Lon: (w.MinLon + w.MaxLon) / 2,
	}
}

// Contains reports whether c lies within w (inclusive).
func (w CoordWindow) Contains(c Coord) bool {
	return c.Lat >= w.MinLat && c.Lat <= w.MaxLat && c.Lon >= w.MinLon && c.Lon <= w.MaxLon
}

// ExpandMeters returns a copy of w enlarged in all directions by approximately m metres.
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
	return CoordWindow{
		MinLat: max(w.MinLat-latDeg, -90),
		MaxLat: min(w.MaxLat+latDeg, 90),
		MinLon: max(w.MinLon-lonDeg, -180),
		MaxLon: min(w.MaxLon+lonDeg, 180),
	}
}

// Node represents a node with coordinates and tags.
type Node struct {
	ID   int64
	Lat  float64
	Lon  float64
	Tags Tags
}

// Way represents a way with node references and tags.
type Way struct {
	ID      int64
	NodeIDs []int64
	Tags    Tags
}

// MemberType for relation members.
type MemberType int

const (
	MemberNode MemberType = iota
	MemberWay
	MemberRelation
)

// Member represents a relation member.
type Member struct {
	Type MemberType
	ID   int64
	Role string
}

// Relation represents an OSM relation with members and tags.
type Relation struct {
	ID      int64
	Members []Member
	Tags    Tags
}

// Options configures Extract behaviour.
type Options struct {
	KeepTag             func(string) bool
	EmitWayNodeIDs      bool
	EmitRelationMembers bool
}

// Callbacks passed to Extract to receive parsed entities.
type Callbacks struct {
	Node            func(id int64, lat, lon float64) error
	AddressNode     func(n Node) error
	HighwayWay      func(w Way) error
	AddressWay      func(w Way) error
	AddressRelation func(r Relation) error
}

// Package graphjson reads the export-graph wire format without retaining flat
// node and edge arrays alongside the router's maps.
package graphjson

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"

	osm "simonwaldherr.de/go/osmmini/core"
)

// Track presence separately: zero is a valid graph ID, but a missing/null ID
// must not silently create a node or edge endpoint at zero.
type graphID struct {
	value int64
	set   bool
}

func (id *graphID) UnmarshalJSON(data []byte) error {
	value, err := strconv.ParseInt(string(data), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid graph ID: %w", err)
	}
	id.value, id.set = value, true
	return nil
}

type node struct {
	ID  graphID `json:"id"`
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

type edge struct {
	From       graphID `json:"from"`
	To         graphID `json:"to"`
	DistM      float64 `json:"dist_m"`
	SpeedKph   float64 `json:"speed_kph"`
	MaxHeightM float64 `json:"max_height_m"`
	MaxWeightT float64 `json:"max_weight_t"`
	HwyType    string  `json:"hwy_type,omitempty"`
}

// Read accepts nodes and edges in either order. IDs stay int64 throughout JSON
// decoding, including values outside JavaScript's exact integer range. The
// caller publishes the returned router only after the entire input is valid.
func Read(input io.Reader) (*osm.Router, error) {
	d := json.NewDecoder(input)
	d.UseNumber() // Unknown extension fields may contain integers wider than float64.
	if err := expect(d, json.Delim('{')); err != nil {
		return nil, err
	}
	coords := make(map[int64]osm.Coord)
	adj := make(map[int64][]osm.Edge)
	seen := make(map[string]bool, 2)
	var n node
	var e edge
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("expected graph field")
		}
		if key != "nodes" && key != "edges" {
			if err := skipValue(d); err != nil {
				return nil, err
			}
			continue
		}
		if seen[key] {
			return nil, fmt.Errorf("duplicate graph field %q", key)
		}
		seen[key] = true
		if err := expect(d, json.Delim('[')); err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		for d.More() {
			if key == "nodes" {
				n = node{Lat: math.NaN(), Lon: math.NaN()}
				if err := d.Decode(&n); err != nil {
					return nil, fmt.Errorf("node: %w", err)
				}
				if !n.ID.set {
					return nil, fmt.Errorf("node requires id")
				}
				if !finite(n.Lat) || !finite(n.Lon) || n.Lat < -90 || n.Lat > 90 || n.Lon < -180 || n.Lon > 180 {
					return nil, fmt.Errorf("node %d: invalid coordinates", n.ID.value)
				}
				if _, exists := coords[n.ID.value]; exists {
					return nil, fmt.Errorf("duplicate node %d", n.ID.value)
				}
				coords[n.ID.value] = osm.Coord{Lat: n.Lat, Lon: n.Lon}
			} else {
				e = edge{DistM: math.NaN()}
				if err := d.Decode(&e); err != nil {
					return nil, fmt.Errorf("edge: %w", err)
				}
				if !e.From.set || !e.To.set {
					return nil, fmt.Errorf("edge requires from and to IDs")
				}
				if !nonnegative(e.DistM) || !nonnegative(e.SpeedKph) || !nonnegative(e.MaxHeightM) || !nonnegative(e.MaxWeightT) {
					return nil, fmt.Errorf("edge %d -> %d: invalid cost or restriction", e.From.value, e.To.value)
				}
				adj[e.From.value] = append(adj[e.From.value], osm.Edge{To: e.To.value, DistM: e.DistM, SpeedKph: e.SpeedKph, MaxHeightM: e.MaxHeightM, MaxWeightT: e.MaxWeightT, HwyType: e.HwyType})
			}
		}
		if err := expect(d, json.Delim(']')); err != nil {
			return nil, err
		}
	}
	if err := expect(d, json.Delim('}')); err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("unexpected value after graph")
		}
		return nil, err
	}
	if !seen["nodes"] || !seen["edges"] || len(coords) == 0 {
		return nil, fmt.Errorf("graph requires nonempty nodes and an edges array")
	}
	for from, edges := range adj {
		if _, ok := coords[from]; !ok {
			return nil, fmt.Errorf("unknown edge source %d", from)
		}
		for _, e := range edges {
			if _, ok := coords[e.To]; !ok {
				return nil, fmt.Errorf("unknown edge destination %d", e.To)
			}
		}
	}
	return osm.NewRouterFromGraph(coords, adj), nil
}

func finite(x float64) bool      { return !math.IsNaN(x) && !math.IsInf(x, 0) }
func nonnegative(x float64) bool { return finite(x) && x >= 0 }

func expect(d *json.Decoder, want json.Delim) error {
	got, err := d.Token()
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("expected %q, got %v", want, got)
	}
	return nil
}

// Skip extensions without buffering their complete JSON value. Decoder.Token
// validates nesting and syntax, including the matching closing delimiter.
func skipValue(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	if delim, ok := t.(json.Delim); ok && (delim == '[' || delim == '{') {
		depth := 1
		for depth > 0 {
			t, err = d.Token()
			if err != nil {
				return err
			}
			if delim, ok := t.(json.Delim); ok {
				switch delim {
				case '[', '{':
					depth++
				case ']', '}':
					depth--
				}
			}
		}
	}
	return nil
}

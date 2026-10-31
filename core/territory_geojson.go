package osmmini

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// territorySource loads a set of territories. GeoJSON is the only source
// today; a future compact/precompiled format (e.g. tinyTiles' planned
// .tterritory) can implement this same interface without changing
// TerritoryStore's API.
type territorySource interface {
	Load() ([]*Territory, error)
}

type geoJSONSource struct{ path string }

func (s geoJSONSource) Load() ([]*Territory, error) { return LoadTerritoriesGeoJSON(s.path) }

type rawFeatureCollection struct {
	Type     string       `json:"type"`
	Features []rawFeature `json:"features"`
}

type rawFeature struct {
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties"`
	Geometry   rawGeometry    `json:"geometry"`
}

type rawGeometry struct {
	Type        string          `json:"type"`
	Coordinates json.RawMessage `json:"coordinates"`
}

// LoadTerritoriesGeoJSON reads and parses a GeoJSON file of territory
// features (Polygon/MultiPolygon geometries with arbitrary properties, as
// produced by tinyTiles' territory builder).
func LoadTerritoriesGeoJSON(path string) ([]*Territory, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("territory: read %s: %w", path, err)
	}
	ts, err := ParseTerritoriesGeoJSON(data)
	if err != nil {
		return nil, fmt.Errorf("territory: parse %s: %w", path, err)
	}
	return ts, nil
}

// ParseTerritoriesGeoJSON parses a GeoJSON FeatureCollection (or a single
// bare Feature) whose geometries are Polygon or MultiPolygon. Every property
// is copied verbatim into Territory.Properties -- osmmini never assumes or
// requires specific field names beyond territory_id/id and
// territory_name/name for identifying the feature itself.
func ParseTerritoriesGeoJSON(data []byte) ([]*Territory, error) {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("invalid GeoJSON: %w", err)
	}

	var rawFeatures []rawFeature
	switch probe.Type {
	case "FeatureCollection":
		var fc rawFeatureCollection
		if err := json.Unmarshal(data, &fc); err != nil {
			return nil, fmt.Errorf("invalid FeatureCollection: %w", err)
		}
		rawFeatures = fc.Features
	case "Feature":
		var f rawFeature
		if err := json.Unmarshal(data, &f); err != nil {
			return nil, fmt.Errorf("invalid Feature: %w", err)
		}
		rawFeatures = []rawFeature{f}
	default:
		return nil, fmt.Errorf("unsupported top-level GeoJSON type %q (want FeatureCollection or Feature)", probe.Type)
	}

	out := make([]*Territory, 0, len(rawFeatures))
	for i, rf := range rawFeatures {
		geometry, err := decodeGeometry(rf.Geometry)
		if err != nil {
			return nil, fmt.Errorf("feature %d: %w", i, err)
		}
		out = append(out, &Territory{
			ID:         featureID(i, rf.Properties),
			Name:       featureName(rf.Properties),
			Geometry:   geometry,
			Properties: rf.Properties,
		})
	}
	return out, nil
}

func decodeGeometry(g rawGeometry) (Geometry, error) {
	switch g.Type {
	case "Polygon":
		var rings [][][2]float64
		if err := json.Unmarshal(g.Coordinates, &rings); err != nil {
			return Geometry{}, fmt.Errorf("decode Polygon coordinates: %w", err)
		}
		return Geometry{Polygons: []Polygon{polygonFromRaw(rings)}}, nil
	case "MultiPolygon":
		var polys [][][][2]float64
		if err := json.Unmarshal(g.Coordinates, &polys); err != nil {
			return Geometry{}, fmt.Errorf("decode MultiPolygon coordinates: %w", err)
		}
		out := make([]Polygon, len(polys))
		for i, rings := range polys {
			out[i] = polygonFromRaw(rings)
		}
		return Geometry{Polygons: out}, nil
	default:
		return Geometry{}, fmt.Errorf("unsupported geometry type %q (only Polygon/MultiPolygon)", g.Type)
	}
}

// polygonFromRaw converts GeoJSON [lon, lat] ring coordinates into a
// Polygon. The first ring is the exterior; any further rings are holes.
func polygonFromRaw(rings [][][2]float64) Polygon {
	if len(rings) == 0 {
		return Polygon{}
	}
	toRing := func(r [][2]float64) Ring {
		ring := make(Ring, len(r))
		for i, p := range r {
			ring[i] = Coord{Lon: p[0], Lat: p[1]}
		}
		return ring
	}
	p := Polygon{Outer: toRing(rings[0])}
	for _, r := range rings[1:] {
		p.Holes = append(p.Holes, toRing(r))
	}
	return p
}

// featureID picks a stable identifier for a territory feature: territory_id,
// then id, then the first property in sorted key order, then its index --
// matching the fallback tinyTiles' own `territory inspect` uses, so an
// osmmini identification of a feature agrees with tinyTiles' own tooling.
func featureID(index int, props map[string]any) string {
	for _, key := range []string{"territory_id", "id"} {
		if v, ok := props[key]; ok {
			return stringifyProp(v)
		}
	}
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		return stringifyProp(props[keys[0]])
	}
	return strconv.Itoa(index)
}

func featureName(props map[string]any) string {
	for _, key := range []string{"territory_name", "name"} {
		if v, ok := props[key]; ok {
			return stringifyProp(v)
		}
	}
	return ""
}

// stringifyProp is an alias for PropertyString, used where the call site is
// about identifying a feature rather than rendering a property for output.
func stringifyProp(v any) string { return PropertyString(v) }

// PropertyString renders a territory property value as plain text: scalars
// print directly, and a list (as produced by tinyTiles' aggregation, e.g.
// when merged source records disagree on a field) is joined with "|" --
// matching tinyTiles' own CSV export convention, so an osmmini CSV column
// and a tinyTiles CSV column built from the same data read the same.
func PropertyString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			parts[i] = PropertyString(item)
		}
		return strings.Join(parts, "|")
	default:
		return fmt.Sprint(x)
	}
}

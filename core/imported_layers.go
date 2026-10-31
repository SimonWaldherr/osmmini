package osmmini

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// ImportedLayer holds one named collection of GeoJSON features imported
// from an external file (GeoJSON, Shapefile, KML, ...). Unlike Territory,
// which is polygon-only because it backs area assignment and routing-cost
// lookups, an imported layer may contain any standard GeoJSON geometry
// (Point, LineString, Polygon and their Multi* variants). It exists purely
// for map display and attribute inspection, so the original GeoJSON bytes
// are the source of truth -- MapLibre already understands every standard
// geometry type, and osmmini never needs a typed Go decode of them.
type ImportedLayer struct {
	Name          string
	GeoJSON       []byte
	FeatureCount  int
	GeometryTypes []string // sorted, de-duplicated geometry type names present
}

// IsPolygonOnly reports whether every feature in the layer is a Polygon or
// MultiPolygon -- the signal callers use to decide whether an imported
// layer should ALSO be registered as a Territory layer (via
// TerritoryStore.LoadLayerTerritories) for dispatch/routing-cost use, on
// top of plain map display.
func (l *ImportedLayer) IsPolygonOnly() bool {
	if len(l.GeometryTypes) == 0 {
		return false
	}
	for _, t := range l.GeometryTypes {
		if t != "Polygon" && t != "MultiPolygon" {
			return false
		}
	}
	return true
}

// ParseImportedLayer validates a GeoJSON FeatureCollection or Feature and
// summarizes it, accepting any geometry type (unlike ParseTerritoriesGeoJSON,
// which rejects anything but Polygon/MultiPolygon). It reuses the same raw
// GeoJSON structural types as territory_geojson.go.
func ParseImportedLayer(name string, data []byte) (*ImportedLayer, error) {
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
	if len(rawFeatures) == 0 {
		return nil, errors.New("no features found")
	}

	seen := make(map[string]bool, 4)
	types := make([]string, 0, 4)
	for i, rf := range rawFeatures {
		if rf.Geometry.Type == "" {
			return nil, fmt.Errorf("feature %d: missing geometry", i)
		}
		if !seen[rf.Geometry.Type] {
			seen[rf.Geometry.Type] = true
			types = append(types, rf.Geometry.Type)
		}
	}
	sort.Strings(types)
	return &ImportedLayer{Name: name, GeoJSON: data, FeatureCount: len(rawFeatures), GeometryTypes: types}, nil
}

// ImportedLayerStore holds zero or more named imported layers. A hot-reload
// (directory rescan) replaces one layer at a time via LoadLayer, mirroring
// TerritoryStore's per-layer replace semantics.
type ImportedLayerStore struct {
	mu     sync.RWMutex
	layers map[string]*ImportedLayer
}

// NewImportedLayerStore returns an empty store.
func NewImportedLayerStore() *ImportedLayerStore {
	return &ImportedLayerStore{layers: make(map[string]*ImportedLayer)}
}

// LoadLayer validates geojson and installs it as name, replacing any
// existing layer with that name.
func (s *ImportedLayerStore) LoadLayer(name string, geojson []byte) (*ImportedLayer, error) {
	layer, err := ParseImportedLayer(name, geojson)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.layers[name] = layer
	s.mu.Unlock()
	return layer, nil
}

// Layer returns the named layer, or nil if it is not loaded.
func (s *ImportedLayerStore) Layer(name string) *ImportedLayer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.layers[name]
}

// Layers returns every loaded layer, sorted by name.
func (s *ImportedLayerStore) Layers() []*ImportedLayer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*ImportedLayer, 0, len(s.layers))
	for _, l := range s.layers {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Remove deletes a layer by name. Removing a name that is not loaded is a
// no-op.
func (s *ImportedLayerStore) Remove(name string) {
	s.mu.Lock()
	delete(s.layers, name)
	s.mu.Unlock()
}

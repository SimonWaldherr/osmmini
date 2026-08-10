package osmmini

import (
	"fmt"
	"math"
	"sort"
	"sync"
)

// neighborBufferMeters is both the bbox pre-filter buffer and the exact
// ring-distance threshold used to decide two territories are neighbors.
const neighborBufferMeters = 50

type territoryLayer struct {
	territories map[string]*Territory
	order       []*Territory
	index       *territoryGridIndex
	neighbors   map[string][]string
}

// TerritoryStore holds one or more independent, named territory layers
// (e.g. "sales", "delivery", "municipality"). Layers are never assumed to
// be mutually exclusive -- the same point can belong to a different
// territory in each layer, and a layer's own territories may overlap too.
type TerritoryStore struct {
	mu     sync.RWMutex
	layers map[string]*territoryLayer
}

// NewTerritoryStore returns an empty store.
func NewTerritoryStore() *TerritoryStore {
	return &TerritoryStore{layers: make(map[string]*territoryLayer)}
}

// LoadLayer loads territories for layer from a GeoJSON file at path,
// replacing any existing layer with that name.
func (s *TerritoryStore) LoadLayer(layer, path string) error {
	ts, err := LoadTerritoriesGeoJSON(path)
	if err != nil {
		return err
	}
	return s.LoadLayerTerritories(layer, ts)
}

// LoadLayerTerritories installs a pre-parsed set of territories as layer,
// replacing any existing layer with that name. This is the entry point for
// programmatic construction (tests, or a future non-GeoJSON source loaded
// elsewhere) as well as for LoadLayer itself.
func (s *TerritoryStore) LoadLayerTerritories(layer string, territories []*Territory) error {
	sorted := make([]*Territory, len(territories))
	copy(sorted, territories)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	byID := make(map[string]*Territory, len(sorted))
	for _, t := range sorted {
		t.Layer = layer
		if _, dup := byID[t.ID]; dup {
			return fmt.Errorf("territory: layer %q has duplicate territory id %q", layer, t.ID)
		}
		byID[t.ID] = t
	}

	tl := &territoryLayer{
		territories: byID,
		order:       sorted,
		index:       buildTerritoryIndex(sorted),
		neighbors:   computeNeighbors(sorted),
	}

	s.mu.Lock()
	s.layers[layer] = tl
	s.mu.Unlock()
	return nil
}

func (s *TerritoryStore) layer(name string) *territoryLayer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.layers[name]
}

// Layers returns the names of all loaded layers, sorted.
func (s *TerritoryStore) Layers() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.layers))
	for name := range s.layers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// FindTerritory returns the best-matching territory for (lat, lon) in
// layer, or nil if none matches. When several territories in the layer
// overlap at that point, the lowest territory ID wins -- deterministic
// regardless of load order or map iteration.
func (s *TerritoryStore) FindTerritory(layer string, lat, lon float64) *Territory {
	matches := s.FindTerritories(layer, lat, lon)
	if len(matches) == 0 {
		return nil
	}
	return matches[0]
}

// FindTerritories returns every territory in layer containing (lat, lon),
// sorted by ID.
func (s *TerritoryStore) FindTerritories(layer string, lat, lon float64) []*Territory {
	tl := s.layer(layer)
	if tl == nil {
		return nil
	}
	c := Coord{Lat: lat, Lon: lon}
	candidates := tl.index.candidatesAt(lat, lon)
	var matches []*Territory
	for _, t := range candidates {
		if t.Geometry.Contains(c) {
			matches = append(matches, t)
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
	return matches
}

// Territories returns every territory in layer, sorted by ID.
func (s *TerritoryStore) Territories(layer string) []*Territory {
	tl := s.layer(layer)
	if tl == nil {
		return nil
	}
	out := make([]*Territory, len(tl.order))
	copy(out, tl.order)
	return out
}

// FindTerritoryByID returns the territory with the given ID in layer, or
// nil if the layer or the ID is unknown.
func (s *TerritoryStore) FindTerritoryByID(layer, id string) *Territory {
	tl := s.layer(layer)
	if tl == nil {
		return nil
	}
	return tl.territories[id]
}

// Neighbors returns the IDs of territories in layer that are adjacent to
// territoryID (their boundaries touch or come within neighborBufferMeters
// of each other), sorted. Adjacency is computed once, when the layer loads.
func (s *TerritoryStore) Neighbors(layer, territoryID string) []string {
	tl := s.layer(layer)
	if tl == nil {
		return nil
	}
	return tl.neighbors[territoryID]
}

// computeNeighbors finds adjacent territory pairs: a cheap buffered-bbox
// pre-filter (using the existing CoordWindow.ExpandMeters) narrows the
// O(n^2) candidate pairs, then an exact ring-to-ring minimum distance
// decides real adjacency. This is sized for territory-layer counts (tens to
// low thousands of polygons), not parcel-scale data.
func computeNeighbors(territories []*Territory) map[string][]string {
	out := make(map[string][]string, len(territories))
	if len(territories) < 2 {
		return out
	}
	buffered := make([]CoordWindow, len(territories))
	for i, t := range territories {
		buffered[i] = t.Geometry.BBox().ExpandMeters(neighborBufferMeters)
	}
	for i := 0; i < len(territories); i++ {
		for j := i + 1; j < len(territories); j++ {
			if !bboxesOverlap(buffered[i], buffered[j]) {
				continue
			}
			if territoriesAdjacent(territories[i], territories[j]) {
				out[territories[i].ID] = append(out[territories[i].ID], territories[j].ID)
				out[territories[j].ID] = append(out[territories[j].ID], territories[i].ID)
			}
		}
	}
	for id := range out {
		sort.Strings(out[id])
	}
	return out
}

func bboxesOverlap(a, b CoordWindow) bool {
	return a.MinLat <= b.MaxLat && a.MaxLat >= b.MinLat && a.MinLon <= b.MaxLon && a.MaxLon >= b.MinLon
}

func territoriesAdjacent(a, b *Territory) bool {
	best := math.Inf(1)
	for _, pa := range a.Geometry.Polygons {
		for _, pb := range b.Geometry.Polygons {
			if d := ringToRingMinDistMeters(pa.Outer, pb.Outer); d < best {
				best = d
			}
		}
	}
	return best <= neighborBufferMeters
}

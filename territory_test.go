package osmmini

import "testing"

func square(minLat, minLon, maxLat, maxLon float64) Ring {
	return Ring{
		{Lat: minLat, Lon: minLon},
		{Lat: minLat, Lon: maxLon},
		{Lat: maxLat, Lon: maxLon},
		{Lat: maxLat, Lon: minLon},
		{Lat: minLat, Lon: minLon},
	}
}

func loadTestStore(t *testing.T, layer, path string) *TerritoryStore {
	t.Helper()
	s := NewTerritoryStore()
	if err := s.LoadLayer(layer, path); err != nil {
		t.Fatalf("load layer %s from %s: %v", layer, path, err)
	}
	return s
}

// 1. Point inside a plain Polygon.
func TestGeometryContainsPolygon(t *testing.T) {
	g := Geometry{Polygons: []Polygon{{Outer: square(0, 0, 10, 10)}}}
	if !g.Contains(Coord{Lat: 5, Lon: 5}) {
		t.Fatal("expected point inside the polygon to be contained")
	}
	if g.Contains(Coord{Lat: 20, Lon: 20}) {
		t.Fatal("expected point outside the polygon to not be contained")
	}
}

// 2. Point inside a MultiPolygon (one of several parts).
func TestGeometryContainsMultiPolygon(t *testing.T) {
	g := Geometry{Polygons: []Polygon{
		{Outer: square(0, 0, 10, 10)},
		{Outer: square(20, 20, 30, 30)},
	}}
	if !g.Contains(Coord{Lat: 25, Lon: 25}) {
		t.Fatal("expected point inside the second part to be contained")
	}
	if g.Contains(Coord{Lat: 15, Lon: 15}) {
		t.Fatal("expected point in the gap between parts to not be contained")
	}
}

// 3. Point inside a polygon hole must be excluded.
func TestGeometryContainsHole(t *testing.T) {
	g := Geometry{Polygons: []Polygon{{
		Outer: square(0, 0, 10, 10),
		Holes: []Ring{square(4, 4, 6, 6)},
	}}}
	if !g.Contains(Coord{Lat: 1, Lon: 1}) {
		t.Fatal("expected point inside the outer ring (outside the hole) to be contained")
	}
	if g.Contains(Coord{Lat: 5, Lon: 5}) {
		t.Fatal("expected point inside the hole to not be contained")
	}
}

// 4. Point exactly on a territory boundary is deterministically included.
func TestGeometryContainsBoundary(t *testing.T) {
	g := Geometry{Polygons: []Polygon{{Outer: square(0, 0, 10, 10)}}}
	if !g.Contains(Coord{Lat: 0, Lon: 5}) {
		t.Fatal("expected point exactly on an edge to be contained")
	}
	if !g.Contains(Coord{Lat: 0, Lon: 0}) {
		t.Fatal("expected point exactly on a vertex to be contained")
	}
}

// 5. Overlapping, independent territory layers: the same point can match a
// different territory in each layer. Uses real tinyTiles v2.2.0 output
// (`tinytiles territory --group-by delivery_zone` / `--group-by
// sales_territory` over the same source postcodes) so this is a genuine
// cross-project interop check, not just a synthetic fixture.
func TestOverlappingIndependentLayers(t *testing.T) {
	s := NewTerritoryStore()
	if err := s.LoadLayer("delivery", "testdata/territory/delivery-zones.geojson"); err != nil {
		t.Fatal(err)
	}
	if err := s.LoadLayer("sales", "testdata/territory/sales-territories.geojson"); err != nil {
		t.Fatal(err)
	}

	lat, lon := 48.65, 12.65
	delivery := s.FindTerritory("delivery", lat, lon)
	sales := s.FindTerritory("sales", lat, lon)
	if delivery == nil || delivery.ID != "Zone-1" {
		t.Fatalf("delivery layer: want Zone-1, got %v", delivery)
	}
	if sales == nil || sales.ID != "North" {
		t.Fatalf("sales layer: want North, got %v", sales)
	}
}

// 6. Unmatched destinations: FindTerritory returns nil rather than an
// arbitrary guess. AssignPoints policy behavior is covered in
// dispatch_test.go.
func TestFindTerritoryUnmatched(t *testing.T) {
	s := loadTestStore(t, "delivery", "testdata/territory/delivery-zones.geojson")
	if got := s.FindTerritory("delivery", 0, 0); got != nil {
		t.Fatalf("expected no match far outside every territory, got %v", got)
	}
}

// 7. Disconnected territory geometry: Zone-2 in the real fixture is a
// genuine MultiPolygon with two geographically separate parts (tinyTiles'
// dissolve keeps unrelated touching-groups apart rather than merging them).
func TestFindTerritoryDisconnectedMultiPolygon(t *testing.T) {
	s := loadTestStore(t, "delivery", "testdata/territory/delivery-zones.geojson")
	a := s.FindTerritory("delivery", 48.65, 12.85)
	b := s.FindTerritory("delivery", 48.35, 13.3)
	if a == nil || a.ID != "Zone-2" {
		t.Fatalf("expected first disjoint part to match Zone-2, got %v", a)
	}
	if b == nil || b.ID != "Zone-2" {
		t.Fatalf("expected second disjoint part to match Zone-2, got %v", b)
	}
	if len(a.Geometry.Polygons) < 2 {
		t.Fatalf("expected Zone-2 to have >= 2 disjoint polygons, got %d", len(a.Geometry.Polygons))
	}
}

// 9. Territory metadata propagation: arbitrary business properties (some
// scalar, some list-valued where tinyTiles' aggregation found disagreeing
// source values) come through unchanged.
func TestTerritoryMetadataPropagation(t *testing.T) {
	s := loadTestStore(t, "delivery", "testdata/territory/delivery-zones.geojson")

	z1 := s.FindTerritoryByID("delivery", "Zone-1")
	if z1 == nil {
		t.Fatal("expected Zone-1 to exist")
	}
	if z1.Properties["vehicle"] != "VAN-01" {
		t.Fatalf("Zone-1 vehicle = %v, want VAN-01", z1.Properties["vehicle"])
	}
	if z1.Properties["depot"] != "Depot-Passau" {
		t.Fatalf("Zone-1 depot = %v, want Depot-Passau", z1.Properties["depot"])
	}

	z2 := s.FindTerritoryByID("delivery", "Zone-2")
	if z2 == nil {
		t.Fatal("expected Zone-2 to exist")
	}
	depots, ok := z2.Properties["depot"].([]any)
	if !ok || len(depots) != 2 {
		t.Fatalf("Zone-2 depot = %v, want a 2-element list (merged source records disagree)", z2.Properties["depot"])
	}
}

// Neighbor detection: Zone-1 and Zone-2 in the real fixture genuinely share
// a boundary (postcode 84131, in Zone-1, sits directly against 84140, in
// Zone-2); Zone-3 is geographically far away and must not be a neighbor.
func TestNeighbors(t *testing.T) {
	s := loadTestStore(t, "delivery", "testdata/territory/delivery-zones.geojson")
	neighbors := s.Neighbors("delivery", "Zone-1")
	found := false
	for _, id := range neighbors {
		if id == "Zone-2" {
			found = true
		}
		if id == "Zone-3" {
			t.Fatalf("Zone-1 and Zone-3 are geographically far apart and should not be neighbors")
		}
	}
	if !found {
		t.Fatalf("expected Zone-1 and Zone-2 to be adjacent, got neighbors=%v", neighbors)
	}
}

// 12. Deterministic lookup: repeated queries against the same store return
// identical results. Deterministic *assignment* (batch API) is covered in
// dispatch_test.go.
func TestFindTerritoryDeterministic(t *testing.T) {
	s := loadTestStore(t, "delivery", "testdata/territory/delivery-zones.geojson")
	first := s.FindTerritory("delivery", 48.65, 12.65)
	for i := 0; i < 5; i++ {
		got := s.FindTerritory("delivery", 48.65, 12.65)
		if got == nil || first == nil || got.ID != first.ID {
			t.Fatalf("non-deterministic result across repeated calls: %v vs %v", first, got)
		}
	}
}

package osmmini

import "testing"

// 11. Routing territory transitions: TerritoryEventsForPath walks a
// polyline and reports the contiguous stretches spent in each territory,
// with entered/left km.
func TestTerritoryEventsForPath(t *testing.T) {
	s := NewTerritoryStore()
	err := s.LoadLayerTerritories("delivery", []*Territory{
		{ID: "west", Geometry: Geometry{Polygons: []Polygon{{Outer: square(0, 0, 10, 5)}}}},
		{ID: "east", Geometry: Geometry{Polygons: []Polygon{{Outer: square(0, 5, 10, 10)}}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// A path along lat=5 from lon=-1 to lon=11: starts outside both
	// territories, crosses "west" (lon 0..5), then "east" (lon 5..10), then
	// leaves again.
	path := []Coord{
		{Lat: 5, Lon: -1},
		{Lat: 5, Lon: 1},
		{Lat: 5, Lon: 4},
		{Lat: 5, Lon: 6},
		{Lat: 5, Lon: 9},
		{Lat: 5, Lon: 11},
	}
	events := TerritoryEventsForPath(s, "delivery", path)
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(events), events)
	}
	west, east := events[0], events[1]
	if west.TerritoryID != "west" || east.TerritoryID != "east" {
		t.Fatalf("unexpected event order/ids: %+v", events)
	}
	if !(west.EnteredAtKM < west.LeftAtKM) {
		t.Fatalf("west event km range invalid: %+v", west)
	}
	if !(east.EnteredAtKM < east.LeftAtKM) {
		t.Fatalf("east event km range invalid: %+v", east)
	}
	if !(west.LeftAtKM <= east.EnteredAtKM) {
		t.Fatalf("expected west to end at/before east begins: west=%+v east=%+v", west, east)
	}
}

func TestTerritoryEventsForPathNeverMatched(t *testing.T) {
	s := NewTerritoryStore()
	if err := s.LoadLayerTerritories("delivery", []*Territory{
		{ID: "far", Geometry: Geometry{Polygons: []Polygon{{Outer: square(50, 50, 60, 60)}}}},
	}); err != nil {
		t.Fatal(err)
	}
	path := []Coord{{Lat: 0, Lon: 0}, {Lat: 1, Lon: 1}, {Lat: 2, Lon: 2}}
	if events := TerritoryEventsForPath(s, "delivery", path); len(events) != 0 {
		t.Fatalf("expected no events for a path that never enters any territory, got %+v", events)
	}
}

// Territory-aware routing costs are opt-in: RouteOptions.TerritoryCost is
// nil by default, and a nil *TerritoryCostPolicy resolves to a no-op
// factor, so default routing is unaffected unless a caller sets it.
func TestTerritoryCostPolicyFactor(t *testing.T) {
	s := NewTerritoryStore()
	if err := s.LoadLayerTerritories("delivery", []*Territory{
		{ID: "home", Geometry: Geometry{Polygons: []Polygon{{Outer: square(0, 0, 10, 10)}}}},
		{ID: "next-door", Geometry: Geometry{Polygons: []Polygon{{Outer: square(0, 10, 10, 20)}}}},
		{ID: "far-away", Geometry: Geometry{Polygons: []Polygon{{Outer: square(100, 100, 110, 110)}}}},
	}); err != nil {
		t.Fatal(err)
	}

	neighbors := s.Neighbors("delivery", "home")
	found := false
	for _, id := range neighbors {
		if id == "next-door" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected home/next-door to be adjacent (they share the lon=10 edge), got neighbors=%v", neighbors)
	}

	policy := &TerritoryCostPolicy{
		Store: s, Layer: "delivery", HomeTerritoryID: "home",
		SameFactor: 1.0, NeighborFactor: 1.1, ForeignFactor: 1.5,
	}
	if f := policy.factorFor(Coord{Lat: 5, Lon: 5}); f != 1.0 {
		t.Fatalf("same-territory factor = %v, want 1.0", f)
	}
	if f := policy.factorFor(Coord{Lat: 5, Lon: 15}); f != 1.1 {
		t.Fatalf("neighbor-territory factor = %v, want 1.1", f)
	}
	if f := policy.factorFor(Coord{Lat: 105, Lon: 105}); f != 1.5 {
		t.Fatalf("foreign-territory factor = %v, want 1.5", f)
	}
	if f := policy.factorFor(Coord{Lat: 50, Lon: 50}); f != 1 {
		t.Fatalf("unmatched-point factor = %v, want 1 (no-op)", f)
	}

	var nilPolicy *TerritoryCostPolicy
	if f := nilPolicy.factorFor(Coord{Lat: 5, Lon: 5}); f != 1 {
		t.Fatalf("nil policy factor = %v, want 1 (default routing unaffected)", f)
	}
}

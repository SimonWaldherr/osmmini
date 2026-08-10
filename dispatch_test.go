package osmmini

import (
	"fmt"
	"math"
	"reflect"
	"testing"
)

// 6. Unmatched destinations: all four documented policies.
func TestAssignPointsUnassignedPolicies(t *testing.T) {
	s := loadTestStore(t, "delivery", "testdata/territory/delivery-zones.geojson")
	outside := Point{ID: "P-out", Lat: 0, Lon: 0}
	inside := Point{ID: "P-in", Lat: 48.65, Lon: 12.65}

	t.Run("unassigned (default)", func(t *testing.T) {
		got, err := s.AssignPointsWithOptions([]Point{outside}, AssignOptions{Layer: "delivery"})
		if err != nil {
			t.Fatal(err)
		}
		if got[0].Matched {
			t.Fatalf("expected unmatched point to stay unassigned, got %+v", got[0])
		}
	})

	t.Run("error", func(t *testing.T) {
		_, err := s.AssignPointsWithOptions([]Point{outside}, AssignOptions{Layer: "delivery", OnUnassigned: PolicyError})
		if err == nil {
			t.Fatal("expected an error for an unmatched point under PolicyError")
		}
	})

	t.Run("nearest", func(t *testing.T) {
		got, err := s.AssignPointsWithOptions([]Point{outside}, AssignOptions{Layer: "delivery", OnUnassigned: PolicyNearest})
		if err != nil {
			t.Fatal(err)
		}
		if !got[0].Matched || got[0].TerritoryID == "" {
			t.Fatalf("expected PolicyNearest to always match, got %+v", got[0])
		}
	})

	t.Run("fallback", func(t *testing.T) {
		got, err := s.AssignPointsWithOptions([]Point{outside}, AssignOptions{Layer: "delivery", OnUnassigned: UnassignedPolicy(PolicyFallbackPrefix + "Zone-3")})
		if err != nil {
			t.Fatal(err)
		}
		if !got[0].Matched || got[0].TerritoryID != "Zone-3" {
			t.Fatalf("expected fallback territory Zone-3, got %+v", got[0])
		}
	})

	// A point that does match should never be affected by the unassigned
	// policy.
	got, err := s.AssignPointsWithOptions([]Point{inside}, AssignOptions{Layer: "delivery", OnUnassigned: PolicyError})
	if err != nil {
		t.Fatal(err)
	}
	if !got[0].Matched || got[0].TerritoryID != "Zone-1" {
		t.Fatalf("expected matched point to resolve normally, got %+v", got[0])
	}
}

// 8. Large batch assignment: the layer's spatial index is built once at
// load time and reused across the whole batch.
func TestAssignPointsLargeBatch(t *testing.T) {
	s := loadTestStore(t, "delivery", "testdata/territory/delivery-zones.geojson")
	const n = 20000
	points := make([]Point, n)
	for i := 0; i < n; i++ {
		lat := 48.2 + float64(i%200)*0.005
		lon := 12.5 + float64((i/200)%200)*0.005
		points[i] = Point{ID: fmt.Sprintf("P%06d", i), Lat: lat, Lon: lon}
	}

	got, err := s.AssignPointsWithOptions(points, AssignOptions{Layer: "delivery"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("got %d assignments, want %d", len(got), n)
	}
	matched := 0
	for _, a := range got {
		if a.Matched {
			matched++
		}
	}
	if matched == 0 {
		t.Fatal("expected at least some of the scattered points to match a territory")
	}
}

// 12. Deterministic assignment: the same store and the same points slice
// always produce the same assignments, in the same order.
func TestAssignPointsDeterministic(t *testing.T) {
	s := loadTestStore(t, "delivery", "testdata/territory/delivery-zones.geojson")
	points := []Point{
		{ID: "P1", Lat: 48.65, Lon: 12.65},
		{ID: "P2", Lat: 48.62, Lon: 12.85},
		{ID: "P3", Lat: 40, Lon: 10},
	}
	first := s.AssignPoints(points, "delivery")
	for i := 0; i < 5; i++ {
		got := s.AssignPoints(points, "delivery")
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d: assignments differ across repeated calls:\n first=%+v\n got=%+v", i, first, got)
		}
	}
}

func TestAssignPointsIncludeFieldSelection(t *testing.T) {
	s := loadTestStore(t, "delivery", "testdata/territory/delivery-zones.geojson")
	got, err := s.AssignPointsWithOptions([]Point{{ID: "P1", Lat: 48.65, Lon: 12.65}}, AssignOptions{
		Layer:   "delivery",
		Include: []string{"territory_id", "depot", "vehicle"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got[0].Properties) != 2 {
		t.Fatalf("expected exactly depot+vehicle in Properties (territory_id is a top-level field, not duplicated), got %+v", got[0].Properties)
	}
	if _, ok := got[0].Properties["area_km2"]; ok {
		t.Fatalf("expected --include to exclude unselected properties, got %+v", got[0].Properties)
	}
}

func TestTerritoryConstraint(t *testing.T) {
	s := loadTestStore(t, "delivery", "testdata/territory/delivery-zones.geojson")
	van1 := Vehicle{ID: "VAN-01", Properties: map[string]any{"territory_id": "Zone-1"}}
	inZone1 := Shipment{ID: "S1", Lat: 48.65, Lon: 12.65}
	inZone3 := Shipment{ID: "S3", Lat: 48.98, Lon: 13.0}

	c := TerritoryConstraint{Store: s, Layer: "delivery", VehicleKey: "territory_id"}
	if !c.Accept(van1, inZone1) {
		t.Fatal("expected a vehicle assigned to Zone-1 to accept a shipment inside Zone-1")
	}
	if c.Accept(van1, inZone3) {
		t.Fatal("expected a vehicle assigned to Zone-1 to reject a shipment inside Zone-3")
	}
	if got := c.Cost(van1, inZone1); got != 0 {
		t.Fatalf("cost for an accepted shipment = %v, want 0", got)
	}
	if got := c.Cost(van1, inZone3); !math.IsInf(got, 1) {
		t.Fatalf("cost for a rejected shipment = %v, want +Inf", got)
	}
}

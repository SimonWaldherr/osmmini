package osmmini

import (
	"context"
	"math"
	"reflect"
	"testing"
)

type workPlanCostConstraint struct{ cost float64 }

func (c workPlanCostConstraint) Accept(Vehicle, Shipment) bool { return true }
func (c workPlanCostConstraint) Cost(v Vehicle, _ Shipment) float64 {
	if v.ID == "a" {
		return c.cost
	}
	return 0
}

func TestWorkPlanCustomCosts(t *testing.T) {
	req := WorkPlanRequest{Resources: []WorkResource{{ID: "a", Capacity: 1}, {ID: "b", Capacity: 1}}, Jobs: []WorkJob{{ID: "job", Demand: 1}}}
	got, err := PlanWork(context.Background(), req, workPlanCostConstraint{cost: 100})
	if err != nil || len(got.Manifests[1].JobIDs) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	for _, cost := range []float64{-1, math.Inf(1), math.NaN()} {
		if _, err := PlanWork(context.Background(), req, workPlanCostConstraint{cost: cost}); err == nil {
			t.Fatal("accepted invalid cost", cost)
		}
	}
}

func TestWorkPlanCapacitySkillsPriorityAndDeterminism(t *testing.T) {
	req := WorkPlanRequest{
		Resources: []WorkResource{
			{ID: "b", Location: Coord{48, 12}, Capacity: 5, Skills: []string{"electrician"}},
			{ID: "a", Location: Coord{48, 12}, Capacity: 5, MaxJobs: 1, Skills: []string{"electrician"}},
		},
		Jobs: []WorkJob{
			{ID: "low", Location: Coord{48, 12}, Demand: 3},
			{ID: "urgent", Location: Coord{48, 12}, Demand: 4, Priority: 10, RequiredSkills: []string{"electrician"}},
			{ID: "oversize", Demand: 6},
			{ID: "specialist", RequiredSkills: []string{"plumber"}},
		},
	}
	originalResources := append([]WorkResource(nil), req.Resources...)
	originalJobs := append([]WorkJob(nil), req.Jobs...)
	got, err := PlanWork(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Manifests[0].JobIDs, []string{"urgent"}) || !reflect.DeepEqual(got.Manifests[1].JobIDs, []string{"low"}) {
		t.Fatalf("unexpected allocation: %+v", got)
	}
	if got.Manifests[0].Load != 4 || got.Manifests[0].RemainingCapacity != 1 || got.Manifests[1].RemainingCapacity != 2 {
		t.Fatal(got.Manifests)
	}
	want := []UnassignedWork{{"oversize", "capacity_exceeded"}, {"specialist", "no_eligible_resource"}}
	if !reflect.DeepEqual(got.Unassigned, want) {
		t.Fatal(got.Unassigned)
	}
	if !reflect.DeepEqual(originalResources, req.Resources) || !reflect.DeepEqual(originalJobs, req.Jobs) {
		t.Fatal("input mutated")
	}
	req.Resources[0], req.Resources[1] = req.Resources[1], req.Resources[0]
	req.Jobs[0], req.Jobs[3] = req.Jobs[3], req.Jobs[0]
	again, err := PlanWork(context.Background(), req)
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatalf("unstable plan: %+v, %v", again, err)
	}
}

func TestWorkPlanNearestAndTerritory(t *testing.T) {
	s := loadTestStore(t, "delivery", "testdata/territory/delivery-zones.geojson")
	req := WorkPlanRequest{
		Resources: []WorkResource{
			{ID: "near", Location: Coord{48.65, 12.65}, Capacity: 10, Properties: map[string]any{"territory_id": "Zone-2"}},
			{ID: "far", Location: Coord{0, 0}, Capacity: 10, Properties: map[string]any{"territory_id": "Zone-1"}},
		},
		Jobs: []WorkJob{{ID: "visit", Location: Coord{48.65, 12.65}, Demand: 1}},
	}
	got, err := PlanWork(context.Background(), req)
	if err != nil || len(got.Manifests[1].JobIDs) != 1 {
		t.Fatalf("nearest: %+v %v", got, err)
	}
	got, err = PlanWork(context.Background(), req, TerritoryConstraint{Store: s, Layer: "delivery", VehicleKey: "territory_id"})
	if err != nil || len(got.Manifests[0].JobIDs) != 1 {
		t.Fatalf("territory: %+v %v", got, err)
	}
}

func TestWorkPlanValidationAndCancellation(t *testing.T) {
	for _, req := range []WorkPlanRequest{
		{Resources: []WorkResource{{ID: "a", Capacity: -1}}},
		{Resources: []WorkResource{{ID: "a", Capacity: math.Inf(1)}}},
		{Resources: []WorkResource{{ID: "a", MaxJobs: -1}}},
		{Resources: []WorkResource{{ID: "a"}, {ID: "a"}}},
		{Jobs: []WorkJob{{ID: "a", Location: Coord{Lat: math.NaN()}}}},
		{Jobs: []WorkJob{{ID: "a", Location: Coord{Lon: 181}}}},
		{Jobs: []WorkJob{{ID: "a", Demand: -1}}},
		{Jobs: []WorkJob{{ID: " "}}},
		{Jobs: []WorkJob{{ID: "a"}, {ID: "a"}}},
		{Jobs: make([]WorkJob, 5001)},
		{Resources: make([]WorkResource, 257)},
	} {
		if _, err := PlanWork(context.Background(), req); err == nil {
			t.Fatalf("accepted invalid request %+v", req)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PlanWork(ctx, WorkPlanRequest{}); err != context.Canceled {
		t.Fatal(err)
	}
	got, err := PlanWork(context.Background(), WorkPlanRequest{Jobs: []WorkJob{{ID: "a"}}})
	if err != nil || len(got.Unassigned) != 1 || got.Unassigned[0].Reason != "no_eligible_resource" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestWorkPlanZeroCapacityAndMaxJobs(t *testing.T) {
	got, err := PlanWork(context.Background(), WorkPlanRequest{
		Resources: []WorkResource{{ID: "a", MaxJobs: 1}},
		Jobs:      []WorkJob{{ID: "a"}, {ID: "b"}, {ID: "c", Demand: 1}},
	})
	if err != nil || len(got.Manifests[0].JobIDs) != 1 || len(got.Unassigned) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
}

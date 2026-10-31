package osmmini

import (
	"math"
	"path/filepath"
	"slices"
	"testing"
)

func TestPreparedRouteCostsAndConstraints(t *testing.T) {
	edge := Edge{To: 2, DistM: 100, SpeedKph: 50, HwyType: "residential", MaxHeightM: 3, MaxWeightT: 5}
	for _, tc := range []struct {
		name           string
		options        RouteOptions
		cost, duration float64
		allowed        bool
	}{
		{"car", RouteOptions{Profile: ProfileCar}, 7.2, 7.2, true},
		{"delivery", RouteOptions{Profile: ProfileDelivery}, 7.2, 7.2, true},
		{"walking", RouteOptions{Profile: ProfileWalking}, 100, 120, true},
		{"cycling", RouteOptions{Profile: ProfileCycling}, 100, 28.8, true},
		{"travel-restriction", RouteOptions{Profile: ProfileTravel}, .1175, 7.2, false},
		{"truck-restriction", RouteOptions{Profile: ProfileTruck}, 7.2, 7.2, false},
		{"unknown-profile", RouteOptions{Profile: "custom"}, 100, 7.2, true},
		{"speed-cap", RouteOptions{Profile: ProfileCar, Weights: ProWeights{MaxSpeedKph: 20}}, 18, 18, true},
		{"height", RouteOptions{Weights: ProWeights{VehicleHeightM: 3.1}}, 100, 7.2, false},
		{"weight", RouteOptions{Weights: ProWeights{VehicleWeightT: 5.1}}, 100, 7.2, false},
		{"emergency", RouteOptions{EmergencyMode: true}, 360.0 / 65, 7.2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eval := newRouteEvaluator(tc.options.withDefaults())
			if got := eval.baseEdgeCost(edge); math.Abs(got-tc.cost) > 1e-12 {
				t.Fatalf("cost = %.15g, want %.15g", got, tc.cost)
			}
			if got := eval.edgeTimeSeconds(edge); math.Abs(got-tc.duration) > 1e-12 {
				t.Fatalf("duration = %.15g, want %.15g", got, tc.duration)
			}
			if got := eval.edgeAllowed(edge); got != tc.allowed {
				t.Fatalf("allowed = %v, want %v", got, tc.allowed)
			}
			// An unclassified edge was historically allowed by every profile.
			unclassified := edge
			unclassified.HwyType = ""
			unclassified.MaxHeightM, unclassified.MaxWeightT = 0, 0
			if !eval.edgeAllowed(unclassified) {
				t.Fatal("missing highway type or dimensional limits must not reject an edge")
			}
		})
	}
}

func TestPreparedRouteHeuristicRetainsGreatCircleFormula(t *testing.T) {
	coords := map[int64]Coord{
		1: {Lat: 48, Lon: 12}, 2: {Lat: 48.001, Lon: 12.002},
		3: {Lat: -33.865, Lon: 151.209}, 4: {Lat: 89.9, Lon: -179.9},
		5: {Lat: 89.9, Lon: 179.9}, 6: {Lat: 0, Lon: 0},
	}
	router := NewRouterFromGraph(coords, nil)
	for _, engine := range []RouteEngine{EngineAStar, EngineDijkstra, EngineDijkstraNode} {
		for _, objective := range []Objective{ObjectiveDistance, ObjectiveDuration, ObjectiveEconomy} {
			opt := (RouteOptions{Engine: engine, Objective: objective, Profile: ProfileWalking}).withDefaults()
			for _, target := range coords {
				h := newRouteHeuristic(target, opt)
				for id, coord := range coords {
					want := 0.0
					if engine == EngineAStar {
						want = haversineMeters(coord.Lat, coord.Lon, target.Lat, target.Lon)
						switch objective {
						case ObjectiveDuration:
							want = want * 3.6 / opt.Weights.MaxSpeedKph
						case ObjectiveEconomy:
							want /= 1000
						}
					}
					if got := h.estimate(router, id); got != want {
						t.Fatalf("%s/%s node %d to %+v: %.17g, want %.17g", engine, objective, id, target, got, want)
					}
				}
			}
		}
	}
}

func TestPreparedRouteTransitionsMatchIndividualTurns(t *testing.T) {
	memory, _, _ := buildGridRouter(3, 3)
	file := filepath.Join(t.TempDir(), "graph")
	if err := memory.WriteGraph(t.Context(), file); err != nil {
		t.Fatal(err)
	}
	disk, err := OpenGraph(file)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	for _, router := range []*Router{memory, disk} {
		for _, weights := range []ProWeights{
			{}, {LeftTurn: 3, RightTurn: 2, UTurn: 10},
			{NoLeftTurn: true, LeftTurn: -2, RightTurn: -1, UTurn: 0.5},
			{Crossing: 1.5, TrafficLightPenalty: 4, LeftTurn: 2},
			{Crossing: -1, TrafficLightPenalty: 4},
		} {
			eval := newRouteEvaluator(RouteOptions{Weights: weights})
			for _, previous := range []int64{0, 2, 4, 6, 8, 99} {
				transition := eval.prepareTransition(router, previous, 5)
				for _, next := range []int64{2, 4, 6, 8, 99} {
					want := 0.0
					if previous != 0 {
						if weights.Crossing > 0 {
							want += weights.Crossing
						}
						if weights.TrafficLightPenalty > 0 {
							want += weights.TrafficLightPenalty
						}
						switch router.turnType(previous, 5, next) {
						case turnLeft:
							if weights.NoLeftTurn {
								want += noLeftTurnPenalty
							} else {
								want += weights.LeftTurn
							}
						case turnRight:
							want += weights.RightTurn
						case turnUTurn:
							want += weights.UTurn
						}
					}
					if got := transition.penalty(router, next); got != want {
						t.Fatalf("%d -> 5 -> %d, weights %+v: %g, want %g", previous, next, weights, got, want)
					}
				}
			}
		}
	}
}

func TestPreparedProfileRoutesMatchFileGraph(t *testing.T) {
	memory, from, to := buildGridRouter(8, 8)
	file := filepath.Join(t.TempDir(), "graph")
	if err := memory.WriteGraph(t.Context(), file); err != nil {
		t.Fatal(err)
	}
	disk, err := OpenGraph(file)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	for _, profile := range []VehicleProfile{ProfileCar, ProfileWalking, ProfileDelivery} {
		for _, engine := range []RouteEngine{EngineAStar, EngineDijkstra, EngineDijkstraNode} {
			opt := RouteOptions{Engine: engine, Profile: profile}
			want, err := memory.RouteWithOptions(t.Context(), from, to, opt)
			if err != nil {
				t.Fatal(err)
			}
			got, err := disk.RouteWithOptions(t.Context(), from, to, opt)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got.Path, want.Path) || got.Cost != want.Cost || got.DistanceM != want.DistanceM || got.DurationS != want.DurationS {
				t.Fatalf("%s/%s file route %+v differs from memory route %+v", engine, profile, got, want)
			}
			cost, err := disk.RouteCostWithOptions(t.Context(), from, to, opt)
			if err != nil || cost != got.Cost {
				t.Fatalf("%s/%s cost-only = %g, %v; want %g", engine, profile, cost, err, got.Cost)
			}
		}
	}
}

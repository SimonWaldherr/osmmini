package osmmini

import (
	"math"
	"slices"
	"testing"
)

func TestCompactAttributeCostsMatchEdgeScoringAcrossCacheCollisions(t *testing.T) {
	speeds := []float64{0, math.Copysign(0, -1), -1, 0.25, 2.99, 3, 50, 130, 200, math.Inf(1), math.Inf(-1), math.Float64frombits(0x7ff8000000000042)}
	classes := []string{"residential", "motorway", "track", "", "custom"}
	edges := make([]Edge, 300)
	for i := range edges {
		edges[i] = Edge{To: 2, DistM: float64(i) + 0.1, SpeedKph: speeds[i%len(speeds)], MaxHeightM: float64(i % 9), MaxWeightT: float64(i % 21), HwyType: classes[i%len(classes)]}
	}
	r := NewRouterFromGraph(map[int64]Coord{1: {Lat: 48, Lon: 12}, 2: {Lat: 48.01, Lon: 12}}, map[int64][]Edge{1: edges})
	disk, _ := writeChainTestGraph(t, r)
	d := disk.g.store
	type record struct {
		target, attribute uint32
		distance          float64
		edge              Edge
	}
	var records []record
	for it := d.edgesAt(d.nodeIndex(1)); it.next(); {
		target, attribute, distance := it.compactFields()
		records = append(records, record{target, attribute, distance, it.edge()})
	}
	distances := []float64{math.Copysign(0, -1), 0, .25, 37.1, 1e-99, 1e100, math.Inf(1), math.Float64frombits(0x7ff8000000000011)}
	for _, profile := range []VehicleProfile{"", ProfileCar, ProfileDelivery, ProfileTruck, ProfileWalking, ProfileCycling, ProfileTravel, ProfileEmergency} {
		for _, objective := range []Objective{ObjectiveDistance, ObjectiveDuration, ObjectiveEconomy} {
			for _, emergency := range []bool{false, true} {
				opt := (RouteOptions{Profile: profile, Objective: objective, EmergencyMode: emergency, Weights: ProWeights{MaxSpeedKph: 80, VehicleHeightM: 3.5, VehicleWeightT: 7.5}}).withDefaults()
				eval := newRouteEvaluator(opt)
				cache := newCompactRouteCosts(eval)
				for pass := 0; pass < 2; pass++ {
					for _, rec := range records {
						if int(rec.target) != d.nodeIndex(rec.edge.To) || math.Float64bits(rec.distance) != math.Float64bits(rec.edge.DistM) {
							t.Fatal("compact edge fields changed")
						}
						for _, distance := range distances {
							edge := rec.edge
							edge.DistM = distance
							got, allowed := cache.edgeCost(d, rec.attribute, distance)
							if allowed != eval.edgeAllowed(edge) {
								t.Fatalf("access changed: %s/%s/%v", profile, objective, emergency)
							}
							if !allowed {
								continue
							}
							want := eval.baseEdgeCost(edge)
							if math.Float64bits(got) != math.Float64bits(want) && !(math.IsNaN(got) && math.IsNaN(want)) {
								t.Fatalf("cost changed: %s/%s/%v, distance=%g: %.17g vs %.17g", profile, objective, emergency, distance, got, want)
							}
						}
					}
					slices.Reverse(records)
				}
			}
		}
	}
}

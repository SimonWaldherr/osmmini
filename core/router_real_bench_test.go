package osmmini

import (
	"os"
	"testing"
)

// Opt in with the same before/after graph files used by the equivalence audit.
// Opening and validation are outside the query measurements.
func BenchmarkRealGraphStorageRouting(b *testing.B) {
	benchmarkRealGraphStorageRouting(b, true)
}

func BenchmarkRealGraphStorageCostRouting(b *testing.B) {
	benchmarkRealGraphStorageRouting(b, false)
}

func benchmarkRealGraphStorageRouting(b *testing.B, wantPath bool) {
	for _, fixture := range []struct{ name, environment string }{{"original", "OSMMINI_ORIGINAL_GRAPH"}, {"packed", "OSMMINI_REAL_GRAPH"}} {
		path := os.Getenv(fixture.environment)
		if path == "" {
			b.Skip("set OSMMINI_ORIGINAL_GRAPH and OSMMINI_REAL_GRAPH")
		}
		b.Run(fixture.name, func(b *testing.B) {
			r, err := OpenGraph(path)
			if err != nil {
				b.Fatal(err)
			}
			defer r.Close()
			from, _, ok := r.NearestNode(48.574, 13.450)
			if !ok {
				b.Fatal("missing Passau endpoint")
			}
			to, _, ok := r.NearestNode(48.549, 12.137)
			if !ok {
				b.Fatal("missing Landshut endpoint")
			}
			for _, objective := range []Objective{ObjectiveDistance, ObjectiveDuration, ObjectiveEconomy} {
				b.Run(string(objective), func(b *testing.B) {
					opt := RouteOptions{Profile: ProfileCar, Objective: objective}
					b.ReportAllocs()
					b.ResetTimer()
					for range b.N {
						if wantPath {
							route, err := r.RouteWithOptions(b.Context(), from, to, opt)
							if err != nil {
								b.Fatal(err)
							}
							graphStoreBenchmarkSum = int64(route.Cost)
						} else {
							cost, err := r.RouteCostWithOptions(b.Context(), from, to, opt)
							if err != nil {
								b.Fatal(err)
							}
							graphStoreBenchmarkSum = int64(cost)
						}
					}
				})
			}
		})
	}
}

package osmmini

import "testing"

// Compare the ID-based search used by in-memory/server graphs with its indexed
// counterpart. Setup and graph construction are outside query measurements.
func BenchmarkNodeSearchLabels(b *testing.B) {
	router, from, to := buildGridRouter(100, 100)
	for _, query := range []struct {
		name string
		to   int64
	}{{"short", from + 1}, {"regional", to}} {
		b.Run(query.name, func(b *testing.B) {
			for _, wantPath := range []bool{false, true} {
				name := "cost"
				if wantPath {
					name = "path"
				}
				b.Run(name, func(b *testing.B) {
					opt := (RouteOptions{Engine: EngineAStar, Profile: ProfileCar, Objective: ObjectiveDuration}).withDefaults()
					b.ReportAllocs()
					for range b.N {
						_, cost, err := router.nodeSearch(b.Context(), from, query.to, opt, wantPath)
						if err != nil {
							b.Fatal(err)
						}
						graphStoreBenchmarkSum = int64(cost)
					}
				})
			}
		})
	}
}

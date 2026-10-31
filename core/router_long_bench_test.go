package osmmini

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

// OSMMINI_LONG_GRAPH points at a Niederbayern graph created with WriteGraph.
// Fixture loading, endpoint snapping and the warm-up query are not timed.
// No route-result cache is used. Each iteration builds the complete path.
func BenchmarkLongRoutes(b *testing.B) {
	path := os.Getenv("OSMMINI_LONG_GRAPH")
	if path == "" {
		source := os.Getenv("OSMMINI_LONG_PBF")
		if source == "" {
			b.Skip("set OSMMINI_LONG_GRAPH or OSMMINI_LONG_PBF to a Niederbayern fixture")
		}
		imported, _, err := BuildRouterWithAddressesOptions(source, BuildOptions{Context: b.Context(), SkipAddresses: true})
		if err != nil {
			b.Fatal(err)
		}
		path = filepath.Join(b.TempDir(), "long-routes.graph")
		if err := imported.WriteGraph(b.Context(), path); err != nil {
			b.Fatal(err)
		}
	}
	r, err := OpenGraph(path)
	if err != nil {
		b.Fatal(err)
	}
	defer r.Close()
	b.Logf("nodes=%d directed_edges=%d", r.NodeCount(), r.EdgeCount())
	for _, route := range []struct {
		name     string
		from, to Coord
	}{
		{"Landshut_Passau", Coord{48.549, 12.137}, Coord{48.574, 13.450}},
		{"Kelheim_Passau", Coord{48.917, 11.870}, Coord{48.574, 13.450}},
		{"Landshut_Freyung", Coord{48.549, 12.137}, Coord{48.808, 13.547}},
	} {
		b.Run(route.name, func(b *testing.B) {
			from, fromDistance, fromOK := r.NearestNode(route.from.Lat, route.from.Lon)
			to, toDistance, toOK := r.NearestNode(route.to.Lat, route.to.Lon)
			if !fromOK || !toOK || fromDistance > 1500 || toDistance > 1500 {
				b.Fatalf("endpoints outside fixture: %g/%g m", fromDistance, toDistance)
			}
			for _, profile := range []VehicleProfile{ProfileCar, ProfileDelivery} {
				b.Run(string(profile), func(b *testing.B) {
					options := RouteOptions{Profile: profile}
					expected, err := r.RouteWithOptions(b.Context(), from, to, options)
					if err != nil {
						b.Fatal(err)
					}
					b.ReportAllocs()
					b.ResetTimer()
					cpuStart := benchmarkCPUSeconds()
					for range b.N {
						result, err := r.RouteWithOptions(b.Context(), from, to, options)
						if err != nil || math.Abs(result.Cost-expected.Cost) > 1e-9*math.Max(1, expected.Cost) {
							b.Fatalf("route cost changed: %g vs %g, %v", result.Cost, expected.Cost, err)
						}
						graphStoreBenchmarkSum = int64(result.Cost)
					}
					if cpu := benchmarkCPUSeconds() - cpuStart; cpu > 0 {
						b.ReportMetric(cpu*1000/float64(b.N), "cpu-ms/op")
					}
					b.ReportMetric(expected.DistanceM/1000, "km/route")
				})
			}
		})
	}
}

// A 360,000-node grid supplements the real-road fixtures. It deliberately
// stresses broad search fronts; its timings are not production-road estimates.
func BenchmarkLongRouteGrid(b *testing.B) {
	r, from, to := buildGridRouter(600, 600)
	for _, profile := range []VehicleProfile{ProfileCar, ProfileDelivery} {
		b.Run(string(profile), func(b *testing.B) {
			options := RouteOptions{Profile: profile}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				result, err := r.RouteWithOptions(b.Context(), from, to, options)
				if err != nil {
					b.Fatal(err)
				}
				graphStoreBenchmarkSum = int64(result.Cost)
			}
		})
	}
}

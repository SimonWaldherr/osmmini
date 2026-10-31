package osmmini

import (
	"context"
	"path/filepath"
	"testing"
)

// Exercise both the node-state and turn-state search with profiles used by
// server callers and the Apple SDK. Geometry and edge weights are identical
// for the map-backed and file-backed graphs.
func BenchmarkRouteProfiles(b *testing.B) {
	memory, from, to := buildGridRouter(100, 100)
	file := filepath.Join(b.TempDir(), "graph")
	if err := memory.WriteGraph(context.Background(), file); err != nil {
		b.Fatal(err)
	}
	disk, err := OpenGraph(file)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { disk.Close() })
	for _, graph := range []struct {
		name   string
		router *Router
	}{{"memory", memory}, {"file", disk}} {
		b.Run(graph.name, func(b *testing.B) {
			for _, profile := range []VehicleProfile{ProfileCar, ProfileWalking, ProfileDelivery} {
				b.Run(string(profile), func(b *testing.B) {
					opt := RouteOptions{Profile: profile}
					want, err := graph.router.RouteCostWithOptions(context.Background(), from, to, opt)
					if err != nil {
						b.Fatal(err)
					}
					b.ReportAllocs()
					b.ResetTimer()
					for range b.N {
						got, err := graph.router.RouteCostWithOptions(context.Background(), from, to, opt)
						if err != nil || got != want {
							b.Fatalf("cost = %g, %v; want %g", got, err, want)
						}
					}
				})
			}
		})
	}
}

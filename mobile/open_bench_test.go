package mobile

import (
	"os"
	"testing"

	osm "simonwaldherr.de/go/osmmini/core"
)

// Includes integrity checks, graph validation, search/place loading and tile
// opening. Files may be in the OS cache; this is not a cold-device launch test.
func BenchmarkRegionOpen(b *testing.B) {
	path := os.Getenv("OSMMINI_REAL_REGION")
	if path == "" {
		b.Skip("set OSMMINI_REAL_REGION to a prepared offline region")
	}
	b.ReportAllocs()
	for b.Loop() {
		engine, err := Open(b.Context(), path)
		if err != nil {
			b.Fatal(err)
		}
		if err := engine.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

// Includes endpoint snapping, the complete graph search, coordinates and all
// maneuver instructions used by Swift. Region opening is excluded.
func BenchmarkRegionRoute(b *testing.B) {
	path := os.Getenv("OSMMINI_REAL_REGION")
	if path == "" {
		b.Skip("set OSMMINI_REAL_REGION to a prepared offline region")
	}
	engine, err := Open(b.Context(), path)
	if err != nil {
		b.Fatal(err)
	}
	defer engine.Close()
	for _, objective := range []osm.Objective{osm.ObjectiveDistance, osm.ObjectiveDuration, osm.ObjectiveEconomy} {
		b.Run(string(objective), func(b *testing.B) {
			request := RouteRequest{
				From:    osm.Coord{Lat: 48.574, Lon: 13.450},
				To:      osm.Coord{Lat: 48.549, Lon: 12.137},
				Options: osm.RouteOptions{Profile: osm.ProfileCar, Objective: objective},
			}
			b.ReportAllocs()
			for b.Loop() {
				route, err := engine.Route(b.Context(), request)
				if err != nil || len(route.Path) < 2 || len(route.Steps) < 2 {
					b.Fatalf("route: %d points, %d instructions, %v", len(route.Path), len(route.Steps), err)
				}
			}
		})
	}
}

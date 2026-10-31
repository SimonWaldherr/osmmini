package osmmini

import (
	"os"
	"path/filepath"
	"testing"
)

// Measure snapping separately from route search on a regional grid. Queries
// touch different cells, including boundaries, while graph setup stays outside
// the measurements. The memory and V1 variants guard the unchanged fallbacks.
func BenchmarkNodeSnapping(b *testing.B) {
	memory, _, _ := buildGridRouter(200, 200)
	queries := []Coord{
		{Lat: 48.0802, Lon: 12.0803},
		{Lat: 48.09999, Lon: 12.10001},
		{Lat: 48.1704, Lon: 12.1504},
	}
	for _, format := range []string{"memory", "legacy", "compact"} {
		b.Run(format, func(b *testing.B) {
			router := memory
			if format != "memory" {
				path := filepath.Join(b.TempDir(), "graph.bin")
				if format == "legacy" {
					if err := os.WriteFile(path, legacyGraphFixture(b, memory), 0o600); err != nil {
						b.Fatal(err)
					}
				} else if err := memory.WriteGraph(b.Context(), path); err != nil {
					b.Fatal(err)
				}
				var err error
				router, err = OpenGraph(path)
				if err != nil {
					b.Fatal(err)
				}
				defer router.Close()
			}
			b.Run("nearest", func(b *testing.B) {
				b.ReportAllocs()
				for i := range b.N {
					query := queries[i%len(queries)]
					id, _, ok := router.NearestNode(query.Lat, query.Lon)
					if !ok {
						b.Fatal("no nearest node")
					}
					graphStoreBenchmarkSum = id
				}
			})
			b.Run("nearby", func(b *testing.B) {
				b.ReportAllocs()
				for i := range b.N {
					query := queries[i%len(queries)]
					nodes, err := router.NearestNodes(b.Context(), query.Lat, query.Lon, 3, 250)
					if err != nil || len(nodes) != 3 {
						b.Fatalf("nearby nodes: %v, %v", nodes, err)
					}
					graphStoreBenchmarkSum = nodes[0].ID
				}
			})
		})
	}
}

package osmmini

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

var graphStoreBenchmarkSum int64
var graphStoreBenchmarkCoords map[int64]Coord
var graphStoreBenchmarkAdj map[int64][]Edge

func benchmarkFileGraph(b *testing.B) *Router {
	b.Helper()
	r, _, _ := buildGridRouter(200, 200)
	path := filepath.Join(b.TempDir(), "graph.bin")
	if err := r.WriteGraph(context.Background(), path); err != nil {
		b.Fatal(err)
	}
	disk, err := OpenGraph(path)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = disk.Close() })
	return disk
}

func BenchmarkFileGraphScan(b *testing.B) {
	r := benchmarkFileGraph(b)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		var sum int64
		r.g.eachNode(func(id int64, c Coord) { sum += id + int64(c.Lat) })
		graphStoreBenchmarkSum = sum
	}
}

func BenchmarkFileGraphCell(b *testing.B) {
	r := benchmarkFileGraph(b)
	x, y := r.idx.cell(48.08, 12.08)
	key := cellKey(x, y)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		var sum int64
		for it := r.g.store.cellNodesIter(key); it.next(); {
			sum += it.nodeID()
		}
		graphStoreBenchmarkSum = sum
	}
}

func BenchmarkFileGraphExport(b *testing.B) {
	r := benchmarkFileGraph(b)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		graphStoreBenchmarkCoords, graphStoreBenchmarkAdj = r.ExportGraph()
	}
}

func BenchmarkFileGraphRewrite(b *testing.B) {
	r := benchmarkFileGraph(b)
	path := filepath.Join(b.TempDir(), "graph-copy.bin")
	b.ReportAllocs()
	b.SetBytes(int64(len(r.g.store.data)))
	b.ResetTimer()
	for range b.N {
		if err := r.WriteGraph(context.Background(), path); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		if err := os.Remove(path); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
	}
}

func BenchmarkMemoryGraphWrite(b *testing.B) {
	r, _, _ := buildGridRouter(200, 200)
	path := filepath.Join(b.TempDir(), "graph.bin")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := r.WriteGraph(context.Background(), path); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		if err := os.Remove(path); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
	}
}

// Compare file-backed reads without expanding either format into a Go graph.
// Setup bytes and writes are excluded; graphs contain the same exact edges.
func BenchmarkGraphStorageFormats(b *testing.B) {
	memory, from, to := buildGridRouter(200, 200)
	for _, format := range []string{"legacy", "compact"} {
		b.Run(format, func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "graph.bin")
			if format == "legacy" {
				if err := os.WriteFile(path, legacyGraphFixture(b, memory), 0o600); err != nil {
					b.Fatal(err)
				}
			} else if err := memory.WriteGraph(context.Background(), path); err != nil {
				b.Fatal(err)
			}
			disk, err := OpenGraph(path)
			if err != nil {
				b.Fatal(err)
			}
			defer disk.Close()
			b.Run("edges", func(b *testing.B) {
				b.ReportMetric(float64(len(disk.g.store.data)), "file_bytes")
				b.ReportAllocs()
				for range b.N {
					var sum int64
					for node := 0; node < disk.g.store.nodes; node++ {
						for it := disk.g.store.edgesAt(node); it.next(); {
							e := it.edge()
							sum += e.To + int64(e.DistM)
						}
					}
					graphStoreBenchmarkSum = sum
				}
			})
			b.Run("route", func(b *testing.B) {
				b.ReportAllocs()
				for range b.N {
					result, err := disk.RouteWithOptions(context.Background(), from, to, RouteOptions{Engine: EngineAStar, Objective: ObjectiveDuration})
					if err != nil {
						b.Fatal(err)
					}
					graphStoreBenchmarkSum = int64(result.Cost)
				}
			})
		})
	}
}

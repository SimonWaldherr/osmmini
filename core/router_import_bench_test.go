package osmmini

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

var benchmarkImportedRouter *Router

// A road grid keeps parser work fixed while exposing adjacency allocations for
// many small node degrees, the common shape of a full-region road graph.
func BenchmarkRoadGraphImport(b *testing.B) {
	const width, height = 200, 200
	var nodes, ways []byte
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			id := int64(y*width + x + 1)
			nodes = append(nodes, testPBFPrimitiveGroup(1, testPBFNode(id, nil, nil, 480000000+int64(y)*10000, 120000000+int64(x)*10000))...)
		}
	}
	for y := 0; y < height; y++ {
		refs := make([]int64, width)
		refs[0] = int64(y*width + 1)
		for x := 1; x < width; x++ {
			refs[x] = 1
		}
		ways = append(ways, testPBFPrimitiveGroup(3, testPBFWay(int64(y+1), []uint64{1}, []uint64{2}, refs))...)
	}
	for x := 0; x < width; x++ {
		refs := make([]int64, height)
		refs[0] = int64(x + 1)
		for y := 1; y < height; y++ {
			refs[y] = width
		}
		ways = append(ways, testPBFPrimitiveGroup(3, testPBFWay(int64(height+x+1), []uint64{1}, []uint64{2}, refs))...)
	}
	data := append(testPBFData([]string{""}, nodes), testPBFData([]string{"", "highway", "residential"}, ways)...)
	path := filepath.Join(b.TempDir(), "grid.osm.pbf")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		router, _, err := BuildRouterWithAddressesOptions(path, BuildOptions{Context: b.Context(), MaxNodes: width * height, SkipAddresses: true})
		if err != nil {
			b.Fatal(err)
		}
		if router.NodeCount() != width*height || router.EdgeCount() != 2*((width-1)*height+(height-1)*width) {
			b.Fatal("incomplete benchmark graph")
		}
		benchmarkImportedRouter = router
	}
}

// Short ways exercise retained per-way references as well as the final graph.
// Heap samples include live graph data and not-yet-collected import garbage.
func BenchmarkRoadGraphImportScale(b *testing.B) {
	for _, width := range []int{100, 400} {
		b.Run(strconv.Itoa(width*width)+"Nodes", func(b *testing.B) {
			var nodes, ways []byte
			for y := range width {
				for x := range width {
					id := int64(y*width + x + 1)
					nodes = append(nodes, testPBFPrimitiveGroup(1, testPBFNode(id, nil, nil, 480000000+int64(y)*10000, 120000000+int64(x)*10000))...)
				}
			}
			var wayID int64
			for direction := range 2 {
				for row := range width {
					for start := 0; start < width-1; start += 8 {
						count := min(9, width-start)
						refs := make([]int64, count)
						refs[0] = int64(row*width + start + 1)
						delta := int64(1)
						if direction == 1 {
							refs[0] = int64(start*width + row + 1)
							delta = int64(width)
						}
						for i := 1; i < count; i++ {
							refs[i] = delta
						}
						wayID++
						ways = append(ways, testPBFPrimitiveGroup(3, testPBFWay(wayID, []uint64{1}, []uint64{2}, refs))...)
					}
				}
			}
			data := append(testPBFData([]string{""}, nodes), testPBFData([]string{"", "highway", "residential"}, ways)...)
			path := filepath.Join(b.TempDir(), "grid.osm.pbf")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			var cpu float64
			var peak uint64
			for range b.N {
				b.StopTimer()
				benchmarkImportedRouter = nil
				runtime.GC()
				ctx := &importHeapContext{Context: b.Context()}
				ctx.sample()
				initial := ctx.peak
				before := benchmarkCPUSeconds()
				b.StartTimer()
				router, _, err := BuildRouterWithAddressesOptions(path, BuildOptions{Context: ctx, MaxNodes: width * width, SkipAddresses: true})
				cpu += benchmarkCPUSeconds() - before
				b.StopTimer()
				ctx.sample()
				peak = max(peak, ctx.peak-initial)
				if err != nil || router.NodeCount() != width*width || router.EdgeCount() != 4*width*(width-1) {
					b.Fatalf("incomplete benchmark graph: %v", err)
				}
				benchmarkImportedRouter = router
				b.StartTimer()
			}
			b.ReportMetric(float64(peak), "peak-heap-B")
			b.ReportMetric(cpu*1e9/float64(b.N), "cpu-ns/op")
		})
	}
}

type importHeapContext struct {
	context.Context
	calls int
	peak  uint64
}

func (c *importHeapContext) Err() error {
	c.calls++
	if c.calls%4096 == 0 {
		c.sample()
	}
	return c.Context.Err()
}

func (c *importHeapContext) sample() {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	c.peak = max(c.peak, stats.HeapAlloc)
}

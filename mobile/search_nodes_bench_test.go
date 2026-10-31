package mobile

import (
	"context"
	"fmt"
	"runtime"
	"slices"
	"testing"

	osm "simonwaldherr.de/go/osmmini/core"
)

func benchmarkSearchWayReferences(count int, emit func([]int64) error) error {
	ways := count / 5
	var nodes [5]int64
	for i := range ways {
		base := (int64(i)*65537%int64(ways))*4 + 100
		nodes = [5]int64{base, base + 1, base + 2, base + 3, base}
		if err := emit(nodes[:]); err != nil {
			return err
		}
	}
	return nil
}

func benchmarkSearchNodeCoordinate(id int64) osm.Coord {
	return osm.Coord{Lat: 48 + float64(id%1000)/1e7, Lon: 12 + float64(id%10000)/1e7}
}

func BenchmarkSearchWayNodeBaseline(b *testing.B) {
	for _, count := range []int{1_000_000, 4_000_000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			b.ReportAllocs()
			var retained uint64
			for range b.N {
				b.StopTimer()
				runtime.GC()
				var before runtime.MemStats
				runtime.ReadMemStats(&before)
				b.StartTimer()
				var refs []int64
				if err := benchmarkSearchWayReferences(count, func(nodes []int64) error {
					refs = append(refs, nodes...)
					return nil
				}); err != nil {
					b.Fatal(err)
				}
				slices.Sort(refs)
				refs = slices.Compact(refs)
				coordinates := make([]osm.Coord, len(refs))
				found := make([]bool, len(refs))
				for i, id := range refs {
					coordinates[i] = benchmarkSearchNodeCoordinate(id)
					found[i] = true
				}
				b.StopTimer()
				runtime.GC()
				var after runtime.MemStats
				runtime.ReadMemStats(&after)
				retained = max(retained, after.HeapAlloc-before.HeapAlloc)
				runtime.KeepAlive(refs)
				runtime.KeepAlive(coordinates)
				runtime.KeepAlive(found)
				b.StartTimer()
				var sum float64
				if err := benchmarkSearchWayReferences(count, func(nodes []int64) error {
					for _, id := range nodes {
						at, ok := slices.BinarySearch(refs, id)
						if !ok || !found[at] {
							b.Fatal("reference missing")
						}
						sum += coordinates[at].Lat
					}
					return nil
				}); err != nil || sum == 0 {
					b.Fatalf("lookup failed: %v", err)
				}
			}
			b.ReportMetric(float64(retained), "retained-heap-B")
		})
	}
}

func BenchmarkSearchWayNodeTable(b *testing.B) {
	for _, count := range []int{1_000_000, 4_000_000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			b.ReportAllocs()
			root := b.TempDir()
			var retained uint64
			for range b.N {
				b.StopTimer()
				runtime.GC()
				var before runtime.MemStats
				runtime.ReadMemStats(&before)
				b.StartTimer()
				builder := searchNodeBuilder{ctx: context.Background(), parent: root}
				if err := benchmarkSearchWayReferences(count, builder.add); err != nil {
					b.Fatal(err)
				}
				table, err := builder.finish()
				if err != nil {
					b.Fatal(err)
				}
				for id := int64(100); id < int64(count/5*4+100); id++ {
					coordinate := benchmarkSearchNodeCoordinate(id)
					if err := table.record(id, coordinate.Lat, coordinate.Lon); err != nil {
						b.Fatal(err)
					}
				}
				if err := table.finishCoordinates(); err != nil {
					b.Fatal(err)
				}
				b.StopTimer()
				runtime.GC()
				var after runtime.MemStats
				runtime.ReadMemStats(&after)
				if after.HeapAlloc > before.HeapAlloc {
					retained = max(retained, after.HeapAlloc-before.HeapAlloc)
				}
				runtime.KeepAlive(builder)
				runtime.KeepAlive(table)
				b.StartTimer()
				var sum float64
				if err := benchmarkSearchWayReferences(count, func(nodes []int64) error {
					for _, id := range nodes {
						coordinate, found, err := table.coordinate(id)
						if err != nil || !found {
							b.Fatalf("reference missing: %v", err)
						}
						sum += coordinate.Lat
					}
					return nil
				}); err != nil || sum == 0 {
					b.Fatalf("lookup failed: %v", err)
				}
				table.close()
				builder.close()
			}
			b.ReportMetric(float64(retained), "retained-heap-B")
			b.ReportMetric(float64(count/5*4*searchNodeRowBytes), "mapped-B")
		})
	}
}

package osmmini

import (
	"os"
	"strconv"
	"testing"
)

// Set OSMMINI_BENCH_PBF to compare full-file decoding on a real extract.
func BenchmarkRealPBFCoordinates(b *testing.B) {
	path := os.Getenv("OSMMINI_BENCH_PBF")
	if path == "" {
		b.Skip("set OSMMINI_BENCH_PBF to an OSM PBF extract")
	}
	info, err := os.Stat(path)
	if err != nil {
		b.Fatal(err)
	}
	var count int
	cb := Callbacks{Node: func(int64, float64, float64) error { count++; return nil }}
	b.SetBytes(info.Size())
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		count = 0
		if err := ExtractFile(path, Options{}, cb); err != nil {
			b.Fatal(err)
		}
		if count == 0 {
			b.Fatal("no nodes decoded")
		}
	}
	b.ReportMetric(float64(count), "nodes/op")
}

func BenchmarkDenseNodes(b *testing.B) {
	const count = 8000
	ids, lats, lons := make([]int64, count), make([]int64, count), make([]int64, count)
	keysVals := make([]uint64, 0, count*3)
	for i := range ids {
		ids[i], lats[i], lons[i] = 1, 1, 1
		keysVals = append(keysVals, 1, 2, 0)
	}
	lats[0], lons[0] = 480000000, 110000000
	raw := testPBFDenseNodes(ids, lats, lons, keysVals)
	ctx := blockCtx{st: []string{"", "amenity", "cafe"}, granularity: 100}
	for _, tagged := range []bool{false, true} {
		name := "Coordinates"
		cb := Callbacks{Node: func(int64, float64, float64) error { return nil }}
		if tagged {
			name = "Tagged"
			cb.TaggedNode = func(Node) error { return nil }
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(raw)))
			for b.Loop() {
				if err := processDenseNodes(raw, ctx, Options{}, cb); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkPBFWay(b *testing.B) {
	ctx := blockCtx{st: []string{"", "highway", "residential", "name", "Teststraße", "surface", "asphalt"}, granularity: 100}
	raw := testPBFWay(1, []uint64{1, 3, 5}, []uint64{2, 4, 6}, []int64{10000000, 1, 1, 1, 1, 1, 1, 1, 1, 1})
	opts := Options{EmitWayNodeIDs: true}
	cb := Callbacks{HighwayWay: func(Way) error { return nil }}
	b.ReportAllocs()
	for b.Loop() {
		if err := processWay(raw, ctx, true, false, opts, cb); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPBFBlob(b *testing.B) {
	for _, count := range []int{8000, 80000} {
		b.Run(strconv.Itoa(count)+"Nodes", func(b *testing.B) {
			ids, lats, lons := make([]int64, count), make([]int64, count), make([]int64, count)
			for i := range ids {
				ids[i] = 1
				lats[i] = int64(i%97) - 48
				lons[i] = int64(i%151) - 75
			}
			raw := testPBFDenseNodes(ids, lats, lons, nil)
			blob := testPBFZlibBlob(b, raw, len(raw))
			b.ReportAllocs()
			b.SetBytes(int64(len(raw)))
			for b.Loop() {
				decoded, err := parseAndDecompressBlob(blob)
				if err != nil || len(decoded) != len(raw) {
					b.Fatalf("decoded %d bytes, want %d: %v", len(decoded), len(raw), err)
				}
			}
		})
	}
}

func BenchmarkCoordinateOnlyBlock(b *testing.B) {
	const count = 8000
	ids, lats, lons := make([]int64, count), make([]int64, count), make([]int64, count)
	for i := range ids {
		ids[i], lats[i], lons[i] = 1, 1, 1
	}
	table := []string{""}
	for i := range count {
		table = append(table, "Unneeded string-table value "+strconv.Itoa(i))
	}
	raw := testPBFBytesField(1, testPBFStringTable(table))
	raw = append(raw, testPBFBytesField(2, testPBFPrimitiveGroup(2, testPBFDenseNodes(ids, lats, lons, nil)))...)
	cb := Callbacks{Node: func(int64, float64, float64) error { return nil }}
	b.ReportAllocs()
	for b.Loop() {
		if err := processOSMData(raw, false, true, false, false, false, false, Options{}, cb); err != nil {
			b.Fatal(err)
		}
	}
}

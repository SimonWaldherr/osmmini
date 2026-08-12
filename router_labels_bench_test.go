package osmmini

import (
	"strconv"
	"testing"
)

func BenchmarkStreetLabels(b *testing.B) {
	const streetCount = 50000
	coords := make(map[int64]Coord, streetCount)
	streets := make(map[string]streetEntry, streetCount)
	for i := 0; i < streetCount; i++ {
		id := int64(i + 1)
		lat := 48.0 + float64(i/250)*0.004
		lon := 11.0 + float64(i%250)*0.004
		coords[id] = Coord{Lat: lat, Lon: lon}
		key := "street-" + strconv.Itoa(i)
		streets[key] = streetEntry{Display: key, NodeIDs: []int64{id}}
	}
	window := CoordWindow{MinLat: 48.20, MaxLat: 48.24, MinLon: 11.20, MaxLon: 11.24}
	b.Run("spatial-index", func(b *testing.B) {
		r := &Router{g: Graph{coords: coords}, streets: streets, streetLabelCells: buildStreetLabelCells(streets, coords)}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = r.StreetLabels(window, 32)
		}
	})
	b.Run("full-scan", func(b *testing.B) {
		r := &Router{g: Graph{coords: coords}, streets: streets}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = r.StreetLabels(window, 32)
		}
	})
}

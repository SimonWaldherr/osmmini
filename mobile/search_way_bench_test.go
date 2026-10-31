package mobile

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	osm "simonwaldherr.de/go/osmmini/core"
)

var benchmarkSearchWayRecord SearchResult

func BenchmarkReadSearchWay(b *testing.B) {
	for _, scenario := range []struct {
		name string
		text int
	}{
		{"small", 0}, {"rich", 2048}, {"larger-than-reader", 8192},
	} {
		b.Run(scenario.name, func(b *testing.B) {
			var spool bytes.Buffer
			writer := bufio.NewWriter(&spool)
			record := SearchResult{ID: 42, Kind: "way", Label: "Campus · München", Category: "school", Text: strings.Repeat("a", scenario.text), Details: &PlaceDetails{Address: "Beispielstraße 7"}}
			if err := writeSearchWay(writer, record, []int64{1, 2, 3, 1}); err != nil {
				b.Fatal(err)
			}
			if err := writer.Flush(); err != nil {
				b.Fatal(err)
			}
			data := spool.Bytes()
			input := bytes.NewReader(data)
			reader := bufio.NewReader(input)
			ctx := b.Context()
			lookup := func(id int64) (osm.Coord, bool, error) {
				return osm.Coord{Lat: 48 + float64(id)/1000, Lon: 12}, true, nil
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			b.ResetTimer()
			for b.Loop() {
				input.Reset(data)
				reader.Reset(input)
				got, err := readSearchWay(ctx, reader, lookup)
				if err != nil || got.Label != record.Label {
					b.Fatalf("read way: %v", err)
				}
				benchmarkSearchWayRecord = got
			}
		})
	}
}

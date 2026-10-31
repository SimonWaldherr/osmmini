package mobile

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	osm "simonwaldherr.de/go/osmmini/core"
)

func TestSearchWayRecordsSurviveReaderBufferReuse(t *testing.T) {
	records := []SearchResult{
		{ID: 1, Kind: "way", Label: "Schule · München", Text: "klein", Details: &PlaceDetails{Address: "Beispielstraße 7"}},
		{ID: 2, Kind: "way", Label: strings.Repeat("Größer", 1000), Text: "groß", Details: &PlaceDetails{Address: "Andere Straße 12"}},
		{ID: 3, Kind: "way", Label: "Café", Text: "wieder klein", Details: &PlaceDetails{Address: "Am Markt 3"}},
	}
	var spool bytes.Buffer
	writer := bufio.NewWriter(&spool)
	for _, record := range records {
		if err := writeSearchWay(writer, record, []int64{1, 3}); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{16, 256, 4096, 16384} {
		reader := bufio.NewReaderSize(bytes.NewReader(spool.Bytes()), size)
		lookup := func(id int64) (osm.Coord, bool, error) {
			return osm.Coord{Lat: float64(id), Lon: -float64(id)}, true, nil
		}
		var got []SearchResult
		for range records {
			record, err := readSearchWay(t.Context(), reader, lookup)
			if err != nil {
				t.Fatalf("buffer %d: %v", size, err)
			}
			got = append(got, record)
		}
		// Check after every buffer refill, not only immediately after decoding.
		for i, want := range records {
			want.Coordinate = osm.Coord{Lat: 2, Lon: -2}
			if !reflect.DeepEqual(got[i], want) {
				t.Fatalf("buffer %d record %d changed after reuse", size, i)
			}
		}
		if _, err := readSearchWay(t.Context(), reader, lookup); err != io.EOF {
			t.Fatalf("buffer %d: stream end: %v", size, err)
		}
	}
}

func TestSearchWayRejectsTruncatedJSONWithAnyReaderSize(t *testing.T) {
	var spool bytes.Buffer
	writer := bufio.NewWriter(&spool)
	if err := writeSearchWay(writer, SearchResult{Kind: "way", Label: strings.Repeat("x", 300)}, []int64{1}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	data := spool.Bytes()
	length := int(binary.LittleEndian.Uint32(data[:4]))
	for _, size := range []int{16, 4096} {
		for _, kept := range []int{0, 1, length - 1} {
			reader := bufio.NewReaderSize(bytes.NewReader(data[:8+kept]), size)
			_, err := readSearchWay(t.Context(), reader, nil)
			expected := io.EOF
			if kept > 0 {
				expected = io.ErrUnexpectedEOF
			}
			if err == io.EOF || !errors.Is(err, expected) {
				t.Fatalf("buffer %d, JSON bytes %d: %v", size, kept, err)
			}
		}
	}
}

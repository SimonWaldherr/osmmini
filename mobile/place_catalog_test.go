package mobile

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	osm "simonwaldherr.de/go/osmmini/core"
)

func TestPlaceCatalogPreservesLegacyCompactAndCompressedSemantics(t *testing.T) {
	records := []SearchResult{
		{ID: 1, Kind: "node", Label: " Legacy town ", Category: "town"},
		{ID: 2, Kind: "node", Label: "München", Category: "restaurant", Place: "city", Text: "unneeded settlement terms"},
		{ID: 3, Kind: "way", Label: "München", Category: "city", Place: "village"},
		{ID: 2, Kind: "node", Label: "München", Place: "village"}, // First duplicate wins exact search; both labels remain.
		{ID: 4, Kind: "node", Label: "Café", Category: "cafe"},
		{ID: 5, Kind: "street", Label: "City street", Place: "city"},
		{ID: 6, Kind: "node", Label: "Not a settlement", Category: "city", Place: "continent"},
		{ID: 7, Kind: "node", Label: "  \t ", Place: "city"},
		{ID: 8, Kind: "node", Label: strings.Repeat("x", 257), Place: "city"},
		{ID: 9, Kind: "node", Label: "Bad latitude", Place: "city", Coordinate: osm.Coord{Lat: 91}},
		{ID: 10, Kind: "node", Label: "Bad longitude", Place: "city", Coordinate: osm.Coord{Lon: -181}},
		{ID: 11, Kind: "node", Label: "Oversized city", Place: "city", Text: strings.Repeat("x", 128<<10)},
		{ID: 12, Kind: "node", Label: "East dateline", Place: "city", Coordinate: osm.Coord{Lat: 90, Lon: 180}},
		{ID: 13, Kind: "node", Label: "Apotheke", Place: "city"}, // Category alias must still use disk search.
	}
	for i := range 110 {
		kind := "isolated_dwelling"
		if i%3 == 0 {
			kind = "town"
		}
		records = append(records, SearchResult{ID: int64(1000 - i), Kind: "node", Label: "Same name", Place: kind})
	}
	legacy := writeSearchCatalog(t, records)
	compact := compactSearchCatalog(t, legacy)
	for name, source := range map[string]string{
		"legacy": legacy, "compact": compact, "compressed": compressedSearchCatalog(t, compact),
	} {
		t.Run(name, func(t *testing.T) {
			search, err := openSearch(source)
			if err != nil {
				t.Fatal(err)
			}
			defer search.close()
			want, err := loadPlaceCatalog(t.Context(), search)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), placeCatalogFilename)
			if err := writePlaceCatalog(t.Context(), path, search); err != nil {
				t.Fatal(err)
			}
			got, err := readPlaceCatalog(t.Context(), path)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("catalog differs: %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(data, []byte(`"text"`)) || bytes.Contains(data, []byte("Oversized city")) || bytes.Contains(data, []byte("Café")) {
				t.Fatalf("irrelevant search data retained in sidecar")
			}
			if len(got.exact.matches("same name")) != maxExactPlacesPerName || got.exact.matches("apotheke") != nil {
				t.Fatal("exact match limits or category aliases changed")
			}
			matches := got.exact.matches("münchen")
			if len(matches) != 2 || matches[0].Place != "city" || matches[0].ID != 2 {
				t.Fatalf("duplicate preference changed: %+v", matches)
			}
		})
	}
}

func placeCatalogBytes(t *testing.T, records ...SearchResult) []byte {
	t.Helper()
	data := make([]byte, placeCatalogHeaderBytes)
	copy(data, placeCatalogMagic)
	binary.LittleEndian.PutUint64(data[8:16], uint64(len(records)))
	for _, record := range records {
		line, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	binary.LittleEndian.PutUint32(data[16:20], crc32.ChecksumIEEE(data[placeCatalogHeaderBytes:]))
	return data
}

func TestPlaceCatalogRejectsCorruptionAndInvalidRecords(t *testing.T) {
	valid := placeCatalogBytes(t, SearchResult{ID: 1, Kind: "node", Label: "City", Place: "city"})
	corrupt := func(edit func([]byte)) []byte {
		data := bytes.Clone(valid)
		edit(data)
		return data
	}
	tests := map[string][]byte{
		"short header":     valid[:placeCatalogHeaderBytes-1],
		"version":          corrupt(func(b []byte) { b[7]++ }),
		"reserved":         corrupt(func(b []byte) { b[23]++ }),
		"count overflow":   corrupt(func(b []byte) { binary.LittleEndian.PutUint64(b[8:16], ^uint64(0)) }),
		"missing record":   corrupt(func(b []byte) { binary.LittleEndian.PutUint64(b[8:16], 2) }),
		"extra record":     corrupt(func(b []byte) { binary.LittleEndian.PutUint64(b[8:16], 0) }),
		"missing newline":  valid[:len(valid)-1],
		"truncated record": valid[:len(valid)-10],
		"checksum":         corrupt(func(b []byte) { b[16]++ }),
		"malformed JSON":   corrupt(func(b []byte) { b[placeCatalogHeaderBytes] = '[' }),
		"poi":              placeCatalogBytes(t, SearchResult{ID: 1, Kind: "node", Label: "Cafe", Category: "cafe"}),
		"street":           placeCatalogBytes(t, SearchResult{ID: 1, Kind: "street", Label: "City", Place: "city"}),
		"invalid place":    placeCatalogBytes(t, SearchResult{ID: 1, Kind: "node", Label: "City", Place: "continent", Category: "city"}),
		"text":             placeCatalogBytes(t, SearchResult{ID: 1, Kind: "node", Label: "City", Place: "city", Text: "search terms"}),
		"invalid coordinate": placeCatalogBytes(t, SearchResult{
			ID: 1, Kind: "node", Label: "City", Place: "city", Coordinate: osm.Coord{Lat: 100},
		}),
		"oversized": placeCatalogBytes(t, SearchResult{ID: 1, Kind: "node", Label: "City", Place: "city", Category: strings.Repeat("x", placeCatalogMaxRecordBytes)}),
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), placeCatalogFilename)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if got, err := readPlaceCatalog(t.Context(), path); err == nil || got != nil {
				t.Fatalf("invalid catalog accepted: %+v, %v", got, err)
			}
		})
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, _, _, valid := settlementRecord(SearchResult{Label: "City", Place: "city", Coordinate: osm.Coord{Lat: value}}); valid {
			t.Fatalf("nonfinite latitude accepted: %v", value)
		}
	}
}

func TestPlaceCatalogEmptyAndEscapedLargeRecords(t *testing.T) {
	for _, source := range []string{"", `{"id":1,"kind":"node","label":"City","place":"city","category":"` + strings.Repeat("<", 40<<10) + `"}`} {
		path := filepath.Join(t.TempDir(), "search.jsonl")
		if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+".idx", nil, 0o600); err != nil {
			t.Fatal(err)
		}
		search, err := openSearch(path)
		if err != nil {
			t.Fatal(err)
		}
		defer search.close()
		want, err := loadPlaceCatalog(t.Context(), search)
		if err != nil {
			t.Fatal(err)
		}
		sidecar := filepath.Join(t.TempDir(), placeCatalogFilename)
		if err := writePlaceCatalog(t.Context(), sidecar, search); err != nil {
			t.Fatal(err)
		}
		got, err := readPlaceCatalog(t.Context(), sidecar)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("empty/expanded record changed: %v", err)
		}
	}
}

func TestPlaceCatalogCancellationAndFailedWritesAreAtomic(t *testing.T) {
	search := openPlaceCatalog(t, []SearchResult{
		{ID: 1, Kind: "node", Label: "Before city", Place: "city"},
		{ID: 2, Kind: "street", Label: "Large street", Text: strings.Repeat("x", 128<<10)},
		{ID: 3, Kind: "node", Label: "After town", Place: "town"},
	})
	path := filepath.Join(t.TempDir(), placeCatalogFilename)
	if err := writePlaceCatalog(t.Context(), path, search); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, preCancelled := range []bool{true, false} {
		ctx, cancel := context.WithCancel(t.Context())
		var scanContext context.Context = ctx
		if preCancelled {
			cancel()
		} else {
			scanContext = &cancelPlaceScanContext{Context: ctx, cancel: cancel}
		}
		if err := writePlaceCatalog(scanContext, path, search); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled write = %v", err)
		}
		cancel()
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("cancelled write changed existing catalog: %v", err)
		}
		ctx, cancel = context.WithCancel(t.Context())
		scanContext = ctx
		if preCancelled {
			cancel()
		} else {
			scanContext = &cancelPlaceScanContext{Context: ctx, cancel: cancel}
		}
		if got, err := readPlaceCatalog(scanContext, path); !errors.Is(err, context.Canceled) || got != nil {
			t.Fatalf("cancelled read = %+v, %v", got, err)
		}
		cancel()
	}
	broken := openPlaceCatalog(t, []SearchResult{{ID: 1, Kind: "node", Label: "City", Place: "city"}})
	broken.data.Close()
	if err := writePlaceCatalog(t.Context(), path, broken); err == nil {
		t.Fatal("failed source read accepted")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("failed write changed existing catalog: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 || entries[0].Name() != placeCatalogFilename {
		t.Fatalf("temporary files remain: %v, %v", entries, err)
	}
}

func TestRealPlaceCatalogEquivalence(t *testing.T) {
	region := os.Getenv("OSMMINI_REAL_REGION")
	if region == "" {
		t.Skip("set OSMMINI_REAL_REGION to a local imported region")
	}
	search, err := openSearch(filepath.Join(region, "search.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer search.close()
	want, err := loadPlaceCatalog(t.Context(), search)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), placeCatalogFilename)
	if err := writePlaceCatalog(t.Context(), path, search); err != nil {
		t.Fatal(err)
	}
	got, err := readPlaceCatalog(t.Context(), path)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("real settlement sidecar differs: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	labels := 0
	for _, cell := range got.labels {
		labels += len(cell)
	}
	t.Logf("%d labels, %d exact-name keys, %d sidecar bytes", labels, len(got.exact), info.Size())
}

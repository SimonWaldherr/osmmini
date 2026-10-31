package mobile

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	osm "simonwaldherr.de/go/osmmini/core"
)

func openPlaceCatalog(t *testing.T, records []SearchResult) *searchReader {
	t.Helper()
	reader, err := openSearch(writeSearchCatalog(t, records))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.close)
	return reader
}

func placeNames(features []VectorFeature) []string {
	names := make([]string, len(features))
	for i, feature := range features {
		names[i] = feature.Name
	}
	slices.Sort(names)
	return names
}

func TestPlaceLabelsSupportExistingCategoriesAndExplicitPlaceTags(t *testing.T) {
	reader := openPlaceCatalog(t, []SearchResult{
		{ID: 1, Kind: "node", Label: " Legacy town ", Category: "town"},
		{ID: 2, Kind: "node", Label: "New city", Category: "restaurant", Place: "city"},
		{ID: 3, Kind: "way", Label: "New village", Category: "town", Place: "village"},
		{ID: 4, Kind: "street", Label: "Town Street", Category: "city", Place: "city"},
		{ID: 5, Kind: "node", Label: "Not a settlement", Category: "city", Place: "continent"},
		{ID: 6, Kind: "node", Label: "Cafe", Category: "cafe"},
		{ID: 7, Kind: "node", Label: "  \t ", Category: "city"},
		{ID: 8, Kind: "node", Label: strings.Repeat("x", 257), Category: "city"},
		{ID: 9, Kind: "node", Label: "Invalid latitude", Category: "city", Coordinate: osm.Coord{Lat: 91}},
		{ID: 10, Kind: "node", Label: "Invalid longitude", Category: "city", Coordinate: osm.Coord{Lon: -181}},
	})
	index, err := loadPlaces(t.Context(), reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		zoom int
		want []string
	}{
		{5, []string{"New city"}},
		{8, []string{"Legacy town", "New city"}},
		{9, []string{"Legacy town", "New city"}},
		{10, []string{"Legacy town", "New city", "New village"}},
	} {
		features := index.features(tc.zoom, 1<<(tc.zoom-1), 1<<(tc.zoom-1))
		if got := placeNames(features); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("zoom %d names = %v, want %v", tc.zoom, got, tc.want)
		}
		for _, feature := range features {
			if feature.Layer != "place" || feature.Kind != 1 || feature.Extent != 4096 ||
				!reflect.DeepEqual(feature.Paths, [][][2]int{{{0, 0}}}) {
				t.Errorf("invalid point label feature: %+v", feature)
			}
			wantClass := map[string]string{"Legacy town": "town", "New city": "city", "New village": "village"}[feature.Name]
			if feature.Class != wantClass {
				t.Errorf("%q class = %q, want %q", feature.Name, feature.Class, wantClass)
			}
		}
	}
}

func TestPlaceLabelsZoomThresholds(t *testing.T) {
	thresholds := map[string]int{
		"city": 5, "town": 8, "village": 10, "hamlet": 12,
		"suburb": 12, "quarter": 13, "neighbourhood": 14, "isolated_dwelling": 14,
	}
	var records []SearchResult
	for kind := range thresholds {
		records = append(records, SearchResult{ID: int64(len(records) + 1), Kind: "node", Label: kind, Place: kind})
	}
	index, err := loadPlaces(t.Context(), openPlaceCatalog(t, records))
	if err != nil {
		t.Fatal(err)
	}
	for zoom := 5; zoom <= 22; zoom++ {
		var want []string
		for kind, minimum := range thresholds {
			if zoom >= minimum {
				want = append(want, kind)
			}
		}
		slices.Sort(want)
		if got := placeNames(index.features(zoom, 1<<(zoom-1), 1<<(zoom-1))); !reflect.DeepEqual(got, want) {
			t.Errorf("zoom %d names = %v, want %v", zoom, got, want)
		}
	}
}

func TestPlaceLabelsProjectToTileCoordinates(t *testing.T) {
	for _, tc := range []struct {
		zoom, x, y, pixelX, pixelY int
	}{
		{5, 16, 10, 1024, 3072},
		{8, 241, 199, 3072, 1024},
		{14, 8230, 2000, 1024, 1024},
	} {
		// Choose known tile-space positions and invert Mercator independently
		// of loadPlaces' forward projection. This catches swapped axes/origins.
		scale := math.Exp2(float64(tc.zoom))
		worldX := (float64(tc.x) + float64(tc.pixelX)/4096) / scale
		worldY := (float64(tc.y) + float64(tc.pixelY)/4096) / scale
		coordinate := osm.Coord{
			Lat: math.Atan(math.Sinh(math.Pi*(1-2*worldY))) * 180 / math.Pi,
			Lon: worldX*360 - 180,
		}
		index, err := loadPlaces(t.Context(), openPlaceCatalog(t, []SearchResult{
			{ID: 1, Kind: "node", Label: "Projected place", Place: "city", Coordinate: coordinate},
		}))
		if err != nil {
			t.Fatal(err)
		}
		features := index.features(tc.zoom, tc.x, tc.y)
		if len(features) != 1 || len(features[0].Paths) != 1 || len(features[0].Paths[0]) != 1 {
			t.Fatalf("zoom %d expected one point, got %+v", tc.zoom, features)
		}
		point := features[0].Paths[0][0]
		if math.Abs(float64(point[0]-tc.pixelX)) > 1 || math.Abs(float64(point[1]-tc.pixelY)) > 1 {
			t.Errorf("zoom %d projected point = %v, want [%d %d] within rounding", tc.zoom, point, tc.pixelX, tc.pixelY)
		}
		if adjacent := index.features(tc.zoom, tc.x+1, tc.y); len(adjacent) != 0 {
			t.Errorf("point leaked into adjacent tile: %+v", adjacent)
		}
	}
}

func TestPlaceLabelsTileEdgesDatelineAndPolarClamping(t *testing.T) {
	index, err := loadPlaces(t.Context(), openPlaceCatalog(t, []SearchResult{
		{ID: 1, Kind: "node", Label: "Origin", Place: "city"},
		{ID: 2, Kind: "node", Label: "West edge", Place: "city", Coordinate: osm.Coord{Lon: -11.25}},
		{ID: 3, Kind: "node", Label: "West dateline", Place: "city", Coordinate: osm.Coord{Lon: -180}},
		{ID: 4, Kind: "node", Label: "East dateline", Place: "city", Coordinate: osm.Coord{Lon: 180}},
		{ID: 5, Kind: "node", Label: "Near east", Place: "city", Coordinate: osm.Coord{Lon: 179.999999}},
		{ID: 6, Kind: "node", Label: "North of equator", Place: "city", Coordinate: osm.Coord{Lat: 0.001}},
		{ID: 7, Kind: "node", Label: "South of equator", Place: "city", Coordinate: osm.Coord{Lat: -0.001}},
		{ID: 8, Kind: "node", Label: "North pole", Place: "city", Coordinate: osm.Coord{Lat: 90}},
		{ID: 9, Kind: "node", Label: "South pole", Place: "city", Coordinate: osm.Coord{Lat: -90}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		x, y int
		want []string
	}{
		{16, 16, []string{"Origin", "South of equator"}},
		{16, 15, []string{"North of equator"}},
		{15, 16, []string{"West edge"}},
		{0, 16, []string{"East dateline", "West dateline"}},
		{31, 16, []string{"Near east"}},
		{16, 0, []string{"North pole"}},
		{16, 31, []string{"South pole"}},
	} {
		features := index.features(5, tc.x, tc.y)
		if got := placeNames(features); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("tile 5/%d/%d names = %v, want %v", tc.x, tc.y, got, tc.want)
		}
		for _, feature := range features {
			point := feature.Paths[0][0]
			if point[0] < 0 || point[0] >= 4096 || point[1] < 0 || point[1] >= 4096 {
				t.Errorf("point outside tile extent: %+v", feature)
			}
		}
	}
	for _, tile := range [][3]int{{4, 0, 0}, {23, 0, 0}, {5, -1, 0}, {5, 32, 0}, {5, 0, -1}, {5, 0, 32}} {
		if got := index.features(tile[0], tile[1], tile[2]); len(got) != 0 {
			t.Errorf("invalid tile %v returned features: %+v", tile, got)
		}
	}
}

func TestPlaceLabelsSkipOversizedRecordsWithoutConsumingSearchReader(t *testing.T) {
	reader := openPlaceCatalog(t, []SearchResult{
		{ID: 1, Kind: "node", Label: "Before city", Category: "city"},
		{ID: 2, Kind: "street", Label: "Oversized street", Category: "town", Text: strings.Repeat("x", 512<<10)},
		{ID: 3, Kind: "node", Label: "After town", Place: "town"},
	})
	if _, err := reader.data.Seek(13, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	index, err := loadPlaces(t.Context(), reader)
	if err != nil {
		t.Fatal(err)
	}
	if got := placeNames(index.features(8, 128, 128)); !reflect.DeepEqual(got, []string{"After town", "Before city"}) {
		t.Fatalf("places around oversized record = %v", got)
	}
	if cursor, err := reader.data.Seek(0, io.SeekCurrent); err != nil || cursor != 13 {
		t.Fatalf("loadPlaces changed search file position: %d, %v", cursor, err)
	}
	for _, query := range []string{"before", "after", "oversized"} {
		got, err := reader.search(t.Context(), query, 10)
		if err != nil || len(got) != 1 {
			t.Errorf("reader unusable for %q after place scan: %+v, %v", query, got, err)
		}
	}
	again, err := loadPlaces(t.Context(), reader)
	if err != nil || !reflect.DeepEqual(again, index) {
		t.Fatalf("repeat scan = %+v, %v; want %+v", again, err, index)
	}
}

type cancelPlaceScanContext struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (ctx *cancelPlaceScanContext) Err() error {
	ctx.checks++
	if ctx.checks == 3 {
		ctx.cancel()
	}
	return ctx.Context.Err()
}

func TestPlaceLabelsCancelBeforeAndDuringOversizedRecordScan(t *testing.T) {
	reader := openPlaceCatalog(t, []SearchResult{
		{ID: 1, Kind: "node", Label: "Before city", Category: "city"},
		{ID: 2, Kind: "street", Label: "Oversized street", Text: strings.Repeat("x", 512<<10)},
		{ID: 3, Kind: "node", Label: "After town", Category: "town"},
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := loadPlaces(ctx, reader); !errors.Is(err, context.Canceled) || got != nil {
		t.Errorf("pre-cancelled scan = %+v, %v", got, err)
	}
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	duringDrain := &cancelPlaceScanContext{Context: ctx, cancel: cancel}
	if got, err := loadPlaces(duringDrain, reader); !errors.Is(err, context.Canceled) || got != nil {
		t.Errorf("scan cancelled while draining = %+v, %v", got, err)
	}
	if duringDrain.checks != 3 {
		t.Errorf("expected cancellation during oversized record drain, got %d checks", duringDrain.checks)
	}
	if got, err := reader.search(t.Context(), "after", 10); err != nil || len(got) != 1 || got[0].ID != 3 {
		t.Errorf("cancelled scan damaged source search reader: %+v, %v", got, err)
	}
}

func TestPlaceLabelsReadFinalRecordWithoutNewlineAndRejectBrokenPlaceJSON(t *testing.T) {
	for _, tc := range []struct {
		name, contents string
		wantError      bool
	}{
		{"final record", `{"id":1,"kind":"node","label":"Last city","category":"city","coordinate":{"lat":0,"lon":0}}`, false},
		{"broken relevant record", `{"category":"city","label":`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "search.jsonl")
			if err := os.WriteFile(path, []byte(tc.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path+".idx", nil, 0o600); err != nil {
				t.Fatal(err)
			}
			reader, err := openSearch(path)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.close()
			index, err := loadPlaces(t.Context(), reader)
			if tc.wantError {
				if err == nil {
					t.Fatal("malformed settlement record accepted")
				}
				return
			}
			if err != nil || !reflect.DeepEqual(placeNames(index.features(5, 16, 16)), []string{"Last city"}) {
				t.Fatalf("final non-newline record = %+v, %v", index, err)
			}
		})
	}
}

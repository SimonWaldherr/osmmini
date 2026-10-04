package main

import (
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	osmmini "simonwaldherr.de/go/osmmini"
)

func TestTinyTilesBuildingHeights(t *testing.T) {
	for _, tc := range []struct {
		name         string
		tags         osmmini.Tags
		height, base float64
		source       string
	}{
		{"rural default", osmmini.Tags{"building": "yes"}, 8, 0, "default"},
		{"garage", osmmini.Tags{"building": "garage"}, 3, 0, "default"},
		{"barn", osmmini.Tags{"building": "barn"}, 6, 0, "default"},
		{"explicit metres win", osmmini.Tags{"height": "12,5 m", "building:levels": "8", "roof:height": "3"}, 12.5, 0, "height"},
		{"feet", osmmini.Tags{"height": "30 ft"}, 9.144, 0, "height"},
		{"levels and roof", osmmini.Tags{"building:levels": "2", "roof:height": "2.5"}, 8.5, 0, "levels"},
		{"roof levels", osmmini.Tags{"building:levels": "2", "roof:levels": "1"}, 9, 0, "levels"},
		{"bad roof", osmmini.Tags{"building:levels": "2", "roof:height": "NaN"}, 6, 0, "levels"},
		{"bad height falls through", osmmini.Tags{"height": "-4", "building:levels": "3"}, 9, 0, "levels"},
		{"zero height falls through", osmmini.Tags{"height": "0", "building:height": "7"}, 7, 0, "height"},
		{"huge height", osmmini.Tags{"height": "99999"}, 8, 0, "default"},
		{"elevated part", osmmini.Tags{"building:part": "yes", "height": "15", "min_height": "6"}, 15, 6, "height"},
		{"minimum level", osmmini.Tags{"building:levels": "5", "building:min_level": "2"}, 15, 6, "levels"},
		{"invalid base", osmmini.Tags{"height": "6", "min_height": "9"}, 6, 0, "height"},
		{"estimated elevated height", osmmini.Tags{"min_height": "12"}, 20, 12, "default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := tinyTilesBuildingProperties(tc.tags)
			if p.Height != tc.height || p.Base != tc.base || p.HeightSource != tc.source {
				t.Fatalf("properties = %+v", p)
			}
		})
	}
}

func tinyTilesBuildingsPBF() []byte {
	strings := []string{"", "building", "yes", "height", "12 m", "building:levels", "2", "garage", "waterway", "stream", "building:part", "min_height", "6"}
	var group []byte
	for i, p := range [][2]int64{{480000000, 110000000}, {480000000, 110010000}, {480010000, 110010000}, {480010000, 110000000}} {
		group = append(group, tinyTilesWaterwayPrimitiveGroup(1, tinyTilesWaterwayNode(int64(i+1), p[0], p[1]))...)
	}
	for _, w := range []struct {
		id           int64
		keys, values []uint64
		refs         []int64
	}{
		{10, []uint64{1}, []uint64{2}, []int64{1, 1, 1, 1, -3}},
		{11, []uint64{1, 3}, []uint64{2, 4}, []int64{1, 1, 1, 1, -3}},
		{12, []uint64{1, 5}, []uint64{7, 6}, []int64{1, 1, 1, 1, -3}},
		{13, []uint64{1}, []uint64{2}, []int64{1, 1, 996, -997}}, // missing node; never interpolate
		{14, []uint64{1}, []uint64{2}, []int64{1, 1, 1}},         // open way
		{15, []uint64{8}, []uint64{9}, []int64{1, 1}},
		{16, []uint64{10, 3, 11}, []uint64{2, 4, 12}, []int64{1, 1, 1, 1, -3}},
	} {
		group = append(group, tinyTilesWaterwayPrimitiveGroup(3, tinyTilesWaterwayWay(w.id, w.keys, w.values, w.refs))...)
	}
	block := tinyTilesWaterwayBytesField(1, tinyTilesWaterwayStringTable(strings))
	block = append(block, tinyTilesWaterwayBytesField(2, group)...)
	blob := tinyTilesWaterwayBytesField(1, block)
	header := tinyTilesWaterwayBytesField(1, []byte("OSMData"))
	header = append(header, tinyTilesWaterwayVarintField(3, uint64(len(blob)))...)
	out := make([]byte, 4)
	binary.BigEndian.PutUint32(out, uint32(len(header)))
	out = append(out, header...)
	return append(out, blob...)
}

func TestTinyTilesCompanionsBuildAndReuseSource(t *testing.T) {
	dir := t.TempDir()
	pbf := filepath.Join(dir, "source.osm.pbf")
	path := tinyTilesWaterwaySidecarPath(dir)
	if err := os.WriteFile(pbf, tinyTilesBuildingsPBF(), 0600); err != nil {
		t.Fatal(err)
	}
	count, err := buildTinyTilesWaterwaySidecar(pbf, path)
	if err != nil || count != 1 {
		t.Fatalf("build: %d, %v", count, err)
	}
	index, err := loadTinyTilesWaterwaySidecar(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(index.buildings.features) != 4 {
		t.Fatalf("buildings = %d", len(index.buildings.features))
	}
	for i, height := range []float64{8, 12, 6, 12} {
		if index.buildings.features[i].Properties.Height != height {
			t.Fatalf("building %d: %+v", i, index.buildings.features[i])
		}
	}
	// Distinguish reuse from a fresh parse: a valid source fingerprint and a
	// validated cached height survive a rebuild, until the PBF mtime changes.
	cached, _ := readTinyTilesWaterwaySidecar(path)
	cached.Buildings[0].Properties.Height = 11
	if err := writeTinyTilesWaterwaySidecar(path, cached); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(dir, "staged.json")
	if _, err := buildTinyTilesWaterwaySidecar(pbf, staged); err != nil {
		t.Fatal(err)
	}
	result, _ := readTinyTilesWaterwaySidecar(staged)
	if result.Buildings[0].Properties.Height != 11 {
		t.Fatal("unchanged source was scanned again")
	}
	info, _ := os.Stat(pbf)
	later := info.ModTime().Add(time.Second)
	if err := os.Chtimes(pbf, later, later); err != nil {
		t.Fatal(err)
	}
	if _, err := buildTinyTilesWaterwaySidecar(pbf, staged); err != nil {
		t.Fatal(err)
	}
	result, _ = readTinyTilesWaterwaySidecar(staged)
	if result.Buildings[0].Properties.Height != 8 {
		t.Fatal("changed source reused stale geometry")
	}
}

func TestTinyTilesBuildingsViewportAndLimits(t *testing.T) {
	ring := [][2]float64{{11, 48}, {11.001, 48}, {11.001, 48.001}, {11, 48}}
	features := make([]tinyTilesBuilding, 3001)
	for i := range features {
		features[i] = tinyTilesBuilding{ID: int64(i), Properties: tinyTilesBuildingProperties(osmmini.Tags{"building": "yes"}), Coordinates: [][][2]float64{ring}}
	}
	index, err := newTinyTilesBuildingIndex(features)
	if err != nil {
		t.Fatal(err)
	}
	s := &server{tinyTilesWaterways: &tinyTilesWaterwayIndex{buildings: index}}
	for _, tc := range []struct {
		query         string
		status, count int
		truncated     bool
	}{
		{"bbox=10.99,47.99,11.01,48.01&zoom=14", 200, 3000, true},
		{"bbox=11.02,48.02,11.04,48.04&zoom=14", 200, 0, false},
		{"bbox=10.99,47.99,11.01,48.01&zoom=13", 200, 0, false},
		{"bbox=invalid&zoom=14", 400, 0, false},
		{"bbox=-180,-90,180,90&zoom=14", 400, 0, false},
	} {
		r := httptest.NewRecorder()
		s.handleTinyTilesBuildings(r, httptest.NewRequest(http.MethodGet, "/api/v1/tinytiles/buildings?"+tc.query, nil))
		if r.Code != tc.status {
			t.Fatalf("%s: status %d", tc.query, r.Code)
		}
		if tc.status != 200 {
			continue
		}
		var data struct {
			Features  []json.RawMessage `json:"features"`
			Truncated bool              `json:"truncated"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		if len(data.Features) != tc.count || data.Truncated != tc.truncated {
			t.Fatalf("%s: %d features, truncated %v", tc.query, len(data.Features), data.Truncated)
		}
	}
}

func TestTinyTilesBuildActivatesBuildingsAfterRestart(t *testing.T) {
	dir := t.TempDir()
	pbf := filepath.Join(dir, "source.osm.pbf")
	if err := os.WriteFile(pbf, tinyTilesBuildingsPBF(), 0600); err != nil {
		t.Fatal(err)
	}
	status := tinyTilesBuildStatus{State: "building", MinZoom: 14, MaxZoom: 14}
	s := &server{pbfPath: pbf, tinyTilesDir: dir, tinyTilesMaxMemory: 8 << 20, tinyTilesBuild: status}
	s.buildTinyTiles(status)
	if s.tinyTilesBuild.State != "ready" || s.tinyTilesBuild.BuildingFeatures != 4 {
		t.Fatalf("build = %+v", s.tinyTilesBuild)
	}
	s.closeTinyTiles()
	s.loadTinyTilesIfPresent()
	defer s.closeTinyTiles()
	if !s.tinyTilesHasBuildingSidecar() || s.tinyTilesBuildingCount() != 4 {
		t.Fatal("building layer not restored on restart")
	}
	// A mismatched artifact must never activate stale building geometry.
	path := tinyTilesWaterwaySidecarPath(dir)
	sidecar, _ := readTinyTilesWaterwaySidecar(path)
	sidecar.ArtifactManifestSHA256 = "stale"
	if err := writeTinyTilesWaterwaySidecar(path, sidecar); err != nil {
		t.Fatal(err)
	}
	if err := s.installTinyTiles(filepath.Join(dir, "basemap.ttiles")); err != nil {
		t.Fatal(err)
	}
	if s.tinyTilesHasBuildingSidecar() {
		t.Fatal("stale geometry activated")
	}
}

func TestTinyTilesBuildingsBackfillLegacyWaterwaySidecar(t *testing.T) {
	dir := t.TempDir()
	pbf := filepath.Join(dir, "source.osm.pbf")
	if err := os.WriteFile(pbf, tinyTilesBuildingsPBF(), 0600); err != nil {
		t.Fatal(err)
	}
	status := tinyTilesBuildStatus{State: "building", MinZoom: 14, MaxZoom: 14}
	s := &server{pbfPath: pbf, tinyTilesDir: dir, tinyTilesMaxMemory: 8 << 20, tinyTilesBuild: status}
	s.buildTinyTiles(status)
	defer s.closeTinyTiles()
	if s.tinyTilesBuild.State != "ready" {
		t.Fatalf("build = %+v", s.tinyTilesBuild)
	}
	path := tinyTilesWaterwaySidecarPath(dir)
	sidecar, err := readTinyTilesWaterwaySidecar(path)
	if err != nil {
		t.Fatal(err)
	}
	sidecar.Version, sidecar.Buildings, sidecar.Source = 2, nil, ""
	if err := writeTinyTilesWaterwaySidecar(path, sidecar); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(dir, "basemap.ttiles")
	if err := s.installTinyTiles(artifact); err != nil {
		t.Fatal(err)
	}
	if !s.tinyTilesHasWaterwaySidecar() || s.tinyTilesHasBuildingSidecar() {
		t.Fatal("legacy sidecar not recognized")
	}
	s.backfillTinyTilesWaterways(artifact)
	if s.tinyTilesBuildingCount() != 4 || s.tinyTilesWaterwayCount() != 1 {
		t.Fatal("migration did not preserve both companion layers")
	}
	if s.tinyTilesBuild.BuildingFeatures != 4 {
		t.Fatal("migration did not update public building count")
	}
}

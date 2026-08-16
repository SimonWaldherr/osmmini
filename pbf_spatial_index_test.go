package osmmini

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPBFSpatialIndexStreamsBlocksAndQueriesWindow(t *testing.T) {
	munich := testPBFData([]string{""}, testPBFPrimitiveGroup(1, testPBFNode(1, nil, nil, 481370000, 115750000)))
	berlin := testPBFData([]string{""}, testPBFPrimitiveGroup(1, testPBFNode(2, nil, nil, 525200000, 134050000)))
	pbfPath := filepath.Join(t.TempDir(), "region.osm.pbf")
	if err := os.WriteFile(pbfPath, append(munich, berlin...), 0o600); err != nil {
		t.Fatal(err)
	}

	indexPath := PBFSpatialIndexPath(pbfPath)
	var last PBFSpatialIndexProgress
	index, err := BuildPBFSpatialIndex(context.Background(), pbfPath, indexPath, func(progress PBFSpatialIndexProgress) {
		last = progress
	})
	if err != nil {
		t.Fatalf("build index: %v", err)
	}
	if len(index.Blocks) != 2 {
		t.Fatalf("indexed blocks = %d, want 2", len(index.Blocks))
	}
	if len(index.Source.SHA256) != 64 {
		t.Fatalf("source hash = %q, want SHA-256", index.Source.SHA256)
	}
	if last.BlocksRead != 2 || last.IndexedBlocks != 2 || last.BytesRead != int64(len(munich)+len(berlin)) {
		t.Fatalf("progress = %#v", last)
	}

	window := CoordWindow{MinLat: 48.0, MaxLat: 48.3, MinLon: 11.4, MaxLon: 11.8}
	blocks := index.BlocksForWindow(window)
	if len(blocks) != 1 || blocks[0].Offset != 0 || blocks[0].NodeCount != 1 {
		t.Fatalf("Munich block query = %#v", blocks)
	}

	loaded, err := LoadPBFSpatialIndex(indexPath)
	if err != nil {
		t.Fatalf("load index: %v", err)
	}
	if !loaded.FreshFor(pbfPath) {
		t.Fatal("loaded index is not fresh for its source PBF")
	}
	if err := os.WriteFile(pbfPath, append(append(munich, berlin...), 0), 0o600); err != nil {
		t.Fatal(err)
	}
	if loaded.FreshFor(pbfPath) {
		t.Fatal("index stayed fresh after source PBF changed")
	}
}

func TestPBFSpatialIndexPathIsShortAndPredictable(t *testing.T) {
	if got, want := PBFSpatialIndexPath("/maps/europe.osm.pbf"), "/maps/europe.idx"; got != want {
		t.Fatalf("PBFSpatialIndexPath = %q, want %q", got, want)
	}
	if got, want := PBFSpatialIndexPath("/maps/region.pbf"), "/maps/region.idx"; got != want {
		t.Fatalf("PBFSpatialIndexPath = %q, want %q", got, want)
	}
}

func TestPBFSpatialIndexRejectsInvalidWindow(t *testing.T) {
	index := &PBFSpatialIndex{Blocks: []PBFSpatialBlock{{Length: 1, NodeCount: 1}}}
	if got := index.BlocksForWindow(CoordWindow{}); got != nil {
		t.Fatalf("invalid window blocks = %#v, want nil", got)
	}
}

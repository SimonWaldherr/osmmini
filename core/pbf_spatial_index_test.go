package osmmini

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
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
	if index.Source.SHA256 != fmt.Sprintf("%x", sha256.Sum256(append(append([]byte(nil), munich...), berlin...))) {
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

func TestPBFSpatialIndexDoesNotReplacePreviousIndexOnSourceChanges(t *testing.T) {
	for _, mode := range []string{"timestamp", "replacement", "truncated blob"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "region.osm.pbf")
			indexPath := filepath.Join(root, "region.idx")
			data := testPBFData([]string{""}, testPBFPrimitiveGroup(1, testPBFNode(1, nil, nil, 1, 1)))
			if mode == "truncated blob" {
				data = data[:len(data)-1]
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			previous := []byte("previous index")
			if err := os.WriteFile(indexPath, previous, 0600); err != nil {
				t.Fatal(err)
			}
			changed := false
			_, err = BuildPBFSpatialIndex(t.Context(), path, indexPath, func(_ PBFSpatialIndexProgress) {
				if changed {
					return
				}
				changed = true
				if mode == "timestamp" {
					changedTime := info.ModTime().Add(time.Second)
					if err := os.Chtimes(path, changedTime, changedTime); err != nil {
						t.Fatal(err)
					}
				} else if mode == "replacement" {
					other := filepath.Join(root, "new.osm.pbf")
					if err := os.WriteFile(other, data, 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Chtimes(other, info.ModTime(), info.ModTime()); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(other, path); err != nil {
						t.Fatal(err)
					}
				}
			})
			if err == nil {
				t.Fatal("changed/truncated input accepted")
			}
			got, readErr := os.ReadFile(indexPath)
			if readErr != nil || !bytes.Equal(got, previous) {
				t.Fatalf("previous index changed: %v", readErr)
			}
		})
	}
}

func TestPBFSpatialIndexNeverOverwritesSource(t *testing.T) {
	for _, mode := range []string{"same path", "hard link", "symbolic link", "default idx suffix"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source.osm.pbf")
			output := filepath.Join(root, "result.idx")
			if mode == "default idx suffix" {
				source = filepath.Join(root, "source.idx")
				output = ""
			}
			data := testPBFData([]string{""}, testPBFPrimitiveGroup(1, testPBFNode(1, nil, nil, 1, 1)))
			if err := os.WriteFile(source, data, 0600); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "same path":
				output = source
			case "hard link":
				if err := os.Link(source, output); err != nil {
					t.Fatal(err)
				}
			case "symbolic link":
				if err := os.Symlink(source, output); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}
			if _, err := BuildPBFSpatialIndex(t.Context(), source, output, nil); err == nil {
				t.Fatal("source alias accepted as output")
			}
			got, err := os.ReadFile(source)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("source was overwritten: %v", err)
			}
		})
	}
}

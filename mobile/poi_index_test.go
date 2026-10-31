package mobile

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	osm "simonwaldherr.de/go/osmmini/core"
)

func poiTestSource(t *testing.T, records []SearchResult, compressed bool) (*searchReader, string) {
	t.Helper()
	path := writeSearchCatalog(t, records)
	if compressed {
		path = compressedSearchCatalog(t, path)
	}
	source, err := openSearch(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(source.close)
	digest, err := fileHash(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	return source, digest
}

func testPOI() SearchResult {
	return SearchResult{ID: 2, Kind: "node", Label: "Café Test", Category: "cafe", Coordinate: osm.Coord{Lat: 48.002, Lon: 12.002}}
}

func poiTile(record SearchResult, z int) (int, int) {
	x, y := poiWorld(record)
	scale := math.Exp2(float64(z))
	return int(x * scale), int(y * scale)
}

func requirePOICacheLock(t *testing.T, directory string) {
	t.Helper()
	release, err := acquirePOILock(t.Context(), directory)
	if errors.Is(err, errPOILockUnavailable) {
		t.Skip("platform uses private temporary indexes")
	}
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestPOISpatialIndexUsesOriginalRecordsAcrossStorageAndZooms(t *testing.T) {
	first := testPOI()
	first.ID = 1 << 62
	first.Details = &PlaceDetails{Address: "Hauptstraße 7", Website: "https://example.org"}
	other := first
	other.ID, other.Kind, other.Label = 3, "way", "Museum"
	other.Coordinate.Lon += 60
	street, address, town := first, first, first
	street.Kind = "street"
	address.Category = ""
	town.Place = "town"
	for _, compressed := range []bool{false, true} {
		source, digest := poiTestSource(t, []SearchResult{street, first, address, town, other}, compressed)
		index, err := openPOIIndex(t.Context(), t.TempDir(), source, digest)
		if err != nil {
			t.Fatal(err)
		}
		defer index.close()
		for _, zoom := range []int{5, 12, 14, 18, 22} {
			x, y := poiTile(first, zoom)
			features, err := index.features(t.Context(), zoom, x, y)
			if err != nil || len(features) != 1 {
				t.Fatalf("compressed=%t z%d: %v, %v", compressed, zoom, features, err)
			}
			feature := features[0]
			if feature.Layer != "poi" || feature.Kind != 1 || feature.Class != "cafe" || feature.Name != first.Label || feature.SourceIdentity != "node/4611686018427387904" || !reflect.DeepEqual(feature.Details, first.Details) {
				t.Fatalf("metadata changed: %+v", feature)
			}
			if len(feature.Paths) != 1 || len(feature.Paths[0]) != 1 || feature.Extent != 4096 {
				t.Fatalf("point geometry: %+v", feature)
			}
			for _, coordinate := range feature.Paths[0][0] {
				if coordinate < 0 || coordinate >= feature.Extent {
					t.Fatal("point outside tile")
				}
			}
		}
		x, y := poiTile(other, 14)
		features, err := index.features(t.Context(), 14, x, y)
		if err != nil || len(features) != 1 || features[0].SourceIdentity != "way/3" {
			t.Fatalf("other tile: %+v, %v", features, err)
		}
		if features, err := index.features(t.Context(), 14, 0, 0); err != nil || len(features) != 0 {
			t.Fatalf("empty tile: %+v, %v", features, err)
		}
		if features, err := index.features(t.Context(), 63, 0, 0); err != nil || len(features) != 0 {
			t.Fatalf("invalid tile: %+v, %v", features, err)
		}
	}
}

func TestPOICacheValidatesSourceAndContentAndRebuildsInterruptedWrites(t *testing.T) {
	source, digest := poiTestSource(t, []SearchResult{testPOI()}, true)
	directory := t.TempDir()
	requirePOICacheLock(t, directory)
	index, err := openPOIIndex(t.Context(), directory, source, digest)
	if err != nil {
		t.Fatal(err)
	}
	index.close()
	path := filepath.Join(directory, poiIndexFilename)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := bytes.Clone(original)
	corrupt[searchCompactHeaderBytes] ^= 1
	for _, data := range [][]byte{original[:10], original[:len(original)-1], corrupt, append(bytes.Clone(original), 0)} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if opened, err := readPOIIndex(t.Context(), path, source, digest); err == nil {
			opened.close()
			t.Fatal("invalid cache accepted")
		}
		index, err = openPOIIndex(t.Context(), directory, source, digest)
		if err != nil {
			t.Fatal(err)
		}
		index.close()
		rebuilt, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(rebuilt, original) {
			t.Fatalf("cache was not rebuilt: %v", err)
		}
	}
	changed := testPOI()
	changed.ID = 99
	nextSource, nextDigest := poiTestSource(t, []SearchResult{changed}, true)
	if opened, err := readPOIIndex(t.Context(), path, nextSource, nextDigest); err == nil {
		opened.close()
		t.Fatal("cache used for different source")
	}
	next, err := openPOIIndex(t.Context(), directory, nextSource, nextDigest)
	if err != nil {
		t.Fatal(err)
	}
	defer next.close()
	x, y := poiTile(changed, 14)
	features, err := next.features(t.Context(), 14, x, y)
	if err != nil || len(features) != 1 || features[0].SourceIdentity != "node/99" {
		t.Fatalf("wrong source result: %+v, %v", features, err)
	}
}

func TestPOICacheReopeningDoesNotScanTheSearchFile(t *testing.T) {
	source, digest := poiTestSource(t, []SearchResult{testPOI()}, false)
	directory := t.TempDir()
	requirePOICacheLock(t, directory)
	index, err := openPOIIndex(t.Context(), directory, source, digest)
	if err != nil {
		t.Fatal(err)
	}
	index.close()
	// Opening a validated cache uses only its index and the already verified
	// source checksum. An attempted scan would fail on this closed descriptor.
	if err := source.data.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openPOIIndex(t.Context(), directory, source, digest)
	if err != nil {
		t.Fatal(err)
	}
	reopened.close()
}

func TestPOIReadOnlyRegionUsesTemporaryIndexAndReleasesIt(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write through directory permissions")
	}
	source, digest := poiTestSource(t, []SearchResult{testPOI()}, true)
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(directory, 0o700)
	probe := filepath.Join(directory, "permission-probe")
	if file, err := os.Create(probe); err == nil {
		file.Close()
		os.Remove(probe)
		t.Skip("platform does not enforce read-only directory modes")
	}
	index, err := openPOIIndex(t.Context(), directory, source, digest)
	if err != nil {
		t.Fatal(err)
	}
	name := index.search.index.Name()
	x, y := poiTile(testPOI(), 14)
	features, err := index.features(t.Context(), 14, x, y)
	if err != nil || len(features) != 1 {
		index.close()
		t.Fatalf("read-only lookup: %+v, %v", features, err)
	}
	index.close()
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Fatalf("temporary index remains: %v", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("read-only region changed: %v, %v", entries, err)
	}
}

func TestPOICancelledBuildPreservesPublishedCacheAndRemovesScratch(t *testing.T) {
	var records []SearchResult
	for i := 1; i <= 100; i++ {
		record := testPOI()
		record.ID = int64(i)
		records = append(records, record)
	}
	source, digest := poiTestSource(t, records, true)
	directory := t.TempDir()
	path := filepath.Join(directory, poiIndexFilename)
	previous := []byte("interrupted earlier write")
	if err := os.WriteFile(path, previous, 0o600); err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := &cancelSearchBuildContext{Context: parent, cancel: cancel, after: 30}
	if index, err := openPOIIndex(ctx, directory, source, digest); !errors.Is(err, context.Canceled) {
		if index != nil {
			index.close()
		}
		t.Fatalf("build cancellation: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, previous) {
		t.Fatalf("published cache modified: %v", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("scratch remains: %v, %v", entries, err)
	}
	for _, entry := range entries {
		if entry.Name() != poiIndexFilename && entry.Name() != poiLockFilename {
			t.Fatalf("scratch remains: %s", entry.Name())
		}
	}
	index, err := openPOIIndex(t.Context(), directory, source, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer index.close()
	x, y := poiTile(testPOI(), 14)
	if features, err := index.features(ctx, 14, x, y); !errors.Is(err, context.Canceled) || features != nil {
		t.Fatalf("tile cancellation: %v", err)
	}
}

func TestPOITileLimitDeduplicationAndParallelReads(t *testing.T) {
	var records []SearchResult
	for i := 1; i <= maxTilePOIs+5; i++ {
		record := testPOI()
		record.ID = int64(i)
		records = append(records, record, record)
	}
	source, digest := poiTestSource(t, records, true)
	index, err := openPOIIndex(t.Context(), t.TempDir(), source, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer index.close()
	x, y := poiTile(testPOI(), 14)
	var group sync.WaitGroup
	for range 6 {
		group.Go(func() {
			features, err := index.features(t.Context(), 14, x, y)
			if err != nil || len(features) != maxTilePOIs {
				t.Errorf("bounded parallel lookup: %d, %v", len(features), err)
				return
			}
			if features[0].SourceIdentity != "node/1" || features[len(features)-1].SourceIdentity != "node/512" {
				t.Error("unstable or duplicate results")
			}
		})
	}
	group.Wait()
}

func TestImportedPOIsCarryDetailsWithoutChangingTheRegionManifest(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "region")
	_, err := Import(t.Context(), ImportOptions{PBF: fixture(t), Destination: directory, MinZoom: 13, MaxZoom: 13}, nil)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := Open(t.Context(), directory)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	results, err := engine.Search(t.Context(), "cafe", 10)
	if err != nil || len(results) != 1 || results[0].Details == nil || results[0].Details.Address != "Teststraße 7" {
		t.Fatalf("imported search details: %+v, %v", results, err)
	}
	x, y := poiTile(testPOI(), 13)
	tile, err := engine.Tile(t.Context(), 13, x, y)
	if err != nil {
		t.Fatal(err)
	}
	var points []VectorFeature
	for _, feature := range tile.Features {
		if feature.Layer == "poi" {
			points = append(points, feature)
		}
	}
	if len(points) != 1 || points[0].SourceIdentity != "node/2" || points[0].Details == nil || points[0].Details.Address != "Teststraße 7" {
		t.Fatalf("imported map details: %+v", points)
	}
	after, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil || !bytes.Equal(after, manifest) {
		t.Fatalf("manifest changed: %v", err)
	}
	if !strings.HasPrefix(points[0].Name, "Café") {
		t.Fatal("POI name lost")
	}
}

func TestPOICrashHelper(t *testing.T) {
	directory := os.Getenv("OSMMINI_POI_CRASH_DIR")
	if directory == "" {
		t.Skip("subprocess helper")
	}
	if _, err := acquirePOILock(t.Context(), directory); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(directory, poiWorkDirectory)
	run := filepath.Join(workspace, ".search-sort-interrupted")
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run, "run-0"), make([]byte, 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "index"), []byte("partial index"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Exit without running deferred cleanup, as when iOS terminates the app.
	os.Exit(23)
}

func TestPOICrashRecoveryReleasesLockAndCleansOnlyItsWorkspace(t *testing.T) {
	source, digest := poiTestSource(t, []SearchResult{testPOI()}, true)
	directory := t.TempDir()
	release, err := acquirePOILock(t.Context(), directory)
	if errors.Is(err, errPOILockUnavailable) {
		t.Skip("platform uses private temporary indexes")
	}
	if err != nil {
		t.Fatal(err)
	}
	release()
	index, err := openPOIIndex(t.Context(), directory, source, digest)
	if err != nil {
		t.Fatal(err)
	}
	index.close()
	original, err := os.ReadFile(filepath.Join(directory, poiIndexFilename))
	if err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(directory, ".search-sort-owned-by-import")
	if err := os.Mkdir(unrelated, 0o700); err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestPOICrashHelper$")
	child.Env = append(os.Environ(), "OSMMINI_POI_CRASH_DIR="+directory)
	output, err := child.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Fatalf("crash helper: %s, %v", output, err)
	}
	if _, err := os.Stat(filepath.Join(directory, poiWorkDirectory, ".search-sort-interrupted", "run-0")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	reopened, err := openPOIIndex(ctx, directory, source, digest)
	if err != nil {
		t.Fatal(err)
	}
	reopened.close()
	if _, err := os.Stat(filepath.Join(directory, poiWorkDirectory)); !os.IsNotExist(err) {
		t.Fatalf("crashed workspace remains: %v", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated workspace removed: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(directory, poiIndexFilename))
	if err != nil || !bytes.Equal(after, original) {
		t.Fatalf("valid published cache changed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, poiLockFilename)); err != nil {
		t.Fatalf("stable lock inode removed: %v", err)
	}
}

func TestPOIParallelOpenWaitsForOwnerAndDoesNotCleanActiveWorkspace(t *testing.T) {
	source, digest := poiTestSource(t, []SearchResult{testPOI()}, true)
	directory := t.TempDir()
	release, err := acquirePOILock(t.Context(), directory)
	if errors.Is(err, errPOILockUnavailable) {
		t.Skip("platform uses private temporary indexes")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	workspace := filepath.Join(directory, poiWorkDirectory)
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(workspace, "active-run")
	if err := os.WriteFile(active, []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 75*time.Millisecond)
	defer cancel()
	if index, err := openPOIIndex(ctx, directory, source, digest); !errors.Is(err, context.DeadlineExceeded) {
		if index != nil {
			index.close()
		}
		t.Fatalf("parallel owner ignored: %v", err)
	}
	if data, err := os.ReadFile(active); err != nil || string(data) != "owned" {
		t.Fatalf("active work removed: %v", err)
	}
	release()
	release = nil
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			index, err := openPOIIndex(t.Context(), directory, source, digest)
			if err != nil {
				t.Error(err)
				return
			}
			defer index.close()
			x, y := poiTile(testPOI(), 14)
			if rows, err := index.features(t.Context(), 14, x, y); err != nil || len(rows) != 1 {
				t.Errorf("parallel open: %v, %v", rows, err)
			}
		})
	}
	group.Wait()
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("work remains: %v", err)
	}
}

func TestPOIWorkspaceCleanupNeverFollowsSymlinks(t *testing.T) {
	source, digest := poiTestSource(t, []SearchResult{testPOI()}, false)
	directory, other := t.TempDir(), t.TempDir()
	marker := filepath.Join(other, "keep")
	if err := os.WriteFile(marker, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, poiWorkDirectory)
	if err := os.Symlink(other, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	index, err := openPOIIndex(t.Context(), directory, source, digest)
	if err != nil {
		t.Fatal(err)
	}
	index.close()
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("unowned symlink changed: %v", err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "untouched" {
		t.Fatalf("symlink target changed: %v", err)
	}
}

func TestEngineDetailTilesDoNotDependOnLowZoomBasemap(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "region")
	if _, err := Import(t.Context(), ImportOptions{PBF: fixtureWithRoadClass(t, "Teststraße", "motorway"), Destination: directory, MinZoom: 5, MaxZoom: 5}, nil); err != nil {
		t.Fatal(err)
	}
	engine, err := Open(t.Context(), directory)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	x, y := poiTile(testPOI(), 18)
	tile, err := engine.Tile(t.Context(), 18, x, y)
	if err != nil {
		t.Fatal(err)
	}
	if len(tile.Features) != 1 || tile.Features[0].Layer != "poi" || tile.Features[0].SourceIdentity != "node/2" {
		t.Fatalf("detail tile lacks POI or repeats basemap: %+v", tile)
	}
	empty, err := engine.Tile(t.Context(), 18, x+1, y)
	if err != nil || len(empty.Features) != 0 {
		t.Fatalf("neighbor detail tile: %+v, %v", empty, err)
	}
	if _, err := engine.Tile(t.Context(), 23, x, y); err == nil {
		t.Fatal("invalid detail zoom accepted")
	}
}

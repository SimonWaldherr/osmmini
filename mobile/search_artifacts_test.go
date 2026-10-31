package mobile

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	osm "simonwaldherr.de/go/osmmini/core"
)

func TestStreamingSearchArtifactsMatchCatalogScans(t *testing.T) {
	source := presetFixture(t)
	// PBF strings can contain invalid UTF-8. Match the JSON-normalized
	// labels used by both legacy catalog scans, including POI validation.
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"Café Fenster", "Testdorf"} {
		replacement := append([]byte{0xff}, []byte(label)[1:]...)
		data = bytes.ReplaceAll(data, []byte(label), replacement)
	}
	if err := os.WriteFile(source, data, 0o600); err != nil {
		t.Fatal(err)
	}
	router, _, err := osm.BuildRouterWithAddressesOptions(source, osm.BuildOptions{Context: t.Context(), SkipAddresses: true})
	if err != nil {
		t.Fatal(err)
	}
	defer router.Close()
	for _, preset := range []POIPreset{POIPresetAll, POIPresetEveryday, POIPresetFirefighting} {
		t.Run(string(preset), func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "search.jsonl")
			if err := buildSearchArtifacts(t.Context(), path, router, source, preset); err != nil {
				t.Fatal(err)
			}
			search, err := openSearch(path)
			if err != nil {
				t.Fatal(err)
			}
			defer search.close()
			digest, err := fileHash(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			expectedPlaces := filepath.Join(directory, "scanned-places")
			if err := writePlaceCatalog(t.Context(), expectedPlaces, search); err != nil {
				t.Fatal(err)
			}
			expectedPOIs := filepath.Join(directory, "scanned-pois")
			if err := buildPOIIndex(t.Context(), expectedPOIs, search, digest); err != nil {
				t.Fatal(err)
			}
			for expected, actual := range map[string]string{
				expectedPlaces: filepath.Join(directory, placeCatalogFilename),
				expectedPOIs:   filepath.Join(directory, poiIndexFilename),
			} {
				want, err := os.ReadFile(expected)
				if err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(actual)
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("streamed artifact %s differs from independent catalog scan: %v", actual, err)
				}
			}
		})
	}
}

func TestCancelledStreamingArtifactsLeaveNoPublishedSidecars(t *testing.T) {
	directory := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := buildSearchArtifacts(ctx, filepath.Join(directory, "search.jsonl"), nil, "missing.pbf", POIPresetAll)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled build: %v", err)
	}
	for _, name := range []string{placeCatalogFilename, poiIndexFilename, "search.ready"} {
		if _, err := os.Stat(filepath.Join(directory, name)); !os.IsNotExist(err) {
			t.Fatalf("cancelled build left %s: %v", name, err)
		}
	}
}

func TestImportResumeRestoresLegacySearchSidecars(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "region")
	options := ImportOptions{PBF: fixture(t), Destination: destination, MinZoom: 14, MaxZoom: 14}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, err := Import(ctx, options, func(progress Progress) {
		if progress.Phase == "building_map" {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	stage := destination + ".pending"
	orphan := filepath.Join(stage, ".places-interrupted.tmp")
	if err := os.WriteFile(orphan, []byte("unfinished labels"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{placeCatalogFilename, poiIndexFilename} {
		if err := os.Remove(filepath.Join(stage, name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Import(t.Context(), options, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, filepath.Base(orphan))); !os.IsNotExist(err) {
		t.Fatalf("published abandoned places temporary file: %v", err)
	}
	engine, err := Open(t.Context(), destination)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if results, err := engine.Search(t.Context(), "cafe", 10); err != nil || len(results) == 0 {
		t.Fatalf("resumed search: %+v, %v", results, err)
	}
}

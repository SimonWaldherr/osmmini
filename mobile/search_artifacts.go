package mobile

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	osm "simonwaldherr.de/go/osmmini/core"
)

// Prepare map labels and spatial POI postings while the importer already owns
// each decoded record. Neither index needs another full decompression/JSON pass
// over the regional search catalog when the new map is first opened.
func buildSearchArtifacts(ctx context.Context, path string, router *osm.Router, source string, preset POIPreset) (err error) {
	directory := filepath.Dir(path)
	if err := cleanPlaceCatalogScratch(ctx, directory); err != nil {
		return err
	}
	placesPath := filepath.Join(directory, placeCatalogFilename)
	poiPath := filepath.Join(directory, poiIndexFilename)
	pois := searchIndexBuilder{ctx: ctx, path: poiPath}
	defer pois.close()
	defer func() {
		if err != nil {
			_ = os.Remove(placesPath)
			_ = os.Remove(poiPath)
		}
	}()
	err = writePlaceCatalogFrom(ctx, placesPath, func(place func(SearchResult) error) error {
		return buildSearchWithPresetObserved(ctx, path, router, source, preset, func(record SearchResult, offset uint64, length uint32) error {
			// The legacy label scan drains records larger than its bounded line
			// buffer. Preserve that selection, even for a short name with huge text.
			if length <= placeScanBufferBytes {
				if _, _, _, valid := settlementRecord(record); valid {
					if err := place(record); err != nil {
						return err
					}
				}
			}
			if !validPOI(record) {
				return nil
			}
			x, y := poiWorld(record)
			cell := poiCell(int(x*(1<<poiIndexZoom)), int(y*(1<<poiIndexZoom)), poiIndexZoom)
			return pois.add(searchPosting{term: poiKey(cell), offset: offset, length: length})
		})
	})
	if err != nil {
		return err
	}
	if err = pois.finish(); err != nil {
		return err
	}
	digest, err := fileHash(ctx, path)
	if err != nil {
		return err
	}
	sourceDigest, err := hex.DecodeString(digest)
	if err != nil {
		return err
	}
	return writePOIIndexFooter(ctx, poiPath, sourceDigest)
}

func restoreSearchArtifacts(ctx context.Context, path string) error {
	if err := cleanPlaceCatalogScratch(ctx, filepath.Dir(path)); err != nil {
		return err
	}
	search, err := openSearch(path)
	if err != nil {
		return err
	}
	defer search.close()
	directory := filepath.Dir(path)
	if err := writePlaceCatalog(ctx, filepath.Join(directory, placeCatalogFilename), search); err != nil {
		return err
	}
	digest, err := fileHash(ctx, path)
	if err != nil {
		return err
	}
	poiPath := filepath.Join(directory, poiIndexFilename)
	if cached, err := readPOIIndex(ctx, poiPath, search, digest); err == nil {
		cached.close()
		return nil
	}
	return buildPOIIndex(ctx, poiPath, search, digest)
}

func cleanPlaceCatalogScratch(ctx context.Context, directory string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if strings.HasPrefix(entry.Name(), ".places-") && strings.HasSuffix(entry.Name(), ".tmp") &&
			(entry.Type().IsRegular() || entry.Type()&os.ModeSymlink != 0) {
			if err := os.Remove(filepath.Join(directory, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

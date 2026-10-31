// Package mobile embeds OSMmini without an HTTP server. Imported regions are
// immutable portable directories and can be copied between macOS and iOS.
package mobile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tinytiles "github.com/Karte-Bayern/tinyTiles/v2"
	osm "simonwaldherr.de/go/osmmini/core"
)

const FormatVersion = 4

// Import semantics may change without changing the readable region format.
// Never mix checkpoints or reuse regions prepared with different import rules.
const importRevision = 8

const landmarkFilename = "routing.alt"

const regionDataNotice = `Map data © OpenStreetMap contributors.

The OpenStreetMap data in this offline region is available under the
Open Database License (ODbL) 1.0:
https://www.openstreetmap.org/copyright
https://opendatacommons.org/licenses/odbl/1-0/

This region contains graph, search and map data derived from the supplied
OpenStreetMap extract. Its source SHA-256 and import time are in manifest.json.
Keep this notice when copying or distributing the region directory.
Software licensing is separate from this map-data notice.
`

type Manifest struct {
	Format         int               `json:"format"`
	Name           string            `json:"name"`
	CreatedAt      time.Time         `json:"created_at"`
	SourceSHA256   string            `json:"source_sha256"`
	Nodes          int               `json:"nodes"`
	Edges          int               `json:"edges"`
	Bounds         osm.CoordWindow   `json:"bounds"`
	MinZoom        int               `json:"min_zoom"`
	MaxZoom        int               `json:"max_zoom"`
	Checksums      map[string]string `json:"checksums"`
	POIPreset      POIPreset         `json:"poi_preset,omitempty"`
	ImportRevision int               `json:"import_revision,omitempty"`
}
type ImportOptions struct {
	PBF             string    `json:"pbf"`
	Destination     string    `json:"destination"`
	Name            string    `json:"name"`
	MinZoom         int       `json:"min_zoom,omitempty"`
	MaxZoom         int       `json:"max_zoom,omitempty"`
	MaxImportNodes  int       `json:"max_import_nodes,omitempty"`
	TileMemoryBytes int64     `json:"tile_memory_bytes,omitempty"`
	POIPreset       POIPreset `json:"poi_preset,omitempty"`
}
type Progress struct {
	Phase    string  `json:"phase"`
	Fraction float64 `json:"fraction"`
}

var importLocks sync.Map

// Import preserves completed graph/search phases in destination.pending when
// cancelled. Repeating the same request resumes those phases. Existing regions
// are never replaced: updates use a new destination and switch after opening.
func Import(ctx context.Context, o ImportOptions, progress func(Progress)) (Manifest, error) {
	var manifest Manifest
	preset, err := o.POIPreset.normalized()
	if err != nil {
		return manifest, err
	}
	o.POIPreset = preset
	if o.PBF == "" || o.Destination == "" {
		return manifest, errors.New("pbf and destination are required")
	}
	if o.MinZoom == 0 {
		o.MinZoom = 5
	}
	if o.MaxZoom == 0 {
		o.MaxZoom = 14
	}
	if o.MinZoom < 5 || o.MaxZoom > 14 || o.MinZoom > o.MaxZoom {
		return manifest, errors.New("zoom must be within 5…14")
	}
	if o.MaxImportNodes == 0 {
		o.MaxImportNodes = 8_000_000
	}
	if o.MaxImportNodes < 0 {
		return manifest, errors.New("max_import_nodes must be positive")
	}
	if o.TileMemoryBytes == 0 {
		o.TileMemoryBytes = 256 << 20
	}
	if o.TileMemoryBytes < 16<<20 {
		return manifest, errors.New("tile_memory_bytes must be at least 16 MiB")
	}
	o.Destination, err = filepath.Abs(o.Destination)
	if err != nil {
		return manifest, err
	}
	if _, running := importLocks.LoadOrStore(o.Destination, struct{}{}); running {
		return manifest, errors.New("this region is already being imported")
	}
	defer importLocks.Delete(o.Destination)
	report := func(phase string, f float64) {
		if progress != nil {
			progress(Progress{phase, f})
		}
	}
	report("checking", 0)
	sourceHash, err := fileHash(ctx, o.PBF)
	if err != nil {
		return manifest, err
	}
	stage := o.Destination + ".pending"
	sourcePath, err := filepath.EvalSymlinks(o.PBF)
	if err != nil {
		return manifest, err
	}
	sourcePath, err = filepath.Abs(sourcePath)
	if err != nil {
		return manifest, err
	}
	resolvedStage := stage
	if resolved, e := filepath.EvalSymlinks(stage); e == nil {
		resolvedStage = resolved
	}
	if relative, e := filepath.Rel(resolvedStage, sourcePath); e == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return manifest, errors.New("source PBF must be outside the import workspace")
	}
	if _, e := os.Stat(o.Destination); e == nil {
		existing, e := Open(ctx, o.Destination)
		if e != nil {
			return manifest, fmt.Errorf("destination already exists: %w", e)
		}
		defer existing.Close()
		if existing.Manifest.SourceSHA256 == sourceHash && existing.Manifest.MinZoom == o.MinZoom && existing.Manifest.MaxZoom == o.MaxZoom && existing.Manifest.POIPreset == o.POIPreset && existing.Manifest.ImportRevision == importRevision {
			report("ready", 1)
			return existing.Manifest, nil
		}
		return manifest, errors.New("destination contains another region revision; select a new directory")
	}
	if err = os.MkdirAll(stage, 0o700); err != nil {
		return manifest, err
	}
	signature := fmt.Sprintf("%s:%d:%d:%d:%d:%s", sourceHash, o.MinZoom, o.MaxZoom, FormatVersion, importRevision, o.POIPreset)
	signaturePath := filepath.Join(stage, "source")
	if b, e := os.ReadFile(signaturePath); e == nil && string(b) != signature {
		return manifest, errors.New("pending import belongs to another source/options/import version; select a new destination")
	}
	if err = os.WriteFile(signaturePath, []byte(signature), 0o600); err != nil {
		return manifest, err
	}
	// Finish and close graph/search preparation before starting the tile
	// generator. Its mmap and street lookup maps need not overlap that peak.
	graph, err := prepareGraphAndSearch(ctx, o, stage, report)
	if err != nil {
		return manifest, err
	}

	if err = cleanMapImportScratch(ctx, stage); err != nil {
		return manifest, err
	}
	report("building_map", 0.5)
	_, err = tinytiles.BuildPBF(ctx, tinytiles.PBFBuildOptions{
		PBFInputs: []string{o.PBF}, ArtifactPath: filepath.Join(stage, "basemap.ttiles"),
		MinZoom: o.MinZoom, MaxZoom: o.MaxZoom, Concurrency: 1,
		// tinySQL budgets a batch at the largest tile's size. The upstream
		// 1,000-row default exceeds 256 MiB even for Niederbayern. Small
		// batches preserve the caller's budget without increasing its limit.
		BatchSize: 32, MaxMemoryBytes: o.TileMemoryBytes, MinFreeBytes: 1,
		ReplaceExisting: true,
		Progress: func(p tinytiles.PBFBuildProgress) {
			fraction := 0.6
			if p.Import != nil && p.Import.TotalRows > 0 {
				fraction += 0.25 * min(1, max(0, float64(p.Import.RowsWritten)/float64(p.Import.TotalRows)))
			}
			if p.Phase == "published" {
				fraction = 0.85
			}
			report("map_"+p.Phase, fraction)
		},
	})
	if err != nil {
		return manifest, err
	}
	manifest = Manifest{Format: FormatVersion, Name: o.Name, CreatedAt: time.Now().UTC(), SourceSHA256: sourceHash, Nodes: graph.nodes, Edges: graph.edges, Bounds: graph.bounds, MinZoom: o.MinZoom, MaxZoom: o.MaxZoom, POIPreset: o.POIPreset, ImportRevision: importRevision, Checksums: map[string]string{}}
	if manifest.Name == "" {
		manifest.Name = filepath.Base(o.PBF)
	}
	report("validating", 0.9)
	for _, name := range []string{"graph.bin", "search.jsonl", "search.jsonl.idx", placeCatalogFilename, landmarkFilename} {
		hash, e := fileHash(ctx, filepath.Join(stage, name))
		if e != nil {
			return manifest, e
		}
		manifest.Checksums[name] = hash
	}
	if err = ctx.Err(); err != nil {
		return manifest, err
	}
	currentHash, err := fileHash(ctx, o.PBF)
	if err != nil {
		return manifest, err
	}
	if currentHash != sourceHash {
		return manifest, errors.New("PBF changed during import; choose a new workspace")
	}
	// The data directory can be copied independently of the SDK or demo. Keep
	// attribution and the data licence with that portable database as well.
	if err = writeSynced(filepath.Join(stage, "NOTICE.txt"), []byte(regionDataNotice)); err != nil {
		return manifest, err
	}
	b, _ := json.MarshalIndent(manifest, "", "  ")
	if err = writeSynced(filepath.Join(stage, "manifest.json"), b); err != nil {
		return manifest, err
	}
	if err = ctx.Err(); err != nil {
		return manifest, err
	}
	if err = os.Rename(stage, o.Destination); err != nil {
		return manifest, err
	}
	report("ready", 1)
	return manifest, nil
}

// Only value metadata leaves this phase; graph buffers and lookup maps can be
// collected while tinyTiles builds its own feature and coordinate structures.
type importGraphInfo struct {
	nodes, edges int
	bounds       osm.CoordWindow
}

func prepareGraphAndSearch(ctx context.Context, o ImportOptions, stage string, report func(string, float64)) (importGraphInfo, error) {
	var err error
	graphPath := filepath.Join(stage, "graph.bin")
	var router *osm.Router
	if _, err = os.Stat(filepath.Join(stage, "graph.ready")); err == nil {
		report("loading_graph", 0.3)
		router, err = osm.OpenGraph(graphPath)
	} else {
		report("building_graph", 0.05)
		router, _, err = osm.BuildRouterWithAddressesOptions(o.PBF, osm.BuildOptions{Context: ctx, MaxNodes: o.MaxImportNodes, SkipAddresses: true})
		if err == nil && router.NodeCount() == 0 {
			err = errors.New("PBF contains no routable roads")
		}
		if err == nil {
			_ = os.Remove(graphPath)
			err = router.WriteGraph(ctx, graphPath)
		}
		if err == nil {
			err = os.WriteFile(filepath.Join(stage, "graph.ready"), []byte("1"), 0o600)
		}
		if err == nil {
			// Drop the importer's coordinate/adjacency maps before the next PBF
			// passes. Search and tile preparation only need the file-backed graph.
			router, err = osm.OpenGraph(graphPath)
		}
	}
	if err != nil {
		return importGraphInfo{}, err
	}
	defer router.Close()
	report("indexing_routing", 0.32)
	landmarkPath := filepath.Join(stage, landmarkFilename)
	if err = router.LoadLandmarks(ctx, landmarkPath); err != nil {
		if err = ctx.Err(); err != nil {
			return importGraphInfo{}, err
		}
		_ = os.Remove(landmarkPath)
		if err = router.WriteLandmarks(ctx, landmarkPath); err != nil {
			return importGraphInfo{}, err
		}
	}
	report("indexing_search", 0.35)
	searchPath := filepath.Join(stage, "search.jsonl")
	if _, err = os.Stat(filepath.Join(stage, "search.ready")); err != nil {
		if err = buildSearchArtifacts(ctx, searchPath, router, o.PBF, o.POIPreset); err != nil {
			return importGraphInfo{}, err
		}
		if err = os.WriteFile(filepath.Join(stage, "search.ready"), []byte("1"), 0o600); err != nil {
			return importGraphInfo{}, err
		}
	} else {
		// Older checkpoints may contain only the search files. Rebuild missing
		// derived data without decoding the source PBF again.
		if err = restoreSearchArtifacts(ctx, searchPath); err != nil {
			return importGraphInfo{}, err
		}
	}
	bounds, _ := router.Bounds()
	return importGraphInfo{nodes: router.NodeCount(), edges: router.EdgeCount(), bounds: bounds}, nil
}

func writeSynced(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write(b); err != nil {
		return err
	}
	return f.Sync()
}
func fileHash(ctx context.Context, path string) (string, error) {
	return fileHashBuffer(ctx, path, make([]byte, 1<<20))
}

func fileHashBuffer(ctx context.Context, path string, buf []byte) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	for {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		n, e := f.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", e
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type Engine struct {
	mu          sync.RWMutex
	router      *osm.Router
	search      *searchReader
	tiles       *tinytiles.Dataset
	places      placeIndex
	settlements exactPlaceIndex
	pois        *poiIndex
	Manifest    Manifest
	closed      bool
}

func Open(ctx context.Context, path string) (*Engine, error) {
	b, err := os.ReadFile(filepath.Join(path, "manifest.json"))
	if err != nil {
		return nil, err
	}
	if len(b) > 1<<20 {
		return nil, errors.New("region manifest is too large")
	}
	var manifest Manifest
	if err = json.Unmarshal(b, &manifest); err != nil {
		return nil, err
	}
	if manifest.Format < 1 || manifest.Format > FormatVersion {
		return nil, fmt.Errorf("unsupported region format %d", manifest.Format)
	}
	names := []string{"graph.bin", "search.jsonl", "search.jsonl.idx"}
	_, hasPlaceCatalog := manifest.Checksums[placeCatalogFilename]
	if hasPlaceCatalog {
		names = append(names, placeCatalogFilename)
	}
	_, hasLandmarks := manifest.Checksums[landmarkFilename]
	if hasLandmarks {
		names = append(names, landmarkFilename)
	}
	// Integrity checks share one bounded scratch buffer, independent of the
	// number and size of files. Never skip validation to speed up reopening.
	hashBuffer := make([]byte, 1<<20)
	for _, name := range names {
		expected, ok := manifest.Checksums[name]
		if !ok {
			return nil, fmt.Errorf("missing checksum: %s", name)
		}
		// Indexed regions verify the graph's mapped bytes below. Reuse that
		// digest for the index binding instead of hashing this large file twice.
		if name == "graph.bin" && hasLandmarks {
			continue
		}
		actual, e := fileHashBuffer(ctx, filepath.Join(path, name), hashBuffer)
		if e != nil {
			return nil, e
		}
		if actual != expected {
			return nil, fmt.Errorf("checksum mismatch: %s", name)
		}
	}
	r, err := osm.OpenGraph(filepath.Join(path, "graph.bin"))
	if err != nil {
		return nil, err
	}
	if hasLandmarks {
		if err := r.VerifyGraphChecksum(ctx, manifest.Checksums["graph.bin"]); err != nil {
			r.Close()
			return nil, err
		}
		if err := r.LoadLandmarks(ctx, filepath.Join(path, landmarkFilename)); err != nil {
			r.Close()
			return nil, err
		}
	}
	search, err := openSearch(filepath.Join(path, "search.jsonl"))
	if err != nil {
		r.Close()
		return nil, err
	}
	var places *placeCatalog
	if hasPlaceCatalog {
		places, err = readPlaceCatalog(ctx, filepath.Join(path, placeCatalogFilename))
	} else {
		// Existing portable regions remain usable without a new PBF import.
		places, err = loadPlaceCatalog(ctx, search)
	}
	if err != nil {
		r.Close()
		search.close()
		return nil, err
	}
	tiles, err := tinytiles.Open(ctx, filepath.Join(path, "basemap.ttiles"), tinytiles.OpenOptions{Readers: 2, MaxMemoryBytes: 16 << 20})
	if err != nil {
		r.Close()
		search.close()
		return nil, err
	}
	pois, err := openPOIIndex(ctx, path, search, manifest.Checksums["search.jsonl"])
	if err != nil {
		r.Close()
		search.close()
		_ = tiles.Close()
		return nil, err
	}
	return &Engine{
		router: r, search: search, tiles: tiles, places: places.labels,
		settlements: places.exact, pois: pois, Manifest: manifest,
	}, nil
}
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	e.places = nil
	e.settlements = nil
	e.pois.close()
	e.search.close()
	_ = e.tiles.Close()
	return e.router.Close()
}
func (e *Engine) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed {
		return nil, errors.New("region is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	exact := e.settlements.matches(query)
	if len(exact) == 0 {
		return e.search.search(ctx, query, limit)
	}
	// Exact settlement names precede disk results before the limit is applied.
	// Copy the immutable lookup rows so callers cannot mutate later searches.
	results := make([]SearchResult, 0, limit)
	type identity struct {
		kind string
		id   int64
	}
	seen := make(map[identity]bool, limit)
	appendUnique := func(candidates []SearchResult) {
		for _, result := range candidates {
			key := identity{result.Kind, result.ID}
			if !seen[key] {
				result.Details = cleanDetails(result.Details)
				results = append(results, result)
				seen[key] = true
				if len(results) == limit {
					return
				}
			}
		}
	}
	appendUnique(exact)
	if len(results) < limit {
		found, err := e.search.search(ctx, query, limit)
		if err != nil {
			return nil, err
		}
		appendUnique(found)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

type RouteRequest struct {
	From    osm.Coord        `json:"from"`
	To      osm.Coord        `json:"to"`
	Options osm.RouteOptions `json:"options"`
}
type Route struct {
	Path      []osm.Coord    `json:"path"`
	DistanceM float64        `json:"distance_m"`
	DurationS float64        `json:"duration_s"`
	FromSnapM float64        `json:"from_snap_m"`
	ToSnapM   float64        `json:"to_snap_m"`
	Steps     []osm.Maneuver `json:"steps"`
}

func (e *Engine) Route(ctx context.Context, request RouteRequest) (Route, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed {
		return Route{}, errors.New("region is closed")
	}
	for _, c := range []osm.Coord{request.From, request.To} {
		if math.IsNaN(c.Lat) || math.IsNaN(c.Lon) || math.IsInf(c.Lat, 0) || math.IsInf(c.Lon, 0) || math.Abs(c.Lat) > 90 || math.Abs(c.Lon) > 180 {
			return Route{}, errors.New("invalid WGS84 coordinate")
		}
		if !e.Manifest.Bounds.Contains(c) {
			return Route{}, errors.New("location is outside the installed region")
		}
	}
	a, sa, ok := e.router.NearestNode(request.From.Lat, request.From.Lon)
	if !ok {
		return Route{}, errors.New("no start road")
	}
	b, sb, ok := e.router.NearestNode(request.To.Lat, request.To.Lon)
	if !ok {
		return Route{}, errors.New("no destination road")
	}
	return routeWithNearbySnaps(ctx, e.router, request,
		osm.NodeSnap{ID: a, DistanceM: sa}, osm.NodeSnap{ID: b, DistanceM: sb})
}
func (e *Engine) Tile(ctx context.Context, z, x, y int) (VectorTile, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed {
		return VectorTile{}, errors.New("region is closed")
	}
	if err := ctx.Err(); err != nil {
		return VectorTile{}, err
	}
	if z < 0 || z > 22 || x < 0 || y < 0 || x >= 1<<z || y >= 1<<z {
		return VectorTile{}, errors.New("invalid tile coordinates")
	}
	tile := VectorTile{Features: []VectorFeature{}}
	// Swift can request detail tiles at its display zoom independently of the
	// lower-resolution basemap. Never ask the dataset for an unknown zoom or
	// repeat its coarse geometry in those overzoomed detail tiles.
	if z <= e.Manifest.MaxZoom {
		data, err := e.tiles.GetTileXYZ(z, x, y)
		if contextErr := ctx.Err(); contextErr != nil {
			return VectorTile{}, contextErr
		}
		if err != nil && !errors.Is(err, tinytiles.ErrTileNotFound) {
			return VectorTile{}, err
		}
		if err == nil {
			tile, err = decodeVectorTile(ctx, data)
			if err != nil {
				return VectorTile{}, err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return VectorTile{}, err
	}
	tile.Features = append(tile.Features, e.places.features(z, x, y)...)
	if z >= min(poiIndexZoom, e.Manifest.MaxZoom) {
		points, err := e.pois.features(ctx, z, x, y)
		if err != nil {
			return VectorTile{}, err
		}
		tile.Features = append(tile.Features, points...)
	}
	return tile, nil
}

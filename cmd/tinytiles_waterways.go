package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	osmmini "simonwaldherr.de/go/osmmini"
)

// tinyTilesWaterwaySidecarVersion deliberately has its own version number:
// waterway geometry is an osmmini companion layer, not part of tinyTiles'
// immutable artifact format. Bumping it lets us reject an incompatible local
// sidecar without making an otherwise valid offline basemap unavailable.
const tinyTilesWaterwaySidecarVersion = 3

const (
	// A quarter-degree cell keeps the index compact for country-sized extracts
	// while avoiding a full feature scan on every map movement. A feature that
	// spans an excessive number of cells stays in largeFeatures instead.
	tinyTilesWaterwayCellSize     = 0.25
	tinyTilesWaterwayMaxGridCells = 4_096
	// A requested viewport must not fan out into an unbounded world-grid scan.
	// It is deliberately the same order of magnitude as the largest feature
	// index footprint, which is ample for practical regional map views.
	tinyTilesWaterwayMaxQueryGridCells = 4_096
	tinyTilesWaterwayMaxPoints         = 100_000
)

// tinyTilesWaterway is a compact, local representation of a linear OSM
// waterway. Coordinates use GeoJSON order ([longitude, latitude]) so they can
// be handed to MapLibre without an additional conversion on every viewport
// refresh. bounds is restored on load and never persisted redundantly.
type tinyTilesWaterway struct {
	ID          int64        `json:"id"`
	Class       string       `json:"class"`
	Name        string       `json:"name,omitempty"`
	MinZoom     int          `json:"min_zoom"`
	Coordinates [][2]float64 `json:"coordinates"`
	bounds      tinyTilesWaterwayBounds
}

type tinyTilesWaterwayBounds struct {
	minLon float64
	minLat float64
	maxLon float64
	maxLat float64
}

type tinyTilesWaterwaySidecar struct {
	Version                int                 `json:"version"`
	ArtifactManifestSHA256 string              `json:"artifact_manifest_sha256,omitempty"`
	Features               []tinyTilesWaterway `json:"features"`
	Buildings              []tinyTilesBuilding `json:"buildings"`
	Source                 string              `json:"source,omitempty"`
}

type tinyTilesWaterwayIndex struct {
	features      []tinyTilesWaterway
	cells         map[int64][]int
	largeFeatures []int
	buildings     *tinyTilesBuildingIndex
	cellSize      float64
}

type tinyTilesWaterwayCandidate struct {
	ID      int64
	Class   string
	Name    string
	MinZoom int
	NodeIDs []int64
}

type tinyTilesWaterwayGeoJSON struct {
	Type      string                         `json:"type"`
	Features  []tinyTilesWaterwayGeoJSONItem `json:"features"`
	Truncated bool                           `json:"truncated,omitempty"`
}

type tinyTilesWaterwayGeoJSONItem struct {
	Type       string                           `json:"type"`
	Properties tinyTilesWaterwayProperties      `json:"properties"`
	Geometry   tinyTilesWaterwayGeoJSONGeometry `json:"geometry"`
}

type tinyTilesWaterwayProperties struct {
	Class string `json:"class"`
	Name  string `json:"name,omitempty"`
}

type tinyTilesWaterwayGeoJSONGeometry struct {
	Type        string       `json:"type"`
	Coordinates [][2]float64 `json:"coordinates"`
}

func tinyTilesWaterwaySidecarPath(dir string) string {
	return filepath.Join(dir, "basemap.waterways.json")
}

// tinyTilesWaterwayClass deliberately keeps the generated companion layer
// useful at a regional scale. River and canal lines appear early, streams at
// closer zoom, and the much denser drainage network only at local-detail zoom.
// This prevents a dense PBF from turning the offline map into a blue mesh.
func tinyTilesWaterwayClass(value string) (class string, minZoom int, ok bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "river":
		return "river", 7, true
	case "canal":
		return "canal", 8, true
	case "stream":
		return "stream", 11, true
	case "wadi":
		return "wadi", 12, true
	case "ditch", "drain":
		return strings.ToLower(strings.TrimSpace(value)), 13, true
	default:
		return "", 0, false
	}
}

// buildTinyTilesWaterwaySidecar extracts the linear water features that the
// upstream tinyTiles minimal generator intentionally omits, together with building
// footprints and heights. Both companion layers share a
// two-pass PBF scan: first retain selected waterway/building ways and their node
// IDs, then load coordinates only for those IDs. The finished sidecar is
// written atomically and is activated alongside the .ttiles artifact.
func buildTinyTilesWaterwaySidecar(pbfPath, outputPath string) (int, error) {
	source, err := tinyTilesCompanionSource(pbfPath)
	if err != nil {
		return 0, err
	}
	// A zoom/postcode rebuild can reuse validated geometry for the same PBF.
	cached, cacheErr := readTinyTilesWaterwaySidecar(tinyTilesWaterwaySidecarPath(filepath.Dir(outputPath)))
	if cacheErr == nil && cached.Version == tinyTilesWaterwaySidecarVersion && cached.Source == source && cached.Buildings != nil {
		if _, err := tinyTilesWaterwayIndexFromSidecar(cached); err == nil {
			cached.ArtifactManifestSHA256 = ""
			if err := writeTinyTilesWaterwaySidecar(outputPath, cached); err != nil {
				return 0, err
			}
			return len(cached.Features), nil
		}
	}
	var buildingCandidates []osmmini.Way
	candidates := make([]tinyTilesWaterwayCandidate, 0, 256)
	neededNodes := make(map[int64]struct{})
	if err := osmmini.ExtractFile(pbfPath, osmmini.Options{
		EmitWayNodeIDs: true,
		TaggedWayKey: func(key string) bool {
			return key == "waterway" || key == "building" || key == "building:part"
		},
		KeepTag: func(key string) bool {
			return key == "waterway" || key == "name" || tinyTilesBuildingTag(key)
		},
	}, osmmini.Callbacks{
		TaggedWay: func(way osmmini.Way) error {
			if tinyTilesIsBuilding(way.Tags) && len(way.NodeIDs) >= 4 && way.NodeIDs[0] == way.NodeIDs[len(way.NodeIDs)-1] {
				way.NodeIDs = append([]int64(nil), way.NodeIDs...)
				buildingCandidates = append(buildingCandidates, way)
				for _, id := range way.NodeIDs {
					neededNodes[id] = struct{}{}
				}
			}
			class, minZoom, ok := tinyTilesWaterwayClass(way.Tags["waterway"])
			if !ok || len(way.NodeIDs) < 2 {
				return nil
			}
			ids := append([]int64(nil), way.NodeIDs...)
			candidates = append(candidates, tinyTilesWaterwayCandidate{
				ID: way.ID, Class: class, Name: strings.TrimSpace(way.Tags["name"]), MinZoom: minZoom, NodeIDs: ids,
			})
			for _, nodeID := range ids {
				neededNodes[nodeID] = struct{}{}
			}
			return nil
		},
	}); err != nil {
		return 0, fmt.Errorf("extract waterway references: %w", err)
	}

	nodes := make(map[int64]osmmini.Coord, len(neededNodes))
	if len(neededNodes) > 0 {
		if err := osmmini.ExtractFile(pbfPath, osmmini.Options{}, osmmini.Callbacks{
			Node: func(id int64, lat, lon float64) error {
				if _, wanted := neededNodes[id]; wanted {
					nodes[id] = osmmini.Coord{Lat: lat, Lon: lon}
				}
				return nil
			},
		}); err != nil {
			return 0, fmt.Errorf("extract waterway coordinates: %w", err)
		}
	}

	buildings := make([]tinyTilesBuilding, 0, len(buildingCandidates))
	for _, candidate := range buildingCandidates {
		coordinates := make([][2]float64, 0, len(candidate.NodeIDs))
		for _, id := range candidate.NodeIDs {
			p, ok := nodes[id]
			if !ok {
				coordinates = nil
				break
			}
			point := [2]float64{p.Lon, p.Lat}
			if len(coordinates) == 0 || coordinates[len(coordinates)-1] != point {
				coordinates = append(coordinates, point)
			}
		}
		properties := tinyTilesBuildingProperties(candidate.Tags)
		building := tinyTilesBuilding{ID: candidate.ID, Properties: properties, Coordinates: [][][2]float64{coordinates}}
		if building.normalize() {
			buildings = append(buildings, building)
		}
	}
	sort.Slice(buildings, func(i, j int) bool { return buildings[i].ID < buildings[j].ID })
	currentSource, err := tinyTilesCompanionSource(pbfPath)
	if err != nil || currentSource != source {
		return 0, fmt.Errorf("PBF changed while extracting companion layers")
	}

	// Stable ordering makes the sidecar deterministic for an unchanged PBF.
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	features := make([]tinyTilesWaterway, 0, len(candidates))
	for _, candidate := range candidates {
		coordinates := make([][2]float64, 0, len(candidate.NodeIDs))
		complete := true
		for _, nodeID := range candidate.NodeIDs {
			coordinate, ok := nodes[nodeID]
			if !ok {
				// Do not silently join coordinates across an absent node: that
				// would draw a plausible but incorrect river segment. A complete
				// regional PBF contains all way nodes, so skipping this feature is
				// safer than fabricating an interpolation here.
				complete = false
				break
			}
			point := [2]float64{coordinate.Lon, coordinate.Lat}
			if len(coordinates) == 0 || coordinates[len(coordinates)-1] != point {
				coordinates = append(coordinates, point)
			}
		}
		if !complete || len(coordinates) < 2 {
			continue
		}
		feature := tinyTilesWaterway{
			ID: candidate.ID, Class: candidate.Class, Name: candidate.Name, MinZoom: candidate.MinZoom, Coordinates: coordinates,
		}
		if !feature.normalize() {
			continue
		}
		features = append(features, feature)
	}
	// Large map views have a deliberate feature limit. Put the major waterways
	// first so a dense drainage network can never hide an otherwise visible
	// river merely because it has a lower OSM ID.
	sortTinyTilesWaterways(features)

	if err := writeTinyTilesWaterwaySidecar(outputPath, tinyTilesWaterwaySidecar{
		Version:   tinyTilesWaterwaySidecarVersion,
		Features:  features,
		Buildings: buildings,
		Source:    source,
	}); err != nil {
		return 0, err
	}
	return len(features), nil
}

func tinyTilesWaterwayPriority(class string) int {
	switch class {
	case "river":
		return 0
	case "canal":
		return 1
	case "stream":
		return 2
	case "wadi":
		return 3
	case "ditch":
		return 4
	case "drain":
		return 5
	default:
		return 99
	}
}

func sortTinyTilesWaterways(features []tinyTilesWaterway) {
	sort.Slice(features, func(i, j int) bool {
		left, right := tinyTilesWaterwayPriority(features[i].Class), tinyTilesWaterwayPriority(features[j].Class)
		if left != right {
			return left < right
		}
		return features[i].ID < features[j].ID
	})
}

func writeTinyTilesWaterwaySidecar(path string, sidecar tinyTilesWaterwaySidecar) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create waterway sidecar directory: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("create waterway sidecar: %w", err)
	}
	temporary := file.Name()
	defer func() {
		if err != nil {
			_ = file.Close()
			_ = os.Remove(temporary)
		}
	}()
	if err := file.Chmod(0o644); err != nil {
		return fmt.Errorf("set waterway sidecar permissions: %w", err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(sidecar); err != nil {
		return fmt.Errorf("encode waterway sidecar: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close waterway sidecar: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("publish waterway sidecar: %w", err)
	}
	return nil
}

func readTinyTilesWaterwaySidecar(path string) (tinyTilesWaterwaySidecar, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return tinyTilesWaterwaySidecar{}, err
	}
	var sidecar tinyTilesWaterwaySidecar
	if err := json.Unmarshal(data, &sidecar); err != nil {
		return tinyTilesWaterwaySidecar{}, fmt.Errorf("decode waterway sidecar: %w", err)
	}
	if sidecar.Version != tinyTilesWaterwaySidecarVersion && sidecar.Version != 2 {
		return tinyTilesWaterwaySidecar{}, fmt.Errorf("unsupported waterway sidecar version %d", sidecar.Version)
	}
	return sidecar, nil
}

func tinyTilesWaterwayIndexFromSidecar(sidecar tinyTilesWaterwaySidecar) (*tinyTilesWaterwayIndex, error) {
	features := sidecar.Features[:0]
	for _, feature := range sidecar.Features {
		class, minZoom, ok := tinyTilesWaterwayClass(feature.Class)
		if !ok || feature.Class != class || feature.MinZoom != minZoom || !feature.normalize() {
			return nil, fmt.Errorf("invalid waterway feature %d", feature.ID)
		}
		features = append(features, feature)
	}
	index := newTinyTilesWaterwayIndex(features)
	if sidecar.Version == tinyTilesWaterwaySidecarVersion && sidecar.Buildings != nil {
		var err error
		index.buildings, err = newTinyTilesBuildingIndex(sidecar.Buildings)
		if err != nil {
			return nil, err
		}
	}
	return index, nil
}

func loadTinyTilesWaterwaySidecar(path string) (*tinyTilesWaterwayIndex, error) {
	sidecar, err := readTinyTilesWaterwaySidecar(path)
	if err != nil {
		return nil, err
	}
	return tinyTilesWaterwayIndexFromSidecar(sidecar)
}

// stampTinyTilesWaterwaySidecar binds the companion data to the precise
// published artifact. It turns two independently written files into a
// verifiable pair: an old sidecar is never silently rendered over a newer
// basemap after a partial publish or a restart.
func stampTinyTilesWaterwaySidecar(path, artifact string) error {
	sidecar, err := readTinyTilesWaterwaySidecar(path)
	if err != nil {
		return err
	}
	fingerprint, err := tinyTilesArtifactManifestSHA256(artifact)
	if err != nil {
		return err
	}
	sidecar.ArtifactManifestSHA256 = fingerprint
	if err := writeTinyTilesWaterwaySidecar(path, sidecar); err != nil {
		return fmt.Errorf("stamp waterway sidecar: %w", err)
	}
	return nil
}

func loadTinyTilesWaterwaySidecarForArtifact(path, artifact string) (*tinyTilesWaterwayIndex, error) {
	sidecar, err := readTinyTilesWaterwaySidecar(path)
	if err != nil {
		return nil, err
	}
	fingerprint, err := tinyTilesArtifactManifestSHA256(artifact)
	if err != nil {
		return nil, err
	}
	if sidecar.ArtifactManifestSHA256 == "" || sidecar.ArtifactManifestSHA256 != fingerprint {
		return nil, fmt.Errorf("waterway sidecar does not match the offline artifact")
	}
	return tinyTilesWaterwayIndexFromSidecar(sidecar)
}

func tinyTilesArtifactManifestPath(artifact string) (string, error) {
	info, err := os.Stat(artifact)
	if err != nil {
		return "", fmt.Errorf("stat offline artifact: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("offline artifact is not a tinyTiles directory")
	}
	return filepath.Join(artifact, "manifest.json"), nil
}

func tinyTilesArtifactManifestSHA256(artifact string) (string, error) {
	manifest, err := tinyTilesArtifactManifestPath(artifact)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(manifest)
	if err != nil {
		return "", fmt.Errorf("read offline artifact manifest: %w", err)
	}
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest), nil
}

func newTinyTilesWaterwayIndex(features []tinyTilesWaterway) *tinyTilesWaterwayIndex {
	return newTinyTilesFeatureGrid(features, tinyTilesWaterwayCellSize)
}

func newTinyTilesFeatureGrid(features []tinyTilesWaterway, cellSize float64) *tinyTilesWaterwayIndex {
	// Sidecars from the local disk are immutable once loaded, but keep an
	// independent, priority-ordered copy so query limits behave correctly even
	// if an older sidecar was written before prioritisation existed.
	ordered := append([]tinyTilesWaterway(nil), features...)
	sortTinyTilesWaterways(ordered)
	index := &tinyTilesWaterwayIndex{
		features: ordered,
		cells:    make(map[int64][]int),
		cellSize: cellSize,
	}
	for featureIndex, feature := range ordered {
		minX, minY := tinyTilesFeatureCell(feature.bounds.minLon, feature.bounds.minLat, cellSize)
		maxX, maxY := tinyTilesFeatureCell(feature.bounds.maxLon, feature.bounds.maxLat, cellSize)
		cellCount := (maxX - minX + 1) * (maxY - minY + 1)
		if cellCount < 1 || cellCount > tinyTilesWaterwayMaxGridCells {
			index.largeFeatures = append(index.largeFeatures, featureIndex)
			continue
		}
		for x := minX; x <= maxX; x++ {
			for y := minY; y <= maxY; y++ {
				key := tinyTilesWaterwayCellKey(x, y)
				index.cells[key] = append(index.cells[key], featureIndex)
			}
		}
	}
	return index
}

func (index *tinyTilesWaterwayIndex) len() int {
	if index == nil {
		return 0
	}
	return len(index.features)
}

func tinyTilesWaterwayGridCellCount(window osmmini.CoordWindow) int64 {
	minX, minY := tinyTilesWaterwayCell(window.MinLon, window.MinLat)
	maxX, maxY := tinyTilesWaterwayCell(window.MaxLon, window.MaxLat)
	return int64(maxX-minX+1) * int64(maxY-minY+1)
}

func (index *tinyTilesWaterwayIndex) featuresIn(window osmmini.CoordWindow) []int {
	if index == nil {
		return nil
	}
	minX, minY := tinyTilesFeatureCell(window.MinLon, window.MinLat, index.cellSize)
	maxX, maxY := tinyTilesFeatureCell(window.MaxLon, window.MaxLat, index.cellSize)
	seen := make(map[int]struct{})
	for x := minX; x <= maxX; x++ {
		for y := minY; y <= maxY; y++ {
			for _, featureIndex := range index.cells[tinyTilesWaterwayCellKey(x, y)] {
				seen[featureIndex] = struct{}{}
			}
		}
	}
	for _, featureIndex := range index.largeFeatures {
		seen[featureIndex] = struct{}{}
	}
	result := make([]int, 0, len(seen))
	for featureIndex := range seen {
		result = append(result, featureIndex)
	}
	sort.Ints(result)
	return result
}

func tinyTilesWaterwayCell(lon, lat float64) (int, int) {
	return tinyTilesFeatureCell(lon, lat, tinyTilesWaterwayCellSize)
}

func tinyTilesFeatureCell(lon, lat, size float64) (int, int) {
	return int(math.Floor((lon + 180) / size)), int(math.Floor((lat + 90) / size))
}

func tinyTilesWaterwayCellKey(x, y int) int64 {
	return int64(int32(x))<<32 | int64(uint32(int32(y)))
}

func (feature *tinyTilesWaterway) normalize() bool {
	if len(feature.Coordinates) < 2 {
		return false
	}
	bounds := tinyTilesWaterwayBounds{
		minLon: math.Inf(1), minLat: math.Inf(1), maxLon: math.Inf(-1), maxLat: math.Inf(-1),
	}
	for _, point := range feature.Coordinates {
		lon, lat := point[0], point[1]
		if math.IsNaN(lon) || math.IsNaN(lat) || math.IsInf(lon, 0) || math.IsInf(lat, 0) || lon < -180 || lon > 180 || lat < -90 || lat > 90 {
			return false
		}
		bounds.minLon = min(bounds.minLon, lon)
		bounds.minLat = min(bounds.minLat, lat)
		bounds.maxLon = max(bounds.maxLon, lon)
		bounds.maxLat = max(bounds.maxLat, lat)
	}
	feature.bounds = bounds
	return true
}

func (bounds tinyTilesWaterwayBounds) intersects(window osmmini.CoordWindow) bool {
	return bounds.minLon <= window.MaxLon && bounds.maxLon >= window.MinLon &&
		bounds.minLat <= window.MaxLat && bounds.maxLat >= window.MinLat
}

func tinyTilesWaterwayResultLimit(zoom int) int {
	switch {
	case zoom >= 13:
		return 1_200
	case zoom >= 11:
		return 700
	case zoom >= 8:
		return 320
	default:
		return 140
	}
}

// handleTinyTilesWaterways serves only waterway features intersecting the
// current map viewport. Sending the complete sidecar to every browser would
// be especially wasteful for streams and ditches; MapLibre receives a small
// regular GeoJSON source instead and redraws it after pan/zoom changes.
func (s *server) handleTinyTilesWaterways(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	window, ok := offlineLabelsWindow(r.URL.Query().Get("bbox"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "bbox must be minLon,minLat,maxLon,maxLat")
		return
	}
	zoom := parseOfflineLabelZoom(r.URL.Query().Get("zoom"))
	if zoom < 7 {
		writeJSON(w, http.StatusOK, tinyTilesWaterwayGeoJSON{Type: "FeatureCollection", Features: []tinyTilesWaterwayGeoJSONItem{}})
		return
	}
	if tinyTilesWaterwayGridCellCount(window) > tinyTilesWaterwayMaxQueryGridCells {
		writeJSONError(w, http.StatusBadRequest, "bbox is too large for the local waterway layer")
		return
	}

	limit := tinyTilesWaterwayResultLimit(zoom)
	response := tinyTilesWaterwayGeoJSON{Type: "FeatureCollection", Features: make([]tinyTilesWaterwayGeoJSONItem, 0, min(limit, 64))}
	pointCount := 0
	s.tinyTilesMu.RLock()
	waterways := s.tinyTilesWaterways
	s.tinyTilesMu.RUnlock()
	for _, featureIndex := range waterways.featuresIn(window) {
		if r.Context().Err() != nil {
			return
		}
		feature := waterways.features[featureIndex]
		if zoom < feature.MinZoom || !feature.bounds.intersects(window) {
			continue
		}
		for _, coordinates := range clipTinyTilesWaterway(feature.Coordinates, window) {
			if r.Context().Err() != nil {
				return
			}
			if len(response.Features) >= limit || pointCount+len(coordinates) > tinyTilesWaterwayMaxPoints {
				response.Truncated = true
				break
			}
			response.Features = append(response.Features, tinyTilesWaterwayGeoJSONItem{
				Type:       "Feature",
				Properties: tinyTilesWaterwayProperties{Class: feature.Class, Name: feature.Name},
				Geometry:   tinyTilesWaterwayGeoJSONGeometry{Type: "LineString", Coordinates: coordinates},
			})
			pointCount += len(coordinates)
		}
		if response.Truncated {
			break
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, response)
}

// clipTinyTilesWaterway returns the visible line fragments for a viewport.
// Bounding-box indexing alone is not sufficient: a long river can pass through
// a viewport without any original OSM node lying inside it. Segment clipping
// both preserves that crossing and prevents unrelated far-away coordinates
// from being sent to the browser.
func clipTinyTilesWaterway(points [][2]float64, window osmmini.CoordWindow) [][][2]float64 {
	if len(points) < 2 {
		return nil
	}
	var clipped [][][2]float64
	var current [][2]float64
	finish := func() {
		if len(current) >= 2 {
			clipped = append(clipped, current)
		}
		current = nil
	}
	for index := 1; index < len(points); index++ {
		start, end, ok := clipTinyTilesWaterwaySegment(points[index-1], points[index], window)
		if !ok {
			finish()
			continue
		}
		if len(current) == 0 || current[len(current)-1] != start {
			current = append(current, start)
		}
		if current[len(current)-1] != end {
			current = append(current, end)
		}
		if !tinyTilesWaterwayPointInWindow(points[index], window) {
			finish()
		}
	}
	finish()
	return clipped
}

// clipTinyTilesWaterwaySegment applies Liang-Barsky clipping to a WGS84 line
// segment. It is compact, handles vertical/horizontal lines, and keeps the
// exact viewport-boundary intersections needed for a continuous river line.
func clipTinyTilesWaterwaySegment(start, end [2]float64, window osmmini.CoordWindow) ([2]float64, [2]float64, bool) {
	dx, dy := end[0]-start[0], end[1]-start[1]
	p := [4]float64{-dx, dx, -dy, dy}
	q := [4]float64{start[0] - window.MinLon, window.MaxLon - start[0], start[1] - window.MinLat, window.MaxLat - start[1]}
	u0, u1 := 0.0, 1.0
	for i := range p {
		if p[i] == 0 {
			if q[i] < 0 {
				return [2]float64{}, [2]float64{}, false
			}
			continue
		}
		ratio := q[i] / p[i]
		if p[i] < 0 {
			if ratio > u1 {
				return [2]float64{}, [2]float64{}, false
			}
			u0 = max(u0, ratio)
		} else {
			if ratio < u0 {
				return [2]float64{}, [2]float64{}, false
			}
			u1 = min(u1, ratio)
		}
	}
	return [2]float64{start[0] + u0*dx, start[1] + u0*dy}, [2]float64{start[0] + u1*dx, start[1] + u1*dy}, true
}

func tinyTilesWaterwayPointInWindow(point [2]float64, window osmmini.CoordWindow) bool {
	return point[0] >= window.MinLon && point[0] <= window.MaxLon && point[1] >= window.MinLat && point[1] <= window.MaxLat
}

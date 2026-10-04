package main

import (
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	osmmini "simonwaldherr.de/go/osmmini"
)

const tinyTilesBuildingMinZoom = 14
const tinyTilesBuildingCellSize = 0.01

type tinyTilesBuildingPropertiesData struct {
	Class        string  `json:"class"`
	Height       float64 `json:"render_height"`
	Base         float64 `json:"render_min_height"`
	HeightSource string  `json:"height_source"`
}

type tinyTilesBuilding struct {
	ID          int64                           `json:"id"`
	Properties  tinyTilesBuildingPropertiesData `json:"properties"`
	Coordinates [][][2]float64                  `json:"coordinates"`
	bounds      tinyTilesWaterwayBounds
}

type tinyTilesBuildingIndex struct {
	features []tinyTilesBuilding
	grid     *tinyTilesWaterwayIndex
}

// Size and nanosecond mtime identify an unchanged local source. The path
// prevents reuse when switching to an equally sized extract of another region.
func tinyTilesCompanionSource(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s:%d:%d", abs, info.Size(), info.ModTime().UnixNano()), nil
}

func tinyTilesBuildingTag(key string) bool {
	switch key {
	case "building", "building:part", "height", "building:height", "building:levels", "roof:height", "roof:levels", "min_height", "building:min_level":
		return true
	}
	return false
}

func tinyTilesIsBuilding(tags osmmini.Tags) bool {
	for _, key := range []string{"building", "building:part"} {
		value := strings.TrimSpace(tags[key])
		if value != "" && value != "no" {
			return true
		}
	}
	return false
}

// OSM lengths are metres by default; accept explicit m/ft and decimal commas.
// Invalid, negative and implausibly large values fall through to the next
// evidence source instead of hiding a building or drawing an enormous prism.
func tinyTilesBuildingNumber(raw string, length bool) (float64, bool) {
	s := strings.ToLower(strings.TrimSpace(raw))
	factor := 1.0
	if length {
		for _, unit := range []string{"meters", "metres", "meter", "metre", "m", "feet", "foot", "ft", "'"} {
			if strings.HasSuffix(s, unit) {
				if unit == "feet" || unit == "foot" || unit == "ft" || unit == "'" {
					factor = 0.3048
				}
				s = strings.TrimSpace(strings.TrimSuffix(s, unit))
				break
			}
		}
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", "."), 64)
	v *= factor
	limit := 400.0
	if length {
		limit = 1200
	}
	return v, err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= limit
}

func tinyTilesBuildingProperties(tags osmmini.Tags) tinyTilesBuildingPropertiesData {
	class := strings.TrimSpace(tags["building"])
	if class == "" || class == "no" {
		class = strings.TrimSpace(tags["building:part"])
	}
	p := tinyTilesBuildingPropertiesData{Class: class, Height: 8, HeightSource: "default"}
	switch class {
	case "garage", "garages", "shed", "hut", "carport", "roof", "greenhouse":
		p.Height = 3
	case "barn", "farm_auxiliary", "warehouse", "industrial":
		p.Height = 6
	}
	for _, key := range []string{"height", "building:height"} {
		if h, ok := tinyTilesBuildingNumber(tags[key], true); ok && h > 0 {
			p.Height, p.HeightSource = h, "height"
			break
		}
	}
	if p.HeightSource == "default" {
		if levels, ok := tinyTilesBuildingNumber(tags["building:levels"], false); ok && levels > 0 {
			roof, valid := tinyTilesBuildingNumber(tags["roof:height"], true)
			if !valid {
				roof = 0
				if levels, ok := tinyTilesBuildingNumber(tags["roof:levels"], false); ok {
					roof = levels * 3
				}
			}
			p.Height, p.HeightSource = math.Min(1200, levels*3+roof), "levels"
		}
	}
	if base, ok := tinyTilesBuildingNumber(tags["min_height"], true); ok {
		p.Base = base
	} else if levels, ok := tinyTilesBuildingNumber(tags["building:min_level"], false); ok {
		p.Base = levels * 3
	}
	// A guessed height needs to clear an explicitly elevated base. Explicit
	// heights remain authoritative; inconsistent bases fall back to ground.
	if p.Base >= p.Height {
		if p.HeightSource == "default" {
			p.Height = math.Min(1200, p.Base+p.Height)
		}
		if p.Base >= p.Height {
			p.Base = 0
		}
	}
	return p
}

func (b *tinyTilesBuilding) normalize() bool {
	if len(b.Coordinates) == 0 || !isFiniteBuildingHeight(b.Properties.Height) || b.Properties.Height <= 0 ||
		!isFiniteBuildingHeight(b.Properties.Base) || b.Properties.Base >= b.Properties.Height {
		return false
	}
	var points [][2]float64
	for _, ring := range b.Coordinates {
		if len(ring) < 4 || len(ring) > 10000 || ring[0] != ring[len(ring)-1] {
			return false
		}
		area := 0.0
		for i := 1; i < len(ring); i++ {
			area += (ring[i-1][0]-ring[0][0])*(ring[i][1]-ring[0][1]) - (ring[i][0]-ring[0][0])*(ring[i-1][1]-ring[0][1])
		}
		if area == 0 {
			return false
		}
		points = append(points, ring...)
	}
	proxy := tinyTilesWaterway{Coordinates: points}
	if !proxy.normalize() {
		return false
	}
	b.bounds = proxy.bounds
	return true
}

func isFiniteBuildingHeight(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 1200
}

func newTinyTilesBuildingIndex(features []tinyTilesBuilding) (*tinyTilesBuildingIndex, error) {
	// Reuse the spatial grid and bounded query machinery used by waterways.
	// Store bounds only in the proxy so geometry is retained once in memory.
	proxies := make([]tinyTilesWaterway, len(features))
	for i := range features {
		if !features[i].normalize() {
			return nil, fmt.Errorf("invalid building feature %d", features[i].ID)
		}
		proxies[i] = tinyTilesWaterway{ID: int64(i), bounds: features[i].bounds}
	}
	return &tinyTilesBuildingIndex{features: features, grid: newTinyTilesFeatureGrid(proxies, tinyTilesBuildingCellSize)}, nil
}

func (s *server) tinyTilesHasBuildingSidecar() bool {
	s.tinyTilesMu.RLock()
	defer s.tinyTilesMu.RUnlock()
	return s.tinyTilesWaterways != nil && s.tinyTilesWaterways.buildings != nil
}

func (s *server) tinyTilesBuildingCount() int {
	s.tinyTilesMu.RLock()
	defer s.tinyTilesMu.RUnlock()
	if s.tinyTilesWaterways == nil || s.tinyTilesWaterways.buildings == nil {
		return 0
	}
	return len(s.tinyTilesWaterways.buildings.features)
}

func (s *server) handleTinyTilesBuildings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	window, ok := offlineLabelsWindow(r.URL.Query().Get("bbox"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "bbox must be minLon,minLat,maxLon,maxLat")
		return
	}
	minX, minY := tinyTilesFeatureCell(window.MinLon, window.MinLat, tinyTilesBuildingCellSize)
	maxX, maxY := tinyTilesFeatureCell(window.MaxLon, window.MaxLat, tinyTilesBuildingCellSize)
	if int64(maxX-minX+1)*int64(maxY-minY+1) > tinyTilesWaterwayMaxQueryGridCells {
		writeJSONError(w, http.StatusBadRequest, "bbox is too large for the local building layer")
		return
	}
	type geometry struct {
		Type        string         `json:"type"`
		Coordinates [][][2]float64 `json:"coordinates"`
	}
	type item struct {
		Type       string                          `json:"type"`
		ID         int64                           `json:"id"`
		Properties tinyTilesBuildingPropertiesData `json:"properties"`
		Geometry   geometry                        `json:"geometry"`
	}
	response := struct {
		Type      string `json:"type"`
		Features  []item `json:"features"`
		Truncated bool   `json:"truncated,omitempty"`
	}{Type: "FeatureCollection", Features: make([]item, 0)}
	s.tinyTilesMu.RLock()
	var index *tinyTilesBuildingIndex
	if s.tinyTilesWaterways != nil {
		index = s.tinyTilesWaterways.buildings
	}
	s.tinyTilesMu.RUnlock()
	if parseOfflineLabelZoom(r.URL.Query().Get("zoom")) >= tinyTilesBuildingMinZoom && index != nil {
		points := 0
		for _, i := range index.grid.featuresIn(window) {
			if r.Context().Err() != nil {
				return
			}
			b := index.features[i]
			if !b.bounds.intersects(window) {
				continue
			}
			count := 0
			for _, ring := range b.Coordinates {
				count += len(ring)
			}
			if len(response.Features) >= 3000 || points+count > tinyTilesWaterwayMaxPoints {
				response.Truncated = true
				break
			}
			points += count
			response.Features = append(response.Features, item{Type: "Feature", ID: b.ID, Properties: b.Properties, Geometry: geometry{Type: "Polygon", Coordinates: b.Coordinates}})
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, response)
}

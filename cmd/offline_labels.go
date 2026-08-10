package main

import (
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"

	osmmini "simonwaldherr.de/go/osmmini"
)

// offlineMapLabel is deliberately a small, screen-oriented API. The local
// tinyTiles style contains geometry only; browser DOM labels let it remain
// fully offline without depending on MapLibre glyph PBFs or a font CDN.
type offlineMapLabel struct {
	Name string  `json:"name"`
	Lat  float64 `json:"lat"`
	Lon  float64 `json:"lon"`
	Kind string  `json:"kind"`
	Rank int     `json:"rank"`
}

func (s *server) handleOfflineLabels(w http.ResponseWriter, r *http.Request) {
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
	labels := make([]offlineMapLabel, 0, 80)

	// Town and locality labels become useful before individual street labels.
	// The POI index is built asynchronously, so an empty result during startup
	// simply means the next map movement will populate these labels.
	if zoom >= 7 {
		labels = append(labels, s.placeLabels(window, placeLabelLimit(zoom))...)
	}
	if zoom >= 12 && s.router != nil {
		for _, label := range s.router.StreetLabels(window, streetLabelLimit(zoom)) {
			labels = append(labels, offlineMapLabel{
				Name: label.Name, Lat: label.Coord.Lat, Lon: label.Coord.Lon,
				Kind: "road", Rank: 1,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"labels": labels})
}

func offlineLabelsWindow(value string) (osmmini.CoordWindow, bool) {
	parts := strings.Split(value, ",")
	if len(parts) != 4 {
		return osmmini.CoordWindow{}, false
	}
	values := [4]float64{}
	for i, part := range parts {
		parsed, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return osmmini.CoordWindow{}, false
		}
		values[i] = parsed
	}
	window := osmmini.CoordWindow{MinLon: values[0], MinLat: values[1], MaxLon: values[2], MaxLat: values[3]}
	return window, window.Valid()
}

func parseOfflineLabelZoom(raw string) int {
	zoom, err := strconv.Atoi(raw)
	if err != nil || zoom < 0 {
		return 10
	}
	if zoom > 22 {
		return 22
	}
	return zoom
}

func placeLabelLimit(zoom int) int {
	switch {
	case zoom >= 13:
		return 36
	case zoom >= 10:
		return 24
	default:
		return 14
	}
}

func streetLabelLimit(zoom int) int {
	if zoom >= 15 {
		return 56
	}
	return 32
}

func (s *server) placeLabels(window osmmini.CoordWindow, limit int) []offlineMapLabel {
	if limit <= 0 {
		return nil
	}
	labels := make([]offlineMapLabel, 0, limit)
	s.poiMu.RLock()
	for _, node := range s.poiTaggedNodes {
		name := strings.TrimSpace(node.Tags["name"])
		rank := placeLabelRank(node.Tags["place"])
		coord := osmmini.Coord{Lat: node.Lat, Lon: node.Lon}
		if name == "" || rank == 0 || !window.Contains(coord) {
			continue
		}
		labels = append(labels, offlineMapLabel{Name: name, Lat: coord.Lat, Lon: coord.Lon, Kind: "place", Rank: rank})
	}
	s.poiMu.RUnlock()
	slices.SortFunc(labels, func(a, b offlineMapLabel) int {
		if a.Rank != b.Rank {
			return b.Rank - a.Rank
		}
		return strings.Compare(a.Name, b.Name)
	})
	if len(labels) > limit {
		labels = labels[:limit]
	}
	return labels
}

func placeLabelRank(place string) int {
	switch strings.ToLower(strings.TrimSpace(place)) {
	case "city":
		return 100
	case "town":
		return 80
	case "village":
		return 60
	case "suburb", "quarter", "neighbourhood":
		return 45
	case "hamlet":
		return 30
	case "locality", "isolated_dwelling":
		return 15
	default:
		return 0
	}
}

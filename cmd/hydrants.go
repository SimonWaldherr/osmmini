package main

import (
	"net/http"
	"strings"

	osmmini "simonwaldherr.de/go/osmmini"
)

// hydrant is a small, screen-oriented view of an emergency=fire_hydrant node,
// used by the "Hydranten anzeigen" map overlay (BOS/Einsatzmodus).
type hydrant struct {
	ID   int64   `json:"id"`
	Lat  float64 `json:"lat"`
	Lon  float64 `json:"lon"`
	Name string  `json:"name,omitempty"`
	// Type mirrors OSM's fire_hydrant:type (underground/pillar/wall/pond),
	// falling back to a best-effort guess from the German name when the tag
	// is missing, since much of the source data only names the hydrant.
	Type string `json:"type,omitempty"`
}

const hydrantResultLimit = 500

func (s *server) handleHydrants(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	window, ok := offlineLabelsWindow(r.URL.Query().Get("bbox"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "bbox must be minLon,minLat,maxLon,maxLat")
		return
	}
	limit := parseLimit(r.URL.Query().Get("limit"), hydrantResultLimit, 1, 2000)

	hydrants := make([]hydrant, 0, 64)
	s.poiMu.RLock()
	for _, node := range s.poiTaggedNodes {
		if node.Tags["emergency"] != "fire_hydrant" {
			continue
		}
		coord := osmmini.Coord{Lat: node.Lat, Lon: node.Lon}
		if !window.Contains(coord) {
			continue
		}
		hydrants = append(hydrants, hydrant{
			ID:   node.ID,
			Lat:  node.Lat,
			Lon:  node.Lon,
			Name: strings.TrimSpace(node.Tags["name"]),
			Type: hydrantType(node.Tags),
		})
		if len(hydrants) >= limit {
			break
		}
	}
	s.poiMu.RUnlock()

	writeJSON(w, http.StatusOK, map[string]any{"hydrants": hydrants})
}

func hydrantType(tags osmmini.Tags) string {
	if t := strings.TrimSpace(tags["fire_hydrant:type"]); t != "" {
		return t
	}
	name := strings.ToLower(tags["name"])
	switch {
	case strings.Contains(name, "unterflur"):
		return "underground"
	case strings.Contains(name, "überflur"), strings.Contains(name, "ueberflur"):
		return "pillar"
	case strings.Contains(name, "wand"):
		return "wall"
	default:
		return ""
	}
}

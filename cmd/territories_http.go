package main

import (
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	osmmini "simonwaldherr.de/go/osmmini"
)

// loadTerritories loads every top-level *.geojson file in dir as a named
// layer. A missing directory is deliberately harmless: territory overlays
// are optional for the main map server.
func (s *server) loadTerritories(dir string) {
	store := osmmini.NewTerritoryStore()
	rawByLayer := make(map[string][]byte)

	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("warning: read territories directory %s: %v", dir, err)
		}
		s.territoriesMu.Lock()
		s.territories = store
		s.territoryRaw = rawByLayer
		s.territoriesMu.Unlock()
		return
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".geojson") {
			continue
		}
		layer := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if layer == "" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			log.Printf("warning: read territory layer %s: %v", path, err)
			continue
		}
		territories, err := osmmini.ParseTerritoriesGeoJSON(raw)
		if err != nil {
			log.Printf("warning: load territory layer %s: %v", path, err)
			continue
		}
		// Postal-code groups can contain very detailed shared boundary rings.
		// They are map/search overlays, so skip the otherwise useful but costly
		// all-pairs neighbour calculation performed for ordinary territory layers.
		var loadErr error
		if isPostalTerritoryLayer(layer) {
			loadErr = store.LoadLayerTerritoriesWithoutNeighbors(layer, territories)
		} else {
			loadErr = store.LoadLayerTerritories(layer, territories)
		}
		if loadErr != nil {
			log.Printf("warning: load territory layer %s: %v", path, loadErr)
			continue
		}
		rawByLayer[layer] = raw
	}

	layers := store.Layers()
	s.territoriesMu.Lock()
	s.territories = store
	s.territoryRaw = rawByLayer
	s.territoriesMu.Unlock()
	if len(layers) > 0 {
		log.Printf("Loaded %d territory layer(s): %s", len(layers), strings.Join(layers, ", "))
	}
}

func isPostalTerritoryLayer(layer string) bool {
	if len(layer) != 4 || !strings.HasPrefix(strings.ToLower(layer), "plz") {
		return false
	}
	return layer[3] >= '1' && layer[3] <= '5'
}

type territoryLayerSummary struct {
	ID          string `json:"id"`
	Territories int    `json:"territories"`
}

// handleTerritoriesList returns metadata for all territory layers loaded at
// startup. The endpoint is intentionally read-only; update the GeoJSON files
// and restart the server to publish a new territory set.
func (s *server) handleTerritoriesList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	layers := []territoryLayerSummary{}
	s.territoriesMu.RLock()
	store := s.territories
	if store != nil {
		for _, layer := range store.Layers() {
			layers = append(layers, territoryLayerSummary{
				ID:          layer,
				Territories: len(store.Territories(layer)),
			})
		}
	}
	s.territoriesMu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]any{"layers": layers})
}

// handleTerritoriesLayer returns the original GeoJSON for one named layer.
func (s *server) handleTerritoriesLayer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	layer := strings.TrimPrefix(r.URL.Path, "/api/v1/territories/")
	if layer == "" || strings.Contains(layer, "/") {
		writeJSONError(w, http.StatusNotFound, "territory layer not found")
		return
	}
	s.territoriesMu.RLock()
	raw, ok := s.territoryRaw[layer]
	if ok {
		raw = append([]byte(nil), raw...)
	}
	s.territoriesMu.RUnlock()
	if !ok {
		writeJSONError(w, http.StatusNotFound, "territory layer not found")
		return
	}
	w.Header().Set("Content-Type", "application/geo+json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(raw)
}

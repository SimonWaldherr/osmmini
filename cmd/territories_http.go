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
	s.territories = osmmini.NewTerritoryStore()
	s.territoryRaw = make(map[string][]byte)

	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("warning: read territories directory %s: %v", dir, err)
		}
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
		if err := s.territories.LoadLayerTerritories(layer, territories); err != nil {
			log.Printf("warning: load territory layer %s: %v", path, err)
			continue
		}
		s.territoryRaw[layer] = raw
	}

	layers := s.territories.Layers()
	if len(layers) > 0 {
		log.Printf("Loaded %d territory layer(s): %s", len(layers), strings.Join(layers, ", "))
	}
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
	if s.territories != nil {
		for _, layer := range s.territories.Layers() {
			layers = append(layers, territoryLayerSummary{
				ID:          layer,
				Territories: len(s.territories.Territories(layer)),
			})
		}
	}
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
	raw, ok := s.territoryRaw[layer]
	if !ok {
		writeJSONError(w, http.StatusNotFound, "territory layer not found")
		return
	}
	w.Header().Set("Content-Type", "application/geo+json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(raw)
}

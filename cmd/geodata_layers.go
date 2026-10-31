package main

import (
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	osmmini "simonwaldherr.de/go/osmmini/core"
)

// maxGeodataImportBytes bounds a single GIS file upload. Shapefile ZIPs and
// dense KML exports can be sizeable, so this is well above the 4 MiB used
// for the fire-stations CSV import.
const maxGeodataImportBytes = 64 << 20

var geodataLayerNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// loadImportedLayers loads every top-level *.geojson file in dir as a named
// imported layer, mirroring server.loadTerritories (cmd/territories_http.go)
// -- but ImportedLayerStore accepts any geometry type, not just
// Polygon/MultiPolygon. A missing directory is harmless: imported layers
// are optional.
func (s *server) loadImportedLayers(dir string) {
	store := osmmini.NewImportedLayerStore()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("warning: read imported-layers directory %s: %v", dir, err)
		}
		s.importedLayersMu.Lock()
		s.importedLayers = store
		s.importedLayersMu.Unlock()
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".geojson") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if name == "" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			log.Printf("warning: read imported layer %s: %v", path, err)
			continue
		}
		if _, err := store.LoadLayer(name, raw); err != nil {
			log.Printf("warning: load imported layer %s: %v", path, err)
			continue
		}
	}
	layers := store.Layers()
	s.importedLayersMu.Lock()
	s.importedLayers = store
	s.importedLayersMu.Unlock()
	if len(layers) > 0 {
		names := make([]string, len(layers))
		for i, l := range layers {
			names[i] = l.Name
		}
		log.Printf("Loaded %d imported GIS layer(s): %s", len(layers), strings.Join(names, ", "))
	}
}

// syncPolygonLayersToTerritories mirrors every polygon-only imported layer
// into -territories-dir as <name>.geojson (removing a stale mirror for a
// layer that is no longer polygon-only), then reloads territories the same
// way publishPostalTerritories does for generated PLZ layers
// (cmd/territories_postal.go) -- so a polygon-shaped import becomes usable
// for dispatch/routing-cost lookups immediately, reusing TerritoryStore's
// existing load/replace logic instead of duplicating it. A layer name must
// not collide with a manually placed territory file of the same name.
func (s *server) syncPolygonLayersToTerritories() {
	s.importedLayersMu.RLock()
	layers := s.importedLayers.Layers()
	s.importedLayersMu.RUnlock()

	if err := os.MkdirAll(s.territoriesDir, 0o755); err != nil {
		log.Printf("warning: create territories directory %s: %v", s.territoriesDir, err)
		return
	}
	polygonNames := make(map[string]bool, len(layers))
	for _, layer := range layers {
		if !layer.IsPolygonOnly() {
			continue
		}
		polygonNames[layer.Name] = true
		path := filepath.Join(s.territoriesDir, layer.Name+".geojson")
		if err := writeFileAtomically(path, layer.GeoJSON); err != nil {
			log.Printf("warning: mirror imported layer %s into territories: %v", layer.Name, err)
		}
	}
	// Remove a mirror left behind by a layer that used to be polygon-only
	// and was re-imported as something else (or removed).
	s.importedLayersMu.RLock()
	mirrored := s.importedLayerMirrors
	s.importedLayersMu.RUnlock()
	for name := range mirrored {
		if polygonNames[name] {
			continue
		}
		_ = os.Remove(filepath.Join(s.territoriesDir, name+".geojson"))
	}
	s.importedLayersMu.Lock()
	s.importedLayerMirrors = polygonNames
	s.importedLayersMu.Unlock()

	s.loadTerritories(s.territoriesDir)
}

type geodataLayerSummary struct {
	ID            string   `json:"id"`
	Features      int      `json:"features"`
	GeometryTypes []string `json:"geometry_types"`
	Territory     bool     `json:"territory"`
}

// handleGeodataLayers lists every imported layer for the "Importierte
// Layer" picker; layers whose geometry is polygon-only also carry
// territory:true, since they are additionally reachable via
// /api/v1/territories/{layer}.
func (s *server) handleGeodataLayers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	s.importedLayersMu.RLock()
	store := s.importedLayers
	s.importedLayersMu.RUnlock()
	out := []geodataLayerSummary{}
	if store != nil {
		for _, l := range store.Layers() {
			out = append(out, geodataLayerSummary{ID: l.Name, Features: l.FeatureCount, GeometryTypes: l.GeometryTypes, Territory: l.IsPolygonOnly()})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"layers": out})
}

// handleGeodataLayerRouter dispatches /api/v1/geodata/layers/{name}: GET
// returns the raw GeoJSON (same contract as handleTerritoriesLayer), DELETE
// removes the layer. One registration per prefix, method-dispatched inside
// -- the convention every other prefix handler in this file/package uses.
func (s *server) handleGeodataLayerRouter(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/v1/geodata/layers/")
	if name == "" || strings.Contains(name, "/") {
		writeJSONError(w, http.StatusNotFound, "layer not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.importedLayersMu.RLock()
		var layer *osmmini.ImportedLayer
		if s.importedLayers != nil {
			layer = s.importedLayers.Layer(name)
		}
		s.importedLayersMu.RUnlock()
		if layer == nil {
			writeJSONError(w, http.StatusNotFound, "layer not found")
			return
		}
		w.Header().Set("Content-Type", "application/geo+json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(layer.GeoJSON)
	case http.MethodDelete:
		if !s.requireSettingsAdmin(w, r) {
			return
		}
		if !geodataLayerNamePattern.MatchString(name) {
			writeJSONError(w, http.StatusNotFound, "layer not found")
			return
		}
		_ = os.Remove(filepath.Join(s.importedLayersDir, name+".geojson"))
		s.loadImportedLayers(s.importedLayersDir)
		s.syncPolygonLayersToTerritories()
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodDelete)
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleGeodataImport accepts a raw-body GIS file upload
// (?layer=NAME&format=geojson|kml|kmz|shp, format defaults to the
// extension implied by an optional ?filename=), converts it to GeoJSON via
// importVectorToGeoJSON, persists it under -imported-layers-dir, reloads
// the imported-layer store, and mirrors it into territories when it is
// polygon-only. Follows the same raw-body + MaxBytesReader + admin-token
// convention as the fire-stations CSV import (cmd/fire_stations.go).
func (s *server) handleGeodataImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.requireSettingsAdmin(w, r) {
		return
	}
	layerName := strings.TrimSpace(r.URL.Query().Get("layer"))
	if !geodataLayerNamePattern.MatchString(layerName) {
		writeJSONError(w, http.StatusBadRequest, "?layer must be 1-64 letters, digits, - or _")
		return
	}
	format := strings.TrimSpace(r.URL.Query().Get("format"))
	if format == "" || format == "auto" {
		format = vectorFormatFromFilename(r.URL.Query().Get("filename"))
	}
	if format == "" {
		writeJSONError(w, http.StatusBadRequest, "specify ?format=geojson|kml|kmz|shp (or ?filename= so it can be derived)")
		return
	}

	body := http.MaxBytesReader(w, r.Body, maxGeodataImportBytes)
	geojson, err := importVectorToGeoJSON(r.Context(), format, body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	layer, err := osmmini.ParseImportedLayer(layerName, geojson)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "converted data is not valid GeoJSON: "+err.Error())
		return
	}
	if err := os.MkdirAll(s.importedLayersDir, 0o755); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "create imported-layers directory: "+err.Error())
		return
	}
	path := filepath.Join(s.importedLayersDir, layerName+".geojson")
	if err := writeFileAtomically(path, layer.GeoJSON); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "save imported layer: "+err.Error())
		return
	}
	s.loadImportedLayers(s.importedLayersDir)
	s.syncPolygonLayersToTerritories()
	writeJSON(w, http.StatusOK, geodataLayerSummary{ID: layer.Name, Features: layer.FeatureCount, GeometryTypes: layer.GeometryTypes, Territory: layer.IsPolygonOnly()})
}

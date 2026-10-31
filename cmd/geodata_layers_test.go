package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	osmmini "simonwaldherr.de/go/osmmini/core"
)

const geodataSamplePointFC = `{"type":"FeatureCollection","features":[
  {"type":"Feature","properties":{"name":"Hydrant 1"},"geometry":{"type":"Point","coordinates":[12.0,48.0]}}
]}`

const geodataSamplePolygonFC = `{"type":"FeatureCollection","features":[
  {"type":"Feature","properties":{"id":"zone-a"},"geometry":{"type":"Polygon","coordinates":[[[12.0,48.0],[12.1,48.0],[12.1,48.1],[12.0,48.1],[12.0,48.0]]]}}
]}`

func newGeodataTestServer(t *testing.T) *server {
	t.Helper()
	return &server{
		importedLayersDir: filepath.Join(t.TempDir(), "imported-layers"),
		importedLayers:    osmmini.NewImportedLayerStore(),
		territoriesDir:    filepath.Join(t.TempDir(), "territories"),
		territories:       osmmini.NewTerritoryStore(),
		territoryRaw:      map[string][]byte{},
	}
}

func TestLoadImportedLayersFromDirectory(t *testing.T) {
	s := newGeodataTestServer(t)
	if err := os.MkdirAll(s.importedLayersDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.importedLayersDir, "hydrants.geojson"), []byte(geodataSamplePointFC), 0o644); err != nil {
		t.Fatal(err)
	}

	s.loadImportedLayers(s.importedLayersDir)

	layer := s.importedLayers.Layer("hydrants")
	if layer == nil {
		t.Fatal("expected the hydrants layer to be loaded")
	}
	if layer.FeatureCount != 1 {
		t.Fatalf("FeatureCount = %d, want 1", layer.FeatureCount)
	}
}

func TestSyncPolygonLayersToTerritoriesRegistersAndUnregisters(t *testing.T) {
	s := newGeodataTestServer(t)
	if _, err := s.importedLayers.LoadLayer("zones", []byte(geodataSamplePolygonFC)); err != nil {
		t.Fatalf("LoadLayer: %v", err)
	}

	s.syncPolygonLayersToTerritories()

	if got := s.territories.FindTerritoryByID("zones", "zone-a"); got == nil {
		t.Fatal("expected the polygon-only imported layer to be registered as a territory layer")
	}
	if _, err := os.Stat(filepath.Join(s.territoriesDir, "zones.geojson")); err != nil {
		t.Fatalf("expected a mirrored territory file: %v", err)
	}

	// Re-importing the same name as a non-polygon layer must remove the
	// stale mirror and unregister it from TerritoryStore.
	if _, err := s.importedLayers.LoadLayer("zones", []byte(geodataSamplePointFC)); err != nil {
		t.Fatalf("LoadLayer (replace): %v", err)
	}
	s.syncPolygonLayersToTerritories()

	if got := s.territories.FindTerritoryByID("zones", "zone-a"); got != nil {
		t.Fatal("a layer that stopped being polygon-only must be unregistered")
	}
	if _, err := os.Stat(filepath.Join(s.territoriesDir, "zones.geojson")); !os.IsNotExist(err) {
		t.Fatalf("expected the stale mirror file to be removed, stat err = %v", err)
	}
}

func TestHandleGeodataImportListGetDelete(t *testing.T) {
	s := newGeodataTestServer(t)

	// Import a point layer (geojson passthrough).
	importReq := httptest.NewRequest(http.MethodPost, "/api/v1/geodata/import?layer=hydrants&format=geojson", bytes.NewReader([]byte(geodataSamplePointFC)))
	importRec := httptest.NewRecorder()
	s.handleGeodataImport(importRec, importReq)
	if importRec.Code != http.StatusOK {
		t.Fatalf("import status = %d: %s", importRec.Code, importRec.Body.String())
	}
	var summary geodataLayerSummary
	if err := json.Unmarshal(importRec.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.ID != "hydrants" || summary.Territory {
		t.Fatalf("summary = %+v, want id=hydrants, territory=false", summary)
	}

	// Reject a bad layer name.
	badName := httptest.NewRequest(http.MethodPost, "/api/v1/geodata/import?layer=..&format=geojson", bytes.NewReader([]byte(geodataSamplePointFC)))
	badNameRec := httptest.NewRecorder()
	s.handleGeodataImport(badNameRec, badName)
	if badNameRec.Code != http.StatusBadRequest {
		t.Fatalf("import with invalid layer name = %d, want 400", badNameRec.Code)
	}

	// List.
	listRec := httptest.NewRecorder()
	s.handleGeodataLayers(listRec, httptest.NewRequest(http.MethodGet, "/api/v1/geodata/layers", nil))
	if listRec.Code != http.StatusOK || !bytes.Contains(listRec.Body.Bytes(), []byte("hydrants")) {
		t.Fatalf("list status=%d body=%s", listRec.Code, listRec.Body.String())
	}

	// Get raw GeoJSON.
	getRec := httptest.NewRecorder()
	s.handleGeodataLayerRouter(getRec, httptest.NewRequest(http.MethodGet, "/api/v1/geodata/layers/hydrants", nil))
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status = %d: %s", getRec.Code, getRec.Body.String())
	}
	var fc map[string]any
	if err := json.Unmarshal(getRec.Body.Bytes(), &fc); err != nil {
		t.Fatalf("layer body is not valid GeoJSON: %v", err)
	}

	// Delete: open by default (requireSettingsAdmin, not the fail-closed
	// confidential-objects gate), matching the fire-stations convention.
	delRec := httptest.NewRecorder()
	s.handleGeodataLayerRouter(delRec, httptest.NewRequest(http.MethodDelete, "/api/v1/geodata/layers/hydrants", nil))
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d: %s", delRec.Code, delRec.Body.String())
	}
	if s.importedLayers.Layer("hydrants") != nil {
		t.Fatal("layer still present after delete")
	}

	missingRec := httptest.NewRecorder()
	s.handleGeodataLayerRouter(missingRec, httptest.NewRequest(http.MethodGet, "/api/v1/geodata/layers/hydrants", nil))
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("get after delete = %d, want 404", missingRec.Code)
	}
}

func TestHandleGeodataImportRequiresAdminTokenWhenConfigured(t *testing.T) {
	s := newGeodataTestServer(t)
	s.adminToken = "secret-token"

	unauthorized := httptest.NewRequest(http.MethodPost, "/api/v1/geodata/import?layer=hydrants&format=geojson", bytes.NewReader([]byte(geodataSamplePointFC)))
	unauthorizedRec := httptest.NewRecorder()
	s.handleGeodataImport(unauthorizedRec, unauthorized)
	if unauthorizedRec.Code != http.StatusUnauthorized {
		t.Fatalf("import without a token = %d, want 401", unauthorizedRec.Code)
	}

	authorized := httptest.NewRequest(http.MethodPost, "/api/v1/geodata/import?layer=hydrants&format=geojson", bytes.NewReader([]byte(geodataSamplePointFC)))
	authorized.Header.Set("Authorization", "Bearer secret-token")
	authorizedRec := httptest.NewRecorder()
	s.handleGeodataImport(authorizedRec, authorized)
	if authorizedRec.Code != http.StatusOK {
		t.Fatalf("import with a valid token = %d: %s", authorizedRec.Code, authorizedRec.Body.String())
	}
}

func TestHandleGeodataImportRejectsUnknownFormat(t *testing.T) {
	s := newGeodataTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/geodata/import?layer=hydrants&format=docx", bytes.NewReader([]byte("x")))
	rec := httptest.NewRecorder()
	s.handleGeodataImport(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("import with an unsupported format = %d, want 400", rec.Code)
	}
}

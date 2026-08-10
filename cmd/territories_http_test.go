package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

const territoryGeoJSONFixture = `{"type":"FeatureCollection","features":[{"type":"Feature","properties":{"territory_id":"north"},"geometry":{"type":"Polygon","coordinates":[[[12,48],[13,48],[13,49],[12,49],[12,48]]]}}]}`

func TestTerritoryHTTPHandlers(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "delivery.geojson"), []byte(territoryGeoJSONFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("not GeoJSON"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := &server{}
	srv.loadTerritories(dir)

	listResponse := httptest.NewRecorder()
	srv.handleTerritoriesList(listResponse, httptest.NewRequest(http.MethodGet, "/api/v1/territories", nil))
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", listResponse.Code, listResponse.Body.String())
	}
	var listed struct {
		Layers []territoryLayerSummary `json:"layers"`
	}
	if err := json.Unmarshal(listResponse.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Layers) != 1 || listed.Layers[0] != (territoryLayerSummary{ID: "delivery", Territories: 1}) {
		t.Fatalf("layers = %#v", listed.Layers)
	}

	layerResponse := httptest.NewRecorder()
	srv.handleTerritoriesLayer(layerResponse, httptest.NewRequest(http.MethodGet, "/api/v1/territories/delivery", nil))
	if layerResponse.Code != http.StatusOK {
		t.Fatalf("layer status = %d: %s", layerResponse.Code, layerResponse.Body.String())
	}
	if got := layerResponse.Header().Get("Content-Type"); got != "application/geo+json; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}
	if got := layerResponse.Body.String(); got != territoryGeoJSONFixture {
		t.Fatalf("layer payload = %q", got)
	}

	missingResponse := httptest.NewRecorder()
	srv.handleTerritoriesLayer(missingResponse, httptest.NewRequest(http.MethodGet, "/api/v1/territories/missing", nil))
	if missingResponse.Code != http.StatusNotFound {
		t.Fatalf("missing layer status = %d", missingResponse.Code)
	}
}

func TestLoadTerritoriesMissingDirectoryIsEmpty(t *testing.T) {
	srv := &server{}
	srv.loadTerritories(filepath.Join(t.TempDir(), "missing"))
	if srv.territories == nil || srv.territoryRaw == nil {
		t.Fatal("missing territory directory did not initialise server state")
	}
	if got := srv.territories.Layers(); len(got) != 0 {
		t.Fatalf("layers = %v, want none", got)
	}
}

func TestIsPostalTerritoryLayer(t *testing.T) {
	for _, layer := range []string{"plz1", "plz3", "PLZ5"} {
		if !isPostalTerritoryLayer(layer) {
			t.Errorf("isPostalTerritoryLayer(%q) = false", layer)
		}
	}
	for _, layer := range []string{"plz", "plz0", "plz6", "postal", "delivery"} {
		if isPostalTerritoryLayer(layer) {
			t.Errorf("isPostalTerritoryLayer(%q) = true", layer)
		}
	}
}

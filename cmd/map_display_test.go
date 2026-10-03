package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestMapDisplayLegacyDefaultsAndRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	legacy := DefaultSettings(t.TempDir(), "")
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	delete(payload, "map_display")
	data, _ = json.Marshal(payload)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	store := NewSettingsStore(path, Settings{})
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	display := store.Get().MapDisplay
	if display.Renderer != "micromap" || display.MicroMap.Quality != "balanced" || display.MicroMap.View != "flat" {
		t.Fatalf("legacy defaults: %+v", display)
	}
	settings := store.Get()
	settings.MapDisplay.Renderer = "maplibre"
	settings.MapDisplay.MicroMap = MicroMapSettings{Quality: "economy", View: "steep", Buildings: "canvas", Sky: "off", Interaction: "simple"}
	srv := &server{settings: store, tiles: NewTileCache(settings.Tiles)}
	defer srv.tiles.Close()
	put := func(payload []byte) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.handleSettings(rec, httptest.NewRequest(http.MethodPut, "/api/v1/settings", bytes.NewReader(payload)))
		return rec
	}
	data, _ = json.Marshal(settings)
	if rec := put(data); rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body.String())
	}
	loaded := NewSettingsStore(path, Settings{})
	if err := loaded.Load(); err != nil {
		t.Fatal(err)
	}
	if loaded.Get().MapDisplay != settings.MapDisplay {
		t.Fatalf("reload lost settings: %+v", loaded.Get().MapDisplay)
	}
	// An older client saving routing settings must retain the renderer choices.
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	delete(payload, "map_display")
	data, _ = json.Marshal(payload)
	if rec := put(data); rec.Code != http.StatusOK {
		t.Fatalf("legacy PUT = %d", rec.Code)
	}
	if store.Get().MapDisplay != settings.MapDisplay {
		t.Fatal("older client reset renderer choices")
	}
	settings.MapDisplay.MicroMap.Quality = "unbounded"
	data, _ = json.Marshal(settings)
	if rec := put(data); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid quality = %d", rec.Code)
	}
	if store.Get().MapDisplay.MicroMap.Quality != "economy" {
		t.Fatal("invalid configuration was applied")
	}
}

func TestMapDisplayRejectsUnsupportedValues(t *testing.T) {
	for _, field := range []string{"renderer", "quality", "view", "buildings", "sky", "interaction"} {
		t.Run(field, func(t *testing.T) {
			v := MapDisplaySettings{}
			normalizeMapDisplay(&v)
			switch field {
			case "renderer":
				v.Renderer = "other"
			case "quality":
				v.MicroMap.Quality = "other"
			case "view":
				v.MicroMap.View = "other"
			case "buildings":
				v.MicroMap.Buildings = "other"
			case "sky":
				v.MicroMap.Sky = "other"
			case "interaction":
				v.MicroMap.Interaction = "other"
			}
			if validateMapDisplay(v) == nil {
				t.Fatal("invalid value accepted")
			}
		})
	}
}

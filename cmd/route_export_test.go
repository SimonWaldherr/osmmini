package main

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	osmmini "simonwaldherr.de/go/osmmini"
)

func routeExportFixture(t *testing.T) *server {
	t.Helper()
	coords := map[int64]osmmini.Coord{
		1: {Lat: 48.000, Lon: 12.000},
		2: {Lat: 48.001, Lon: 12.000},
		3: {Lat: 48.002, Lon: 12.000},
	}
	edge := func(to int64) osmmini.Edge {
		return osmmini.Edge{To: to, DistM: 111, SpeedKph: 50, HwyType: "residential"}
	}
	adj := map[int64][]osmmini.Edge{1: {edge(2)}, 2: {edge(1), edge(3)}, 3: {edge(2)}}
	cache := newRouteCache(time.Minute, 16)
	t.Cleanup(cache.Close)
	return &server{
		router:     osmmini.NewRouterFromGraph(coords, adj),
		settings:   NewSettingsStore(filepath.Join(t.TempDir(), "settings.json"), DefaultSettings(t.TempDir(), "")),
		routeCache: cache,
	}
}

func postRoute(t *testing.T, s *server, query string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"from":{"lat":48.0,"lon":12.0},"to":{"lat":48.002,"lon":12.0}}`
	rec := httptest.NewRecorder()
	s.handleRoute(rec, httptest.NewRequest(http.MethodPost, "/api/v1/route"+query, strings.NewReader(body)))
	return rec
}

func TestRouteExportGPX(t *testing.T) {
	s := routeExportFixture(t)
	rec := postRoute(t, s, "?format=gpx")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/gpx+xml") {
		t.Fatalf("content type %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, ".gpx") {
		t.Fatalf("content disposition %q", cd)
	}
	var doc gpxDoc
	if err := xml.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("invalid GPX: %v\n%s", err, rec.Body)
	}
	if doc.Version != "1.1" || len(doc.Wpts) != 2 || len(doc.Trk.Seg.Points) != 3 {
		t.Fatalf("unexpected GPX: %+v", doc)
	}
	if doc.Wpts[0].Type != "start" || doc.Wpts[1].Type != "end" {
		t.Fatalf("waypoint roles: %+v", doc.Wpts)
	}
	if doc.Trk.Seg.Points[2].Lat != "48.0020000" {
		t.Fatalf("last track point: %+v", doc.Trk.Seg.Points[2])
	}

	// A cache hit must produce the same export format.
	if again := postRoute(t, s, "?format=gpx"); !strings.HasPrefix(again.Header().Get("Content-Type"), "application/gpx+xml") {
		t.Fatalf("cached response is not GPX: %s", again.Header().Get("Content-Type"))
	}
}

func TestRouteExportGeoJSON(t *testing.T) {
	rec := postRoute(t, routeExportFixture(t), "?format=geojson")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var fc struct {
		Type     string `json:"type"`
		Features []struct {
			Geometry struct {
				Type        string          `json:"type"`
				Coordinates json.RawMessage `json:"coordinates"`
			} `json:"geometry"`
			Properties map[string]any `json:"properties"`
		} `json:"features"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &fc); err != nil {
		t.Fatal(err)
	}
	if fc.Type != "FeatureCollection" || len(fc.Features) != 3 {
		t.Fatalf("unexpected collection: %s", rec.Body)
	}
	if fc.Features[0].Geometry.Type != "LineString" || fc.Features[1].Properties["kind"] != "start" {
		t.Fatalf("unexpected features: %s", rec.Body)
	}
	var line [][2]float64
	if err := json.Unmarshal(fc.Features[0].Geometry.Coordinates, &line); err != nil || len(line) != 3 || line[0] != [2]float64{12, 48} {
		t.Fatalf("line must use [lon, lat]: %v %v", line, err)
	}
}

func TestRouteExportRejectsUnknownFormat(t *testing.T) {
	if rec := postRoute(t, routeExportFixture(t), "?format=kml"); rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if rec := postRoute(t, routeExportFixture(t), ""); rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("default must stay JSON: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestRouteExportGPXEscapesNames(t *testing.T) {
	var b strings.Builder
	exp := routeExport{Name: `A & B <"x">`, Waypoints: []routeExportWaypoint{{Role: "start", Name: "<Start>"}}}
	if err := encodeRouteGPX(&b, exp); err != nil {
		t.Fatal(err)
	}
	var doc gpxDoc
	if err := xml.Unmarshal([]byte(b.String()), &doc); err != nil || doc.Metadata.Name != exp.Name || doc.Wpts[0].Name != "<Start>" {
		t.Fatalf("names not round-tripped: %v %+v", err, doc)
	}
}

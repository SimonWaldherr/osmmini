package main

import (
	"bytes"
	"encoding/json"
	"html/template"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"testing"

	osmmini "simonwaldherr.de/go/osmmini"
)

func encodedPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 0x20, G: 0x60, B: 0xa0, A: 0xff})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func encodedJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 0xa0, G: 0x60, B: 0x20, A: 0xff})
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestTileCacheSeparatesSourcesAndPreservesImageType(t *testing.T) {
	pngTile := encodedPNG(t)
	jpegTile := encodedJPEG(t)
	var hitsA, hitsB int
	upstreamA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsA++
		_, _ = w.Write(pngTile)
	}))
	defer upstreamA.Close()
	upstreamB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsB++
		_, _ = w.Write(jpegTile)
	}))
	defer upstreamB.Close()

	cacheDir := t.TempDir()
	cache := NewTileCache(TileSettings{
		CacheDir: cacheDir,
		Upstream: upstreamA.URL + "/{z}/{x}/{y}",
		MapType:  "raster",
		MaxZoom:  19,
	})
	defer cache.Close()

	request := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		cache.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/tiles/1/0/0.png", nil))
		return rec
	}

	first := request()
	if first.Code != http.StatusOK || first.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("first tile = status %d, content-type %q", first.Code, first.Header().Get("Content-Type"))
	}
	if hitsA != 1 {
		t.Fatalf("source A hits = %d, want 1", hitsA)
	}

	cache.Update(TileSettings{
		CacheDir: cacheDir,
		Upstream: upstreamB.URL + "/{z}/{x}/{y}",
		MapType:  "raster",
		MaxZoom:  19,
	})
	second := request()
	if second.Code != http.StatusOK || second.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("second tile = status %d, content-type %q", second.Code, second.Header().Get("Content-Type"))
	}
	if hitsB != 1 {
		t.Fatalf("source B hits = %d, want 1", hitsB)
	}
	if bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) {
		t.Fatal("source switch returned stale tile bytes")
	}
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("cache namespaces = %d, want 2", len(entries))
	}
}

func TestTileCacheRejectsOutOfRangeCoordinates(t *testing.T) {
	cache := NewTileCache(TileSettings{CacheDir: t.TempDir(), Upstream: "https://example.test/{z}/{x}/{y}", MaxZoom: 19})
	defer cache.Close()
	rec := httptest.NewRecorder()
	cache.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/tiles/1/2/0.png", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestTileCacheDoesNotProxyDirectRasterSources(t *testing.T) {
	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	defer upstream.Close()

	cache := NewTileCache(TileSettings{
		CacheDir: t.TempDir(),
		Upstream: upstream.URL + "/{z}/{x}/{y}.png",
		MapType:  "raster-direct",
		MaxZoom:  19,
	})
	defer cache.Close()

	rec := httptest.NewRecorder()
	cache.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/tiles/1/0/0.png", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if called {
		t.Fatal("direct raster source unexpectedly reached the server proxy")
	}
}

func TestBuildWMSGetMapURL(t *testing.T) {
	raw, err := buildWMSGetMapURL("https://maps.example.test/wms?token=abc", "by_dop20c", 1, 2, 3, 4)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	for key, want := range map[string]string{
		"SERVICE": "WMS", "REQUEST": "GetMap", "VERSION": "1.3.0", "CRS": "EPSG:3857",
		"LAYERS": "by_dop20c", "BBOX": "1.000000,2.000000,3.000000,4.000000", "token": "abc",
	} {
		if got := q.Get(key); got != want {
			t.Fatalf("query %s = %q, want %q", key, got, want)
		}
	}
}

func TestTileCacheRejectsWMSExceptionDocument(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("LAYERS"); got != "by_dop20c" {
			t.Errorf("LAYERS = %q, want by_dop20c", got)
		}
		if got := r.URL.Query().Get("CRS"); got != "EPSG:3857" {
			t.Errorf("CRS = %q, want EPSG:3857", got)
		}
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte("<ServiceException>outside coverage</ServiceException>"))
	}))
	defer upstream.Close()

	cache := NewTileCache(TileSettings{
		CacheDir:  t.TempDir(),
		MapType:   "wms",
		Upstream:  upstream.URL + "/wms",
		WMSLayers: "by_dop20c",
		MaxZoom:   19,
	})
	defer cache.Close()
	rec := httptest.NewRecorder()
	cache.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/tiles/1/0/0.png", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
}

func TestValidateTileSettingsAcceptsBayernWMTS(t *testing.T) {
	tiles := TileSettings{
		CacheDir: "tiles-cache",
		MapType:  "raster",
		Upstream: "https://wmtsod1.bayernwolke.de/wmts/by_webkarte/smerc/{z}/{x}/{y}",
		MaxZoom:  19,
	}
	if err := validateTileSettings(&tiles); err != nil {
		t.Fatalf("Bayern WMTS rejected: %v", err)
	}
}

func TestTinyTilesOfflinePresetHasLocalStyle(t *testing.T) {
	var preset *TileSourcePreset
	for i := range BuiltinTilePresets {
		if BuiltinTilePresets[i].ID == "tinytiles_local" {
			preset = &BuiltinTilePresets[i]
			break
		}
	}
	if preset == nil {
		t.Fatal("tinyTiles offline preset is missing")
	}
	if preset.MapType != "vector" || preset.StyleURL != "/static/styles/tinytiles-minimal.json" || preset.MaxZoom != 14 {
		t.Fatalf("tinyTiles preset = %#v", preset)
	}
	if err := validateTileSettings(&TileSettings{CacheDir: "tiles-cache", MapType: preset.MapType, StyleURL: preset.StyleURL, MaxZoom: preset.MaxZoom}); err != nil {
		t.Fatalf("tinyTiles preset rejected: %v", err)
	}
}

func TestTinyTilesOfflineStyleIsEmbedded(t *testing.T) {
	style, err := embedded.ReadFile("web/static/styles/tinytiles-minimal.json")
	if err != nil {
		t.Fatalf("read embedded tinyTiles style: %v", err)
	}
	if !bytes.Contains(style, []byte(`"/tinytiles/tilejson.json"`)) ||
		!bytes.Contains(style, []byte(`"source-layer":"building"`)) ||
		!bytes.Contains(style, []byte(`"source-layer":"water"`)) ||
		!bytes.Contains(style, []byte(`"source-layer":"landcover"`)) ||
		!bytes.Contains(style, []byte(`"farmland","meadow"`)) {
		t.Fatalf("tinyTiles style misses expected local layers: %s", style)
	}
}

func TestOfflineMapAssetsAreEmbedded(t *testing.T) {
	for _, path := range []string{
		"web/static/leaflet/leaflet.css",
		"web/static/leaflet/leaflet.js",
		"web/static/leaflet/leaflet.markercluster.js",
		"web/static/maplibre/maplibre-gl.js",
		"web/static/maplibre/maplibre-gl.css",
		"web/static/maplibre/leaflet-maplibre-gl.js",
	} {
		asset, err := embedded.ReadFile(path)
		if err != nil {
			t.Fatalf("read embedded %s: %v", path, err)
		}
		if len(asset) < 100 {
			t.Fatalf("embedded %s is unexpectedly small (%d bytes)", path, len(asset))
		}
	}
	index, err := embedded.ReadFile("web/index.html")
	if err != nil {
		t.Fatalf("read embedded index: %v", err)
	}
	if bytes.Contains(index, []byte("unpkg.com")) {
		t.Fatal("offline page still contains a CDN fallback")
	}
}

func TestApplyBayernVectorFallbackMigratesLegacySettings(t *testing.T) {
	standard := TileSettings{
		MapType:  "vector",
		StyleURL: "https://vtod1.bayernwolke.de/styles/by_style_standard.json",
	}
	if !applyBayernVectorFallback(&standard) {
		t.Fatal("standard Bayern vector style did not receive a fallback")
	}
	if standard.Upstream != bayernWMTSWebkarte || standard.MaxZoom != 19 {
		t.Fatalf("standard fallback = %#v", standard)
	}

	aerial := TileSettings{
		MapType:  "vector",
		StyleURL: "https://vtod1.bayernwolke.de/styles/by_style_luftbild.json",
	}
	if !applyBayernVectorFallback(&aerial) || aerial.Upstream != bayernWMTSAerial {
		t.Fatalf("aerial fallback = %#v", aerial)
	}

	custom := TileSettings{MapType: "vector", StyleURL: "https://example.test/style.json"}
	if applyBayernVectorFallback(&custom) || custom.Upstream != "" {
		t.Fatalf("non-Bayern style unexpectedly changed: %#v", custom)
	}
}

func TestDefaultSettingsUseDirectOSMTiles(t *testing.T) {
	settings := DefaultSettings(t.TempDir(), "https://tile.openstreetmap.org/{z}/{x}/{y}.png")
	if settings.Tiles.MapType != "raster-direct" {
		t.Fatalf("default map type = %q, want raster-direct", settings.Tiles.MapType)
	}
	if settings.Tiles.Attribution != "© OpenStreetMap contributors" {
		t.Fatalf("default attribution = %q", settings.Tiles.Attribution)
	}
}

func TestApplyOSMDirectRasterMigratesLegacyProxySettings(t *testing.T) {
	tiles := TileSettings{
		MapType:  "raster",
		Upstream: "https://tile.openstreetmap.org/{z}/{x}/{y}.png",
	}
	if !applyOSMDirectRaster(&tiles) {
		t.Fatal("legacy OSM proxy setting was not migrated")
	}
	if tiles.MapType != "raster-direct" {
		t.Fatalf("map type = %q, want raster-direct", tiles.MapType)
	}
}

func TestRouteCacheKeyIncludesAllRoutingWeights(t *testing.T) {
	cache := &RouteCache{}
	base := osmmini.RouteOptions{Weights: osmmini.ProWeights{LeftTurn: 1}}
	changed := base
	changed.Weights.TrafficLightPenalty = 15
	if cache.cacheKey(1, 2, base) == cache.cacheKey(1, 2, changed) {
		t.Fatal("route cache key ignored traffic-light penalty")
	}
	changed = base
	changed.EmergencyMode = true
	if cache.cacheKey(1, 2, base) == cache.cacheKey(1, 2, changed) {
		t.Fatal("route cache key ignored emergency mode")
	}
}

func TestSearchPOIMatchesIncludesTaggedNodes(t *testing.T) {
	s := &server{
		poiNodes: map[int64]osmmini.Coord{7: {Lat: 48.137, Lon: 11.575}},
		poiTaggedNodes: map[int64]osmmini.Node{7: {
			ID: 7, Lat: 48.137, Lon: 11.575,
			Tags: osmmini.Tags{"name": "Bayern Tankstelle", "amenity": "fuel"},
		}},
		poiWays: map[int64]osmmini.Way{},
	}
	results := s.searchPOIMatches("Tankstelle", 5)
	if len(results) != 1 || results[0].Kind != "node" || results[0].ID != 7 {
		t.Fatalf("results = %#v, want tagged node", results)
	}
}

func TestPOICachePersistsTaggedNodes(t *testing.T) {
	path := t.TempDir() + "/poi.json"
	s := &server{}
	nodes := map[int64]osmmini.Coord{7: {Lat: 48.137, Lon: 11.575}}
	tagged := map[int64]osmmini.Node{7: {ID: 7, Lat: 48.137, Lon: 11.575, Tags: osmmini.Tags{"amenity": "fuel", "name": "Bayern Tankstelle"}}}
	if err := s.savePOICache(path, nodes, tagged, map[int64]osmmini.Way{}, map[int64]osmmini.Relation{}); err != nil {
		t.Fatal(err)
	}
	loadedNodes := map[int64]osmmini.Coord{}
	loadedTagged := map[int64]osmmini.Node{}
	if err := s.loadPOICache(path, loadedNodes, loadedTagged, map[int64]osmmini.Way{}, map[int64]osmmini.Relation{}); err != nil {
		t.Fatal(err)
	}
	if got := loadedTagged[7].Tags["amenity"]; got != "fuel" {
		t.Fatalf("loaded tagged node amenity = %q, want fuel", got)
	}
}

func TestPublicSettingsRedactsAPIKey(t *testing.T) {
	set := publicSettings(Settings{AI: AISettings{OpenAIAPIKey: "secret", OpenAIBaseURL: "https://api.example.test"}})
	if set.AI.OpenAIAPIKey != "" {
		t.Fatal("public settings exposed API key")
	}
	if set.AI.OpenAIBaseURL == "" {
		t.Fatal("public settings unexpectedly removed non-secret configuration")
	}
}

func TestSettingsHandlerDoesNotReturnAPIKey(t *testing.T) {
	store := NewSettingsStore(t.TempDir()+"/settings.json", Settings{AI: AISettings{OpenAIAPIKey: "secret"}})
	s := &server{settings: store}
	rec := httptest.NewRecorder()
	s.handleSettings(rec, httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("secret")) {
		t.Fatalf("settings response leaked API key: %s", rec.Body.String())
	}
}

func TestHandleIndexEmbedsSettingsAsJSONObject(t *testing.T) {
	settings := DefaultSettings(t.TempDir(), "https://tiles.example.test/{z}/{x}/{y}.png")
	settings.AI.OpenAIAPIKey = "secret"
	store := NewSettingsStore(t.TempDir()+"/settings.json", settings)
	tmpl := template.Must(template.ParseFS(embedded, "web/index.html"))
	s := &server{settings: store, indexTmpl: tmpl}
	rec := httptest.NewRecorder()
	s.handleIndex(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	match := regexp.MustCompile(`(?s)<script id="initialSettings" type="application/json">(.*?)</script>`).FindStringSubmatch(rec.Body.String())
	if len(match) != 2 {
		t.Fatalf("initialSettings script not found: %s", rec.Body.String())
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(match[1]), &payload); err != nil {
		t.Fatalf("initial settings is not a JSON object: %v; payload=%s", err, match[1])
	}
	if _, ok := payload["tiles"]; !ok {
		t.Fatalf("initial settings has no tiles object: %s", match[1])
	}
	if bytes.Contains([]byte(match[1]), []byte("secret")) {
		t.Fatalf("initial settings leaked API key: %s", match[1])
	}
}

func TestIndexIncludesVisualMapSourceChoice(t *testing.T) {
	page, err := embedded.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range [][]byte{
		[]byte(`id="mapWelcomeOverlay"`),
		[]byte(`id="tileSourceCards"`),
		[]byte(`id="tileLoadOverlay"`),
		[]byte(`id="tinyTilesBuild"`),
	} {
		if !bytes.Contains(page, marker) {
			t.Fatalf("map-source UI misses %s", marker)
		}
	}
	if bytes.Contains(page, []byte(`id="tilePreset"`)) {
		t.Fatal("legacy tile-source dropdown is still present")
	}
}

func TestSettingsHandlerRequiresAdminToken(t *testing.T) {
	settings := DefaultSettings(t.TempDir(), "https://tiles.example.test/{z}/{x}/{y}.png")
	store := NewSettingsStore(t.TempDir()+"/settings.json", settings)
	cache := NewTileCache(settings.Tiles)
	defer cache.Close()
	s := &server{settings: store, tiles: cache, adminToken: "test-admin-token"}
	body, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}

	denied := httptest.NewRecorder()
	s.handleSettings(denied, httptest.NewRequest(http.MethodPut, "/api/v1/settings", bytes.NewReader(body)))
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d, want %d", denied.Code, http.StatusUnauthorized)
	}

	allowed := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-admin-token")
	s.handleSettings(allowed, req)
	if allowed.Code != http.StatusOK {
		t.Fatalf("authenticated status = %d, want %d: %s", allowed.Code, http.StatusOK, allowed.Body.String())
	}
}

func TestTinyTilesBuildStatusIsPublicButStartRequiresAdmin(t *testing.T) {
	s := &server{adminToken: "test-admin-token"}

	status := httptest.NewRecorder()
	s.handleTinyTilesBuild(status, httptest.NewRequest(http.MethodGet, "/api/v1/tinytiles/build", nil))
	if status.Code != http.StatusOK {
		t.Fatalf("status endpoint = %d, want %d", status.Code, http.StatusOK)
	}
	var payload tinyTilesBuildStatus
	if err := json.Unmarshal(status.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.State != "idle" {
		t.Fatalf("initial tinyTiles status = %#v, want idle", payload)
	}

	denied := httptest.NewRecorder()
	s.handleTinyTilesBuild(denied, httptest.NewRequest(http.MethodPost, "/api/v1/tinytiles/build", bytes.NewBufferString(`{}`)))
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized build = %d, want %d", denied.Code, http.StatusUnauthorized)
	}
}

func TestSettingsHandlerAllowsWritesByDefaultWithoutAdminToken(t *testing.T) {
	settings := DefaultSettings(t.TempDir(), "https://tiles.example.test/{z}/{x}/{y}.png")
	store := NewSettingsStore(t.TempDir()+"/settings.json", settings)
	cache := NewTileCache(settings.Tiles)
	defer cache.Close()
	s := &server{settings: store, tiles: cache}
	body, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.handleSettings(rec, httptest.NewRequest(http.MethodPut, "/api/v1/settings", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("default settings write status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestApplyDefaultHighwaySpeedOverridesKeepsUnspecifiedTypes(t *testing.T) {
	original := osmmini.DefaultHighwaySpeeds
	defer func() { osmmini.DefaultHighwaySpeeds = original }()
	osmmini.DefaultHighwaySpeeds = map[string]float64{"motorway": 110, "secondary": 70}
	applyDefaultHighwaySpeedOverrides(map[string]float64{"motorway": 130})
	if got := osmmini.DefaultHighwaySpeeds["motorway"]; got != 130 {
		t.Fatalf("motorway = %v, want 130", got)
	}
	if got := osmmini.DefaultHighwaySpeeds["secondary"]; got != 70 {
		t.Fatalf("secondary = %v, want preserved value 70", got)
	}
}

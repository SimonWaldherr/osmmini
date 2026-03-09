package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	osmmini "simonwaldherr.de/go/osmmini"
)

//go:embed web/index.html web/style.css web/app.js docs/* api/openapi.yaml
var embedded embed.FS

const buildVersion = "dev"

// aiQueryTimeout is the maximum time allowed for an AI query to complete.
const aiQueryTimeout = 120 * time.Second

// maxAIResponseBytes is the maximum response body size read from AI providers.
const maxAIResponseBytes = 1 << 20

// ---- Settings ----

// TileSettings controls the tile cache / map display behavior.
// These settings are persisted in the settings file and can be updated
// at runtime via the settings API.
type TileSettings struct {
	CacheDir     string `json:"cache_dir"`
	Upstream     string `json:"upstream"`
	UserAgent    string `json:"user_agent"`
	MapType      string `json:"map_type,omitempty"`       // "raster" (default), "vector", "wms"
	StyleURL     string `json:"style_url,omitempty"`      // vector: MapLibre GL style URL
	WMSLayers    string `json:"wms_layers,omitempty"`     // wms: comma-separated layer names
	Attribution  string `json:"attribution,omitempty"`    // attribution text shown on the map
	MemCacheSize int    `json:"mem_cache_size,omitempty"` // L1 in-memory tile cache capacity (0 = default 512)
}

// TileSourcePreset is a named tile/map-source configuration shown in the UI preset picker.
type TileSourcePreset struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	MapType     string `json:"map_type"`
	Upstream    string `json:"upstream,omitempty"`
	StyleURL    string `json:"style_url,omitempty"`
	WMSLayers   string `json:"wms_layers,omitempty"`
	Attribution string `json:"attribution"`
	MaxZoom     int    `json:"max_zoom,omitempty"`
}

// BuiltinTilePresets lists the map/tile sources available out of the box.
// These are served via GET /api/v1/tile-sources.
var BuiltinTilePresets = []TileSourcePreset{
	{
		ID: "osm", Label: "OpenStreetMap Standard", MapType: "raster",
		Upstream:    "https://tile.openstreetmap.org/{z}/{x}/{y}.png",
		Attribution: "© OpenStreetMap contributors", MaxZoom: 19,
	},
	{
		ID: "osm_de", Label: "OpenStreetMap Deutschland", MapType: "raster",
		Upstream:    "https://tile.openstreetmap.de/{z}/{x}/{y}.png",
		Attribution: "© OpenStreetMap contributors", MaxZoom: 18,
	},
	{
		ID: "osm_hot", Label: "OSM Humanitarian", MapType: "raster",
		Upstream:    "https://a.tile.openstreetmap.fr/hot/{z}/{x}/{y}.png",
		Attribution: "© OpenStreetMap contributors, Tiles by HOT", MaxZoom: 19,
	},
	{
		ID: "osm_topo", Label: "OpenTopoMap", MapType: "raster",
		Upstream:    "https://tile.opentopomap.org/{z}/{x}/{y}.png",
		Attribution: "© OpenStreetMap contributors, © OpenTopoMap", MaxZoom: 17,
	},
	{
		ID: "carto_light", Label: "CartoDB Positron (hell)", MapType: "raster",
		Upstream:    "https://a.basemaps.cartocdn.com/light_all/{z}/{x}/{y}.png",
		Attribution: "© OpenStreetMap contributors © CARTO", MaxZoom: 19,
	},
	{
		ID: "carto_dark", Label: "CartoDB Dark Matter (dunkel)", MapType: "raster",
		Upstream:    "https://a.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}.png",
		Attribution: "© OpenStreetMap contributors © CARTO", MaxZoom: 19,
	},
	{
		ID: "basemap_de", Label: "Basemap.de (BKG)", MapType: "raster",
		Upstream:    "https://sgx.geodatenzentrum.de/wmts_basemapde/tile/1.0.0/basemap_de_webkarte/default/GLOBAL_WEBMERCATOR/{z}/{y}/{x}.png",
		Attribution: "© Bundesamt für Kartographie und Geodäsie (BKG)", MaxZoom: 18,
	},
	{
		ID: "geodaten_bavaria", Label: "Geodaten Bayern – BayernAtlas", MapType: "wms",
		Upstream:    "https://geoservices.bayern.de/od/wms/dop/v1/dop20?",
		WMSLayers:   "by_dop20c",
		Attribution: "© Bayerische Vermessungsverwaltung", MaxZoom: 18,
	},
	{
		ID: "maplibre_demo", Label: "MapLibre Demo Tiles (Vektor)", MapType: "vector",
		StyleURL:    "https://demotiles.maplibre.org/style.json",
		Attribution: "© MapLibre", MaxZoom: 19,
	},
}

// staticPOIMapping maps normalized query keywords to OSM tag filters
// used for quick, language-aware POI lookup without LLMs.
var staticPOIMapping = map[string]map[string]string{
	// German
	"mcdonald":    {"amenity": "fast_food", "brand": "McDonald's"},
	"mcdonalds":   {"amenity": "fast_food", "brand": "McDonald's"},
	"tankstelle":  {"amenity": "fuel"},
	"benzin":      {"amenity": "fuel"},
	"bahnhof":     {"railway": "station"},
	"apotheke":    {"amenity": "pharmacy"},
	"supermarkt":  {"shop": "supermarket"},
	"bäckerei":    {"shop": "bakery"},
	"baeckerei":   {"shop": "bakery"},
	"parkplatz":   {"amenity": "parking"},
	"bank":        {"amenity": "bank"},
	"krankenhaus": {"amenity": "hospital"},
	"schule":      {"amenity": "school"},
	"museum":      {"tourism": "museum"},
	"see":         {"natural": "water"},
	"wald":        {"landuse": "forest"},
	// English
	"gas":           {"amenity": "fuel"},
	"fuel":          {"amenity": "fuel"},
	"train station": {"railway": "station"},
	"pharmacy":      {"amenity": "pharmacy"},
	"supermarket":   {"shop": "supermarket"},
	"parking":       {"amenity": "parking"},
	"hospital":      {"amenity": "hospital"},
	"school":        {"amenity": "school"},
	// museum already covered above (German/English), skip duplicate
	"lake":   {"natural": "water"},
	"forest": {"landuse": "forest"},
}

// brandAliasMap maps common user typos/aliases to a canonical normalized brand key
var brandAliasMap = map[string]string{
	"maci":        "mcdonalds",
	"mcdo":        "mcdonalds",
	"mcd":         "mcdonalds",
	"mcdonald":    "mcdonalds",
	"mcdonalds":   "mcdonalds",
	"macdonalds":  "mcdonalds",
	"maccdonalds": "mcdonalds",
	"macca":       "mcdonalds",
}

// buildBrandAliases inspects the loaded POI index and generates simple
// alias mappings for brands/names to improve fuzzy matching of user queries.
func (s *server) buildBrandAliases() {
	s.poiMu.RLock()
	defer s.poiMu.RUnlock()

	addAlias := func(raw string) {
		if raw == "" {
			return
		}
		canon := normalizeForCompare(raw)
		if canon == "" {
			return
		}
		// create a compact alias key (no spaces/punctuation)
		alias := strings.ReplaceAll(canon, " ", "")
		alias = strings.ReplaceAll(alias, "'", "")
		alias = strings.ReplaceAll(alias, "\"", "")
		alias = strings.ReplaceAll(alias, "-", "")
		alias = strings.ReplaceAll(alias, ".", "")
		if alias == "" {
			return
		}
		// don't overwrite existing manual aliases
		if _, exists := brandAliasMap[alias]; !exists {
			brandAliasMap[alias] = canon
		}
	}

	// scan ways and relations
	for _, w := range s.poiWays {
		addAlias(w.Tags["brand"])
		addAlias(w.Tags["name"])
	}
	for _, r := range s.poiRels {
		addAlias(r.Tags["brand"])
		addAlias(r.Tags["name"])
	}
	// scan address entries
	for _, a := range s.addrs {
		addAlias(a.Tags["brand"])
		addAlias(a.Tags["name"])
	}
}

// normalizeForCompare returns a lowercased, ASCII-fied string without punctuation
// suitable for simple fuzzy matching.
func normalizeForCompare(s string) string {
	s = strings.ToLower(s)
	// replace German umlauts and common punctuation
	repl := strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss", "'", "", "\u2019", "", "\u2018", "", "-", " ", ",", " ", ".", " ")
	s = repl.Replace(s)
	// remove other punctuation
	trimmed := make([]rune, 0, len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == ' ' {
			trimmed = append(trimmed, r)
		}
	}
	return strings.TrimSpace(string(trimmed))
}

// Settings holds the server configuration persisted in the settings.json
// file. The server loads these settings early during startup so routing
// defaults (e.g. highway speeds) can be applied before the graph is built.
// Use the HTTP API `PUT /api/v1/settings` to update them at runtime; some
// changes (like default speeds used during graph import) require rebuilding
// the graph or restarting the server to take full effect.
type Settings struct {
	Routing osmmini.RouteOptions `json:"routing"`
	Tiles   TileSettings         `json:"tiles"`
	// SavePOICache controls whether POI cache files (<pbf>.poi.json) are
	// written when extracting POIs from a PBF. Set to false to disable.
	SavePOICache bool `json:"save_poi_cache,omitempty"`
	// DefaultHighwaySpeeds allows overriding fallback speeds per highway type
	DefaultHighwaySpeeds map[string]float64 `json:"default_highway_speeds,omitempty"`
	// AllowedHighwayTypes controls which highway types are imported/allowed
	AllowedHighwayTypes []string `json:"allowed_highway_types,omitempty"`
}

// DefaultSettings returns a sane set of defaults used when no
// settings.json exists. These defaults are chosen for Germany: the
// routing objective is set to minimize duration and motorway speeds are
// assumed higher (e.g. 150 km/h). Adjust `settings.json` to override.
func DefaultSettings(cacheDir, upstream string) Settings {
	return Settings{
		Routing: osmmini.RouteOptions{
			Engine: osmmini.EngineAStar,
			// Default routing objective for Germany: minimize duration
			Objective: osmmini.ObjectiveDuration,
			Pro:       false,
			Weights: osmmini.ProWeights{
				LeftTurn:  0,
				RightTurn: 0,
				UTurn:     0,
				Crossing:  0,
				// MaxSpeedKph caps speeds used by the routing heuristics
				// and cost calculations. Set higher to allow faster
				// assumptions on unrestricted Autobahn sections.
				MaxSpeedKph: 150,
			},
		},
		Tiles: TileSettings{
			CacheDir:     cacheDir,
			Upstream:     upstream,
			UserAgent:    "osmmini-routerd/1.0 (offline routing)",
			MapType:      "raster",
			Attribution:  "© OpenStreetMap contributors",
			MemCacheSize: tileMemCacheDefaultMaxItems,
		},
		// persist POI cache files by default
		SavePOICache: true,
		// DefaultHighwaySpeeds: fallback speeds (kph) per highway type.
		// These are the built-in defaults used when no "default_highway_speeds"
		// are provided in the settings file. At startup the server loads the
		// configured settings (e.g. settings.json) and, if a
		// `default_highway_speeds` map is present, applies those values to the
		// routing engine so they override these built-in defaults.
		// Note: changing the settings file after startup requires using the
		// settings API (`PUT /api/v1/settings`) or restarting the server for
		// the new defaults to be applied during graph build.
		DefaultHighwaySpeeds: map[string]float64{
			"motorway":      150, // km/h - German Autobahn (assumed higher/default)
			"trunk":         100, // km/h - from German Bundesstraße default
			"primary":       80,  // km/h - from German Landesstraße default
			"secondary":     70,  // km/h - from German Kreisstraße default
			"tertiary":      60,  // km/h - from German Gemeindestraße default
			"unclassified":  50,  // km/h - typical urban road
			"residential":   30,  // km/h - typical urban residential
			"living_street": 10,  // km/h - typical living street
			"service":       20,  // km/h - typical service road
			"track":         25,  // km/h - typical unpaved track
		},
	}
}

type SettingsStore struct {
	mu   sync.RWMutex
	path string
	v    Settings
}

func NewSettingsStore(path string, def Settings) *SettingsStore {
	return &SettingsStore{path: path, v: def}
}

func (s *SettingsStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s.saveLocked()
		}
		return err
	}
	var v Settings
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	s.v = v
	return nil
}

func (s *SettingsStore) Get() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.v
}

func (s *SettingsStore) Put(v Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.v = v
	return s.saveLocked()
}

func (s *SettingsStore) saveLocked() error {
	tmp := s.path + ".tmp"
	b, err := json.MarshalIndent(s.v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// ---- Tile cache proxy ----

// tileCacheKey is the lookup key for a single tile (zoom / x / y).
type tileCacheKey struct{ z, x, y int }

// tileMemEntry holds a tile's raw bytes and its L1 expiry time.
type tileMemEntry struct {
	data      []byte
	expiresAt time.Time
}

// tileMemCacheDefaultMaxItems is the default in-memory tile cache capacity.
const tileMemCacheDefaultMaxItems = 512

// tileCacheStats tracks hit/miss counters for observability.
type tileCacheStats struct {
	memHits  int64
	diskHits int64
	fetches  int64
	errors   int64
}

// TileCache proxies /tiles/{z}/{x}/{y}.png requests using a two-tier cache:
//
//   - L1: bounded in-memory cache (fast; lost on restart)
//   - L2: disk cache under cfg.CacheDir (persistent across restarts)
//
// Request flow: L1 hit → serve immediately; L2 hit → warm L1, serve;
// miss → fetch upstream, write to L1+L2, serve.
type TileCache struct {
	mu     sync.RWMutex
	cfg    TileSettings
	client *http.Client

	// L1 in-memory cache
	mem    map[tileCacheKey]*tileMemEntry
	memMax int

	stats tileCacheStats
	stopC chan struct{} // closed to stop the background eviction goroutine
}

func NewTileCache(cfg TileSettings) *TileCache {
	memMax := cfg.MemCacheSize
	if memMax <= 0 {
		memMax = tileMemCacheDefaultMaxItems
	}
	tc := &TileCache{
		cfg:    cfg,
		client: &http.Client{Timeout: 25 * time.Second},
		mem:    make(map[tileCacheKey]*tileMemEntry, memMax),
		memMax: memMax,
		stopC:  make(chan struct{}),
	}
	// Background L1 eviction: purge expired entries every hour.
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				tc.evictMem()
			case <-tc.stopC:
				return
			}
		}
	}()
	return tc
}

// Close stops the background eviction goroutine.
func (tc *TileCache) Close() {
	close(tc.stopC)
}

func (tc *TileCache) Update(cfg TileSettings) {
	newMax := cfg.MemCacheSize
	if newMax <= 0 {
		newMax = tileMemCacheDefaultMaxItems
	}
	tc.mu.Lock()
	tc.cfg = cfg
	if newMax != tc.memMax {
		tc.memMax = newMax
		// Clear L1 on capacity change so the new limit is respected immediately.
		tc.mem = make(map[tileCacheKey]*tileMemEntry, tc.memMax)
	}
	tc.mu.Unlock()
}

func (tc *TileCache) cfgCopy() TileSettings {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	return tc.cfg
}

// Stats returns a snapshot of cache hit/miss counters.
func (tc *TileCache) Stats() map[string]int64 {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	return map[string]int64{
		"mem_size":  int64(len(tc.mem)),
		"mem_hits":  tc.stats.memHits,
		"disk_hits": tc.stats.diskHits,
		"fetches":   tc.stats.fetches,
		"errors":    tc.stats.errors,
	}
}

// sortTileEntriesByExpiry sorts a slice of (key, expiry) pairs ascending
// by expiry time using slices.SortFunc for O(n log n) performance.
type tileKV struct {
	k tileCacheKey
	t time.Time
}

func sortTileEntriesByExpiry(entries []tileKV) {
	slices.SortFunc(entries, func(a, b tileKV) int {
		return a.t.Compare(b.t)
	})
}

// evictMem removes expired L1 entries under the write lock.
// If still over capacity after expiry removal, it removes the oldest 25%.
func (tc *TileCache) evictMem() {
	now := time.Now()
	tc.mu.Lock()
	defer tc.mu.Unlock()
	for k, e := range tc.mem {
		if now.After(e.expiresAt) {
			delete(tc.mem, k)
		}
	}
	if len(tc.mem) <= tc.memMax {
		return
	}
	// Still over capacity: sort by expiry and remove oldest 25%.
	entries := make([]tileKV, 0, len(tc.mem))
	for k, e := range tc.mem {
		entries = append(entries, tileKV{k, e.expiresAt})
	}
	sortTileEntriesByExpiry(entries)
	n := max(1, len(entries)/4)
	for _, kv := range entries[:n] {
		delete(tc.mem, kv.k)
	}
}

// getFromMem checks the L1 cache while holding the read lock throughout,
// ensuring the expiry check is race-free.
func (tc *TileCache) getFromMem(key tileCacheKey) ([]byte, bool) {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	e, ok := tc.mem[key]
	if !ok || time.Now().After(e.expiresAt) {
		return nil, false
	}
	return e.data, true
}

// setInMem inserts or updates an L1 entry with a 24-hour TTL.
// When at capacity it first removes expired entries, then sorts and
// removes the oldest 25% if still full.
func (tc *TileCache) setInMem(key tileCacheKey, data []byte) {
	expiry := time.Now().Add(24 * time.Hour)
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if len(tc.mem) >= tc.memMax {
		// Pass 1: purge expired entries.
		now := time.Now()
		for k, e := range tc.mem {
			if now.After(e.expiresAt) {
				delete(tc.mem, k)
			}
		}
		// Pass 2: if still full, remove oldest 25% by expiry.
		if len(tc.mem) >= tc.memMax {
			entries := make([]tileKV, 0, len(tc.mem))
			for k, e := range tc.mem {
				entries = append(entries, tileKV{k, e.expiresAt})
			}
			sortTileEntriesByExpiry(entries)
			n := max(1, len(entries)/4)
			for _, kv := range entries[:n] {
				delete(tc.mem, kv.k)
			}
		}
	}
	tc.mem[key] = &tileMemEntry{data: data, expiresAt: expiry}
}

func (tc *TileCache) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// /tiles/{z}/{x}/{y}.png
	p := strings.TrimPrefix(r.URL.Path, "/tiles/")
	parts := strings.Split(p, "/")
	if len(parts) != 3 {
		http.NotFound(w, r)
		return
	}
	z, err1 := strconv.Atoi(parts[0])
	x, err2 := strconv.Atoi(parts[1])
	yPart := parts[2]
	if err1 != nil || err2 != nil || z < 0 || z > 20 {
		http.NotFound(w, r)
		return
	}
	if !strings.HasSuffix(yPart, ".png") {
		http.NotFound(w, r)
		return
	}
	y, err3 := strconv.Atoi(strings.TrimSuffix(yPart, ".png"))
	if err3 != nil || x < 0 || y < 0 {
		http.NotFound(w, r)
		return
	}

	key := tileCacheKey{z, x, y}
	cfg := tc.cfgCopy()

	// L1: in-memory
	if data, ok := tc.getFromMem(key); ok {
		tc.mu.Lock()
		tc.stats.memHits++
		tc.mu.Unlock()
		serveTile(w, data)
		return
	}

	// L2: disk
	cachePath := filepath.Join(cfg.CacheDir, strconv.Itoa(z), strconv.Itoa(x), fmt.Sprintf("%d.png", y))
	if data, ok := readFileIfExists(cachePath); ok {
		tc.mu.Lock()
		tc.stats.diskHits++
		tc.mu.Unlock()
		tc.setInMem(key, data) // warm L1
		serveTile(w, data)
		return
	}

	// Miss: fetch from upstream. Support both tile template upstreams and
	// WMS endpoints (when cfg.MapType == "wms"). For WMS we compute the
	// EPSG:3857 bounding box for the tile and perform a GetMap request.
	var req *http.Request
	if cfg.MapType == "wms" {
		// Compute EPSG:3857 bbox for the slippy tile
		minx, miny, maxx, maxy := tileBBox3857(z, x, y)
		layers := cfg.WMSLayers
		// Build GetMap URL (WMS 1.3.0 using CRS=EPSG:3857)
		// Many WMS servers also accept CRS param and width/height.
		up := cfg.Upstream
		// Ensure no trailing query markers
		sep := "?"
		if strings.Contains(up, "?") {
			sep = "&"
		}
		wmsURL := fmt.Sprintf("%s%vSERVICE=WMS&REQUEST=GetMap&VERSION=1.3.0&CRS=EPSG:3857&LAYERS=%s&BBOX=%f,%f,%f,%f&WIDTH=256&HEIGHT=256&FORMAT=image/png",
			up, sep, url.QueryEscape(layers), minx, miny, maxx, maxy)
		var err error
		req, err = http.NewRequestWithContext(r.Context(), http.MethodGet, wmsURL, nil)
		if err != nil {
			tc.mu.Lock()
			tc.stats.errors++
			tc.mu.Unlock()
			http.Error(w, "tile upstream error", http.StatusBadGateway)
			return
		}
	} else {
		// Template-based upstream (e.g., {z}/{x}/{y}.png)
		up := cfg.Upstream
		up = strings.ReplaceAll(up, "{z}", strconv.Itoa(z))
		up = strings.ReplaceAll(up, "{x}", strconv.Itoa(x))
		up = strings.ReplaceAll(up, "{y}", strconv.Itoa(y))
		var err error
		req, err = http.NewRequestWithContext(r.Context(), http.MethodGet, up, nil)
		if err != nil {
			tc.mu.Lock()
			tc.stats.errors++
			tc.mu.Unlock()
			http.Error(w, "tile upstream error", http.StatusBadGateway)
			return
		}
	}
	if cfg.UserAgent != "" {
		req.Header.Set("User-Agent", cfg.UserAgent)
	}
	resp, err := tc.client.Do(req)
	if err != nil {
		tc.mu.Lock()
		tc.stats.errors++
		tc.mu.Unlock()
		http.Error(w, "tile upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		tc.mu.Lock()
		tc.stats.errors++
		tc.mu.Unlock()
		http.Error(w, "tile upstream non-200", http.StatusBadGateway)
		return
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil || len(data) == 0 {
		tc.mu.Lock()
		tc.stats.errors++
		tc.mu.Unlock()
		http.Error(w, "tile read error", http.StatusBadGateway)
		return
	}

	// Persist to L2 (disk) and warm L1.
	_ = os.MkdirAll(filepath.Dir(cachePath), 0o755)
	_ = os.WriteFile(cachePath, data, 0o644)
	tc.setInMem(key, data)

	tc.mu.Lock()
	tc.stats.fetches++
	tc.mu.Unlock()

	serveTile(w, data)
}

func serveTile(w http.ResponseWriter, data []byte) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(data)
}

func readFileIfExists(path string) ([]byte, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return b, true
}

// tileBBox3857 returns the bounding box (minx,miny,maxx,maxy) in EPSG:3857
// for the given slippy tile coordinates (z, x, y).
func tileBBox3857(z, x, y int) (float64, float64, float64, float64) {
	n := math.Pow(2, float64(z))
	lonMin := float64(x)/n*360.0 - 180.0
	lonMax := float64(x+1)/n*360.0 - 180.0

	latMax := tileYToLat(y, z)
	latMin := tileYToLat(y+1, z)

	// convert degrees to WebMercator meters
	const originShift = 20037508.342789244
	minx := lonMin * originShift / 180.0
	maxx := lonMax * originShift / 180.0

	miny := math.Log(math.Tan((90.0+latMin)*math.Pi/360.0)) / (math.Pi / 180.0)
	miny = miny * originShift / 180.0
	maxy := math.Log(math.Tan((90.0+latMax)*math.Pi/360.0)) / (math.Pi / 180.0)
	maxy = maxy * originShift / 180.0

	return minx, miny, maxx, maxy
}

func tileYToLat(y, z int) float64 {
	n := math.Pow(2, float64(z))
	latRad := math.Atan(math.Sinh(math.Pi * (1 - 2*float64(y)/n)))
	return latRad * 180.0 / math.Pi
}

// ---- API types ----

type apiSearchResult struct {
	ID    int64        `json:"id"`
	Kind  string       `json:"kind"`
	Label string       `json:"label"`
	Lat   float64      `json:"lat"`
	Lon   float64      `json:"lon"`
	Tags  osmmini.Tags `json:"tags,omitempty"`
}

type poiInfoResponse struct {
	ID          int64        `json:"id"`
	Kind        string       `json:"kind"`
	Label       string       `json:"label"`
	Lat         float64      `json:"lat"`
	Lon         float64      `json:"lon"`
	Tags        osmmini.Tags `json:"tags,omitempty"`
	DistanceM   *float64     `json:"distance_m,omitempty"`
	WikiSummary string       `json:"wiki_summary,omitempty"`
}

type Location struct {
	Query string   `json:"query,omitempty"`
	Lat   *float64 `json:"lat,omitempty"`
	Lon   *float64 `json:"lon,omitempty"`
}

type RouteRequest struct {
	From    Location              `json:"from"`
	To      Location              `json:"to"`
	Options *osmmini.RouteOptions `json:"options,omitempty"`
}

type RoutePoint struct {
	Input string  `json:"input"`
	Label string  `json:"label"`
	Lat   float64 `json:"lat"`
	Lon   float64 `json:"lon"`
	Node  int64   `json:"node"`
	SnapM float64 `json:"snap_m"`
}

type RouteResponse struct {
	From          RoutePoint         `json:"from"`
	To            RoutePoint         `json:"to"`
	Engine        string             `json:"engine"`
	Objective     string             `json:"objective"`
	Profile       string             `json:"profile,omitempty"`
	Cost          float64            `json:"cost"`
	DistanceM     float64            `json:"distance_m"`
	DurationS     float64            `json:"duration_s"`
	Path          []osmmini.Coord    `json:"path"`
	GoogleMapsURL string             `json:"google_maps_url"`
	AppleMapsURL  string             `json:"apple_maps_url"`
	Steps         []osmmini.Maneuver `json:"steps,omitempty"`
	Cached        bool               `json:"cached,omitempty"`
}

type TripStop struct {
	ID       string   `json:"id"`
	Location Location `json:"location"`
}

type Dependency struct {
	Before string `json:"before"`
	After  string `json:"after"`
}

type TripPlan struct {
	Start        Location     `json:"start"`
	End          *Location    `json:"end,omitempty"`
	Stops        []TripStop   `json:"stops"`
	Dependencies []Dependency `json:"dependencies,omitempty"`
	Optimize     bool         `json:"optimize"`
	Loop         bool         `json:"loop,omitempty"`
}

type TripSolveRequest struct {
	Plan    TripPlan              `json:"plan"`
	Options *osmmini.RouteOptions `json:"options,omitempty"`
}

type TripStopResolved struct {
	ID    string  `json:"id"`
	Label string  `json:"label"`
	Lat   float64 `json:"lat"`
	Lon   float64 `json:"lon"`
	Node  int64   `json:"node"`
	SnapM float64 `json:"snap_m"`
}

type TripSolveResponse struct {
	Engine        string             `json:"engine"`
	Objective     string             `json:"objective"`
	Order         []string           `json:"order"`
	Stops         []TripStopResolved `json:"stops"`
	DistanceM     float64            `json:"distance_m"`
	DurationS     float64            `json:"duration_s"`
	Cost          float64            `json:"cost"`
	Path          []osmmini.Coord    `json:"path"`
	Legs          []RouteResponse    `json:"legs"`
	GoogleMapsURL string             `json:"google_maps_url"`
	AppleMapsURL  string             `json:"apple_maps_url"`
}

// ---- Route result cache ----

// routeCacheKey uniquely identifies a route request for caching purposes.
type routeCacheKey struct {
	fromNode int64
	toNode   int64
	// include routing-relevant options in the key
	engine    string
	objective string
	profile   string
	maxSpeed  float64
	heightM   float64
	weightT   float64
}

// routeCacheEntry holds a cached route response with its expiry time.
type routeCacheEntry struct {
	resp      RouteResponse
	expiresAt time.Time
}

// routeCacheDefaultMaxItems is the default capacity bound for the route cache.
const routeCacheDefaultMaxItems = 4096

// RouteCache is a bounded in-memory cache for route responses.
// It uses a simple RW-mutex protected map with TTL eviction.
// Maximum capacity is capped so it never grows unbounded.
type RouteCache struct {
	mu       sync.RWMutex
	entries  map[routeCacheKey]*routeCacheEntry
	ttl      time.Duration
	maxItems int
}

func newRouteCache(ttl time.Duration, maxItems int) *RouteCache {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	if maxItems <= 0 {
		maxItems = routeCacheDefaultMaxItems
	}
	c := &RouteCache{
		entries:  make(map[routeCacheKey]*routeCacheEntry, 64),
		ttl:      ttl,
		maxItems: maxItems,
	}
	// Background eviction goroutine: purge expired entries every TTL/2.
	go func() {
		ticker := time.NewTicker(ttl / 2)
		defer ticker.Stop()
		for range ticker.C {
			c.evict()
		}
	}()
	return c
}

func (c *RouteCache) cacheKey(fromNode, toNode int64, opt osmmini.RouteOptions) routeCacheKey {
	return routeCacheKey{
		fromNode:  fromNode,
		toNode:    toNode,
		engine:    string(opt.Engine),
		objective: string(opt.Objective),
		profile:   string(opt.Profile),
		maxSpeed:  opt.Weights.MaxSpeedKph,
		heightM:   opt.Weights.VehicleHeightM,
		weightT:   opt.Weights.VehicleWeightT,
	}
}

func (c *RouteCache) get(key routeCacheKey) (RouteResponse, bool) {
	c.mu.RLock()
	e, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(e.expiresAt) {
		return RouteResponse{}, false
	}
	return e.resp, true
}

func (c *RouteCache) set(key routeCacheKey, resp RouteResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// When at capacity, evict all entries that are already expired.
	// If still at capacity afterwards, remove entries with the earliest
	// expiry time to make room (approximate LRU via expiry ordering).
	if len(c.entries) >= c.maxItems {
		now := time.Now()
		for k, e := range c.entries {
			if now.After(e.expiresAt) {
				delete(c.entries, k)
			}
		}
		// If still full, remove the half with the soonest expiry.
		if len(c.entries) >= c.maxItems {
			type kv struct {
				k routeCacheKey
				t time.Time
			}
			evictions := make([]kv, 0, len(c.entries))
			for k, e := range c.entries {
				evictions = append(evictions, kv{k, e.expiresAt})
			}
			// Sort ascending so we remove the shortest-lived first.
			for i := 1; i < len(evictions); i++ {
				for j := i; j > 0 && evictions[j].t.Before(evictions[j-1].t); j-- {
					evictions[j], evictions[j-1] = evictions[j-1], evictions[j]
				}
			}
			for _, kv := range evictions[:len(evictions)/2] {
				delete(c.entries, kv.k)
			}
		}
	}
	c.entries[key] = &routeCacheEntry{resp: resp, expiresAt: time.Now().Add(c.ttl)}
}

func (c *RouteCache) evict() {
	now := time.Now()
	c.mu.Lock()
	for k, e := range c.entries {
		if now.After(e.expiresAt) {
			delete(c.entries, k)
		}
	}
	c.mu.Unlock()
}

func (c *RouteCache) Invalidate() {
	c.mu.Lock()
	c.entries = make(map[routeCacheKey]*routeCacheEntry, 64)
	c.mu.Unlock()
}

// Size returns the current number of cached entries.
func (c *RouteCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// ---- Server ----

// aiProbeCache holds a cached AI provider status result with a short TTL
// so handleAIQuery doesn't need to re-probe providers on every request.
type aiProbeCache struct {
	mu        sync.Mutex
	providers []aiProvider
	expiresAt time.Time
}

// get returns cached providers if still valid, nil otherwise.
func (c *aiProbeCache) get() []aiProvider {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Now().Before(c.expiresAt) {
		return c.providers
	}
	return nil
}

// set stores providers with a 30-second TTL.
func (c *aiProbeCache) set(providers []aiProvider) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.providers = providers
	c.expiresAt = time.Now().Add(30 * time.Second)
}

// invalidate clears the cache so the next get() returns nil.
func (c *aiProbeCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.providers = nil
	c.expiresAt = time.Time{}
}

type server struct {
	router        *osmmini.Router
	addrs         []osmmini.AddressEntry
	startedAt     time.Time
	settings      *SettingsStore
	tiles         *TileCache
	routeCache    *RouteCache
	window        *osmmini.CoordWindow
	enforceWindow bool

	indexTmpl *template.Template
	openAPI   []byte
	// pbfPath is the path to the OSM PBF used to build the router. It is
	// retained so we can perform on-demand extraction for POIs/areas.
	pbfPath string

	// POI / area index populated at startup (best-effort). These maps are
	// populated by loadPOIIndex and protected by poiMu.
	poiMu    sync.RWMutex
	poiNodes map[int64]osmmini.Coord
	poiWays  map[int64]osmmini.Way
	poiRels  map[int64]osmmini.Relation
	// small in-memory cache for Wikipedia summaries (key: lang:title)
	wikiMu    sync.RWMutex
	wikiCache map[string]string
	// AI session store for multi-turn context (session id -> messages)
	aiMu       sync.Mutex
	aiSessions map[string][]aiMessage
	// aiProbe caches AI provider availability to avoid re-probing on every query
	aiProbe aiProbeCache
}

type aiMessage struct {
	Role    string    `json:"role"`
	Content string    `json:"content"`
	Ts      time.Time `json:"ts"`
}

func main() {
	// Command-line flags. `-pbf` should point at the OSM PBF used to
	// build the routing graph (e.g. a regional extract). If the file
	// is missing the server will fail to build the router.
	pbf := flag.String("pbf", "region.osm.pbf", "Path to OSM PBF")
	listen := flag.String("listen", ":8080", "HTTP listen address")
	settingsPath := flag.String("settings", "settings.json", "Settings JSON file")
	tilesDir := flag.String("tiles-dir", "tiles-cache", "Tile cache directory")
	tileUpstream := flag.String("tile-upstream", "https://tile.openstreetmap.org/{z}/{x}/{y}.png", "Tile upstream template")
	buildCH := flag.Bool("build-ch", true, "Build Contraction Hierarchies (CH) after graph load")

	// Coord window support
	windowStr := flag.String("window", "", "Coord window minLat,maxLat,minLon,maxLon (optional)")
	windowBufferM := flag.Float64("window-buffer-m", 0, "Expand window by meters for import (optional)")
	enforceWindow := flag.Bool("enforce-window", false, "Reject requests outside window (if window set)")

	flag.Parse()

	var win *osmmini.CoordWindow
	if strings.TrimSpace(*windowStr) != "" {
		w, err := parseWindow(*windowStr)
		if err != nil {
			log.Fatalf("invalid --window: %v", err)
		}
		win = &w
	}

	// Load settings early so defaults (e.g. highway speeds) can be applied
	store := NewSettingsStore(*settingsPath, DefaultSettings(*tilesDir, *tileUpstream))
	if err := store.Load(); err != nil {
		log.Fatalf("settings load failed: %v", err)
	}

	// Apply default highway speeds from settings (if provided)
	if m := store.Get().DefaultHighwaySpeeds; m != nil {
		osmmini.DefaultHighwaySpeeds = m
	}

	log.Printf("Loading PBF %s and building router...", *pbf)
	r, addrs, err := osmmini.BuildRouterWithAddressesOptions(*pbf, osmmini.BuildOptions{
		Window:        win,
		WindowBufferM: *windowBufferM,
	})
	if err != nil {
		log.Fatalf("router build failed: %v", err)
	}

	if b, ok := r.Bounds(); ok {
		log.Printf("Graph bounds: lat[%.6f..%.6f] lon[%.6f..%.6f]", b.MinLat, b.MaxLat, b.MinLon, b.MaxLon)
	}
	log.Printf("Graph ready: nodes=%d edges=%d addresses=%d", r.NodeCount(), r.EdgeCount(), len(addrs))
	if *buildCH {
		log.Printf("Building CH (upward graph)...")
		r.BuildCH()
		log.Printf("CH ready")
	}

	tileCache := NewTileCache(store.Get().Tiles)
	rCache := newRouteCache(5*time.Minute, routeCacheDefaultMaxItems)

	indexTmpl := template.Must(template.ParseFS(embedded, "web/index.html"))
	openapiBytes, _ := embedded.ReadFile("api/openapi.yaml")

	srv := &server{
		router:        r,
		addrs:         addrs,
		startedAt:     time.Now(),
		settings:      store,
		tiles:         tileCache,
		routeCache:    rCache,
		window:        win,
		enforceWindow: *enforceWindow,
		indexTmpl:     indexTmpl,
		openAPI:       openapiBytes,
		pbfPath:       *pbf,
	}

	// Best-effort: load POI / area index (may be slow; non-fatal)
	if err := srv.loadPOIIndex(*pbf); err != nil {
		log.Printf("warning: POI index failed: %v", err)
	}

	httpSrv := &http.Server{
		Addr:              *listen,
		Handler:           srv.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	log.Printf("Listening on %s", *listen)
	log.Fatal(httpSrv.ListenAndServe())
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()

	// Static assets - prefer local files over embedded for development
	webHandler := createHybridFileServer("cmd/web", "web")
	mux.Handle("/static/", http.StripPrefix("/static/", webHandler))

	docsHandler := createHybridFileServer("cmd/docs", "docs")
	mux.Handle("/docs/", http.StripPrefix("/docs/", docsHandler))
	mux.HandleFunc("/docs", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/docs/", http.StatusFound)
	})

	// OpenAPI
	mux.HandleFunc("/api/v1/openapi.yaml", s.handleOpenAPI)

	// Tiles
	mux.Handle("/tiles/", s.tiles)

	// API
	mux.HandleFunc("/api/v1/health", s.handleHealth)
	mux.HandleFunc("/api/v1/status", s.handleStatus)
	mux.HandleFunc("/api/v1/settings", s.handleSettings)
	mux.HandleFunc("/api/v1/tile-sources", s.handleTileSources)
	mux.HandleFunc("/api/v1/profiles", s.handleProfiles)
	mux.HandleFunc("/api/v1/search", s.handleSearch)
	mux.HandleFunc("/api/v1/route", s.handleRoute)
	mux.HandleFunc("/api/v1/trip/solve", s.handleTripSolve)
	mux.HandleFunc("/api/v1/ai/status", s.handleAIStatus)
	mux.HandleFunc("/api/v1/ai/query", s.handleAIQuery)
	mux.HandleFunc("/api/v1/agent/query", s.handleAgentQuery)
	mux.HandleFunc("/api/v1/agent/execute", s.handleAgentExecute)
	mux.HandleFunc("/api/v1/poi/", s.handlePOIInfo)

	// UI
	mux.HandleFunc("/", s.handleIndex)

	return withLogging(withCORS(mux))
}

// loadPOIIndex performs a best-effort extraction of tagged ways and relations
// from the PBF so area and POI queries can be answered. It's intentionally
// permissive in the tags it keeps to capture landuse/natural/amenity/shop.
func (s *server) loadPOIIndex(pbfPath string) error {
	if pbfPath == "" {
		return nil
	}
	nodes := make(map[int64]osmmini.Coord)
	ways := make(map[int64]osmmini.Way)
	rels := make(map[int64]osmmini.Relation)

	keepTags := map[string]bool{
		"name": true, "amenity": true, "shop": true, "brand": true,
		"natural": true, "landuse": true, "leisure": true, "tourism": true,
		"boundary": true, "admin_level": true,
	}

	opts := osmmini.Options{
		EmitWayNodeIDs:      true,
		EmitRelationMembers: true,
		KeepTag: func(k string) bool {
			return keepTags[k]
		},
	}

	cb := osmmini.Callbacks{
		Node: func(id int64, lat, lon float64) error {
			nodes[id] = osmmini.Coord{Lat: lat, Lon: lon}
			return nil
		},
		TaggedWay: func(w osmmini.Way) error {
			ways[w.ID] = w
			return nil
		},
		TaggedRelation: func(r osmmini.Relation) error {
			rels[r.ID] = r
			return nil
		},
	}

	// ExtractFile can be slow on large PBFs; run with a background context
	// and a modest timeout to avoid blocking startup indefinitely.
	// Try loading from a cache file first
	cachePath := pbfPath + ".poi.json"
	if loadErr := s.loadPOICache(cachePath, nodes, ways, rels); loadErr == nil {
		// loaded from cache
		s.poiMu.Lock()
		s.poiNodes = nodes
		s.poiWays = ways
		s.poiRels = rels
		s.wikiMu.Lock()
		s.wikiCache = make(map[string]string)
		s.wikiMu.Unlock()
		s.poiMu.Unlock()
		// build brand aliases from indexed POIs to improve fuzzy matching
		s.buildBrandAliases()
		// ensure aiSessions map initialized
		s.aiMu.Lock()
		if s.aiSessions == nil {
			s.aiSessions = make(map[string][]aiMessage)
		}
		s.aiMu.Unlock()
		return nil
	}

	done := make(chan error, 1)
	go func() {
		done <- osmmini.ExtractFile(pbfPath, opts, cb)
	}()
	select {
	case err := <-done:
		if err != nil {
			return err
		}
	case <-time.After(30 * time.Second):
		// give up after 30s — this is best-effort indexing
		return nil
	}

	// persist cache asynchronously (best-effort)
	if s.settings.Get().SavePOICache {
		go func() {
			_ = s.savePOICache(cachePath, nodes, ways, rels)
		}()
	}

	s.poiMu.Lock()
	s.poiNodes = nodes
	s.poiWays = ways
	s.poiRels = rels
	s.poiMu.Unlock()
	return nil
}

// loadPOICache attempts to populate the provided maps from a JSON cache file.
func (s *server) loadPOICache(path string, nodes map[int64]osmmini.Coord, ways map[int64]osmmini.Way, rels map[int64]osmmini.Relation) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var payload struct {
		Nodes map[int64]osmmini.Coord    `json:"nodes"`
		Ways  map[int64]osmmini.Way      `json:"ways"`
		Rels  map[int64]osmmini.Relation `json:"rels"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		return err
	}
	for k, v := range payload.Nodes {
		nodes[k] = v
	}
	for k, v := range payload.Ways {
		ways[k] = v
	}
	for k, v := range payload.Rels {
		rels[k] = v
	}
	return nil
}

// savePOICache writes the POI index to a JSON cache file (best-effort).
func (s *server) savePOICache(path string, nodes map[int64]osmmini.Coord, ways map[int64]osmmini.Way, rels map[int64]osmmini.Relation) error {
	payload := struct {
		Nodes map[int64]osmmini.Coord    `json:"nodes"`
		Ways  map[int64]osmmini.Way      `json:"ways"`
		Rels  map[int64]osmmini.Relation `json:"rels"`
	}{Nodes: nodes, Ways: ways, Rels: rels}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// fetchWikiSummary fetches a short summary for a wiki title (lang, title)
// and caches the result in-memory. Returns empty string on failure.
func (s *server) fetchWikiSummary(ctx context.Context, lang, title string) string {
	if lang == "" {
		lang = "en"
	}
	key := lang + ":" + title
	s.wikiMu.RLock()
	if v, ok := s.wikiCache[key]; ok {
		s.wikiMu.RUnlock()
		return v
	}
	s.wikiMu.RUnlock()

	titleEsc := url.PathEscape(title)
	u := fmt.Sprintf("https://%s.wikipedia.org/api/rest_v1/page/summary/%s", lang, titleEsc)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Do(req)
	if err != nil || resp == nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var out struct {
		Extract string `json:"extract"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8*1024)).Decode(&out); err != nil {
		return ""
	}
	s.wikiMu.Lock()
	s.wikiCache[key] = out.Extract
	s.wikiMu.Unlock()
	return out.Extract
}

// session helpers
func (s *server) appendSessionMessage(sessionID, role, content string) string {
	if sessionID == "" {
		return ""
	}
	s.aiMu.Lock()
	defer s.aiMu.Unlock()
	if s.aiSessions == nil {
		s.aiSessions = make(map[string][]aiMessage)
	}
	msgs := s.aiSessions[sessionID]
	msgs = append(msgs, aiMessage{Role: role, Content: content, Ts: time.Now()})
	// keep last 40 messages
	if len(msgs) > 40 {
		msgs = msgs[len(msgs)-40:]
	}
	s.aiSessions[sessionID] = msgs
	return sessionID
}

func (s *server) getSessionContext(sessionID string) []aiMessage {
	if sessionID == "" {
		return nil
	}
	s.aiMu.Lock()
	defer s.aiMu.Unlock()
	if s.aiSessions == nil {
		return nil
	}
	msgs := s.aiSessions[sessionID]
	if len(msgs) == 0 {
		return nil
	}
	// return a copy
	out := make([]aiMessage, len(msgs))
	copy(out, msgs)
	return out
}

// latLonToWebMercator converts WGS84 degrees to WebMercator meters.
func latLonToWebMercator(lat, lon float64) (float64, float64) {
	const originShift = 20037508.342789244
	x := lon * originShift / 180.0
	y := math.Log(math.Tan((90.0+lat)*math.Pi/360.0)) / (math.Pi / 180.0)
	y = y * originShift / 180.0
	return x, y
}

// polygonAreaMeters computes polygon area (signed) in square meters using
// the shoelace formula on WebMercator-projected coordinates.
func polygonAreaMeters(coords []osmmini.Coord) float64 {
	if len(coords) < 3 {
		return 0
	}
	area := 0.0
	// project coords
	pts := make([][2]float64, len(coords))
	for i, c := range coords {
		x, y := latLonToWebMercator(c.Lat, c.Lon)
		pts[i][0] = x
		pts[i][1] = y
	}
	for i := 0; i < len(pts); i++ {
		j := (i + 1) % len(pts)
		area += pts[i][0]*pts[j][1] - pts[j][0]*pts[i][1]
	}
	return math.Abs(area) * 0.5
}

// pointInPolygon performs a winding-number / raycast test in lat/lon space.
// coords must form a closed ring (first and last equal) or will be treated cyclically.
func pointInPolygon(pt osmmini.Coord, poly []osmmini.Coord) bool {
	inside := false
	j := len(poly) - 1
	for i := 0; i < len(poly); i++ {
		xi, yi := poly[i].Lon, poly[i].Lat
		xj, yj := poly[j].Lon, poly[j].Lat
		intersect := ((yi > pt.Lat) != (yj > pt.Lat)) &&
			(pt.Lon < (xj-xi)*(pt.Lat-yi)/(yj-yi+1e-12)+xi)
		if intersect {
			inside = !inside
		}
		j = i
	}
	return inside
}

// computeAreaForAdmin finds administrative relations matching name (case-insensitive)
// and sums area of forest/wood multipolygons inside them. Returns total area m^2
// and an optional representative polygon (outer ring) if available.
func (s *server) computeAreaForAdmin(ctx context.Context, adminName string) (float64, []osmmini.Coord, error) {
	s.poiMu.RLock()
	nodes := s.poiNodes
	ways := s.poiWays
	rels := s.poiRels
	s.poiMu.RUnlock()

	if len(rels) == 0 {
		return 0, nil, fmt.Errorf("no POI data indexed")
	}

	nameLow := normalizeForCompare(adminName)
	var matchedRel osmmini.Relation
	found := false
	for _, r := range rels {
		if r.Tags["boundary"] == "administrative" {
			n := normalizeForCompare(r.Tags["name"])
			if n != "" && strings.Contains(n, nameLow) {
				matchedRel = r
				found = true
				break
			}
		}
	}
	if !found {
		return 0, nil, fmt.Errorf("administrative area not found: %s", adminName)
	}

	// collect member ways
	memberWays := make([]osmmini.Way, 0)
	for _, m := range matchedRel.Members {
		if m.Type == osmmini.MemberWay {
			if w, ok := ways[m.ID]; ok {
				memberWays = append(memberWays, w)
			}
		}
	}

	rings := assembleRingsFromWays(memberWays, nodes)

	totalArea := 0.0
	for _, ring := range rings {
		isForest := false
		for _, w := range memberWays {
			if w.Tags["landuse"] == "forest" || w.Tags["natural"] == "wood" {
				isForest = true
				break
			}
		}
		a := polygonAreaOnSphere(ring)
		if isForest || len(rings) == 1 {
			totalArea += a
		}
	}

	var rep []osmmini.Coord
	maxA := 0.0
	for _, r := range rings {
		a := polygonAreaOnSphere(r)
		if a > maxA {
			maxA = a
			rep = r
		}
	}
	return totalArea, rep, nil
}

// assembleRingsFromWays stitches ways into closed rings when possible.
func assembleRingsFromWays(ways []osmmini.Way, nodes map[int64]osmmini.Coord) [][]osmmini.Coord {
	// map start/end node -> list of ways indices
	startMap := make(map[int64][]int)
	endMap := make(map[int64][]int)
	for i, w := range ways {
		if len(w.NodeIDs) == 0 {
			continue
		}
		start := w.NodeIDs[0]
		end := w.NodeIDs[len(w.NodeIDs)-1]
		startMap[start] = append(startMap[start], i)
		endMap[end] = append(endMap[end], i)
	}

	used := make([]bool, len(ways))
	rings := make([][]osmmini.Coord, 0)

	for i := range ways {
		if used[i] || len(ways[i].NodeIDs) == 0 {
			continue
		}
		// start a chain
		chain := append([]int{}, i)
		used[i] = true
		// forward chain
		curEnd := ways[i].NodeIDs[len(ways[i].NodeIDs)-1]
		for {
			nextIdx := -1
			if lst, ok := startMap[curEnd]; ok {
				for _, cand := range lst {
					if !used[cand] {
						nextIdx = cand
						break
					}
				}
			}
			if nextIdx == -1 {
				break
			}
			used[nextIdx] = true
			chain = append(chain, nextIdx)
			curEnd = ways[nextIdx].NodeIDs[len(ways[nextIdx].NodeIDs)-1]
		}
		// try to close chain: if chain start equals chain end, build ring
		firstStart := ways[chain[0]].NodeIDs[0]
		lastEnd := ways[chain[len(chain)-1]].NodeIDs[len(ways[chain[len(chain)-1]].NodeIDs)-1]
		if firstStart == lastEnd {
			// build coords
			coords := make([]osmmini.Coord, 0)
			for _, idx := range chain {
				for _, nid := range ways[idx].NodeIDs {
					if c, ok := nodes[nid]; ok {
						coords = append(coords, c)
					}
				}
			}
			// ensure closed
			if len(coords) > 2 && (coords[0] != coords[len(coords)-1]) {
				coords = append(coords, coords[0])
			}
			if len(coords) > 2 {
				rings = append(rings, coords)
			}
		}
	}
	return rings
}

// polygonAreaOnSphere computes area in square meters using spherical excess
// approximation. Coordinates should form a closed ring.
func polygonAreaOnSphere(coords []osmmini.Coord) float64 {
	if len(coords) < 3 {
		return 0
	}
	// use algorithm from "Some Algorithms for Polygons on a Sphere" (Robert Chamberlain)
	R := 6378137.0
	total := 0.0
	for i := 0; i < len(coords)-1; i++ {
		lon1 := deg2rad(coords[i].Lon)
		lat1 := deg2rad(coords[i].Lat)
		lon2 := deg2rad(coords[i+1].Lon)
		lat2 := deg2rad(coords[i+1].Lat)
		total += (lon2 - lon1) * (2 + math.Sin(lat1) + math.Sin(lat2))
	}
	area := math.Abs(total) * (R * R) / 2.0
	return area
}

// deg2rad converts degrees to radians.
func deg2rad(d float64) float64 { return d * math.Pi / 180.0 }

// createHybridFileServer returns a handler that serves from localDir if it exists,
// otherwise falls back to embedded files.
func createHybridFileServer(localDir, embeddedDir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Try local file first
		localPath := filepath.Join(localDir, r.URL.Path)
		if info, err := os.Stat(localPath); err == nil && !info.IsDir() {
			http.ServeFile(w, r, localPath)
			return
		}

		// Also try under a "static" subdirectory (some assets live in cmd/web/static)
		localPath2 := filepath.Join(localDir, "static", r.URL.Path)
		if info, err := os.Stat(localPath2); err == nil && !info.IsDir() {
			http.ServeFile(w, r, localPath2)
			return
		}

		// Fall back to embedded; try both direct path and under embeddedDir/static
		embedFS, _ := fs.Sub(embedded, embeddedDir)
		// Try direct
		if f, err := fs.Stat(embedFS, r.URL.Path); err == nil && !f.IsDir() {
			http.FileServer(http.FS(embedFS)).ServeHTTP(w, r)
			return
		}
		// Try embeddedDir/static/<path>
		embedFS2, _ := fs.Sub(embedded, filepath.Join(embeddedDir, "static"))
		if f, err := fs.Stat(embedFS2, r.URL.Path); err == nil && !f.IsDir() {
			http.FileServer(http.FS(embedFS2)).ServeHTTP(w, r)
			return
		}
		// default: serve (this will produce 404)
		http.FileServer(http.FS(embedFS)).ServeHTTP(w, r)
	})
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t0 := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s (%s)", r.Method, r.URL.Path, time.Since(t0))
	})
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeJSONErrorDetails(w http.ResponseWriter, status int, msg string, details map[string]any) {
	out := map[string]any{"error": msg}
	for k, v := range details {
		out[k] = v
	}
	writeJSON(w, status, out)
}

func readJSON(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("unexpected extra json")
	}
	return nil
}

// ---- Handlers ----

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// Attempt to refresh settings from disk before serving the page.
	// Any load error is intentionally ignored — Get() returns the last
	// successfully loaded settings in that case.
	_ = s.settings.Load()

	settings := s.settings.Get()
	settingsJSON, _ := json.Marshal(settings)

	_ = s.indexTmpl.Execute(w, map[string]any{
		"Version":  buildVersion,
		"Settings": string(settingsJSON),
	})
}

// handleAgentQuery implements a lightweight local retrieval -> action controller
// for agent-style queries. It returns structured JSON actions conforming to
// docs/agentic-maps/LLM_ACTION_SCHEMA.json. This is intentionally local-only
// (no external LLM) and handles common "nearest X" queries.
func (s *server) handleAgentQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req aiQueryRequest
	if err := readJSON(w, r, &req, maxAIResponseBytes); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		writeJSONError(w, http.StatusBadRequest, "prompt required")
		return
	}

	lower := strings.ToLower(prompt)

	// only implement a simple nearest-X handler here; other queries fall back
	// to a noop action with a hint reply.
	if strings.Contains(lower, "nächste") || strings.Contains(lower, "nächster") || strings.Contains(lower, "nearest") || strings.Contains(lower, "near me") || strings.Contains(lower, "in der nähe") {
		// determine query coords (prefer explicit user geolocation)
		coordFound := false
		var qlat, qlon float64
		if req.UserLat != nil && req.UserLon != nil {
			qlat, qlon = *req.UserLat, *req.UserLon
			coordFound = true
		} else if req.Lat != nil && req.Lon != nil {
			qlat, qlon = *req.Lat, *req.Lon
			coordFound = true
		} else if req.MapLat != nil && req.MapLon != nil {
			qlat, qlon = *req.MapLat, *req.MapLon
			coordFound = true
		}

		if !coordFound {
			// ask user for location via ask_user action
			actions := []any{map[string]any{"type": "ask_user", "params": map[string]any{"prompt": "Bitte gib deinen Standort an (lat,lon) oder erlaube Standortfreigabe."}}}
			writeJSON(w, http.StatusOK, map[string]any{"actions": actions, "reply": "Wo bist du gerade? Bitte Standort angeben oder Standortfreigabe erlauben.", "session_id": req.Session})
			return
		}

		// extract place term by removing common phrases and coords
		place := strings.TrimSpace(prompt)
		pats := []string{"wo ist der nächste", "wo ist der nächster", "where is the nearest", "nearest", "nächste", "nächster", "in der nähe", "in der Nähe", "near me", "wo ist", "wo", "ist", "der", "die", "das"}
		for _, p := range pats {
			place = strings.ReplaceAll(strings.ToLower(place), strings.ToLower(p), "")
		}
		// strip simple coord patterns
		re := regexp.MustCompile(`([-+]?[0-9]*\.?[0-9]+)\s*,?\s*([-+]?[0-9]*\.?[0-9]+)`)
		place = re.ReplaceAllString(place, "")
		place = strings.Trim(place, " ?.!")

		// fallback: if empty place, ask user
		if place == "" {
			actions := []any{map[string]any{"type": "ask_user", "params": map[string]any{"prompt": "Welches Ziel suchst du? (z. B. 'McDonald's')"}}}
			writeJSON(w, http.StatusOK, map[string]any{"actions": actions, "reply": "Welches Ziel meinst du? Zum Beispiel 'McDonald's'.", "session_id": req.Session})
			return
		}

		// search addresses using existing helpers
		matches := []osmmini.AddressEntry{}
		aq := osmmini.ParseAddressGuess(place)
		matches = osmmini.SearchAddresses(s.addrs, aq, 200)

		// fuzzy fallback over address names/brands
		if len(matches) == 0 {
			placeNorm := normalizeForCompare(place)
			s.poiMu.RLock()
			for _, a := range s.addrs {
				nameNorm := normalizeForCompare(a.Tags["name"])
				brandNorm := normalizeForCompare(a.Tags["brand"])
				if nameNorm != "" && strings.Contains(nameNorm, placeNorm) {
					matches = append(matches, a)
					continue
				}
				if brandNorm != "" && strings.Contains(brandNorm, placeNorm) {
					matches = append(matches, a)
					continue
				}
				if canon, ok := brandAliasMap[placeNorm]; ok {
					if brandNorm != "" && strings.Contains(brandNorm, canon) {
						matches = append(matches, a)
						continue
					}
				}
				// heuristic for short queries (e.g., 'maci') and fast_food
				if len(placeNorm) <= 6 && a.Tags["amenity"] == "fast_food" {
					if strings.Contains(nameNorm, "mcdonald") || strings.Contains(brandNorm, "mcdonald") {
						matches = append(matches, a)
						continue
					}
				}
			}
			s.poiMu.RUnlock()
		}

		if len(matches) == 0 {
			// no match found
			actions := []any{map[string]any{"type": "noop"}}
			writeJSON(w, http.StatusOK, map[string]any{"actions": actions, "reply": "Kein passendes Ziel gefunden für: " + place, "session_id": req.Session})
			return
		}

		// choose nearest
		bestIdx := -1
		bestDist := math.MaxFloat64
		for i, m := range matches {
			d := haversineMeters(qlat, qlon, m.Coord.Lat, m.Coord.Lon)
			if d < bestDist {
				bestDist = d
				bestIdx = i
			}
		}
		if bestIdx == -1 {
			writeJSONError(w, http.StatusNotFound, "Kein passendes Ziel gefunden")
			return
		}
		best := matches[bestIdx]

		// Build structured actions: highlight, show_info, compute_route
		actions := []any{
			map[string]any{"type": "highlight_poi", "params": map[string]any{"id": best.ID}},
			map[string]any{"type": "show_info", "params": map[string]any{"id": best.ID}},
			map[string]any{"type": "compute_route", "params": map[string]any{"from": map[string]float64{"lat": qlat, "lon": qlon}, "to": map[string]any{"id": best.ID}}},
		}

		reply := fmt.Sprintf("Ich habe '%s' (Entfernung %.0f m) gefunden.", formatAddressLabel(best.Tags), bestDist)
		// create/echo session id
		sid := req.Session
		if sid == "" {
			sid = fmt.Sprintf("s-%d", time.Now().UnixNano())
		}
		_ = s.appendSessionMessage(sid, "user", prompt)
		_ = s.appendSessionMessage(sid, "assistant", reply)

		writeJSON(w, http.StatusOK, map[string]any{"actions": actions, "reply": reply, "session_id": sid})
		return
	}

	// fallback: noop with hint the agent can't handle this locally
	writeJSON(w, http.StatusOK, map[string]any{"actions": []any{map[string]any{"type": "noop"}}, "reply": "Ich kann diese Anfrage derzeit nicht lokal beantworten. Bitte formuliere die Frage anders oder nutze die AI-Abfrage.", "session_id": req.Session})
}

// handleAgentExecute validates and optionally executes an action bundle
// posted by the client or produced by an LLM. Validation is intentionally
// lightweight (checks action types + required params) and prevents
// obviously malformed or dangerous payloads from being executed.
func (s *server) handleAgentExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req map[string]any
	if err := readJSON(w, r, &req, 1<<20); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	// extract fields
	actionsRaw, _ := req["actions"]
	sessionID, _ := req["session_id"].(string)
	confirm, _ := req["confirm"].(bool)
	dryRun, _ := req["dry_run"].(bool)

	actionsSlice, ok := actionsRaw.([]any)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "missing or invalid actions array")
		return
	}

	// validate actions
	validated := make([]map[string]any, 0, len(actionsSlice))
	for i, a := range actionsSlice {
		m, ok := a.(map[string]any)
		if !ok {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("action %d: invalid action object", i))
			return
		}
		t, ok := m["type"].(string)
		if !ok || t == "" {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("action %d: missing type", i))
			return
		}
		params, _ := m["params"].(map[string]any)

		// simple required-field checks per action type
		switch t {
		case "highlight_poi", "show_info":
			// require id
			if params == nil {
				writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("action %d: params required for %s", i, t))
				return
			}
			if _, ok := params["id"]; !ok {
				writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("action %d: missing id param for %s", i, t))
				return
			}
		case "compute_route":
			if params == nil {
				writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("action %d: params required for %s", i, t))
				return
			}
			if _, ok := params["to"]; !ok {
				writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("action %d: missing to param for compute_route", i))
				return
			}
		case "ask_user", "noop", "zoom_to", "add_waypoint":
			// no strong validation here
		default:
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("action %d: unknown action type: %s", i, t))
			return
		}
		// normalize: ensure params present
		if params == nil {
			params = map[string]any{}
			m["params"] = params
		}
		validated = append(validated, m)
	}

	// If only dry run requested or not confirmed, return validation result
	if dryRun || !confirm {
		resp := map[string]any{"validated_actions": validated, "session_id": sessionID, "requires_confirm": !confirm}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// execute actions sequentially; collect results for client
	results := make([]any, 0, len(validated))
	for _, act := range validated {
		t := act["type"].(string)
		params := act["params"].(map[string]any)
		switch t {
		case "highlight_poi":
			// client-side visual; server returns poi info for client to render
			idf := params["id"]
			var idint int64
			switch v := idf.(type) {
			case float64:
				idint = int64(v)
			case int64:
				idint = v
			case int:
				idint = int64(v)
			case string:
				if parsed, err := strconv.ParseInt(v, 10, 64); err == nil {
					idint = parsed
				}
			}
			if idint == 0 {
				results = append(results, map[string]any{"error": "invalid id"})
				continue
			}
			// fetch POI summary to return to client
			s.poiMu.RLock()
			var out any
			if wv, ok := s.poiWays[idint]; ok {
				out = map[string]any{"id": idint, "kind": "way", "tags": wv.Tags}
			} else if nv, ok := s.poiNodes[idint]; ok {
				out = map[string]any{"id": idint, "kind": "node", "lat": nv.Lat, "lon": nv.Lon}
			} else if rv, ok := s.poiRels[idint]; ok {
				out = map[string]any{"id": idint, "kind": "rel", "tags": rv.Tags}
			} else {
				out = map[string]any{"error": "not found"}
			}
			s.poiMu.RUnlock()
			results = append(results, map[string]any{"type": t, "result": out})
		case "show_info":
			// return full POI info like GET /api/v1/poi/{id}
			idf := params["id"]
			var idint int64
			switch v := idf.(type) {
			case float64:
				idint = int64(v)
			case int64:
				idint = v
			case int:
				idint = int64(v)
			case string:
				if parsed, err := strconv.ParseInt(v, 10, 64); err == nil {
					idint = parsed
				}
			}
			if idint == 0 {
				results = append(results, map[string]any{"error": "invalid id"})
				continue
			}
			// reuse existing handler logic to build poiInfoResponse-ish map
			s.poiMu.RLock()
			if wv, ok := s.poiWays[idint]; ok {
				// compute centroid quickly
				var cx, cy float64
				var cnt int
				for _, nid := range wv.NodeIDs {
					if c, ok := s.poiNodes[nid]; ok {
						cx += c.Lat
						cy += c.Lon
						cnt++
					}
				}
				lat, lon := 0.0, 0.0
				if cnt > 0 {
					lat = cx / float64(cnt)
					lon = cy / float64(cnt)
				}
				out := map[string]any{"id": idint, "kind": "way", "label": wv.Tags["name"], "lat": lat, "lon": lon, "tags": wv.Tags}
				s.poiMu.RUnlock()
				results = append(results, map[string]any{"type": t, "result": out})
				continue
			}
			if nv, ok := s.poiNodes[idint]; ok {
				out := map[string]any{"id": idint, "kind": "node", "lat": nv.Lat, "lon": nv.Lon}
				s.poiMu.RUnlock()
				results = append(results, map[string]any{"type": t, "result": out})
				continue
			}
			if rv, ok := s.poiRels[idint]; ok {
				s.poiMu.RUnlock()
				// simple relation handling
				out := map[string]any{"id": idint, "kind": "rel", "tags": rv.Tags}
				results = append(results, map[string]any{"type": t, "result": out})
				continue
			}
			s.poiMu.RUnlock()
			results = append(results, map[string]any{"error": "not found"})
		case "compute_route":
			// params: from (lat/lon or query), to (id or lat/lon or query), options (optional)
			// build RouteRequest and reuse routing internals similar to handleRoute
			pr := RouteRequest{}
			if pf, ok := params["from"]; ok {
				if pm, ok2 := pf.(map[string]any); ok2 {
					if latv, ok3 := pm["lat"].(float64); ok3 {
						if lonv, ok4 := pm["lon"].(float64); ok4 {
							pr.From.Lat = &latv
							pr.From.Lon = &lonv
						}
					}
					if q, okq := pm["query"].(string); okq {
						pr.From.Query = q
					}
				}
			}
			if pt, ok := params["to"]; ok {
				if pm, ok2 := pt.(map[string]any); ok2 {
					if idv, okid := pm["id"]; okid {
						// try to resolve id to coords
						var idint int64
						switch v := idv.(type) {
						case float64:
							idint = int64(v)
						case int64:
							idint = v
						case int:
							idint = int64(v)
						case string:
							if parsed, err := strconv.ParseInt(v, 10, 64); err == nil {
								idint = parsed
							}
						}
						if idint != 0 {
							s.poiMu.RLock()
							if wv, ok := s.poiWays[idint]; ok {
								var cx, cy float64
								var cnt int
								for _, nid := range wv.NodeIDs {
									if c, ok := s.poiNodes[nid]; ok {
										cx += c.Lat
										cy += c.Lon
										cnt++
									}
								}
								if cnt > 0 {
									lat := cx / float64(cnt)
									lon := cy / float64(cnt)
									pr.To.Lat = &lat
									pr.To.Lon = &lon
								}
							} else if nv, ok := s.poiNodes[idint]; ok {
								lat := nv.Lat
								lon := nv.Lon
								pr.To.Lat = &lat
								pr.To.Lon = &lon
							}
							s.poiMu.RUnlock()
						}
					} else {
						if latv, ok3 := pm["lat"].(float64); ok3 {
							if lonv, ok4 := pm["lon"].(float64); ok4 {
								pr.To.Lat = &latv
								pr.To.Lon = &lonv
							}
						}
						if q, okq := pm["query"].(string); okq {
							pr.To.Query = q
						}
					}
				}
			}
			// set options if present
			if optv, ok := params["options"].(map[string]any); ok && optv != nil {
				// very small subset: profile
				var ro osmmini.RouteOptions
				if profile, ok := optv["profile"].(string); ok {
					ro.Profile = osmmini.VehicleProfile(profile)
				}
				pr.Options = &ro
			}

			// try to resolve via existing helpers
			fromCoord, _, _, err1 := s.resolveLocation(pr.From)
			if err1 != nil {
				results = append(results, map[string]any{"type": t, "error": "from: " + err1.Error()})
				continue
			}
			toCoord, _, _, err2 := s.resolveLocation(pr.To)
			if err2 != nil {
				results = append(results, map[string]any{"type": t, "error": "to: " + err2.Error()})
				continue
			}

			startID, _, ok := s.router.NearestNode(fromCoord.Lat, fromCoord.Lon)
			if !ok {
				results = append(results, map[string]any{"type": t, "error": "no start node found"})
				continue
			}
			endID, _, ok := s.router.NearestNode(toCoord.Lat, toCoord.Lon)
			if !ok {
				results = append(results, map[string]any{"type": t, "error": "no end node found"})
				continue
			}

			opt := s.settings.Get().Routing
			if pr.Options != nil {
				opt = *pr.Options
			}
			res, err := s.router.RouteWithOptions(r.Context(), startID, endID, opt)
			if err != nil {
				results = append(results, map[string]any{"type": t, "error": err.Error()})
				continue
			}
			// build minimal response payload similar to RouteResponse
			rr := map[string]any{
				"engine":     string(res.Engine),
				"objective":  string(res.Objective),
				"distance_m": res.DistanceM,
				"duration_s": res.DurationS,
				"path":       res.PathCoords,
			}
			results = append(results, map[string]any{"type": t, "result": rr})
		default:
			results = append(results, map[string]any{"type": act["type"], "note": "no-op on server"})
		}
	}

	// persist session messages if provided
	if sessionID != "" {
		_ = s.appendSessionMessage(sessionID, "assistant", fmt.Sprintf("executed %d actions", len(validated)))
	}

	writeJSON(w, http.StatusOK, map[string]any{"executed": true, "results": results, "session_id": sessionID})
}

func (s *server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(s.openAPI)
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	uptime := time.Since(s.startedAt).Seconds()
	set := s.settings.Get()

	out := map[string]any{
		"uptime_s":  uptime,
		"graph":     map[string]any{"nodes": s.router.NodeCount(), "edges": s.router.EdgeCount()},
		"addresses": len(s.addrs),
		"settings":  set,
		"route_cache": map[string]any{
			"size": s.routeCache.Size(),
		},
		"tile_cache": s.tiles.Stats(),
	}
	if s.window != nil {
		out["window"] = s.window
		out["enforce_window"] = s.enforceWindow
	}
	if b, ok := s.router.Bounds(); ok {
		out["graph_bounds"] = b
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleTileSources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, BuiltinTilePresets)
}

func (s *server) handleProfiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, osmmini.BuiltinProfiles)
}

func (s *server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.settings.Get())
		return
	case http.MethodPut:
		var v Settings
		if err := readJSON(w, r, &v, 1<<20); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		if v.Tiles.CacheDir == "" {
			writeJSONError(w, http.StatusBadRequest, "tiles.cache_dir required")
			return
		}
		// upstream is required for raster/wms; for vector map_type, style_url is used instead
		if v.Tiles.Upstream == "" && v.Tiles.StyleURL == "" {
			writeJSONError(w, http.StatusBadRequest, "tiles.upstream or tiles.style_url required")
			return
		}
		if err := s.settings.Put(v); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.tiles.Update(v.Tiles)
		writeJSON(w, http.StatusOK, v)
		return
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
}

func (s *server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusOK, []apiSearchResult{})
		return
	}
	limit := parseLimit(r.URL.Query().Get("limit"), 10, 1, 50)
	aq := osmmini.ParseAddressGuess(q)
	addrMatches := osmmini.SearchAddresses(s.addrs, aq, limit)

	out := make([]apiSearchResult, 0, limit)
	for _, m := range addrMatches {
		if s.window != nil && !s.window.Contains(m.Coord) {
			continue
		}
		out = append(out, apiSearchResult{
			ID:    m.ID,
			Kind:  "address",
			Label: formatAddressLabel(m.Tags),
			Lat:   m.Coord.Lat,
			Lon:   m.Coord.Lon,
			Tags:  m.Tags,
		})
	}

	// Also search named POI ways (amenity, shop, tourism, etc.) if we still
	// have room and the POI index is available.
	if remain := limit - len(out); remain > 0 {
		qNorm := normalizeForCompare(q)
		s.poiMu.RLock()
		for _, w := range s.poiWays {
			if len(out) >= limit {
				break
			}
			name := w.Tags["name"]
			brand := w.Tags["brand"]
			if name == "" && brand == "" {
				continue
			}
			nameNorm := normalizeForCompare(name)
			brandNorm := normalizeForCompare(brand)
			if qNorm == "" || (!strings.Contains(nameNorm, qNorm) && !strings.Contains(brandNorm, qNorm)) {
				continue
			}
			// compute centroid
			var cx, cy float64
			var cnt int
			for _, nid := range w.NodeIDs {
				if c, ok := s.poiNodes[nid]; ok {
					cx += c.Lat
					cy += c.Lon
					cnt++
				}
			}
			if cnt == 0 {
				continue
			}
			coord := osmmini.Coord{Lat: cx / float64(cnt), Lon: cy / float64(cnt)}
			if s.window != nil && !s.window.Contains(coord) {
				continue
			}
			label := name
			if label == "" {
				label = brand
			}
			out = append(out, apiSearchResult{
				ID:    w.ID,
				Kind:  "poi",
				Label: label,
				Lat:   coord.Lat,
				Lon:   coord.Lon,
				Tags:  w.Tags,
			})
		}
		s.poiMu.RUnlock()
	}

	remain := limit - len(out)
	if remain > 0 {
		streetMatches := s.router.SearchStreets(q, remain)
		for _, st := range streetMatches {
			if s.window != nil && !s.window.Contains(st.Coord) {
				continue
			}
			out = append(out, apiSearchResult{
				ID:    st.NodeID,
				Kind:  "street",
				Label: st.Name,
				Lat:   st.Coord.Lat,
				Lon:   st.Coord.Lon,
				Tags:  osmmini.Tags{"street": st.Name},
			})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req RouteRequest
	if err := readJSON(w, r, &req, 1<<20); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	opt := s.settings.Get().Routing
	if req.Options != nil {
		opt = *req.Options
	}

	fromCoord, fromLabel, fromInput, err := s.resolveLocation(req.From)
	if err != nil {
		var lr *locationResolveError
		if errors.As(err, &lr) {
			writeJSONErrorDetails(w, http.StatusBadRequest, "from: "+lr.Error(), map[string]any{
				"target":      "from",
				"query":       lr.Query,
				"ambiguous":   lr.Ambiguous,
				"suggestions": lr.Suggestions,
			})
			return
		}
		writeJSONError(w, http.StatusBadRequest, "from: "+err.Error())
		return
	}
	toCoord, toLabel, toInput, err := s.resolveLocation(req.To)
	if err != nil {
		var lr *locationResolveError
		if errors.As(err, &lr) {
			writeJSONErrorDetails(w, http.StatusBadRequest, "to: "+lr.Error(), map[string]any{
				"target":      "to",
				"query":       lr.Query,
				"ambiguous":   lr.Ambiguous,
				"suggestions": lr.Suggestions,
			})
			return
		}
		writeJSONError(w, http.StatusBadRequest, "to: "+err.Error())
		return
	}

	startID, startDist, ok := s.router.NearestNode(fromCoord.Lat, fromCoord.Lon)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "no start node found")
		return
	}
	endID, endDist, ok := s.router.NearestNode(toCoord.Lat, toCoord.Lon)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "no end node found")
		return
	}

	// Check route cache before running the (potentially expensive) pathfinder.
	cacheKey := s.routeCache.cacheKey(startID, endID, opt)
	if cached, hit := s.routeCache.get(cacheKey); hit {
		// Update from/to labels (they depend on the query string, not the node)
		cached.From = RoutePoint{Input: fromInput, Label: fromLabel, Lat: fromCoord.Lat, Lon: fromCoord.Lon, Node: startID, SnapM: startDist}
		cached.To = RoutePoint{Input: toInput, Label: toLabel, Lat: toCoord.Lat, Lon: toCoord.Lon, Node: endID, SnapM: endDist}
		cached.Cached = true
		writeJSON(w, http.StatusOK, cached)
		return
	}

	res, err := s.router.RouteWithOptions(r.Context(), startID, endID, opt)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(res.PathCoords) == 0 {
		writeJSONError(w, http.StatusInternalServerError, "empty path")
		return
	}

	gURL := buildGoogleMapsURL([]osmmini.Coord{fromCoord, toCoord}, 0)
	aURL := buildAppleMapsURL([]osmmini.Coord{fromCoord, toCoord})

	// generate maneuvers/steps for the frontend
	steps := s.router.ManeuversForPath(res.Path, opt)

	resp := RouteResponse{
		From: RoutePoint{
			Input: fromInput,
			Label: fromLabel,
			Lat:   fromCoord.Lat,
			Lon:   fromCoord.Lon,
			Node:  startID,
			SnapM: startDist,
		},
		To: RoutePoint{
			Input: toInput,
			Label: toLabel,
			Lat:   toCoord.Lat,
			Lon:   toCoord.Lon,
			Node:  endID,
			SnapM: endDist,
		},
		Engine:        string(res.Engine),
		Objective:     string(res.Objective),
		Profile:       string(opt.Profile),
		Cost:          res.Cost,
		DistanceM:     res.DistanceM,
		DurationS:     res.DurationS,
		Path:          res.PathCoords,
		GoogleMapsURL: gURL,
		AppleMapsURL:  aURL,
		Steps:         steps,
	}
	s.routeCache.set(cacheKey, resp)
	writeJSON(w, http.StatusOK, resp)
}

func (s *server) handleTripSolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req TripSolveRequest
	if err := readJSON(w, r, &req, 4<<20); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	opt := s.settings.Get().Routing
	if req.Options != nil {
		opt = *req.Options
	}

	resp, err := s.solveTrip(r.Context(), req.Plan, opt)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---- Trip / TSP ----

type stopR struct {
	id    string
	label string
	coord osmmini.Coord
	node  int64
	snap  float64
}

func (s *server) solveTrip(ctx context.Context, plan TripPlan, opt osmmini.RouteOptions) (TripSolveResponse, error) {
	startCoord, startLabel, _, err := s.resolveLocation(plan.Start)
	if err != nil {
		return TripSolveResponse{}, fmt.Errorf("start: %w", err)
	}
	startNode, startSnap, ok := s.router.NearestNode(startCoord.Lat, startCoord.Lon)
	if !ok {
		return TripSolveResponse{}, errors.New("start: no graph node found")
	}

	var endLoc Location
	if plan.Loop || plan.End == nil {
		endLoc = plan.Start
	} else {
		endLoc = *plan.End
	}
	endCoord, endLabel, _, err := s.resolveLocation(endLoc)
	if err != nil {
		return TripSolveResponse{}, fmt.Errorf("end: %w", err)
	}
	endNode, endSnap, ok := s.router.NearestNode(endCoord.Lat, endCoord.Lon)
	if !ok {
		return TripSolveResponse{}, errors.New("end: no graph node found")
	}

	if len(plan.Stops) > 60 {
		return TripSolveResponse{}, errors.New("too many stops (max 60)")
	}

	stops := make([]stopR, 0, len(plan.Stops))
	seen := map[string]bool{}
	for i, st := range plan.Stops {
		id := strings.TrimSpace(st.ID)
		if id == "" {
			id = fmt.Sprintf("S%d", i+1)
		}
		if seen[id] {
			return TripSolveResponse{}, fmt.Errorf("duplicate stop id: %s", id)
		}
		seen[id] = true

		c, lab, in, err := s.resolveLocation(st.Location)
		if err != nil {
			return TripSolveResponse{}, fmt.Errorf("stop %s: %w", id, err)
		}
		n, snap, ok := s.router.NearestNode(c.Lat, c.Lon)
		if !ok {
			return TripSolveResponse{}, fmt.Errorf("stop %s: no graph node found", id)
		}
		_ = in
		stops = append(stops, stopR{id: id, label: lab, coord: c, node: n, snap: snap})
	}

	n := len(stops)
	idToIdx := make(map[string]int, n)
	for i := range stops {
		idToIdx[stops[i].id] = i
	}

	pre := make([]uint64, n)
	for _, d := range plan.Dependencies {
		a := strings.TrimSpace(d.Before)
		b := strings.TrimSpace(d.After)
		ia, okA := idToIdx[a]
		ib, okB := idToIdx[b]
		if !okA || !okB {
			return TripSolveResponse{}, fmt.Errorf("dependency refers to unknown id: %q -> %q", a, b)
		}
		pre[ib] |= (1 << uint64(ia))
	}

	var orderIdx []int
	if n == 0 {
		orderIdx = nil
	} else if !plan.Optimize {
		mask := uint64(0)
		for _, st := range stops {
			i := idToIdx[st.id]
			if (mask & pre[i]) != pre[i] {
				return TripSolveResponse{}, fmt.Errorf("dependency violated before visiting %s", st.id)
			}
			mask |= 1 << uint64(i)
		}
		orderIdx = make([]int, n)
		for i := 0; i < n; i++ {
			orderIdx[i] = i
		}
	} else {
		if n <= 16 {
			ord, err := s.tspExact(ctx, startNode, endNode, stops, pre, opt)
			if err != nil {
				return TripSolveResponse{}, err
			}
			orderIdx = ord
		} else {
			ord, err := s.tspGreedy(ctx, startNode, endNode, stops, pre, opt)
			if err != nil {
				return TripSolveResponse{}, err
			}
			orderIdx = ord
		}
	}

	points := make([]stopR, 0, n+2)
	points = append(points, stopR{id: "START", label: startLabel, coord: startCoord, node: startNode, snap: startSnap})
	for _, i := range orderIdx {
		points = append(points, stops[i])
	}
	points = append(points, stopR{id: "END", label: endLabel, coord: endCoord, node: endNode, snap: endSnap})

	totalDist, totalDur, totalCost := 0.0, 0.0, 0.0
	legs := make([]RouteResponse, 0, len(points)-1)
	combined := make([]osmmini.Coord, 0, 4096)

	for i := 0; i < len(points)-1; i++ {
		a := points[i]
		b := points[i+1]
		rr, err := s.router.RouteWithOptions(ctx, a.node, b.node, opt)
		if err != nil {
			return TripSolveResponse{}, fmt.Errorf("leg %s->%s: %w", a.id, b.id, err)
		}
		if len(rr.PathCoords) == 0 {
			return TripSolveResponse{}, fmt.Errorf("leg %s->%s: empty path", a.id, b.id)
		}

		legs = append(legs, RouteResponse{
			From:          RoutePoint{Input: a.id, Label: a.label, Lat: a.coord.Lat, Lon: a.coord.Lon, Node: a.node, SnapM: a.snap},
			To:            RoutePoint{Input: b.id, Label: b.label, Lat: b.coord.Lat, Lon: b.coord.Lon, Node: b.node, SnapM: b.snap},
			Engine:        string(rr.Engine),
			Objective:     string(rr.Objective),
			Cost:          rr.Cost,
			DistanceM:     rr.DistanceM,
			DurationS:     rr.DurationS,
			Path:          rr.PathCoords,
			GoogleMapsURL: buildGoogleMapsURL([]osmmini.Coord{a.coord, b.coord}, 0),
			AppleMapsURL:  buildAppleMapsURL([]osmmini.Coord{a.coord, b.coord}),
		})

		totalDist += rr.DistanceM
		totalDur += rr.DurationS
		totalCost += rr.Cost

		if i == 0 {
			combined = append(combined, rr.PathCoords...)
		} else {
			combined = append(combined, rr.PathCoords[1:]...)
		}
	}

	orderIDs := make([]string, 0, len(orderIdx))
	resStops := make([]TripStopResolved, 0, len(orderIdx))
	for _, i := range orderIdx {
		orderIDs = append(orderIDs, stops[i].id)
		resStops = append(resStops, TripStopResolved{
			ID:    stops[i].id,
			Label: stops[i].label,
			Lat:   stops[i].coord.Lat,
			Lon:   stops[i].coord.Lon,
			Node:  stops[i].node,
			SnapM: stops[i].snap,
		})
	}

	tripCoords := make([]osmmini.Coord, 0, len(points))
	for _, p := range points {
		tripCoords = append(tripCoords, p.coord)
	}

	return TripSolveResponse{
		Engine:        string(opt.Engine),
		Objective:     string(opt.Objective),
		Order:         orderIDs,
		Stops:         resStops,
		DistanceM:     totalDist,
		DurationS:     totalDur,
		Cost:          totalCost,
		Path:          combined,
		Legs:          legs,
		GoogleMapsURL: buildGoogleMapsURL(tripCoords, 9),
		AppleMapsURL:  buildAppleMapsURL(tripCoords),
	}, nil
}

func (s *server) tspExact(ctx context.Context, startNode, endNode int64, stops []stopR, pre []uint64, opt osmmini.RouteOptions) ([]int, error) {
	n := len(stops)
	if n == 0 {
		return nil, nil
	}

	inf := math.MaxFloat64 / 4

	costStart := make([]float64, n)
	costEnd := make([]float64, n)
	costBetween := make([][]float64, n)
	for i := 0; i < n; i++ {
		costBetween[i] = make([]float64, n)
	}

	for i := 0; i < n; i++ {
		c, err := s.router.RouteCostWithOptions(ctx, startNode, stops[i].node, opt)
		if err != nil {
			costStart[i] = inf
		} else {
			costStart[i] = c
		}
		c2, err := s.router.RouteCostWithOptions(ctx, stops[i].node, endNode, opt)
		if err != nil {
			costEnd[i] = inf
		} else {
			costEnd[i] = c2
		}
	}

	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			if i == j {
				costBetween[i][j] = inf
				continue
			}
			c, err := s.router.RouteCostWithOptions(ctx, stops[i].node, stops[j].node, opt)
			if err != nil {
				costBetween[i][j] = inf
			} else {
				costBetween[i][j] = c
			}
		}
	}

	size := 1 << uint(n)
	dp := make([][]float64, size)
	par := make([][]int16, size)
	for m := 0; m < size; m++ {
		dp[m] = make([]float64, n)
		par[m] = make([]int16, n)
		for i := 0; i < n; i++ {
			dp[m][i] = inf
			par[m][i] = -1
		}
	}

	for i := 0; i < n; i++ {
		if pre[i] == 0 && costStart[i] < inf {
			m := 1 << uint(i)
			dp[m][i] = costStart[i]
			par[m][i] = -1
		}
	}

	for m := 0; m < size; m++ {
		for last := 0; last < n; last++ {
			if dp[m][last] >= inf {
				continue
			}
			for nxt := 0; nxt < n; nxt++ {
				if (m & (1 << uint(nxt))) != 0 {
					continue
				}
				if (uint64(m) & pre[nxt]) != pre[nxt] {
					continue
				}
				nm := m | (1 << uint(nxt))
				c := dp[m][last] + costBetween[last][nxt]
				if c < dp[nm][nxt] {
					dp[nm][nxt] = c
					par[nm][nxt] = int16(last)
				}
			}
		}
	}

	all := size - 1
	best := inf
	bestLast := -1
	for last := 0; last < n; last++ {
		if dp[all][last] >= inf || costEnd[last] >= inf {
			continue
		}
		c := dp[all][last] + costEnd[last]
		if c < best {
			best = c
			bestLast = last
		}
	}
	if bestLast == -1 {
		return nil, errors.New("tsp: no feasible order (unreachable legs or dependency cycle)")
	}

	order := make([]int, 0, n)
	m := all
	cur := bestLast
	for cur >= 0 {
		order = append(order, cur)
		p := par[m][cur]
		m = m &^ (1 << uint(cur))
		if p < 0 {
			break
		}
		cur = int(p)
	}
	for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
		order[i], order[j] = order[j], order[i]
	}
	return order, nil
}

func (s *server) tspGreedy(ctx context.Context, startNode, endNode int64, stops []stopR, pre []uint64, opt osmmini.RouteOptions) ([]int, error) {
	n := len(stops)
	if n == 0 {
		return nil, nil
	}
	visited := uint64(0)
	order := make([]int, 0, n)
	curNode := startNode

	for len(order) < n {
		best := math.MaxFloat64
		bestIdx := -1
		for i := 0; i < n; i++ {
			bit := uint64(1) << uint64(i)
			if (visited & bit) != 0 {
				continue
			}
			if (visited & pre[i]) != pre[i] {
				continue
			}
			c, err := s.router.RouteCostWithOptions(ctx, curNode, stops[i].node, opt)
			if err != nil {
				continue
			}
			if c < best {
				best = c
				bestIdx = i
			}
		}
		if bestIdx == -1 {
			return nil, errors.New("tsp: no eligible next stop (dependency cycle or unreachable stop)")
		}
		order = append(order, bestIdx)
		visited |= uint64(1) << uint64(bestIdx)
		curNode = stops[bestIdx].node
	}

	// 2-opt improvement: try swapping segments to find shorter tours
	if n >= 4 {
		order = s.tsp2opt(ctx, startNode, endNode, stops, pre, opt, order)
	}

	_ = endNode
	return order, nil
}

// tsp2opt applies the 2-opt local search improvement to the given tour order.
// It repeatedly reverses sub-segments of the tour if doing so reduces total cost,
// while respecting dependency constraints. Route costs are cached to avoid
// redundant pathfinding computations.
func (s *server) tsp2opt(ctx context.Context, startNode, endNode int64, stops []stopR, pre []uint64, opt osmmini.RouteOptions, order []int) []int {
	n := len(order)
	if n < 4 {
		return order
	}

	// Cache route costs between node pairs to avoid repeated pathfinding
	type nodePair struct{ from, to int64 }
	costCache := make(map[nodePair]float64)
	cachedCost := func(from, to int64) float64 {
		key := nodePair{from, to}
		if c, ok := costCache[key]; ok {
			return c
		}
		c, err := s.router.RouteCostWithOptions(ctx, from, to, opt)
		if err != nil {
			c = math.MaxFloat64
		}
		costCache[key] = c
		return c
	}

	// helper to compute total tour cost for a given order
	tourCost := func(ord []int) float64 {
		total := 0.0
		prev := startNode
		for _, idx := range ord {
			c := cachedCost(prev, stops[idx].node)
			if c >= math.MaxFloat64/4 {
				return math.MaxFloat64
			}
			total += c
			prev = stops[idx].node
		}
		c := cachedCost(prev, endNode)
		if c >= math.MaxFloat64/4 {
			return math.MaxFloat64
		}
		total += c
		return total
	}

	// check if an order respects all dependency constraints
	depsOK := func(ord []int) bool {
		mask := uint64(0)
		for _, idx := range ord {
			if (mask & pre[idx]) != pre[idx] {
				return false
			}
			mask |= 1 << uint64(idx)
		}
		return true
	}

	bestCost := tourCost(order)
	improved := true
	for improved {
		improved = false
		for i := 0; i < n-1; i++ {
			for j := i + 2; j < n; j++ {
				// try reversing the segment between i+1 and j
				newOrder := make([]int, n)
				copy(newOrder, order)
				for l, r := i+1, j; l < r; l, r = l+1, r-1 {
					newOrder[l], newOrder[r] = newOrder[r], newOrder[l]
				}
				if !depsOK(newOrder) {
					continue
				}
				newCost := tourCost(newOrder)
				if newCost < bestCost {
					order = newOrder
					bestCost = newCost
					improved = true
				}
			}
		}
	}
	return order
}

// ---- Location resolve + window enforcement ----

type locationResolveError struct {
	Message     string
	Query       string
	Ambiguous   bool
	Suggestions []apiSearchResult
}

func (e *locationResolveError) Error() string {
	if e == nil {
		return "location resolution failed"
	}
	return e.Message
}

func airportHint(raw string) bool {
	n := normalizeForCompare(raw)
	return strings.Contains(n, "flughafen") || strings.Contains(n, "airport")
}

func munichHint(raw string) bool {
	n := normalizeForCompare(raw)
	return strings.Contains(n, "muenchen") || strings.Contains(n, "munchen")
}

func containsMunichInTags(tags osmmini.Tags) bool {
	hay := normalizeForCompare(strings.Join([]string{
		tags["name"],
		tags["addr:city"],
		tags["addr:place"],
		tags["operator"],
		tags["brand"],
		tags["aeroway"],
	}, " "))
	return strings.Contains(hay, "muenchen") || strings.Contains(hay, "munchen")
}

func (s *server) buildLocationSuggestions(raw string, limit int) []apiSearchResult {
	if limit < 1 {
		limit = 1
	}
	if limit > 12 {
		limit = 12
	}

	aq := osmmini.ParseAddressGuess(raw)
	addrMatches := osmmini.SearchAddresses(s.addrs, aq, limit)
	out := make([]apiSearchResult, 0, limit)
	for _, m := range addrMatches {
		if s.window != nil && !s.window.Contains(m.Coord) {
			continue
		}
		out = append(out, apiSearchResult{
			ID:    m.ID,
			Kind:  "address",
			Label: formatAddressLabel(m.Tags),
			Lat:   m.Coord.Lat,
			Lon:   m.Coord.Lon,
			Tags:  m.Tags,
		})
		if len(out) >= limit {
			return out
		}
	}
	remain := limit - len(out)
	if remain > 0 {
		streets := s.router.SearchStreets(raw, remain)
		for _, st := range streets {
			if s.window != nil && !s.window.Contains(st.Coord) {
				continue
			}
			out = append(out, apiSearchResult{
				ID:    st.NodeID,
				Kind:  "street",
				Label: st.Name,
				Lat:   st.Coord.Lat,
				Lon:   st.Coord.Lon,
				Tags:  osmmini.Tags{"street": st.Name},
			})
			if len(out) >= limit {
				break
			}
		}
	}
	return out
}

func (s *server) resolveLocation(loc Location) (osmmini.Coord, string, string, error) {
	if loc.Lat != nil && loc.Lon != nil {
		lat, lon := *loc.Lat, *loc.Lon
		if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
			return osmmini.Coord{}, "", "", errors.New("invalid lat/lon")
		}
		c := osmmini.Coord{Lat: lat, Lon: lon}
		if s.enforceWindow && s.window != nil && !s.window.Contains(c) {
			return osmmini.Coord{}, "", "", errors.New("outside configured window")
		}
		return c, "Koordinate", fmt.Sprintf("%.6f %.6f", lat, lon), nil
	}

	raw := strings.TrimSpace(loc.Query)
	if raw == "" {
		return osmmini.Coord{}, "", "", errors.New("missing query or lat/lon")
	}
	if c, ok := parseLatLon(raw); ok {
		if s.enforceWindow && s.window != nil && !s.window.Contains(c) {
			return osmmini.Coord{}, "", "", errors.New("outside configured window")
		}
		return c, "Koordinate", raw, nil
	}

	q := osmmini.ParseAddressGuess(raw)
	if airportHint(raw) && munichHint(raw) {
		airportCandidates := osmmini.SearchAddresses(s.addrs, q, 20)
		munichCandidates := make([]osmmini.AddressEntry, 0, len(airportCandidates))
		for _, c := range airportCandidates {
			if containsMunichInTags(c.Tags) {
				munichCandidates = append(munichCandidates, c)
			}
		}
		if len(munichCandidates) == 1 {
			c := munichCandidates[0]
			if s.enforceWindow && s.window != nil && !s.window.Contains(c.Coord) {
				return osmmini.Coord{}, "", "", errors.New("outside configured window")
			}
			return c.Coord, formatAddressLabel(c.Tags), raw, nil
		}
		if len(munichCandidates) > 1 {
			suggestions := make([]apiSearchResult, 0, min(6, len(munichCandidates)))
			for i := 0; i < len(munichCandidates) && i < 6; i++ {
				c := munichCandidates[i]
				suggestions = append(suggestions, apiSearchResult{
					ID:    c.ID,
					Kind:  "address",
					Label: formatAddressLabel(c.Tags),
					Lat:   c.Coord.Lat,
					Lon:   c.Coord.Lon,
					Tags:  c.Tags,
				})
			}
			return osmmini.Coord{}, "", "", &locationResolveError{
				Message:     "mehrdeutiges Ziel, bitte auswählen",
				Query:       raw,
				Ambiguous:   true,
				Suggestions: suggestions,
			}
		}
		return osmmini.Coord{}, "", "", &locationResolveError{
			Message:     "Kein passendes Ziel gefunden",
			Query:       raw,
			Ambiguous:   false,
			Suggestions: s.buildLocationSuggestions(raw, 6),
		}
	}
	if addr, ok := osmmini.FindBestAddress(s.addrs, q); ok {
		if s.enforceWindow && s.window != nil && !s.window.Contains(addr.Coord) {
			return osmmini.Coord{}, "", "", errors.New("outside configured window")
		}
		return addr.Coord, formatAddressLabel(addr.Tags), raw, nil
	}
	if q.Street != "" {
		if nodeID, okStreet := s.router.StreetNode(q.Street); okStreet {
			c, _ := s.router.Coord(nodeID)
			if s.enforceWindow && s.window != nil && !s.window.Contains(c) {
				return osmmini.Coord{}, "", "", errors.New("outside configured window")
			}
			return c, q.Street, raw, nil
		}
	}
	return osmmini.Coord{}, "", "", fmt.Errorf("not found: %s", raw)
}

func parseLatLon(raw string) (osmmini.Coord, bool) {
	s := strings.TrimSpace(raw)
	s = strings.ReplaceAll(s, ",", " ")
	fields := strings.Fields(s)
	if len(fields) != 2 {
		return osmmini.Coord{}, false
	}
	lat, err1 := strconv.ParseFloat(fields[0], 64)
	lon, err2 := strconv.ParseFloat(fields[1], 64)
	if err1 != nil || err2 != nil {
		return osmmini.Coord{}, false
	}
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return osmmini.Coord{}, false
	}
	return osmmini.Coord{Lat: lat, Lon: lon}, true
}

func formatAddressLabel(tags osmmini.Tags) string {
	street := tags["addr:street"]
	hn := tags["addr:housenumber"]
	post := tags["addr:postcode"]
	city := tags["addr:city"]
	if city == "" {
		city = tags["addr:place"]
	}
	name := tags["name"]

	line1 := ""
	switch {
	case street != "" && hn != "":
		line1 = street + " " + hn
	case street != "":
		line1 = street
	case name != "":
		line1 = name
	default:
		line1 = "Adresse"
	}

	line2 := strings.TrimSpace(strings.Join([]string{post, city}, " "))
	if line2 != "" {
		return line1 + ", " + line2
	}
	return line1
}

func parseLimit(raw string, def, min, max int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

func parseWindow(s string) (osmmini.CoordWindow, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return osmmini.CoordWindow{}, errors.New("expected minLat,maxLat,minLon,maxLon")
	}
	vals := make([]float64, 4)
	for i := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(parts[i]), 64)
		if err != nil {
			return osmmini.CoordWindow{}, err
		}
		vals[i] = f
	}
	w := osmmini.CoordWindow{MinLat: vals[0], MaxLat: vals[1], MinLon: vals[2], MaxLon: vals[3]}
	if !w.Valid() {
		return osmmini.CoordWindow{}, errors.New("invalid window bounds")
	}
	return w, nil
}

// ---- Maps links ----

func buildGoogleMapsURL(points []osmmini.Coord, maxWaypoints int) string {
	if len(points) < 2 {
		return ""
	}
	if maxWaypoints <= 0 {
		maxWaypoints = 9
	}
	origin := fmt.Sprintf("%f,%f", points[0].Lat, points[0].Lon)
	destination := fmt.Sprintf("%f,%f", points[len(points)-1].Lat, points[len(points)-1].Lon)

	waypoints := []string{}
	for i := 1; i < len(points)-1 && len(waypoints) < maxWaypoints; i++ {
		waypoints = append(waypoints, fmt.Sprintf("%f,%f", points[i].Lat, points[i].Lon))
	}

	q := url.Values{}
	q.Set("api", "1")
	q.Set("origin", origin)
	q.Set("destination", destination)
	if len(waypoints) > 0 {
		q.Set("waypoints", strings.Join(waypoints, "|"))
	}
	q.Set("travelmode", "driving")
	return "https://www.google.com/maps/dir/?" + q.Encode()
}

func buildAppleMapsURL(points []osmmini.Coord) string {
	if len(points) < 2 {
		return ""
	}
	saddr := fmt.Sprintf("%f,%f", points[0].Lat, points[0].Lon)

	dparts := make([]string, 0, len(points)-1)
	for i := 1; i < len(points); i++ {
		ll := fmt.Sprintf("%f,%f", points[i].Lat, points[i].Lon)
		if i == 1 {
			dparts = append(dparts, ll)
		} else {
			dparts = append(dparts, "to:"+ll)
		}
	}

	q := url.Values{}
	q.Set("saddr", saddr)
	q.Set("daddr", strings.Join(dparts, "+"))
	q.Set("dirflg", "d")
	return "https://maps.apple.com/?" + q.Encode()
}

// ---- AI Integration ----

// aiProvider describes a detected LLM provider (Ollama or LM Studio).
type aiProvider struct {
	Name      string   `json:"name"`
	URL       string   `json:"url"`
	Available bool     `json:"available"`
	Models    []string `json:"models,omitempty"`
}

// aiStatusResponse is returned by GET /api/v1/ai/status.
type aiStatusResponse struct {
	Available bool         `json:"available"`
	Providers []aiProvider `json:"providers"`
}

// aiQueryRequest is the body for POST /api/v1/ai/query.
type aiQueryRequest struct {
	Prompt string   `json:"prompt"`
	Model  string   `json:"model,omitempty"`
	Lat    *float64 `json:"lat,omitempty"`
	Lon    *float64 `json:"lon,omitempty"`
	// map center coordinates (hint)
	MapLat *float64 `json:"map_lat,omitempty"`
	MapLon *float64 `json:"map_lon,omitempty"`
	// explicit user geolocation when available
	UserLat *float64 `json:"user_lat,omitempty"`
	UserLon *float64 `json:"user_lon,omitempty"`
	// optional session id to enable multi-turn context
	Session string `json:"session,omitempty"`
	// optional current route context (from/to + stats)
	RouteFrom     string  `json:"route_from,omitempty"`
	RouteTo       string  `json:"route_to,omitempty"`
	RouteDistM    float64 `json:"route_dist_m,omitempty"`
	RouteDurS     float64 `json:"route_dur_s,omitempty"`
	RouteEngine    string  `json:"route_engine,omitempty"`
	RouteObjective string  `json:"route_objective,omitempty"`
	// bounding box of the current route path (for poi_on_route queries)
	RouteBBoxMinLat float64 `json:"route_bbox_min_lat,omitempty"`
	RouteBBoxMinLon float64 `json:"route_bbox_min_lon,omitempty"`
	RouteBBoxMaxLat float64 `json:"route_bbox_max_lat,omitempty"`
	RouteBBoxMaxLon float64 `json:"route_bbox_max_lon,omitempty"`
}

// aiLocation is a lightweight location descriptor returned in AI responses.
type aiLocation struct {
	Query string   `json:"query,omitempty"`
	Label string   `json:"label,omitempty"`
	Lat   *float64 `json:"lat,omitempty"`
	Lon   *float64 `json:"lon,omitempty"`
}

// aiQueryResponse is returned by POST /api/v1/ai/query.
type aiQueryResponse struct {
	Provider    string            `json:"provider"`
	Model       string            `json:"model"`
	Response    string            `json:"response"`
	Route       *RouteResponse    `json:"route,omitempty"`
	Suggestions []apiSearchResult `json:"suggestions,omitempty"`
	From        *aiLocation       `json:"from,omitempty"`
	To          *aiLocation       `json:"to,omitempty"`
	Waypoints   []aiLocation      `json:"waypoints,omitempty"`
	SessionID   string            `json:"session_id,omitempty"`
}

// probeAIProvider checks if a provider is reachable and fetches available models.
func probeAIProvider(ctx context.Context, name, baseURL, modelsPath string) aiProvider {
	p := aiProvider{Name: name, URL: baseURL, Available: false}
	client := &http.Client{Timeout: 3 * time.Second}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+modelsPath, nil)
	if err != nil {
		return p
	}
	resp, err := client.Do(req)
	if err != nil {
		return p
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return p
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAIResponseBytes))
	if err != nil {
		return p
	}
	p.Available = true

	// Parse models - Ollama format: {"models":[{"name":"..."},...]}
	// LM Studio format (OpenAI-compatible): {"data":[{"id":"..."},...]}
	var ollamaResp struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &ollamaResp); err == nil && len(ollamaResp.Models) > 0 {
		for _, m := range ollamaResp.Models {
			p.Models = append(p.Models, m.Name)
		}
		return p
	}

	var openaiResp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &openaiResp); err == nil && len(openaiResp.Data) > 0 {
		for _, m := range openaiResp.Data {
			p.Models = append(p.Models, m.ID)
		}
	}
	return p
}

func (s *server) handleAIStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	providers := []aiProvider{
		probeAIProvider(ctx, "ollama", "http://localhost:11434", "/api/tags"),
		probeAIProvider(ctx, "lmstudio", "http://localhost:1234", "/v1/models"),
	}

	// Update the probe cache so handleAIQuery can reuse the result.
	s.aiProbe.set(providers)

	anyAvailable := false
	for _, p := range providers {
		if p.Available {
			anyAvailable = true
			break
		}
	}

	writeJSON(w, http.StatusOK, aiStatusResponse{
		Available: anyAvailable,
		Providers: providers,
	})
}

func (s *server) handleAIQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req aiQueryRequest
	if err := readJSON(w, r, &req, maxAIResponseBytes); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeJSONError(w, http.StatusBadRequest, "prompt required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), aiQueryTimeout)
	defer cancel()

	// Try to classify and handle the intent locally before hitting the LLM.
	intent := classifyPromptIntent(req.Prompt)
	if intent.Type != intentUnknown {
		if s.handleIntentLocally(ctx, w, req, intent) {
			return
		}
	}

	// Build system prompt with context about available routing features and
	// the user's current map state so the LLM can give accurate answers.
	var sysB strings.Builder
	sysB.WriteString("Du bist ein Routing-Assistent für OSMmini, ein Offline-Routing-System. " +
		"Du kannst Fragen zu Routenplanung, Navigation und Verkehr beantworten. " +
		"Verfügbare Features: A*-, Dijkstra- und CH-Routing, TSP-Optimierung (bis 16 Stops exakt, " +
		"darüber hinaus Greedy mit 2-opt), Linksabbiege-Vermeidung, Ampelstrafen, " +
		"BOS/Einsatzmodus (Feuerwehr, Rettungsdienst), Fahrzeugbeschränkungen (Höhe, Gewicht). " +
		"Antworte kurz und hilfreich auf Deutsch.")
	// Add user's location context when available
	if req.UserLat != nil && req.UserLon != nil {
		fmt.Fprintf(&sysB, " Nutzerstandort: %.5f,%.5f.", *req.UserLat, *req.UserLon)
	} else if req.MapLat != nil && req.MapLon != nil {
		fmt.Fprintf(&sysB, " Aktuelle Kartenansicht: %.5f,%.5f.", *req.MapLat, *req.MapLon)
	}
	// Add current route context if provided
	if req.RouteFrom != "" || req.RouteTo != "" {
		sysB.WriteString(" Aktuelle Route:")
		if req.RouteFrom != "" {
			fmt.Fprintf(&sysB, " von '%s'", req.RouteFrom)
		}
		if req.RouteTo != "" {
			fmt.Fprintf(&sysB, " nach '%s'", req.RouteTo)
		}
		if req.RouteDistM > 0 {
			fmt.Fprintf(&sysB, " (%.1f km", req.RouteDistM/1000)
			if req.RouteDurS > 0 {
				fmt.Fprintf(&sysB, ", %.0f min", req.RouteDurS/60)
			}
			sysB.WriteString(")")
		}
		if req.RouteEngine != "" {
			fmt.Fprintf(&sysB, " [%s/%s]", req.RouteEngine, req.RouteObjective)
		}
		sysB.WriteString(".")
	}
	// Critical: instruct the LLM to emit a structured action block whenever
	// a route should actually be computed. The backend will parse this block,
	// resolve the locations, run the router, and return the route to the UI.
	// The block must appear verbatim at the very end of the response so it
	// can be extracted reliably without disturbing the human-readable text.
	sysB.WriteString(
		"\n\nREGEL – ROUTE-ACTION-BLOCK: Sobald ein Ziel bekannt ist (auch beim ersten Mal, ohne " +
			"auf Bestätigung zu warten!), MUSST du am ENDE deiner Antwort exakt diesen Block ausgeben:\n" +
			"```route-action\n{\"action\":\"compute_route\",\"from\":\"STARTPUNKT\",\"to\":\"ZIELORT\"}\n```\n" +
			"Ersetze STARTPUNKT mit den bekannten Nutzerkoordinaten (lat,lon). " +
			"Ersetze ZIELORT mit dem echten Ortsnamen (z.B. 'Flughafen München'). " +
			"Benutze NIEMALS Platzhalter wie <Zielort> oder <Startort> – nur echte Werte. " +
			"Wenn Nutzerkoordinaten bekannt sind und ein Ziel genannt wird: SOFORT den Block ausgeben, " +
			"OHNE nach Bestätigung zu fragen. " +
			"OHNE diesen Block wird KEINE Route auf der Karte angezeigt. " +
			"Gib KEINE fiktiven Entfernungen oder Zeiten an – diese berechnet das System selbst.",
	)
	systemPrompt := sysB.String()

	// Quick heuristic: if the user asks for the "nearest" X, try to answer
	// directly from local OSM data instead of querying the LLM. This allows
	// the assistant to return actual nearby POIs and compute a route when a
	// reference location (lat/lon) is provided in the prompt.
	lower := strings.ToLower(req.Prompt)

	// Area / polygon queries (e.g. "Welche Waldfläche hat der Landkreis Dingolfing-Landau?")
	if (strings.Contains(lower, "fläche") || strings.Contains(lower, "waldfläche") || strings.Contains(lower, "fläche hat")) && (strings.Contains(lower, "landkreis") || strings.Contains(lower, "kreis") || strings.Contains(lower, "stadt")) {
		// try to extract admin name
		rex := regexp.MustCompile(`(?i)(?:landkreis|kreisfreie stadt|kreis|stadt)\s+([A-Za-zÄÖÜäöüß\-\s]+)`)
		if m := rex.FindStringSubmatch(req.Prompt); len(m) >= 2 {
			admin := strings.TrimSpace(m[1])
			if admin != "" {
				areaM2, poly, err := s.computeAreaForAdmin(ctx, admin)
				if err != nil {
					writeJSONError(w, http.StatusNotFound, "Fläche nicht gefunden: "+err.Error())
					return
				}
				km2 := areaM2 / 1e6
				respTxt := fmt.Sprintf("Die geschätzte Waldfläche im Landkreis %s beträgt %.3f km² (%.0f m²).", admin, km2, areaM2)
				// include centroid link if polygon available
				if len(poly) > 0 {
					// compute centroid
					cx, cy := 0.0, 0.0
					for _, c := range poly {
						cx += c.Lat
						cy += c.Lon
					}
					cx /= float64(len(poly))
					cy /= float64(len(poly))
					gurl := buildGoogleMapsURL([]osmmini.Coord{{Lat: cx, Lon: cy}}, 12)
					respTxt += " Ansicht: " + gurl
				}
				writeJSON(w, http.StatusOK, aiQueryResponse{Provider: "local", Model: "area-estimate", Response: respTxt})
				return
			}
		}
	}
	if strings.Contains(lower, "nächste") || strings.Contains(lower, "nächster") || strings.Contains(lower, "nearest") || strings.Contains(lower, "near me") || strings.Contains(lower, "in der nähe") {
		// Determine query coordinates. Prefer explicit user geolocation, then
		// the request `lat`/`lon`, then provided map center, then try parsing
		// coordinates from the prompt text.
		coordFound := false
		var qlat, qlon float64
		if req.UserLat != nil && req.UserLon != nil {
			qlat, qlon = *req.UserLat, *req.UserLon
			coordFound = true
		} else if req.Lat != nil && req.Lon != nil {
			qlat, qlon = *req.Lat, *req.Lon
			coordFound = true
		} else if req.MapLat != nil && req.MapLon != nil {
			qlat, qlon = *req.MapLat, *req.MapLon
			coordFound = true
		}

		// crude float pair regex for coords in prompt (fallback)
		re := regexp.MustCompile(`([-+]?[0-9]*\.?[0-9]+)\s*,?\s*([-+]?[0-9]*\.?[0-9]+)`)
		if !coordFound {
			if m := re.FindStringSubmatch(req.Prompt); len(m) == 3 {
				if f1, err1 := strconv.ParseFloat(m[1], 64); err1 == nil {
					if f2, err2 := strconv.ParseFloat(m[2], 64); err2 == nil {
						// determine which is lat and lon by range
						if f1 >= -90 && f1 <= 90 && f2 >= -180 && f2 <= 180 {
							qlat, qlon = f1, f2
							coordFound = true
						} else if f2 >= -90 && f2 <= 90 && f1 >= -180 && f1 <= 180 {
							qlat, qlon = f2, f1
							coordFound = true
						}
					}
				}
			}
		}
		if !coordFound {
			writeJSONError(w, http.StatusBadRequest, "Bitte geben Sie Ihren Standort (lat lon) an, z.B. '48.1351,11.5820', oder stellen Sie die Frage mit 'near me'.")
			return
		}

		// Heuristic: extract place name by removing common phrasing and coords.
		place := strings.TrimSpace(req.Prompt)
		// common English/German phrases
		pats := []string{"wo ist der nächste", "wo ist der nächster", "where is the nearest", "nearest", "nächste", "nächster", "in der nähe", "in der Nähe", "near me", "wo ist", "wo", "ist", "der", "die", "das"}
		for _, p := range pats {
			place = strings.ReplaceAll(strings.ToLower(place), strings.ToLower(p), "")
		}
		// strip coords
		place = re.ReplaceAllString(place, "")
		place = strings.Trim(place, " ?.!\n\r\t")
		if place == "" {
			writeJSONError(w, http.StatusBadRequest, "Bitte nennen Sie das Ziel (z. B. 'McDonald's') zusammen mit Ihrem Standort.")
			return
		}

		// normalized place string for fuzzy matching
		// use normalizeForCompare below when scanning names/brands

		// First try to recognise simple POI types (lakes, forests, etc.)
		matches := []osmmini.AddressEntry{}
		loweredPlace := strings.ToLower(place)
		isPOIHandled := false
		if strings.Contains(loweredPlace, "see") || strings.Contains(loweredPlace, "lake") {
			// lakes/ponds: natural=water OR water=lake OR name contains 'see'
			for _, a := range s.addrs {
				if a.Tags["natural"] == "water" || a.Tags["water"] == "lake" {
					matches = append(matches, a)
					continue
				}
				n := strings.ToLower(a.Tags["name"])
				if n != "" && strings.Contains(n, "see") {
					matches = append(matches, a)
				}
			}
			isPOIHandled = true
		}
		if !isPOIHandled && (strings.Contains(loweredPlace, "wald") || strings.Contains(loweredPlace, "forest")) {
			// forests: landuse=forest or natural=wood or name contains 'wald'
			for _, a := range s.addrs {
				if a.Tags["landuse"] == "forest" || a.Tags["natural"] == "wood" {
					matches = append(matches, a)
					continue
				}
				n := strings.ToLower(a.Tags["name"])
				if n != "" && strings.Contains(n, "wald") {
					matches = append(matches, a)
				}
			}
			isPOIHandled = true
		}
		// additional POI shortcuts using indexed POI ways (best-effort)
		if !isPOIHandled {
			// map of keyword -> (tagKey, tagValue)
			poiMap := map[string][2]string{
				"tankstelle":  {"amenity", "fuel"},
				"benzin":      {"amenity", "fuel"},
				"bahnhof":     {"railway", "station"},
				"apotheke":    {"amenity", "pharmacy"},
				"supermarkt":  {"shop", "supermarket"},
				"bäckerei":    {"shop", "bakery"},
				"parkplatz":   {"amenity", "parking"},
				"bank":        {"amenity", "bank"},
				"krankenhaus": {"amenity", "hospital"},
				"schule":      {"amenity", "school"},
				"museum":      {"tourism", "museum"},
			}
			for k, tv := range poiMap {
				if strings.Contains(loweredPlace, k) {
					// scan indexed ways for matching tag
					s.poiMu.RLock()
					for _, w := range s.poiWays {
						if v, ok := w.Tags[tv[0]]; ok {
							if tv[1] == "" || strings.EqualFold(v, tv[1]) {
								// build centroid from nodes
								var cx, cy float64
								var cnt int
								for _, nid := range w.NodeIDs {
									if c, ok := s.poiNodes[nid]; ok {
										cx += c.Lat
										cy += c.Lon
										cnt++
									}
								}
								if cnt == 0 {
									continue
								}
								centroid := osmmini.Coord{Lat: cx / float64(cnt), Lon: cy / float64(cnt)}
								matches = append(matches, osmmini.AddressEntry{ID: w.ID, Coord: centroid, Tags: w.Tags})
								if len(matches) >= 100 {
									break
								}
							}
						}
					}
					s.poiMu.RUnlock()
					isPOIHandled = true
					break
				}
			}
		}
		// If we didn't handle a POI keyword specifically, fall back to structured address search
		if !isPOIHandled {
			aq := osmmini.ParseAddressGuess(place)
			matches = osmmini.SearchAddresses(s.addrs, aq, 50)
		}

		// If no structured matches, try fuzzy name/brand scan over addresses
		if len(matches) == 0 {
			placeNormCmp := normalizeForCompare(place)
			for _, a := range s.addrs {
				nameNorm := normalizeForCompare(a.Tags["name"])
				brandNorm := normalizeForCompare(a.Tags["brand"])
				// direct containment checks on normalized forms
				if nameNorm != "" && placeNormCmp != "" && (strings.Contains(nameNorm, placeNormCmp) || strings.Contains(placeNormCmp, nameNorm)) {
					matches = append(matches, a)
					continue
				}
				if brandNorm != "" && placeNormCmp != "" && (strings.Contains(brandNorm, placeNormCmp) || strings.Contains(placeNormCmp, brandNorm)) {
					matches = append(matches, a)
					continue
				}
				// alias lookup (e.g., user typed 'maci')
				if canon, ok := brandAliasMap[placeNormCmp]; ok {
					if brandNorm != "" && strings.Contains(brandNorm, canon) {
						matches = append(matches, a)
						continue
					}
					if a.Tags["amenity"] == "fast_food" && (strings.Contains(nameNorm, canon) || strings.Contains(brandNorm, canon)) {
						matches = append(matches, a)
						continue
					}
				}
				// heuristic: if user typed something short like 'maci', and the POI is fast_food, consider it
				if len(placeNormCmp) <= 6 && a.Tags["amenity"] == "fast_food" {
					if strings.Contains(nameNorm, "mcdonald") || strings.Contains(brandNorm, "mcdonald") {
						matches = append(matches, a)
						continue
					}
				}
			}
		}

		// also search streets as fallback
		if len(matches) == 0 {
			streets := s.router.SearchStreets(place, 20)
			for _, st := range streets {
				matches = append(matches, osmmini.AddressEntry{ID: st.NodeID, Coord: st.Coord, Tags: osmmini.Tags{"street": st.Name}})
			}
		}
		if len(matches) == 0 {
			writeJSONError(w, http.StatusNotFound, "Kein passendes Ziel gefunden für: "+place)
			return
		}

		// Build suggestions list (top matches) to return to the UI
		suggestLimit := 6
		if len(matches) < suggestLimit {
			suggestLimit = len(matches)
		}
		suggestions := make([]apiSearchResult, 0, suggestLimit)
		for i := 0; i < suggestLimit; i++ {
			m := matches[i]
			suggestions = append(suggestions, apiSearchResult{
				ID:    m.ID,
				Kind:  "address",
				Label: formatAddressLabel(m.Tags),
				Lat:   m.Coord.Lat,
				Lon:   m.Coord.Lon,
				Tags:  m.Tags,
			})
		}

		// find nearest by haversine
		bestIdx := -1
		bestDist := math.MaxFloat64
		for i, m := range matches {
			d := haversineMeters(qlat, qlon, m.Coord.Lat, m.Coord.Lon)
			if d < bestDist {
				bestDist = d
				bestIdx = i
			}
		}
		if bestIdx == -1 {
			writeJSONError(w, http.StatusNotFound, "Kein passendes Ziel gefunden")
			return
		}
		best := matches[bestIdx]
		label := formatAddressLabel(best.Tags)
		// try to build a route from query coord to the POI node using server defaults
		var routeObj *RouteResponse
		startID, startDist, okF := s.router.NearestNode(qlat, qlon)
		endID, endDist, okT := s.router.NearestNode(best.Coord.Lat, best.Coord.Lon)
		if okF && okT {
			opt := s.settings.Get().Routing
			if res, err := s.router.RouteWithOptions(ctx, startID, endID, opt); err == nil && len(res.PathCoords) > 0 {
				gURL := buildGoogleMapsURL([]osmmini.Coord{{Lat: qlat, Lon: qlon}, best.Coord}, 0)
				aURL := buildAppleMapsURL([]osmmini.Coord{{Lat: qlat, Lon: qlon}, best.Coord})
				steps := s.router.ManeuversForPath(res.Path, opt)
				routeObj = &RouteResponse{
					From:          RoutePoint{Input: fmt.Sprintf("%.6f %.6f", qlat, qlon), Label: "Start", Lat: qlat, Lon: qlon, Node: startID, SnapM: startDist},
					To:            RoutePoint{Input: formatAddressLabel(best.Tags), Label: label, Lat: best.Coord.Lat, Lon: best.Coord.Lon, Node: endID, SnapM: endDist},
					Engine:        string(res.Engine),
					Objective:     string(res.Objective),
					Profile:       string(opt.Profile),
					Cost:          res.Cost,
					DistanceM:     res.DistanceM,
					DurationS:     res.DurationS,
					Path:          res.PathCoords,
					GoogleMapsURL: gURL,
					AppleMapsURL:  aURL,
					Steps:         steps,
				}
			}
		}

		// respond in German with found result and optional route
		respTxt := fmt.Sprintf("Ich habe '%s' (Entfernung %.0f m) in der Nähe gefunden. Koordinaten: %.6f, %.6f.", label, bestDist, best.Coord.Lat, best.Coord.Lon)
		if routeObj != nil {
			respTxt += " Route berechnet."
		} else {
			// include a quick Google Maps link when no internal route could be computed
			if g := buildGoogleMapsURL([]osmmini.Coord{{Lat: qlat, Lon: qlon}, best.Coord}, 0); g != "" {
				respTxt += " Route: " + g
			}
		}
		// build structured from/to for the UI
		var fromLoc *aiLocation
		var toLoc *aiLocation
		if routeObj != nil {
			fromLat := routeObj.From.Lat
			fromLon := routeObj.From.Lon
			toLat := routeObj.To.Lat
			toLon := routeObj.To.Lon
			fromLoc = &aiLocation{Query: routeObj.From.Input, Label: routeObj.From.Label, Lat: &fromLat, Lon: &fromLon}
			toLoc = &aiLocation{Query: routeObj.To.Input, Label: routeObj.To.Label, Lat: &toLat, Lon: &toLon}
		} else {
			// even without internal route, provide minimal structured locations
			qlatC := qlat
			qlonC := qlon
			fromLoc = &aiLocation{Query: fmt.Sprintf("%.6f,%.6f", qlatC, qlonC), Label: "Start", Lat: &qlatC, Lon: &qlonC}
			tlat := best.Coord.Lat
			tlon := best.Coord.Lon
			toLoc = &aiLocation{Query: formatAddressLabel(best.Tags), Label: formatAddressLabel(best.Tags), Lat: &tlat, Lon: &tlon}
		}
		writeJSON(w, http.StatusOK, aiQueryResponse{Provider: "local", Model: "local-search", Response: respTxt, Route: routeObj, Suggestions: suggestions, From: fromLoc, To: toLoc})
		return
	}

	// Include session history (if any) into the system prompt to enable
	// multi-turn follow-ups where the user refers to previous results
	if req.Session != "" {
		hist := s.getSessionContext(req.Session)
		if len(hist) > 0 {
			var b strings.Builder
			b.WriteString("Vorherige Konversation (letzte Nachrichten):\n")
			for _, m := range hist {
				role := strings.ToUpper(m.Role[:1]) + m.Role[1:]
				// truncate long messages
				content := m.Content
				if len(content) > 400 {
					content = content[:400] + "..."
				}
				b.WriteString(fmt.Sprintf("%s: %s\n", role, content))
			}
			systemPrompt += "\n" + b.String()
		}
	}

	// Determine which providers are available. Re-use a recently cached probe
	// result to avoid re-probing on every query (the cache has a 30s TTL).
	// If no cached result exists, probe now and populate the cache.
	cachedProviders := s.aiProbe.get()
	if cachedProviders == nil {
		probeCtx, probeCancel := context.WithTimeout(ctx, 5*time.Second)
		cachedProviders = []aiProvider{
			probeAIProvider(probeCtx, "ollama", "http://localhost:11434", "/api/tags"),
			probeAIProvider(probeCtx, "lmstudio", "http://localhost:1234", "/v1/models"),
		}
		probeCancel()
		s.aiProbe.set(cachedProviders)
	}

	// Try available providers. If a specific model is requested we pick the
	// provider that knows about it; otherwise we use the first available one.
	providerURLs := map[string]string{
		"ollama":   "http://localhost:11434",
		"lmstudio": "http://localhost:1234",
	}
	for _, p := range cachedProviders {
		if !p.Available || len(p.Models) == 0 {
			continue
		}

		// If a specific model was requested, skip providers that don't have it.
		model := req.Model
		if model != "" {
			found := false
			for _, m := range p.Models {
				if m == model {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		} else {
			model = p.Models[0]
		}

		baseURL := providerURLs[p.Name]
		if baseURL == "" {
			continue
		}

		var respText string
		var err error
		if p.Name == "ollama" {
			respText, err = queryOllama(ctx, baseURL, model, systemPrompt, req.Prompt)
		} else {
			respText, err = queryOpenAICompatible(ctx, baseURL, model, systemPrompt, req.Prompt)
		}
		if err != nil {
			// Invalidate cache on error so the next request re-probes.
			s.aiProbe.invalidate()
			continue
		}

		// Parse a route-action block from the LLM response. When found, resolve
		// the locations, compute the route, and attach it to the response so the
		// UI renders it immediately without any additional user interaction.
		var routeObj *RouteResponse
		var fromAI, toAI *aiLocation
		fromStr, toStr, cleanText, hasAction := extractRouteAction(respText)
		if hasAction {
			// Always strip the block from the displayed text, even if routing fails.
			respText = cleanText
			// Skip obvious LLM placeholders like "<Zielort>" or "<Startort>".
			if strings.HasPrefix(strings.TrimSpace(toStr), "<") {
				toStr = ""
			}
			if strings.HasPrefix(strings.TrimSpace(fromStr), "<") {
				fromStr = ""
			}
		}
		if hasAction && toStr != "" {
			// If from is empty, fall back to the user's coordinates.
			if fromStr == "" {
				if req.UserLat != nil && req.UserLon != nil {
					fromStr = fmt.Sprintf("%.6f,%.6f", *req.UserLat, *req.UserLon)
				} else if req.MapLat != nil && req.MapLon != nil {
					fromStr = fmt.Sprintf("%.6f,%.6f", *req.MapLat, *req.MapLon)
				}
			}
			opt := s.settings.Get().Routing
			if rr, fa, ta, rerr := s.computeRouteFromLocQuery(ctx, fromStr, toStr, opt); rerr == nil {
				routeObj = rr
				fromAI = fa
				toAI = ta
			} else {
				log.Printf("ai route-action failed (from=%q to=%q): %v", fromStr, toStr, rerr)
				// Append a brief note to the response so the user knows what happened.
				respText += fmt.Sprintf("\n\n⚠️ Route konnte nicht berechnet werden: %v", rerr)
			}
		}

		// persist session messages if session present (user + assistant)
		sid := req.Session
		if sid == "" {
			sid = fmt.Sprintf("s-%d", time.Now().UnixNano())
		}
		_ = s.appendSessionMessage(sid, "user", req.Prompt)
		_ = s.appendSessionMessage(sid, "assistant", respText)

		writeJSON(w, http.StatusOK, aiQueryResponse{
			Provider:  p.Name,
			Model:     model,
			Response:  respText,
			Route:     routeObj,
			From:      fromAI,
			To:        toAI,
			SessionID: sid,
		})
		return
	}

	writeJSONError(w, http.StatusServiceUnavailable, "Kein KI-Provider verfügbar. Bitte Ollama oder LM Studio starten.")
}

// handlePOIInfo returns detailed information for a POI by id.
// Path: /api/v1/poi/{id}
func (s *server) handlePOIInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// path is /api/v1/poi/{id}
	idStr := strings.TrimPrefix(r.URL.Path, "/api/v1/poi/")
	if idStr == "" {
		writeJSONError(w, http.StatusBadRequest, "missing id")
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
		return
	}

	s.poiMu.RLock()
	// prefer way, then node, then relation
	if wv, ok := s.poiWays[id]; ok {
		// compute centroid
		var cx, cy float64
		var cnt int
		for _, nid := range wv.NodeIDs {
			if c, ok := s.poiNodes[nid]; ok {
				cx += c.Lat
				cy += c.Lon
				cnt++
			}
		}
		lat, lon := 0.0, 0.0
		if cnt > 0 {
			lat = cx / float64(cnt)
			lon = cy / float64(cnt)
		}
		resp := poiInfoResponse{ID: id, Kind: "way", Label: wv.Tags["name"], Lat: lat, Lon: lon, Tags: wv.Tags}
		s.poiMu.RUnlock()
		// optional distance
		if qlatS := r.URL.Query().Get("lat"); qlatS != "" {
			if qlonS := r.URL.Query().Get("lon"); qlonS != "" {
				if qlat, err1 := strconv.ParseFloat(qlatS, 64); err1 == nil {
					if qlon, err2 := strconv.ParseFloat(qlonS, 64); err2 == nil {
						d := haversineMeters(qlat, qlon, resp.Lat, resp.Lon)
						resp.DistanceM = &d
					}
				}
			}
		}
		// wikipedia summary if available
		if tag, ok := resp.Tags["wikipedia"]; ok && tag != "" {
			lang := ""
			title := tag
			if strings.Contains(tag, ":") {
				parts := strings.SplitN(tag, ":", 2)
				lang = parts[0]
				title = parts[1]
			}
			if summary := s.fetchWikiSummary(r.Context(), lang, title); summary != "" {
				resp.WikiSummary = summary
			}
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if nv, ok := s.poiNodes[id]; ok {
		// node may not have tags in node index; search in ways/relations for tag info
		// build minimal response
		resp := poiInfoResponse{ID: id, Kind: "node", Label: "", Lat: nv.Lat, Lon: nv.Lon, Tags: nil}
		s.poiMu.RUnlock()
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if rv, ok := s.poiRels[id]; ok {
		// relation: return centroid of member ways/nodes when possible
		s.poiMu.RUnlock()
		// try to assemble centroid from members
		var sumLat, sumLon float64
		var cnt int
		for _, m := range rv.Members {
			if m.Type == osmmini.MemberWay {
				if wv, ok := s.poiWays[m.ID]; ok {
					for _, nid := range wv.NodeIDs {
						if c, ok := s.poiNodes[nid]; ok {
							sumLat += c.Lat
							sumLon += c.Lon
							cnt++
						}
					}
				}
			} else if m.Type == osmmini.MemberNode {
				if c, ok := s.poiNodes[m.ID]; ok {
					sumLat += c.Lat
					sumLon += c.Lon
					cnt++
				}
			}
		}
		lat, lon := 0.0, 0.0
		if cnt > 0 {
			lat = sumLat / float64(cnt)
			lon = sumLon / float64(cnt)
		}
		resp := poiInfoResponse{ID: id, Kind: "relation", Label: rv.Tags["name"], Lat: lat, Lon: lon, Tags: rv.Tags}
		// optional wiki
		if tag, ok := resp.Tags["wikipedia"]; ok && tag != "" {
			lang := ""
			title := tag
			if strings.Contains(tag, ":") {
				parts := strings.SplitN(tag, ":", 2)
				lang = parts[0]
				title = parts[1]
			}
			if summary := s.fetchWikiSummary(r.Context(), lang, title); summary != "" {
				resp.WikiSummary = summary
			}
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	s.poiMu.RUnlock()
	writeJSONError(w, http.StatusNotFound, "POI not found")
}

// ---- Intent classifier ----

// promptIntentType classifies the user's intent so the system can answer
// locally without involving an LLM.
type promptIntentType int

const (
	intentUnknown    promptIntentType = iota
	intentNavigate                    // "Navigation nach Dingolfing"
	intentPOINear                     // "Tankstellen in der Nähe"
	intentPOIOnRoute                  // "Tankstelle auf der Route"
	intentMultiStop                   // "Flughafen mit Zwischenstopp an Tankstelle"
	intentDuration                    // "Wie lange dauert es von X nach Y"
	intentLeisure                     // "An der Isar spazieren gehen"
)

// promptIntent holds the classified intent and extracted parameters.
type promptIntent struct {
	Type        promptIntentType
	Destination string // primary destination (navigate / duration / multi-stop)
	Origin      string // explicit origin for duration queries
	POIType     string // keyword the user typed (for UI messages)
	POITagKey   string // resolved OSM tag key
	POITagVal   string // resolved OSM tag value
}

// poiKeywordTags maps German and English POI keywords to OSM tag key/value pairs.
var poiKeywordTags = map[string][2]string{
	"tankstelle":     {"amenity", "fuel"},
	"tankstellen":    {"amenity", "fuel"},
	"benzin":         {"amenity", "fuel"},
	"diesel":         {"amenity", "fuel"},
	"gas station":    {"amenity", "fuel"},
	"fuel":           {"amenity", "fuel"},
	"bahnhof":        {"railway", "station"},
	"haltestelle":    {"highway", "bus_stop"},
	"bushaltestelle": {"highway", "bus_stop"},
	"apotheke":       {"amenity", "pharmacy"},
	"pharmacy":       {"amenity", "pharmacy"},
	"supermarkt":     {"shop", "supermarket"},
	"supermarket":    {"shop", "supermarket"},
	"edeka":          {"shop", "supermarket"},
	"lidl":           {"shop", "supermarket"},
	"aldi":           {"shop", "supermarket"},
	"rewe":           {"shop", "supermarket"},
	"bäckerei":       {"shop", "bakery"},
	"bakery":         {"shop", "bakery"},
	"parkplatz":      {"amenity", "parking"},
	"parking":        {"amenity", "parking"},
	"bank":           {"amenity", "bank"},
	"geldautomat":    {"amenity", "atm"},
	"atm":            {"amenity", "atm"},
	"krankenhaus":    {"amenity", "hospital"},
	"hospital":       {"amenity", "hospital"},
	"schule":         {"amenity", "school"},
	"school":         {"amenity", "school"},
	"museum":         {"tourism", "museum"},
	"hotel":          {"tourism", "hotel"},
	"gasthaus":       {"amenity", "restaurant"},
	"restaurant":     {"amenity", "restaurant"},
	"gaststätte":     {"amenity", "restaurant"},
	"café":           {"amenity", "cafe"},
	"cafe":           {"amenity", "cafe"},
	"kaffee":         {"amenity", "cafe"},
	"mcdonald":       {"amenity", "fast_food"},
	"mcdonalds":      {"amenity", "fast_food"},
	"burger king":    {"amenity", "fast_food"},
	"fastfood":       {"amenity", "fast_food"},
	"imbiss":         {"amenity", "fast_food"},
	"arzt":           {"amenity", "doctors"},
	"zahnarzt":       {"amenity", "dentist"},
	"post":           {"amenity", "post_office"},
	"postamt":        {"amenity", "post_office"},
	"friseur":        {"shop", "hairdresser"},
	"spielplatz":     {"leisure", "playground"},
	"schwimmbad":     {"leisure", "swimming_pool"},
	"freibad":        {"leisure", "swimming_pool"},
	"hallenbad":      {"leisure", "sports_centre"},
	"sportplatz":     {"leisure", "pitch"},
	"park":           {"leisure", "park"},
	"therme":         {"leisure", "spa"},
	"sauna":          {"leisure", "sauna"},
	"wald":           {"natural", "wood"},
	"wäldchen":       {"natural", "wood"},
	"forest":         {"landuse", "forest"},
	"see":            {"natural", "water"},
	"lake":           {"natural", "water"},
	"fluss":          {"waterway", "river"},
	"river":          {"waterway", "river"},
	"isar":           {"waterway", "river"},
	"kirche":         {"amenity", "place_of_worship"},
	"church":         {"amenity", "place_of_worship"},
	"rathaus":        {"amenity", "townhall"},
	"bibliothek":     {"amenity", "library"},
	"library":        {"amenity", "library"},
	"zoo":            {"tourism", "zoo"},
	"tierpark":       {"tourism", "zoo"},
	"kino":           {"amenity", "cinema"},
	"cinema":         {"amenity", "cinema"},
	"feuerwehr":      {"amenity", "fire_station"},
	"polizei":        {"amenity", "police"},
	"police":         {"amenity", "police"},
}

// navigatePrefixes are phrase prefixes that signal a navigate intent.
var navigatePrefixes = []string{
	"navigation nach ", "navigiere nach ", "navigier nach ",
	"fahre nach ", "fahr nach ", "fahrt nach ", "fahrt zu ",
	"route nach ", "route zu ", "route zum ", "route zur ",
	"geh nach ", "gehe nach ", "gehe zu ", "geh zu ",
	"bring mich nach ", "bring mich zu ", "bring mich zum ", "bring mich zur ",
	"zeig mir den weg nach ", "zeig den weg nach ",
	"wie komme ich nach ", "wie komm ich nach ",
	"navigate to ", "directions to ", "route to ", "take me to ",
	"drive to ", "go to ",
}

// durationPhrases indicate the user wants travel time or distance between two places.
var durationPhrases = []string{
	"wie lange dauert es von ", "wie lange dauert die fahrt von ",
	"wie lange brauche ich von ", "wie weit ist es von ",
	"wie lange dauert", "wie lange brauche ich",
	"how long does it take", "how far is it from ",
	"entfernung von ", "fahrzeit von ",
}

// leisureKeywords indicate the user is looking for a recreational activity.
var leisureKeywords = []string{
	"spazieren", "spaziergang", "wandern", "wanderung", "joggen",
	"radfahren", "pilze sammeln", "pilze", "entspannen",
	"erholung", "ausflug", "picknick", "walk", "hiking", "cycling",
}

// extractPOIFromPrompt finds the first known POI keyword in a lowercased prompt.
func extractPOIFromPrompt(lower string) (keyword, tagKey, tagVal string) {
	for kw, tv := range poiKeywordTags {
		if strings.Contains(lower, kw) {
			return kw, tv[0], tv[1]
		}
	}
	return "", "", ""
}

// classifyPromptIntent analyses a natural-language prompt and returns the
// most likely intent so the system can answer locally without an LLM.
// fillerPrefixes are conversational filler words that may precede an actual command.
var fillerPrefixes = []string{
	"äh, ", "äh ", "ähm, ", "ähm ", "hmm, ", "hmm ", "ok, ", "ok ",
	"also, ", "also ", "bitte ", "bitte, ",
}

func classifyPromptIntent(prompt string) promptIntent {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	// Strip leading filler words so "äh, Flughafen München" reduces to "Flughafen München".
	for _, f := range fillerPrefixes {
		if strings.HasPrefix(lower, f) {
			lower = strings.TrimSpace(lower[len(f):])
			break
		}
	}

	// duration intent: "wie lange dauert es von X nach Y"
	for _, ph := range durationPhrases {
		if strings.Contains(lower, ph) {
			re := regexp.MustCompile(`(?i)von\s+(.+?)\s+nach\s+(.+?)(?:\s*[?!.]|$)`)
			if m := re.FindStringSubmatch(lower); len(m) == 3 {
				return promptIntent{Type: intentDuration, Origin: strings.TrimSpace(m[1]), Destination: strings.TrimSpace(m[2])}
			}
			re2 := regexp.MustCompile(`(?i)nach\s+(.+?)(?:\s*[?!.]|$)`)
			if m := re2.FindStringSubmatch(lower); len(m) == 2 {
				return promptIntent{Type: intentDuration, Destination: strings.TrimSpace(m[1])}
			}
			return promptIntent{Type: intentDuration}
		}
	}

	// multi-stop intent: explicit "zwischenstopp" or "über … nach"
	reover := regexp.MustCompile(`(?i)\büber\b.+\bnach\b`)
	if strings.Contains(lower, "zwischenstopp") || strings.Contains(lower, "zwischenstation") ||
		strings.Contains(lower, "zwischenhalt") || strings.Contains(lower, "stopover") ||
		reover.MatchString(lower) {
		dest := ""
		reStop := regexp.MustCompile(`(?i)nach\s+(.+?)(?:\s+mit|\s+und|\s*[?!.]|$)`)
		if m := reStop.FindStringSubmatch(lower); len(m) == 2 {
			dest = strings.TrimSpace(m[1])
		}
		kw, tagKey, tagVal := extractPOIFromPrompt(lower)
		return promptIntent{Type: intentMultiStop, Destination: dest, POIType: kw, POITagKey: tagKey, POITagVal: tagVal}
	}

	// poi_on_route: "auf der Route" / "auf dem Weg"
	if strings.Contains(lower, "auf der route") || strings.Contains(lower, "auf meiner route") ||
		strings.Contains(lower, "entlang der route") || strings.Contains(lower, "along the route") ||
		strings.Contains(lower, "auf dem weg") || strings.Contains(lower, "am weg") {
		kw, tagKey, tagVal := extractPOIFromPrompt(lower)
		return promptIntent{Type: intentPOIOnRoute, POIType: kw, POITagKey: tagKey, POITagVal: tagVal}
	}

	// navigate intent: prefix forms ("Navigation nach X")
	for _, pfx := range navigatePrefixes {
		if strings.HasPrefix(lower, pfx) {
			dest := strings.TrimRight(strings.TrimSpace(lower[len(pfx):]), " ?!.")
			if dest != "" {
				return promptIntent{Type: intentNavigate, Destination: dest}
			}
		}
	}
	// navigate intent: postfix form ("nach X navigieren/fahren")
	reNav := regexp.MustCompile(`(?i)nach\s+(.+?)\s+(?:navigieren|fahren|route|navigation)`)
	if m := reNav.FindStringSubmatch(lower); len(m) == 2 {
		if dest := strings.TrimSpace(m[1]); dest != "" {
			return promptIntent{Type: intentNavigate, Destination: dest}
		}
	}

	// poi_near: "in der Nähe" / "nächste" etc.
	if strings.Contains(lower, "in der nähe") || strings.Contains(lower, "nächste") ||
		strings.Contains(lower, "nächster") || strings.Contains(lower, "nächstes") ||
		strings.Contains(lower, "nearest") || strings.Contains(lower, "near me") ||
		strings.Contains(lower, "nahe von") || strings.Contains(lower, "nahe bei") {
		kw, tagKey, tagVal := extractPOIFromPrompt(lower)
		return promptIntent{Type: intentPOINear, POIType: kw, POITagKey: tagKey, POITagVal: tagVal}
	}

	// leisure intent
	for _, kw := range leisureKeywords {
		if strings.Contains(lower, kw) {
			poiKw, tagKey, tagVal := extractPOIFromPrompt(lower)
			return promptIntent{Type: intentLeisure, POIType: poiKw, POITagKey: tagKey, POITagVal: tagVal}
		}
	}

	// Bare place-name fallback: short prompts with no verbs/question words are
	// likely navigation targets (e.g. "Flughafen München", "Dingolfing").
	// Only apply when the text contains no question indicators.
	words := strings.Fields(lower)
	questionWords := []string{"was", "wie", "wann", "warum", "wer", "wo", "welche", "welcher", "welches", "what", "how", "when", "why", "who", "where", "which"}
	verbWords := []string{"ist", "sind", "hat", "haben", "kann", "gibt", "zeig", "erkläre", "erklar"}
	if len(words) >= 1 && len(words) <= 5 {
		isQuestion := false
		for _, w := range words {
			for _, qw := range questionWords {
				if w == qw {
					isQuestion = true
					break
				}
			}
			for _, vw := range verbWords {
				if w == vw {
					isQuestion = true
					break
				}
			}
		}
		if !isQuestion {
			return promptIntent{Type: intentNavigate, Destination: strings.TrimRight(lower, " ?!.")}
		}
	}

	return promptIntent{Type: intentUnknown}
}

// poiNearResult is a single POI with its distance from a reference point.
type poiNearResult struct {
	osmmini.AddressEntry
	DistM float64
	Label string
}

// searchPOIsNear returns up to limit POIs matching tagKey/tagVal sorted by
// distance from the given reference coordinates.
func (s *server) searchPOIsNear(lat, lon float64, tagKey, tagVal string, limit int) []poiNearResult {
	var results []poiNearResult

	s.poiMu.RLock()
	for _, w := range s.poiWays {
		v, ok := w.Tags[tagKey]
		if !ok || (tagVal != "" && !strings.EqualFold(v, tagVal)) {
			continue
		}
		var cx, cy float64
		var cnt int
		for _, nid := range w.NodeIDs {
			if c, ok2 := s.poiNodes[nid]; ok2 {
				cx += c.Lat
				cy += c.Lon
				cnt++
			}
		}
		if cnt == 0 {
			continue
		}
		coord := osmmini.Coord{Lat: cx / float64(cnt), Lon: cy / float64(cnt)}
		lbl := w.Tags["name"]
		if lbl == "" {
			lbl = w.Tags["brand"]
		}
		if lbl == "" {
			lbl = tagKey + "=" + tagVal
		}
		results = append(results, poiNearResult{
			AddressEntry: osmmini.AddressEntry{ID: w.ID, Coord: coord, Tags: w.Tags},
			DistM:        haversineMeters(lat, lon, coord.Lat, coord.Lon),
			Label:        lbl,
		})
	}
	s.poiMu.RUnlock()

	for _, a := range s.addrs {
		v, ok := a.Tags[tagKey]
		if !ok || (tagVal != "" && !strings.EqualFold(v, tagVal)) {
			continue
		}
		lbl := formatAddressLabel(a.Tags)
		results = append(results, poiNearResult{
			AddressEntry: a,
			DistM:        haversineMeters(lat, lon, a.Coord.Lat, a.Coord.Lon),
			Label:        lbl,
		})
	}

	slices.SortFunc(results, func(a, b poiNearResult) int {
		if a.DistM < b.DistM {
			return -1
		}
		if a.DistM > b.DistM {
			return 1
		}
		return 0
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results
}

// handleIntentLocally processes a classified prompt intent without an LLM.
// Returns true if the request was handled and an HTTP response was written.
func (s *server) handleIntentLocally(ctx context.Context, w http.ResponseWriter, req aiQueryRequest, intent promptIntent) bool {
	var qlat, qlon float64
	hasCoord := false
	if req.UserLat != nil && req.UserLon != nil {
		qlat, qlon = *req.UserLat, *req.UserLon
		hasCoord = true
	} else if req.MapLat != nil && req.MapLon != nil {
		qlat, qlon = *req.MapLat, *req.MapLon
		hasCoord = true
	}
	opt := s.settings.Get().Routing

	coordStr := func() string {
		if hasCoord {
			return fmt.Sprintf("%.6f,%.6f", qlat, qlon)
		}
		return req.RouteFrom
	}

	formatDur := func(durS float64) string {
		durMin := durS / 60
		if durMin < 60 {
			return fmt.Sprintf("%.0f min", durMin)
		}
		h := int(durMin) / 60
		m := int(durMin) % 60
		if m == 0 {
			return fmt.Sprintf("%d h", h)
		}
		return fmt.Sprintf("%d h %d min", h, m)
	}

	switch intent.Type {
	case intentNavigate:
		if intent.Destination == "" {
			return false
		}
		from := coordStr()
		// Prefer POI name match over address DB to avoid street names like
		// "Flughafenstraße, Nürnberg" winning over the actual airport.
		// Use distance-sorted lookup when user coordinates are available.
		destStr := intent.Destination
		if hasCoord {
			if coord, lbl, ok := s.resolvePOIFuzzyNear(intent.Destination, qlat, qlon); ok {
				_ = lbl
				destStr = fmt.Sprintf("%.6f,%.6f", coord.Lat, coord.Lon)
			}
		} else if coord, _, ok := s.resolvePOIFuzzy(intent.Destination); ok {
			destStr = fmt.Sprintf("%.6f,%.6f", coord.Lat, coord.Lon)
		}
		rr, fromAI, toAI, err := s.computeRouteFromLocQuery(ctx, from, destStr, opt)
		if err != nil {
			writeJSONError(w, http.StatusNotFound, "Route nicht gefunden: "+err.Error())
			return true
		}
		resp := fmt.Sprintf("Route nach **%s**: %.1f km, ca. %s.", toAI.Label, rr.DistanceM/1000, formatDur(rr.DurationS))
		writeJSON(w, http.StatusOK, aiQueryResponse{Provider: "local", Model: "navigate", Response: resp, Route: rr, From: fromAI, To: toAI})
		return true

	case intentDuration:
		origin := intent.Origin
		if origin == "" {
			origin = coordStr()
		}
		if intent.Destination == "" || origin == "" {
			return false
		}
		rr, fromAI, toAI, err := s.computeRouteFromLocQuery(ctx, origin, intent.Destination, opt)
		if err != nil {
			writeJSONError(w, http.StatusNotFound, "Route nicht gefunden: "+err.Error())
			return true
		}
		resp := fmt.Sprintf("Von **%s** nach **%s**: %.1f km, ca. %s.", fromAI.Label, toAI.Label, rr.DistanceM/1000, formatDur(rr.DurationS))
		writeJSON(w, http.StatusOK, aiQueryResponse{Provider: "local", Model: "duration", Response: resp, Route: rr, From: fromAI, To: toAI})
		return true

	case intentPOINear:
		if !hasCoord || intent.POITagKey == "" {
			return false
		}
		results := s.searchPOIsNear(qlat, qlon, intent.POITagKey, intent.POITagVal, 8)
		if len(results) == 0 {
			writeJSONError(w, http.StatusNotFound, fmt.Sprintf("Keine '%s' in der Nähe gefunden.", intent.POIType))
			return true
		}
		best := results[0]
		var routeObj *RouteResponse
		var fromAI, toAI *aiLocation
		if rr, fa, ta, err := s.computeRouteFromLocQuery(ctx, fmt.Sprintf("%.6f,%.6f", qlat, qlon), fmt.Sprintf("%.6f,%.6f", best.Coord.Lat, best.Coord.Lon), opt); err == nil {
			routeObj = rr
			fromAI = fa
			toAI = ta
			if toAI != nil {
				toAI.Label = best.Label
				toAI.Query = best.Label
			}
		}
		suggestions := make([]apiSearchResult, 0, len(results))
		for _, r := range results {
			suggestions = append(suggestions, apiSearchResult{ID: r.ID, Kind: "poi", Label: r.Label, Lat: r.Coord.Lat, Lon: r.Coord.Lon, Tags: r.Tags})
		}
		resp := fmt.Sprintf("%d **%s** in der Nähe gefunden. Nächste: **%s** (%.0f m).", len(results), intent.POIType, best.Label, best.DistM)
		if routeObj != nil {
			resp += " Route berechnet."
		}
		writeJSON(w, http.StatusOK, aiQueryResponse{Provider: "local", Model: "poi-near", Response: resp, Route: routeObj, From: fromAI, To: toAI, Suggestions: suggestions})
		return true

	case intentPOIOnRoute:
		if intent.POITagKey == "" || (req.RouteBBoxMinLat == 0 && req.RouteBBoxMaxLat == 0) {
			return false
		}
		refLat := (req.RouteBBoxMinLat + req.RouteBBoxMaxLat) / 2
		refLon := (req.RouteBBoxMinLon + req.RouteBBoxMaxLon) / 2
		var results []poiNearResult
		s.poiMu.RLock()
		for _, w := range s.poiWays {
			v, ok := w.Tags[intent.POITagKey]
			if !ok || (intent.POITagVal != "" && !strings.EqualFold(v, intent.POITagVal)) {
				continue
			}
			var cx, cy float64
			var cnt int
			for _, nid := range w.NodeIDs {
				if c, ok2 := s.poiNodes[nid]; ok2 {
					cx += c.Lat
					cy += c.Lon
					cnt++
				}
			}
			if cnt == 0 {
				continue
			}
			clat, clon := cx/float64(cnt), cy/float64(cnt)
			if clat < req.RouteBBoxMinLat || clat > req.RouteBBoxMaxLat ||
				clon < req.RouteBBoxMinLon || clon > req.RouteBBoxMaxLon {
				continue
			}
			lbl := w.Tags["name"]
			if lbl == "" {
				lbl = w.Tags["brand"]
			}
			if lbl == "" {
				lbl = intent.POITagKey + "=" + intent.POITagVal
			}
			results = append(results, poiNearResult{
				AddressEntry: osmmini.AddressEntry{ID: w.ID, Coord: osmmini.Coord{Lat: clat, Lon: clon}, Tags: w.Tags},
				DistM:        haversineMeters(refLat, refLon, clat, clon),
				Label:        lbl,
			})
		}
		s.poiMu.RUnlock()
		slices.SortFunc(results, func(a, b poiNearResult) int {
			if a.DistM < b.DistM {
				return -1
			}
			if a.DistM > b.DistM {
				return 1
			}
			return 0
		})
		if len(results) > 8 {
			results = results[:8]
		}
		if len(results) == 0 {
			writeJSONError(w, http.StatusNotFound, fmt.Sprintf("Keine '%s' auf der Route gefunden.", intent.POIType))
			return true
		}
		suggestions := make([]apiSearchResult, 0, len(results))
		for _, r := range results {
			suggestions = append(suggestions, apiSearchResult{ID: r.ID, Kind: "poi", Label: r.Label, Lat: r.Coord.Lat, Lon: r.Coord.Lon, Tags: r.Tags})
		}
		resp := fmt.Sprintf("%d **%s** auf der Route. Nächste: **%s**.", len(results), intent.POIType, results[0].Label)
		writeJSON(w, http.StatusOK, aiQueryResponse{Provider: "local", Model: "poi-on-route", Response: resp, Suggestions: suggestions})
		return true

	case intentMultiStop:
		if intent.Destination == "" || !hasCoord {
			return false
		}
		fromStr := fmt.Sprintf("%.6f,%.6f", qlat, qlon)
		// Find the nearest intermediate POI stop if a type was specified.
		var stopCoord *osmmini.Coord
		var stopLabel string
		if intent.POITagKey != "" {
			if poiRes := s.searchPOIsNear(qlat, qlon, intent.POITagKey, intent.POITagVal, 1); len(poiRes) > 0 {
				c := poiRes[0].Coord
				stopCoord = &c
				stopLabel = poiRes[0].Label
			}
		}
		if stopCoord != nil {
			stopStr := fmt.Sprintf("%.6f,%.6f", stopCoord.Lat, stopCoord.Lon)
			rr1, _, _, err1 := s.computeRouteFromLocQuery(ctx, fromStr, stopStr, opt)
			rr2, _, toAI2, err2 := s.computeRouteFromLocQuery(ctx, stopStr, intent.Destination, opt)
			if err1 == nil && err2 == nil {
				combined := &RouteResponse{
					From:      rr1.From,
					To:        rr2.To,
					Engine:    rr2.Engine,
					Objective: rr2.Objective,
					Profile:   rr2.Profile,
					DistanceM: rr1.DistanceM + rr2.DistanceM,
					DurationS: rr1.DurationS + rr2.DurationS,
					Path:      append(rr1.Path, rr2.Path...),
					Steps:     append(rr1.Steps, rr2.Steps...),
				}
				startLat, startLon := rr1.From.Lat, rr1.From.Lon
				fa := &aiLocation{Query: fromStr, Label: rr1.From.Label, Lat: &startLat, Lon: &startLon}
				stopLatV, stopLonV := stopCoord.Lat, stopCoord.Lon
				stopAI := aiLocation{Query: stopLabel, Label: stopLabel, Lat: &stopLatV, Lon: &stopLonV}
				resp := fmt.Sprintf("Route mit Zwischenstopp bei **%s** nach **%s**: %.1f km, ca. %s.",
					stopLabel, toAI2.Label, combined.DistanceM/1000, formatDur(combined.DurationS))
				writeJSON(w, http.StatusOK, aiQueryResponse{Provider: "local", Model: "multi-stop", Response: resp, Route: combined, From: fa, To: toAI2, Waypoints: []aiLocation{stopAI}})
				return true
			}
		}
		// Fallback: direct route without stop.
		rr, fromAI, toAI, err := s.computeRouteFromLocQuery(ctx, fromStr, intent.Destination, opt)
		if err != nil {
			return false
		}
		resp := fmt.Sprintf("Route nach **%s**: %.1f km, ca. %s.", toAI.Label, rr.DistanceM/1000, formatDur(rr.DurationS))
		if intent.POITagKey != "" {
			resp += fmt.Sprintf(" (Kein '%s' auf dem Weg gefunden.)", intent.POIType)
		}
		writeJSON(w, http.StatusOK, aiQueryResponse{Provider: "local", Model: "multi-stop", Response: resp, Route: rr, From: fromAI, To: toAI})
		return true

	case intentLeisure:
		if !hasCoord || intent.POITagKey == "" {
			return false
		}
		results := s.searchPOIsNear(qlat, qlon, intent.POITagKey, intent.POITagVal, 5)
		if len(results) == 0 {
			return false
		}
		best := results[0]
		var routeObj *RouteResponse
		var fromAI, toAI *aiLocation
		if rr, fa, ta, err := s.computeRouteFromLocQuery(ctx, fmt.Sprintf("%.6f,%.6f", qlat, qlon), fmt.Sprintf("%.6f,%.6f", best.Coord.Lat, best.Coord.Lon), opt); err == nil {
			routeObj = rr
			fromAI = fa
			toAI = ta
			if toAI != nil {
				toAI.Label = best.Label
				toAI.Query = best.Label
			}
		}
		suggestions := make([]apiSearchResult, 0, len(results))
		for _, r := range results {
			suggestions = append(suggestions, apiSearchResult{ID: r.ID, Kind: "poi", Label: r.Label, Lat: r.Coord.Lat, Lon: r.Coord.Lon, Tags: r.Tags})
		}
		resp := fmt.Sprintf("Nächstes passendes Ziel: **%s** (%.0f m).", best.Label, best.DistM)
		if routeObj != nil {
			resp += fmt.Sprintf(" %.1f km, ca. %s.", routeObj.DistanceM/1000, formatDur(routeObj.DurationS))
		}
		writeJSON(w, http.StatusOK, aiQueryResponse{Provider: "local", Model: "leisure", Response: resp, Route: routeObj, From: fromAI, To: toAI, Suggestions: suggestions})
		return true
	}
	return false
}

// extractRouteAction scans an LLM response text for a ```route-action``` code
// block and returns the from/to values when one is found. The block must
// contain a JSON object with "action":"compute_route", "from", and "to" keys.
// The function also returns the cleaned response text with the block removed.
func extractRouteAction(text string) (from, to, cleanText string, found bool) {
	// Match ```route-action\n{...}\n``` (allow optional spaces/newlines)
	re := regexp.MustCompile("(?s)```route-action\\s*\\n(\\{[^`]+\\})\\s*```")
	m := re.FindStringSubmatchIndex(text)
	if m == nil {
		return "", "", text, false
	}
	jsonStr := strings.TrimSpace(text[m[2]:m[3]])
	var action struct {
		Action string `json:"action"`
		From   string `json:"from"`
		To     string `json:"to"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &action); err != nil {
		return "", "", text, false
	}
	if action.Action != "compute_route" || action.To == "" {
		return "", "", text, false
	}
	// Remove the block from the text and trim trailing whitespace/newlines.
	clean := strings.TrimRight(text[:m[0]], " \t\n\r") + strings.TrimLeft(text[m[1]:], " \t\n\r")
	clean = strings.TrimSpace(clean)
	return action.From, action.To, clean, true
}

// resolvePOIFuzzyNear is like resolvePOIFuzzy but sorts all candidates by
// distance from (lat, lon) and returns the nearest match. This prevents address
// entries like "Flughafenstraße, Nürnberg" from winning over "Flughafen München"
// when the user is near Munich.
func (s *server) resolvePOIFuzzyNear(query string, lat, lon float64) (osmmini.Coord, string, bool) {
	norm := normalizeForCompare(query)
	if norm == "" {
		return osmmini.Coord{}, "", false
	}
	type cand struct {
		coord osmmini.Coord
		label string
		dist  float64
	}
	var cands []cand
	s.poiMu.RLock()
	for _, w := range s.poiWays {
		name := normalizeForCompare(w.Tags["name"])
		brand := normalizeForCompare(w.Tags["brand"])
		if !strings.Contains(name, norm) && !strings.Contains(norm, name) &&
			!strings.Contains(brand, norm) && !strings.Contains(norm, brand) {
			continue
		}
		var cx, cy float64
		var cnt int
		for _, nid := range w.NodeIDs {
			if c, ok := s.poiNodes[nid]; ok {
				cx += c.Lat
				cy += c.Lon
				cnt++
			}
		}
		if cnt == 0 {
			continue
		}
		coord := osmmini.Coord{Lat: cx / float64(cnt), Lon: cy / float64(cnt)}
		lbl := w.Tags["name"]
		if lbl == "" {
			lbl = w.Tags["brand"]
		}
		cands = append(cands, cand{coord: coord, label: lbl, dist: haversineMeters(lat, lon, coord.Lat, coord.Lon)})
	}
	s.poiMu.RUnlock()
	for _, a := range s.addrs {
		name := normalizeForCompare(a.Tags["name"])
		if name != "" && (strings.Contains(name, norm) || strings.Contains(norm, name)) {
			cands = append(cands, cand{coord: a.Coord, label: formatAddressLabel(a.Tags), dist: haversineMeters(lat, lon, a.Coord.Lat, a.Coord.Lon)})
		}
	}
	if len(cands) == 0 {
		return osmmini.Coord{}, "", false
	}
	slices.SortFunc(cands, func(a, b cand) int {
		if a.dist < b.dist {
			return -1
		}
		if a.dist > b.dist {
			return 1
		}
		return 0
	})
	return cands[0].coord, cands[0].label, true
}

// resolvePOIFuzzy searches the POI way/address index for a name that contains
// the query string (case-insensitive). Returns the best matching coord and label.
func (s *server) resolvePOIFuzzy(query string) (osmmini.Coord, string, bool) {
	norm := normalizeForCompare(query)
	if norm == "" {
		return osmmini.Coord{}, "", false
	}
	s.poiMu.RLock()
	defer s.poiMu.RUnlock()

	// Search ways first (usually have better centroid data).
	for _, w := range s.poiWays {
		name := normalizeForCompare(w.Tags["name"])
		brand := normalizeForCompare(w.Tags["brand"])
		if name == "" && brand == "" {
			continue
		}
		if !strings.Contains(name, norm) && !strings.Contains(norm, name) &&
			!strings.Contains(brand, norm) && !strings.Contains(norm, brand) {
			continue
		}
		var cx, cy float64
		var cnt int
		for _, nid := range w.NodeIDs {
			if c, ok := s.poiNodes[nid]; ok {
				cx += c.Lat
				cy += c.Lon
				cnt++
			}
		}
		if cnt == 0 {
			continue
		}
		label := w.Tags["name"]
		if label == "" {
			label = w.Tags["brand"]
		}
		return osmmini.Coord{Lat: cx / float64(cnt), Lon: cy / float64(cnt)}, label, true
	}
	// Fall back to address entries.
	for _, a := range s.addrs {
		name := normalizeForCompare(a.Tags["name"])
		if name != "" && (strings.Contains(name, norm) || strings.Contains(norm, name)) {
			return a.Coord, formatAddressLabel(a.Tags), true
		}
	}
	return osmmini.Coord{}, "", false
}

// computeRouteFromLocQuery resolves from/to location strings and runs the
// router, returning a fully-populated RouteResponse ready for the UI.
// Either argument may be empty (treated as "use map center / best guess").
// Resolution order: lat,lon parse → address index → POI fuzzy search.
func (s *server) computeRouteFromLocQuery(ctx context.Context, from, to string, opt osmmini.RouteOptions) (*RouteResponse, *aiLocation, *aiLocation, error) {
	fromLoc := Location{Query: from}
	toLoc := Location{Query: to}

	fromCoord, fromLabel, _, err := s.resolveLocation(fromLoc)
	if err != nil {
		// Try POI fuzzy fallback.
		if c, lbl, ok := s.resolvePOIFuzzy(from); ok {
			fromCoord = c
			fromLabel = lbl
		} else {
			return nil, nil, nil, fmt.Errorf("start: %w", err)
		}
	}
	toCoord, toLabel, _, err := s.resolveLocation(toLoc)
	if err != nil {
		// Try POI fuzzy fallback.
		if c, lbl, ok := s.resolvePOIFuzzy(to); ok {
			toCoord = c
			toLabel = lbl
		} else {
			return nil, nil, nil, fmt.Errorf("ziel '%s': %w", to, err)
		}
	}

	startID, startSnap, okF := s.router.NearestNode(fromCoord.Lat, fromCoord.Lon)
	if !okF {
		return nil, nil, nil, fmt.Errorf("kein Startknoten gefunden")
	}
	endID, endSnap, okT := s.router.NearestNode(toCoord.Lat, toCoord.Lon)
	if !okT {
		return nil, nil, nil, fmt.Errorf("kein Zielknoten gefunden")
	}

	res, err := s.router.RouteWithOptions(ctx, startID, endID, opt)
	if err != nil {
		return nil, nil, nil, err
	}

	steps := s.router.ManeuversForPath(res.Path, opt)
	gURL := buildGoogleMapsURL([]osmmini.Coord{fromCoord, toCoord}, 0)
	aURL := buildAppleMapsURL([]osmmini.Coord{fromCoord, toCoord})

	rr := &RouteResponse{
		From:          RoutePoint{Input: from, Label: fromLabel, Lat: fromCoord.Lat, Lon: fromCoord.Lon, Node: startID, SnapM: startSnap},
		To:            RoutePoint{Input: to, Label: toLabel, Lat: toCoord.Lat, Lon: toCoord.Lon, Node: endID, SnapM: endSnap},
		Engine:        string(res.Engine),
		Objective:     string(res.Objective),
		Profile:       string(opt.Profile),
		Cost:          res.Cost,
		DistanceM:     res.DistanceM,
		DurationS:     res.DurationS,
		Path:          res.PathCoords,
		GoogleMapsURL: gURL,
		AppleMapsURL:  aURL,
		Steps:         steps,
	}
	fromLat, fromLon := fromCoord.Lat, fromCoord.Lon
	toLat, toLon := toCoord.Lat, toCoord.Lon
	fromAI := &aiLocation{Query: from, Label: fromLabel, Lat: &fromLat, Lon: &fromLon}
	toAI := &aiLocation{Query: to, Label: toLabel, Lat: &toLat, Lon: &toLon}
	return rr, fromAI, toAI, nil
}

// queryOllama sends a chat completion request to an Ollama instance.
func queryOllama(ctx context.Context, baseURL, model, systemPrompt, userPrompt string) (string, error) {
	payload := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"stream": false,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: aiQueryTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama returned %d", resp.StatusCode)
	}
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxAIResponseBytes))
	if err != nil {
		return "", err
	}
	var result struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", err
	}
	return result.Message.Content, nil
}

// queryOpenAICompatible sends a chat completion request to an OpenAI-compatible API (LM Studio).
func queryOpenAICompatible(ctx context.Context, baseURL, model, systemPrompt, userPrompt string) (string, error) {
	payload := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"max_tokens": 1024,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: aiQueryTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("lm studio returned %d", resp.StatusCode)
	}
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxAIResponseBytes))
	if err != nil {
		return "", err
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", err
	}
	if len(result.Choices) == 0 {
		return "", errors.New("no choices returned")
	}
	return result.Choices[0].Message.Content, nil
}

// haversineMeters returns distance in meters between two lat/lon points.
func haversineMeters(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371000.0
	toRad := func(d float64) float64 { return d * math.Pi / 180.0 }
	dLat := toRad(lat2 - lat1)
	dLon := toRad(lon2 - lon1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(toRad(lat1))*math.Cos(toRad(lat2))*math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return R * c
}

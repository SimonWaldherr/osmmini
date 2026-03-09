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

// Settings holds the server configuration persisted in the settings.json
// file. The server loads these settings early during startup so routing
// defaults (e.g. highway speeds) can be applied before the graph is built.
// Use the HTTP API `PUT /api/v1/settings` to update them at runtime; some
// changes (like default speeds used during graph import) require rebuilding
// the graph or restarting the server to take full effect.
type Settings struct {
	Routing osmmini.RouteOptions `json:"routing"`
	Tiles   TileSettings         `json:"tiles"`
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

	// UI
	mux.HandleFunc("/", s.handleIndex)

	return withLogging(withCORS(mux))
}

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
		writeJSONError(w, http.StatusBadRequest, "from: "+err.Error())
		return
	}
	toCoord, toLabel, toInput, err := s.resolveLocation(req.To)
	if err != nil {
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

	// Build system prompt with context about available routing features
	systemPrompt := "Du bist ein Routing-Assistent für OSMmini, ein Offline-Routing-System. " +
		"Du kannst Fragen zu Routenplanung, Navigation und Verkehr beantworten. " +
		"Verfügbare Features: A*-, Dijkstra- und CH-Routing, TSP-Optimierung (bis 16 Stops exakt, " +
		"darüber hinaus Greedy mit 2-opt), Linksabbiege-Vermeidung, Ampelstrafen, " +
		"BOS/Einsatzmodus (Feuerwehr, Rettungsdienst), Fahrzeugbeschränkungen (Höhe, Gewicht). " +
		"Antworte kurz und hilfreich auf Deutsch."

	// Quick heuristic: if the user asks for the "nearest" X, try to answer
	// directly from local OSM data instead of querying the LLM. This allows
	// the assistant to return actual nearby POIs and compute a route when a
	// reference location (lat/lon) is provided in the prompt.
	lower := strings.ToLower(req.Prompt)
	if strings.Contains(lower, "nächste") || strings.Contains(lower, "nächster") || strings.Contains(lower, "nearest") || strings.Contains(lower, "near me") || strings.Contains(lower, "in der nähe") {
		// Determine query coordinates: prefer explicit lat/lon fields from the
		// request, otherwise try to extract coordinates from the prompt text.
		coordFound := false
		var qlat, qlon float64
		if req.Lat != nil && req.Lon != nil {
			qlat, qlon = *req.Lat, *req.Lon
			coordFound = true
		}

		// crude float pair regex for coords in prompt
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

		// Normalize common brand/name variants (e.g., McDonald's)
		norm := func(s string) string {
			s = strings.ToLower(s)
			s = strings.ReplaceAll(s, "'", "")
			s = strings.ReplaceAll(s, "\u2019", "") // right single quote
			s = strings.ReplaceAll(s, "\u2018", "") // left single quote
			s = strings.ReplaceAll(s, "-", "")
			s = strings.ReplaceAll(s, " ", "")
			s = strings.ReplaceAll(s, "mcdonalds", "mcdonalds")
			s = strings.ReplaceAll(s, "mcdonald", "mcdonalds")
			s = strings.ReplaceAll(s, "mcdo", "mcdonalds")
			return s
		}
		placeNorm := norm(place)

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
		// If we didn't handle a POI keyword specifically, fall back to structured address search
		if !isPOIHandled {
			aq := osmmini.ParseAddressGuess(place)
			matches = osmmini.SearchAddresses(s.addrs, aq, 50)
		}

		// If no structured matches, try fuzzy name/brand scan over addresses
		if len(matches) == 0 {
			for _, a := range s.addrs {
				name := strings.ToLower(a.Tags["name"])
				brand := strings.ToLower(a.Tags["brand"])
				if name != "" && strings.Contains(strings.ReplaceAll(name, " ", ""), placeNorm) {
					matches = append(matches, a)
					continue
				}
				if brand != "" && strings.Contains(strings.ReplaceAll(brand, " ", ""), placeNorm) {
					matches = append(matches, a)
					continue
				}
				// also check amenity/brand combinations for fast_food
				if a.Tags["amenity"] == "fast_food" && (strings.Contains(name, "mcdonald") || strings.Contains(brand, "mcdonald")) {
					matches = append(matches, a)
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

	// Try Ollama first, then LM Studio
	providers := []struct {
		name    string
		baseURL string
		models  string
	}{
		{"ollama", "http://localhost:11434", "/api/tags"},
		{"lmstudio", "http://localhost:1234", "/v1/models"},
	}

	for _, prov := range providers {
		p := probeAIProvider(ctx, prov.name, prov.baseURL, prov.models)
		if !p.Available || len(p.Models) == 0 {
			continue
		}

		model := req.Model
		if model == "" {
			model = p.Models[0]
		}

		var respText string
		var err error
		if prov.name == "ollama" {
			respText, err = queryOllama(ctx, prov.baseURL, model, systemPrompt, req.Prompt)
		} else {
			respText, err = queryOpenAICompatible(ctx, prov.baseURL, model, systemPrompt, req.Prompt)
		}
		if err != nil {
			continue
		}

		writeJSON(w, http.StatusOK, aiQueryResponse{
			Provider: prov.name,
			Model:    model,
			Response: respText,
		})
		return
	}

	writeJSONError(w, http.StatusServiceUnavailable, "Kein KI-Provider verfügbar. Bitte Ollama oder LM Studio starten.")
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

package osmmini

import (
	"cmp"
	"container/heap"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// RouteEngine selects the pathfinding engine implementation.
// - "astar": A* with admissible heuristic
// - "dijkstra": A* with h=0 (equivalent to Dijkstra)
type RouteEngine string

const (
	EngineAStar    RouteEngine = "astar"
	EngineDijkstra RouteEngine = "dijkstra" // A* with h=0
	// EngineDijkstraNode is a simple node-based Dijkstra implementation
	// that ignores turn-state penalties and uses per-edge costs only.
	// It is provided as an alternative algorithm for comparison/testing.
	EngineDijkstraNode RouteEngine = "dijkstra-node"
	// EngineCH is a minimal Contraction Hierarchies query over an upward-only
	// graph using bidirectional Dijkstra. Preprocessing builds ranks and
	// upward adjacency; shortcut generation is minimal and can be extended.
	EngineCH RouteEngine = "ch"
)

// Objective controls the routing objective used for scoring edges:
// - distance: minimize length in meters
// - duration: minimize travel time (seconds)
// - economy: minimize an economy cost proxy
type Objective string

const (
	ObjectiveDistance Objective = "distance"
	ObjectiveDuration Objective = "duration"
	ObjectiveEconomy  Objective = "economy"
)

// VehicleProfile is an identifier for a named travel profile.
// Profiles encode sensible defaults for specific use-cases (max speed,
// allowed highway types, objective, weights) and can be used as a
// convenient shortcut instead of supplying all individual RouteOptions
// fields. When a non-empty profile is set in RouteOptions, its defaults
// are applied before any explicit field overrides.
type VehicleProfile string

const (
	ProfileCar          VehicleProfile = "car"          // standard passenger car
	ProfileDelivery     VehicleProfile = "delivery"     // light delivery van (urban streets)
	ProfileTruck        VehicleProfile = "truck"        // heavy goods vehicle (HGV)
	ProfileTravel       VehicleProfile = "travel"       // long-distance / touring
	ProfileFirefighting VehicleProfile = "firefighting" // fire engine (ignores some restrictions)
	ProfileEmergency    VehicleProfile = "emergency"    // ambulance / police
	ProfileCycling      VehicleProfile = "cycling"      // bicycle
	ProfileWalking      VehicleProfile = "walking"      // pedestrian
)

// VehicleProfileDef defines the pre-set routing parameters for a VehicleProfile.
type VehicleProfileDef struct {
	ID            VehicleProfile `json:"id"`
	Label         string         `json:"label"`
	Icon          string         `json:"icon"`
	Objective     Objective      `json:"objective"`
	MaxSpeedKph   float64        `json:"max_speed_kph"`
	AllowedHwySet []string       `json:"allowed_highway_types,omitempty"` // nil = all driveable
	// SpeedScale multiplies the edge speed for this profile (e.g. 0.25 for cycling)
	SpeedScale float64 `json:"speed_scale,omitempty"`
	LeftTurn   float64 `json:"left_turn,omitempty"`
	UTurn      float64 `json:"u_turn,omitempty"`
}

// BuiltinProfiles lists the named travel profiles available out of the box.
var BuiltinProfiles = []VehicleProfileDef{
	{
		ID: ProfileCar, Label: "Pkw", Icon: "🚗",
		Objective: ObjectiveDuration, MaxSpeedKph: 130,
		AllowedHwySet: []string{"motorway", "trunk", "primary", "secondary", "tertiary",
			"unclassified", "residential", "living_street", "service", "road",
			"motorway_link", "trunk_link", "primary_link", "secondary_link", "tertiary_link"},
	},
	{
		ID: ProfileDelivery, Label: "Lieferfahrzeug", Icon: "🚚",
		Objective: ObjectiveDuration, MaxSpeedKph: 80,
		AllowedHwySet: []string{"primary", "secondary", "tertiary", "unclassified",
			"residential", "living_street", "service", "road",
			"primary_link", "secondary_link", "tertiary_link"},
		LeftTurn: 3, UTurn: 10,
	},
	{
		ID: ProfileTruck, Label: "LKW / HGV", Icon: "🚛",
		Objective: ObjectiveDuration, MaxSpeedKph: 90,
		AllowedHwySet: []string{"motorway", "trunk", "primary", "secondary",
			"motorway_link", "trunk_link", "primary_link"},
	},
	{
		ID: ProfileTravel, Label: "Fernreise / Touring", Icon: "🛣️",
		Objective: ObjectiveEconomy, MaxSpeedKph: 130,
		AllowedHwySet: []string{"motorway", "trunk", "primary", "secondary", "tertiary",
			"motorway_link", "trunk_link", "primary_link", "secondary_link"},
	},
	{
		ID: ProfileFirefighting, Label: "Feuerwehr", Icon: "🚒",
		Objective: ObjectiveDuration, MaxSpeedKph: 130,
		// fire engines use all roads; no explicit restriction
	},
	{
		ID: ProfileEmergency, Label: "Rettungsdienst / Polizei", Icon: "🚑",
		Objective: ObjectiveDuration, MaxSpeedKph: 130,
		// emergency vehicles use all roads; no explicit restriction
	},
	{
		ID: ProfileCycling, Label: "Fahrrad", Icon: "🚲",
		Objective: ObjectiveDistance, MaxSpeedKph: 25,
		AllowedHwySet: []string{"primary", "secondary", "tertiary", "unclassified",
			"residential", "living_street", "service", "track", "road",
			"primary_link", "secondary_link", "tertiary_link"},
		SpeedScale: 0.25,
	},
	{
		ID: ProfileWalking, Label: "Zu Fuß", Icon: "🚶",
		Objective: ObjectiveDistance, MaxSpeedKph: 6,
		// pedestrians use all non-motorway roads and paths
		AllowedHwySet: []string{"secondary", "tertiary", "unclassified",
			"residential", "living_street", "service", "track", "road", "path",
			"secondary_link", "tertiary_link"},
		SpeedScale: 0.05,
	},
}

// profileDefByID returns the VehicleProfileDef for id, or nil if not found.
func profileDefByID(id VehicleProfile) *VehicleProfileDef {
	for i := range BuiltinProfiles {
		if BuiltinProfiles[i].ID == id {
			return &BuiltinProfiles[i]
		}
	}
	return nil
}

// ProWeights contains optional advanced routing weights and vehicle constraints.
// Values influence penalties (turns, crossings) and maximum assumed speed
// used by heuristics and cost calculations.
type ProWeights struct {
	LeftTurn            float64 `json:"left_turn"`
	RightTurn           float64 `json:"right_turn"`
	UTurn               float64 `json:"u_turn"`
	Crossing            float64 `json:"crossing"`
	MaxSpeedKph         float64 `json:"max_speed_kph"`
	VehicleHeightM      float64 `json:"vehicle_height_m"`
	VehicleWeightT      float64 `json:"vehicle_weight_t"`
	NoLeftTurn          bool    `json:"no_left_turn,omitempty"`
	TrafficLightPenalty float64 `json:"traffic_light_penalty,omitempty"`
}

// RouteOptions are supplied to routing calls to control engine, objective
// and pro-level weights/constraints.
// When Profile is set the matching VehicleProfileDef supplies sensible
// defaults; explicit fields in Weights override those defaults.
type RouteOptions struct {
	Engine        RouteEngine    `json:"engine,omitempty"`
	Objective     Objective      `json:"objective"`
	Profile       VehicleProfile `json:"profile,omitempty"`
	Pro           bool           `json:"pro"`
	Weights       ProWeights     `json:"weights"`
	EmergencyMode bool           `json:"emergency_mode,omitempty"`
}

func (o RouteOptions) withDefaults() RouteOptions {
	// Apply profile defaults first, then override with explicit values.
	if o.Profile != "" {
		if def := profileDefByID(o.Profile); def != nil {
			if o.Objective == "" {
				o.Objective = def.Objective
			}
			if o.Weights.MaxSpeedKph <= 0 {
				o.Weights.MaxSpeedKph = def.MaxSpeedKph
			}
			if o.Weights.LeftTurn == 0 && def.LeftTurn > 0 {
				o.Weights.LeftTurn = def.LeftTurn
			}
			if o.Weights.UTurn == 0 && def.UTurn > 0 {
				o.Weights.UTurn = def.UTurn
			}
		}
	}

	if o.Engine == "" {
		o.Engine = EngineAStar
	}
	if o.Engine != EngineAStar && o.Engine != EngineDijkstra && o.Engine != EngineDijkstraNode {
		o.Engine = EngineAStar
	}
	if o.Objective == "" {
		// default objective falls back to distance when not set
		o.Objective = ObjectiveDistance
	}
	if o.Weights.MaxSpeedKph <= 0 {
		o.Weights.MaxSpeedKph = 130
	}
	if o.Weights.MaxSpeedKph < 5 {
		o.Weights.MaxSpeedKph = 5
	}
	return o
}

// profileAllowedHwySet returns the set of allowed highway types for the given
// options, or nil if no profile restriction applies.
func profileAllowedHwySet(opt RouteOptions) map[string]bool {
	if opt.Profile == "" {
		return nil
	}
	def := profileDefByID(opt.Profile)
	if def == nil || len(def.AllowedHwySet) == 0 {
		return nil
	}
	m := make(map[string]bool, len(def.AllowedHwySet))
	for _, h := range def.AllowedHwySet {
		m[h] = true
	}
	return m
}

// profileSpeedScale returns the speed scale factor for the given options.
func profileSpeedScale(opt RouteOptions) float64 {
	if opt.Profile == "" {
		return 1
	}
	def := profileDefByID(opt.Profile)
	if def == nil || def.SpeedScale <= 0 {
		return 1
	}
	return def.SpeedScale
}

type Edge struct {
	To         int64
	DistM      float64
	SpeedKph   float64
	MaxHeightM float64
	MaxWeightT float64
	HwyType    string // OSM highway tag value (e.g. "residential")
}

type Graph struct {
	coords map[int64]Coord
	adj    map[int64][]Edge
}

// Router wraps a road graph built from OSM highways and provides
// nearest-node lookup, street search and pathfinding (A*/Dijkstra).
type Router struct {
	g       Graph
	streets map[string]streetEntry // normalized -> display + sample nodes
	idx     *spatialIndex
	bounds  *CoordWindow

	// CH data
	ch *chData
}

type streetEntry struct {
	Display string
	NodeIDs []int64
}

// BuildOptions controls router graph extraction.
type BuildOptions struct {
	Window        *CoordWindow
	WindowBufferM float64
}

func BuildRouterWithAddresses(path string) (*Router, []AddressEntry, error) {
	return BuildRouterWithAddressesOptions(path, BuildOptions{})
}

// BuildRouterWithAddressesOptions parses a PBF, builds a highway graph and collects address nodes.
// If Window is set, nodes/addresses are filtered to Window expanded by WindowBufferM.
func BuildRouterWithAddressesOptions(path string, bo BuildOptions) (*Router, []AddressEntry, error) {
	var filterWin *CoordWindow
	if bo.Window != nil && bo.Window.Valid() {
		w := *bo.Window
		if bo.WindowBufferM > 0 {
			w = w.ExpandMeters(bo.WindowBufferM)
		}
		filterWin = &w
	}

	coordsAll := make(map[int64]Coord, 1<<20)
	addrs := make([]AddressEntry, 0, 1<<16)
	highways := make([]highwayWay, 0, 1<<18)
	streets := make(map[string]streetEntry, 1<<18)

	opts := Options{
		EmitWayNodeIDs:      true,
		EmitRelationMembers: false,
		KeepTag: func(k string) bool {
			switch {
			case k == "highway", k == "name", k == "maxspeed", k == "oneway", k == "junction":
				return true
			case k == "maxheight", k == "maxweight":
				return true
			case k == "access", k == "vehicle", k == "motor_vehicle", k == "hgv":
				return true
			case strings.HasPrefix(k, "addr:"):
				return true
			default:
				return false
			}
		},
	}

	cb := Callbacks{
		Node: func(id int64, lat, lon float64) error {
			c := Coord{Lat: lat, Lon: lon}
			if filterWin != nil && !filterWin.Contains(c) {
				return nil
			}
			coordsAll[id] = c
			return nil
		},
		AddressNode: func(n Node) error {
			c := Coord{Lat: n.Lat, Lon: n.Lon}
			if filterWin != nil && !filterWin.Contains(c) {
				return nil
			}
			addrs = append(addrs, AddressEntry{ID: n.ID, Coord: c, Tags: n.Tags})
			return nil
		},
		HighwayWay: func(w Way) error {
			hwy := w.Tags["highway"]
			if !isDriveableHighway(hwy, w.Tags) {
				return nil
			}
			if isAccessDenied(w.Tags) {
				return nil
			}
			if len(w.NodeIDs) < 2 {
				return nil
			}

			speed := parseMaxSpeedKph(w.Tags["maxspeed"])
			if speed <= 0 {
				speed = defaultSpeedForHighway(hwy)
			}
			if speed <= 0 {
				speed = 50
			}

			mh := parseMeters(w.Tags["maxheight"])
			mw := parseTons(w.Tags["maxweight"])

			oneway := parseOneway(w.Tags["oneway"])
			if oneway == 0 && strings.EqualFold(w.Tags["junction"], "roundabout") {
				oneway = 1
			}

			ids := make([]int64, len(w.NodeIDs))
			copy(ids, w.NodeIDs)
			highways = append(highways, highwayWay{
				nodes:      ids,
				hwyType:    strings.ToLower(strings.TrimSpace(hwy)),
				speedKph:   speed,
				maxHeightM: mh,
				maxWeightT: mw,
				oneway:     oneway,
			})

			if name := w.Tags["name"]; name != "" {
				key := normalize(name)
				se := streets[key]
				if se.Display == "" || len(name) > len(se.Display) {
					se.Display = name
				}
				se.NodeIDs = appendUniqueLimited(se.NodeIDs, sampleWayNodeIDs(ids), 16)
				streets[key] = se
			}
			return nil
		},
	}

	if err := ExtractFile(path, opts, cb); err != nil {
		return nil, nil, err
	}

	adj := make(map[int64][]Edge, 1<<20)
	addEdge := func(from, to int64, dist float64, hw highwayWay) {
		adj[from] = append(adj[from], Edge{
			To:         to,
			DistM:      dist,
			SpeedKph:   hw.speedKph,
			MaxHeightM: hw.maxHeightM,
			MaxWeightT: hw.maxWeightT,
			HwyType:    hw.hwyType,
		})
	}

	for _, hw := range highways {
		ids := hw.nodes
		for i := 0; i < len(ids)-1; i++ {
			aID := ids[i]
			bID := ids[i+1]
			a, okA := coordsAll[aID]
			b, okB := coordsAll[bID]
			if !okA || !okB {
				continue
			}
			d := haversineMeters(a.Lat, a.Lon, b.Lat, b.Lon)
			if d <= 0 || math.IsNaN(d) || math.IsInf(d, 0) {
				continue
			}
			switch hw.oneway {
			case 1:
				addEdge(aID, bID, d, hw)
			case -1:
				addEdge(bID, aID, d, hw)
			default:
				addEdge(aID, bID, d, hw)
				addEdge(bID, aID, d, hw)
			}
		}
	}

	// prune coords to nodes that actually have edges
	coords := make(map[int64]Coord, len(adj))
	for id := range adj {
		if c, ok := coordsAll[id]; ok && len(adj[id]) > 0 {
			coords[id] = c
		}
	}

	// prune streets samples
	for k, se := range streets {
		pruned := se.NodeIDs[:0]
		for _, id := range se.NodeIDs {
			if _, ok := coords[id]; ok {
				pruned = append(pruned, id)
			}
		}
		se.NodeIDs = pruned
		if len(se.NodeIDs) == 0 {
			delete(streets, k)
			continue
		}
		streets[k] = se
	}

	idx := buildSpatialIndex(coords)

	var bounds *CoordWindow
	if b, ok := computeBounds(coords); ok {
		bounds = &b
	}

	return &Router{
		g:       Graph{coords: coords, adj: adj},
		streets: streets,
		idx:     idx,
		bounds:  bounds,
	}, addrs, nil
}

func computeBounds(coords map[int64]Coord) (CoordWindow, bool) {
	if len(coords) == 0 {
		return CoordWindow{}, false
	}
	minLat := math.MaxFloat64
	maxLat := -math.MaxFloat64
	minLon := math.MaxFloat64
	maxLon := -math.MaxFloat64
	for _, c := range coords {
		if c.Lat < minLat {
			minLat = c.Lat
		}
		if c.Lat > maxLat {
			maxLat = c.Lat
		}
		if c.Lon < minLon {
			minLon = c.Lon
		}
		if c.Lon > maxLon {
			maxLon = c.Lon
		}
	}
	w := CoordWindow{MinLat: minLat, MaxLat: maxLat, MinLon: minLon, MaxLon: maxLon}
	return w, w.Valid()
}

// NewRouterFromGraph constructs a `Router` from an existing graph.
// This is useful for environments where parsing PBFs is not practical,
// such as WebAssembly. Provide `coords` and `adj` maps and this will
// build the spatial index and bounds accordingly.
func NewRouterFromGraph(coords map[int64]Coord, adj map[int64][]Edge) *Router {
	idx := buildSpatialIndex(coords)
	var bounds *CoordWindow
	if b, ok := computeBounds(coords); ok {
		bounds = &b
	}
	return &Router{
		g:       Graph{coords: coords, adj: adj},
		streets: make(map[string]streetEntry),
		idx:     idx,
		bounds:  bounds,
	}
}

// ExportGraph returns the underlying graph maps (coords and adjacency)
// for serialization or transport (e.g., to produce a compact JSON for
// WebAssembly clients). The returned maps are references into the
// router's internal graph; callers should not modify them.
func (r *Router) ExportGraph() (map[int64]Coord, map[int64][]Edge) {
	return r.g.coords, r.g.adj
}

func (r *Router) Bounds() (CoordWindow, bool) {
	if r.bounds == nil {
		return CoordWindow{}, false
	}
	return *r.bounds, true
}

func (r *Router) NodeCount() int { return len(r.g.coords) }

func (r *Router) EdgeCount() int {
	n := 0
	for _, es := range r.g.adj {
		n += len(es)
	}
	return n
}

func (r *Router) Coord(id int64) (Coord, bool) {
	c, ok := r.g.coords[id]
	return c, ok
}

func (r *Router) CoordsForPath(path []int64) []Coord {
	coords := make([]Coord, 0, len(path))
	for _, id := range path {
		if c, ok := r.g.coords[id]; ok {
			coords = append(coords, c)
		}
	}
	return coords
}

func (r *Router) StreetNode(street string) (int64, bool) {
	key := normalize(street)
	if key == "" {
		return 0, false
	}
	if se, ok := r.streets[key]; ok {
		for _, id := range se.NodeIDs {
			if _, ok := r.g.coords[id]; ok {
				return id, true
			}
		}
	}
	ms := r.SearchStreets(street, 1)
	if len(ms) == 1 {
		return ms[0].NodeID, true
	}
	return 0, false
}

type StreetMatch struct {
	Name   string `json:"name"`
	NodeID int64  `json:"node_id"`
	Coord  Coord  `json:"coord"`
}

func (r *Router) SearchStreets(q string, limit int) []StreetMatch {
	nq := normalize(q)
	if nq == "" {
		return nil
	}

	type scored struct {
		key   string
		e     streetEntry
		score int
	}
	sc := make([]scored, 0, 32)
	for key, e := range r.streets {
		s := streetScore(key, nq)
		if s == 0 {
			continue
		}
		sc = append(sc, scored{key: key, e: e, score: s})
	}
	slices.SortFunc(sc, func(a, b scored) int {
		if a.score != b.score {
			return cmp.Compare(b.score, a.score) // descending by score
		}
		return cmp.Compare(a.e.Display, b.e.Display)
	})
	if limit > 0 && limit < len(sc) {
		sc = sc[:limit]
	}

	out := make([]StreetMatch, 0, len(sc))
	for _, m := range sc {
		for _, id := range m.e.NodeIDs {
			if c, ok := r.g.coords[id]; ok {
				out = append(out, StreetMatch{Name: m.e.Display, NodeID: id, Coord: c})
				break
			}
		}
	}
	return out
}

func streetScore(nameKey, queryKey string) int {
	if nameKey == queryKey {
		return 3
	}
	if strings.HasPrefix(nameKey, queryKey) {
		return 2
	}
	if strings.Contains(nameKey, queryKey) {
		return 1
	}
	return 0
}

// ---- Address support ----

type AddressEntry struct {
	ID    int64
	Coord Coord
	Tags  Tags
}

type AddressQuery struct {
	Street      string
	Housenumber string
	Postcode    string
	City        string
	Raw         string
}

func ParseAddressGuess(raw string) AddressQuery {
	q := AddressQuery{Raw: raw}
	fields := strings.Fields(raw)
	for _, f := range fields {
		if len(f) == 5 && allDigits(f) {
			q.Postcode = f
			continue
		}
		if hasDigit(f) && q.Housenumber == "" {
			q.Housenumber = f
			continue
		}
	}

	streetParts := []string{}
	cityParts := []string{}
	seenPostcode := false
	for _, f := range fields {
		switch {
		case len(f) == 5 && allDigits(f):
			seenPostcode = true
		case !seenPostcode:
			streetParts = append(streetParts, f)
		default:
			cityParts = append(cityParts, f)
		}
	}

	cleanedStreet := []string{}
	for _, f := range streetParts {
		if q.Housenumber != "" && f == q.Housenumber {
			continue
		}
		cleanedStreet = append(cleanedStreet, f)
	}

	if q.City == "" && len(cityParts) == 0 && len(cleanedStreet) > 1 {
		q.City = cleanedStreet[len(cleanedStreet)-1]
		cleanedStreet = cleanedStreet[:len(cleanedStreet)-1]
	}

	q.Street = strings.TrimSpace(strings.Join(cleanedStreet, " "))
	q.City = strings.TrimSpace(strings.Join(cityParts, " "))
	return q
}

func FindBestAddress(entries []AddressEntry, q AddressQuery) (AddressEntry, bool) {
	var best AddressEntry
	bestScore := -1
	for _, e := range entries {
		score := matchScore(e, q)
		if score > bestScore {
			bestScore = score
			best = e
		}
	}
	if bestScore <= 0 {
		return AddressEntry{}, false
	}
	return best, true
}

func matchScore(e AddressEntry, q AddressQuery) int {
	score := 0
	street := e.Tags["addr:street"]
	if q.Street != "" && equalNorm(street, q.Street) {
		score += 3
	}
	if q.Housenumber != "" && equalNorm(e.Tags["addr:housenumber"], q.Housenumber) {
		score += 4
	}
	if q.Postcode != "" && equalNorm(e.Tags["addr:postcode"], q.Postcode) {
		score += 2
	}
	if q.City != "" {
		if equalNorm(e.Tags["addr:city"], q.City) || equalNorm(e.Tags["addr:place"], q.City) {
			score += 1
		}
	}
	// If street/address matching didn't produce a high score, also try matching
	// common POI/company tags so users can search by firm/shop names.
	if q.Street != "" {
		// exact name match (high relevance)
		if equalNorm(e.Tags["name"], q.Street) {
			score += 5
		} else if containsNorm(e.Tags["name"], q.Street) {
			score += 2
		}
		// other common business tags
		if equalNorm(e.Tags["brand"], q.Street) || equalNorm(e.Tags["operator"], q.Street) {
			score += 3
		} else if containsNorm(e.Tags["brand"], q.Street) || containsNorm(e.Tags["operator"], q.Street) {
			score += 1
		}
		if equalNorm(e.Tags["shop"], q.Street) || equalNorm(e.Tags["office"], q.Street) || equalNorm(e.Tags["amenity"], q.Street) {
			score += 2
		} else if containsNorm(e.Tags["shop"], q.Street) || containsNorm(e.Tags["office"], q.Street) || containsNorm(e.Tags["amenity"], q.Street) {
			score += 1
		}
		// fallback: partial street match as before
		if score == 0 && containsNorm(street, q.Street) {
			score += 1
		}
	}
	return score
}

func equalNorm(a, b string) bool { return normalize(a) == normalize(b) }

func containsNorm(haystack, needle string) bool {
	h := normalize(haystack)
	n := normalize(needle)
	return h != "" && n != "" && strings.Contains(h, n)
}

func SearchAddresses(entries []AddressEntry, q AddressQuery, limit int) []AddressEntry {
	type scored struct {
		e AddressEntry
		s int
	}
	sc := make([]scored, 0, len(entries))
	for _, e := range entries {
		s := matchScore(e, q)
		if s <= 0 {
			continue
		}
		sc = append(sc, scored{e: e, s: s})
	}
	slices.SortFunc(sc, func(a, b scored) int {
		if a.s != b.s {
			return cmp.Compare(b.s, a.s) // descending by score
		}
		return cmp.Compare(a.e.ID, b.e.ID)
	})
	if limit <= 0 || limit > len(sc) {
		limit = len(sc)
	}
	res := make([]AddressEntry, 0, limit)
	for i := 0; i < limit; i++ {
		res = append(res, sc[i].e)
	}
	return res
}

// ---- Routing ----

type RouteResult struct {
	Path       []int64     `json:"-"`
	PathCoords []Coord     `json:"path"`
	DistanceM  float64     `json:"distance_m"`
	DurationS  float64     `json:"duration_s"`
	Cost       float64     `json:"cost"`
	Objective  Objective   `json:"objective"`
	Engine     RouteEngine `json:"engine"`
}

// Maneuver describes a single navigation instruction (turn, continue, arrive).
type Maneuver struct {
	Type        string  `json:"type"`        // depart, continue, turn-left, turn-right, uturn, arrive
	Instruction string  `json:"instruction"` // human readable text
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
	Node        int64   `json:"node"`
	DistanceM   float64 `json:"distance_m"` // distance until next maneuver / to destination
	DurationS   float64 `json:"duration_s"`
}

func (r *Router) Route(from, to int64) ([]int64, float64, error) {
	// Default to duration-based routing (prefer faster roads like motorways)
	res, err := r.RouteWithOptions(context.Background(), from, to, RouteOptions{Objective: ObjectiveDuration})
	if err != nil {
		return nil, 0, err
	}
	return res.Path, res.DistanceM, nil
}

func (r *Router) RouteCostWithOptions(ctx context.Context, from, to int64, opt RouteOptions) (float64, error) {
	opt = opt.withDefaults()
	if opt.Engine == EngineDijkstraNode {
		_, cost, err := r.dijkstraNode(ctx, from, to, opt)
		return cost, err
	}
	if opt.Engine == EngineCH {
		if r.ch == nil {
			return 0, errors.New("ch: not built")
		}
		_, cost, err := r.chQuery(ctx, from, to, opt)
		return cost, err
	}
	_, cost, _, err := r.astar(ctx, from, to, opt, false)
	return cost, err
}

func (r *Router) RouteWithOptions(ctx context.Context, from, to int64, opt RouteOptions) (RouteResult, error) {
	opt = opt.withDefaults()
	if opt.Engine == EngineDijkstraNode {
		path, cost, err := r.dijkstraNode(ctx, from, to, opt)
		if err != nil {
			return RouteResult{}, err
		}
		coords := r.CoordsForPath(path)
		distM, durS := r.computeMetrics(path, opt)
		return RouteResult{
			Path:       path,
			PathCoords: coords,
			DistanceM:  distM,
			DurationS:  durS,
			Cost:       cost,
			Objective:  opt.Objective,
			Engine:     opt.Engine,
		}, nil
	}
	if opt.Engine == EngineCH {
		if r.ch == nil {
			return RouteResult{}, errors.New("ch: not built")
		}
		path, cost, err := r.chQuery(ctx, from, to, opt)
		if err != nil {
			return RouteResult{}, err
		}
		coords := r.CoordsForPath(path)
		distM, durS := r.computeMetrics(path, opt)
		return RouteResult{
			Path:       path,
			PathCoords: coords,
			DistanceM:  distM,
			DurationS:  durS,
			Cost:       cost,
			Objective:  opt.Objective,
			Engine:     opt.Engine,
		}, nil
	}

	goalState, cost, came, err := r.astar(ctx, from, to, opt, true)
	if err != nil {
		return RouteResult{}, err
	}
	path := reconstructPathTurnState(goalState, came)
	coords := r.CoordsForPath(path)
	distM, durS := r.computeMetrics(path, opt)

	return RouteResult{
		Path:       path,
		PathCoords: coords,
		DistanceM:  distM,
		DurationS:  durS,
		Cost:       cost,
		Objective:  opt.Objective,
		Engine:     opt.Engine,
		// maneuvers are not part of internal Path (keeps compatibility)
		// but callers who JSON-encode RouteResult will see `maneuvers`.
		// Note: field added dynamically via JSON marshalling helper below if needed.
	}, nil
}

// helper: find street display name for a node by scanning street samples.
func (r *Router) streetNameForNode(node int64) string {
	for _, se := range r.streets {
		for _, id := range se.NodeIDs {
			if id == node {
				return se.Display
			}
		}
	}
	return ""
}

// ManeuversForPath produces a slice of Maneuver instructions for the given path.
func (r *Router) ManeuversForPath(path []int64, opt RouteOptions) []Maneuver {
	if len(path) == 0 {
		return nil
	}
	coords := r.CoordsForPath(path)
	n := len(path)
	// trivial single-node route
	if n == 1 {
		c := coords[0]
		return []Maneuver{{Type: "arrive", Instruction: "Arrive at destination", Lat: c.Lat, Lon: c.Lon, Node: path[0], DistanceM: 0, DurationS: 0}}
	}

	// precompute bearings and per-segment distances/durations
	bears := make([]float64, n-1)
	segDist := make([]float64, n-1)
	segDur := make([]float64, n-1)
	for i := 0; i < n-1; i++ {
		a := coords[i]
		b := coords[i+1]
		bears[i] = bearingDeg(a.Lat, a.Lon, b.Lat, b.Lon)
		segDist[i] = haversineMeters(a.Lat, a.Lon, b.Lat, b.Lon)
		if e, ok := r.edgeBetween(path[i], path[i+1]); ok && e.SpeedKph > 0 {
			segDur[i] = segDist[i] / (e.SpeedKph * 1000.0 / 3600.0)
		} else {
			segDur[i] = segDist[i] / (50.0 * 1000.0 / 3600.0)
		}
	}

	// build maneuvers: depart at first node
	maneuvers := []Maneuver{}
	depart := Maneuver{Type: "depart", Instruction: "Depart", Lat: coords[0].Lat, Lon: coords[0].Lon, Node: path[0]}
	// accumulate until next maneuver
	curIdx := 0
	for i := 1; i < n-1; i++ {
		// detect street name change
		curName := r.streetNameForNode(path[i])
		nextName := r.streetNameForNode(path[i+1])
		nameChange := curName != "" && nextName != "" && curName != nextName
		// detect angular change
		prevB := bears[i-1]
		nextB := bears[i]
		d := angleDiffDeg(nextB, prevB)
		angChange := math.Abs(d) >= 45

		if nameChange || angChange {
			// create a maneuver at node i
			// summarize distance/duration from curIdx to i
			sumDist := 0.0
			sumDur := 0.0
			for k := curIdx; k < i; k++ {
				sumDist += segDist[k]
				sumDur += segDur[k]
			}
			// decide type and instruction
			mtype := "continue"
			instr := "Continue"
			if angChange {
				if math.Abs(d) > 150 {
					mtype = "uturn"
					instr = "Make a U-turn"
				} else if d > 0 {
					mtype = "turn-right"
					instr = "Turn right"
				} else {
					mtype = "turn-left"
					instr = "Turn left"
				}
			} else if nameChange {
				mtype = "turn"
				instr = "Turn"
			}
			// append street name if available
			if nextName != "" {
				instr = fmt.Sprintf("%s onto %s", instr, nextName)
			}
			m := Maneuver{Type: mtype, Instruction: instr, Lat: coords[i].Lat, Lon: coords[i].Lon, Node: path[i], DistanceM: sumDist, DurationS: sumDur}
			if len(maneuvers) == 0 {
				// first maneuver after depart: set depart accumulators
				depart.DistanceM = sumDist
			}
			maneuvers = append(maneuvers, m)
			curIdx = i
		}
	}
	// final segment to arrive
	sumDist := 0.0
	sumDur := 0.0
	for k := curIdx; k < n-1; k++ {
		sumDist += segDist[k]
		sumDur += segDur[k]
	}
	arrive := Maneuver{Type: "arrive", Instruction: "Arrive at destination", Lat: coords[n-1].Lat, Lon: coords[n-1].Lon, Node: path[n-1], DistanceM: 0, DurationS: 0}
	// set depart fields
	if depart.DistanceM == 0 && len(maneuvers) > 0 {
		depart.DistanceM = 0
	}
	// prepend depart
	out := []Maneuver{depart}
	out = append(out, maneuvers...)
	// add final arrival; set last maneuver's Distance to distance until arrival
	if len(out) > 0 {
		// if there are maneuvers, set last maneuver's Distance to sumDist
		if len(maneuvers) > 0 {
			last := &out[len(out)-1]
			last.DistanceM = sumDist
			last.DurationS = sumDur
		} else {
			// no intermediate maneuvers: set depart's distance to total
			out[0].DistanceM = sumDist
			out[0].DurationS = sumDur
		}
	}
	out = append(out, arrive)
	return out
}

// ---- Minimal Contraction Hierarchies ----

type chData struct {
	rank map[int64]int32  // smaller rank = contracted earlier
	up   map[int64][]Edge // upward adjacency (to higher rank)
}

// BuildCH constructs a minimal CH upward graph using a simple rank heuristic.
// This placeholder can be extended to add full shortcut generation.
func (r *Router) BuildCH() {
	ndeg := make(map[int64]int32, len(r.g.adj))
	for id, es := range r.g.adj {
		ndeg[id] = int32(len(es))
	}
	// rank by degree (low degree first)
	type kv struct {
		id int64
		d  int32
	}
	arr := make([]kv, 0, len(ndeg))
	for id, d := range ndeg {
		arr = append(arr, kv{id, d})
	}
	slices.SortFunc(arr, func(a, b kv) int {
		if a.d != b.d {
			return cmp.Compare(a.d, b.d) // ascending by degree
		}
		return cmp.Compare(a.id, b.id)
	})
	rank := make(map[int64]int32, len(arr))
	for i, v := range arr {
		rank[v.id] = int32(i)
	}

	up := make(map[int64][]Edge, len(r.g.adj))
	for u, es := range r.g.adj {
		ru := rank[u]
		for _, e := range es {
			rv := rank[e.To]
			if rv > ru { // upward edge
				up[u] = append(up[u], e)
			}
		}
	}
	r.ch = &chData{rank: rank, up: up}
}

// chQuery performs bidirectional Dijkstra on upward edges.
func (r *Router) chQuery(ctx context.Context, from, to int64, opt RouteOptions) ([]int64, float64, error) {
	if from == to {
		return []int64{from}, 0, nil
	}
	if r.ch == nil {
		return nil, 0, errors.New("ch: not built")
	}

	// forward
	fdist := map[int64]float64{from: 0}
	fprev := map[int64]int64{}
	fpq := &chPQ{}
	heap.Push(fpq, &dijkstraItem{id: from, dist: 0})
	// backward (on upward graph of reversed edges approximated by scanning all nodes)
	bdist := map[int64]float64{to: 0}
	bprev := map[int64]int64{}
	bpq := &chPQ{}
	heap.Push(bpq, &dijkstraItem{id: to, dist: 0})

	best := math.MaxFloat64
	meet := int64(0)

	step := func(pq *chPQ, dist map[int64]float64, prev map[int64]int64, forward bool) {
		if pq.Len() == 0 {
			return
		}
		it := heap.Pop(pq).(*dijkstraItem)
		u := it.id
		if it.dist > dist[u] {
			return
		}
		// scan upward edges
		for _, e := range r.ch.up[u] {
			v := e.To
			alt := dist[u] + r.edgeCost(e, opt)
			if d, ok := dist[v]; !ok || alt < d {
				dist[v] = alt
				prev[v] = u
				heap.Push(pq, &dijkstraItem{id: v, dist: alt})
			}
		}
		// update best via other frontier
		var other map[int64]float64
		if forward {
			other = bdist
		} else {
			other = fdist
		}
		if d2, ok := other[u]; ok {
			if it.dist+d2 < best {
				best = it.dist + d2
				meet = u
			}
		}
	}

	// alternate expansions
	for fpq.Len() > 0 || bpq.Len() > 0 {
		if fpq.Len() > 0 {
			step(fpq, fdist, fprev, true)
		}
		if bpq.Len() > 0 {
			step(bpq, bdist, bprev, false)
		}
		if best < math.MaxFloat64 && fpq.Len() == 0 && bpq.Len() == 0 {
			break
		}
		if ctx != nil {
			select {
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			default:
			}
		}
	}
	if meet == 0 || best == math.MaxFloat64 {
		return nil, 0, errors.New("ch: no path")
	}

	// reconstruct path: forward from 'from' to meet, backward from 'to' to meet
	fpath := []int64{}
	for cur := meet; cur != 0; {
		fpath = append(fpath, cur)
		if cur == from {
			break
		}
		p, ok := fprev[cur]
		if !ok {
			break
		}
		cur = p
	}
	// reverse forward part
	slices.Reverse(fpath)
	bpath := []int64{}
	for cur := meet; cur != 0; {
		if cur == to {
			break
		}
		p, ok := bprev[cur]
		if !ok {
			break
		}
		bpath = append(bpath, p)
		cur = p
	}
	path := append(fpath, bpath...)
	return path, best, nil
}

// chPQ: min-heap for CH queries (reuses dijkstraItem).
type chPQ []*dijkstraItem

func (h chPQ) Len() int           { return len(h) }
func (h chPQ) Less(i, j int) bool { return h[i].dist < h[j].dist }
func (h chPQ) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *chPQ) Push(x any)        { *h = append(*h, x.(*dijkstraItem)) }
func (h *chPQ) Pop() any          { old := *h; n := len(old); it := old[n-1]; *h = old[:n-1]; return it }

// dijkstraNode is a simple node-based Dijkstra implementation that computes
// shortest path using per-edge costs (r.edgeCost) and does not account for
// turn/state penalties. It is useful as an alternative algorithm and for
// testing differences to the turn-aware A*/Dijkstra implementation.
func (r *Router) dijkstraNode(ctx context.Context, from, to int64, opt RouteOptions) ([]int64, float64, error) {
	if from == to {
		return []int64{from}, 0, nil
	}
	if _, ok := r.g.coords[from]; !ok {
		return nil, 0, errors.New("start node missing in graph")
	}
	if _, ok := r.g.coords[to]; !ok {
		return nil, 0, errors.New("target node missing in graph")
	}

	// use a small slice-backed priority queue for nodes
	pq := make([]*dijkstraItem, 0)
	push := func(it *dijkstraItem) {
		heap.Push(&pqWrapper{&pq}, it)
	}
	pop := func() *dijkstraItem {
		if len(pq) == 0 {
			return nil
		}
		it := heap.Pop(&pqWrapper{&pq}).(*dijkstraItem)
		return it
	}

	dist := make(map[int64]float64, len(r.g.coords))
	prev := make(map[int64]int64, len(r.g.coords))
	for id := range r.g.coords {
		dist[id] = math.MaxFloat64
	}
	dist[from] = 0
	push(&dijkstraItem{id: from, dist: 0})

	for len(pq) > 0 {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			default:
			}
		}
		it := pop()
		if it == nil {
			break
		}
		u := it.id
		if it.dist != dist[u] {
			continue // stale
		}
		if u == to {
			break
		}
		for _, e := range r.g.adj[u] {
			if !edgeAllowed(e, opt) {
				continue
			}
			v := e.To
			alt := dist[u] + r.edgeCost(e, opt)
			if alt < dist[v] {
				dist[v] = alt
				prev[v] = u
				push(&dijkstraItem{id: v, dist: alt})
			}
		}
	}

	if dist[to] == math.MaxFloat64 {
		return nil, 0, errors.New("no path found")
	}
	// reconstruct path
	path := make([]int64, 0)
	for cur := to; ; {
		path = append(path, cur)
		if cur == from {
			break
		}
		p, ok := prev[cur]
		if !ok {
			return nil, 0, errors.New("path reconstruction failed")
		}
		cur = p
	}
	// reverse
	slices.Reverse(path)
	return path, dist[to], nil
}

// dijkstraItem is a small heap entry used by the node-Dijkstra implementation.
type dijkstraItem struct {
	id   int64
	dist float64
}

// pqWrapper adapts a []*dijkstraItem slice to heap.Interface for dijkstraNode.
type pqWrapper struct{ s *[]*dijkstraItem }

func (w pqWrapper) Len() int           { return len(*w.s) }
func (w pqWrapper) Less(i, j int) bool { return (*w.s)[i].dist < (*w.s)[j].dist }
func (w pqWrapper) Swap(i, j int)      { (*w.s)[i], (*w.s)[j] = (*w.s)[j], (*w.s)[i] }
func (w *pqWrapper) Push(x any)        { *w.s = append(*w.s, x.(*dijkstraItem)) }
func (w *pqWrapper) Pop() any {
	old := *w.s
	n := len(old)
	it := old[n-1]
	*w.s = old[:n-1]
	return it
}

func (r *Router) computeMetrics(path []int64, opt RouteOptions) (distM, durS float64) {
	if len(path) < 2 {
		return 0, 0
	}
	for i := 0; i < len(path)-1; i++ {
		e, ok := r.edgeBetween(path[i], path[i+1])
		if !ok {
			continue
		}
		distM += e.DistM
		durS += edgeTimeSeconds(e, opt)
	}
	return distM, durS
}

func (r *Router) edgeBetween(from, to int64) (Edge, bool) {
	es := r.g.adj[from]
	best := Edge{}
	found := false
	bestD := math.MaxFloat64
	for _, e := range es {
		if e.To != to {
			continue
		}
		if e.DistM < bestD {
			bestD = e.DistM
			best = e
			found = true
		}
	}
	return best, found
}

// ---- A* with turn state (prev,cur) ----

type turnState struct {
	prev int64
	cur  int64
}

type pqItem struct {
	s   turnState
	f   float64
	g   float64
	idx int
}

type priorityQueue []*pqItem

func (pq priorityQueue) Len() int           { return len(pq) }
func (pq priorityQueue) Less(i, j int) bool { return pq[i].f < pq[j].f }
func (pq priorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].idx = i
	pq[j].idx = j
}
func (pq *priorityQueue) Push(x any) {
	it := x.(*pqItem)
	it.idx = len(*pq)
	*pq = append(*pq, it)
}
func (pq *priorityQueue) Pop() any {
	old := *pq
	n := len(old)
	it := old[n-1]
	*pq = old[:n-1]
	return it
}

func (r *Router) astar(ctx context.Context, from, to int64, opt RouteOptions, wantPath bool) (goal turnState, goalCost float64, came map[turnState]turnState, err error) {
	if from == to {
		if wantPath {
			came = map[turnState]turnState{}
		}
		return turnState{prev: 0, cur: from}, 0, came, nil
	}
	if _, ok := r.g.coords[from]; !ok {
		return turnState{}, 0, nil, errors.New("start node missing in graph")
	}
	if _, ok := r.g.coords[to]; !ok {
		return turnState{}, 0, nil, errors.New("target node missing in graph")
	}
	if len(r.g.adj[from]) == 0 {
		return turnState{}, 0, nil, errors.New("start node has no edges")
	}
	if len(r.g.adj[to]) == 0 {
		return turnState{}, 0, nil, errors.New("target node has no edges")
	}

	if wantPath {
		came = make(map[turnState]turnState, 1<<16)
	}

	start := turnState{prev: 0, cur: from}
	gScore := map[turnState]float64{start: 0}

	pq := priorityQueue{}
	heap.Push(&pq, &pqItem{s: start, g: 0, f: r.heuristic(from, to, opt)})

	pops := 0
	for pq.Len() > 0 {
		pops++
		if ctx != nil && pops%1024 == 0 {
			select {
			case <-ctx.Done():
				return turnState{}, 0, nil, ctx.Err()
			default:
			}
		}

		curIt := heap.Pop(&pq).(*pqItem)
		curS := curIt.s

		if best, ok := gScore[curS]; ok && curIt.g > best {
			continue // stale
		}

		if curS.cur == to {
			return curS, curIt.g, came, nil
		}

		for _, e := range r.g.adj[curS.cur] {
			if !edgeAllowed(e, opt) {
				continue
			}
			next := e.To
			base := r.edgeCost(e, opt)
			pen := r.transitionPenalty(curS.prev, curS.cur, next, opt)
			tent := curIt.g + base + pen

			nextS := turnState{prev: curS.cur, cur: next}
			if old, ok := gScore[nextS]; ok && tent >= old {
				continue
			}

			gScore[nextS] = tent
			if wantPath {
				came[nextS] = curS
			}

			h := r.heuristic(next, to, opt)
			heap.Push(&pq, &pqItem{s: nextS, g: tent, f: tent + h})
		}
	}

	return turnState{}, 0, nil, errors.New("no path found")
}

func reconstructPathTurnState(goal turnState, came map[turnState]turnState) []int64 {
	path := make([]int64, 0, 256)
	s := goal
	for {
		path = append(path, s.cur)
		prev, ok := came[s]
		if !ok {
			break
		}
		s = prev
	}
	slices.Reverse(path)
	return path
}

func (r *Router) heuristic(from, to int64, opt RouteOptions) float64 {
	if opt.Engine == EngineDijkstra {
		return 0
	}
	a := r.g.coords[from]
	b := r.g.coords[to]
	d := haversineMeters(a.Lat, a.Lon, b.Lat, b.Lon)

	switch opt.Objective {
	case ObjectiveDuration:
		return d * 3.6 / opt.Weights.MaxSpeedKph
	case ObjectiveEconomy:
		return d / 1000
	default:
		return d
	}
}

func (r *Router) edgeCost(e Edge, opt RouteOptions) float64 {
	if opt.EmergencyMode {
		// BOS/emergency: minimize time, use higher assumed speeds
		v := e.SpeedKph
		if v <= 0 {
			v = 60
		}
		// Emergency vehicles can exceed normal speed limits by ~30%
		v *= 1.3
		if v < 10 {
			v = 10
		}
		return e.DistM * 3.6 / v
	}
	switch opt.Objective {
	case ObjectiveDuration:
		return edgeTimeSeconds(e, opt)
	case ObjectiveEconomy:
		km := e.DistM / 1000
		v := effectiveSpeedKph(e, opt)
		f := 1.0 + 0.7*math.Pow(v/100.0, 2)
		return km * f
	default:
		return e.DistM
	}
}

func (r *Router) transitionPenalty(prev, cur, next int64, opt RouteOptions) float64 {
	if prev == 0 {
		return 0
	}
	pen := 0.0
	if opt.Weights.Crossing > 0 && len(r.g.adj[cur]) > 2 {
		pen += opt.Weights.Crossing
	}
	// Traffic light penalty at intersections with more than 2 edges
	if opt.Weights.TrafficLightPenalty > 0 && len(r.g.adj[cur]) > 2 {
		pen += opt.Weights.TrafficLightPenalty
	}
	tt := r.turnType(prev, cur, next)
	switch tt {
	case turnLeft:
		if opt.Weights.NoLeftTurn {
			pen += noLeftTurnPenalty
		} else {
			pen += opt.Weights.LeftTurn
		}
	case turnRight:
		pen += opt.Weights.RightTurn
	case turnUTurn:
		pen += opt.Weights.UTurn
	}
	return pen
}

type turnKind uint8

const (
	turnStraight turnKind = iota
	turnLeft
	turnRight
	turnUTurn
)

// noLeftTurnPenalty is a very high cost added when NoLeftTurn is enabled,
// effectively forbidding left turns.
const noLeftTurnPenalty = 1e6

func (r *Router) turnType(prev, cur, next int64) turnKind {
	p, ok1 := r.g.coords[prev]
	c, ok2 := r.g.coords[cur]
	n, ok3 := r.g.coords[next]
	if !ok1 || !ok2 || !ok3 {
		return turnStraight
	}
	b1 := bearingDeg(p.Lat, p.Lon, c.Lat, c.Lon)
	b2 := bearingDeg(c.Lat, c.Lon, n.Lat, n.Lon)
	diff := angleDiffDeg(b2, b1)
	ad := math.Abs(diff)
	if ad > 160 {
		return turnUTurn
	}
	if diff > 30 {
		return turnRight
	}
	if diff < -30 {
		return turnLeft
	}
	return turnStraight
}

func angleDiffDeg(a, b float64) float64 {
	return math.Mod(a-b+540, 360) - 180
}

func bearingDeg(lat1, lon1, lat2, lon2 float64) float64 {
	φ1 := deg2rad(lat1)
	φ2 := deg2rad(lat2)
	Δλ := deg2rad(lon2 - lon1)
	y := math.Sin(Δλ) * math.Cos(φ2)
	x := math.Cos(φ1)*math.Sin(φ2) - math.Sin(φ1)*math.Cos(φ2)*math.Cos(Δλ)
	θ := math.Atan2(y, x)
	return math.Mod(rad2deg(θ)+360, 360)
}

func rad2deg(r float64) float64 { return r * 180 / math.Pi }

func effectiveSpeedKph(e Edge, opt RouteOptions) float64 {
	v := e.SpeedKph
	if v <= 0 {
		v = 50
	}
	// Apply profile speed scale (e.g. cycling at 25% of road speed)
	if scale := profileSpeedScale(opt); scale > 0 && scale < 1 {
		v *= scale
	}
	if opt.Weights.MaxSpeedKph > 0 && v > opt.Weights.MaxSpeedKph {
		v = opt.Weights.MaxSpeedKph
	}
	if v < 3 {
		v = 3 // minimum 3 kph (walking pace) to keep heuristics well-behaved
	}
	return v
}

func edgeTimeSeconds(e Edge, opt RouteOptions) float64 {
	v := effectiveSpeedKph(e, opt)
	return e.DistM * 3.6 / v
}

func edgeAllowed(e Edge, opt RouteOptions) bool {
	if opt.Weights.VehicleHeightM > 0 && e.MaxHeightM > 0 && opt.Weights.VehicleHeightM > e.MaxHeightM {
		return false
	}
	if opt.Weights.VehicleWeightT > 0 && e.MaxWeightT > 0 && opt.Weights.VehicleWeightT > e.MaxWeightT {
		return false
	}
	// Check profile highway-type restriction.
	if allowed := profileAllowedHwySet(opt); allowed != nil && e.HwyType != "" {
		if !allowed[e.HwyType] {
			return false
		}
	}
	return true
}

// ---- Nearest node (spatial index) ----

type spatialIndex struct {
	cellSize float64
	minLat   float64
	minLon   float64
	maxX     int32
	maxY     int32
	cells    map[int64][]int64
}

func buildSpatialIndex(coords map[int64]Coord) *spatialIndex {
	if len(coords) == 0 {
		return nil
	}
	minLat := math.MaxFloat64
	minLon := math.MaxFloat64
	for _, c := range coords {
		if c.Lat < minLat {
			minLat = c.Lat
		}
		if c.Lon < minLon {
			minLon = c.Lon
		}
	}
	const cellSize = 0.01
	idx := &spatialIndex{
		cellSize: cellSize,
		minLat:   minLat,
		minLon:   minLon,
		cells:    make(map[int64][]int64, len(coords)/16),
	}

	var maxX, maxY int32
	for id, c := range coords {
		ix, iy := idx.cell(c.Lat, c.Lon)
		if ix > maxX {
			maxX = ix
		}
		if iy > maxY {
			maxY = iy
		}
		k := cellKey(ix, iy)
		idx.cells[k] = append(idx.cells[k], id)
	}
	idx.maxX = maxX
	idx.maxY = maxY
	return idx
}

func (idx *spatialIndex) cell(lat, lon float64) (int32, int32) {
	ix := int32(math.Floor((lat - idx.minLat) / idx.cellSize))
	iy := int32(math.Floor((lon - idx.minLon) / idx.cellSize))
	return ix, iy
}

func cellKey(ix, iy int32) int64 {
	return (int64(ix) << 32) | int64(uint32(iy))
}

func (r *Router) NearestNode(lat, lon float64) (int64, float64, bool) {
	if r.idx == nil {
		return r.nearestNodeBrute(lat, lon)
	}

	ix0, iy0 := r.idx.cell(lat, lon)
	if ix0 < 0 {
		ix0 = 0
	} else if ix0 > r.idx.maxX {
		ix0 = r.idx.maxX
	}
	if iy0 < 0 {
		iy0 = 0
	} else if iy0 > r.idx.maxY {
		iy0 = r.idx.maxY
	}

	bestID := int64(0)
	best := math.MaxFloat64

	scanCell := func(ix, iy int32) {
		ids := r.idx.cells[cellKey(ix, iy)]
		for _, id := range ids {
			c := r.g.coords[id]
			d := haversineMeters(lat, lon, c.Lat, c.Lon)
			if d < best {
				best = d
				bestID = id
			}
		}
	}

	const maxScanRadius = int32(256)
	for radius := int32(0); radius <= maxScanRadius; radius++ {
		if radius == 0 {
			scanCell(ix0, iy0)
		} else {
			minX, maxX := ix0-radius, ix0+radius
			minY, maxY := iy0-radius, iy0+radius
			for x := minX; x <= maxX; x++ {
				scanCell(x, minY)
				scanCell(x, maxY)
			}
			for y := minY + 1; y <= maxY-1; y++ {
				scanCell(minX, y)
				scanCell(maxX, y)
			}
		}
		if bestID != 0 {
			metersPerDeg := 111320.0 * math.Abs(math.Cos(deg2rad(lat)))
			if metersPerDeg > 0 {
				minPossible := float64(radius+1) * r.idx.cellSize * metersPerDeg
				if minPossible > best {
					break
				}
			} else if radius >= 8 {
				break
			}
		}
	}

	if bestID != 0 {
		return bestID, best, true
	}
	return r.nearestNodeBrute(lat, lon)
}

func (r *Router) nearestNodeBrute(lat, lon float64) (int64, float64, bool) {
	var bestID int64
	best := math.MaxFloat64
	for id, c := range r.g.coords {
		if len(r.g.adj[id]) == 0 {
			continue
		}
		d := haversineMeters(lat, lon, c.Lat, c.Lon)
		if d < best {
			best = d
			bestID = id
		}
	}
	if best == math.MaxFloat64 {
		return 0, 0, false
	}
	return bestID, best, true
}

// ---- Highway parsing helpers ----

type highwayWay struct {
	nodes      []int64
	hwyType    string // OSM highway tag value
	speedKph   float64
	maxHeightM float64
	maxWeightT float64
	oneway     int8 // 0=both, 1=forward, -1=reverse
}

// DefaultHighwaySpeeds can be overridden by the caller (e.g. server settings)
// to control the fallback speed used when a way has no maxspeed tag.
var DefaultHighwaySpeeds = map[string]float64{
	"motorway":      110,
	"trunk":         100,
	"primary":       80,
	"secondary":     70,
	"tertiary":      60,
	"unclassified":  50,
	"residential":   30,
	"living_street": 10,
	"service":       20,
	"track":         25,
}

// AllowedHighwayTypesMap, if non-empty, restricts which highway types are treated as driveable.
var AllowedHighwayTypesMap map[string]bool

func isDriveableHighway(hwy string, tags Tags) bool {
	h := strings.ToLower(strings.TrimSpace(hwy))
	// If caller supplied an allowed-types map, only accept listed keys (and their _link variants).
	if AllowedHighwayTypesMap != nil && len(AllowedHighwayTypesMap) > 0 {
		if AllowedHighwayTypesMap[h] {
			return true
		}
		// allow e.g. motorway_link when motorway present
		if strings.HasSuffix(h, "_link") {
			base := strings.TrimSuffix(h, "_link")
			if AllowedHighwayTypesMap[base] {
				return true
			}
		}
		return false
	}
	// accept common highway types and any *_link (ramps/entrances/exits)
	if strings.HasSuffix(h, "_link") {
		return true
	}
	switch h {
	case "motorway", "trunk", "primary", "secondary", "tertiary",
		"unclassified", "residential", "living_street", "service", "road":
		return true
	case "track":
		return true
	default:
		return false
	}
}

func isAccessDenied(tags Tags) bool {
	deny := func(v string) bool {
		v = strings.ToLower(strings.TrimSpace(v))
		return v == "no" || v == "private"
	}
	if deny(tags["access"]) {
		return true
	}
	if deny(tags["vehicle"]) || deny(tags["motor_vehicle"]) || deny(tags["hgv"]) {
		return true
	}
	return false
}

func defaultSpeedForHighway(hwy string) float64 {
	key := strings.ToLower(strings.TrimSpace(hwy))
	if v, ok := DefaultHighwaySpeeds[key]; ok {
		return v
	}
	return 50
}

func parseOneway(v string) int8 {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "yes", "true", "1":
		return 1
	case "-1", "reverse":
		return -1
	default:
		return 0
	}
}

func parseMaxSpeedKph(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	low := strings.ToLower(s)
	switch low {
	case "de:urban":
		return 50
	case "de:rural":
		return 100
	case "de:motorway":
		return 130
	}
	v, ok := firstFloat(s)
	if !ok {
		return 0
	}
	if strings.Contains(low, "mph") {
		return v * 1.60934
	}
	return v
}

func parseMeters(s string) float64 {
	v, ok := firstFloat(s)
	if !ok {
		return 0
	}
	return v
}

func parseTons(s string) float64 {
	v, ok := firstFloat(s)
	if !ok {
		return 0
	}
	return v
}

func firstFloat(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	start := -1
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || c == '.' || c == ',' {
			start = i
			break
		}
	}
	if start == -1 {
		return 0, false
	}
	end := start
	for end < len(s) {
		c := s[end]
		if (c >= '0' && c <= '9') || c == '.' || c == ',' {
			end++
			continue
		}
		break
	}
	num := strings.ReplaceAll(s[start:end], ",", ".")
	v, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// ---- misc helpers ----

func appendUniqueLimited(dst []int64, src []int64, limit int) []int64 {
	for _, v := range src {
		if limit > 0 && len(dst) >= limit {
			break
		}
		seen := false
		for _, d := range dst {
			if d == v {
				seen = true
				break
			}
		}
		if !seen {
			dst = append(dst, v)
		}
	}
	return dst
}

func sampleWayNodeIDs(ids []int64) []int64 {
	n := len(ids)
	if n == 0 {
		return nil
	}
	if n <= 3 {
		out := make([]int64, 0, n)
		for _, id := range ids {
			if len(out) == 0 || out[len(out)-1] != id {
				out = append(out, id)
			}
		}
		return out
	}
	out := make([]int64, 0, 3)
	out = append(out, ids[0])
	mid := ids[n/2]
	if mid != ids[0] && mid != ids[n-1] {
		out = append(out, mid)
	}
	last := ids[n-1]
	if last != ids[0] && last != mid {
		out = append(out, last)
	}
	return out
}

func normalize(s string) string {
	if s == "" {
		return ""
	}
	lower := strings.ToLower(s)
	replacer := strings.NewReplacer("ß", "ss", "ä", "ae", "ö", "oe", "ü", "ue")
	cleaned := replacer.Replace(lower)

	out := make([]rune, 0, len(cleaned))
	for _, r := range cleaned {
		switch r {
		case ' ', '-', ',', '.', ';', ':':
			continue
		default:
			out = append(out, r)
		}
	}
	return string(out)
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

func hasDigit(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			return true
		}
	}
	return false
}

// haversineMeters returns the great-circle distance in meters.
func haversineMeters(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371000.0
	dLat := deg2rad(lat2 - lat1)
	dLon := deg2rad(lon2 - lon1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(deg2rad(lat1))*math.Cos(deg2rad(lat2))*math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return R * c
}

func deg2rad(d float64) float64 { return d * math.Pi / 180 }

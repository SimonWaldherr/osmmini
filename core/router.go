package osmmini

import (
	"bytes"
	"cmp"
	"container/heap"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Sentinel routing errors. Callers (e.g. the HTTP API) can match these with
// errors.Is to present a translated, user-facing message instead of the raw
// internal error text.
var (
	ErrRouteEngineNotReady    = errors.New("ch: not built")
	ErrRouteEngineUnsupported = errors.New("ch routing is unavailable: shortcut graph is incomplete")
	ErrRouteNoPath            = errors.New("no path found")
	ErrRouteStartUnreachable  = errors.New("start node missing in graph")
	ErrRouteTargetUnreachable = errors.New("target node missing in graph")
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
	// EngineCH is retained for compatibility but rejected explicitly. The
	// experimental upward graph has no shortcuts and cannot answer routes
	// reliably. Use A* or Dijkstra until a complete CH implementation exists.
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
	ProfileTHW          VehicleProfile = "thw"          // Technisches Hilfswerk (technical relief, ignores some restrictions)
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

	// allowedSet is AllowedHwySet as a lookup set, precomputed once in init()
	// below rather than rebuilt from the slice on every edgeAllowed() call --
	// that call happens once per edge relaxation during a route search
	// (potentially millions of times), so reallocating a map there was a
	// measured hot-path cost. Unexported: never serialized, derived data only.
	allowedSet map[string]bool
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
		ID: ProfileTHW, Label: "THW", Icon: "🛠️",
		Objective: ObjectiveDuration, MaxSpeedKph: 130,
		// THW vehicles (technical relief) use all roads; no explicit restriction
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

func init() {
	for i := range BuiltinProfiles {
		if len(BuiltinProfiles[i].AllowedHwySet) == 0 {
			continue
		}
		set := make(map[string]bool, len(BuiltinProfiles[i].AllowedHwySet))
		for _, h := range BuiltinProfiles[i].AllowedHwySet {
			set[h] = true
		}
		BuiltinProfiles[i].allowedSet = set
	}
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
	// TerritoryCost optionally scales edge costs by which territory they
	// fall in, relative to a home territory (see TerritoryCostPolicy). It
	// is nil by default, in which case routing behaves exactly as before --
	// territory boundaries are metadata, not a routing restriction, unless
	// a caller sets this explicitly. Not settable from client JSON.
	TerritoryCost *TerritoryCostPolicy `json:"-"`
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

type Edge struct {
	To         int64
	DistM      float64
	SpeedKph   float64
	MaxHeightM float64
	MaxWeightT float64
	HwyType    string // OSM highway tag value (e.g. "residential")
}

type Graph struct {
	store  *diskGraph
	coords map[int64]Coord
	adj    map[int64][]Edge
}

// Router wraps a road graph built from OSM highways and provides
// nearest-node lookup, street search and pathfinding (A*/Dijkstra).
type Router struct {
	g                Graph
	streets          map[string]streetEntry // normalized -> display + sample nodes
	streetLabelCells map[int64][]streetLabelRef
	streetNameByNode map[int64]string
	idx              *spatialIndex
	bounds           *CoordWindow
	landmarks        *landmarkIndex

	// CH data
	ch *chData
}

type streetEntry struct {
	Display string
	NodeIDs []int64
}

// streetLabelRef is deliberately smaller than MapLabel: coordinates remain in
// the graph and are resolved only for cells intersecting the current viewport.
// A Bavaria-sized graph has many named streets, while one map frame normally
// touches only a few dozen of these references.
type streetLabelRef struct {
	streetKey string
	nodeID    int64
}

type mapLabelCandidate struct {
	label MapLabel
	dist  float64
}

// BuildOptions controls router graph extraction.
type BuildOptions struct {
	// SkipAddresses is useful for importers that build a separate disk search index.
	SkipAddresses bool
	// MaxNodes limits referenced road nodes; unrelated OSM nodes do not count.
	// With a Window, only retained coordinates count. Zero is unlimited.
	MaxNodes      int
	Context       context.Context
	Window        *CoordWindow
	WindowBufferM float64
}

func BuildRouterWithAddresses(path string) (*Router, []AddressEntry, error) {
	return BuildRouterWithAddressesOptions(path, BuildOptions{})
}

// BuildRouterWithAddressesOptions parses a PBF, builds a highway graph and collects address nodes.
// Whole-region imports read roads before their coordinates, so unrelated OSM
// nodes never enter the graph's coordinate map. A spatial Window instead limits
// coordinates/addresses in one pass, expanded by WindowBufferM, without retaining
// a reference set for all the roads outside that window.
func BuildRouterWithAddressesOptions(path string, bo BuildOptions) (*Router, []AddressEntry, error) {
	var filterWin *CoordWindow
	if bo.Window != nil && bo.Window.Valid() {
		w := *bo.Window
		if bo.WindowBufferM > 0 {
			w = w.ExpandMeters(bo.WindowBufferM)
		}
		filterWin = &w
	}

	initialNodes := 1 << 16
	if bo.MaxNodes > 0 {
		initialNodes = min(initialNodes, bo.MaxNodes)
	}
	// The same map first tracks referenced IDs and later counts outgoing
	// edges. Reusing its storage avoids keeping a separate degree map alive
	// while the coordinate and adjacency maps are built.
	var referenced map[int64]int
	if filterWin == nil {
		referenced = make(map[int64]int, initialNodes)
	}
	var coordsAll map[int64]Coord
	var addrs []AddressEntry
	if !bo.SkipAddresses {
		addrs = make([]AddressEntry, 0, 1<<16)
	}
	highways := make([]highwayWay, 0, 1024)
	streets := make(map[string]streetEntry, 1024)

	opts := Options{
		Context:             bo.Context,
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
			if filterWin == nil {
				if _, needed := referenced[id]; !needed {
					return nil
				}
			}
			c := Coord{Lat: lat, Lon: lon}
			if filterWin != nil && !filterWin.Contains(c) {
				return nil
			}
			if _, exists := coordsAll[id]; !exists && bo.MaxNodes > 0 && len(coordsAll) >= bo.MaxNodes {
				return fmt.Errorf("graph import exceeds %d nodes; choose a smaller region or increase MaxNodes", bo.MaxNodes)
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
				switch strings.ToLower(strings.TrimSpace(w.Tags["oneway"])) {
				case "no", "false", "0":
					// Explicit two-way tags override the roundabout default.
				default:
					oneway = 1
				}
			}

			ids := w.NodeIDs
			if filterWin == nil {
				for _, id := range ids {
					if _, exists := referenced[id]; exists {
						continue
					}
					if bo.MaxNodes > 0 && len(referenced) >= bo.MaxNodes {
						return fmt.Errorf("graph import exceeds %d road nodes; choose a smaller region or increase MaxNodes", bo.MaxNodes)
					}
					referenced[id] = 0
				}
			}
			highways = append(highways, highwayWay{
				refs:       packHighwayRefs(ids),
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

	if bo.SkipAddresses {
		cb.AddressNode = nil
	}
	if filterWin != nil {
		coordsAll = make(map[int64]Coord, initialNodes)
		if err := ExtractFile(path, opts, cb); err != nil {
			return nil, nil, err
		}
		referenced = make(map[int64]int)
	} else {
		if err := ExtractFile(path, opts, Callbacks{HighwayWay: cb.HighwayWay}); err != nil {
			return nil, nil, err
		}
		coordsAll = make(map[int64]Coord, len(referenced))
		cb.HighwayWay = nil
		opts.EmitWayNodeIDs = false
		if err := ExtractFile(path, opts, cb); err != nil {
			return nil, nil, err
		}
	}
	// Count valid outgoing edges before allocating adjacency storage. Appending
	// each edge to a separately grown node slice otherwise leaves spare capacity
	// and millions of small allocations behind in a whole-region graph. A zero
	// degree still marks a valid one-way sink whose coordinate must be retained.
	clear(referenced)
	edgeCount := 0
	if err := visitHighwayEdges(bo.Context, coordsAll, highways, func(from, to int64, _ float64, _ highwayWay) {
		referenced[from]++
		if _, exists := referenced[to]; !exists {
			referenced[to] = 0
		}
		edgeCount++
	}); err != nil {
		return nil, nil, err
	}
	adj := make(map[int64][]Edge, len(referenced))
	edges := make([]Edge, edgeCount)
	edgeOffset := 0
	for id, degree := range referenced {
		if degree > 0 {
			// Limit each slice to its own span: later append through ExportGraph
			// must not be able to overwrite the next node's edges.
			adj[id] = edges[edgeOffset : edgeOffset : edgeOffset+degree]
			edgeOffset += degree
		}
	}
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
	if err := visitHighwayEdges(bo.Context, coordsAll, highways, addEdge); err != nil {
		return nil, nil, err
	}
	if _, err := deduplicateImportedEdges(bo.Context, adj); err != nil {
		return nil, nil, err
	}

	// prune coords to nodes that actually have edges
	coords := coordsAll
	if filterWin != nil {
		// Window imports temporarily keep all coordinates in their bounds;
		// compact them so non-road map capacity does not outlive the import.
		coords = make(map[int64]Coord, len(referenced))
		for id := range referenced {
			coords[id] = coordsAll[id]
		}
	} else {
		for id := range coords {
			if _, used := referenced[id]; !used {
				delete(coords, id)
			}
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
	streetLabelCells := buildStreetLabelCells(streets, coords)
	streetNameByNode := buildStreetNameIndex(streets)

	var bounds *CoordWindow
	if b, ok := computeBounds(coords); ok {
		bounds = &b
	}

	return &Router{
		g:                Graph{coords: coords, adj: adj},
		streets:          streets,
		streetLabelCells: streetLabelCells,
		streetNameByNode: streetNameByNode,
		idx:              idx,
		bounds:           bounds,
	}, addrs, nil
}

// visitHighwayEdges shares the filtering and ordering between degree counting
// and adjacency filling. Missing or degenerate segments must be skipped in both
// passes, including when a Window clips a way in the middle.
func visitHighwayEdges(ctx context.Context, coords map[int64]Coord, highways []highwayWay, visit func(int64, int64, float64, highwayWay)) error {
	for wi, hw := range highways {
		if wi%4096 == 0 && ctx != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		refs := hw.refs
		aID, n := binary.Varint(refs)
		if n <= 0 {
			return errors.New("invalid imported highway references")
		}
		refs = refs[n:]
		a, okA := coords[aID]
		for segment := 0; len(refs) > 0; segment++ {
			if segment%4096 == 0 && ctx != nil {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			delta, n := binary.Varint(refs)
			if n <= 0 {
				return errors.New("invalid imported highway references")
			}
			refs = refs[n:]
			bID := aID + delta
			b, okB := coords[bID]
			if okA && okB {
				d := haversineMeters(a.Lat, a.Lon, b.Lat, b.Lon)
				if d > 0 && !math.IsNaN(d) && !math.IsInf(d, 0) {
					switch hw.oneway {
					case 1:
						visit(aID, bID, d, hw)
					case -1:
						visit(bID, aID, d, hw)
					default:
						visit(aID, bID, d, hw)
						visit(bID, aID, d, hw)
					}
				}
			}
			aID, a, okA = bID, b, okB
		}
	}
	return nil
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
	if r.g.store != nil {
		return r.g.store.export()
	}
	return r.g.coords, r.g.adj
}

func (r *Router) Bounds() (CoordWindow, bool) {
	if r.bounds == nil {
		return CoordWindow{}, false
	}
	return *r.bounds, true
}

func (r *Router) NodeCount() int {
	if r.g.store != nil {
		return r.g.store.nodes
	}
	return len(r.g.coords)
}

func (r *Router) EdgeCount() int {
	if r.g.store != nil {
		return r.g.store.edgeCount
	}
	n := 0
	for _, es := range r.g.adj {
		n += len(es)
	}
	return n
}

func (r *Router) Coord(id int64) (Coord, bool) {
	c, ok := r.g.coord(id)
	return c, ok
}

func (r *Router) CoordsForPath(path []int64) []Coord {
	coords := make([]Coord, 0, len(path))
	for _, id := range path {
		if c, ok := r.g.coord(id); ok {
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
			if _, ok := r.g.coord(id); ok {
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

// MapLabel is a small, display-ready label anchor extracted from the local
// routing graph. It deliberately contains no graph internals, so callers can
// render named streets without re-reading the source PBF.
type MapLabel struct {
	Name  string `json:"name"`
	Coord Coord  `json:"coord"`
}

// StreetLabels returns at most one named-street anchor per street in window.
// It is intended for lightweight map labelling rather than search: callers
// should keep the limit low and query only the visible map area.
func (r *Router) StreetLabels(window CoordWindow, limit int) []MapLabel {
	if !window.Valid() || limit <= 0 || len(r.streets) == 0 {
		return nil
	}
	if limit > 500 {
		limit = 500
	}
	center := window.Center()
	if len(r.streetLabelCells) == 0 {
		return r.streetLabelsScan(window, center, limit)
	}
	// The old implementation walked every named street for every map movement.
	// Querying the fixed grid makes cost proportional to the visible map area.
	// Duplicated samples of a street are reduced to the nearest visible one.
	byStreet := make(map[string]mapLabelCandidate, limit*2)
	minX, minY, maxX, maxY := labelCellRange(window)
	for x := minX; x <= maxX; x++ {
		for y := minY; y <= maxY; y++ {
			for _, ref := range r.streetLabelCells[cellKey(x, y)] {
				coord, ok := r.g.coord(ref.nodeID)
				if !ok || !window.Contains(coord) {
					continue
				}
				dLat, dLon := coord.Lat-center.Lat, coord.Lon-center.Lon
				dist := dLat*dLat + dLon*dLon
				if previous, exists := byStreet[ref.streetKey]; exists && previous.dist <= dist {
					continue
				}
				entry, ok := r.streets[ref.streetKey]
				if !ok || entry.Display == "" {
					continue
				}
				byStreet[ref.streetKey] = mapLabelCandidate{label: MapLabel{Name: entry.Display, Coord: coord}, dist: dist}
			}
		}
	}
	return sortStreetLabelCandidates(byStreet, window, limit)
}

func (r *Router) streetLabelsScan(window CoordWindow, center Coord, limit int) []MapLabel {
	candidates := make([]mapLabelCandidate, 0, min(len(r.streets), limit*3))
	for _, entry := range r.streets {
		if entry.Display == "" {
			continue
		}
		var best Coord
		bestDist := math.Inf(1)
		for _, id := range entry.NodeIDs {
			coord, ok := r.g.coord(id)
			if !ok || !window.Contains(coord) {
				continue
			}
			// Squared degrees are sufficient for ordering candidates inside one
			// viewport and avoid an expensive geodesic calculation per label.
			dLat, dLon := coord.Lat-center.Lat, coord.Lon-center.Lon
			d := dLat*dLat + dLon*dLon
			if d < bestDist {
				best, bestDist = coord, d
			}
		}
		if bestDist != math.Inf(1) {
			candidates = append(candidates, mapLabelCandidate{label: MapLabel{Name: entry.Display, Coord: best}, dist: bestDist})
		}
	}
	return distributeStreetLabelCandidates(candidates, window, limit)
}

func sortStreetLabelCandidates(byStreet map[string]mapLabelCandidate, window CoordWindow, limit int) []MapLabel {
	candidates := make([]mapLabelCandidate, 0, len(byStreet))
	for _, candidate := range byStreet {
		candidates = append(candidates, candidate)
	}
	return distributeStreetLabelCandidates(candidates, window, limit)
}

// Take one candidate per viewport cell before taking a second from any cell.
// A global nearest-first limit otherwise spends the entire label budget in
// the centre of the map, even when streets near its edges are visible.
func distributeStreetLabelCandidates(candidates []mapLabelCandidate, window CoordWindow, limit int) []MapLabel {
	slices.SortFunc(candidates, func(a, b mapLabelCandidate) int {
		if a.dist < b.dist {
			return -1
		}
		if a.dist > b.dist {
			return 1
		}
		if name := strings.Compare(a.label.Name, b.label.Name); name != 0 {
			return name
		}
		if a.label.Coord.Lat < b.label.Coord.Lat {
			return -1
		}
		if a.label.Coord.Lat > b.label.Coord.Lat {
			return 1
		}
		if a.label.Coord.Lon < b.label.Coord.Lon {
			return -1
		}
		if a.label.Coord.Lon > b.label.Coord.Lon {
			return 1
		}
		return 0
	})
	side := max(1, min(8, int(math.Sqrt(float64(limit)))))
	cells := make([][]MapLabel, side*side)
	for _, candidate := range candidates {
		coord := candidate.label.Coord
		x := min(side-1, max(0, int((coord.Lon-window.MinLon)/(window.MaxLon-window.MinLon)*float64(side))))
		y := min(side-1, max(0, int((coord.Lat-window.MinLat)/(window.MaxLat-window.MinLat)*float64(side))))
		cells[y*side+x] = append(cells[y*side+x], candidate.label)
	}
	out := make([]MapLabel, 0, min(limit, len(candidates)))
	for depth := 0; len(out) < limit; depth++ {
		added := false
		for _, cell := range cells {
			if depth < len(cell) {
				out = append(out, cell[depth])
				added = true
				if len(out) == limit {
					return out
				}
			}
		}
		if !added {
			break
		}
	}
	return out
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
			if c, ok := r.g.coord(id); ok {
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
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return AddressQuery{}
	}
	q := AddressQuery{Raw: raw}
	if parts := strings.SplitN(raw, ",", 2); len(parts) == 2 {
		q.Street = strings.TrimSpace(parts[0])
		q.City = strings.TrimSpace(parts[1])
	}
	fields := strings.Fields(addressSeparators.Replace(raw))
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

	if q.Street != "" || q.City != "" {
		// Comma-separated input already gave us a strong street/city split, so it
		// intentionally wins over the looser token-based fallback below.
		return q
	}

	if q.City == "" && len(cityParts) == 0 && len(cleanedStreet) > 1 && (q.Housenumber != "" || q.Postcode != "") {
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
	query := normalizeAddressQuery(q)
	for _, e := range entries {
		score := matchScoreNormalized(e, query)
		if score > bestScore {
			bestScore = score
			best = e
		}
	}
	if bestScore <= 0 {
		return AddressEntry{}, false
	}
	// A house number alone is never a safe destination. In a regional extract
	// there can be hundreds of identical numbers, and choosing the first one
	// silently can send a route to a completely different town. A valid match
	// with a house number must also match at least one other address or POI
	// field (street, city, name, brand, ...).
	const houseNumberOnlyScore = 4
	if query.housenumber != "" && bestScore <= houseNumberOnlyScore {
		return AddressEntry{}, false
	}
	return best, true
}

func matchScore(e AddressEntry, q AddressQuery) int {
	return matchScoreNormalized(e, normalizeAddressQuery(q))
}

type normalizedAddressQuery struct {
	street, housenumber, postcode, city, raw string
}

func normalizeAddressQuery(q AddressQuery) normalizedAddressQuery {
	return normalizedAddressQuery{
		street:      normalize(q.Street),
		housenumber: normalize(q.Housenumber),
		postcode:    normalize(q.Postcode),
		city:        normalize(q.City),
		raw:         normalize(strings.TrimSpace(q.Raw)),
	}
}

func matchScoreNormalized(e AddressEntry, q normalizedAddressQuery) int {
	return normalizedAddressFieldsFromTags(e.Tags, q).score(q)
}

func equalNorm(a, b string) bool { return normalize(a) == normalize(b) }

func containsNorm(haystack, needle string) bool {
	h := normalize(haystack)
	n := normalize(needle)
	return h != "" && n != "" && strings.Contains(h, n)
}

func SearchAddresses(entries []AddressEntry, q AddressQuery, limit int) []AddressEntry {
	return searchAddressEntries(entries, normalizeAddressQuery(q), limit, func(i int, q normalizedAddressQuery) int { return matchScoreNormalized(entries[i], q) })
}

func searchAddressEntries(entries []AddressEntry, query normalizedAddressQuery, limit int, score func(int, normalizedAddressQuery) int) []AddressEntry {
	type scored struct {
		e AddressEntry
		s int
	}
	if limit <= 0 || limit > len(entries) {
		limit = len(entries)
	}
	if limit == 0 {
		return nil
	}
	// Address search runs on every type-ahead request. Keep a bounded ordered
	// result set instead of allocating and sorting every matching address.
	// With the UI's small limits this has the same ranking at a fraction of
	// the work for a large regional address index.
	sc := make([]scored, 0, limit)
	allResults := limit == len(entries)
	better := func(a, b scored) bool {
		return a.s > b.s || (a.s == b.s && a.e.ID < b.e.ID)
	}
	for i, e := range entries {
		s := score(i, query)
		if s <= 0 {
			continue
		}
		candidate := scored{e: e, s: s}
		if allResults {
			sc = append(sc, candidate)
			continue
		}
		if len(sc) == limit && !better(candidate, sc[len(sc)-1]) {
			continue
		}
		insertAt := len(sc)
		for insertAt > 0 && better(candidate, sc[insertAt-1]) {
			insertAt--
		}
		if len(sc) < limit {
			sc = append(sc, scored{})
		}
		copy(sc[insertAt+1:], sc[insertAt:len(sc)-1])
		sc[insertAt] = candidate
	}
	// Unlimited callers need every match. Collect then sort once rather than
	// repeatedly shifting an ever-growing result slice (quadratic work).
	if allResults {
		slices.SortStableFunc(sc, func(a, b scored) int {
			if better(a, b) {
				return -1
			}
			if better(b, a) {
				return 1
			}
			return 0
		})
	}
	res := make([]AddressEntry, len(sc))
	for i := range sc {
		res[i] = sc[i].e
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
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
	}
	if opt.Engine == EngineCH {
		return 0, ErrRouteEngineUnsupported
	}
	opt = opt.withDefaults()
	if opt.Engine == EngineDijkstraNode || !needsTurnState(opt) {
		_, cost, err := r.nodeSearch(ctx, from, to, opt, false)
		return cost, err
	}
	_, cost, err := r.turnSearch(ctx, from, to, opt, false)
	return cost, err
}

func (r *Router) RouteWithOptions(ctx context.Context, from, to int64, opt RouteOptions) (RouteResult, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return RouteResult{}, err
		}
	}
	if opt.Engine == EngineCH {
		return RouteResult{}, ErrRouteEngineUnsupported
	}
	opt = opt.withDefaults()
	if opt.Engine == EngineDijkstraNode || !needsTurnState(opt) {
		path, cost, err := r.nodeSearch(ctx, from, to, opt, true)
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
	path, cost, err := r.turnSearch(ctx, from, to, opt, true)
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
		// maneuvers are not part of internal Path (keeps compatibility)
		// but callers who JSON-encode RouteResult will see `maneuvers`.
		// Note: field added dynamically via JSON marshalling helper below if needed.
	}, nil
}

// streetNameForNode returns the display name captured for a sampled road node.
// The lookup is used for every interior point when generating maneuvers, so a
// pre-built index avoids repeatedly scanning all named streets on long routes.
func (r *Router) streetNameForNode(node int64) string {
	if r.streetNameByNode != nil {
		// The index covers every named sample. An absent entry is an unnamed
		// node, not a reason to scan all streets again at each geometry point.
		return r.streetNameByNode[node]
	}
	// Keep manually constructed Router values and older serialized test data
	// working. Normal PBF-built routers always have streetNameByNode.
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
		return []Maneuver{{Type: "arrive", Instruction: "Ziel erreicht", Lat: c.Lat, Lon: c.Lon, Node: path[0], DistanceM: 0, DurationS: 0}}
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
	depart := Maneuver{Type: "depart", Instruction: "Los geht's", Lat: coords[0].Lat, Lon: coords[0].Lon, Node: path[0]}
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
			instr := "Weiterfahren"
			if angChange {
				if math.Abs(d) > 150 {
					mtype = "uturn"
					instr = "Wenden"
				} else if d > 0 {
					mtype = "turn-right"
					instr = "Rechts abbiegen"
				} else {
					mtype = "turn-left"
					instr = "Links abbiegen"
				}
			} else if nameChange {
				mtype = "turn"
				instr = "Abbiegen"
			}
			// append street name if available
			if nextName != "" {
				instr = fmt.Sprintf("%s auf %s", instr, nextName)
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
	arrive := Maneuver{Type: "arrive", Instruction: "Ziel erreicht", Lat: coords[n-1].Lat, Lon: coords[n-1].Lon, Node: path[n-1], DistanceM: 0, DurationS: 0}
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

// BuildCH constructs an incomplete experimental upward graph.
// Deprecated: RouteWithOptions rejects EngineCH because this graph lacks the
// shortcuts needed for correct shortest paths.
func (r *Router) BuildCH() {
	if r.g.store != nil {
		return
	}
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
		return nil, 0, ErrRouteEngineNotReady
	}

	eval := newRouteEvaluator(opt)
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
			alt := dist[u] + eval.edgeCost(r, e)
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
		return nil, 0, ErrRouteNoPath
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
	return r.dijkstraNodeSearch(ctx, from, to, opt, true)
}

func (r *Router) dijkstraNodeSearch(ctx context.Context, from, to int64, opt RouteOptions, wantPath bool) ([]int64, float64, error) {
	opt.Engine = EngineDijkstraNode
	return r.nodeSearch(ctx, from, to, opt, wantPath)
}

// dijkstraItem is a small heap entry used by the node-Dijkstra implementation.
type dijkstraItem struct {
	id   int64
	dist float64
}

func (r *Router) computeMetrics(path []int64, opt RouteOptions) (distM, durS float64) {
	if len(path) < 2 {
		return 0, 0
	}
	eval := newRouteEvaluator(opt)
	for i := 0; i < len(path)-1; i++ {
		e, ok := r.edgeBetween(path[i], path[i+1])
		if !ok {
			continue
		}
		distM += e.DistM
		durS += eval.edgeTimeSeconds(e)
	}
	return distM, durS
}

func (r *Router) edgeBetween(from, to int64) (Edge, bool) {
	best := Edge{}
	found := false
	bestD := math.MaxFloat64
	for it := r.g.edges(from); it.next(); {
		e := it.edge()
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
	s turnState
	f float64
	g float64
}

func (r *Router) astar(ctx context.Context, from, to int64, opt RouteOptions, wantPath bool) (goal turnState, goalCost float64, came map[turnState]turnState, err error) {
	if from == to {
		if wantPath {
			came = map[turnState]turnState{}
		}
		return turnState{prev: 0, cur: from}, 0, came, nil
	}
	if _, ok := r.g.coord(from); !ok {
		return turnState{}, 0, nil, ErrRouteStartUnreachable
	}
	target, ok := r.g.coord(to)
	if !ok {
		return turnState{}, 0, nil, ErrRouteTargetUnreachable
	}
	if r.g.degree(from) == 0 {
		return turnState{}, 0, nil, ErrRouteStartUnreachable
	}

	if wantPath {
		came = make(map[turnState]turnState, min(64, r.NodeCount()))
	}

	var bearings routeBearingCache[int64]
	eval := newRouteEvaluator(opt)
	heuristic := r.queryHeuristic(to, target, opt)
	start := turnState{prev: 0, cur: from}
	gScore := make(map[turnState]float64, min(64, r.NodeCount()))
	gScore[start] = 0

	pq := routeValueHeap{}
	pq.push(pqItem{s: start, g: 0, f: heuristic.estimate(r, from)})

	pops := 0
	for len(pq) > 0 {
		pops++
		if ctx != nil && pops%1024 == 0 {
			select {
			case <-ctx.Done():
				return turnState{}, 0, nil, ctx.Err()
			default:
			}
		}

		curIt := pq.pop()
		curS := curIt.s

		if best, ok := gScore[curS]; ok && curIt.g > best {
			continue // stale
		}

		if curS.cur == to {
			return curS, curIt.g, came, nil
		}

		transition := eval.prepareCachedTransition(r, curS.prev, curS.cur, &bearings)
		for it := r.g.edges(curS.cur); it.next(); {
			e := it.edge()
			if !eval.edgeAllowed(e) {
				continue
			}
			next := e.To
			base := eval.edgeCost(r, e)
			pen := transition.penalty(r, next)
			tent := curIt.g + base + pen

			nextS := turnState{prev: curS.cur, cur: next}
			if old, ok := gScore[nextS]; ok && tent >= old {
				continue
			}

			gScore[nextS] = tent
			if wantPath {
				came[nextS] = curS
			}

			h := heuristic.estimate(r, next)
			pq.push(pqItem{s: nextS, g: tent, f: tent + h})
		}
	}

	return turnState{}, 0, nil, ErrRouteNoPath
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

// A route's target never changes. Reuse its latitude cosine across all
// estimates while keeping the same great-circle formula and cost units.
type routeHeuristic struct {
	target         Coord
	cosLatitude    float64
	objective      Objective
	maxSpeedKph    float64
	disabled       bool
	landmarks      *landmarkIndex
	landmarkTarget [8]uint16
	landmarkScale  float64
	landmarkTime   bool
}

func newRouteHeuristic(target Coord, opt RouteOptions) routeHeuristic {
	h := routeHeuristic{
		target: target, objective: opt.Objective, maxSpeedKph: opt.Weights.MaxSpeedKph,
		disabled: opt.Engine == EngineDijkstra || opt.Engine == EngineDijkstraNode || opt.EmergencyMode || opt.TerritoryCost != nil,
	}
	if !h.disabled {
		h.cosLatitude = math.Cos(deg2rad(target.Lat))
	}
	return h
}

func (h *routeHeuristic) estimate(r *Router, from int64) float64 {
	if h.disabled {
		return 0
	}
	if h.landmarks != nil {
		return h.estimateIndex(r.g.store, uint32(r.g.store.nodeIndex(from)))
	}
	a, _ := r.g.coord(from)
	return h.estimateCoordinate(a)
}

func (h *routeHeuristic) estimateCoordinate(a Coord) float64 {
	if h.disabled {
		return 0
	}
	dLat := deg2rad(h.target.Lat - a.Lat)
	dLon := deg2rad(h.target.Lon - a.Lon)
	sinLat, sinLon := math.Sin(dLat/2), math.Sin(dLon/2)
	v := sinLat*sinLat + math.Cos(deg2rad(a.Lat))*h.cosLatitude*sinLon*sinLon
	d := 6371000.0 * (2 * math.Atan2(math.Sqrt(v), math.Sqrt(1-v)))

	switch h.objective {
	case ObjectiveDuration:
		return d * 3.6 / h.maxSpeedKph
	case ObjectiveEconomy:
		return d / 1000
	default:
		return d
	}
}

// routeEvaluator resolves profile settings once per search. Keep it local to
// the query: callers can use different profiles concurrently on the same graph.
// The evaluator only borrows the built-in profile's immutable highway set.
type routeEvaluator struct {
	options         RouteOptions
	allowedHighways map[string]bool
	speedScale      float64
	crossingPenalty float64
	turnDirections  bool
}

func newRouteEvaluator(opt RouteOptions) routeEvaluator {
	eval := routeEvaluator{options: opt, speedScale: 1}
	if opt.Profile != "" {
		if def := profileDefByID(opt.Profile); def != nil {
			eval.allowedHighways = def.allowedSet
			if def.SpeedScale > 0 && def.SpeedScale < 1 {
				eval.speedScale = def.SpeedScale
			}
		}
	}
	w := &eval.options.Weights
	if w.Crossing > 0 {
		eval.crossingPenalty += w.Crossing
	}
	if w.TrafficLightPenalty > 0 {
		eval.crossingPenalty += w.TrafficLightPenalty
	}
	eval.turnDirections = w.NoLeftTurn || w.LeftTurn != 0 || w.RightTurn != 0 || w.UTurn != 0
	return eval
}

// edgeCost is baseEdgeCost, optionally scaled by opt.TerritoryCost. The
// scaling is a no-op whenever TerritoryCost is nil, the default.
func (eval *routeEvaluator) edgeCost(r *Router, e Edge) float64 {
	cost := eval.baseEdgeCost(e)
	if policy := eval.options.TerritoryCost; policy != nil {
		if c, ok := r.g.coord(e.To); ok {
			cost *= policy.factorFor(c)
		}
	}
	return cost
}

func (eval *routeEvaluator) baseEdgeCost(e Edge) float64 {
	if eval.options.EmergencyMode {
		// BOS/emergency: minimize time, use higher assumed speeds.
		v := e.SpeedKph
		if v <= 0 {
			v = 60
		}
		v *= 1.3
		if v < 10 {
			v = 10
		}
		return e.DistM * 3.6 / v
	}
	switch eval.options.Objective {
	case ObjectiveDuration:
		return eval.edgeTimeSeconds(e)
	case ObjectiveEconomy:
		km := e.DistM / 1000
		v := eval.effectiveSpeedKph(e)
		f := 1.0 + 0.7*math.Pow(v/100.0, 2)
		return km * f
	default:
		return e.DistM
	}
}

// All outgoing edges in one expansion share the same incoming bearing and
// crossing cost. Compute those once, including file-graph coordinate lookups.
type routeTransition struct {
	weights         *ProWeights
	crossing        float64
	current         Coord
	incomingBearing float64
	turnDirections  bool
	bearings        *routeBearingCache[int64]
	currentID       int64
}

func (eval *routeEvaluator) prepareTransition(r *Router, prev, cur int64) routeTransition {
	return eval.prepareCachedTransition(r, prev, cur, nil)
}

func (eval *routeEvaluator) prepareCachedTransition(r *Router, prev, cur int64, bearings *routeBearingCache[int64]) routeTransition {
	transition := routeTransition{weights: &eval.options.Weights, bearings: bearings, currentID: cur}
	if prev == 0 {
		return transition
	}
	if eval.crossingPenalty > 0 && r.g.degree(cur) > 2 {
		transition.crossing = eval.crossingPenalty
	}
	if !eval.turnDirections {
		return transition
	}
	p, ok1 := r.g.coord(prev)
	c, ok2 := r.g.coord(cur)
	if ok1 && ok2 {
		transition.current = c
		if bearings != nil {
			transition.incomingBearing = bearings.bearing(prev, cur, p, c)
		} else {
			transition.incomingBearing = bearingDeg(p.Lat, p.Lon, c.Lat, c.Lon)
		}
		transition.turnDirections = true
	}
	return transition
}

func (transition *routeTransition) penalty(r *Router, next int64) float64 {
	pen := transition.crossing
	if !transition.turnDirections {
		return pen
	}
	n, ok := r.g.coord(next)
	if !ok {
		return pen
	}
	if transition.bearings != nil {
		outgoing := transition.bearings.bearing(transition.currentID, next, transition.current, n)
		return transition.penaltyBearing(outgoing)
	}
	return transition.penaltyCoordinate(n)
}

func (transition *routeTransition) penaltyCoordinate(n Coord) float64 {
	c := transition.current
	outgoing := bearingDeg(c.Lat, c.Lon, n.Lat, n.Lon)
	return transition.penaltyBearing(outgoing)
}

func (transition *routeTransition) penaltyBearing(outgoing float64) float64 {
	pen := transition.crossing
	w := transition.weights
	switch turnKindForBearings(transition.incomingBearing, outgoing) {
	case turnLeft:
		if w.NoLeftTurn {
			pen += noLeftTurnPenalty
		} else {
			pen += w.LeftTurn
		}
	case turnRight:
		pen += w.RightTurn
	case turnUTurn:
		pen += w.UTurn
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
	p, ok1 := r.g.coord(prev)
	c, ok2 := r.g.coord(cur)
	n, ok3 := r.g.coord(next)
	if !ok1 || !ok2 || !ok3 {
		return turnStraight
	}
	b1 := bearingDeg(p.Lat, p.Lon, c.Lat, c.Lon)
	b2 := bearingDeg(c.Lat, c.Lon, n.Lat, n.Lon)
	return turnKindForBearings(b1, b2)
}

func turnKindForBearings(incoming, outgoing float64) turnKind {
	diff := angleDiffDeg(outgoing, incoming)
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
	// Both bearings are in [0, 360), so the shifted difference needs at most
	// two revolutions removed. Keep the original addition order: changing it
	// can move a rounded value across a turn-classification threshold.
	d := a - b + 540
	if d >= 720 {
		d -= 720
	} else if d >= 360 {
		d -= 360
	}
	return d - 180
}

func bearingDeg(lat1, lon1, lat2, lon2 float64) float64 {
	φ1 := deg2rad(lat1)
	φ2 := deg2rad(lat2)
	Δλ := deg2rad(lon2 - lon1)
	y := math.Sin(Δλ) * math.Cos(φ2)
	x := math.Cos(φ1)*math.Sin(φ2) - math.Sin(φ1)*math.Cos(φ2)*math.Cos(Δλ)
	θ := math.Atan2(y, x)
	bearing := rad2deg(θ) + 360
	if bearing >= 360 {
		bearing -= 360
	}
	return bearing
}

func rad2deg(r float64) float64 { return r * 180 / math.Pi }

func (eval *routeEvaluator) effectiveSpeedKph(e Edge) float64 {
	v := e.SpeedKph
	if v <= 0 {
		v = 50
	}
	if eval.speedScale != 1 {
		v *= eval.speedScale
	}
	if maxSpeed := eval.options.Weights.MaxSpeedKph; maxSpeed > 0 && v > maxSpeed {
		v = maxSpeed
	}
	if v < 3 {
		v = 3
	}
	return v
}

func (eval *routeEvaluator) edgeTimeSeconds(e Edge) float64 {
	return e.DistM * 3.6 / eval.effectiveSpeedKph(e)
}

func (eval *routeEvaluator) edgeAllowed(e Edge) bool {
	w := &eval.options.Weights
	if w.VehicleHeightM > 0 && e.MaxHeightM > 0 && w.VehicleHeightM > e.MaxHeightM {
		return false
	}
	if w.VehicleWeightT > 0 && e.MaxWeightT > 0 && w.VehicleWeightT > e.MaxWeightT {
		return false
	}
	return eval.allowedHighways == nil || e.HwyType == "" || eval.allowedHighways[e.HwyType]
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

// mapLabelCellSize keeps label lookups small at interactive map zoom levels
// (~2.2 km latitude). It is intentionally independent of the nearest-node
// index so label queries retain stable bounds even for a clipped graph.
const mapLabelCellSize = 0.02

func buildStreetLabelCells(streets map[string]streetEntry, coords map[int64]Coord) map[int64][]streetLabelRef {
	if len(streets) == 0 {
		return nil
	}
	cells := make(map[int64][]streetLabelRef, min(len(streets), 4096))
	for key, entry := range streets {
		for _, nodeID := range entry.NodeIDs {
			coord, ok := coords[nodeID]
			if !ok {
				continue
			}
			x, y := mapLabelCell(coord.Lat, coord.Lon)
			cells[cellKey(x, y)] = append(cells[cellKey(x, y)], streetLabelRef{streetKey: key, nodeID: nodeID})
		}
	}
	return cells
}

func buildStreetNameIndex(streets map[string]streetEntry) map[int64]string {
	if len(streets) == 0 {
		return nil
	}
	byNode := make(map[int64]string, len(streets))
	for _, entry := range streets {
		if entry.Display == "" {
			continue
		}
		for _, nodeID := range entry.NodeIDs {
			// Junctions can belong to multiple named ways. Pick one stable name;
			// the former map iteration happened to pick an arbitrary one anyway.
			if prior, exists := byNode[nodeID]; !exists || entry.Display < prior {
				byNode[nodeID] = entry.Display
			}
		}
	}
	return byNode
}

func mapLabelCell(lat, lon float64) (int32, int32) {
	return int32(math.Floor((lat + 90) / mapLabelCellSize)), int32(math.Floor((lon + 180) / mapLabelCellSize))
}

func labelCellRange(window CoordWindow) (minX, minY, maxX, maxY int32) {
	minX, minY = mapLabelCell(window.MinLat, window.MinLon)
	maxX, maxY = mapLabelCell(window.MaxLat, window.MaxLon)
	return minX, minY, maxX, maxY
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
		visit := func(id int64, c Coord) {
			d := haversineMeters(lat, lon, c.Lat, c.Lon)
			if d < best {
				best = d
				bestID = id
			}
		}
		if r.g.store != nil {
			for it := r.g.store.cellNodesIter(cellKey(ix, iy)); it.next(); {
				visit(it.nodeID(), it.coord())
			}
			return
		}
		for _, id := range r.idx.cells[cellKey(ix, iy)] {
			c, _ := r.g.coord(id)
			visit(id, c)
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
			// Use the actual query's distance to the searched rectangle. The
			// nearest node can lie across a cell boundary even when the current
			// cell already contains a point less than one cell-width away.
			latGap := math.Min(lat-(r.idx.minLat+float64(ix0-radius)*r.idx.cellSize), r.idx.minLat+float64(ix0+radius+1)*r.idx.cellSize-lat)
			lonGap := math.Min(lon-(r.idx.minLon+float64(iy0-radius)*r.idx.cellSize), r.idx.minLon+float64(iy0+radius+1)*r.idx.cellSize-lon)
			if latGap > 0 && lonGap > 0 {
				const earthRadius = 6371000.0
				toMeridian := math.Asin(math.Min(1, math.Abs(math.Cos(deg2rad(lat))*math.Sin(deg2rad(lonGap))))) * earthRadius
				minPossible := math.Min(deg2rad(latGap)*earthRadius, toMeridian)
				if minPossible > best {
					break
				}
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
	r.g.eachRoutableNode(func(id int64, c Coord) {
		d := haversineMeters(lat, lon, c.Lat, c.Lon)
		if d < best {
			best = d
			bestID = id
		}
	})
	if best == math.MaxFloat64 {
		return 0, 0, false
	}
	return bestID, best, true
}

// ---- Highway parsing helpers ----

type highwayWay struct {
	refs       []byte // delta-varint OSM IDs; private to the importer
	hwyType    string // OSM highway tag value
	speedKph   float64
	maxHeightM float64
	maxWeightT float64
	oneway     int8 // 0=both, 1=forward, -1=reverse
}

func packHighwayRefs(ids []int64) []byte {
	// Most ways fit on the stack. Clone just their encoded bytes rather than
	// retaining another full-width reference array for the entire import.
	var buffer [512]byte
	encoded := buffer[:0]
	var previous int64
	for _, id := range ids {
		// Signed subtraction/addition intentionally wrap just as PBF deltas do,
		// preserving even transitions across the ends of the int64 range.
		encoded = binary.AppendVarint(encoded, id-previous)
		previous = id
	}
	return bytes.Clone(encoded)
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
	// Fast path for the overwhelmingly common already-normalized ASCII key.
	// More importantly, the general path below avoids allocating a lowercase
	// string, a Replacer result and a rune slice for every indexed field.
	clean := true
	for _, r := range s {
		if r >= 'A' && r <= 'Z' || r == ' ' || r == '-' || r == ',' || r == '.' || r == ';' || r == ':' || r > 127 {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var out strings.Builder
	out.Grow(len(s))
	for _, r := range s {
		r = unicode.ToLower(r)
		switch r {
		case ' ', '-', ',', '.', ';', ':':
			continue
		case 'ß':
			out.WriteString("ss")
		case 'ä':
			out.WriteString("ae")
		case 'ö':
			out.WriteString("oe")
		case 'ü':
			out.WriteString("ue")
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
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
	sinLat, sinLon := math.Sin(dLat/2), math.Sin(dLon/2)
	a := sinLat*sinLat + math.Cos(deg2rad(lat1))*math.Cos(deg2rad(lat2))*sinLon*sinLon
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return R * c
}

func deg2rad(d float64) float64 { return d * math.Pi / 180 }

// AllStreets returns one representative per named street for offline indexing.
func (r *Router) AllStreets() []StreetMatch {
	out := make([]StreetMatch, 0, len(r.streets))
	for _, e := range r.streets {
		for _, id := range e.NodeIDs {
			if c, ok := r.Coord(id); ok {
				out = append(out, StreetMatch{Name: e.Display, NodeID: id, Coord: c})
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

package osmmini

import (
    "container/heap"
    "errors"
    "math"
    "sort"
    "strings"
)

// Coord holds WGS84 coordinates.
type Coord struct {
    Lat float64
    Lon float64
}

type edge struct {
    to int64
    w  float64 // meters
}

type Graph struct {
    coords map[int64]Coord
    adj    map[int64][]edge
}

// Router wraps a road graph built from OSM highways.
type Router struct {
    g Graph
    streetNodes map[string][]int64
}

// NodeCount returns number of nodes in the graph.
func (r *Router) NodeCount() int { return len(r.g.coords) }

// Coord returns the coordinate for a given node ID, if present.
func (r *Router) Coord(id int64) (Coord, bool) {
    c, ok := r.g.coords[id]
    return c, ok
}

// CoordsForPath returns the coordinates for each node id in the path.
func (r *Router) CoordsForPath(path []int64) []Coord {
    coords := make([]Coord, 0, len(path))
    for _, id := range path {
        if c, ok := r.g.coords[id]; ok {
            coords = append(coords, c)
        }
    }
    return coords
}

// StreetNode returns any node that belongs to a highway with the given street name.
func (r *Router) StreetNode(street string) (int64, bool) {
    ids := r.streetNodes[normalize(street)]
    for _, id := range ids {
        if _, ok := r.g.coords[id]; ok {
            return id, true
        }
    }
    return 0, false
}

// AddressEntry stores an addressable point extracted from addr:* nodes.
type AddressEntry struct {
    ID    int64
    Coord Coord
    Tags  Tags
}

// BuildRouterWithAddresses parses a PBF, builds a highway graph and collects address nodes.
// It keeps all nodes for routing, but only ways tagged as highway become edges.

func BuildRouterWithAddresses(path string) (*Router, []AddressEntry, error) {
    coords := make(map[int64]Coord, 1<<20)
    ways := make([][]int64, 0, 1<<18)
    addrs := make([]AddressEntry, 0, 1<<16)
    streetNodes := make(map[string][]int64)

    opts := Options{
        EmitWayNodeIDs:      true,
        EmitRelationMembers: false,
    }

    cb := Callbacks{
        Node: func(id int64, lat, lon float64) error {
            coords[id] = Coord{Lat: lat, Lon: lon}
            return nil
        },
        AddressNode: func(n Node) error {
            addrs = append(addrs, AddressEntry{ID: n.ID, Coord: Coord{Lat: n.Lat, Lon: n.Lon}, Tags: n.Tags})
            return nil
        },
        HighwayWay: func(w Way) error {
            if len(w.NodeIDs) >= 2 {
                // copy to detach from reused buffers
                ids := make([]int64, len(w.NodeIDs))
                copy(ids, w.NodeIDs)
                ways = append(ways, ids)
            }
            if name := w.Tags["name"]; name != "" {
                key := normalize(name)
                if len(w.NodeIDs) > 0 {
                    streetNodes[key] = append(streetNodes[key], w.NodeIDs...)
                }
            }
            return nil
        },
    }

    if err := ExtractFile(path, opts, cb); err != nil {
        return nil, nil, err
    }

    adj := make(map[int64][]edge, len(coords))
    for _, ids := range ways {
        for i := 0; i < len(ids)-1; i++ {
            aID := ids[i]
            bID := ids[i+1]
            a, okA := coords[aID]
            b, okB := coords[bID]
            if !okA || !okB {
                continue
            }
            d := haversineMeters(a.Lat, a.Lon, b.Lat, b.Lon)
            if d <= 0 || math.IsNaN(d) || math.IsInf(d, 0) {
                continue
            }
            adj[aID] = append(adj[aID], edge{to: bID, w: d})
            adj[bID] = append(adj[bID], edge{to: aID, w: d})
        }
    }

    return &Router{g: Graph{coords: coords, adj: adj}, streetNodes: streetNodes}, addrs, nil
}

// NearestNode returns the closest graph node to the given coordinate.
func (r *Router) NearestNode(lat, lon float64) (int64, float64, bool) {
    var bestID int64
    best := math.MaxFloat64
    for id, c := range r.g.coords {
        if len(r.g.adj[id]) == 0 {
            continue // skip isolated nodes not in the road graph
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

// Route computes the shortest path (meters) using A* between two graph nodes.
func (r *Router) Route(from, to int64) ([]int64, float64, error) {
    if from == to {
        return []int64{from}, 0, nil
    }
    if _, ok := r.g.coords[from]; !ok {
        return nil, 0, errors.New("start node missing in graph")
    }
    if _, ok := r.g.coords[to]; !ok {
        return nil, 0, errors.New("target node missing in graph")
    }

    pq := priorityQueue{}
    start := &pqItem{id: from, f: 0, g: 0}
    heap.Push(&pq, start)

    came := make(map[int64]int64)
    gScore := map[int64]float64{from: 0}

    goalCoord := r.g.coords[to]

    for pq.Len() > 0 {
        cur := heap.Pop(&pq).(*pqItem)
        if cur.id == to {
            path := reconstructPath(came, to)
            return path, cur.g, nil
        }

        for _, e := range r.g.adj[cur.id] {
            tentative := cur.g + e.w
            if old, ok := gScore[e.to]; ok && tentative >= old {
                continue
            }
            gScore[e.to] = tentative
            h := haversineMeters(r.g.coords[e.to].Lat, r.g.coords[e.to].Lon, goalCoord.Lat, goalCoord.Lon)
            heap.Push(&pq, &pqItem{id: e.to, g: tentative, f: tentative + h})
            came[e.to] = cur.id
        }
    }

    return nil, 0, errors.New("no path found")
}

func reconstructPath(came map[int64]int64, goal int64) []int64 {
    path := []int64{goal}
    for {
        prev, ok := came[goal]
        if !ok {
            break
        }
        path = append(path, prev)
        goal = prev
    }
    // reverse
    for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
        path[i], path[j] = path[j], path[i]
    }
    return path
}

// AddressQuery represents a loose address request parsed from user input.
type AddressQuery struct {
    Street      string
    Housenumber string
    Postcode    string
    City        string
    Raw         string
}

// ParseAddressGuess does a very light parse of a freeform string.
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
        // collect potential city later; for now treat as street or city
    }

    // crude split: everything before postcode treated as street/city; if housenumber present, attach to street side
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

    // Remove housenumber token from streetParts
    cleanedStreet := []string{}
    for _, f := range streetParts {
        if q.Housenumber != "" && f == q.Housenumber {
            continue
        }
        cleanedStreet = append(cleanedStreet, f)
    }

    // If keine Postleitzahl angegeben und keine Stadt erkannt, nimm letztes Wort als Stadt.
    if q.City == "" && len(cityParts) == 0 && len(cleanedStreet) > 1 {
        q.City = cleanedStreet[len(cleanedStreet)-1]
        cleanedStreet = cleanedStreet[:len(cleanedStreet)-1]
    }

    q.Street = strings.TrimSpace(strings.Join(cleanedStreet, " "))
    q.City = strings.TrimSpace(strings.Join(cityParts, " "))
    return q
}

// FindBestAddress picks the highest-scoring address entry for the query.
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
    // small bonus if street contains query even when not exact (handles variants like str/straße)
    if score == 0 && q.Street != "" && containsNorm(street, q.Street) {
        score += 1
    }
    return score
}

func equalNorm(a, b string) bool { return normalize(a) == normalize(b) }

func containsNorm(haystack, needle string) bool {
    h := normalize(haystack)
    n := normalize(needle)
    return h != "" && n != "" && strings.Contains(h, n)
}

func normalize(s string) string {
    if s == "" {
        return ""
    }
    lower := strings.ToLower(s)
    // transliterate a few common German characters for matching
    replacer := strings.NewReplacer(
        "ß", "ss",
        "ä", "ae",
        "ö", "oe",
        "ü", "ue",
    )
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

// SearchAddresses returns up to `limit` address entries matching the query, ordered by score.
// If limit <= 0, all matches are returned.
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
    sort.Slice(sc, func(i, j int) bool {
        if sc[i].s == sc[j].s {
            return sc[i].e.ID < sc[j].e.ID
        }
        return sc[i].s > sc[j].s
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

// StreetMatch represents a highway/street found in the graph.
type StreetMatch struct {
    Name   string
    NodeID int64
    Coord  Coord
}

// SearchStreets returns up to `limit` streets whose normalized name contains the query.
func (r *Router) SearchStreets(q string, limit int) []StreetMatch {
    nq := normalize(q)
    if nq == "" {
        return nil
    }
    matches := make([]StreetMatch, 0, 8)
    for nameKey, ids := range r.streetNodes {
        if !strings.Contains(nameKey, nq) {
            continue
        }
        // pick first id that has coordinates
        for _, id := range ids {
            if c, ok := r.g.coords[id]; ok {
                matches = append(matches, StreetMatch{Name: nameKey, NodeID: id, Coord: c})
                break
            }
        }
        if limit > 0 && len(matches) >= limit {
            break
        }
    }
    return matches
}

// priority queue for A*
type priorityQueue []*pqItem

type pqItem struct {
    id  int64
    f   float64
    g   float64
    idx int
}

func (pq priorityQueue) Len() int { return len(pq) }

func (pq priorityQueue) Less(i, j int) bool { return pq[i].f < pq[j].f }

func (pq priorityQueue) Swap(i, j int) {
    pq[i], pq[j] = pq[j], pq[i]
    pq[i].idx = i
    pq[j].idx = j
}

func (pq *priorityQueue) Push(x interface{}) {
    iw := x.(*pqItem)
    iw.idx = len(*pq)
    *pq = append(*pq, iw)
}

func (pq *priorityQueue) Pop() interface{} {
    old := *pq
    n := len(old)
    iw := old[n-1]
    *pq = old[0 : n-1]
    return iw
}

// haversineMeters returns the great-circle distance in meters.
func haversineMeters(lat1, lon1, lat2, lon2 float64) float64 {
    const R = 6371000.0 // Earth radius in meters
    dLat := deg2rad(lat2 - lat1)
    dLon := deg2rad(lon2 - lon1)
    a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(deg2rad(lat1))*math.Cos(deg2rad(lat2))*math.Sin(dLon/2)*math.Sin(dLon/2)
    c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
    return R * c
}

func deg2rad(d float64) float64 { return d * math.Pi / 180 }
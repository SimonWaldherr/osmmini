package graphjson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	osm "simonwaldherr.de/go/osmmini/core"
)

const validGraph = `{"nodes":[{"id":9007199254740993,"lat":48,"lon":12},{"id":-2,"lat":48.001,"lon":12.001}],"edges":[{"from":9007199254740993,"to":-2,"dist_m":123,"speed_kph":30,"hwy_type":"residential"}]}`

func TestReadGraphPreservesIDsAndRouting(t *testing.T) {
	for _, tc := range []struct {
		input    string
		from, to int64
	}{
		{validGraph, 9007199254740993, -2},
		{`{"extra":{"nested":[true,{"v":null}],"large":1e999},"edges":[{"from":0,"to":2,"dist_m":2}],"nodes":[{"id":0,"lat":0,"lon":0},{"id":2,"lat":0,"lon":1}]}`, 0, 2},
	} {
		r, err := Read(strings.NewReader(tc.input))
		if err != nil {
			t.Fatal(err)
		}
		coords, edges := r.ExportGraph()
		if _, ok := coords[tc.from]; !ok {
			t.Fatalf("missing exact ID %d", tc.from)
		}
		if _, ok := coords[tc.to]; !ok {
			t.Fatalf("missing exact ID %d", tc.to)
		}
		res, err := r.RouteWithOptions(t.Context(), tc.from, tc.to, osm.RouteOptions{})
		if err != nil || len(res.Path) != 2 || res.Path[0] != tc.from || res.Path[1] != tc.to || res.DistanceM != edges[tc.from][0].DistM {
			t.Fatalf("route = %+v, %v", res, err)
		}
	}
}

func TestReadRejectsInvalidGraph(t *testing.T) {
	const nodes = `"nodes":[{"id":0,"lat":0,"lon":0}]`
	for _, tc := range []struct{ name, input, errorText string }{
		{"null graph", `null`, "expected"},
		{"missing fields", `{}`, "requires"},
		{"empty nodes", `{"nodes":[],"edges":[]}`, "nonempty nodes"},
		{"null nodes", `{"nodes":null,"edges":[]}`, "expected"},
		{"null node", `{"nodes":[null],"edges":[]}`, "requires id"},
		{"missing coordinates", `{"nodes":[{"id":1}],"edges":[]}`, "invalid coordinates"},
		{"latitude range", `{"nodes":[{"id":1,"lat":91,"lon":0}],"edges":[]}`, "invalid coordinates"},
		{"longitude range", `{"nodes":[{"id":1,"lat":0,"lon":181}],"edges":[]}`, "invalid coordinates"},
		{"duplicate ID", `{"nodes":[{"id":1,"lat":0,"lon":0},{"id":1,"lat":1,"lon":1}],"edges":[]}`, "duplicate node"},
		{"missing ID", `{"nodes":[{"lat":0,"lon":0}],"edges":[]}`, "requires id"},
		{"null ID", `{"nodes":[{"id":null,"lat":0,"lon":0}],"edges":[]}`, "invalid graph ID"},
		{"ID overflow", `{"nodes":[{"id":9223372036854775808,"lat":0,"lon":0}],"edges":[]}`, "invalid graph ID"},
		{"null edge", `{` + nodes + `,"edges":[null]}`, "requires from and to"},
		{"missing source", `{` + nodes + `,"edges":[{"to":0,"dist_m":1}]}`, "requires from and to"},
		{"missing destination", `{` + nodes + `,"edges":[{"from":0,"dist_m":1}]}`, "requires from and to"},
		{"dangling source", `{` + nodes + `,"edges":[{"from":2,"to":0,"dist_m":1}]}`, "unknown edge source"},
		{"dangling destination", `{` + nodes + `,"edges":[{"from":0,"to":2,"dist_m":1}]}`, "unknown edge destination"},
		{"negative distance", `{` + nodes + `,"edges":[{"from":0,"to":0,"dist_m":-1}]}`, "invalid cost"},
		{"negative speed", `{` + nodes + `,"edges":[{"from":0,"to":0,"dist_m":1,"speed_kph":-1}]}`, "invalid cost"},
		{"negative height", `{` + nodes + `,"edges":[{"from":0,"to":0,"dist_m":1,"max_height_m":-1}]}`, "invalid cost"},
		{"negative weight", `{` + nodes + `,"edges":[{"from":0,"to":0,"dist_m":1,"max_weight_t":-1}]}`, "invalid cost"},
		{"coordinate overflow", `{"nodes":[{"id":1,"lat":1e999,"lon":0}],"edges":[]}`, "node:"},
		{"duplicate nodes", `{` + nodes + `,"nodes":[],"edges":[]}`, "duplicate graph field"},
		{"duplicate edges", `{` + nodes + `,"edges":[],"edges":[]}`, "duplicate graph field"},
		{"trailing value", validGraph + ` {}`, "after graph"},
		{"truncated graph", validGraph[:len(validGraph)-1], "end of JSON input"},
		{"malformed extension", `{"extension":[1,}`, "invalid character"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Read(strings.NewReader(tc.input))
			if err == nil || !strings.Contains(err.Error(), tc.errorText) {
				t.Fatalf("error = %v, want %q", err, tc.errorText)
			}
		})
	}
}

func graphFixture(n int) []byte {
	var b bytes.Buffer
	b.WriteString(`{"nodes":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":%d,"lat":48,"lon":%g}`, i, 12+float64(i)/100000)
	}
	b.WriteString(`],"edges":[`)
	for i := 1; i < n; i++ {
		if i > 1 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"from":%d,"to":%d,"dist_m":10,"speed_kph":30}`, i-1, i)
	}
	b.WriteString(`]}`)
	return b.Bytes()
}

// Compare identical routing structures, including their spatial indexes. The
// legacy case retains the old flat-array decoder as a measurement reference.
func BenchmarkGraphRead(b *testing.B) {
	data := graphFixture(50_000)
	for _, mode := range []string{"flat-arrays", "stream"} {
		b.Run(mode, func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			for range b.N {
				var r *osm.Router
				if mode == "stream" {
					var err error
					r, err = Read(bytes.NewReader(data))
					if err != nil {
						b.Fatal(err)
					}
				} else {
					var g struct {
						Nodes []struct {
							ID       int64
							Lat, Lon float64
						}
						Edges []struct {
							From       int64   `json:"from"`
							To         int64   `json:"to"`
							DistM      float64 `json:"dist_m"`
							SpeedKph   float64 `json:"speed_kph"`
							MaxHeightM float64 `json:"max_height_m"`
							MaxWeightT float64 `json:"max_weight_t"`
						}
					}
					if err := json.Unmarshal(data, &g); err != nil {
						b.Fatal(err)
					}
					coords := make(map[int64]osm.Coord, len(g.Nodes))
					adj := make(map[int64][]osm.Edge, len(g.Nodes))
					for _, n := range g.Nodes {
						coords[n.ID] = osm.Coord{Lat: n.Lat, Lon: n.Lon}
					}
					for _, e := range g.Edges {
						adj[e.From] = append(adj[e.From], osm.Edge{To: e.To, DistM: e.DistM, SpeedKph: e.SpeedKph, MaxHeightM: e.MaxHeightM, MaxWeightT: e.MaxWeightT})
					}
					r = osm.NewRouterFromGraph(coords, adj)
				}
				if r.NodeCount() != 50_000 {
					b.Fatal(r.NodeCount())
				}
			}
		})
	}
}

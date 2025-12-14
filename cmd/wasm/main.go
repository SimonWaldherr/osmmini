//go:build js && wasm

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"syscall/js"

	osmmini "simonwaldherr.de/go/osmmini"
)

var r *osmmini.Router

type nodeJSON struct {
	ID  int64   `json:"id"`
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

type edgeJSON struct {
	From       int64   `json:"from"`
	To         int64   `json:"to"`
	DistM      float64 `json:"dist_m"`
	SpeedKph   float64 `json:"speed_kph"`
	MaxHeightM float64 `json:"max_height_m"`
	MaxWeightT float64 `json:"max_weight_t"`
}

type graphJSON struct {
	Nodes []nodeJSON `json:"nodes"`
	Edges []edgeJSON `json:"edges"`
}

func jsInit(this js.Value, args []js.Value) any {
	if len(args) < 1 {
		return js.ValueOf(map[string]any{"ok": false, "error": "missing graph JSON"})
	}
	var g graphJSON
	if err := json.Unmarshal([]byte(args[0].String()), &g); err != nil {
		return js.ValueOf(map[string]any{"ok": false, "error": fmt.Sprintf("unmarshal: %v", err)})
	}
	coords := make(map[int64]osmmini.Coord, len(g.Nodes))
	adj := make(map[int64][]osmmini.Edge, len(g.Nodes))
	for _, n := range g.Nodes {
		coords[n.ID] = osmmini.Coord{Lat: n.Lat, Lon: n.Lon}
	}
	for _, e := range g.Edges {
		adj[e.From] = append(adj[e.From], osmmini.Edge{
			To:         e.To,
			DistM:      e.DistM,
			SpeedKph:   e.SpeedKph,
			MaxHeightM: e.MaxHeightM,
			MaxWeightT: e.MaxWeightT,
		})
	}
	r = osmmini.NewRouterFromGraph(coords, adj)
	return js.ValueOf(map[string]any{"ok": true})
}

func jsRoute(this js.Value, args []js.Value) any {
	if r == nil {
		return js.ValueOf(map[string]any{"ok": false, "error": "router not initialized"})
	}
	if len(args) < 6 {
		return js.ValueOf(map[string]any{"ok": false, "error": "usage: route(lat1, lon1, lat2, lon2, engine, objective)"})
	}
	lat1 := args[0].Float()
	lon1 := args[1].Float()
	lat2 := args[2].Float()
	lon2 := args[3].Float()
	engine := osmmini.RouteEngine(args[4].String())
	objective := osmmini.Objective(args[5].String())

	fromID, _, ok := r.NearestNode(lat1, lon1)
	if !ok {
		return js.ValueOf(map[string]any{"ok": false, "error": "no start node"})
	}
	toID, _, ok := r.NearestNode(lat2, lon2)
	if !ok {
		return js.ValueOf(map[string]any{"ok": false, "error": "no end node"})
	}

	res, err := r.RouteWithOptions(context.Background(), fromID, toID, osmmini.RouteOptions{Engine: engine, Objective: objective})
	if err != nil {
		return js.ValueOf(map[string]any{"ok": false, "error": err.Error()})
	}
	path := make([]any, 0, len(res.PathCoords))
	for _, c := range res.PathCoords {
		path = append(path, map[string]any{"lat": c.Lat, "lon": c.Lon})
	}
	return js.ValueOf(map[string]any{
		"ok":         true,
		"path":       path,
		"distance_m": res.DistanceM,
		"duration_s": res.DurationS,
		"cost":       res.Cost,
		"engine":     string(res.Engine),
		"objective":  string(res.Objective),
	})
}

func main() {
	js.Global().Set("osmInit", js.FuncOf(jsInit))
	js.Global().Set("osmRoute", js.FuncOf(jsRoute))
	select {}
}

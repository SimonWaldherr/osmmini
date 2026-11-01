//go:build js && wasm

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"runtime"
	"strings"
	"syscall/js"

	"simonwaldherr.de/go/osmmini/cmd/wasm/internal/graphjson"
	osm "simonwaldherr.de/go/osmmini/core"
)

var router *osm.Router

func failure(message string) any {
	return js.ValueOf(map[string]any{"ok": false, "error": message})
}

func jsInit(_ js.Value, args []js.Value) any {
	if len(args) != 1 {
		return failure("usage: osmInit(graphJSONStringOrUint8Array)")
	}
	var next *osm.Router
	var err error
	switch input := args[0]; {
	case input.Type() == js.TypeString:
		// The decoder reads the string directly, without a second []byte copy.
		next, err = graphjson.Read(strings.NewReader(input.String()))
	case input.Type() == js.TypeObject && input.InstanceOf(js.Global().Get("Uint8Array")):
		data := make([]byte, input.Get("byteLength").Int())
		js.CopyBytesToGo(data, input)
		next, err = graphjson.Read(bytes.NewReader(data))
	default:
		return failure("graph must be a JSON string or Uint8Array")
	}
	if err != nil {
		return failure(err.Error())
	}
	// An unsuccessful replacement must leave the previous region usable.
	router = next
	return js.ValueOf(map[string]any{"ok": true})
}

func jsRoute(_ js.Value, args []js.Value) any {
	if router == nil {
		return failure("router not initialized")
	}
	if len(args) != 6 {
		return failure("usage: osmRoute(lat1, lon1, lat2, lon2, engine, objective)")
	}
	for i := 0; i < 4; i++ {
		if args[i].Type() != js.TypeNumber {
			return failure("coordinates must be numbers")
		}
		x := args[i].Float()
		bound := 90.0
		if i%2 != 0 {
			bound = 180
		}
		if math.IsNaN(x) || math.IsInf(x, 0) || x < -bound || x > bound {
			return failure("invalid coordinates")
		}
	}
	if args[4].Type() != js.TypeString || args[5].Type() != js.TypeString {
		return failure("engine and objective must be strings")
	}
	engine := osm.RouteEngine(args[4].String())
	objective := osm.Objective(args[5].String())
	fromID, _, ok := router.NearestNode(args[0].Float(), args[1].Float())
	if !ok {
		return failure("no start node")
	}
	toID, _, ok := router.NearestNode(args[2].Float(), args[3].Float())
	if !ok {
		return failure("no end node")
	}
	res, err := router.RouteWithOptions(context.Background(), fromID, toID, osm.RouteOptions{Engine: engine, Objective: objective})
	if err != nil {
		return failure(err.Error())
	}
	// One JSON transfer avoids allocating []any/maps for every coordinate and
	// thousands of syscall/js calls on long routes. The public object is unchanged.
	data, err := json.Marshal(struct {
		OK bool `json:"ok"`
		osm.RouteResult
	}{true, res})
	if err != nil {
		return failure(err.Error())
	}
	return js.Global().Get("JSON").Call("parse", string(data))
}

func jsReset(_ js.Value, _ []js.Value) any {
	router = nil
	// Release the Go graph before importing another large region. Linear WASM
	// memory need not shrink; the runtime can reuse the freed heap pages.
	runtime.GC()
	return js.ValueOf(map[string]any{"ok": true})
}

func main() {
	js.Global().Set("osmInit", js.FuncOf(jsInit))
	js.Global().Set("osmRoute", js.FuncOf(jsRoute))
	js.Global().Set("osmReset", js.FuncOf(jsReset))
	select {}
}

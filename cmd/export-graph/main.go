package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	osmmini "simonwaldherr.de/go/osmmini"
)

func main() {
	pbf := flag.String("pbf", "region.osm.pbf", "Path to OSM PBF")
	out := flag.String("out", "graph.json", "Output JSON file")
	flag.Parse()

	if _, err := os.Stat(*pbf); err != nil {
		log.Fatalf("pbf file not found: %v", err)
	}

	log.Printf("Building router from PBF %s (this may take a while)...", *pbf)
	r, _, err := osmmini.BuildRouterWithAddressesOptions(*pbf, osmmini.BuildOptions{})
	if err != nil {
		log.Fatalf("build router: %v", err)
	}

	coords, adj := r.ExportGraph()

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

	nodes := make([]nodeJSON, 0, len(coords))
	edges := make([]edgeJSON, 0, 1024)
	for id, c := range coords {
		nodes = append(nodes, nodeJSON{ID: id, Lat: c.Lat, Lon: c.Lon})
	}
	for from, es := range adj {
		for _, e := range es {
			edges = append(edges, edgeJSON{
				From:       from,
				To:         e.To,
				DistM:      e.DistM,
				SpeedKph:   e.SpeedKph,
				MaxHeightM: e.MaxHeightM,
				MaxWeightT: e.MaxWeightT,
			})
		}
	}

	outf, err := os.Create(*out)
	if err != nil {
		log.Fatalf("create out: %v", err)
	}
	defer outf.Close()

	enc := json.NewEncoder(outf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(map[string]any{"nodes": nodes, "edges": edges}); err != nil {
		log.Fatalf("encode: %v", err)
	}

	fmt.Printf("wrote %s (nodes=%d edges=%d)\n", *out, len(nodes), len(edges))
}

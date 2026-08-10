package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	osmmini "simonwaldherr.de/go/osmmini"
)

// runRouteCLI implements the standalone `osmmini route` subcommand. It
// builds a router directly from a PBF (the same way cmd/export-graph does)
// rather than talking to a running server, and optionally reports
// territory-layer transitions along the computed path. It is kept separate
// from the HTTP /api/v1/route handler in main.go -- wiring territory events
// into that JSON API too is a small, separate follow-up.
func runRouteCLI(args []string) {
	fs := flag.NewFlagSet("route", flag.ExitOnError)
	pbf := fs.String("pbf", "region.osm.pbf", "path to OSM PBF")
	from := fs.String("from", "", "start coordinate, \"lat,lon\"")
	to := fs.String("to", "", "end coordinate, \"lat,lon\"")
	territoryLayer := fs.String("territory-layer", "", "territory layer name to report route transitions for")
	territoriesPath := fs.String("territories", "", "territory GeoJSON file for --territory-layer")
	territoryEvents := fs.Bool("territory-events", false, "include territory transition events in the output")
	fs.Parse(args)

	if *from == "" || *to == "" {
		fmt.Fprintln(os.Stderr, "usage: osmmini route --pbf FILE --from LAT,LON --to LAT,LON [--territory-layer NAME --territories FILE.geojson --territory-events]")
		os.Exit(2)
	}
	fromCoord, ok := parseLatLon(*from)
	if !ok {
		log.Fatalf("route: invalid --from %q (want \"lat,lon\")", *from)
	}
	toCoord, ok := parseLatLon(*to)
	if !ok {
		log.Fatalf("route: invalid --to %q (want \"lat,lon\")", *to)
	}

	var store *osmmini.TerritoryStore
	if *territoryLayer != "" {
		if *territoriesPath == "" {
			log.Fatalf("route: --territory-layer requires --territories")
		}
		store = osmmini.NewTerritoryStore()
		if err := store.LoadLayer(*territoryLayer, *territoriesPath); err != nil {
			log.Fatalf("route: %v", err)
		}
	}

	if _, err := os.Stat(*pbf); err != nil {
		log.Fatalf("route: pbf file not found: %v", err)
	}
	log.Printf("Building router from PBF %s (this may take a while)...", *pbf)
	router, _, err := osmmini.BuildRouterWithAddressesOptions(*pbf, osmmini.BuildOptions{})
	if err != nil {
		log.Fatalf("route: build router: %v", err)
	}

	startID, _, ok := router.NearestNode(fromCoord.Lat, fromCoord.Lon)
	if !ok {
		log.Fatalf("route: no start node found near %v", fromCoord)
	}
	endID, _, ok := router.NearestNode(toCoord.Lat, toCoord.Lon)
	if !ok {
		log.Fatalf("route: no end node found near %v", toCoord)
	}

	res, err := router.RouteWithOptions(context.Background(), startID, endID, osmmini.RouteOptions{})
	if err != nil {
		log.Fatalf("route: %v", err)
	}

	out := map[string]any{
		"distance_m": res.DistanceM,
		"duration_s": res.DurationS,
		"path":       res.PathCoords,
	}
	if *territoryEvents {
		if store == nil {
			log.Fatalf("route: --territory-events requires --territory-layer")
		}
		out["territories"] = osmmini.TerritoryEventsForPath(store, *territoryLayer, res.PathCoords)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		log.Fatalf("route: %v", err)
	}
}

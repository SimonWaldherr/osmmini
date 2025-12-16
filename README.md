# OSMmini

Lightweight offline routing server and web UI using OSM PBF extracts.

![](https://simonwaldherr.de/gh-pages/osmmini.png)

Features
- Build a routing graph from an OSM PBF and serve offline routes via HTTP API
- Multiple routing engines: `astar`, `dijkstra`, `dijkstra-node` (node-only Dijkstra), minimal `ch` scaffold
- Web UI (Leaflet) in `cmd/web` with search, trip solver, settings and turn-by-turn maneuvers
- Tile proxy with local cache

Requirements
- Go 1.20+ (project used with go1.25)
- An OSM PBF extract (e.g. `region.osm.pbf`)

Quick start

1. Build or run the server (example):

```bash
go run ./cmd -pbf region.osm.pbf
```

2. Open the web UI: http://localhost:8080/

Flags
- `-pbf`: Path to OSM PBF (default `region.osm.pbf`)
- `-listen`: HTTP listen address (default `:8080`)
- `-tiles-dir`: Tile cache directory
- `-tile-upstream`: Upstream tile URL template
- `-build-ch`: Build Contraction Hierarchies after graph load (default true)

Frontend / assets
- The UI lives in `cmd/web` and static assets under `cmd/web/static` (Leaflet files, CSS, JS)
- The server prefers local `cmd/web/static/leaflet/*` assets and falls back to a CDN if not present

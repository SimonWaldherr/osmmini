# OSMmini

[![DOI](https://zenodo.org/badge/1116409323.svg)](https://doi.org/10.5281/zenodo.18929953)

OSMmini is an open-source routing server and map web UI that can run fully
offline. It builds its routing graph, address search and POI index from one
OpenStreetMap PBF extract, so it works for a single region without any external
service.

![OSMmini web UI](https://simonwaldherr.de/gh-pages/osmmini.png)

**Contents:**
[Features](#features) ·
[Quick start](#quick-start) ·
[Map profiles](#map-profiles) ·
[Large PBF files](#large-pbf-files) ·
[Web UI guide](#web-ui-guide) ·
[HTTP API](#http-api) ·
[Command-line tools](#command-line-tools) ·
[Configuration](#configuration) ·
[Production](#running-in-production) ·
[Development](#development)

## Features

**Routing and trips**
- Routing engines `astar`, `dijkstra` and `dijkstra-node`, optimising for
  distance or duration, with turn-by-turn directions
- Trip solver for up to 60 stops with dependencies ("A before B") and vehicle
  capacity; up to 16 stops are solved exactly
- **Route export as GPX** (for sat navs and GPS apps) **or GeoJSON** (for GIS
  tools), available in the UI, through the API and on the command line

**Search and maps**
- Address, street and POI search with German and English category aliases
  ([category catalog](docs/osm-categories.md))
- Map-first MapLibre GL UI with a mobile layout and a keyboard shortcut
  (<kbd>Ctrl</kbd>/<kbd>⌘</kbd>+<kbd>K</kbd>) to focus search
- Offline vector basemap generated from your PBF (tinyTiles) with adjustable
  styles; alternatively, OSM raster, BayernAtlas vector/WMTS or your own
  MBTiles/GeoTIFF

**GIS and field work**
- POI export (viewport or radius) as GeoJSON and distance measurement
- Import of GeoJSON, KML/KMZ and Shapefile layers; territory layers with lookup
  and CSV assignment
- OSM-Edit: a draft editor for nodes, ways and relations that exports `.osc`
  change files and never uploads anything itself
- Delivery-proof, maintenance and status log with barcode scanning and CSV
  export
- Emergency mode: fire hydrants, fire stations with vehicle rosters, and
  confidential objects that never leave the server

## Quick start

**Requirements:** Go 1.26.5+, Node.js (only for the JS tests), `curl`, and an OSM
PBF extract, for example from [Geofabrik](https://download.geofabrik.de/).

```bash
# 1. Download a PBF and the pinned MapLibre assets (one-off, needs network):
make offline-prep GEOFABRIK_URL='https://download.geofabrik.de/europe/germany/bayern/niederbayern-latest.osm.pbf'

# 2. Start the server:
make run                     # same as: go run -tags=sqliteimport ./cmd -pbf region.osm.pbf

# 3. Open http://localhost:8080/
```

The default profile ([`settings.json`](settings.json)) uses the local offline
map. Build it once with **Einstellungen → Offline-Karte erzeugen**. After that,
the map is available without a network connection. Routing, search and POIs
work as soon as the graph is loaded (see the log line `Graph ready: …`).

`osmmini help` lists all commands and flags, and `osmmini version` prints the
build version.

## Map profiles

| Profile | Start | Basemap |
| --- | --- | --- |
| Default | `make run` | Local tinyTiles vector map (offline). OSM raster and other presets can be selected in the settings |
| Bavaria | `make bayern ADMIN_TOKEN=… BAYERN_PBF=bayern.osm.pbf` | Official BayernAtlas vector map. It falls back automatically to BayernAtlas WMTS when WebGL is unavailable. This map only covers Bavaria |
| Fully offline | `make offline ADMIN_TOKEN=… PBF=region.osm.pbf` | tinyTiles only, no online source |

The map can be global, but routing, address search and POIs always cover only
the PBF you loaded.

### Offline map (tinyTiles)

**Offline-Karte erzeugen** builds the `.ttiles` artifact from the loaded PBF.
It includes roads by class, paths, buildings, water, forest and farmland. The
artifact is stored in `offline-tiles/basemap.ttiles` and is loaded again
automatically after a restart. If the same value was passed as `-admin-token`,
first enter it under **Einstellungen → Administrationsschutz**.

- **Style:** **Einstellungen → Offline-Karte → Kartenstil anpassen** has the
  presets Natur, Atlas, Nacht and Hoher Kontrast. You can also change colours,
  road width, label size and which layers are shown. Changes are previewed
  immediately and saved in the browser, and the tiles do not need to be
  rebuilt.
- **Waterways:** a separate local sidecar adds rivers and canals from zoom 7,
  streams from zoom 11 and drainage details from zoom 13. Existing artifacts are
  upgraded in the background at startup if the PBF has not changed.
- **Postcodes:** if the build includes postcode boundaries, you can query them
  with `GET /tinytiles/postcode/search?q=940`, `/tinytiles/postcode/94032` and
  `/tinytiles/postcode/at?lon=13.46&lat=48.57`.
- Complex multipolygon areas need a more detailed tile generator.

For a truly offline browser, the MapLibre assets must be present locally
(`make maplibre-assets`). OSMmini never loads them from a CDN.

## Large PBF files

Load a regional extract rather than a whole country or continent. The routing
graph, POI index and offline map then stay small:

```bash
go run ./cmd region-extract \
  -pbf germany-latest.osm.pbf \
  -bbox 11.7,47.8,14.3,49.3 \
  -output regions/niederbayern.osm.pbf

go run ./cmd -pbf regions/niederbayern.osm.pbf
```

`region-extract` requires the external tool
[`osmium`](https://osmcode.org/osmium-tool/). It uses the `complete_ways`
strategy, so ways that cross PBF block boundaries are kept in full. By default
it creates or reuses the spatial sidecar index `germany-latest.idx`
(`-index=false` skips this). A matching regional PBF that already exists is
reused (`-reuse=false` forces a rebuild).

The sidecar index can also be created on its own:

```bash
go run ./cmd pbf-index -pbf europe.osm.pbf   # writes europe.idx
```

The indexer streams the PBF one block at a time without loading it into memory.
For each block it records the byte range and node extent, together with the
file's size, timestamp and SHA-256. The index becomes invalid when the PBF
changes. Because ways reference nodes in other blocks, the index only selects
node blocks. Extracting complete ways still happens in `region-extract`.

## Web UI guide

### Routes and trips

Enter a start and destination (an address, a POI, `lat,lon`, or a click on the
map) and add stops as needed. With **Reihenfolge optimieren** switched on,
OSMmini calculates the best order of the stops. Once a
route has been calculated, these actions are available:

- **Einpassen / Löschen**: fit the route to the map or remove it
- **GeoJSON**: a LineString with distance and duration, for QGIS, uMap and
  similar tools
- **GPX**: start, stops and destination as waypoints, the turn-by-turn
  directions as `<rte>` and the exact road geometry as `<trk>`. It can be
  imported into Garmin devices, OsmAnd, Komoot, Locus and most GPS apps
- links for handing the route over to Google Maps or Apple Maps

### GIS & Geo-Werkzeuge

- Search POIs in the viewport or within 1–50 km of the map centre, filter them
  by name or category, and download them as GeoJSON. The UI shows at most 320
  points; the API returns up to 1,000.
- **Strecke messen**: draw a polyline independently of the route. The result is
  the sum of great-circle distances, not a driving distance or survey
  measurement.
- **Importieren**: GeoJSON, KML/KMZ and Shapefile ZIPs become map layers.
  Polygon layers are also available as territory layers. Admins can use one
  MBTiles or GeoTIFF file as an extra map source.

### OSM-Edit (draft editor)

OSM-Edit never uploads anything. Drafts stay in the browser (`localStorage`)
and can only be exported as an `.osc` change file or a `.json` backup for manual
upload in a full OSM editor.

1. **Finden**: select a place on the map, search for one, add a point, or draw
   a way or area. New vertices snap to existing ones.
2. **Bearbeiten**: typed fields for each place type, hints for common mistakes,
   a change summary and the full tag table. For ways, the editor can move
   vertices (**Punkte bearbeiten**), split a way (**Weg teilen**) and join two
   ways (**Mit Weg-ID verbinden**). <kbd>Ctrl</kbd>/<kbd>⌘</kbd>+<kbd>S</kbd>
   saves.
3. **Entwürfe**: undo/redo, compare with the current OSM state, export. If an
   object has changed on OSM in the meantime, the export is blocked.
4. **Relationen**: create a relation or load one by ID, reorder members and
   edit their roles.

Objects marked **vertraulich speichern** (for example fire-brigade internals or
access information) are stored only on the server, in
`confidential-objects.json`. They never appear in an OSM export, and they
require `-admin-token`.

Place types are defined in `cmd/web/osm-presets.js`; validation, topology and
export logic is in `cmd/web/osm-editor.js`.

### Zustellung & Wartung (log)

This local audit log is designed for mobile use:

- **Zustellnachweis**: barcode/QR code or object ID, recipient, reference, time
  and optional GPS position
- **Wartung**: work type, status and the person who did the work
- **Lage & Check**: availability, damage or closures of resources and operation
  sectors

Camera scanning uses the browser's `BarcodeDetector`, and manual entry always
works. Entries can be filtered and exported as CSV. When the connection drops,
the browser queues entries and sends them on the next contact.

How the data is stored depends on `-deployment-mode`:

| Mode | Storage | Suitable for |
| --- | --- | --- |
| `browser-local` | Only the browser's `localStorage`, exported as CSV | Single offline laptop or phone |
| `single-user` (default) | `operations.json` on the server, protected by `-admin-token` if set | One user or a trusted team |
| `multi-user` | Central history with a personal token for each operator, who is recorded in every entry | Teams |

```bash
cp operators.example.json operators.json   # replace the tokens with long random values
go run ./cmd -deployment-mode multi-user -operators-file operators.json
```

### Emergency mode (Einsatzmodus)

Map overlays for fire hydrants (`emergency=fire_hydrant`) and fire stations.
Stations can be enriched with vehicle rosters (call sign, equipment) manually or
by CSV import. The rosters are stored only locally in `fire-stations.json`.

## HTTP API

The full specification is in [`cmd/api/openapi.yaml`](cmd/api/openapi.yaml), and
the server also serves it at `/api/v1/openapi.yaml`. Coordinates in GeoJSON are
always `[lon, lat]`.

| Endpoint | Purpose |
| --- | --- |
| `GET /api/v1/health` | Liveness check (`{"ok":true}`) |
| `GET /api/v1/status` | Version, uptime, graph size, bounds, cache statistics |
| `GET /api/v1/search?q=…&limit=…` | Address, street and POI search |
| `POST /api/v1/route[?format=gpx\|geojson]` | Route between two locations |
| `POST /api/v1/trip/solve[?format=gpx\|geojson]` | Multi-stop trip, optionally optimised |
| `GET /api/v1/geo/pois` | POIs as GeoJSON (`bbox` or `lat`/`lon`/`radius_m`, `category`, `q`, `limit`) |
| `POST /api/v1/geo/measure` | Great-circle length of a polyline |
| `GET /api/v1/poi/{id}` | Details and tags for a POI |
| `GET /api/v1/territories[/{layer}]` | Loaded territory layers |
| `GET\|POST /api/v1/operations` | Delivery/maintenance log |
| `GET\|PUT /api/v1/settings` | Settings (writing requires `-admin-token` if set) |
| `POST /api/v1/tinytiles/build` | Build the offline map |

### Examples

```bash
# Search
curl 'http://localhost:8080/api/v1/search?q=Rathaus%20Passau&limit=5'

# Route as JSON; "from"/"to" accept {"lat","lon"} or {"query": "address"}
curl -X POST http://localhost:8080/api/v1/route \
  -H 'Content-Type: application/json' \
  -d '{"from":{"query":"Passau Hauptbahnhof"},"to":{"lat":48.5667,"lon":13.4319}}'

# The same route as a GPX file for a sat nav
curl -X POST 'http://localhost:8080/api/v1/route?format=gpx' \
  -H 'Content-Type: application/json' \
  -d '{"from":{"query":"Passau Hauptbahnhof"},"to":{"lat":48.5667,"lon":13.4319}}' \
  -o route.gpx

# Optimised trip as GeoJSON (route + numbered stops)
curl -X POST 'http://localhost:8080/api/v1/trip/solve?format=geojson' \
  -H 'Content-Type: application/json' \
  -d '{"plan":{"start":{"lat":48.574,"lon":13.46},"loop":true,"optimize":true,
       "stops":[{"id":"A","location":{"lat":48.56,"lon":13.43}},
                {"id":"B","location":{"lat":48.58,"lon":13.48}}]}}' \
  -o tour.geojson

# POIs within 5 km, filtered by category (German aliases work too)
curl 'http://localhost:8080/api/v1/geo/pois?lat=48.57&lon=13.46&radius_m=5000&category=Apotheke'
```

GPX responses use `application/gpx+xml`, GeoJSON responses use
`application/geo+json`, and both come with `Content-Disposition: attachment`.
An unknown `format` returns `400`.

## Command-line tools

All tools are subcommands of the same binary (`go run ./cmd <command>` or
`bin/osmmini <command>`). `osmmini <command> -h` shows their flags.

| Command | Purpose |
| --- | --- |
| `route` | Calculate a route directly from the PBF and print it as JSON, GPX or GeoJSON |
| `territories load` / `territory lookup` | Check a territory layer, or find the territory for a coordinate |
| `dispatch assign` / `dispatch manifests` | Assign CSV addresses to territories, or write one manifest per group |
| `geodata import` | Import GeoJSON, KML/KMZ or Shapefile as a layer |
| `pbf-index` / `region-extract` | Tools for [large PBF files](#large-pbf-files) |
| `version` / `help` | Show the version / overview |

```bash
# Route as GPX without a running server
go run ./cmd route -pbf region.osm.pbf -from 48.574,13.460 -to 48.567,13.432 -format gpx > route.gpx

# Which delivery zone is a coordinate in?
go run ./cmd territory lookup -layer delivery -lat 48.65 -lon 12.65 \
  -territories testdata/territory/delivery-zones.geojson

# Assign parcels (CSV with the columns parcel_id, lat, lon) to delivery zones
go run ./cmd dispatch assign -territories testdata/territory/delivery-zones.geojson \
  -input parcels.csv -output assignments.csv
```

The root Go package can also be used as a library for streaming OSM extraction
and routing; see [`example/main.go`](example/main.go).

## Configuration

### Server flags

| Flag | Default | Description |
| --- | --- | --- |
| `-pbf` | `region.osm.pbf` | OSM PBF for the graph, search and POIs |
| `-listen` | `:8080` | HTTP address |
| `-settings` | `settings.json` | Settings file (profile) |
| `-admin-token` | `$OSMMINI_ADMIN_TOKEN` | Bearer token for writing settings, offline-map builds, imports and confidential objects. Without it, settings can be changed without authentication |
| `-deployment-mode` | `single-user` | `browser-local`, `single-user` or `multi-user` |
| `-operators-file` | – | Operator tokens (required for `multi-user`) |
| `-operations-file` | `operations.json` | Delivery/maintenance log |
| `-confidential-objects-file` | `confidential-objects.json` | Confidential editor objects |
| `-tiles-dir` / `-tile-upstream` | `tiles-cache` / – | Cache and upstream for proxied raster tiles |
| `-tinytiles-dir` | `offline-tiles` | Directory for the offline map |
| `-tinytiles-max-memory-mb` | `768` | Memory limit when building the offline map; raise it for large regions |
| `-tinytiles-readers` | `4` | Concurrent readers of the offline map |
| `-tinytiles-reader-memory-mb` | `32` | Page cache per reader |
| `-tinytiles-tile-cache-mb` | `64` | Hot-tile cache (`-1` = off) |
| `-territories-dir` | `territories` | `*.geojson` territory layers (file name = layer name) |
| `-imported-layers-dir` | `imported-layers` | Layers created by GIS imports |
| `-geodata-tiles-dir` | `geodata-tiles` | Imported MBTiles/GeoTIFF map source |
| `-window` | – | Load only the window `minLat,maxLat,minLon,maxLon` from the PBF |
| `-window-buffer-m` | `0` | Buffer around `-window` in metres |
| `-enforce-window` | `false` | Reject requests outside `-window` |
| `-build-ch` | `false` | Experimental contraction hierarchies |

### Settings file

The settings file holds the routing defaults (`routing`: engine, objective,
turn penalties, vehicle height and weight), the map source (`tiles`), optional
AI endpoints (`ai`) and `default_highway_speeds` (km/h per `highway` type, as
overrides of the built-in values). Most of these values can also be changed in
the UI under **Einstellungen**. `allowed_highway_types` is applied only when the
graph is built and requires a restart.

The local assistant automatically detects [Ollama](https://ollama.com/)
(`localhost:11434`) or LM Studio (`localhost:1234`). You can also configure any
OpenAI-compatible endpoint with `ai.openai_base_url` and `ai.openai_api_key`.

### Make targets

```text
make help                  list all targets
make run                   start the server (PBF, SETTINGS, LISTEN, ADMIN_TOKEN)
make bayern / offline      start the Bavaria / offline profile (ADMIN_TOKEN required)
make build                 build bin/osmmini
make pbf-download          download a Geofabrik PBF (GEOFABRIK_URL, FORCE=1)
make maplibre-assets       download the pinned MapLibre assets and verify them
make offline-prep          pbf-download + maplibre-assets
make check                 Go tests, vet, build, JS syntax and JS tests
```

## Running in production

- **Protection:** set `-admin-token` (or `OSMMINI_ADMIN_TOKEN`). Without it,
  settings can be changed without authentication. `make bayern` and
  `make offline` refuse to start without a token.
- **TLS:** OSMmini serves plain HTTP. Put a reverse proxy (Caddy, nginx) in
  front of it for public access and distribute personal tokens securely.
- **Shutdown:** `SIGINT`/`SIGTERM` (Ctrl+C, `systemctl stop`, `docker stop`)
  shut the server down cleanly. Open requests get up to 10 s to finish, and
  the offline map is closed properly.
- **Tile sources:** `raster-direct` is loaded by the browser directly, while
  `raster` and `wms` go through the local `/tiles` cache. OSM standard tiles are
  deliberately `raster-direct` and are never proxied or cached; see the
  [OSM tile usage policy](https://operations.osmfoundation.org/policies/tiles/).
  For heavy traffic, use your own or a commercial tile provider.
- **Local data** (`operations.json`, `operators.json`, `fire-stations.json`,
  `confidential-objects.json`, `territories/`, `imported-layers/`, `*.poi.json`)
  is excluded by `.gitignore` and belongs in backups, not in the repository.

Example systemd unit:

```ini
[Unit]
Description=OSMmini
After=network.target

[Service]
WorkingDirectory=/opt/osmmini
ExecStart=/opt/osmmini/bin/osmmini -pbf /opt/osmmini/region.osm.pbf -listen 127.0.0.1:8080
Environment=OSMMINI_ADMIN_TOKEN=change-me
Restart=on-failure
User=osmmini

[Install]
WantedBy=multi-user.target
```

## Development

### Project structure

| Path | Contents |
| --- | --- |
| `extract.go`, `pbf_spatial_index.go` | Streaming PBF extraction and sidecar index |
| `router.go`, `types.go` | Routing graph, A*/Dijkstra, address search, maneuvers |
| `territory*.go`, `dispatch.go`, `imported_layers.go` | Territories, point-in-polygon, assignment |
| `cmd/main.go` | Server, HTTP API, settings, search |
| `cmd/route_export.go` | GPX/GeoJSON route export |
| `cmd/tsp.go`, `cmd/route_cache.go` | Trip optimisation, route cache |
| `cmd/tinytiles*.go`, `cmd/offline_labels.go` | Offline map, waterways, labels |
| `cmd/geo*.go`, `cmd/geodata*.go` | POI geo index, GIS imports |
| `cmd/web/` | Embedded UI (`index.html`, `app.js`, `style.css`, editor modules) |
| `cmd/web_test/` | JS regression tests (Node test runner, no dependencies) |
| `cmd/api/openapi.yaml` | API specification |
| `cmd/wasm`, `cmd/export-graph` | Browser routing tools (built separately) |
| `docs/` | Additional documentation |

### Checks

```bash
make check      # go test, go vet, build, node --check, node --test
make test-race  # Go race detector
make test-js    # JS tests only
```

`make check` needs Go and Node.js. The first run downloads the pinned MapLibre
assets (6.7.0) and verifies their checksums. The JS tests use a small DOM and
network harness and download nothing. CI runs `make check` on every pull
request.

### Performance notes and benchmarks

- **Routing:** search state grows with the visited part of the graph.
  `dijkstra-node` skips predecessors and path reconstruction when only the cost
  is needed. Cancelled requests stop before any search state is allocated.
- **Trips:** up to 16 stops use exact dynamic programming over subsets; 17–60
  stops use nearest neighbour followed by 2-opt (a heuristic, with no guarantee
  of the global optimum). Road costs are treated as directed and cached for each
  solve.
- **POI search:** text normalisation is a single pass without allocations for
  text that is already normalised. The POI cache (`cmd/poi_cache.go`, version 3)
  is streamed entity by entity and replaced atomically.
- **Geo index:** a grid with one representative point per POI provides fast
  viewport and radius queries. It covers the poles and the antimeridian, and it
  returns `503` while it is still loading.

```bash
go test . -run '^$' -bench 'BenchmarkShortRouteCost$' -benchmem
go test ./cmd -run '^$' -bench 'BenchmarkTSPExact12$' -benchmem
go test ./cmd -run '^$' -bench '^BenchmarkRouteCacheEviction$' -benchmem
go test ./cmd -run '^$' -bench '^BenchmarkGeoViewport100k$' -benchmem
go test ./cmd -run '^$' -bench 'BenchmarkPOINormalization$|BenchmarkPOISearch5000$' -benchmem
```

## Citation

If you use OSMmini in academic work, please cite it through the Zenodo DOI
[10.5281/zenodo.18929953](https://doi.org/10.5281/zenodo.18929953).
Map data © [OpenStreetMap](https://www.openstreetmap.org/copyright) contributors, ODbL.

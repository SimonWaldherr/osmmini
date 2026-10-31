# OSMmini

[![DOI](https://zenodo.org/badge/1116409323.svg)](https://doi.org/10.5281/zenodo.18929953)

OSMmini provides offline routing, search and maps from an OpenStreetMap PBF
extract. It includes a Go server with a browser UI and an embeddable
[Swift SDK for iPhone, iPad and macOS](apple/README.md).

OSMmini is an open-source project by [Karte.Bayern](https://karte.bayern/).
Use OSMmini with your own map data, or visit Karte.Bayern for a ready-to-use
map and route planner in the browser.

OSMmini's own source code is licensed under [AGPL-3.0-only](LICENSE). Bundled
third-party components and map data retain their own licences; see
[apple/NOTICE](apple/NOTICE) and the notices supplied with those components.

![OSMmini web UI](https://simonwaldherr.de/gh-pages/osmmini.png)

## Features

- A*/Dijkstra routing by distance, duration or economy, vehicle profiles,
  directions, and GPX/GeoJSON export.
- Trips with up to 60 stops, ordering dependencies and capacity constraints;
  exact optimisation for up to 16 stops.
- Address, street and POI search with [category aliases](docs/osm-categories.md).
- Offline vector maps, optional online basemaps, and imported map sources.
- [GIS workbench](docs/gis-workbench.md) with GeoJSON/KML/Shapefile import,
  attribute editing, filters, CSV joins and export.
- [Work planning](docs/work-planning.md), territory assignment, delivery and
  maintenance logs, and emergency-resource overlays.
- OSM draft editing with `.osc` export; the editor does not upload changes.

## Quick start

Requires Go 1.26.5+, `curl`, and an OSM PBF extract. Node.js is needed for browser
tests; building microMap assets also requires npm.

```sh
make offline-prep PBF_URL='https://download.geofabrik.de/europe/germany/bayern/niederbayern-latest.osm.pbf'
make run
```

Open <http://localhost:8080/>. Routing and search become available after the graph
loads. In **Einstellungen → Offline-Karte erzeugen**, build the local basemap
once; subsequent use needs no network connection.

Use `make run PBF=/path/to/region.osm.pbf` for an existing extract.
`go run ./cmd help` lists commands and flags.

`make clean` removes compiled binaries from `local/bin/` (or `BIN_DIR`).
Downloaded maps and prepared regions are retained.

`PBF_URL` also accepts extracts from other providers, including
[OpenStreetMap France](https://download.openstreetmap.fr/extracts/):

```sh
make offline-prep PBF=local/gibraltar.osm.pbf PBF_URL='https://download.openstreetmap.fr/extracts/europe/gibraltar-latest.osm.pbf'
make run PBF=local/gibraltar.osm.pbf
```

The default remains the Geofabrik Bavaria extract. Existing `GEOFABRIK_URL`
overrides remain supported; `PBF_URL` takes precedence when both are set.
Failed or cancelled PBF downloads leave the destination unchanged.
Downloads and asset preparation require network access; routing, search and the
prepared local basemap work offline afterward.

## Map profiles

| Profile | Command | Basemap |
| --- | --- | --- |
| Default | `make run` | Local tinyTiles map; other sources selectable in settings |
| Bavaria | `make bayern ADMIN_TOKEN=…` | BayernAtlas vector/WMTS |
| Offline | `make offline ADMIN_TOKEN=…` | Local tinyTiles only |

Routing, search and POIs cover the loaded PBF, independently of the basemap.
Offline maps are stored in `local/offline-tiles/basemap.ttiles`. Map settings control
colours, layers, labels and road widths without rebuilding the data.

The default renderer is [microMap](https://github.com/Karte-Bayern/microMap),
with Canvas 2D support. MapLibre GL is optional. Both use local assets; switch
renderers in **Einstellungen → Kartenanzeige**. Offline maps include roads,
buildings, water and landcover. Local companion layers add waterways and 2.5D
building geometry while the source PBF remains available. The building companion
omits multipolygon relations. microMap does not support terrain, hillshade or
globe projection.

## Web UI guide

Search for an address, POI or coordinate, or select a point on the map. Set route
endpoints, add stops, and enable **Reihenfolge optimieren** to optimise a trip.
Routes can be fitted, cleared or exported as GPX/GeoJSON.

The GIS tools support POI export, distance measurement and local layers.
OSM-Edit keeps drafts in the browser, supports undo/redo, and exports changes
for review in an OSM editor. Confidential objects stay on the server and require
an administrator token.

Delivery and maintenance logs support barcode entry, CSV export and queued
submission after connection loss. Storage follows `-deployment-mode`:

| Mode | Storage |
| --- | --- |
| `browser-local` | Browser storage |
| `single-user` | Server-side `local/operations.json` |
| `multi-user` | Server history with personal operator tokens |

For multi-user operation, configure [operators.example.json](config/operators.example.json)
and start with `-deployment-mode multi-user -operators-file local/operators.json`.

## HTTP API

The [OpenAPI specification](cmd/api/openapi.yaml) documents request and response
schemas. It is also served at `/api/v1/openapi.yaml`.

| Endpoint | Purpose |
| --- | --- |
| `GET /api/v1/health`, `/api/v1/status` | Health, version and graph status |
| `GET /api/v1/search?q=…` | Address, street and POI search |
| `POST /api/v1/route` | Route calculation |
| `POST /api/v1/trip/solve` | Multi-stop routing |
| `POST /api/v1/dispatch/plan` | Multi-resource work planning |
| `GET /api/v1/geo/pois` | POIs by viewport or radius |
| `POST /api/v1/geo/measure` | Polyline distance |
| `GET /api/v1/territories` | Territory layers |
| `GET/POST /api/v1/operations` | Delivery and maintenance log |
| `GET/PUT /api/v1/settings` | Read or update settings |
| `POST /api/v1/tinytiles/build` | Offline map preparation |

```sh
curl 'http://localhost:8080/api/v1/search?q=Passau&limit=5'

curl 'http://localhost:8080/api/v1/route?format=gpx' \
  -H 'Content-Type: application/json' \
  -d '{"from":{"lat":48.574,"lon":13.450},"to":{"lat":48.549,"lon":12.137}}' \
  -o local/route.gpx
```

Route and trip endpoints accept `format=gpx` or `format=geojson`. GeoJSON uses
`[lon, lat]`; request coordinate objects use named `lat` and `lon` fields.

## Command-line tools

Run `go run ./cmd <command> -h` for options.

| Command | Purpose |
| --- | --- |
| `route` | Calculate a route as JSON, GPX or GeoJSON |
| `territories load`, `territory lookup` | Load and query territories |
| `dispatch assign`, `dispatch manifests`, `dispatch plan` | Assignment and work planning |
| `geodata import` | Import GIS layers |
| `pbf-index`, `region-extract` | Index or extract regional PBF data |
| `version`, `help` | Build information and command reference |

```sh
go run ./cmd route -pbf local/region.osm.pbf \
  -from 48.574,13.450 -to 48.549,12.137 -format gpx > local/route.gpx
```

## Large PBF files

Prefer a regional extract to loading an entire country. `region-extract` needs
[osmium](https://osmcode.org/osmium-tool/) and preserves complete ways:

```sh
go run ./cmd region-extract -pbf local/germany-latest.osm.pbf \
  -bbox 11.7,47.8,14.3,49.3 -output local/regions/local.osm.pbf
```

`go run ./cmd pbf-index -pbf local/germany-latest.osm.pbf` creates a reusable block index.
The index is invalidated when its source changes.

## Configuration

[settings.json](config/settings.json) contains routing defaults, map sources, optional
AI endpoints and highway speeds. Most settings are editable in the UI.
Graph-import filters require a restart. Additional profiles are provided in
[settings.bayern.json](config/settings.bayern.json) and
[settings.tinytiles.json](config/settings.tinytiles.json).

| Option | Purpose |
| --- | --- |
| `-pbf`, `-settings`, `-listen` | Source extract, profile and HTTP address |
| `-admin-token` / `OSMMINI_ADMIN_TOKEN` | Protect administrative writes |
| `-deployment-mode`, `-operators-file` | Log storage and operator authentication |
| `-tinytiles-dir` | Offline map location |
| `-tinytiles-max-memory-mb` | Tile-build memory budget |
| `-tinytiles-readers`, `-tinytiles-reader-memory-mb` | Reader count and per-reader cache |
| `-territories-dir`, `-imported-layers-dir` | Local GIS data |

Cache and import budgets do not limit total process memory. Use
`go run ./cmd -h` for the complete option list and defaults.

## Running in production

- Set an administrator token; without one, settings writes are unauthenticated.
- Put a TLS reverse proxy in front of the HTTP server for public access.
- Back up local settings, operator files, logs, confidential objects and GIS data.
- Use local tiles for offline operation. Standard OpenStreetMap tiles are
  browser-direct and cannot be prefetched through the proxy; follow the
  [tile usage policy](https://operations.osmfoundation.org/policies/tiles/).

`SIGINT` and `SIGTERM` shut down the server and close the offline map cleanly.

## Native Apple SDK and offline demo

```sh
make build-apple
open apple/Demo/OSMminiDemo.xcodeproj
```

The [Apple guide](apple/README.md) covers the Swift API, native application,
signing and portable region packages. To prepare a package without Xcode:

```sh
CGO_ENABLED=0 go run ./cmd/prepare-region \
  -pbf local/region.osm.pbf -output local/regions/local
```

## Routing and data limits

The importer applies coarse road-access filters and supports one-way roads and
roundabouts. Mode-specific access exceptions, conditional restrictions and OSM
turn-restriction relations are not fully implemented. Angle penalties do not
replace turn restrictions. Routing stays within one loaded region; continuous
navigation and automatic map updates are not provided.

Prepared regions retain their imported data. Reimport into a new destination to
apply changed import rules. Import timestamps describe preparation time;
source hashes identify the input snapshot, not its freshness.

## Development

```sh
make check         # Go tests, vet, build and browser checks
make test-race     # Go race detector
make check-mobile  # Offline library without SQLite/cgo
make test-js       # Browser tests
```

`make check` requires Go and Node.js and downloads pinned MapLibre assets if
missing. Browser tests use local fixtures. Benchmarks are included in the Go
and Swift test suites; see the Apple guide for opt-in real-region checks.

The repository uses these top-level directories:

| Directory | Contents |
| --- | --- |
| `core/` | Go routing and import library |
| `cmd/` | Server, browser UI and command-line tools |
| `mobile/` | Offline region library |
| `apple/`, `bindings/` | Swift SDK, native apps and C bridge |
| `config/` | Server profiles and example operator configuration |
| `docs/`, `testdata/` | Documentation and fixtures |
| `scripts/`, `example/` | Build scripts and Go example |
| `local/` | Ignored downloads, caches, builds and private local data |

The Go module remains `simonwaldherr.de/go/osmmini`; import the library as
`simonwaldherr.de/go/osmmini/core` and the offline library as
`simonwaldherr.de/go/osmmini/mobile`. The root Swift package remains
available. `make check-layout` enforces a maximum of twenty root entries, including
hidden files and directories such as `.git`.

## Citation and map data

For academic use, cite [10.5281/zenodo.18929953](https://doi.org/10.5281/zenodo.18929953).
Map data © [OpenStreetMap contributors](https://www.openstreetmap.org/copyright),
licensed under ODbL. Preserve attribution when distributing derived data.

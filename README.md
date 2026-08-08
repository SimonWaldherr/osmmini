# OSMmini

[![DOI](https://zenodo.org/badge/1116409323.svg)](https://doi.org/10.5281/zenodo.18929953)

Lightweight, open source offline routing server and web UI using OSM PBF
extracts. OSMmini is the small sibling of [Karte.Bayern](https://karte.bayern),
a closed-source navigation product for Bavaria by the same author.

![](https://simonwaldherr.de/gh-pages/osmmini.png)

Features

- Build a routing graph from an OSM PBF and serve offline routes via HTTP API
- Multiple routing engines: `astar`, `dijkstra`, `dijkstra-node` (node-only Dijkstra)
- Web UI (Leaflet) in `cmd/web` with search, trip solver, settings and turn-by-turn maneuvers
- Global raster map profile plus official BayernAtlas vector and WMTS presets
- Local tinyTiles vector profile for an optional fully offline basemap
- Tile proxy with a source-namespaced local cache for proxied sources

Requirements

- Go 1.26.5+
- An OSM PBF extract (e.g. `region.osm.pbf`)

Quick start

1. Build or run the server (example):

```bash
go run ./cmd -pbf region.osm.pbf
```

2. Open the web UI: http://localhost:8080/

The included [`settings.json`](settings.json) is the global profile. It loads
the standard OpenStreetMap raster map directly in the browser, so it works
inside and outside Bavaria without routing public OSM tiles through this
server. The map is global; routing, address search and local POIs are limited
to the PBF extract you loaded.

## Bavaria profile

For a Bavaria-focused deployment, use the included
[`settings.bayern.json`](settings.bayern.json). It uses the official Bayern
vector map and automatically falls back to official Bayern WMTS when WebGL or
MapLibre is unavailable:

```bash
OSMMINI_ADMIN_TOKEN='choose-a-secret' go run ./cmd \
  -pbf bayern.osm.pbf \
  -settings settings.bayern.json
```

This visual basemap is regional. Keep the global profile or select another
global preset when users should browse outside Bavaria.

## Fully offline tinyTiles profile

[`settings.tinytiles.json`](settings.tinytiles.json) activates osmmini's
integrated tinyTiles endpoint. The built-in tinyTiles generator creates local
vector tiles for roads, buildings, water, forest and agricultural land;
osmmini serves both the tiles and matching local MapLibre style.

For a truly offline browser renderer, vendor MapLibre locally once while
online (see "Frontend / assets" below), then use the **Offline-Karte
erzeugen** action in the web UI to build the `.ttiles` artifact from the PBF
loaded by osmmini. After preparation, the map UI needs no network connection:

```bash
# Vendor MapLibre locally once, while online, for a fully offline UI:
# make maplibre-assets

OSMMINI_ADMIN_TOKEN='choose-a-secret' go run ./cmd \
  -pbf region.osm.pbf \
  -settings settings.tinytiles.json \
  -listen :8080
```

Enter the same value once in **Einstellungen → Administrationsschutz**, then
start **Offline-Karte erzeugen**. The resulting artifact is kept in
`offline-tiles/basemap.ttiles` and is restored automatically after a restart.

The source is also available as **tinyTiles lokal (offline)** in the
map-source selector. Its initial minimal renderer covers closed OSM ways;
complex multipolygon areas still require a richer tileset generator.

## Make targets

```bash
make help
make run PBF=region.osm.pbf
make bayern BAYERN_PBF=bayern.osm.pbf ADMIN_TOKEN='choose-a-secret'
make offline PBF=region.osm.pbf ADMIN_TOKEN='choose-a-secret'
make maplibre-assets
make check
```

By default the server runs with settings updates open (no token needed) so it
works out of the box. `make bayern` and `make offline` deliberately still
require an admin token as a safety net for these more public-facing profiles;
set `-admin-token` (or `OSMMINI_ADMIN_TOKEN`) yourself whenever a deployment
should require a token before accepting settings writes.

Flags

- `-pbf`: Path to OSM PBF (default `region.osm.pbf`)
- `-listen`: HTTP listen address (default `:8080`)
- `-tiles-dir`: Tile cache directory
- `-tile-upstream`: Upstream tile URL template
- `-tinytiles-dir`: Directory for the generated local `.ttiles` artifact
- `-tinytiles-max-memory-mb`: Maximum memory (MB) tinyTiles may use while building a `.ttiles` artifact (default `768`); raise this for larger PBF regions
- `-build-ch`: Build experimental Contraction Hierarchies after graph load (default false)
- `-admin-token`: Optional bearer token (or `OSMMINI_ADMIN_TOKEN`); when set, it is required for settings updates. When unset, settings updates are unauthenticated.

## Tile sources and production use

`raster-direct` sources are fetched directly by the browser; `raster` and
`wms` sources use the local `/tiles` cache proxy. The OpenStreetMap standard
tiles are intentionally configured as `raster-direct`, because their public
service must not be used as a general server-side tile proxy or offline tile
cache. For a public high-traffic service, configure a suitable commercial or
self-hosted global provider and follow its terms. See the
[OpenStreetMap tile usage policy](https://operations.osmfoundation.org/policies/tiles/).

Frontend / assets

- The UI lives in `cmd/web`; own JS/CSS is committed, third-party libraries (Leaflet, MapLibre GL) are not.
- Leaflet and MapLibre load from a CDN by default. For the fully offline
  tinyTiles profile, run `make maplibre-assets` once (while online) to vendor
  a local, checksum-verified copy under `cmd/web/static`; osmmini prefers
  that local copy over the CDN whenever it is present.

# OSMmini for Apple platforms

A Swift 6 offline engine and SwiftUI map for iPhone, iPad and native macOS.
It shares the Go routing core with the server and needs no local HTTP server,
web view or SQLite dependency.

OSMmini is a project by [Karte.Bayern](https://karte.bayern/).

## Build and run

Requires macOS, full Xcode and Go 1.26.5+. Deployment targets are iOS/iPadOS 17
and macOS 14.

From the repository root:

```sh
make build-apple
open apple/Demo/OSMminiDemo.xcodeproj
```

Choose the **OSMminiDemo** scheme and an iPhone, iPad, Simulator or Mac target.
The generated XCFramework contains iOS arm64, Simulator arm64/x86_64 and macOS
arm64/x86_64. Build it before resolving the local Swift package.

For device signing, create the ignored local configuration from the template
if it does not exist, then replace `YOUR_TEAM_ID`:

```sh
cp apple/Demo/Configuration/Signing.local.xcconfig.example apple/Demo/Configuration/Signing.local.xcconfig
```

Edit an existing local configuration directly. Simulator and unsigned builds
need no signing team.

For macOS-only development:

```sh
make build-apple-macos
swift run --package-path apple OSMminiDemoCLI
swift test --package-path apple
```

This replaces the framework with Mac slices only. Run `make build-apple` again
before an iOS build. No Go installation is needed on the destination device.

## Demo application

1. Choose **Geofabrik** or **OpenStreetMap France** in the region source picker,
   then download a region; alternatively, select a local `.osm.pbf` file.
2. Wait for graph, search and map preparation.
3. Search for a place or select a map position, then choose **Als Start** or
   **Route hierher**. The latter requests your location when no start is set.
   Plan a route by distance or duration and export GPX/GeoJSON.

Each source has its own cached catalog, available offline after loading. The
OpenStreetMap France catalog uses the polygon list from its
[official extract repository](https://github.com/osm-fr/osm-extract-replication)
and downloads PBFs from [download.openstreetmap.fr](https://download.openstreetmap.fr/extracts/).
Region coverage and names differ between providers; changing sources does not
replace an installed region. Catalog refresh and PBF downloads require network
access; prepared regions work offline.

Choose **Karte hinzufügen** in the sidebar to download a region, create a custom
area or import a local PBF. Select an **Importprofil** before starting:

| Profile | Points of interest |
| --- | --- |
| **Alltag** (default) | Shops, food, leisure, health and public facilities; technical emergency objects are omitted. |
| **Feuerwehr** | Hydrants, water supply points, emergency access points, fire/ambulance stations, police, hospitals, fuel and related facilities. |
| **Outdoor** | Drinking water, toilets, shelters, campsites, viewpoints, guideposts, peaks, passes, springs and selected rescue points. |
| **Radfahren** | Bicycle shops, repair stations, rentals and parking, plus water, rest stops, food and campsites. |
| **Reisen** | Accommodation, sights, museums, stations, stops, ferries, airports, parking, fuel and travel supplies. |
| **Alle POIs** | All supported POI categories, including technical emergency objects. |

The profile filters the local search and POI map data. Roads, addresses,
settlements, buildings, water and land cover stay available. It does not reduce
the source PBF download or change vehicle routing rules. A profile change
requires a new import; installed regions remain available with their original
content. The selected profile is shown beside each installed region; its menu
opens **Kartendetails** with the import date and any reimport advice. Downloads,
local file copies and interrupted imports retain the profile chosen at start.

Native POI extraction reads nodes and ways tagged with `amenity`, `shop`,
`tourism`, `leisure`, `office`, `emergency` or `place`, plus
selected `natural`, `railway`, `public_transport`, `aeroway` and `highway`
objects for the extended profiles. It does not import every OSM object or the
web application's complete category catalog. Named streets and address records
are indexed separately. Feuerwehr objects expose available OSM references,
hydrant types, diameters, pressures, water sources and volumes as source text;
missing values are left empty and units are not inferred.

The current import rules are recorded as `import_revision: 8`. Earlier regions
remain readable; newly prepared variants require both the selected profile and
current rules to match before reusing a region or an unfinished import.

Choose **KML importieren …** under **Kartenebenen** to display a local `.kml`
file. Points, lines, polygons with holes and MultiGeometry are supported. Layers
can be fitted, hidden, shown or removed separately and are restored on launch.
The original file remains untouched. Unreadable layers can be removed from the
list to free a slot without deleting the retained copy. KML also works without
an installed basemap; search and road routing still require an offline OSM region.

Imports stay on the device and are limited to eight layers, each up to 10 MiB,
5,000 features and 100,000 coordinate positions. The map uses a shared purple
style in two dimensions. KMZ archives, remote NetworkLinks, imagery and custom
KML styles are not loaded. Imported geometry does not add roads to the routing
graph. KML files can also be opened directly with the app from Files or Finder.

For a custom area, choose **Karte hinzufügen → Umkreis herunterladen**.
Tap **Standort ermitteln**, then select a radius of **30, 60, 90, 120 or 150 km**.
The app captures the location once and stops that location session. Starting
**Ausschnitt erstellen und laden** sends the circular boundary to SliceOSM;
the resulting PBF is downloaded and prepared for offline use. The sheet shows
the location accuracy and the radius separately from the diameter.

SliceOSM runs on demand, so preparation can take time and large or densely
populated areas may exceed service or device limits. Existing jobs are reused
for retries within 24 hours. Cancelling stops polling or downloading locally;
the service has no API for cancelling its server-side job. Use a smaller radius
if the service or local importer rejects an area. Regions crossing a pole or
the date line are currently rejected before submission.

Save places with the star and find them again in search, including under your
own names. Saved places and the last 20 selected places stay local and survive
region replacement or deletion. GPS updates are not added to this history.
Places outside the current region are marked; open a matching offline map to
use them. Remove individual entries or clear recent selections from the search
sheet.

Tap a named road or a visible POI marker to open its details, save it or use it
as a route endpoint. Road selections use the nearest point on the visible line;
empty areas still select a free map point. POI markers appear from display zoom
14; their detail tiles follow the display zoom independently of the basemap,
with at most 512 POIs per detail tile. Details include address, opening-hours
text, phone and website when retained by the import. Existing regions gain POI
markers from their search catalog without the source PBF; additional source
tags require a new import. Opening hours are stored OSM text, not a live
open/closed status. Website and OSM links open only when selected and require
a network connection.

POIs use category colors, with category symbols from display zoom 16 and
collision-checked names from zoom 17. Nearby markers are spaced apart in screen
coordinates. Crowded markers show a count and expose every member through
`MapSelection.alternatives`, retaining each place's own coordinate and details.
The demo offers a searchable choice list, including for coincident POIs at the
maximum zoom. Drawing and selection share the same group and label layout.
The place inspector shows the address above route/save actions, expands
opening-hours details on demand, displays the website's host and can share a
place as text.
Selecting another place resets the inspector's scroll position and disclosures.
On narrow screens, a compact inspector preserves map space and opens full
details in a separate sheet with route/save actions fixed above the safe area.

Published regions work without the source PBF or a network connection. The app
can reopen the last region, retry interrupted imports, remove installed regions
and clean up downloaded PBFs separately. Completed graph/search phases resume;
an interrupted map build restarts that phase. Background completion while the
app is suspended is not guaranteed.

During native region preparation, the demo pauses the map and releases the
previous engine and prepared drawing caches. Downloads and local PBF copies
keep the current map available. If preparation fails or is cancelled, the app
reopens the previous region and retains the planned route and its endpoints.
Importing a matching PBF/profile reuses its prepared region; an already open
matching region keeps its engine and planned route.

The map displays local vector geometry, road/place labels, routes and location
accuracy. It supports panning, continuous zooming and fitting the region or route.
On macOS, scroll over the map or pinch the trackpad to zoom around the pointer.
On iOS, pinch to zoom around the gesture's starting point. The zoom buttons and
double tap use short, interruptible transitions; Reduce Motion disables them.
Existing tiles cover missing geometry while a new detail level loads, within
the same cache budget. Enlarging basemap tiles adds no road or building detail.
Location access starts only after an explicit action and stops in the background.

The location arrow on the map zooms to the current GPS fix at street level.
When no fresh fix exists, the demo requests location access and focuses the map
once a fix arrives. Repeated taps recenter without changing route endpoints.
Outside the installed region, the position remains visible with a prompt to
open a matching offline map. Embedders can supply `onLocate`/`isLocating` to
connect their location provider; with only `userLocation`, the renderer's
location button focuses that coordinate without requesting permissions.

Adaptive split views and map details follow the available window size,
including iPad multitasking and resizable displays. macOS uses native windows
and menus, without Catalyst. Shortcuts: **⌘O** imports, **⌘F** searches and
**⇧⌘R** toggles route planning.

## Swift integration

Add this repository or `apple/` as a local Swift package in Xcode and select the
**OSMmini** library product. Given a readable `sourceURL` and a new
`regionDirectory`:

```swift
import OSMmini

let options = ImportOptions(
    pbf: sourceURL, destination: regionDirectory, name: "Local region")
try await OfflineEngine.importPBF(options) { progress in
    Task { @MainActor in print(progress.phase, progress.fraction) }
}

let engine = try await OfflineEngine.open(regionDirectory)
let places = try await engine.search("Passau", limit: 20)
var routing = RouteOptions()
routing.objective = .duration
let route = try await engine.route(RouteRequest(
    from: Coordinate(lat: 48.574, lon: 13.450),
    to: Coordinate(lat: 48.549, lon: 12.137), options: routing))
let gpx = route.gpx()
let geoJSON = try route.geoJSON()
await engine.close()
```

Use `OfflineMap(engine: engine, route: route)` in SwiftUI, or read vector tiles
with `engine.tile(z:x:y:)` for another renderer. Catalog and download APIs are
independent of the offline engine; engine queries use no network.

```swift
let extracts = OSMExtracts(directory: downloads, source: .openStreetMapFrance)
let regions = try await extracts.regions()
// Download an OSMExtractRegion selected from the catalog.
let pbf = try await extracts.download(region)
```

Use `.geofabrik` for the other source. `Geofabrik` and `GeofabrikRegion` remain
compatibility aliases; the default source is Geofabrik.

For on-demand extracts, use `SliceOSM(directory:)` and
`download(center:radiusKM:name:progress:)`. `SliceOSMRegion` validates the five
supported radii and supplies a closed geodesic boundary. Creating a client does
not start a request; only an explicit download submits or resumes a job.

Use `onFeatureSelect` for road/POI selection, including snapped coordinates and
optional source metadata:

```swift
OfflineMap(engine: engine, onFeatureSelect: { selection in
    // A nil feature denotes a free map point.
    print(selection.feature?.name ?? "Map point", selection.coordinate)
})
```

The existing `onSelect` callback continues to return the tapped coordinate.
When both callbacks are set, `onFeatureSelect` takes precedence.

Native work runs off the main actor. Progress callbacks run on a worker thread;
UI changes belong on `MainActor`. Task cancellation cancels the native operation.
`close()` is idempotent and waits for active native work. Callers retain access
to security-scoped source URLs during import.

The bridge contains one statically linked Go runtime. Applications embedding
another Go framework must consolidate native entry points into one archive.

## Portable regions

Prepare a region on a server or Mac, then copy the entire directory into the
application's readable sandbox:

```sh
CGO_ENABLED=0 go run ./cmd/prepare-region \
  -pbf local/region.osm.pbf -output local/regions/local -name "Local region"
```

| File | Contents |
| --- | --- |
| `manifest.json` | Version, bounds, source hash and file checksums |
| `graph.bin` | Memory-mapped routing graph with shared road-chain storage |
| `routing.alt` | Memory-mapped ALT lower bounds for distance and duration |
| `search.jsonl`, `search.jsonl.idx` | Compressed records and disk-backed prefix index |
| `places.jsonl` | Settlement catalog for faster opening in new imports |
| `.poi-v1.idx` (optional) | Locally derived spatial index referencing search records |
| `basemap.ttiles/` | Local vector-map artifact |
| `NOTICE.txt` | Map-data attribution and licence links |

New imports use region format 4; formats 1–3 remain readable. The settlement
catalog is optional for older regions and validated when declared by the
manifest. Original OSM IDs, coordinates, directed connectivity and road geometry
are preserved by chain packing.

Published map files are immutable. Reimport into a new directory to update
data or obtain the current storage format. New imports prepare settlement and
spatial POI indexes while writing search records. Older regions can build the
derived POI index on first open; later opens reuse it. The index is bound to the
search-data checksum and does not replace source files. Read-only regions use
a temporary index when none is available. In writable regions,
interrupted index builds are cleaned up on the next open. Incompatible pending
imports need a new workspace. Keep `NOTICE.txt` with distributed regions.

New imports prepare two directed landmarks per metric using conservative
16-bit cost intervals. Older regions remain usable without this optional
index. Emergency and territory-cost queries use a zero heuristic when their
costs cannot be bounded by the prepared metrics.

The default import cap is eight million referenced road nodes; change it with
`-max-import-nodes`. Tile zooms default to 5–14. Node caps and tile-cache budgets
are not total process-memory limits. Importing large extracts still requires
memory for intermediate map geometry.

Search import spills way-node references and coordinates to private temporary
files instead of retaining region-sized arrays on the Go heap. This requires
additional temporary disk space; completion and cancellation remove the files,
and resumed imports discard abandoned scratch data. The upstream tinyTiles
geometry generator still retains region-wide structures before artifact
writing, so `TileMemoryBytes` is not a cap on that generation phase.

## Rendering and scope

The SwiftUI Canvas renderer uses prepared vector paths, visible-feature culling,
cached label priorities and collision checks. Up to four tile jobs run at once;
completed tiles appear independently. Geometry is retained for the current
viewport, with a maximum of 128 cached tiles. Long routes use prepared chunks.

The SDK operates on one region at a time. It does not provide cross-region
routing, continuous turn guidance or automatic data updates. Routing shares the
[core import limitations](../README.md#routing-and-data-limits). Native search
indexes node/way addresses and POIs with prefix/AND matching and a small German
category-alias set; exact settlement names take priority.

## Validation

```sh
make check-mobile
make test-apple
```

`make test-apple` builds a macOS-only framework. For an existing Niederbayern
region, run the optional integration check without downloading data:

```sh
OSMMINI_REAL_REGION=/path/to/niederbayern \
  swift test --package-path apple -c release --filter niederbayernRealRegionIntegration
```

It checks search, routing and local map geometry. Optional CPU benchmarks use
`OSMMINI_MAP_BENCHMARK=1` or `OSMMINI_BENCHMARKS=1`; device launch time, memory
and frame rate require device profiling.

## Distribution

```sh
make build-apple
scripts/package-apple.sh /tmp/OSMmini-Apple.zip
```

The ZIP contains a local Swift package and the framework. Consumers do not need
Go. For remote SwiftPM releases, use
[`prepare-apple-release.sh`](../scripts/prepare-apple-release.sh) to prepare the
framework asset, checksum and matching manifest before publishing them together.

See [LICENSE](LICENSE) and [NOTICE](NOTICE). Software and map-data licences are
separate.

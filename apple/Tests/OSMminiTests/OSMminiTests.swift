import Foundation
import Testing

@testable import OSMmini

@Test func offlineRegionWithoutPBF() async throws {
  let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
  try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
  defer { try? FileManager.default.removeItem(at: directory) }
  let source = directory.appendingPathComponent("source.osm.pbf")
  try FileManager.default.copyItem(
    at: #require(
      Bundle.module.url(forResource: "region", withExtension: "osm.pbf", subdirectory: "Fixtures")),
    to: source)
  let destination = directory.appendingPathComponent("region")
  var options = ImportOptions(pbf: source, destination: destination, name: "Fixture")
  options.minZoom = 14
  options.maxZoom = 14
  options.tileMemoryBytes = 64 * 1024 * 1024
  let manifest = try await OfflineEngine.importPBF(options)
  #expect(manifest.format == RegionManifest.currentFormat)
  #expect(manifest.nodes == 3)
  let notice = try String(
    contentsOf: destination.appendingPathComponent("NOTICE.txt"), encoding: .utf8)
  #expect(notice.contains("OpenStreetMap contributors"))
  #expect(notice.contains("https://opendatacommons.org/licenses/odbl/1-0/"))
  try FileManager.default.removeItem(at: source)
  let engine = try await OfflineEngine.open(destination)
  let results = try await engine.search("cafe")
  #expect(results.contains { $0.label.contains("Café") })
  let decomposedResults = try await engine.search("Cafe\u{301}")
  #expect(decomposedResults.map(\.identity) == results.map(\.identity))
  let cafe = try #require(results.first { $0.category == "cafe" })
  #expect(cafe.details?.address?.isEmpty == false)
  let route = try await engine.route(
    RouteRequest(
      from: Coordinate(lat: 48.001, lon: 12.001), to: Coordinate(lat: 48.003, lon: 12.003)))
  #expect(route.path.count == 3)
  #expect(route.distanceM > 0)
  #expect(!route.gpx().isEmpty)
  let geometry = try JSONSerialization.jsonObject(with: route.geoJSON()) as? [String: Any]
  #expect(geometry?["type"] as? String == "Feature")
  let x = Int((12.002 + 180) / 360 * 16384)
  let y = Int((1 - asinh(tan(48.002 * .pi / 180)) / .pi) / 2 * 16384)
  let tile = try await engine.tile(z: 14, x: x, y: y)
  #expect(!tile.features.isEmpty)
  let poi = try #require(tile.features.first { $0.sourceIdentity == cafe.identity })
  #expect(poi.layer == "poi" && poi.kind == 1)
  #expect(poi.name == cafe.label && poi.class == cafe.category)
  #expect(poi.details == cafe.details)
  // The map now overlaps native reads and path preparation. Exercise that
  // pipeline against the real C/Go engine, including adjacent empty tiles.
  let loader = await OfflineMapTileLoader()
  let keys = [-1, 0, 1].flatMap { dx in
    [-1, 0, 1].map { dy in MapTileKey(z: 14, x: x + dx, y: y + dy) }
  }
  await loader.load(owner: ObjectIdentifier(engine), keys: keys) { key in
    try await PreparedMapTile.prepare(engine.tile(z: key.z, x: key.x, y: key.y))
  }
  let loadedCount = await loader.cache.tiles.count
  let failures = await loader.failures(owner: ObjectIdentifier(engine), keys: keys)
  #expect(loadedCount == keys.count)
  #expect(failures == 0)
  await engine.close()
  do {
    _ = try await engine.search("test")
    Issue.record("closed engine accepted query")
  } catch {}
}

@Test func geofabrikCatalogValidation() throws {
  let data = Data(
    #"{"type":"FeatureCollection","features":[{"properties":{"id":"niederbayern","parent":"bayern","name":"Niederbayern","urls":{"pbf":"https://download.geofabrik.de/europe/germany/bayern/niederbayern-latest.osm.pbf"}}},{"properties":{"id":"bad","name":"Bad","urls":{"pbf":"https://example.com/file.osm.pbf"}}}]}"#
      .utf8)
  let regions = try Geofabrik.decodeCatalog(data)
  #expect(regions.count == 1)
  #expect(regions.first?.parent == "bayern")
}

@Test func cancelledNativeCallDoesNotPublishRegion() async throws {
  let task = Task {
    try Task.checkCancellation()
    return try await OfflineEngine.open(URL(fileURLWithPath: "/does-not-exist"))
  }
  task.cancel()
  do {
    _ = try await task.value
    Issue.record("cancelled opening succeeded")
  } catch {}
}

@Test func geofabrikCacheSeparatesEqualBasenames() throws {
  let europe = try #require(
    URL(string: "https://download.geofabrik.de/europe/georgia-latest.osm.pbf"))
  let us = try #require(
    URL(string: "https://download.geofabrik.de/north-america/us/georgia-latest.osm.pbf"))
  #expect(europe.lastPathComponent == us.lastPathComponent)
  #expect(Geofabrik.cacheFilename(europe) != Geofabrik.cacheFilename(us))
}

@Test func mercatorRoundTripPreservesSelectedCoordinates() {
  for zoom in [0, 5, 14, 22] {
    for latitude in [-85.0511, -48, 0, 48.002, 85.0511] {
      for longitude in [-180, -179.999, -12, 0, 12.002, 179.999] {
        let source = Coordinate(lat: latitude, lon: longitude)
        let selected = WebMercator.coordinate(WebMercator.world(source, zoom: zoom), zoom: zoom)
        #expect(abs(selected.lat - latitude) < 1e-8)
        #expect(abs(selected.lon - longitude) < 1e-8)
      }
    }
  }
}

@Test func mercatorWrapsTheDateLineAndClampsThePoles() {
  let zoom = 5
  let scale = exp2(Double(zoom)) * 256
  for (worldX, longitude) in [
    (-scale, -180.0), (-1.0, 179.9560546875),
    (0.0, -180.0), (scale / 2, 0.0),
    (scale, -180.0), (scale + 1, -179.9560546875),
    (2 * scale, -180.0),
  ] {
    let selected = WebMercator.coordinate(CGPoint(x: worldX, y: scale / 2), zoom: zoom)
    #expect(abs(selected.lon - longitude) < 1e-8)
    #expect(abs(selected.lat) < 1e-8)
  }
  for latitude in [-90.0, 90.0] {
    let selected = WebMercator.coordinate(
      WebMercator.world(Coordinate(lat: latitude, lon: 12), zoom: zoom), zoom: zoom)
    #expect(abs(selected.lat - (latitude < 0 ? -85.0511 : 85.0511)) < 1e-8)
  }
  let origin = WebMercator.world(Coordinate(lat: 48, lon: 12), zoom: zoom)
  let east = WebMercator.coordinate(CGPoint(x: origin.x + 128, y: origin.y), zoom: zoom)
  #expect(abs(east.lon - 17.625) < 1e-8)
}

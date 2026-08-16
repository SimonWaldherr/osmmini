// Optimized App JS with preloaded settings

// Load settings from inline script (server-rendered)
let preloadedSettings = null;
try {
  const settingsEl = document.getElementById('initialSettings');
  if (settingsEl) {
    const parsed = JSON.parse(settingsEl.textContent);
    // Accept pages served by older server binaries too: they embedded a
    // pre-marshaled JSON string, which html/template encoded once more.
    preloadedSettings = typeof parsed === 'string' ? JSON.parse(parsed) : parsed;
  }
} catch (e) {
  console.warn('Failed to load preloaded settings:', e);
}

// Sync the has-value class on an input's .input-clear-wrap parent so the
// inline clear button shows/hides correctly after programmatic value changes.
function syncInputClearState(inputId) {
  const el = document.getElementById(inputId);
  if (!el) return;
  el.closest('.input-clear-wrap')?.classList.toggle('has-value', !!el.value);
}

const map = new maplibregl.Map({
  container: 'map',
  style: { version: 8, sources: {}, layers: [] },
  center: [12.7, 48.7],
  zoom: 10,
  // MapLibre adds a default AttributionControl unless this is disabled; we
  // add our own explicitly below (so it's easy to find/adjust), which would
  // otherwise render twice.
  attributionControl: false,
});
map.addControl(new maplibregl.NavigationControl(), 'top-left');
map.addControl(new maplibregl.AttributionControl());
// map.once('style.load')/isStyleLoaded() can both be satisfied a tick before
// addSource/addLayer are actually safe to call on the *very first* style
// (an inline object, not a fetched URL) — 'load' fires exactly once, only
// after the map has genuinely finished initializing, and is a more reliable
// gate for that first call specifically. Later style swaps use
// waitForStyleReady() below instead, since 'load' never fires again.
const mapInitialLoad = new Promise((resolve) => map.once('load', resolve));
let currentTileLayer = null; // { kind: 'raster', sourceId } | { kind: 'vector', styleURL } | null
let baseLayerKind = null; // 'raster' | 'vector' | null (mirrors currentTileLayer.kind, tracked separately since currentTileLayer is cleared on style resets)
let tileLayerGeneration = 0;
let userLocationMarker = null;
let userLocation = null; // {lat, lon} from browser geolocation (explicit user permission)
let searchResultMarkers = []; // ad-hoc markers from AI actions (highlight_poi/show_info), not clustered
let searchClusterRenderedMarkers = []; // currently-rendered cluster-bubble + leaf markers for the last showSearchResultsOnMap() call
let lastSearchResults = []; // the full result list backing the cluster source, indexed by feature.properties.__idx
// tinyTiles deliberately has no MapLibre glyph dependency (the offline style
// has no `glyphs` config, so native symbol/text layers aren't an option).
// Text labels for the local map are therefore rendered as small MapLibre DOM
// marker overlays from the loaded PBF, tracked manually since MapLibre has
// no Leaflet-style layer-group container to add/remove them as a unit.
let offlineLabelMarkers = [];
let offlineLabelsEnabled = false;
let offlineLabelsTimer = null;
let offlineLabelsRequest = null;

// Switching the base tile/vector layer goes through map.setStyle(), which
// replaces the whole style and silently discards any sources/layers added
// outside of it (route line, markers-as-GeoJSON, territory overlay, ...).
// Later migration phases register a callback here to re-add their data after
// every successful base-layer switch instead of each having to special-case
// setStyle's wipe-and-rebuild behavior themselves.
const mapLayerRehydrateHooks = [];
function registerMapLayerRehydrate(fn) { mapLayerRehydrateHooks.push(fn); }
function rehydrateMapLayers() {
  mapLayerRehydrateHooks.forEach((fn) => {
    try { fn(); } catch (e) { console.warn('map layer rehydrate failed', e); }
  });
}
const SEARCH_RESULTS_RENDER_LIMIT = 320;
const SEARCH_CLUSTER_RENDER_CAP = 2200;
const SEARCH_COORD_PRECISION = 6;

function normalizeLatLon(lat, lon) {
  const parsedLat = typeof lat === 'number' ? lat : Number(lat);
  const parsedLon = typeof lon === 'number' ? lon : Number(lon);
  if (!Number.isFinite(parsedLat) || !Number.isFinite(parsedLon)) return null;
  return { lat: parsedLat, lon: parsedLon };
}

function removeMarkers(markers) {
  markers.forEach((marker) => {
    try { marker.remove(); } catch (_) {}
  });
  markers.length = 0;
}

function removeFromMarkerList(marker, list) {
  const idx = list.indexOf(marker);
  if (idx >= 0) list.splice(idx, 1);
}

function removeSearchResultMarker(marker) {
  if (!marker) return;
  try { marker.remove(); } catch (_) {}
  removeFromMarkerList(marker, searchClusterRenderedMarkers);
  removeFromMarkerList(marker, searchResultMarkers);
}

function markerElement(className, text, title) {
  const el = document.createElement('div');
  if (className) el.className = className;
  if (text != null) el.textContent = text;
  if (title) el.title = title;
  return el;
}

function normalizeSearchResult(item) {
  if (!item) return null;
  const point = normalizeLatLon(item.lat, item.lon);
  if (!point) return null;
  return { ...item, lat: point.lat, lon: point.lon };
}

function formatLatLon(lat, lon, precision = SEARCH_COORD_PRECISION) {
  const point = normalizeLatLon(lat, lon);
  if (!point) return '';
  return `${point.lat.toFixed(precision)},${point.lon.toFixed(precision)}`;
}

function supportsWebGL() {
  try {
    const canvas = document.createElement('canvas');
    return !!(window.WebGLRenderingContext &&
      (canvas.getContext('webgl') || canvas.getContext('experimental-webgl')));
  } catch (_) {
    return false;
  }
}

// Legacy Bayern vector configurations occasionally only contain a style URL.
// The official vector service has a matching WMTS base map, so use it directly
// as a last-resort browser fallback while the server migrates the configuration
// and resumes proxying it on the next request/restart.
function bayernRasterFallback(tiles) {
  let style;
  try {
    style = new URL(tiles.style_url);
  } catch (_) {
    return null;
  }
  if (style.hostname.toLowerCase() !== 'vtod1.bayernwolke.de' ||
      !style.pathname.startsWith('/styles/by_style_')) {
    return null;
  }
  const isAerial = style.pathname.toLowerCase().includes('luftbild');
  return {
    url: isAerial
      ? 'https://wmtsod1.bayernwolke.de/wmts/by_dop/smerc/{z}/{x}/{y}'
      : 'https://wmtsod1.bayernwolke.de/wmts/by_webkarte/smerc/{z}/{x}/{y}',
    attribution: escapeHtml('© Datenquellen: Bayerische Vermessungsverwaltung, GeoBasis-DE / BKG 2023 – Daten verändert'),
  };
}

// Direct raster sources are intentionally loaded by the browser rather than
// through /tiles. This preserves normal browser request metadata for public
// tile services and keeps their traffic out of the application's cache proxy.
// The hostname check also repairs older settings files that still describe the
// standard OSM source as plain "raster".
function usesDirectRaster(mapType, tiles) {
  if (mapType === 'raster-direct') return true;
  try {
    return new URL(tiles.upstream).hostname.toLowerCase() === 'tile.openstreetmap.org';
  } catch (_) {
    return false;
  }
}

function updateMapModeUI(tiles = {}) {
  const context = document.querySelector('.workspace-context');
  const icon = document.getElementById('mapModeIcon');
  const title = document.getElementById('mapModeTitle');
  const meta = document.getElementById('mapModeMeta');
  if (!context || !icon || !title || !meta) return;

  const styleURL = String(tiles.style_url || '');
  const upstream = String(tiles.upstream || '');
  const isTinyTiles = styleURL === '/static/styles/tinytiles-minimal.json';
  const isBavaria = styleURL.includes('bayernwolke.de') || upstream.includes('bayernwolke.de') || upstream.includes('geoservices.bayern.de');
  context.dataset.mode = isTinyTiles ? 'offline' : isBavaria ? 'bayern' : 'online';

  if (isTinyTiles) {
    icon.textContent = '📦';
    title.textContent = 'Offline-Karte aktiv';
    meta.textContent = 'tinyTiles + Routing: lokale PBF.';
  } else if (isBavaria) {
    icon.textContent = '⌖';
    title.textContent = 'Bayern-Karte aktiv';
    meta.textContent = 'BayernAtlas · Routing: lokale PBF.';
  } else {
    icon.textContent = '◌';
    title.textContent = 'Online-Karte aktiv';
    meta.textContent = 'Routing & Suche: lokale PBF.';
  }
}

function isTinyTilesSettings(tiles = {}) {
  return String(tiles.style_url || '') === '/static/styles/tinytiles-minimal.json';
}

function queueOfflineLabels() {
  if (!offlineLabelsEnabled) return;
  window.clearTimeout(offlineLabelsTimer);
  offlineLabelsTimer = window.setTimeout(refreshOfflineLabels, 160);
}

function clearOfflineLabelMarkers() {
  removeMarkers(offlineLabelMarkers);
}

// Builds the DOM element for one offline label (a plain text span in an
// anchor div — reuses the existing library-agnostic offline-label-* CSS).
function offlineLabelElement(kind, name) {
  const el = markerElement(`map-marker offline-label-anchor offline-label-${kind}`);
  const span = markerElement('offline-map-label', name);
  el.appendChild(span);
  return el;
}

async function refreshOfflineLabels() {
  if (!offlineLabelsEnabled) return;
  const zoom = map.getZoom();
  if (zoom < 7) {
    clearOfflineLabelMarkers();
    return;
  }
  const bounds = map.getBounds();
  const bbox = [
    bounds.getWest(), bounds.getSouth(), bounds.getEast(), bounds.getNorth(),
  ].map((value) => Number(value).toFixed(6)).join(',');
  offlineLabelsRequest?.abort();
  const controller = new AbortController();
  offlineLabelsRequest = controller;
  try {
    const response = await fetch(`/api/v1/offline-labels?bbox=${encodeURIComponent(bbox)}&zoom=${Math.round(zoom)}`, {
      signal: controller.signal,
      headers: { Accept: 'application/json' },
    });
    if (!response.ok) throw new Error(`offline labels: ${response.status}`);
    const payload = await response.json();
    if (!offlineLabelsEnabled || offlineLabelsRequest !== controller) return;
    clearOfflineLabelMarkers();
    const labels = (Array.isArray(payload.labels) ? payload.labels : [])
      .map((label) => {
        const coord = normalizeLatLon(label.lat, label.lon);
        const name = String(label.name || '').trim();
        if (!coord || !name) return null;
        return { lat: coord.lat, lon: coord.lon, name, kind: label.kind === 'place' ? 'place' : 'road' };
      })
      .filter(Boolean)
      // MapLibre markers stack in DOM append order (no zIndexOffset like
      // Leaflet) — add 'road' labels first so 'place' labels render on top.
      .sort((a, b) => (a.kind === b.kind ? 0 : a.kind === 'place' ? 1 : -1));
    for (const label of labels) {
      const marker = new maplibregl.Marker({ element: offlineLabelElement(label.kind, label.name), anchor: 'center' })
        .setLngLat([label.lon, label.lat])
        .addTo(map);
      offlineLabelMarkers.push(marker);
    }
  } catch (error) {
    if (error?.name !== 'AbortError') console.debug('Offline labels are temporarily unavailable', error);
  }
}

function setOfflineLabelsVisible(enabled) {
  if (offlineLabelsEnabled === enabled) {
    if (enabled) queueOfflineLabels();
    return;
  }
  offlineLabelsEnabled = enabled;
  window.clearTimeout(offlineLabelsTimer);
  offlineLabelsRequest?.abort();
  offlineLabelsRequest = null;
  if (!enabled) {
    map.off('moveend', queueOfflineLabels);
    map.off('zoomend', queueOfflineLabels);
    clearOfflineLabelMarkers();
    return;
  }
  map.on('moveend', queueOfflineLabels);
  map.on('zoomend', queueOfflineLabels);
  queueOfflineLabels();
}

// ---- Hydrants overlay (BOS/Einsatzmodus) ----
// Same queue/refresh/clear-on-pan/zoom shape as the offline labels above,
// but independent of the base map style (works on raster, vector, and
// offline alike) and gated to a closer zoom since hydrants are dense enough
// that showing them zoomed out would just be visual noise.
let hydrantMarkers = [];
let hydrantsEnabled = false;
let hydrantsTimer = null;
let hydrantsRequest = null;
const HYDRANT_MIN_ZOOM = 14;

function hydrantElement(type) {
  const label = '🚰';
  const el = markerElement('map-marker map-hydrant-marker', label);
  el.title = type === 'underground' ? 'Unterflurhydrant'
    : type === 'pillar' ? 'Überflurhydrant'
    : type === 'wall' ? 'Wandhydrant'
    : 'Hydrant';
  return el;
}

function clearHydrantMarkers() {
  removeMarkers(hydrantMarkers);
}

function queueHydrants() {
  if (!hydrantsEnabled) return;
  window.clearTimeout(hydrantsTimer);
  hydrantsTimer = window.setTimeout(refreshHydrants, 200);
}

async function refreshHydrants() {
  if (!hydrantsEnabled) return;
  if (map.getZoom() < HYDRANT_MIN_ZOOM) {
    clearHydrantMarkers();
    return;
  }
  const bounds = map.getBounds();
  const bbox = [
    bounds.getWest(), bounds.getSouth(), bounds.getEast(), bounds.getNorth(),
  ].map((value) => Number(value).toFixed(6)).join(',');
  hydrantsRequest?.abort();
  const controller = new AbortController();
  hydrantsRequest = controller;
  try {
    const response = await fetch(`/api/v1/hydrants?bbox=${encodeURIComponent(bbox)}`, {
      signal: controller.signal,
      headers: { Accept: 'application/json' },
    });
    if (!response.ok) throw new Error(`hydrants: ${response.status}`);
    const payload = await response.json();
    if (!hydrantsEnabled || hydrantsRequest !== controller) return;
    clearHydrantMarkers();
    for (const h of Array.isArray(payload.hydrants) ? payload.hydrants : []) {
      const coord = normalizeLatLon(h.lat, h.lon);
      if (!coord) continue;
      const marker = new maplibregl.Marker({ element: hydrantElement(h.type), anchor: 'center' })
        .setLngLat([coord.lon, coord.lat])
        .addTo(map);
      if (h.name) marker.setPopup(new maplibregl.Popup().setText(h.name));
      hydrantMarkers.push(marker);
    }
  } catch (error) {
    if (error?.name !== 'AbortError') console.debug('Hydrants are temporarily unavailable', error);
  }
}

function setHydrantsVisible(enabled) {
  if (hydrantsEnabled === enabled) {
    if (enabled) queueHydrants();
    return;
  }
  hydrantsEnabled = enabled;
  window.clearTimeout(hydrantsTimer);
  hydrantsRequest?.abort();
  hydrantsRequest = null;
  if (!enabled) {
    map.off('moveend', queueHydrants);
    map.off('zoomend', queueHydrants);
    clearHydrantMarkers();
    return;
  }
  map.on('moveend', queueHydrants);
  map.on('zoomend', queueHydrants);
  queueHydrants();
  if (map.getZoom() < HYDRANT_MIN_ZOOM) {
    showToast('Näher heranzoomen, um Hydranten zu sehen', 'info', 2500);
  }
}

document.getElementById('showHydrants')?.addEventListener('change', (ev) => {
  try { ev.target.setAttribute('aria-checked', ev.target.checked ? 'true' : 'false'); } catch (e) {}
  setHydrantsVisible(!!ev.target.checked);
});

// ---- Fire stations overlay (Einsatzmodus) ----
// Stations are auto-detected from the loaded PBF (amenity=fire_station) on
// the server; vehicles/Funkrufnamen are optional local enrichment added here
// via manual entry or CSV import (never committed — see cmd/fire_stations.go).
let fireStationMarkers = [];
let fireStationsEnabled = false;
let fireStationsTimer = null;
let fireStationsRequest = null;
let fireStationAddMode = false;
const FIRE_STATION_MIN_ZOOM = 11;

function fireStationElement() {
  return markerElement('map-marker map-firestation-marker', '🚒');
}

function clearFireStationMarkers() {
  removeMarkers(fireStationMarkers);
}

function queueFireStations() {
  if (!fireStationsEnabled) return;
  window.clearTimeout(fireStationsTimer);
  fireStationsTimer = window.setTimeout(refreshFireStations, 250);
}

function fireStationPopupHtml(station) {
  const vehicleRows = (station.vehicles || []).map((v) =>
    `<div class="territory-popup-row"><span class="territory-popup-key">${escapeHtml(v.callsign || '')}</span><span class="territory-popup-val">${escapeHtml(v.type || '')}</span></div>`
  ).join('') || '<div style="opacity:0.7;font-size:12px;">Keine Fahrzeuge hinterlegt</div>';
  return `<div style="min-width:200px;">
    <strong>${escapeHtml(station.name || 'Feuerwehrhaus (kein Name in OSM)')}</strong>
    <div style="margin-top:6px;">${vehicleRows}</div>
    <div style="margin-top:8px;display:flex;gap:4px;flex-wrap:wrap;">
      <button type="button" class="btn btn-sm btn-outline add-vehicle-btn" style="flex:1;">+ Fahrzeug</button>
      <button type="button" class="btn btn-sm btn-outline rename-station-btn" style="flex:1;">Umbenennen</button>
      ${station.source === 'manual' ? '<button type="button" class="btn btn-sm btn-outline delete-station-btn" style="flex:1;">Löschen</button>' : ''}
    </div>
  </div>`;
}

// Same "don't touch the popup's own HTML after it opens" rule as the custom
// markers above: this only runs once per open (popup.on('open', ...)), so
// actions here close the popup afterward rather than trying to refresh it.
function wireFireStationPopup(popup, station) {
  const el = popup.getElement();
  if (!el) return;
  const addBtn = el.querySelector('.add-vehicle-btn');
  if (addBtn) {
    addBtn.addEventListener('click', async () => {
      const callsign = window.prompt('Funkrufname (z.B. FL Musterstadt 40/1):');
      if (!callsign || !callsign.trim()) return;
      const type = window.prompt('Fahrzeugtyp (optional, z.B. LF 20):') || '';
      const vehicles = [...(station.vehicles || []), { callsign: callsign.trim(), type: type.trim() }];
      try {
        const res = await fetch(`/api/v1/fire-stations/${encodeURIComponent(station.id)}`, {
          method: 'PUT',
          headers: adminAuthHeaders({ 'Content-Type': 'application/json' }),
          body: JSON.stringify({ name: station.name, lat: station.lat, lon: station.lon, vehicles }),
        });
        if (!res.ok) throw new Error(await res.text());
        popup.remove();
        showToast('Fahrzeug hinzugefügt', 'success', 1500);
        queueFireStations();
      } catch (e) { showToast('Fehler beim Speichern', 'error', 3000); }
    });
  }
  const renameBtn = el.querySelector('.rename-station-btn');
  if (renameBtn) {
    renameBtn.addEventListener('click', async () => {
      // Some real OSM fire_station ways carry no "name" tag at all, so they
      // never show up under their real name and can't be matched by CSV
      // import — this lets an operator attach/correct a display name.
      const next = window.prompt('Name des Feuerwehrhauses:', station.name || '');
      if (next === null) return;
      const name = next.trim();
      if (!name) return;
      try {
        const res = await fetch(`/api/v1/fire-stations/${encodeURIComponent(station.id)}`, {
          method: 'PUT',
          headers: adminAuthHeaders({ 'Content-Type': 'application/json' }),
          body: JSON.stringify({ name, lat: station.lat, lon: station.lon, vehicles: station.vehicles || [] }),
        });
        if (!res.ok) throw new Error(await res.text());
        popup.remove();
        showToast('Name gespeichert', 'success', 1500);
        queueFireStations();
      } catch (e) { showToast('Fehler beim Speichern', 'error', 3000); }
    });
  }
  const delBtn = el.querySelector('.delete-station-btn');
  if (delBtn) {
    delBtn.addEventListener('click', async () => {
      try {
        const res = await fetch(`/api/v1/fire-stations/${encodeURIComponent(station.id)}`, { method: 'DELETE', headers: adminAuthHeaders() });
        if (!res.ok) throw new Error(await res.text());
        popup.remove();
        showToast('Feuerwehrhaus gelöscht', 'info', 1500);
        queueFireStations();
      } catch (e) { showToast('Fehler beim Löschen', 'error', 3000); }
    });
  }
}

async function refreshFireStations() {
  if (!fireStationsEnabled) return;
  if (map.getZoom() < FIRE_STATION_MIN_ZOOM) {
    clearFireStationMarkers();
    return;
  }
  const bounds = map.getBounds();
  const bbox = [
    bounds.getWest(), bounds.getSouth(), bounds.getEast(), bounds.getNorth(),
  ].map((value) => Number(value).toFixed(6)).join(',');
  fireStationsRequest?.abort();
  const controller = new AbortController();
  fireStationsRequest = controller;
  try {
    const response = await fetch(`/api/v1/fire-stations?bbox=${encodeURIComponent(bbox)}`, {
      signal: controller.signal,
      headers: { Accept: 'application/json' },
    });
    if (!response.ok) throw new Error(`fire-stations: ${response.status}`);
    const payload = await response.json();
    if (!fireStationsEnabled || fireStationsRequest !== controller) return;
    clearFireStationMarkers();
    for (const st of Array.isArray(payload.stations) ? payload.stations : []) {
      const coord = normalizeLatLon(st.lat, st.lon);
      if (!coord) continue;
      const marker = new maplibregl.Marker({ element: fireStationElement(), anchor: 'bottom' }).setLngLat([coord.lon, coord.lat]).addTo(map);
      const popup = new maplibregl.Popup().setHTML(fireStationPopupHtml(st));
      popup.on('open', () => wireFireStationPopup(popup, st));
      marker.setPopup(popup);
      fireStationMarkers.push(marker);
    }
  } catch (error) {
    if (error?.name !== 'AbortError') console.debug('Fire stations are temporarily unavailable', error);
  }
}

function setFireStationsVisible(enabled) {
  document.getElementById('fireStationTools')?.classList.toggle('hidden', !enabled);
  if (fireStationsEnabled === enabled) {
    if (enabled) queueFireStations();
    return;
  }
  fireStationsEnabled = enabled;
  window.clearTimeout(fireStationsTimer);
  fireStationsRequest?.abort();
  fireStationsRequest = null;
  if (!enabled) {
    map.off('moveend', queueFireStations);
    map.off('zoomend', queueFireStations);
    clearFireStationMarkers();
    return;
  }
  map.on('moveend', queueFireStations);
  map.on('zoomend', queueFireStations);
  queueFireStations();
  if (map.getZoom() < FIRE_STATION_MIN_ZOOM) {
    showToast('Näher heranzoomen, um Feuerwehrhäuser zu sehen', 'info', 2500);
  }
}

document.getElementById('showFireStations')?.addEventListener('change', (ev) => {
  try { ev.target.setAttribute('aria-checked', ev.target.checked ? 'true' : 'false'); } catch (e) {}
  setFireStationsVisible(!!ev.target.checked);
});

document.getElementById('addFireStationBtn')?.addEventListener('click', () => {
  fireStationAddMode = !fireStationAddMode;
  const btn = document.getElementById('addFireStationBtn');
  if (btn) btn.textContent = fireStationAddMode ? '📍 Jetzt auf die Karte klicken …' : '📍 Feuerwehrhaus manuell hinzufügen (auf Karte klicken)';
  if (fireStationAddMode) showToast('Klicke auf die Karte, um ein Feuerwehrhaus zu platzieren', 'info', 2500);
});

map.on('click', async (ev) => {
  if (!fireStationAddMode) return;
  fireStationAddMode = false;
  const btn = document.getElementById('addFireStationBtn');
  if (btn) btn.textContent = '📍 Feuerwehrhaus manuell hinzufügen (auf Karte klicken)';
  const name = window.prompt('Name des Feuerwehrhauses:');
  if (!name || !name.trim()) return;
  try {
    const res = await fetch('/api/v1/fire-stations', {
      method: 'POST',
      headers: adminAuthHeaders({ 'Content-Type': 'application/json' }),
      body: JSON.stringify({ name: name.trim(), lat: ev.lngLat.lat, lon: ev.lngLat.lng }),
    });
    if (!res.ok) throw new Error(await res.text());
    showToast('Feuerwehrhaus hinzugefügt', 'success', 1500);
    queueFireStations();
  } catch (e) { showToast('Fehler beim Speichern', 'error', 3000); }
});

document.getElementById('importFireStationCsvBtn')?.addEventListener('click', async () => {
  const csvEl = document.getElementById('fireStationCsv');
  const csvText = csvEl?.value.trim();
  if (!csvText) { showToast('Bitte CSV-Text einfügen', 'info', 2000); return; }
  const btn = document.getElementById('importFireStationCsvBtn');
  const originalLabel = btn ? btn.textContent : '';
  // Rows with no direct name match trigger a geocode lookup server-side —
  // each one is a real address/POI search, so this can take a while for a
  // roster with several unmatched stations. Make that visible instead of
  // leaving the button looking stuck.
  if (btn) { btn.disabled = true; btn.textContent = 'Importiere …'; }
  try {
    const res = await fetch('/api/v1/fire-stations/import', {
      method: 'POST',
      headers: adminAuthHeaders({ 'Content-Type': 'text/csv' }),
      body: csvText,
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      throw new Error(err.error || res.statusText);
    }
    const result = await res.json();
    const matched = result.matched?.length || 0;
    const guessed = result.guessed?.length || 0;
    const unmatched = result.unmatched?.length || 0;
    const parts = [`${matched} zugeordnet`];
    if (guessed > 0) parts.push(`${guessed} über Ortsnamen gefunden`);
    if (unmatched > 0) parts.push(`${unmatched} ohne Treffer`);
    showToast(`Import: ${parts.join(', ')}`, unmatched > 0 ? 'info' : 'success', 5000);
    renderFireStationImportResult(result);
    queueFireStations();
  } catch (e) {
    showToast('Import fehlgeschlagen: ' + (e.message || ''), 'error', 4000);
  } finally {
    if (btn) { btn.disabled = false; btn.textContent = originalLabel; }
  }
});

// "guessed" = auto-matched to a nearby *unnamed* OSM fire_station by
// geocoding the place name (see backend geocodeStationHint) — worth a
// glance, but already merged in. Genuinely unmatched rows with a geocode
// hint get a one-click "Hier anlegen" button instead of requiring the
// operator to hunt for the right spot on the map by hand.
function renderFireStationImportResult(result) {
  const el = document.getElementById('fireStationImportResult');
  if (!el) return;
  const guessed = result.guessed || [];
  const unmatched = result.unmatched || [];
  if (guessed.length === 0 && unmatched.length === 0) { el.innerHTML = ''; return; }
  const guessedHtml = guessed.length ? `<div style="font-size:11px;opacity:0.85;margin-bottom:4px;">Über Ortsnamen gefunden (bitte kurz prüfen): ${guessed.map(escapeHtml).join(', ')}</div>` : '';
  const unmatchedHtml = unmatched.map((u, i) => {
    const hasHint = typeof u.hintLat === 'number' && typeof u.hintLon === 'number' && (u.hintLat !== 0 || u.hintLon !== 0);
    return `<div class="territory-popup-row" style="align-items:center;">
      <span class="territory-popup-key" style="font-size:11px;">${escapeHtml(u.name)}${hasHint ? ` <span style="opacity:0.7;">(nahe ${escapeHtml(u.hintLabel || '')})</span>` : ''}</span>
      ${hasHint ? `<button type="button" class="btn btn-sm btn-outline place-unmatched-btn" data-idx="${i}" style="font-size:11px;padding:2px 6px;">Hier anlegen</button>` : '<span style="font-size:11px;opacity:0.6;">kein Vorschlag</span>'}
    </div>`;
  }).join('');
  el.innerHTML = `${guessedHtml}${unmatchedHtml ? `<div style="font-size:11px;opacity:0.85;margin:4px 0;">Ohne Treffer:</div>${unmatchedHtml}` : ''}`;
  el.querySelectorAll('.place-unmatched-btn').forEach((btn) => {
    btn.addEventListener('click', async () => {
      const u = unmatched[Number(btn.dataset.idx)];
      if (!u) return;
      btn.disabled = true;
      try {
        const res = await fetch('/api/v1/fire-stations', {
          method: 'POST',
          headers: adminAuthHeaders({ 'Content-Type': 'application/json' }),
          body: JSON.stringify({ name: u.name, lat: u.hintLat, lon: u.hintLon }),
        });
        if (!res.ok) throw new Error(await res.text());
        showToast(`${u.name} angelegt`, 'success', 1500);
        btn.closest('.territory-popup-row')?.remove();
        queueFireStations();
      } catch (e) {
        showToast('Fehler beim Anlegen', 'error', 3000);
        btn.disabled = false;
      }
    });
  });
}

document.getElementById('fireStationCsvFile')?.addEventListener('change', async (ev) => {
  const file = ev.target.files?.[0];
  if (!file) return;
  try {
    const text = await file.text();
    const csvEl = document.getElementById('fireStationCsv');
    if (csvEl) csvEl.value = text;
    showToast(`Datei geladen: ${file.name}`, 'success', 1500);
  } catch (e) {
    showToast('Datei konnte nicht gelesen werden', 'error', 3000);
  } finally {
    ev.target.value = '';
  }
});

document.getElementById('downloadFireStationCsvExampleBtn')?.addEventListener('click', () => {
  const example = 'Dienststelle,Funkrufname,Fahrzeugtyp\n'
    + 'FF Musterstadt,FL Musterstadt 40/1,LF 20\n'
    + 'FF Musterstadt,FL Musterstadt 41/1,LF 10\n'
    + 'FF Musterhausen,FL Musterhausen 30/1,DLK 23/12\n';
  const blob = new Blob([example], { type: 'text/csv' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = 'fahrzeuge-beispiel.csv';
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
});

// Use a compact, non-secret fingerprint for browser-cache namespacing. Passing
// a raw upstream URL here could expose custom service tokens in browser/server
// logs; the server independently derives its own cache namespace.
function tileSourceCacheKey(mapType, tiles) {
  const source = [mapType, tiles.upstream || '', tiles.wms_layers || ''].join('\u001f');
  let h1 = 0xdeadbeef;
  let h2 = 0x41c6ce57;
  for (let i = 0; i < source.length; i++) {
    const code = source.charCodeAt(i);
    h1 = Math.imul(h1 ^ code, 2654435761);
    h2 = Math.imul(h2 ^ code, 1597334677);
  }
  return `${(h1 >>> 0).toString(36)}-${(h2 >>> 0).toString(36)}`;
}

// Let a committed map change reach the next paint. Source-specific readiness
// checks live below; this only handles the last browser frame.
function waitForMapLayerPaint() {
  return new Promise((resolve) => {
    window.requestAnimationFrame(() => window.requestAnimationFrame(resolve));
  });
}

// Resolves once the map's current style (initial load or a prior
// map.setStyle() call) has fully finished loading, or false on error/timeout.
function waitForStyleReady(timeoutMs = 5000) {
  return new Promise((resolve) => {
    let done = false;
    const finish = (ready) => {
      if (done) return;
      done = true;
      window.clearTimeout(timeout);
      map.off('error', onError);
      resolve(ready);
    };
    const onError = () => finish(false);
    const timeout = window.setTimeout(() => finish(true), timeoutMs);
    map.once('error', onError);
    if (map.isStyleLoaded()) { finish(true); return; }
    map.once('style.load', () => finish(true));
  });
}

// Resolves once a given source has finished loading its visible tiles (or on
// timeout/error), the MapLibre-native equivalent of the old tileload/tileerror
// counting used for Leaflet raster layers.
function waitForSourceReady(sourceId, timeoutMs = 5000) {
  return new Promise((resolve) => {
    let done = false;
    let sawError = false;
    const finish = (ready) => {
      if (done) return;
      done = true;
      window.clearTimeout(timeout);
      map.off('sourcedata', onSourceData);
      map.off('error', onError);
      resolve(ready);
    };
    const onSourceData = (e) => {
      if (e.sourceId === sourceId && e.isSourceLoaded && map.isSourceLoaded(sourceId)) finish(true);
    };
    const onError = (e) => {
      if (e.sourceId === sourceId) sawError = true;
    };
    const timeout = window.setTimeout(() => finish(!sawError), timeoutMs);
    map.on('sourcedata', onSourceData);
    map.on('error', onError);
    if (map.isSourceLoaded(sourceId)) { finish(true); return; }
  });
}

// Raster/WMS base layers are added as plain sources+layers directly onto the
// current style (cheap, no flicker: the old source stays visible until the
// new one is confirmed ready). Vector base layers instead replace the whole
// style via map.setStyle() below, so switching *back* to raster first needs
// to land on a raster-capable (i.e. non-vector) style.
async function ensureRasterCapableStyle(generation) {
  if (baseLayerKind === 'raster') return true;
  if (baseLayerKind !== null) {
    // Coming from a vector style: reset to an empty style before adding a
    // raster source, since the vector style owns its own background/water/
    // road layers that a raster source can't simply be layered underneath.
    map.setStyle({ version: 8, sources: {}, layers: [] });
    currentTileLayer = null;
  }
  const ready = await waitForStyleReady();
  return ready && generation === tileLayerGeneration;
}

async function activateRasterSource(sourceId, sourceSpec, generation) {
  map.addSource(sourceId, sourceSpec);
  map.addLayer({ id: sourceId, type: 'raster', source: sourceId });
  const ready = await waitForSourceReady(sourceId);
  if (!ready || generation !== tileLayerGeneration) {
    if (map.getLayer(sourceId)) map.removeLayer(sourceId);
    if (map.getSource(sourceId)) map.removeSource(sourceId);
    return false;
  }
  const previous = currentTileLayer;
  if (previous && previous.kind === 'raster' && previous.sourceId !== sourceId) {
    if (map.getLayer(previous.sourceId)) map.removeLayer(previous.sourceId);
    if (map.getSource(previous.sourceId)) map.removeSource(previous.sourceId);
  }
  currentTileLayer = { kind: 'raster', sourceId };
  baseLayerKind = 'raster';
  await waitForMapLayerPaint();
  return true;
}

// Apply a tile/map layer from settings. The new source/style is prepared and
// confirmed ready before the old one is torn down, so a failed switch leaves
// the visible map intact. A temporary source preview goes directly to the
// selected provider: the server-side cache deliberately only knows the
// persisted source configuration.
async function applyTileLayer(settings, { directPreview = false } = {}) {
  await mapInitialLoad;
  const generation = ++tileLayerGeneration;
  const tiles = (settings && settings.tiles) || {};
  const mapType = (tiles.map_type || 'raster').toLowerCase();
  const attribution = escapeHtml(tiles.attribution || '');
  const maxZoom = Number.isInteger(tiles.max_zoom) && tiles.max_zoom > 0 ? tiles.max_zoom : 19;
  const directRaster = directPreview || usesDirectRaster(mapType, tiles);

  // The query component gives the browser cache a source-specific URL. The
  // server independently namespaces L1/L2 by the same render-relevant fields.
  // This prevents an OSM tile from being reused after switching to BayernAtlas.
  const proxyURL = '/tiles/{z}/{x}/{y}.png?source=' + tileSourceCacheKey(mapType, tiles);

  if (mapType === 'vector' && tiles.style_url && supportsWebGL()) {
    try {
      map.setStyle(tiles.style_url);
      const ready = await waitForStyleReady();
      if (!ready || generation !== tileLayerGeneration) return false;
      currentTileLayer = { kind: 'vector', styleURL: tiles.style_url };
      baseLayerKind = 'vector';
      await waitForMapLayerPaint();
      updateMapModeUI(tiles);
      setOfflineLabelsVisible(isTinyTilesSettings(tiles));
      rehydrateMapLayers();
      return true;
    } catch (e) {
      console.warn('MapLibre GL style load failed, falling back to raster tiles', e);
    }
  } else if (mapType === 'vector' && tiles.style_url) {
    console.warn('WebGL is unavailable, falling back to raster tiles');
  }

  if (generation !== tileLayerGeneration) return false;
  if (mapType === 'wms' && tiles.upstream && directPreview) {
    const ok = await ensureRasterCapableStyle(generation);
    if (!ok) return false;
    // MapLibre raster sources understand the {bbox-epsg-3857} WMS template
    // token natively; non-preview WMS instead goes through the server-side
    // /tiles proxy below, which already normalizes WMS to plain XYZ tiles.
    const sep = tiles.upstream.includes('?') ? '&' : '?';
    const wmsURL = `${tiles.upstream}${sep}SERVICE=WMS&VERSION=1.1.1&REQUEST=GetMap&FORMAT=image%2Fpng&TRANSPARENT=false&LAYERS=${encodeURIComponent(tiles.wms_layers || '')}&SRS=EPSG%3A3857&WIDTH=256&HEIGHT=256&BBOX={bbox-epsg-3857}`;
    const sourceId = `base-raster-${generation}`;
    const applied = await activateRasterSource(sourceId, {
      type: 'raster', tiles: [wmsURL], tileSize: 256, attribution, maxzoom: maxZoom,
    }, generation);
    if (applied) {
      updateMapModeUI(tiles);
      setOfflineLabelsVisible(isTinyTilesSettings(tiles));
      rehydrateMapLayers();
    }
    return applied;
  }
  const rasterFallback = tiles.upstream
    ? { url: directRaster ? tiles.upstream : proxyURL, attribution }
    : bayernRasterFallback(tiles);
  if (!rasterFallback) {
    const message = tiles.style_url === '/static/styles/tinytiles-minimal.json'
      ? 'Die lokale tinyTiles-Karte benötigt WebGL. Bitte WebGL aktivieren oder eine Raster-Kartenquelle auswählen.'
      : 'Diese Vektorkarte benötigt WebGL oder eine Raster-Fallback-URL. Bitte BayernAtlas WMTS auswählen.';
    console.error(message);
    showToast(message, 'error', 7000);
    return false;
  }
  // Proxied raster, WMTS, and WMS sources use the same-origin tile endpoint.
  // For WMS the server converts the slippy coordinate to GetMap parameters;
  // direct raster sources are loaded from their own URL instead.
  const ok = await ensureRasterCapableStyle(generation);
  if (!ok) return false;
  const sourceId = `base-raster-${generation}`;
  const applied = await activateRasterSource(sourceId, {
    type: 'raster',
    tiles: [rasterFallback.url],
    tileSize: 256,
    attribution: rasterFallback.attribution,
    maxzoom: maxZoom,
  }, generation);
  if (applied) {
    updateMapModeUI(tiles);
    setOfflineLabelsVisible(isTinyTilesSettings(tiles));
    rehydrateMapLayers();
  }
  return applied;
}

// Initialize the tile layer from preloaded (server-side) settings
applyTileLayer(preloadedSettings);

// Toast notification system
function showToast(message, type = 'info', duration = 3000) {
  const toast = document.createElement('div');
  toast.className = `toast ${type}`;
  const icon = type === 'success' ? '✅' : type === 'error' ? '❌' : 'ℹ️';
  const iconEl = document.createElement('span');
  iconEl.style.fontSize = '18px';
  iconEl.textContent = icon;
  const messageEl = document.createElement('span');
  messageEl.textContent = String(message || '');
  toast.append(iconEl, messageEl);
  document.body.appendChild(toast);
  setTimeout(() => {
    toast.style.animation = 'toastSlide 0.3s ease reverse';
    setTimeout(() => toast.remove(), 300);
  }, duration);
}

// A map-level progress affordance is separate from the build status in the
// settings panel.  It makes a source switch comprehensible even while that
// panel is collapsed.
function showTileLoadOverlay({ title = 'Karte wird geladen', message = 'Die Kartenquelle wird vorbereitet.', progress = null, mode = 'online' } = {}) {
  const overlay = document.getElementById('tileLoadOverlay');
  if (!overlay) return;
  const titleEl = document.getElementById('tileLoadTitle');
  const messageEl = document.getElementById('tileLoadMessage');
  const iconEl = document.getElementById('tileLoadIcon');
  const progressEl = document.getElementById('tileLoadProgress');
  const progressBar = document.getElementById('tileLoadProgressBar');
  const numericProgress = Number(progress);
  const determinate = Number.isFinite(numericProgress);
  const value = Math.max(0, Math.min(100, Math.round(numericProgress)));

  if (titleEl) titleEl.textContent = title;
  if (messageEl) messageEl.textContent = message;
  if (iconEl) iconEl.textContent = mode === 'tinytiles' ? '📦' : '◌';
  if (progressEl) {
    progressEl.classList.toggle('is-determinate', determinate);
    progressEl.setAttribute('aria-valuetext', determinate ? `${value} %` : 'Wird geladen');
    if (determinate) progressEl.setAttribute('aria-valuenow', String(value));
    else progressEl.removeAttribute('aria-valuenow');
  }
  if (progressBar) {
    progressBar.style.width = determinate ? `${value}%` : '';
  }
  overlay.hidden = false;
}

function hideTileLoadOverlay() {
  const overlay = document.getElementById('tileLoadOverlay');
  if (overlay) overlay.hidden = true;
}

// Use browser geolocation to set 'from' input and center the map
document.getElementById('useLocationBtn')?.addEventListener('click', async () => {
  if (!navigator.geolocation) {
    showToast('Geolocation wird von diesem Browser nicht unterstützt', 'error');
    return;
  }
  showToast('Standort wird ermittelt…', 'info', 3000);
  navigator.geolocation.getCurrentPosition((pos) => {
    const lat = pos.coords.latitude;
    const lon = pos.coords.longitude;
    userLocation = { lat, lon };
    const fromInput = document.getElementById('from');
    if (fromInput) {
      fromInput.value = `${lat.toFixed(6)},${lon.toFixed(6)}`;
      syncInputClearState('from');
    }
    // set user marker
    if (userLocationMarker) userLocationMarker.remove();
    userLocationMarker = new maplibregl.Marker({ element: dotElement('#2ee6a7'), anchor: 'center' })
      .setLngLat([lon, lat])
      .setPopup(new maplibregl.Popup().setText('Ihr Standort'))
      .addTo(map);
    userLocationMarker.togglePopup();
    map.jumpTo({ center: [lon, lat], zoom: 14 });
    showToast('Standort gesetzt', 'success', 1500);
  }, (err) => {
    showToast('Standort konnte nicht ermittelt werden: ' + (err.message||''), 'error', 4000);
  }, { enableHighAccuracy: true, timeout: 10000 });
});

// helper: detect if prompt explicitly refers to the current map view/area
function promptReferencesMap(prompt) {
  if (!prompt) return false;
  const p = prompt.toLowerCase();
  const phrases = ['in der nähe der aktuellen karte', 'in diesem bereich', 'in dieser karte', 'auf der karte', 'aktuelle karte', 'dieser bereich', 'in der nähe der karte'];
  return phrases.some(ph => p.includes(ph));
}

// Debounce helper for performance (defined early so UI code can reference it)
function debounce(func, wait) {
  let timeout;
  return function executedFunction(...args) {
    const later = () => {
      clearTimeout(timeout);
      func(...args);
    };
    clearTimeout(timeout);
    timeout = setTimeout(later, wait);
  };
}

// debounced wrapper used by inputs (compute is hoisted)
const debouncedCompute = debounce(function(){ try{ compute(); } catch(e){} }, 300);

// The route line lives on a single persistent GeoJSON source+layer rather
// than being removed/recreated per Leaflet's L.polyline. `polyline` stays an
// object shaped like { coords, remove(), getBounds(), getLatLngs() } so the
// many call sites below (zoom-to-route, clear, export) don't need to change.
const ROUTE_SOURCE_ID = 'route';
function emptyFeatureCollection() { return { type: 'FeatureCollection', features: [] }; }
function ensureRouteLayer() {
  if (map.getSource(ROUTE_SOURCE_ID)) return;
  map.addSource(ROUTE_SOURCE_ID, { type: 'geojson', data: emptyFeatureCollection() });
  map.addLayer({
    id: ROUTE_SOURCE_ID,
    type: 'line',
    source: ROUTE_SOURCE_ID,
    layout: { 'line-cap': 'round', 'line-join': 'round' },
    paint: { 'line-color': '#3a8eef', 'line-width': 5, 'line-opacity': 0.8 },
  });
}
function makeRouteLine(lngLatCoords) {
  ensureRouteLayer();
  map.getSource(ROUTE_SOURCE_ID).setData({
    type: 'FeatureCollection',
    features: [{ type: 'Feature', geometry: { type: 'LineString', coordinates: lngLatCoords }, properties: {} }],
  });
  return {
    coords: lngLatCoords,
    remove() {
      if (map.getSource(ROUTE_SOURCE_ID)) map.getSource(ROUTE_SOURCE_ID).setData(emptyFeatureCollection());
    },
    getBounds() {
      return lngLatCoords.reduce((acc, c) => acc.extend(c), new maplibregl.LngLatBounds(lngLatCoords[0], lngLatCoords[0]));
    },
    getLatLngs() {
      return lngLatCoords.map(c => ({ lng: c[0], lat: c[1] }));
    },
  };
}
// A base-layer switch (map.setStyle()) wipes sources added outside the
// style, including the route source — re-add it, and redraw the last route
// if one was on screen, after every switch.
registerMapLayerRehydrate(() => {
  ensureRouteLayer();
  if (polyline) {
    map.getSource(ROUTE_SOURCE_ID).setData({
      type: 'FeatureCollection',
      features: [{ type: 'Feature', geometry: { type: 'LineString', coordinates: polyline.coords }, properties: {} }],
    });
  }
});

let polyline = null;
let startMarker = null, endMarker = null;
let currentRouteBBox = null; // {minLat, minLon, maxLat, maxLon} of the last rendered route
let lastRoutePath = null; // [{lat, lon}, ...] of the last rendered route, for territory transition lookups
const stops = []; // map markers
const waypoints = []; // input waypoints
let stopSeq = 1;
let waypointSeq = 1;
let lastAIResponse = null;

// Prevent Safari autofill on input fields
function preventAutofill() {
  // Create dynamic input fields to avoid Safari's autofill popup
  function createDynamicInput(containerId, fieldId, placeholder) {
    const container = document.getElementById(containerId);
    if (!container) return null;
    
    // Wrap input + clear button in a flex container
    const wrap = document.createElement('div');
    wrap.className = 'input-clear-wrap';

    const input = document.createElement('input');
    input.id = fieldId;
    input.type = 'text';
    input.placeholder = placeholder;
    input.className = 'dynamic-input';
    input.setAttribute('autocomplete', 'off');
    input.setAttribute('autocorrect', 'off');
    input.setAttribute('autocapitalize', 'off');
    input.setAttribute('spellcheck', 'false');
    input.setAttribute('data-lpignore', 'true');
    input.setAttribute('inputmode', 'search');
    input.setAttribute('aria-label', placeholder);

    const clearBtn = document.createElement('button');
    clearBtn.type = 'button';
    clearBtn.className = 'btn-input-clear';
    clearBtn.title = 'Eingabe löschen';
    clearBtn.setAttribute('aria-label', 'Eingabe löschen');
    clearBtn.textContent = '✕';
    clearBtn.addEventListener('click', () => {
      input.value = '';
      wrap.classList.remove('has-value');
      input.focus();
      // Hide suggestions
      const suggestEl = container.closest('.route-input-wrap')?.querySelector('.suggest');
      if (suggestEl) suggestEl.style.display = 'none';
    });

    wrap.appendChild(input);
    wrap.appendChild(clearBtn);
    container.appendChild(wrap);
    
    // Update has-value class and autofill detection
    let lastValue = '';
    let autofillTimer = null;
    function updateClearVisible() {
      wrap.classList.toggle('has-value', !!input.value);
    }
    input.addEventListener('input', (e) => {
      lastValue = e.target.value;
      updateClearVisible();
    });
    input.addEventListener('focus', () => {
      // Only watch while the input is focused; clear on blur to avoid leaking.
      autofillTimer = setInterval(() => {
        const currentValue = input.value;
        if (currentValue !== lastValue && currentValue.includes(' ')) {
          const isLikelyAutofill = /\d+|straße|str\.|platz|weg/i.test(currentValue) &&
                                    lastValue.length < 3;
          if (isLikelyAutofill) {
            input.value = lastValue;
          }
        }
        updateClearVisible();
      }, 100);
    });
    input.addEventListener('blur', () => {
      if (autofillTimer !== null) {
        clearInterval(autofillTimer);
        autofillTimer = null;
      }
      updateClearVisible();
    });
    
    return input;
  }
  
  const fromInput = createDynamicInput('from-container', 'from', 'Start: Adresse oder Koordinaten');
  const toInput = createDynamicInput('to-container', 'to', 'Ziel: Adresse oder Koordinaten');
}

// Call this immediately
preventAutofill();

// Theme Management
function initTheme() {
  const saved = localStorage.getItem('theme-mode');
  const prefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches;
  const isDark = saved ? saved === 'dark' : prefersDark;
  
  document.documentElement.classList.toggle('light-mode', !isDark);
  updateThemeButton();
}

function updateThemeButton() {
  const isDark = !document.documentElement.classList.contains('light-mode');
  const moon = document.getElementById('themeIconMoon');
  const sun  = document.getElementById('themeIconSun');
  if (moon) moon.style.display = isDark ? 'block' : 'none';
  if (sun)  sun.style.display  = isDark ? 'none'  : 'block';
}

function toggleTheme() {
  const root = document.documentElement;
  root.classList.toggle('light-mode');
  const isDark = !root.classList.contains('light-mode');
  localStorage.setItem('theme-mode', isDark ? 'dark' : 'light');
  updateThemeButton();
}

document.getElementById('themeToggle').addEventListener('click', toggleTheme);
initTheme();

// Builds the "📍 <label>" DOM element used for draggable waypoint stop
// markers (replaces the old pinIcon() L.divIcon helper — a MapLibre Marker
// takes a DOM element directly instead of an icon spec).
function pinElement(label, icon){
  return markerElement('map-marker map-pin-marker', `${icon || '📍'} ${label}`);
}

// ---- Custom markers (draggable stop pins): delete, move, rename, icon,
// persisted locally so they survive a page reload. ----
const CUSTOM_MARKER_ICONS = ['📍','🏠','🏢','⛽','🅿️','🚧','⭐','📦'];
const CUSTOM_MARKERS_STORAGE_KEY = 'osmmini-custom-markers';

function saveCustomMarkers() {
  try {
    const data = stops.map(s => ({ id: s.id, lat: s.lat, lon: s.lon, label: s.label, icon: s.icon }));
    localStorage.setItem(CUSTOM_MARKERS_STORAGE_KEY, JSON.stringify(data));
  } catch (e) { /* storage unavailable/full — non-fatal, markers just won't persist */ }
}

function stopPopupHtml(s) {
  const iconButtons = CUSTOM_MARKER_ICONS.map((ic) =>
    `<button type="button" class="btn btn-sm btn-outline icon-pick-btn" data-icon="${ic}" style="padding:4px 8px;font-size:14px;">${ic}</button>`
  ).join('');
  return `<div style="min-width:170px;">
    <strong>${escapeHtml(s.icon || '📍')} ${escapeHtml(s.label)}</strong>
    <div style="margin-top:6px;display:flex;flex-wrap:wrap;gap:4px;">${iconButtons}</div>
    <div style="margin-top:6px;display:flex;gap:4px;">
      <button type="button" class="btn btn-sm btn-outline rename-stop-btn" style="flex:1;">Umbenennen</button>
      <button type="button" class="btn btn-sm btn-outline delete-stop-btn" style="flex:1;">Löschen</button>
    </div>
  </div>`;
}

function deleteStopMarker(id) {
  const i = stops.findIndex(x => x.id === id);
  if (i < 0) return;
  const s = stops[i];
  s.marker.remove();
  stops.splice(i, 1);
  renderStopList();
  saveCustomMarkers();
  showToast(`Marker ${s.label} gelöscht`, 'info', 1500);
}

function deleteAllStopMarkers() {
  if (stops.length === 0) return;
  const count = stops.length;
  if (!window.confirm(`Wirklich alle ${count} Marker löschen?`)) return;

  while (stops.length) {
    const s = stops.pop();
    s.marker.remove();
  }
  stopSeq = 1;
  saveCustomMarkers();
  renderStopList();
  showToast(`${count} Marker gelöscht`, 'info', 1800);
}

function setStopAsDestination(s) {
  const value = formatLatLon(s?.lat, s?.lon);
  const toEl = document.getElementById('to');
  if (!value || !toEl) return;

  toEl.value = value;
  syncInputClearState('to');
  showToast(`Ziel gesetzt: ${s.label || s.id}`, 'success', 1800);
  compute();
}

// Wires the popup's buttons each time it opens (MapLibre Popups only emit
// 'open' once per open-transition, so this must run there — not after later
// content changes, which is why icon/rename actions below avoid touching the
// popup's own HTML and instead just update the marker + close it).
function wireStopPopup(popup, s) {
  const el = popup.getElement();
  if (!el) return;
  el.querySelectorAll('.icon-pick-btn').forEach((btn) => {
    btn.addEventListener('click', () => {
      s.icon = btn.dataset.icon;
      const markerEl = s.marker.getElement();
      if (markerEl) markerEl.textContent = `${s.icon} ${s.label}`;
      saveCustomMarkers();
      popup.remove();
    });
  });
  const renameBtn = el.querySelector('.rename-stop-btn');
  if (renameBtn) {
    renameBtn.addEventListener('click', () => {
      const next = window.prompt('Neuer Name für diesen Marker:', s.label);
      if (next === null) return;
      const trimmed = next.trim();
      if (!trimmed) return;
      s.label = trimmed;
      const markerEl = s.marker.getElement();
      if (markerEl) markerEl.textContent = `${s.icon || '📍'} ${s.label}`;
      renderStopList();
      saveCustomMarkers();
      popup.remove();
    });
  }
  const deleteBtn = el.querySelector('.delete-stop-btn');
  if (deleteBtn) {
    deleteBtn.addEventListener('click', () => {
      popup.remove();
      deleteStopMarker(s.id);
    });
  }
}

function createStopMarker(id, lat, lon, label, icon) {
  label = label || id;
  icon = icon || '📍';
  const marker = new maplibregl.Marker({ element: pinElement(label, icon), draggable: true })
    .setLngLat([lon, lat])
    .addTo(map);
  const s = { id, marker, lat, lon, label, icon };
  const popup = new maplibregl.Popup().setHTML(stopPopupHtml(s));
  popup.on('open', () => wireStopPopup(popup, s));
  marker.setPopup(popup);
  marker.on('dragend', () => {
    const ll = marker.getLngLat();
    s.lat = ll.lat;
    s.lon = ll.lng;
    renderStopList();
    saveCustomMarkers();
    showToast(`Marker ${s.label} verschoben`, 'info', 1500);
  });
  // MapLibre markers have no built-in contextmenu event (unlike Leaflet's
  // marker.on('contextmenu', ...)); listen on the underlying DOM element.
  // Kept as a power-user shortcut alongside the popup's "Löschen" button.
  marker.getElement().addEventListener('contextmenu', (domEv) => {
    domEv.preventDefault();
    deleteStopMarker(id);
  });
  return s;
}

function restoreCustomMarkers() {
  let saved;
  try {
    saved = JSON.parse(localStorage.getItem(CUSTOM_MARKERS_STORAGE_KEY) || '[]');
  } catch (e) {
    saved = [];
  }
  if (!Array.isArray(saved) || saved.length === 0) return;
  let maxSeq = 0;
  saved.forEach((entry) => {
    const coord = normalizeLatLon(entry.lat, entry.lon);
    if (!entry || !entry.id || !coord) return;
    stops.push(createStopMarker(entry.id, coord.lat, coord.lon, entry.label, entry.icon));
    const m = /^M(\d+)$/.exec(entry.id);
    if (m) maxSeq = Math.max(maxSeq, parseInt(m[1], 10));
  });
  stopSeq = Math.max(stopSeq, maxSeq + 1);
  renderStopList();
}

// Small colored dot DOM element — the closest MapLibre-native equivalent to
// Leaflet's L.circleMarker (a vector-drawn map-plane circle), used for the
// user-location dot and the route start/end markers.
function dotElement(color) {
  const el = markerElement('map-marker map-dot-marker');
  el.style.setProperty('--dot-color', color || '#3a8eef');
  return el;
}

function searchResultMarkerElement() {
  return markerElement('map-marker map-search-result-marker', '📍');
}

function syncStopIcons(orderIds) {
  const idToRank = new Map();
  orderIds.forEach((id, i) => idToRank.set(id, i+1));

  // Batch icon updates using requestAnimationFrame for better performance
  requestAnimationFrame(() => {
    stops.forEach((s, idx) => {
      const n = idToRank.get(s.id) || (idx+1);
      const el = s.marker.getElement();
      if (el) el.textContent = `${n}. ${s.icon || '📍'} ${s.label || s.id}`;
    });
  });
}

function renderStopList(orderIds) {
  const el = document.getElementById('stopList');
  const order = (orderIds && orderIds.length) ? orderIds : stops.map(s => s.id);

  // Use requestAnimationFrame for smooth rendering
  requestAnimationFrame(() => {
    el.innerHTML = '';

    if (order.length === 0) {
      el.innerHTML = '<div style="padding:10px; color:#8aaedc; font-style:italic; font-size:12px;">Keine Marker auf der Karte</div>';
      return;
    }

    const toolbar = document.createElement('div');
    toolbar.className = 'stop-list-toolbar';
    const title = document.createElement('span');
    title.className = 'stop-list-title';
    title.textContent = `Marker (${stops.length})`;
    toolbar.appendChild(title);

    const clearAllBtn = document.createElement('button');
    clearAllBtn.type = 'button';
    clearAllBtn.className = 'stop-list-clear-all';
    clearAllBtn.title = 'Alle Marker löschen';
    clearAllBtn.textContent = 'Alle löschen';
    clearAllBtn.addEventListener('click', deleteAllStopMarkers);
    toolbar.appendChild(clearAllBtn);
    el.appendChild(toolbar);

    order.forEach(id => {
    const s = stops.find(x => x.id === id);
    if (!s) return;
    const item = document.createElement('div');
    item.className = 'stop-item';
    const main = document.createElement('div');
    main.className = 'stop-item-main';
    const name = document.createElement('span');
    name.className = 'stop-item-name';
    name.textContent = `${s.icon || '📍'} ${s.label || id}`;
    const coords = document.createElement('span');
    coords.className = 'stop-item-coords';
    coords.textContent = `${s.lat.toFixed(4)}, ${s.lon.toFixed(4)}`;
    main.append(name, coords);

    const actions = document.createElement('div');
    actions.className = 'stop-item-actions';
    const destinationBtn = document.createElement('button');
    destinationBtn.type = 'button';
    destinationBtn.className = 'stop-list-action stop-list-destination';
    destinationBtn.title = 'Marker als Ziel übernehmen';
    destinationBtn.textContent = 'Als Ziel';
    destinationBtn.addEventListener('click', (event) => {
      event.stopPropagation();
      setStopAsDestination(s);
    });
    const deleteBtn = document.createElement('button');
    deleteBtn.type = 'button';
    deleteBtn.className = 'stop-list-action stop-list-delete';
    deleteBtn.title = 'Marker löschen';
    deleteBtn.setAttribute('aria-label', `${s.label || id} löschen`);
    deleteBtn.textContent = '×';
    deleteBtn.addEventListener('click', (event) => {
      event.stopPropagation();
      deleteStopMarker(s.id);
    });
    actions.append(destinationBtn, deleteBtn);
    item.append(main, actions);
    item.title = 'Klicken zum Zentrieren';
    item.style.cursor = 'pointer';
    item.onclick = () => map.panTo([s.lon, s.lat]);
    el.appendChild(item);
    });
  });
}

function setMapsLinks(g, a) {
  const gEl = document.getElementById('gmaps');
  const aEl = document.getElementById('amaps');
  if (g) { gEl.href = g; gEl.style.display = 'inline-block'; } else { gEl.style.display = 'none'; }
  if (a) { aEl.href = a; aEl.style.display = 'inline-block'; } else { aEl.style.display = 'none'; }
}

function routeOptionsFromUI(){
  return {
    engine: document.getElementById('engine').value,
    objective: document.getElementById('objective').value,
    profile: document.getElementById('profile')?.value || '',
    pro: document.getElementById('pro').checked,
    emergency_mode: document.getElementById('emergencyMode').checked,
    weights: {
      left_turn: parseFloat(document.getElementById('w_left').value) || 0,
      right_turn: parseFloat(document.getElementById('w_right').value) || 0,
      no_left_turn: document.getElementById('noLeftTurn').checked,
      traffic_light_penalty: parseFloat(document.getElementById('w_traffic_light').value) || 0
    }
  };
}

// Cache for API responses
const apiCache = new Map();
const CACHE_TTL = 30000; // 30 seconds

function getCachedOrFetch(key, fetchFn) {
  const cached = apiCache.get(key);
  if (cached && Date.now() - cached.timestamp < CACHE_TTL) {
    return Promise.resolve(cached.data);
  }
  return fetchFn().then(data => {
    apiCache.set(key, { data, timestamp: Date.now() });
    return data;
  });
}

async function apiGetSettings() {
  return getCachedOrFetch('settings', async () => {
    const res = await fetch('/api/v1/settings', {
      headers: { 'Accept': 'application/json' },
      cache: 'default'
    });
    if (!res.ok) throw new Error('settings fetch failed');
    return res.json();
  });
}

// Administrative endpoints share the session-only token configured in the
// settings panel. Keeping this in one helper makes local maintenance actions
// (such as building a tinyTiles artifact) behave exactly like saving settings.
function adminAuthHeaders(headers = {}) {
  // Prefer a token that was just entered in the visible field. This permits a
  // protected maintenance action before the user has saved unrelated settings.
  const entered = document.getElementById('adminToken')?.value.trim() || '';
  const token = entered || sessionStorage.getItem('osmminiAdminToken') || '';
  if (token) headers.Authorization = `Bearer ${token}`;
  return headers;
}

async function apiPutSettings(settings) {
  const headers = adminAuthHeaders({'Content-Type':'application/json'});
  const res = await fetch('/api/v1/settings', {
    method: 'PUT',
    headers,
    body: JSON.stringify(settings)
  });
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body.error || `settings save failed (${res.status})`);
  }
  return res.json();
}

async function apiRoute(from,to,options){
  const res = await fetch('/api/v1/route',{method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({from:{query:from}, to:{query:to}, options})});
  if(!res.ok){
    const err = await res.json().catch(()=>({}));
    const ex = new Error(err.error || res.statusText);
    ex.details = err;
    throw ex;
  }
  return res.json();
}

async function apiTripSolve(from,to,options){
  const optimize = document.getElementById('optimize').checked;
  const allStops = [];
  waypoints.forEach(wp=>{
    const v=wp.input.value.trim();
    if(!v) return;
    const demand = parseFloat(wp.demandInput?.value) || 0;
    allStops.push({id:wp.id, location:{query:v}, demand: demand || undefined});
  });
  stops.forEach(s=> allStops.push({id:s.id, location:{lat:s.lat, lon:s.lon}}));
  const vehicleCapacity = parseFloat(document.getElementById('vehicleCapacity')?.value) || 0;
  const plan = { start:{query:from}, end:{query:to}, stops: allStops, dependencies:[], optimize, vehicle_capacity: vehicleCapacity || undefined };
  const res = await fetch('/api/v1/trip/solve', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({plan, options})});
  if(!res.ok){
    const err = await res.json().catch(()=>({}));
    const ex = new Error(err.error || res.statusText);
    ex.details = err;
    throw ex;
  }
  return res.json();
}

function renderDisambiguationButtons(details) {
  if (!details || !Array.isArray(details.suggestions) || details.suggestions.length === 0) return;
  const messagesEl = document.getElementById('aiMessages');
  if (!messagesEl) return;

  const target = details.target === 'from' ? 'from' : 'to';
  const query = details.query || '';

  const wrapper = document.createElement('div');
  wrapper.className = 'ai-message ai-assistant';

  const header = document.createElement('div');
  header.style.fontSize = '11px';
  header.style.color = 'var(--text-muted)';
  header.style.marginBottom = '6px';
  header.textContent = 'Mehrdeutiges Ziel';
  wrapper.appendChild(header);

  const text = document.createElement('div');
  text.style.fontSize = '13px';
  text.style.marginBottom = '8px';
  text.innerHTML = `Ich bin nicht sicher, welches ${target === 'from' ? 'Start' : 'Ziel'} gemeint ist${query ? ` (<strong>${escapeHtml(query)}</strong>)` : ''}. Bitte auswählen:`;
  wrapper.appendChild(text);

  const btnRow = document.createElement('div');
  btnRow.style.display = 'flex';
  btnRow.style.flexWrap = 'wrap';
  btnRow.style.gap = '6px';

  details.suggestions.slice(0, 6).forEach((sug) => {
    const btn = document.createElement('button');
    btn.className = 'btn';
    btn.style.padding = '6px 10px';
    btn.style.fontSize = '12px';
    const sugCoord = normalizeSearchResult(sug);
    btn.textContent = getResultInputValue(sug) || (sugCoord ? `${sugCoord.lat.toFixed(5)}, ${sugCoord.lon.toFixed(5)}` : '');
    btn.title = [sug.kind || 'Treffer', getResultSecondary(sug)].filter(Boolean).join(' • ');
    btn.addEventListener('click', async () => {
          const val = getResultInputValue(sug) || (sugCoord ? `${sugCoord.lat},${sugCoord.lon}` : '');
      const el = document.getElementById(target);
      if (el) el.value = val;
      try {
        if (sugCoord) map.panTo([sugCoord.lon, sugCoord.lat]);
      } catch (e) {}
      showToast(`${target === 'from' ? 'Start' : 'Ziel'} gesetzt: ${val}`, 'success', 1800);
      try { await compute(); } catch (e) { console.warn('compute after disambiguation failed', e); }
    });
    btnRow.appendChild(btn);
  });

  wrapper.appendChild(btnRow);
  messagesEl.appendChild(wrapper);
  messagesEl.scrollTop = messagesEl.scrollHeight;
}

function renderPath(path, meta){
  const coords = path.map(p=>[p.lat,p.lon]);
  if(polyline) polyline.remove();
  if(startMarker) startMarker.remove(); if(endMarker) endMarker.remove();
  lastRoutePath = path;
  if(coords.length===0) { updateTerritoryRouteTransitions(null); return; }
  // Track route bounding box for poi_on_route queries
  currentRouteBBox = coords.reduce((bb, c) => ({
    minLat: Math.min(bb.minLat, c[0]),
    minLon: Math.min(bb.minLon, c[1]),
    maxLat: Math.max(bb.maxLat, c[0]),
    maxLon: Math.max(bb.maxLon, c[1]),
  }), {minLat: coords[0][0], minLon: coords[0][1], maxLat: coords[0][0], maxLon: coords[0][1]});
  polyline = makeRouteLine(coords.map(c => [c[1], c[0]]));
  startMarker = new maplibregl.Marker({ element: dotElement('#6ef2a0'), anchor: 'center' }).setLngLat([coords[0][1], coords[0][0]]).addTo(map);
  endMarker = new maplibregl.Marker({ element: dotElement('#ffcc66'), anchor: 'center' }).setLngLat([coords[coords.length-1][1], coords[coords.length-1][0]]).addTo(map);
  map.fitBounds(polyline.getBounds(),{padding:40});
  
  const distKm = (meta.distance_m / 1000).toFixed(2);
  const durMin = Math.round(meta.duration_s / 60);
  const durHours = Math.floor(durMin / 60);
  const durMins = durMin % 60;
  const durationText = durHours > 0 ? `${durHours}h ${durMins}min` : `${durMin} min`;
  
  document.getElementById('distance').innerHTML = `<strong>${distKm} km</strong> • ${durationText}`;
  
  // Show detailed route info
  const detailsEl = document.getElementById('routeDetails');
  if (detailsEl) {
    detailsEl.style.display = 'block';
    document.getElementById('detailDistance').textContent = `${distKm} km`;
    document.getElementById('detailDuration').textContent = durationText;
    const eta = new Date(Date.now() + meta.duration_s * 1000);
    document.getElementById('detailETA').textContent = eta.toLocaleTimeString('de-DE', {hour: '2-digit', minute: '2-digit'});
    document.getElementById('detailEngine').textContent = meta.engine || 'astar';
    const computeEl = document.getElementById('detailComputeMs');
    const computePill = document.getElementById('detailComputePill');
    if (computeEl && computePill) {
      if (meta.cached) {
        computeEl.textContent = 'aus Cache';
      } else if (typeof meta.compute_ms === 'number') {
        computeEl.textContent = meta.compute_ms < 1 ? '<1 ms' : `${Math.round(meta.compute_ms)} ms`;
      } else {
        computeEl.textContent = '—';
      }
      computePill.style.display = (meta.cached || typeof meta.compute_ms === 'number') ? '' : 'none';
    }
  }
  
  // Show route actions
  const actionsEl = document.getElementById('routeActions');
  if (actionsEl) actionsEl.style.display = 'flex';

  updateTerritoryRouteTransitions(path);
}

async function compute() {
  const from = document.getElementById('from').value.trim();
  const to = document.getElementById('to').value.trim();
  // Sync clear-button visibility for main inputs (covers programmatic value sets)
  syncInputClearState('from');
  syncInputClearState('to');
  const options = routeOptionsFromUI();
  document.getElementById('status').textContent = 'Berechne...';
  setMapsLinks('', '');
  showSpinner(true);
  setComputeDisabled(true);
  
  // Hide route details while computing
  const detailsEl = document.getElementById('routeDetails');
  const actionsEl = document.getElementById('routeActions');
  if (detailsEl) detailsEl.style.display = 'none';
  if (actionsEl) actionsEl.style.display = 'none';

  try {
    const hasWaypoints = waypoints.some(wp => wp.input.value.trim() !== '');
    const hasStops = stops.length > 0;
    
    if (!hasWaypoints && !hasStops) {
      if (!from || !to) {
        document.getElementById('status').textContent = 'Bereit';
        showToast('Bitte Start und Ziel eingeben', 'info');
        return;
      }
      const data = await apiRoute(from, to, options);
      renderPath(data.path, data);
      // A bare "lat,lon" query (used instead of a free-text label to avoid
      // ambiguous server-side resolution, see resultCoordValue()) is
      // upgraded to the resolved place name once the route confirms it.
      const coordPattern = /^-?\d+\.?\d*,-?\d+\.?\d*$/;
      const fromEl = document.getElementById('from');
      const toEl = document.getElementById('to');
      if (fromEl && coordPattern.test(from) && data.from?.label) {
        fromEl.value = data.from.label;
        syncInputClearState('from');
      }
      if (toEl && coordPattern.test(to) && data.to?.label) {
        toEl.value = data.to.label;
        syncInputClearState('to');
      }
      // ensure maneuvers shown from response (top-level steps)
      (function(){
        let steps = data.steps || null;
        if ((!steps || steps.length === 0) && data.legs && Array.isArray(data.legs)) {
          steps = [];
          data.legs.forEach(l => { if (l && l.steps) steps = steps.concat(l.steps); });
        }
        if (steps && steps.length) renderManeuvers(steps);
      })();
      document.getElementById('status').textContent = '✅ Route gefunden';
      renderStopList([]);
      setMapsLinks(data.google_maps_url, data.apple_maps_url);
      showToast(`Route berechnet: ${(data.distance_m/1000).toFixed(1)} km`, 'success', 2000);
    } else {
      const data = await apiTripSolve(from, to, options);
      renderPath(data.path, data);
      // aggregate maneuvers from legs if present
      (function(){
        let steps = data.steps || null;
        if ((!steps || steps.length === 0) && data.legs && Array.isArray(data.legs)) {
          steps = [];
          data.legs.forEach(l => { if (l && l.steps) steps = steps.concat(l.steps); });
        }
        if (steps && steps.length) renderManeuvers(steps);
      })();
      const optText = document.getElementById('optimize').checked ? 'optimiert' : 'fix';
      document.getElementById('status').textContent = '✅ Trip (' + optText + ')';
      syncStopIcons(data.order || []);
      renderStopList(data.order || []);
      setMapsLinks(data.google_maps_url, data.apple_maps_url);
      showToast(`Trip berechnet mit ${stops.length + waypoints.filter(w=>w.input.value.trim()).length} Stops`, 'success', 2000);
      if (data.capacity_warning) {
        showToast('⚠️ ' + data.capacity_warning, 'error', 6000);
      } else if (data.vehicle_capacity) {
        showToast(`Ladung: ${data.total_demand} / ${data.vehicle_capacity}`, 'info', 3000);
      }
    }
  } catch (e) {
    document.getElementById('status').textContent = '❌ Fehler';
    try {
      if (e && e.details && Array.isArray(e.details.suggestions) && e.details.suggestions.length > 0) {
        renderDisambiguationButtons(e.details);
        showToast('Mehrdeutiges Ziel: Bitte einen Vorschlag auswählen', 'info', 2600);
      }
    } catch (de) { /* non-fatal */ }
    showToast(e.message || 'Fehler bei der Routenberechnung', 'error', 4000);
    console.error(e);
  }
  finally {
    showSpinner(false);
    setComputeDisabled(false);
  }
}

function formatMeters(m) {
  if (m >= 1000) return (m/1000).toFixed(1) + ' km';
  return Math.round(m) + ' m';
}

function formatDuration(s) {
  if (!s) return '';
  const mins = Math.round(s/60);
  if (mins >= 60) {
    const h = Math.floor(mins/60);
    const m = mins % 60;
    return `${h}h ${m}m`;
  }
  return `${mins} min`;
}

function iconForType(t) {
  switch(t) {
    case 'turn-left': return '⬅️';
    case 'turn-right': return '➡️';
    case 'uturn': return '⤴️';
    case 'depart': return '🚦';
    case 'arrive': return '🏁';
    case 'continue': return '➡️';
    default: return '➡️';
  }
}

function renderManeuvers(steps) {
  const el = document.getElementById('maneuvers');
  if (!el) return;
  el.innerHTML = '';
  if (!steps || steps.length === 0) { el.style.display = 'none'; return; }
  const list = document.createElement('div');
  list.className = 'maneuver-list';
  steps.forEach((s) => {
    const row = document.createElement('div');
    row.className = 'maneuver-row';

    const left = document.createElement('div');
    left.className = 'maneuver-left';

    const ic = document.createElement('div');
    ic.className = 'maneuver-icon';
    ic.textContent = iconForType(s.type || s.Type || '');

    const txt = document.createElement('div');
    txt.className = 'maneuver-text';
    txt.innerHTML = `<div class="maneuver-instr">${escapeHtml(s.instruction || s.Instruction || '')}</div>`
                  + `<div class="maneuver-type">${escapeHtml((s.type||s.Type||'').toString())}</div>`;
    left.append(ic, txt);

    const right = document.createElement('div');
    right.className = 'maneuver-right';
    right.innerHTML = `<div class="maneuver-dist">${formatMeters(s.distance_m || s.DistanceM || 0)}</div>`
                    + `<div class="maneuver-dur">${formatDuration(s.duration_s || s.DurationS || 0)}</div>`;

    row.append(left, right);
    row.addEventListener('click', () => {
      const lat = s.lat || s.Lat; const lon = s.lon || s.Lon;
      if (lat && lon) map.panTo([lon, lat]);
    });
    list.appendChild(row);
  });
  el.appendChild(list);
  el.style.display = 'block';
}

document.getElementById('go').addEventListener('click', e=>{ e.preventDefault(); compute(); });
document.getElementById('clear').addEventListener('click', () => {
  // Clear map markers and route polyline
  while(stops.length){ const s=stops.pop(); s.marker.remove(); } 
  stopSeq=1; 
  renderStopList(); 
  if(polyline) polyline.remove();
  if(startMarker) startMarker.remove();
  if(endMarker) endMarker.remove();
  clearSearchResults();
  // Clear input fields
  const fromEl = document.getElementById('from');
  const toEl   = document.getElementById('to');
  if (fromEl) {
    fromEl.value = '';
    fromEl.closest('.input-clear-wrap')?.classList.remove('has-value');
  }
  if (toEl) {
    toEl.value = '';
    toEl.closest('.input-clear-wrap')?.classList.remove('has-value');
  }
  // Clear waypoints
  [...waypoints].forEach(w => removeWaypoint(w.id));
  document.getElementById('status').textContent = 'Bereit';
  document.getElementById('distance').textContent = '';
  const detailsEl = document.getElementById('routeDetails');
  const actionsEl = document.getElementById('routeActions');
  const maneuversEl = document.getElementById('maneuvers');
  if (detailsEl) detailsEl.style.display = 'none';
  if (actionsEl) actionsEl.style.display = 'none';
  if (maneuversEl) maneuversEl.style.display = 'none';
  setMapsLinks('', '');
  showToast('Zurückgesetzt', 'info', 1500);
});

// Route control buttons
document.getElementById('zoomToRoute')?.addEventListener('click', () => {
  if (polyline) {
    map.fitBounds(polyline.getBounds(), {padding: 40});
    showToast('Route zentriert', 'info', 1500);
  }
});

document.getElementById('clearRoute')?.addEventListener('click', () => {
  if(polyline) polyline.remove();
  if(startMarker) startMarker.remove();
  if(endMarker) endMarker.remove();
  const detailsEl = document.getElementById('routeDetails');
  const actionsEl = document.getElementById('routeActions');
  if (detailsEl) detailsEl.style.display = 'none';
  if (actionsEl) actionsEl.style.display = 'none';
  document.getElementById('status').textContent = 'Bereit';
  document.getElementById('distance').textContent = '';
  setMapsLinks('', '');
  showToast('Route gelöscht', 'info', 1500);
});

document.getElementById('exportRoute')?.addEventListener('click', () => {
  if (!polyline) {
    showToast('Keine Route zum Exportieren', 'error', 2000);
    return;
  }
  const coords = polyline.getLatLngs();
  const geojson = {
    type: 'Feature',
    geometry: {
      type: 'LineString',
      coordinates: coords.map(c => [c.lng, c.lat])
    },
    properties: {
      name: 'OSMmini Route',
      distance_m: document.getElementById('detailDistance')?.textContent || '',
      duration: document.getElementById('detailDuration')?.textContent || '',
      engine: document.getElementById('detailEngine')?.textContent || '',
      timestamp: new Date().toISOString()
    }
  };
  const blob = new Blob([JSON.stringify(geojson, null, 2)], {type: 'application/json'});
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = `osmmini-route-${Date.now()}.geojson`;
  a.click();
  URL.revokeObjectURL(url);
  showToast('Route als GeoJSON exportiert', 'success', 2000);
});

// --- Agent action executor ---
async function postAgentExecute(actions, session, confirm=false, dry_run=false) {
  const body = { actions, session_id: session, confirm: confirm, dry_run: dry_run };
  const res = await fetch('/api/v1/agent/execute', { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify(body) });
  if (!res.ok) {
    const err = await res.json().catch(()=>({})); throw new Error(err.error || res.statusText);
  }
  return res.json();
}

async function fetchPoiPayload(id) {
  const qlat = userLocation ? userLocation.lat : null;
  const qlon = userLocation ? userLocation.lon : null;
  const qs = (qlat !== null && qlon !== null) ? `?lat=${qlat}&lon=${qlon}` : '';
  const res = await fetch(`/api/v1/poi/${id}${qs}`);
  if (!res.ok) return null;
  return res.json();
}

function describeSearchResultDistance(item) {
  return item && item.distance_m ? `${Math.round(item.distance_m)} m` : '';
}

function summarizePoiAction(type, item) {
  const label = item.label || (item.id == null ? 'POI' : `#${item.id}`);
  const distance = describeSearchResultDistance(item);
  return `${type}: ${label} ${distance}`.trim();
}

async function resolveRouteEndpointValue(value) {
  if (!value) return '';
  if (value.query) return String(value.query);

  const coord = formatLatLon(value.lat, value.lon);
  if (coord) return coord;

  const id = value.id;
  if (id == null) return '';
  try {
    const data = await fetchPoiPayload(id);
    if (!data) return '';
    if (data.label) return data.label;
    return formatLatLon(data.lat, data.lon);
  } catch (_) {
    return '';
  }
}

async function addMarkerForPoiAction(id) {
  if (id == null) return null;
  const data = await fetchPoiPayload(id);
  if (!data) return null;
  const markerData = normalizeSearchResult(data);
  if (!markerData) return null;
  const marker = createSearchResultMarker(markerData);
  if (!marker) return null;
  return { marker, markerData };
}

// Execute a list of actions returned by the agent. If actions include
// compute_route we ask for confirmation and call server execute to compute.
async function executeAgentActions(actions, session_id) {
  if (!Array.isArray(actions)) return;
  // handle non-routing actions immediately and collect summaries
  const summaries = [];
  for (const act of actions) {
    const t = act.type || act.Type || '';
    const params = act.params || {};
    if (t === 'highlight_poi') {
      try {
        const id = params.id;
        const result = await addMarkerForPoiAction(id);
        if (result?.markerData && result.marker) {
          result.marker.addTo(map);
          result.marker.togglePopup();
          searchResultMarkers.push(result.marker);
          summaries.push(summarizePoiAction('Hervorgehoben', result.markerData));
        }
      } catch (e) { console.warn('highlight failed', e); summaries.push('Hervorhebung fehlgeschlagen'); }
    } else if (t === 'show_info') {
      try {
        const id = params.id;
        const result = await addMarkerForPoiAction(id);
        if (result?.markerData && result.marker) {
          result.marker.addTo(map);
          result.marker.togglePopup();
          searchResultMarkers.push(result.marker);
          map.panTo([result.markerData.lon, result.markerData.lat]);
          summaries.push(summarizePoiAction('Info angezeigt', result.markerData));
        }
      } catch (e) { console.warn('show_info failed', e); summaries.push('Details konnten nicht geladen werden'); }
    }
  }

  // append a concise assistant message summarizing non-routing actions
  try {
    const messagesEl = document.getElementById('aiMessages');
    if (summaries.length > 0 && messagesEl) {
      const assist = document.createElement('div');
      assist.className = 'ai-message ai-assistant';
      assist.innerHTML = `<div style="font-size:11px;color:var(--text-muted);margin-bottom:4px;">lokaler Agent — Aktionen</div><div style="font-size:13px;">${escapeHtml(summaries.join(' • '))}</div>`;
      messagesEl.appendChild(assist);
      messagesEl.scrollTop = messagesEl.scrollHeight;
    }
  } catch (e) { /* non-fatal */ }

  // If any compute_route actions exist, populate the #from and #to inputs
  const computeActions = actions.filter(a => (a.type||a.Type) === 'compute_route');
  if (computeActions.length === 0) return;

  try {
    // Use the first compute_route action to populate the form (common case)
    const c = computeActions[0];
    const params = c.params || {};
    // determine from value
    const fromVal = await resolveRouteEndpointValue(params.from);
    // determine to value; if id present, try to fetch POI label
    const toVal = await resolveRouteEndpointValue(params.to);

    // Populate the form fields
    try {
      if (fromVal) {
        const fe = document.getElementById('from'); if (fe) { fe.value = fromVal; syncInputClearState('from'); }
      }
      if (toVal) {
        const te = document.getElementById('to'); if (te) { te.value = toVal; syncInputClearState('to'); }
      }
      showToast('Agent: Formular mit Quelle und Ziel ausgefüllt', 'success', 2200);

      // append an assistant message summarizing what was filled
      const messagesEl = document.getElementById('aiMessages');
      if (messagesEl) {
        const assist = document.createElement('div');
        assist.className = 'ai-message ai-assistant';
        const parts = [];
        if (fromVal) parts.push('Quelle: ' + escapeHtml(fromVal));
        if (toVal) parts.push('Ziel: ' + escapeHtml(toVal));
        assist.innerHTML = `<div style="font-size:11px;color:var(--text-muted);margin-bottom:4px;">lokaler Agent — Formular ausgefüllt</div><div style="font-size:13px;">${parts.join(' • ')}</div>`;
        messagesEl.appendChild(assist);
        messagesEl.scrollTop = messagesEl.scrollHeight;
      }

      // automatically trigger route computation after populating the form
      try {
        setTimeout(() => {
          compute();
        }, 200);
      } catch (e) { console.warn('auto compute failed', e); }
    } catch (e) { console.warn('Failed to populate form', e); }
  } catch (e) {
    console.error('Agent compute handling failed', e);
    showToast('Agent konnte Quelle/Ziel nicht einfügen', 'error', 4000);
  }
}

// Export for quick manual testing from console
window.executeAgentActions = executeAgentActions;

map.on('click', ev=>{
  // Marker/popup DOM elements don't stop click propagation to the map by
  // default, so without this guard every click on an existing marker (a
  // fire station, hydrant, search result, or another stop) or its popup
  // buttons would also drop a brand-new stop marker at that spot.
  const target = ev.originalEvent && ev.originalEvent.target;
  if (target && target.closest && target.closest('.maplibregl-marker, .maplibregl-popup')) return;
  const id = 'M'+(stopSeq++);
  const s = createStopMarker(id, ev.lngLat.lat, ev.lngLat.lng);
  stops.push(s);
  renderStopList();
  saveCustomMarkers();
  showToast(`Marker ${id} hinzugefügt`, 'success', 1500);
});

restoreCustomMarkers();

makeSuggest('from-suggest','from'); 
makeSuggest('to-suggest','to');

function addWaypoint() {
  const id = 'WP' + (waypointSeq++);
  const container = document.getElementById('waypointsContainer');
  
  const wrapper = document.createElement('div');
  wrapper.className = 'waypoint';
  wrapper.id = 'waypoint-' + id;
  
  // Wrap input + suggest in a flex group for proper positioning
  const inputGroup = document.createElement('div');
  inputGroup.className = 'input-group route-input-wrap';
  inputGroup.style.flex = '1';
  inputGroup.style.marginBottom = '0';
  inputGroup.style.position = 'relative';
  
  const suggestDiv = document.createElement('div');
  suggestDiv.className = 'suggest';
  suggestDiv.id = id + '-suggest';

  const input = document.createElement('input');
  input.type = 'text';
  input.placeholder = 'Zwischenstopp: Adresse oder Koordinaten';
  input.setAttribute('autocomplete', 'off');
  input.setAttribute('autocorrect', 'off');
  input.setAttribute('spellcheck', 'false');
  input.setAttribute('inputmode', 'search');
  input.setAttribute('aria-label', 'Zwischenstopp ' + id);
  
  inputGroup.appendChild(suggestDiv);
  inputGroup.appendChild(input);

  // Optional per-stop load, compared against Einstellungen > Trip-Optionen >
  // Fahrzeugkapazität on the server (Lieferdienst/Spedition use case).
  const demandInput = document.createElement('input');
  demandInput.type = 'number';
  demandInput.step = '1';
  demandInput.min = '0';
  demandInput.placeholder = 'Ladung';
  demandInput.title = 'Ladung an diesem Stopp (optional, vergleiche Fahrzeugkapazität)';
  demandInput.className = 'waypoint-demand-input';
  demandInput.setAttribute('aria-label', 'Ladung an Zwischenstopp ' + id);

  const removeBtn = document.createElement('button');
  removeBtn.textContent = '✕';
  removeBtn.className = 'btn-remove';
  removeBtn.title = 'Entfernen';
  removeBtn.setAttribute('aria-label', 'Zwischenstopp entfernen');
  removeBtn.onclick = () => removeWaypoint(id);

  wrapper.appendChild(inputGroup);
  wrapper.appendChild(demandInput);
  wrapper.appendChild(removeBtn);
  container.appendChild(wrapper);

  const suggestHandle = makeSuggest(id + '-suggest', input);
  waypoints.push({id, input, wrapper, suggestHandle, demandInput});
  
  input.addEventListener('change', debouncedCompute);
  
  // Also trigger on Enter key for immediate compute
  input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') {
      e.preventDefault();
      compute();
    }
  });

  input.focus();
}

// helper: add a waypoint and set its input value
function addWaypointWithValue(val) {
  addWaypoint();
  const wp = waypoints[waypoints.length - 1];
  if (wp && wp.input) {
    wp.input.value = val;
    debouncedCompute();
  }
  return wp;
}

function removeWaypoint(id) {
  const idx = waypoints.findIndex(w => w.id === id);
  if (idx >= 0) {
    waypoints[idx].suggestHandle?.destroy();
    waypoints[idx].wrapper.remove();
    waypoints.splice(idx, 1);
    compute();
  }
}

function makeSuggest(containerId, inputOrId) {
  const container = document.getElementById(containerId);
  const input = typeof inputOrId === 'string' ? document.getElementById(inputOrId) : inputOrId;
  if (!input || !container) return;
  
  let timeout = null;
  let seq = 0;
  let ctrl = null;
  let selectedIndex = -1;

  function hide() { container.style.display = 'none'; container.innerHTML = ''; }
  function show() { if (container.innerHTML.trim()) container.style.display = 'block'; }

  let lastQuery = '';
  input.addEventListener('input', () => {
    const q = input.value.trim();
    if (timeout) clearTimeout(timeout);
    if (ctrl) { ctrl.abort(); ctrl = null; }
    if (q.length < 2) { hide(); return; }
    if (q === lastQuery) return; // Skip duplicate queries
    lastQuery = q;

    // Show loading indicator immediately using DOM APIs (no innerHTML)
    container.innerHTML = '';
    const loadingItem = document.createElement('div');
    loadingItem.className = 'item suggest-loading';
    loadingItem.textContent = 'Suche\u2026';
    container.appendChild(loadingItem);
    container.style.display = 'block';
    selectedIndex = -1;

    timeout = setTimeout(async () => {
      const mySeq = ++seq;
      ctrl = new AbortController();
      try {
        const res = await fetch('/api/v1/search?limit=6&q=' + encodeURIComponent(q), { signal: ctrl.signal });
        if (!res.ok) {
          hide();
          showToast('Suche fehlgeschlagen (Fehler ' + res.status + ')', 'error', 2500);
          return;
        }
        const data = await res.json();
        if (mySeq !== seq) return;
        container.innerHTML = '';
        selectedIndex = -1;
        if (!Array.isArray(data) || data.length === 0) { hide(); return; }

        data.slice(0, 6).forEach((item, i) => {
          const el = document.createElement('div');
          el.className = 'item';
          el.dataset.index = String(i);
          const primary = getResultPrimary(item);
          const secondary = getResultSecondary(item);

          // highlight matches and show primary + secondary using DOM (safe, no XSS)
          const curQ = input.value.trim();
          const col = document.createElement('div');
          col.className = 'suggest-item-col';

          const primEl = document.createElement('div');
          primEl.className = 'suggest-item-primary';
          primEl.innerHTML = curQ ? highlight(primary || item.label || '', curQ) : escapeHtml(primary || item.label || '');
          col.appendChild(primEl);

          if (secondary) {
            const secEl = document.createElement('div');
            secEl.className = 'suggest-item-secondary';
            secEl.innerHTML = curQ ? highlight(secondary, curQ) : escapeHtml(secondary);
            col.appendChild(secEl);
          }

          el.appendChild(col);
          el.setAttribute('role', 'option');
          el.setAttribute('aria-selected', 'false');
          el.addEventListener('mouseover', () => { selectedIndex = i; updateActive(); });
          el.onclick = () => {
            const val = resultCoordValue(item) || primary || '';
            input.value = val;
            // update clear-button visibility
            const wrap = input.closest('.input-clear-wrap');
            if (wrap) wrap.classList.toggle('has-value', !!val);
            hide();
            compute();
            input.focus();
          };
          container.appendChild(el);
        });
        // also show all returned results on the map
        try { showSearchResultsOnMap(data); } catch(e) {}
        updateActive();
        show();
      } catch (err) {
        if (err && err.name === 'AbortError') return;
        hide();
        showToast('Suche fehlgeschlagen: ' + (err && err.message ? err.message : 'Netzwerkfehler'), 'error', 2500);
      }
    }, 220);
  });

  // keyboard navigation for suggestions
  input.addEventListener('keydown', (ev) => {
    if (ev.key === 'Escape') { hide(); return; }
    const items = Array.from(container.querySelectorAll('.item:not(.suggest-loading)'));
    if (!items.length) return;
    if (ev.key === 'ArrowDown') {
      ev.preventDefault();
      selectedIndex = Math.min(selectedIndex + 1, items.length - 1);
      updateActive();
    } else if (ev.key === 'ArrowUp') {
      ev.preventDefault();
      selectedIndex = Math.max(selectedIndex - 1, 0);
      updateActive();
    } else if (ev.key === 'Enter') {
      if (selectedIndex >= 0 && items[selectedIndex]) {
        ev.preventDefault(); items[selectedIndex].click();
      }
    }
  });

  function updateActive() {
    const items = Array.from(container.querySelectorAll('.item:not(.suggest-loading)'));
    items.forEach((it, idx) => {
      const active = idx === selectedIndex;
      it.classList.toggle('active', active);
      it.setAttribute('aria-selected', active ? 'true' : 'false');
    });
    // ensure active item is visible
    const active = container.querySelector('.item.active');
    if (active) active.scrollIntoView({block: 'nearest'});
  }

  function onDocumentClick(ev) {
    if (!container.contains(ev.target) && ev.target !== input) hide();
  }
  document.addEventListener('click', onDocumentClick);

  return {
    destroy() {
      document.removeEventListener('click', onDocumentClick);
      if (timeout) clearTimeout(timeout);
      if (ctrl) ctrl.abort();
    }
  };
}

const SEARCH_SOURCE_ID = 'search-results';
const SEARCH_CLUSTER_MAX_ZOOM = 16;
const SEARCH_CLUSTER_RADIUS = 40;

function clearSearchResults() {
  removeMarkers(searchClusterRenderedMarkers);
  removeMarkers(searchResultMarkers);
  lastSearchResults = [];
  if (map.getSource(SEARCH_SOURCE_ID)) map.getSource(SEARCH_SOURCE_ID).setData(emptyFeatureCollection());
}

// Small colored bubble DOM element for a cluster of search results — the
// MapLibre-native replacement for Leaflet.markercluster's icon, kept as a
// DOM marker (like the offline labels) rather than a map circle/symbol layer
// so it renders identically under styles with no text glyphs configured
// (e.g. the offline tinyTiles style).
function clusterBubbleElement(count) {
  const safeCount = Number(count) || 0;
  const sizeClass = safeCount < 10 ? 'small' : safeCount < 50 ? 'medium' : 'large';
  const el = markerElement(`map-marker map-cluster-marker map-cluster-marker-${sizeClass}`);
  el.textContent = String(safeCount);
  el.setAttribute('aria-label', `${safeCount} Treffer im Cluster`);
  return el;
}

// Shows a lightweight list of overlapping results, used as the equivalent of
// Leaflet.markercluster's spiderfy when a cluster is already at max zoom and
// clicking it can't split it apart any further.
function showClusterLeavesPopup(lngLat, leaves) {
  const wrap = document.createElement('div');
  wrap.className = 'search-result-cluster-popup';
  const title = document.createElement('strong');
  title.className = 'search-result-cluster-title';
  title.textContent = `${leaves.length} Treffer an dieser Stelle`;
  wrap.appendChild(title);
  leaves.forEach((leaf) => {
    const item = lastSearchResults[leaf.properties.__idx];
    if (!item) return;
    const label = item.label || getResultInputValue(item) || resultCoordValue(item) || 'Treffer';
    const row = document.createElement('div');
    row.className = 'search-result-cluster-row';
    const btn = document.createElement('button');
    btn.className = 'btn btn-sm btn-outline search-result-cluster-btn';
    btn.textContent = label;
    btn.addEventListener('click', () => {
      popup.remove();
      const m = createSearchResultMarker(item);
      if (!m) return;
      m.addTo(map);
      searchClusterRenderedMarkers.push(m);
      map.easeTo({ center: [item.lon, item.lat], zoom: SEARCH_CLUSTER_MAX_ZOOM + 1 });
      window.setTimeout(() => m.togglePopup(), 300);
    });
    row.appendChild(btn);
    wrap.appendChild(row);
  });
  const popup = new maplibregl.Popup().setLngLat(lngLat).setDOMContent(wrap).addTo(map);
}

function ensureSearchClusterSource() {
  if (map.getSource(SEARCH_SOURCE_ID)) return;
  map.addSource(SEARCH_SOURCE_ID, {
    type: 'geojson',
    data: emptyFeatureCollection(),
    cluster: true,
    clusterRadius: SEARCH_CLUSTER_RADIUS,
    clusterMaxZoom: SEARCH_CLUSTER_MAX_ZOOM,
  });
  // Clusters/leaves render as DOM markers (see clusterBubbleElement() and
  // createSearchResultMarker()), not a map paint layer — but MapLibre only
  // loads/tiles a GeoJSON source's features for tiles a *layer* actually
  // requests, so querySourceFeatures() below stays empty without one. This
  // layer is fully transparent and exists solely to make the source's tiles
  // (and therefore its clustering) load.
  map.addLayer({
    id: SEARCH_SOURCE_ID,
    type: 'circle',
    source: SEARCH_SOURCE_ID,
    paint: { 'circle-radius': 0, 'circle-opacity': 0 },
  });
  map.on('moveend', syncSearchClusterMarkers);
  map.on('zoomend', syncSearchClusterMarkers);
}
// A base-layer switch (map.setStyle()) wipes the cluster source too — the
// route/territory sources restore their own data on rehydrate, but the
// search-results source is ephemeral (cleared whenever a new search starts
// anyway), so it only needs to exist again, not repopulate stale results.
registerMapLayerRehydrate(() => {
  if (map.getSource(SEARCH_SOURCE_ID)) return; // still present, style diff kept it
  ensureSearchClusterSource();
});

async function syncSearchClusterMarkers() {
  if (!map.getSource(SEARCH_SOURCE_ID) || lastSearchResults.length === 0) return;
  removeMarkers(searchClusterRenderedMarkers);
  const features = map.querySourceFeatures(SEARCH_SOURCE_ID);
  const seenClusters = new Set();
  let renderedCount = 0;
  for (const f of features) {
    if (renderedCount >= SEARCH_CLUSTER_RENDER_CAP) break;
    const coords = f.geometry?.coordinates;
    if (!Array.isArray(coords) || coords.length !== 2) continue;
    if (f.properties.cluster) {
      const clusterId = f.properties.cluster_id;
      if (seenClusters.has(clusterId)) continue; // Point features never span tiles, but guard anyway
      seenClusters.add(clusterId);
      const el = clusterBubbleElement(f.properties.point_count);
      const marker = new maplibregl.Marker({ element: el, anchor: 'center' }).setLngLat(coords).addTo(map);
      el.addEventListener('click', async () => {
        const source = map.getSource(SEARCH_SOURCE_ID);
        const expansionZoom = await source.getClusterExpansionZoom(clusterId);
        if (expansionZoom > SEARCH_CLUSTER_MAX_ZOOM - 0.5) {
          const leaves = await source.getClusterLeaves(clusterId, 200, 0);
          showClusterLeavesPopup(coords, leaves);
        } else {
          map.easeTo({ center: coords, zoom: expansionZoom });
        }
      });
      searchClusterRenderedMarkers.push(marker);
      renderedCount += 1;
    } else {
      const item = lastSearchResults[f.properties.__idx];
      if (!item) continue;
      const marker = createSearchResultMarker(item);
      if (!marker) continue;
      marker.addTo(map);
      searchClusterRenderedMarkers.push(marker);
      renderedCount += 1;
    }
  }
}

function showSearchResultsOnMap(results) {
  clearSearchResults();
  if (!Array.isArray(results) || results.length === 0) return;
  ensureSearchClusterSource();
  const valid = [];
  for (const item of results) {
    if (valid.length >= SEARCH_RESULTS_RENDER_LIMIT) break;
    const normalized = normalizeSearchResult(item);
    if (normalized) valid.push(normalized);
  }
  if (results.length > SEARCH_RESULTS_RENDER_LIMIT) {
    showToast(`Suchergebnisanzeige auf ${SEARCH_RESULTS_RENDER_LIMIT} Treffer begrenzt`, 'info', 1800);
  }
  lastSearchResults = valid;
  const bounds = valid.map((item) => [item.lon, item.lat]);
  if (bounds.length === 0) return;
  map.getSource(SEARCH_SOURCE_ID).setData({
    type: 'FeatureCollection',
    features: valid.map((item, idx) => ({
      type: 'Feature',
      geometry: { type: 'Point', coordinates: [item.lon, item.lat] },
      properties: { __idx: idx },
    })),
  });
  // map.once('idle', ...) right after setData() is a known race (the map
  // can briefly report idle before the new tiles/clusters have finished
  // loading) — waitForSourceReady polls the source's own load state instead.
  waitForSourceReady(SEARCH_SOURCE_ID).then(syncSearchClusterMarkers);
  if (bounds.length === 1) {
    map.panTo(bounds[0]);
  } else if (bounds.length > 1) {
    try {
      const b = bounds.reduce((acc, c) => acc.extend(c), new maplibregl.LngLatBounds(bounds[0], bounds[0]));
      map.fitBounds(b, { padding: 40 });
    } catch(e) {}
  }
}

// Builds one search-result marker with its full "Mehr Info"/"Als
// Zwischenstopp" popup — used for both un-clustered leaves and results
// picked from the cluster-leaves fallback list.
function createSearchResultMarker(item) {
    const normalized = normalizeSearchResult(item);
    if (!normalized) return null;
    const m = new maplibregl.Marker({ element: searchResultMarkerElement(), anchor: 'bottom' }).setLngLat([normalized.lon, normalized.lat]);
    const markerEl = m.getElement();
    if (markerEl) markerEl.title = normalized.label || '';
    const secondary = getResultSecondary(normalized);
    const popupWithButton = `<div class="search-result-popup">
      <strong class="search-result-popup-title">${escapeHtml(normalized.label || '')}</strong>
      ${secondary ? `<div class="search-result-popup-subtitle">${escapeHtml(secondary)}</div>` : ''}
      <div class="search-result-popup-actions">
        <button class="btn btn-sm btn-outline info-btn">Mehr Info</button>
        <button class="btn btn-sm btn-outline set-destination-btn">Als Ziel</button>
        <button class="btn btn-sm btn-outline add-waypoint-btn">Als Zwischenstopp</button>
        <button class="btn btn-sm btn-outline delete-marker-btn">Löschen</button>
      </div>
    </div>`;
    const popup = new maplibregl.Popup().setHTML(popupWithButton);
    popup.on('open', () => {
      const btn = popup.getElement().querySelector('.add-waypoint-btn');
      const infoBtn = popup.getElement().querySelector('.info-btn');
      const destBtn = popup.getElement().querySelector('.set-destination-btn');
      const deleteBtn = popup.getElement().querySelector('.delete-marker-btn');
      if (destBtn) {
        destBtn.addEventListener('click', () => {
          const val = resultCoordValue(normalized);
          const toEl = document.getElementById('to');
          if (val && toEl) {
            toEl.value = val;
            syncInputClearState('to');
          }
          popup.remove();
          if (val) compute();
        });
      }
      if (deleteBtn) {
        deleteBtn.addEventListener('click', () => {
          popup.remove();
          removeSearchResultMarker(m);
        });
      }
      if (btn) {
        btn.addEventListener('click', () => {
          const val = resultCoordValue(normalized);
          if (val) addWaypointWithValue(val);
          popup.remove();
        });
      }
      if (infoBtn) {
        infoBtn.addEventListener('click', async () => {
          // MapLibre's Popup has no getContent() getter (unlike Leaflet's),
          // so the "original" content to restore on "Zurück" is the HTML we
          // built above rather than something read back from the popup.
          const orig = popupWithButton;
          // show loading
          popup.setHTML('<div class="search-result-popup-loading">Informationen werden geladen…</div>');
          try {
            const qlat = userLocation ? userLocation.lat : null;
            const qlon = userLocation ? userLocation.lon : null;
            const qs = (qlat !== null && qlon !== null) ? `?lat=${qlat}&lon=${qlon}` : '';
            // Search uses "poi" for way-based POIs and also displays address
            // and street results. Only entity kinds accepted by the typed API
            // are encoded; legacy lookup remains useful for the other kinds.
            const entityKind = normalized.kind === 'poi' ? 'way' : (['node', 'way', 'relation'].includes(normalized.kind) ? normalized.kind : '');
            const poiPath = entityKind ? `${entityKind}/${normalized.id}` : String(normalized.id);
            const res = await fetch(`/api/v1/poi/${poiPath}${qs}`);
            if (!res.ok) throw new Error('fetch failed');
            const data = await res.json();
            // build info html
            let infoHtml = `<div class="search-result-popup">
              <strong class="search-result-popup-title">${escapeHtml(data.label || '')}</strong>
              <div class="search-result-popup-actions">
                <button class="btn btn-sm btn-primary route-btn">Als Ziel</button>
                <button class="btn btn-sm btn-outline waypoint-btn">Als Zwischenstopp</button>
                <button class="btn btn-sm btn-outline delete-btn">Löschen</button>
                <button class="btn btn-sm btn-outline back-btn">Zurück</button>
              </div>`;
            if (data.tags) {
              const keys = Object.keys(data.tags).sort();
              const knownLabels = { phone: 'Telefon', website: 'Website', opening_hours: 'Öffnungszeiten' };
              const knownRows = [];
              const otherRows = [];
              keys.forEach(k => {
                const v = data.tags[k];
                if (k === 'phone') {
                  const telHref = v.replace(/[^+\d]/g, '');
                  knownRows.push(`<div><strong>${knownLabels.phone}:</strong> <a href="tel:${escapeHtml(telHref)}">${escapeHtml(v)}</a></div>`);
                } else if (k === 'website') {
                  const isSafeUrl = /^https?:\/\//i.test(v);
                  const link = isSafeUrl
                    ? `<a href="${escapeHtml(v)}" target="_blank" rel="noopener noreferrer">${escapeHtml(v)}</a>`
                    : escapeHtml(v);
                  knownRows.push(`<div><strong>${knownLabels.website}:</strong> ${link}</div>`);
                } else if (k === 'opening_hours') {
                  knownRows.push(`<div><strong>${knownLabels.opening_hours}:</strong> ${escapeHtml(v)}</div>`);
                } else {
                  otherRows.push(`<div><strong>${escapeHtml(k)}:</strong> ${escapeHtml(v)}</div>`);
                }
              });
              infoHtml += '<div class="search-result-popup-details">';
              infoHtml += knownRows.join('') + otherRows.join('');
              infoHtml += '</div>';
            }
            if (data.wiki_summary) {
              infoHtml += `<div class="search-result-popup-details search-result-popup-muted">${escapeHtml(data.wiki_summary)}</div>`;
            }
            if (data.distance_m) {
              infoHtml += `<div class="search-result-popup-note">Entfernung: ${Math.round(data.distance_m)} m</div>`;
            }
            infoHtml += '</div>';
            popup.setHTML(infoHtml);
            // wire buttons
            setTimeout(() => {
              const el = popup.getElement();
              if (!el) return;
              const rbtn = el.querySelector('.route-btn');
              const wbtn = el.querySelector('.waypoint-btn');
              const dbtn = el.querySelector('.delete-btn');
              const bbtn = el.querySelector('.back-btn');
              if (rbtn) rbtn.addEventListener('click', () => {
                const to = resultCoordValue(data) || resultCoordValue(normalized);
                document.getElementById('to').value = to;
                syncInputClearState('to');
                popup.remove();
                compute();
              });
              if (wbtn) wbtn.addEventListener('click', () => {
                const val = resultCoordValue(data) || resultCoordValue(normalized);
                if (val) addWaypointWithValue(val);
                popup.remove();
              });
              if (dbtn) dbtn.addEventListener('click', () => {
                popup.remove();
                removeSearchResultMarker(m);
              });
              if (bbtn) bbtn.addEventListener('click', () => {
                popup.setHTML(orig);
              });
            }, 50);
          } catch (e) {
            popup.setHTML('<div class="search-result-popup-error">Informationen konnten nicht geladen werden.</div>');
            setTimeout(() => popup.setHTML(orig), 2000);
          }
        });
      }
    });
    // marker.setPopup() wires the marker's own click handler to toggle it,
    // matching Leaflet's implicit "click marker to open its bound popup".
    m.setPopup(popup);
    return m;
}

function getResultPrimary(item) {
  const tags = item && item.tags ? item.tags : {};
  return tags.name || tags.brand || tags.operator || (item && item.label) || '';
}

function getResultSecondary(item) {
  if (!item) return '';
  const parts = [];
  if (item.subtitle) parts.push(item.subtitle);
  if (item.match) parts.push(item.match);
  if (parts.length) return parts.join(' • ');

  const tags = item.tags || {};
  const fallback = [];
  if (tags.shop) fallback.push(tags.shop);
  if (tags.amenity) fallback.push(tags.amenity);
  if (tags['addr:street']) fallback.push(tags['addr:street']);
  if (tags['addr:city']) fallback.push(tags['addr:city']);
  return fallback.join(' • ');
}

function getResultInputValue(item) {
  if (!item) return '';
  return item.label || getResultPrimary(item) || '';
}

// Prefer exact "lat,lon" coordinates over the free-text label when routing
// to/through a result whose coordinates we already know. Round-tripping a
// label (especially one missing a city, e.g. an unaddressed POI) back
// through the server's free-text search can resolve to a same-housenumber
// address somewhere else entirely — coordinates sidestep that ambiguity.
function resultCoordValue(item) {
  return formatLatLon(item?.lat, item?.lon);
}

// simple HTML escaper for suggestion labels
function escapeHtml(s){
  if(!s) return '';
  return s.replaceAll('&','&amp;').replaceAll('<','&lt;').replaceAll('>','&gt;').replaceAll('"','&quot;').replaceAll("'",'&#39;');
}

// (debounce already defined earlier)
// const debouncedCompute is defined near the top to avoid TDZ

document.getElementById('addWaypoint').onclick = addWaypoint;

const pro = document.getElementById('pro');
const weights = document.getElementById('weights');
pro.addEventListener('change', () => {
  if (pro.checked) {
    weights.classList.remove('hidden');
    setTimeout(() => weights.style.display = 'block', 10);
  } else {
    weights.style.display = 'none';
    setTimeout(() => weights.classList.add('hidden'), 300);
  }
  // update ARIA state for switch
  try { pro.setAttribute('aria-checked', pro.checked ? 'true' : 'false'); } catch (e) {}
  compute();
});

document.getElementById('engine').addEventListener('change', compute);
document.getElementById('objective').addEventListener('change', compute);

// Route objective quick-toggle in the main route card — a more discoverable
// shortcut for the same "Ziel" select in Einstellungen > Routing, kept in
// sync with it in both directions rather than duplicating routing logic.
function syncRouteObjectiveToggle() {
  const current = document.getElementById('objective')?.value || 'distance';
  document.querySelectorAll('.route-objective-btn').forEach((btn) => {
    btn.classList.toggle('is-active', btn.dataset.objective === current);
  });
}
document.getElementById('routeObjectiveToggle')?.addEventListener('click', (ev) => {
  const btn = ev.target.closest('.route-objective-btn');
  if (!btn) return;
  const objectiveEl = document.getElementById('objective');
  if (!objectiveEl || objectiveEl.value === btn.dataset.objective) return;
  objectiveEl.value = btn.dataset.objective;
  objectiveEl.dispatchEvent(new Event('change', { bubbles: true }));
});
document.getElementById('objective')?.addEventListener('change', syncRouteObjectiveToggle);
syncRouteObjectiveToggle();
const optimizeEl = document.getElementById('optimize');
optimizeEl.addEventListener('change', (ev) => {
  try { optimizeEl.setAttribute('aria-checked', optimizeEl.checked ? 'true' : 'false'); } catch (e) {}
  compute();
});

// Initialize settings UI
function initializeSettingsUI(s) {
  if (!s) return;
    if(s.routing) {
        document.getElementById('engine').value = (s.routing.engine || 'astar');
        document.getElementById('objective').value = s.routing.objective || 'duration';
        syncRouteObjectiveToggle();
        const profileEl = document.getElementById('profile');
        if (profileEl) profileEl.value = s.routing.profile || '';
        document.getElementById('pro').checked = !!s.routing.pro;
        document.getElementById('emergencyMode').checked = !!s.routing.emergency_mode;
        if(s.routing.pro) weights.classList.remove('hidden');
        
        const w = s.routing.weights || {};
        document.getElementById('w_left').value = w.left_turn || 0;
        document.getElementById('w_right').value = w.right_turn || 0;
        document.getElementById('w_traffic_light').value = w.traffic_light_penalty || 0;
        document.getElementById('noLeftTurn').checked = !!w.no_left_turn;
    }
    // populate allowed highways UI
    const highwayTypes = [
      {type: "motorway", icon: "🛣️", label: "Autobahn"},
      {type: "trunk", icon: "🚗", label: "Schnellstraße"},
      {type: "primary", icon: "🛣️", label: "Bundesstraße"},
      {type: "secondary", icon: "🛣️", label: "Landstraße"},
      {type: "tertiary", icon: "🛣️", label: "Kreisstraße"},
      {type: "unclassified", icon: "🛣️", label: "Nebenstraße"},
      {type: "residential", icon: "🏠", label: "Wohnstraße"},
      {type: "living_street", icon: "🚶", label: "Spielstraße"},
      {type: "service", icon: "🏪", label: "Zufahrt"},
      {type: "track", icon: "🚜", label: "Feldweg"},
      {type: "motorway_link", icon: "🔀", label: "Autobahnauffahrt"},
      {type: "trunk_link", icon: "🔀", label: "Auffahrt"}
    ];
    const allowedContainer = document.getElementById('allowedHighways');
    allowedContainer.innerHTML = '';
    highwayTypes.forEach(hw => {
      const id = 'ah_'+hw.type;
      const lbl = document.createElement('label'); lbl.className='checkbox';
      const cb = document.createElement('input'); cb.type='checkbox'; cb.className='allowed-highway'; cb.dataset.type = hw.type; cb.id = id;
      if (s.allowed_highway_types && s.allowed_highway_types.indexOf(hw.type) !== -1) cb.checked = true;
      lbl.appendChild(cb);
      const span = document.createElement('span'); 
      span.innerHTML = `${hw.icon} <small style="opacity:0.8">${hw.label}</small>`;
      span.style.marginLeft='6px'; 
      lbl.appendChild(span);
      allowedContainer.appendChild(lbl);
    });

    // populate speed defaults UI
    const speedContainer = document.getElementById('speedDefaults');
    speedContainer.innerHTML = '';
    const speeds = s.default_highway_speeds || {"motorway":150};
    const speedTypes = [
      {type: "motorway", label: "Autobahn", icon: "🚗"},
      {type: "trunk", label: "Schnellstraße", icon: "🚗"},
      {type: "primary", label: "Bundesstraße", icon: "🛣️"},
      {type: "secondary", label: "Landstraße", icon: "🛣️"},
      {type: "tertiary", label: "Kreisstraße", icon: "🛣️"},
      {type: "residential", label: "Wohnstraße", icon: "🏠"},
      {type: "service", label: "Zufahrt", icon: "🏪"},
      {type: "track", label: "Feldweg", icon: "🚜"}
    ];
    speedTypes.forEach(st => {
      const row = document.createElement('div');
      row.className = 'speed-input-row';
      const lab = document.createElement('label');
      lab.innerHTML = `${st.icon} ${st.label}`;
      const inp = document.createElement('input');
      inp.type = 'number';
      inp.className = 'speed-input';
      inp.dataset.type = st.type;
      inp.value = speeds[st.type] || '';
      inp.placeholder = 'km/h';
      inp.min = '5';
      inp.max = '300';
      inp.step = '5';
      row.appendChild(lab);
      row.appendChild(inp);
      speedContainer.appendChild(row);
    });

    // populate tile/map source UI
    const tiles = s.tiles || {};
    const mt = tiles.map_type || 'raster';
    const mtEl = document.getElementById('mapType');
    if (mtEl) mtEl.value = mt;
    const upEl = document.getElementById('tileUpstream');
    if (upEl) upEl.value = tiles.upstream || '';
    const suEl = document.getElementById('tileStyleUrl');
    if (suEl) suEl.value = tiles.style_url || '';
    const wlEl = document.getElementById('wmsLayers');
    if (wlEl) wlEl.value = tiles.wms_layers || '';
    const mzEl = document.getElementById('tileMaxZoom');
    if (mzEl) mzEl.value = Number.isInteger(tiles.max_zoom) && tiles.max_zoom > 0 ? tiles.max_zoom : '';
    const atEl = document.getElementById('tileAttribution');
    if (atEl) atEl.value = tiles.attribution || '';
    updateMapTypeVisibility(mt);
    // OpenAI / remote API settings
    const ai = s.ai || {};
    const keyEl = document.getElementById('openaiApiKey');
    if (keyEl) keyEl.value = ai.openai_api_key || '';
    const urlEl = document.getElementById('openaiBaseUrl');
    if (urlEl) urlEl.value = ai.openai_base_url || '';
    const adminTokenEl = document.getElementById('adminToken');
    if (adminTokenEl) adminTokenEl.value = sessionStorage.getItem('osmminiAdminToken') || '';
    settingsUIReady = true;
    syncTileSourcePickerFromTiles(tiles);
    maybeShowMapWelcome();
}

// ─── Visual tile-source picker ───────────────────────────────────────────
// Presets are deliberately represented as accessible cards rather than a
// select box: their coverage, online/offline behaviour and preview can be
// understood before changing the live map.
const tilePreviewCoordinate = { z: 10, x: 544, y: 354 };
let tilePresets = [];
let activeTilePresetID = '';
let activeTileSourceFilter = 'recommended';
let tilePreviewObserver = null;
let settingsUIReady = false;
let tilePresetAPIReady = false;

function tilePresetCategory(preset) {
  if (preset?.id === 'tinytiles_local') return 'offline';
  if (String(preset?.id || '').startsWith('bayern_') || preset?.id === 'geodaten_bavaria') return 'bayern';
  return 'online';
}

function isRecommendedTilePreset(preset) {
  return ['tinytiles_local', 'carto_voyager', 'basemap_de', 'bayern_vector_standard'].includes(preset?.id);
}

function tilePresetKindLabel(preset) {
  const type = String(preset?.map_type || 'raster').toLowerCase();
  if (type === 'vector') return 'Vektor';
  if (type === 'wms') return 'WMS';
  return 'Raster';
}

function tilePresetDescription(preset) {
  if (preset?.id === 'tinytiles_local') return 'Aus deiner PBF – ohne externe Tile-API';
  if (tilePresetCategory(preset) === 'bayern') return preset.map_type === 'vector' ? 'Bayern · detaillierte Vektorkarte' : 'Bayern · amtliche Karte';
  if (['carto_voyager', 'carto_light', 'carto_dark'].includes(preset?.id)) return 'Global · zuverlässige Onlinekarte';
  if (preset?.id === 'basemap_de') return 'Deutschland · amtliche Basiskarte';
  return 'Online · Kartenquelle des Anbieters';
}

function tilePresetIcon(preset) {
  if (preset?.id === 'tinytiles_local') return '📦';
  if (String(preset?.id || '').includes('luftbild')) return '🛰️';
  if (preset?.map_type === 'vector') return '◈';
  if (preset?.map_type === 'wms') return '▦';
  return '▤';
}

function tilePresetBadges(preset) {
  const badges = [];
  const category = tilePresetCategory(preset);
  if (category === 'offline') badges.push('Offline');
  else if (category === 'bayern') badges.push('Bayern');
  else badges.push('Online');
  badges.push(tilePresetKindLabel(preset));
  return badges;
}

function tilePreviewURL(preset) {
  if (!preset?.upstream || preset.map_type === 'wms') return '';
  const { z, x, y } = tilePreviewCoordinate;
  return preset.upstream
    .replaceAll('{z}', String(z))
    .replaceAll('{x}', String(x))
    .replaceAll('{y}', String(y));
}

function hydrateTilePreview(image) {
  if (!image?.dataset.src || image.dataset.loaded === '1') return;
  image.dataset.loaded = '1';
  image.src = image.dataset.src;
  image.addEventListener('error', () => image.closest('.tile-source-preview')?.classList.add('preview-unavailable'), { once: true });
}

function observeTilePreview(image) {
  if (!image?.dataset.src) return;
  if (!('IntersectionObserver' in window)) return;
  if (!tilePreviewObserver) {
    tilePreviewObserver = new IntersectionObserver((entries) => {
      entries.forEach((entry) => {
        if (!entry.isIntersecting) return;
        hydrateTilePreview(entry.target);
        tilePreviewObserver?.unobserve(entry.target);
      });
    }, { rootMargin: '120px' });
  }
  tilePreviewObserver.observe(image);
}

function hydrateVisibleTilePreviews() {
  document.querySelectorAll('.tile-source-preview img[data-src]').forEach(hydrateTilePreview);
}

function createTilePreview(preset) {
  const preview = document.createElement('span');
  preview.className = `tile-source-preview tile-source-preview-${String(preset?.map_type || 'raster').toLowerCase()}`;
  preview.setAttribute('aria-hidden', 'true');
  const url = tilePreviewURL(preset);
  if (url) {
    const image = document.createElement('img');
    image.alt = '';
    image.loading = 'lazy';
    image.decoding = 'async';
    image.dataset.src = url;
    preview.appendChild(image);
    observeTilePreview(image);
  }
  const vectorRoad = document.createElement('span');
  vectorRoad.className = 'tile-preview-road';
  const vectorWater = document.createElement('span');
  vectorWater.className = 'tile-preview-water';
  const vectorBlocks = document.createElement('span');
  vectorBlocks.className = 'tile-preview-blocks';
  preview.append(vectorRoad, vectorWater, vectorBlocks);
  return preview;
}

function matchingPresetForTiles(tiles = {}) {
  return tilePresets.find((preset) => preset.style_url && preset.style_url === tiles.style_url) ||
    tilePresets.find((preset) =>
      preset.upstream && preset.upstream === tiles.upstream &&
      (preset.map_type || 'raster') === (tiles.map_type || 'raster') &&
      (preset.wms_layers || '') === (tiles.wms_layers || '')
    ) || null;
}

function syncTileSourcePickerFromTiles(tiles = {}) {
  activeTilePresetID = matchingPresetForTiles(tiles)?.id || '';
  renderTileSourceCards();
  renderWelcomeOnlineCards();
}

function setTileSourceForm(preset) {
  const mapType = document.getElementById('mapType');
  const upstream = document.getElementById('tileUpstream');
  const style = document.getElementById('tileStyleUrl');
  const layers = document.getElementById('wmsLayers');
  const attribution = document.getElementById('tileAttribution');
  const maxZoom = document.getElementById('tileMaxZoom');
  if (mapType) mapType.value = preset.map_type || 'raster';
  if (upstream) upstream.value = preset.upstream || '';
  if (style) style.value = preset.style_url || '';
  if (layers) layers.value = preset.wms_layers || '';
  if (attribution) attribution.value = preset.attribution || '';
  if (maxZoom) maxZoom.value = Number.isInteger(preset.max_zoom) && preset.max_zoom > 0 ? preset.max_zoom : '';
  updateMapTypeVisibility(preset.map_type || 'raster');
}

function setTileSourceAdvancedOpen(open) {
  const advanced = document.getElementById('tileSourceAdvanced');
  const toggle = document.getElementById('tileSourceAdvancedToggle');
  if (!advanced || !toggle) return;
  advanced.hidden = !open;
  toggle.setAttribute('aria-expanded', open ? 'true' : 'false');
  toggle.textContent = open ? 'Auswahl schließen' : 'Eigene Quelle';
}

function sourceSelectionHint(preset = null) {
  const hint = document.getElementById('tileSourceSelectionHint');
  if (!hint) return;
  if (!preset) {
    hint.textContent = 'Benutzerdefinierte Kartenquelle – bei Bedarf die erweiterten Felder öffnen.';
    return;
  }
  if (preset.id === 'tinytiles_local') {
    const state = normalizedTinyTilesState(window.tinyTilesLatestStatus?.state);
    hint.textContent = state === 'ready'
      ? 'Lokale Offline-Karte ist bereit.'
      : state === 'building'
        ? 'Lokale Offline-Karte wird gerade erzeugt.'
        : 'Wählt die lokale Offline-Karte und startet bei Bedarf deren Erzeugung.';
    return;
  }
  hint.textContent = `${tilePresetDescription(preset)} · Vorschau aktiv, mit „Speichern“ dauerhaft übernehmen.`;
}

function createTileSourceCard(preset, { compact = false } = {}) {
  const card = document.createElement('button');
  card.type = 'button';
  card.className = compact ? 'tile-source-card tile-source-card-compact' : 'tile-source-card';
  card.dataset.presetID = preset.id;
  const active = activeTilePresetID === preset.id;
  card.classList.toggle('is-active', active);
  card.setAttribute('aria-pressed', active ? 'true' : 'false');

  card.appendChild(createTilePreview(preset));
  const body = document.createElement('span');
  body.className = 'tile-source-card-body';
  const title = document.createElement('strong');
  title.textContent = preset.label;
  const description = document.createElement('small');
  description.textContent = tilePresetDescription(preset);
  const badges = document.createElement('span');
  badges.className = 'tile-source-badges';
  tilePresetBadges(preset).forEach((label) => {
    const badge = document.createElement('span');
    badge.textContent = label;
    badges.appendChild(badge);
  });
  body.append(title, description, badges);
  card.appendChild(body);
  const check = document.createElement('span');
  check.className = 'tile-source-check';
  check.setAttribute('aria-hidden', 'true');
  check.textContent = active ? '✓' : tilePresetIcon(preset);
  card.appendChild(check);
  card.addEventListener('click', () => { void selectTilePreset(preset, { fromWelcome: compact }); });
  return card;
}

function visibleTilePresets() {
  if (activeTileSourceFilter === 'recommended') return tilePresets.filter(isRecommendedTilePreset);
  return tilePresets.filter((preset) => tilePresetCategory(preset) === activeTileSourceFilter);
}

function renderTileSourceCards() {
  const container = document.getElementById('tileSourceCards');
  if (!container) return;
  container.replaceChildren();
  const presets = visibleTilePresets();
  if (presets.length === 0) {
    const empty = document.createElement('div');
    empty.className = 'tile-source-empty';
    empty.textContent = 'Für diese Kategorie ist keine Kartenquelle verfügbar.';
    container.appendChild(empty);
  } else {
    presets.forEach((preset) => container.appendChild(createTileSourceCard(preset)));
  }
  container.setAttribute('aria-busy', tilePresets.length === 0 ? 'true' : 'false');
  sourceSelectionHint(tilePresets.find((preset) => preset.id === activeTilePresetID) || null);
}

function renderWelcomeOnlineCards() {
  const container = document.getElementById('mapWelcomeOnlineCards');
  if (!container) return;
  container.replaceChildren();
  const wanted = ['carto_voyager', 'basemap_de', 'bayern_vector_standard'];
  const choices = wanted.map((id) => tilePresets.find((preset) => preset.id === id)).filter(Boolean);
  choices.forEach((preset) => container.appendChild(createTileSourceCard(preset, { compact: true })));
}

// Show/hide custom source fields based on the selected map type.
function updateMapTypeVisibility(mt) {
  const upstreamRow = document.getElementById('upstreamRow');
  const styleUrlRow = document.getElementById('styleUrlRow');
  const wmsLayersRow = document.getElementById('wmsLayersRow');
  if (upstreamRow) upstreamRow.style.display = (mt === 'vector') ? 'none' : '';
  if (styleUrlRow) styleUrlRow.style.display = (mt === 'vector') ? '' : 'none';
  if (wmsLayersRow) wmsLayersRow.style.display = (mt === 'wms') ? '' : 'none';
}

// Read just the map-source fields from the settings form. Keeping this in one
// place makes preview and save behave identically.
function tileSettingsFromUI(fallback = {}) {
  const tiles = { ...fallback };
  const mtEl = document.getElementById('mapType');
  if (mtEl) tiles.map_type = mtEl.value;
  const upEl = document.getElementById('tileUpstream');
  if (upEl) tiles.upstream = upEl.value.trim();
  const suEl = document.getElementById('tileStyleUrl');
  if (suEl) tiles.style_url = suEl.value.trim();
  const wlEl = document.getElementById('wmsLayers');
  if (wlEl) tiles.wms_layers = wlEl.value.trim();
  const mzEl = document.getElementById('tileMaxZoom');
  if (mzEl) {
    const maxZoom = Number.parseInt(mzEl.value, 10);
    tiles.max_zoom = Number.isInteger(maxZoom) && maxZoom > 0 ? maxZoom : 0;
  }
  const atEl = document.getElementById('tileAttribution');
  if (atEl) tiles.attribution = atEl.value;
  return tiles;
}

function previewTileSource() {
  const base = preloadedSettings || {};
  const preview = { ...base, tiles: tileSettingsFromUI(base.tiles || {}) };
  return applyTileLayer(preview, { directPreview: true });
}

function setTileSourceFilter(filter) {
  activeTileSourceFilter = filter;
  document.querySelectorAll('.tile-source-filter').forEach((button) => {
    const active = button.dataset.tileFilter === filter;
    button.classList.toggle('is-active', active);
    button.setAttribute('aria-selected', active ? 'true' : 'false');
  });
  renderTileSourceCards();
}

async function selectTilePreset(preset, { fromWelcome = false } = {}) {
  if (!preset) return;
  if (preset.id === 'tinytiles_local') {
    await selectTinyTilesSource({ fromWelcome });
    return;
  }
  const previousPresetID = activeTilePresetID;
  const previousTiles = tileSettingsFromUI((preloadedSettings || {}).tiles || {});
  activeTilePresetID = preset.id;
  setTileSourceForm(preset);
  setTileSourceAdvancedOpen(false);
  renderTileSourceCards();
  sourceSelectionHint(preset);
  if (fromWelcome) completeMapWelcome('online');
  showTileLoadOverlay({ title: 'Online-Karte wird geladen', message: `${preset.label} wird vorbereitet.`, mode: 'online' });
  try {
    const applied = await previewTileSource();
    if (!applied) {
      activeTilePresetID = previousPresetID;
      setTileSourceForm(previousTiles);
      renderTileSourceCards();
      sourceSelectionHint(tilePresets.find((item) => item.id === previousPresetID) || null);
      throw new Error('Kartenquelle konnte nicht geladen werden; die bisherige Karte bleibt aktiv.');
    }
    await waitForMapLayerPaint();
    showToast('Kartenvorschau geladen – zum Behalten bitte speichern.', 'info', 3500);
  } catch (error) {
    console.error('Failed to activate tile source', error);
    showToast('Kartenquelle konnte nicht aktiviert werden.', 'error', 5000);
  } finally {
    hideTileLoadOverlay();
  }
}

// ─── First-start map choice ──────────────────────────────────────────────
// The choice is deliberately remembered only as onboarding completion. The
// actual server setting still changes only through the explicit Save action.
const mapWelcomeStorageKey = 'osmmini.map-choice-onboarding-v1';
let mapWelcomeDeferredThisSession = false;

function mapWelcomeWasCompleted() {
  try {
    return Boolean(localStorage.getItem(mapWelcomeStorageKey));
  } catch (_) {
    return false;
  }
}

function resetMapWelcomeView() {
  const main = document.getElementById('mapWelcomeMain');
  const choices = document.getElementById('mapWelcomeOnlineChoices');
  if (main) main.hidden = false;
  if (choices) choices.hidden = true;
}

function maybeShowMapWelcome() {
  if (!settingsUIReady || !tilePresetAPIReady || mapWelcomeDeferredThisSession || mapWelcomeWasCompleted()) return;
  const overlay = document.getElementById('mapWelcomeOverlay');
  const dialog = document.getElementById('mapWelcomeDialog');
  if (!overlay || !dialog || !overlay.hidden) return;
  resetMapWelcomeView();
  overlay.hidden = false;
  document.body.classList.add('map-welcome-open');
  window.setTimeout(() => dialog.focus(), 0);
}

function hideMapWelcome({ defer = false } = {}) {
  const overlay = document.getElementById('mapWelcomeOverlay');
  if (overlay) overlay.hidden = true;
  document.body.classList.remove('map-welcome-open');
  if (defer) mapWelcomeDeferredThisSession = true;
}

function completeMapWelcome(choice) {
  try {
    localStorage.setItem(mapWelcomeStorageKey, choice || 'chosen');
  } catch (_) {
    // The map remains usable in strict privacy modes; the prompt may simply
    // reappear after a reload.
  }
  hideMapWelcome();
}

function openSettingsSection(headerID, contentID, storageKey) {
  const settingsBodyEl = document.getElementById('settingsBody');
  const settingsCardEl = document.getElementById('settingsCard');
  const settingsToggleEl = document.getElementById('settingsToggle');
  if (settingsBodyEl) settingsBodyEl.style.display = 'block';
  settingsCardEl?.classList.remove('collapsed');
  if (settingsToggleEl) {
    settingsToggleEl.setAttribute('aria-expanded', 'true');
    settingsToggleEl.textContent = '‹';
  }
  try { localStorage.setItem('settingsOpen', '1'); } catch (_) {}

  const header = document.getElementById(headerID);
  const content = document.getElementById(contentID);
  if (content) content.style.display = 'grid';
  header?.closest('.settings-section')?.classList.add('expanded');
  try { localStorage.setItem(storageKey, '1'); } catch (_) {}
}

function showMapSourcePicker(filter = 'recommended') {
  openSettingsSection('mapHeader', 'mapSettings', 'mapSettingsOpen');
  setTileSourceFilter(filter);
  window.setTimeout(() => {
    document.getElementById('tileSourceCards')?.scrollIntoView({ block: 'center', behavior: 'smooth' });
    hydrateVisibleTilePreviews();
  }, 0);
}

function openMapSourcePicker() {
  completeMapWelcome('online');
  showMapSourcePicker('online');
}

document.getElementById('mapWelcomeLater')?.addEventListener('click', () => hideMapWelcome({ defer: true }));
document.getElementById('openMapSources')?.addEventListener('click', () => showMapSourcePicker());
document.getElementById('mapWelcomeOffline')?.addEventListener('click', () => { void selectTinyTilesSource({ fromWelcome: true }); });
document.getElementById('mapWelcomeOnline')?.addEventListener('click', () => {
  const main = document.getElementById('mapWelcomeMain');
  const choices = document.getElementById('mapWelcomeOnlineChoices');
  if (main) main.hidden = true;
  if (choices) choices.hidden = false;
  renderWelcomeOnlineCards();
  window.setTimeout(hydrateVisibleTilePreviews, 0);
});
document.getElementById('mapWelcomeBack')?.addEventListener('click', resetMapWelcomeView);
document.getElementById('mapWelcomeMoreSources')?.addEventListener('click', openMapSourcePicker);
document.getElementById('mapWelcomeOverlay')?.addEventListener('click', (event) => {
  if (event.target === event.currentTarget) hideMapWelcome({ defer: true });
});
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape' && !document.getElementById('mapWelcomeOverlay')?.hidden) {
    hideMapWelcome({ defer: true });
  }
});

// Tile preset loading from API.
async function loadTilePresets() {
  try {
    const res = await fetch('/api/v1/tile-sources');
    if (!res.ok) throw new Error(`tile preset request failed (${res.status})`);
    const presets = await res.json();
    tilePresets = Array.isArray(presets) ? presets : [];
    const curTiles = (preloadedSettings || {}).tiles || {};
    activeTilePresetID = matchingPresetForTiles(curTiles)?.id || activeTilePresetID;
    renderTileSourceCards();
    renderWelcomeOnlineCards();
  } catch (error) {
    console.warn('Failed to load tile presets:', error);
    const container = document.getElementById('tileSourceCards');
    if (container) {
      container.replaceChildren();
      const empty = document.createElement('div');
      empty.className = 'tile-source-empty';
      empty.textContent = 'Kartenquellen konnten nicht geladen werden.';
      container.appendChild(empty);
      container.setAttribute('aria-busy', 'false');
    }
  } finally {
    tilePresetAPIReady = true;
    maybeShowMapWelcome();
  }
}
void loadTilePresets();

document.getElementById('tileSourceFilters')?.addEventListener('click', (event) => {
  const button = event.target.closest('[data-tile-filter]');
  if (button) setTileSourceFilter(button.dataset.tileFilter);
});

document.getElementById('tileSourceAdvancedToggle')?.addEventListener('click', () => {
  const advanced = document.getElementById('tileSourceAdvanced');
  const open = Boolean(advanced?.hidden);
  setTileSourceAdvancedOpen(open);
  if (open) {
    activeTilePresetID = '';
    renderTileSourceCards();
    sourceSelectionHint(null);
  }
});

document.getElementById('mapHeader')?.addEventListener('click', () => window.setTimeout(hydrateVisibleTilePreviews, 0));

// Handle map type selector change.
const mapTypeEl = document.getElementById('mapType');
if (mapTypeEl) {
  mapTypeEl.addEventListener('change', () => updateMapTypeVisibility(mapTypeEl.value));
}

document.getElementById('previewTileSource')?.addEventListener('click', () => {
  activeTilePresetID = '';
  renderTileSourceCards();
  sourceSelectionHint(null);
  showTileLoadOverlay({ title: 'Karte wird geladen', message: 'Die eigene Kartenquelle wird vorbereitet.', mode: 'online' });
  previewTileSource()
    .then((applied) => {
      if (!applied) throw new Error('Kartenquelle konnte nicht aktiviert werden.');
      return waitForMapLayerPaint();
    })
    .then(() => showToast('Kartenvorschau geladen – zum Behalten bitte speichern.', 'info', 3500))
    .catch(() => showToast('Kartenquelle konnte nicht aktiviert werden.', 'error', 5000))
    .finally(hideTileLoadOverlay);
});

// A manual source edit turns the selection into a custom configuration.
['mapType', 'tileUpstream', 'tileStyleUrl', 'wmsLayers', 'tileMaxZoom', 'tileAttribution'].forEach((id) => {
  document.getElementById(id)?.addEventListener('input', () => {
    activeTilePresetID = '';
    renderTileSourceCards();
    sourceSelectionHint(null);
  });
  document.getElementById(id)?.addEventListener('change', () => {
    activeTilePresetID = '';
    renderTileSourceCards();
    sourceSelectionHint(null);
  });
});

// ─── Local tinyTiles build ────────────────────────────────────────────────
// The server owns the PBF path and the actual generator. The browser only
// starts the asynchronous job and polls its small status document, so a build
// never blocks routing or the rest of the UI.
const tinyTilesBuildEndpoint = '/api/v1/tinytiles/build';
const tinyTilesPollIntervalMs = 2500;
let tinyTilesPollTimer = null;
let tinyTilesPollInFlight = false;
let tinyTilesAutoActivateWhenReady = false;
let tinyTilesLoadRequested = false;
let tinyTilesLatestStatus = { state: 'idle', phase: 'idle', progress: 0 };
window.tinyTilesLatestStatus = tinyTilesLatestStatus;

function tinyTilesElements() {
  return {
    build: document.getElementById('tinyTilesBuild'),
    activate: document.getElementById('tinyTilesActivate'),
    status: document.getElementById('tinyTilesBuildStatus'),
    icon: document.getElementById('tinyTilesStatusIcon'),
    title: document.getElementById('tinyTilesStatusTitle'),
    message: document.getElementById('tinyTilesStatusMessage'),
    progress: document.getElementById('tinyTilesProgress'),
    progressBar: document.querySelector('#tinyTilesProgress .tinytiles-progress-bar span'),
    facts: document.getElementById('tinyTilesFacts'),
  };
}

function normalizedTinyTilesState(value) {
  switch (String(value || 'idle').toLowerCase()) {
    case 'building':
    case 'ready':
    case 'failed':
    case 'idle':
      return String(value).toLowerCase();
    default:
      return 'idle';
  }
}

function tinyTilesZoomsFromUI() {
  const min = Number.parseInt(document.getElementById('tinyTilesMinZoom')?.value, 10);
  const max = Number.parseInt(document.getElementById('tinyTilesMaxZoom')?.value, 10);
  // The packaged minimal MapLibre style is defined for zoom 5–14. Keeping the
  // UI in that range avoids generating tiles which that local style cannot use.
  if (!Number.isInteger(min) || !Number.isInteger(max) || min < 5 || max > 14 || min > max) {
    throw new Error('Bitte gültige Zoomstufen zwischen 5 und 14 wählen (Minimum ≤ Maximum).');
  }
  return { min_zoom: min, max_zoom: max };
}

function updateTinyTilesPresetSelection() {
  const min = Number.parseInt(document.getElementById('tinyTilesMinZoom')?.value, 10);
  const max = Number.parseInt(document.getElementById('tinyTilesMaxZoom')?.value, 10);
  document.querySelectorAll('.tinytiles-preset').forEach((preset) => {
    const selected = Number.parseInt(preset.dataset.tinytilesMin, 10) === min
      && Number.parseInt(preset.dataset.tinytilesMax, 10) === max;
    preset.classList.toggle('is-selected', selected);
    preset.setAttribute('aria-pressed', String(selected));
  });
}

function formatTinyTilesBytes(value) {
  const bytes = Number(value);
  if (!Number.isFinite(bytes) || bytes <= 0) return '';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let scaled = bytes;
  let unit = 0;
  while (scaled >= 1024 && unit < units.length - 1) {
    scaled /= 1024;
    unit += 1;
  }
  const digits = scaled >= 10 || unit === 0 ? 0 : 1;
  return `${new Intl.NumberFormat('de-DE', { maximumFractionDigits: digits }).format(scaled)} ${units[unit]}`;
}

function formatTinyTilesDuration(status, state) {
  const started = Date.parse(status.started_at || '');
  const finished = Date.parse(status.finished_at || '');
  if (!Number.isFinite(started)) return '';
  const until = Number.isFinite(finished) ? finished : state === 'building' ? Date.now() : NaN;
  if (!Number.isFinite(until) || until <= started) return '';
  const seconds = Math.round((until - started) / 1000);
  if (seconds < 60) return `${seconds} Sek.`;
  const minutes = Math.floor(seconds / 60);
  const rest = seconds % 60;
  return rest ? `${minutes} Min. ${rest} Sek.` : `${minutes} Min.`;
}

function updateTinyTilesFacts(status, state, facts) {
  if (!facts) return;
  const number = new Intl.NumberFormat('de-DE');
  const entries = [];
  const sourceBytes = formatTinyTilesBytes(status.source_bytes);
  const estimatedDisk = formatTinyTilesBytes(status.estimated_disk_bytes);
  const estimatedTiles = Number(status.estimated_tile_count);
  const generatedTiles = Number(status.generated_tiles);
  const roads = Number(status.road_features);
  const duration = formatTinyTilesDuration(status, state);
  if (sourceBytes) entries.push(['PBF-Quelle', sourceBytes]);
  if (estimatedTiles > 0) entries.push(['Geschätzte Kacheln', number.format(estimatedTiles)]);
  if (estimatedDisk) entries.push(['Geschätzter Speicher', estimatedDisk]);
  if (generatedTiles > 0) entries.push(['Erzeugte Kacheln', number.format(generatedTiles)]);
  if (roads > 0) entries.push(['Straßenobjekte', number.format(roads)]);
  if (duration) entries.push([state === 'building' ? 'Läuft seit' : 'Build-Dauer', duration]);
  facts.replaceChildren(...entries.map(([label, value]) => {
    const item = document.createElement('div');
    const term = document.createElement('dt');
    const detail = document.createElement('dd');
    term.textContent = label;
    detail.textContent = value;
    item.append(term, detail);
    return item;
  }));
  facts.hidden = entries.length === 0;
}

function tinyTilesStatusText(status, state) {
  if (state === 'building') {
    return {
      icon: '◌',
      title: 'Offline-Karte wird erzeugt',
      message: status.message || 'Die lokale Karte wird aus der PBF aufgebaut. Das kann je nach Gebiet einige Zeit dauern.',
    };
  }
  if (state === 'ready') {
    return {
      icon: '✓',
      title: 'Offline-Karte bereit',
      message: status.message || 'Die lokale Kartenquelle kann jetzt ohne externe Tile-API verwendet werden.',
    };
  }
  if (state === 'failed') {
    return {
      icon: '!',
      title: 'Erzeugung fehlgeschlagen',
      message: status.error || status.message || 'Die Offline-Karte konnte nicht erzeugt werden.',
    };
  }
  return {
    icon: '○',
    title: 'Noch keine Offline-Karte',
    message: status.message || 'Die lokale Karte wird aus der geladenen PBF erzeugt.',
  };
}

function updateTinyTilesUI(rawStatus = {}) {
  const state = normalizedTinyTilesState(rawStatus.state);
  const text = tinyTilesStatusText(rawStatus, state);
  const el = tinyTilesElements();
  if (el.status) {
    el.status.dataset.state = state;
    el.status.setAttribute('aria-busy', state === 'building' ? 'true' : 'false');
  }
  if (el.icon) el.icon.textContent = text.icon;
  if (el.title) el.title.textContent = text.title;
  if (el.message) el.message.textContent = text.message;
  updateTinyTilesFacts(rawStatus, state, el.facts);
  if (el.progress) {
    el.progress.hidden = state !== 'building';
    const rawProgress = Number(rawStatus.progress);
    const hasProgress = state === 'building' && Number.isFinite(rawProgress);
    const progress = Math.max(0, Math.min(100, Math.round(rawProgress)));
    el.progress.classList.toggle('is-determinate', hasProgress);
    el.progress.setAttribute('aria-valuetext', state === 'building'
      ? hasProgress ? `${progress} % – ${text.message}` : text.message
      : state === 'ready' ? 'Abgeschlossen' : 'Nicht aktiv');
    if (hasProgress) el.progress.setAttribute('aria-valuenow', String(progress));
    else el.progress.removeAttribute('aria-valuenow');
    if (el.progressBar) el.progressBar.style.width = hasProgress ? `${progress}%` : '';
  }
  if (el.build) {
    el.build.disabled = state === 'building';
    el.build.textContent = state === 'building'
      ? 'Offline-Karte wird erzeugt …'
      : state === 'ready' ? 'Offline-Karte neu erzeugen' : 'Offline-Karte erzeugen';
  }
  if (el.activate) el.activate.style.display = state === 'ready' ? '' : 'none';
  return state;
}

function stopTinyTilesPolling() {
  if (tinyTilesPollTimer !== null) {
    window.clearTimeout(tinyTilesPollTimer);
    tinyTilesPollTimer = null;
  }
}

function scheduleTinyTilesPolling() {
  if (tinyTilesPollTimer !== null) return;
  tinyTilesPollTimer = window.setTimeout(async () => {
    tinyTilesPollTimer = null;
    const status = await fetchTinyTilesStatus({ silent: true });
    if (status && normalizedTinyTilesState(status.state) === 'building') scheduleTinyTilesPolling();
  }, tinyTilesPollIntervalMs);
}

async function tinyTilesResponseError(res, fallback) {
  if (res.status === 401 || res.status === 403) {
    return 'Administrationsschutz: Server mit -admin-token starten und den Token unten eintragen.';
  }
  const body = await res.json().catch(() => ({}));
  if (typeof body?.error === 'string' && body.error) return body.error;
  if (typeof body?.message === 'string' && body.message) return body.message;
  return `${fallback} (${res.status})`;
}

function tinyTilesStyleSettings(status = {}) {
  const min = Number.isInteger(status.min_zoom) ? status.min_zoom : 8;
  const max = Number.isInteger(status.max_zoom) ? status.max_zoom : 14;
  return {
    map_type: 'vector',
    upstream: '',
    style_url: '/static/styles/tinytiles-minimal.json',
    wms_layers: '',
    attribution: '© OpenStreetMap contributors',
    max_zoom: Math.max(5, Math.min(14, Math.max(min, max))),
  };
}

function syncTinyTilesSourceForm(tiles) {
  const mapType = document.getElementById('mapType');
  const upstream = document.getElementById('tileUpstream');
  const style = document.getElementById('tileStyleUrl');
  const layers = document.getElementById('wmsLayers');
  const attribution = document.getElementById('tileAttribution');
  const maxZoom = document.getElementById('tileMaxZoom');
  if (mapType) mapType.value = tiles.map_type;
  if (upstream) upstream.value = tiles.upstream;
  if (style) style.value = tiles.style_url;
  if (layers) layers.value = tiles.wms_layers;
  if (attribution) attribution.value = tiles.attribution;
  if (maxZoom) maxZoom.value = tiles.max_zoom;
  activeTilePresetID = 'tinytiles_local';
  renderTileSourceCards();
  renderWelcomeOnlineCards();
  sourceSelectionHint(tilePresets.find((preset) => preset.id === 'tinytiles_local') || null);
  updateMapTypeVisibility(tiles.map_type);
}

async function activateTinyTilesMap(status = {}) {
  const tiles = tinyTilesStyleSettings(status);
  const base = preloadedSettings || {};
  tinyTilesLoadRequested = true;
  showTileLoadOverlay({
    title: 'Lokale Karte wird geladen',
    message: 'Die Offline-Kacheln werden im Browser vorbereitet.',
    progress: 100,
    mode: 'tinytiles',
  });
  try {
    const applied = await applyTileLayer({ ...base, tiles: { ...(base.tiles || {}), ...tiles } });
    if (!applied) throw new Error('Die lokale Kartenquelle konnte nicht aktiviert werden.');
    // This only changes the form and the in-memory preview. Clicking the regular
    // “Speichern” button remains the explicit opt-in for persisting the source.
    syncTinyTilesSourceForm(tiles);
  } finally {
    tinyTilesLoadRequested = false;
    hideTileLoadOverlay();
  }
}

function handleTinyTilesStatus(status) {
  const safeStatus = status || { state: 'idle', phase: 'idle', progress: 0 };
  tinyTilesLatestStatus = safeStatus;
  window.tinyTilesLatestStatus = tinyTilesLatestStatus;
  const state = updateTinyTilesUI(safeStatus);
  if (state === 'ready' && Number.isInteger(safeStatus.min_zoom) && Number.isInteger(safeStatus.max_zoom)) {
    const min = document.getElementById('tinyTilesMinZoom');
    const max = document.getElementById('tinyTilesMaxZoom');
    if (min) min.value = String(safeStatus.min_zoom);
    if (max) max.value = String(safeStatus.max_zoom);
  }
  updateTinyTilesPresetSelection();
  const text = tinyTilesStatusText(safeStatus, state);
  if (tinyTilesLoadRequested && state === 'building') {
    showTileLoadOverlay({
      title: 'Offline-Karte wird erzeugt',
      message: text.message,
      progress: safeStatus.progress,
      mode: 'tinytiles',
    });
  }
  if (activeTilePresetID === 'tinytiles_local') {
    renderTileSourceCards();
    sourceSelectionHint(tilePresets.find((preset) => preset.id === 'tinytiles_local') || null);
  }
  const postalButton = document.getElementById('territoryBuildPostal');
  if (postalButton) {
    const buildingPostal = state === 'building' && safeStatus.postal_codes;
    postalButton.disabled = buildingPostal;
    postalButton.textContent = buildingPostal ? 'PLZ-Gebiete werden erzeugt …' : 'PLZ-Gebiete erzeugen';
  }
  const postalStatus = document.getElementById('territoryBuildStatus');
  if (postalStatus) {
    const showPostalStatus = Boolean(safeStatus.postal_codes) && (state === 'building' || state === 'ready' || state === 'failed');
    postalStatus.hidden = !showPostalStatus;
    if (showPostalStatus) {
      const progress = Number.isFinite(Number(safeStatus.progress)) ? ` (${Math.max(0, Math.min(100, Math.round(Number(safeStatus.progress))))} %)` : '';
      postalStatus.textContent = `${safeStatus.message || (state === 'failed' ? 'PLZ-Gebiete konnten nicht erzeugt werden.' : 'PLZ-Gebiete werden verarbeitet.')}${progress}`;
    }
  }
  if (state === 'building') {
    scheduleTinyTilesPolling();
    return;
  }
  stopTinyTilesPolling();
  if (state === 'failed') {
    tinyTilesAutoActivateWhenReady = false;
    if (tinyTilesLoadRequested) {
      tinyTilesLoadRequested = false;
      hideTileLoadOverlay();
    }
  }
  if (state === 'ready' && tinyTilesAutoActivateWhenReady) {
    tinyTilesAutoActivateWhenReady = false;
    activateTinyTilesMap(safeStatus)
      .then(() => showToast('Offline-Karte ist bereit und wird angezeigt. Zum dauerhaften Übernehmen bitte speichern.', 'success', 5500))
      .catch((error) => {
        console.error('Failed to activate tinyTiles map', error);
        showToast('Offline-Karte ist bereit, konnte aber nicht angezeigt werden.', 'error', 5500);
      });
  }
  if (state === 'ready' && safeStatus.territory_layer && safeStatus.territory_layer !== territoryBuildAnnouncedLayer) {
    territoryBuildAnnouncedLayer = safeStatus.territory_layer;
    delete territoryGeoJSONCache[safeStatus.territory_layer];
    void loadTerritoryLayers();
    const count = Number.isInteger(safeStatus.territories) ? safeStatus.territories : 0;
    showToast(`${safeStatus.territory_layer.toUpperCase()} ist bereit${count ? ` (${count} Gebiete)` : ''}.`, 'success', 5000);
  }
}

async function fetchTinyTilesStatus({ silent = false } = {}) {
  if (tinyTilesPollInFlight) return tinyTilesLatestStatus;
  tinyTilesPollInFlight = true;
  try {
    const res = await fetch(tinyTilesBuildEndpoint, {
      headers: adminAuthHeaders({ Accept: 'application/json' }),
      cache: 'no-store',
    });
    if (!res.ok) throw new Error(await tinyTilesResponseError(res, 'tinyTiles-Status nicht abrufbar'));
    const status = await res.json();
    handleTinyTilesStatus(status || {});
    return status;
  } catch (error) {
    stopTinyTilesPolling();
    if (!silent) {
      updateTinyTilesUI({ state: 'idle', message: error instanceof Error ? error.message : 'tinyTiles-Status nicht abrufbar' });
    }
    console.warn('Failed to fetch tinyTiles build status:', error);
    return null;
  } finally {
    tinyTilesPollInFlight = false;
  }
}

function openTinyTilesBuilder() {
  openSettingsSection('tinyTilesHeader', 'tinyTilesSettings', 'tinyTilesSettingsOpen');
  window.setTimeout(() => {
    document.getElementById('tinyTilesSettings')?.scrollIntoView({ block: 'center', behavior: 'smooth' });
  }, 0);
}

async function selectTinyTilesSource({ fromWelcome = false } = {}) {
  const preset = tilePresets.find((item) => item.id === 'tinytiles_local');
  activeTilePresetID = 'tinytiles_local';
  if (preset) setTileSourceForm(preset);
  setTileSourceAdvancedOpen(false);
  renderTileSourceCards();
  sourceSelectionHint(preset || null);
  if (fromWelcome) completeMapWelcome('offline');

  tinyTilesLoadRequested = true;
  showTileLoadOverlay({
    title: 'Lokale Karte wird vorbereitet',
    message: 'Prüfe, ob eine Offline-Karte vorhanden ist …',
    mode: 'tinytiles',
  });
  const status = await fetchTinyTilesStatus({ silent: false });
  if (!status) {
    tinyTilesLoadRequested = false;
    hideTileLoadOverlay();
    openTinyTilesBuilder();
    return;
  }

  const state = normalizedTinyTilesState(status.state);
  if (state === 'ready') {
    try {
      await activateTinyTilesMap(status);
      showToast('Offline-Karte wird angezeigt. Zum dauerhaften Übernehmen bitte speichern.', 'success', 5000);
    } catch (error) {
      console.error('Failed to activate tinyTiles map', error);
      showToast('Offline-Karte konnte nicht angezeigt werden.', 'error', 5000);
    }
    return;
  }
  if (state === 'building') {
    tinyTilesAutoActivateWhenReady = true;
    openTinyTilesBuilder();
    return;
  }
  openTinyTilesBuilder();
  await startTinyTilesBuild();
}

async function startTinyTilesBuild({ postalCodes = false, postalPrefixLength = 3, autoActivate = true, showMapOverlay = true } = {}) {
  let zooms;
  try {
    zooms = tinyTilesZoomsFromUI();
  } catch (error) {
    showToast(error instanceof Error ? error.message : 'Ungültige Zoomstufen.', 'error', 5000);
    return;
  }

  const build = tinyTilesElements().build;
  if (build) build.disabled = true;
  tinyTilesLoadRequested = showMapOverlay;
  handleTinyTilesStatus({ state: 'building', phase: 'preparing', progress: 0, message: 'Offline-Karte wird vorbereitet …' });
  try {
    const res = await fetch(tinyTilesBuildEndpoint, {
      method: 'POST',
      headers: adminAuthHeaders({ 'Content-Type': 'application/json', Accept: 'application/json' }),
      body: JSON.stringify({ ...zooms, postal_codes: postalCodes, postal_prefix_length: postalPrefixLength }),
    });
    if (!res.ok) {
      const message = await tinyTilesResponseError(res, 'Offline-Karte konnte nicht gestartet werden');
      // A second tab may already have started exactly the same job. Treat that
      // as a status refresh rather than pretending the existing build failed.
      if (res.status === 409) {
        tinyTilesAutoActivateWhenReady = autoActivate;
        const status = await fetchTinyTilesStatus({ silent: false });
        if (status) {
          if (normalizedTinyTilesState(status.state) === 'building') {
            showToast('Offline-Karte wird bereits erzeugt.', 'info', 3500);
          }
          return;
        }
      }
      throw new Error(message);
    }
    const status = await res.json().catch(() => ({ state: 'building' }));
    tinyTilesAutoActivateWhenReady = autoActivate;
    handleTinyTilesStatus(status || { state: 'building' });
    showToast('Offline-Karte wird im Hintergrund erzeugt.', 'info', 3500);
  } catch (error) {
    tinyTilesAutoActivateWhenReady = false;
    tinyTilesLoadRequested = false;
    hideTileLoadOverlay();
    const message = error instanceof Error ? error.message : 'Offline-Karte konnte nicht gestartet werden.';
    handleTinyTilesStatus({ state: 'failed', phase: 'failed', progress: 0, error: message });
    showToast(message, 'error', 6000);
  }
}

document.getElementById('tinyTilesBuild')?.addEventListener('click', () => { void startTinyTilesBuild(); });

document.querySelectorAll('.tinytiles-preset').forEach((preset) => {
  preset.addEventListener('click', () => {
    const min = Number.parseInt(preset.dataset.tinytilesMin, 10);
    const max = Number.parseInt(preset.dataset.tinytilesMax, 10);
    if (!Number.isInteger(min) || !Number.isInteger(max)) return;
    const minInput = document.getElementById('tinyTilesMinZoom');
    const maxInput = document.getElementById('tinyTilesMaxZoom');
    if (minInput) minInput.value = String(min);
    if (maxInput) maxInput.value = String(max);
    updateTinyTilesPresetSelection();
  });
});
document.getElementById('tinyTilesMinZoom')?.addEventListener('change', updateTinyTilesPresetSelection);
document.getElementById('tinyTilesMaxZoom')?.addEventListener('change', updateTinyTilesPresetSelection);
updateTinyTilesPresetSelection();

document.getElementById('tinyTilesActivate')?.addEventListener('click', async () => {
  try {
    const status = await fetchTinyTilesStatus({ silent: false });
    if (!status || normalizedTinyTilesState(status.state) !== 'ready') return;
    await activateTinyTilesMap(status);
    showToast('Offline-Karte wird angezeigt. Zum dauerhaften Übernehmen bitte speichern.', 'success', 5000);
  } catch (error) {
    console.error('Failed to activate tinyTiles map', error);
    showToast('Offline-Karte konnte nicht angezeigt werden.', 'error', 5000);
  }
});

// A fresh page can show the existing artifact state, but does not replace the
// user's current source until they explicitly start or activate tinyTiles.
fetchTinyTilesStatus({ silent: false });
window.addEventListener('pagehide', stopTinyTilesPolling, { once: true });

// Vehicle profile loading from API
async function loadVehicleProfiles() {
  try {
    const res = await fetch('/api/v1/profiles');
    if (!res.ok) return;
    const profiles = await res.json();
    const sel = document.getElementById('profile');
    if (!sel) return;
    // remove old dynamic options (preserve the blank entry)
    Array.from(sel.options).forEach(o => { if (o.value !== '') o.remove(); });
    profiles.forEach(p => {
      const opt = document.createElement('option');
      opt.value = p.id;
      opt.textContent = `${p.icon || ''} ${p.label}`.trim();
      sel.appendChild(opt);
    });
    // restore active profile from preloaded settings
    const curProfile = preloadedSettings?.routing?.profile || '';
    if (curProfile) sel.value = curProfile;
  } catch (e) {
    console.warn('Failed to load vehicle profiles:', e);
  }
}
loadVehicleProfiles();

// ─── Territories ────────────────────────────────────────────────────────
// The backend only exposes a minimal, read-only surface (list of layers +
// raw GeoJSON passthrough per layer, see /api/v1/territories). Point-in-
// territory lookups and route transition detection happen client-side here,
// reusing the same GeoJSON already fetched to draw the overlay -- this
// keeps the whole feature frontend-only, with no extra round-trips and no
// changes to the shared route handler/cache on the server.
const territoryGeoJSONCache = {}; // layer name -> parsed FeatureCollection
const TERRITORY_SOURCE_ID = 'territories';
const TERRITORY_FILL_LAYER_ID = 'territories-fill';
let territoryOverlayVisible = false;
let lastTerritoryGeoJSON = null; // last-shown, __color-annotated FeatureCollection, for style-swap rehydration
let territoryBuildAnnouncedLayer = '';

function ensureTerritoryLayer() {
  if (map.getSource(TERRITORY_SOURCE_ID)) return;
  map.addSource(TERRITORY_SOURCE_ID, { type: 'geojson', data: emptyFeatureCollection() });
  map.addLayer({
    id: TERRITORY_FILL_LAYER_ID,
    type: 'fill',
    source: TERRITORY_SOURCE_ID,
    // A PLZ3 feature is a MultiPolygon made from genuine PLZ5 boundaries. Do
    // not stroke every retained component: those internal edges made one
    // correctly grouped PLZ3 region look like many incorrectly recognised
    // regions. Different prefix groups remain clearly visible by fill.
    paint: { 'fill-color': ['get', '__color'], 'fill-opacity': 0.18, 'fill-outline-color': 'rgba(0,0,0,0)' },
  });
  // MapLibre fill layers don't auto-bind a popup per feature the way
  // Leaflet's L.geoJSON onEachFeature did — wire one click handler instead.
  map.on('click', TERRITORY_FILL_LAYER_ID, (e) => {
    const feature = e.features && e.features[0];
    if (!feature) return;
    new maplibregl.Popup().setLngLat(e.lngLat).setHTML(territoryPopupHtml(feature.properties)).addTo(map);
  });
  map.on('mouseenter', TERRITORY_FILL_LAYER_ID, () => { map.getCanvas().style.cursor = 'pointer'; });
  map.on('mouseleave', TERRITORY_FILL_LAYER_ID, () => { map.getCanvas().style.cursor = ''; });
}
// A base-layer switch (map.setStyle()) wipes the territory source too.
registerMapLayerRehydrate(() => {
  if (!territoryOverlayVisible) return;
  ensureTerritoryLayer();
  if (lastTerritoryGeoJSON) map.getSource(TERRITORY_SOURCE_ID).setData(lastTerritoryGeoJSON);
});

function territoryColor(id) {
  const s = String(id ?? '');
  let hash = 0;
  for (let i = 0; i < s.length; i++) hash = (hash * 31 + s.charCodeAt(i)) >>> 0;
  // Murmur3-style finalizer: a plain multiplicative hash leaves adjacent
  // inputs (e.g. "Zone-1"/"Zone-2") only 1-2 apart before the mod, which
  // clusters their hues together. This avalanches the bits first so
  // similar territory_ids still get visually distinct colors.
  hash = Math.imul(hash ^ (hash >>> 16), 2246822507);
  hash = Math.imul(hash ^ (hash >>> 13), 3266489909);
  hash = (hash ^ (hash >>> 16)) >>> 0;
  return `hsl(${hash % 360}, 65%, 50%)`;
}

function territoryPopupHtml(props) {
  props = props || {};
  const id = props.territory_id ?? '';
  const rows = Object.keys(props).filter(k => k !== 'territory_id' && k !== '__color').sort().map(k => {
    const v = Array.isArray(props[k]) ? props[k].join(', ') : props[k];
    return `<div class="territory-popup-row"><span class="territory-popup-key">${escapeHtml(k)}</span><span class="territory-popup-val">${escapeHtml(String(v))}</span></div>`;
  }).join('');
  return `<div class="territory-popup"><strong>${escapeHtml(String(id))}</strong>${rows}</div>`;
}

async function fetchTerritoryGeoJSON(layer) {
  if (territoryGeoJSONCache[layer]) return territoryGeoJSONCache[layer];
  const res = await fetch(`/api/v1/territories/${encodeURIComponent(layer)}`, { headers: { 'Accept': 'application/json' } });
  if (!res.ok) throw new Error(`territory layer ${layer} fetch failed`);
  const data = await res.json();
  territoryGeoJSONCache[layer] = data;
  return data;
}

// Even-odd (PNPOLY) point-in-ring test. GeoJSON rings are [lon, lat] pairs.
function territoryPointInRing(ring, lon, lat) {
  let inside = false;
  for (let i = 0, j = ring.length - 1; i < ring.length; j = i++) {
    const xi = ring[i][0], yi = ring[i][1];
    const xj = ring[j][0], yj = ring[j][1];
    const crosses = ((yi > lat) !== (yj > lat)) && (lon < (xj - xi) * (lat - yi) / (yj - yi) + xi);
    if (crosses) inside = !inside;
  }
  return inside;
}

function territoryPolygonContains(rings, lon, lat) {
  if (!rings || rings.length === 0 || !territoryPointInRing(rings[0], lon, lat)) return false;
  for (let h = 1; h < rings.length; h++) {
    if (territoryPointInRing(rings[h], lon, lat)) return false; // inside a hole
  }
  return true;
}

function territoryGeometryContains(geometry, lon, lat) {
  if (!geometry) return false;
  if (geometry.type === 'Polygon') return territoryPolygonContains(geometry.coordinates, lon, lat);
  if (geometry.type === 'MultiPolygon') return geometry.coordinates.some(poly => territoryPolygonContains(poly, lon, lat));
  return false;
}

function territoryFindFeature(geojson, lat, lon) {
  if (!geojson || !Array.isArray(geojson.features)) return null;
  const matches = geojson.features.filter(f => territoryGeometryContains(f.geometry, lon, lat));
  if (matches.length === 0) return null;
  // Deterministic tie-break when territories in one layer overlap, mirroring
  // the backend's own FindTerritory (lowest territory_id wins).
  matches.sort((a, b) => String(a.properties?.territory_id ?? '').localeCompare(String(b.properties?.territory_id ?? '')));
  return matches[0];
}

function territoryHaversineMeters(lat1, lon1, lat2, lon2) {
  const R = 6371000, toRad = d => d * Math.PI / 180;
  const dLat = toRad(lat2 - lat1), dLon = toRad(lon2 - lon1);
  const a = Math.sin(dLat / 2) ** 2 + Math.cos(toRad(lat1)) * Math.cos(toRad(lat2)) * Math.sin(dLon / 2) ** 2;
  return 2 * R * Math.atan2(Math.sqrt(a), Math.sqrt(1 - a));
}

// Mirrors the server's TerritoryEventsForPath: walks the route polyline,
// grouping consecutive same-territory points into entered/left-at-km events.
function territoryEventsForPath(geojson, path) {
  if (!geojson || !path || path.length === 0) return [];
  const events = [];
  let cur = null, km = 0;
  for (let i = 0; i < path.length; i++) {
    if (i > 0) km += territoryHaversineMeters(path[i - 1].lat, path[i - 1].lon, path[i].lat, path[i].lon) / 1000;
    const feature = territoryFindFeature(geojson, path[i].lat, path[i].lon);
    const id = feature ? (feature.properties?.territory_id ?? '') : '';
    if (!id) {
      if (cur) { cur.left_at_km = km; events.push(cur); cur = null; }
    } else if (!cur) {
      cur = { territory_id: id, entered_at_km: km };
    } else if (cur.territory_id !== id) {
      cur.left_at_km = km; events.push(cur);
      cur = { territory_id: id, entered_at_km: km };
    }
  }
  if (cur) { cur.left_at_km = km; events.push(cur); }
  return events;
}

function renderTerritoryTransitionsList(events) {
  const el = document.getElementById('territoryTransitions');
  if (!el) return;
  if (!events || events.length === 0) { el.style.display = 'none'; el.innerHTML = ''; return; }
  el.innerHTML = '<div class="territory-transitions-title">Gebietswechsel</div>' + events.map(ev =>
    `<div class="territory-transition-row">` +
    `<span class="territory-swatch" style="background:${territoryColor(ev.territory_id)}"></span>` +
    `<span class="territory-transition-id">${escapeHtml(ev.territory_id)}</span>` +
    `<span class="territory-transition-range">${ev.entered_at_km.toFixed(1)}–${ev.left_at_km.toFixed(1)} km</span>` +
    `</div>`
  ).join('');
  el.style.display = 'block';
}

async function updateTerritoryRouteTransitions(path) {
  const el = document.getElementById('territoryTransitions');
  if (!el) return;
  const toggle = document.getElementById('territoryRouteEvents');
  const layerName = document.getElementById('territoryLayer')?.value;
  if (!toggle?.checked || !layerName || !path || path.length === 0) {
    el.style.display = 'none'; el.innerHTML = '';
    return;
  }
  try {
    const geojson = await fetchTerritoryGeoJSON(layerName);
    renderTerritoryTransitionsList(territoryEventsForPath(geojson, path));
  } catch (e) {
    console.warn('Territory route transitions failed:', e);
    el.style.display = 'none';
  }
}

async function setTerritoryOverlayVisible(visible) {
  territoryOverlayVisible = visible;
  if (!visible) {
    lastTerritoryGeoJSON = null;
    if (map.getSource(TERRITORY_SOURCE_ID)) map.getSource(TERRITORY_SOURCE_ID).setData(emptyFeatureCollection());
    return;
  }
  const layerName = document.getElementById('territoryLayer')?.value;
  const checkbox = document.getElementById('territoryShowOnMap');
  if (!layerName) {
    showToast('Bitte zuerst eine Gebietsebene wählen', 'info', 2200);
    if (checkbox) checkbox.checked = false;
    territoryOverlayVisible = false;
    return;
  }
  try {
    const geojson = await fetchTerritoryGeoJSON(layerName);
    // Precompute each feature's fill color: MapLibre paint expressions can't
    // call an arbitrary JS hash function like territoryColor() at render
    // time, so it's injected as a property instead and read back via ['get'].
    const colored = {
      ...geojson,
      features: (geojson.features || []).map((f) => ({
        ...f,
        properties: { ...f.properties, __color: territoryColor(f.properties?.territory_id) },
      })),
    };
    lastTerritoryGeoJSON = colored;
    ensureTerritoryLayer();
    map.getSource(TERRITORY_SOURCE_ID).setData(colored);
  } catch (e) {
    console.error('Territory overlay failed:', e);
    showToast('Gebiete konnten nicht geladen werden', 'error', 3000);
    if (checkbox) checkbox.checked = false;
    territoryOverlayVisible = false;
  }
}

async function loadTerritoryLayers() {
  const select = document.getElementById('territoryLayer');
  const empty = document.getElementById('territoryEmpty');
  const content = document.getElementById('territoryContent');
  const badge = document.getElementById('territoryStatusBadge');
  if (!select) return;
  let layers = [];
  try {
    const res = await fetch('/api/v1/territories', { headers: { 'Accept': 'application/json' } });
    if (res.ok) layers = (await res.json()).layers || [];
  } catch (e) { /* territories are optional; silently show the empty state */ }

  select.innerHTML = '';
  if (layers.length === 0) {
    if (empty) empty.style.display = 'block';
    if (content) content.style.display = 'none';
    if (badge) badge.style.display = 'none';
    return;
  }
  if (empty) empty.style.display = 'none';
  if (content) content.style.display = 'block';
  if (badge) { badge.textContent = `${layers.length} Ebene${layers.length === 1 ? '' : 'n'}`; badge.className = 'status-badge ok'; badge.style.display = 'inline-block'; }
  layers.forEach(l => {
    const opt = document.createElement('option');
    opt.value = l.id;
    opt.textContent = `${l.id} (${l.territories})`;
    select.appendChild(opt);
  });
  if (territoryBuildAnnouncedLayer && layers.some(l => l.id === territoryBuildAnnouncedLayer)) {
    select.value = territoryBuildAnnouncedLayer;
  }
  if (document.getElementById('territoryShowOnMap')?.checked) void setTerritoryOverlayVisible(true);
}

// Makes an entire card header act as the collapse/expand toggle (click or
// Enter/Space), while leaving the dedicated chevron button's own click
// handling untouched. Shared so every sidebar card (AI, Territories,
// Settings, Shortcuts) behaves the same way instead of only the AI card's
// header being fully clickable.
function wireCollapsibleHeader(headerEl, toggleBtnEl, toggleFn) {
  if (!headerEl) return;
  headerEl.addEventListener('click', (e) => {
    if (toggleBtnEl && (e.target === toggleBtnEl || toggleBtnEl.contains(e.target))) return;
    toggleFn();
  });
  headerEl.addEventListener('keydown', (e) => {
    if (e.key !== 'Enter' && e.key !== ' ') return;
    e.preventDefault();
    toggleFn();
  });
}

// Territories card collapse/expand (same pattern as the settings/AI cards)
const territoryToggleEl = document.getElementById('territoryToggle');
const territoryBodyEl = document.getElementById('territoryBody');
const territoryCardEl = document.getElementById('territoryCard');
const territoryCardHeaderEl = document.getElementById('territoryCardHeader');
if (territoryToggleEl && territoryBodyEl && territoryCardEl) {
  const setTerritoryOpen = (open) => {
    territoryBodyEl.style.display = open ? 'block' : 'none';
    territoryCardEl.classList.toggle('collapsed', !open);
    territoryToggleEl.setAttribute('aria-expanded', open ? 'true' : 'false');
    territoryCardHeaderEl?.setAttribute('aria-expanded', open ? 'true' : 'false');
    territoryToggleEl.textContent = open ? '‹' : '›';
    localStorage.setItem('territoryOpen', open ? '1' : '0');
  };
  territoryToggleEl.addEventListener('click', () => setTerritoryOpen(territoryBodyEl.style.display === 'none'));
  wireCollapsibleHeader(territoryCardHeaderEl, territoryToggleEl, () => setTerritoryOpen(territoryBodyEl.style.display === 'none'));
  setTerritoryOpen(localStorage.getItem('territoryOpen') === '1');
}

document.getElementById('territoryLayer')?.addEventListener('change', () => {
  if (document.getElementById('territoryShowOnMap')?.checked) setTerritoryOverlayVisible(true);
  updateTerritoryRouteTransitions(lastRoutePath);
});
document.getElementById('territoryShowOnMap')?.addEventListener('change', (e) => setTerritoryOverlayVisible(e.target.checked));
document.getElementById('territoryRouteEvents')?.addEventListener('change', () => updateTerritoryRouteTransitions(lastRoutePath));
document.getElementById('territoryBuildPostal')?.addEventListener('click', () => {
  const prefixLength = Number.parseInt(document.getElementById('territoryPostalPrefix')?.value, 10);
  if (!Number.isInteger(prefixLength) || prefixLength < 1 || prefixLength > 5) {
    showToast('Bitte eine PLZ-Gliederung von 1 bis 5 wählen.', 'error', 4000);
    return;
  }
  void startTinyTilesBuild({ postalCodes: true, postalPrefixLength: prefixLength, autoActivate: false, showMapOverlay: false });
});

loadTerritoryLayers();

// When a profile is selected, auto-apply its default objective if the user
// hasn't explicitly changed it.
const profileSelEl = document.getElementById('profile');
if (profileSelEl) {
  profileSelEl.addEventListener('change', async function() {
    if (this.value) {
      try {
        const res = await fetch('/api/v1/profiles');
        if (res.ok) {
          const profiles = await res.json();
          const def = profiles.find(p => p.id === this.value);
          if (def) {
            const objEl = document.getElementById('objective');
            if (objEl && def.objective) objEl.value = def.objective;
          }
        }
      } catch (e) { /* non-critical */ }
    }
    // A route already on screen reflects the previous profile — recompute it
    // instead of silently leaving a stale (e.g. car) route displayed after
    // switching to a different vehicle profile (e.g. cycling).
    const detailsEl = document.getElementById('routeDetails');
    if (detailsEl && detailsEl.style.display !== 'none') {
      showToast('Profil geändert – Route wird neu berechnet…', 'info', 1800);
      try { await compute(); } catch (e) { /* compute() already surfaces errors */ }
    }
  });
}

// Load settings (use preloaded or fetch)
(async () => {
  try {
    let s = preloadedSettings;
    if (!s) {
      // Fallback to API if preload failed
      s = await apiGetSettings();
    }
    initializeSettingsUI(s);
  } catch (e) {
    console.error('Failed to load settings:', e);
  }
})();

// Mark app as initialized for page spinner
window.initializeApp = true;

document.getElementById('save').onclick = async () => {
  const btn = document.getElementById('save');
  const origText = btn.innerHTML;
  btn.innerHTML = '⏳ Speichern...';
  btn.disabled = true;
  
  try {
    const cur = await apiGetSettings();
    cur.routing = routeOptionsFromUI();
    // collect allowed highways
    const allowed = Array.from(document.querySelectorAll('.allowed-highway')).filter(x=>x.checked).map(x=>x.dataset.type);
    cur.allowed_highway_types = allowed;
    // collect speed defaults
    const speedInputs = Array.from(document.querySelectorAll('.speed-input'));
    cur.default_highway_speeds = cur.default_highway_speeds || {};
    speedInputs.forEach(si => { const k=si.dataset.type; const v=parseFloat(si.value); if(!isNaN(v)) cur.default_highway_speeds[k]=v; });
    // collect tile/map source settings
    cur.tiles = tileSettingsFromUI(cur.tiles || {});
    // OpenAI / remote API key
    cur.ai = cur.ai || {};
    const keyEl = document.getElementById('openaiApiKey');
    if (keyEl) cur.ai.openai_api_key = keyEl.value.trim();
    const urlEl = document.getElementById('openaiBaseUrl');
    if (urlEl) cur.ai.openai_base_url = urlEl.value.trim();
    const adminTokenEl = document.getElementById('adminToken');
    if (adminTokenEl) {
      const token = adminTokenEl.value.trim();
      if (token) sessionStorage.setItem('osmminiAdminToken', token);
      else sessionStorage.removeItem('osmminiAdminToken');
    }
    const saved = await apiPutSettings(cur);
    preloadedSettings = saved;
    apiCache.delete('settings'); // Invalidate cache
    // Refresh map tile layer with updated settings
    applyTileLayer(cur);
    // Invalidate AI provider cache so new API key takes effect immediately.
    try { await fetch('/api/v1/ai/status'); } catch (_e) {}
    btn.innerHTML = '✅ Gespeichert';
    showToast('Einstellungen erfolgreich gespeichert', 'success', 2000);
    setTimeout(() => { btn.innerHTML = origText; btn.disabled = false; }, 1500);
  } catch (e) {
    btn.innerHTML = '❌ Fehler';
    const detail = e instanceof Error ? e.message : 'unbekannter Fehler';
    showToast('Einstellungen nicht gespeichert: ' + detail, 'error', 6000);
    setTimeout(() => { btn.innerHTML = origText; btn.disabled = false; }, 2000);
  }
};

// API key eye-toggle (show/hide password)
document.getElementById('toggleApiKeyVis')?.addEventListener('click', () => {
  const inp = document.getElementById('openaiApiKey');
  const eyeOpen = document.getElementById('eyeOpen');
  const eyeClosed = document.getElementById('eyeClosed');
  if (!inp) return;
  const show = inp.type === 'password';
  inp.type = show ? 'text' : 'password';
  if (eyeOpen)   eyeOpen.style.display   = show ? 'none' : 'block';
  if (eyeClosed) eyeClosed.style.display = show ? 'block' : 'none';
});

// settings collapse/expand
const settingsToggle = document.getElementById('settingsToggle');
const settingsBody = document.getElementById('settingsBody');
const settingsCard = document.getElementById('settingsCard');
const settingsCardHeaderEl = document.getElementById('settingsCardHeader');
function setSettingsOpen(open){
  settingsBody.style.display = open ? 'block' : 'none';
  settingsCard.classList.toggle('collapsed', !open);
  settingsToggle.setAttribute('aria-expanded', open ? 'true' : 'false');
  settingsCardHeaderEl?.setAttribute('aria-expanded', open ? 'true' : 'false');
  settingsToggle.textContent = open ? '‹' : '›';
  localStorage.setItem('settingsOpen', open ? '1' : '0');
}
settingsToggle.addEventListener('click', ()=>{ setSettingsOpen(settingsBody.style.display==='none'); });
wireCollapsibleHeader(settingsCardHeaderEl, settingsToggle, () => setSettingsOpen(settingsBody.style.display === 'none'));
// default collapsed
if(localStorage.getItem('settingsOpen') === null) setSettingsOpen(false); else setSettingsOpen(localStorage.getItem('settingsOpen')==='1');

// help card collapse/expand
const helpToggle = document.getElementById('helpToggle');
const helpBody = document.getElementById('helpBody');
const helpCard = document.getElementById('helpCard');
const helpCardHeaderEl = document.getElementById('helpCardHeader');
if (helpToggle) {
  function setHelpOpen(open){
    helpBody.style.display = open ? 'block' : 'none';
    helpCard.classList.toggle('collapsed', !open);
    helpToggle.setAttribute('aria-expanded', open ? 'true' : 'false');
    helpCardHeaderEl?.setAttribute('aria-expanded', open ? 'true' : 'false');
    helpToggle.textContent = open ? '‹' : '›';
    localStorage.setItem('helpOpen', open ? '1' : '0');
  }
  helpToggle.addEventListener('click', ()=>{ setHelpOpen(helpBody.style.display==='none'); });
  wireCollapsibleHeader(helpCardHeaderEl, helpToggle, () => setHelpOpen(helpBody.style.display === 'none'));
  if(localStorage.getItem('helpOpen') === null) setHelpOpen(false); else setHelpOpen(localStorage.getItem('helpOpen')==='1');
}

// Collapsible settings sections
function setupCollapsibleSection(headerId, contentId, storageKey) {
  const header = document.getElementById(headerId);
  const content = document.getElementById(contentId);
  const section = header?.closest('.settings-section');
  if (!header || !content) return;
  
  function setOpen(open) {
    content.style.display = open ? 'grid' : 'none';
    section?.classList.toggle('expanded', open);
    localStorage.setItem(storageKey, open ? '1' : '0');
  }
  
  header.addEventListener('click', () => {
    setOpen(content.style.display === 'none');
  });
  
  // Default collapsed
  const saved = localStorage.getItem(storageKey);
  setOpen(saved === '1');
}

setupCollapsibleSection('highwayHeader', 'allowedHighways', 'highwaysOpen');
setupCollapsibleSection('speedHeader', 'speedDefaults', 'speedsOpen');
setupCollapsibleSection('mapHeader', 'mapSettings', 'mapSettingsOpen');
setupCollapsibleSection('tinyTilesHeader', 'tinyTilesSettings', 'tinyTilesSettingsOpen');

// Sidebar shortcuts expose the three common next steps without requiring a
// long scroll through the tool cards. They reuse the existing expansion
// controls so keyboard, stored collapse state and all normal interactions
// stay identical.
function revealSidebarTool(kind) {
  if (kind === 'map') {
    showMapSourcePicker();
    return;
  }
  const targets = {
    offline: { card: 'settingsCard', body: 'settingsBody', toggle: 'settingsToggle', section: 'tinyTilesHeader' },
    territories: { card: 'territoryCard', body: 'territoryBody', toggle: 'territoryToggle' },
  };
  const target = targets[kind];
  if (!target) return;
  const body = document.getElementById(target.body);
  if (body?.style.display === 'none') document.getElementById(target.toggle)?.click();
  if (target.section) {
    const header = document.getElementById(target.section);
    const content = header?.nextElementSibling;
    if (content?.style.display === 'none') header?.click();
  }
  window.setTimeout(() => document.getElementById(target.section || target.card)?.scrollIntoView({ block: 'start', behavior: 'smooth' }), 0);
}

document.querySelectorAll('[data-sidebar-open]').forEach((button) => {
  button.addEventListener('click', () => revealSidebarTool(button.dataset.sidebarOpen));
});

// Intersection Observer for lazy rendering of collapsed sections
if ('IntersectionObserver' in window) {
  const lazyObserver = new IntersectionObserver((entries) => {
    entries.forEach(entry => {
      if (entry.isIntersecting) {
        entry.target.classList.add('visible');
        lazyObserver.unobserve(entry.target);
      }
    });
  }, { rootMargin: '50px' });
  
  document.querySelectorAll('.settings-section').forEach(section => {
    lazyObserver.observe(section);
  });
}

document.getElementById('resetSettings').addEventListener('click', async ()=>{
  if (!confirm('Einstellungen zurücksetzen? Die Seite wird neu geladen.')) return;
  showToast('Einstellungen werden zurückgesetzt...', 'info', 1500);
  setTimeout(() => window.location.reload(), 500);
});

// UI helpers
function showSpinner(on) {
  const s = document.getElementById('spinner');
  if (!s) return; s.style.display = on ? 'inline-block' : 'none';
  s.setAttribute('aria-hidden', on ? 'false' : 'true');
}

function setComputeDisabled(dis) {
  const btn = document.getElementById('go');
  if (!btn) return;
  if (dis) { btn.setAttribute('disabled','disabled'); btn.setAttribute('aria-disabled','true'); }
  else { btn.removeAttribute('disabled'); btn.setAttribute('aria-disabled','false'); }
}

// highlight occurrences of q in text (case-insensitive)
function highlight(text, q) {
  if(!text || !q) return escapeHtml(text || '');
  try{
    const re = new RegExp('(' + q.replace(/[-/\\^$*+?.()|[\]{}]/g,'\\$&') + ')','ig');
    return escapeHtml(text).replace(re, '<mark>$1</mark>');
  } catch(e) { return escapeHtml(text); }
}

// keyboard shortcuts
document.addEventListener('keydown', (ev) => {
  // Cmd/Ctrl + Enter to compute
  if ((ev.ctrlKey || ev.metaKey) && ev.key === 'Enter') {
    ev.preventDefault(); 
    compute();
  }
  // Cmd/Ctrl + K to focus start input
  if ((ev.ctrlKey || ev.metaKey) && ev.key === 'k') {
    ev.preventDefault();
    document.getElementById('from')?.focus();
  }
  // Escape to blur active input
  if (ev.key === 'Escape') {
    document.activeElement?.blur();
  }
});

// Emergency mode change handler
document.getElementById('emergencyMode').addEventListener('change', (ev) => {
  try { ev.target.setAttribute('aria-checked', ev.target.checked ? 'true' : 'false'); } catch (e) {}
  if (ev.target.checked) {
    showToast('🚒 Einsatzmodus aktiviert', 'info', 2000);
  }
  compute();
});

// No-left-turn change handler
document.getElementById('noLeftTurn').addEventListener('change', (ev) => {
  try { ev.target.setAttribute('aria-checked', ev.target.checked ? 'true' : 'false'); } catch (e) {}
  compute();
});

// ---- AI Integration ----

// AI card collapse/expand
const aiToggle = document.getElementById('aiToggle');
const aiBody = document.getElementById('aiBody');
const aiCard = document.getElementById('aiCard');
const aiCardHeader = document.getElementById('aiCardHeader');
if (aiToggle) {
  function setAIOpen(open){
    aiBody.style.display = open ? 'block' : 'none';
    aiCard.classList.toggle('collapsed', !open);
    aiToggle.setAttribute('aria-expanded', open ? 'true' : 'false');
    if (aiCardHeader) aiCardHeader.setAttribute('aria-expanded', open ? 'true' : 'false');
    aiToggle.textContent = open ? '‹' : '›';
    localStorage.setItem('aiOpen', open ? '1' : '0');
    if (open && !aiCard.dataset.checked) {
      aiCard.dataset.checked = '1';
      checkAIStatus();
    }
  }
  // Clicking the toggle button or anywhere on the header toggles the panel
  aiToggle.addEventListener('click', (e) => { e.stopPropagation(); setAIOpen(aiBody.style.display === 'none'); });
  wireCollapsibleHeader(aiCardHeader, aiToggle, () => setAIOpen(aiBody.style.display === 'none'));
  if(localStorage.getItem('aiOpen') === null) setAIOpen(false); else setAIOpen(localStorage.getItem('aiOpen')==='1');
}

let aiAvailable = false;
let aiModels = [];

// Persist AI session ID across page reloads via localStorage.
// _aiSessionId holds the current multi-turn session id in memory;
// localStorage mirrors it for persistence across page reloads.
let _aiSessionId = localStorage.getItem('ai_session_id') || '';
function getAISessionId() {
  return _aiSessionId;
}
function setAISessionId(sid) {
  if (sid) {
    _aiSessionId = sid;
    localStorage.setItem('ai_session_id', sid);
  }
}

// Clear chat history
document.getElementById('aiClearChat')?.addEventListener('click', () => {
  const messagesEl = document.getElementById('aiMessages');
  if (messagesEl) messagesEl.innerHTML = '';
  // reset session so next message starts a fresh context
  _aiSessionId = '';
  localStorage.removeItem('ai_session_id');
  lastAIResponse = null;
  showToast('Chatverlauf gelöscht', 'info', 1500);
});

async function checkAIStatus() {
  const statusEl = document.getElementById('aiStatus');
  const modelSelectEl = document.getElementById('aiModelSelect');
  const sendBtn = document.getElementById('aiSend');
  
  try {
    const res = await fetch('/api/v1/ai/status');
    if (!res.ok) throw new Error('AI status check failed');
    const data = await res.json();
    
    if (data.available) {
      aiAvailable = true;
      aiModels = [];
      const select = document.getElementById('aiModel');
      select.innerHTML = '';
      
      data.providers.forEach(p => {
        if (p.available && p.models) {
          p.models.forEach(m => {
            aiModels.push({provider: p.name, model: m});
            const opt = document.createElement('option');
            opt.value = m;
            opt.textContent = `${p.name}: ${m}`;
            select.appendChild(opt);
          });
        }
      });
      
      if (aiModels.length > 0) {
        modelSelectEl.style.display = 'block';
        sendBtn.disabled = false;
        const providers = data.providers.filter(p => p.available).map(p => p.name).join(', ');
        statusEl.textContent = `${providers} · ${aiModels.length} Modell${aiModels.length>1?'e':''}`;
        const badge = document.getElementById('aiStatusBadge');
        if (badge) { badge.textContent = '● Online'; badge.className = 'status-badge ok'; badge.style.display = 'inline-flex'; }
      } else {
        statusEl.textContent = 'Provider erreichbar, aber keine Modelle geladen.';
      }
    } else {
      statusEl.innerHTML = 'Keine KI verfügbar. Starte <a href="https://ollama.com" target="_blank">Ollama</a> oder <a href="https://lmstudio.ai" target="_blank">LM Studio</a>.';
    }
  } catch (e) {
    statusEl.textContent = 'KI-Status nicht abrufbar.';
  }
}

async function sendAIQuery() {
  const input = document.getElementById('aiPrompt');
  const prompt = input.value.trim();
  if (!prompt || !aiAvailable) return;
  
  const messagesEl = document.getElementById('aiMessages');
  const sendBtn = document.getElementById('aiSend');
  
  // Add user message
  const userMsg = document.createElement('div');
  userMsg.className = 'ai-message ai-user';
  userMsg.textContent = prompt;
  messagesEl.appendChild(userMsg);
  
  // Add loading indicator
  const loadingMsg = document.createElement('div');
  loadingMsg.className = 'ai-message ai-assistant';
  loadingMsg.innerHTML = '<span class="spinner" style="width:14px;height:14px;border-width:2px;"></span> Denke nach...';
  messagesEl.appendChild(loadingMsg);
  messagesEl.scrollTop = messagesEl.scrollHeight;
  
  input.value = '';
  sendBtn.disabled = true;
  
  try {
    const model = document.getElementById('aiModel').value || '';
    // Helper: attempt a one-time browser geolocation read (short timeout)
    async function tryObtainUserLocation(ms) {
      return new Promise((resolve) => {
        if (userLocation && typeof userLocation.lat === 'number' && typeof userLocation.lon === 'number') {
          return resolve(userLocation);
        }
        if (!navigator.geolocation) return resolve(null);
        let done = false;
        const tid = setTimeout(() => { if (!done) { done = true; resolve(null); } }, ms);
        navigator.geolocation.getCurrentPosition((pos) => {
          if (done) return;
          done = true; clearTimeout(tid);
          userLocation = { lat: pos.coords.latitude, lon: pos.coords.longitude };
          resolve(userLocation);
        }, (err) => {
          if (done) return;
          done = true; clearTimeout(tid);
          resolve(null);
        }, { enableHighAccuracy: true, timeout: ms });
      });
    }

    // Try to obtain user location (non-blocking but awaited with short timeout)
    try { await tryObtainUserLocation(3000); } catch (e) {}

    // include map center as location hint if available
    const payload = { prompt, model };
    // propagate AI session id for multi-turn context when available
    const _sid = getAISessionId();
    if (_sid) payload.session = _sid;
    // include current route context so the LLM has accurate info
    try {
      const fromVal = document.getElementById('from')?.value?.trim();
      const toVal = document.getElementById('to')?.value?.trim();
      const distText = document.getElementById('detailDistance')?.textContent?.trim();
      const engineText = document.getElementById('detailEngine')?.textContent?.trim();
      if (fromVal) payload.route_from = fromVal;
      if (toVal) payload.route_to = toVal;
      if (distText) {
        const distKm = parseFloat(distText);
        if (!isNaN(distKm)) payload.route_dist_m = distKm * 1000;
      }
      if (engineText) payload.route_engine = engineText;
      // include route bounding box for poi_on_route queries
      if (currentRouteBBox) {
        payload.route_bbox_min_lat = currentRouteBBox.minLat;
        payload.route_bbox_min_lon = currentRouteBBox.minLon;
        payload.route_bbox_max_lat = currentRouteBBox.maxLat;
        payload.route_bbox_max_lon = currentRouteBBox.maxLon;
      }
    } catch (_e) {}
    try {
      // Always include current map center as a hint
      if (window.map && typeof map.getCenter === 'function') {
        const c = map.getCenter();
        if (c && typeof c.lat === 'number' && typeof c.lng === 'number') {
          payload.map_lat = c.lat;
          payload.map_lon = c.lng;
          // Do NOT set `lat`/`lon` from map center to avoid misrepresenting user's location.
          // `lat`/`lon` are set only when explicit browser geolocation (`userLocation`) is available.
        }
      }
      // Also include explicit browser geolocation when available
      if (userLocation && typeof userLocation.lat === 'number' && typeof userLocation.lon === 'number') {
        payload.user_lat = userLocation.lat;
        payload.user_lon = userLocation.lon;
        // prefer user coords for backward-compat `lat`/`lon`
        payload.lat = userLocation.lat;
        payload.lon = userLocation.lon;
      }
    } catch (e) {}
    // Follow-up handling: if user asks duration and we have a recent route, answer locally
    const lowerPrompt = (prompt || '').toLowerCase();

    // Local UX shortcut: chained intent
    // "vom aktuellen Ort zur nächsten Tankstelle und dann weiter zum Flughafen ..."
    // If we already have a tankstelle destination in #to, keep it as waypoint and route onward.
    try {
      const fromEl = document.getElementById('from');
      const toEl = document.getElementById('to');
      const chainedFuelToDestination = (prompt || '').trim().match(/(?:ich\s+meinte|gemeint|vom\s+aktuellen\s+ort|von\s+meinem\s+standort|von\s+hier|vom\s+standort).*(?:n(?:ä|ae)chst\w*\s+tankstelle).*(?:und\s+dann\s+)?weiter(?:\s+(?:zum|zur|nach))\s+(.+)$/i);
      if (chainedFuelToDestination && fromEl && toEl) {
        const finalDestination = (chainedFuelToDestination[1] || '').trim();
        if (finalDestination) {
          // Prefer explicit browser location as actual start when available.
          if (userLocation && typeof userLocation.lat === 'number' && typeof userLocation.lon === 'number') {
            fromEl.value = `${userLocation.lat.toFixed(6)},${userLocation.lon.toFixed(6)}`;
          }

          // Preserve current destination (nearest fuel stop) as waypoint for the onward route.
          const fuelStop = (toEl.value || '').trim();
          if (fuelStop && fuelStop.toLowerCase() !== finalDestination.toLowerCase()) {
            const exists = waypoints.some(w => (w && w.input && w.input.value || '').trim().toLowerCase() === fuelStop.toLowerCase());
            if (!exists) addWaypointWithValue(fuelStop);
          }

          toEl.value = finalDestination;
          loadingMsg.innerHTML = `<div style="font-size:11px;color:var(--text-muted);margin-bottom:4px;">lokal</div>Mehrziel-Route erkannt: <strong>${escapeHtml(fromEl.value || 'Start')}</strong> → <strong>${escapeHtml(fuelStop || 'Tankstelle')}</strong> → <strong>${escapeHtml(finalDestination)}</strong><br/>Berechne Route...`;
          await compute();
          const dist = document.getElementById('detailDistance')?.textContent || '';
          const dur = document.getElementById('detailDuration')?.textContent || '';
          if (dist || dur) {
            loadingMsg.innerHTML = `<div style="font-size:11px;color:var(--text-muted);margin-bottom:4px;">lokal</div>Mehrziel-Route berechnet: <strong>${escapeHtml(dist)}</strong>${dur ? ` • ${escapeHtml(dur)}` : ''}`;
          }
          sendBtn.disabled = false;
          messagesEl.scrollTop = messagesEl.scrollHeight;
          return;
        }
      }
    } catch (e) { console.warn('chained-fuel shortcut failed', e); }

    // Local UX shortcut: "weiter zum ..." should continue from current destination
    // and directly compute a new route instead of asking the LLM again.
    try {
      const continueMatch = (prompt || '').trim().match(/^(?:und\s+)?weiter(?:\s+(?:zum|zur|nach))?\s+(.+)$/i);
      const fromEl = document.getElementById('from');
      const toEl = document.getElementById('to');
      if (continueMatch && fromEl && toEl && toEl.value.trim()) {
        const nextDestination = (continueMatch[1] || '').trim();
        // Guard: this shortcut should only handle short direct continuations.
        // Longer correction/explanation sentences are handled by other intent branches.
        const looksLikeSentence = /\b(ich|meinte|vom|von|und dann|zur n|zur naechsten|zur nächsten)\b/i.test(nextDestination) || nextDestination.split(/\s+/).length > 6;
        if (nextDestination && !looksLikeSentence) {
          fromEl.value = toEl.value.trim();
          toEl.value = nextDestination;
          loadingMsg.innerHTML = `<div style="font-size:11px;color:var(--text-muted);margin-bottom:4px;">lokal</div>Weiterfahrt: <strong>${escapeHtml(fromEl.value)}</strong> → <strong>${escapeHtml(nextDestination)}</strong><br/>Berechne Route...`;
          await compute();
          const dist = document.getElementById('detailDistance')?.textContent || '';
          const dur = document.getElementById('detailDuration')?.textContent || '';
          if (dist || dur) {
            loadingMsg.innerHTML = `<div style="font-size:11px;color:var(--text-muted);margin-bottom:4px;">lokal</div>Route aktualisiert: <strong>${escapeHtml(dist)}</strong>${dur ? ` • ${escapeHtml(dur)}` : ''}`;
          }
          sendBtn.disabled = false;
          messagesEl.scrollTop = messagesEl.scrollHeight;
          return;
        }
      }
    } catch (e) { console.warn('continue-route shortcut failed', e); }

    // Local UX shortcut: simple preference confirmations should trigger compute directly.
    try {
      const fromEl = document.getElementById('from');
      const toEl = document.getElementById('to');
      const isPreferenceOnly = /^(normal|ganz normal|standard|standardroute|schnellste|kürzeste)$/i.test((prompt || '').trim());
      if (isPreferenceOnly && fromEl && toEl && fromEl.value.trim() && toEl.value.trim()) {
        loadingMsg.innerHTML = '<div style="font-size:11px;color:var(--text-muted);margin-bottom:4px;">lokal</div>Präferenz erkannt, berechne Route...';
        await compute();
        const dist = document.getElementById('detailDistance')?.textContent || '';
        const dur = document.getElementById('detailDuration')?.textContent || '';
        if (dist || dur) {
          loadingMsg.innerHTML = `<div style="font-size:11px;color:var(--text-muted);margin-bottom:4px;">lokal</div>Route berechnet: <strong>${escapeHtml(dist)}</strong>${dur ? ` • ${escapeHtml(dur)}` : ''}`;
        }
        sendBtn.disabled = false;
        messagesEl.scrollTop = messagesEl.scrollHeight;
        return;
      }
    } catch (e) { console.warn('preference shortcut failed', e); }

    // Local UX shortcut: "Route auf der Karte anzeigen" / "ausgeben" should use existing
    // map route first, and only compute when no route is currently displayed.
    try {
      const showRouteIntent = /(route.*(karte|anzeigen|zeige|einblenden)|auf der karte anzeigen|route anzeigen|ausgeben|abbiegehinweise|abbiegehinweis|anweisungen)/i.test(lowerPrompt);
      const fromEl = document.getElementById('from');
      const toEl = document.getElementById('to');
      if (showRouteIntent) {
        if (polyline) {
          map.fitBounds(polyline.getBounds(), { padding: 40 });
          const dist = document.getElementById('detailDistance')?.textContent || '';
          const dur = document.getElementById('detailDuration')?.textContent || '';
          loadingMsg.innerHTML = `<div style="font-size:11px;color:var(--text-muted);margin-bottom:4px;">lokal</div>Route auf der Karte angezeigt${dist || dur ? `: <strong>${escapeHtml(dist)}</strong>${dur ? ` • ${escapeHtml(dur)}` : ''}` : ''}`;
          sendBtn.disabled = false;
          messagesEl.scrollTop = messagesEl.scrollHeight;
          return;
        }
        if (fromEl && toEl && fromEl.value.trim() && toEl.value.trim()) {
          loadingMsg.innerHTML = '<div style="font-size:11px;color:var(--text-muted);margin-bottom:4px;">lokal</div>Keine aktive Route, berechne jetzt...';
          await compute();
          const dist = document.getElementById('detailDistance')?.textContent || '';
          const dur = document.getElementById('detailDuration')?.textContent || '';
          loadingMsg.innerHTML = `<div style="font-size:11px;color:var(--text-muted);margin-bottom:4px;">lokal</div>Route berechnet und angezeigt${dist || dur ? `: <strong>${escapeHtml(dist)}</strong>${dur ? ` • ${escapeHtml(dur)}` : ''}` : ''}`;
          sendBtn.disabled = false;
          messagesEl.scrollTop = messagesEl.scrollHeight;
          return;
        }
      }
    } catch (e) { console.warn('show-route shortcut failed', e); }

    // If user issues a correction/negation asking for recalculation, trigger a fresh compute
    try {
      const correctionRE = /\b(falsch|nein|nö|nicht richtig|nicht korrekt|korrigier|korrigiere|noch ?mal|erneut|rechn(e|et|ung)|berechne|neu berechnen|neu berechnen)\b/i;
      if (correctionRE.test(lowerPrompt)) {
        loadingMsg.innerHTML = '<div style="font-size:11px;color:var(--text-muted);margin-bottom:4px;">lokal</div>Berechne Route neu...';
        try {
          await compute();
        } catch (e) { console.warn('recompute failed', e); }
        sendBtn.disabled = false;
        messagesEl.scrollTop = messagesEl.scrollHeight;
        return;
      }
    } catch (e) {}
    if ((lowerPrompt.includes('wie lange') || lowerPrompt.includes('dauert') || lowerPrompt.includes('wie lang')) && lastAIResponse && lastAIResponse.route) {
      // show assistant quick reply with duration/distance
      const meta = lastAIResponse.route;
      const distKm = (meta.distance_m/1000).toFixed(2);
      const durMin = Math.round(meta.duration_s/60);
      const durText = durMin >= 60 ? Math.floor(durMin/60) + 'h ' + (durMin%60) + 'min' : durMin + ' min';
      loadingMsg.innerHTML = `<div style="font-size:11px;color:var(--text-muted);margin-bottom:4px;">local/assistant</div>Die Fahrt dauert ca. <strong>${durText}</strong> (${distKm} km).`;
      renderPath(meta.path, meta);
      setMapsLinks(meta.google_maps_url || meta.googleMapsURL || '', meta.apple_maps_url || meta.appleMapsURL || '');
      sendBtn.disabled = false;
      messagesEl.scrollTop = messagesEl.scrollHeight;
      return;
    }

    // First, try the lightweight local agent retrieval which handles nearest-X queries.
    try {
      const agentPayload = Object.assign({}, payload, { session: getAISessionId() });
      const agentRes = await fetch('/api/v1/agent/query', { method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(agentPayload) });
      if (agentRes && agentRes.ok) {
        const agentData = await agentRes.json();
        if (agentData && Array.isArray(agentData.actions) && agentData.actions.length) {
          // detect noop-only
          const meaningful = agentData.actions.some(a => (a.type && a.type !== 'noop') || (a.Type && a.Type !== 'noop'));
          if (meaningful) {
            // ensure session id
            if (agentData.session_id) setAISessionId(agentData.session_id);
            loadingMsg.innerHTML = '<div style="font-size:11px;color:var(--text-muted);margin-bottom:4px;">lokaler Agent</div>Aktionen werden ausgeführt...';
            await executeAgentActions(agentData.actions, agentData.session_id || getAISessionId());
            sendBtn.disabled = false;
            messagesEl.scrollTop = messagesEl.scrollHeight;
            return;
          }
        }
      }
    } catch (e) {
      // non-fatal: fall back to full AI query
      console.warn('local agent query failed:', e);
    }

    const res = await fetch('/api/v1/ai/query', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(payload)
    });
    
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      throw new Error(err.error || 'KI-Anfrage fehlgeschlagen');
    }
    
    const data = await res.json();
    // persist session id from AI responses for multi-turn requests
    try {
      if (data && (data.session_id || data.sessionId || data.SessionID)) {
        setAISessionId(data.session_id || data.sessionId || data.SessionID);
      }
    } catch (e) {}
    // remember for follow-ups
    try { lastAIResponse = data; } catch(e){}
    // Build the base message text first; we'll append route info below if present.
    loadingMsg.innerHTML = `<div style="font-size:11px;color:var(--text-muted);margin-bottom:4px;">${escapeHtml(data.provider)}/${escapeHtml(data.model)}</div>${escapeHtml(data.response)}`;
    // If the AI returned a computed route, render it on the map and fill inputs
    try {
      if (data && data.route && data.route.path && data.route.path.length) {
        renderPath(data.route.path, data.route);
        // Fill from/to inputs so user can tweak/compute further
        try {
          const fromEl = document.getElementById('from');
          const toEl = document.getElementById('to');
          if (fromEl && data.from && (data.from.query || data.from.label)) fromEl.value = data.from.label || data.from.query;
          if (toEl && data.to && (data.to.query || data.to.label)) toEl.value = data.to.label || data.to.query;
        } catch (e) {}
        setMapsLinks(data.route.google_maps_url || data.route.googleMapsURL || '', data.route.apple_maps_url || data.route.appleMapsURL || '');
        // Append a compact route summary badge to the message
        try {
          const distKm = data.route.distance_m > 0 ? (data.route.distance_m / 1000).toFixed(1) + ' km' : '';
          const durMin = data.route.duration_s > 0 ? Math.round(data.route.duration_s / 60) : 0;
          const durText = durMin >= 60 ? Math.floor(durMin/60) + 'h ' + (durMin%60) + 'min' : (durMin > 0 ? durMin + ' min' : '');
          const parts = [distKm, durText].filter(Boolean).join(' • ');
          if (parts) {
            loadingMsg.innerHTML += `<div style="margin-top:8px;padding:6px 10px;border-radius:6px;background:rgba(110,242,160,0.12);border:1px solid rgba(110,242,160,0.3);font-size:12px;color:#6ef2a0;">✅ Route berechnet: <strong>${parts}</strong></div>`;
          }
        } catch (_e) {}
        showToast(`KI: Route ${(data.route.distance_m/1000).toFixed(1)} km berechnet`, 'success', 2000);
      }
      // If the AI returned suggestions (multiple POI matches), show them on the map
      if (data && data.suggestions && Array.isArray(data.suggestions) && data.suggestions.length) {
        // the map marker display expects objects with lat/lon/label
        showSearchResultsOnMap(data.suggestions);
        // if user asked for stops (via / mit / stop), auto-add suggestions as waypoints
        const p = prompt.toLowerCase();
        if (p.includes('mit') || p.includes('via') || p.includes('stopp') || p.includes('stopps') || p.includes('zwischen')) {
          const toAdd = data.suggestions.map(s => getResultInputValue(s) || s.Label || '').filter(Boolean);
          if (toAdd.length === 0) {
            /* nothing */
          } else if (toAdd.length > 6) {
            if (confirm(`Die KI möchte ${toAdd.length} Zwischenstopps hinzufügen. Wirklich hinzufügen?`)) {
              toAdd.forEach(v => addWaypointWithValue(v));
              showToast('KI: Vorschläge als Zwischenstopps hinzugefügt', 'success', 1800);
            } else {
              showToast('KI: Zwischenstopps verworfen', 'info', 1400);
            }
          } else {
            toAdd.forEach(v => addWaypointWithValue(v));
            showToast('KI: Vorschläge als Zwischenstopps hinzugefügt', 'success', 1800);
          }
        }
      }
      // If AI provided structured from/to/waypoints, apply them
      try {
        if (data && data.from) {
          const fe = document.getElementById('from'); if (fe && (data.from.label || data.from.query)) fe.value = data.from.label || data.from.query;
        }
        if (data && data.to) {
          const te = document.getElementById('to'); if (te && (data.to.label || data.to.query)) te.value = data.to.label || data.to.query;
        }
        if (data && data.waypoints && Array.isArray(data.waypoints)) {
          const qws = data.waypoints.map(w => w && (w.label || w.query)).filter(Boolean);
          if (qws.length > 6) {
            if (confirm(`Die KI hat ${qws.length} vorgeschlagene Zwischenstopps. Wirklich hinzufügen?`)) {
              qws.forEach(v => addWaypointWithValue(v));
            }
          } else {
            qws.forEach(v => addWaypointWithValue(v));
          }
        }
      } catch (e) {}
    } catch (e) { console.warn('Failed to render AI route/suggestions', e); }
  } catch (e) {
    loadingMsg.textContent = '❌ ' + (e.message || 'Fehler');
    loadingMsg.style.color = '#ff6b6b';
  }
  
  sendBtn.disabled = false;
  messagesEl.scrollTop = messagesEl.scrollHeight;
}

document.getElementById('aiSend')?.addEventListener('click', sendAIQuery);
document.getElementById('aiPrompt')?.addEventListener('keydown', (e) => {
  if (e.key === 'Enter') { e.preventDefault(); sendAIQuery(); }
});

// Visual feedback on button clicks
document.querySelectorAll('.btn').forEach(btn => {
  btn.addEventListener('click', function() {
    this.style.transform = 'scale(0.95)';
    setTimeout(() => this.style.transform = '', 100);
  });
});

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

const map = L.map('map').setView([48.7, 12.7], 10);
let currentTileLayer = null;
let tileLayerGeneration = 0;
let userLocationMarker = null;
let userLocation = null; // {lat, lon} from browser geolocation (explicit user permission)
let searchResultMarkers = [];
let searchResultCluster = null;

// Dynamic script/css loader helpers (used for MapLibre GL lazy-loading)
function _loadScript(src) {
  return new Promise((resolve, reject) => {
    const existing = document.querySelector(`script[src="${src}"]`);
    if (existing?.dataset.loadState === 'loaded') { resolve(); return; }
    if (existing) existing.remove();
    const s = document.createElement('script');
    s.src = src;
    s.onload = () => { s.dataset.loadState = 'loaded'; resolve(); };
    s.onerror = () => { s.remove(); reject(new Error(`Could not load ${src}`)); };
    document.head.appendChild(s);
  });
}
function _loadCss(href) {
  return new Promise((resolve, reject) => {
    const existing = document.querySelector(`link[href="${href}"]`);
    if (existing?.dataset.loadState === 'loaded') { resolve(); return; }
    if (existing) existing.remove();
    const l = document.createElement('link');
    l.rel = 'stylesheet'; l.href = href;
    l.onload = () => { l.dataset.loadState = 'loaded'; resolve(); };
    l.onerror = () => { l.remove(); reject(new Error(`Could not load ${href}`)); };
    document.head.appendChild(l);
  });
}
function _loadMapLibreGL() {
  if (window.maplibregl && window.L && L.maplibreGL) return Promise.resolve();
  // Prefer a locally vendored copy (see `make maplibre-assets`, used for the
  // fully offline tinyTiles profile) and fall back to the CDN otherwise, so
  // the vector map works out of the box without a vendoring step.
  const cdnCss = 'https://unpkg.com/maplibre-gl@4.7.1/dist/maplibre-gl.css';
  const cdnJs = 'https://unpkg.com/maplibre-gl@4.7.1/dist/maplibre-gl.js';
  const cdnAdapter = 'https://unpkg.com/@maplibre/maplibre-gl-leaflet@0.0.20/leaflet-maplibre-gl.js';
  return _loadCss('/static/maplibre/maplibre-gl.css').catch(() => _loadCss(cdnCss))
    .then(() => _loadScript('/static/maplibre/maplibre-gl.js').catch(() => _loadScript(cdnJs)))
    .then(() => _loadScript('/static/maplibre/leaflet-maplibre-gl.js').catch(() => _loadScript(cdnAdapter)));
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

function waitForMapLibreLayer(layer, timeoutMs = 5000) {
  return new Promise((resolve) => {
    const glMap = layer?.getMaplibreMap?.();
    if (!glMap) { resolve(false); return; }
    let done = false;
    const finish = (ready) => {
      if (done) return;
      done = true;
      window.clearTimeout(timeout);
      resolve(ready);
    };
    const timeout = window.setTimeout(() => finish(false), timeoutMs);
    if (glMap.loaded?.()) {
      finish(true);
      return;
    }
    glMap.once?.('idle', () => finish(true));
    glMap.once?.('error', () => finish(false));
  });
}

function waitForRasterLayer(layer, timeoutMs = 5000) {
  return new Promise((resolve) => {
    let done = false;
    let loadedTiles = 0;
    let failedTiles = 0;
    const finish = (ready) => {
      if (done) return;
      done = true;
      window.clearTimeout(timeout);
      layer.off?.('tileload', onTileLoad);
      layer.off?.('tileerror', onTileError);
      layer.off?.('load', onLoad);
      resolve(ready);
    };
    const onTileLoad = () => { loadedTiles += 1; };
    const onTileError = () => {
      failedTiles += 1;
      // A single retry/error should not discard a source that has working
      // neighbours. Several failed tiles without one success is conclusive.
      if (loadedTiles === 0 && failedTiles >= 3) finish(false);
    };
    const onLoad = () => finish(loadedTiles > 0);
    const timeout = window.setTimeout(() => finish(loadedTiles > 0), timeoutMs);
    layer.on?.('tileload', onTileLoad);
    layer.on?.('tileerror', onTileError);
    layer.on?.('load', onLoad);
  });
}

async function activateRasterLayer(layer, generation) {
  const previous = currentTileLayer;
  layer.addTo(map);
  const ready = await waitForRasterLayer(layer);
  if (!ready || generation !== tileLayerGeneration) {
    map.removeLayer(layer);
    return false;
  }
  if (previous && previous !== layer) map.removeLayer(previous);
  currentTileLayer = layer;
  await waitForMapLayerPaint();
  return true;
}

// Apply a tile/map layer from settings.  MapLibre/assets are prepared before
// replacing the old layer, so a failed switch leaves the visible map intact.
// A temporary source preview goes directly to the selected provider: the
// server-side cache deliberately only knows the persisted source configuration.
async function applyTileLayer(settings, { directPreview = false } = {}) {
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
      await _loadMapLibreGL();
      if (generation !== tileLayerGeneration) return false;
      const previous = currentTileLayer;
      const layer = L.maplibreGL({ style: tiles.style_url, attribution }).addTo(map);
      const ready = await waitForMapLibreLayer(layer);
      if (!ready || generation !== tileLayerGeneration) {
        map.removeLayer(layer);
        return false;
      }
      if (previous && previous !== layer) map.removeLayer(previous);
      currentTileLayer = layer;
      await waitForMapLayerPaint();
      updateMapModeUI(tiles);
      return true;
    } catch (e) {
      console.warn('MapLibre GL load failed, falling back to raster tiles', e);
    }
  } else if (mapType === 'vector' && tiles.style_url) {
    console.warn('WebGL is unavailable, falling back to raster tiles');
  }

  if (generation !== tileLayerGeneration) return false;
  if (mapType === 'wms' && tiles.upstream && directPreview) {
    const layer = L.tileLayer.wms(tiles.upstream, {
      layers: tiles.wms_layers || '',
      format: 'image/png',
      transparent: false,
      attribution,
      maxZoom,
      updateWhenIdle: true,
      updateWhenZooming: false,
      keepBuffer: 2,
    }).on('tileerror', () => {
      console.warn('Map WMS tile could not be loaded');
    });
    const applied = await activateRasterLayer(layer, generation);
    if (applied) updateMapModeUI(tiles);
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
  const layer = L.tileLayer(rasterFallback.url, {
    maxZoom,
    attribution: rasterFallback.attribution,
    updateWhenIdle: true,
    updateWhenZooming: false,
    keepBuffer: 2,
  }).on('tileerror', () => {
    console.warn('Map tile could not be loaded');
  });
  const applied = await activateRasterLayer(layer, generation);
  if (applied) updateMapModeUI(tiles);
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
    userLocationMarker = L.circleMarker([lat, lon], { radius:6, color:'#2ee6a7', fillColor:'#2ee6a7', fillOpacity:0.9 }).addTo(map).bindPopup('Ihr Standort').openPopup();
    map.setView([lat, lon], 14);
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

let polyline = null;
let startMarker = null, endMarker = null;
let currentRouteBBox = null; // {minLat, minLon, maxLat, maxLon} of the last rendered route
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

function pinIcon(label){
  return L.divIcon({className:'', html:`<div style="font-size:14px; text-shadow: 0 1px 2px black;">📍 ${label}</div>`, iconSize:[30,18]});
}

function syncStopIcons(orderIds) {
  const idToRank = new Map();
  orderIds.forEach((id, i) => idToRank.set(id, i+1));
  
  // Batch icon updates using requestAnimationFrame for better performance
  requestAnimationFrame(() => {
    stops.forEach((s, idx) => {
      const n = idToRank.get(s.id) || (idx+1);
      s.marker.setIcon(pinIcon(n));
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
      el.innerHTML = '<div style="padding:10px; color:#8aaedc; font-style:italic; font-size:12px;">Keine Stops auf der Karte</div>';
      return;
    }

    order.forEach(id => {
    const s = stops.find(x => x.id === id);
    if (!s) return;
    const item = document.createElement('div');
    item.className = 'stop-item';
    item.innerHTML = `<span>📍 ${id}</span> <span style="color:#8aaedc; font-size:11px; margin-left:auto;">${s.lat.toFixed(4)}, ${s.lon.toFixed(4)}</span>`;
    item.title = 'Klicken zum Zentrieren';
    item.style.cursor = 'pointer';
    item.onclick = () => map.panTo([s.lat, s.lon]);
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
  waypoints.forEach(wp=>{ const v=wp.input.value.trim(); if(v) allStops.push({id:wp.id, location:{query:v}}); });
  stops.forEach(s=> allStops.push({id:s.id, location:{lat:s.lat, lon:s.lon}}));
  const plan = { start:{query:from}, end:{query:to}, stops: allStops, dependencies:[], optimize };
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
    btn.textContent = getResultInputValue(sug) || `${(sug.lat || 0).toFixed(5)}, ${(sug.lon || 0).toFixed(5)}`;
    btn.title = [sug.kind || 'Treffer', getResultSecondary(sug)].filter(Boolean).join(' • ');
    btn.addEventListener('click', async () => {
      const val = getResultInputValue(sug) || `${sug.lat},${sug.lon}`;
      const el = document.getElementById(target);
      if (el) el.value = val;
      try {
        if (typeof sug.lat === 'number' && typeof sug.lon === 'number') map.panTo([sug.lat, sug.lon]);
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
  if(coords.length===0) return;
  // Track route bounding box for poi_on_route queries
  currentRouteBBox = coords.reduce((bb, c) => ({
    minLat: Math.min(bb.minLat, c[0]),
    minLon: Math.min(bb.minLon, c[1]),
    maxLat: Math.max(bb.maxLat, c[0]),
    maxLon: Math.max(bb.maxLon, c[1]),
  }), {minLat: coords[0][0], minLon: coords[0][1], maxLat: coords[0][0], maxLon: coords[0][1]});
  polyline = L.polyline(coords,{color:'#3a8eef', weight:5, opacity: 0.8}).addTo(map);
  startMarker = L.circleMarker(coords[0],{radius:7, color:'#6ef2a0', fillColor:'#6ef2a0', fillOpacity:0.8}).addTo(map);
  endMarker = L.circleMarker(coords[coords.length-1],{radius:7, color:'#ffcc66', fillColor:'#ffcc66', fillOpacity:0.8}).addTo(map);
  map.fitBounds(polyline.getBounds(),{padding:[40,40]});
  
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
  }
  
  // Show route actions
  const actionsEl = document.getElementById('routeActions');
  if (actionsEl) actionsEl.style.display = 'flex';
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
      if (lat && lon) map.panTo([lat, lon]);
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
    map.fitBounds(polyline.getBounds(), {padding: [40, 40]});
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
        const qlat = userLocation ? userLocation.lat : null;
        const qlon = userLocation ? userLocation.lon : null;
        const qs = (qlat!==null && qlon!==null) ? `?lat=${qlat}&lon=${qlon}` : '';
        const res = await fetch(`/api/v1/poi/${id}${qs}`);
        if (res.ok) {
          const data = await res.json();
          const m = L.marker([data.lat, data.lon]).addTo(map);
          m.bindPopup(`<strong>${escapeHtml(data.label||'')}</strong>`).openPopup();
          searchResultMarkers.push(m);
          const distText = data.distance_m ? `${Math.round(data.distance_m)} m` : '';
          summaries.push(`Hervorgehoben: ${data.label || ('#' + id)} ${distText}`);
        }
      } catch (e) { console.warn('highlight failed', e); summaries.push('Hervorhebung fehlgeschlagen'); }
    } else if (t === 'show_info') {
      try {
        const id = params.id;
        const qlat = userLocation ? userLocation.lat : null;
        const qlon = userLocation ? userLocation.lon : null;
        const qs = (qlat!==null && qlon!==null) ? `?lat=${qlat}&lon=${qlon}` : '';
        const res = await fetch(`/api/v1/poi/${id}${qs}`);
        if (res.ok) {
          const data = await res.json();
          const html = `<div style="min-width:200px;"><strong>${escapeHtml(data.label||'')}</strong><div style="font-size:12px;opacity:0.8;">${escapeHtml(Object.keys(data.tags||{}).slice(0,6).map(k=>k+': '+data.tags[k]).join('<br/>'))}</div></div>`;
          const m = L.marker([data.lat, data.lon]).addTo(map);
          m.bindPopup(html).openPopup();
          searchResultMarkers.push(m);
          map.panTo([data.lat, data.lon]);
          const distText = data.distance_m ? `${Math.round(data.distance_m)} m` : '';
          summaries.push(`Info angezeigt: ${data.label || ('#' + id)} ${distText}`);
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
    let fromVal = '';
    if (params.from) {
      const f = params.from;
      if (f.query) fromVal = f.query;
      else if (typeof f.lat === 'number' && typeof f.lon === 'number') fromVal = `${f.lat.toFixed(6)},${f.lon.toFixed(6)}`;
    }
    // determine to value; if id present, try to fetch POI label
    let toVal = '';
    if (params.to) {
      const t = params.to;
      if (t.query) toVal = t.query;
      else if (typeof t.lat === 'number' && typeof t.lon === 'number') toVal = `${t.lat.toFixed(6)},${t.lon.toFixed(6)}`;
      else if (t.id) {
        const id = t.id;
        try {
          const qlat = userLocation ? userLocation.lat : null;
          const qlon = userLocation ? userLocation.lon : null;
          const qs = (qlat!==null && qlon!==null) ? `?lat=${qlat}&lon=${qlon}` : '';
          const res = await fetch(`/api/v1/poi/${id}${qs}`);
          if (res.ok) {
            const data = await res.json();
            if (data.label) toVal = data.label;
            else if (typeof data.lat === 'number' && typeof data.lon === 'number') toVal = `${data.lat.toFixed(6)},${data.lon.toFixed(6)}`;
          }
        } catch (e) { /* ignore */ }
      }
    }

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
  const id = 'M'+(stopSeq++);
  const marker = L.marker(ev.latlng,{draggable:true, icon: pinIcon(id)}).addTo(map);
  const s = {id, marker, lat:ev.latlng.lat, lon:ev.latlng.lng};
  stops.push(s); renderStopList();
  marker.on('dragend', ()=>{ 
    const ll=marker.getLatLng(); 
    s.lat=ll.lat; 
    s.lon=ll.lng; 
    renderStopList(); 
    showToast(`Marker ${id} verschoben`, 'info', 1500);
  });
  marker.on('contextmenu', ()=>{ 
    marker.remove(); 
    const i=stops.findIndex(x=>x.id===id); 
    if(i>=0) stops.splice(i,1); 
    renderStopList(); 
    showToast(`Marker ${id} gelöscht`, 'info', 1500);
  });
  showToast(`Marker ${id} hinzugefügt`, 'success', 1500);
});

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
  
  const removeBtn = document.createElement('button');
  removeBtn.textContent = '✕';
  removeBtn.className = 'btn-remove';
  removeBtn.title = 'Entfernen';
  removeBtn.setAttribute('aria-label', 'Zwischenstopp entfernen');
  removeBtn.onclick = () => removeWaypoint(id);
  
  wrapper.appendChild(inputGroup);
  wrapper.appendChild(removeBtn);
  container.appendChild(wrapper);
  
  const suggestHandle = makeSuggest(id + '-suggest', input);
  waypoints.push({id, input, wrapper, suggestHandle});
  
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
        if (!res.ok) { hide(); return; }
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
            const val = getResultInputValue(item) || primary || '';
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

function clearSearchResults() {
  if (searchResultCluster && typeof searchResultCluster.clearLayers === 'function') {
    try { searchResultCluster.clearLayers(); } catch(e) {}
    searchResultCluster = null;
  }
  searchResultMarkers.forEach(m => m.remove());
  searchResultMarkers = [];
}

function showSearchResultsOnMap(results) {
  clearSearchResults();
  if (!Array.isArray(results) || results.length === 0) return;
  const bounds = [];
  // Use marker clustering when available for large result sets, with custom icon
  const useCluster = !!(window.L && typeof L.markerClusterGroup === 'function');
  if (useCluster) {
    searchResultCluster = L.markerClusterGroup({
      spiderfyOnMaxZoom: true,
      showCoverageOnHover: false,
      maxClusterRadius: 40,
      iconCreateFunction: function(cluster) {
        const count = cluster.getChildCount();
        const size = count < 10 ? 'small' : (count < 50 ? 'medium' : 'large');
        const html = `<div class="cluster-icon ${size}"><span>${count}</span></div>`;
        return L.divIcon({ html, className: 'custom-cluster', iconSize: L.point(40, 40) });
      }
    });
  }
  results.forEach((item, idx) => {
    if (!item || !item.lat || !item.lon) return;
    const m = L.marker([item.lat, item.lon], { title: item.label || '' });
    const secondary = getResultSecondary(item);
    let popupHtml = `<strong>${escapeHtml(item.label || '')}</strong><br/>`;
    if (secondary) popupHtml += `<small style="opacity:0.8">${escapeHtml(secondary)}</small>`;
    // add an action button to popup to quickly add this result as waypoint
    const popupWithButton = popupHtml + `<div style="margin-top:6px;text-align:right;">
      <button class="btn btn-sm btn-outline info-btn">Mehr Info</button>
      <button class="btn btn-sm btn-outline add-waypoint-btn">Als Zwischenstopp</button>
    </div>`;
    m.bindPopup(popupWithButton);
    m.on('popupopen', (ev) => {
      const btn = ev.popup.getElement().querySelector('.add-waypoint-btn');
      const infoBtn = ev.popup.getElement().querySelector('.info-btn');
      if (btn) {
        btn.addEventListener('click', () => {
          const lbl = getResultInputValue(item) || '';
          if (lbl) addWaypointWithValue(lbl);
          ev.popup._close();
        });
      }
      if (infoBtn) {
        infoBtn.addEventListener('click', async () => {
          const orig = ev.popup.getContent();
          // show loading
          ev.popup.setContent('<div>Informationen werden geladen…</div>');
          try {
            const qlat = userLocation ? userLocation.lat : null;
            const qlon = userLocation ? userLocation.lon : null;
            const qs = (qlat !== null && qlon !== null) ? `?lat=${qlat}&lon=${qlon}` : '';
            // Search uses "poi" for way-based POIs and also displays address
            // and street results. Only entity kinds accepted by the typed API
            // are encoded; legacy lookup remains useful for the other kinds.
            const entityKind = item.kind === 'poi' ? 'way' : (['node', 'way', 'relation'].includes(item.kind) ? item.kind : '');
            const poiPath = entityKind ? `${entityKind}/${item.id}` : String(item.id);
            const res = await fetch(`/api/v1/poi/${poiPath}${qs}`);
            if (!res.ok) throw new Error('fetch failed');
            const data = await res.json();
            // build info html
            let infoHtml = `<strong>${escapeHtml(data.label || '')}</strong><br/>`;
            if (data.tags) {
              const keys = Object.keys(data.tags).sort();
              infoHtml += '<div style="margin-top:6px; font-size:13px;">';
              keys.forEach(k => {
                const v = data.tags[k];
                infoHtml += `<div><strong>${escapeHtml(k)}:</strong> ${escapeHtml(v)}</div>`;
              });
              infoHtml += '</div>';
            }
            if (data.wiki_summary) {
              infoHtml += `<div style="margin-top:8px; font-size:13px; color:#333;">${escapeHtml(data.wiki_summary)}</div>`;
            }
            if (data.distance_m) {
              infoHtml += `<div style="margin-top:6px; font-size:12px; color:#666;">Entfernung: ${Math.round(data.distance_m)} m</div>`;
            }
            infoHtml += `<div style="margin-top:8px;text-align:right;"><button class=\"btn btn-sm btn-primary route-btn\">Route berechnen</button> <button class=\"btn btn-sm btn-outline back-btn\">Zurück</button></div>`;
            ev.popup.setContent(infoHtml);
            // wire buttons
            setTimeout(() => {
              const el = ev.popup.getElement();
              if (!el) return;
              const rbtn = el.querySelector('.route-btn');
              const bbtn = el.querySelector('.back-btn');
              if (rbtn) rbtn.addEventListener('click', () => {
                const to = data.label || item.label || `${data.lat},${data.lon}`;
                document.getElementById('to').value = to;
                ev.popup._close();
                compute();
              });
              if (bbtn) bbtn.addEventListener('click', () => {
                ev.popup.setContent(orig);
              });
            }, 50);
          } catch (e) {
            ev.popup.setContent('<div>Informationen konnten nicht geladen werden.</div>');
            setTimeout(() => ev.popup.setContent(orig), 2000);
          }
        });
      }
    });
    m.on('click', () => { m.openPopup(); });
    if (searchResultCluster) {
      searchResultCluster.addLayer(m);
    } else {
      m.addTo(map);
    }
    searchResultMarkers.push(m);
    bounds.push([item.lat, item.lon]);
  });
  if (searchResultCluster) {
    map.addLayer(searchResultCluster);
  }
  if (bounds.length === 1) {
    map.panTo(bounds[0]);
  } else if (bounds.length > 1) {
    try { map.fitBounds(bounds, { padding: [40, 40] }); } catch(e) {}
  }
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
document.getElementById('openMapSources')?.addEventListener('click', showMapSourcePicker);
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

async function startTinyTilesBuild() {
  let zooms;
  try {
    zooms = tinyTilesZoomsFromUI();
  } catch (error) {
    showToast(error instanceof Error ? error.message : 'Ungültige Zoomstufen.', 'error', 5000);
    return;
  }

  const build = tinyTilesElements().build;
  if (build) build.disabled = true;
  tinyTilesLoadRequested = true;
  handleTinyTilesStatus({ state: 'building', phase: 'preparing', progress: 0, message: 'Offline-Karte wird vorbereitet …' });
  try {
    const res = await fetch(tinyTilesBuildEndpoint, {
      method: 'POST',
      headers: adminAuthHeaders({ 'Content-Type': 'application/json', Accept: 'application/json' }),
      body: JSON.stringify(zooms),
    });
    if (!res.ok) {
      const message = await tinyTilesResponseError(res, 'Offline-Karte konnte nicht gestartet werden');
      // A second tab may already have started exactly the same job. Treat that
      // as a status refresh rather than pretending the existing build failed.
      if (res.status === 409) {
        tinyTilesAutoActivateWhenReady = true;
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
    tinyTilesAutoActivateWhenReady = true;
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

// When a profile is selected, auto-apply its default objective if the user
// hasn't explicitly changed it.
const profileSelEl = document.getElementById('profile');
if (profileSelEl) {
  profileSelEl.addEventListener('change', async function() {
    if (!this.value) return; // custom — no auto-apply
    try {
      const res = await fetch('/api/v1/profiles');
      if (!res.ok) return;
      const profiles = await res.json();
      const def = profiles.find(p => p.id === this.value);
      if (def) {
        const objEl = document.getElementById('objective');
        if (objEl && def.objective) objEl.value = def.objective;
      }
    } catch (e) { /* non-critical */ }
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
function setSettingsOpen(open){
  settingsBody.style.display = open ? 'block' : 'none';
  settingsCard.classList.toggle('collapsed', !open);
  settingsToggle.setAttribute('aria-expanded', open ? 'true' : 'false');
  settingsToggle.textContent = open ? '‹' : '›';
  localStorage.setItem('settingsOpen', open ? '1' : '0');
}
settingsToggle.addEventListener('click', ()=>{ setSettingsOpen(settingsBody.style.display==='none'); });
// default collapsed
if(localStorage.getItem('settingsOpen') === null) setSettingsOpen(false); else setSettingsOpen(localStorage.getItem('settingsOpen')==='1');

// help card collapse/expand
const helpToggle = document.getElementById('helpToggle');
const helpBody = document.getElementById('helpBody');
const helpCard = document.getElementById('helpCard');
if (helpToggle) {
  function setHelpOpen(open){
    helpBody.style.display = open ? 'block' : 'none';
    helpCard.classList.toggle('collapsed', !open);
    helpToggle.setAttribute('aria-expanded', open ? 'true' : 'false');
    helpToggle.textContent = open ? '‹' : '›';
    localStorage.setItem('helpOpen', open ? '1' : '0');
  }
  helpToggle.addEventListener('click', ()=>{ setHelpOpen(helpBody.style.display==='none'); });
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
  if (aiCardHeader) {
    aiCardHeader.addEventListener('click', (e) => {
      if (e.target === aiToggle) return; // button handles its own click
      setAIOpen(aiBody.style.display === 'none');
    });
    // Keyboard accessibility for the header
    aiCardHeader.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' || e.key === ' ') {
        e.preventDefault();
        setAIOpen(aiBody.style.display === 'none');
      }
    });
  }
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
          map.fitBounds(polyline.getBounds(), { padding: [40, 40] });
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

// Optimized App JS with preloaded settings

// Load settings from inline script (server-rendered)
let preloadedSettings = null;
try {
  const settingsEl = document.getElementById('initialSettings');
  if (settingsEl) {
    preloadedSettings = JSON.parse(settingsEl.textContent);
  }
} catch (e) {
  console.warn('Failed to load preloaded settings:', e);
}

const map = L.map('map').setView([48.7, 12.7], 10);
let currentTileLayer = null;
let userLocationMarker = null;
let userLocation = null; // {lat, lon} from browser geolocation (explicit user permission)
let searchResultMarkers = [];
let searchResultCluster = null;

// Dynamic script/css loader helpers (used for MapLibre GL lazy-loading)
function _loadScript(src) {
  return new Promise((resolve, reject) => {
    if (document.querySelector(`script[src="${src}"]`)) { resolve(); return; }
    const s = document.createElement('script');
    s.src = src;
    s.onload = resolve; s.onerror = reject;
    document.head.appendChild(s);
  });
}
function _loadCss(href) {
  if (document.querySelector(`link[href="${href}"]`)) return;
  const l = document.createElement('link');
  l.rel = 'stylesheet'; l.href = href;
  document.head.appendChild(l);
}
function _loadMapLibreGL() {
  if (window.maplibregl && window.L && L.maplibreGL) return Promise.resolve();
  _loadCss('https://unpkg.com/maplibre-gl@4.7.1/dist/maplibre-gl.css');
  return _loadScript('https://unpkg.com/maplibre-gl@4.7.1/dist/maplibre-gl.js')
    .then(() => _loadScript('https://unpkg.com/@maplibre/maplibre-gl-leaflet@0.0.20/leaflet-maplibre-gl.js'));
}

// Apply a tile/map layer from settings. Removes the old layer first.
async function applyTileLayer(settings) {
  if (currentTileLayer) { map.removeLayer(currentTileLayer); }
  currentTileLayer = null;
  const tiles = (settings && settings.tiles) || {};
  const mapType = tiles.map_type || 'raster';
  const attribution = tiles.attribution || '';

  if (mapType === 'vector' && tiles.style_url) {
    try {
      await _loadMapLibreGL();
      currentTileLayer = L.maplibreGL({ style: tiles.style_url, attribution }).addTo(map);
      return;
    } catch (e) {
      console.warn('MapLibre GL load failed, falling back to raster tiles', e);
    }
  } else if (mapType === 'wms' && tiles.upstream) {
    currentTileLayer = L.tileLayer.wms(tiles.upstream, {
      layers: tiles.wms_layers || '',
      format: 'image/png',
      transparent: false,
      attribution,
      maxZoom: 18,
    }).addTo(map);
    return;
  }
  // Default: raster tiles via the server proxy
  currentTileLayer = L.tileLayer('/tiles/{z}/{x}/{y}.png', {
    maxZoom: 19,
    attribution,
    updateWhenIdle: true,
    updateWhenZooming: false,
    keepBuffer: 2,
  }).addTo(map);
}

// Initialize the tile layer from preloaded (server-side) settings
applyTileLayer(preloadedSettings);

// Toast notification system
function showToast(message, type = 'info', duration = 3000) {
  const toast = document.createElement('div');
  toast.className = `toast ${type}`;
  const icon = type === 'success' ? '✅' : type === 'error' ? '❌' : 'ℹ️';
  toast.innerHTML = `<span style="font-size:18px;">${icon}</span><span>${message}</span>`;
  document.body.appendChild(toast);
  setTimeout(() => {
    toast.style.animation = 'toastSlide 0.3s ease reverse';
    setTimeout(() => toast.remove(), 300);
  }, duration);
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
    if (fromInput) fromInput.value = `${lat.toFixed(6)},${lon.toFixed(6)}`;
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
    input.setAttribute('inputmode', 'none'); // Disable mobile keyboard suggestions
    
    container.appendChild(input);
    
    // Use a workaround: reset value on suspicious autofill attempts
    let lastValue = '';
    let autofillTimer = null;
    input.addEventListener('input', (e) => {
      lastValue = e.target.value;
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
      }, 100);
    });
    input.addEventListener('blur', () => {
      if (autofillTimer !== null) {
        clearInterval(autofillTimer);
        autofillTimer = null;
      }
    });
    
    return input;
  }
  
  const fromInput = createDynamicInput('from-container', 'from', 'Adresse oder lat lon');
  const toInput = createDynamicInput('to-container', 'to', 'Adresse oder lat lon');
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
  const btn = document.getElementById('themeToggle');
  const isDark = !document.documentElement.classList.contains('light-mode');
  btn.textContent = isDark ? '☀️' : '🌙';
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

async function apiPutSettings(settings) {
  const res = await fetch('/api/v1/settings', {
    method: 'PUT',
    headers: {'Content-Type':'application/json'},
    body: JSON.stringify(settings)
  });
  if (!res.ok) throw new Error('settings save failed');
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
    btn.textContent = sug.label || `${(sug.lat || 0).toFixed(5)}, ${(sug.lon || 0).toFixed(5)}`;
    btn.title = sug.kind || 'Treffer';
    btn.addEventListener('click', async () => {
      const val = sug.label || `${sug.lat},${sug.lon}`;
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
  while(stops.length){ const s=stops.pop(); s.marker.remove(); } 
  stopSeq=1; 
  renderStopList(); 
  if(polyline) polyline.remove();
  if(startMarker) startMarker.remove();
  if(endMarker) endMarker.remove();
  document.getElementById('status').textContent = 'Bereit';
  document.getElementById('distance').textContent = '';
  const detailsEl = document.getElementById('routeDetails');
  const actionsEl = document.getElementById('routeActions');
  if (detailsEl) detailsEl.style.display = 'none';
  if (actionsEl) actionsEl.style.display = 'none';
  setMapsLinks('', '');
  showToast('Karte zurückgesetzt', 'info', 1500);
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
        const fe = document.getElementById('from'); if (fe) fe.value = fromVal;
      }
      if (toVal) {
        const te = document.getElementById('to'); if (te) te.value = toVal;
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

addWaypoint();

function addWaypoint() {
  const id = 'WP' + (waypointSeq++);
  const container = document.getElementById('waypointsContainer');
  
  const wrapper = document.createElement('div');
  wrapper.className = 'waypoint';
  wrapper.id = 'waypoint-' + id;
  
  const inputGroup = document.createElement('div');
  inputGroup.className = 'input-group';
  inputGroup.style.flex = '1';
  inputGroup.style.marginBottom = '0';
  
  const input = document.createElement('input');
  input.type = 'text';
  input.placeholder = 'Zwischenstopp ' + waypoints.length;
  input.autocomplete = 'off';
  
  const suggestDiv = document.createElement('div');
  suggestDiv.className = 'suggest';
  suggestDiv.id = id + '-suggest';
  
  inputGroup.appendChild(input);
  inputGroup.appendChild(suggestDiv);
  
  const removeBtn = document.createElement('button');
  removeBtn.textContent = '✕';
  removeBtn.className = 'btn-remove';
  removeBtn.title = 'Entfernen';
  removeBtn.onclick = () => removeWaypoint(id);
  
  wrapper.appendChild(inputGroup);
  wrapper.appendChild(removeBtn);
  container.appendChild(wrapper);
  
  waypoints.push({id, input, wrapper});
  makeSuggest(id + '-suggest', input);
  
  input.addEventListener('change', debouncedCompute);
  
  // Also trigger on Enter key for immediate compute
  input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') {
      e.preventDefault();
      compute();
    }
  });
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

  function hide() { container.style.display = 'none'; }
  function show() { if (container.innerHTML.trim()) container.style.display = 'block'; }

  let lastQuery = '';
  input.addEventListener('input', () => {
    const q = input.value.trim();
    if (timeout) clearTimeout(timeout);
    if (ctrl) ctrl.abort();
    if (q.length < 2) { hide(); return; }
    if (q === lastQuery) return; // Skip duplicate queries
    lastQuery = q;

    timeout = setTimeout(async () => {
      const mySeq = ++seq;
      ctrl = new AbortController();
      try {
        const res = await fetch('/api/v1/search?limit=5&q=' + encodeURIComponent(q), { signal: ctrl.signal });
        if (!res.ok) return;
        const data = await res.json();
        if (mySeq !== seq) return;
        container.innerHTML = '';
        selectedIndex = -1;
        if (!Array.isArray(data) || data.length === 0) { hide(); return; }

        data.slice(0,5).forEach((item, i) => {
          const el = document.createElement('div');
          el.className = 'item';
          el.dataset.index = String(i);
          // build rich label: prefer POI/company name, then street label
          const tags = item.tags || {};
          const primary = tags.name || item.label || tags.brand || tags.operator || '';
          const secondaryParts = [];
          if (tags.shop) secondaryParts.push(tags.shop);
          if (tags.amenity) secondaryParts.push(tags.amenity);
          if (tags['addr:street']) secondaryParts.push(tags['addr:street']);
          if (tags['addr:city']) secondaryParts.push(tags['addr:city']);
          const secondary = secondaryParts.join(' • ');

          // highlight matches and show primary + secondary
          const q = input.value.trim();
          const primHtml = q ? highlight(primary || item.label || '', q) : escapeHtml(primary || item.label || '');
          const secHtml = q ? highlight(secondary, q) : escapeHtml(secondary);
          el.innerHTML = `<div style="display:flex;flex-direction:column;">
                            <div style="font-weight:600;">${primHtml}</div>
                            <div style="font-size:12px; color:rgba(200,220,255,0.6); margin-top:4px;">${secHtml}</div>
                          </div>`;
          el.addEventListener('mouseover', () => { selectedIndex = i; updateActive(); });
          el.onclick = () => { input.value = primary || item.label || ''; hide(); compute(); input.focus(); };
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
    const items = Array.from(container.querySelectorAll('.item'));
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
    } else if (ev.key === 'Escape') {
      hide();
    }
  });

  function updateActive() {
    const items = Array.from(container.querySelectorAll('.item'));
    items.forEach((it, idx) => {
      if (idx === selectedIndex) it.classList.add('active'); else it.classList.remove('active');
    });
    // ensure active item is visible
    const active = container.querySelector('.item.active');
    if (active) active.scrollIntoView({block: 'nearest'});
  }

  document.addEventListener('click', (ev) => {
    if (!container.contains(ev.target) && ev.target !== input) hide();
  });
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
    const tags = item.tags || {};
    let popupHtml = `<strong>${escapeHtml(item.label || '')}</strong><br/>`;
    if (tags['addr:street'] || tags['addr:housenumber']) popupHtml += `${escapeHtml(tags['addr:street']||'')} ${escapeHtml(tags['addr:housenumber']||'')}<br/>`;
    popupHtml += `<small style="opacity:0.8">${escapeHtml((tags['addr:city']||''))}</small>`;
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
          const lbl = item.label || (item.tags && item.tags.name) || '';
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
            const res = await fetch(`/api/v1/poi/${item.id}${qs}`);
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
                const to = `${data.lat},${data.lon}`;
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

// simple HTML escaper for suggestion labels
function escapeHtml(s){
  if(!s) return '';
  return s.replaceAll('&','&amp;').replaceAll('<','&lt;').replaceAll('>','&gt;');
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
    const atEl = document.getElementById('tileAttribution');
    if (atEl) atEl.value = tiles.attribution || '';
    updateMapTypeVisibility(mt);
}

// Show/hide tile source fields based on selected map type
function updateMapTypeVisibility(mt) {
  const upstreamRow = document.getElementById('upstreamRow');
  const styleUrlRow = document.getElementById('styleUrlRow');
  const wmsLayersRow = document.getElementById('wmsLayersRow');
  if (upstreamRow) upstreamRow.style.display = (mt === 'vector') ? 'none' : '';
  if (styleUrlRow) styleUrlRow.style.display = (mt === 'vector') ? '' : 'none';
  if (wmsLayersRow) wmsLayersRow.style.display = (mt === 'wms') ? '' : 'none';
}

// Tile preset loading from API
async function loadTilePresets() {
  try {
    const res = await fetch('/api/v1/tile-sources');
    if (!res.ok) return;
    const presets = await res.json();
    const sel = document.getElementById('tilePreset');
    if (!sel) return;
    // remove old dynamic options (preserve the blank '— Benutzerdefiniert —' entry)
    Array.from(sel.options).forEach(o => { if (o.value !== '') o.remove(); });
    presets.forEach(p => {
      const opt = document.createElement('option');
      opt.value = p.id;
      opt.textContent = p.label;
      opt.dataset.preset = JSON.stringify(p);
      sel.appendChild(opt);
    });
    // set current selection based on active upstream/style_url
    const curSettings = preloadedSettings || {};
    const curTiles = curSettings.tiles || {};
    const match = presets.find(p =>
      (p.upstream && p.upstream === curTiles.upstream) ||
      (p.style_url && p.style_url === curTiles.style_url)
    );
    if (match) sel.value = match.id;
  } catch (e) {
    console.warn('Failed to load tile presets:', e);
  }
}
loadTilePresets();

// Handle map type selector change
const mapTypeEl = document.getElementById('mapType');
if (mapTypeEl) {
  mapTypeEl.addEventListener('change', () => updateMapTypeVisibility(mapTypeEl.value));
}

// Handle tile preset selector change — auto-fill fields
const tilePresetEl = document.getElementById('tilePreset');
if (tilePresetEl) {
  tilePresetEl.addEventListener('change', function() {
    const opt = this.options[this.selectedIndex];
    if (!opt || !opt.dataset.preset) return;
    try {
      const p = JSON.parse(opt.dataset.preset);
      const mtEl = document.getElementById('mapType');
      if (mtEl) mtEl.value = p.map_type || 'raster';
      const upEl = document.getElementById('tileUpstream');
      if (upEl) upEl.value = p.upstream || '';
      const suEl = document.getElementById('tileStyleUrl');
      if (suEl) suEl.value = p.style_url || '';
      const wlEl = document.getElementById('wmsLayers');
      if (wlEl) wlEl.value = p.wms_layers || '';
      const atEl = document.getElementById('tileAttribution');
      if (atEl) atEl.value = p.attribution || '';
      updateMapTypeVisibility(p.map_type || 'raster');
    } catch (e) {
      console.warn('Failed to parse preset data:', e);
    }});
}

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
    cur.tiles = cur.tiles || {};
    const mtEl = document.getElementById('mapType');
    if (mtEl) cur.tiles.map_type = mtEl.value;
    const upEl = document.getElementById('tileUpstream');
    if (upEl) cur.tiles.upstream = upEl.value;
    const suEl = document.getElementById('tileStyleUrl');
    if (suEl) cur.tiles.style_url = suEl.value;
    const wlEl = document.getElementById('wmsLayers');
    if (wlEl) cur.tiles.wms_layers = wlEl.value;
    const atEl = document.getElementById('tileAttribution');
    if (atEl) cur.tiles.attribution = atEl.value;
    await apiPutSettings(cur);
    apiCache.delete('settings'); // Invalidate cache
    // Refresh map tile layer with updated settings
    applyTileLayer(cur);
    btn.innerHTML = '✅ Gespeichert';
    showToast('Einstellungen erfolgreich gespeichert', 'success', 2000);
    setTimeout(() => { btn.innerHTML = origText; btn.disabled = false; }, 1500);
  } catch (e) {
    btn.innerHTML = '❌ Fehler';
    showToast('Fehler beim Speichern der Einstellungen', 'error', 3000);
    setTimeout(() => { btn.innerHTML = origText; btn.disabled = false; }, 2000);
  }
};

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
if (aiToggle) {
  function setAIOpen(open){
    aiBody.style.display = open ? 'block' : 'none';
    aiCard.classList.toggle('collapsed', !open);
    aiToggle.setAttribute('aria-expanded', open ? 'true' : 'false');
    aiToggle.textContent = open ? '‹' : '›';
    localStorage.setItem('aiOpen', open ? '1' : '0');
    if (open && !aiCard.dataset.checked) {
      aiCard.dataset.checked = '1';
      checkAIStatus();
    }
  }
  aiToggle.addEventListener('click', ()=>{ setAIOpen(aiBody.style.display==='none'); });
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
          if (fromEl && data.from && (data.from.query || data.from.label)) fromEl.value = data.from.query || data.from.label;
          if (toEl && data.to && (data.to.query || data.to.label)) toEl.value = data.to.query || data.to.label;
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
          const toAdd = data.suggestions.map(s => s.label || s.Label || (s.tags && s.tags.name) || '').filter(Boolean);
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
          const fe = document.getElementById('from'); if (fe && data.from.query) fe.value = data.from.query;
        }
        if (data && data.to) {
          const te = document.getElementById('to'); if (te && data.to.query) te.value = data.to.query;
        }
        if (data && data.waypoints && Array.isArray(data.waypoints)) {
          const qws = data.waypoints.map(w => w && w.query).filter(Boolean);
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

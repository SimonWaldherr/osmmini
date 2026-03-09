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
let searchResultMarkers = [];

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
    
    // Use a workaround: reset value every few ms to prevent Safari autofill
    let lastValue = '';
    input.addEventListener('input', (e) => {
      lastValue = e.target.value;
    });
    
    // Clear any suspicious autofill attempts
    setInterval(() => {
      if (input === document.activeElement) {
        const currentValue = input.value;
        // If value changed without user input event, Safari likely autofilled
        if (currentValue !== lastValue && currentValue.includes(' ')) {
          // Check if it looks like Safari's autofill (typically has spaces/numbers)
          const isLikelyAutofill = /\d+|straße|str\.|platz|weg/i.test(currentValue) && 
                                    lastValue.length < 3;
          if (isLikelyAutofill) {
            input.value = lastValue;
          }
        }
      }
    }, 100);
    
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
  if(!res.ok){ const err = await res.json().catch(()=>({})); throw new Error(err.error || res.statusText); }
  return res.json();
}

async function apiTripSolve(from,to,options){
  const optimize = document.getElementById('optimize').checked;
  const allStops = [];
  waypoints.forEach(wp=>{ const v=wp.input.value.trim(); if(v) allStops.push({id:wp.id, location:{query:v}}); });
  stops.forEach(s=> allStops.push({id:s.id, location:{lat:s.lat, lon:s.lon}}));
  const plan = { start:{query:from}, end:{query:to}, stops: allStops, dependencies:[], optimize };
  const res = await fetch('/api/v1/trip/solve', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({plan, options})});
  if(!res.ok){ const err = await res.json().catch(()=>({})); throw new Error(err.error || res.statusText); }
  return res.json();
}

function renderPath(path, meta){
  const coords = path.map(p=>[p.lat,p.lon]);
  if(polyline) polyline.remove();
  if(startMarker) startMarker.remove(); if(endMarker) endMarker.remove();
  if(coords.length===0) return;
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
  steps.forEach((s, idx) => {
    const row = document.createElement('div');
    row.className = 'maneuver-row';
    row.style.display = 'flex';
    row.style.alignItems = 'center';
    row.style.justifyContent = 'space-between';
    row.style.padding = '6px 8px';
    row.style.borderRadius = '6px';
    row.style.marginBottom = '6px';
    row.style.background = 'rgba(255,255,255,0.02)';

    const left = document.createElement('div');
    left.style.display = 'flex';
    left.style.alignItems = 'center';
    left.style.gap = '8px';

    const ic = document.createElement('div'); ic.textContent = iconForType(s.type || s.Type || ''); ic.style.fontSize = '18px';
    const txt = document.createElement('div'); txt.style.fontSize = '13px'; txt.style.lineHeight = '1.1';
    txt.innerHTML = `<div style="font-weight:600">${escapeHtml(s.instruction || s.Instruction || '')}</div><div style="font-size:11px; opacity:0.7;">${escapeHtml((s.type||s.Type||'').toString())}</div>`;
    left.appendChild(ic); left.appendChild(txt);

    const right = document.createElement('div');
    right.style.textAlign = 'right';
    right.style.minWidth = '70px';
    right.innerHTML = `<div style="font-weight:600">${formatMeters(s.distance_m || s.DistanceM || 0)}</div><div style="font-size:11px; opacity:0.7">${formatDuration(s.duration_s || s.DurationS || 0)}</div>`;

    row.appendChild(left);
    row.appendChild(right);

    // pan to maneuver on click
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
  searchResultMarkers.forEach(m => m.remove());
  searchResultMarkers = [];
}

function showSearchResultsOnMap(results) {
  clearSearchResults();
  if (!Array.isArray(results) || results.length === 0) return;
  const bounds = [];
  results.forEach((item, idx) => {
    if (!item || !item.lat || !item.lon) return;
    const m = L.marker([item.lat, item.lon], { title: item.label || '' }).addTo(map);
    const tags = item.tags || {};
    let popupHtml = `<strong>${escapeHtml(item.label || '')}</strong><br/>`;
    if (tags['addr:street'] || tags['addr:housenumber']) popupHtml += `${escapeHtml(tags['addr:street']||'')} ${escapeHtml(tags['addr:housenumber']||'')}<br/>`;
    popupHtml += `<small style="opacity:0.8">${escapeHtml((tags['addr:city']||''))}</small>`;
    m.bindPopup(popupHtml);
    m.on('click', () => { m.openPopup(); });
    searchResultMarkers.push(m);
    bounds.push([item.lat, item.lon]);
  });
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
        statusEl.innerHTML = `<span style="color:#6ef2a0;">✅ KI verfügbar</span> (${providers}, ${aiModels.length} Modell${aiModels.length>1?'e':''})`;
      } else {
        statusEl.innerHTML = '<span style="color:#ffcc66;">⚠️ Provider verfügbar, aber keine Modelle geladen</span>';
      }
    } else {
      statusEl.innerHTML = '<span style="color:var(--text-muted);">❌ Keine KI verfügbar. Starte <a href="https://ollama.com" target="_blank" style="color:var(--primary);">Ollama</a> oder <a href="https://lmstudio.ai" target="_blank" style="color:var(--primary);">LM Studio</a>.</span>';
    }
  } catch (e) {
    statusEl.innerHTML = '<span style="color:var(--text-muted);">❌ KI-Status konnte nicht abgefragt werden</span>';
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
    // include map center as location hint if available
    const payload = { prompt, model };
    try {
      if (window.map && typeof map.getCenter === 'function') {
        const c = map.getCenter();
        if (c && typeof c.lat === 'number' && typeof c.lng === 'number') {
          payload.lat = c.lat;
          payload.lon = c.lng;
        }
      }
    } catch (e) {}
    // Follow-up handling: if user asks duration and we have a recent route, answer locally
    const lowerPrompt = (prompt || '').toLowerCase();
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
    // remember for follow-ups
    try { lastAIResponse = data; } catch(e){}
    loadingMsg.innerHTML = `<div style="font-size:11px;color:var(--text-muted);margin-bottom:4px;">${escapeHtml(data.provider)}/${escapeHtml(data.model)}</div>${escapeHtml(data.response)}`;
    // If the AI returned a computed route, render it on the map and fill inputs
    try {
      if (data && data.route && data.route.path && data.route.path.length) {
        renderPath(data.route.path, data.route);
        // Fill from/to inputs so user can tweak/compute further
        try {
          const fromEl = document.getElementById('from');
          const toEl = document.getElementById('to');
          if (fromEl && data.from && data.from.query) fromEl.value = data.from.query;
          if (toEl && data.to && data.to.query) toEl.value = data.to.query;
        } catch (e) {}
        setMapsLinks(data.route.google_maps_url || data.route.googleMapsURL || '', data.route.apple_maps_url || data.route.appleMapsURL || '');
        showToast('KI: Route berechnet und angezeigt', 'success', 1800);
      }
      // If the AI returned suggestions (multiple POI matches), show them on the map
      if (data && data.suggestions && Array.isArray(data.suggestions) && data.suggestions.length) {
        // the map marker display expects objects with lat/lon/label
        showSearchResultsOnMap(data.suggestions);
        // if user asked for stops (via / mit / stop), auto-add suggestions as waypoints
        const p = prompt.toLowerCase();
        if (p.includes('mit') || p.includes('via') || p.includes('stopp') || p.includes('stopps') || p.includes('zwischen')) {
          data.suggestions.forEach(s => {
            const val = s.label || s.Label || (s.tags && s.tags.name) || '';
            if (val) addWaypointWithValue(val);
          });
          showToast('KI: Vorschläge als Zwischenstopps hinzugefügt', 'success', 1800);
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
          data.waypoints.forEach(w => { if (w && w.query) addWaypointWithValue(w.query); });
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

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
L.tileLayer('/tiles/{z}/{x}/{y}.png', { 
  maxZoom: 19,
  attribution: '',
  updateWhenIdle: true,
  updateWhenZooming: false,
  keepBuffer: 2
}).addTo(map);

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
    pro: document.getElementById('pro').checked,
    weights: {
      left_turn: parseFloat(document.getElementById('w_left').value) || 0,
      right_turn: parseFloat(document.getElementById('w_right').value) || 0
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
      document.getElementById('status').textContent = '✅ Route gefunden';
      renderStopList([]);
      setMapsLinks(data.google_maps_url, data.apple_maps_url);
      showToast(`Route berechnet: ${(data.distance_m/1000).toFixed(1)} km`, 'success', 2000);
    } else {
      const data = await apiTripSolve(from, to, options);
      renderPath(data.path, data);
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
        document.getElementById('pro').checked = !!s.routing.pro;
        if(s.routing.pro) weights.classList.remove('hidden');
        
        const w = s.routing.weights || {};
        document.getElementById('w_left').value = w.left_turn || 0;
        document.getElementById('w_right').value = w.right_turn || 0;
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
    await apiPutSettings(cur);
    apiCache.delete('settings'); // Invalidate cache
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

// Visual feedback on button clicks
document.querySelectorAll('.btn').forEach(btn => {
  btn.addEventListener('click', function() {
    this.style.transform = 'scale(0.95)';
    setTimeout(() => this.style.transform = '', 100);
  });
});

const map = L.map('map').setView([48.7, 12.7], 10);
L.tileLayer('/tiles/{z}/{x}/{y}.png', { maxZoom: 19 }).addTo(map);

let line, startM, endM;
const stops = []; // {id, marker, lat, lon}
let stopSeq = 1;

const pinIcon = (n) => L.divIcon({
  className: '',
  html: '<div class="pin">📌<span class="num">'+n+'</span></div>',
  iconSize: [20,20],
  iconAnchor: [10,20]
});

function syncStopIcons(orderIds) {
  const idToRank = new Map();
  orderIds.forEach((id, i) => idToRank.set(id, i+1));
  stops.forEach((s, idx) => {
    const n = idToRank.get(s.id) || (idx+1);
    s.marker.setIcon(pinIcon(n));
  });
}

function renderStopList(orderIds) {
  const el = document.getElementById('stopList');
  el.innerHTML = '';
  const order = (orderIds && orderIds.length) ? orderIds : stops.map(s => s.id);
  order.forEach(id => {
    const s = stops.find(x => x.id === id);
    if (!s) return;
    const pill = document.createElement('div');
    pill.className = 'stop';
    pill.textContent = id;
    pill.title = 'Click to pan';
    pill.onclick = () => map.panTo([s.lat, s.lon]);
    el.appendChild(pill);
  });
}

function setMapsLinks(g, a) {
  const gEl = document.getElementById('gmaps');
  const aEl = document.getElementById('amaps');
  if (g) { gEl.href = g; gEl.style.display = 'inline-block'; } else { gEl.style.display = 'none'; }
  if (a) { aEl.href = a; aEl.style.display = 'inline-block'; } else { aEl.style.display = 'none'; }
}

function routeOptionsFromUI() {
  const engine = document.getElementById('engine').value;
  const objective = document.getElementById('objective').value;
  const pro = document.getElementById('pro').checked;
  const w = {
    left_turn:  parseFloat(document.getElementById('w_left').value || '0') || 0,
    right_turn: parseFloat(document.getElementById('w_right').value || '0') || 0,
    u_turn:     parseFloat(document.getElementById('w_uturn').value || '0') || 0,
    crossing:   parseFloat(document.getElementById('w_cross').value || '0') || 0,
    max_speed_kph: parseFloat(document.getElementById('w_max').value || '0') || 0,
    vehicle_height_m: parseFloat(document.getElementById('w_h').value || '0') || 0,
    vehicle_weight_t: parseFloat(document.getElementById('w_w').value || '0') || 0,
  };
  return { engine, objective, pro, weights: w };
}

async function apiGetSettings() {
  const res = await fetch('/api/v1/settings');
  if (!res.ok) throw new Error('settings fetch failed');
  return res.json();
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

async function apiRoute(from, to, options) {
  const res = await fetch('/api/v1/route', {
    method: 'POST',
    headers: {'Content-Type':'application/json'},
    body: JSON.stringify({from:{query:from}, to:{query:to}, options})
  });
  if (!res.ok) {
    const err = await res.json().catch(()=>({}));
    throw new Error(err.error || res.statusText);
  }
  return res.json();
}

async function apiTripSolve(from, to, options) {
  const optimize = document.getElementById('optimize').checked;
  const plan = {
    start: {query: from},
    end: {query: to},
    stops: stops.map(s => ({id: s.id, location: {lat: s.lat, lon: s.lon}})),
    dependencies: [],
    optimize
  };
  const res = await fetch('/api/v1/trip/solve', {
    method: 'POST',
    headers: {'Content-Type':'application/json'},
    body: JSON.stringify({plan, options})
  });
  if (!res.ok) {
    const err = await res.json().catch(()=>({}));
    throw new Error(err.error || res.statusText);
  }
  return res.json();
}

function renderPath(path, meta) {
  const coords = path.map(p => [p.lat, p.lon]);
  if (line) line.remove();
  if (startM) startM.remove();
  if (endM) endM.remove();

  line = L.polyline(coords, {color:'#4da3ff', weight:5}).addTo(map);
  startM = L.circleMarker(coords[0], {radius: 6, color:'#7cf29c', fillColor:'#7cf29c', fillOpacity:0.9}).addTo(map);
  endM = L.circleMarker(coords[coords.length-1], {radius: 6, color:'#ffb347', fillColor:'#ffb347', fillOpacity:0.9}).addTo(map);
  map.fitBounds(line.getBounds(), {padding:[30,30]});

  const km = (meta.distance_m/1000).toFixed(1);
  const min = (meta.duration_s/60).toFixed(0);
  document.getElementById('distance').textContent =
    km + ' km | ~' + min + ' min | cost=' + meta.cost.toFixed(1) + ' ('+meta.engine+'/'+meta.objective+')';
}

async function compute() {
  const from = document.getElementById('from').value.trim();
  const to = document.getElementById('to').value.trim();
  const options = routeOptionsFromUI();
  document.getElementById('status').textContent = 'Berechne...';
  setMapsLinks('', '');

  try {
    if (stops.length === 0) {
      const data = await apiRoute(from, to, options);
      renderPath(data.path, data);
      document.getElementById('status').textContent = 'OK: ' + data.from.label + ' → ' + data.to.label;
      renderStopList([]);
      setMapsLinks(data.google_maps_url, data.apple_maps_url);
    } else {
      const data = await apiTripSolve(from, to, options);
      renderPath(data.path, data);
      document.getElementById('status').textContent = 'OK Trip (' + (document.getElementById('optimize').checked ? 'optimized' : 'manual') + ')';
      syncStopIcons(data.order || []);
      renderStopList(data.order || []);
      setMapsLinks(data.google_maps_url, data.apple_maps_url);
    }
  } catch (e) {
    document.getElementById('status').textContent = 'Fehler: ' + e.message;
  }
}

document.getElementById('go').onclick = (e) => { e.preventDefault(); compute(); };

document.getElementById('clear').onclick = () => {
  while (stops.length) {
    const s = stops.pop();
    s.marker.remove();
  }
  stopSeq = 1;
  compute();
};

// map click -> add stop
map.on('click', (ev) => {
  const id = 'S' + (stopSeq++);
  const m = L.marker(ev.latlng, {draggable:true, icon: pinIcon(stops.length+1)}).addTo(map);
  const s = {id, marker: m, lat: ev.latlng.lat, lon: ev.latlng.lng};
  stops.push(s);

  m.on('dragend', () => {
    const ll = m.getLatLng();
    s.lat = ll.lat; s.lon = ll.lng;
    compute();
  });
  m.on('contextmenu', () => {
    m.remove();
    const idx = stops.findIndex(x => x.id === id);
    if (idx >= 0) stops.splice(idx, 1);
    compute();
  });

  compute();
});

// autocomplete
function makeSuggest(containerId, inputId) {
  const container = document.getElementById(containerId);
  const input = document.getElementById(inputId);
  let timeout = null;
  let seq = 0;
  let ctrl = null;

  function hide() { container.style.display = 'none'; }

  input.addEventListener('input', () => {
    const q = input.value.trim();
    if (timeout) clearTimeout(timeout);
    if (ctrl) ctrl.abort();
    if (q.length < 2) { hide(); return; }

    timeout = setTimeout(async () => {
      const mySeq = ++seq;
      ctrl = new AbortController();
      try {
        const res = await fetch('/api/v1/search?limit=10&q=' + encodeURIComponent(q), { signal: ctrl.signal });
        if (!res.ok) return;
        const data = await res.json();
        if (mySeq !== seq) return;

        container.innerHTML = '';
        if (!Array.isArray(data) || data.length === 0) { hide(); return; }

        data.slice(0,10).forEach(item => {
          const el = document.createElement('div');
          el.className = 'item';
          el.textContent = item.label || '';
          el.onclick = () => { input.value = item.label || ''; hide(); compute(); };
          container.appendChild(el);
        });
        container.style.display = 'block';
      } catch (err) {
        if (err && err.name === 'AbortError') return;
        hide();
      }
    }, 220);
  });

  document.addEventListener('click', (ev) => {
    if (!container.contains(ev.target) && ev.target !== input) hide();
  });
}
makeSuggest('from-suggest','from');
makeSuggest('to-suggest','to');

// pro panel toggle
const pro = document.getElementById('pro');
const weights = document.getElementById('weights');
pro.addEventListener('change', () => {
  weights.style.display = pro.checked ? 'flex' : 'none';
  compute();
});
document.getElementById('engine').addEventListener('change', compute);
document.getElementById('objective').addEventListener('change', compute);
document.getElementById('optimize').addEventListener('change', compute);

// load settings into UI
(async () => {
  try {
    const s = await apiGetSettings();
    document.getElementById('engine').value = (s.routing.engine || 'astar');
    document.getElementById('objective').value = s.routing.objective || 'distance';
    document.getElementById('pro').checked = !!s.routing.pro;
    weights.style.display = pro.checked ? 'flex' : 'none';

    const w = (s.routing && s.routing.weights) || {};
    document.getElementById('w_left').value = w.left_turn || 0;
    document.getElementById('w_right').value = w.right_turn || 0;
    document.getElementById('w_uturn').value = w.u_turn || 0;
    document.getElementById('w_cross').value = w.crossing || 0;
    document.getElementById('w_max').value = w.max_speed_kph || 130;
    document.getElementById('w_h').value = w.vehicle_height_m || 0;
    document.getElementById('w_w').value = w.vehicle_weight_t || 0;
  } catch (e) {
    // ignore
  }
  compute();
})();

// save settings
document.getElementById('save').onclick = async () => {
  try {
    const cur = await apiGetSettings();
    cur.routing = routeOptionsFromUI();
    const saved = await apiPutSettings(cur);
    document.getElementById('status').textContent = 'Settings saved';
    document.getElementById('engine').value = saved.routing.engine || 'astar';
    document.getElementById('objective').value = saved.routing.objective || 'distance';
    compute();
  } catch (e) {
    document.getElementById('status').textContent = 'Settings save failed: ' + e.message;
  }
};

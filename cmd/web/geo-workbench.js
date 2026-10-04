/* Browser-local vector preparation inspired by Mapshaper workflows. Original code. */
(function (root) {
  'use strict';
  const MAX_BYTES = 10 * 1024 * 1024, MAX_POINTS = 50000, MAX_FEATURES = 5000;
  const clone = value => JSON.parse(JSON.stringify(value));
  const empty = () => ({type: 'FeatureCollection', features: []});
  const own = (o, key) => o != null && Object.hasOwn(o, key);
  const scalar = value => value === null || ['string', 'number', 'boolean'].includes(typeof value);
  const text = value => value === undefined ? '' : typeof value === 'object' && value !== null ? JSON.stringify(value) : String(value ?? '');

  function validate(input) {
    if (!input || input.type !== 'FeatureCollection' || !Array.isArray(input.features)) throw Error('Eine GeoJSON FeatureCollection laden.');
    if (input.crs) throw Error('Nur WGS84-GeoJSON ohne CRS-Angabe wird unterstützt.');
    if (input.features.length > MAX_FEATURES) throw Error(`Maximal ${MAX_FEATURES} Objekte laden.`);
    let points = 0;
    function position(p) {
      if (!Array.isArray(p) || p.length < 2 || !p.every(Number.isFinite) || Math.abs(p[0]) > 180 || Math.abs(p[1]) > 90) throw Error('Ungültige WGS84-Koordinaten.');
      if (++points > MAX_POINTS) throw Error(`Maximal ${MAX_POINTS} Koordinaten laden.`);
    }
    function line(coords, ring = false) {
      if (!Array.isArray(coords) || coords.length < (ring ? 4 : 2)) throw Error('Zu wenige Linien- oder Ringpunkte.');
      coords.forEach(position);
      if (ring && (coords[0][0] !== coords.at(-1)[0] || coords[0][1] !== coords.at(-1)[1])) throw Error('Polygonringe müssen geschlossen sein.');
    }
    function polygon(coords) {
      if (!Array.isArray(coords) || !coords.length) throw Error('Ein Polygon benötigt einen Außenring.');
      coords.forEach(c => line(c, true));
    }
    function geometry(g, depth = 0) {
      if (g === null) return;
      if (!g || depth > 10) throw Error('Ungültige oder zu tief verschachtelte Geometrie.');
      const c = g.coordinates;
      switch (g.type) {
        case 'Point': position(c); break;
        case 'MultiPoint': if (!Array.isArray(c)) throw Error('Ungültige Punkte.'); c.forEach(position); break;
        case 'LineString': line(c); break;
        case 'MultiLineString': if (!Array.isArray(c)) throw Error('Ungültige Linien.'); c.forEach(x => line(x)); break;
        case 'Polygon': polygon(c); break;
        case 'MultiPolygon': if (!Array.isArray(c)) throw Error('Ungültige Polygone.'); c.forEach(polygon); break;
        case 'GeometryCollection': if (!Array.isArray(g.geometries)) throw Error('Ungültige Geometriesammlung.'); g.geometries.forEach(x => geometry(x, depth + 1)); break;
        default: throw Error(`Geometrietyp ${g.type || '(leer)'} wird nicht unterstützt.`);
      }
    }
    for (const f of input.features) {
      if (!f || f.type !== 'Feature' || !own(f, 'geometry') || (f.properties != null && (typeof f.properties !== 'object' || Array.isArray(f.properties)))) throw Error('Ungültiges GeoJSON-Objekt oder Attribute.');
      geometry(f.geometry);
    }
    return points;
  }
  function parse(source) {
    if (new TextEncoder().encode(source).length > MAX_BYTES) throw Error('Datei ist größer als 10 MiB.');
    const data = JSON.parse(source.replace(/^\uFEFF/, ''));
    validate(data); return data;
  }
  function fields(data) { return [...new Set(data.features.flatMap(f => Object.keys(f.properties || {})))].sort(); }
  function bounds(data) {
    const points = [];
    function coordinates(c) { if (!Array.isArray(c) || !c.length) return; if (typeof c[0] === 'number') points.push(c); else c.forEach(coordinates); }
    function geometry(g) { if (!g) return; if (g.type === 'GeometryCollection') g.geometries.forEach(geometry); else coordinates(g.coordinates); }
    data.features.forEach(f => geometry(f.geometry));
    if (!points.length) throw Error('Die Auswahl enthält keine Geometrie.');
    const xs = points.map(p => p[0]), ys = points.map(p => p[1]);
    let west = Math.min(...xs), east = Math.max(...xs);
    if (east - west > 180) { const shifted = xs.map(x => x < 0 ? x + 360 : x), w = Math.min(...shifted), e = Math.max(...shifted); if (e - w < east - west) { west = w; east = e; } }
    const south = Math.max(-85, Math.min(85, Math.min(...ys))), north = Math.max(-85, Math.min(85, Math.max(...ys)));
    return [[west - (west === east ? 0.001 : 0), Math.max(-85, south - (south === north ? 0.001 : 0))], [east + (west === east ? 0.001 : 0), Math.min(85, north + (south === north ? 0.001 : 0))]];
  }
  function matching(data, field, value) {
    return data.features.map((f, i) => ({f, i})).filter(({f}) => !field || (own(f.properties, field) && text(f.properties[field]) === value)).map(({i}) => i);
  }
  function subset(data, indices) {
    const result = clone(data); delete result.bbox;
    result.features = indices.map(i => {
      if (!Number.isInteger(i) || i < 0 || i >= data.features.length) throw Error('Ungültige Objektauswahl.');
      return clone(data.features[i]);
    });
    return result;
  }
  function selectFields(data, keep) {
    const result = clone(data);
    for (const f of result.features) f.properties = Object.fromEntries(keep.filter(key => own(f.properties, key)).map(key => [key, f.properties[key]]));
    return result;
  }
  function edit(data, index, field, value) {
    if (!Number.isInteger(index) || !data.features[index] || !fields(data).includes(field)) throw Error('Objekt und Attribut auswählen.');
    const previous = data.features[index].properties?.[field];
    if (previous !== undefined && !scalar(previous)) throw Error('Verschachtelte Attribute werden nur angezeigt.');
    let next = value;
    if (typeof previous === 'number') {
      if (!value.trim() || !Number.isFinite(Number(value))) throw Error('Eine endliche Zahl eingeben.');
      next = Number(value);
    } else if (typeof previous === 'boolean') {
      if (!['true', 'false'].includes(value)) throw Error('Für dieses Attribut true oder false eingeben.');
      next = value === 'true';
    } else if (previous === null && value === 'null') next = null;
    const result = clone(data), properties = result.features[index].properties || {};
    Object.defineProperty(properties, field, {value: next, enumerable: true, writable: true, configurable: true});
    result.features[index].properties = properties;
    return result;
  }

  function parseCSV(source) {
    if (new TextEncoder().encode(source).length > MAX_BYTES) throw Error('CSV ist größer als 10 MiB.');
    source = source.replace(/^\uFEFF/, '');
    const rows = []; let row = [], cell = '', quoted = false, closed = false, start = true;
    function finishCell() { row.push(cell); cell = ''; closed = false; start = true; }
    function finishRow() { finishCell(); if (row.some(x => x !== '')) rows.push(row); row = []; if (rows.length > 10001) throw Error('Maximal 10000 CSV-Datensätze.'); }
    for (let i = 0; i < source.length; i++) {
      const c = source[i];
      if (quoted) {
        if (c === '"') { if (source[i + 1] === '"') { cell += '"'; i++; } else { quoted = false; closed = true; } }
        else cell += c;
      } else if (c === ',') finishCell();
      else if (c === '\r' || c === '\n') { if (c === '\r' && source[i + 1] === '\n') i++; finishRow(); }
      else if (c === '"' && start) { quoted = true; start = false; }
      else {
        if (closed || c === '"') throw Error('Ungültige CSV-Anführungszeichen.');
        cell += c; start = false;
      }
    }
    if (quoted) throw Error('Nicht geschlossenes CSV-Textfeld.');
    if (cell || row.length || closed) finishRow();
    const header = rows.shift();
    if (!header?.length || header.some(h => !h.trim()) || new Set(header).size !== header.length || header.length > 100) throw Error('CSV benötigt eindeutige Spaltennamen (maximal 100).');
    if (rows.some(r => r.length !== header.length)) throw Error('CSV-Zeilen haben unterschiedliche Spaltenzahlen.');
    return {header, rows: rows.map(r => Object.fromEntries(header.map((h, i) => [h, r[i]])))};
  }
  function join(data, csv, field, csvKey) {
    if (!fields(data).includes(field) || !csv.header.includes(csvKey)) throw Error('Gültige Schlüsselspalten auswählen.');
    const lookup = new Map();
    for (const row of csv.rows) {
      const key = row[csvKey];
      if (!key.trim() || lookup.has(key)) throw Error('CSV-Schlüssel müssen befüllt und eindeutig sein.');
      lookup.set(key, row);
    }
    const columns = csv.header.filter(h => h !== csvKey);
    const result = clone(data); let matched = 0;
    for (const f of result.features) {
      if (!own(f.properties, field)) continue;
      const row = lookup.get(text(f.properties[field]));
      if (!row) continue;
      for (const h of columns) {
        const key = 'csv_' + h;
        if (own(f.properties, key)) throw Error(`Attribut ${key} existiert bereits; Verknüpfung abgebrochen.`);
        Object.defineProperty(f.properties, key, {value: row[h], enumerable: true, writable: true, configurable: true});
      }
      matched++;
    }
    return {data: result, matched, unmatched: result.features.length - matched};
  }
  function toCSV(data) {
    const keys = fields(data);
    // Spreadsheet formula neutralization applies only to CSV, never GeoJSON.
    const cell = value => {
      let s = text(value);
      if (/^[\s]*[=+@-]/.test(s) || /^[\t\r\n]/.test(s)) s = "'" + s;
      return '"' + s.replace(/"/g, '""') + '"';
    };
    return [keys.map(cell).join(','), ...data.features.map(f => keys.map(k => cell(f.properties?.[k])).join(','))].join('\r\n') + '\r\n';
  }

  function simplify(data, tolerance) {
    if (!Number.isFinite(tolerance) || tolerance < 0 || tolerance > 1000) throw Error('Toleranz zwischen 0 und 1000 Metern wählen.');
    validate(data);
    const result = clone(data); let operations = 0, removed = 0, lines = 0;
    function reduce(points) {
      lines++;
      if (points.length < 3 || tolerance === 0) return points;
      let previous = points[0][0];
      const unwrapped = points.map(p => {
        let lon = p[0]; while (lon - previous > 180) lon -= 360; while (lon - previous < -180) lon += 360;
        previous = lon; return [lon, p[1]];
      });
      const xs = unwrapped.map(p => p[0]), ys = unwrapped.map(p => p[1]);
      if (Math.max(...xs) - Math.min(...xs) > 5 || Math.max(...ys) - Math.min(...ys) > 5 || ys.some(y => Math.abs(y) > 85)) throw Error('Linienvereinfachung ist auf regionale Daten (5° Ausdehnung, ±85° Breite) begrenzt.');
      const latitude = (Math.max(...ys) + Math.min(...ys)) / 2 * Math.PI / 180;
      const projected = unwrapped.map(p => [(p[0] - unwrapped[0][0]) * Math.cos(latitude) * 111195.08, (p[1] - unwrapped[0][1]) * 111195.08]);
      const keep = new Set([0, points.length - 1]), stack = [[0, points.length - 1]];
      while (stack.length) {
        const [first, last] = stack.pop(), a = projected[first], b = projected[last];
        const dx = b[0] - a[0], dy = b[1] - a[1], length = dx * dx + dy * dy;
        let best = tolerance * tolerance, at = -1;
        for (let i = first + 1; i < last; i++) {
          if (++operations > 2000000) throw Error('Linie ist für die interaktive Vereinfachung zu komplex.');
          const p = projected[i], t = length ? Math.max(0, Math.min(1, ((p[0]-a[0])*dx + (p[1]-a[1])*dy) / length)) : 0;
          const distance = (p[0] - a[0] - t*dx)**2 + (p[1] - a[1] - t*dy)**2;
          if (distance > best) { best = distance; at = i; }
        }
        if (at >= 0) { keep.add(at); stack.push([first, at], [at, last]); }
      }
      const simplified = points.filter((_, i) => keep.has(i));
      // A closed line must not collapse to a doubled endpoint or two edges.
      if (points[0][0] === points.at(-1)[0] && points[0][1] === points.at(-1)[1] && simplified.length < 4) return points;
      removed += points.length - simplified.length;
      return simplified;
    }
    function visit(g) {
      if (!g) return;
      if (g.type === 'LineString') g.coordinates = reduce(g.coordinates);
      if (g.type === 'MultiLineString') g.coordinates = g.coordinates.map(reduce);
      if (g.type === 'GeometryCollection') g.geometries.forEach(visit);
      if (removed) delete g.bbox;
    }
    for (const f of result.features) { visit(f.geometry); if (removed) delete f.bbox; }
    if (removed) delete result.bbox;
    return {data: result, removed, lines};
  }

  function create(map, options = {}) {
    const el = id => document.getElementById('wb' + id);
    let data = empty(), history = [], csv = null, page = 0, busy = false;
    const PAGE_SIZE = 25;
    const status = message => { el('Status').textContent = message; };
    const current = () => matching(data, el('FilterField').value, el('FilterValue').value);
    const guard = fn => (...args) => { try { return fn(...args); } catch (e) { status(e.message); } };
    function remember(next) {
      validate(next);
      if (new TextEncoder().encode(JSON.stringify(next)).length > MAX_BYTES) throw Error('Arbeitsstand überschreitet 10 MiB.');
      history.push(data); if (history.length > 5) history.shift();
      data = next; page = 0; render();
    }
    function picker(element, keys, blank = false) {
      const value = element.value;
      element.replaceChildren();
      for (const key of [...(blank ? [''] : []), ...keys]) {
        const option = document.createElement('option'); option.value = key; option.textContent = key || 'Alle Objekte'; element.append(option);
      }
      if (keys.includes(value)) element.value = value;
    }
    function renderMap() {
      if (!map || !map.isStyleLoaded()) return;
      if (!map.getSource('workbench')) {
        map.addSource('workbench', {type: 'geojson', data: empty()});
        map.addLayer({id: 'workbench-fill', type: 'fill', source: 'workbench', filter: ['==', '$type', 'Polygon'], paint: {'fill-color': '#0891b2', 'fill-opacity': 0.22}});
        map.addLayer({id: 'workbench-line', type: 'line', source: 'workbench', filter: ['!=', '$type', 'Point'], paint: {'line-color': '#0891b2', 'line-width': 3}});
        map.addLayer({id: 'workbench-point', type: 'circle', source: 'workbench', filter: ['==', '$type', 'Point'], paint: {'circle-color': '#0891b2', 'circle-radius': 6, 'circle-stroke-width': 1, 'circle-stroke-color': '#fff'}});
      }
      map.getSource('workbench').setData(el('Show').checked ? subset(data, current()) : empty());
    }
    function render() {
      const keys = fields(data);
      picker(el('FilterField'), keys, true); picker(el('EditField'), keys); picker(el('JoinField'), keys);
      const indices = current(), pages = Math.max(1, Math.ceil(indices.length / PAGE_SIZE));
      page = Math.min(page, pages - 1);
      el('Stats').textContent = `${data.features.length} Objekte · ${validate(data)} Koordinaten · ${keys.length} Attribute · ${indices.length} ausgewählt`;
      el('Page').textContent = `Seite ${page + 1} / ${pages}`;
      el('Previous').disabled = busy || page === 0; el('Next').disabled = busy || page + 1 >= pages;
      el('Undo').disabled = busy || !history.length;
      for (const id of ['Keep', 'Simplify', 'Export', 'CSVExport', 'Edit', 'Zoom']) el(id).disabled = busy || !data.features.length;
      el('Join').disabled = busy || !csv || !data.features.length;
      for (const id of ['File', 'LoadLayer', 'CSVFile', 'Refresh']) el(id).disabled = busy;
      const head = document.createElement('tr');
      for (const title of ['Objekt', 'Geometrie', ...keys.slice(0, 8)]) { const th = document.createElement('th'); th.scope = 'col'; th.textContent = title; head.append(th); }
      el('Head').replaceChildren(head); el('Body').replaceChildren();
      for (const i of indices.slice(page * PAGE_SIZE, (page + 1) * PAGE_SIZE)) {
        const f = data.features[i], row = document.createElement('tr'), td = document.createElement('td'), button = document.createElement('button');
        button.type = 'button'; button.className = 'btn btn-ghost'; button.textContent = String(i + 1); button.setAttribute('aria-label', `Objekt ${i + 1} bearbeiten`);
        button.addEventListener('click', () => { el('EditIndex').value = i + 1; fillEdit(); }); td.append(button); row.append(td);
        for (const value of [f.geometry?.type || 'Ohne Geometrie', ...keys.slice(0, 8).map(k => text(f.properties?.[k]))]) { const cell = document.createElement('td'); cell.textContent = value; row.append(cell); }
        el('Body').append(row);
      }
      el('Columns').textContent = keys.length > 8 ? 'Tabelle zeigt die ersten 8 Attribute; alle sind über die Attributauswahl erreichbar.' : '';
      const prior = new Set([...el('Fields').selectedOptions].map(o => o.value));
      el('Fields').replaceChildren();
      for (const key of keys) { const option = document.createElement('option'); option.value = key; option.textContent = key; option.selected = prior.has(key); el('Fields').append(option); }
      fillEdit(); renderMap();
    }
    function fillEdit() {
      const value = data.features[Number(el('EditIndex').value) - 1]?.properties?.[el('EditField').value];
      el('EditValue').value = text(value); el('EditValue').disabled = busy || (value !== undefined && !scalar(value));
    }
    async function action(fn) {
      if (busy) return;
      busy = true; render();
      try { await fn(); } catch (e) { status(e.message); }
      finally { busy = false; render(); }
    }
    async function readFile(file) {
      if (!file) throw Error('Datei auswählen.');
      if (file.size > MAX_BYTES) throw Error('Datei ist größer als 10 MiB.');
      return file.text();
    }
    el('File').addEventListener('change', () => action(async () => { const next = parse(await readFile(el('File').files[0])); remember(next); status('GeoJSON lokal geladen. Änderungen bleiben in dieser Sitzung; zum Speichern exportieren.'); }));
    el('CSVFile').addEventListener('change', () => action(async () => { const next = parseCSV(await readFile(el('CSVFile').files[0])); csv = next; picker(el('CSVKey'), csv.header); status(`${csv.rows.length} CSV-Datensätze geladen. Schlüssel auswählen und verknüpfen.`); }));
    async function refresh() {
      const res = await fetch('/api/v1/geodata/layers'); if (!res.ok) throw Error('Server-Layer konnten nicht geladen werden.');
      const layers = (await res.json()).layers || [];
      picker(el('Layer'), layers.map(x => x.id));
      status(layers.length ? 'Server-Layer auswählen und als Arbeitskopie laden.' : 'Keine importierten Server-Layer; eine lokale GeoJSON-Datei wählen.');
    }
    el('Refresh').addEventListener('click', () => action(refresh));
    el('LoadLayer').addEventListener('click', () => action(async () => {
      const name = el('Layer').value; if (!name) throw Error('Server-Layer auswählen.');
      const res = await fetch(`/api/v1/geodata/layers/${encodeURIComponent(name)}`);
      if (!res.ok) throw Error('Server-Layer konnte nicht geladen werden.');
      // Stream with a byte limit, including responses without Content-Length.
      if (!res.body) throw Error('Serverantwort kann nicht gelesen werden.');
      const reader = res.body.getReader(), chunks = []; let bytes = 0;
      try {
        while (true) { const part = await reader.read(); if (part.done) break; bytes += part.value.length; if (bytes > MAX_BYTES) { await reader.cancel(); throw Error('Server-Layer ist größer als 10 MiB.'); } chunks.push(part.value); }
      } finally { reader.releaseLock(); }
      const buffer = new Uint8Array(bytes); let offset = 0; for (const chunk of chunks) { buffer.set(chunk, offset); offset += chunk.length; }
      remember(parse(new TextDecoder().decode(buffer))); status(`${name} als lokale Arbeitskopie geladen.`);
    }));
    for (const id of ['FilterField', 'FilterValue']) el(id).addEventListener('input', guard(() => { page = 0; render(); }));
    el('Show').addEventListener('change', guard(renderMap));
    el('Zoom').addEventListener('click', guard(() => { if (busy || !map) return; map.fitBounds(bounds(subset(data, current())), {padding: options.fitPadding?.() || 50, maxZoom: 16}); }));
    el('Previous').addEventListener('click', guard(() => { page--; render(); }));
    el('Next').addEventListener('click', guard(() => { page++; render(); }));
    for (const id of ['EditIndex', 'EditField']) el(id).addEventListener('change', fillEdit);
    el('Edit').addEventListener('click', guard(() => { if (busy) return; remember(edit(data, Number(el('EditIndex').value) - 1, el('EditField').value, el('EditValue').value)); status('Attribut geändert.'); }));
    el('Keep').addEventListener('click', guard(() => { if (busy) return; remember(subset(data, current())); status('Auswahl übernommen. Rückgängig stellt den vorherigen Stand wieder her.'); }));
    el('Simplify').addEventListener('click', guard(() => { if (busy) return; const result = simplify(data, Number(el('Tolerance').value)); remember(result.data); status(`${result.lines} Linien bearbeitet, ${result.removed} Punkte entfernt. Flächen und Punkte unverändert.`); }));
    el('Join').addEventListener('click', guard(() => { if (busy) return; const result = join(data, csv, el('JoinField').value, el('CSVKey').value); remember(result.data); status(`${result.matched} Objekte verknüpft; ${result.unmatched} ohne CSV-Treffer. Neue Attribute beginnen mit csv_.`); }));
    el('Undo').addEventListener('click', guard(() => { if (busy || !history.length) return; data = history.pop(); page = 0; render(); status('Vorheriger Arbeitsstand wiederhergestellt.'); }));
    function exportData(format) {
      if (busy) return;
      let result = subset(data, current());
      const keep = [...el('Fields').selectedOptions].map(o => o.value); if (keep.length) result = selectFields(result, keep);
      options.download(format === 'csv' ? toCSV(result) : JSON.stringify(result, null, 2), 'osmmini-workbench.' + format, format === 'csv' ? 'text/csv;charset=utf-8' : 'application/geo+json');
      status(`${result.features.length} ausgewählte Objekte exportiert.`);
    }
    el('Export').addEventListener('click', guard(() => exportData('geojson')));
    el('CSVExport').addEventListener('click', guard(() => exportData('csv')));
    render();
    return {render: renderMap, load: next => remember(clone(next)), snapshot: () => clone(data)};
  }
  root.GeoWorkbench = {parse, validate, fields, bounds, matching, subset, selectFields, edit, parseCSV, join, toCSV, simplify, create};
})(typeof window !== 'undefined' ? window : globalThis);

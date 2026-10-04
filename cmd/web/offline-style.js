// Local presentation preferences: no tile rebuild or network service required.
const OfflineMapStyle = (() => {
  const key = 'osmmini.offline-style.v1';
  const standard = { background: '#f4f2eb', water: '#aad3df', forest: '#c6d9ba', farmland: '#eee7ce', meadow: '#dce5c7', buildings: '#d8cec3', motorway: '#f1c582', primary: '#f8e4ac', residential: '#ffffff', text: '#243346', halo: '#ffffff', roadWidth: 1, labelSize: 1, showBuildings: true, showPaths: true, showLabels: true };
  const presets = {
    standard,
    atlas: { ...standard, background: '#fffff5', water: '#a9cdee', forest: '#d4eaa5', farmland: '#f5f2dd', meadow: '#e4efc8', motorway: '#ffab68', primary: '#ffdc70', buildings: '#ded7df', text: '#323b40', roadWidth: 1.15 },
    night: { ...standard, background: '#17212c', water: '#264d64', forest: '#2a4439', farmland: '#353b30', meadow: '#304337', buildings: '#48515d', motorway: '#d7a65e', primary: '#bcb288', residential: '#788492', text: '#e6edf5', halo: '#17212c' },
    contrast: { ...standard, background: '#ffffff', water: '#83c7e5', forest: '#abd09c', farmland: '#f1e8bb', motorway: '#e99a36', primary: '#edc657', residential: '#e2ded7', text: '#17212b', roadWidth: 1.35, labelSize: 1.15 },
  };
  function normalize(value) {
    const input = value && typeof value === 'object' ? value : {};
    const result = { ...standard };
    for (const name of Object.keys(standard)) {
      if (typeof standard[name] === 'string' && /^#[0-9a-f]{6}$/i.test(input[name] || '')) result[name] = input[name];
      if (typeof standard[name] === 'boolean' && typeof input[name] === 'boolean') result[name] = input[name];
    }
    for (const [name, min, max] of [['roadWidth', 0.6, 2], ['labelSize', 0.8, 1.5]]) {
      const number = input[name];
      if (typeof number === 'number' && Number.isFinite(number)) result[name] = Math.max(min, Math.min(max, number));
    }
    return result;
  }
  // Keep zoom expressions at the top level, as required by MapLibre.
  function scaleWidth(value, factor) {
    if (typeof value === 'number') return value * factor;
    if (!Array.isArray(value)) return value;
    const out = structuredClone(value);
    if (out[0] === 'interpolate') for (let i = 4; i < out.length; i += 2) out[i] = scaleWidth(out[i], factor);
    if (out[0] === 'match') {
      for (let i = 3; i < out.length - 1; i += 2) out[i] = scaleWidth(out[i], factor);
      out[out.length - 1] = scaleWidth(out[out.length - 1], factor);
    }
    return out;
  }
  function paintFor(layer, style) {
    const paint = structuredClone(layer.paint || {});
    const colors = { background: ['background-color', style.background], water: ['fill-color', style.water], forest: ['fill-color', style.forest], buildings: ['fill-color', style.buildings] };
    if (colors[layer.id]) paint[colors[layer.id][0]] = colors[layer.id][1];
    if (layer.id === 'offline-buildings-extrusion') paint['fill-extrusion-color'] = style.buildings;
    if (layer.id === 'farmland') paint['fill-color'] = ['match', ['get', 'class'], 'meadow', style.meadow, style.farmland];
    if (layer.id === 'roads') paint['line-color'] = ['match', ['get', 'class'], ['motorway', 'trunk'], style.motorway, ['primary', 'secondary'], style.primary, style.residential];
    if (layer.id.startsWith('offline-waterways-')) paint['line-color'] = style.water;
    if (layer['source-layer'] === 'transportation') paint['line-width'] = scaleWidth(paint['line-width'], style.roadWidth);
    return paint;
  }
  let draft;
  try { draft = normalize(JSON.parse(localStorage.getItem(key))); } catch { draft = { ...standard }; }
  let map, layers = [], active = false, onLabelsChanged = () => {};
  function apply() {
    if (!map || !active) return;
    for (const layer of layers) {
      if (!map.getLayer(layer.id)) continue;
      for (const [property, value] of Object.entries(paintFor(layer, draft))) map.setPaintProperty(layer.id, property, value);
      if (layer.id === 'buildings' || layer.id === 'offline-buildings-extrusion') map.setLayoutProperty(layer.id, 'visibility', draft.showBuildings ? 'visible' : 'none');
      if (layer.id === 'paths') map.setLayoutProperty(layer.id, 'visibility', draft.showPaths ? 'visible' : 'none');
    }
    const element = map.getContainer();
    element.style.setProperty('--offline-label-color', draft.text);
    element.style.setProperty('--offline-label-halo', draft.halo);
    element.style.setProperty('--offline-label-size', draft.labelSize);
    element.style.setProperty('--offline-label-display', draft.showLabels ? 'block' : 'none');
    onLabelsChanged();
  }
  function activate(target, enabled) {
    map = target;
    active = enabled;
    layers = enabled ? structuredClone(map.getStyle().layers.filter(layer => layer.source === 'tinytiles' || layer.source === 'offline-waterways' || layer.source === 'offline-buildings' || layer.id === 'background')) : [];
    apply();
  }
  function bind(refreshLabels) {
    onLabelsChanged = refreshLabels;
    const editor = document.getElementById('offlineStyleEditor');
    if (!editor) return;
    const status = document.getElementById('offlineStyleStatus');
    const preset = document.getElementById('offlineStylePreset');
    const controls = [...editor.querySelectorAll('[data-style-field]')];
    function sync() {
      for (const input of controls) {
        const value = draft[input.dataset.styleField];
        if (input.type === 'checkbox') input.checked = value;
        else input.value = value;
        const output = document.getElementById(input.id + 'Value');
        if (output) output.textContent = Math.round(value * 100) + ' %';
      }
    }
    function preview() {
      apply();
      status.textContent = active ? 'Vorschau aktiv. Mit „Stil speichern“ in diesem Browser behalten.' : 'Wird beim Anzeigen der Offline-Karte angewendet. Speichern gilt für diesen Browser.';
    }
    editor.addEventListener('input', event => {
      const input = event.target;
      const field = input.dataset.styleField;
      if (!field) return;
      draft = normalize({ ...draft, [field]: input.type === 'checkbox' ? input.checked : input.type === 'range' ? Number(input.value) : input.value });
      preset.value = 'custom';
      sync(); preview();
    });
    preset.addEventListener('change', () => {
      if (!presets[preset.value]) return;
      draft = { ...presets[preset.value] }; sync(); preview();
    });
    document.getElementById('offlineStyleSave').addEventListener('click', () => {
      try { localStorage.setItem(key, JSON.stringify(draft)); status.textContent = 'Stil in diesem Browser gespeichert.'; }
      catch { status.textContent = 'Speichern nicht möglich. Die Vorschau bleibt für diese Sitzung aktiv.'; }
    });
    document.getElementById('offlineStyleReset').addEventListener('click', () => {
      draft = { ...standard }; preset.value = 'standard'; sync(); preview();
    });
    preset.value = Object.keys(presets).find(name => JSON.stringify(presets[name]) === JSON.stringify(draft)) || 'custom';
    sync();
  }
  return { activate, bind, normalize, scaleWidth, paintFor, presets };
})();

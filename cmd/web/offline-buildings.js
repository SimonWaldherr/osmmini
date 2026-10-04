/* Viewport-sized offline building prisms, shared by microMap and MapLibre. */
(function(root) {
  'use strict';
  const sourceID = 'offline-buildings', layerID = 'offline-buildings-extrusion', minZoom = 14;
  let map, enabled = false, timer, request, onStyle;
  const empty = () => ({type: 'FeatureCollection', features: []});
  function ensure() {
    if (map.getSource(sourceID)) return;
    map.addSource(sourceID, {type: 'geojson', data: empty(), maxzoom: 18, tolerance: 0, buffer: 64});
    map.addLayer({
      id: layerID, type: 'fill-extrusion', source: sourceID, minzoom: minZoom,
      paint: {
        'fill-extrusion-color': '#d8cec3', 'fill-extrusion-opacity': 1,
        'fill-extrusion-height': ['coalesce', ['get', 'render_height'], 8],
        'fill-extrusion-base': ['coalesce', ['get', 'render_min_height'], 0],
        'fill-extrusion-vertical-gradient': true,
      },
    });
  }
  function clear() { map?.getSource(sourceID)?.setData(empty()); }
  function cancel() { request?.abort(); request = null; }
  function queue() {
    if (!enabled) return;
    root.clearTimeout(timer);
    cancel();
    // Use the actual fractional zoom: a z13.6 request must not draw z14 data.
    if (map.getZoom() < minZoom) { cancel(); clear(); return; }
    timer = root.setTimeout(refresh, 180);
  }
  async function refresh() {
    if (!enabled || map.getZoom() < minZoom) return;
    const target = map, b = target.getBounds();
    const bbox = [b.getWest(), b.getSouth(), b.getEast(), b.getNorth()].map(n => Number(n).toFixed(6)).join(',');
    cancel();
    const controller = new AbortController();
    request = controller;
    try {
      const response = await root.fetch(`/api/v1/tinytiles/buildings?bbox=${encodeURIComponent(bbox)}&zoom=${Math.floor(target.getZoom())}`, {
        signal: controller.signal, headers: {Accept: 'application/geo+json, application/json'}, cache: 'no-store',
      });
      if (!response.ok) throw Error(`offline buildings: ${response.status}`);
      const data = await response.json();
      if (!enabled || request !== controller || target !== map) return;
      target.getSource(sourceID)?.setData(data?.type === 'FeatureCollection' ? data : empty());
    } catch (error) {
      if (error?.name !== 'AbortError') root.console?.debug('Offline buildings are temporarily unavailable', error);
    }
  }
  function activate(target, visible) {
    if (map && (map !== target || !visible)) {
      map.off('moveend', queue); map.off('zoomend', queue); map.off('style.load', onStyle);
      root.clearTimeout(timer); cancel(); clear(); enabled = false;
    }
    const bind = !enabled || map !== target;
    map = target; enabled = visible;
    if (!enabled) return;
    ensure();
    if (bind) {
      onStyle = () => { if (enabled) { cancel(); ensure(); queue(); } };
      map.on('moveend', queue); map.on('zoomend', queue); map.on('style.load', onStyle);
    }
    queue();
  }
  root.OfflineBuildings = {activate};
})(typeof window === 'undefined' ? globalThis : window);

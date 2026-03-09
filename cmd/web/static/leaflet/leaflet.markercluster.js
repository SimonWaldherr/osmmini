/* Lightweight shim for Leaflet.markercluster used as a development fallback.
   This does NOT implement clustering; it provides a minimal API so the
   app can use `L.markerClusterGroup()` without failing when the full
   library is not present locally. Replace with the official library for
   production (https://github.com/Leaflet/Leaflet.markercluster).
*/
(function(){
  if (!window.L) return;
  if (window.L.markerClusterGroup) return; // already provided by real lib

  // Minimal MarkerClusterGroup implementation that behaves like a LayerGroup
  L.MarkerClusterGroup = L.FeatureGroup.extend({
    initialize: function(options){
      L.FeatureGroup.prototype.initialize.call(this, options);
    },
    addLayer: function(layer){
      // simply add the layer to the group (no clustering)
      L.FeatureGroup.prototype.addLayer.call(this, layer);
      return this;
    },
    removeLayer: function(layer){
      L.FeatureGroup.prototype.removeLayer.call(this, layer);
      return this;
    }
  });

  L.markerClusterGroup = function(opts){ return new L.MarkerClusterGroup(opts); };
})();

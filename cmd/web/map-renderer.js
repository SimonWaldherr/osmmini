/* Load the local mapping library and bridge renderer-specific browser APIs. */
(function(root){
  'use strict';
  const defaults={quality:'balanced',view:'flat',buildings:'auto',sky:'auto',interaction:'full'};
  const choices={quality:['economy','balanced','sharp'],view:['flat','perspective','steep'],buildings:['auto','canvas'],sky:['auto','off'],interaction:['full','simple']};
  const presets={
    standard:{...defaults},
    performance:{...defaults,quality:'economy',buildings:'canvas',sky:'off',interaction:'simple'},
    perspective:{...defaults,quality:'sharp',view:'perspective'},
  };
  const fields={quality:'microMapQuality',view:'microMapView',buildings:'microMapBuildings',sky:'microMapSky',interaction:'microMapInteraction'};
  const viewportKey='osmminiRendererViewport';
  let cssReady=null, uiBound=false;

  function normalize(settings={}) {
    const input=settings && typeof settings==='object' ? settings : {};
    const micromap={};
    for(const key of Object.keys(defaults)) micromap[key]=choices[key].includes(input.micromap?.[key]) ? input.micromap[key] : defaults[key];
    return {renderer:input.renderer==='maplibre' ? 'maplibre' : 'micromap',micromap};
  }
  function initialSettings() {
    try {
      const parsed=JSON.parse(root.document.getElementById('initialSettings').textContent);
      return (typeof parsed==='string' ? JSON.parse(parsed) : parsed)?.map_display;
    }catch {return undefined;}
  }
  function moduleURL(renderer,version='') {
    return renderer==='micromap' ? '/static/micromap/micromap.mjs?v='+encodeURIComponent(version) : '/static/maplibre/maplibre-gl.mjs';
  }
  function loadCSS() {
    if(cssReady) return cssReady;
    cssReady=new Promise((resolve,reject)=>{
      const css=root.document.createElement('link');
      css.rel='stylesheet';css.href='/static/maplibre/maplibre-gl.css';
      css.onload=resolve;css.onerror=()=>{cssReady=null;css.remove?.();reject(Error('MapLibre CSS konnte nicht geladen werden.'));};
      root.document.head.appendChild(css);
    });
    return cssReady;
  }
  async function load({version='', search=root.location?.search || '', settings, importer=url=>import(url)}={}) {
    const config=normalize(settings);
    const override=new URLSearchParams(search).get('renderer');
    if(['micromap','maplibre'].includes(override)) config.renderer=override;
    if(config.renderer==='maplibre') await loadCSS();
    const library=await importer(moduleURL(config.renderer,version));
    // Existing application modules use the shared MapLibre-shaped API.
    root.maplibregl=library;
    root.mapRenderer=config.renderer;
    root.mapDisplay=config;
    root.mapRendererVersion=version;
    return library;
  }
  async function preflight(settings,importer=url=>import(url)) {
    const config=normalize(settings);
    if(config.renderer===root.mapRenderer) return;
    // Check local assets before persisting a renderer that cannot load.
    if(config.renderer==='maplibre') await loadCSS();
    const library=await importer(moduleURL(config.renderer,root.mapRendererVersion));
    if(typeof library.Map!=='function') throw Error('Die gewählte Kartenbibliothek ist nicht verfügbar.');
  }
  function options(settings) {
    const config=normalize(settings);
    if(config.renderer!=='micromap') return {};
    const mm=config.micromap;
    return {
      pixelRatio:{economy:1,balanced:1.5,sharp:2}[mm.quality],
      pitch:{flat:0,perspective:45,steep:60}[mm.view],maxPitch:60,
      webgl:mm.buildings!=='canvas',
      dragRotate:mm.interaction==='full',touchPitch:mm.interaction==='full',
      pitchWithRotate:mm.interaction==='full',
    };
  }
  function mapOptions() {
    const result=options(root.mapDisplay);
    try {
      const raw=root.sessionStorage?.getItem(viewportKey);
      if(raw) {
        root.sessionStorage.removeItem(viewportKey);
        const saved=JSON.parse(raw);
        if(Date.now()-saved.time<60000 && Date.now()>=saved.time && Array.isArray(saved.center) && saved.center.length===2 &&
          saved.center.every(Number.isFinite) && Math.abs(saved.center[0])<=180 && Math.abs(saved.center[1])<=85.051129 &&
          Number.isFinite(saved.zoom) && saved.zoom>=0 && saved.zoom<=22) {
          result.center=saved.center;result.zoom=saved.zoom;
          root.MapRenderer.restoreSettingsPanel=true;
        }
      }
    }catch { /* A storage restriction must not prevent displaying the map. */ }
    return result;
  }
  function bindMap(map) {
    if(root.mapRenderer!=='micromap') return;
    if(root.mapDisplay.micromap.interaction==='simple') map.touchZoomRotate?.disableRotation();
    const applySky=()=>{if(root.mapDisplay.micromap.sky==='off') map.setSky(false);};
    map.on('style.load',applySky);
    applySky();
  }
  function readSettingsUI() {
    const micromap={};
    for(const [key,id] of Object.entries(fields)) micromap[key]=root.document.getElementById(id)?.value;
    return normalize({renderer:root.document.getElementById('mapRendererSelect')?.value,micromap});
  }
  function presetName(micromap) {
    return Object.keys(presets).find(name=>Object.keys(defaults).every(key=>presets[name][key]===micromap[key])) || 'custom';
  }
  function updateUI() {
    const config=readSettingsUI();
    root.document.getElementById('microMapOptions').hidden=config.renderer!=='micromap';
    root.document.getElementById('microMapPreset').value=presetName(config.micromap);
    const active=root.mapRenderer==='maplibre' ? 'MapLibre GL' : 'microMap';
    root.document.getElementById('mapRendererStatus').textContent='Aktiv: '+active+(needsReload(config) ? ' · Änderung noch nicht gespeichert' : '');
  }
  function bindSettingsUI(settings) {
    // A URL override is reflected in the form and can be made permanent by Save.
    const config=normalize(settings);config.renderer=root.mapRenderer || config.renderer;
    root.document.getElementById('mapRendererSelect').value=config.renderer;
    for(const [key,id] of Object.entries(fields)) root.document.getElementById(id).value=config.micromap[key];
    if(!uiBound) {
      root.document.getElementById('mapRendererSelect').addEventListener('change',updateUI);
      for(const id of Object.values(fields)) root.document.getElementById(id).addEventListener('change',updateUI);
      root.document.getElementById('microMapPreset').addEventListener('change',event=>{
        const preset=presets[event.target.value];
        if(preset) for(const [key,id] of Object.entries(fields)) root.document.getElementById(id).value=preset[key];
        updateUI();
      });
      uiBound=true;
    }
    updateUI();
  }
  function needsReload(settings) {
    const current=normalize(root.mapDisplay),next=normalize(settings);
    return current.renderer!==next.renderer || (next.renderer==='micromap' &&
      Object.keys(defaults).some(key=>current.micromap[key]!==next.micromap[key]));
  }
  function applySavedSettings(settings,map) {
    const reload=needsReload(settings);
    // Saving from the form supersedes the old URL override.
    const url=new URL(root.location.href);url.searchParams.delete('renderer');
    if(reload) {
      try {
        const center=map.getCenter();
        root.sessionStorage.setItem(viewportKey,JSON.stringify({center:[center.lng,center.lat],zoom:map.getZoom(),time:Date.now()}));
      }catch { /* Reload still works if browser storage is unavailable. */ }
      root.location.replace(url.href);
    }else {
      root.mapDisplay=normalize(settings);
      root.history.replaceState(null,'',url.href);
      updateUI();
    }
    return reload;
  }

  function capture(map) {
    if(root.mapRenderer!=='micromap') return map.getCanvas();
    // microMap's getCanvas() contains labels only. Its tiles live on separate
    // canvases/images. In a flat, north-facing view their DOM rectangles give
    // the exact positions needed to compose a complete map image.
    if(Math.abs(map.getPitch())>0.001 || Math.abs(map.getBearing())>0.001) {
      throw Error('Für den Bildexport die Karte nach Norden ausrichten und die Neigung auf 0° stellen.');
    }
    const container=map.getContainer(), bounds=container.getBoundingClientRect();
    if(!bounds.width || !bounds.height || bounds.width*bounds.height>16000000) throw Error('Kartenbild ist zu groß oder die Karte ist nicht sichtbar.');
    const canvas=root.document.createElement('canvas');
    canvas.width=Math.round(bounds.width);canvas.height=Math.round(bounds.height);
    const ctx=canvas.getContext('2d');
    const background=root.getComputedStyle(container).backgroundColor;
    ctx.fillStyle=background && background!=='transparent' && background!=='rgba(0, 0, 0, 0)' ? background : '#ffffff';
    ctx.fillRect(0,0,canvas.width,canvas.height);
    function stack(node) {
      const indices=[];
      while(node && node!==container) {
        const css=root.getComputedStyle(node);
        if(css.display==='none' || css.visibility==='hidden' || css.opacity==='0') return null;
        indices.unshift(parseInt(css.zIndex,10)||0);
        node=node.parentElement;
      }
      return indices;
    }
    const layers=Array.from(container.querySelectorAll('canvas, img')).map((node,index)=>({node,index,stack:stack(node)})).filter(layer=>layer.stack);
    layers.sort((a,b)=>{
      for(let i=0;i<Math.max(a.stack.length,b.stack.length);i++) {
        const diff=(a.stack[i]||0)-(b.stack[i]||0);
        if(diff) return diff;
      }
      return a.index-b.index;
    });
    for(const {node} of layers) {
      if(node.closest('.micromap-marker, .micromap-popup, .micromap-ctrl-corner')) continue;
      if(node.tagName==='IMG' && (!node.complete || !node.naturalWidth)) continue;
      if(node.tagName==='CANVAS' && (!node.width || !node.height)) continue;
      const r=node.getBoundingClientRect();
      if(!r.width || !r.height || r.right<=bounds.left || r.left>=bounds.right || r.bottom<=bounds.top || r.top>=bounds.bottom) continue;
      ctx.drawImage(node,r.left-bounds.left,r.top-bounds.top,r.width,r.height);
    }
    return canvas;
  }
  root.MapRenderer={load,capture,normalize,initialSettings,options,mapOptions,bindMap,preflight,bindSettingsUI,readSettingsUI,needsReload,applySavedSettings};
})(typeof window==='undefined'?globalThis:window);

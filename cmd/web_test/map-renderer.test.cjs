const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const vm=require('node:vm');
const source=fs.readFileSync(path.join(__dirname,'../web/map-renderer.js'),'utf8');
function harness(extra={}) {
  const links=[];
  const context=vm.createContext({URLSearchParams,document:{createElement:()=>({}),head:{appendChild(link){links.push(link);link.onload();}}},...extra});
  vm.runInContext(source,context);
  return {context,links,renderer:context.MapRenderer};
}
test('default loads only the local microMap bundle and exposes its shared API',async()=>{
  const h=harness(),imports=[],library={Map(){},Marker(){}};
  await h.renderer.load({version:'test 1',importer:async url=>{imports.push(url);return library;}});
  assert.deepEqual(imports,['/static/micromap/micromap.mjs?v=test%201']);
  assert.equal(h.context.maplibregl,library);
  assert.equal(h.context.mapRenderer,'micromap');
  assert.equal(h.links.length,0);
});
test('explicit MapLibre selection loads its local CSS and ES module',async()=>{
  const h=harness(),imports=[];
  await h.renderer.load({search:'?renderer=maplibre',importer:async url=>{imports.push(url);return {};}});
  assert.equal(h.context.mapRenderer,'maplibre');
  assert.deepEqual(imports,['/static/maplibre/maplibre-gl.mjs']);
  assert.equal(h.links[0].href,'/static/maplibre/maplibre-gl.css');
});
test('library failure propagates to the page error handler without CDN fallback',async()=>{
  const h=harness(),imports=[];
  await assert.rejects(h.renderer.load({search:'?renderer=unknown',importer:async url=>{imports.push(url);throw Error('missing bundle');}}),/missing bundle/);
  assert.equal(imports.length,1);
  assert.equal(h.context.maplibregl,undefined);
});
test('microMap applies a vector style without requiring WebGL',async()=>{
  const app=fs.readFileSync(path.join(__dirname,'../web/app.js'),'utf8');
  const applied=[];
  const context=vm.createContext({
    window:{mapRenderer:'micromap'},mapInitialLoad:Promise.resolve(),tileLayerGeneration:0,
    currentTileLayer:null,baseLayerKind:null,
    map:{setStyle:url=>applied.push(url)},supportsWebGL:()=>false,
    waitForStyleReady:async()=>true,waitForMapLayerPaint:async()=>{},
    isTinyTilesSettings:()=>true,OfflineMapStyle:{activate(){}},
    escapeHtml:s=>s,tileSourceCacheKey:()=>'',usesDirectRaster:()=>false,
    updateMapModeUI(){},setOfflineLabelsVisible(){},setOfflineWaterwaysVisible(){},rehydrateMapLayers(){},console,
  });
  vm.runInContext(app.slice(app.indexOf('async function applyTileLayer('),app.indexOf('// Initialize the tile layer')),context);
  assert.equal(await context.applyTileLayer({tiles:{map_type:'vector',style_url:'/static/styles/tinytiles-minimal.json'}}),true);
  assert.deepEqual(applied,['/static/styles/tinytiles-minimal.json']);
});
test('flat microMap capture includes raster and vector tiles in paint order, omitting hidden canvases',()=>{
  const drawn=[],output={getContext:()=>({fillRect(){},drawImage:(...args)=>drawn.push(args)})};
  const bounds={left:10,top:20,right:210,bottom:120,width:200,height:100};
  const container={getBoundingClientRect:()=>bounds,css:{backgroundColor:'#abc'}};
  const node=(tag,z,rect=bounds,parent=container)=>({tagName:tag,width:200,height:100,complete:true,naturalWidth:256,css:{zIndex:String(z)},parentElement:parent,closest:()=>null,getBoundingClientRect:()=>rect});
  const raster=node('IMG',0),vector=node('CANVAS',1),labels=node('CANVAS',2);
  const hidden=node('CANVAS',1,bounds,{parentElement:container,css:{display:'none'}});
  container.querySelectorAll=()=>[labels,hidden,vector,raster];
  const h=harness({mapRenderer:'micromap',document:{createElement:()=>output},getComputedStyle:node=>node.css});
  const result=h.renderer.capture({getPitch:()=>0,getBearing:()=>0,getContainer:()=>container});
  assert.equal(result,output);
  assert.equal(result.width,200);assert.equal(result.height,100);
  assert.deepEqual(drawn.map(args=>args[0]),[raster,vector,labels]);
  assert.deepEqual(drawn[0].slice(1),[0,0,200,100]);
  assert.throws(()=>h.renderer.capture({getPitch:()=>20,getBearing:()=>0}),/Neigung/);
});
test('MapLibre capture returns its complete render canvas',()=>{
  const h=harness({mapRenderer:'maplibre'}),canvas={};
  assert.equal(h.renderer.capture({getCanvas:()=>canvas}),canvas);
});

test('persisted renderer is used unless an explicit supported URL override is present',async()=>{
  const h=harness(),imports=[];
  await h.renderer.load({settings:{renderer:'maplibre'},importer:async url=>{imports.push(url);return {};}});
  assert.equal(h.context.mapRenderer,'maplibre');
  await h.renderer.load({settings:{renderer:'maplibre'},search:'?renderer=micromap',importer:async url=>{imports.push(url);return {};}});
  assert.equal(h.context.mapRenderer,'micromap');
  await h.renderer.load({settings:{renderer:'maplibre'},search:'?renderer=unknown',importer:async()=>({})});
  assert.equal(h.context.mapRenderer,'maplibre');
});
test('microMap choices control actual rendering and camera constructor options',()=>{
  const h=harness();
  const config={renderer:'micromap',micromap:{quality:'economy',view:'steep',buildings:'canvas',interaction:'simple',sky:'off'}};
  const options=h.renderer.options(config);
  assert.equal(options.pixelRatio,1);assert.equal(options.pitch,60);
  assert.equal(options.webgl,false);assert.equal(options.dragRotate,false);assert.equal(options.touchPitch,false);
  assert.equal(h.renderer.options({micromap:{quality:'sharp',view:'perspective'}}).pixelRatio,2);
  assert.equal(h.renderer.options({micromap:{quality:'sharp',view:'perspective'}}).pitch,45);
  assert.equal(Object.keys(h.renderer.options({renderer:'maplibre',micromap:config.micromap})).length,0);
  assert.equal(h.renderer.normalize({micromap:{quality:'unknown'}}).micromap.quality,'balanced');
});
function uiHarness(extra={}) {
  const elements={};
  for(const id of ['mapRendererSelect','microMapPreset','microMapOptions','mapRendererStatus','microMapQuality','microMapView','microMapBuildings','microMapSky','microMapInteraction']) {
    elements[id]={value:'',handlers:{},addEventListener(type,fn){this.handlers[type]=fn;}};
  }
  const h=harness({URL,mapRenderer:'micromap',document:{getElementById:id=>elements[id]},...extra});
  h.context.mapDisplay=h.renderer.normalize({});
  return {...h,elements,change(id,value){elements[id].value=value;elements[id].handlers.change({target:elements[id]});}};
}
test('settings presets populate individual controls; custom edits and hidden values survive renderer switches',()=>{
  const h=uiHarness();
  h.renderer.bindSettingsUI({});
  assert.equal(h.elements.microMapPreset.value,'standard');
  h.change('microMapPreset','performance');
  assert.equal(h.elements.microMapQuality.value,'economy');
  assert.equal(h.elements.microMapBuildings.value,'canvas');
  assert.equal(h.renderer.readSettingsUI().micromap.interaction,'simple');
  h.change('microMapView','steep');
  assert.equal(h.elements.microMapPreset.value,'custom');
  h.change('mapRendererSelect','maplibre');
  assert.equal(h.elements.microMapOptions.hidden,true);
  assert.equal(h.renderer.readSettingsUI().micromap.view,'steep');
  h.change('mapRendererSelect','micromap');
  assert.equal(h.elements.microMapOptions.hidden,false);
  assert.equal(h.elements.microMapView.value,'steep');
  assert.match(h.elements.mapRendererStatus.textContent,/noch nicht gespeichert/);
});
test('sky preference is reapplied after every style change',()=>{
  const h=harness({mapRenderer:'micromap',mapDisplay:{micromap:{sky:'off',interaction:'simple'}}});
  let sky,listener,rotationDisabled=false;
  h.renderer.bindMap({touchZoomRotate:{disableRotation:()=>rotationDisabled=true},setSky:value=>sky=value,on:(type,fn)=>{assert.equal(type,'style.load');listener=fn;}});
  assert.equal(sky,false);sky=true;listener();assert.equal(sky,false);assert.equal(rotationDisabled,true);
});
test('saving a new renderer clears the URL override and preserves the viewport across reload',()=>{
  let nextURL;
  const storage=new Map();
  const h=uiHarness({location:{href:'http://localhost/?renderer=micromap&test=1',replace:url=>nextURL=url},sessionStorage:{setItem:(key,value)=>storage.set(key,value),getItem:key=>storage.get(key),removeItem:key=>storage.delete(key)}});
  const config={renderer:'maplibre',micromap:h.renderer.normalize({}).micromap};
  assert.equal(h.renderer.applySavedSettings(config,{getCenter:()=>({lng:12.7,lat:48.7}),getZoom:()=>14}),true);
  assert.equal(nextURL,'http://localhost/?test=1');
  h.context.mapDisplay=config;
  const restored=h.renderer.mapOptions();
  assert.deepEqual(Array.from(restored.center),[12.7,48.7]);assert.equal(restored.zoom,14);
  assert.equal(h.renderer.restoreSettingsPanel,true);assert.equal(storage.size,0);
  assert.equal(h.renderer.mapOptions().center,undefined);
});
test('inactive microMap preferences are saved without reloading MapLibre',()=>{
  let historyURL;
  const h=uiHarness({mapRenderer:'maplibre',location:{href:'http://localhost/?renderer=maplibre'},history:{replaceState:(_,__,url)=>historyURL=url}});
  h.context.mapDisplay=h.renderer.normalize({renderer:'maplibre'});
  h.renderer.bindSettingsUI(h.context.mapDisplay);
  const config=h.renderer.normalize({renderer:'maplibre',micromap:{view:'perspective'}});
  assert.equal(h.renderer.applySavedSettings(config,{}),false);
  assert.equal(h.context.mapDisplay.micromap.view,'perspective');
  assert.equal(historyURL,'http://localhost/');
});
test('preflight rejects missing renderer assets before they can be saved',async()=>{
  const h=harness({mapRenderer:'micromap'});
  await assert.rejects(h.renderer.preflight({renderer:'maplibre'},async()=>{throw Error('missing assets');}),/missing assets/);
  assert.equal(h.context.mapRenderer,'micromap');
});

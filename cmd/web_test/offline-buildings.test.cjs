const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const source=fs.readFileSync(require.resolve('../web/offline-buildings.js'),'utf8');
function harness() {
  const timers=new Map(),handlers=new Map(),sources=new Map(),layers=new Map(),requests=[],writes=[];
  let zoom=15,serial=0;
  const map={getZoom:()=>zoom,getBounds:()=>({getWest:()=>11,getSouth:()=>48,getEast:()=>11.01,getNorth:()=>48.01}),
    getSource:id=>sources.get(id),addSource:(id,spec)=>sources.set(id,{spec,setData:data=>writes.push(data)}),addLayer:layer=>layers.set(layer.id,layer),
    on:(event,fn)=>handlers.set(event,fn),off:(event,fn)=>{if(handlers.get(event)===fn)handlers.delete(event);}};
  const context=vm.createContext({AbortController,console,setTimeout:fn=>{timers.set(++serial,fn);return serial;},clearTimeout:id=>timers.delete(id),
    fetch:(url,options)=>new Promise(resolve=>requests.push({url,options,resolve}))});
  vm.runInContext(source,context);
  async function flush(){const fn=[...timers.values()].at(-1);timers.clear();fn?.();await Promise.resolve();}
  async function respond(i,data){requests[i].resolve({ok:true,json:async()=>data});await new Promise(setImmediate);}
  return {api:context.OfflineBuildings,map,timers,handlers,sources,layers,requests,writes,flush,respond,setZoom:value=>zoom=value};
}
test('offline building layer supplies real heights, bases and a fallback to both renderers',async()=>{
  const h=harness();h.api.activate(h.map,true);await h.flush();
  assert.equal(h.layers.get('offline-buildings-extrusion').type,'fill-extrusion');
  assert.equal(h.sources.get('offline-buildings').spec.tolerance,0);
  const paint=h.layers.get('offline-buildings-extrusion').paint;
  assert.deepEqual(JSON.parse(JSON.stringify(paint['fill-extrusion-height'])),['coalesce',['get','render_height'],8]);
  assert.deepEqual(JSON.parse(JSON.stringify(paint['fill-extrusion-base'])),['coalesce',['get','render_min_height'],0]);
  assert.match(h.requests[0].url,/\/api\/v1\/tinytiles\/buildings\?bbox=11\.000000%2C48\.000000%2C11\.010000%2C48\.010000&zoom=15/);
  const data={type:'FeatureCollection',features:[{id:1}]};await h.respond(0,data);assert.equal(h.writes.at(-1),data);
});
test('zooming out or switching maps invalidates pending building responses',async()=>{
  const h=harness();h.api.activate(h.map,true);await h.flush();
  h.setZoom(13.9);h.handlers.get('zoomend')();
  assert.equal(h.requests[0].options.signal.aborted,true);assert.equal(h.writes.at(-1).features.length,0);
  await h.respond(0,{type:'FeatureCollection',features:[{id:'stale'}]});assert.equal(h.writes.at(-1).features.length,0);
  h.setZoom(15);h.handlers.get('zoomend')();await h.flush();h.api.activate(h.map,false);
  assert.equal(h.handlers.size,0);assert.equal(h.requests[1].options.signal.aborted,true);
  await h.respond(1,{type:'FeatureCollection',features:[{id:'stale'}]});assert.equal(h.writes.at(-1).features.length,0);
});
test('style reload restores building source and cancels requests for the previous style',async()=>{
  const h=harness();h.api.activate(h.map,true);await h.flush();h.sources.clear();h.layers.clear();h.handlers.get('style.load')();
  assert.equal(h.requests[0].options.signal.aborted,true);assert.ok(h.layers.has('offline-buildings-extrusion'));await h.flush();
  await h.respond(0,{type:'FeatureCollection',features:[{id:'old'}]});assert.equal(h.writes.length,0);
  await h.respond(1,{type:'FeatureCollection',features:[{id:'new'}]});assert.equal(h.writes.at(-1).features[0].id,'new');
});

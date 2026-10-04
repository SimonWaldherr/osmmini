const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const vm=require('node:vm');
const source=fs.readFileSync(path.join(__dirname,'../web/offline-style.js'),'utf8');
const base=JSON.parse(fs.readFileSync(path.join(__dirname,'../web/static/styles/tinytiles-minimal.json'),'utf8'));
function harness(saved) {
 const context=vm.createContext({structuredClone,localStorage:{getItem:()=>saved},document:{getElementById:()=>null}});
 vm.runInContext(source+'\nglobalThis.editor=OfflineMapStyle;',context);
 return context.editor;
}
test('offline style restores valid settings and rejects corrupt or unbounded preferences',()=>{
 const editor=harness('{invalid');
 const normalized=editor.normalize({water:'url(https://example.org)',roadWidth:200,labelSize:-2,showLabels:'false'});
 assert.equal(normalized.water,editor.presets.standard.water);
 assert.equal(normalized.roadWidth,2);
 assert.equal(normalized.labelSize,0.8);
 assert.equal(normalized.showLabels,true);
});
test('road width scaling preserves zoom stops, road classes and original expressions',()=>{
 const editor=harness(null);
 const roads=base.layers.find(l=>l.id==='roads');
 const original=JSON.stringify(roads);
 const paint=editor.paintFor(roads,{...editor.presets.standard,roadWidth:2});
 const expression=paint['line-width'];
 assert.equal(expression[0],'interpolate');
 assert.equal(expression[3],5);
 assert.equal(expression[5],10);
 assert.equal(expression[4],roads.paint['line-width'][4]*2);
 assert.equal(expression[6][3],roads.paint['line-width'][6][3]*2);
 assert.equal(JSON.stringify(roads),original);
});
test('saved style applies only to offline base layers and remains unchanged on repeat application',()=>{
 const editor=harness(JSON.stringify({water:'#123456',roadWidth:1.5,showBuildings:false}));
 const writes=[]; const variables={};
 const map={getStyle:()=>({...base,layers:[...base.layers,{id:'route',source:'route',paint:{'line-color':'red'}}]}),getLayer:()=>true,setPaintProperty:(...v)=>writes.push(v),setLayoutProperty:(...v)=>writes.push(v),getContainer:()=>({style:{setProperty:(k,v)=>variables[k]=v}})};
 editor.activate(map,true);
 assert.ok(writes.some(v=>v[0]==='water'&&v[2]==='#123456'));
 assert.ok(writes.some(v=>v[0]==='buildings'&&v[1]==='visibility'&&v[2]==='none'));
 assert.equal(writes.some(v=>v[0]==='route'),false);
 const first=JSON.stringify(writes);writes.length=0;
 editor.activate(map,true);assert.equal(JSON.stringify(writes),first);
 writes.length=0;editor.activate(map,false);assert.equal(writes.length,0);
});

 test('offline building extrusions follow the saved building colour and visibility',()=>{
 const editor=harness(JSON.stringify({buildings:'#123456',showBuildings:false}));
 const writes=[];
 const extrusion={id:'offline-buildings-extrusion',type:'fill-extrusion',source:'offline-buildings',paint:{'fill-extrusion-color':'#d8cec3','fill-extrusion-height':['get','render_height']}};
 const map={getStyle:()=>({layers:[extrusion]}),getLayer:()=>true,setPaintProperty:(...v)=>writes.push(v),setLayoutProperty:(...v)=>writes.push(v),getContainer:()=>({style:{setProperty(){}}})};
 editor.activate(map,true);
 assert.ok(writes.some(v=>v[1]==='fill-extrusion-color'&&v[2]==='#123456'));
 assert.ok(writes.some(v=>v[1]==='visibility'&&v[2]==='none'));
 assert.equal(writes.some(v=>v[1]==='fill-color'),false);
 });

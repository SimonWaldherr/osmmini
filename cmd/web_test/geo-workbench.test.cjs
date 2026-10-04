const {test} = require('node:test');
const assert = require('node:assert/strict');
require('../web/geo-workbench.js');
const W = GeoWorkbench;
const feature = (properties, geometry = {type:'Point', coordinates:[12,48]}) => ({type:'Feature', properties, geometry});
const collection = features => ({type:'FeatureCollection', features});

test('GeoJSON loading validates shape, coordinates, rings and workload limits', () => {
 const data = collection([feature({code:'001'}), feature(null, null)]);
 assert.deepEqual(W.parse(JSON.stringify(data)), data);
 assert.equal(W.validate(data), 1);
 for (const data of [null, {type:'FeatureCollection',features:{}}, collection([{}]), collection([feature({}, {type:'Point',coordinates:[181,0]})]), collection([feature({}, {type:'LineString',coordinates:[[0,0]]})]), collection([feature({}, {type:'Polygon',coordinates:[[[0,0],[1,0],[1,1],[0,1]]]})]), collection([feature({}, {type:'Point',coordinates:[0,NaN]})]), {...collection([]),crs:{type:'name'}}, collection(Array(5001).fill(feature({})))]) assert.throws(() => W.validate(data));
 assert.throws(() => W.parse(' '.repeat(10*1024*1024+1)));
 const types = collection([feature({}, {type:'GeometryCollection',geometries:[{type:'MultiPoint',coordinates:[[12,48],[13,49]]},{type:'MultiLineString',coordinates:[[[12,48],[13,49]]]},{type:'MultiPolygon',coordinates:[[[[12,48],[13,48],[13,49],[12,48]]]]}]})]);
 assert.equal(W.validate(types), 8);
});

test('attribute selection and filtering keep IDs, null geometries and original data', () => {
 const data = collection([{...feature({zone:'north',code:'001'}),id:0}, feature({zone:'south',code:'002'}), feature(null,null)]);
 data.bbox = [12,48,12,48];
 assert.deepEqual(W.matching(data,'zone','north'), [0]);
 assert.deepEqual(W.matching(data,'zone','North'), []);
 assert.deepEqual(W.matching(data,'toString',''), []);
 assert.deepEqual(W.matching(data,'',''), [0,1,2]);
 const subset = W.selectFields(W.subset(data,[0]), ['code','toString']);
 assert.equal(subset.features[0].id,0);
 assert.deepEqual(subset.features[0].properties,{code:'001'});
 assert.equal(subset.bbox,undefined);
 assert.equal(data.features.length,3);
 assert.equal(data.features[0].properties.zone,'north');
 assert.throws(() => W.subset(data,[-1]));
});

test('attribute edits preserve number and boolean types and safely handle special keys', () => {
 const data = W.parse('{"type":"FeatureCollection","features":[{"type":"Feature","geometry":null,"properties":{"__proto__":"x","count":2,"active":true,"nested":{"a":1},"nullable":null}}]}');
 assert.equal(W.edit(data,0,'count','3').features[0].properties.count,3);
 assert.equal(W.edit(data,0,'active','false').features[0].properties.active,false);
 assert.equal(W.edit(data,0,'nullable','null').features[0].properties.nullable,null);
 assert.equal(W.edit(data,0,'__proto__','safe').features[0].properties.__proto__,'safe');
 assert.equal({}.safe,undefined);
 for (const [key,value] of [['count',''],['count','Infinity'],['active','yes'],['nested','text']]) assert.throws(()=>W.edit(data,0,key,value));
 assert.equal(data.features[0].properties.count,2);
});

test('CSV parsing handles BOM, quoted commas, quotes, multiline cells and CRLF', () => {
 const csv = W.parseCSV('\uFEFFcode,name,note\r\n001,"A, B","line 1\nline ""2"""\r\n002,,\r\n');
 assert.deepEqual(csv.header,['code','name','note']);
 assert.equal(csv.rows[0].code,'001');
 assert.equal(csv.rows[0].name,'A, B');
 assert.equal(csv.rows[0].note,'line 1\nline "2"');
 assert.equal(csv.rows[1].name,'');
 for (const source of ['a,a\n1,2','a,\n1,2','a,b\n1','a\n"open','a\n"closed"junk','a\nwrong"quote']) assert.throws(() => W.parseCSV(source));
});

test('CSV joins are deterministic left joins with unique keys, no overwrites or lost leading zeros', () => {
 const data = collection([feature({code:'001'}),feature({code:'002'}),feature({code:'003'}),feature({code:'001'})]);
 const csv = W.parseCSV('id,status\n001,planned\n002,done');
 const result = W.join(data,csv,'code','id');
 assert.equal(result.matched,3); assert.equal(result.unmatched,1);
 assert.equal(result.data.features[0].properties.csv_status,'planned');
 assert.equal(result.data.features[2].properties.csv_status,undefined);
 assert.equal(data.features[0].properties.csv_status,undefined);
 assert.throws(()=>W.join(result.data,csv,'code','id'));
 assert.throws(()=>W.join(data,W.parseCSV('id,status\n001,a\n001,b'),'code','id'));
 assert.throws(()=>W.join(data,W.parseCSV('id,status\n,a'),'code','id'));
 assert.throws(()=>W.join(data,csv,'code','missing'));
});

test('CSV export quotes data and neutralizes spreadsheet formulas without changing GeoJSON', () => {
 const data = collection([feature({name:'A,"B"',note:'=SUM(A1:A2)',number:12,nested:{a:1}})]);
 const csv = W.parseCSV(W.toCSV(data));
 assert.equal(csv.rows[0].name,'A,"B"');
 assert.equal(csv.rows[0].note,"'=SUM(A1:A2)");
 assert.equal(csv.rows[0].nested,'{"a":1}');
 assert.equal(data.features[0].properties.note,'=SUM(A1:A2)');
});

test('line simplification uses metres, preserves endpoints and altitude and leaves polygons intact', () => {
 const coords = [[12,48,5],[12.0005,48.000001,6],[12.001,48,7]];
 const polygon = {type:'Polygon',coordinates:[[[12,48],[13,48],[13,49],[12,48]]]};
 const data = collection([feature({name:'line'},{type:'LineString',coordinates:coords,bbox:[12,48,12.001,48.001]}),feature({},polygon)]);
 data.bbox=[12,48,13,49]; data.features[0].bbox=[12,48,12.001,48.001];
 const result = W.simplify(data,1);
 assert.equal(result.removed,1); assert.equal(result.lines,1);
 assert.deepEqual(result.data.features[0].geometry.coordinates,[coords[0],coords[2]]);
 assert.deepEqual(result.data.features[1].geometry,polygon);
 assert.equal(result.data.bbox,undefined); assert.equal(result.data.features[0].bbox,undefined);
 assert.equal(result.data.features[0].geometry.bbox,undefined);
 assert.equal(data.features[0].geometry.coordinates.length,3);
 assert.deepEqual(W.simplify(data,0).data,data);
 const turn=collection([feature({},{type:'LineString',coordinates:[[0,0],[.001,.001],[.002,0]]})]);
 assert.equal(W.simplify(turn,10).removed,0);
});

test('simplification handles the dateline, closed loops and rejects nonregional lines atomically', () => {
 const dateline=collection([feature({},{type:'MultiLineString',coordinates:[[[179.999,0],[-180,0],[-179.999,0]]]})]);
 assert.equal(W.simplify(dateline,1).removed,1);
 const loop=collection([feature({},{type:'LineString',coordinates:[[12,48],[12.001,48],[12.001,48.001],[12,48]]})]);
 assert.deepEqual(W.simplify(loop,1000).data,loop);
 const large=collection([feature({},{type:'LineString',coordinates:[[0,0],[5,0],[10,0]]})]);
 assert.throws(()=>W.simplify(large,1));
 for(const tolerance of [-1,1001,NaN]) assert.throws(()=>W.simplify(dateline,tolerance));
});

class Element {
 constructor(){this.value='';this.children=[];this.listeners={};this.checked=false;this.selected=false;this.files=[];}
 append(...children){this.children.push(...children);if(this.tag==='select'&&!this.value&&this.children.length===1)this.value=children[0].value;}
 replaceChildren(...children){this.children=children;if(this.tag==='select')this.value='';}
 get selectedOptions(){return this.children.filter(o=>o.selected);}
 setAttribute(key,value){this[key]=value;}
 addEventListener(event,fn){this.listeners[event]=fn;}
 click(){return this.listeners.click?.();}
}
function controller(){
 const elements={};
 for(const id of ['FilterField','EditField','JoinField','CSVKey','Layer','Fields']){const e=new Element();e.tag='select';elements['wb'+id]=e;}
 global.document={getElementById:id=>elements[id]??=new Element(),createElement:tag=>{const e=new Element();e.tag=tag;return e;}};
 document.getElementById('wbShow').checked=true;document.getElementById('wbEditIndex').value='1';document.getElementById('wbTolerance').value='1';
 let source=null, ready=true;const layers=[];
 const map={isStyleLoaded:()=>ready,getSource:()=>source,addSource:(_,s)=>source={data:s.data,setData(d){this.data=d;}},addLayer:l=>layers.push(l)};
 let downloaded;const c=W.create(map,{download:(...args)=>downloaded=args});
 return {c,elements,layers,get source(){return source;},get downloaded(){return downloaded;},reset(){source=null;},ready(v){ready=v;}};
}

test('workbench controller links table, filtering, edits, undo, field exports and map rehydration', () => {
 const state=controller(), e=state.elements;
 state.c.load(collection([feature({code:'001',zone:'north'}),feature({code:'002',zone:'south'})]));
 assert.equal(state.source.data.features.length,2);assert.equal(e.wbBody.children.length,2);
 e.wbFilterField.value='zone';e.wbFilterValue.value='north';e.wbFilterValue.listeners.input();
 assert.equal(state.source.data.features.length,1);
 e.wbKeep.click();assert.equal(state.c.snapshot().features.length,1);
 e.wbUndo.click();assert.equal(state.c.snapshot().features.length,2);
 e.wbEditField.value='code';e.wbEditValue.value='003';e.wbEdit.click();
 assert.equal(state.c.snapshot().features[0].properties.code,'003');
 e.wbFields.children.find(o=>o.value==='code').selected=true;e.wbExport.click();
 assert.deepEqual(JSON.parse(state.downloaded[0]).features[0].properties,{code:'003'});
 state.reset();state.ready(false);state.c.render();assert.equal(state.source,null);
 state.ready(true);state.c.render();assert.equal(state.source.data.features.length,1);
 e.wbShow.checked=false;e.wbShow.listeners.change();assert.equal(state.source.data.features.length,0);
});

test('workbench paginates attributes and failed operations leave the working copy intact', () => {
 const state=controller(), e=state.elements;
 state.c.load(collection(Array.from({length:30},(_,i)=>feature({count:i}))));
 assert.equal(e.wbBody.children.length,25);e.wbNext.click();assert.equal(e.wbBody.children.length,5);
 e.wbEditField.value='count';e.wbEditValue.value='wrong';e.wbEdit.click();
 assert.equal(state.c.snapshot().features[0].properties.count,0);
 assert.match(e.wbStatus.textContent,/Zahl/);
 e.wbUndo.click();assert.equal(state.c.snapshot().features.length,0);
});

test('zoom bounds handle points, collections, dateline crossings and polar display limits',()=>{
 assert.deepEqual(W.bounds(collection([feature({},null),feature({},{type:'Point',coordinates:[12,48]})])),[[11.999,47.999],[12.001,48.001]]);
 const bounds=W.bounds(collection([feature({},{type:'MultiPoint',coordinates:[[179.9,0],[-179.9,1]]})]));
 assert.ok(bounds[1][0]-bounds[0][0]<1);
 assert.deepEqual(W.bounds(collection([feature({},{type:'Point',coordinates:[0,90]})]))[0][1],84.999);
 assert.throws(()=>W.bounds(collection([feature({},null)])));
});

test('example data and CSV join produce three enrichments and a smaller line',()=>{
 const fs=require('node:fs');const path=require('node:path');
 const data=W.parse(fs.readFileSync(path.join(__dirname,'../../testdata/workbench/inspection.geojson'),'utf8'));
 const csv=W.parseCSV(fs.readFileSync(path.join(__dirname,'../../testdata/workbench/inspection.csv'),'utf8'));
 const joined=W.join(data,csv,'code','code');
 assert.equal(joined.matched,3);
 assert.equal(joined.data.features[0].properties.csv_team,'Elektro-Team');
 assert.equal(W.simplify(joined.data,10).removed,1);
});

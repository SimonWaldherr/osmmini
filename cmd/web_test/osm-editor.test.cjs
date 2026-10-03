const {test}=require('node:test');const assert=require('node:assert/strict');require('../web/osm-presets.js');require('../web/osm-editor.js');const E=OSMEditor;
const base={type:'node',id:42,version:3,lat:48,lon:12,tags:{name:'Alt',amenity:'bench'}};
test('tag comparison covers additions changes removals and prototype-like keys',()=>{
 const t=E.tags([['name','Neu'],['__proto__','Wert']]);assert.equal(t.__proto__,'Wert');
 assert.deepEqual(E.changes(base.tags,t).map(d=>d.key),['__proto__','amenity','name']);
 for(const rows of [[['name','a'],['name','b']],[['','x']],[['name','\0']],[['name','x'.repeat(256)]]])assert.throws(()=>E.tags(rows));
 assert.equal(E.tags([['name','😀'.repeat(255)]]).name.length,510);
});
test('OSC preserves version geometry and way references and escapes XML',()=>{
 const way={type:'way',id:33,version:9,nodes:[1,2,3,1],tags:{building:'yes'}};
 const xml=E.osc([{base,value:{...base,tags:{name:'A & "B" <C>\nD'}}},{base:way,value:{...way,tags:{building:'house'}}},{base:null,value:{type:'node',id:-1,lat:49,lon:13,tags:{amenity:'bench'}}}]);
 assert.match(xml,/<modify>/);assert.match(xml,/<create>/);assert.match(xml,/id="42" version="3" lat="48" lon="12"/);assert.match(xml,/A &amp; &quot;B&quot; &lt;C&gt;&#10;D/);assert.equal((xml.match(/<nd ref=/g)||[]).length,4);assert.ok(!xml.includes('changeset='));
});
test('invalid versions and type/ID mutations are blocked, but a real geometry change now validates',()=>{
 for(const value of [{...base,version:4},{...base,type:'relation'},{...base,id:43},{...base,version:0}])assert.throws(()=>E.validateDraft({base,value}));
 const moved=E.validateDraft({base,value:{...base,lat:49}});assert.equal(moved.value.lat,49);assert.equal(moved.base.lat,48);
 assert.match(E.osc([{base,value:{...base,lat:49}}]),/<modify>[\s\S]*lat="49"/);
 const d={base,value:{...base,tags:{name:'Neu'}}};assert.throws(()=>E.osc([d,d]));
 assert.throws(()=>E.element({...base,id:Number.MAX_SAFE_INTEGER+1}));
 assert.throws(()=>E.element({type:'way',id:2,version:1,nodes:[],tags:{}}));
});
test('unchanged objects do not appear in OSC; backups validate on restore',()=>{
 assert.ok(!E.osc([{base,value:base}]).includes('<modify>'));
 const restored=E.restore(JSON.stringify({version:1,drafts:[{base,value:{...base,tags:{name:'Neu'}}}]}));assert.equal(restored[0].base.version,3);
 const moved=E.restore(JSON.stringify({version:1,drafts:[{base,value:{...base,lon:13}}]}));assert.equal(moved[0].value.lon,13);
 const way={type:'way',id:33,version:9,nodes:[1,2,3],tags:{building:'yes'}};
 assert.throws(()=>E.restore(JSON.stringify({version:1,drafts:[{base:way,value:{...way,nodes:[1,2,-999]}}]})),/unbekannt/);
 assert.throws(()=>E.restore(JSON.stringify({version:2,drafts:[]})));
});
test('relations validate members, order create groups by type and delete in reverse',()=>{
 const wayA={type:'way',id:10,version:1,nodes:[1,2],tags:{highway:'residential'}};
 const rel={type:'relation',id:5,version:2,members:[{type:'way',ref:10,role:''},{type:'way',ref:11,role:''}],tags:{type:'route'}};
 assert.throws(()=>E.element({type:'relation',id:5,version:2,members:[{type:'way',ref:0,role:''}],tags:{}}));
 assert.throws(()=>E.element({type:'relation',id:5,version:2,members:Array.from({length:301},()=>({type:'way',ref:1,role:''})),tags:{}}));
 assert.throws(()=>E.validateDraft({base:null,value:{type:'relation',id:-1,members:[],tags:{}}}),/mindestens eine Eigenschaft/);
 const newWay={type:'way',id:-1,tags:{highway:'path'},nodes:[1,-2]};
 const newNode={type:'node',id:-2,tags:{},lat:48.001,lon:12.001};
 const newRel={type:'relation',id:-3,tags:{type:'route'},members:[{type:'way',ref:10,role:''},{type:'way',ref:-1,role:''}]};
 const xml=E.osc([{base:wayA,value:wayA},{base:null,value:newWay},{base:null,value:newNode},{base:null,value:newRel},{base:rel,deleted:true}]);
 const createBlock=xml.match(/<create>[\s\S]*?<\/create>/)[0];
 assert.ok(createBlock.indexOf('<node')<createBlock.indexOf('<way')&&createBlock.indexOf('<way')<createBlock.indexOf('<relation'));
 assert.match(xml,/<member type="way" ref="10" role=""\/>/);
 assert.match(xml,/<relation id="5" version="2"\/>/);
 assert.throws(()=>E.osc([{base:null,value:{type:'way',id:-1,tags:{highway:'path'},nodes:[1,-777]}}]),/unbekannt oder gelöscht/);
 assert.throws(()=>E.osc([{base:wayA,deleted:true},{base:rel,value:rel}]),/kann nicht gelöscht werden/);
 assert.throws(()=>E.osc([{base:null,value:{type:'node',id:-9,tags:{},lat:48,lon:12}}]),/braucht eine Eigenschaft/);
 assert.equal(E.checkReferences([{base:null,value:{type:'way',id:-1,tags:{highway:'path'},nodes:[1,-777]}}]).length,1);
});
test('per-type temporary IDs are independent negative counters',()=>{
 const drafts=[{base:null,value:{type:'node',id:-1,tags:{},lat:1,lon:1}},{base:null,value:{type:'way',id:-1,tags:{highway:'path'},nodes:[1,-1]}}];
 assert.equal(E.nextTempId(drafts,'node'),-2);
 assert.equal(E.nextTempId(drafts,'way'),-2);
 assert.equal(E.nextTempId(drafts,'relation'),-1);
});
test('geometry and member diffs are summarised in plain language for the change list',()=>{
 const way={type:'way',id:33,version:9,nodes:[1,2,3],tags:{}};
 assert.equal(E.geometryChange(way,{...way,nodes:[1,2,3,4]}).key,'_geometry');
 assert.equal(E.geometryChange(base,{...base,lat:49}).key,'_position');
 assert.equal(E.geometryChange(base,base),null);
 const rel={type:'relation',id:5,version:1,members:[{type:'way',ref:10,role:'outer'}],tags:{}};
 const added=E.memberChanges(rel,{...rel,members:[...rel.members,{type:'way',ref:11,role:'inner'}]});
 assert.ok(added.some(c=>c.after?.includes('way/11')));
 const roleChanged=E.memberChanges(rel,{...rel,members:[{type:'way',ref:10,role:'inner'}]});
 assert.ok(roleChanged.some(c=>c.after?.includes('Rolle')));
});
test('version check detects external edits and deleted objects without changing drafts',async()=>{
 const d={base,value:{...base,tags:{name:'Neu'}}},snapshot=JSON.stringify(d);
 assert.deepEqual(await E.checkVersions([d],async()=>({ok:true,json:async()=>({elements:[base]})})),[]);
 assert.match((await E.checkVersions([d],async()=>({ok:true,json:async()=>({elements:[{...base,version:4}]})})))[0],/aktuell 4/);
 assert.match((await E.checkVersions([d],async()=>({ok:false,status:410})))[0],/nicht mehr verfügbar/);
 await assert.rejects(E.checkVersions([d],async()=>({ok:false,status:429})),/429/);
 assert.equal(JSON.stringify(d),snapshot);
 assert.deepEqual(await E.checkVersions([{base:null,value:{...base,id:-1}}],()=>{throw Error('No network for creations');}),[]);
});

const near=(actual,expected)=>expected.forEach((v,i)=>assert.ok(Math.abs(actual[i]-v)<1e-9,`${actual} vs ${expected}`));
test('way outlines, click radius and distances are derived without touching the network',()=>{
 const shape=E.wayShape([{type:'node',id:1,lon:12,lat:48},{type:'node',id:2,lon:12.002,lat:48},{type:'node',id:3,lon:12.002,lat:48.001},{type:'node',id:4,lon:12,lat:48.001},{type:'way',id:9,nodes:[1,2,3,4,1]}],9);
 assert.equal(shape.closed,true);assert.deepEqual(shape.bounds,[[12,48],[12.002,48.001]]);near(shape.center,[12.001,48.0005]);
 assert.equal(E.wayShape([{type:'way',id:9,nodes:[1,2]}],9),null);
 assert.ok(E.pickRadiusMeters(48,10)>E.pickRadiusMeters(48,19));assert.equal(E.pickRadiusMeters(48,30),6);assert.equal(E.pickRadiusMeters(0,0),500);
 assert.equal(E.formatDistance(12.4),'12 m');assert.equal(E.formatDistance(1530),'1,5 km');assert.equal(E.formatDistance(undefined),'');
});
test('resolveLiveCandidates hit-tests real way geometry directly against OSM, bypassing the local POI index',async()=>{
 const fetcher=async url=>{
  assert.match(url,/map\.json\?bbox=/);
  return {ok:true,json:async()=>({elements:[
   {type:'node',id:1,lon:12,lat:48,tags:{}},
   {type:'node',id:2,lon:12.001,lat:48,tags:{}},
   {type:'node',id:5,lon:12.0002,lat:48.0001,tags:{name:'Alte Schmiede',craft:'blacksmith'}},
   {type:'way',id:9,nodes:[1,2],tags:{highway:'residential'}},
  ]})};
 };
 const found=await E.resolveLiveCandidates(48,12.0002,50,fetcher);
 const way=found.find(c=>c.type==='way'&&c.id===9);
 assert.ok(way,'a plain residential way is found even though the local POI index excludes untagged roads');
 assert.ok(way.distance<5,`expected the click to sit almost on the segment, got ${way.distance}`);
 const node=found.find(c=>c.type==='node'&&c.id===5);
 assert.ok(node);assert.equal(node.category,'blacksmith');assert.equal(node.label,'Alte Schmiede');
 assert.equal(found.some(c=>c.type==='node'&&(c.id===1||c.id===2)),false,'bare untagged way vertices are not offered as their own selectable object');
 assert.ok(found[0].distance<=found[1].distance,'results are sorted nearest first');
});
test('a stored map position survives backups but never reaches the export',()=>{
 const way={type:'way',id:33,version:9,nodes:[1,2,3],tags:{building:'yes'}};
 const draft={base:way,value:{...way,tags:{building:'house'}},center:[12.5,48.5]};
 assert.deepEqual(E.restore(JSON.stringify({version:1,drafts:[draft]}))[0].center,[12.5,48.5]);
 assert.equal(E.validateDraft({...draft,center:[500,0]}).center,undefined);
 assert.ok(!E.osc([draft]).includes('12.5'));
});

class Element{
 constructor(tag='div'){
  const classes=new Set();
  Object.assign(this,{tag,children:[],value:'',listeners:{},attrs:{},hidden:false,disabled:false,className:'',textContent:'',innerHTML:'',style:{}});
  this.classList={add:c=>classes.add(c),remove:c=>classes.delete(c),contains:c=>classes.has(c),toggle:(c,on)=>{(on===undefined?!classes.has(c):on)?classes.add(c):classes.delete(c);}};
 }
 append(...es){for(const e of es){e.parent=this;this.children.push(e);}}
 replaceChildren(...es){this.children=[];this.append(...es);}
 remove(){if(this.parent)this.parent.children=this.parent.children.filter(e=>e!==this);}
 addEventListener(e,f){this.listeners[e]=f;}
 setAttribute(k,v){this.attrs[k]=String(v);}getAttribute(k){return this.attrs[k];}removeAttribute(k){delete this.attrs[k];}
 focus(){this.focused=true;}
 click(){if(!this.disabled)return this.listeners.click?.({preventDefault(){}});}
}
const walk=(node,found=[])=>{found.push(node);node.children.forEach(child=>walk(child,found));return found;};
const cafe={type:'node',id:42,version:3,lat:48,lon:12,tags:{name:'Alt',amenity:'cafe',website:'https://alt.example'}};
const poi=(id,label,category,distance,kind='node')=>({type:'Feature',properties:{osm_id:id,kind,label,category,distance_m:distance},geometry:{type:'Point',coordinates:[12,48]}});

function harness(initial=[],routes={}){
 const es=new Proxy({},{get:(target,id)=>target[id]??=new Element()}),keydowns=[],requests=[];
 global.document={getElementById:id=>es[id],createElement:tag=>new Element(tag),addEventListener:(type,f)=>{if(type==='keydown')keydowns.push(f);}};
 let stored=JSON.stringify({version:1,drafts:initial});
 global.localStorage={getItem:()=>stored,setItem:(_,v)=>stored=v};
 const sources={},layers={},canvas={style:{}};
 const map={sources,layers,canvas,isStyleLoaded:()=>true,getSource:id=>sources[id]?{setData:d=>sources[id].data=d}:null,addSource:(id,s)=>sources[id]={data:s.data},addLayer:l=>layers[l.id]=l,getLayer:id=>layers[id],getCanvas:()=>canvas,flyTo(o){this.flown=o;},fitBounds(b,o){this.fitted=[b,o];},getZoom:()=>17,getCenter:()=>({lng:12,lat:48}),
  on(type,fn){(this.handlers??={})[type]=fn;},
  project:coord=>({x:(Array.isArray(coord)?coord[0]:coord.lng)*100000,y:-(Array.isArray(coord)?coord[1]:coord.lat)*100000}),
  getBounds:()=>({getWest:()=>11.999,getSouth:()=>47.999,getEast:()=>12.001,getNorth:()=>48.001}),
  dragPan:{disabled:false,disable(){this.disabled=true;},enable(){this.disabled=false;}},
 };
 const originalFetch=global.fetch;
 global.fetch=async(url,options)=>{
  requests.push(String(url));
  const route=Object.entries(routes).find(([part])=>String(url).includes(part));
  if(!route)return {ok:true,json:async()=>({features:[]})};
  const value=typeof route[1]==='function'?route[1](String(url),options):route[1];
  return {ok:value.ok??true,status:value.status??200,json:async()=>value.body};
 };
 const editor=E.create(map);
 const field=key=>walk(es.osmFields).find(n=>n.attrs?.['data-key']===key);
 return {es,map,editor,requests,keydowns,restore:()=>{global.fetch=originalFetch;},
  drafts:()=>E.restore(stored),data:id=>sources[id]?.data,field,
  control:key=>field(key).children.find(n=>n.tag==='input'||n.className==='osm-choice'),
  type(key,value){const input=this.control(key);input.value=value;input.listeners.input();},
  preset:label=>walk(es.osmPresetChips).find(n=>n.tag==='button'&&n.children.some(c=>c.textContent===label))?.click(),
  rows:()=>es.osmTags.children.map(row=>[row.children[0].value,row.children[1].value]),
 };
}
const withHarness=(initial,routes,fn)=>async()=>{const h=harness(initial,routes);try{await fn(h);}finally{h.restore();}};

test('a new place is placed on the map, typed, saved, undone and redone without changing OSM',withHarness([],{},async h=>{
 const e=h.es;
 e.osmNew.click();assert.equal(h.editor.active,true);assert.equal(h.map.canvas.style.cursor,'crosshair');assert.match(e.osmPickText.textContent,/neuen Ort/);assert.equal(e.osmEditView.attrs['data-mode'],'place');
 await h.editor.mapClick({lngLat:{lat:48,lng:12}});
 assert.equal(h.editor.active,false);assert.equal(h.editor.tab,'edit');assert.equal(e.osmForm.hidden,false);assert.equal(e.osmPanelEdit.hidden,false);assert.equal(e.osmPanelFind.hidden,true);
 assert.equal(h.data('osm-drafts').features.length,1);assert.equal(h.data('osm-drafts').features[0].properties.selected,true);
 h.preset('Sitzbank');h.type('seats','3');e.osmSave.click();
 const [draft]=h.drafts();assert.equal(draft.value.tags.amenity,'bench');assert.equal(draft.value.tags.seats,'3');assert.deepEqual(draft.center,[12,48]);
 assert.equal(h.editor.tab,'find');assert.equal(e.osmEditorStatus.focused,true);assert.equal(e.osmDraftCount.textContent,'1');assert.equal(e.osmDraftCount.hidden,false);
 assert.match(e.osmDrafts.children[0].children[0].children[1].children[0].textContent,/Sitzbank/);
 e.osmUndo.click();assert.equal(h.drafts().length,0);assert.equal(h.data('osm-drafts').features.length,0);assert.equal(e.osmDraftCount.hidden,true);
 e.osmRedo.click();assert.equal(h.drafts().length,1);assert.equal(h.data('osm-drafts').features.length,1);
}));

test('guided new-place flow keeps own properties, replaces the previous type and explains unsaved state',withHarness([],{},async h=>{
 const e=h.es;
 assert.match(e.osmDraftSummary.textContent,/Noch keine/);assert.equal(e.osmCheck.disabled,true);
 h.editor.startAt({lat:48,lng:12});
 assert.equal(e.osmSave.disabled,true);assert.match(e.osmValidation.textContent,/Art/);assert.equal(e.osmFields.hidden,true);
 e.osmAddTag.click();const custom=e.osmTags.children[0];custom.children[0].value='operator';custom.children[0].listeners.input();custom.children[1].value='Stadt';custom.children[1].listeners.input();
 h.preset('Bushaltestelle');assert.deepEqual(h.rows().sort(),[['highway','bus_stop'],['operator','Stadt']]);
 assert.equal(e.osmFields.hidden,false);assert.ok(h.field('shelter'));assert.equal(h.field('seats'),undefined);
 assert.match(e.osmEditState.textContent,/Nicht gespeichert/);assert.equal(e.osmSave.disabled,false);assert.match(e.osmDiscard.textContent,/verwerfen/);
 walk(e.osmPresetChips).find(n=>n.textContent==='Ändern').click();h.preset('Café');
 assert.deepEqual(h.rows().sort(),[['amenity','cafe'],['operator','Stadt']]);
 assert.equal(e.osmExport.disabled,true);e.osmSave.click();
 assert.equal(h.drafts()[0].value.tags.amenity,'cafe');assert.equal(h.drafts()[0].value.tags.highway,undefined);assert.equal(e.osmExport.disabled,false);assert.match(e.osmDraftSummary.textContent,/1 Entwurf/);
}));

test('friendly fields, choices and raw tags stay in sync, including deletions and duplicates',withHarness([],{'api.openstreetmap.org/api/0.6/node/42':{body:{elements:[cafe]}}},async h=>{
 const e=h.es;
 await h.editor.load('node',42);
 assert.equal(h.editor.tab,'edit');assert.equal(e.osmPresetWrap.hidden,true);assert.equal(h.control('name').value,'Alt');assert.match(e.osmObjectMeta.textContent,/Version 3/);assert.equal(e.osmOsmLink.href,'https://www.openstreetmap.org/node/42');
 h.type('name','');assert.ok(!h.rows().some(([k])=>k==='name'));assert.ok(e.osmDiff.children.some(li=>li.textContent.includes('wird entfernt')));
 assert.equal(h.field('name').classList.contains('is-changed'),true);
 const site=e.osmTags.children.find(row=>row.children[0].value==='website');site.children[1].value='https://neu.example';site.children[1].listeners.input();
 assert.equal(h.control('website').value,'https://neu.example');
 const [yes]=h.control('wheelchair').children;yes.click();assert.ok(h.rows().some(([k,v])=>k==='wheelchair'&&v==='yes'));assert.equal(yes.attrs['aria-pressed'],'true');
 yes.click();assert.ok(!h.rows().some(([k])=>k==='wheelchair'));
 e.osmAddTag.click();const last=e.osmTags.children.at(-1);last.children[0].value='amenity';last.children[0].listeners.input();last.children[1].value='bar';last.children[1].listeners.input();
 assert.match(e.osmValidation.textContent,/Doppelter/);assert.equal(e.osmSave.disabled,true);
 last.children[2].click();assert.equal(e.osmSave.disabled,false);e.osmSave.click();
 assert.equal(h.drafts()[0].value.tags.name,undefined);assert.equal(h.drafts()[0].value.tags.website,'https://neu.example');assert.equal(h.drafts()[0].base.tags.name,'Alt');
}));

test('values are trimmed and typed hints warn without blocking the save',withHarness([],{'node/42':{body:{elements:[cafe]}}},async h=>{
 await h.editor.load('node',42);
 h.type('website','example.org');assert.equal(h.field('website').children.at(-1).hidden,false);assert.match(h.field('website').children.at(-1).textContent,/https/);assert.equal(h.es.osmSave.disabled,false);
 h.type('name','  Neuer Name  ');h.es.osmSave.click();assert.equal(h.drafts()[0].value.tags.name,'Neuer Name');
}));

test('network loading disables conflicting controls and restores them after failure',async()=>{
 const h=harness(),e=h.es;let reject;
 global.fetch=()=>new Promise((_,r)=>reject=r);
 try{
   e.osmType.value='node';e.osmID.value='42';const request=e.osmLoad.click();
   assert.equal(e.osmNew.disabled,true);assert.equal(e.osmFormFields.disabled,true);assert.equal(e.osmFormFields.attrs['aria-busy'],'true');assert.equal(e.osmDiscard.disabled,true);assert.equal(h.editor.active,false);
   reject(Error('Offline'));await request;
   assert.equal(e.osmNew.disabled,false);assert.equal(e.osmFormFields.disabled,false);assert.equal(e.osmFormFields.attrs['aria-busy'],'false');assert.match(e.osmEditorStatus.textContent,/Offline/);
 }finally{h.restore();}
});

test('discarding an unsaved point clears its preview and returns focus to the search',withHarness([],{},async h=>{
 const e=h.es;e.osmNew.click();await h.editor.mapClick({lngLat:{lat:48,lng:12}});
 assert.equal(h.data('osm-drafts').features.length,1);e.osmDiscard.click();
 assert.equal(h.data('osm-drafts').features.length,0);assert.equal(e.osmForm.hidden,true);assert.equal(e.osmEmpty.hidden,false);assert.equal(h.editor.tab,'find');assert.equal(e.osmSearch.focused,true);
}));

test('validation names invalid fields, updates renamed tag labels and restores focus after removal',withHarness([],{},async h=>{
 const e=h.es;h.editor.startAt({lat:48,lng:12});h.preset('Café');
 h.type('name','x'.repeat(256));assert.equal(h.control('name').attrs['aria-invalid'],'true');
 const row=e.osmTags.children.find(r=>r.children[0].value==='name');assert.equal(row.children[1].attrs['aria-invalid'],'true');assert.equal(row.children[1].attrs['aria-describedby'],'osmValidation');
 h.type('name','Bank');assert.equal(h.control('name').attrs['aria-invalid'],'false');
 const amenity=e.osmTags.children.find(r=>r.children[0].value==='amenity');
 amenity.children[0].value='operator';amenity.children[0].listeners.input();assert.equal(amenity.children[2].attrs['aria-label'],'Betreiber entfernen');
 amenity.children[2].click();e.osmAddTag.click();
 assert.equal(e.osmTags.children.length,2);
 const last=e.osmTags.children.at(-1);last.children[2].click();assert.equal(e.osmTags.children[0].children[0].focused,true);
}));

test('a context action starts an OSM draft at the selected point without replacing unsaved edits',withHarness([],{},async h=>{
 h.editor.startAt({lat:48.12,lng:12.34});assert.equal(h.data('osm-drafts').features[0].geometry.coordinates[1],48.12);
 h.preset('Café');h.type('name','Mein Ort');h.editor.startAt({lat:49,lng:13});
 assert.equal(h.control('name').value,'Mein Ort');assert.equal(h.data('osm-drafts').features[0].geometry.coordinates[1],48.12);assert.match(h.es.osmEditorStatus.textContent,/ungespeichert/);
}));

test('clicking the map while the edit view is open picks the nearest recorded place',withHarness([],{'api/v1/geo/pois':{body:{features:[poi(42,'Alt','cafe',6)]}},'node/42':{body:{elements:[cafe]}}},async h=>{
 const e=h.es,timers=[],setTimeoutOriginal=global.setTimeout;global.setTimeout=fn=>timers.push(fn);
 try{h.editor.enter();}finally{global.setTimeout=setTimeoutOriginal;}
 assert.equal(h.editor.active,true);assert.equal(h.map.canvas.style.cursor,'pointer');assert.equal(e.osmPickHint.attrs['data-active'],'true');
 await h.editor.mapClick({lngLat:{lat:48,lng:12}});
 const poiRequest=h.requests.find(url=>url.includes('geo/pois'));assert.match(poiRequest,/radius_m=\d+/);assert.match(poiRequest,/limit=10/);
 assert.equal(h.editor.tab,'edit');assert.equal(e.osmSelected.textContent,'Alt');assert.equal(h.editor.active,false);
 e.osmDiscard.click();assert.equal(h.editor.active,true);
 h.editor.leave();assert.equal(h.editor.active,false);assert.equal(h.map.canvas.style.cursor,'');
}));

test('a click without a recorded place offers to add one there',withHarness([],{},async h=>{
 const e=h.es,setTimeoutOriginal=global.setTimeout;global.setTimeout=()=>0;
 try{h.editor.enter();}finally{global.setTimeout=setTimeoutOriginal;}
 await h.editor.mapClick({lngLat:{lat:48.5,lng:12.5}});
 assert.equal(e.osmPickMiss.hidden,false);assert.equal(h.editor.tab,'find');
 e.osmPickPlace.click();assert.equal(e.osmPickMiss.hidden,true);assert.equal(h.editor.tab,'edit');assert.equal(h.data('osm-drafts').features[0].geometry.coordinates[1],48.5);
}));

test('picking a spot with several recorded places lets you choose instead of silently guessing',withHarness([],{'api/v1/geo/pois':{body:{features:[poi(42,'Café Alt','cafe',5),poi(43,'Bank','bench',7)]}}},async h=>{
 const e=h.es,setTimeoutOriginal=global.setTimeout;global.setTimeout=()=>0;
 try{h.editor.enter();}finally{global.setTimeout=setTimeoutOriginal;}
 await h.editor.mapClick({lngLat:{lat:48,lng:12}});
 assert.equal(h.editor.tab,'find');
 assert.equal(e.osmCandidates.children.length,2);
 assert.match(e.osmEditorStatus.textContent,/2 Objekte/);
}));

test('picking a spot the local index excludes still resolves the real way underneath via a direct OSM lookup',withHarness([],{
 'api/v1/geo/pois':{body:{features:[]}},
 'map.json':{body:{elements:[
  {type:'node',id:1,lon:12,lat:48},
  {type:'node',id:2,lon:12.001,lat:48},
  {type:'way',id:9,nodes:[1,2],tags:{highway:'residential'}},
 ]}},
 'way/9/full.json':{body:{elements:[
  {type:'node',id:1,version:1,lon:12,lat:48,tags:{}},
  {type:'node',id:2,version:1,lon:12.001,lat:48,tags:{}},
  {type:'way',id:9,version:1,nodes:[1,2],tags:{highway:'residential'}},
 ]}},
},async h=>{
 const e=h.es,setTimeoutOriginal=global.setTimeout;global.setTimeout=()=>0;
 try{h.editor.enter();}finally{global.setTimeout=setTimeoutOriginal;}
 await h.editor.mapClick({lngLat:{lat:48,lng:12.0002}});
 assert.equal(h.editor.tab,'edit');
 assert.match(e.osmObjectMeta.textContent,/OSM-ID 9/);
}));

test('searching by name falls back to a direct OSM lookup when the local index has nothing',withHarness([],{
 'api/v1/geo/pois':{body:{features:[]}},
 'map.json':{body:{elements:[{type:'node',id:1,lon:12,lat:48,tags:{name:'Alte Schmiede',craft:'blacksmith'}}]}},
},async h=>{
 const e=h.es;
 e.osmSearch.value='Schmiede';
 await e.osmSearchForm.listeners.submit({preventDefault(){}});
 assert.equal(e.osmCandidates.children.length,1);
 assert.ok(walk(e.osmCandidates).some(n=>n.textContent?.includes('Alte Schmiede')));
}));

test('search lists places with category, distance and a name that describes the action',withHarness([],{'api/v1/geo/pois':{body:{features:[poi(42,'Café Alt','cafe',120),poi(7,'Bank','bench',2300,'way')]}},'node/42':{body:{elements:[cafe]}}},async h=>{
 const e=h.es;e.osmSearch.value='Bank';await e.osmSearchForm.listeners.submit({preventDefault(){}});
 assert.match(h.requests[0],/q=Bank/);assert.match(h.requests[0],/radius_m=5000/);
 assert.equal(e.osmCandidates.children.length,2);assert.equal(e.osmCandidatesEmpty.hidden,true);
 const [first,second]=e.osmCandidates.children.map(li=>li.children[0]);
 assert.match(first.attrs['aria-label'],/Café Alt, .*120 m, bearbeiten/);assert.match(second.attrs['aria-label'],/Sitzbank · Weg \/ Fläche · 2,3 km/);
 assert.equal(h.data('osm-candidates').features.length,2);
 first.listeners.mouseenter();assert.equal(h.data('osm-candidates').features[0].properties.active,true);
 await first.click();assert.equal(h.editor.tab,'edit');assert.equal(e.osmSelected.textContent,'Alt');assert.equal(h.data('osm-candidates').features.length,0);
}));

test('ways load their outline, keep a stored centre and never change geometry',withHarness([],{'way/9/full.json':{body:{elements:[{type:'node',id:1,version:1,lon:12,lat:48,tags:{}},{type:'node',id:2,version:1,lon:12.002,lat:48,tags:{}},{type:'node',id:3,version:1,lon:12.002,lat:48.001,tags:{}},{type:'node',id:4,version:1,lon:12,lat:48.001,tags:{}},{type:'way',id:9,version:2,nodes:[1,2,3,4,1],tags:{building:'yes'}}]}}},async h=>{
 const e=h.es;await h.editor.load('way',9);
 assert.ok(h.requests.some(r=>/way\/9\/full\.json/.test(r)));assert.equal(h.data('osm-selection').features[0].geometry.type,'Polygon');assert.ok(h.map.fitted);
 assert.equal(e.osmSelected.textContent,'Gebäude');assert.ok(h.field('name'));
 h.type('name','Rathaus');e.osmSave.click();
 const [draft]=h.drafts();near(draft.center,[12.001,48.0005]);assert.deepEqual(draft.value.nodes,[1,2,3,4,1]);assert.equal(h.data('osm-drafts').features[0].geometry.type,'Point');
 assert.equal(e.osmDrafts.children[0].children[1].disabled,false);
}));

test('drawing a new way places its vertices as bare nodes and needs a type before it can be saved',withHarness([],{},async h=>{
 const e=h.es;
 await h.editor.startDrawing('line');
 assert.equal(h.editor.drawing,true);assert.equal(h.map.canvas.style.cursor,'crosshair');
 await h.editor.mapClick({lngLat:{lat:48,lng:12}});
 await h.editor.mapClick({lngLat:{lat:48.001,lng:12.001}});
 assert.equal(h.editor.drawPointCount,2);
 h.editor.finishDraw();
 assert.equal(h.editor.drawing,false);assert.equal(h.editor.tab,'edit');
 assert.equal(h.drafts().length,0);
 assert.match(e.osmValidation.textContent,/Art/);
 h.preset('Fußweg');
 e.osmSave.click();
 const ways=h.drafts().filter(d=>d.value.type==='way'),nodes=h.drafts().filter(d=>d.value.type==='node');
 assert.equal(ways.length,1);assert.equal(nodes.length,2);
 assert.deepEqual(ways[0].value.nodes,[nodes[0].value.id,nodes[1].value.id]);
 assert.equal(ways[0].value.tags.highway,'footway');
 assert.equal(Object.keys(nodes[0].value.tags).length,0);
 assert.doesNotThrow(()=>E.osc(h.drafts()));
}));

test('drawing snaps onto an already-recorded node instead of creating a duplicate',withHarness([],{'map.json':{body:{elements:[{type:'node',id:501,lon:12,lat:48}]}}},async h=>{
 await h.editor.startDrawing('line');
 await h.editor.mapClick({lngLat:{lat:48,lng:12}});
 await h.editor.mapClick({lngLat:{lat:48.001,lng:12.001}});
 h.editor.finishDraw();
 h.preset('Fußweg');
 h.es.osmSave.click();
 const ways=h.drafts().filter(d=>d.value.type==='way'),nodes=h.drafts().filter(d=>d.value.type==='node');
 assert.equal(ways[0].value.nodes[0],501);
 assert.equal(nodes.length,1);
 assert.equal(nodes[0].value.id<0,true);
}));

test('escape cancels an in-progress drawing and clears its preview layer',withHarness([],{},async h=>{
 const e=h.es;
 await h.editor.startDrawing('line');
 await h.editor.mapClick({lngLat:{lat:48,lng:12}});
 assert.equal(h.data('osm-draw').features.length,1);
 h.keydowns.forEach(f=>f({key:'Escape',preventDefault(){}}));
 assert.equal(h.editor.drawing,false);
 assert.equal(h.data('osm-draw').features.length,0);
 assert.match(e.osmEditorStatus.textContent,/abgebrochen/);
}));

test('dragging an existing way vertex moves the node without disturbing the way',withHarness([],{'way/9/full.json':{body:{elements:[
 {type:'node',id:1,version:5,lon:12,lat:48,tags:{}},
 {type:'node',id:2,version:3,lon:12.002,lat:48,tags:{}},
 {type:'node',id:3,version:1,lon:12.002,lat:48.001,tags:{}},
 {type:'way',id:9,version:2,nodes:[1,2,3],tags:{building:'yes'}},
]}}},async h=>{
 await h.editor.load('way',9);
 h.editor.toggleGeometryMode();
 assert.equal(h.editor.geometryMode,true);
 const hitPoint=h.map.project([12,48]);
 await h.editor.enter();
 h.editor.mapMouseDown({point:hitPoint,lngLat:{lng:12,lat:48},originalEvent:{}});
 assert.equal(h.map.dragPan.disabled,true);
 h.editor.mapMouseMove({point:hitPoint,lngLat:{lng:12.0007,lat:48.0007}});
 await h.editor.mapMouseUp();
 assert.equal(h.map.dragPan.disabled,false);
 assert.equal(h.drafts().length,0,'dragging stays uncommitted until Save');
 const line=h.data('osm-selection').features[0].geometry.coordinates;
 assert.ok(Math.abs(line[0][1]-48.0007)<1e-9,'preview outline follows the unsaved move');
 h.es.osmSave.click();
 const nodeDraft=h.drafts().find(d=>d.value.type==='node'&&d.value.id===1);
 assert.ok(nodeDraft);assert.equal(nodeDraft.base.version,5);
 assert.ok(Math.abs(nodeDraft.value.lat-48.0007)<1e-9);
 assert.equal(h.drafts().some(d=>d.value.type==='way'),false);

}));

test('splitting a way at an interior point keeps the base way and drafts a new segment',withHarness([],{'way/9/full.json':{body:{elements:[
 {type:'node',id:1,version:1,lon:12,lat:48,tags:{}},
 {type:'node',id:2,version:1,lon:12.001,lat:48,tags:{}},
 {type:'node',id:3,version:1,lon:12.002,lat:48,tags:{}},
 {type:'way',id:9,version:4,nodes:[1,2,3],tags:{highway:'residential'}},
]}}},async h=>{
 await h.editor.load('way',9);
 h.editor.startSplit();
 assert.equal(h.editor.splitMode,true);assert.equal(h.editor.geometryMode,true);
 const point=h.map.project([12.001,48]);
 await h.editor.enter();
 h.editor.mapMouseDown({point,lngLat:{lng:12.001,lat:48}});
 assert.equal(h.editor.splitMode,false);
 const ways=h.drafts().filter(d=>!d.deleted&&d.value.type==='way');
 assert.equal(ways.length,2);
 const first=ways.find(w=>w.base?.id===9),second=ways.find(w=>!w.base);
 assert.deepEqual(first.value.nodes,[1,2]);
 assert.deepEqual(second.value.nodes,[2,3]);
 assert.equal(second.value.tags.highway,'residential');
 assert.equal(second.value.id<0,true);
 assert.doesNotThrow(()=>E.osc(h.drafts()));
}));

test('splitting at an endpoint is rejected because both parts would be degenerate',withHarness([],{'way/9/full.json':{body:{elements:[
 {type:'node',id:1,version:1,lon:12,lat:48,tags:{}},
 {type:'node',id:2,version:1,lon:12.001,lat:48,tags:{}},
 {type:'way',id:9,version:4,nodes:[1,2],tags:{highway:'residential'}},
]}}},async h=>{
 await h.editor.load('way',9);
 h.editor.startSplit();
 const point=h.map.project([12,48]);
 await h.editor.enter();
 h.editor.mapMouseDown({point,lngLat:{lng:12,lat:48}});
 assert.equal(h.drafts().length,0);
 assert.match(h.es.osmEditorStatus.textContent,/inneren Punkt/);
}));

test('merging two ways with a shared endpoint unions their tags, keeps one and deletes the other',withHarness(
 [{base:{type:'way',id:20,version:2,nodes:[5,6],tags:{highway:'unclassified',surface:'asphalt'}},value:{type:'way',id:20,version:2,nodes:[5,6],tags:{highway:'unclassified',surface:'asphalt'}}}],
 {'way/9/full.json':{body:{elements:[
   {type:'node',id:2,version:1,lon:12.001,lat:48,tags:{}},
   {type:'node',id:5,version:1,lon:12.002,lat:48,tags:{}},
   {type:'way',id:9,version:4,nodes:[2,5],tags:{highway:'residential'}},
 ]}}},
 async h=>{
  const e=h.es;
  await h.editor.load('way',9);
  await h.editor.mergeWith('20');
  const ways=h.drafts().filter(d=>!d.deleted&&d.value.type==='way'),deleted=h.drafts().filter(d=>d.deleted);
  assert.equal(ways.length,1);assert.equal(deleted.length,1);
  assert.equal(deleted[0].base.id,20);
  assert.deepEqual(ways[0].value.nodes,[2,5,6]);
  assert.equal(ways[0].value.tags.highway,'residential');
  assert.equal(ways[0].value.tags.surface,'asphalt');
  assert.match(e.osmEditorStatus.textContent,/highway/);
  assert.doesNotThrow(()=>E.osc(h.drafts()));
  const deletedRow=e.osmDrafts.children.find(li=>li.className.includes('osm-draft-deleted'));
  assert.ok(deletedRow);assert.ok(walk(deletedRow).some(n=>n.textContent?.includes('wird gelöscht')));
  walk(deletedRow).find(n=>n.tag==='button').click();
  assert.equal(h.drafts().filter(d=>d.deleted).length,0);
 }
));

test('merging ways without a shared endpoint is refused',withHarness([],{'way/9/full.json':{body:{elements:[
 {type:'node',id:1,version:1,lon:12,lat:48,tags:{}},
 {type:'node',id:2,version:1,lon:12.001,lat:48,tags:{}},
 {type:'way',id:9,version:4,nodes:[1,2],tags:{highway:'residential'}},
]}},'way/30/full.json':{body:{elements:[
 {type:'node',id:40,version:1,lon:13,lat:49,tags:{}},
 {type:'node',id:41,version:1,lon:13.001,lat:49,tags:{}},
 {type:'way',id:30,version:1,nodes:[40,41],tags:{highway:'residential'}},
]}}},async h=>{
 await h.editor.load('way',9);
 await h.editor.mergeWith('30');
 assert.equal(h.drafts().length,0);
 assert.match(h.es.osmEditorStatus.textContent,/keinen Endpunkt/);
}));

test('creating a relation lets you add members from drafts and by ID, reorder and save',withHarness(
 [{base:null,value:{type:'way',id:-1,tags:{highway:'path'},nodes:[1,2]}}],{},
 async h=>{
  const e=h.es;
  h.editor.newRelation();
  assert.equal(h.editor.tab,'edit');
  assert.equal(e.osmRelationTools.hidden,false);assert.equal(e.osmPresetWrap.hidden,true);
  const option=[...e.osmMemberDraft.children].find(o=>o.value==='way/-1');
  assert.ok(option);
  e.osmMemberDraft.value='way/-1';e.osmMemberDraft.listeners.change();
  assert.equal(h.editor.members.length,1);assert.equal(h.editor.members[0].ref,-1);
  e.osmMemberType.value='way';e.osmMemberId.value='500';e.osmMemberRole.value='outer';
  e.osmMemberAdd.click();
  assert.equal(h.editor.members.length,2);
  const up=e.osmMembers.children[1].children[2];
  up.click();
  assert.equal(h.editor.members[0].ref,500);assert.equal(h.editor.members[1].ref,-1);
  h.control('type').children.find(b=>b.textContent==='Route').click();
  e.osmSave.click();
  const rel=h.drafts().find(d=>!d.deleted&&d.value.type==='relation');
  assert.ok(rel);assert.equal(rel.value.tags.type,'route');
  assert.deepEqual(rel.value.members.map(m=>m.ref),[500,-1]);
  assert.equal(h.editor.tab,'relations');
  assert.doesNotThrow(()=>E.osc(h.drafts()));
 }
));

test('loading an existing relation supports editing roles and removing members',withHarness([],{'relation/50/full.json':{body:{elements:[
 {type:'way',id:10,version:1,nodes:[1,2],tags:{}},
 {type:'way',id:11,version:1,nodes:[2,3],tags:{}},
 {type:'relation',id:50,version:3,members:[{type:'way',ref:10,role:'outer'},{type:'way',ref:11,role:'inner'}],tags:{type:'multipolygon'}},
]}}},async h=>{
 const e=h.es;
 await h.editor.loadRelation('50');
 assert.equal(h.editor.tab,'edit');
 assert.equal(h.editor.members.length,2);assert.equal(e.osmMembers.children.length,2);
 const removeButton=e.osmMembers.children[1].children[4];
 removeButton.click();
 assert.equal(h.editor.members.length,1);
 assert.match(e.osmDiff.children[0].textContent,/Mitglied/);
 e.osmSave.click();
 const rel=h.drafts().find(d=>!d.deleted&&d.value.type==='relation');
 assert.equal(rel.value.members.length,1);assert.equal(rel.base.id,50);
}));

test('opening a way shows which relations it already belongs to, with a shortcut to edit them',withHarness([],{
 'way/9/full.json':{body:{elements:[{type:'node',id:1,lon:12,lat:48},{type:'node',id:2,lon:12.001,lat:48},{type:'way',id:9,version:1,nodes:[1,2],tags:{highway:'residential'}}]}},
 'way/9/relations.json':{body:{elements:[{type:'relation',id:77,tags:{type:'route',name:'Wanderweg'},members:[{type:'way',ref:9,role:''}]}]}},
},async h=>{
 const e=h.es;
 await h.editor.load('way',9);
 await new Promise(resolve=>setTimeout(resolve,0));
 assert.equal(e.osmMemberships.hidden,false);
 assert.ok(walk(e.osmMemberships).some(n=>n.textContent?.includes('Wanderweg')));
}));

test('tabs follow the keyboard pattern and control panels',withHarness([],{},async h=>{
 const e=h.es;
 assert.equal(e.osmTabFind.attrs['aria-selected'],'true');assert.equal(e.osmTabEdit.attrs.tabindex,'-1');assert.equal(e.osmPanelDrafts.hidden,true);
 let prevented=false;e.osmTabFind.listeners.keydown({key:'ArrowRight',preventDefault:()=>prevented=true});
 assert.equal(prevented,true);assert.equal(e.osmTabEdit.attrs['aria-selected'],'true');assert.equal(e.osmTabEdit.focused,true);assert.equal(e.osmPanelEdit.hidden,false);assert.equal(e.osmPanelFind.hidden,true);
 e.osmTabEdit.listeners.keydown({key:'End',preventDefault(){}});assert.equal(h.editor.tab,'relations');
 e.osmTabRelations.listeners.keydown({key:'ArrowRight',preventDefault(){}});assert.equal(h.editor.tab,'find');
 e.osmTabFind.listeners.keydown({key:'Home',preventDefault(){}});assert.equal(h.editor.tab,'find');
 e.osmTabFind.listeners.keydown({key:'ArrowLeft',preventDefault(){}});assert.equal(h.editor.tab,'relations');
}));

test('the save shortcut works only with an open, valid form',withHarness([],{},async h=>{
 const e=h.es,press=()=>{let prevented=false;h.keydowns.forEach(f=>f({key:'s',ctrlKey:true,preventDefault:()=>prevented=true}));return prevented;};
 assert.equal(press(),false);
 h.editor.startAt({lat:48,lng:12});assert.equal(press(),true);assert.equal(h.drafts().length,0);
 h.preset('Sitzbank');assert.equal(press(),true);assert.equal(h.drafts().length,1);assert.equal(h.editor.tab,'find');
}));

test('saving without differences removes a draft instead of exporting an empty change',withHarness([{base:cafe,value:{...cafe,tags:{...cafe.tags,name:'Neu'}}}],{},async h=>{
 const e=h.es;e.osmDrafts.children[0].children[0].click();assert.equal(h.control('name').value,'Neu');
 h.type('name','Alt');e.osmSave.click();assert.equal(h.drafts().length,0);assert.match(e.osmEditorStatus.textContent,/nicht im Export/);
}));

test('the confidential toggle diverts save() to POST /api/v1/confidential-objects and never touches drafts',withHarness([],{'confidential-objects':(url,options)=>{
 const method=(options?.method||'GET').toUpperCase();
 if(method==='POST')return {status:201,body:{id:'co-1',...JSON.parse(options.body),created_at:'now',updated_at:'now'}};
 return {body:{objects:[]}};
}},async h=>{
 const e=h.es;
 h.editor.startAt({lat:48,lng:12});
 assert.equal(e.osmConfidentialWrap.hidden,false,'the toggle must be offered for a brand-new point');
 h.preset('Sitzbank');
 e.osmConfidentialToggle.checked=true;
 await e.osmSave.click();
 const posted=h.requests.filter(u=>u.includes('confidential-objects'));
 assert.ok(posted.length>=1,'expected a request to /api/v1/confidential-objects');
 assert.equal(h.drafts().length,0,'a confidential save must never create an OSM draft');
 assert.equal(E.osc(h.drafts()),'<?xml version="1.0" encoding="UTF-8"?>\n<osmChange version="0.6" generator="OSMmini">\n\n</osmChange>\n','the .osc export must stay empty');
 assert.match(e.osmEditorStatus.textContent,/nie Teil des OSM-Exports/);
 assert.equal(e.osmForm.hidden,true,'the edit form closes after a confidential save, like an ordinary save');
}));

test('an existing confidential object is offered for tag-only editing (PUT, not POST) and hides geometry tools',withHarness([],{'confidential-objects':(url,options)=>{
 const method=(options?.method||'GET').toUpperCase();
 if(method==='PUT')return {body:{id:'co-1',...JSON.parse(options.body),created_at:'t0',updated_at:'t1'}};
 if(method==='DELETE')return {body:{}};
 return {body:{objects:[{id:'co-1',geometry_type:'Point',coordinates:[12,48],tags:{note:'Zugangscode 1234'},created_at:'t0',updated_at:'t0'}]}};
}},async h=>{
 const e=h.es;
 await h.editor.enter();
 assert.equal(h.data('osm-confidential').features.length,1);
 assert.equal(e.osmConfidentialList.children.length,1);
 assert.equal(e.osmConfidentialEmpty.hidden,true);
 e.osmConfidentialList.children[0].children[0].click();
 assert.equal(h.editor.tab,'edit');
 assert.equal(e.osmConfidentialToggle.checked,true);assert.equal(e.osmConfidentialToggle.disabled,true);
 assert.equal(e.osmGeometryToggle.hidden,true,'geometry editing is not offered when re-editing a confidential object');
 // "note" isn't a known preset field, so it only surfaces in the raw tag
 // table (osmTags), not the friendly-field UI h.type() targets.
 const row=e.osmTags.children.find(r=>r.children[0].value==='note');
 assert.ok(row,'expected the existing "note" tag as a raw tag row');
 row.children[1].value='Zugangscode 5678';row.children[1].listeners.input();
 await e.osmSave.click();
 const putRequest=h.requests.find(u=>u.includes('confidential-objects/co-1'));
 assert.ok(putRequest,'expected a PUT to the object\'s own URL');
 assert.equal(h.drafts().length,0);
}));

test('deleting a confidential object from the management list issues DELETE and refreshes the layer',async()=>{
 let deleted=false;
 const routes={'confidential-objects':(url,options)=>{
  const method=(options?.method||'GET').toUpperCase();
  if(method==='DELETE'){deleted=true;return {status:204,body:{}};}
  return {body:{objects:deleted?[]:[{id:'co-1',geometry_type:'Point',coordinates:[12,48],tags:{name:'Löschteich'},created_at:'t0',updated_at:'t0'}]}};
 }};
 const h=harness([],routes),e=h.es;
 const originalConfirm=global.confirm;global.confirm=()=>true;
 try{
  await h.editor.enter();
  assert.equal(h.data('osm-confidential').features.length,1);
  const row=e.osmConfidentialList.children[0];
  await row.children[2].click();
  assert.ok(h.requests.some(u=>u.includes('confidential-objects/co-1')));
  assert.equal(h.data('osm-confidential').features.length,0);
  assert.equal(e.osmConfidentialEmpty.hidden,false);
 }finally{global.confirm=originalConfirm;h.restore();}
});

test('layers wait for a loading style and redraw once it is ready',async()=>{
 const h=harness(),timers=[],original=global.setTimeout;let loaded=false;
 const add=h.map.addSource;h.map.addSource=(id,source)=>{if(!loaded)throw Error('Style is not done loading');return add(id,source);};
 h.map.getLayer=id=>h.map.layers[id];
 for(const id of Object.keys(h.map.sources))delete h.map.sources[id];
 global.setTimeout=fn=>timers.push(fn);
 try{
  h.editor.startAt({lat:48,lng:12});
  assert.equal(h.data('osm-drafts'),undefined);assert.equal(timers.length,1);
  loaded=true;timers.shift()();
  assert.equal(h.data('osm-drafts').features.length,1);assert.ok(h.map.layers['osm-drafts-points']);assert.ok(h.map.layers['osm-candidates-points']);
  h.es.osmDiscard.click();assert.equal(h.data('osm-drafts').features.length,0);assert.equal(timers.length,0);
 }finally{global.setTimeout=original;}
 assert.equal(h.es.osmEditView.attrs['data-tab'],'find');
});

const geometryElements=[
 {type:'node',id:1,version:5,lon:12,lat:48,tags:{}},
 {type:'node',id:2,version:3,lon:12.002,lat:48,tags:{}},
 {type:'node',id:3,version:1,lon:12.002,lat:48.002,tags:{}},
 {type:'node',id:4,version:2,lon:12,lat:48.002,tags:{}},
 {type:'way',id:9,version:2,nodes:[1,2,3,4,1],tags:{building:'yes'}},
];
const geometryRoutes={'way/9/full.json':{body:{elements:geometryElements}},'map.json?bbox=':{body:{elements:geometryElements}}};

test('vertex edits preserve closure and block degenerate lines and areas',()=>{
 assert.deepEqual(E.editWayNodes([1,2,3,1],0,4),[1,4,2,3,1]);
 assert.deepEqual(E.editWayNodes([1,2,3,4,1],0),[2,3,4,2]);
 assert.deepEqual(E.editWayNodes([1,2,3],0),[2,3]);
 assert.throws(()=>E.editWayNodes([1,2],0),/mindestens zwei/);
 assert.throws(()=>E.editWayNodes([1,2,3,1],1),/mindestens drei/);
 assert.throws(()=>E.editWayNodes([1,2,3],0,2),/bereits verwendeter/);
});
test('loaded OSM geometry skips incomplete ways and picks inside buildings',()=>{
 const data=E.mapDataFeatures([...geometryElements,{type:'way',id:99,nodes:[1,999]}]);
 assert.equal(data.features.filter(f=>f.properties.osm_type==='way').length,1);
 assert.equal(data.features.find(f=>f.properties.osm_id===9).geometry.type,'Polygon');
 const hit=E.candidatesFromElements(geometryElements,48.001,12.001,10);
 assert.equal(hit[0].id,9);assert.equal(hit[0].distance,0);
});
test('inserted and removed way vertices save atomically, retain version and undo together',withHarness([],geometryRoutes,async h=>{
 await h.editor.load('way',9);h.editor.toggleGeometryMode();
 h.editor.insertVertex(0,[12.001,48]);
 assert.equal(h.es.osmEditState.attrs['data-state'],'dirty');
 assert.equal(h.drafts().length,0);
 assert.match(h.es.osmDiff.children.map(n=>n.textContent).join(' '),/Geometrie/);
 assert.equal(h.data('osm-selection').features[0].geometry.coordinates[0].length,6);
 h.es.osmSave.click();
 const saved=h.drafts(),way=saved.find(d=>d.value.type==='way'),node=saved.find(d=>d.value.type==='node');
 assert.equal(way.base.version,2);assert.equal(way.value.nodes[1],node.value.id);
 assert.match(E.osc(saved),/<create>[\s\S]*<node/);assert.match(E.osc(saved),/<modify>[\s\S]*<way/);
 h.es.osmUndo.click();assert.equal(h.drafts().length,0);
 h.es.osmRedo.click();assert.equal(h.drafts().length,2);
 await h.editor.load('way',9);h.editor.toggleGeometryMode();h.editor.selectVertex(node.value.id);h.editor.removeVertex();h.es.osmSave.click();
 assert.equal(h.drafts().length,0,'restoring the way drops its unused new untagged vertex');
}));
test('discard reverses pending moves and insertions without touching shared nodes',withHarness([],geometryRoutes,async h=>{
 await h.editor.load('way',9);h.editor.toggleGeometryMode();
 await h.editor.commitNodeMove(1,[12.0005,48.0005]);h.editor.insertVertex(1,[12.002,48.001]);
 assert.equal(h.drafts().length,0);h.es.osmDiscard.click();assert.equal(h.drafts().length,0);
 await h.editor.load('way',9);h.editor.toggleGeometryMode();h.editor.selectVertex(2);h.editor.removeVertex();h.es.osmSave.click();
 const saved=h.drafts();assert.equal(saved.length,1);assert.deepEqual(saved[0].value.nodes,[1,3,4,1]);assert.equal(saved.some(d=>d.deleted),false);
}));
test('node coordinate edits validate, stay unsaved until Save, and retain tags and version',withHarness([],{'node/42.json':{body:{elements:[cafe]}}},async h=>{
 await h.editor.load('node',42);h.editor.toggleGeometryMode();
 await h.editor.commitNodeMove(42,[181,48]);assert.match(h.es.osmEditorStatus.textContent,/Ungültige/);assert.equal(h.drafts().length,0);
 await h.editor.commitNodeMove(42,[12.1,48.1]);assert.equal(h.drafts().length,0);assert.equal(h.es.osmSave.disabled,false);
 h.es.osmSave.click();const d=h.drafts()[0];assert.equal(d.value.lon,12.1);assert.equal(d.base.version,3);assert.equal(d.value.tags.website,cafe.tags.website);
}));
test('OSM map data loads on demand, toggles visibility and leaves the base map untouched',withHarness([],geometryRoutes,async h=>{
 await h.editor.enter();await h.editor.loadMapData();
 assert.equal(h.data('osm-map-data').features.length,6);assert.match(h.es.osmMapDataStatus.textContent,/1 Wege/);
 h.es.osmMapDataToggle.click();assert.equal(h.data('osm-map-data').features.length,0);
 h.es.osmMapDataToggle.click();assert.equal(h.data('osm-map-data').features.length,6);
 h.editor.leave();assert.equal(h.data('osm-map-data').features.length,0);assert.equal(h.editor.active,false);
}));

test('geometry validation rejects coincident vertices, collapsed areas and bow-tie polygons',()=>{
 assert.throws(()=>E.validateWayCoordinates([[12,48],[12,48]],false),/verschiedene/);
 assert.throws(()=>E.validateWayCoordinates([[0,0],[1,1],[2,2],[0,0]],true),/Ausdehnung/);
 assert.throws(()=>E.validateWayCoordinates([[0,0],[1,1],[0,1],[1,0],[0,0]],true),/überschneidet/);
 assert.doesNotThrow(()=>E.validateWayCoordinates([[0,0],[1,0],[1,1],[0,1],[0,0]],true));
});
test('saved draft ways keep a visible outline and geometry edits upsert shared nodes once',withHarness([],geometryRoutes,async h=>{
 await h.editor.enter();await h.editor.load('way',9);h.editor.toggleGeometryMode();await h.editor.commitNodeMove(1,[11.9999,48]);h.es.osmSave.click();
 const initial=h.drafts();assert.equal(initial.length,1);assert.equal(initial[0].value.type,'node');
 await h.editor.load('way',9);h.editor.toggleGeometryMode();await h.editor.commitNodeMove(1,[11.9998,48]);h.editor.insertVertex(1,[12.002,48.001]);h.es.osmSave.click();
 const saved=h.drafts();assert.equal(saved.filter(d=>d.value.type==='node'&&d.value.id===1).length,1);
 assert.equal(saved.find(d=>d.value.id===1).base.version,5);
 assert.equal(h.data('osm-draft-geometries').features.length,2);
 assert.equal(h.data('osm-draft-geometries').features[0].geometry.coordinates[0].length,6);
}));
test('OSM map download refuses zoomed-out views and reports failures without replacing loaded data',withHarness([],geometryRoutes,async h=>{
 await h.editor.enter();await h.editor.loadMapData();const initial=JSON.stringify(h.data('osm-map-data'));
 h.map.getZoom=()=>10;const before=h.requests.length;await h.editor.loadMapData();
 assert.equal(h.requests.length,before);assert.match(h.es.osmEditorStatus.textContent,/Zoome/);assert.equal(JSON.stringify(h.data('osm-map-data')),initial);
}));
test('tool shortcuts finish a drawing but leave native text-input undo alone',withHarness([],{},async h=>{
 await h.editor.enter();
 h.keydowns.forEach(f=>f({key:'l',target:{tagName:'INPUT'},preventDefault(){throw Error('text input shortcut intercepted');}}));assert.equal(h.editor.drawing,false);
 h.keydowns.forEach(f=>f({key:'l',target:{tagName:'DIV'},preventDefault(){}}));assert.equal(h.editor.drawing,true);
 await h.editor.mapClick({lngLat:{lng:12,lat:48}});await h.editor.mapClick({lngLat:{lng:12.001,lat:48.001}});
 h.keydowns.forEach(f=>f({key:'Enter',target:{tagName:'DIV'},preventDefault(){}}));assert.equal(h.editor.drawing,false);assert.equal(h.editor.tab,'edit');
 h.preset('Fußweg');h.es.osmSave.click();assert.equal(h.drafts().length,3);
 h.keydowns.forEach(f=>f({key:'z',ctrlKey:true,target:{tagName:'INPUT'},preventDefault(){throw Error('input undo intercepted');}}));assert.equal(h.drafts().length,3);
 h.keydowns.forEach(f=>f({key:'z',ctrlKey:true,target:{tagName:'DIV'},preventDefault(){}}));assert.equal(h.drafts().length,0);
}));

test('removing a drafted way removes only its unused new vertices and remains undoable',withHarness([],{},async h=>{
 await h.editor.startDrawing('line');await h.editor.mapClick({lngLat:{lng:12,lat:48}});await h.editor.mapClick({lngLat:{lng:12.001,lat:48.001}});h.editor.finishDraw();h.preset('Fußweg');h.es.osmSave.click();
 assert.equal(h.es.osmDrafts.children.length,1);assert.equal(h.es.osmDraftVertices.children.length,2);assert.equal(h.es.osmDraftCount.textContent,'1');
 h.editor.removeDraft(h.drafts().findIndex(d=>d.value.type==='way'));assert.equal(h.drafts().length,0);
 h.es.osmUndo.click();assert.equal(h.drafts().length,3);assert.doesNotThrow(()=>E.osc(h.drafts()));
}));
test('clicking a new local way opens its draft without sending its negative ID to OSM',withHarness([],{},async h=>{
 await h.editor.startDrawing('line');await h.editor.mapClick({lngLat:{lng:12,lat:48}});await h.editor.mapClick({lngLat:{lng:12.001,lat:48.001}});h.editor.finishDraw();h.preset('Fußweg');h.es.osmSave.click();
 await h.editor.enter();const before=h.requests.length;await h.editor.mapClick({lngLat:{lng:12.0005,lat:48.0005}});
 assert.equal(h.editor.tab,'edit');assert.match(h.es.osmSelected.textContent,/Fußweg/);assert.equal(h.requests.length,before);
}));
test('a deletion draft requires successful parent checks and keeps the original version',withHarness([],{'node/42.json':{body:{elements:[cafe]}},'node/42/ways.json':{body:{elements:[]}},'node/42/relations.json':{body:{elements:[]}}},async h=>{
 const originalConfirm=global.confirm;global.confirm=()=>true;
 try{
  await h.editor.load('node',42);await h.editor.deleteObject();
  const d=h.drafts()[0];assert.equal(d.deleted,true);assert.equal(d.base.version,3);assert.match(E.osc(h.drafts()),/<delete>[\s\S]*id="42" version="3"/);
  h.es.osmUndo.click();assert.equal(h.drafts().length,0);
 }finally{global.confirm=originalConfirm;}
}));
test('parent references block deletion and preserve the working object',withHarness([],{'node/42.json':{body:{elements:[cafe]}},'node/42/ways.json':{body:{elements:[{type:'way',id:9}]}}},async h=>{
 const originalConfirm=global.confirm;global.confirm=()=>true;
 try{await h.editor.load('node',42);await h.editor.deleteObject();assert.equal(h.drafts().length,0);assert.match(h.es.osmEditorStatus.textContent,/verwendet/);assert.equal(h.es.osmForm.hidden,false);}
 finally{global.confirm=originalConfirm;}
}));

test('a failed parent lookup cannot create a deletion draft',withHarness([],{'node/42.json':{body:{elements:[cafe]}},'node/42/ways.json':{ok:false,status:503,body:{}}},async h=>{
 const originalConfirm=global.confirm;global.confirm=()=>true;
 try{await h.editor.load('node',42);await h.editor.deleteObject();assert.equal(h.drafts().length,0);assert.match(h.es.osmEditorStatus.textContent,/nicht geprüft/);assert.equal(h.es.osmForm.hidden,false);}
 finally{global.confirm=originalConfirm;}
}));

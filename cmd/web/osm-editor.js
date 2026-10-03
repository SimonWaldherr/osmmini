/* Versioned OSM editing workspace (nodes, ways, relations), local history and OSC export. */
(function(root){
  'use strict';
  const validID=n=>Number.isSafeInteger(n)&&n>0;
  const refID=n=>Number.isSafeInteger(n)&&n!==0;
  const xmlText=s=>![...s].some(c=>{const n=c.codePointAt(0);return !(n===9||n===10||n===13||(n>=32&&n<=0xD7FF)||(n>=0xE000&&n<=0xFFFD)||(n>=0x10000&&n<=0x10FFFF));});
  const normLon=lng=>((lng+180)%360+360)%360-180;
  const TYPE_RANK={node:0,way:1,relation:2};
  const KIND_WORD={node:'Knoten',way:'Weg',relation:'Relation'};
  function tags(entries) {
    if(entries.length>200)throw Error('Maximal 200 Tags pro Objekt.');
    const out=Object.create(null);
    for(const [k,v] of entries){
      if(typeof k!=='string'||typeof v!=='string')throw Error('Tags müssen Text sein.');
      if(!k&&!v)continue;
      if(!k||[...k].length>255||[...v].length>255||!xmlText(k)||!xmlText(v))throw Error('Tags benötigen einen Schlüssel und gültigen Text mit maximal 255 Zeichen pro Feld.');
      if(Object.hasOwn(out,k))throw Error('Doppelter Tag-Schlüssel: '+k);
      out[k]=v;
    }
    return out;
  }
  // A relation member reference; ref may be a real OSM ID or a negative placeholder from this same batch.
  function member(raw){
    if(!raw||!Object.hasOwn(TYPE_RANK,raw.type)||!refID(raw.ref))throw Error('Ungültiges Relationsmitglied.');
    const role=raw.role||'';
    if(typeof role!=='string'||[...role].length>255||!xmlText(role))throw Error('Ungültige Rolle in einer Relation.');
    return {type:raw.type,ref:raw.ref,role};
  }
  function element(raw,create=false){
    if(!raw||!Object.hasOwn(TYPE_RANK,raw.type)||!(create?Number.isSafeInteger(raw.id)&&raw.id<0:validID(raw.id)&&validID(raw.version))||raw.visible===false)throw Error('Objekt ohne gültige ID/Version oder nicht mehr sichtbar.');
    if(raw.tags!==undefined&&(!raw.tags||typeof raw.tags!=='object'||Array.isArray(raw.tags)))throw Error('Ungültige Tags.');
    const e={type:raw.type,id:raw.id,...(!create?{version:raw.version}:{}),tags:tags(Object.entries(raw.tags||{}))};
    if(e.type==='node'){
      if(!Number.isFinite(raw.lon)||!Number.isFinite(raw.lat)||Math.abs(raw.lon)>180||Math.abs(raw.lat)>90)throw Error('Ungültige Punktkoordinaten.');
      e.lon=raw.lon;e.lat=raw.lat;
    }else if(e.type==='way'){
      if(!Array.isArray(raw.nodes)||raw.nodes.length<2||raw.nodes.length>2000||!raw.nodes.every(refID))throw Error('Unvollständige Wegreferenzen.');
      e.nodes=raw.nodes.slice();
    }else{
      if(!Array.isArray(raw.members))throw Error('Eine Relation benötigt eine Mitgliederliste.');
      if(raw.members.length>300)throw Error('Eine Relation darf höchstens 300 Mitglieder haben.');
      e.members=raw.members.map(member);
    }
    return e;
  }
  function changes(before,after){
    return [...new Set([...Object.keys(before),...Object.keys(after)])].sort().filter(k=>before[k]!==after[k]).map(key=>({key,before:before[key],after:after[key]}));
  }
  // Coarse, human-readable summary of a geometry change; used only for the on-screen diff, never for export.
  function geometryChange(base,value){
    if(!base)return null;
    if(base.type==='node')return base.lat===value.lat&&base.lon===value.lon?null:{key:'_position',before:`${base.lat.toFixed(5)}, ${base.lon.toFixed(5)}`,after:`${value.lat.toFixed(5)}, ${value.lon.toFixed(5)}`};
    if(base.type==='way')return JSON.stringify(base.nodes)===JSON.stringify(value.nodes)?null:{key:'_geometry',before:`${base.nodes.length} Punkte`,after:`${value.nodes.length} Punkte`};
    return null;
  }
  // Added/removed/renamed relation members, in plain language for the diff list.
  function memberChanges(base,value){
    const before=base?base.members:[],after=value.members,label=m=>`${m.type}/${m.ref}${m.role?' ('+m.role+')':''}`,key=m=>m.type+'/'+m.ref;
    const beforeKeys=before.map(key),afterKeys=after.map(key),out=[];
    for(const m of before)if(!afterKeys.includes(key(m)))out.push({key:'_member',before:label(m),after:undefined});
    for(const m of after)if(!beforeKeys.includes(key(m)))out.push({key:'_member',before:undefined,after:label(m)});
    for(const m of after){
      const prior=before.find(b=>key(b)===key(m));
      if(prior&&prior.role!==m.role)out.push({key:'_member',before:`${key(m)} Rolle „${prior.role||'–'}“`,after:`${key(m)} Rolle „${m.role||'–'}“`});
    }
    if(!out.length&&JSON.stringify(beforeKeys)!==JSON.stringify(afterKeys))out.push({key:'_member',before:'Reihenfolge',after:'geändert'});
    return out;
  }
  // A map position kept next to a draft so ways can be found again; it never reaches the export.
  const viewCenter=raw=>Array.isArray(raw)&&raw.length===2&&raw.every(Number.isFinite)&&Math.abs(raw[0])<=180&&Math.abs(raw[1])<=90?[raw[0],raw[1]]:undefined;
  function validateDraft(d){
    if(d.deleted===true){
      if(!d.base)throw Error('Nur bereits gespeicherte Objekte können gelöscht werden.');
      return {base:element(d.base),deleted:true};
    }
    const base=d.base?element(d.base):null, value=element(d.value,!base), center=viewCenter(d.center);
    if(base&&(base.type!==value.type||base.id!==value.id||base.version!==value.version))throw Error('Typ, ID und Version eines bestehenden Objekts dürfen sich nicht ändern.');
    if(!base&&value.type!=='node'&&!Object.values(value.tags).some(v=>v.trim()))throw Error('Ein neuer Weg oder eine neue Relation benötigt mindestens eine Eigenschaft.');
    return {base,value,...(center?{center}:{})};
  }
  // Per-type negative placeholder IDs: OSC create IDs are independent counters per element type.
  function nextTempId(drafts,type){
    const ids=drafts.filter(d=>!d.deleted&&d.value.type===type&&d.value.id<0).map(d=>d.value.id);
    return Math.min(0,...ids)-1;
  }
  // Cross-draft referential integrity: every way/relation reference must resolve, and nothing
  // still in use may be deleted. A brand-new, untagged node must be part of a way or relation —
  // otherwise it is a meaningless floating point rather than a way vertex.
  function collectIssues(validated){
    const issues=[],known=new Map();
    for(const d of validated){const e=d.deleted?d.base:d.value;known.set(e.type+'/'+e.id,!!d.deleted);}
    const exists=(type,id)=>known.has(type+'/'+id)?!known.get(type+'/'+id):id>0;
    for(const d of validated){
      if(d.deleted)continue;
      const e=d.value;
      if(e.type==='way')for(const ref of e.nodes)if(!exists('node',ref))issues.push(`Weg ${e.id}: Knoten ${ref} ist unbekannt oder gelöscht.`);
      if(e.type==='relation')for(const m of e.members)if(!exists(m.type,m.ref))issues.push(`Relation ${e.id}: Mitglied ${m.type}/${m.ref} ist unbekannt oder gelöscht.`);
    }
    for(const d of validated){
      if(!d.deleted)continue;
      const e=d.base;
      for(const other of validated){
        if(other===d||other.deleted)continue;
        const v=other.value;
        if(e.type==='node'&&v.type==='way'&&v.nodes.includes(e.id))issues.push(`Knoten ${e.id} kann nicht gelöscht werden: wird von Weg ${v.id} verwendet.`);
        if(v.type==='relation'&&v.members.some(m=>m.type===e.type&&m.ref===e.id))issues.push(`${KIND_WORD[e.type]} ${e.id} kann nicht gelöscht werden: wird von Relation ${v.id} verwendet.`);
      }
    }
    for(const d of validated){
      if(d.deleted||d.base||d.value.type!=='node'||Object.values(d.value.tags).some(v=>v.trim()))continue;
      const used=validated.some(other=>!other.deleted&&((other.value.type==='way'&&other.value.nodes.includes(d.value.id))||(other.value.type==='relation'&&other.value.members.some(m=>m.type==='node'&&m.ref===d.value.id))));
      if(!used)issues.push(`Neuer Punkt ${d.value.id} braucht eine Eigenschaft oder muss Teil eines Wegs sein.`);
    }
    return issues;
  }
  function checkReferences(drafts){return collectIssues(drafts.map(validateDraft));}
  // True when neither tags, geometry nor (for a relation) membership differ from the base.
  function elementUnchanged(base,value){
    if(changes(base.tags,value.tags).length)return false;
    return value.type==='relation'?!memberChanges(base,value).length:!geometryChange(base,value);
  }
  const esc=s=>String(s).replace(/[&<>"'\n\r\t]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&apos;','\n':'&#10;','\r':'&#13;','\t':'&#9;'}[c]));
  function osc(drafts){
    if(drafts.length>100)throw Error('Maximal 100 Entwürfe.');
    const validated=drafts.map(validateDraft),seen=new Set();
    for(const d of validated){const e=d.deleted?d.base:d.value,key=e.type+'/'+e.id;if(seen.has(key))throw Error('Objekt mehrfach vorhanden.');seen.add(key);}
    const issues=collectIssues(validated);
    if(issues.length)throw Error(issues.join(' '));
    const groups={create:[],modify:[],delete:[]};
    for(const d of validated){
      if(d.deleted){groups.delete.push({rank:TYPE_RANK[d.base.type],xml:`    <${d.base.type} id="${d.base.id}" version="${d.base.version}"/>`});continue;}
      const {base,value:e}=d;
      if(base&&elementUnchanged(base,e))continue;
      const attrs=`id="${e.id}"${base?` version="${e.version}"`:''}${e.type==='node'?` lat="${e.lat}" lon="${e.lon}"`:''}`;
      const children=(e.nodes||[]).map(id=>`      <nd ref="${id}"/>`)
        .concat((e.members||[]).map(m=>`      <member type="${m.type}" ref="${m.ref}" role="${esc(m.role)}"/>`))
        .concat(Object.keys(e.tags).sort().map(k=>`      <tag k="${esc(k)}" v="${esc(e.tags[k])}"/>`));
      groups[base?'modify':'create'].push({rank:TYPE_RANK[e.type],xml:`    <${e.type} ${attrs}>\n${children.join('\n')}\n    </${e.type}>`});
    }
    groups.create.sort((a,b)=>a.rank-b.rank);
    groups.delete.sort((a,b)=>b.rank-a.rank);
    const body=['create','modify','delete'].filter(g=>groups[g].length).map(g=>`  <${g}>\n${groups[g].map(x=>x.xml).join('\n')}\n  </${g}>`).join('\n');
    return '<?xml version="1.0" encoding="UTF-8"?>\n<osmChange version="0.6" generator="OSMmini">\n'+body+'\n</osmChange>\n';
  }
  function restore(raw){const d=JSON.parse(raw);if(d.version!==1||!Array.isArray(d.drafts)||d.drafts.length>100)throw Error('Ungültige Entwurfsdatei.');const drafts=d.drafts.map(validateDraft);osc(drafts);return drafts;}
  async function checkVersions(drafts, fetcher=fetch){
    const issues=[];
    for(const {base} of drafts){
      if(!base)continue;
      const response=await fetcher(`https://api.openstreetmap.org/api/0.6/${base.type}/${base.id}.json`,{signal:AbortSignal.timeout(15000),credentials:'omit'});
      if(response.status===404||response.status===410){issues.push(`${base.type}/${base.id}: nicht mehr verfügbar.`);continue;}
      if(!response.ok)throw Error(`Versionsprüfung fehlgeschlagen (HTTP ${response.status}).`);
      const body=await response.json(),live=element(body.elements?.find(e=>e.type===base.type&&e.id===base.id));
      if(live.version!==base.version)issues.push(`${base.type}/${base.id}: Basisversion ${base.version}, aktuell ${live.version}.`);
    }
    return issues;
  }

  // Full node elements (id, version, lat/lon, tags) from an API `/full` response, keyed by ID —
  // needed to move an existing way's vertices, since a plain lon/lat pair can't build a node draft.
  function nodeIndex(elements){
    const map=new Map();
    for(const e of elements||[]){if(e.type==='node')try{map.set(e.id,element(e));}catch{}}
    return map;
  }
  // Outline and centre of a way from an API `/full` response, used for the map highlight.
  function wayShape(elements,id){
    const nodes=new Map((elements||[]).filter(e=>e.type==='node'&&Number.isFinite(e.lon)&&Number.isFinite(e.lat)).map(n=>[n.id,[n.lon,n.lat]]));
    const way=(elements||[]).find(e=>e.type==='way'&&e.id===id);
    const coordinates=(way?.nodes||[]).map(ref=>nodes.get(ref)).filter(Boolean);
    if(!coordinates.length)return null;
    const lons=coordinates.map(c=>c[0]),lats=coordinates.map(c=>c[1]);
    const west=Math.min(...lons),east=Math.max(...lons),south=Math.min(...lats),north=Math.max(...lats);
    const first=coordinates[0],last=coordinates[coordinates.length-1];
    return {coordinates,closed:coordinates.length>3&&first[0]===last[0]&&first[1]===last[1],center:[(west+east)/2,(south+north)/2],bounds:[[west,south],[east,north]]};
  }
  // How many metres a fingertip-sized click target covers at this latitude and zoom.
  function pickRadiusMeters(lat,zoom,pixels=26){
    const metresPerPixel=156543.03392*Math.cos(lat*Math.PI/180)/Math.pow(2,zoom);
    return Math.max(6,Math.min(metresPerPixel*pixels,500));
  }
  const formatDistance=m=>!Number.isFinite(m)?'':m<1000?`${Math.max(1,Math.round(m))} m`:`${(m/1000).toFixed(1).replace('.',',')} km`;
  function metersBetween(lat1,lon1,lat2,lon2){
    const dLat=(lat2-lat1)*111320,dLon=(lon2-lon1)*111320*Math.cos((lat1+lat2)/2*Math.PI/180);
    return Math.hypot(dLat,dLon);
  }
  // Closest point on a line segment to (lat,lon), all in a flat local-metres approximation —
  // fine at the short distances a click radius or a bbox around one covers.
  function nearestPointOnSegment(lat,lon,aLat,aLon,bLat,bLon){
    const cos=Math.cos(lat*Math.PI/180)||1;
    const toXY=(la,lo)=>[(lo-lon)*111320*cos,(la-lat)*111320];
    const [ax,ay]=toXY(aLat,aLon),[bx,by]=toXY(bLat,bLon);
    const dx=bx-ax,dy=by-ay,lenSq=dx*dx+dy*dy;
    const t=lenSq>0?Math.max(0,Math.min(1,(-ax*dx-ay*dy)/lenSq)):0;
    const px=ax+t*dx,py=ay+t*dy;
    return {distance:Math.hypot(px,py),lon:lon+px/(111320*cos),lat:lat+py/111320};
  }
  // Live fallback for object selection: the local POI index only covers a curated set of tagged
  // categories and never relations (see isIndexablePOITags server-side), so a plain path or an
  // untagged building is otherwise unselectable. When a click or search finds nothing locally,
  // this asks OSM directly for a small bbox around the point — any tagged node, and any way at
  // all hit-tested against its real line geometry, not just a POI point.
  async function resolveLiveCandidates(lat,lon,radiusMeters,fetcher=fetch){
    const cos=Math.cos(lat*Math.PI/180)||1;
    const dLat=radiusMeters/111320,dLon=radiusMeters/(111320*cos);
    const west=normLon(lon-dLon),east=normLon(lon+dLon),south=Math.max(-90,lat-dLat),north=Math.min(90,lat+dLat);
    const response=await fetcher(`https://api.openstreetmap.org/api/0.6/map.json?bbox=${west},${south},${east},${north}`,{signal:AbortSignal.timeout(12000),credentials:'omit'});
    if(!response.ok)throw Error('Kartendaten von OSM konnten nicht geladen werden.');
    const body=await response.json();
    return candidatesFromElements(body.elements,lat,lon,radiusMeters);
  }
  function candidatesFromElements(elements,lat,lon,radiusMeters){
    const nodeCoords=new Map();
    for(const e of elements||[])if(e.type==='node'&&Number.isFinite(e.lon)&&Number.isFinite(e.lat))nodeCoords.set(e.id,[e.lon,e.lat]);
    const primaryCategory=tags=>root.OSMPresets?.primaryCategory(tags||{})||'';
    const out=[];
    for(const e of elements||[]){
      if(e.type==='node'&&e.tags&&Object.keys(e.tags).length){
        const d=metersBetween(lat,lon,e.lat,e.lon);
        if(d<=radiusMeters)out.push({type:'node',id:e.id,label:e.tags.name||'',category:primaryCategory(e.tags),distance:d,coordinates:[e.lon,e.lat]});
      }else if(e.type==='way'&&Array.isArray(e.nodes)&&e.nodes.length>1){
        let best=null;
        for(let i=0;i<e.nodes.length-1;i++){
          const a=nodeCoords.get(e.nodes[i]),b=nodeCoords.get(e.nodes[i+1]);
          if(!a||!b)continue;
          const hit=nearestPointOnSegment(lat,lon,a[1],a[0],b[1],b[0]);
          if(!best||hit.distance<best.distance)best=hit;
        }
        const ring=e.nodes.map(id=>nodeCoords.get(id));
        if(e.nodes.length>3&&e.nodes[0]===e.nodes[e.nodes.length-1]&&ring.every(Boolean)&&e.tags?.area!=='no'&&(!e.tags?.highway||e.tags?.area==='yes')){
          let inside=false;
          for(let i=0,j=ring.length-1;i<ring.length;j=i++){
            const a=ring[i],b=ring[j];
            if((a[1]>lat)!==(b[1]>lat)&&lon<(b[0]-a[0])*(lat-a[1])/(b[1]-a[1])+a[0])inside=!inside;
          }
          if(inside)best={distance:0,lon,lat};
        }
        if(best&&best.distance<=radiusMeters)out.push({type:'way',id:e.id,label:e.tags?.name||'',category:primaryCategory(e.tags),distance:best.distance,coordinates:[best.lon,best.lat]});
      }
    }
    return out.sort((a,b)=>a.distance-b.distance).slice(0,20);
  }

  function editWayNodes(nodes,index,id){
    if(!Array.isArray(nodes)||nodes.length<2||!Number.isInteger(index)||index<0||index>=nodes.length-1)throw Error('Ungültiger Wegabschnitt.');
    const closed=nodes.length>3&&nodes[0]===nodes[nodes.length-1];
    const next=nodes.slice();
    if(id!==undefined){
      if(!refID(id)||nodes.includes(id))throw Error('Ungültiger oder bereits verwendeter Knoten.');
      next.splice(index+1,0,id);
    }else{
      const ring=closed?nodes.slice(0,-1):next;
      ring.splice(index,1);
      if(new Set(ring).size<(closed?3:2))throw Error(closed?'Eine Fläche braucht mindestens drei verschiedene Punkte.':'Ein Weg braucht mindestens zwei verschiedene Punkte.');
      return closed?[...ring,ring[0]]:ring;
    }
    if(next.length>2000)throw Error('Maximal 2000 Wegpunkte.');
    return next;
  }
  function validateWayCoordinates(coordinates,closed){
    if(!Array.isArray(coordinates)||coordinates.some(p=>!Array.isArray(p)||p.length!==2||!p.every(Number.isFinite)))throw Error('Unvollständige Weggeometrie.');
    const points=closed?coordinates.slice(0,-1):coordinates;
    const minimum=closed?3:2;
    if(new Set(points.map(p=>p.join(','))).size<minimum)throw Error(`Mindestens ${minimum} verschiedene Positionen nötig.`);
    if(coordinates.some((p,i)=>i>0&&p[0]===coordinates[i-1][0]&&p[1]===coordinates[i-1][1]))throw Error('Aufeinanderfolgende Wegpunkte dürfen nicht an derselben Position liegen.');
    if(!closed)return;
    const cross=(a,b,c)=>(b[0]-a[0])*(c[1]-a[1])-(b[1]-a[1])*(c[0]-a[0]);
    const on=(a,b,c)=>Math.abs(cross(a,b,c))<1e-14&&c[0]>=Math.min(a[0],b[0])&&c[0]<=Math.max(a[0],b[0])&&c[1]>=Math.min(a[1],b[1])&&c[1]<=Math.max(a[1],b[1]);
    for(let i=0;i<coordinates.length-1;i++)for(let j=i+2;j<coordinates.length-1;j++){
      if(i===0&&j===coordinates.length-2)continue;
      const a=coordinates[i],b=coordinates[i+1],c=coordinates[j],d=coordinates[j+1];
      if((cross(a,b,c)*cross(a,b,d)<0&&cross(c,d,a)*cross(c,d,b)<0)||on(a,b,c)||on(a,b,d)||on(c,d,a)||on(c,d,b))throw Error('Die Fläche überschneidet sich selbst. Korrigiere die Wegpunkte.');
    }
    const area=points.reduce((sum,p,i)=>{const q=points[(i+1)%points.length];return sum+(p[0]-points[0][0])*(q[1]-points[0][1])-(q[0]-points[0][0])*(p[1]-points[0][1]);},0);
    if(Math.abs(area)<1e-14)throw Error('Die Fläche hat keine gültige Ausdehnung.');
  }
  function mapDataFeatures(elements){
    const nodes=new Map((elements||[]).filter(e=>e.type==='node'&&Number.isFinite(e.lon)&&Number.isFinite(e.lat)).map(e=>[e.id,e]));
    const features=[];
    for(const e of elements||[]){
      if(e.type==='relation')continue;
      const properties={osm_type:e.type,osm_id:e.id,name:e.tags?.name||'',category:root.OSMPresets?.primaryCategory(e.tags||{})||''};
      if(e.type==='node'&&nodes.has(e.id))features.push({type:'Feature',properties:{...properties,tagged:!!Object.keys(e.tags||{}).length},geometry:{type:'Point',coordinates:[e.lon,e.lat]}});
      if(e.type==='way'&&e.nodes?.length>=2&&e.nodes.every(id=>nodes.has(id))){
        const coordinates=e.nodes.map(id=>[nodes.get(id).lon,nodes.get(id).lat]);
        const area=e.nodes.length>3&&e.nodes[0]===e.nodes[e.nodes.length-1]&&e.tags?.area!=='no'&&(!e.tags?.highway||e.tags?.area==='yes')&&!e.tags?.barrier;
        features.push({type:'Feature',properties,geometry:area?{type:'Polygon',coordinates:[coordinates]}:{type:'LineString',coordinates}});
      }
    }
    return {type:'FeatureCollection',features};
  }

  const iconHTML=id=>`<svg class="ui-icon" aria-hidden="true" focusable="false" viewBox="0 0 24 24"><use href="#${id}"/></svg>`;
  const PROPS=new Set(['value','type','hidden','disabled','placeholder','rows','href','id','inputMode','maxLength','target','rel','className','innerHTML']);
  // Small element factory; text always goes through textContent so OSM data cannot inject markup.
  function h(tag,props={},...kids){
    const node=document.createElement(tag);
    for(const [name,value] of Object.entries(props)){
      if(value==null||value===false)continue;
      if(name==='text')node.textContent=value;
      else if(name==='class')node.className=value;
      else if(name==='on'){for(const [event,handler] of Object.entries(value))node.addEventListener(event,handler);}
      else if(PROPS.has(name))node[name]=value;
      else node.setAttribute(name,value===true?'':String(value));
    }
    node.append(...kids.filter(Boolean));
    return node;
  }

  function create(map,options={}){
    const P=root.OSMPresets;
    if(!P)throw Error('OSMPresets muss vor dem Editor geladen werden.');
    const el=id=>document.getElementById(id),status=el('osmEditorStatus'),key='osmmini.osm-drafts.v1';
    const say=text=>{status.textContent=text;};
    const TABS=[['find','osmTabFind','osmPanelFind'],['edit','osmTabEdit','osmPanelEdit'],['drafts','osmTabDrafts','osmPanelDrafts'],['relations','osmTabRelations','osmPanelRelations']];
    let drafts=[],current=null,working=[],members=[],dirty=false,busy=false,viewActive=false,placing=false,tab='find';
    let candidates=[],highlighted='',nearbyPoint=null,lastSearch=null,missPoint=null,presetOpen=false,customKind=false,renderedKind='',careMode='basic',presetSearchTerm='',presetAllOpen=false;
    let undo=[],redo=[],checkedFor='',conflicts=[],validForm=false;
    let geometryMode=false,dragNodeId=null,splitMode=false,insertMode=false,selectedVertexId=null;
    let mapElements=[],mapDataVisible=true;const dragPreview=new Map();
    let drawing=false,drawPoints=[],drawKind='line',drawSnapNodes=null,pendingDrawTags=null;
    const fieldNodes=new Map();
    try{const raw=localStorage.getItem(key);if(raw)drafts=restore(raw);}catch{say('Gespeicherte Entwürfe konnten nicht geladen werden.');}
    let saved=JSON.stringify(drafts);

    const versions=()=>JSON.stringify(drafts.filter(d=>d.base).map(d=>[d.base.type,d.base.id,d.base.version]).sort());
    // A deleted draft carries no `.value` — key it by `.base` instead, like every other identity check here.
    const keyOf=d=>{const e=d.deleted?d.base:d.value;return e.type+'/'+e.id;};
    const sameObject=(a,b)=>keyOf(a)===keyOf(b);
    const isCurrent=d=>!d.deleted&&!!current&&sameObject(d,current);
    const positionOf=d=>d.deleted?(d.base.type==='node'?[d.base.lon,d.base.lat]:null):d.center||(d.value.type==='node'?[d.value.lon,d.value.lat]:null);
    const kindName=type=>type==='node'?'Punkt':type==='relation'?'Relation':'Weg / Fläche';
    const fieldLabel=P.keyLabel;
    const label=e=>e.tags.name||P.presetForTags(e.tags)?.label||P.categoryLabel(P.primaryCategory(e.tags))||(e.type==='node'?'Unbenannter Ort':e.type==='relation'?'Unbenannte Relation':'Unbenannter Weg / Fläche');
    const iconFor=e=>P.presetForTags(e.tags)?.icon||P.categoryIcon(P.primaryCategory(e.tags));
    const trimmed=()=>working.map(([k,v])=>[k.trim(),v.trim()]);
    const valueOf=k=>working.find(row=>row[0]===k)?.[1]||'';
    function setValue(k,value){
      const index=working.findIndex(row=>row[0]===k);
      if(!value.trim()){if(index>=0)working.splice(index,1);}
      else if(index>=0)working[index][1]=value;
      else working.push([k,value]);
    }
    function tagsNow(){try{return tags(trimmed());}catch{return Object.fromEntries(trimmed().filter(([k,v])=>k&&v));}}
    function userError(error){
      if(error.name==='TimeoutError')return 'Die Antwort dauert zu lange. Bitte erneut versuchen.';
      if(error.name==='TypeError')return 'Verbindung fehlgeschlagen. Prüfe deine Internetverbindung und versuche es erneut.';
      if(error.name==='SyntaxError')return 'Die Datei oder Antwort konnte nicht gelesen werden. Verwende eine hier erstellte JSON-Sicherung oder lade den Ort erneut.';
      return error.message;
    }

    // ── Modes: the editor owns map clicks while the view is open on "Finden", while placing a
    // point, or while drawing a new way/area. Dragging a vertex is a separate, always-armed
    // mousedown/mousemove/mouseup path gated on geometryMode, since it needs raw pointer events
    // rather than a settled click.
    const mode=()=>!viewActive&& !drawing&&!placing?null:drawing?'draw':placing?'place':geometryMode?'geometry':viewActive&&tab==='find'&&!busy?'pick':null;
    function applyMode(){
      const m=mode();
      map.getCanvas().style.cursor=m==='place'||m==='draw'||m==='geometry'?'crosshair':m==='pick'?'pointer':'';
      el('osmNew').setAttribute('aria-pressed',String(placing));
      el('osmNew').textContent=placing?'Punktsetzen abbrechen':'Neuen Ort eintragen';
      el('osmPickText').textContent=placing?'Klicke auf die Karte, um den neuen Ort zu setzen. Abbrechen mit Escape.':'Wähle auf der Karte einen Ort aus, den du bearbeiten möchtest.';
      el('osmPickHint').setAttribute('data-active',String(!!m));
      el('osmEditView').setAttribute('data-mode',m||'');
      syncGeometryUI();
      for(const [id,active] of [['osmToolPoint',placing],['osmToolLine',drawing&&drawKind==='line'],['osmToolArea',drawing&&drawKind==='area']])el(id)?.setAttribute('aria-pressed',String(active));
      const drawBar=el('osmDrawBar');if(drawBar)drawBar.hidden=!drawing;
      const drawCount=el('osmDrawCount');if(drawCount)drawCount.textContent=String(drawPoints.length);
      const geomButton=el('osmGeometryToggle');if(geomButton)geomButton.setAttribute('aria-pressed',String(geometryMode));
    }
    function cancel(){
      const hadDraw=drawing&&drawPoints.length;
      placing=false;drawing=false;drawPoints=[];drawSnapNodes=null;
      pendingDrawTags=null;
      applyMode();
      if(hadDraw)render();
    }
    function setTab(name,focus=false){
      if(name!=='edit'&&geometryMode){geometryMode=false;insertMode=false;selectedVertexId=null;if(dragNodeId!=null)map.dragPan?.enable?.();dragNodeId=null;dragPreview.clear();}
      tab=name;
      for(const [id,buttonId,panelId] of TABS){
        const selected=id===name,button=el(buttonId);
        button.setAttribute('aria-selected',String(selected));
        button.setAttribute('tabindex',selected?'0':'-1');
        el(panelId).hidden=!selected;
        if(selected&&focus)button.focus?.();
      }
      if(name!=='find')el('osmPickMiss').hidden=true;
      el('osmEditView').setAttribute('data-tab',name);
      applyMode();
    }
    function setBusy(value){if(value)placing=false;busy=value;applyMode();syncUI();}

    // ── Drafts, persistence, undo/redo
    function persist(record=true){
      const next=JSON.stringify(drafts);
      if(record&&next!==saved){undo.push(saved);if(undo.length>30)undo.shift();redo=[];}
      saved=next;
      try{localStorage.setItem(key,JSON.stringify({version:1,drafts}));return '';}catch{return ' Lokale Speicherung fehlgeschlagen; bitte exportieren.';}
    }
    function canSwitch(){
      if(dirty){say('Du hast ungespeicherte Änderungen. Speichere sie oder verwirf sie im Reiter „Bearbeiten“.');setTab('edit');el('osmSelected').focus?.();return false;}
      return !busy;
    }
    function closeCurrent(){current=null;working=[];members=[];dirty=false;presetOpen=false;customKind=false;careMode='basic';presetSearchTerm='';presetAllOpen=false;geometryMode=false;dragNodeId=null;splitMode=false;insertMode=false;selectedVertexId=null;dragPreview.clear();renderForm();applyMode();render();syncUI();}

    // ── Analysis of the working tags: validity, diff, hints
    const display=(k,v)=>{
      const option=P.fields[k]?.options?.find(([raw])=>raw===v)?.[1]||Object.values(P.presets).find(p=>Object.keys(p.tags).length===1&&p.tags[k]===v)?.label;
      return JSON.stringify(option?`${option} (${v})`:v);
    };
    function analyze(){
      const list=el('osmDiff');list.replaceChildren();validForm=false;
      let message='';
      const counts=new Map();working.forEach(([k])=>counts.set(k.trim(),(counts.get(k.trim())||0)+1));
      const flags=working.map(([k,v])=>{let invalid=false;try{tags([[k.trim(),v.trim()]]);}catch{invalid=true;}return invalid||(k.trim()&&counts.get(k.trim())>1);});
      [...el('osmTags').children].forEach((row,i)=>{
        if(!working[i])return;
        const [keyInput,valueInput,remove]=row.children,name=fieldLabel(working[i][0].trim())||'Eigenschaft';
        keyInput.setAttribute('aria-invalid',String(flags[i]));valueInput.setAttribute('aria-invalid',String(flags[i]));
        valueInput.setAttribute('aria-label',name+' – Wert');remove.setAttribute('aria-label',name+' entfernen');
      });
      let delta=[];
      if(current){
        const isRelation=current.value.type==='relation';
        try{
          const next=tags(trimmed());
          const tagDelta=changes(current.base?.tags||{},next);
          const memberDelta=isRelation&&current.base?memberChanges(current.base,{...current.value,members}):[];
          const geometryDelta=current.base?geometryChange(current.base,current.value):null;
          const vertexDelta=(current.pendingNodes||[]).map(d=>d.base?geometryChange(d.base,d.value):{key:'_position',before:'Neuer Wegpunkt',after:`${d.value.lat.toFixed(5)}, ${d.value.lon.toFixed(5)}`}).filter(Boolean);
          delta=[...tagDelta,...memberDelta,...(geometryDelta?[geometryDelta]:[]),...vertexDelta];
          el('osmSelected').textContent=label({...current.value,tags:next,...(isRelation?{members}:{})});
          for(const d of delta){
            const item=document.createElement('li');
            item.textContent=d.key==='_position'||d.key==='_geometry'?`Geometrie: ${d.before} → ${d.after}`:d.key==='_member'?`Mitglied: ${d.before===undefined?'neu':d.before} → ${d.after===undefined?'entfernt':d.after}`:`${fieldLabel(d.key)}: ${d.before===undefined?'neu':display(d.key,d.before)} → ${d.after===undefined?'wird entfernt':display(d.key,d.after)}`;
            list.append(item);
          }
          if(!current.base&&!Object.values(next).some(v=>v.trim()))message=isRelation?'Ergänze mindestens eine Eigenschaft, z. B. die Art der Relation.':'Wähle eine Art aus oder ergänze eine Eigenschaft für den neuen Ort.';
          else validForm=!!delta.length||!!current.pendingNodes?.length||drafts.some(isCurrent);
        }catch(e){message=e.message;}
        if(!delta.length&&!message){const item=document.createElement('li');item.textContent='Noch nichts geändert.';list.append(item);}
      }
      const changed=new Set(delta.map(d=>d.key));
      for(const [k,node] of fieldNodes){
        const rowIndex=working.findIndex(row=>row[0].trim()===k),invalid=rowIndex>=0&&flags[rowIndex];
        if(node.field.type!=='choice')node.input.setAttribute('aria-invalid',String(!!invalid));
        node.wrap.classList.toggle('is-changed',changed.has(k));
        const hint=invalid?'':P.checkValue(node.field,valueOf(k));
        node.warn.textContent=hint;node.warn.hidden=!hint;
      }
      el('osmDiffSummary').textContent=delta.length?`Änderungen (${delta.length})`:'Änderungen';
      if(el('osmValidation').textContent!==message)el('osmValidation').textContent=message;
      const wantedKind=P.presetKeyForTags(tagsNow());
      if(current&&wantedKind!==renderedKind){renderPresets();renderFields();}
      renderMembers();
      syncUI();
    }

    // ── Form rendering
    function fieldNode(field){
      const id='osmField-'+field.key.replace(/[^a-z0-9]/gi,'-'),labelId=id+'-label',warnId=id+'-warn';
      const fieldHint=field.key==='access'&&P.presetKeyForTags(tagsNow())==='defibrillator'
        ?'Bei AEDs: „Öffentlich zugänglich“ wählen, wenn jede Person das Gerät erreichen kann. Zeitliche Einschränkungen zusätzlich über Öffnungszeiten beschreiben.'
        :field.hint;
      const wrap=h('div',{class:'osm-field','data-key':field.key});
      const warn=h('p',{id:warnId,class:'osm-field-warn',hidden:true});
      let input,set,control;
      if(field.type==='choice'){
        const options=[...field.options];
        const known=value=>options.some(([raw])=>raw===value);
        const buttons=new Map();
        control=h('div',{class:'osm-choice',role:'group','aria-labelledby':labelId,'aria-describedby':fieldHint?id+'-hint':undefined});
        const press=raw=>{setValue(field.key,valueOf(field.key)===raw?'':raw);dirty=true;set(valueOf(field.key));renderTable();analyze();};
        const add=(raw,text)=>{const button=h('button',{type:'button',class:'osm-choice-button',text,'aria-pressed':'false',on:{click:()=>press(raw)}});buttons.set(raw,button);control.append(button);};
        options.forEach(([raw,text])=>add(raw,text));
        set=value=>{
          if(value&&!known(value)&&!buttons.has(value))add(value,value);
          for(const [raw,button] of buttons)button.setAttribute('aria-pressed',String(raw===value));
        };
        input=control;
        wrap.append(h('span',{id:labelId,class:'osm-field-label',text:field.label}),control);
      }else{
        input=h('input',{id,type:field.type==='hours'?'text':field.type==='number'?'text':field.type,placeholder:field.placeholder||'',class:'setting-input','aria-describedby':warnId+(fieldHint?' '+id+'-hint':'')});
        if(field.type==='number')input.inputMode='numeric';
        if(field.type==='tel')input.setAttribute('autocomplete','off');
        input.addEventListener('input',()=>{setValue(field.key,input.value);dirty=true;renderTable();analyze();});
        set=value=>{if(input.value!==value)input.value=value;};
        control=input;
        wrap.append(h('label',{id:labelId,class:'osm-field-label','for':id,text:field.label}),control);
        if(field.suggestions){
          const chips=h('div',{class:'osm-suggestions','aria-label':field.label+' – Vorschläge',role:'group'});
          for(const [raw,text] of field.suggestions)chips.append(h('button',{type:'button',class:'osm-suggestion',text,on:{click:()=>{setValue(field.key,raw);dirty=true;set(raw);renderTable();analyze();input.focus?.();}}}));
          wrap.append(chips);
        }
      }
      if(fieldHint)wrap.append(h('p',{id:id+'-hint',class:'osm-field-hint',text:fieldHint}));
      wrap.append(warn);
      set(valueOf(field.key));
      fieldNodes.set(field.key,{field,input,wrap,warn,set});
      return wrap;
    }
    function defibrillatorSurveyPrompt(){
      if(current?.confidentialID)return null;
      return h('div',{class:'osm-special-guidance'},
        h('strong',{text:'Vor-Ort-Prüfung dokumentieren'}),
        h('p',{text:'Nur antippen, wenn du das Gerät und seinen Zugang selbst vor Ort geprüft hast. Osmmini ergänzt das heutige Prüfdatum und source=survey.'}),
        h('button',{type:'button',class:'btn btn-ghost',text:'Heute vor Ort geprüft',on:{click:()=>{
          const now=new Date(),today=`${now.getFullYear()}-${String(now.getMonth()+1).padStart(2,'0')}-${String(now.getDate()).padStart(2,'0')}`;
          setValue('check_date',today);setValue('source','survey');dirty=true;
          fieldNodes.get('check_date')?.set(today);fieldNodes.get('source')?.set('survey');
          renderTable();analyze();say(`Vor-Ort-Prüfung für heute (${today}) im Entwurf vermerkt.`);
        }}}));
    }
    function renderFields(){
      const box=el('osmFields');box.replaceChildren();fieldNodes.clear();
      if(current?.value.type==='relation'){
        box.hidden=false;box.append(fieldNode(P.fields.type));
        return;
      }
      const kind=P.presetKeyForTags(tagsNow());renderedKind=kind;
      const show=!!current&&(current.base||kind||customKind);
      box.hidden=!show;
      if(!show)return;
      const {fields,address}=P.fieldsFor(tagsNow());
      if(careMode==='basic'&&!current.base){
        const preset=P.presets[kind],basicKeys=new Set(preset?.basicFields||[]);
        const nameField=fields.find(field=>field.key==='name')||P.fields.name;
        box.append(fieldNode(nameField));
        for(const field of fields)if(basicKeys.has(field.key))box.append(fieldNode(field));
        const optional=fields.filter(field=>field.key!=='name'&&!basicKeys.has(field.key));
        if(optional.length||address.length){
          const more=h('details',{class:'osm-optional-fields'},h('summary',{text:`Weitere Angaben (${optional.length+address.length})`}));
          for(const field of optional)more.append(fieldNode(field));
          const addressDetails=h('details',{class:'osm-advanced osm-address'},h('summary',{text:'Adresse'}),...address.map(fieldNode));
          more.append(addressDetails);box.append(more);
        }
        if(kind==='fire_water_pond'&&current.value.type==='node'){
          box.append(h('div',{class:'osm-special-guidance'},h('strong',{text:'Löschteiche als Fläche erfassen'}),h('p',{text:'Zeichne den Umriss des Teichs auf der Karte. Art und bereits eingetragene Angaben werden übernommen.'}),h('button',{type:'button',class:'btn btn-ghost',text:'Teichfläche auf Karte zeichnen',on:{click:drawSelectedArea}})));
        }
        if(kind==='defibrillator'&&!current?.confidentialID)box.append(defibrillatorSurveyPrompt());
        return;
      }
      for(const field of fields)box.append(fieldNode(field));
      const details=h('details',{class:'osm-advanced osm-address'},h('summary',{text:'Adresse'}),...address.map(fieldNode));
      details.open=address.some(f=>valueOf(f.key));
      box.append(details);
      if(kind==='defibrillator'&&!current?.confidentialID)box.append(defibrillatorSurveyPrompt());
    }
    function setCareMode(mode,announce=true){
      if(!current)return;
      careMode=mode==='professional'?'professional':'basic';
      if(careMode==='professional')el('osmAdvancedTags').open=true;
      renderForm();
      if(announce)say(careMode==='professional'?'Professionelle Ansicht: alle Merkmale, OSM-Tags und passende Geometriewerkzeuge sind verfügbar.':'Einfache Ansicht: Art und Name reichen; weitere Angaben bleiben erhalten und sind optional.');
    }
    function renderPresets(focusFirst=false){
      const wrap=el('osmPresetChips');wrap.replaceChildren();let first=null;
      const kind=P.presetKeyForTags(tagsNow()),preset=P.presets[kind];
      el('osmPresetWrap').hidden=!current||!!current.base||current.value.type==='relation';
      if(!current||current.base||current.value.type==='relation')return;
      if(preset&&!presetOpen){
        wrap.append(h('div',{class:'osm-preset-current'},h('span',{class:'osm-preset-icon','aria-hidden':'true',text:preset.icon}),h('strong',{text:preset.label}),h('button',{type:'button',class:'btn btn-ghost',text:'Ändern','aria-label':'Art des Ortes ändern',on:{click:()=>{presetOpen=true;renderPresets(true);}}})));
        return;
      }
      const search=h('input',{id:'osmPresetSearch',type:'search',class:'setting-input osm-preset-search',value:presetSearchTerm,placeholder:'z. B. Café, Sitzbank, amenity=bench','aria-label':'Art des neuen Ortes suchen',autocomplete:'off'});
      const results=h('div',{id:'osmPresetResults',class:'osm-preset-results'});
      const updateResults=()=>{
        const query=search.value.trim().normalize('NFD').replace(/[\u0300-\u036f]/g,'').toLocaleLowerCase('de');
        presetSearchTerm=search.value;
        results.replaceChildren();
        const geometry=current.value.type==='way'?(current.shape?.closed?'area':'line'):'point';
        const all=Object.entries(P.presets).filter(([,p])=>!p.geometry||p.geometry===geometry);
        const searchable=([key,p])=>[key,p.label,p.group,...(p.synonyms||[]),...Object.entries(p.tags).flat()].join(' ').normalize('NFD').replace(/[\u0300-\u036f]/g,'').toLocaleLowerCase('de');
        const matches=query?all.filter(entry=>searchable(entry).includes(query)):all;
        const quickKeys=geometry==='area'?['building','house','garage','grass','playground','fire_station']:geometry==='line'?['footway','cycleway','path','residential','service']:['cafe','pharmacy','bus_stop','bench','toilets','drinking_water'];
        const shown=query||presetAllOpen?matches:all.filter(([key,p])=>quickKeys.includes(key)||p.group==='Feuerwehr & Erste Hilfe');
        if(query)results.append(h('p',{class:'osm-preset-picker-hint',text:`${matches.length} ${matches.length===1?'passende Art':'passende Arten'}`}));
        if(!matches.length){results.append(h('p',{class:'osm-empty',text:'Keine passende Art gefunden. Du kannst eigene OSM-Eigenschaften ergänzen.'}));return;}
        const groups=query||presetAllOpen?[...new Set(shown.map(([,p])=>p.group))]:['Häufige Einträge','Feuerwehr & Erste Hilfe'];
        for(const group of groups){
          const entries=shown.filter(([key,p])=>query||presetAllOpen?p.group===group:group==='Häufige Einträge'?quickKeys.includes(key):p.group===group);
          const grid=h('div',{class:'osm-preset-grid',role:'group','aria-label':group});
          for(const [k,p] of entries){const button=h('button',{type:'button',class:'osm-preset','aria-pressed':String(k===kind),on:{click:()=>applyPreset(k)}},h('span',{class:'osm-preset-icon','aria-hidden':'true',text:p.icon}),h('span',{text:p.label}));first??=button;grid.append(button);}
          results.append(h('div',{class:'osm-preset-group'},h('h4',{text:group}),grid));
        }
      };
      search.addEventListener('input',updateResults);
      wrap.append(h('label',{class:'osm-field-label',for:'osmPresetSearch',text:'Welche Art möchtest du eintragen?'}),search,results);
      if(!presetSearchTerm&&!presetAllOpen)wrap.append(h('button',{type:'button',class:'btn btn-ghost osm-preset-more',text:`Alle ${Object.keys(P.presets).length} Arten anzeigen`,on:{click:()=>{presetAllOpen=true;renderPresets(true);}}}));
      updateResults();
      if(focusFirst)search.focus?.();
      wrap.append(h('button',{type:'button',class:'btn btn-ghost osm-preset-other',text:'Andere Art – eigene Eigenschaften',on:{click:()=>{customKind=true;presetOpen=false;setCareMode('professional',false);el('osmAddTag').click();}}}));
    }
    function applyPreset(k){
      const preset=P.presets[k];
      if(!current||current.base||!preset){say('Wähle zuerst eine Art für den neuen Ort.');return;}
      const previous=P.presetForTags(tagsNow());
      if(previous)for(const name of Object.keys(previous.tags))setValue(name,'');
      for(const [name,value] of Object.entries(preset.tags))setValue(name,value);
      dirty=true;presetOpen=false;presetSearchTerm='';presetAllOpen=false;
      renderPresets();renderFields();renderTable();analyze();
      say(preset.label+' gewählt. Ergänze nur Angaben, die du sicher kennst, und speichere den Entwurf.');
      [...fieldNodes.values()][0]?.input.focus?.();
    }
    function rowNode(pair){
      const keyInput=h('input',{value:pair[0],placeholder:'Schlüssel','aria-label':'OSM-Schlüssel','aria-describedby':'osmValidation'});
      const valueInput=h('textarea',{value:pair[1],rows:1,placeholder:'Wert','aria-describedby':'osmValidation'});
      const remove=h('button',{type:'button',innerHTML:iconHTML('icon-close')});
      const div=h('div',{class:'osm-tag-row'},keyInput,valueInput,remove);
      const edited=()=>{dirty=true;for(const [name,node] of fieldNodes)node.set(valueOf(name));analyze();};
      keyInput.addEventListener('input',()=>{pair[0]=keyInput.value;edited();});
      valueInput.addEventListener('input',()=>{pair[1]=valueInput.value;edited();});
      remove.addEventListener('click',()=>{
        const index=working.indexOf(pair);working.splice(index,1);dirty=true;
        renderTable();for(const [name,node] of fieldNodes)node.set(valueOf(name));analyze();
        const next=el('osmTags').children[index]||el('osmTags').children[index-1];
        (next?.children[0]||el('osmAddTag')).focus?.();
      });
      return div;
    }
    function renderTable(){const box=el('osmTags');box.replaceChildren();working.forEach(pair=>box.append(rowNode(pair)));}
    function renderForm(){
      el('osmForm').hidden=!current;el('osmEmpty').hidden=!!current;
      if(!current){el('osmDuplicates').hidden=true;return;}
      const {value,base}=current;
      el('osmObjectMeta').textContent=base?`${kindName(value.type)} · OSM-ID ${value.id} · Version ${value.version}`:value.type==='node'?`Neuer Ort · ${value.lat.toFixed(5)}, ${value.lon.toFixed(5)}`:`Neuer ${kindName(value.type)}`;
      const modes=el('osmEditorModes');
      if(modes){modes.hidden=value.type==='relation'||!!current.confidentialID;el('osmModeBasic')?.setAttribute('aria-pressed',String(careMode==='basic'));el('osmModeProfessional')?.setAttribute('aria-pressed',String(careMode==='professional'));el('osmEditorModeHint').textContent=careMode==='professional'?'Alle Merkmale und OSM-Tags bearbeiten; Wege und Relationen fachlich prüfen.':'Art und Name reichen für den ersten Entwurf. Weitere Angaben bleiben optional.';}
      const advancedTags=el('osmAdvancedTags');if(advancedTags){advancedTags.hidden=careMode!=='professional';if(careMode==='professional')advancedTags.open=true;}
      const link=el('osmOsmLink');link.hidden=!base;if(base)link.href=`https://www.openstreetmap.org/${value.type}/${value.id}`;
      const geomToggle=el('osmGeometryToggle');
      if(geomToggle){geomToggle.hidden=value.type==='relation'||!!current.confidentialID;geomToggle.setAttribute('aria-pressed',String(geometryMode));geomToggle.textContent=geometryMode?'Punkte bearbeiten beenden':'Punkte bearbeiten';}
      const topology=el('osmTopologyTools');
      if(topology)topology.hidden=value.type!=='way'||!base;
      const relationTools=el('osmRelationTools');
      if(relationTools)relationTools.hidden=value.type!=='relation';
      const confWrap=el('osmConfidentialWrap');
      if(confWrap)confWrap.hidden=!!current.confidentialID?false:(!!base||value.type==='relation');
      renderPresets();renderFields();renderTable();renderMembers();
    }
    // A saved way draft never carries `shape` (it's transient, UI-only) — rebuild it from our
    // own node drafts when every referenced node is one we drew ourselves, so "auf Karte zeigen"
    // and the vertex layer still work after reopening from the Entwürfe list or a page reload.
    function localWayShape(value){
      const known=new Map(drafts.filter(d=>!d.deleted&&d.value.type==='node').map(d=>[d.value.id,d.value]));
      if(!value.nodes.every(id=>known.has(id)))return null;
      const elements=value.nodes.map(id=>({type:'node',id,lon:known.get(id).lon,lat:known.get(id).lat}));
      return wayShape([...elements,{type:'way',id:value.id,nodes:value.nodes}],value.id);
    }
    function open(d){
      current=structuredClone(d);dirty=false;working=Object.entries(current.value.tags);
      careMode=current.base||current.value.type!=='node'?'professional':'basic';
      presetSearchTerm='';presetAllOpen=false;
      const confToggle=el('osmConfidentialToggle');if(confToggle){confToggle.checked=false;confToggle.disabled=false;confToggle.setAttribute('aria-checked','false');}
      if(current.value.type==='way'&&!current.nodeElements)current.nodeElements=nodeIndex(mapElements);
      if(current.value.type==='way'&&!current.shape){const shape=localWayShape(current.value)||wayShape(mapElements,current.value.id);if(shape)current.shape=shape;}
      if(current.value.type==='way')current.shape={...current.shape,closed:current.value.nodes.length>3&&current.value.nodes[0]===current.value.nodes[current.value.nodes.length-1]};
      members=current.value.type==='relation'?current.value.members.map(m=>({...m})):[];
      presetOpen=!current.base&&current.value.type!=='relation'&&!P.presetKeyForTags(current.value.tags);customKind=false;
      el('osmDuplicates').hidden=true;el('osmDuplicates').replaceChildren();
      const memberships=el('osmMemberships');if(memberships){memberships.hidden=true;memberships.replaceChildren();}
      el('osmSelected').textContent=label(current.value);
      renderForm();analyze();setTab('edit');render();
      el('osmSelected').focus?.();
      if(current.base&&current.value.type!=='relation')loadMemberships(current.value.type,current.value.id);
    }

    // ── Relations: membership list, member editing, and read-only "part of" lookups
    function memberLabel(m){
      const draft=drafts.find(d=>!d.deleted&&d.value.type===m.type&&d.value.id===m.ref);
      const name=draft?label(draft.value):`${kindName(m.type)} ${m.ref}`;
      return m.role?`${name} · Rolle „${m.role}“`:name;
    }
    function addMember(type,ref,role){
      if(!current||current.value.type!=='relation')return;
      if(members.some(m=>m.type===type&&m.ref===ref)){say('Dieses Mitglied ist bereits in der Relation.');return;}
      members.push({type,ref,role});
      dirty=true;renderMembers();analyze();
      say('Mitglied hinzugefügt.');
    }
    function renderMembers(){
      const box=el('osmMembers');if(!box)return;
      box.replaceChildren();
      const show=!!current&&current.value.type==='relation';
      const empty=el('osmMembersEmpty');if(empty)empty.hidden=!show||!!members.length;
      if(!show)return;
      members.forEach((m,i)=>{
        const roleInput=h('input',{value:m.role,placeholder:'Rolle (optional)','aria-label':`Rolle für ${memberLabel(m)}`});
        roleInput.addEventListener('input',()=>{members[i]={...m,role:roleInput.value};dirty=true;analyze();});
        const up=h('button',{type:'button',class:'osm-icon-button',text:'↑',disabled:i===0,'aria-label':`${memberLabel(m)} nach oben verschieben`,on:{click:()=>{[members[i-1],members[i]]=[members[i],members[i-1]];dirty=true;renderMembers();analyze();}}});
        const down=h('button',{type:'button',class:'osm-icon-button',text:'↓',disabled:i===members.length-1,'aria-label':`${memberLabel(m)} nach unten verschieben`,on:{click:()=>{[members[i+1],members[i]]=[members[i],members[i+1]];dirty=true;renderMembers();analyze();}}});
        const remove=h('button',{type:'button',class:'osm-icon-button osm-danger',innerHTML:iconHTML('icon-close'),'aria-label':`${memberLabel(m)} entfernen`,on:{click:()=>{members.splice(i,1);dirty=true;renderMembers();analyze();}}});
        box.append(h('li',{class:'osm-member-row'},h('span',{class:'osm-member-label',text:memberLabel(m)}),roleInput,up,down,remove));
      });
      const picker=el('osmMemberDraft');
      if(picker){
        picker.replaceChildren(h('option',{value:'',text:'– auswählen –'}));
        for(const d of drafts){
          if(d.deleted||d.value.type==='relation'||isCurrent(d))continue;
          picker.append(h('option',{value:`${d.value.type}/${d.value.id}`,text:`${kindName(d.value.type)}: ${label(d.value)}`}));
        }
      }
    }
    // Read-only: which relations already contain this node/way, fetched live since the local
    // POI index deliberately excludes relations.
    async function loadMemberships(type,id){
      const box=el('osmMemberships');if(!box)return;
      try{
        const response=await fetch(`https://api.openstreetmap.org/api/0.6/${type}/${id}/relations.json`,{signal:AbortSignal.timeout(10000),credentials:'omit'});
        const stillOpen=()=>!!current&&!current.deleted&&current.value.type===type&&current.value.id===id;
        if(!response.ok||!stillOpen())return;
        const body=await response.json();
        const relations=(body.elements||[]).filter(e=>e.type==='relation');
        if(!relations.length||!stillOpen())return;
        box.replaceChildren(h('strong',{text:'Teil von Relationen:'}));
        for(const r of relations.slice(0,10)){
          const role=r.members?.find(m=>m.type===type&&m.ref===id)?.role||'';
          const name=r.tags?.name||`Relation ${r.id}`;
          box.append(h('button',{type:'button',class:'osm-dup','aria-label':`${name} bearbeiten`,on:{click:()=>{if(canSwitch())load('relation',r.id);}}},h('span',{text:name}),h('small',{text:role||r.tags?.type||''})));
        }
        box.hidden=false;
      }catch{/* the hint is optional */}
    }
    function newRelation(){
      if(!canSwitch())return;
      const id=nextTempId(drafts,'relation');
      const value=element({type:'relation',id,members:[],tags:{}},true);
      cancel();open({base:null,value});dirty=true;analyze();
      say('Neue Relation angelegt. Wähle die Art und ergänze Mitglieder.');
    }

    // ── Sync of buttons, badges and status cards with the current state
    function syncUI(){
      el('osmFormFields').disabled=busy;
      el('osmFormFields').setAttribute('aria-busy',String(busy));
      el('osmCandidates').setAttribute('aria-busy',String(busy));
      for(const id of ['osmNearby','osmNew','osmLoad','osmImport'])el(id).disabled=busy;
      el('osmNearby').textContent=busy?'Bitte warten …':'Suchen';
      el('osmSave').disabled=busy||!current||!validForm;
      el('osmDiscard').disabled=busy;
      if(el('osmDeleteObject')){el('osmDeleteObject').hidden=!current?.base||!!current?.confidentialID;el('osmDeleteObject').disabled=busy||dirty;}
      el('osmDiscard').textContent=dirty?'Änderungen verwerfen':'Schließen';
      el('osmEditState').textContent=dirty?'Nicht gespeichert':'Keine offenen Änderungen';
      el('osmEditState').setAttribute('data-state',dirty?'dirty':'clean');
      el('osmUndo').disabled=busy||dirty||!undo.length;
      el('osmRedo').disabled=busy||dirty||!redo.length;
      for(const [tool,original] of [['osmToolUndo','osmUndo'],['osmToolRedo','osmRedo']])if(el(tool))el(tool).disabled=el(original).disabled;
      for(const id of ['osmToolPoint','osmToolLine','osmToolArea','osmLoadMapData'])if(el(id))el(id).disabled=busy;
      syncGeometryUI();
      el('osmCheck').disabled=busy||dirty||!drafts.some(d=>d.base);
      for(const id of ['osmExport','osmBackup'])el(id).disabled=busy||dirty||!drafts.length;
      const mainCount=drafts.filter(d=>!supportingVertex(d)).length;
      const count=el('osmDraftCount');count.textContent=String(mainCount);count.hidden=!drafts.length;
      el('osmTabDrafts').setAttribute('aria-label',`Entwürfe, ${mainCount} Objekte im Arbeitsstand`);
      el('osmDraftSummary').textContent=drafts.length?`${mainCount} ${mainCount===1?'Entwurf':'Entwürfe'} · ${drafts.length} OSM-Objekte im Export.${dirty?' Offene Änderungen zuerst speichern oder verwerfen.':''}`:'Noch keine Entwürfe. Wähle einen Ort auf der Karte oder trage einen neuen ein.';
      el('osmCheckState').textContent=!drafts.length?'':checkedFor===versions()&&conflicts.length?'OSM wurde inzwischen geändert. Sichere den Arbeitsstand und löse die Konflikte in einem OSM-Editor.':!drafts.some(d=>d.base)?'Nur neue Orte. Bitte vor der Veröffentlichung prüfen, ob sie bereits in OSM existieren.':checkedFor===versions()?'Beim letzten Vergleich waren die OSM-Versionen aktuell. Vor der Veröffentlichung erneut prüfen.':'Noch nicht mit dem aktuellen OSM-Stand verglichen. Internetverbindung erforderlich.';
      const overview=el('osmDraftOverview');overview.replaceChildren();
      if(drafts.length){
        const outdated=checkedFor!==versions()&&drafts.some(d=>d.base);
        const state=conflicts.length&&!outdated?'warn':outdated?'pending':'ok';
        overview.setAttribute('data-state',state);
        const title=conflicts.length&&!outdated?`${conflicts.length} Versionswarnung${conflicts.length===1?'':'en'}`:outdated?'Vor dem Export mit OSM vergleichen':'Export bereit';
        const next=h('button',{type:'button',class:'btn btn-ghost',text:conflicts.length&&!outdated?'Warnung anzeigen':outdated?'Jetzt vergleichen':'Entwürfe ansehen',on:{click:()=>{
          if(conflicts.length&&!outdated){const issue=conflicts[0].match(/^(node|way)\/(\d+)/),draft=issue&&drafts.find(d=>!d.deleted&&d.value.type===issue[1]&&String(d.value.id)===issue[2]);if(draft&&canSwitch())open(draft);}
          else if(outdated)el('osmCheck').focus?.();
          else el('osmDrafts').scrollIntoView?.({block:'nearest',behavior:'smooth'});
        }}});
        overview.append(h('strong',{text:title}));
        if(state!=='ok')overview.append(next);
      }else overview.removeAttribute('data-state');
      if(checkedFor===versions()&&conflicts.length)el('osmExport').disabled=true;
    }
    function supportingVertex(d){
      return !d.deleted&&d.value.type==='node'&&!Object.keys(d.value.tags).length&&drafts.some(other=>!other.deleted&&other.value.type==='way'&&other.value.nodes.includes(d.value.id));
    }
    function removeDraft(index){
      if(!canSwitch())return;
      try{
        const removed=drafts[index],next=drafts.filter((_,i)=>i!==index);
        const vertexIds=new Set(!removed.deleted&&removed.value.type==='way'?removed.value.nodes:[]);
        for(let i=next.length-1;i>=0;i--){
          const n=next[i];if(n.base||n.deleted||n.value.type!=='node'||n.value.id>0||!vertexIds.has(n.value.id)||Object.keys(n.value.tags).length)continue;
          if(!next.some(d=>!d.deleted&&((d.value.type==='way'&&d.value.nodes.includes(n.value.id))||(d.value.type==='relation'&&d.value.members.some(m=>m.type==='node'&&m.ref===n.value.id)))))next.splice(i,1);
        }
        const issues=checkReferences(next);if(issues.length)throw Error(issues.join(' '));
        drafts=next;say('Entwurf entfernt. Mit „Rückgängig“ wiederherstellen.'+persist());refresh();el('osmDraftSummary').focus?.();
      }catch(e){say(userError(e));}
    }
    function draftNode(d,index){
      if(d.deleted){
        const name=label(d.base);
        const main=h('span',{class:'osm-draft-main'},h('span',{class:'osm-draft-icon','aria-hidden':'true',text:iconFor(d.base)}),h('span',{class:'osm-draft-text'},h('strong',{text:name}),h('small',{text:`${kindName(d.base.type)} · wird gelöscht`})));
        const undo=h('button',{type:'button',class:'osm-icon-button osm-danger','aria-label':`Löschung von ${name} verwerfen`,title:'Löschung verwerfen',innerHTML:iconHTML('icon-close'),on:{click:()=>{
          if(!canSwitch())return;
          drafts.splice(index,1);
          say('Löschung verworfen.'+persist());refresh();el('osmDraftSummary').focus?.();
        }}});
        return h('li',{class:'osm-draft osm-draft-deleted'},main,undo);
      }
      const changeCount=d.base?changes(d.base.tags,d.value.tags).length:0;
      const meta=`${kindName(d.value.type)} · ${d.base?`${changeCount} ${changeCount===1?'Änderung':'Änderungen'}`:'neu'}`;
      const name=label(d.value);
      const edit=h('button',{type:'button',class:'osm-draft-main','aria-label':`${name} bearbeiten`,on:{click:()=>{if(canSwitch()){cancel();open(d);flyToObject(d);}}}},h('span',{class:'osm-draft-icon','aria-hidden':'true',text:iconFor(d.value)}),h('span',{class:'osm-draft-text'},h('strong',{text:name}),h('small',{text:meta})));
      const locate=h('button',{type:'button',class:'osm-icon-button','aria-label':`${name} auf der Karte zeigen`,title:'Auf der Karte zeigen',innerHTML:iconHTML('icon-location'),on:{click:()=>flyToObject(d)}});
      locate.disabled=!positionOf(d);
      const remove=h('button',{type:'button',class:'osm-icon-button osm-danger','aria-label':`${name} aus dem Arbeitsstand entfernen`,title:'Entfernen',innerHTML:iconHTML('icon-close'),on:{click:()=>{
        removeDraft(index);
      }}});
      return h('li',{class:'osm-draft'},edit,locate,remove);
    }
    function refresh(){
      syncUI();
      const list=el('osmDrafts');list.replaceChildren();
      drafts.forEach((d,i)=>{if(!supportingVertex(d))list.append(draftNode(d,i));});
      const vertices=drafts.map((d,i)=>({d,i})).filter(({d})=>supportingVertex(d));
      const vertexList=el('osmDraftVertices');if(vertexList){vertexList.replaceChildren();for(const {d,i} of vertices)vertexList.append(draftNode(d,i));}
      if(el('osmDraftVertexDetails'))el('osmDraftVertexDetails').hidden=!vertices.length;
      if(el('osmDraftVertexSummary'))el('osmDraftVertexSummary').textContent=`${vertices.length} zugehörige Wegpunkte`;
      const relationsList=el('osmRelationsList');
      if(relationsList){
        relationsList.replaceChildren();
        drafts.forEach((d,i)=>{if((d.deleted?d.base:d.value).type==='relation')relationsList.append(draftNode(d,i));});
        const empty=el('osmRelationsEmpty');if(empty)empty.hidden=!!relationsList.children.length;
      }
      render();
    }

    // ── Candidate list and map layers
    function renderCandidates(){
      const list=el('osmCandidates');list.replaceChildren();
      for(const c of candidates){
        const category=P.categoryLabel(c.category),name=P.isRawLabel(c.label,c.category)?category||c.label||'Ort':c.label,distance=formatDistance(c.distance);
        const meta=[category&&category!==name?category:'',kindName(c.type),distance].filter(Boolean).join(' · ');
        const button=h('button',{type:'button',class:'osm-candidate','aria-label':`${name}, ${meta}, bearbeiten`,on:{click:()=>load(c.type,c.id),mouseenter:()=>highlight(c),mouseleave:()=>highlight(null),focus:()=>highlight(c),blur:()=>highlight(null)}},h('span',{class:'osm-draft-icon','aria-hidden':'true',text:P.categoryIcon(c.category)}),h('span',{class:'osm-draft-text'},h('strong',{text:name}),h('small',{text:meta})),h('span',{class:'osm-candidate-go','aria-hidden':'true',innerHTML:iconHTML('icon-chevron')}));
        list.append(h('li',{},button));
      }
      el('osmCandidatesEmpty').hidden=!!candidates.length;
      render();
    }
    function highlight(c){highlighted=c?c.type+'/'+c.id:'';render();}
    // The map throws while its style is still loading; render() then retries shortly instead of dropping the update.
    function put(id,data,layers){
      try{
        const source=map.getSource(id);
        if(source)source.setData(data);else map.addSource(id,{type:'geojson',data});
        for(const layer of layers)if(!map.getLayer(layer.id))map.addLayer({...layer,source:id});
        return true;
      }catch{return false;}
    }
    let retry=null,attempts=0;
    function rememberElements(elements){
      const known=new Map(mapElements.map(e=>[e.type+'/'+e.id,e]));
      for(const e of elements||[])known.set(e.type+'/'+e.id,e);
      mapElements=[...known.values()].slice(-50000);
    }
    function draftGeometries(){
      const known=nodeIndex(mapElements);
      for(const d of drafts)if(!d.deleted&&d.value.type==='node')known.set(d.value.id,d.value);
      const features=[];
      for(const d of drafts){
        const way=d.deleted?d.base:d.value;if(way.type!=='way'||isCurrent(d)||!way.nodes.every(id=>known.has(id)))continue;
        const coordinates=way.nodes.map(id=>[known.get(id).lon,known.get(id).lat]);
        const closed=way.nodes.length>3&&way.nodes[0]===way.nodes[way.nodes.length-1]&&way.tags.area!=='no'&&(!way.tags.highway||way.tags.area==='yes');
        features.push({type:'Feature',properties:{deleted:!!d.deleted},geometry:closed?{type:'Polygon',coordinates:[coordinates]}:{type:'LineString',coordinates}});
      }
      return {type:'FeatureCollection',features};
    }
    // microMap draws line layers from LineStrings; provide explicit polygon outlines
    // so both renderers show the same editing boundary without drawing it twice.
    function withOutlines(data){
      return {...data,features:data.features.flatMap(f=>f.geometry.type==='Polygon'?[f,...f.geometry.coordinates.map(ring=>({...f,geometry:{type:'LineString',coordinates:ring}}))]:[f])};
    }
    function render(){
      const point=(coordinates,properties)=>({type:'Feature',properties,geometry:{type:'Point',coordinates}});
      const collection=features=>({type:'FeatureCollection',features});
      // Rebuilt from live per-node positions (not the shape snapshot taken at open()) so a
      // dragged vertex immediately moves the outline too, not just its own point.
      const wayLine=current&&current.value.type==='way'?current.value.nodes.map((id,i)=>coordsOfNode(id)||current.shape?.coordinates?.[i]||null).filter(Boolean):null;
      let complete=true;
      complete&=put('osm-map-data',withOutlines(mapDataVisible&&viewActive?mapDataFeatures(mapElements):collection([])),[
        {id:'osm-map-data-fill',type:'fill',filter:['==','$type','Polygon'],paint:{'fill-color':'#64748b','fill-opacity':.12}},
        {id:'osm-map-data-line',type:'line',filter:['==','$type','LineString'],paint:{'line-color':'#64748b','line-width':2}},
        {id:'osm-map-data-nodes',type:'circle',filter:['==','$type','Point'],paint:{'circle-radius':['case',['get','tagged'],5,2.5],'circle-color':'#64748b','circle-stroke-color':'#fff','circle-stroke-width':1}},
      ]);
      complete&=put('osm-draft-geometries',withOutlines(viewActive?draftGeometries():collection([])),[
        {id:'osm-draft-geometries-fill',type:'fill',filter:['==','$type','Polygon'],paint:{'fill-color':['case',['get','deleted'],'#dc2626','#8b5cf6'],'fill-opacity':.15}},
        {id:'osm-draft-geometries-line',type:'line',filter:['==','$type','LineString'],paint:{'line-color':['case',['get','deleted'],'#dc2626','#8b5cf6'],'line-width':3}},
      ]);
      complete&=put('osm-selection',withOutlines(collection(wayLine&&wayLine.length>=2?[{type:'Feature',properties:{},geometry:current.shape?.closed?{type:'Polygon',coordinates:[wayLine]}:{type:'LineString',coordinates:wayLine}}]:[])),[
        {id:'osm-selection-fill',type:'fill',filter:['==','$type','Polygon'],paint:{'fill-color':'#2563eb','fill-opacity':.16}},
        {id:'osm-selection-line',type:'line',filter:['==','$type','LineString'],paint:{'line-color':'#2563eb','line-width':4}},
      ]);
      complete&=put('osm-candidates',collection(current?[]:candidates.filter(c=>c.coordinates).map(c=>point(c.coordinates,{active:highlighted===c.type+'/'+c.id}))),[
        {id:'osm-candidates-points',type:'circle',paint:{'circle-radius':['case',['get','active'],10,6],'circle-color':['case',['get','active'],'#2563eb','#60a5fa'],'circle-opacity':.9,'circle-stroke-width':2,'circle-stroke-color':'#fff'}},
      ]);
      const shown=drafts.filter(d=>!d.deleted&&!isCurrent(d));if(current)shown.push(current);
      complete&=put('osm-drafts',collection(shown.map(d=>({d,at:d.value.type==='node'?[d.value.lon,d.value.lat]:d.center})).filter(x=>x.at).map(x=>point(x.at,{selected:isCurrent(x.d)}))),[
        {id:'osm-drafts-points',type:'circle',paint:{'circle-radius':['case',['get','selected'],10,7],'circle-color':['case',['get','selected'],'#2563eb','#8b5cf6'],'circle-stroke-width':2,'circle-stroke-color':'#fff'}},
      ]);
      const vertices=geometryMode&&current?(current.value.type==='way'?[...new Set(current.value.nodes)]:current.value.type==='node'?[current.value.id]:[]):[];
      complete&=put('osm-vertices',collection(vertices.map(id=>coordsOfNode(id)).filter(Boolean).map((coord,i)=>point(coord,{dragging:vertices[i]===dragNodeId,selected:vertices[i]===selectedVertexId}))),[
        {id:'osm-vertices-points',type:'circle',paint:{'circle-radius':['case',['get','dragging'],9,7],'circle-color':['case',['get','selected'],'#2563eb','#f59e0b'],'circle-stroke-width':2,'circle-stroke-color':'#fff'}},
      ]);
      const drawLine=drawPoints.length>1?[{type:'Feature',properties:{},geometry:drawKind==='area'&&drawPoints.length>2?{type:'Polygon',coordinates:[closeRing(drawPoints.map(p=>[p.lon,p.lat]))]}:{type:'LineString',coordinates:drawPoints.map(p=>[p.lon,p.lat])}}]:[];
      const drawVertices=drawPoints.map(p=>point([p.lon,p.lat],{snapped:!!p.nodeId}));
      complete&=put('osm-draw',collection([...drawLine,...drawVertices]),[
        {id:'osm-draw-fill',type:'fill',filter:['==','$type','Polygon'],paint:{'fill-color':'#16a34a','fill-opacity':.16}},
        {id:'osm-draw-line',type:'line',filter:['==','$type','LineString'],paint:{'line-color':'#16a34a','line-width':3,'line-dasharray':[2,1]}},
        {id:'osm-draw-points',type:'circle',filter:['==','$type','Point'],paint:{'circle-radius':6,'circle-color':['case',['get','snapped'],'#16a34a','#ffffff'],'circle-stroke-width':2,'circle-stroke-color':'#16a34a'}},
      ]);
      // Confidential objects (cmd/confidential_objects.go): a visually distinct, dashed/lock
      // layer, entirely separate from every OSM-draft source above. See confidentialGeometry()
      // for why this never shares state with drafts/osc().
      complete&=put('osm-confidential',collection(confidentialObjects.map(o=>({type:'Feature',properties:{id:o.id},geometry:{type:o.geometry_type,coordinates:o.coordinates}}))),[
        {id:'osm-confidential-fill',type:'fill',filter:['==','$type','Polygon'],paint:{'fill-color':'#475569','fill-opacity':.14}},
        {id:'osm-confidential-line',type:'line',filter:['==','$type','LineString'],paint:{'line-color':'#475569','line-width':2.5,'line-dasharray':[3,2]}},
        {id:'osm-confidential-points',type:'circle',filter:['==','$type','Point'],paint:{'circle-radius':7,'circle-color':'#475569','circle-stroke-width':2,'circle-stroke-color':'#fff'}},
        {id:'osm-confidential-lock',type:'symbol',filter:['==','$type','Point'],layout:{'text-field':'🔒','text-size':11,'text-offset':[0,-1.3],'text-allow-overlap':true}},
      ]);
      if(complete)attempts=0;
      else if(!retry&&attempts++<40)retry=setTimeout(()=>{retry=null;render();},250);
      options.onChange?.();
    }
    const closeRing=coords=>coords.length>2?[...coords,coords[0]]:coords;
    // While a vertex is being dragged its live position overrides the saved/base coordinate.
    function coordsOfNode(id){
      if(dragPreview.has(id))return dragPreview.get(id);
      if(current?.value.type==='node'&&current.value.id===id)return [current.value.lon,current.value.lat];
      const draft=current?.pendingNodes?.find(d=>d.value.id===id)||drafts.find(d=>!d.deleted&&d.value.type==='node'&&d.value.id===id);
      if(draft)return [draft.value.lon,draft.value.lat];
      const base=current?.nodeElements?.get(id);
      return base?[base.lon,base.lat]:null;
    }
    function flyToObject(target){
      if(target.shape?.bounds&&map.fitBounds){map.fitBounds(target.shape.bounds,{maxZoom:19,padding:60});return;}
      const at=positionOf(target);
      if(at)map.flyTo({center:at,zoom:Math.max(map.getZoom?.()??0,18)});
    }

    // ── Geometry: dragging an existing way's vertices, and drawing a brand-new way/area.
    // Both are opt-in (a toggle button, or an explicit "draw" action) so ordinary tag editing
    // never risks an accidental geometry change.
    function toggleGeometryMode(){
      if(!current||current.value.type==='relation'||current.confidentialID||busy)return;
      if(dragNodeId!=null)map.dragPan?.enable?.();
      geometryMode=!geometryMode;insertMode=false;selectedVertexId=current.value.type==='node'?current.value.id:null;dragNodeId=null;dragPreview.clear();
      applyMode();render();
      const geomToggle=el('osmGeometryToggle');
      if(geomToggle){geomToggle.setAttribute('aria-pressed',String(geometryMode));geomToggle.textContent=geometryMode?'Punkte bearbeiten beenden':'Punkte bearbeiten';}
      say(geometryMode?'Geometrie bearbeiten: Punkte ziehen oder auswählen. Änderungen mit „Entwurf speichern“ übernehmen.':'Punkte bearbeiten beendet.');
    }
    function syncGeometryUI(){
      const box=el('osmGeometryTools');if(!box)return;
      box.hidden=!geometryMode||!current;
      const selected=selectedVertexId!=null?coordsOfNode(selectedVertexId):null;
      el('osmVertexStatus').textContent=selected?`Knoten ${selectedVertexId} ausgewählt`:'Wähle einen Wegpunkt auf der Karte.';
      for(const [id,value] of [['osmVertexLon',selected?.[0]],['osmVertexLat',selected?.[1]]]){el(id).disabled=!selected||busy;el(id).value=value==null?'':value.toFixed(7);}
      el('osmVertexApply').disabled=!selected||busy;
      el('osmVertexRemove').disabled=!selected||busy||current?.value.type!=='way';
      el('osmVertexInsert').hidden=current?.value.type!=='way';
      el('osmVertexInsert').setAttribute('aria-pressed',String(insertMode));
    }
    function selectVertex(id){
      if(!geometryMode||!current)return;
      const ids=current.value.type==='node'?[current.value.id]:current.value.nodes;
      if(!ids.includes(id))return;
      selectedVertexId=id;syncGeometryUI();render();
    }
    function insertVertex(index,coordinate){
      try{
        if(!geometryMode||busy||current?.value.type!=='way'||current.confidentialID)return;
        const id=nextTempId([...drafts,...(current.pendingNodes||[])],'node');
        const nodes=editWayNodes(current.value.nodes,index,id);
        const node=validateDraft({base:null,value:{type:'node',id,lon:coordinate[0],lat:coordinate[1],tags:{}}});
        (current.pendingNodes||=[]).push(node);current.value.nodes=nodes;
        selectedVertexId=id;insertMode=false;dirty=true;analyze();applyMode();render();
        say('Wegpunkt eingefügt. Entwurf speichern, um die Änderung zu behalten.');
      }catch(e){say(userError(e));}
    }
    function removeVertex(){
      try{
        if(!geometryMode||busy||selectedVertexId==null||current?.value.type!=='way')return;
        const index=current.value.nodes.indexOf(selectedVertexId);
        // The last vertex of an open line is removable too; insertion has a different range.
        const nodes=index===current.value.nodes.length-1&&current.value.nodes[0]!==selectedVertexId
          ?editWayNodes([...current.value.nodes].reverse(),0).reverse():editWayNodes(current.value.nodes,index);
        current.value.nodes=nodes;
        current.pendingNodes=(current.pendingNodes||[]).filter(d=>d.value.id!==selectedVertexId||nodes.includes(selectedVertexId));
        selectedVertexId=null;dirty=true;analyze();applyMode();render();
        say('Punkt aus diesem Weg entfernt. Gemeinsame OSM-Knoten bleiben erhalten.');
      }catch(e){say(userError(e));}
    }
    function geometryClick(event){
      if(!geometryMode||!current||busy)return;
      const point=event.point||map.project([event.lngLat.lng,event.lngLat.lat]);
      if(!insertMode){const id=vertexAt(point);if(id!=null)selectVertex(id);return;}
      let best=null;
      for(let i=0;i<current.value.nodes.length-1;i++){
        const a=coordsOfNode(current.value.nodes[i]),b=coordsOfNode(current.value.nodes[i+1]);if(!a||!b)continue;
        const pa=map.project(a),pb=map.project(b),dx=pb.x-pa.x,dy=pb.y-pa.y;
        const length=dx*dx+dy*dy;if(!length)continue;
        const t=Math.max(0,Math.min(1,((point.x-pa.x)*dx+(point.y-pa.y)*dy)/length));
        const distance=Math.hypot(point.x-pa.x-t*dx,point.y-pa.y-t*dy);
        if(!best||distance<best.distance)best={index:i,distance,coordinate:[a[0]+t*(b[0]-a[0]),a[1]+t*(b[1]-a[1])]};
      }
      if(best&&best.distance<20)insertVertex(best.index,best.coordinate);else say('Klicke direkt auf einen Wegabschnitt, um einen Punkt einzufügen.');
    }
    async function loadMapData(){
      if(!canSwitch())return;
      try{
        if(map.getZoom()<16)throw Error('Zoome auf Stufe 16 oder näher, um OSM-Objekte zu laden.');
        const b=map.getBounds(),bbox=[b.getWest(),b.getSouth(),b.getEast(),b.getNorth()];
        if(!bbox.every(Number.isFinite)||bbox[0]>=bbox[2]||bbox[1]>=bbox[3]||(bbox[2]-bbox[0])*(bbox[3]-bbox[1])>.01)throw Error('Wähle einen kleineren Kartenausschnitt.');
        setBusy(true);say('OSM-Objekte im sichtbaren Ausschnitt laden …');
        const response=await fetch(`https://api.openstreetmap.org/api/0.6/map.json?bbox=${bbox.join(',')}`,{signal:AbortSignal.timeout(20000),credentials:'omit'});
        if(!response.ok)throw Error(`OSM-Kartendaten konnten nicht geladen werden (HTTP ${response.status}).`);
        const body=await response.json();if(!Array.isArray(body.elements)||body.elements.length>50000)throw Error('Ungültiger oder zu großer OSM-Ausschnitt.');
        mapElements=body.elements;mapDataVisible=true;el('osmMapDataToggle').setAttribute('aria-pressed','true');
        render();el('osmMapDataStatus').textContent=`${mapElements.filter(e=>e.type==='way').length} Wege · ${mapElements.filter(e=>e.type==='node').length} Knoten geladen`;
        say('OSM-Objekte geladen. Wähle einen Punkt, Weg oder eine Fläche auf der Karte.');
      }catch(e){say(userError(e));}finally{setBusy(false);}
    }
    function vertexAt(point){
      if(!geometryMode||!current||busy||!map.project)return null;
      let best=null,bestDist=14;
      for(const id of new Set(current.value.type==='node'?[current.value.id]:current.value.nodes)){
        const coord=coordsOfNode(id);if(!coord)continue;
        const p=map.project(coord),dist=Math.hypot(p.x-point.x,p.y-point.y);
        if(dist<bestDist){bestDist=dist;best=id;}
      }
      return best;
    }
    function mapMouseDown(event){
      if(event.originalEvent?.target?.closest?.('.maplibregl-marker, .maplibregl-popup, .micromap-marker, .micromap-popup'))return;
      if(!viewActive||insertMode||event.originalEvent?.button>0)return;
      const id=vertexAt(event.point);
      if(id==null)return;
      event.preventDefault?.();
      if(splitMode){performSplit(id);return;}
      selectedVertexId=id;syncGeometryUI();
      dragNodeId=id;dragPreview.set(id,coordsOfNode(id));
      map.dragPan?.disable?.();
      map.getCanvas().style.cursor='grabbing';
      render();
    }
    function mapMouseMove(event){
      if(!viewActive)return;
      if(dragNodeId==null){
        if(geometryMode&&!busy)map.getCanvas().style.cursor=vertexAt(event.point)!=null?'grab':'crosshair';
        return;
      }
      dragPreview.set(dragNodeId,[normLon(event.lngLat.lng),event.lngLat.lat]);
      render();
    }
    function mapMouseUp(){
      if(dragNodeId==null)return;
      const id=dragNodeId,coord=dragPreview.get(id);
      dragNodeId=null;dragPreview.clear();
      map.dragPan?.enable?.();
      map.getCanvas().style.cursor=geometryMode?'grab':'';
      if(!coord){render();return;}
      const prior=coordsOfNode(id);
      if(prior&&prior[0]===coord[0]&&prior[1]===coord[1]){syncGeometryUI();render();return;}
      return commitNodeMove(id,coord);
    }
    async function commitNodeMove(id,[lon,lat]){
      try{
        if(!Number.isFinite(lon)||!Number.isFinite(lat)||Math.abs(lon)>180||Math.abs(lat)>90)throw Error('Ungültige Koordinaten.');
        if(current?.value.type==='node'&&current.value.id===id){
          current.value={...current.value,lon,lat};current.center=[lon,lat];dirty=true;analyze();render();syncGeometryUI();return;
        }
        const prior=drafts.find(x=>!x.deleted&&x.value.type==='node'&&x.value.id===id);
        const pending=current.pendingNodes||=[];
        const pendingIndex=pending.findIndex(x=>x.value.id===id);
        let base=pendingIndex>=0?pending[pendingIndex].base:prior?.base||current.nodeElements?.get(id);
        const value=pendingIndex>=0?pending[pendingIndex].value:prior?.value||base;
        if(!value)throw Error('Knotenbasis fehlt. Lade den Weg erneut von OSM.');
        const moved=validateDraft({base:base||null,value:{...value,lon,lat}});
        if(pendingIndex>=0)pending[pendingIndex]=moved;else pending.push(moved);
        dirty=true;analyze();render();syncGeometryUI();say('Punkt verschoben. Entwurf speichern, um die Änderung zu behalten.');
      }catch(e){say(userError(e));render();}
    }
    // Snapping a drawn point onto an already-recorded vertex keeps new ways connected to the
    // existing network instead of floating disconnected next to a road they should join.
    async function fetchSnapNodes(){
      drawSnapNodes=[];
      if(!map.getBounds)return;
      const b=map.getBounds();
      const west=normLon(b.getWest()),east=normLon(b.getEast()),south=Math.max(-90,b.getSouth()),north=Math.min(90,b.getNorth());
      const area=Math.abs(east-west)*Math.abs(north-south);
      if(!(area>0)||area>0.25){say('Zum Andocken an das bestehende Netz bitte näher heranzoomen; neue Punkte werden sonst ohne Verbindung angelegt.');return;}
      try{
        const response=await fetch(`https://api.openstreetmap.org/api/0.6/map.json?bbox=${west},${south},${east},${north}`,{signal:AbortSignal.timeout(15000),credentials:'omit'});
        if(!response.ok)throw Error('Kartendaten für das Andocken konnten nicht geladen werden.');
        const body=await response.json();
        drawSnapNodes=(body.elements||[]).filter(e=>e.type==='node'&&Number.isFinite(e.lon)&&Number.isFinite(e.lat)).map(n=>({id:n.id,lon:n.lon,lat:n.lat}));
      }catch(e){say(userError(e)+' Neue Punkte werden ohne Andocken an das bestehende Netz erstellt.');}
    }
    function nearestSnapNode(lngLat){
      if(!drawSnapNodes?.length||!map.project)return null;
      const p=map.project(lngLat);
      let best=null,bestDist=12;
      for(const n of drawSnapNodes){
        const np=map.project([n.lon,n.lat]),dist=Math.hypot(np.x-p.x,np.y-p.y);
        if(dist<bestDist){bestDist=dist;best=n;}
      }
      return best;
    }
    function startDrawing(kind){
      if(!canSwitch())return;
      geometryMode=false;insertMode=false;setTab('find');
      cancel();options.onStart?.();
      drawing=true;drawKind=kind;drawPoints=[];
      applyMode();render();
      say(`Klicke auf die Karte, um Punkte für ${kind==='area'?'die Fläche':'den Weg'} zu setzen. „Fertig“ zum Abschließen, Escape zum Abbrechen.`);
      return fetchSnapNodes();
    }
    function drawSelectedArea(){
      if(!current)return;
      const preservedTags=tags(trimmed());
      closeCurrent();setTab('find');
      startDrawing('area');
      pendingDrawTags=preservedTags;
    }
    function addDrawPoint(lngLat){
      if(drawPoints.length>=500){say('Maximal 500 Punkte pro Weg.');return;}
      const snap=nearestSnapNode(lngLat);
      drawPoints.push(snap?{lon:snap.lon,lat:snap.lat,nodeId:snap.id}:{lon:normLon(lngLat.lng),lat:lngLat.lat,nodeId:null});
      applyMode();render();
      say(`${drawPoints.length} Punkt${drawPoints.length===1?'':'e'}${snap?' · an bestehenden Punkt angedockt':''}. „Fertig“ zum Abschließen.`);
    }
    function undoDrawPoint(){if(!drawPoints.length)return;drawPoints.pop();applyMode();render();}
    function finishDraw(){
      if(!drawing)return;
      const minimum=drawKind==='area'?3:2;
      if(drawPoints.length<minimum){say(`Mindestens ${minimum} Punkte nötig, um ${drawKind==='area'?'eine Fläche':'einen Weg'} abzuschließen.`);return;}
      try{
        const drawingCoordinates=drawPoints.map(p=>[p.lon,p.lat]);
        validateWayCoordinates(drawKind==='area'?[...drawingCoordinates,drawingCoordinates[0]]:drawingCoordinates,drawKind==='area');
        const pendingNodes=[];
        const nodeIds=drawPoints.map(p=>{
          if(p.nodeId)return p.nodeId;
          const id=nextTempId([...drafts,...pendingNodes],'node');
          pendingNodes.push(validateDraft({base:null,value:{type:'node',id,lat:p.lat,lon:p.lon,tags:{}}}));
          return id;
        });
        if(drawKind==='area')nodeIds.push(nodeIds[0]);
        const wayId=nextTempId(drafts,'way');
        const value=element({type:'way',id:wayId,nodes:nodeIds,tags:pendingDrawTags||{}},true);
        const lons=drawPoints.map(p=>p.lon),lats=drawPoints.map(p=>p.lat);
        const center=[lons.reduce((a,b)=>a+b,0)/lons.length,lats.reduce((a,b)=>a+b,0)/lats.length];
        const bounds=[[Math.min(...lons),Math.min(...lats)],[Math.max(...lons),Math.max(...lats)]];
        let coordinates=drawPoints.map(p=>[p.lon,p.lat]);
        const closed=drawKind==='area';
        if(closed)coordinates=[...coordinates,coordinates[0]];
        drawing=false;drawPoints=[];drawSnapNodes=null;applyMode();
        pendingDrawTags=null;
        open({base:null,value,center,pendingNodes,shape:{bounds,coordinates,closed}});dirty=true;analyze();
        say((drawKind==='area'?'Fläche':'Weg')+' angelegt. Wähle die Art und ergänze, was du weißt.');
      }catch(e){say(userError(e));}
    }

    // Any relation draft that references the way being split gains the new segment right after
    // it, with the same role — the common case; ring order in a multipolygon may still need a
    // manual check afterwards.
    function patchRelationsForSplit(list,originalId,newId){
      return list.map(d=>{
        if(d.deleted||d.value.type!=='relation')return d;
        const members=d.value.members,at=members.findIndex(m=>m.type==='way'&&m.ref===originalId);
        if(at<0)return d;
        const patched=[...members.slice(0,at+1),{type:'way',ref:newId,role:members[at].role},...members.slice(at+1)];
        return validateDraft({...d,value:{...d.value,members:patched}});
      });
    }
    // Any relation draft referencing the removed way is repointed at the kept way instead.
    function patchRelationsForMerge(list,removedId,keptId){
      return list.map(d=>{
        if(d.deleted||d.value.type!=='relation')return d;
        const members=d.value.members;
        const patched=members.map(m=>m.type==='way'&&m.ref===removedId?{...m,ref:keptId}:m);
        return patched.some((m,i)=>m!==members[i])?validateDraft({...d,value:{...d.value,members:patched}}):d;
      });
    }
    function startSplit(){
      if(!current||current.value.type!=='way'||!current.base)return;
      if(dirty){say('Speichere oder verwirf offene Änderungen, bevor du den Weg teilst.');return;}
      if(!geometryMode)toggleGeometryMode();
      splitMode=true;
      say('Klicke auf einen inneren Punkt des Wegs, um ihn dort zu teilen. Escape zum Abbrechen.');
    }
    function performSplit(nodeId){
      splitMode=false;
      try{
        const nodes=current.value.nodes,index=nodes.indexOf(nodeId);
        if(index<=0||index>=nodes.length-1)throw Error('An diesem Punkt kann der Weg nicht geteilt werden. Wähle einen inneren Punkt, nicht Anfang oder Ende.');
        const firstValue={...current.value,nodes:nodes.slice(0,index+1)};
        const secondValue=element({type:'way',id:nextTempId(drafts,'way'),nodes:nodes.slice(index),tags:{...current.value.tags}},true);
        const firstDraft=validateDraft({base:current.base,value:firstValue,center:current.center});
        const secondDraft=validateDraft({base:null,value:secondValue,center:current.center});
        let next=drafts.slice();
        const firstIndex=next.findIndex(x=>sameObject(x,firstDraft));
        if(firstIndex<0)next.push(firstDraft);else next[firstIndex]=firstDraft;
        next.push(secondDraft);
        next=patchRelationsForSplit(next,firstValue.id,secondValue.id);
        const issues=checkReferences(next);
        if(issues.length)throw Error(issues.join(' '));
        drafts=next;
        closeCurrent();say('Weg geteilt. Beide Teile liegen als Entwürfe vor.'+persist());refresh();setTab('drafts');
      }catch(e){say(userError(e));render();}
    }
    async function mergeWith(idInput){
      if(!current||current.value.type!=='way'||!current.base)return;
      if(dirty){say('Speichere oder verwirf offene Änderungen, bevor du Wege verbindest.');return;}
      if(!validID(Number(idInput))){say('Gib die numerische OSM-ID des anderen Wegs ein.');return;}
      const id=Number(idInput);
      if(id===current.value.id){say('Wähle einen anderen Weg zum Verbinden.');return;}
      const existingDraft=drafts.find(d=>!d.deleted&&d.value.type==='way'&&d.value.id===id);
      setBusy(true);say('Anderen Weg laden …');
      try{
        let other=existingDraft?.value;
        if(!other){
          const response=await fetch(`https://api.openstreetmap.org/api/0.6/way/${id}/full.json`,{signal:AbortSignal.timeout(15000),credentials:'omit'});
          if(!response.ok)throw Error(response.status===404||response.status===410?'Dieser Weg ist auf OSM nicht verfügbar.':`Weg konnte nicht geladen werden (HTTP ${response.status}).`);
          const body=await response.json();
          other=element(body.elements?.find(e=>e.type==='way'&&e.id===id));
        }
        const a=current.value.nodes,b=other.nodes;
        let merged;
        if(a.at(-1)===b[0])merged=[...a,...b.slice(1)];
        else if(a.at(-1)===b.at(-1))merged=[...a,...b.slice(0,-1).reverse()];
        else if(a[0]===b.at(-1))merged=[...b,...a.slice(1)];
        else if(a[0]===b[0])merged=[...b.slice().reverse(),...a.slice(1)];
        else throw Error('Die Wege teilen sich keinen Endpunkt und können nicht verbunden werden.');
        const conflicts=changes(other.tags,current.value.tags).filter(c=>c.before!==undefined&&c.after!==undefined&&c.before!==c.after);
        const unionTags=Object.fromEntries([...Object.entries(other.tags),...Object.entries(current.value.tags)]);
        const keptValue={...current.value,nodes:merged,tags:unionTags};
        const keptDraft=validateDraft({base:current.base,value:keptValue,center:current.center});
        const removedDraft=validateDraft({base:existingDraft?existingDraft.base:other,deleted:true});
        let next=drafts.slice();
        const keptIndex=next.findIndex(x=>sameObject(x,keptDraft));
        if(keptIndex<0)next.push(keptDraft);else next[keptIndex]=keptDraft;
        const removedIndex=next.findIndex(d=>!d.deleted&&d.value.type==='way'&&d.value.id===id);
        if(removedIndex>=0)next[removedIndex]=removedDraft;else next.push(removedDraft);
        next=patchRelationsForMerge(next,id,keptValue.id);
        const issues=checkReferences(next);
        if(issues.length)throw Error(issues.join(' '));
        drafts=next;
        closeCurrent();
        say(`Wege verbunden.${conflicts.length?' Unterschiedliche Werte für '+conflicts.map(c=>c.key).join(', ')+' wurden zugunsten des geöffneten Wegs übernommen; bitte im verbundenen Weg prüfen.':''}`+persist());
        refresh();setTab('drafts');
      }catch(e){say(userError(e));}finally{setBusy(false);}
    }

    // A list built around an old map position is misleading once the map has moved on.
    function movedAway(){
      const c=map.getCenter();
      return !lastSearch||Math.hypot((c.lng-lastSearch.lng)*Math.cos(c.lat*Math.PI/180),c.lat-lastSearch.lat)*111320>300;
    }

    // ── Local index queries (own server, nothing leaves the machine)
    async function fetchPois(params,timeout=15000){
      const response=await fetch('/api/v1/geo/pois?'+new URLSearchParams(params),{signal:AbortSignal.timeout(timeout)});
      if(!response.ok)throw Error('Lokale Suche fehlgeschlagen.');
      const body=await response.json();
      return (body.features||[]).filter(f=>f.properties&&validID(Number(f.properties.osm_id))).map(f=>({type:f.properties.kind==='node'?'node':'way',id:Number(f.properties.osm_id),label:f.properties.label,category:f.properties.category,distance:f.properties.distance_m,coordinates:f.geometry?.coordinates}));
    }
    async function search(){
      if(busy)return;
      const query=el('osmSearch').value.trim(),point=nearbyPoint||map.getCenter();nearbyPoint=null;lastSearch={lng:point.lng,lat:point.lat};
      setBusy(true);el('osmPickMiss').hidden=true;
      say(query?`Suche „${query}“ im Umkreis von 5 km …`:'Suche erfasste Orte im Umkreis von 500 m …');
      try{
        candidates=await fetchPois({lat:point.lat,lon:normLon(point.lng),radius_m:query?5000:500,limit:50,...(query?{q:query}:{})});
        if(!candidates.length&&query){
          say(`„${query}“ nicht in der lokalen Liste; prüfe direkt bei OpenStreetMap …`);
          try{
            const needle=query.toLowerCase();
            const live=await resolveLiveCandidates(point.lat,normLon(point.lng),5000);
            candidates=live.filter(c=>(c.label||'').toLowerCase().includes(needle)||(c.category||'').toLowerCase().includes(needle));
          }catch{/* keep the empty local result; the status message below still explains it */}
        }
        renderCandidates();
        if(tab==='find')say(candidates.length?`${candidates.length} ${candidates.length===1?'Ort':'Orte'} gefunden. Wähle einen aus, um ihn zu bearbeiten.`:'Keine erfassten Orte gefunden. Verschiebe die Karte, ändere die Suche oder trage einen neuen Ort ein.');
      }catch(e){say(userError(e));}finally{setBusy(false);}
    }
    async function pick(lngLat){
      if(busy||!canSwitch())return;
      el('osmPickMiss').hidden=true;
      const radius=Math.round(pickRadiusMeters(lngLat.lat,map.getZoom?.()??17)),lat=lngLat.lat,lon=normLon(lngLat.lng);
      const draftElements=drafts.filter(d=>!d.deleted).map(d=>d.value);
      const workingElements=new Map(mapElements.map(e=>[e.type+'/'+e.id,e]));
      for(const e of draftElements)workingElements.set(e.type+'/'+e.id,e);
      const localHit=candidatesFromElements([...workingElements.values()],lat,lon,radius).find(c=>drafts.some(d=>!d.deleted&&d.value.type===c.type&&d.value.id===c.id));
      if(localHit){const draft=drafts.find(d=>!d.deleted&&d.value.type===localHit.type&&d.value.id===localHit.id);cancel();open(draft);say('Lokalen Entwurf geöffnet.');return;}
      let hit=null;
      setBusy(true);say('Suche erfassten Ort an dieser Stelle …');
      try{
        let found=candidatesFromElements(mapElements,lat,lon,radius);
        if(!found.length)found=await fetchPois({lat,lon,radius_m:radius,limit:10},8000);
        if(!found.length){
          say('Nichts in der lokalen Liste; prüfe direkt bei OpenStreetMap …');
          try{found=await resolveLiveCandidates(lat,lon,Math.max(radius,15));}catch{/* stays empty; miss message below covers it */}
        }
        if(found.length===1)hit=found[0];
        else if(found.length>1){
          candidates=found;renderCandidates();
          say(`${found.length} Objekte an dieser Stelle. Wähle eines aus, um es zu bearbeiten.`);
        }else{
          missPoint=lngLat;el('osmPickMiss').hidden=false;say('Hier ist kein Objekt bekannt. Zoome näher heran oder trage einen neuen Ort ein.');
        }
      }catch(e){say(userError(e));}finally{setBusy(false);}
      if(hit)await load(hit.type,hit.id);
    }
    async function checkDuplicates(value){
      const box=el('osmDuplicates');box.hidden=true;box.replaceChildren();
      try{
        const found=await fetchPois({lat:value.lat,lon:value.lon,radius_m:30,limit:4},6000);
        if(!found.length||!current||current.value.id!==value.id)return;
        box.append(h('strong',{text:'Schon erfasst? Bearbeite stattdessen:'}));
        for(const c of found.slice(0,3)){
          const name=P.isRawLabel(c.label,c.category)?P.categoryLabel(c.category)||'Ort':c.label,distance=formatDistance(c.distance);
          box.append(h('button',{type:'button',class:'osm-dup','aria-label':`${name}, ${distance} entfernt, stattdessen bearbeiten`,on:{click:async()=>{closeCurrent();await load(c.type,c.id);}}},h('span',{text:name}),h('small',{text:distance})));
        }
        box.hidden=false;
      }catch{/* the hint is optional */}
    }

    // ── Loading and placing
    async function load(type,id){
      if(!canSwitch())return;
      if(!['node','way','relation'].includes(type)||!validID(Number(id))){say('Gib die numerische OSM-ID ein, zum Beispiel 123456. Du findest sie auf der Objektseite von OpenStreetMap.');return;}
      const existing=drafts.find(d=>!d.deleted&&d.value.type===type&&d.value.id===Number(id));
      if(existing){
        cancel();let enriched=existing;
        if(type==='way'&&Number(id)>0){
          setBusy(true);
          try{
            const response=await fetch(`https://api.openstreetmap.org/api/0.6/way/${id}/full.json`,{signal:AbortSignal.timeout(15000),credentials:'omit'});
            if(response.ok){const body=await response.json();rememberElements(body.elements);enriched={...existing,nodeElements:nodeIndex(body.elements),shape:wayShape(body.elements,Number(id))};}
          }catch{/* Saved tags remain editable offline; unavailable geometry stays uneditable. */}
          finally{setBusy(false);}
        }
        open(enriched);flyToObject(enriched);say('Vorhandenen Entwurf geöffnet. Die Basisversion bleibt erhalten.');return;
      }
      id=String(Number(id));
      cancel();setBusy(true);say('Aktuellen Stand von api.openstreetmap.org laden …');
      try{
        const response=await fetch(`https://api.openstreetmap.org/api/0.6/${type}/${id}${type==='node'?'':'/full'}.json`,{signal:AbortSignal.timeout(15000),credentials:'omit'});
        if(!response.ok)throw Error(response.status===404||response.status===410?'Dieses Objekt ist auf OSM nicht verfügbar. Prüfe die ID oder wähle ein anderes Objekt.':response.status===429?'OSM erhält gerade zu viele Anfragen. Warte kurz und versuche es erneut.':`OSM konnte das Objekt nicht laden (HTTP ${response.status}). Bitte erneut versuchen.`);
        const body=await response.json(),base=element(body.elements?.find(e=>e.type===type&&e.id===Number(id)));
        rememberElements(body.elements);
        if(dirty)throw Error('Offene Änderungen erst speichern oder verwerfen, dann erneut laden.');
        const shape=base.type==='way'?wayShape(body.elements,base.id):null;
        const nodeElements=base.type==='way'?nodeIndex(body.elements):null;
        const center=shape?shape.center:base.type==='node'?[base.lon,base.lat]:undefined;
        open({base,value:structuredClone(base),center,shape,nodeElements});
        flyToObject(current);
        say('Objekt geladen. Ändere die Angaben und speichere sie als Entwurf.');
      }catch(e){say(userError(e));}finally{setBusy(false);}
    }
    function place(lngLat){
      const id=nextTempId(drafts,'node');
      try{
        const value=element({type:'node',id,lat:lngLat.lat,lon:normLon(lngLat.lng),tags:{}},true);
        placing=false;applyMode();
        open({base:null,value,center:[value.lon,value.lat]});dirty=true;analyze();
        say('Position gewählt. Wähle die Art des Ortes und ergänze, was du weißt.');
        checkDuplicates(value);
      }catch(e){say(userError(e));}
    }
    function mapClick(event){
      if(event.originalEvent?.target?.closest?.('.maplibregl-marker, .maplibregl-popup, .micromap-marker, .micromap-popup'))return;
      const m=mode();
      if(m==='geometry')return geometryClick(event);
      if(m==='draw')return addDrawPoint(event.lngLat);
      if(m==='place')return place(event.lngLat);
      if(m==='pick')return pick(event.lngLat);
    }

    // ── Confidential objects: local-only points/lines/areas that never touch `drafts`, `osc()`
    // or the .osc export (fire-department internals, access data, ... that must never reach OSM;
    // see cmd/confidential_objects.go). Loaded and managed entirely through their own REST
    // endpoint and their own map layer, deliberately kept out of every OSM-draft code path above.
    let confidentialObjects=[];
    function confidentialAuthHeaders(extra){return typeof root.adminAuthHeaders==='function'?root.adminAuthHeaders(extra):extra||{};}
    // Coordinates for a way come from the live vertex positions (matches the osm-selection
    // preview in render()), not the immutable shape snapshot, so any drag before the first save
    // is reflected; a reopened confidential way has no real node drafts, so every lookup falls
    // through to shape.coordinates by design (see openConfidential).
    function confidentialGeometry(){
      if(!current)return null;
      const {value}=current;
      if(value.type==='node')return {type:'Point',coordinates:[value.lon,value.lat]};
      if(value.type==='way'){
        const line=value.nodes.map((id,i)=>coordsOfNode(id)||current.shape?.coordinates?.[i]||null).filter(Boolean);
        if(line.length<2)return null;
        return current.shape?.closed?{type:'Polygon',coordinates:[line]}:{type:'LineString',coordinates:line};
      }
      return null;
    }
    async function loadConfidentialObjects(){
      try{
        const response=await fetch('/api/v1/confidential-objects',{headers:confidentialAuthHeaders({'Accept':'application/json'})});
        // No admin token configured (503) or an invalid/missing one (401): the layer and list
        // stay empty and silent, exactly as the approved plan specifies -- this is not an error
        // state for users who never use confidential objects.
        if(!response.ok){confidentialObjects=[];renderConfidentialList();render();return;}
        const body=await response.json();
        confidentialObjects=Array.isArray(body.objects)?body.objects:[];
      }catch{confidentialObjects=[];}
      renderConfidentialList();render();
    }
    function confidentialLabel(obj){
      const t=obj.tags||{};
      return t.name||t.notiz||t.note||Object.values(t)[0]||`${obj.geometry_type} · ${obj.id}`;
    }
    function flyToConfidential(obj){
      if(obj.geometry_type==='Point'){if(map.flyTo)map.flyTo({center:obj.coordinates,zoom:Math.max(map.getZoom?.()??0,18)});return;}
      const ring=obj.geometry_type==='Polygon'?obj.coordinates[0]:obj.coordinates;
      if(!ring?.length||!map.fitBounds)return;
      const lons=ring.map(c=>c[0]),lats=ring.map(c=>c[1]);
      map.fitBounds([[Math.min(...lons),Math.min(...lats)],[Math.max(...lons),Math.max(...lats)]],{maxZoom:19,padding:60});
    }
    function openConfidential(obj){
      if(!canSwitch())return;
      cancel();
      const isPoint=obj.geometry_type==='Point';
      const ring=isPoint?null:obj.geometry_type==='Polygon'?obj.coordinates[0]:obj.coordinates;
      const value=isPoint
        ?{type:'node',id:nextTempId(drafts,'node'),lat:obj.coordinates[1],lon:obj.coordinates[0],tags:{...obj.tags}}
        :{type:'way',id:nextTempId(drafts,'way'),nodes:ring.map((_,i)=>-(i+1)),tags:{...obj.tags}};
      const shape=isPoint?null:{coordinates:ring,closed:obj.geometry_type==='Polygon',bounds:null};
      const center=isPoint?obj.coordinates:ring[0];
      open({base:null,value,center,shape});
      current.confidentialID=obj.id;
      renderForm();
      const toggle=el('osmConfidentialToggle');if(toggle){toggle.checked=true;toggle.disabled=true;}
      const geomToggle=el('osmGeometryToggle');if(geomToggle)geomToggle.hidden=true;
      say('Vertrauliches Objekt geladen. Die Geometrie bleibt beim Bearbeiten unverändert; nur Eigenschaften lassen sich anpassen.');
    }
    async function deleteConfidential(id){
      try{
        const response=await fetch(`/api/v1/confidential-objects/${encodeURIComponent(id)}`,{method:'DELETE',headers:confidentialAuthHeaders()});
        if(!response.ok&&response.status!==404)throw Error(`Löschen fehlgeschlagen (HTTP ${response.status}).`);
        if(current?.confidentialID===id)closeCurrent();
        say('Vertrauliches Objekt entfernt.');
        await loadConfidentialObjects();
      }catch(e){say(userError(e));}
    }
    function renderConfidentialList(){
      const list=el('osmConfidentialList');if(!list)return;
      list.replaceChildren();
      for(const obj of confidentialObjects){
        const name=confidentialLabel(obj);
        const main=h('button',{type:'button',class:'osm-draft-main','aria-label':`${name} bearbeiten`,on:{click:()=>openConfidential(obj)}},h('span',{class:'osm-draft-icon','aria-hidden':'true',text:'🔒'}),h('span',{class:'osm-draft-text'},h('strong',{text:name}),h('small',{text:obj.geometry_type})));
        const locate=h('button',{type:'button',class:'osm-icon-button','aria-label':`${name} auf der Karte zeigen`,title:'Auf der Karte zeigen',innerHTML:iconHTML('icon-location'),on:{click:()=>flyToConfidential(obj)}});
        const remove=h('button',{type:'button',class:'osm-icon-button osm-danger','aria-label':`${name} löschen`,title:'Löschen',innerHTML:iconHTML('icon-close'),on:{click:()=>{if(confirm(`„${name}“ endgültig löschen?`))return deleteConfidential(obj.id);}}});
        list.append(h('li',{class:'osm-draft'},main,locate,remove));
      }
      const empty=el('osmConfidentialEmpty');if(empty)empty.hidden=!!confidentialObjects.length;
    }
    async function saveConfidential(){
      try{
        if(!current)return;
        const geometry=confidentialGeometry();
        if(!geometry){say('Für ein vertrauliches Objekt wird ein Punkt oder eine gezeichnete Linie/Fläche mit mindestens 2 Punkten benötigt.');return;}
        const tagMap=tags(trimmed());
        if(!Object.keys(tagMap).length)throw Error('Ein vertrauliches Objekt benötigt mindestens eine Eigenschaft.');
        setBusy(true);
        const isUpdate=!!current.confidentialID;
        const response=await fetch(isUpdate?`/api/v1/confidential-objects/${encodeURIComponent(current.confidentialID)}`:'/api/v1/confidential-objects',{
          method:isUpdate?'PUT':'POST',
          headers:confidentialAuthHeaders({'Content-Type':'application/json'}),
          body:JSON.stringify({geometry_type:geometry.type,coordinates:geometry.coordinates,tags:tagMap}),
        });
        if(!response.ok){const body=await response.json().catch(()=>({}));throw Error(body.error||`Speichern fehlgeschlagen (HTTP ${response.status}).`);}
        closeCurrent();
        say((isUpdate?'Vertrauliches Objekt aktualisiert.':'Vertrauliches Objekt gespeichert.')+' Bleibt nur lokal auf diesem Server, nie Teil des OSM-Exports.');
        setTab('find');
        await loadConfidentialObjects();
      }catch(e){say(userError(e));}
      finally{setBusy(false);syncUI();}
    }

    // ── Save, discard, export
    function save(){
      try{
        if(!current)return;
        if(current.confidentialID||(!current.base&&current.value.type!=='relation'&&el('osmConfidentialToggle')?.checked))return saveConfidential();
        const isRelation=current.value.type==='relation';
        if(current.value.type==='way'&&(!current.base||geometryChange(current.base,current.value)||current.pendingNodes?.length)){
          const coordinates=current.value.nodes.map(coordsOfNode);
          if(coordinates.every(Boolean))validateWayCoordinates(coordinates,current.value.nodes.length>3&&current.value.nodes[0]===current.value.nodes[current.value.nodes.length-1]);
        }
        if(current.value.type==='way'){const coords=current.value.nodes.map(coordsOfNode).filter(Boolean);if(coords.length)current.center=[(Math.min(...coords.map(p=>p[0]))+Math.max(...coords.map(p=>p[0])))/2,(Math.min(...coords.map(p=>p[1]))+Math.max(...coords.map(p=>p[1])))/2];}
        const d=validateDraft({...current,value:{...current.value,tags:tags(trimmed()),...(isRelation?{members:members.map(m=>({...m}))}:{})}});
        const pending=current.pendingNodes||[];
        if(!d.base&&d.value.type==='node'&&!Object.keys(d.value.tags).length)throw Error('Ein neuer Punkt benötigt mindestens einen Tag.');
        const index=drafts.findIndex(x=>sameObject(x,d));
        const before=new Set(drafts.find(x=>sameObject(x,d))?.value?.nodes||[]);
        const next=drafts.slice();
        for(const n of pending){const at=next.findIndex(x=>sameObject(x,n));if(at<0)next.push(n);else next[at]=n;}
        const unchanged=d.base&&elementUnchanged(d.base,d.value);
        if(unchanged){if(index>=0)next.splice(index,1);}
        else if(index<0)next.push(d);else next[index]=d;
        if(d.value.type==='way'){
          for(let i=next.length-1;i>=0;i--){
            const n=next[i];if(n.base||n.deleted||n.value.type!=='node'||n.value.id>0||!before.has(n.value.id)||Object.keys(n.value.tags).length)continue;
            if(!next.some(x=>!x.deleted&&((x.value.type==='way'&&x.value.nodes.includes(n.value.id))||(x.value.type==='relation'&&x.value.members.some(m=>m.type==='node'&&m.ref===n.value.id)))))next.splice(i,1);
          }
        }
        if(next.length>100)throw Error('Maximal 100 Entwürfe.');
        const issues=checkReferences(next);if(issues.length)throw Error(issues.join(' '));
        drafts=next;
        const message=unchanged&&!pending.length?'Keine Änderungen; das Objekt ist nicht im Export.':`Entwurf gespeichert (${drafts.length} im Arbeitsstand). Wähle das nächste Objekt oder öffne „Entwürfe“ zum Export.`;
        closeCurrent();say(message+persist());refresh();setTab(isRelation?'relations':'find');
        status.focus?.();
      }catch(e){say(userError(e));}finally{syncUI();}
    }
    async function deleteObject(){
      if(!current?.base||current.confidentialID||!canSwitch())return;
      const base=current.base;
      if(!confirm(`„${label(current.value)}“ als Löschentwurf vormerken? Die Löschung bleibt lokal und kann rückgängig gemacht werden.`))return;
      setBusy(true);
      try{
        const endpoints=base.type==='node'?['ways','relations']:['relations'];
        for(const endpoint of endpoints){
          const response=await fetch(`https://api.openstreetmap.org/api/0.6/${base.type}/${base.id}/${endpoint}.json`,{signal:AbortSignal.timeout(15000),credentials:'omit'});
          if(!response.ok)throw Error('Verwendungen des Objekts konnten nicht geprüft werden. Löschentwurf nicht angelegt.');
          const body=await response.json();if(!Array.isArray(body.elements))throw Error('Ungültige Antwort bei der Prüfung der Verwendungen.');
          const used=body.elements.filter(e=>{
            const parent=drafts.find(d=>(d.deleted?d.base:d.value).type===e.type&&(d.deleted?d.base:d.value).id===e.id);
            if(!parent)return true;if(parent.deleted)return false;
            return parent.value.type==='way'?parent.value.nodes.includes(base.id):parent.value.members.some(m=>m.type===base.type&&m.ref===base.id);
          });
          if(used.length)throw Error(`Objekt wird noch von ${used.length} ${endpoint==='ways'?'Wegen':'Relationen'} verwendet. Zuerst die Verwendungen bearbeiten.`);
        }
        const next=drafts.filter(d=>keyOf(d)!==keyOf(current));next.push(validateDraft({base,deleted:true}));
        if(next.length>100)throw Error('Maximal 100 Entwürfe.');
        const issues=checkReferences(next);if(issues.length)throw Error(issues.join(' '));
        drafts=next;closeCurrent();say('Löschentwurf angelegt. Mit „Rückgängig“ wiederherstellen.'+persist());refresh();setTab('drafts');
      }catch(e){say(userError(e));}finally{setBusy(false);}
    }
    function discard(){
      const wasRelation=current?.value.type==='relation';
      closeCurrent();setTab(wasRelation?'relations':'find');
      say('Bearbeitung geschlossen. Gespeicherte Entwürfe bleiben erhalten.');
      if(!wasRelation)el('osmSearch').focus?.();
    }
    function download(content,name,type){const url=URL.createObjectURL(new Blob([content],{type})),a=document.createElement('a');a.href=url;a.download=name;a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);}

    // ── Wiring
    el('osmModeBasic')?.addEventListener('click',()=>setCareMode('basic'));
    el('osmModeProfessional')?.addEventListener('click',()=>setCareMode('professional'));
    el('osmSearchForm').addEventListener('submit',event=>{event.preventDefault?.();return search();});
    el('osmNew').addEventListener('click',()=>{
      if(!canSwitch())return;
      if(placing){cancel();say('Punktsetzen beendet.');return;}
      cancel();geometryMode=false;setTab('find');options.onStart?.();placing=true;el('osmPickMiss').hidden=true;applyMode();
      say('Punktsetzen aktiv. Klicke auf die genaue Position des neuen Ortes.');syncUI();
    });
    el('osmPickPlace').addEventListener('click',()=>{if(missPoint&&canSwitch()){const point=missPoint;missPoint=null;el('osmPickMiss').hidden=true;placing=true;place(point);}});
    el('osmDrawLine')?.addEventListener('click',()=>startDrawing('line'));
    el('osmDrawArea')?.addEventListener('click',()=>startDrawing('area'));
    el('osmDrawUndo')?.addEventListener('click',undoDrawPoint);
    el('osmDrawFinish')?.addEventListener('click',finishDraw);
    el('osmDrawCancel')?.addEventListener('click',()=>{cancel();say('Zeichnen abgebrochen.');});
    for(const [tool,original] of [['osmToolPoint','osmNew'],['osmToolLine','osmDrawLine'],['osmToolArea','osmDrawArea'],['osmToolUndo','osmUndo'],['osmToolRedo','osmRedo']])el(tool)?.addEventListener('click',()=>el(original).click());
    el('osmLoadMapData')?.addEventListener('click',loadMapData);
    el('osmMapDataToggle')?.addEventListener('click',()=>{mapDataVisible=!mapDataVisible;el('osmMapDataToggle').setAttribute('aria-pressed',String(mapDataVisible));render();});
    el('osmVertexInsert')?.addEventListener('click',()=>{insertMode=!insertMode;syncGeometryUI();say(insertMode?'Klicke auf einen Wegabschnitt, um einen Punkt einzufügen.':'Punkt einfügen beendet.');});
    el('osmVertexRemove')?.addEventListener('click',removeVertex);
    el('osmVertexApply')?.addEventListener('click',()=>{
      const lon=el('osmVertexLon').value.trim(),lat=el('osmVertexLat').value.trim();
      if(!lon||!lat){say('Beide Koordinaten ausfüllen.');return;}
      return commitNodeMove(selectedVertexId,[Number(lon),Number(lat)]);
    });
    el('osmGeometryToggle')?.addEventListener('click',toggleGeometryMode);
    el('osmSplit')?.addEventListener('click',startSplit);
    el('osmMergeGo')?.addEventListener('click',()=>mergeWith(el('osmMergeId').value));
    el('osmNewRelation')?.addEventListener('click',newRelation);
    el('osmRelationLoad')?.addEventListener('click',()=>load('relation',el('osmRelationId').value));
    el('osmMemberDraft')?.addEventListener('change',()=>{
      const value=el('osmMemberDraft').value;if(!value)return;
      const [type,ref]=value.split('/');addMember(type,Number(ref),'');
      el('osmMemberDraft').value='';
    });
    el('osmMemberAdd')?.addEventListener('click',()=>{
      try{
        const type=el('osmMemberType').value,ref=Number(el('osmMemberId').value);
        if(!validID(ref))throw Error('Gib die numerische OSM-ID des Mitglieds ein.');
        addMember(type,ref,el('osmMemberRole').value||'');
        el('osmMemberId').value='';el('osmMemberRole').value='';
      }catch(e){say(userError(e));}
    });
    el('osmLoad').addEventListener('click',()=>load(el('osmType').value,el('osmID').value));
    el('osmAddTag').addEventListener('click',()=>{const pair=['',''];working.push(pair);dirty=true;renderTable();analyze();el('osmTags').children[el('osmTags').children.length-1]?.children[0].focus?.();});
    el('osmSave').addEventListener('click',save);
    el('osmDiscard').addEventListener('click',discard);
    el('osmDeleteObject')?.addEventListener('click',deleteObject);
    el('osmLocate').addEventListener('click',()=>{if(current)flyToObject(current);});
    el('osmEmptyFind').addEventListener('click',()=>setTab('find',true));
    for(const [id,buttonId] of TABS){
      el(buttonId).addEventListener('click',()=>{setTab(id,false);if(id==='drafts')say(dirty?'Offene Änderungen zuerst speichern oder verwerfen.':drafts.length?'Arbeitsstand lokal gespeichert. Prüfe die Änderungen vor dem Export.':'Noch keine Entwürfe im Arbeitsstand.');});
      el(buttonId).addEventListener('keydown',event=>{
        const order=TABS.map(t=>t[0]),at=order.indexOf(id);
        const next={ArrowRight:order[(at+1)%order.length],ArrowLeft:order[(at+order.length-1)%order.length],Home:order[0],End:order[order.length-1]}[event.key];
        if(next){event.preventDefault?.();setTab(next,true);}
      });
    }
    el('osmExport').addEventListener('click',()=>{try{if(busy)throw Error('Laufenden Vorgang zuerst abschließen lassen.');if(checkedFor===versions()&&conflicts.length)throw Error('Versionskonflikte: Entwurf sichern und in einem OSM-Editor abgleichen.');if(dirty)throw Error('Offene Änderungen erst speichern oder verwerfen.');download(osc(drafts),'osm-entwurf.osc','application/xml');say('Änderungsdatei exportiert. Vor einem Upload Basisversionen und Änderungen in einem OSM-Editor prüfen.');}catch(e){say(userError(e));}});
    el('osmBackup').addEventListener('click',()=>{if(dirty){say('Offene Änderungen erst speichern oder verwerfen.');return;}download(JSON.stringify({version:1,drafts},null,2),'osm-entwurf.json','application/json');});
    for(const id of ['osmUndo','osmRedo']){
      // Resolve stacks dynamically because a new edit discards redo history.
      el(id).addEventListener('click',()=>{
        if(!canSwitch())return;
        const source=id==='osmUndo'?undo:redo,target=id==='osmUndo'?redo:undo;if(!source.length)return;
        target.push(JSON.stringify(drafts));drafts=JSON.parse(source.pop());closeCurrent();
        say('Entwurfsänderung '+(id==='osmUndo'?'rückgängig gemacht.':'wiederhergestellt.')+persist(false));refresh();
      });
    }
    el('osmCheck').addEventListener('click',async()=>{
      if(!canSwitch())return;setBusy(true);const snapshot=JSON.stringify(drafts);say('Basisversionen mit OSM abgleichen …');
      try{
        const found=await checkVersions(JSON.parse(snapshot));
        if(snapshot!==JSON.stringify(drafts))throw Error('Entwürfe wurden geändert; Versionsprüfung wiederholen.');
        checkedFor=versions();conflicts=found;
        say(found.length?'Versionskonflikte: '+found.join(' ')+' Bitte in einem OSM-Editor abgleichen.':drafts.some(d=>d.base)?'Alle Basisversionen sind aktuell. Vor einem späteren Upload erneut prüfen.':'Nur neue Punkte: keine Basisversionen zu prüfen. Duplikate bitte manuell prüfen.');
      }catch(e){say(userError(e));}finally{setBusy(false);}
    });
    el('osmImport').addEventListener('change',async()=>{
      const file=el('osmImport').files[0];if(!file)return;
      if(!canSwitch()){el('osmImport').value='';return;}
      try{
        if(file.size>5*1024*1024)throw Error('Datei größer als 5 MB.');
        setBusy(true);
        const incoming=restore(await file.text()),merged=[...drafts,...incoming];
        if(dirty)throw Error('Offene Änderungen zuerst speichern oder verwerfen.');
        osc(merged);drafts=merged;say('Entwürfe ergänzt.'+persist());refresh();
      }catch(e){say(userError(e));}finally{setBusy(false);el('osmImport').value='';}
    });
    root.addEventListener?.('beforeunload',e=>{if(dirty){e.preventDefault();e.returnValue='';}});
    document.addEventListener('keydown',e=>{
      const editingInput=/^(INPUT|TEXTAREA|SELECT)$/.test(e.target?.tagName)||e.target?.isContentEditable;
      if(viewActive&&!editingInput&&!busy){
        if((e.ctrlKey||e.metaKey)&&(e.key||'').toLowerCase()==='z'){e.preventDefault();el(e.shiftKey?'osmRedo':'osmUndo').click();return;}
        if(!e.ctrlKey&&!e.metaKey&&!e.altKey){
          if(e.key==='Enter'&&drawing){e.preventDefault();finishDraw();return;}
          if((e.key==='Delete'||e.key==='Backspace')&&geometryMode){e.preventDefault();removeVertex();return;}
          const shortcut={p:'osmToolPoint',l:'osmToolLine',a:'osmToolArea'}[(e.key||'').toLowerCase()];
          if(shortcut){e.preventDefault();el(shortcut).click();return;}
        }
      }
      if(e.key==='Escape'&&insertMode){insertMode=false;syncGeometryUI();say('Punkt einfügen abgebrochen.');}
      else if(e.key==='Escape'&&geometryMode&&!splitMode){toggleGeometryMode();}
      else if(e.key==='Escape'&&splitMode){splitMode=false;say('Weg teilen abgebrochen.');}
      else if(e.key==='Escape'&&(placing||drawing)){const wasDrawing=drawing;cancel();say(wasDrawing?'Zeichnen abgebrochen.':'Punktsetzen beendet.');}
      const view=el('osmEditView');
      if((e.ctrlKey||e.metaKey)&&(e.key||'').toLowerCase()==='s'&&current&&!view.hidden&&view.offsetParent!==null){e.preventDefault();if(!el('osmSave').disabled)save();}
    });
    map.on?.('mousedown',mapMouseDown);
    map.on?.('mousemove',mapMouseMove);
    map.on?.('mouseup',mapMouseUp);

    setTab('find');renderForm();refresh();
    const api={
      startAt(point){if(!canSwitch())return;options.onStart?.();placing=true;place(point);},
      findNearby(point){if(!canSwitch())return;setTab('find');el('osmSearch').value='';nearbyPoint=point;search();status.focus?.();},
      enter(){
        viewActive=true;options.onStart?.();applyMode();render();
        // Deferred so an action that opens the view (context menu, hover) can claim the editor first.
        setTimeout(()=>{if(viewActive&&tab==='find'&&!busy&&!current&&(!candidates.length||movedAway()))search();},0);
        return loadConfidentialObjects();
      },
      leave(){viewActive=false;cancel();geometryMode=false;dragNodeId=null;dragPreview.clear();map.dragPan?.enable?.();applyMode();render();},
      load,cancel,render,setTab,mapClick,addPoint:mapClick,
      startDrawing,finishDraw,undoDrawPoint,toggleGeometryMode,startSplit,mergeWith,
      loadMapData,insertVertex,removeVertex,selectVertex,commitNodeMove,removeDraft,deleteObject,
      newRelation,loadRelation:id=>load('relation',id),addMember,
      mapMouseDown,mapMouseMove,mapMouseUp,
      get active(){return mode()!==null;},
      get tab(){return tab;},
      get drawing(){return drawing;},
      get drawPointCount(){return drawPoints.length;},
      get geometryMode(){return geometryMode;},
      get splitMode(){return splitMode;},
      get members(){return members;},
    };
    return api;
  }
  root.OSMEditor={tags,element,changes,geometryChange,memberChanges,validateDraft,nextTempId,checkReferences,osc,restore,checkVersions,wayShape,nodeIndex,pickRadiusMeters,formatDistance,metersBetween,nearestPointOnSegment,resolveLiveCandidates,candidatesFromElements,editWayNodes,validateWayCoordinates,mapDataFeatures,create};
})(typeof window==='undefined'?globalThis:window);

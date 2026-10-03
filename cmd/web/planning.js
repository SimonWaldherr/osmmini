/* Local sketch measurements. Coordinates are WGS84; results are spherical approximations. */
(function (root) {
  'use strict';
  const R = 6371008.8, rad = Math.PI / 180;
  const kinds = {line: 'Strecke', site: 'Bezugsfläche', building: 'Gebäude'};
  const distance = (a, b) => {
    const h = Math.sin((b[1]-a[1])*rad/2)**2 + Math.cos(a[1]*rad)*Math.cos(b[1]*rad)*Math.sin((b[0]-a[0])*rad/2)**2;
    return 2*R*Math.asin(Math.sqrt(Math.min(1, h)));
  };
  function unwrap(points) {
    return points.reduce((out, p) => {
      let x = p[0];
      if (out.length) { while (x-out.at(-1)[0]>180) x-=360; while (x-out.at(-1)[0]<-180) x+=360; }
      out.push([x,p[1]]); return out;
    }, []);
  }
  const cross = (a,b,c) => (b[0]-a[0])*(c[1]-a[1])-(b[1]-a[1])*(c[0]-a[0]);
  function onSegment(a,b,p) {
    return Math.abs(cross(a,b,p)) < 1e-12 && p[0]>=Math.min(a[0],b[0])-1e-12 && p[0]<=Math.max(a[0],b[0])+1e-12 && p[1]>=Math.min(a[1],b[1])-1e-12 && p[1]<=Math.max(a[1],b[1])+1e-12;
  }
  function intersects(a,b,c,d) {
    return (cross(a,b,c)*cross(a,b,d)<0 && cross(c,d,a)*cross(c,d,b)<0) || onSegment(a,b,c) || onSegment(a,b,d) || onSegment(c,d,a) || onSegment(c,d,b);
  }
  function inside(p, ring) {
    let yes=false;
    for(let i=0,j=ring.length-1;i<ring.length;j=i++) {
      const a=ring[j], b=ring[i];
      if(onSegment(a,b,p)) return true;
      if((a[1]>p[1])!==(b[1]>p[1]) && p[0]<(b[0]-a[0])*(p[1]-a[1])/(b[1]-a[1])+a[0]) yes=!yes;
    }
    return yes;
  }
  function ringsIntersect(a,b) {
    return a.some((p,i)=>b.some((q,j)=>intersects(p,a[(i+1)%a.length],q,b[(j+1)%b.length])));
  }
  function measure(item) {
    if (!item || !Object.hasOwn(kinds,item.kind)) throw Error('Unbekannter Geometrietyp.');
    const points=item.points;
    if(!Array.isArray(points) || points.length<(item.kind==='line'?2:3) || points.length>500) throw Error('Mindestens zwei Streckenpunkte oder drei Flächenecken setzen (maximal 500).');
    for(const p of points) if(!Array.isArray(p)||p.length!==2||!p.every(Number.isFinite)||Math.abs(p[0])>180||Math.abs(p[1])>85) throw Error('Ungültige Koordinaten (Breitengrad zwischen −85° und 85°).');
    if(points.some(p=>distance(points[0],p)>20000)) throw Error('Entwürfe sind auf 20 km Abstand vom ersten Punkt begrenzt.');
    if(item.kind==='building' && (!Number.isInteger(item.floors)||item.floors<1||item.floors>100)) throw Error('Geschosszahl muss zwischen 1 und 100 liegen.');
    const p=unwrap(points), closed=item.kind!=='line';
    let length=0, area=0;
    for(let i=0;i<(closed?p.length:p.length-1);i++) {
      const a=p[i],b=p[(i+1)%p.length], edge=distance(a,b);
      if(edge<0.01) throw Error('Aufeinanderfolgende Punkte müssen mindestens 1 cm auseinanderliegen.');
      length+=edge;
      area+=(b[0]-a[0])*rad*(Math.sin(a[1]*rad)+Math.sin(b[1]*rad));
      if(closed) for(let j=i+1;j<p.length;j++) {
        if(j===i+1 || (i===0&&j===p.length-1)) continue;
        if(intersects(a,b,p[j],p[(j+1)%p.length])) throw Error('Flächenkanten dürfen sich nicht kreuzen oder berühren.');
      }
      if(closed && onSegment(a,b,p[(i+2)%p.length])) throw Error('Flächenecken dürfen nicht auf benachbarten Kanten liegen.');
    }
    area=closed?Math.abs(area)*R*R/2:0;
    if(closed&&area<0.01) throw Error('Die Fläche ist zu klein oder hat keine Ausdehnung.');
    return {length_m:length, area_m2:area, floor_area_m2:item.kind==='building'?area*item.floors:0};
  }
  function balance(items) {
    const sites=items.filter(x=>x.kind==='site'), buildings=items.filter(x=>x.kind==='building');
    const footprint=buildings.reduce((s,b)=>s+measure(b).area_m2,0);
    const floors=buildings.reduce((s,b)=>s+measure(b).floor_area_m2,0);
    const result={footprint_m2:footprint, floor_area_m2:floors, count:buildings.length};
    if(sites.length!==1) return {...result, warning:'Für eine Flächenbilanz genau eine Bezugsfläche zeichnen.'};
    const site=unwrap(sites[0].points);
    const aligned=buildings.map(b=>{ const p=unwrap(b.points); const offset=Math.round((site[0][0]-p[0][0])/360)*360; return p.map(([x,y])=>[x+offset,y]); });
    for(let i=0;i<aligned.length;i++) {
      const p=aligned[i];
      if(p.some(q=>!inside(q,site)) || ringsIntersect(p,site)) return {...result,warning:'Gebäude müssen vollständig innerhalb der Bezugsfläche liegen, ohne deren Rand zu berühren.'};
      for(let j=0;j<i;j++) if(ringsIntersect(p,aligned[j])||inside(p[0],aligned[j])||inside(aligned[j][0],p)) return {...result,warning:'Gebäude überlappen oder berühren sich. Für die Bilanz getrennte Grundrisse zeichnen.'};
    }
    const area=measure(sites[0]).area_m2;
    return {...result,site_m2:area,free_m2:Math.max(0,area-footprint),coverage_percent:100*footprint/area};
  }
  function collection(items) {
    return {type:'FeatureCollection',features:items.map(item=>{
      const metrics=measure(item), p=unwrap(item.points);
      return {type:'Feature',properties:{name:item.name,kind:item.kind,...(item.kind==='building'?{floors:item.floors}:{}),...metrics,measurement_model:'sphere R=6371008.8m; sketch, not survey'},geometry:item.kind==='line'?{type:'LineString',coordinates:p}:{type:'Polygon',coordinates:[[...p,p[0]]]}};
    })};
  }
  function restore(raw) {
    const data=JSON.parse(raw);
    if(!data||data.version!==1||!Array.isArray(data.items)||data.items.length>100) throw Error('Ungültiger gespeicherter Entwurf.');
    return data.items.map(item=>{measure(item);return {kind:item.kind,points:item.points.map(p=>p.slice()),name:String(item.name||kinds[item.kind]).slice(0,80),floors:item.kind==='building'?item.floors:1};});
  }
  function create(map, options={}) {
    const el=id=>document.getElementById(id), status=el('planStatus'), storageKey='osmmini.planning.v1';
    const fmt=(n,unit)=>`${new Intl.NumberFormat('de-DE',{maximumFractionDigits:1}).format(n)} ${unit}`;
    let items=[], draft=[], active=false;
    let storageWarning='';
    try {const raw=localStorage.getItem(storageKey);if(raw) items=restore(raw);} catch {storageWarning='Gespeicherter Entwurf konnte nicht geladen werden.';}
    const draftItem=()=>({kind:el('planKind').value, name:el('planName').value.trim().slice(0,80)||kinds[el('planKind').value],floors:Number(el('planFloors').value),points:draft});
    function notice(text) {status.textContent=text+(storageWarning?' '+storageWarning:'');}
    function persist() {
      try {localStorage.setItem(storageKey,JSON.stringify({version:1,items}));storageWarning='';} catch {storageWarning='Speichern im Browser nicht möglich. Bitte GeoJSON exportieren.';}
    }
    function render() {
      if(!map.isStyleLoaded()) return;
      const data=collection(items);
      data.features.forEach(f=>{f.properties.draft=false;});
      if(draft.length>1) data.features.push({type:'Feature',properties:{kind:el('planKind').value,draft:true},geometry:{type:'LineString',coordinates:unwrap(draft)}});
      for(const p of unwrap(draft)) data.features.push({type:'Feature',properties:{draft:true},geometry:{type:'Point',coordinates:p}});
      if(map.getSource('planning')) map.getSource('planning').setData(data);
      else {
        map.addSource('planning',{type:'geojson',data});
        const color=['match',['get','kind'],'building','#f59e0b','site','#14b8a6','#3b82f6'];
        map.addLayer({id:'planning-fill',type:'fill',source:'planning',filter:['==',['geometry-type'],'Polygon'],paint:{'fill-color':color,'fill-opacity':0.22}});
        map.addLayer({id:'planning-lines',type:'line',source:'planning',filter:['!=',['geometry-type'],'Point'],paint:{'line-color':color,'line-width':3}});
        map.addLayer({id:'planning-points',type:'circle',source:'planning',filter:['==',['geometry-type'],'Point'],paint:{'circle-color':'#fff','circle-radius':5,'circle-stroke-width':2,'circle-stroke-color':'#2563eb'}});
      }
    }
    function update() {
      render();
      options.onChange?.();
      el('planStart').disabled=active || items.length>=100;
      el('planFinish').disabled=!active||draft.length<(el('planKind').value==='line'?2:3);
      el('planUndo').disabled=!draft.length;
      el('planCancel').disabled=!active;
      el('planKind').disabled=active;
      el('planFloorsWrap').hidden=el('planKind').value!=='building';
      el('planExport').disabled=!items.length;
      const list=el('planItems'); list.replaceChildren();
      items.forEach((item,index)=>{
        const m=measure(item), row=document.createElement('li'), title=document.createElement('strong'), detail=document.createElement('span');
        title.textContent=item.name;
        detail.textContent=`${kinds[item.kind]} · ${item.kind==='line'?fmt(m.length_m,'m'):fmt(m.area_m2,'m²')+' · Umfang '+fmt(m.length_m,'m')}${item.kind==='building'?' · '+item.floors+' Geschosse':''}`;
        const zoom=document.createElement('button');zoom.type='button';zoom.className='btn btn-ghost';zoom.textContent='Anzeigen';zoom.setAttribute('aria-label',item.name+' auf der Karte anzeigen');
        zoom.addEventListener('click',()=>{const p=unwrap(item.points);map.fitBounds([[Math.min(...p.map(x=>x[0])),Math.min(...p.map(x=>x[1]))],[Math.max(...p.map(x=>x[0])),Math.max(...p.map(x=>x[1]))]],{padding:options.padding?.()||60,maxZoom:19});});
        const remove=document.createElement('button');remove.type='button';remove.className='btn btn-ghost';remove.textContent='Löschen';remove.setAttribute('aria-label',item.name+' löschen');
        remove.addEventListener('click',()=>{items.splice(index,1);persist();update();notice('Geometrie gelöscht.');});
        row.append(title,detail,zoom,remove);list.append(row);
      });
      const b=balance(items);
      el('planBalance').textContent=`${b.count} Gebäude · Grundflächen gesamt ${fmt(b.footprint_m2,'m²')} · Geschossflächen geschätzt ${fmt(b.floor_area_m2,'m²')}. `+(b.warning||`Bezugsfläche ${fmt(b.site_m2,'m²')} · Bebaut ${fmt(b.coverage_percent,'%')} · Unbebaute Fläche ${fmt(b.free_m2,'m²')}.`);
    }
    function stop(message) {active=false;draft=[];map.getCanvas().style.cursor='';update();notice(message);}
    el('planStart').addEventListener('click',()=>{options.onStart?.();active=true;draft=[];map.getCanvas().style.cursor='crosshair';update();notice('Punkte auf der Karte setzen, dann „Übernehmen“. Esc verwirft die Skizze.');});
    el('planKind').addEventListener('change',update);
    el('planUndo').addEventListener('click',()=>{draft.pop();update();notice(`${draft.length} Punkte gesetzt.`);});
    el('planCancel').addEventListener('click',()=>stop('Skizze verworfen.'));
    el('planFinish').addEventListener('click',()=>{
      try {const item=draftItem();measure(item);items.push({...item,points:draft.map(p=>p.slice())});persist();stop('Geometrie übernommen. Entwurf wird in diesem Browser gespeichert.');} catch(e) {notice(e.message);}
    });
    el('planExport').addEventListener('click',()=>{
      const url=URL.createObjectURL(new Blob([JSON.stringify(collection(items),null,2)],{type:'application/geo+json'}));
      const a=document.createElement('a');a.href=url;a.download='messen-planen.geojson';a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);
    });
    document.addEventListener('keydown',e=>{if(e.key==='Escape'&&active){e.preventDefault();stop('Skizze verworfen.');}});
    update();notice('Geometrietyp wählen und mit „Zeichnen“ beginnen.');
    return {get active(){return active;},render,cancel(){if(active) stop('Skizze verworfen.');},addPoint(event){
      if(!active||event.originalEvent?.target?.closest?.('.maplibregl-marker, .maplibregl-popup, .micromap-marker, .micromap-popup')) return;
      if(draft.length>=500) return notice('Maximal 500 Punkte pro Geometrie.');
      const p=[((event.lngLat.lng+180)%360+360)%360-180,event.lngLat.lat];
      if(!p.every(Number.isFinite)||Math.abs(p[1])>85) return notice('Dieser Breitengrad wird nicht unterstützt.');
      if(draft.length&&distance(draft[0],p)>20000) return notice('Maximal 20 km Abstand zum ersten Punkt.');
      if(draft.length&&distance(draft.at(-1),p)<0.01) return;
      draft.push(p);update();
      try {const m=measure(draftItem());notice(`${draft.length} Punkte · ${fmt(m.length_m,'m')}${el('planKind').value==='line'?'':' Umfang · '+fmt(m.area_m2,'m²')}`);} catch(e) {notice(`${draft.length} Punkte · ${e.message}`);}
    }};
  }
  root.PlanningTools={distance,measure,balance,collection,restore,create};
})(typeof window==='undefined'?globalThis:window);

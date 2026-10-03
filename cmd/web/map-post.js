/* Compose a reviewable map image and accompanying text entirely in the browser. */
(function(root){
  'use strict';
  const formats={square:[1080,1080],landscape:[1200,630],portrait:[1080,1350]};
  function wrap(text,measure,width,maxLines) {
    const lines=[];
    for(const paragraph of String(text).split('\n')) {
      let line='';
      for(const word of paragraph.split(/\s+/).filter(Boolean)) {
        const candidate=line?line+' '+word:word;
        if(measure(candidate)<=width) {line=candidate;continue;}
        if(line) {lines.push(line);line='';}
        for(const char of word) {
          if(line&&measure(line+char)>width) {lines.push(line);line='';}
          line+=char;
        }
      }
      if(line) lines.push(line);
    }
    if(lines.length>maxLines) throw Error('Text passt nicht in das Bild. Bitte kürzen oder ein größeres Format wählen.');
    return lines;
  }
  function caption(fields,pin) {
    return [fields.title,fields.description,fields.location&&'Ort: '+fields.location,fields.date&&'Termin / Zeitraum: '+fields.date,pin&&`Kartenpunkt: ${pin[1].toFixed(6)}, ${pin[0].toFixed(6)}\nhttps://www.openstreetmap.org/?mlat=${pin[1].toFixed(6)}&mlon=${pin[0].toFixed(6)}#map=16/${pin[1].toFixed(6)}/${pin[0].toFixed(6)}`,fields.link&&'Weitere Informationen: '+fields.link,'Bildbeschreibung: '+fields.alt].filter(Boolean).join('\n\n');
  }
  function create(map) {
    const el=id=>document.getElementById(id), status=el('postStatus');
    let pin=null, imageURL=null, textURL=null, busy=false, revision=0;
    const fields=()=>Object.fromEntries(['title','description','location','date','link','alt'].map(k=>[k,el('post'+k[0].toUpperCase()+k.slice(1)).value.trim()]));
    function invalidate() {
      revision++;
      el('postPNG').hidden=true;el('postTXT').hidden=true;
      if(imageURL) URL.revokeObjectURL(imageURL);if(textURL) URL.revokeObjectURL(textURL);
      imageURL=null;textURL=null;el('postPreview').hidden=true;el('postPreview').removeAttribute('src');
      status.textContent='Ausschnitt und Texte einstellen, dann Vorschau erstellen.';
    }
    function render() {
      if(!map.isStyleLoaded()) return;
      const data={type:'FeatureCollection',features:pin?[{type:'Feature',properties:{},geometry:{type:'Point',coordinates:pin}}]:[]};
      if(map.getSource('map-post')) map.getSource('map-post').setData(data);
      else {map.addSource('map-post',{type:'geojson',data});map.addLayer({id:'map-post-pin',type:'circle',source:'map-post',paint:{'circle-radius':10,'circle-color':'#e11d48','circle-stroke-color':'#ffffff','circle-stroke-width':3}});}
    }
    el('postPin').addEventListener('click',()=>{const p=map.getCenter();pin=[((p.lng+180)%360+360)%360-180,p.lat];render();invalidate();status.textContent='Rote Ortsmarkierung an der Kartenmitte gesetzt.';});
    el('postUnpin').addEventListener('click',()=>{pin=null;render();invalidate();});
    el('mapPostTools').addEventListener('input',invalidate);
    map.on('moveend',invalidate);
    map.on('style.load',()=>{render();invalidate();});
    const capture=()=>{
      // microMap does not emit `render` for triggerRepaint on an idle map.
      // The loaded-tile check below makes its current composite safe to copy.
      if(root.mapRenderer==='micromap') return Promise.resolve().then(()=>root.MapRenderer.capture(map));
      return new Promise((resolve,reject)=>{
      const timeout=setTimeout(()=>{map.off('render',draw);reject(Error('Kartenbild konnte nicht erfasst werden. Bitte erneut versuchen.'));},5000);
      function draw(){clearTimeout(timeout);try{const source=root.MapRenderer ? root.MapRenderer.capture(map) : map.getCanvas(),copy=document.createElement('canvas');copy.width=source.width;copy.height=source.height;copy.getContext('2d').drawImage(source,0,0);resolve(copy);}catch(e){reject(e);}}
      map.once('render',draw);map.triggerRepaint();
      });
    };
    el('postGenerate').addEventListener('click',async()=>{
      if(busy)return;
      invalidate();
      const f=fields();
      if(!f.title||!f.alt){status.textContent='Bitte Überschrift und Bildbeschreibung ergänzen.';return;}
      if(f.link) {try {if(!['https:','http:'].includes(new URL(f.link).protocol))throw Error();}catch{status.textContent='Der Informationslink muss eine vollständige HTTP- oder HTTPS-Adresse sein.';return;}}
      if(!map.isStyleLoaded()||!map.areTilesLoaded()){status.textContent='Die Karte lädt noch. Bitte kurz warten und erneut versuchen.';return;}
      const generation=revision;
      busy=true;el('postGenerate').disabled=true;status.textContent='Vorschau wird erstellt …';
      try {
        const [w,h]=formats[el('postFormat').value], snapshot=await capture();
        const canvas=document.createElement('canvas');canvas.width=w;canvas.height=h;const ctx=canvas.getContext('2d');
        ctx.fillStyle='#ffffff';ctx.fillRect(0,0,w,h);ctx.fillStyle='#0f172a';ctx.textBaseline='top';
        function text(value,x,y,size,width,maxLines,color='#0f172a') {ctx.font=`${size>=32?'700':'400'} ${size}px system-ui, sans-serif`;ctx.fillStyle=color;const lines=wrap(value,s=>ctx.measureText(s).width,width,maxLines);lines.forEach((line,i)=>ctx.fillText(line,x,y+i*size*1.25));return lines.length*size*1.25;}
        let y=30;y+=text(f.title,36,y,38,w-72,2)+12;
        if(f.description)y+=text(f.description,36,y,23,w-72,3)+12;
        const whenWhere=[f.location,f.date].filter(Boolean).join(' · ');
        if(whenWhere)y+=text(whenWhere,36,y,21,w-72,2,'#475569')+12;
        const attribution=map.getContainer().querySelector('.maplibregl-ctrl-attrib-inner, .micromap-ctrl-attrib-inner')?.textContent?.replace(/\s+/g,' ').trim()||'© OpenStreetMap contributors';
        ctx.font='16px system-ui, sans-serif';
        const credits=wrap(attribution+' · openstreetmap.org/copyright',s=>ctx.measureText(s).width,w-72,6);
        const footer=credits.length*21+52, mapHeight=h-y-footer;
        if(mapHeight<150) throw Error('Zu wenig Platz für die Karte. Texte kürzen oder ein höheres Format wählen.');
        const scale=Math.min(w/snapshot.width,mapHeight/snapshot.height),dw=snapshot.width*scale,dh=snapshot.height*scale,dx=(w-dw)/2,dy=y+(mapHeight-dh)/2;
        ctx.fillStyle='#e2e8f0';ctx.fillRect(0,y,w,mapHeight);ctx.drawImage(snapshot,dx,dy,dw,dh);
        // Local offline labels are DOM overlays, so draw their visible text as well.
        const bounds=map.getCanvas().getBoundingClientRect(),sx=dw/bounds.width,sy=dh/bounds.height;
        ctx.save();ctx.beginPath();ctx.rect(dx,dy,dw,dh);ctx.clip();
        map.getContainer().querySelectorAll('.offline-map-label').forEach(label=>{
          const r=label.getBoundingClientRect();if(!r.width||!r.height)return;
          const x=dx+(r.left-bounds.left)*sx,yy=dy+(r.top-bounds.top)*sy;
          const css=getComputedStyle(label);ctx.font=`${css.fontWeight} ${Math.max(9,parseFloat(css.fontSize)*sy)}px system-ui, sans-serif`;ctx.fillStyle='#243247';ctx.strokeStyle='#ffffff';ctx.lineWidth=3;ctx.strokeText(label.textContent,x,yy);ctx.fillText(label.textContent,x,yy);
        });ctx.restore();
        let footY=h-footer+12;ctx.font='16px system-ui, sans-serif';ctx.fillStyle='#475569';
        credits.forEach(line=>{ctx.fillText(line,36,footY);footY+=21;});
        ctx.fillText('OSMmini · Kartenskizze, Angaben vor Veröffentlichung prüfen',36,h-30);
        const blob=await new Promise((resolve,reject)=>canvas.toBlob(b=>b?resolve(b):reject(Error('PNG konnte nicht erstellt werden.')),'image/png'));
        if(generation!==revision) {status.textContent='Karte oder Texte wurden geändert. Bitte die Vorschau erneut erstellen.';return;}
        imageURL=URL.createObjectURL(blob);textURL=URL.createObjectURL(new Blob([caption(f,pin)+'\n\nKartenquellen: '+attribution],{type:'text/plain;charset=utf-8'}));
        el('postPreview').src=imageURL;el('postPreview').alt=f.alt;el('postPreview').hidden=false;
        el('postPNG').href=imageURL;el('postPNG').hidden=false;el('postTXT').href=textURL;el('postTXT').hidden=false;
        status.textContent=`Vorschau bereit (${w} × ${h}). Bild und Begleittext prüfen und herunterladen.`;
      }catch(e){status.textContent=e.name==='SecurityError'?'Diese Kartenquelle erlaubt keinen Bildexport. Bitte eine andere Kartenquelle wählen.':e.message;}
      finally{busy=false;el('postGenerate').disabled=false;}
    });
    return {render,invalidate};
  }
  root.MapPost={wrap,caption,formats,create};
})(typeof window==='undefined'?globalThis:window);

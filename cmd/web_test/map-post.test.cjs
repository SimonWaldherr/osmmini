const {test}=require('node:test');
const assert=require('node:assert/strict');
require('../web/map-post.js');
test('image text wraps long words and explicit newlines without silent clipping',()=>{
 assert.deepEqual(MapPost.wrap('Eine neue\nRadverbindung',s=>s.length,10,4),['Eine neue','Radverbind','ung']);
 assert.deepEqual(MapPost.wrap('Hallo Welt noch',s=>s.length,10,2),['Hallo Welt','noch']);
 assert.throws(()=>MapPost.wrap('Viel zu viel Text',s=>s.length,4,2));
 assert.deepEqual(MapPost.wrap('🌍🌍',s=>Array.from(s).length,1,2),['🌍','🌍']);
});
test('companion text preserves user content, alt text and exact map location',()=>{
 const text=MapPost.caption({title:'Baustelle',description:'Durchfahrt gesperrt',location:'Am Markt',date:'Montag',link:'https://example.org/info',alt:'Rote Markierung am Markt'},[12.7,48.7]);
 for(const value of ['Baustelle','Durchfahrt gesperrt','Am Markt','Montag','https://example.org/info','Bildbeschreibung: Rote Markierung','mlat=48.700000&mlon=12.700000']) assert.ok(text.includes(value));
 assert.ok(!MapPost.caption({title:'Hinweis',alt:'Karte'},null).includes('Kartenpunkt'));
});
test('image export validates required text and produces PNG plus companion download',async()=>{
 class Element {
   constructor(){this.value='';this.hidden=false;this.listeners={};this.width=1200;this.height=800;}
   addEventListener(type,fn){this.listeners[type]=fn;}removeAttribute(name){delete this[name];}
   async click(){await this.listeners.click?.();}
   getContext(){return context;} toBlob(callback){callback(new Blob(['png'],{type:'image/png'}));}
   getBoundingClientRect(){return {left:0,top:0,width:1200,height:800};}
 }
 const drawn=[];
 const context={drawImage:()=>drawn.push('map'),fillRect(){},fillText:text=>drawn.push(text),measureText:text=>({width:text.length*10}),save(){},restore(){},beginPath(){},rect(){},clip(){}};
 const elements={};global.document={getElementById:id=>elements[id]??=new Element(),createElement:()=>new Element()};
 const source=new Element(),handlers={};let loaded=true,repaints=0;
 const map={on:(e,fn)=>handlers[e]=fn,once:(e,fn)=>handlers[e]=fn,off(){},triggerRepaint:()=>{repaints++;handlers.render();},getCanvas:()=>source,isStyleLoaded:()=>loaded,areTilesLoaded:()=>loaded,getContainer:()=>({querySelector:()=>({textContent:'© Testkartenquelle'}),querySelectorAll:()=>[]})};
 MapPost.create(map);elements.postFormat=new Element();elements.postFormat.value='square';
 await elements.postGenerate.click();assert.match(elements.postStatus.textContent,/Überschrift und Bildbeschreibung/);
 elements.postTitle.value='Neue Verbindung';elements.postAlt.value='Karte mit einer neuen Verbindung';
 elements.postLink.value='javascript:alert(1)';await elements.postGenerate.click();assert.match(elements.postStatus.textContent,/HTTP/);
 elements.postLink.value='https://example.org';loaded=false;await elements.postGenerate.click();assert.match(elements.postStatus.textContent,/lädt noch/);
 loaded=true;await elements.postGenerate.click();
 assert.equal(elements.postPNG.hidden,false);assert.match(elements.postPreview.src,/^blob:/);
 assert.equal(elements.postPreview.alt,elements.postAlt.value);
 assert.ok(drawn.includes('map'));assert.ok(drawn.some(x=>x.includes('Testkartenquelle')));
 assert.match(await (await fetch(elements.postTXT.href)).text(),/Bildbeschreibung: Karte mit einer neuen Verbindung/);
 handlers.moveend();assert.equal(elements.postPNG.hidden,true);assert.equal(elements.postPreview.src,undefined);
 // microMap needs a composite capture and never emits render on idle repaint.
 const originalRenderer=global.mapRenderer,originalAPI=global.MapRenderer;
 try {
   const previousRepaints=repaints;let captures=0;
   global.mapRenderer='micromap';global.MapRenderer={capture:()=>{captures++;return source;}};
   await elements.postGenerate.click();
   assert.equal(captures,1);assert.equal(repaints,previousRepaints);
   assert.equal(elements.postPNG.hidden,false);assert.match(elements.postStatus.textContent,/Vorschau bereit/);
 }finally{global.mapRenderer=originalRenderer;global.MapRenderer=originalAPI;}
 handlers.moveend();
});

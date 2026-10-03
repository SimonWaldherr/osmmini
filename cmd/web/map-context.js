/* Pointer and keyboard context actions for a precise map location. */
(function(root){
  'use strict';
  function create(map,actions){
    const container=map.getContainer(),canvas=map.getCanvas();
    const menu=document.createElement('div');menu.className='map-context-menu';menu.hidden=true;menu.setAttribute('role','menu');menu.setAttribute('aria-label','Aktionen an dieser Kartenposition');
    document.body.append(menu);
    canvas.setAttribute('aria-describedby','mapContextHint');
    let point=null,timer=null,touch=null,held=false,suppressUntil=0,returnFocus=canvas;
    const pointers=new Set(),buttons=[];
    const heading=document.createElement('div');heading.className='map-context-position';heading.setAttribute('aria-hidden','true');menu.append(heading);
    function cancelHold(){if(timer!==null)clearTimeout(timer);timer=null;touch=null;}
    function close(focus=false){const wasOpen=!menu.hidden;menu.hidden=true;if(focus&&wasOpen)returnFocus.focus?.();}
    for(const action of actions){
      const button=document.createElement('button');button.type='button';button.textContent=action.label;button.setAttribute('role','menuitem');button.tabIndex=-1;
      button.addEventListener('click',()=>{const selected={...point};close(true);action.run(selected);});
      menu.append(button);buttons.push(button);
    }
    function focusAt(index){buttons.forEach((b,i)=>{b.tabIndex=i===index?0:-1;});buttons[index]?.focus();}
    function openAt(clientX,clientY,location){
      cancelHold();const rect=canvas.getBoundingClientRect();
      const raw=location||map.unproject([clientX-rect.left,clientY-rect.top]);
      point={lng:((raw.lng+180)%360+360)%360-180,lat:raw.lat};
      if(!Number.isFinite(point.lng)||!Number.isFinite(point.lat))return;
      returnFocus=document.activeElement===document.body?canvas:document.activeElement||canvas;
      heading.textContent=`${point.lat.toFixed(5)}, ${point.lng.toFixed(5)}`;
      menu.hidden=false;
      menu.style.left='0px';menu.style.top='0px';
      menu.style.left=Math.max(8,Math.min(clientX,root.innerWidth-menu.offsetWidth-8))+'px';
      menu.style.top=Math.max(8,Math.min(clientY,root.innerHeight-menu.offsetHeight-8))+'px';
      focusAt(0);
    }
    const isControl=target=>target.closest?.('.maplibregl-ctrl, .maplibregl-popup, .micromap-ctrl-corner, .micromap-popup, button, input, select, textarea, a');
    container.addEventListener('contextmenu',event=>{
      if(isControl(event.target))return;
      event.preventDefault();event.stopPropagation();
      // Touch devices can emit a native contextmenu after our long press.
      if(held||Date.now()<suppressUntil)return;
      if(pointers.size){held=true;suppressUntil=Date.now()+1500;}
      openAt(event.clientX,event.clientY);
    },true);
    container.addEventListener('pointerdown',event=>{
      if(event.pointerType!=='touch'&&event.pointerType!=='pen')return;
      pointers.add(event.pointerId);cancelHold();
      if(pointers.size!==1||isControl(event.target))return;
      held=false;suppressUntil=0;
      touch={id:event.pointerId,x:event.clientX,y:event.clientY};
      const start=touch;
      timer=setTimeout(()=>{timer=null;if(touch!==start||pointers.size!==1)return;held=true;suppressUntil=Date.now()+1500;openAt(start.x,start.y);},550);
    },true);
    container.addEventListener('pointermove',event=>{
      if(touch&&event.pointerId===touch.id&&Math.hypot(event.clientX-touch.x,event.clientY-touch.y)>10)cancelHold();
    },true);
    function end(event){pointers.delete(event.pointerId);cancelHold();if(held){suppressUntil=Date.now()+750;held=false;}}
    container.addEventListener('pointerup',end,true);container.addEventListener('pointercancel',end,true);
    root.addEventListener('pointerup',end,true);root.addEventListener('pointercancel',end,true);
    container.addEventListener('click',event=>{
      if(Date.now()<suppressUntil){event.preventDefault();event.stopImmediatePropagation();}
    },true);
    canvas.addEventListener('keydown',event=>{
      if(event.key==='ContextMenu'||(event.shiftKey&&event.key==='F10')){
        event.preventDefault();const p=map.project(map.getCenter()),r=canvas.getBoundingClientRect();openAt(r.left+p.x,r.top+p.y,map.getCenter());
      }
    });
    menu.addEventListener('keydown',event=>{
      const index=buttons.indexOf(document.activeElement);
      if(event.key==='Escape'){event.preventDefault();event.stopPropagation();close(true);}
      else if(event.key==='Tab'){close(true);}
      else if(['ArrowDown','ArrowUp','Home','End'].includes(event.key)){
        event.preventDefault();focusAt(event.key==='Home'?0:event.key==='End'?buttons.length-1:(index+(event.key==='ArrowDown'?1:-1)+buttons.length)%buttons.length);
      }
    });
    document.addEventListener('pointerdown',event=>{if(!menu.contains(event.target))close();},true);
    root.addEventListener('resize',()=>close());root.addEventListener('blur',()=>{cancelHold();pointers.clear();held=false;suppressUntil=0;close();});
    map.on('movestart',()=>{cancelHold();close();});
    return {openAt,close};
  }
  root.MapContext={create};
})(typeof window==='undefined'?globalThis:window);

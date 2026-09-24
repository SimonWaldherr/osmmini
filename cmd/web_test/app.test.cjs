const assert = require('node:assert/strict');
const { test } = require('node:test');
const { readFileSync } = require('node:fs');
const vm = require('node:vm');

// Exercise the shipped functions with controlled network completion and DOM
// events. No MapLibre, external tiles or third-party test runtime is needed.
const source = readFileSync(require('node:path').join(__dirname, '../web/app.js'), 'utf8');
function section(start, end) {
  const a = source.indexOf(start);
  const b = source.indexOf(end, a);
  assert.ok(a >= 0 && b > a, `missing source section: ${start}`);
  return source.slice(a, b);
}
function deferred() {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
class Element extends EventTarget {
  constructor() {
    super();
    this.children = [];
    this.attributes = {};
    this.style = {};
    this.dataset = {};
    this.value = '';
    this.classList = { toggle() {} };
  }
  setAttribute(k, v) { this.attributes[k] = v; }
  removeAttribute(k) { delete this.attributes[k]; }
  set innerHTML(value) { this.children = []; this.html = value; }
  get innerHTML() { return this.html || (this.children.length ? '<children>' : ''); }
  appendChild(child) { this.children.push(child); }
  querySelectorAll() { return this.children.filter(c => c.className === 'item'); }
  querySelector() { return null; }
  contains(target) { return target === this || this.children.includes(target); }
  focus() {}
}
function searchHarness() {
  const input = new Element();
  const container = new Element();
  const doc = new Element();
  doc.getElementById = () => container;
  doc.createElement = () => new Element();
  const timers = new Map();
  let id = 0;
  const requests = [], toasts = [], rendered = [];
  let cancellations = 0;
  const context = vm.createContext({
    document: doc, AbortController,
    setTimeout(fn) { timers.set(++id, fn); return id; },
    clearTimeout(key) { timers.delete(key); },
    fetch(url, options) {
      const response = deferred();
      requests.push({ url, options, ...response });
      return response.promise;
    },
    cancelRouteComputation() { cancellations++; }, clearRouteLocationChoices() {},
    applySearchResultToInput(input, item) { input.value = item.label; return true; },
    getResultPrimary: item => item.label, getResultSecondary: () => '',
    highlight: value => value, escapeHtml: value => value,
    showSearchResultsOnMap: data => rendered.push(data),
    showToast: (...args) => toasts.push(args),
  });
  vm.runInContext(section('function makeSuggest(', '\nconst SEARCH_SOURCE_ID'), context);
  const handle = context.makeSuggest('results', input);
  return {
    input, container, requests, toasts, rendered, handle,
    get cancellations() { return cancellations; },
    type(value) { input.value = value; input.dispatchEvent(new Event('input')); },
    key(key) { const event = new Event('keydown'); event.key = key; input.dispatchEvent(event); },
    flush() { const jobs = [...timers.values()]; timers.clear(); return jobs.map(fn => fn()); },
  };
}

test('search ignores a stale JSON body during the next debounce interval', async () => {
  const h = searchHarness();
  h.type('Berlin');
  assert.equal(h.input.attributes['aria-expanded'], 'true');
  const [first] = h.flush();
  const body = deferred();
  h.requests[0].resolve({ ok: true, json: () => body.promise });
  await Promise.resolve();
  h.type('Hamburg');
  assert.equal(h.requests[0].options.signal.aborted, true);
  body.resolve([{ label: 'Berlin' }]);
  await first;
  assert.equal(h.rendered.length, 0);
  assert.equal(h.container.children[0].textContent, 'Suche…');
  const [second] = h.flush();
  h.requests[1].resolve({ ok: true, json: async () => [{ label: 'Hamburg' }] });
  await second;
  assert.equal(h.rendered[0][0].label, 'Hamburg');
});

test('clearing a search allows the same query to run again', async () => {
  const h = searchHarness();
  h.type('Berlin');
  const [first] = h.flush();
  h.type('');
  h.requests[0].resolve({ ok: true, json: async () => [{ label: 'Berlin' }] });
  await first;
  assert.equal(h.container.style.display, 'none');
  assert.equal(h.rendered.length, 0);
  h.type('Berlin');
  const [retry] = h.flush();
  assert.equal(h.requests.length, 2);
  h.requests[1].resolve({ ok: true, json: async () => [] });
  await retry;
});

test('Escape cancels both scheduled and running searches', async () => {
  const h = searchHarness();
  h.type('Berlin');
  h.key('Escape');
  assert.equal(h.flush().length, 0);
  h.type('Berlin');
  const [pending] = h.flush();
  h.key('Escape');
  assert.equal(h.requests[0].options.signal.aborted, true);
  h.requests[0].resolve({ ok: false, status: 500 });
  await pending;
  assert.equal(h.toasts.length, 0);
  assert.equal(h.input.attributes['aria-expanded'], 'false');
});

test('destroy detaches input handlers and silences late network failures', async () => {
  const h = searchHarness();
  h.type('Berlin');
  const [pending] = h.flush();
  h.handle.destroy();
  h.requests[0].reject(new Error('connection lost'));
  await pending;
  h.type('Hamburg');
  assert.equal(h.flush().length, 0);
  assert.equal(h.toasts.length, 0);
});

function routeHarness() {
  const elements = Object.fromEntries(['from', 'to', 'status', 'routeDetails', 'routeActions', 'optimize'].map(id => [id, new Element()]));
  elements.from.value = 'Start';
  elements.to.value = 'Ziel';
  const requests = [], rendered = [], toasts = [], busy = [];
  const request = (from, to, options, signal) => {
    const pending = deferred();
    requests.push({ signal, ...pending });
    return pending.promise;
  };
  const context = vm.createContext({
    AbortController, console,
    document: { getElementById: id => elements[id] },
    debouncedCompute: { cancel() {} },
    routeLocationForInput: input => ({ query: input.value }),
    routeLocationHasValue: location => Boolean(location.query),
    syncInputClearState() {}, routeOptionsFromUI: () => ({}),
    setMapsLinks() {}, showSpinner() {}, setComputeDisabled: value => busy.push(value),
    waypoints: [], stops: [], apiRoute: request, apiTripSolve: request,
    renderPath: (path, data) => rendered.push(data),
    setResolvedRouteResponsePoint() {}, clearRouteLocationChoices() {},
    renderManeuvers() {}, renderStopList() {}, renderDisambiguationButtons() {},
    showToast: (...args) => toasts.push(args),
  });
  vm.runInContext(section('let routeRequest = null;', '\nfunction formatMeters('), context);
  return { context, elements, requests, rendered, toasts, busy };
}

test('only the latest route may render or release the busy state', async () => {
  const h = routeHarness();
  const first = h.context.compute();
  const second = h.context.compute();
  assert.equal(h.requests[0].signal.aborted, true);
  h.requests[0].resolve({ path: ['old'], distance_m: 100 });
  await first;
  assert.equal(h.rendered.length, 0);
  assert.equal(h.busy.at(-1), true);
  h.requests[1].resolve({ path: ['new'], distance_m: 200 });
  await second;
  assert.equal(h.rendered.length, 1);
  assert.equal(h.rendered[0].distance_m, 200);
  assert.equal(h.busy.at(-1), false);
});

test('reset cancels an in-flight trip without restoring its result', async () => {
  const h = routeHarness();
  h.context.stops.push({ id: 'stop' });
  const pending = h.context.compute();
  h.context.cancelRouteComputation();
  assert.equal(h.requests[0].signal.aborted, true);
  h.requests[0].resolve({ path: ['stale trip'], distance_m: 100 });
  await pending;
  assert.equal(h.rendered.length, 0);
  assert.equal(h.toasts.length, 0);
  assert.equal(h.elements.status.textContent, 'Bereit');
  assert.equal(h.busy.at(-1), false);
});

test('a failed superseded route cannot replace the current status', async () => {
  const h = routeHarness();
  const first = h.context.compute();
  const second = h.context.compute();
  h.requests[0].reject(new Error('old request failed'));
  await first;
  assert.equal(h.elements.status.textContent, 'Berechne...');
  assert.equal(h.toasts.length, 0);
  h.requests[1].resolve({ path: [], distance_m: 200 });
  await second;
});

test('swapping endpoints invalidates a pending search without editing its label', async () => {
  const h = searchHarness();
  h.type('Berlin');
  const [pending] = h.flush();
  h.input.value = 'Hamburg';
  h.input.dispatchEvent(new Event('routepointchange'));
  h.requests[0].resolve({ ok: true, json: async () => [{ label: 'Berlin' }] });
  await pending;
  assert.equal(h.input.value, 'Hamburg');
  assert.equal(h.container.style.display, 'none');
  assert.equal(h.rendered.length, 0);
});

test('route and trip clients pass the cancellation signal to fetch', async () => {
  const calls = [];
  const context = vm.createContext({
    fetch: async (url, options) => { calls.push({ url, options }); return { ok: true, json: async () => ({}) }; },
    document: { getElementById: () => ({ checked: false, value: '' }) },
    waypoints: [], stops: [],
  });
  vm.runInContext(section('async function apiRoute(', '\nfunction clearRouteLocationChoices('), context);
  const controller = new AbortController();
  await context.apiRoute({ query: 'start' }, { query: 'end' }, {}, controller.signal);
  await context.apiTripSolve({ query: 'start' }, { query: 'end' }, {}, controller.signal);
  assert.equal(calls.length, 2);
  for (const call of calls) {
    assert.equal(call.options.signal, controller.signal);
    assert.equal(call.options.method, 'POST');
  }
});

test('batch waypoint removal does not issue intermediate route requests', () => {
  let computes = 0, destroyed = 0, removed = 0;
  const context = vm.createContext({
    compute() { computes++; },
    waypoints: [1, 2, 3].map(id => ({ id, suggestHandle: { destroy() { destroyed++; } }, wrapper: { remove() { removed++; } } })),
  });
  vm.runInContext(section('function removeWaypoint(', '\nfunction makeSuggest('), context);
  context.removeWaypoint(1, false);
  context.removeWaypoint(2, false);
  assert.equal(computes, 0);
  context.removeWaypoint(3);
  assert.equal(computes, 1);
  assert.equal(destroyed, 3);
  assert.equal(removed, 3);
});

test('cancelled debounce cannot restart a reset route', () => {
  const timers = new Map();
  const context = vm.createContext({
    setTimeout(fn) { timers.set(1, fn); return 1; },
    clearTimeout(id) { timers.delete(id); },
  });
  vm.runInContext(section('function debounce(', '\n// debounced wrapper'), context);
  let calls = 0;
  const run = context.debounce(() => calls++, 300);
  run();
  run.cancel();
  for (const fn of timers.values()) fn();
  assert.equal(calls, 0);
});


test('selecting a suggestion cancels pending automatic route work', async () => {
  const h = searchHarness();
  h.type('Berlin');
  const [pending] = h.flush();
  h.requests[0].resolve({ ok: true, json: async () => [{ label: 'Berlin Hauptbahnhof' }] });
  await pending;
  const before = h.cancellations;
  h.container.children[0].onclick();
  assert.equal(h.cancellations, before + 1);
  assert.equal(h.input.value, 'Berlin Hauptbahnhof');
  assert.equal(h.input.attributes['aria-expanded'], 'false');
});

test('route inputs preserve selected addresses across focus without polling', () => {
  const containers = { 'from-container': new Element(), 'to-container': new Element() };
  const context = vm.createContext({
    document: { getElementById: id => containers[id], createElement: () => new Element() },
    clearResolvedRoutePoint() {},
    setInterval() { throw new Error('route inputs must not poll'); },
  });
  vm.runInContext(section('function preventAutofill()', '\n// Call this immediately'), context);
  context.preventAutofill();
  const input = containers['from-container'].children[0].children[0];
  input.value = 'Hauptstraße 5';
  input.dispatchEvent(new Event('focus'));
  input.dispatchEvent(new Event('change'));
  input.dispatchEvent(new Event('blur'));
  assert.equal(input.value, 'Hauptstraße 5');
  assert.equal(input.attributes.autocomplete, 'off');
});

function gisHarness() {
  const status = new Element();
  const timers = new Map();
  const requests = [];
  let timerID = 0;
  const context = vm.createContext({
    AbortController, URLSearchParams,
    map: { isStyleLoaded: () => false, getSource: () => null, once() {} },
    document: { getElementById: () => status },
    registerMapLayerRehydrate() {},
    formatMeters: value => String(value),
    setTimeout(fn) { timers.set(++timerID, fn); return timerID; },
    clearTimeout(id) { timers.delete(id); },
    fetch(url, options) { const request = deferred(); requests.push({ url, options, ...request }); return request.promise; },
  });
  vm.runInContext(section('let gisMeasureActive = false;', "document.getElementById('gisViewport')"), context);
  return { context, status, requests, flush() { const jobs = [...timers.values()]; timers.clear(); return jobs.map(fn => fn()); } };
}

test('GIS measurement clearing aborts work and ignores delayed results', async () => {
  const h = gisHarness();
  vm.runInContext('gisMeasurePoints = [[12,48],[12.01,48.01]]; updateGISMeasurement();', h.context);
  const [pending] = h.flush();
  assert.equal(h.requests.length, 1);
  vm.runInContext('gisMeasurePoints = []; updateGISMeasurement();', h.context);
  assert.equal(h.requests[0].options.signal.aborted, true);
  h.requests[0].resolve({ ok: true, json: async () => ({ length_m: 1000 }) });
  await pending;
  assert.match(h.status.textContent, /^0 Messpunkte/);
});

test('GIS display unwraps antimeridian segments without changing submitted positions', () => {
  const h = gisHarness();
  const input = [[179,0],[-179,1],[178,2]];
  const output = h.context.gisDisplayCoordinates(input);
  assert.equal(output[1][0], 181);
  assert.equal(input[1][0], -179);
  assert.equal(h.context.gisLongitude(540), -180);
});


test('GIS measurement updates its source while the map is loading data', () => {
  const h = gisHarness();
  let rendered;
  h.context.map.getSource = () => ({ setData(data) { rendered = data; } });
  vm.runInContext('gisMeasurePoints = [[12,48],[12.01,48.01]]; renderGISMeasurement();', h.context);
  assert.equal(rendered.features.length, 3);
  assert.equal(rendered.features[2].geometry.type, 'LineString');
});

test('offline label collisions preserve touching edges and reject overlapping names', () => {
  const context = vm.createContext({});
  vm.runInContext(section('function offlineLabelBoxesOverlap(', '\nfunction setOfflineLabelsVisible'), context);
  const box = {left: 10, right: 100, top: 10, bottom: 30};
  assert.equal(context.offlineLabelBoxesOverlap(box, {left: 90, right: 150, top: 20, bottom: 40}), true);
  assert.equal(context.offlineLabelBoxesOverlap(box, {left: 100, right: 150, top: 10, bottom: 30}), false);
  assert.equal(context.offlineLabelBoxesOverlap(box, {left: 10, right: 100, top: 31, bottom: 50}), false);
});

test('AI destination choices route to selected coordinates and retain an existing start', async () => {
  const from = new Element(); from.value = 'Gewählter Start';
  const to = new Element();
  const container = new Element();
  let selected, computes = 0;
  const context = vm.createContext({
    document: { createElement: () => new Element(), getElementById: id => id === 'from' ? from : to },
    setResolvedSearchResult(input, suggestion) { selected = suggestion; input.value = suggestion.label; return true; },
    clearResolvedRoutePoint() {}, syncInputClearState() {},
    async compute() { computes++; },
  });
  vm.runInContext(section('function appendAITargetChoices(', 'function mapsCameraPadding('), context);
  context.appendAITargetChoices(container, { from: {query:'48.6,12.7'}, suggestions:[{label:'FOCUS Cinemas',lat:48.78,lon:12.87}] });
  const button = container.children[0].children[0];
  assert.equal(button.textContent, 'FOCUS Cinemas');
  button.dispatchEvent(new Event('click'));
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(selected.lon,12.87);
  assert.equal(from.value,'Gewählter Start');
  assert.equal(computes,1);
  assert.equal(button.disabled,false);
});

test('AI context preserves selected coordinates and exact route totals', () => {
  const context = vm.createContext({ resolvedRoutePoint: input => input.point });
  vm.runInContext(section('function aiRouteContext(', '// Clear chat history'), context);
  const result = context.aiRouteContext({value:'Selected address', point:{lat:48.6,lon:12.7}}, {value:'Dingolfing'}, {distance_m:12345.67,duration_s:987.6,engine:'local'});
  assert.equal(result.route_from, '48.6,12.7');
  assert.equal(result.route_to, 'Dingolfing');
  assert.equal(result.route_dist_m, 12345.67);
  assert.equal(result.route_dur_s, 987.6);
  assert.equal(context.aiRouteContext({value:''}, {value:''}, null).route_dur_s, 0);
});

test('AI status keeps local navigation available and filters non-chat models', async () => {
  const elements = Object.fromEntries(['aiStatus','aiModelSelect','aiSend','aiModel','aiStatusBadge'].map(id => [id,new Element()]));
  elements.aiModel.replaceChildren = function(){this.children=[];};
  let providers = [];
  const context = vm.createContext({document:{getElementById:id=>elements[id],createElement:()=>new Element()}, aiRequestController:null, fetch:async()=>({ok:true,json:async()=>({providers})})});
  vm.runInContext(section('async function checkAIStatus()', 'async function sendAIQuery()'),context);
  await context.checkAIStatus();
  assert.equal(elements.aiSend.disabled,false);
  assert.match(elements.aiStatus.textContent,/ohne Sprachmodell/);
  providers = [{name:'local',available:true,models:['embed-small','chat-model','reranker']}];
  await context.checkAIStatus();
  assert.equal(elements.aiModel.children.length,1);
  assert.equal(elements.aiModel.children[0].value,'chat-model');
});

test('cancelled AI responses cannot overwrite a newer request', async () => {
  const elements = Object.fromEntries(['aiPrompt','aiStop','aiSend','aiMessages','aiModel','from','to'].map(id=>[id,new Element()]));
  const requests=[];
  const context=vm.createContext({AbortController,console,document:{getElementById:id=>elements[id],createElement:()=>new Element()},currentRouteMeta:null,currentRouteBBox:null,userLocation:null,map:{getCenter:()=>({lat:48, lng:12})},getAISessionId:()=>'',resolvedRoutePoint:()=>null,cancelRouteComputation(){},fetch:(_url,options)=>{const d=deferred();requests.push({...d,options});return d.promise;},escapeHtml:s=>s||'',setAISessionId(){}});
  vm.runInContext('let aiRequestController = null; let aiRequestMessage = null;\n' + section('function cancelAIQuery()', '// Clear chat history'),context);
  vm.runInContext(section('async function sendAIQuery()', "document.getElementById('aiSend')?.addEventListener"),context);
  elements.aiPrompt.value='Hallo';
  const first=context.sendAIQuery();
  elements.aiPrompt.value='Hallo nochmals';
  await context.sendAIQuery();
  assert.equal(requests.length,1);
  context.cancelAIQuery();
  assert.equal(requests[0].options.signal.aborted,true);
  const second=context.sendAIQuery();
  requests[0].resolve({ok:true,json:async()=>({response:'STALE'})});
  await first;
  assert.equal(elements.aiSend.disabled,true);
  requests[1].resolve({ok:true,json:async()=>({response:'CURRENT'})});
  await second;
  assert.equal(elements.aiSend.disabled,false);
  assert.equal(elements.aiStop.hidden,true);
  assert.match(elements.aiMessages.children.at(-1).innerHTML,/CURRENT/);
  assert.doesNotMatch(elements.aiMessages.children[1].innerHTML,/STALE/);
});

function placesHarness() {
  const elements = Object.fromEntries(['placeSearch','placeResults','placeSearchStatus','placeResultsTitle','clearPlaces','to','from'].map(id => [id, new Element()]));
  function element() {
    const el = new Element();
    el.append = (...children) => el.children.push(...children);
    el.replaceChildren = (...children) => { el.children = children; };
    return el;
  }
  elements.placeResults = element();
  const requests = [], views = [], markers = [], selections = [];
  let clears = 0;
  const context = vm.createContext({
    document: {getElementById: id => elements[id], querySelector: () => null, querySelectorAll: () => [], createElement: element},
    AbortController, URLSearchParams, clearTimeout,
    map: {getCenter: () => ({lat:48.63,lng:12.49}), flyTo(){}},
    setMapsView: view => views.push(view),
    normalizeSearchResult: value => Number.isFinite(value.lat) && Number.isFinite(value.lon) ? value : null,
    getResultPrimary: value => value.label, getResultSecondary: () => '',
    showSearchResultsOnMap: values => markers.push(values),
    clearSearchResults() { clears++; },
    applySearchResultToInput: (input, place) => selections.push({input, place}),
    fetch(url, options) { const response = deferred(); requests.push({url, options, ...response}); return response.promise; },
  });
  vm.runInContext(section('let placeSearchRequest = null;', "document.getElementById('placeSearchForm')?"), context);
  return {elements, requests, views, markers, selections, context, get clears() { return clears; }};
}

test('discovery search discards an old response after a newer search completes', async () => {
  const h = placesHarness();
  h.elements.placeSearch.value = 'old';
  const first = h.context.searchPlaces();
  h.elements.placeSearch.value = 'new';
  const second = h.context.searchPlaces();
  assert.equal(h.requests[0].options.signal.aborted, true);
  h.requests[1].resolve({ok:true,json:async()=>[{label:'New',lat:48,lon:12}]});
  await second;
  h.requests[0].resolve({ok:true,json:async()=>[{label:'Old',lat:49,lon:13}]});
  await first;
  assert.equal(h.markers.length, 1);
  assert.equal(h.markers[0][0].label, 'New');
  assert.equal(h.elements.placeSearchStatus.textContent, '1 Orte gefunden');
});

test('category search uses the map center and preserves coordinates for routing', async () => {
  const h = placesHarness();
  const pending = h.context.searchPlaces('cafe');
  const url = new URL(h.requests[0].url, 'http://localhost');
  assert.equal(url.pathname, '/api/v1/geo/pois');
  assert.equal(url.searchParams.get('radius_m'), '10000');
  assert.equal(url.searchParams.get('lat'), '48.63');
  h.requests[0].resolve({ok:true,json:async()=>({features:[{properties:{osm_id:123,kind:'node',label:'Café'},geometry:{coordinates:[12.49,48.63]}}]})});
  await pending;
  h.elements.placeResults.children[0].children[1].dispatchEvent(new Event('click'));
  assert.equal(h.selections[0].input, h.elements.to);
  assert.equal(h.selections[0].place.lon, 12.49);
  assert.equal(h.views.at(-1), 'route');
});

test('discovery search provides empty and error states without showing stale rows', async () => {
  const h = placesHarness();
  h.elements.placeSearch.value = 'nothing';
  const empty = h.context.searchPlaces();
  h.requests[0].resolve({ok:true,json:async()=>[]});
  await empty;
  assert.match(h.elements.placeSearchStatus.textContent, /Keine Orte gefunden/);
  const failed = h.context.searchPlaces();
  h.requests[1].resolve({ok:false});
  await failed;
  assert.match(h.elements.placeSearchStatus.textContent, /erneut versuchen/);
  assert.equal(h.elements.placeResults.children.length, 0);
});

test('AI starters distinguish map center from user location and require a route for route context', () => {
  const context=vm.createContext({});
  vm.runInContext(section('function aiStarterPrompt(', 'function prepareAIQuestion('),context);
  assert.match(context.aiStarterPrompt('nearby',{lat:48.63,lng:12.49},null), /Kartenmitte bei 48.630000, 12.490000/);
  assert.match(context.aiStarterPrompt('route',{lat:48,lng:12},null), /zuerst nach Start und Ziel/);
  assert.match(context.aiStarterPrompt('route',{lat:48,lng:12},{distance_m:1000}), /berechneten Entfernung/);
  assert.match(context.aiStarterPrompt('circle',{lat:48,lng:12},null), /1000 Metern/);
});

test('preparing an AI question preserves a user draft and never sends it', () => {
  const input=new Element(); input.value='Mein Entwurf';
  const views=[],toasts=[];
  const context=vm.createContext({document:{getElementById:()=>input},setMapsView:v=>views.push(v),showToast:t=>toasts.push(t)});
  vm.runInContext(section('function prepareAIQuestion(', "document.querySelectorAll('[data-ai-starter]')"),context);
  context.prepareAIQuestion('Vorschlag');
  assert.equal(input.value,'Mein Entwurf');
  assert.equal(toasts.length,1);
  input.value='';
  context.prepareAIQuestion('Vorschlag');
  assert.equal(input.value,'Vorschlag');
  assert.deepEqual(views,['assistant','assistant']);
});

test('restoring an open AI panel initializes request state before checking model availability', () => {
  const elements=Object.fromEntries(['aiToggle','aiBody','aiCard','aiCardHeader'].map(id=>[id,new Element()]));
  const context=vm.createContext({document:{getElementById:id=>elements[id],querySelector:()=>null},localStorage:{getItem:()=> '1',setItem(){}},wireCollapsibleHeader(){}});
  vm.runInContext('let observed; function checkAIStatus(){ observed={models:aiModels.length,busy:aiRequestController!==null}; }',context);
  vm.runInContext(section('// ---- AI Integration ----', '// Persist AI session'),context);
  assert.equal(vm.runInContext('observed.models',context),0);
  assert.equal(vm.runInContext('observed.busy',context),false);
  assert.equal(elements.aiBody.style.display,'block');
});

test('compact panel hides its controls and restores content without losing state', () => {
  const classes = new Set(['panel-expanded']);
  const elements = Object.fromEntries(['mapsPanelContent','mapsPanelToggle','mapsPanelExpand'].map(id => [id,new Element()]));
  const shell = {classList:{toggle(name, enabled){if(enabled) classes.add(name); else classes.delete(name);},remove(name){classes.delete(name);}}};
  let paddingUpdates=0;
  const context=vm.createContext({document:{querySelector:()=>shell,getElementById:id=>elements[id]},map:{setPadding(){paddingUpdates++;}},mapsCameraPadding:()=>({left:40})});
  vm.runInContext(section('function setMapsPanelCollapsed(', "document.getElementById('mapsPanelToggle')?"),context);
  context.setMapsPanelCollapsed(true);
  assert.equal(elements.mapsPanelContent.hidden,true);
  assert.equal(elements.mapsPanelToggle.attributes['aria-expanded'],'false');
  assert.equal(elements.mapsPanelToggle.textContent,'Details zeigen');
  assert.equal(classes.has('panel-expanded'),false);
  assert.equal(elements.mapsPanelExpand.attributes['aria-pressed'],'false');
  context.setMapsPanelCollapsed(false);
  assert.equal(elements.mapsPanelContent.hidden,false);
  assert.equal(elements.mapsPanelToggle.attributes['aria-expanded'],'true');
  assert.equal(paddingUpdates,2);
});

test('AI composer supports multiline text and does not send during IME composition', () => {
  let handler,sent=0;
  const context=vm.createContext({document:{getElementById:()=>({addEventListener:(_name,fn)=>handler=fn})},sendAIQuery(){sent++;}});
  vm.runInContext(section("document.getElementById('aiPrompt')?.addEventListener('keydown'", '// Visual feedback on button clicks'),context);
  handler({key:'Enter',shiftKey:true,preventDefault(){throw Error('newline blocked');}});
  handler({key:'Enter',isComposing:true,preventDefault(){throw Error('composition blocked');}});
  let prevented=false;
  handler({key:'Enter',preventDefault(){prevented=true;}});
  assert.equal(sent,1);
  assert.equal(prevented,true);
});


test('clearing discovery cancels pending work and removes old markers and result state', async () => {
  const h = placesHarness();
  vm.runInContext(section('function resetPlaceSearch(', "document.getElementById('clearPlaces')?.addEventListener"), h.context);
  h.elements.placeSearch.value = 'Museum';
  const pending = h.context.searchPlaces();
  assert.equal(h.clears, 1);
  assert.equal(h.elements.placeResults.attributes['aria-busy'], 'true');
  h.context.resetPlaceSearch();
  assert.equal(h.requests[0].options.signal.aborted, true);
  assert.equal(h.elements.placeSearch.value, '');
  assert.equal(h.elements.clearPlaces.hidden, true);
  assert.equal(h.elements.placeResults.attributes['aria-busy'], 'false');
  assert.equal(h.clears, 2);
  h.requests[0].resolve({ok:true,json:async()=>[{label:'Stale',lat:48,lon:12}]});
  await pending;
  assert.equal(h.markers.length, 0);
  assert.equal(h.elements.placeResults.children.length, 0);
});

test('GPX export escapes labels and contains waypoints, maneuvers and track', () => {
  const context = vm.createContext({});
  vm.runInContext(section('function xmlEscape(', "\ndocument.getElementById('exportRoute')"), context);
  const gpx = context.buildRouteGPX(
    [{ lat: 48, lng: 12 }, { lat: 48.001, lng: 12.0005 }],
    {
      from: { label: 'Café <A&B>', lat: 48, lon: 12 },
      to: { label: 'Ziel "Nord"', lat: 48.001, lon: 12.0005 },
      steps: [{ type: 'depart', instruction: 'Losfahren', lat: 48, lon: 12 }],
    },
  );
  assert.match(gpx, /^<\?xml version="1\.0" encoding="UTF-8"\?>\n<gpx version="1\.1"/);
  assert.match(gpx, /<name>Café &lt;A&amp;B&gt; → Ziel &quot;Nord&quot;<\/name>/);
  assert.equal((gpx.match(/<wpt /g) || []).length, 2);
  assert.equal((gpx.match(/<rtept /g) || []).length, 1);
  assert.equal((gpx.match(/<trkpt /g) || []).length, 2);
  assert.match(gpx, /<trkpt lat="48\.0010000" lon="12\.0005000">/);
});

test('GPX export of a trip lists numbered stops and leg maneuvers', () => {
  const context = vm.createContext({});
  vm.runInContext(section('function xmlEscape(', "\ndocument.getElementById('exportRoute')"), context);
  const gpx = context.buildRouteGPX([{ lat: 1, lng: 2 }], {
    stops: [{ id: 'S1', label: 'Kunde', lat: 1, lon: 2 }],
    legs: [{ steps: [{ type: 'arrive', instruction: 'Ankunft', lat: 1, lon: 2 }] }],
  });
  assert.match(gpx, /<name>1\. Kunde<\/name><type>stop<\/type>/);
  assert.match(gpx, /<name>Ankunft<\/name>/);
  assert.match(gpx, /<metadata><name>OSMmini Route<\/name>/);
});

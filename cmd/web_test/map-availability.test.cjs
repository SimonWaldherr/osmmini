const {test} = require('node:test');
const assert = require('node:assert/strict');
const {readFileSync} = require('node:fs');
const vm = require('node:vm');
const source = readFileSync(require.resolve('../web/app.js'), 'utf8');
const offline = {style_url: '/static/styles/tinytiles-minimal.json'};

function harness(tiles = offline) {
  const ids = ['mapAvailability', 'mapAvailabilityTitle', 'mapAvailabilityMessage',
    'mapAvailabilityOffline', 'mapModeTitle'];
  const elements = Object.fromEntries(ids.map(id => [id, {hidden: true, textContent: ''}]));
  const context = vm.createContext({
    preloadedSettings: {tiles}, window: {},
    document: {getElementById: id => elements[id], querySelector: () => null},
  });
  vm.runInContext(source.slice(source.indexOf('let mapAvailabilityTiles ='),
    source.indexOf('function queueOfflineLabels(')), context);
  return {context, elements};
}

test('initial offline availability does not depend on the first map paint', () => {
  const {context, elements} = harness();
  context.updateMapAvailabilityUI();
  assert.equal(elements.mapAvailability.hidden, true);
  context.updateMapAvailabilityUI({state: 'idle'});
  assert.equal(elements.mapAvailability.hidden, false);
  assert.match(elements.mapAvailabilityTitle.textContent, /fehlt/);
  assert.equal(elements.mapModeTitle.textContent, 'Offline-Karte ausgewählt');
});

test('building, failure and readiness update recovery actions without exposing server errors', () => {
  const {context, elements} = harness();
  context.updateMapAvailabilityUI({state: 'building'});
  assert.equal(elements.mapAvailability.hidden, false);
  assert.equal(elements.mapAvailabilityOffline.textContent, 'Fortschritt ansehen');
  context.updateMapAvailabilityUI({state: 'failed', error: '<private server details>'});
  assert.equal(elements.mapAvailabilityOffline.textContent, 'Vorbereitung öffnen');
  assert.ok(!elements.mapAvailabilityMessage.textContent.includes('private server'));
  context.updateMapAvailabilityUI({state: 'ready'});
  assert.equal(elements.mapAvailability.hidden, true);
  assert.equal(elements.mapModeTitle.textContent, 'Offline-Karte aktiv');
});

test('late offline status cannot show a warning over a selected online map', () => {
  const {context, elements} = harness();
  context.updateMapAvailabilityUI({state: 'idle'});
  context.updateMapModeUI({style_url: 'https://example.org/style.json'});
  for (const state of ['idle', 'building', 'failed', 'ready']) {
    context.updateMapAvailabilityUI({state});
    assert.equal(elements.mapAvailability.hidden, true, state);
  }
});

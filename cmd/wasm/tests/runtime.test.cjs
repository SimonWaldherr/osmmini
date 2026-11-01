const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { before, test } = require('node:test');

const directory = path.resolve(process.env.OSMMINI_WASM_DIR || 'local/wasm');
const graph = {
  nodes: [{ id: 1, lat: 48, lon: 12 }, { id: 2, lat: 48.001, lon: 12.001 }],
  edges: [{ from: 1, to: 2, dist_m: 120, speed_kph: 30 }],
};
const route = () => osmRoute(48, 12, 48.001, 12.001, 'astar', 'distance');

before(async () => {
  require(path.join(directory, 'wasm_exec.js'));
  const go = new Go();
  const { instance } = await WebAssembly.instantiate(fs.readFileSync(path.join(directory, 'osmmini.wasm')), go.importObject);
  // main intentionally remains alive to serve calls; do not await go.run().
  go.run(instance).catch(error => { throw error; });
});

test('string and byte imports preserve the public route result', () => {
  assert.equal(osmReset().ok, true);
  assert.equal(route().ok, false);
  for (const input of [JSON.stringify(graph), new TextEncoder().encode(JSON.stringify(graph))]) {
    assert.deepEqual(osmInit(input), { ok: true });
    const result = route();
    assert.equal(result.ok, true);
    assert.deepEqual(result.path, [{ lat: 48, lon: 12 }, { lat: 48.001, lon: 12.001 }]);
    assert.equal(result.distance_m, 120);
    assert.equal(result.duration_s, 14.4);
    assert.equal(result.cost, 120);
    assert.equal(result.engine, 'astar');
    assert.equal(result.objective, 'distance');
  }
});

test('invalid calls return errors without killing the Go runtime', () => {
  assert.equal(osmInit(JSON.stringify(graph)).ok, true);
  for (const input of [undefined, null, 7, {}, new Int32Array(2), '{', JSON.stringify({ nodes: [], edges: [] })]) {
    assert.equal(osmInit(input).ok, false);
    assert.equal(route().ok, true, 'failed replacement discarded the old graph');
  }
  for (const args of [[], ['48', 12, 48, 12, 'astar', 'distance'],
    [NaN, 12, 48, 12, 'astar', 'distance'], [48, Infinity, 48, 12, 'astar', 'distance'],
    [91, 12, 48, 12, 'astar', 'distance'], [48, 181, 48, 12, 'astar', 'distance'],
    [48, 12, 48, 12, {}, 'distance'], [48, 12, 48, 12, 'astar', null]]) {
    assert.equal(osmRoute(...args).ok, false);
    assert.equal(route().ok, true);
  }
});

test('long paths cross the bridge with every coordinate intact', () => {
  const nodes = Array.from({ length: 2000 }, (_, i) => ({ id: i + 1, lat: 48, lon: 12 + i / 100000 }));
  const edges = nodes.slice(1).map((node, i) => ({ from: i + 1, to: node.id, dist_m: 1, speed_kph: 30 }));
  assert.equal(osmInit(JSON.stringify({ nodes, edges })).ok, true);
  const result = osmRoute(48, 12, 48, nodes.at(-1).lon, 'dijkstra', 'distance');
  assert.equal(result.ok, true);
  assert.equal(result.path.length, nodes.length);
  assert.equal(result.distance_m, nodes.length - 1);
  assert.deepEqual(result.path.at(-1), { lat: 48, lon: nodes.at(-1).lon });
  assert.equal(osmReset().ok, true);
  assert.equal(route().ok, false);
});

test('adjacent IDs above the JS safe integer limit remain distinct', () => {
  const json = '{"nodes":[{"id":9007199254740992,"lat":48,"lon":12},{"id":9007199254740993,"lat":48.001,"lon":12.001}],"edges":[{"from":9007199254740992,"to":9007199254740993,"dist_m":120,"speed_kph":30}]}';
  assert.equal(osmInit(new TextEncoder().encode(json)).ok, true);
  assert.equal(route().distance_m, 120);
});

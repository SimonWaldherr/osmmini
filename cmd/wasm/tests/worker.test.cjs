const assert = require('node:assert/strict');
const fs = require('node:fs');
const { once } = require('node:events');
const http = require('node:http');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
const { Worker: NodeWorker } = require('node:worker_threads');
const { test } = require('node:test');
const directory = path.resolve(process.env.OSMMINI_WASM_DIR || 'local/wasm');

// Adapt only the browser worker transport. Go, WASM compilation, graph fetches,
// routing and the shipped worker/client scripts run unchanged in another thread.
class BrowserWorker {
  constructor(url) {
    this.worker = new NodeWorker(`
      const { parentPort, workerData } = require('node:worker_threads');
      const fs = require('node:fs');
      const vm = require('node:vm');
      globalThis.self = globalThis;
      self.postMessage = value => parentPort.postMessage(value);
      self.importScripts = url => vm.runInThisContext(fs.readFileSync(new URL(url), 'utf8'), { filename: url });
      vm.runInThisContext(fs.readFileSync(new URL(workerData), 'utf8'), { filename: workerData });
      parentPort.on('message', data => self.onmessage({ data }));
    `, { eval: true, workerData: url.href });
  }
  addEventListener(type, fn) {
    if (type === 'message') this.worker.on('message', data => fn({ data }));
    else if (type === 'error') this.worker.on('error', error => fn({ message: error.message }));
    else this.worker.on(type, fn);
  }
  postMessage(value) { this.worker.postMessage(value); }
  terminate() { this.worker.terminate(); }
}
globalThis.Worker = BrowserWorker;

test('worker client fetches, routes concurrently and disposes', async t => {
  const graph = JSON.stringify({ nodes: [{ id: 1, lat: 48, lon: 12 }, { id: 2, lat: 48.001, lon: 12.001 }],
    edges: [{ from: 1, to: 2, dist_m: 120, speed_kph: 30 }] });
  const server = http.createServer((req, res) => {
    if (req.url === '/graph.json') { res.end(graph); return; }
    if (req.url === '/broken.json') { res.end('{'); return; }
    if (req.url === '/osmmini.wasm' || req.url === '/fallback.wasm') {
      res.setHeader('Content-Type', req.url === '/osmmini.wasm' ? 'application/wasm' : 'application/octet-stream');
      res.end(fs.readFileSync(path.join(directory, 'osmmini.wasm'))); return;
    }
    res.writeHead(404); res.end();
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise(resolve => server.close(resolve)));
  const base = `http://127.0.0.1:${server.address().port}`;
  const { createRouter } = await import(pathToFileURL(path.join(directory, 'client.mjs')));
  const options = { graphURL: `${base}/graph.json`, wasmURL: `${base}/osmmini.wasm`, runtimeURL: pathToFileURL(path.join(directory, 'wasm_exec.js')) };
  const rawWorker = new BrowserWorker(pathToFileURL(path.join(directory, 'worker.js')));
  t.after(() => rawWorker.terminate());
  async function send(data) {
    const message = once(rawWorker.worker, 'message');
    rawWorker.postMessage(data);
    return (await message)[0];
  }
  assert.match((await send(null)).error, /Invalid WASM request/);
  assert.match((await send({ id: 1, action: 'route', args: {} })).error, /args must be an array/);
  const urls = { ...options, runtimeURL: options.runtimeURL.href };
  assert.equal((await send({ id: 2, action: 'init', args: [urls] })).result.ok, true);
  assert.equal((await send({ id: 3, action: 'route', args: [48, 12, 48.001, 12.001, 'astar', 'distance'] })).result.distance_m, 120);
  for (const file of ['osmmini.wasm', 'fallback.wasm']) {
    const router = await createRouter({ ...options, wasmURL: `${base}/${file}` });
    t.after(() => router.dispose());
    const results = await Promise.all(Array.from({ length: 4 }, () => router.route(48, 12, 48.001, 12.001)));
    for (const result of results) assert.equal(result.distance_m, 120);
    await assert.rejects(router.route(NaN, 12, 48, 12), /coordinates/);
    assert.equal((await router.route(48, 12, 48.001, 12.001)).ok, true);
    const pending = router.route(48, 12, 48.001, 12.001);
    router.dispose();
    await assert.rejects(pending, /disposed/);
    await assert.rejects(router.route(48, 12, 48, 12), /disposed/);
  }
  await assert.rejects(createRouter({ ...options, graphURL: `${base}/missing.json` }), /Graph download failed \(404\)/);
  await assert.rejects(createRouter({ ...options, graphURL: `${base}/broken.json` }), /EOF|end of JSON input/);
  const controller = new AbortController();
  const loading = createRouter({ ...options, signal: controller.signal });
  controller.abort();
  await assert.rejects(loading, /aborted/);
  await assert.rejects(createRouter({ ...options, signal: controller.signal }), /aborted/);
  await assert.rejects(createRouter({ ...options, wasmURL: `${base}/missing.wasm` }), /WASM download failed \(404\)/);
});

test('client rejects pending work when its worker fails', async t => {
  const { createRouter } = await import(pathToFileURL(path.join(directory, 'client.mjs')));
  const original = globalThis.Worker;
  t.after(() => { globalThis.Worker = original; });
  for (const event of ['error', 'messageerror', 'fatal']) {
    let terminated = false;
    globalThis.Worker = class {
      handlers = new Map();
      addEventListener(type, fn) { this.handlers.set(type, fn); }
      postMessage() {
        queueMicrotask(() => event === 'fatal'
          ? this.handlers.get('message')({ data: { fatal: 'Go runtime exited' } })
          : this.handlers.get(event)({ message: 'worker failed' }));
      }
      terminate() { terminated = true; }
    };
    await assert.rejects(createRouter({ graphURL: 'http://127.0.0.1/graph.json' }), /worker|runtime/);
    assert.equal(terminated, true);
  }
});

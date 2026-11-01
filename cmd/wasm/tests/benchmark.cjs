// Optional real-WASM timing; includes graph decoding and the JS result bridge.
// Usage: node cmd/wasm/tests/benchmark.cjs [module.wasm] [wasm_exec.js]
const fs = require('node:fs');
const path = require('node:path');
const directory = path.resolve(process.env.OSMMINI_WASM_DIR || 'local/wasm');
const file = path.resolve(process.argv[2] || path.join(directory, 'osmmini.wasm'));
require(path.resolve(process.argv[3] || path.join(directory, 'wasm_exec.js')));
(async () => {
  const go = new Go();
  const { instance } = await WebAssembly.instantiate(fs.readFileSync(file), go.importObject);
  go.run(instance).catch(error => { throw error; });
  const nodes = Array.from({ length: 5000 }, (_, i) => ({ id: i + 1, lat: 48, lon: 12 + i / 100000 }));
  const edges = nodes.slice(1).map((node, i) => ({ from: i + 1, to: node.id, dist_m: 1, speed_kph: 30 }));
  const graph = JSON.stringify({ nodes, edges });
  const start = performance.now();
  const init = osmInit(graph);
  if (!init.ok) throw new Error(init.error);
  const importMS = performance.now() - start;
  const timings = [];
  for (let i = 0; i < 25; i++) {
    const start = performance.now();
    const result = osmRoute(48, 12, 48, nodes.at(-1).lon, 'astar', 'distance');
    const elapsed = performance.now() - start;
    if (!result.ok || result.path.length !== nodes.length) throw new Error('incorrect route');
    if (i >= 5) timings.push(elapsed);
  }
  timings.sort((a, b) => a - b);
  console.log(JSON.stringify({ module: file, nodes: nodes.length, bytes: fs.statSync(file).size,
    import_ms: importMS, median_route_ms: timings[Math.floor(timings.length / 2)] }));
})().catch(error => { console.error(error); process.exitCode = 1; });

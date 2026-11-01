// Each instance owns a worker and its Go heap. Dispose when the region is no
// longer needed; terminating the worker also cancels downloads and routing.
export async function createRouter({
  graphURL,
  signal,
  wasmURL = new URL('./osmmini.wasm', import.meta.url),
  runtimeURL = new URL('./wasm_exec.js', import.meta.url),
} = {}) {
  if (signal?.aborted) throw new Error('WASM router was aborted');
  if (!graphURL) throw new TypeError('graphURL is required');
  // Resolve relative graph URLs in the page's context, before crossing into the
  // worker (whose base URL is the WASM distribution directory).
  const base = globalThis.location?.href || import.meta.url;
  const urls = Object.fromEntries(Object.entries({ graphURL, wasmURL, runtimeURL })
    .map(([key, value]) => [key, new URL(value, base).href]));
  const worker = new Worker(new URL('./worker.js', import.meta.url));
  const pending = new Map();
  let nextID = 0;
  let closed = false;
  function close(error) {
    if (closed) return;
    closed = true;
    signal?.removeEventListener('abort', abort);
    worker.terminate();
    for (const { reject } of pending.values()) reject(error);
    pending.clear();
  }
  const abort = () => close(new Error('WASM router was aborted'));
  signal?.addEventListener('abort', abort, { once: true });
  worker.addEventListener('message', ({ data }) => {
    if (data.fatal) { close(new Error(data.fatal)); return; }
    const request = pending.get(data.id);
    if (!request) return;
    pending.delete(data.id);
    if (data.error) request.reject(new Error(data.error));
    else request.resolve(data.result);
  });
  worker.addEventListener('error', event => close(new Error(event.message || 'WASM worker failed')));
  worker.addEventListener('messageerror', () => close(new Error('Invalid WASM worker response')));
  function call(action, args = []) {
    if (closed) return Promise.reject(new Error('WASM router is disposed'));
    return new Promise((resolve, reject) => {
      const id = ++nextID;
      pending.set(id, { resolve, reject });
      try { worker.postMessage({ id, action, args }); }
      catch (error) { pending.delete(id); reject(error); }
    });
  }
  try { await call('init', [urls]); }
  catch (error) { close(error); throw error; }
  return {
    route(lat1, lon1, lat2, lon2, engine = 'astar', objective = 'distance') {
      return call('route', [lat1, lon1, lat2, lon2, engine, objective]);
    },
    dispose() { close(new Error('WASM router is disposed')); },
  };
}

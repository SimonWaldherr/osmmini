/* Run Go and all graph work in this dedicated worker, never on the UI thread. */
let runtime;

async function start(wasmURL, runtimeURL) {
  importScripts(runtimeURL);
  const go = new Go();
  const response = await fetch(wasmURL);
  if (!response.ok) throw new Error(`WASM download failed (${response.status})`);
  // Use streaming compilation when the server supplies the correct MIME type.
  // The fallback consumes the same response once, without a cloned body buffer.
  const mime = (response.headers.get('Content-Type') || '').split(';')[0].trim().toLowerCase();
  const { instance } = typeof WebAssembly.instantiateStreaming === 'function' && mime === 'application/wasm'
    ? await WebAssembly.instantiateStreaming(response, go.importObject)
    : await WebAssembly.instantiate(await response.arrayBuffer(), go.importObject);
  go.run(instance).then(
    () => self.postMessage({ fatal: 'Go runtime exited' }),
    error => self.postMessage({ fatal: error.message || String(error) }),
  );
  if (typeof osmInit !== 'function') throw new Error('WASM router did not initialize');
}

// Serialize requests even though initialization awaits network reads. A route
// submitted during a graph download must not run against the previous graph.
let queue = Promise.resolve();
self.onmessage = ({ data }) => {
  queue = queue.then(async () => {
    const id = data?.id;
    try {
      if (!data || typeof data !== 'object') throw new Error('Invalid WASM request');
      const { action, args = [] } = data;
      if (!Array.isArray(args)) throw new Error('WASM request args must be an array');
      let result;
      switch (action) {
        case 'init': {
          const { wasmURL, runtimeURL, graphURL } = args[0];
          runtime ||= start(wasmURL, runtimeURL);
          await runtime;
          const response = await fetch(graphURL);
          if (!response.ok) throw new Error(`Graph download failed (${response.status})`);
          // Decode bytes in Go rather than creating a second JS string copy.
          result = osmInit(new Uint8Array(await response.arrayBuffer()));
          break;
        }
        case 'route': result = osmRoute(...args); break;
        case 'reset': result = osmReset(); break;
        default: throw new Error(`Unknown WASM action: ${action}`);
      }
      if (!result.ok) throw new Error(result.error);
      self.postMessage({ id, result });
    } catch (error) {
      self.postMessage({ id, error: error.message || String(error) });
    }
  });
};

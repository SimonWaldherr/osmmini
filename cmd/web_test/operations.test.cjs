const assert = require('node:assert/strict');
const { test } = require('node:test');
const { readFileSync } = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const source = readFileSync(path.join(__dirname, '../web/app.js'), 'utf8');
const start = source.indexOf("const operationsEndpoint = '/api/v1/operations';");
const end = source.indexOf('\nfunction stopOperationScanner()', start);
assert.ok(start >= 0 && end > start);

function deferred() {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

function harness() {
  const storage = new Map();
  const status = { textContent: '' };
  const requests = [];
  let nextID = 0;
  const context = vm.createContext({
    console, Uint8Array,
    crypto: { randomUUID: () => `event-${++nextID}` },
    localStorage: {
      getItem: key => storage.get(key) || null,
      setItem: (key, value) => storage.set(key, value),
    },
    navigator: { onLine: true },
    document: { getElementById: () => status },
    adminAuthHeaders: headers => headers,
    fetch: (_url, options) => {
      const pending = deferred();
      requests.push({ options, ...pending });
      return pending.promise;
    },
  });
  vm.runInContext(source.slice(start, end), context);
  context.loadOperations = async () => {};
  return { context, storage, status, requests, pending: () => JSON.parse(storage.get('osmmini.operations.pending.v1') || '[]') };
}

test('full offline queue rejects a new record without dropping the oldest', () => {
  const h = harness();
  for (let i = 0; i < 100; i++) h.context.queueOperation({ client_event_id: `event-${i}` });
  assert.equal(h.pending().length, 100);
  assert.throws(() => h.context.queueOperation({ client_event_id: 'event-101' }), /voll/);
  assert.equal(h.pending()[0].client_event_id, 'event-0');
  assert.equal(h.pending().length, 100);
});

test('flush keeps records added while a network request is running', async () => {
  const h = harness();
  h.context.queueOperation({ client_event_id: 'first' });
  const firstFlush = h.context.flushPendingOperations();
  const sameFlush = h.context.flushPendingOperations();
  assert.equal(h.requests.length, 1);
  h.context.queueOperation({ client_event_id: 'new-during-flush' });
  h.requests[0].resolve({ ok: true, json: async () => ({ id: 'saved' }) });
  await Promise.all([firstFlush, sameFlush]);
  assert.deepEqual(h.pending().map(record => record.client_event_id), ['new-during-flush']);
});

test('failed send retains the same event ID for an idempotent retry', async () => {
  const h = harness();
  h.context.queueOperation({ client_event_id: 'stable-id', asset_code: 'WATER-1' });
  const failed = h.context.flushPendingOperations();
  h.requests[0].reject(new TypeError('connection lost'));
  await failed;
  assert.equal(h.pending()[0].client_event_id, 'stable-id');
  assert.match(h.status.textContent, /1 Offline-Einträge warten noch/);
  const retry = h.context.flushPendingOperations();
  assert.equal(JSON.parse(h.requests[1].options.body).client_event_id, 'stable-id');
  h.requests[1].resolve({ ok: true, json: async () => ({ id: 'saved' }) });
  await retry;
  assert.equal(h.pending().length, 0);
});

test('corrupt browser storage is preserved instead of replaced by an empty queue', async () => {
  const h = harness();
  h.storage.set('osmmini.operations.pending.v1', '{broken');
  assert.throws(() => h.context.queueOperation({ client_event_id: 'new' }), /beschädigt/);
  await h.context.flushPendingOperations();
  assert.equal(h.storage.get('osmmini.operations.pending.v1'), '{broken');
  assert.match(h.status.textContent, /beschädigt/);
  h.storage.set('osmmini.operations.local.v1', '{broken');
  assert.throws(() => h.context.localOperations(), /beschädigt/);
  assert.equal(h.storage.get('osmmini.operations.local.v1'), '{broken');
});

test('browser-local history refuses overflow without deleting older records', async () => {
  const h = harness();
  vm.runInContext('deployment.browser_local_operations = true', h.context);
  const existing = Array.from({ length: 500 }, (_, i) => ({ id: `old-${i}` }));
  h.storage.set('osmmini.operations.local.v1', JSON.stringify(existing));
  await assert.rejects(h.context.submitOperation({ client_event_id: 'new' }), /voll/);
  assert.equal(JSON.parse(h.storage.get('osmmini.operations.local.v1')).length, 500);
});

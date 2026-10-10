import test from 'node:test';
import assert from 'node:assert/strict';
// Views keep a browser copy in localStorage; Node has none.
const store = new Map();
globalThis.localStorage = { getItem: (k) => (store.has(k) ? store.get(k) : null), setItem: (k, v) => store.set(k, String(v)), removeItem: (k) => store.delete(k) };
const {
  BUILTINS, V, cleanStore, cleanView, allViews, findView, defaultId, saveView, saveAsNew, resetView, renameView, duplicateView, deleteView, setDefault,
  redirect, migrateLegacy, loadViews, persist, viewWindow, hasForecast, sameView, _test,
} = await import('../../internal/api/dashboard/screens/views.js');
const { S, prefs, setPref } = await import('../../internal/api/dashboard/core.js');

const empty = () => cleanStore({});
let n = 0;
const rand = () => ((n++ * 7919) % 1000) / 1000;

test('built-ins come first, in order, then saved views in creation order', () => {
  let s = empty();
  assert.deepEqual(allViews(s).map((v) => v.id), ['allowance', 'usage', 'performance']);
  const a = saveAsNew(s, findView(s, 'usage'), 'Costs', { rand });
  const b = saveAsNew(a.store, findView(a.store, 'allowance'), 'Mine', { rand });
  s = b.store;
  assert.deepEqual(allViews(s).map((v) => v.name), ['Allowance', 'Usage', 'Performance', 'Costs', 'Mine']);
  assert.equal(findView(s, a.id).builtin, false);
  assert.deepEqual(findView(s, a.id).panels, findView(s, 'usage').panels);
});

test('the built-in views hold the panels, columns and windows of the brief', () => {
  const by = Object.fromEntries(BUILTINS.map((v) => [v.id, v]));
  assert.deepEqual(by.allowance.panels.map((p) => p.type), ['allowance', 'available', 'tokens']);
  assert.deepEqual(by.usage.panels.map((p) => p.type), ['tokens', 'cost', 'requests', 'activity']);
  assert.deepEqual(by.performance.panels.map((p) => p.type), ['ttft', 'latency', 'throughput', 'failures', 'available']);
  assert.equal(by.allowance.window, 'around_now');
  assert.equal(by.usage.window, 'last7d');
  assert.equal(by.performance.window, 'last24h');
  const now = Date.parse('2026-10-08T14:00:00Z');
  assert.deepEqual(viewWindow('around_now', now), { start: now - 3.5 * 864e5, end: now + 3.5 * 864e5 });
  assert.equal(hasForecast(by.allowance), true);
  assert.equal(hasForecast(by.usage), false);
});

test('saving an edited built-in stores an override, and reset restores it', () => {
  let s = empty();
  const edited = { ...findView(s, 'usage'), panels: [{ type: 'cost', options: { format: 'bars' } }] };
  s = saveView(s, edited);
  assert.equal(s.views.length, 1);
  const v = findView(s, 'usage');
  assert.equal(v.overridden, true);
  assert.deepEqual(v.panels.map((p) => p.type), ['cost']);
  s = resetView(s, 'usage');
  assert.equal(s.views.length, 0);
  assert.deepEqual(findView(s, 'usage').panels, BUILTINS[1].panels);
  // Saving a built-in back to how it was built needs no override.
  s = saveView(saveView(empty(), edited), BUILTINS[1]);
  assert.equal(s.views.length, 0);
});

test('rename, duplicate, set as default and delete', () => {
  let s = empty();
  const made = saveAsNew(s, findView(s, 'performance'), 'Speed', { rand, keepAccounts: true, accounts: ['a'] });
  s = made.store;
  assert.deepEqual(findView(s, made.id).accounts, ['a']);
  s = renameView(s, made.id, '  Fast  ');
  assert.equal(findView(s, made.id).name, 'Fast');
  assert.equal(renameView(s, made.id, '   '), s, 'an empty name changes nothing');
  const dup = duplicateView(s, made.id, rand);
  s = dup.store;
  assert.equal(findView(s, dup.id).name, 'Fast copy');
  assert.notEqual(dup.id, made.id);
  s = setDefault(s, made.id);
  assert.equal(defaultId(s), made.id);
  s = deleteView(s, made.id);
  assert.equal(findView(s, made.id), null);
  assert.equal(defaultId(s), 'allowance', 'deleting the default falls back to Allowance');
  assert.equal(deleteView(s, 'usage'), s, 'built-ins cannot be deleted');
  // Renaming a built-in keeps it a built-in, with an override.
  s = renameView(s, 'usage', 'Spend');
  assert.equal(findView(s, 'usage').name, 'Spend');
  assert.equal(findView(s, 'usage').builtin, true);
});

test('a stored default that no longer exists falls back to Allowance', () => {
  assert.equal(defaultId(cleanStore({ views: [], default: 'gone' })), 'allowance');
  assert.equal(defaultId(cleanStore({ views: [], default: 'performance' })), 'performance');
});

test('stored views are cleaned: unknown panels, columns and duplicates dropped', () => {
  const s = cleanStore({ views: [
    { id: 'x', name: 'X', panels: [{ type: 'tokens' }, { type: 'nope' }, { type: 'tokens', options: { format: 'bars' } }], columns: ['tokens', 'bogus', 'tokens'], window: 'weird' },
    { id: 'x', name: 'Again' },
    { name: 'no id' },
    'junk',
  ], default: 7 });
  assert.equal(s.views.length, 1);
  assert.deepEqual(s.views[0].panels, [{ type: 'tokens', options: {} }]);
  assert.deepEqual(s.views[0].columns, ['tokens']);
  assert.equal(s.views[0].window, 'last7d');
  assert.equal(s.default, '');
  assert.equal(cleanView({ id: 'usage', panels: [] }).builtin, true);
});

test('changes to a view are noticed, names aside', () => {
  const base = findView(empty(), 'allowance');
  assert.equal(sameView(base, { ...base, name: 'Other' }), true);
  assert.equal(sameView(base, { ...base, window: 'last7d' }), false);
  assert.equal(sameView(base, { ...base, panels: base.panels.slice().reverse() }), false);
  assert.equal(sameView(base, { ...base, panels: base.panels.map((p) => (p.type === 'tokens' ? { ...p, options: { format: 'bars' } } : p)) }), false);
});

test('old routes open the matching view, the bare route opens the default', () => {
  const s = setDefault(empty(), 'performance');
  assert.equal(redirect('#/usage', s), '#/telemetry/allowance');
  assert.equal(redirect('#/performance', s), '#/telemetry/performance');
  assert.equal(redirect('#/telemetry', s), '#/telemetry/performance');
  assert.equal(redirect('#/telemetry/missing', s), '#/telemetry/performance');
  assert.equal(redirect('#/telemetry/usage', s), null);
  assert.equal(redirect('#/accounts', s), null);
});

test('a customised old Performance layout becomes an override, once', () => {
  const ui = { usSection: 'history', usWindow: '5h', historyViewport: { start: 1, end: 2 }, pfRange: '7d' };
  const p = { performance: { order: ['failures', 'ttft', 'requests', 'latency'], on: ['ttft', 'failures', 'requests'] }, charts: { 'perf-requests': 'bars' } };
  const s = migrateLegacy(empty(), ui, p);
  assert.deepEqual(Object.keys(ui), [], 'old per-screen keys are dropped');
  const v = findView(s, 'performance');
  assert.deepEqual(v.panels.map((x) => x.type), ['failures', 'ttft', 'requests', 'available']);
  assert.deepEqual(v.panels.find((x) => x.type === 'requests').options, { format: 'bars' });
  // A second run, with the override stored, leaves it alone.
  assert.equal(migrateLegacy(s, {}, p), s);
  // Nothing customised: nothing stored.
  assert.equal(migrateLegacy(empty(), {}, {}).views.length, 0);
});

// A stand-in for /dashboard/views: GET returns the stored body, PUT replaces
// it. down makes every request fail.
function fakeServer(body = { views: [], default: '' }) {
  const s = { body: JSON.parse(JSON.stringify(body)), down: false, puts: 0 };
  s.fetch = async (url, init) => {
    if (s.down) throw new Error('offline');
    if (init?.method === 'PUT') { s.body = JSON.parse(init.body); s.puts++; return { ok: true }; }
    return { ok: true, json: async () => JSON.parse(JSON.stringify(s.body)) };
  };
  return s;
}
const resetViews = () => { V.loading = null; V.loaded = false; V.where = ''; V.note = ''; V.store = empty(); store.clear(); };
const named = (s) => s.views.map((v) => v.name);

test('views load from the server, else from this browser with a note', async () => {
  resetViews();
  const srv = fakeServer({ views: [{ id: 'v-1', name: 'Server view', panels: [{ type: 'cost' }], columns: [], window: 'last24h' }], default: 'v-1' });
  await loadViews(srv.fetch);
  assert.equal(V.where, 'server');
  assert.equal(defaultId(V.store), 'v-1');
  assert.equal(V.note, '');

  resetViews();
  _test.writeLocal(cleanStore({ views: [{ id: 'v-2', name: 'Local view', panels: [], columns: [], window: 'last7d' }] }));
  await loadViews(async () => { throw new Error('offline'); });
  assert.equal(V.where, 'local');
  assert.ok(findView(V.store, 'v-2'));
  assert.match(V.note, /this browser/);
});

test('two tabs saving one after the other keep both views', async () => {
  resetViews();
  const srv = fakeServer();
  await loadViews(srv.fetch);
  // Both tabs loaded the same empty set of views.
  const tabB = V.store;
  // Tab A creates view A.
  await persist(saveAsNew(V.store, findView(V.store, 'usage'), 'View A', { rand }).store, srv.fetch);
  // Tab B, still showing what it loaded, creates view B.
  V.store = tabB;
  const where = await persist(saveAsNew(tabB, findView(tabB, 'allowance'), 'View B', { rand }).store, srv.fetch);
  assert.equal(where, 'server');
  assert.deepEqual(named(srv.body), ['View A', 'View B']);
  // Tab B now shows both.
  assert.deepEqual(named(V.store), ['View A', 'View B']);
  // A rename in tab A, from its stale copy, touches only that view and keeps the default.
  const idA = srv.body.views[0].id;
  srv.body.default = srv.body.views[1].id;
  V.store = cleanStore({ views: [srv.body.views[0]], default: '' });
  await persist(renameView(V.store, idA, 'Renamed A'), srv.fetch);
  assert.deepEqual(named(srv.body), ['Renamed A', 'View B']);
  assert.equal(srv.body.default, srv.body.views[1].id);
  // Deleting one leaves the other.
  await persist(deleteView(V.store, idA), srv.fetch);
  assert.deepEqual(named(srv.body), ['View B']);
});

test('changes kept in this browser survive a reload and go up once the proxy takes them', async () => {
  resetViews();
  const srv = fakeServer({ views: [{ id: 'v-9', name: 'Old name', panels: [], columns: [], window: 'last7d' }], default: '' });
  await loadViews(srv.fetch);
  // The proxy stops storing views; a rename stays in this browser.
  srv.down = true;
  const where = await persist(renameView(V.store, 'v-9', 'New name'), srv.fetch);
  assert.equal(where, 'local');
  assert.match(V.note, /this browser/);
  // Reload while still down: the rename shows.
  V.loading = null; V.loaded = false; V.store = empty();
  await loadViews(srv.fetch);
  assert.equal(findView(V.store, 'v-9').name, 'New name');
  assert.match(V.note, /this browser/);
  // Reload once the proxy is back: the rename wins over the stored name and goes up.
  srv.down = false;
  V.loading = null; V.loaded = false; V.store = empty();
  await loadViews(srv.fetch);
  assert.equal(findView(V.store, 'v-9').name, 'New name');
  assert.deepEqual(named(srv.body), ['New name']);
  assert.equal(V.note, '');
  assert.equal(store.has(_test.PENDING), false);
  assert.equal(store.has(_test.LOCAL_STORE), false);
});

test('a failed upload keeps the waiting changes and the note', async () => {
  resetViews();
  const srv = fakeServer({ views: [{ id: 'v-9', name: 'Stored', panels: [], columns: [], window: 'last7d' }], default: '' });
  _test.writeLocal(cleanStore({}));
  store.set(_test.PENDING, JSON.stringify([{ op: 'set', id: 'v-9', view: { id: 'v-9', name: 'Waiting', panels: [], columns: [], window: 'last7d' } }]));
  const fetchFn = async (url, init) => (init?.method === 'PUT' ? { ok: false, status: 503 } : srv.fetch(url, init));
  await loadViews(fetchFn);
  assert.equal(findView(V.store, 'v-9').name, 'Waiting');
  assert.equal(_test.readPending().length, 1);
  assert.match(V.note, /this browser/);
});

test('views an earlier version kept in this browser move to the server without removing others', async () => {
  resetViews();
  _test.writeLocal(cleanStore({ views: [{ id: 'v-3', name: 'Kept', panels: [], columns: [], window: 'last7d' }] }));
  const srv = fakeServer({ views: [{ id: 'v-4', name: 'On server', panels: [], columns: [], window: 'last7d' }], default: '' });
  await loadViews(srv.fetch);
  assert.equal(srv.puts, 1);
  assert.deepEqual(named(srv.body), ['On server', 'Kept']);
  assert.ok(findView(V.store, 'v-3'));
  assert.equal(store.has(_test.LOCAL_STORE), false);
});

test('migration of the old Performance layout runs when views load', async () => {
  resetViews();
  S.ui = { usMetric: 'tokens' };
  setPref('performance', { order: ['tokens', 'ttft'], on: ['tokens', 'ttft'] });
  const srv = fakeServer();
  await loadViews(srv.fetch);
  assert.deepEqual(findView(V.store, 'performance').panels.map((p) => p.type), ['tokens', 'ttft', 'available']);
  assert.equal(prefs().performance, undefined);
  assert.equal(S.ui.usMetric, undefined);
  assert.equal(srv.puts, 1);
  assert.deepEqual(srv.body.views.map((v) => v.id), ['performance']);
});

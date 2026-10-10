import test from 'node:test';
import assert from 'node:assert/strict';
// Saving views against the proxy's revision contract, with two tabs of one
// browser: two copies of the views module sharing one localStorage.
const mem = new Map();
globalThis.localStorage = { getItem: (k) => (mem.has(k) ? mem.get(k) : null), setItem: (k, v) => mem.set(k, String(v)), removeItem: (k) => mem.delete(k) };
const tabA = await import('../../internal/api/dashboard/screens/views.js');
const tabB = await import('../../internal/api/dashboard/screens/views.js?tab=b');
const { cleanStore, findView, saveAsNew, renameView, deleteView, setDefault, saveView } = tabA;

const tick = () => new Promise((r) => setImmediate(r));
const clone = (x) => JSON.parse(JSON.stringify(x));
const reply = (status, body, etag) => ({ ok: status >= 200 && status < 300, status, headers: { get: (k) => (k.toLowerCase() === 'etag' ? etag : null) }, json: async () => clone(body) });
const ID = /^[a-z0-9-]{1,40}$/;
const VIEW_KEYS = ['id', 'name', 'panels', 'columns', 'window', 'accounts', 'builtin', 'updated_at'];
const stampOk = (n, min) => Number.isSafeInteger(n) && n >= min;

// The body check of PR #28: views as before plus an optional updated_at,
// at most 200 deletions of {id, updated_at > 0} with unique ids.
function check(b) {
  if (!b || !Array.isArray(b.views)) return 'views must be an array';
  if (Object.keys(b).some((k) => !['views', 'default', 'deleted', 'revision'].includes(k))) return 'unknown field';
  if (b.views.length > 50) return 'at most 50 views';
  if (b.default && !ID.test(b.default)) return 'default must be empty or a view id';
  const seen = new Set();
  for (const v of b.views) {
    if (Object.keys(v).some((k) => !VIEW_KEYS.includes(k))) return 'unknown field';
    if (!ID.test(v.id)) return 'bad id';
    if (!String(v.name).trim() || [...v.name].length > 60) return 'name must be 1 to 60 characters';
    if ('updated_at' in v && !stampOk(v.updated_at, 0)) return 'bad updated_at';
    if (seen.has(v.id)) return 'id used twice';
    seen.add(v.id);
  }
  const del = b.deleted ?? [];
  if (!Array.isArray(del) || del.length > 200) return 'bad deleted';
  const ids = new Set();
  for (const d of del) {
    if (!ID.test(d.id) || !stampOk(d.updated_at, 1) || ids.has(d.id)) return 'bad deleted';
    ids.add(d.id);
  }
  return '';
}

// Revisions are opaque: large, never 0, not counting up from anything.
let lastRev = 1759000000000;
const nextRev = () => (lastRev += 1000 + Math.floor(Math.random() * 90000));

// A stand-in for /dashboard/views after PR #28 (head 342cca0d), in its order
// of checks: If-Match required (428), body checked (400), revision matched
// (409 with the current document; "0" never matches), then stored at a new
// opaque revision. A damaged store answers GET with 500; a PUT then gets a
// 409 whose current is the empty set at a fresh revision, and a PUT from
// that revision replaces it. hold pauses PUTs until released; down makes
// every request fail, as offline.
function contractServer(views = [], extra = {}) {
  const s = { doc: { views: clone(views), default: '', deleted: [], ...clone(extra), revision: nextRev() }, down: false, damaged: false, puts: 0, conflicts: 0, gate: null };
  s.hold = () => { let release; s.gate = new Promise((r) => { release = r; }); return () => { s.gate = null; release(); }; };
  s.fetch = async (url, init = {}) => {
    await tick();
    if (s.down) throw new Error('offline');
    if (init.method !== 'PUT') return s.damaged ? reply(500, { error: 'saved views file cannot be read' }) : reply(200, s.doc, `"${s.doc.revision}"`);
    if (s.gate) await s.gate;
    const match = init.headers?.['If-Match'];
    if (match == null) return reply(428, { error: 'If-Match header required' });
    let body;
    try { body = JSON.parse(init.body); } catch (e) { return reply(400, { error: 'bad body' }); }
    const bad = check(body);
    if (bad) return reply(400, { error: bad });
    if (s.damaged) { s.damaged = false; s.doc = { views: [], default: '', deleted: [], revision: nextRev() }; }
    if (String(match).replace(/"/g, '') !== String(s.doc.revision)) {
      s.conflicts++;
      return reply(409, { error: 'views changed since you loaded them', revision: s.doc.revision, current: s.doc }, `"${s.doc.revision}"`);
    }
    s.doc = { views: body.views, default: body.default || '', deleted: body.deleted || [], revision: nextRev() };
    s.puts++;
    return reply(200, s.doc, `"${s.doc.revision}"`);
  };
  // A change made by another browser: applied straight to the store.
  s.elsewhere = (fn) => { fn(s.doc); s.doc.revision = nextRev(); };
  return s;
}

const v = (id, name, updated_at) => ({ id, name, panels: [], columns: [], window: 'last7d', accounts: null, builtin: false, ...(updated_at ? { updated_at } : {}) });
const names = (doc) => doc.views.map((x) => x.name);
function fresh() {
  mem.clear();
  for (const t of [tabA, tabB]) {
    Object.assign(t.V, { loading: null, loaded: false, note: '', notice: '', unsaved: false, doc: null, store: cleanStore({}) });
    t._test.reset();
  }
}
let n = 0;
const rand = () => ((n++ * 7919) % 1000) / 1000;
const offline = async () => { throw new Error('offline'); };

test('saves from one tab that overlap both land, one after the other', async () => {
  fresh();
  const srv = contractServer();
  await tabA.loadViews(srv.fetch);
  const a = saveAsNew(tabA.V.store, findView(tabA.V.store, 'usage'), 'View A', { rand });
  const first = tabA.persist(a.store, srv.fetch);
  const b = saveAsNew(a.store, findView(a.store, 'allowance'), 'View B', { rand });
  const second = tabA.persist(b.store, srv.fetch);
  assert.deepEqual(await Promise.all([first, second]), ['server', 'server']);
  assert.deepEqual(names(srv.doc), ['View A', 'View B']);
  assert.equal(srv.conflicts, 0, 'the second save waited for the first');
  assert.deepEqual(tabA._test.pending(), []);
});

test('two tabs saving at the same moment both land: the later one applies its change again after a 409', async () => {
  fresh();
  const srv = contractServer([v('v-old', 'Old', 1000)]);
  await tabA.loadViews(srv.fetch);
  await tabB.loadViews(srv.fetch);
  const a = saveAsNew(tabA.V.store, findView(tabA.V.store, 'usage'), 'From A', { rand });
  const b = saveAsNew(tabB.V.store, findView(tabB.V.store, 'usage'), 'From B', { rand });
  const res = await Promise.all([tabA.persist(a.store, srv.fetch), tabB.persist(b.store, srv.fetch)]);
  assert.deepEqual(res, ['server', 'server']);
  assert.deepEqual(names(srv.doc).sort(), ['From A', 'From B', 'Old']);
  assert.ok(srv.conflicts >= 1, 'the stores raced and one got a 409');
  assert.equal(srv.puts, 2);
  assert.ok([tabA.V.doc.revision, tabB.V.doc.revision].includes(srv.doc.revision));
  assert.equal(tabA.V.notice + tabB.V.notice, '', 'nothing was lost, so no note');
});

test('a stale tab changing one field keeps the rename another tab saved', async () => {
  fresh();
  const srv = contractServer([v('v-x', 'Original', 1000)]);
  await tabA.loadViews(srv.fetch);
  await tabB.loadViews(srv.fetch);
  // Tab A renames and saves.
  assert.equal(await tabA.persist(renameView(tabA.V.store, 'v-x', 'Renamed in A'), srv.fetch), 'server');
  // Tab B, still showing the original name, changes the window.
  const edited = saveView(tabB.V.store, { ...findView(tabB.V.store, 'v-x'), window: 'last24h' });
  assert.equal(await tabB.persist(edited, srv.fetch), 'server');
  assert.deepEqual([srv.doc.views[0].name, srv.doc.views[0].window], ['Renamed in A', 'last24h']);
  assert.equal(findView(tabB.V.store, 'v-x').name, 'Renamed in A', 'tab B now shows the rename');
  assert.equal(tabA.V.notice + tabB.V.notice, '');
});

test('two browsers renaming one view in the same millisecond: the later save is written, not taken as done', async () => {
  fresh();
  const srv = contractServer([v('v-x', 'Start', 1000)]);
  await tabA.loadViews(srv.fetch);
  await tabB.loadViews(srv.fetch);
  // The same clock reading for both edits.
  assert.equal(await tabA.persist(renameView(tabA.V.store, 'v-x', 'From A'), srv.fetch, 5000), 'server');
  assert.equal(await tabB.persist(renameView(tabB.V.store, 'v-x', 'From B'), srv.fetch, 5000), 'server');
  assert.deepEqual(names(srv.doc), ['From B']);
  assert.equal(srv.puts, 2);
  assert.equal(findView(tabB.V.store, 'v-x').name, 'From B');
});

test('a save is sent even when browser storage refuses writes, and nothing is stored there', async () => {
  fresh();
  const srv = contractServer([v('v-x', 'Start', 1000)]);
  const set = globalThis.localStorage.setItem;
  globalThis.localStorage.setItem = () => { throw new Error('QuotaExceededError'); };
  try {
    await tabA.loadViews(srv.fetch);
    assert.equal(await tabA.persist(renameView(tabA.V.store, 'v-x', 'Saved anyway'), srv.fetch), 'server');
    assert.equal(srv.puts, 1);
    assert.deepEqual(names(srv.doc), ['Saved anyway']);
  } finally {
    globalThis.localStorage.setItem = set;
  }
  assert.equal(mem.size, 0);
});

test('a save that cannot reach the proxy is kept in memory only, says so, and goes up on retry', async () => {
  fresh();
  const srv = contractServer([v('v-x', 'Start', 1000)]);
  await tabA.loadViews(srv.fetch);
  assert.equal(await tabA.persist(renameView(tabA.V.store, 'v-x', 'Waiting'), offline), 'failed');
  assert.equal(tabA.V.unsaved, true);
  assert.equal(findView(tabA.V.store, 'v-x').name, 'Waiting', 'the tab still shows the edit');
  assert.equal(tabA._test.pending().length, 1);
  assert.equal(mem.size, 0, 'nothing in browser storage');
  // A reload would start empty: nothing is replayed from storage.
  fresh();
  await tabA.loadViews(srv.fetch);
  assert.equal(findView(tabA.V.store, 'v-x').name, 'Start');
  // In the same tab, Retry sends it.
  assert.equal(await tabA.persist(renameView(tabA.V.store, 'v-x', 'Waiting'), offline), 'failed');
  assert.equal(await tabA.retrySaves(srv.fetch), 'server');
  assert.deepEqual(names(srv.doc), ['Waiting']);
  assert.equal(tabA.V.unsaved, false);
  assert.deepEqual(tabA._test.pending(), []);
});

test('an edit to a view deleted elsewhere is dropped with the conflict note', async () => {
  fresh();
  const srv = contractServer([v('v-x', 'Doomed', 1000), v('v-y', 'Other', 1000)]);
  await tabA.loadViews(srv.fetch);
  srv.elsewhere((d) => { d.views = d.views.filter((x) => x.id !== 'v-x'); d.deleted.push({ id: 'v-x', updated_at: 3000 }); });
  const edited = saveView(tabA.V.store, { ...findView(tabA.V.store, 'v-x'), window: 'last24h' });
  assert.equal(await tabA.persist(edited, srv.fetch), 'server');
  assert.deepEqual(names(srv.doc), ['Other']);
  assert.equal(findView(tabA.V.store, 'v-x'), null);
  assert.equal(tabA.V.notice, 'Doomed changed elsewhere, your edit was not saved');
  // Picking a deleted view as the default loses the same way.
  tabA.V.store = { ...tabA.V.store, views: [...tabA.V.store.views, cleanStore({ views: [v('v-x', 'Doomed')] }).views[0]] };
  assert.equal(await tabA.persist(setDefault(tabA.V.store, 'v-x'), srv.fetch), 'server');
  assert.equal(srv.doc.default, '');
  assert.equal(tabA.V.notice, 'The default view changed elsewhere, your edit was not saved');
});

test('deleting the old default keeps a newer default chosen elsewhere', async () => {
  fresh();
  const srv = contractServer([v('v-x', 'X', 1000), v('v-y', 'Y', 1000)], { default: 'v-x' });
  await tabA.loadViews(srv.fetch);
  srv.elsewhere((d) => { d.default = 'v-y'; });
  assert.equal(await tabA.persist(deleteView(tabA.V.store, 'v-x'), srv.fetch), 'server');
  assert.deepEqual(names(srv.doc), ['Y']);
  assert.equal(srv.doc.default, 'v-y');
  assert.deepEqual(srv.doc.deleted.map((d) => d.id), ['v-x']);
});

test('a 409 that keeps coming is tried three times, then the change waits for a retry', async () => {
  fresh();
  const srv = contractServer();
  await tabA.loadViews(srv.fetch);
  const busy = async (url, init = {}) => {
    if (init.method === 'PUT') srv.elsewhere(() => {});
    return srv.fetch(url, init);
  };
  const a = saveAsNew(tabA.V.store, findView(tabA.V.store, 'usage'), 'Busy', { rand });
  assert.equal(await tabA.persist(a.store, busy), 'failed');
  assert.equal(srv.conflicts, 3);
  assert.equal(tabA._test.pending().length, 1);
  assert.equal(await tabA.retrySaves(srv.fetch), 'server');
  assert.deepEqual(names(srv.doc), ['Busy']);
});

test('a name the proxy would refuse is never sent, and a refused save does not block later ones', async () => {
  fresh();
  assert.equal(tabA.nameError('x'.repeat(61)), 'View names can be up to 60 characters.');
  assert.equal(tabA.nameError('é'.repeat(60)), '');
  assert.equal(tabA.nameError('  '), 'Give the view a name.');
  const srv = contractServer([v('v-x', 'Short', 1000)]);
  await tabA.loadViews(srv.fetch);
  assert.equal(await tabA.persist(renameView(tabA.V.store, 'v-x', 'y'.repeat(61)), srv.fetch), 'server');
  assert.equal(srv.doc.views[0].name, 'y'.repeat(60));
  const refuse = async (url, init = {}) => (init.method === 'PUT' ? reply(400, { error: 'views[0]: something new' }) : srv.fetch(url, init));
  assert.equal(await tabA.persist(renameView(tabA.V.store, 'v-x', 'Refused'), refuse), 'rejected');
  assert.equal(tabA.V.notice, 'Views were not saved: views[0]: something new');
  assert.deepEqual(tabA._test.pending(), []);
  assert.equal(await tabA.persist(renameView(tabA.V.store, 'v-x', 'Later'), srv.fetch), 'server');
  assert.equal(srv.doc.views[0].name, 'Later');
});

test('revisions go back exactly as received, quoted', async () => {
  fresh();
  const srv = contractServer([v('v-x', 'X', 1000), v('v-y', 'Y', 1000)]);
  const seen = [];
  const watch = (url, init = {}) => { if (init.method === 'PUT') seen.push(init.headers['If-Match']); return srv.fetch(url, init); };
  await tabA.loadViews(watch);
  const read = srv.doc.revision;
  assert.equal(await tabA.persist(setDefault(tabA.V.store, 'v-y'), watch), 'server');
  assert.deepEqual(seen, [`"${read}"`]);
  assert.equal(tabA.V.doc.revision, srv.doc.revision);
  // The next save reads the revision afresh rather than counting on.
  srv.elsewhere(() => {});
  assert.equal(await tabA.persist(setDefault(tabA.V.store, 'v-x'), watch), 'server');
  assert.equal(srv.conflicts, 0);
  assert.equal(srv.doc.default, 'v-x');
});

test('a damaged store is replaced from the fresh revision its 409 gives', async () => {
  fresh();
  const srv = contractServer([v('v-x', 'Lost', 1000)]);
  srv.damaged = true;
  await tabA.loadViews(srv.fetch);
  assert.equal(tabA.V.note, '', 'the 409 to If-Match "0" gave a document to build on');
  assert.deepEqual(tabA.V.store.views, []);
  const a = saveAsNew(tabA.V.store, findView(tabA.V.store, 'usage'), 'After repair', { rand });
  assert.equal(await tabA.persist(a.store, srv.fetch), 'server');
  assert.deepEqual(names(srv.doc), ['After repair']);
  // A proxy without revisions refuses the probe (it has an unknown field), so
  // a read error there never writes anything.
  const writes = [];
  const legacy = async (url, init = {}) => {
    if (init.method !== 'PUT') return reply(500, { error: 'saved views file cannot be read' });
    const b = JSON.parse(init.body);
    if (Object.keys(b).some((k) => !['views', 'default'].includes(k))) return reply(400, { error: 'unknown field' });
    writes.push(b);
    return reply(200, b);
  };
  fresh();
  await tabA.loadViews(legacy);
  assert.match(tabA.V.note, /could not be loaded/);
  assert.deepEqual(writes, []);
});

test('a proxy without revisions gets plain saves, with no If-Match and no new fields', async () => {
  fresh();
  const sent = [];
  let body = { views: [], default: '' };
  const legacy = async (url, init = {}) => {
    await tick();
    if (init.method === 'PUT') {
      const b = JSON.parse(init.body);
      sent.push({ headers: init.headers, body: b });
      if (Object.keys(b).some((k) => !['views', 'default'].includes(k)) || b.views.some((x) => 'updated_at' in x)) return reply(400, { error: 'unknown field' });
      body = b;
    }
    return reply(200, body);
  };
  await tabA.loadViews(legacy);
  const a = saveAsNew(tabA.V.store, findView(tabA.V.store, 'usage'), 'Plain', { rand });
  assert.equal(await tabA.persist(a.store, legacy), 'server');
  assert.equal(await tabA.persist(deleteView(tabA.V.store, a.id), legacy), 'server');
  assert.equal(sent.length, 2);
  for (const s of sent) assert.equal(s.headers['If-Match'], undefined);
  assert.deepEqual(body.views, []);
});

test('a create whose reply was lost is not repeated, and cannot bring back a view deleted meanwhile', async () => {
  fresh();
  const srv = contractServer();
  await tabA.loadViews(srv.fetch);
  // The PUT reaches the proxy, but its reply is lost.
  let drop = true;
  const flaky = async (url, init = {}) => {
    const res = await srv.fetch(url, init);
    if (init.method === 'PUT' && drop) { drop = false; throw new Error('connection reset'); }
    return res;
  };
  const a = saveAsNew(tabA.V.store, findView(tabA.V.store, 'usage'), 'Sent once', { rand });
  assert.equal(await tabA.persist(a.store, flaky), 'failed');
  assert.deepEqual(names(srv.doc), ['Sent once']);
  // Retry finds it already there: done, no second copy.
  assert.equal(await tabA.retrySaves(srv.fetch), 'server');
  assert.deepEqual(names(srv.doc), ['Sent once']);
  assert.equal(tabA.V.notice, '');
  // Again, but another tab deletes it before the retry.
  drop = true;
  const b = saveAsNew(tabA.V.store, findView(tabA.V.store, 'usage'), 'Deleted elsewhere', { rand });
  assert.equal(await tabA.persist(b.store, flaky), 'failed');
  await tabB.loadViews(srv.fetch);
  assert.equal(await tabB.persist(deleteView(tabB.V.store, b.id), srv.fetch), 'server');
  assert.equal(await tabA.retrySaves(srv.fetch), 'server');
  assert.deepEqual(names(srv.doc), ['Sent once']);
  assert.ok(srv.doc.deleted.some((d) => d.id === b.id), 'the deletion record stays');
  assert.equal(tabA.V.notice, 'Deleted elsewhere changed elsewhere, your edit was not saved');
  assert.deepEqual(tabA._test.pending(), []);
});

test('renaming a built-in back keeps a field another tab changed in its override', async () => {
  fresh();
  const srv = contractServer();
  await tabA.loadViews(srv.fetch);
  assert.equal(await tabA.persist(renameView(tabA.V.store, 'usage', 'My usage'), srv.fetch), 'server');
  await tabB.loadViews(srv.fetch);
  const win = saveView(tabB.V.store, { ...findView(tabB.V.store, 'usage'), window: 'last24h' });
  assert.equal(await tabB.persist(win, srv.fetch), 'server');
  // Tab A, still showing last7d, renames it back to Usage.
  assert.equal(await tabA.persist(renameView(tabA.V.store, 'usage', 'Usage'), srv.fetch), 'server');
  const over = srv.doc.views.find((x) => x.id === 'usage');
  assert.deepEqual([over?.name, over?.window], ['Usage', 'last24h']);
  // With no field left differing from the built-in, the override goes.
  const back = saveView(tabA.V.store, { ...findView(tabA.V.store, 'usage'), window: 'last7d' });
  assert.equal(await tabA.persist(back, srv.fetch), 'server');
  assert.equal(srv.doc.views.some((x) => x.id === 'usage'), false);
});

test('a new view past the limit is not saved, not kept, and says why', async () => {
  fresh();
  const custom = Array.from({ length: 46 }, (_, i) => v(`v-c${i}`, `View ${i}`, 1000));
  const overrides = ['allowance', 'usage', 'performance'].map((id) => ({ ...v(id, `${id} mine`, 1000), builtin: true }));
  const srv = contractServer([...overrides, ...custom]);
  await tabA.loadViews(srv.fetch);
  await tabB.loadViews(srv.fetch);
  const a = saveAsNew(tabA.V.store, findView(tabA.V.store, 'usage'), 'Fiftieth', { rand });
  const b = saveAsNew(tabB.V.store, findView(tabB.V.store, 'usage'), 'Fifty-first', { rand });
  assert.ok(a.id && b.id, 'each tab saw room for one more');
  assert.equal(await tabA.persist(a.store, srv.fetch), 'server');
  assert.equal(await tabB.persist(b.store, srv.fetch), 'full');
  assert.equal(srv.doc.views.length, 50);
  assert.ok(srv.doc.views.some((x) => x.name === 'Fiftieth'));
  assert.equal(srv.doc.views.some((x) => x.name === 'Fifty-first'), false);
  assert.equal(findView(tabB.V.store, b.id), null);
  assert.deepEqual(tabB._test.pending(), []);
  assert.equal(tabB.V.notice, tabA.LIMIT_TEXT);
  assert.match(tabA.LIMIT_TEXT, /Delete one first/);
});

test('resetting a built-in that is the default keeps it the default', async () => {
  fresh();
  const srv = contractServer([{ ...v('performance', 'My performance', 1000), builtin: true }], { default: 'performance' });
  await tabA.loadViews(srv.fetch);
  assert.equal(await tabA.persist(tabA.resetView(tabA.V.store, 'performance'), srv.fetch), 'server');
  assert.deepEqual(srv.doc.views, []);
  assert.equal(srv.doc.default, 'performance');
  assert.equal(tabA.defaultId(tabA.V.store), 'performance');
  // The reset belongs to that one save: a later edit elsewhere is not undone by the next save here.
  await tabB.loadViews(srv.fetch);
  assert.equal(await tabB.persist(renameView(tabB.V.store, 'performance', 'Perf'), srv.fetch), 'server');
  assert.equal(await tabA.persist(setDefault(tabA.V.store, 'usage'), srv.fetch), 'server');
  assert.equal(srv.doc.views.find((x) => x.id === 'performance')?.name, 'Perf');
});

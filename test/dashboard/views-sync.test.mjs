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
    Object.assign(t.V, { loading: null, loaded: false, where: '', note: '', notice: '', doc: null, store: cleanStore({}) });
    t._test.reset();
  }
}
let n = 0;
const rand = () => ((n++ * 7919) % 1000) / 1000;

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
  assert.equal(tabA._test.readPending().length, 0);
});

test('two tabs saving at the same moment both land: the later one merges again after a 409', async () => {
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
  // Each tab now holds the revision the proxy last gave it, exactly as sent.
  assert.ok([tabA.V.doc.revision, tabB.V.doc.revision].includes(srv.doc.revision));
  assert.equal(tabA.V.notice + tabB.V.notice, '', 'nothing was lost, so no note');
});

test('an offline rename does not overwrite a newer rename made elsewhere', async () => {
  fresh();
  const srv = contractServer([v('v-x', 'Start', 1000)]);
  await tabA.loadViews(srv.fetch);
  srv.down = true;
  assert.equal(await tabA.persist(renameView(tabA.V.store, 'v-x', 'Mine'), srv.fetch, 1500), 'local');
  srv.down = false;
  srv.elsewhere((d) => { d.views[0].name = 'Elsewhere'; d.views[0].updated_at = 2000; });
  // The tab reloads once back online and sends what waited.
  tabA.V.loading = null;
  await tabA.loadViews(srv.fetch);
  assert.deepEqual(names(srv.doc), ['Elsewhere']);
  assert.equal(findView(tabA.V.store, 'v-x').name, 'Elsewhere');
  assert.equal(tabA.V.notice, 'Start changed elsewhere, your edit was not saved', 'named as it was on screen before the edit');
  assert.equal(tabA._test.readPending().length, 0, 'the lost edit is not retried');
});

test('a stale tab cannot bring back a view deleted elsewhere', async () => {
  fresh();
  const srv = contractServer([v('v-x', 'Doomed', 1000), v('v-y', 'Other', 1000)]);
  await tabA.loadViews(srv.fetch);
  srv.elsewhere((d) => { d.views = d.views.filter((x) => x.id !== 'v-x'); d.deleted.push({ id: 'v-x', updated_at: 3000 }); });
  const edited = saveView(tabA.V.store, { ...findView(tabA.V.store, 'v-x'), window: 'last24h' });
  assert.equal(await tabA.persist(edited, srv.fetch, 2500), 'server');
  assert.deepEqual(names(srv.doc), ['Other']);
  assert.equal(findView(tabA.V.store, 'v-x'), null);
  assert.match(tabA.V.notice, /^Doomed changed elsewhere/);
  // An edit to a view nobody else touched still lands.
  assert.equal(await tabA.persist(renameView(tabA.V.store, 'v-y', 'Renamed'), srv.fetch, 2600), 'server');
  assert.deepEqual(names(srv.doc), ['Renamed']);
});

test('deleting the old default keeps a newer default chosen elsewhere, and a stale default change loses', async () => {
  fresh();
  const srv = contractServer([v('v-x', 'X', 1000), v('v-y', 'Y', 1000), v('v-z', 'Z', 1000), v('v-w', 'W', 1000)], { default: 'v-x' });
  await tabA.loadViews(srv.fetch);
  srv.elsewhere((d) => { d.default = 'v-y'; });
  assert.equal(await tabA.persist(deleteView(tabA.V.store, 'v-x'), srv.fetch, 2000), 'server');
  assert.deepEqual(names(srv.doc), ['Y', 'Z', 'W']);
  assert.equal(srv.doc.default, 'v-y');
  assert.equal(tabA.V.notice, '');
  // This tab now shows Y as the default. Another browser picks Z, then this
  // tab, not having seen that, picks W: the newer choice stays.
  srv.elsewhere((d) => { d.default = 'v-z'; });
  assert.equal(await tabA.persist(setDefault(tabA.V.store, 'v-w'), srv.fetch, 2100), 'server');
  assert.equal(srv.doc.default, 'v-z');
  assert.equal(tabA.V.store.default, 'v-z');
  assert.equal(tabA.V.notice, 'The default view changed elsewhere, your edit was not saved');
  // Picking again, from the copy that now shows Z, works.
  assert.equal(await tabA.persist(setDefault(tabA.V.store, 'v-w'), srv.fetch, 2200), 'server');
  assert.equal(srv.doc.default, 'v-w');
});

test('a save that finishes clears only its own changes, not another one still waiting', async () => {
  fresh();
  const srv = contractServer([]);
  await tabA.loadViews(srv.fetch);
  await tabB.loadViews(srv.fetch);
  // Tab A's save is on its way: its PUT is held.
  const release = srv.hold();
  const a = saveAsNew(tabA.V.store, findView(tabA.V.store, 'usage'), 'From A', { rand });
  const sending = tabA.persist(a.store, srv.fetch);
  await tick(); await tick();
  // Tab B cannot reach the proxy: its change waits in this browser.
  const offline = async () => { throw new Error('offline'); };
  const b = saveAsNew(tabB.V.store, findView(tabB.V.store, 'usage'), 'From B', { rand });
  assert.equal(await tabB.persist(b.store, offline), 'local');
  release();
  assert.equal(await sending, 'server');
  const waiting = tabB._test.readPending();
  assert.equal(waiting.length, 1, "tab B's change still waits");
  assert.equal(waiting[0].view.name, 'From B');
  // Tab B's next attempt sends it.
  tabB.V.loading = null;
  await tabB.loadViews(srv.fetch);
  assert.deepEqual(names(srv.doc), ['From A', 'From B']);
  assert.equal(tabB._test.readPending().length, 0);
});

test('a 409 that keeps coming is retried three times, then the changes wait', async () => {
  fresh();
  const srv = contractServer([]);
  await tabA.loadViews(srv.fetch);
  const busy = async (url, init = {}) => {
    if (init.method === 'PUT') srv.elsewhere(() => {});
    return srv.fetch(url, init);
  };
  const a = saveAsNew(tabA.V.store, findView(tabA.V.store, 'usage'), 'Busy', { rand });
  assert.equal(await tabA.persist(a.store, busy), 'local');
  assert.equal(srv.conflicts, 4, 'one try and three retries');
  assert.equal(tabA._test.readPending().length, 1);
  assert.equal(await tabA.persist(tabA.V.store, srv.fetch), 'local', 'nothing new to send');
});

test('a name the proxy would refuse is never queued, and a refused save does not block later ones', async () => {
  fresh();
  assert.equal(tabA.nameError('x'.repeat(61)), 'View names can be up to 60 characters.');
  assert.equal(tabA.nameError('é'.repeat(60)), '');
  assert.equal(tabA.nameError('  '), 'Give the view a name.');
  const srv = contractServer([v('v-x', 'Short', 1000)]);
  await tabA.loadViews(srv.fetch);
  // Even if a long name gets past the field, what is sent fits.
  assert.equal(await tabA.persist(renameView(tabA.V.store, 'v-x', 'y'.repeat(61)), srv.fetch, 2000), 'server');
  assert.equal(srv.doc.views[0].name, 'y'.repeat(60));
  assert.equal(tabA._test.readPending().length, 0);
  // A save the proxy refuses is dropped with a note, so the next one goes up.
  const refuse = async (url, init = {}) => (init.method === 'PUT' ? reply(400, { error: 'views[0]: something new' }) : srv.fetch(url, init));
  assert.equal(await tabA.persist(renameView(tabA.V.store, 'v-x', 'Refused'), refuse, 2100), 'rejected');
  assert.equal(tabA.V.notice, 'Views were not saved: views[0]: something new');
  assert.equal(tabA._test.readPending().length, 0);
  assert.equal(await tabA.persist(renameView(tabA.V.store, 'v-x', 'Later'), srv.fetch, 2200), 'server');
  assert.equal(srv.doc.views[0].name, 'Later');
});

test('revisions go back exactly as received, quoted, and a default change compares them only for equality', async () => {
  fresh();
  const srv = contractServer([v('v-x', 'X', 1000), v('v-y', 'Y', 1000), v('v-z', 'Z', 1000)]);
  const seen = [];
  const watch = (url, init = {}) => { if (init.method === 'PUT') seen.push(init.headers['If-Match']); return srv.fetch(url, init); };
  await tabA.loadViews(watch);
  const read = srv.doc.revision;
  assert.equal(await tabA.persist(setDefault(tabA.V.store, 'v-y'), watch), 'server');
  assert.deepEqual(seen, [`"${read}"`]);
  assert.equal(tabA.V.doc.revision, srv.doc.revision);
  // A revision lower than the one the tab read still means the store moved on.
  srv.elsewhere((d) => { d.default = 'v-x'; });
  srv.doc.revision = read - 5000;
  assert.equal(await tabA.persist(setDefault(tabA.V.store, 'v-z'), watch), 'server');
  assert.equal(srv.doc.default, 'v-x');
  assert.match(tabA.V.notice, /^The default view changed elsewhere/);
});

test('a damaged store is replaced from the fresh revision its 409 gives', async () => {
  fresh();
  const srv = contractServer([v('v-x', 'Lost', 1000)]);
  srv.damaged = true;
  await tabA.loadViews(srv.fetch);
  assert.equal(tabA.V.where, 'server', 'the 409 to If-Match "0" gave a document to build on');
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
  assert.equal(tabA.V.where, 'local');
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

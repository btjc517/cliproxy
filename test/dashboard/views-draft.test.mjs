import test from 'node:test';
import assert from 'node:assert/strict';
// An unsaved draft in tab A while tab B saves other fields of the same view.
// Tab A is the page's own modules; tab B is a second copy of views.js.
const mem = new Map();
globalThis.localStorage = { getItem: (k) => (mem.has(k) ? mem.get(k) : null), setItem: (k, v) => mem.set(k, String(v)), removeItem: (k) => mem.delete(k) };
globalThis.window = { dispatchEvent() {}, addEventListener() {} };
globalThis.Event = class { constructor(t) { this.type = t; } };
globalThis.document = { querySelector: () => null, querySelectorAll: () => [], addEventListener() {}, removeEventListener() {} };
const { S } = await import('../../internal/api/dashboard/core.js');
const tabA = await import('../../internal/api/dashboard/screens/views.js');
const tabB = await import('../../internal/api/dashboard/screens/views.js?tab=b');
const { current, edit } = await import('../../internal/api/dashboard/screens/telemetry.js');
const { saveDraft, setDefaultView } = await import('../../internal/api/dashboard/screens/viewmenus.js');

const tick = () => new Promise((r) => setImmediate(r));
const clone = (x) => JSON.parse(JSON.stringify(x));
const reply = (status, body) => ({ ok: status >= 200 && status < 300, status, json: async () => clone(body) });

// /dashboard/views with revisions: a PUT must name the revision it read.
function server() {
  let rev = 1759000000000;
  const s = { doc: { views: [], default: '', deleted: [], revision: rev } };
  s.fetch = async (url, init = {}) => {
    await tick();
    if (init.method !== 'PUT') return reply(200, s.doc);
    if (String(init.headers?.['If-Match']).replace(/"/g, '') !== String(s.doc.revision)) return reply(409, { error: 'changed', revision: s.doc.revision, current: s.doc });
    const body = JSON.parse(init.body);
    s.doc = { views: body.views, default: body.default || '', deleted: body.deleted || [], revision: (rev += 7919) };
    return reply(200, s.doc);
  };
  return s;
}

async function setup() {
  const s = server();
  globalThis.fetch = s.fetch;
  for (const t of [tabA, tabB]) { t._test.reset(); t.V.loading = null; await t.loadViews(s.fetch); }
  S.ui = {};
  return s;
}

const stored = (s, id) => s.doc.views.find((v) => v.id === id);
const tabBColumns = async (s, columns) => {
  const v = tabB.findView(tabB.V.store, 'usage');
  assert.equal(await tabB.persist(tabB.saveView(tabB.V.store, { ...v, columns }), s.fetch), 'server');
};

test('saving a draft after the views refresh keeps the columns another tab saved', async () => {
  const s = await setup();
  edit('usage', (d) => { d.window = 'last24h'; });
  await tabBColumns(s, ['tokens', 'cost']);
  // Set as default saves, which reloads the views tab A shows.
  await setDefaultView('performance');
  assert.deepEqual(tabA.findView(tabA.V.store, 'usage').columns, ['tokens', 'cost'], 'tab A now shows tab B\'s columns as saved');
  // The draft shows the saved columns with its own window on top.
  assert.deepEqual(current('usage').view.columns, ['tokens', 'cost']);
  assert.equal(current('usage').view.window, 'last24h');
  await saveDraft('usage');
  const u = stored(s, 'usage');
  assert.equal(u.window, 'last24h', 'the draft\'s edit is saved');
  assert.deepEqual(u.columns, ['tokens', 'cost'], 'tab B\'s columns are kept');
  assert.equal(s.doc.default, 'performance');
});

test('a draft edited after the refresh still sends only its own fields', async () => {
  const s = await setup();
  edit('usage', (d) => { d.window = 'last24h'; });
  await tabBColumns(s, ['tokens']);
  await setDefaultView('usage');
  // A Display edit made after the refresh joins the same draft.
  edit('usage', (d) => { d.panels = d.panels.slice(0, 2); });
  await saveDraft('usage');
  const u = stored(s, 'usage');
  assert.equal(u.window, 'last24h');
  assert.equal(u.panels.length, 2);
  assert.deepEqual(u.columns, ['tokens']);
});

test('a draft undone to the saved view, after the saved view changed, is no draft', async () => {
  const s = await setup();
  edit('usage', (d) => { d.window = 'last24h'; });
  await tabBColumns(s, ['tokens']);
  await setDefaultView('usage');
  edit('usage', (d) => { d.window = 'last7d'; });
  assert.equal(current('usage').dirty, false);
  assert.deepEqual(current('usage').view.columns, ['tokens']);
});

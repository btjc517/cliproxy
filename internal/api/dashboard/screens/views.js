// Telemetry views: the built-in Allowance, Usage and Performance views, the
// viewer's saved changes to them (overrides) and their own views, the default
// view, and where they are stored. The server keeps overrides and saved views
// at /dashboard/views; when it cannot, this browser keeps them instead.
import { S, prefs, setPref, plainObject, toast } from "../core.js";
import { DAY } from "./timeaxis.js";

export const PANEL_TYPES = ["allowance", "available", "tokens", "cost", "requests", "output", "cache", "ttft", "latency", "throughput", "failures", "activity"];
export const COLUMN_IDS = [
  "weekLeft", "perDay", "heading", "nextReset",
  "tokens", "requests", "input", "cacheWrite", "cacheRead", "output", "cost", "cacheReuse",
  "ttft50", "ttft90", "throughput", "failures", "failovers",
];
export const WINDOWS = [["last24h", "Last 24 hours"], ["last7d", "Last 7 days"], ["around_now", "Around now"]];
export const MAX_VIEWS = 50;
const LOCAL_STORE = "cliproxy-dashboard-views";

export const BUILTINS = [
  {
    id: "allowance", name: "Allowance", window: "around_now", accounts: null, builtin: true,
    panels: [{ type: "allowance", options: { window: "week" } }, { type: "available", options: {} }, { type: "tokens", options: { format: "lines" } }],
    columns: ["weekLeft", "perDay", "heading", "nextReset"],
  },
  {
    id: "usage", name: "Usage", window: "last7d", accounts: null, builtin: true,
    panels: [{ type: "tokens", options: { format: "lines" } }, { type: "cost", options: { format: "bars" } }, { type: "requests", options: { format: "lines" } }, { type: "activity", options: {} }],
    columns: ["tokens", "requests", "input", "cacheWrite", "cacheRead", "output", "cost", "cacheReuse"],
  },
  {
    id: "performance", name: "Performance", window: "last24h", accounts: null, builtin: true,
    panels: [{ type: "ttft", options: {} }, { type: "latency", options: {} }, { type: "throughput", options: {} }, { type: "failures", options: { format: "bars" } }, { type: "available", options: {} }],
    columns: ["requests", "ttft50", "ttft90", "throughput", "failures", "failovers"],
  },
];
const BUILTIN_IDS = BUILTINS.map((v) => v.id);
// What the viewer is told when a new view would pass the limit.
export const LIMIT_TEXT = `You can keep up to ${MAX_VIEWS - BUILTINS.length} views. Delete one first.`;
export const isBuiltin = (id) => BUILTIN_IDS.includes(id);
const copy = (v) => JSON.parse(JSON.stringify(v));

// ---------- cleaning ----------

// The proxy's limits: ids of 1 to 40 lowercase letters, digits or hyphens,
// names of 1 to 60 characters. Nothing past them is ever sent, as the proxy
// would refuse it every time.
const ID_RULE = /^[a-z0-9-]{1,40}$/;
export const MAX_NAME = 60;
const trimName = (name) => Array.from(String(name || "").trim()).slice(0, MAX_NAME).join("").trim();

// Why a name typed for a view cannot be saved, or "" when it can.
export function nameError(name) {
  const n = String(name || "").trim();
  if (!n) return "Give the view a name.";
  if (Array.from(n).length > MAX_NAME) return `View names can be up to ${MAX_NAME} characters.`;
  return "";
}

// A view as stored, cleaned: known panels with plain options, known columns
// once each, a known window, account ids or null. null when it is no view.
export function cleanView(v) {
  if (!plainObject(v) || typeof v.id !== "string" || !ID_RULE.test(v.id)) return null;
  const types = new Set();
  const panels = (Array.isArray(v.panels) ? v.panels : [])
    .filter((p) => plainObject(p) && PANEL_TYPES.includes(p.type) && !types.has(p.type) && types.add(p.type))
    .map((p) => ({ type: p.type, options: { ...(plainObject(p.options) || {}) } }));
  const columns = [...new Set((Array.isArray(v.columns) ? v.columns : []).filter((c) => COLUMN_IDS.includes(c)))];
  return {
    id: v.id,
    name: trimName(v.name) || "Untitled view",
    panels,
    columns,
    window: WINDOWS.some(([w]) => w === v.window) ? v.window : "last7d",
    accounts: Array.isArray(v.accounts) ? v.accounts.filter((id) => typeof id === "string") : null,
    builtin: isBuiltin(v.id),
  };
}

// The stored body, cleaned: overrides of built-ins and the viewer's views in
// creation order, each id once, and the default view's id.
export function cleanStore(body) {
  const b = plainObject(body) || {};
  const seen = new Set();
  const views = [];
  for (const raw of Array.isArray(b.views) ? b.views : []) {
    const v = cleanView(raw);
    if (!v || seen.has(v.id)) continue;
    seen.add(v.id);
    views.push(v);
  }
  return { views: views.slice(0, MAX_VIEWS), default: typeof b.default === "string" ? b.default : "" };
}

// ---------- reading ----------

// Every view in sidebar order: built-ins (with any saved override), then the
// viewer's views in the order they were made.
export function allViews(store) {
  const over = new Map(store.views.filter((v) => v.builtin).map((v) => [v.id, v]));
  return [
    ...BUILTINS.map((b) => (over.has(b.id) ? { ...copy(over.get(b.id)), builtin: true, overridden: true } : copy(b))),
    ...store.views.filter((v) => !v.builtin).map(copy),
  ];
}

export const findView = (store, id) => allViews(store).find((v) => v.id === id) || null;

// The default view: the stored one while it exists, else Allowance.
export function defaultId(store) {
  return findView(store, store.default) ? store.default : "allowance";
}

// Whether two views show the same thing (names aside).
export function sameView(a, b) {
  if (!a || !b) return false;
  const pick = (v) => JSON.stringify([v.panels.map((p) => [p.type, Object.entries(p.options || {}).sort()]), v.columns, v.window, v.accounts || null]);
  return pick(a) === pick(b);
}

// ---------- changing ----------
// Each returns a new store and leaves the old one as it was.

const withViews = (store, views) => ({ ...store, views });

// Saves a view's edited copy: a built-in gets an override, a viewer's view is replaced.
export function saveView(store, draft) {
  const v = cleanView(draft);
  if (!v) return store;
  if (v.builtin) {
    const base = BUILTINS.find((b) => b.id === v.id);
    // Saved exactly as built in: no override needed.
    if (sameView(v, base) && v.name === base.name) return dropOverride(store, v.id);
    const has = store.views.some((x) => x.id === v.id);
    return withViews(store, has ? store.views.map((x) => (x.id === v.id ? v : x)) : [...store.views, v]);
  }
  return withViews(store, store.views.map((x) => (x.id === v.id ? v : x)));
}

// A new id no stored view has.
export function newId(store, rand = Math.random) {
  for (;;) {
    const id = "v-" + Math.floor(rand() * 36 ** 6).toString(36).padStart(6, "0");
    if (!isBuiltin(id) && !store.views.some((v) => v.id === id)) return id;
  }
}

// Adds a copy of draft as a new view named name. keepAccounts stores the
// account selection with it. Returns {store, id}, or the store unchanged when
// the limit is reached.
export function saveAsNew(store, draft, name, { keepAccounts = false, accounts = null, rand } = {}) {
  if (store.views.filter((v) => !v.builtin).length >= MAX_VIEWS - BUILTINS.length) return { store, id: null };
  const id = newId(store, rand);
  const v = cleanView({ ...copy(draft), id, name, accounts: keepAccounts ? accounts : null });
  return { store: withViews(store, [...store.views, v]), id };
}

// Drops a built-in's override, back to how it was built.
// Drops a built-in's override without saying it was reset: the view was
// edited back to how it was built, field by field.
const dropOverride = (store, id) => withViews(store, store.views.filter((v) => v.id !== id));

// Resets a built-in to how it was built, as the viewer asked: every field,
// whatever another tab changed. reset marks it so the save sends a reset
// rather than field changes; persist does not keep the mark.
export const resetView = (store, id) => ({ ...dropOverride(store, id), reset: [...(store.reset || []), id] });

export function renameView(store, id, name) {
  const v = findView(store, id);
  const clean = String(name || "").trim();
  if (!v || !clean) return store;
  return saveView(store, { ...v, name: clean });
}

// A copy placed after the viewer's other views.
export function duplicateView(store, id, rand) {
  const v = findView(store, id);
  if (!v) return { store, id: null };
  return saveAsNew(store, v, `${v.name} copy`, { keepAccounts: !!v.accounts, accounts: v.accounts, rand });
}

// Deletes a viewer's view. Built-ins cannot be deleted. The default falls
// back to Allowance when it was the deleted view.
export function deleteView(store, id) {
  if (isBuiltin(id)) return store;
  return { views: store.views.filter((v) => v.id !== id), default: store.default === id ? "" : store.default };
}

export const setDefault = (store, id) => (findView(store, id) ? { ...store, default: id } : store);

// ---------- windows ----------

// The window a view opens on, at now. Only a view with a forecast panel looks past now.
export function viewWindow(name, now = Date.now()) {
  if (name === "last24h") return { start: now - DAY, end: now };
  if (name === "around_now") return { start: now - 3.5 * DAY, end: now + 3.5 * DAY };
  return { start: now - 7 * DAY, end: now };
}
export const hasForecast = (v) => v.panels.some((p) => p.type === "allowance" || p.type === "available");

// ---------- routes ----------

// Old routes and the bare Telemetry route, as the route they now open. null
// when the hash needs no redirect.
export function redirect(hash, store) {
  const [a, b] = hash.replace(/^#\/?/, "").split("/");
  if (a === "usage") return "#/telemetry/allowance";
  if (a === "performance") return "#/telemetry/performance";
  if (a === "telemetry" && (!b || !findView(store, decodeURIComponent(b)))) return "#/telemetry/" + encodeURIComponent(defaultId(store));
  return null;
}
export const viewHref = (id) => "#/telemetry/" + encodeURIComponent(id);

// ---------- migration ----------

const OLD_METRIC = { ttft: "ttft", latency: "latency", throughput: "throughput", failures: "failures", requests: "requests", cache: "cache", tokens: "tokens", failovers: "failures" };
const OLD_UI_KEYS = ["usSection", "usWindow", "usMetric", "usHistoryView", "historyViewport", "usRange", "pfRange"];

// Carries the old screens' choices into views, once: a customised Performance
// layout becomes an override of the Performance view, bar charts there keep
// their bars, and the old per-screen keys are dropped. Returns the new store.
export function migrateLegacy(store, ui, p) {
  for (const k of OLD_UI_KEYS) delete ui[k];
  const old = plainObject(p.performance);
  const charts = plainObject(p.charts) || {};
  if (!old || store.views.some((v) => v.id === "performance")) return store;
  const base = BUILTINS.find((b) => b.id === "performance");
  const order = Array.isArray(old.order) ? old.order : [];
  const on = new Set(Array.isArray(old.on) ? old.on : []);
  const types = [...new Set(order.filter((id) => on.has(id) && OLD_METRIC[id]).map((id) => OLD_METRIC[id]))];
  if (!types.length) return store;
  const panels = types.map((type) => ({ type, options: charts["perf-" + type] === "bars" ? { format: "bars" } : {} }));
  if (!types.includes("available")) panels.push({ type: "available", options: {} });
  const v = { ...copy(base), panels };
  return sameView(v, base) ? store : saveView(store, v);
}

// ---------- storage ----------
// Every change the viewer makes is an operation: create a view, update some
// of its fields, delete it, or set the default. Operations live in this tab's
// memory only. One save runs at a time: it reads the stored views and their
// revision, applies the operations to that fresh copy and writes it back with
// If-Match on the revision. A 409 means another save got in first, so the
// same operations are applied again to the copy it returns, up to three tries
// in all. The proxy's 200 is the acknowledgement. An operation whose view was
// deleted elsewhere is dropped with a note; otherwise the last writer wins,
// field by field. A save that cannot reach the proxy keeps its operations in
// memory and says "Not saved", with a retry. Nothing is kept in browser
// storage, so nothing stale can be replayed later.
//
// A proxy from before revisions sends none: the same save runs without
// If-Match and without the newer fields.

const NOTE = "Saved views could not be loaded, so only the built-in views show.";
const TRIES = 3;
const FIELDS = ["name", "panels", "columns", "window", "accounts"];
const OLD_KEYS = [LOCAL_STORE, "cliproxy-dashboard-views-pending"];

const same = (a, b) => JSON.stringify(a ?? null) === JSON.stringify(b ?? null);

// The fields of view b that differ from view a, as copies.
export function changedFields(a, b) {
  const fields = {};
  for (const f of FIELDS) if (!same(a[f], b[f])) fields[f] = copy(b[f]);
  return fields;
}

// The operations that turn store a into store b. names: each view's name as
// shown before the change, for the note if the change is lost. Deleting the
// default view clears it as part of the delete, not as a change of its own,
// so a newer default chosen elsewhere survives.
export function opsFor(a, b) {
  const out = [];
  const before = new Map(a.views.map((v) => [v.id, v]));
  const after = new Map(b.views.map((v) => [v.id, v]));
  const changed = (old, v) => {
    const fields = changedFields(old, v);
    return Object.keys(fields).length ? fields : null;
  };
  // Built-ins always exist: an edit, even one back to how it was built, is
  // the fields that changed, so other fields another tab set are kept. Only
  // an explicit reset drops the whole override.
  for (const bv of BUILTINS) {
    if (b.reset?.includes(bv.id)) { if (before.has(bv.id)) out.push({ op: "reset", id: bv.id, name: findView(a, bv.id).name }); continue; }
    const fields = changed(findView(a, bv.id), findView(b, bv.id));
    if (fields) out.push({ op: "update", id: bv.id, fields, name: findView(a, bv.id).name });
  }
  for (const [id, v] of after) {
    if (isBuiltin(id)) continue;
    const old = findView(a, id);
    if (!old) { out.push({ op: "create", id, view: copy(v), name: v.name }); continue; }
    const fields = changed(old, v);
    if (fields) out.push({ op: "update", id, fields, name: old.name });
  }
  for (const [id, v] of before) if (!after.has(id) && !isBuiltin(id)) out.push({ op: "delete", id, name: v.name });
  const cleared = !b.default && a.default && !isBuiltin(a.default) && before.has(a.default) && !after.has(a.default);
  if ((a.default || "") !== (b.default || "") && !cleared) out.push({ op: "default", id: b.default || "" });
  return out;
}

// The stored document as read from the proxy: the views (cleaned), each
// view's updated_at, the deletions, and the revision, null from a proxy
// without revisions. A revision is opaque: it is only sent back.
export function readDoc(body) {
  const b = plainObject(body) || {};
  const store = cleanStore(b);
  const at = {};
  for (const raw of Array.isArray(b.views) ? b.views : []) {
    if (plainObject(raw) && typeof raw.id === "string" && Number.isSafeInteger(raw.updated_at) && raw.updated_at > 0) at[raw.id] = raw.updated_at;
  }
  const deleted = [];
  for (const d of Array.isArray(b.deleted) ? b.deleted : []) {
    if (plainObject(d) && typeof d.id === "string" && Number.isSafeInteger(d.updated_at) && d.updated_at > 0 && !deleted.some((x) => x.id === d.id)) deleted.push({ id: d.id, updated_at: d.updated_at });
  }
  const rev = b.revision;
  return { store, at, deleted, revision: Number.isSafeInteger(rev) || (typeof rev === "string" && rev) ? rev : null };
}

// Applies operations to a document. Returns the new document and the
// operations that were lost because their view was deleted elsewhere.
// Applies operations to a document. Returns the new document, the operations
// lost because their view was deleted elsewhere, and those refused because
// the document would hold more than MAX_VIEWS views. Nothing is ever cut to
// fit: a refused operation is left out whole.
//   create   done already when its id is in the document (its earlier save
//            reached the proxy and only the reply was lost); lost when its id
//            was deleted since. A create never removes a deletion record.
//   update   the changed fields on top of the fresh view; for a built-in,
//            the override goes only when no field differs from the built-in.
//   reset    drops a built-in's override; it stays the default if it was.
//   delete   of a view already gone is done.
//   default  lost when the view it names was deleted.
export function applyOps(doc, ops, now = Date.now()) {
  let views = doc.store.views.slice(), def = doc.store.default || "";
  const at = { ...doc.at };
  let deleted = doc.deleted.slice();
  const lost = [], full = [];
  const gone = (id) => deleted.some((d) => d.id === id);
  // Adds an entry, unless the document is full.
  const add = (v, op) => {
    if (views.length >= MAX_VIEWS) { full.push(op); return false; }
    views = [...views, v];
    return true;
  };
  for (const op of ops) {
    const i = views.findIndex((v) => v.id === op.id);
    if (op.op === "create") {
      if (i >= 0) continue;
      if (gone(op.id)) { lost.push(op); continue; }
      const v = cleanView(op.view);
      if (v && add(v, op)) at[op.id] = now;
    } else if (op.op === "update") {
      // A built-in without an override is the built-in itself.
      const base = i >= 0 ? views[i] : isBuiltin(op.id) ? BUILTINS.find((b) => b.id === op.id) : null;
      if (!base) { lost.push(op); continue; }
      const v = cleanView({ ...copy(base), ...copy(op.fields), id: op.id });
      const plain = v.builtin && BUILTINS.find((b) => b.id === v.id);
      if (plain && sameView(v, plain) && v.name === plain.name) {
        // No field differs from the built-in any more: no override needed.
        views = views.filter((x) => x.id !== op.id);
        delete at[op.id];
      } else if (i >= 0) {
        views = views.map((x, j) => (j === i ? v : x));
        at[op.id] = now;
      } else if (add(v, op)) at[op.id] = now;
    } else if (op.op === "reset") {
      views = views.filter((x) => x.id !== op.id);
      delete at[op.id];
    } else if (op.op === "delete") {
      if (i < 0) continue;
      views = views.filter((x) => x.id !== op.id);
      delete at[op.id];
      deleted = [...deleted.filter((d) => d.id !== op.id), { id: op.id, updated_at: now }];
      if (def === op.id) def = "";
    } else if (op.op === "default") {
      if (op.id && !isBuiltin(op.id) && i < 0) { lost.push(op); continue; }
      def = op.id;
    }
  }
  deleted = deleted.sort((a, b) => a.updated_at - b.updated_at).slice(-200);
  return { doc: { store: cleanStore({ views, default: def }), at, deleted, revision: doc.revision }, lost, full };
}

// The body a proxy stores: with versions and deletions when it keeps
// revisions, else just the views and default (it refuses unknown fields).
function bodyOf(doc) {
  if (doc.revision == null) return doc.store;
  const views = doc.store.views.map((v) => (doc.at[v.id] ? { ...v, updated_at: doc.at[v.id] } : v));
  return { views, default: doc.store.default, deleted: doc.deleted };
}

// The views as last loaded: the store shown, the document read from the
// proxy, whether they loaded, a note when they could not, the last notice
// about a lost or refused change, and whether changes wait unsaved.
export const V = { store: cleanStore({}), doc: null, loaded: false, loading: null, note: "", notice: "", unsaved: false };

// This tab's operations not yet acknowledged, oldest first.
let pending = [];

function say(msg) {
  V.notice = msg;
  if (globalThis.document) toast(msg, true);
}

// Reads the stored document, or null when the proxy cannot be reached. A
// proxy with revisions answers 500 when its file is damaged; a PUT with
// If-Match "0" (never a revision) then returns the document to build on,
// the empty set at a fresh revision. The probe's body has "deleted", which a
// proxy without revisions refuses, so the probe never writes anything there.
async function getDoc(fetchFn) {
  try {
    const res = await fetchFn("/dashboard/views", { cache: "no-store" });
    if (res.ok) return readDoc(await res.json());
    if (res.status !== 500) return null;
    const probe = await putDoc({ store: cleanStore({}), at: {}, deleted: [], revision: "0" }, fetchFn);
    return probe.status === 409 ? probe.doc : null;
  } catch (e) { return null; }
}

// Writes a document. Returns {ok, status, doc, error}: doc is what the proxy
// stored, or for a 409 the document it holds now.
async function putDoc(doc, fetchFn) {
  const headers = { "Content-Type": "application/json" };
  if (doc.revision != null) headers["If-Match"] = `"${doc.revision}"`;
  try {
    const res = await fetchFn("/dashboard/views", { method: "PUT", headers, body: JSON.stringify(bodyOf(doc)) });
    let body = null;
    try { body = await res.json(); } catch (e) { /* no body */ }
    if (res.ok) return { ok: true, doc: body ? readDoc(body) : { ...doc, revision: null } };
    if (res.status === 409 && plainObject(body?.current)) {
      const current = readDoc(body.current);
      if (current.revision == null && body.revision != null) current.revision = body.revision;
      if (current.revision != null) return { ok: false, status: 409, doc: current };
    }
    return { ok: false, status: res.status, error: typeof body?.error === "string" ? body.error : "" };
  } catch (e) { return { ok: false, status: 0 }; }
}

const lostText = (lost) => {
  const names = [...new Set(lost.map((op) => (op.op === "default" ? "The default view" : op.name || "A view")))];
  return `${names.join(", ")} changed elsewhere, your edit was not saved`;
};

function drop(ops) {
  const gone = new Set(ops);
  pending = pending.filter((op) => !gone.has(op));
}

// Sends this tab's operations once. Returns "server", "full" when a new
// view was left out at the limit (the rest was saved), "failed" when the
// proxy could not be reached or kept changing (they stay in memory for a
// retry), or "rejected" when it refused them as invalid (they are dropped,
// as it would refuse them every time).
async function flush(fetchFn) {
  const ops = pending.slice();
  if (!ops.length) return V.unsaved ? "failed" : "server";
  let doc = await getDoc(fetchFn);
  for (let tries = 0; doc && tries < TRIES; tries++) {
    const m = applyOps(doc, ops);
    // A view that would go past the limit is not saved and not kept: the
    // limit note says so. The rest of the save goes ahead.
    const notes = [];
    if (m.lost.length) notes.push(lostText(m.lost));
    if (m.full.length) notes.push(LIMIT_TEXT);
    const nothing = m.full.length + m.lost.length === ops.length;
    const res = nothing ? { ok: true, doc } : await putDoc(m.doc, fetchFn);
    if (res.ok) {
      drop(ops);
      V.doc = res.doc;
      V.store = applyOps(res.doc, pending).doc.store;
      V.unsaved = pending.length > 0;
      V.note = "";
      if (notes.length) say(notes.join(". "));
      return m.full.length ? "full" : "server";
    }
    if (res.status === 409) { doc = res.doc; continue; }
    if (res.status === 400) {
      drop(ops);
      V.store = applyOps(doc, pending).doc.store;
      V.unsaved = pending.length > 0;
      say(`Views were not saved: ${res.error || "the proxy refused them"}`);
      return "rejected";
    }
    break;
  }
  V.unsaved = true;
  return "failed";
}

// One queue per tab: a save starts only after the one before it ends.
let queue = Promise.resolve();
const enqueue = (fetchFn) => (queue = queue.then(() => flush(fetchFn)));

// Sends the changes still waiting after a failed save.
export const retrySaves = (fetchFn = globalThis.fetch) => enqueue(fetchFn);

// Loads the views from the proxy, once; later calls get the same promise.
// When the proxy cannot be reached the built-in views show with a note.
// fetchFn is for tests.
export function loadViews(fetchFn = globalThis.fetch) {
  if (V.loading) return V.loading;
  V.loading = (async () => {
    // Earlier versions of this page kept views in browser storage.
    for (const k of OLD_KEYS) { try { localStorage.removeItem(k); } catch (e) { /* storage blocked */ } }
    const doc = await getDoc(fetchFn);
    V.doc = doc;
    V.store = doc ? applyOps(doc, pending).doc.store : applyOps({ store: cleanStore({}), at: {}, deleted: [], revision: null }, pending).doc.store;
    V.note = doc ? "" : NOTE;
    const migrated = migrateLegacy(V.store, S.ui, prefs());
    if (prefs().performance) setPref("performance", undefined);
    if (migrated !== V.store) await persist(migrated, fetchFn);
    V.loaded = true;
    return V.store;
  })();
  return V.loading;
}

// Saves the change from the views shown to next. V.store shows next at once.
// Returns "server", "full", "failed" or "rejected" (see flush).
export async function persist(next, fetchFn = globalThis.fetch) {
  const ops = opsFor(V.store, next);
  // A reset mark belongs to this one change; the store shown does not keep it.
  V.store = { views: next.views, default: next.default };
  V.notice = "";
  if (!ops.length) return V.unsaved ? "failed" : "server";
  pending.push(...ops);
  return enqueue(fetchFn);
}

export const _test = {
  reset() { pending = []; queue = Promise.resolve(); },
  pending: () => pending.slice(),
};

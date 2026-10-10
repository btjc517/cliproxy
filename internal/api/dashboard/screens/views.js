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
    if (sameView(v, base) && v.name === base.name) return resetView(store, v.id);
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
export const resetView = (store, id) => withViews(store, store.views.filter((v) => v.id !== id));

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
// A save is a list of operations, one per view touched: set a view, delete
// one, or set the default. Each records the version it was based on: the
// view's updated_at, or for the default the document's revision. One queue
// per tab sends them, oldest first. It reads the stored document, drops any
// operation whose view changed elsewhere since its base (the stored version
// wins and a note says so), applies the rest and writes the result back with
// If-Match on the revision it read. A 409 means another save got in first:
// the operations are merged again onto the document it returns, up to three
// times. Operations wait in this browser until the proxy acknowledges them,
// and only acknowledged ones are cleared.
//
// A proxy from before revisions sends none. It gets the earlier behaviour:
// the operations applied on top of what it holds, with no version checks.

const PENDING = "cliproxy-dashboard-views-pending";
const NOTE = "Views are saved in this browser only, as the proxy could not store them.";
const MAX_DELETED = 200;
const RETRIES = 3;

// The changes that turn store a into store b. Deleting the default view
// clears it as a side effect of the delete, so that is no change of its own:
// a newer default chosen elsewhere then survives the delete.
export function diffStores(a, b) {
  const out = [];
  const before = new Map(a.views.map((v) => [v.id, v]));
  const after = new Map(b.views.map((v) => [v.id, v]));
  for (const [id, v] of after) if (JSON.stringify(before.get(id)) !== JSON.stringify(v)) out.push({ op: "set", id, view: copy(v) });
  for (const id of before.keys()) if (!after.has(id)) out.push({ op: "delete", id });
  const cleared = !b.default && a.default && before.has(a.default) && !after.has(a.default);
  if ((a.default || "") !== (b.default || "") && !cleared) out.push({ op: "default", id: b.default || "" });
  return out;
}

// The stored document as read from the proxy: the views (cleaned), each
// view's updated_at, the deletions, and the revision, null from a proxy
// without revisions.
export function readDoc(body) {
  const b = plainObject(body) || {};
  const store = cleanStore(b);
  const at = {};
  for (const raw of Array.isArray(b.views) ? b.views : []) {
    if (plainObject(raw) && typeof raw.id === "string" && Number.isSafeInteger(raw.updated_at)) at[raw.id] = raw.updated_at;
  }
  const deleted = [];
  for (const d of Array.isArray(b.deleted) ? b.deleted : []) {
    if (plainObject(d) && typeof d.id === "string" && Number.isSafeInteger(d.updated_at) && !deleted.some((x) => x.id === d.id)) deleted.push({ id: d.id, updated_at: d.updated_at });
  }
  return { store, at, deleted, revision: validRevision(b.revision) ? b.revision : null };
}

// A revision as the proxy sends it: an opaque number or string.
function validRevision(r) {
  return Number.isSafeInteger(r) || (typeof r === "string" && r !== "");
}

// The version of a view in a document: its updated_at while it exists, else
// when it was deleted, else 0.
function versionOf(doc, id) {
  if (doc.store.views.some((v) => v.id === id)) return doc.at[id] || 0;
  return doc.deleted.find((d) => d.id === id)?.updated_at || 0;
}

// Applies operations to a store with no checks, newest last. This is what
// the tab shows while they wait, and what a proxy without revisions gets.
export function applyChanges(store, changes) {
  let views = store.views.slice(), def = store.default || "";
  for (const c of changes || []) {
    if (!plainObject(c) || typeof c.id !== "string") continue;
    if (c.op === "set") {
      const v = cleanView(c.view);
      if (!v || v.id !== c.id) continue;
      const i = views.findIndex((x) => x.id === c.id);
      if (i >= 0) views[i] = v; else views.push(v);
    } else if (c.op === "delete") {
      views = views.filter((x) => x.id !== c.id);
      if (def === c.id) def = "";
    } else if (c.op === "default") def = c.id;
  }
  return cleanStore({ views, default: def });
}

// Merges operations into a document with revisions. An operation whose view
// changed since its base, or was deleted since, is lost: the stored version
// wins. So is a later operation on a view that lost, as it built on the lost
// one. A default change is lost when the document moved on since its base and
// another default was chosen meanwhile. An operation the document already
// holds (another tab sent it) counts as done. Returns the merged document and
// which operations were done and lost.
export function mergeOps(doc, ops) {
  let views = doc.store.views.slice(), def = doc.store.default || "";
  const at = { ...doc.at };
  let deleted = doc.deleted.slice();
  const done = [], lost = [], lostIds = new Set();
  const version = (id) => versionOf({ store: { views }, at, deleted }, id);
  for (const op of ops) {
    if (op.op === "default") {
      const moved = op.baseRev != null && doc.revision != null && doc.revision !== op.baseRev;
      if (moved && def !== (op.baseDefault || "") && def !== op.id) { lost.push(op); continue; }
      def = op.id;
      done.push(op);
      continue;
    }
    if (lostIds.has(op.id)) { lost.push(op); continue; }
    const cur = version(op.id);
    const live = views.some((v) => v.id === op.id);
    if (op.at && cur === op.at) { done.push(op); continue; }
    if (op.base != null) {
      const vanished = op.op === "set" && op.base > 0 && !live && !deleted.some((d) => d.id === op.id);
      if (cur > op.base || vanished) { lost.push(op); lostIds.add(op.id); continue; }
    }
    if (op.op === "set") {
      const v = cleanView(op.view);
      if (!v || v.id !== op.id) { lost.push(op); continue; }
      const i = views.findIndex((x) => x.id === op.id);
      views = i >= 0 ? views.map((x, j) => (j === i ? v : x)) : [...views, v];
      if (op.at) at[op.id] = op.at;
      deleted = deleted.filter((d) => d.id !== op.id);
    } else if (op.op === "delete") {
      if (live) {
        views = views.filter((x) => x.id !== op.id);
        delete at[op.id];
        deleted = [...deleted.filter((d) => d.id !== op.id), { id: op.id, updated_at: op.at || Date.now() }];
        if (def === op.id) def = "";
      }
    }
    done.push(op);
  }
  deleted = deleted.sort((a, b) => a.updated_at - b.updated_at).slice(-MAX_DELETED);
  return { doc: { store: cleanStore({ views, default: def }), at, deleted, revision: doc.revision }, done, lost };
}

// The body a proxy stores: with versions and deletions when it keeps
// revisions, else just the views and default (it refuses unknown fields).
function bodyOf(doc) {
  if (doc.revision == null) return doc.store;
  const views = doc.store.views.map((v) => (doc.at[v.id] ? { ...v, updated_at: doc.at[v.id] } : v));
  return { views, default: doc.store.default, deleted: doc.deleted };
}

function readJSON(key, fallback) {
  try { const raw = localStorage.getItem(key); return raw ? JSON.parse(raw) : fallback; } catch (e) { return fallback; }
}
function writeJSON(key, value) {
  try { localStorage.setItem(key, JSON.stringify(value)); return true; } catch (e) { return false; }
}
function forget(key) {
  try { localStorage.removeItem(key); } catch (e) { /* storage blocked */ }
}

// The views as last shown in this browser, for when the proxy is unreachable,
// with the document they were based on. kept marks this format: an earlier
// version kept a bare store here (see legacyPending).
const LOCAL_FORMAT = 2;
function readLocal() {
  const raw = readJSON(LOCAL_STORE, {});
  return cleanStore(raw?.kept === LOCAL_FORMAT ? raw.store : raw);
}
function readLocalDoc() {
  const raw = readJSON(LOCAL_STORE, null);
  return raw?.kept === LOCAL_FORMAT && raw.doc ? readDoc(raw.doc) : null;
}
const writeLocal = (store, doc = null) => writeJSON(LOCAL_STORE, { kept: LOCAL_FORMAT, store, doc: doc ? { ...bodyOf(doc), revision: doc.revision } : null });

// Operations the proxy has not acknowledged, oldest first, shared by every
// tab of this browser. Kept in memory as well, for when storage is blocked.
let memPending = [];
let seq = 0;
const uid = () => Date.now().toString(36) + "-" + (++seq).toString(36) + "-" + Math.floor(Math.random() * 36 ** 4).toString(36);
function readPending() {
  let list;
  try {
    const raw = localStorage.getItem(PENDING);
    list = raw ? JSON.parse(raw) : [];
  } catch (e) { list = memPending; }
  if (!Array.isArray(list)) list = [];
  // Operations from an earlier version have no id and no base: they get an
  // id, and are applied without version checks.
  if (list.some((op) => plainObject(op) && !op.uid)) {
    list = list.filter(plainObject).map((op) => (op.uid ? op : { ...op, uid: uid(), base: null }));
    writePending(list);
  }
  return list.filter(plainObject);
}
function writePending(list) {
  memPending = list;
  if (list.length) writeJSON(PENDING, list); else forget(PENDING);
}

// An earlier version kept the whole store here with no operations. Its views
// become operations, so they reach the proxy without undoing other views.
function legacyPending() {
  const pending = readPending();
  const raw = readJSON(LOCAL_STORE, null);
  if (pending.length || !raw || raw.kept === LOCAL_FORMAT) return pending;
  const old = cleanStore(raw);
  const out = old.views.map((v) => ({ uid: uid(), op: "set", id: v.id, view: v, base: null }));
  if (old.default) out.push({ uid: uid(), op: "default", id: old.default, baseRev: null });
  writePending(out);
  return out;
}

// Gives changes their ids and bases. A view's base is its version in the
// last document read, or the stamp of a waiting operation on it, as that one
// lands first. The default's base is the revision read and the default it
// held, after the waiting operations.
function stampOps(changes, pending, doc, now) {
  const last = {};
  let def = doc ? doc.store.default || "" : "";
  for (const op of pending) {
    if (op.op === "default") def = op.id;
    else { last[op.id] = op.at; if (op.op === "delete" && def === op.id) def = ""; }
  }
  return changes.map((c) => {
    const op = { ...c, uid: uid() };
    if (c.op === "default") {
      Object.assign(op, { baseRev: doc ? doc.revision : null, baseDefault: def });
      def = c.id;
      return op;
    }
    const base = !doc || doc.revision == null ? null : last[c.id] ?? versionOf(doc, c.id);
    op.base = base;
    op.at = Math.max(now, (base || 0) + 1, (last[c.id] || 0) + 1);
    last[c.id] = op.at;
    return op;
  });
}

// The views as last loaded: the store shown, the document read from the
// proxy, where the views live, whether they loaded, and the notes to show.
export const V = { store: cleanStore({}), doc: null, where: "", loaded: false, loading: null, note: "", notice: "" };

// Says what was not saved, once, in a short note.
function say(msg) {
  V.notice = msg;
  if (globalThis.document) toast(msg, true);
}

// Reads the stored document. A proxy with revisions answers 500 when its
// file is damaged; a PUT with If-Match "0" (never a revision, so always
// stale) then returns the document to build on, the empty set at a fresh
// revision. The probe's body has "deleted", which a proxy without revisions
// refuses as an unknown field, so the probe can never write anything there.
async function getDoc(fetchFn) {
  try {
    const res = await fetchFn("/dashboard/views", { cache: "no-store" });
    if (res.ok) return readDoc(await res.json());
    if (res.status !== 500) return null;
    const probe = await putDoc({ store: cleanStore({}), at: {}, deleted: [], revision: "0" }, fetchFn);
    return probe.status === 409 ? probe.doc : null;
  } catch (e) { return null; }
}

// Writes a document. Returns {ok, status, doc}: doc is what the proxy stored,
// or for a 409 the document it holds now. Revisions are opaque: sent back as
// read, never compared for order or counted.
async function putDoc(doc, fetchFn) {
  const headers = { "Content-Type": "application/json" };
  if (doc.revision != null) headers["If-Match"] = `"${doc.revision}"`;
  try {
    const res = await fetchFn("/dashboard/views", { method: "PUT", headers, body: JSON.stringify(bodyOf(doc)) });
    let body = null;
    try { body = await res.json(); } catch (e) { /* no body */ }
    if (res.ok) {
      const stored = body ? readDoc(body) : null;
      if (stored && (doc.revision == null || stored.revision != null)) return { ok: true, doc: stored };
      // Stored, but the reply did not say as what: the next save reads it again.
      return { ok: true, doc: { ...doc, revision: null } };
    }
    if (res.status === 409 && plainObject(body?.current)) {
      const current = readDoc(body.current);
      if (current.revision == null && validRevision(body.revision)) current.revision = body.revision;
      return current.revision == null ? { ok: false, status: 0 } : { ok: false, status: 409, doc: current };
    }
    return { ok: false, status: res.status, error: typeof body?.error === "string" ? body.error : "" };
  } catch (e) { return { ok: false, status: 0 }; }
}

function keepLocally() {
  writeLocal(V.store, V.doc);
  V.where = "local";
  V.note = NOTE;
  return "local";
}

// Drops operations from the waiting list by id, leaving any added meanwhile.
function clearOps(ops) {
  const gone = new Set(ops.map((op) => op.uid));
  const rest = readPending().filter((op) => !gone.has(op.uid));
  writePending(rest);
  return rest;
}

// The view's name as it was on screen before the edit, else as edited.
const nameOf = (op) => op.was || op.view?.name || (V.doc && findView(V.doc.store, op.id)?.name) || "A view";
const lostText = (lost) => {
  const names = [...new Set(lost.map((op) => (op.op === "default" ? "The default view" : nameOf(op))))];
  return `${names.join(", ")} changed elsewhere, your edit was not saved`;
};

// Sends what waits, once. Returns where the views now live: "server",
// "local" while the proxy cannot take them, or "rejected" when it refused
// them as invalid (they are dropped, as it would refuse them every time).
async function flush(fetchFn) {
  const ops = readPending();
  if (!ops.length) return V.where || "server";
  let doc = await getDoc(fetchFn);
  if (!doc) return keepLocally();
  for (let attempt = 0; attempt <= RETRIES; attempt++) {
    const checked = doc.revision != null;
    const m = checked ? mergeOps(doc, ops) : { doc: { ...doc, store: applyChanges(doc.store, ops) }, done: ops, lost: [] };
    const res = await putDoc(m.doc, fetchFn);
    if (res.ok) {
      const note = m.lost.length ? lostText(m.lost) : "";
      const rest = clearOps([...m.done, ...m.lost]);
      V.doc = res.doc;
      V.store = applyChanges(res.doc.store, rest);
      V.where = "server";
      V.note = "";
      if (!rest.length) forget(LOCAL_STORE);
      if (note) say(note);
      return "server";
    }
    if (res.status === 409 && res.doc) { doc = res.doc; continue; }
    if (res.status === 400) {
      clearOps(ops);
      V.store = applyChanges(doc.store, readPending());
      say(`Views were not saved: ${res.error || "the proxy refused them"}`);
      return "rejected";
    }
    return keepLocally();
  }
  // Still changing elsewhere after every retry: they wait for the next save.
  return keepLocally();
}

// One queue per tab: a save starts only after the one before it ends.
let queue = Promise.resolve();
const enqueue = (fetchFn) => (queue = queue.then(() => flush(fetchFn)));

// Loads views from the server, else from this browser, with operations still
// waiting here shown on top and sent up. Runs once; later calls get the same
// promise. fetchFn is for tests.
export function loadViews(fetchFn = globalThis.fetch) {
  if (V.loading) return V.loading;
  V.loading = (async () => {
    const doc = await getDoc(fetchFn);
    const pending = legacyPending();
    if (doc) {
      V.doc = doc;
      V.store = applyChanges(doc.store, pending);
      V.where = "server";
      V.note = "";
      if (pending.length) await enqueue(fetchFn);
    } else {
      V.doc = readLocalDoc();
      V.store = applyChanges(readLocal(), pending);
      V.where = "local";
      V.note = NOTE;
    }
    const migrated = migrateLegacy(V.store, S.ui, prefs());
    if (prefs().performance) setPref("performance", undefined);
    if (migrated !== V.store) await persist(migrated, fetchFn);
    V.loaded = true;
    return V.store;
  })();
  return V.loading;
}

// Saves the change from the views shown to next: queued as operations and
// sent in turn. V.store shows next at once. Returns where the views went:
// "server", "local" or "rejected".
export async function persist(next, fetchFn = globalThis.fetch, now = Date.now()) {
  // Each change keeps the name the view had on screen, for the note if it is lost.
  const changes = diffStores(V.store, next).map((c) => (c.op === "default" ? c : { ...c, was: findView(V.store, c.id)?.name || "" }));
  V.store = next;
  V.notice = "";
  if (!changes.length) return V.where || "server";
  const pending = readPending();
  writePending([...pending, ...stampOps(changes, pending, V.doc, now)]);
  return enqueue(fetchFn);
}

export const _test = {
  readLocal, writeLocal, readPending, LOCAL_STORE, PENDING,
  reset() { memPending = []; queue = Promise.resolve(); },
};

// Telemetry views: the built-in Allowance, Usage and Performance views, the
// viewer's saved changes to them (overrides) and their own views, the default
// view, and where they are stored. The server keeps overrides and saved views
// at /dashboard/views; when it cannot, this browser keeps them instead.
import { S, prefs, setPref, plainObject } from "../core.js";
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

// A view as stored, cleaned: known panels with plain options, known columns
// once each, a known window, account ids or null. null when it is no view.
export function cleanView(v) {
  if (!plainObject(v) || typeof v.id !== "string" || !v.id || v.id.length > 64) return null;
  const types = new Set();
  const panels = (Array.isArray(v.panels) ? v.panels : [])
    .filter((p) => plainObject(p) && PANEL_TYPES.includes(p.type) && !types.has(p.type) && types.add(p.type))
    .map((p) => ({ type: p.type, options: { ...(plainObject(p.options) || {}) } }));
  const columns = [...new Set((Array.isArray(v.columns) ? v.columns : []).filter((c) => COLUMN_IDS.includes(c)))];
  return {
    id: v.id,
    name: String(v.name || "").trim().slice(0, 80) || "Untitled view",
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

function readLocal() {
  try { return cleanStore(JSON.parse(localStorage.getItem(LOCAL_STORE) || "{}")); } catch (e) { return cleanStore({}); }
}
function writeLocal(store) {
  try { localStorage.setItem(LOCAL_STORE, JSON.stringify(store)); return true; } catch (e) { return false; }
}

// The views as last loaded: the store, where it lives, and whether it loaded.
export const V = { store: cleanStore({}), where: "", loaded: false, loading: null, note: "" };

// Loads views from the server, else from this browser. Runs once; later
// calls get the same promise. fetchFn is for tests.
export function loadViews(fetchFn = globalThis.fetch) {
  if (V.loading) return V.loading;
  V.loading = (async () => {
    let store = null;
    try {
      const res = await fetchFn("/dashboard/views", { cache: "no-store" });
      if (res.ok) { store = cleanStore(await res.json()); V.where = "server"; }
    } catch (e) { /* unreachable: keep them in this browser */ }
    if (!store) { store = readLocal(); V.where = "local"; V.note = "Views are saved in this browser only, as the proxy could not store them."; }
    // Views kept here while the server could not store them move to it once.
    const local = readLocal();
    if (V.where === "server" && !store.views.length && local.views.length) {
      store = local;
      if (await putViews(store, fetchFn)) { try { localStorage.removeItem(LOCAL_STORE); } catch (e) { /* storage blocked */ } }
    }
    const migrated = migrateLegacy(store, S.ui, prefs());
    if (prefs().performance) setPref("performance", undefined);
    V.store = migrated;
    if (migrated !== store) await persist(migrated, fetchFn);
    V.loaded = true;
    return V.store;
  })();
  return V.loading;
}

async function putViews(store, fetchFn) {
  try {
    const res = await fetchFn("/dashboard/views", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(store) });
    return res.ok;
  } catch (e) { return false; }
}

// Stores the views: on the server when it takes them, else in this browser.
// Returns where they went.
export async function persist(store, fetchFn = globalThis.fetch) {
  V.store = store;
  if (await putViews(store, fetchFn)) {
    V.where = "server";
    V.note = "";
    try { localStorage.removeItem(LOCAL_STORE); } catch (e) { /* storage blocked */ }
    return "server";
  }
  writeLocal(store);
  V.where = "local";
  V.note = "Views are saved in this browser only, as the proxy could not store them.";
  return "local";
}

export const _test = { readLocal, writeLocal, LOCAL_STORE };

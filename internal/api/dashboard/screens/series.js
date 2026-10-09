// Measures over time for any window: usage buckets per account, performance
// buckets for the picked accounts, and sums over a stretch of time. Every
// panel, table and figure card reads through here, so their numbers agree.
import { S, scopeParam, dayKey, loadSpan } from "../core.js";

export const EMPTY = () => ({ requests: 0, failed: 0, input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, api_cost: 0 });
export function addC(a, b) { if (b) for (const k in a) a[k] += Number(b[k]) || 0; return a; }

// ---------- usage ----------

// Usage buckets from the last reply: starts and ends in ms, the bucket size,
// and each account's counters per bucket. null before any usage arrived.
export function usageData() {
  const ur = S.data?.summary?.usage_range;
  if (!ur || !Array.isArray(ur.starts) || !ur.starts.length) return null;
  const starts = ur.starts.map((x) => Date.parse(x));
  const step = (Number(ur.bucket_seconds) || 3600) * 1000;
  const ends = Array.isArray(ur.ends) && ur.ends.length === starts.length ? ur.ends.map((x) => Date.parse(x)) : starts.map((t) => t + step);
  return { starts, ends, step, accounts: ur.accounts || {} };
}

// Bucket indexes that overlap [from, to). Buckets are whole: a bucket that
// starts before from counts in full, as counters cannot be split.
export function bucketsIn(d, from, to) {
  const out = [];
  if (!d) return out;
  for (let i = 0; i < d.starts.length; i++) if (d.starts[i] < to && d.ends[i] > from) out.push(i);
  return out;
}

// Totals over [from, to) for the given accounts, each account's own totals,
// and whether the buckets cover the whole stretch up to now.
export function usageSum(d, ids, from, to, now = Date.now()) {
  const sum = EMPTY(), per = {};
  for (const id of ids) per[id] = EMPTY();
  const idx = bucketsIn(d, from, to);
  for (const i of idx) for (const id of ids) { const b = d.accounts[id]?.[i]; if (b) { addC(sum, b); addC(per[id], b); } }
  const until = Math.min(to, now);
  const covered = !!d && idx.length > 0 && d.starts[idx[0]] <= from && d.ends[idx[idx.length - 1]] >= until;
  return { sum, per, covered, first: idx.length ? d.starts[idx[0]] : null };
}

// Buckets to draw: the loaded ones, or whole local days once a window is
// longer than three days, so a week reads as seven points, not 168.
export function displayBuckets(d, v) {
  if (!d || d.step >= 864e5 || v.end - v.start <= 3 * 864e5) return d;
  const starts = [], ends = [], accounts = {};
  for (const id in d.accounts) accounts[id] = [];
  let key = "";
  for (let i = 0; i < d.starts.length; i++) {
    const k = dayKey(d.starts[i]);
    if (k !== key) {
      key = k;
      starts.push(d.starts[i]);
      ends.push(d.ends[i]);
      for (const id in accounts) accounts[id].push(null);
    } else ends[ends.length - 1] = d.ends[i];
    const at = starts.length - 1;
    for (const id in accounts) { const b = d.accounts[id]?.[i]; if (b) accounts[id][at] = addC(accounts[id][at] || EMPTY(), b); }
  }
  return { starts, ends, step: 864e5, accounts };
}

// The bucket that holds t, or -1.
export function bucketAt(d, t) {
  if (!d) return -1;
  for (let i = 0; i < d.starts.length; i++) if (d.starts[i] <= t && t < d.ends[i]) return i;
  return -1;
}

// Provider totals per day from the history, for days before per-account
// detail starts. days: [{date, providers: {claude: counters}}].
export function historyBefore(before, from, provs) {
  const h = S.data?.summary?.history?.days || [];
  const sum = EMPTY();
  let any = false;
  const fromKey = from ? dayKey(from) : "";
  const beforeKey = dayKey(before);
  for (const d of h) {
    if (d.date >= beforeKey || (fromKey && d.date < fromKey)) continue;
    for (const [p, x] of Object.entries(d.providers || {})) if (!provs || provs.includes(p)) { addC(sum, x); any = true; }
  }
  return any ? sum : null;
}

// ---------- performance ----------

const SUM_KEYS = ["requests", "failed", "failovers", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens"];

// Performance for the picked accounts over the loaded window: the summary
// (percentiles included when the backend merged exactly these accounts) and
// its buckets with start and end times. When the picked accounts are some but
// not all of a provider and no merged scope arrived, counts are added up here
// and every percentile is left out.
export function perfScope(sc, src = S.data?.summary?.performance, merged = S.dataScope) {
  if (!src?.scopes) return null;
  const step = (Number(src.bucket_seconds) || 3600) * 1000;
  const withTimes = (series) => (series || []).filter((b) => b && b.start).map((b) => ({ ...b, t0: Date.parse(b.start), t1: Date.parse(b.start) + step }));
  let q;
  if (!sc.some) q = src.scopes[sc.prov];
  else if (merged && merged === scopeParam(sc.ids) && src.scopes.selection) q = src.scopes.selection;
  if (q) return { q, series: withTimes(q.series), step, custom: src.range === "custom", range: src.range };
  const list = sc.ids.map((id) => src.scopes[id]).filter(Boolean);
  if (!list.length) return null;
  const len = Math.max(0, ...list.map((p) => (p.series || []).length));
  const series = [];
  for (let i = 0; i < len; i++) {
    const pts = list.map((p) => { const s = p.series || []; return s[s.length - len + i]; }).filter(Boolean);
    const b = { start: pts[0]?.start };
    for (const k of SUM_KEYS) if (pts.some((x) => x && k in x)) b[k] = pts.reduce((t, x) => t + (Number(x[k]) || 0), 0);
    series.push(b);
  }
  const out = { requests: 0, failed: 0, partial: true };
  for (const p of list) { out.requests += Number(p.requests) || 0; out.failed += Number(p.failed) || 0; }
  if (list.some((p) => "failovers" in p)) out.failovers = list.reduce((t, p) => t + (Number(p.failovers) || 0), 0);
  return { q: out, series: withTimes(series), step, custom: src.range === "custom", range: src.range };
}

// Counts over [from, to) from performance buckets.
export function perfCounts(ps, from, to) {
  const out = { requests: 0, failed: 0, failovers: 0, any: false, hasFailovers: false };
  for (const b of ps?.series || []) {
    if (b.t0 >= to || b.t1 <= from) continue;
    out.any = true;
    out.requests += Number(b.requests) || 0;
    out.failed += Number(b.failed) || 0;
    if ("failovers" in b) { out.hasFailovers = true; out.failovers += Number(b.failovers) || 0; }
  }
  return out;
}

// The performance bucket holding t, or null.
export const perfAt = (ps, t) => (ps?.series || []).find((b) => b.t0 <= t && t < b.t1) || null;

// ---------- a selection's own performance ----------

// Percentiles cannot be added up from buckets, so a selected range gets its
// own reading from a backend that measures custom windows. RP holds the last.
export const RP = { key: "", perf: null, scope: "", loading: "" };

const rangeKey = (span, scope) => (span ? span.start + "," + span.end + "|" + scope : "");

// Loads the performance for a selected range, once per range and scope, then
// redraws. Does nothing on a backend without custom windows.
export async function loadRangePerf(range, scope, fetchFn = globalThis.fetch) {
  if (!range || S.data?.summary?.performance?.range !== "custom") return;
  const span = loadSpan(range);
  const key = rangeKey(span, scope);
  if (RP.key === key || RP.loading === key) return;
  RP.loading = key;
  try {
    const iso = (t) => new Date(t).toISOString().replace(/\.\d{3}Z$/, "Z");
    const q = new URLSearchParams({ range: "24h", perf_start: iso(span.start), perf_end: iso(span.end) });
    if (scope) q.set("scope", scope);
    const res = await fetchFn("/dashboard/data?" + q, { cache: "no-store" });
    if (!res.ok) throw new Error(String(res.status));
    const data = await res.json();
    if (RP.loading !== key) return;
    Object.assign(RP, { key, perf: data?.summary?.performance || null, scope, loading: "" });
    window.dispatchEvent(new Event("dash:render"));
  } catch (e) {
    if (RP.loading === key) RP.loading = "";
  }
}

// The selected range's performance for these accounts, once it has arrived.
export function rangePerfFor(range, sc, scope) {
  if (!range || !RP.perf) return null;
  if (RP.key !== rangeKey(loadSpan(range), scope)) return null;
  return perfScope(sc, RP.perf, RP.scope);
}

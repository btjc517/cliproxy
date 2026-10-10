// Measures over time for any window: usage buckets per account, performance
// buckets for the picked accounts, and sums over a stretch of time. Every
// panel, table and figure card reads through here, so their numbers agree.
import { S, scopeParam, dayKey, fallbackRange, perfSpan } from "../core.js";
import { midnight, addDays, tzOffset } from "./timeaxis.js";

export const EMPTY = () => ({ requests: 0, failed: 0, input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, api_cost: 0 });
export function addC(a, b) { if (b) for (const k in a) a[k] += Number(b[k]) || 0; return a; }
const nonZero = (c) => !!c && ["requests", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens"].some((k) => Number(c[k]) > 0);

// ---------- bucket edges ----------

// The proxy's own rule for where buckets of hours local hours start, walked
// in quarter hours as it does (usagestats isBucketStart and nextBucketStart):
// a whole local hour divisible by hours, or a quarter hour whose local day,
// or hours-long block of the day, differs from the one before it. So an
// hourly bucket from 01:00 BST on 25 October ends at 01:00 GMT, an hour on,
// while a three hour one from midnight BST runs to 03:00 GMT, four hours on.
const SLOT = 15 * 60e3;
function isBucketStart(t, hours) {
  const at = new Date(t + tzOffset(t)), before = new Date(t - SLOT + tzOffset(t - SLOT));
  if (hours < 24 && at.getUTCMinutes() === 0 && at.getUTCSeconds() === 0 && at.getUTCHours() % hours === 0) return true;
  return at.toISOString().slice(0, 10) !== before.toISOString().slice(0, 10) || Math.floor(at.getUTCHours() / hours) !== Math.floor(before.getUTCHours() / hours);
}

// The start of the bucket after the one starting at t: the next local
// boundary, or for buckets of several days, that many local days on.
export function nextBucketStart(t, step) {
  const hours = Math.max(1, Math.round(step / 3600e3));
  if (hours > 24) return midnight(addDays(dayKey(t), Math.round(hours / 24)));
  let next = Math.floor(t / SLOT) * SLOT + SLOT;
  for (let i = 0; i < 4 * 24 * 3 && !isBucketStart(next, hours); i++) next += SLOT;
  return next;
}

// Each bucket ends where the next one starts; the proxy lists every bucket,
// so that is exact across clock changes. The last ends at the next local
// bucket start.
export function bucketEnds(starts, step) {
  return starts.map((t, i) => (i + 1 < starts.length ? starts[i + 1] : nextBucketStart(t, step)));
}

// ---------- usage ----------

// Usage buckets from a reply (the last one by default): starts and ends in
// ms, the bucket size, each account's counters per bucket, and when the log
// backfill hands over to the proxy's own tally. null before usage arrived.
export function usageData(summary = S.data?.summary) {
  const ur = summary?.usage_range;
  if (!ur || !Array.isArray(ur.starts) || !ur.starts.length) return null;
  const starts = ur.starts.map((x) => Date.parse(x));
  const step = (Number(ur.bucket_seconds) || 3600) * 1000;
  const ends = Array.isArray(ur.ends) && ur.ends.length === starts.length ? ur.ends.map((x) => Date.parse(x)) : bucketEnds(starts, step);
  const cutoff = Date.parse(summary?.history?.backfill_cutoff || "");
  return { starts, ends, step, accounts: ur.accounts || {}, cutoff: Number.isFinite(cutoff) ? cutoff : null };
}

// When per-account counting starts: the first bucket with any usage on any
// account, or the backfill's hand-over to the proxy when that is earlier.
// The reply has buckets for the whole window, empty ones included, so the
// first bucket returned says nothing. null when neither is known.
export function coverageStart(d) {
  if (!d) return null;
  let first = null;
  for (let i = 0; i < d.starts.length && first == null; i++) {
    for (const id in d.accounts) if (nonZero(d.accounts[id]?.[i])) { first = d.starts[i]; break; }
  }
  if (d.cutoff != null && (first == null || d.cutoff < first)) return d.cutoff;
  return first;
}

// Bucket indexes that overlap [from, to). Buckets are whole: a bucket that
// starts before from counts in full, as counters cannot be split.
export function bucketsIn(d, from, to) {
  const out = [];
  if (!d) return out;
  for (let i = 0; i < d.starts.length; i++) if (d.starts[i] < to && d.ends[i] > from) out.push(i);
  return out;
}

// Totals over [from, to) for the given accounts and each account's own
// totals. any: buckets overlap the stretch. first: when per-account counts
// start within it. covered: they cover all of it up to now.
export function usageSum(d, ids, from, to, now = Date.now()) {
  const sum = EMPTY(), per = {};
  for (const id of ids) per[id] = EMPTY();
  const idx = bucketsIn(d, from, to);
  for (const i of idx) for (const id of ids) { const b = d.accounts[id]?.[i]; if (b) { addC(sum, b); addC(per[id], b); } }
  if (!idx.length) return { sum, per, any: false, covered: false, first: null };
  const until = Math.min(to, now);
  const start = coverageStart(d);
  const counted = start == null ? d.starts[idx[0]] : Math.max(start, d.starts[idx[0]]);
  const covered = counted <= from && d.ends[idx[idx.length - 1]] >= until;
  return { sum, per, any: true, covered, first: counted };
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
// detail starts and inside the stretch [from, to): a day counts when it
// starts before to. days: [{date, providers: {claude: counters}}].
export function historyBefore(before, from, provs, to = Infinity) {
  const h = S.data?.summary?.history?.days || [];
  const sum = EMPTY();
  let any = false;
  const fromKey = from ? dayKey(from) : "";
  const beforeKey = dayKey(before);
  for (const d of h) {
    if (d.date >= beforeKey || (fromKey && d.date < fromKey) || midnight(d.date) >= to) continue;
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
  const withTimes = (series) => {
    const list = (series || []).filter((b) => b && b.start);
    const ends = bucketEnds(list.map((b) => Date.parse(b.start)), step);
    return list.map((b, i) => ({ ...b, t0: Date.parse(b.start), t1: ends[i] }));
  };
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

// ---------- a selection's own data ----------

// A selection's figures need data covering exactly the selection: the window
// on screen may cover only part of it after a zoom, and its buckets may be
// wider than it (two-day buckets in a yearly window). Percentiles cannot be
// added up from buckets either. So a selection loads its own reply, with
// usage and performance for just that range. RD holds the last one.
export const RD = { key: "", summary: null, scope: "", at: 0, loading: "", req: 0 };

const rangeKey = (span, scope) => (span ? span.start + "," + span.end + "|" + scope : "");
// How long a loaded selection holds: a minute while it runs up to now, five
// otherwise, as usage can be recorded late.
const FRESH_LIVE = 60e3, FRESH_PAST = 5 * 60e3;
let requests = 0;

// Loads usage and performance for a selected range, once per range and
// scope until it is stale, then redraws. The window asked for comes from
// perfSpan, the same rule as for the window on screen. Each request has its
// own number, and only the latest one's reply is kept, so an older reply for
// the same range never replaces a newer one.
export async function loadRangeData(range, scope, fetchFn = globalThis.fetch, now = Date.now()) {
  if (!range || range.start >= now) return;
  const span = perfSpan(range);
  const key = rangeKey(span, scope);
  const fresh = range.end > RD.at ? FRESH_LIVE : FRESH_PAST;
  if ((RD.key === key && now - RD.at <= fresh) || RD.loading === key) return;
  const my = ++requests;
  RD.loading = key;
  RD.req = my;
  try {
    const iso = (t) => new Date(t).toISOString().replace(/\.\d{3}Z$/, "Z");
    const q = new URLSearchParams({ range: fallbackRange(span.start, now), usage_start: iso(span.start), usage_end: iso(span.end), perf_start: iso(span.start), perf_end: iso(span.end) });
    if (scope) q.set("scope", scope);
    const res = await fetchFn("/dashboard/data?" + q, { cache: "no-store" });
    if (!res.ok) throw new Error(String(res.status));
    const data = await res.json();
    if (RD.req !== my) return;
    Object.assign(RD, { key, summary: data?.summary || null, scope, at: now, loading: "" });
    globalThis.window?.dispatchEvent(new Event("dash:render"));
  } catch (e) {
    if (RD.req === my) RD.loading = "";
  }
}

// Whether buckets with these edges add up to exactly [from, to): the first
// starts at from and the last ends at to, or holds now when to is now.
function lineUp(starts, ends, from, to, now) {
  const idx = [];
  for (let i = 0; i < starts.length; i++) if (starts[i] < to && ends[i] > from) idx.push(i);
  if (!idx.length) return false;
  const a = starts[idx[0]], b = ends[idx[idx.length - 1]];
  return Math.abs(a - from) < 60e3 && (Math.abs(b - to) < 60e3 || (to >= now - 60e3 && b >= now));
}

// Whether a reply's performance buckets cover exactly [from, to), cut at
// now: its first bucket starts at from, and its last ends at to, or holds
// now when to is later. The proxy widens a window to whole buckets, so a
// reply's totals and percentiles describe the stretch only when its own
// bucket edges say so, whatever window was asked for.
export function coversExactly(ps, from, to, now) {
  const s = ps?.series || [];
  if (!s.length) return false;
  const first = s[0], last = s[s.length - 1];
  if (Math.abs(first.t0 - from) >= 60e3) return false;
  if (to > now) return last.t0 <= now && now < last.t1;
  return Math.abs(last.t1 - to) < 60e3;
}

// What a figure, chip or table cell reads for the stretch it describes: the
// selection while there is one, else the window. One answer for every panel,
// strip and table on a page:
//   from, to   the stretch, to clamped at now
//   usage      usage buckets covering it, or null while they load
//   perfSrc    performance covering it (its scopes), or null while it loads
//   merged     the account scope perfSrc merged, for perfScope
//   current    perfSrc is for this window, not one before a zoom or pan
//   exact      perfSrc's buckets cover exactly this stretch, so its summaries hold
//   ready      false while a selection waits for its own reply
//   sel        it describes a selection, not the window
export function spanData(ctx) {
  const r = ctx.range, now = ctx.now;
  if (!r) {
    const to = Math.min(ctx.window.end, now);
    const current = !!ctx.perfCurrent;
    return { from: ctx.window.start, to, usage: ctx.usage, perfSrc: S.data?.summary?.performance || null, merged: S.dataScope, current, exact: current && coversExactly(ctx.perf, ctx.window.start, ctx.window.end, now), ready: true, sel: false };
  }
  const from = r.start, to = Math.min(r.end, now);
  const scope = ctx.sc.some ? scopeParam(ctx.sc.ids) : "";
  if (RD.summary && RD.key === rangeKey(perfSpan(r), scope)) {
    const p = RD.summary.performance || null;
    // Its summaries hold only when its buckets are the selection's; a reply
    // that covers more (an hour around a short selection, or buckets wider
    // than the selection's edges) gives counts from its buckets and no
    // percentiles.
    const exact = p?.range === "custom" && coversExactly(perfScope(ctx.sc, p, RD.scope), r.start, r.end, now);
    return { from, to, usage: usageData(RD.summary), perfSrc: p, merged: RD.scope, current: true, exact, ready: true, sel: true };
  }
  // Until it arrives, the loaded window answers where its buckets line up
  // with the selection; elsewhere the figures wait.
  const u = ctx.usage && lineUp(ctx.usage.starts, ctx.usage.ends, from, to, now) ? ctx.usage : null;
  const ps = ctx.perf;
  const p = ps && lineUp(ps.series.map((b) => b.t0), ps.series.map((b) => b.t1), from, to, now) ? S.data?.summary?.performance || null : null;
  return { from, to, usage: u, perfSrc: p, merged: S.dataScope, current: false, exact: false, ready: false, sel: true };
}

// Performance figures over a span for some accounts (an account scope, or
// one account as {some: false, prov: id, ids: [id]}): request, failure and
// failover counts, and percentiles where they hold for the span. null while
// the span's data loads.
export function perfFigures(sd, sc) {
  if (!sd.perfSrc) return null;
  const ps = perfScope(sc, sd.perfSrc, sd.merged);
  if (!ps) return { ps: null, q: null, requests: 0, failed: 0, failovers: undefined, any: false };
  if (sd.exact) {
    const q = ps.q;
    return { ps, q, requests: Number(q.requests) || 0, failed: Number(q.failed) || 0, failovers: "failovers" in q ? Number(q.failovers) || 0 : undefined, any: true };
  }
  const c = perfCounts(ps, sd.from, sd.to);
  // Without an exact reading a selection has no percentiles. The window
  // keeps those of what was loaded for it, a fixed fallback range or its own
  // window widened to whole buckets, which the panels label with the stretch
  // they cover; never those of another window still loaded before a zoom.
  const fixed = sd.perfSrc.range !== "custom";
  return { ps, q: !sd.sel && (fixed || sd.current) ? ps.q : null, requests: c.requests, failed: c.failed, failovers: c.hasFailovers ? c.failovers : undefined, any: c.any };
}

// Usage totals over a span for the given accounts, or null while they load.
export const usageFigures = (sd, ids, now = Date.now()) => (sd.usage ? usageSum(sd.usage, ids, sd.from, sd.to, now) : null);

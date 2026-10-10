import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
const mem = new Map();
globalThis.localStorage = { getItem: (k) => (mem.has(k) ? mem.get(k) : null), setItem: (k, v) => mem.set(k, String(v)), removeItem: (k) => mem.delete(k) };
globalThis.window = { dispatchEvent() {}, addEventListener() {} };
globalThis.Event = class { constructor(t) { this.type = t; } };
const { S, accountScope, setScreenWants, wantPerfWindow } = await import('../../internal/api/dashboard/core.js');
const { usageData, usageSum, coverageStart, bucketEnds, perfScope, perfCounts, perfAt, historyBefore, loadRangeData, RD, spanData, perfFigures } = await import('../../internal/api/dashboard/screens/series.js');
const { panelContext, buildPanel, dayWindow } = await import('../../internal/api/dashboard/screens/panels.js');
const { tableHtml, viewTime, view } = await import('../../internal/api/dashboard/screens/telemetry.js');
const { V } = await import('../../internal/api/dashboard/screens/views.js');
const { limitWindow, timeState } = await import('../../internal/api/dashboard/screens/timeaxis.js');

const HOUR = 3600e3, DAY = 24 * HOUR;
const iso = (t) => new Date(t).toISOString();
// Hours well in the past, so nothing here runs up to now.
const T = Math.floor(Date.now() / HOUR) * HOUR - 20 * HOUR;
const ACCOUNTS = [{ id: 'a', provider: 'claude', email: 'a@x.com' }, { id: 'b', provider: 'claude', email: 'b@x.com' }];
const counters = (o = {}) => ({ requests: 0, failed: 0, input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, api_cost: 0, ...o });
const ts = (window, range = null) => ({ key: 'test', window, range });
const reset = () => { S.ui = {}; S.dataPerf = ''; S.dataScope = ''; RD.key = ''; RD.summary = null; RD.loading = ''; RD.exact = false; };
// The loaded performance is for the window the screen shows.
const loadedFor = (win) => { setScreenWants(() => ({ perf: win })); S.dataPerf = wantPerfWindow(); };

// The text of each cell in a table row, in order, the account cell first.
function rowCells(html, cls) {
  const row = html.split('<div class="tr ').find((r) => r.startsWith(cls));
  if (!row) return null;
  return [...row.matchAll(/<div class="c[^"]*"[^>]*>(.*?)<\/div>/g)].map((m) => m[1].replace(/<[^>]+>/g, '').trim());
}

test('mixed table columns show each value under its own heading, the total row too', () => {
  reset();
  const start = T - HOUR;
  loadedFor({ start, end: T });
  S.data = { accounts: ACCOUNTS, summary: {
    usage_range: { bucket_seconds: 3600, starts: [iso(start)], accounts: {
      a: [counters({ input_tokens: 100, output_tokens: 200, cache_read_tokens: 300 })],
      b: [counters({ input_tokens: 1, output_tokens: 2, cache_read_tokens: 3 })],
    } },
    performance: { range: 'custom', bucket_seconds: 3600, scopes: {
      all: { requests: 12, failed: 0, throughput: { p50: 80 }, series: [{ start: iso(start), requests: 12 }] },
      a: { requests: 11, failed: 0, throughput: { p50: 77 }, series: [{ start: iso(start), requests: 11 }] },
      b: { requests: 1, failed: 0, throughput: { p50: 50 }, series: [{ start: iso(start), requests: 1 }] },
    } },
  } };
  const ctx = panelContext(ts({ start, end: T }), accountScope());
  const html = tableHtml({ columns: ['tokens', 'requests', 'input', 'output', 'throughput'] }, ctx);
  assert.deepEqual(rowCells(html, 'head').slice(1), ['Tokens', 'Requests', 'New input', 'Output', 'Throughput']);
  assert.deepEqual(rowCells(html, 'row').slice(1), ['600', '11', '100', '200', '77 tokens/s']);
  assert.deepEqual(rowCells(html, 'total').slice(1), ['606', '12', '101', '202', '80 tokens/s']);
});

// A four hour selection whose window was zoomed to its last hour.
function zoomed() {
  reset();
  const hour = (i, req, failed = 0) => counters({ requests: req, failed });
  S.data = { accounts: ACCOUNTS, summary: {
    usage_range: { bucket_seconds: 3600, starts: [iso(T + 3 * HOUR)], accounts: { a: [hour(0, 10)] } },
    performance: { range: 'custom', bucket_seconds: 3600, scopes: { all: { requests: 10, failed: 5, series: [{ start: iso(T + 3 * HOUR), requests: 10, failed: 5 }] } } },
  } };
  loadedFor({ start: T + 3 * HOUR, end: T + 4 * HOUR });
  return { window: { start: T + 3 * HOUR, end: T + 4 * HOUR }, range: { start: T, end: T + 4 * HOUR } };
}

test('a zoom leaves a selection\'s totals alone: they come from data for the whole selection', async () => {
  const { window, range } = zoomed();
  let ctx = panelContext(ts(window, range), accountScope());
  // Its own data has not arrived: the window covers only an hour of it, so figures wait.
  assert.equal(buildPanel('requests', {}, ctx).figure, '–');
  assert.equal(spanData(ctx).ready, false);
  // The selection's own reply, four hours of ten requests each.
  const own = { summary: {
    usage_range: { bucket_seconds: 3600, starts: [0, 1, 2, 3].map((i) => iso(T + i * HOUR)), accounts: { a: [0, 1, 2, 3].map(() => counters({ requests: 10 })) } },
    performance: { range: 'custom', bucket_seconds: 3600, scopes: { all: { requests: 40, failed: 0, series: [] } } },
  } };
  await loadRangeData(range, '', async () => ({ ok: true, json: async () => own }), ctx.now);
  ctx = panelContext(ts(window, range), accountScope());
  assert.equal(buildPanel('requests', {}, ctx).figure, '40');
  const html = tableHtml({ columns: ['requests'] }, ctx);
  assert.equal(rowCells(html, 'row')[1], '40');
});

test('every figure of a selection reads the same data: failures panel, strip numbers and table agree', async () => {
  const { window, range } = zoomed();
  // The window's bucket says 5 of 10 failed; the selection's own reading says none of 10.
  const sel = { start: T + 3 * HOUR, end: T + 4 * HOUR };
  const one = [{ start: iso(sel.start), requests: 10, failed: 0, failovers: 0 }];
  const own = { summary: { usage_range: { bucket_seconds: 3600, starts: [], accounts: {} }, performance: { range: 'custom', bucket_seconds: 3600, scopes: { all: { requests: 10, failed: 0, failovers: 0, series: one }, a: { requests: 10, failed: 0, failovers: 0, series: one } } } } };
  // A one hour selection inside a window of wider buckets.
  S.data.summary.performance.bucket_seconds = 7200;
  S.data.summary.performance.scopes.all.series = [{ start: iso(T + 2 * HOUR), requests: 100, failed: 50 }];
  await loadRangeData(sel, '', async () => ({ ok: true, json: async () => own }), Date.now());
  const ctx = panelContext(ts({ start: T, end: T + 6 * HOUR }, sel), accountScope());
  const p = buildPanel('failures', {}, ctx);
  assert.equal(p.qual, '0 of 10 requests, 0 moved to another account');
  assert.equal(perfFigures(ctx.span, ctx.sc).failed, 0);
  const html = tableHtml({ columns: ['requests', 'failures'] }, ctx);
  assert.deepEqual(rowCells(html, 'row').slice(1), ['10', '0']);
});

test('a view\'s saved accounts apply once both views and accounts are known, in either order', () => {
  const view = { id: 'v-acc', window: 'last7d', panels: [], columns: [], accounts: ['a'] };
  // Views first, then the first data.
  reset();
  V.loaded = true;
  S.data = null;
  viewTime(view);
  S.data = { accounts: ACCOUNTS, summary: {} };
  viewTime(view);
  assert.deepEqual(S.ui.accounts, ['a']);
  // Data first, then the views.
  reset();
  V.loaded = false;
  viewTime(view);
  V.loaded = true;
  viewTime(view);
  assert.deepEqual(S.ui.accounts, ['a']);
  // Once applied, a later pick on the same view is kept.
  S.ui.accounts = ['b'];
  viewTime(view);
  assert.deepEqual(S.ui.accounts, ['b']);
});

test('per-account coverage starts at the first bucket with usage, so earlier local logs show', () => {
  reset();
  const day0 = Date.parse('2026-09-01T00:00:00Z');
  const days = [0, 1, 2, 3].map((i) => day0 + i * DAY);
  S.data = { accounts: ACCOUNTS, summary: {
    timezone: 'UTC',
    usage_range: { bucket_seconds: 86400, starts: days.map(iso), accounts: { a: [counters(), counters(), counters(), counters({ requests: 5, input_tokens: 5 })] } },
    history: { days: [{ date: '2026-09-01', providers: { claude: counters({ requests: 7, input_tokens: 7 }) } }, { date: '2026-09-02', providers: { claude: counters({ requests: 7, input_tokens: 7 }) } }] },
  } };
  const d = usageData();
  assert.equal(coverageStart(d), days[3], 'empty buckets before tracking began do not count');
  assert.equal(usageSum(d, ['a'], days[0], days[3] + DAY, days[3] + DAY).first, days[3]);
  const ctx = panelContext(ts({ start: days[0], end: days[3] + DAY }), accountScope());
  const html = tableHtml({ columns: ['requests'] }, ctx);
  assert.deepEqual(rowCells(html, 'earlier'), ['Earlier, from local logs', '14']);
  // A backfill hand-over earlier than the first usage wins.
  S.data.summary.history.backfill_cutoff = iso(days[1]);
  assert.equal(coverageStart(usageData()), days[1]);
});

test('bucket ends follow local days across the UK clock changes', () => {
  reset();
  S.data = { summary: { timezone: 'Europe/London' } };
  // Spring: 29 March 2026 has 23 hours.
  const spring = ['2026-03-28T00:00:00Z', '2026-03-29T00:00:00Z', '2026-03-29T23:00:00Z'].map(Date.parse);
  assert.deepEqual(bucketEnds(spring, DAY).map(iso), ['2026-03-29T00:00:00.000Z', '2026-03-29T23:00:00.000Z', '2026-03-30T23:00:00.000Z']);
  // Autumn: 25 October 2026 has 25 hours, and the last bucket ends at the next local midnight.
  const autumn = ['2026-10-23T23:00:00Z', '2026-10-24T23:00:00Z'].map(Date.parse);
  assert.deepEqual(bucketEnds(autumn, DAY).map(iso), ['2026-10-24T23:00:00.000Z', '2026-10-26T00:00:00.000Z']);
  // Selecting 30 March counts only 30 March.
  const src = { range: 'custom', bucket_seconds: 86400, scopes: { all: { requests: 30, series: [
    { start: iso(spring[0]), requests: 5 }, { start: iso(spring[1]), requests: 10 }, { start: iso(spring[2]), requests: 20 },
  ] } } };
  const ps = perfScope({ some: false, prov: 'all', ids: [] }, src, '');
  assert.equal(perfCounts(ps, spring[2], spring[2] + DAY).requests, 20);
  // The 25 hour day's last hour still has a bucket.
  const src2 = { range: 'custom', bucket_seconds: 86400, scopes: { all: { series: autumn.map((t) => ({ start: iso(t), requests: 1 })) } } };
  const ps2 = perfScope({ some: false, prov: 'all', ids: [] }, src2, '');
  assert.ok(perfAt(ps2, Date.parse('2026-10-25T23:30:00Z')));
});

const sent = (url, k) => new URLSearchParams(url.split('?')[1]).get(k);
const noMs = (t) => iso(t).replace('.000', '');

test('a running selection asks for exactly itself, not the hour before now', async () => {
  reset();
  // 10 past the hour, with this hour selected. The proxy stops a window at
  // now, so asking for the whole hour gives exactly the ten minutes so far.
  const h0 = Math.floor(Date.now() / HOUR) * HOUR, now = h0 + 10 * 60e3;
  const range = { start: h0, end: h0 + HOUR };
  const reply = { summary: {
    usage_range: { bucket_seconds: 3600, starts: [iso(h0)], accounts: { a: [counters({ requests: 10 })] } },
    performance: { range: 'custom', bucket_seconds: 3600, scopes: { all: { requests: 10, failed: 0, ttft_ms: { p50: 900 }, series: [{ start: iso(h0), requests: 10, failed: 0 }] } } },
  } };
  S.data = { accounts: ACCOUNTS, summary: { performance: { range: 'custom', bucket_seconds: 3600, scopes: {} } } };
  let url = '';
  await loadRangeData(range, '', async (u) => { url = u; return { ok: true, json: async () => reply }; }, now);
  assert.deepEqual([sent(url, 'perf_start'), sent(url, 'perf_end')], [noMs(h0), noMs(h0 + HOUR)]);
  const ctx = panelContext(ts({ start: h0 - 6 * HOUR, end: h0 + HOUR }, range), accountScope());
  ctx.now = now;
  ctx.span = spanData(ctx);
  const f = perfFigures(ctx.span, ctx.sc);
  assert.deepEqual([f.requests, f.failed], [10, 0]);
  assert.equal(buildPanel('failures', {}, ctx).qual, '0 of 10 requests');
  assert.equal(buildPanel('ttft', {}, ctx).figure, '900ms');
});

test('a selection shorter than an hour gets the hour that contains it, and counts only', async () => {
  reset();
  const h0 = Math.floor(Date.now() / HOUR) * HOUR - 5 * HOUR;
  const range = { start: h0, end: h0 + HOUR / 2 };
  const five = 5 * 60e3;
  // The hour after its start: 12 five-minute buckets of one request, 6 in the selection.
  const series = Array.from({ length: 12 }, (_, i) => ({ start: iso(h0 + i * five), requests: 1, failed: i >= 6 ? 1 : 0 }));
  const reply = { summary: { performance: { range: 'custom', bucket_seconds: 300, scopes: { all: { requests: 12, failed: 6, ttft_ms: { p50: 5000 }, series } } } } };
  S.data = { accounts: ACCOUNTS, summary: { performance: { range: 'custom', bucket_seconds: 300, scopes: {} } } };
  let url = '';
  await loadRangeData(range, '', async (u) => { url = u; return { ok: true, json: async () => reply }; });
  assert.deepEqual([sent(url, 'perf_start'), sent(url, 'perf_end')], [noMs(h0), noMs(h0 + HOUR)], 'it contains the selection');
  const ctx = panelContext(ts({ start: h0 - 6 * HOUR, end: h0 + 2 * HOUR }, range), accountScope());
  const f = perfFigures(ctx.span, ctx.sc);
  assert.deepEqual([f.requests, f.failed, f.q], [6, 0, null], 'counts from the buckets inside it, no percentiles for the wider hour');
  assert.equal(buildPanel('ttft', {}, ctx).figure, '–');
});

test('earlier local logs stop at the end of the window', () => {
  reset();
  const sep1 = Date.parse('2026-09-01T00:00:00Z');
  S.data = { accounts: ACCOUNTS, summary: {
    timezone: 'UTC',
    usage_range: { bucket_seconds: 86400, starts: [iso(sep1)], accounts: { a: [counters()] } },
    history: { backfill_cutoff: '2026-10-01T00:00:00Z', days: [
      { date: '2026-09-01', providers: { claude: counters({ requests: 7 }) } },
      { date: '2026-09-20', providers: { claude: counters({ requests: 90 }) } },
    ] },
  } };
  const ctx = panelContext(ts({ start: sep1, end: sep1 + DAY }), accountScope());
  assert.deepEqual(rowCells(tableHtml({ columns: ['requests'] }, ctx), 'earlier'), ['Earlier, from local logs', '7']);
  assert.equal(historyBefore(Date.parse('2026-10-01T00:00:00Z'), sep1, null, sep1 + DAY).requests, 7);
});

test('activity totals for picked accounts cover the whole period or show none', () => {
  reset();
  const now = Date.parse('2026-10-28T12:00:00Z');
  const day0 = Date.parse('2026-10-01T00:00:00Z');
  const days = Array.from({ length: 28 }, (_, i) => day0 + i * DAY);
  const key = (t) => iso(t).slice(0, 10);
  const daily = days.slice(14).map((t) => ({ date: key(t), ...counters({ input_tokens: 1 }) }));
  S.data = { accounts: ACCOUNTS, summary: {
    timezone: 'UTC',
    accounts: { a: { daily }, b: { daily } },
    usage_range: { bucket_seconds: 86400, starts: days.map(iso), accounts: { a: days.map((t, i) => counters({ input_tokens: i === 2 ? 100 : i >= 14 ? 1 : 0 })) } },
    history: { today: counters({ input_tokens: 5 }), this_month: counters({ input_tokens: 500 }), lifetime: counters({ input_tokens: 5000 }) },
  } };
  S.ui = { accounts: ['a'] };
  const ctx = panelContext(ts({ start: now - 7 * DAY, end: now }), accountScope());
  ctx.now = now;
  const p = buildPanel('activity', {}, ctx);
  const stats = Object.fromEntries([...p.html.matchAll(/<div class="act-stat"><span class="muted">(.*?)<\/span><b>(.*?)<\/b>(.*?)<\/div>/g)].map((m) => [m[1], [m[2], m[3].replace(/<[^>]+>/g, '')]]));
  assert.equal(stats.Today[0], '1');
  assert.equal(stats['This month'][0], '114', 'the 100 from 3 October counts');
  assert.deepEqual(stats.Lifetime, ['–', 'Not kept per account']);
  assert.equal(p.figure, '–');
});

test('three-hour buckets follow the London clock change on 25 October', () => {
  reset();
  S.data = { summary: { timezone: 'Europe/London' } };
  const at = (s) => Date.parse(s);
  // Local 21:00 and 00:00 BST, then 03:00 GMT: the middle bucket is four hours.
  const starts = ['2026-10-24T20:00:00Z', '2026-10-24T23:00:00Z', '2026-10-25T03:00:00Z'].map(at);
  assert.deepEqual(bucketEnds(starts, 3 * HOUR).map(iso), ['2026-10-24T23:00:00.000Z', '2026-10-25T03:00:00.000Z', '2026-10-25T06:00:00.000Z']);
  // The last bucket alone ends at the next local boundary too.
  assert.deepEqual(bucketEnds([starts[1]], 3 * HOUR).map(iso), ['2026-10-25T03:00:00.000Z']);
  // Spring: midnight GMT to 03:00 BST is two hours.
  assert.deepEqual(bucketEnds([at('2026-03-29T00:00:00Z')], 3 * HOUR).map(iso), ['2026-03-29T02:00:00.000Z']);
  // Data recorded at 02:30 UTC has its bucket.
  const src = { range: 'custom', bucket_seconds: 10800, scopes: { all: { series: starts.map((t) => ({ start: iso(t), requests: 1 })) } } };
  const ps = perfScope({ some: false, prov: 'all', ids: [] }, src, '');
  assert.equal(perfAt(ps, at('2026-10-25T02:30:00Z'))?.t0, starts[1]);
});

test('a selection kept through a zoom counts available accounts at its own end', () => {
  reset();
  const now = Date.now();
  S.data = {
    accounts: [{ id: 'a', provider: 'claude', email: 'a@x.com' }],
    router: { accounts: { a: { meters: [{ name: '7d', long: true, utilization: 0.5, burn_per_hour: 0, reset_at: iso(now + 5 * DAY) }] } } },
    summary: {},
  };
  // Selected up to two days ahead, then zoomed into the next hour.
  const ctx = panelContext(ts({ start: now, end: now + HOUR }, { start: now + HOUR, end: now + 2 * DAY }), accountScope(), { forecast: true });
  assert.equal(buildPanel('available', {}, ctx).figure, '1 of 1');
});

test('clicking today opens midnight to now, not yesterday afternoon to now', () => {
  reset();
  S.data = { summary: { timezone: 'UTC' } };
  // The window a page shows after the click, as the Usage view keeps it.
  const opened = (k, now) => {
    S.ui = {};
    const opts = { defaultWindow: (t) => ({ start: t - 7 * DAY, end: t }), future: false, now };
    timeState('tv', opts).setWindow(dayWindow(k, now, false));
    const w = timeState('tv', opts).window;
    return [iso(w.start), iso(w.end)];
  };
  assert.deepEqual(opened('2026-10-10', Date.parse('2026-10-10T14:00:00Z')), ['2026-10-10T00:00:00.000Z', '2026-10-10T14:00:00.000Z']);
  // In the first hour of the day too: midnight to now, not yesterday 23:30.
  assert.deepEqual(opened('2026-10-10', Date.parse('2026-10-10T00:30:00Z')), ['2026-10-10T00:00:00.000Z', '2026-10-10T00:30:00.000Z']);
  // Later the same window has grown with now, and stops at the day's end.
  S.ui = {};
  const opts = (now) => ({ defaultWindow: (t) => ({ start: t - 7 * DAY, end: t }), future: false, now });
  timeState('tv', opts(Date.parse('2026-10-10T00:30:00Z'))).setWindow(dayWindow('2026-10-10', Date.parse('2026-10-10T00:30:00Z'), false));
  assert.equal(iso(timeState('tv', opts(Date.parse('2026-10-10T09:00:00Z'))).window.end), '2026-10-10T09:00:00.000Z');
  assert.equal(iso(timeState('tv', opts(Date.parse('2026-10-11T09:00:00Z'))).window.end), '2026-10-11T00:00:00.000Z');
  // A page with forecasts opens the whole day; an earlier day opens whole too.
  assert.equal(iso(dayWindow('2026-10-10', Date.parse('2026-10-10T14:00:00Z'), true).end), '2026-10-11T00:00:00.000Z');
  assert.deepEqual(opened('2026-10-08', Date.parse('2026-10-10T14:00:00Z')), ['2026-10-08T00:00:00.000Z', '2026-10-09T00:00:00.000Z']);
});

test('a window on the current hour asks for that hour, not the hour before now, and its figures are exact', () => {
  reset();
  // A forecast window showing this hour, which runs past now.
  const h0 = Math.floor(Date.now() / HOUR) * HOUR;
  const win = { start: h0, end: h0 + HOUR };
  setScreenWants(() => ({ perf: win }));
  assert.equal(wantPerfWindow(), `${noMs(h0)},${noMs(h0 + HOUR)}`, 'the same rule as a selection');
  S.dataPerf = wantPerfWindow();
  S.data = { accounts: ACCOUNTS, summary: { performance: { range: 'custom', bucket_seconds: 3600, scopes: { all: { requests: 10, failed: 0, ttft_ms: { p50: 900 }, series: [{ start: iso(h0), requests: 10 }] } } } } };
  const ctx = panelContext(ts(win), accountScope(), { forecast: true });
  assert.equal(ctx.perfExact, true);
  assert.equal(buildPanel('ttft', {}, ctx).figure, '900ms');
  assert.equal(buildPanel('ttft', {}, ctx).qual, 'median');
});

test('exactness comes from the reply\'s bucket edges, not from the window asked for', async () => {
  reset();
  S.data = { accounts: ACCOUNTS, summary: { timezone: 'UTC', performance: { range: 'custom', scopes: {} } } };
  // A wide past selection; the proxy answers in two-day buckets from 12 December.
  const sel = { start: Date.parse('2025-12-13T00:00:00Z'), end: Date.parse('2026-10-08T00:00:00Z') };
  const starts = [];
  for (let t = Date.parse('2025-12-12T00:00:00Z'); t < sel.end; t += 2 * DAY) starts.push(t);
  const reply = { summary: { performance: { range: 'custom', bucket_seconds: 172800, scopes: { all: { requests: 2, failed: 0, ttft_ms: { p50: 700 }, series: starts.map((t, i) => ({ start: iso(t), requests: i === 0 ? 1 : i === 1 ? 1 : 0 })) } } } } };
  await loadRangeData(sel, '', async () => ({ ok: true, json: async () => reply }));
  const ctx = panelContext(ts({ start: sel.start - DAY, end: sel.end + DAY }, sel), accountScope());
  assert.equal(ctx.span.exact, false, 'its first bucket starts a day before the selection');
  assert.equal(perfFigures(ctx.span, ctx.sc).q, null);
  assert.equal(buildPanel('ttft', {}, ctx).figure, '–');
  // The window: buckets wider than it keep their percentiles, labelled with what they cover.
  reset();
  const win = { start: T + 30 * 60e3, end: T + 3 * HOUR + 30 * 60e3 };
  loadedFor(win);
  S.data = { accounts: ACCOUNTS, summary: { timezone: 'UTC', performance: { range: 'custom', bucket_seconds: 3600, scopes: { all: { requests: 4, failed: 0, ttft_ms: { p50: 800 }, series: [0, 1, 2, 3].map((i) => ({ start: iso(T + i * HOUR), requests: 1 })) } } } } };
  const wctx = panelContext(ts(win), accountScope());
  assert.equal(wctx.perfExact, false);
  const p = buildPanel('ttft', {}, wctx);
  assert.equal(p.figure, '800ms');
  const hm = (t) => new Date(t).toISOString().slice(11, 16);
  // The shared span label: the end's date only when it is another day.
  assert.match(p.qual, new RegExp(`^median, \\d+ \\w+ ${hm(T)} to (\\d+ \\w+ )?${hm(T + 4 * HOUR)}$`));
});

test('a kept selection\'s allowance count is for its own end, wherever the window is panned', () => {
  reset();
  const now = Date.now();
  S.data = {
    accounts: [{ id: 'a', provider: 'claude', email: 'a@x.com' }],
    // 10% left, burning 2% an hour: none left by tomorrow, reset in five days.
    allowance: { a: [{ long: true, utilization: 0.9, burned: 0.48, burned_since: iso(now - DAY), last_at: iso(now), reset_at: iso(now + 5 * DAY), window_seconds: 7 * 86400, step_seconds: 600, start: iso(now - DAY), used: [] }] },
    summary: {},
  };
  const sel = { start: now + 20 * HOUR, end: now + DAY };
  const figure = (win) => buildPanel('allowance', { window: 'week' }, panelContext(ts(win, sel), accountScope(), { forecast: true })).figure;
  assert.equal(figure({ start: now - DAY, end: now + 2 * DAY }), '0 of 1');
  // Panned into the past, the same selection still says 0 of 1.
  assert.equal(figure({ start: now - 3 * DAY, end: now - 2 * DAY }), '0 of 1');
});

test('hourly bucket ends follow the London clock change, the last bucket too', () => {
  reset();
  S.data = { summary: { timezone: 'Europe/London' } };
  const end = (s) => iso(bucketEnds([Date.parse(s)], HOUR)[0]);
  // 25 October: 00:00 BST, 01:00 BST, then 01:00 GMT again, each an hour.
  assert.equal(end('2026-10-24T23:00:00Z'), '2026-10-25T00:00:00.000Z');
  assert.equal(end('2026-10-25T00:00:00Z'), '2026-10-25T01:00:00.000Z');
  assert.equal(end('2026-10-25T01:00:00Z'), '2026-10-25T02:00:00.000Z');
  // 29 March: 00:00 GMT runs to 02:00 BST, an hour.
  assert.equal(end('2026-03-29T00:00:00Z'), '2026-03-29T01:00:00.000Z');
  // Days and three-hour buckets as before.
  assert.equal(iso(bucketEnds([Date.parse('2026-10-24T23:00:00Z')], DAY)[0]), '2026-10-26T00:00:00.000Z');
  assert.equal(iso(bucketEnds([Date.parse('2026-10-24T23:00:00Z')], 3 * HOUR)[0]), '2026-10-25T03:00:00.000Z');
});

test('a saved view can be deleted on a narrow screen, from the options in its header', async () => {
  const { readFile } = await import('node:fs/promises');
  const css = await readFile(new URL('../../internal/api/dashboard/app.css', import.meta.url), 'utf8');
  // Every rule inside a narrow-screen block.
  const block = css.split('@media (max-width: 860px)').slice(1).map((s) => s.slice(0, s.indexOf('\n}\n'))).join('\n');
  assert.match(block, /\.nav \.vrow \.vmore \{ display: none !important; \}/, 'the sidebar options stay hidden there');
  assert.match(block, /\.tbar \.vopts \{ display: inline-flex; \}/, 'the header options show instead');
  reset();
  V.loaded = true;
  V.store = { views: [{ id: 'v-mine', name: 'Mine', panels: [], columns: [], window: 'last7d', accounts: null, builtin: false }], default: '' };
  S.data = { accounts: ACCOUNTS, summary: {} };
  const out = view({ params: ['v-mine'] });
  assert.match(out.html, /<button class="vopts" data-vopts[^>]*aria-label="Mine options"/);
});

test('the availability forecast follows the weekly cycle to the end of a long window', async () => {
  reset();
  const { model, available, valueAt } = await import('../../internal/api/dashboard/screens/timeline.js');
  const now = Date.now();
  const reset1 = now + DAY;
  S.data = {
    accounts: [{ id: 'a', provider: 'claude', email: 'a@x.com' }],
    // Half left, burning 2% an hour, refilled every week from tomorrow.
    router: { accounts: { a: { meters: [{ name: '7d', long: true, utilization: 0.5, burn_per_hour: 0.02, reset_at: iso(reset1) }] } } },
    summary: {},
  };
  const m = model(S.data.accounts[0], now, { start: now, end: now + 110 * DAY });
  // Day 102 is three days after a refill; 100% lasts 50 hours, so none is left.
  const day102 = now + 102 * DAY;
  assert.equal(valueAt(m, day102), 0);
  assert.equal(available(m, day102, now), false);
  // An hour after the refill on day 99 it is nearly full again.
  const after = reset1 + 14 * 7 * DAY + HOUR;
  assert.equal(Math.round(valueAt(m, after)), 98);
  assert.equal(available(m, after, now), true);
});

test('a five-hour forecast a year long for three accounts draws in under 200 ms', async () => {
  // Run in a child process so a render that blocks for seconds fails on the
  // time limit instead of holding up the whole suite.
  const { spawnSync } = await import('node:child_process');
  const dash = new URL('../../internal/api/dashboard/', import.meta.url).href;
  const code = `
    const mem = new Map();
    globalThis.localStorage = { getItem: (k) => (mem.has(k) ? mem.get(k) : null), setItem: (k, v) => mem.set(k, String(v)), removeItem: (k) => mem.delete(k) };
    globalThis.window = { dispatchEvent() {}, addEventListener() {} };
    globalThis.Event = class { constructor(t) { this.type = t; } };
    const { S, accountScope } = await import(${JSON.stringify(dash + 'core.js')});
    const { panelContext, buildPanel } = await import(${JSON.stringify(dash + 'screens/panels.js')});
    const HOUR = 3600e3, DAY = 24 * HOUR, now = Date.now(), iso = (t) => new Date(t).toISOString();
    const ids = ['a', 'b', 'c'];
    const ser = (i) => ({ long: false, utilization: 0.4, burn_per_hour: 0.05, burned: 0.2, burned_since: iso(now - 5 * HOUR), last_at: iso(now), reset_at: iso(now + (i + 1) * HOUR), window_seconds: 5 * 3600, step_seconds: 600, start: iso(now - DAY), used: [] });
    S.data = { accounts: ids.map((id) => ({ id, provider: 'claude', email: id + '@x.com' })), allowance: Object.fromEntries(ids.map((id, i) => [id, [ser(i)]])), summary: {} };
    const t0 = performance.now();
    const ctx = panelContext({ key: 'perf', window: { start: now - DAY, end: now + 365 * DAY }, range: null }, accountScope(), { forecast: true });
    const p = buildPanel('allowance', { window: '5h' }, ctx);
    buildPanel('available', {}, ctx);
    const ms = performance.now() - t0;
    const marks = [...p.html.matchAll(/<span class="(end|)" style="left:([0-9.]+)%"[^>]*>.*?<b>([^<]*)<\\/b>/g)].map((m) => [m[1], Number(m[2]), m[3]]);
    console.log(JSON.stringify({ ms, kb: p.html.length / 1024, dots: (p.html.match(/tp-dot/g) || []).length, marks }));
  `;
  // Time a warm run: the first one also compiles every module.
  let out;
  for (let i = 0; i < 2; i++) {
    const r = spawnSync(process.execPath, ['--input-type=module', '-e', code], { encoding: 'utf8', timeout: 10000 });
    assert.equal(r.status, 0, `the render finished within 10 s (${r.signal || r.stderr})`);
    out = JSON.parse(r.stdout.trim().split('\n').pop());
  }
  assert.ok(out.ms < 200, `drawn in ${Math.round(out.ms)} ms`);
  // About 1750 resets an account: reset dots and labels are thinned to what
  // the plot width can show, so the markup stays a few hundred kB.
  assert.ok(out.kb < 400, `${Math.round(out.kb)} kB of markup`);
  assert.ok(out.dots < 1000, `${out.dots} dots`);
  assert.ok(out.marks.length > 0 && out.marks.length < 60, `${out.marks.length} reset labels`);
  assertMarksApart(out.marks);
});

// Reset labels placed the way app.css places them, read from the CSS
// itself: a label starts its shift left of its reset, or with class "end"
// ends that far right of it. Widths are the 12px icon, its 4px gap and the
// text at 7px a character, the width the panel assumes outside a browser.
const CSS = readFileSync(new URL('../../internal/api/dashboard/app.css', import.meta.url), 'utf8');
function markRules() {
  const plain = /\.tp-marks > span \{[^}]*transform: translateX\(-(\d+)px\)/.exec(CSS);
  const end = /\.tp-marks > span\.end \{[^}]*transform: translateX\(calc\(-100% \+ (\d+)px\)\)/.exec(CSS);
  const gap = /\.tp-marks > span \{[^}]*gap: (\d+)px/.exec(CSS);
  assert.ok(plain && end && gap, 'app.css still places reset labels by translateX');
  return { plain: Number(plain[1]), end: Number(end[1]), gap: Number(gap[1]) };
}
function assertMarksApart(marks, W = 1096) {
  const r = markRules();
  const boxes = marks.map(([cls, pct, text]) => {
    const x = (pct / 100) * W, width = 12 + r.gap + text.length * 7;
    return cls === 'end' ? { a: x + r.end - width, b: x + r.end, text } : { a: x - r.plain, b: x - r.plain + width, text };
  });
  for (let i = 1; i < boxes.length; i++) {
    assert.ok(boxes[i].a >= boxes[i - 1].b, `"${boxes[i - 1].text}" ends at ${boxes[i - 1].b.toFixed(1)}px but "${boxes[i].text}" starts at ${boxes[i].a.toFixed(1)}px`);
  }
  for (const b of boxes) assert.ok(b.b <= W + r.end && b.a >= -r.plain - 0.01, `"${b.text}" stays on the plot`);
}

test('reset labels near the right edge never overlap, placed as the CSS places them', () => {
  reset();
  const now = Date.now();
  const ids = ['a', 'b', 'c'];
  // Three five-hour accounts resetting in half an hour, 2.5 and 4.5 hours.
  const ser = (h) => ({ long: false, utilization: 0.4, burn_per_hour: 0.05, burned: 0.2, burned_since: iso(now - 5 * HOUR), last_at: iso(now), reset_at: iso(now + h * HOUR), window_seconds: 5 * 3600, step_seconds: 600, start: iso(now - DAY), used: [] });
  S.data = { accounts: ids.map((id) => ({ id, provider: 'claude', email: id + '@x.com' })), allowance: { a: [ser(0.5)], b: [ser(2.5)], c: [ser(4.5)] }, summary: {} };
  const windows = [
    { start: now, end: now + DAY },
    { start: now - HOUR, end: now + DAY },
    { start: now - 12 * HOUR, end: now + 12 * HOUR },
    { start: now - DAY, end: now + DAY },
    { start: now - DAY, end: now + 3 * DAY },
    { start: now - DAY, end: now + 30 * DAY },
  ];
  for (const win of windows) {
    const html = buildPanel('allowance', { window: '5h' }, panelContext(ts(win), accountScope(), { forecast: true })).html;
    const marks = [...html.matchAll(/<span class="(end|)" style="left:([0-9.]+)%"[^>]*>.*?<b>([^<]*)<\/b>/g)].map((m) => [m[1], Number(m[2]), m[3]]);
    assert.ok(marks.length > 0, 'the row has reset labels');
    assertMarksApart(marks);
  }
});

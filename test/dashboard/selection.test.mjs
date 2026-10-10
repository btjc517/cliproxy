import test from 'node:test';
import assert from 'node:assert/strict';
const mem = new Map();
globalThis.localStorage = { getItem: (k) => (mem.has(k) ? mem.get(k) : null), setItem: (k, v) => mem.set(k, String(v)), removeItem: (k) => mem.delete(k) };
globalThis.window = { dispatchEvent() {}, addEventListener() {} };
globalThis.Event = class { constructor(t) { this.type = t; } };
const { S, accountScope } = await import('../../internal/api/dashboard/core.js');
const { usageData, usageSum, coverageStart, bucketEnds, perfScope, perfCounts, perfAt, loadRangeData, RD, spanData, perfFigures } = await import('../../internal/api/dashboard/screens/series.js');
const { panelContext, buildPanel } = await import('../../internal/api/dashboard/screens/panels.js');
const { tableHtml, viewTime } = await import('../../internal/api/dashboard/screens/telemetry.js');
const { V } = await import('../../internal/api/dashboard/screens/views.js');

const HOUR = 3600e3, DAY = 24 * HOUR;
const iso = (t) => new Date(t).toISOString();
// Hours well in the past, so nothing here runs up to now.
const T = Math.floor(Date.now() / HOUR) * HOUR - 20 * HOUR;
const ACCOUNTS = [{ id: 'a', provider: 'claude', email: 'a@x.com' }, { id: 'b', provider: 'claude', email: 'b@x.com' }];
const counters = (o = {}) => ({ requests: 0, failed: 0, input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, api_cost: 0, ...o });
const ts = (window, range = null) => ({ key: 'test', window, range });
const reset = () => { S.ui = {}; S.dataPerf = ''; S.dataScope = ''; RD.key = ''; RD.summary = null; RD.loading = ''; };

// The text of each cell in a table row, in order, the account cell first.
function rowCells(html, cls) {
  const row = html.split('<div class="tr ').find((r) => r.startsWith(cls));
  if (!row) return null;
  return [...row.matchAll(/<div class="c[^"]*"[^>]*>(.*?)<\/div>/g)].map((m) => m[1].replace(/<[^>]+>/g, '').trim());
}

test('mixed table columns show each value under its own heading, the total row too', () => {
  reset();
  const start = T - HOUR;
  S.dataPerf = 'set';
  S.data = { accounts: ACCOUNTS, summary: {
    usage_range: { bucket_seconds: 3600, starts: [iso(start)], accounts: {
      a: [counters({ input_tokens: 100, output_tokens: 200, cache_read_tokens: 300 })],
      b: [counters({ input_tokens: 1, output_tokens: 2, cache_read_tokens: 3 })],
    } },
    performance: { range: 'custom', bucket_seconds: 3600, scopes: {
      all: { requests: 12, failed: 0, throughput: { p50: 80 }, series: [] },
      a: { requests: 11, failed: 0, throughput: { p50: 77 }, series: [] },
      b: { requests: 1, failed: 0, throughput: { p50: 50 }, series: [] },
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
  S.dataPerf = 'set';
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
  const own = { summary: { usage_range: { bucket_seconds: 3600, starts: [], accounts: {} }, performance: { range: 'custom', bucket_seconds: 3600, scopes: { all: { requests: 10, failed: 0, failovers: 0, series: [] }, a: { requests: 10, failed: 0, failovers: 0, series: [] } } } } };
  const sel = { start: T + 3 * HOUR, end: T + 4 * HOUR };
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

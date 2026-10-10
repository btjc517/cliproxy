import test from 'node:test';
import assert from 'node:assert/strict';
const mem = new Map();
globalThis.localStorage = { getItem: (k) => (mem.has(k) ? mem.get(k) : null), setItem: (k, v) => mem.set(k, String(v)), removeItem: (k) => mem.delete(k) };
const { S, fallbackRange, loadSpan, dataQuery, perfExact, scopeParam } = await import('../../internal/api/dashboard/core.js');
const { usageData, usageSum, bucketAt, historyBefore, perfScope, perfCounts, loadRangeData, RD } = await import('../../internal/api/dashboard/screens/series.js');
const { projectionParts, historyLine, cellOutline, opt } = await import('../../internal/api/dashboard/screens/panels.js');

const HOUR = 3600e3, DAY = 24 * HOUR;
const now = Date.parse('2026-10-08T14:00:00Z');
const iso = (t) => new Date(t).toISOString();

test('a backend without custom windows gets the shortest fixed range that covers the window', () => {
  S.data = { summary: { performance: { ranges: ['24h', '7d', '30d', 'all'] } } };
  assert.equal(fallbackRange(now - 20 * HOUR, now), '24h');
  assert.equal(fallbackRange(now - 3 * DAY, now), '7d');
  assert.equal(fallbackRange(now - 7 * DAY, now), '7d');
  assert.equal(fallbackRange(now - 40 * DAY, now), 'all', '180d is not offered, so all time');
  S.data = { summary: { performance: {} } };
  assert.equal(fallbackRange(now - 3 * DAY, now), '7d', 'an old backend knows 24h and 7d');
  assert.equal(fallbackRange(now - 30 * DAY, now), '7d');
});

test('the loaded span is whole five minutes, ends by now and lasts an hour to 366 days', () => {
  const s = loadSpan({ start: now - DAY + 61e3, end: now + 3 * DAY }, now + 7e3);
  assert.equal(s.end, now + 5 * 60e3);
  assert.equal(s.start, now - DAY);
  const short = loadSpan({ start: now - 10 * 60e3, end: now }, now);
  assert.equal(short.end - short.start, HOUR);
  const future = loadSpan({ start: now + DAY, end: now + 2 * DAY }, now);
  assert.equal(future.end, now);
  assert.equal(future.end - future.start, HOUR);
  const long = loadSpan({ start: now - 900 * DAY, end: now }, now);
  assert.equal(long.end - long.start, 366 * DAY);
});

test('the data query carries the window for usage and performance', () => {
  const q = new URLSearchParams(dataQuery('7d', 'a,b', '2026-10-01T14:00:00Z,2026-10-08T14:00:00Z', '2026-10-01T14:00:00Z,2026-10-08T14:00:00Z'));
  assert.equal(q.get('range'), '7d');
  assert.equal(q.get('scope'), 'a,b');
  assert.equal(q.get('usage_start'), '2026-10-01T14:00:00Z');
  assert.equal(q.get('perf_end'), '2026-10-08T14:00:00Z');
  const bare = new URLSearchParams(dataQuery('24h', '', '', ''));
  assert.equal(bare.has('perf_start'), false);
  assert.equal(bare.has('scope'), false);
});

test('percentiles count as exact only when the backend answered with a custom window', () => {
  S.dataPerf = '2026-10-01T14:00:00Z,2026-10-08T14:00:00Z';
  S.data = { summary: { performance: { range: '7d' } } };
  assert.equal(perfExact(), false);
  S.data = { summary: { performance: { range: 'custom' } } };
  assert.equal(perfExact(), true);
  S.dataPerf = '';
  assert.equal(perfExact(), false);
});

// Usage buckets: hourly for the last 6 hours, for two accounts.
const starts = Array.from({ length: 6 }, (_, i) => now - (6 - i) * HOUR);
const b = (req, tok) => ({ requests: req, failed: 0, input_tokens: tok, output_tokens: 0, cache_read_tokens: tok, cache_write_tokens: 0, api_cost: req / 10 });
const usage = { range: 'viewport', bucket_seconds: 3600, starts: starts.map(iso), accounts: { a: starts.map(() => b(1, 10)), c: starts.map((_, i) => (i < 3 ? null : b(2, 20))) } };

test('usage sums agree with the buckets and say whether they cover the stretch', () => {
  S.data = { summary: { usage_range: usage } };
  const d = usageData();
  assert.equal(d.step, HOUR);
  assert.equal(d.ends[0], starts[0] + HOUR, 'ends follow the bucket size when the backend leaves them out');
  const all = usageSum(d, ['a', 'c'], starts[0], now, now);
  assert.equal(all.sum.requests, 6 + 6);
  assert.equal(all.per.c.requests, 6);
  assert.equal(all.covered, true);
  const early = usageSum(d, ['a'], starts[0] - 2 * HOUR, now, now);
  assert.equal(early.covered, false, 'the window starts before the buckets');
  assert.equal(early.first, starts[0]);
  const part = usageSum(d, ['a'], starts[2] + 10 * 60e3, starts[4], now);
  assert.equal(part.sum.requests, 2, 'a bucket is counted whole');
  assert.equal(bucketAt(d, starts[3] + 1), 3);
  assert.equal(bucketAt(d, now + 1), -1);
});

test('history before per-account data covers whole days, by provider', () => {
  S.data = { summary: { history: { days: [
    { date: '2026-10-01', providers: { claude: b(5, 50), codex: b(1, 1) } },
    { date: '2026-10-02', providers: { claude: b(7, 70) } },
    { date: '2026-10-08', providers: { claude: b(100, 100) } },
  ] } } };
  const before = historyBefore(Date.parse('2026-10-08T00:00:00Z'), Date.parse('2026-10-01T00:00:00Z'), null);
  assert.equal(before.requests, 13);
  assert.equal(historyBefore(Date.parse('2026-10-08T00:00:00Z'), 0, ['claude']).requests, 12);
  assert.equal(historyBefore(Date.parse('2026-10-01T00:00:00Z'), 0, null), null);
});

test('performance for picked accounts: the merged scope when it arrived, else counts only', () => {
  const series = (n) => Array.from({ length: 3 }, (_, i) => ({ start: iso(now - (3 - i) * HOUR), requests: n, failed: 1, ttft_p50_ms: 100 }));
  const src = { range: 'custom', bucket_seconds: 3600, scopes: {
    all: { requests: 9, series: series(3) }, claude: { requests: 6, series: series(2) },
    a: { requests: 3, failed: 3, series: series(1) }, b: { requests: 3, failed: 3, failovers: 1, series: series(1) },
    selection: { requests: 6, ttft_ms: { p50: 120 }, series: series(2) },
  } };
  const whole = perfScope({ some: false, prov: 'claude', ids: ['a', 'b'] }, src, '');
  assert.equal(whole.q.requests, 6);
  assert.equal(whole.custom, true);
  assert.equal(whole.series[0].t1 - whole.series[0].t0, HOUR);
  const merged = perfScope({ some: true, prov: 'claude', ids: ['b', 'a'] }, src, scopeParam(['a', 'b']));
  assert.equal(merged.q.ttft_ms.p50, 120);
  const summed = perfScope({ some: true, prov: 'claude', ids: ['a', 'b'] }, src, '');
  assert.equal(summed.q.partial, true);
  assert.equal(summed.q.ttft_ms, undefined, 'no percentile is made up');
  assert.equal(summed.q.failovers, 1);
  assert.equal(summed.series[0].requests, 2);
  const c = perfCounts(whole, now - 2 * HOUR, now);
  assert.equal(c.requests, 4);
  assert.equal(c.failed, 2);
});

test('a selection loads its own usage and performance, once per range and scope', async () => {
  const calls = [];
  const fetchFn = async (url) => { calls.push(url); return { ok: true, json: async () => ({ summary: { performance: { range: 'custom', bucket_seconds: 3600, scopes: { all: { requests: 4, ttft_ms: { p50: 90 }, series: [] } } } } }) }; };
  globalThis.window = { dispatchEvent() {} };
  globalThis.Event = class { constructor(t) { this.type = t; } };
  S.data = { summary: { performance: { range: 'custom' } } };
  const range = { start: now - 3 * HOUR, end: now - HOUR };
  await loadRangeData(range, '', fetchFn, now);
  await loadRangeData(range, '', fetchFn, now);
  assert.equal(calls.length, 1, 'once per range');
  const q = new URLSearchParams(calls[0].split('?')[1]);
  assert.equal(q.get('perf_start'), '2026-10-08T11:00:00Z');
  assert.equal(q.get('perf_end'), '2026-10-08T13:00:00Z');
  assert.equal(q.get('usage_start'), '2026-10-08T11:00:00Z');
  assert.equal(q.get('usage_end'), '2026-10-08T13:00:00Z');
  assert.equal(RD.summary.performance.scopes.all.ttft_ms.p50, 90);
  // Another scope loads again; a range wholly after now loads nothing.
  await loadRangeData(range, 'a,b', fetchFn, now);
  assert.equal(calls.length, 2);
  await loadRangeData({ start: now + HOUR, end: now + 2 * HOUR }, '', fetchFn, now);
  assert.equal(calls.length, 2);
  RD.key = ''; RD.summary = null;
});

test('allowance projections: no steps at resets, nothing along zero, faded after the second reset', () => {
  const week = 7 * DAY;
  const tr = { leftNow: 50, rate: 1, reset: now + 20 * HOUR, long: true };
  const l = { tr, period: week };
  const p = projectionParts(l, now, now + 3 * week);
  const all = [...p.strong, ...p.faded].filter(Boolean);
  for (let i = 1; i < p.strong.length; i++) {
    const a = p.strong[i - 1], c = p.strong[i];
    if (a && c) assert.notEqual(a.t, c.t, 'no vertical segment');
    if (a && c) assert.ok(!(a.v === 0 && c.v === 0), 'no line along zero');
  }
  const second = tr.reset + week;
  assert.ok(p.strong.filter(Boolean).every((x) => x.t <= second));
  assert.ok(p.faded.filter(Boolean).every((x) => x.t >= second));
  assert.ok(p.faded.some(Boolean));
  assert.deepEqual(p.resets.slice(0, 2), [tr.reset, second]);
  assert.ok(all.every((x) => x.v >= 0 && x.v <= 100));
});

test('the activity outline traces the edge of the window\'s days', () => {
  // One cell: a square 2px outside it.
  assert.equal(cellOutline(new Set(['0,0'])), 'M-2 -2 L20 -2 L20 20 L-2 20 L-2 -2 Z');
  // Two cells in a column: one rectangle, no inner edge.
  assert.equal(cellOutline(new Set(['3,1', '3,2'])), 'M64 20 L86 20 L86 42 L86 64 L64 64 L64 42 L64 20 Z');
  // Days apart: two outlines.
  assert.equal(cellOutline(new Set(['0,0', '2,0'])), 'M-2 -2 L20 -2 L20 20 L-2 20 L-2 -2 ZM42 -2 L64 -2 L64 20 L42 20 L42 -2 Z');
});

test('panel options fall back to their defaults', () => {
  assert.equal(opt('tokens', {}, 'format'), 'lines');
  assert.equal(opt('tokens', { format: 'bars' }, 'format'), 'bars');
  assert.equal(opt('tokens', { format: 'pie' }, 'format'), 'lines');
  assert.equal(opt('failures', {}, 'format'), 'bars');
  assert.equal(opt('allowance', {}, 'window'), 'week');
});

test('the Display menu keeps a row in place when it is ticked or unticked', async () => {
  const { panelList } = await import('../../internal/api/dashboard/screens/viewmenus.js');
  const view = (types) => ({ panels: types.map((type) => ({ type, options: {} })) });
  // Fresh: shown panels first, in the view's order.
  const first = panelList(view(['tokens', 'cost', 'requests', 'activity']));
  assert.deepEqual(first.slice(0, 5).map((p) => p.type + (p.on ? '+' : '')), ['tokens+', 'cost+', 'requests+', 'activity+', 'allowance']);
  const rows = first.map((p) => p.type);
  // Unticking Requests leaves it third.
  const after = panelList(view(['tokens', 'cost', 'activity']), rows);
  assert.deepEqual(after.slice(0, 4).map((p) => p.type + (p.on ? '+' : '')), ['tokens+', 'cost+', 'requests', 'activity+']);
  // A reorder in the view fills the ticked rows' places.
  const moved = panelList(view(['activity', 'cost', 'tokens']), rows);
  assert.deepEqual(moved.slice(0, 4).map((p) => p.type), ['activity', 'cost', 'requests', 'tokens']);
  assert.equal(moved.length, first.length);
});

test('allowance history: a refill starts a new stretch and time used up is left out', () => {
  const p = (t, v) => ({ t, v });
  const out = historyLine([p(0, 40), p(1, 10), p(2, 0), p(3, 0), p(4, 0), p(5, 100), p(6, 90), p(7, 95), p(8, 30)]);
  // Down to zero, a gap while used up, then the refill starts fresh.
  assert.deepEqual(out.map((x) => (x ? x.v : null)), [40, 10, 0, null, 100, 90, 95, 30]);
  // A small rise is drawn as a line.
  assert.equal(out[6].move, undefined);
  // A refill in the middle of a stretch moves instead of drawing a riser.
  assert.equal(historyLine([p(0, 20), p(1, 100)])[1].move, true);
  // Missing readings stay gaps.
  assert.deepEqual(historyLine([p(0, 50), null, p(2, 40)]).map((x) => (x ? x.v : null)), [50, null, 40]);
});

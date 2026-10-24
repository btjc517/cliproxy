import test from 'node:test';
import assert from 'node:assert/strict';
globalThis.localStorage = { getItem: () => null, setItem() {}, removeItem() {} };
const { S } = await import('../../internal/api/dashboard/core.js');
const { accountTrajectory, trajectory, resetsUntil, projectedAt, allowanceRange } = await import('../../internal/api/dashboard/screens/burn.js');
const { allowanceLines, projectionParts, buildPanel } = await import('../../internal/api/dashboard/screens/panels.js');
const HOUR = 3600e3, now = Date.parse('2026-10-25T00:00:00Z');
const iso = t => new Date(t).toISOString();
function setup(weeklyUsed = 1, weeklyReset = now + 48 * HOUR) {
  const short = { long: false, utilization: 0, reset_at: iso(now + HOUR), window_seconds: 18000,
    last_at: iso(now), burn_per_hour: .2, start: iso(now - HOUR), step_seconds: 3600, used: [0, 0] };
  const week = { ...short, long: true, utilization: weeklyUsed, reset_at: weeklyReset ? iso(weeklyReset) : '', window_seconds: 604800 };
  S.data = { accounts: [{ id: 'a', provider: 'claude', email: 'a@example.test' }], allowance: { a: [short, week] } };
  return { short, week };
}
test('a weekly block removes five-hour refills, forecasts and current usable allowance', () => {
  const { short } = setup();
  const [line] = allowanceLines(['a'], false, now);
  assert.equal(line.tr.leftNow, 0);
  assert.equal(line.tr.blockedByWeek, true);
  assert.deepEqual(resetsUntil(line.tr, now, now + 72 * HOUR, line.period), []);
  assert.deepEqual(projectionParts(line, now, now + 72 * HOUR), { strong: [], faded: [], resets: [] });
  assert.equal(projectedAt(line.tr, now, line.period, now + 2 * HOUR), 0);
  assert.equal(projectedAt(line.tr, now, line.period, now + 48 * HOUR), null);
  assert.deepEqual(allowanceRange(short, line.tr, now, now, now + 2 * HOUR), { used: 0, resets: 0 });
  assert.equal(short.utilization, 0, 'raw five-hour readings remain unchanged');
});
test('five-hour resets stay visible when weekly allowance remains', () => {
  const { short } = setup(.43);
  const tr = accountTrajectory('a', false, now);
  assert.deepEqual(tr, trajectory(short, now));
  assert.deepEqual(resetsUntil(tr, now, now + 7 * HOUR, 5 * HOUR), [now + HOUR, now + 6 * HOUR]);
});
test('weekly charts are unchanged and a passed weekly reset no longer blocks the short chart', () => {
  const { week } = setup();
  assert.deepEqual(accountTrajectory('a', true, now), trajectory(week, now));
  setup(1, now - HOUR);
  assert.equal(accountTrajectory('a', false, now).blockedByWeek, undefined);
});
test('an exhausted weekly meter without a reset does not invent a recovery', () => {
  setup(1, 0);
  const tr = accountTrajectory('a', false, now);
  assert.equal(tr.blockedByWeek, true);
  assert.equal(projectedAt(tr, now, 5 * HOUR, now + 72 * HOUR), 0);
});
test('the rendered five-hour chart explains the weekly block and has no reset marks', () => {
  setup();
  const panel = buildPanel('allowance', { window: '5h' }, { window: { start: now - HOUR, end: now + 8 * HOUR }, now,
    sc: { ids: ['a'] }, ticks: [], group: 'test', range: null });
  assert.match(panel.qual, /1 at weekly limit/);
  assert.doesNotMatch(panel.html, /Resets to 100%/);
  assert.match(panel.probe(now, null, true).card, /Weekly limit used up/);
});
test('a mixed pool suppresses only the exhausted accounts, for either provider', () => {
  const { short, week } = setup();
  S.data.accounts.push({ id: 'b', provider: 'claude', email: 'b@example.test' }, { id: 'c', provider: 'codex', email: 'c@example.test' });
  S.data.allowance.b = [short, { ...week, utilization: .43 }];
  S.data.allowance.c = [short, week];
  const [a, b, c] = allowanceLines(['a', 'b', 'c'], false, now);
  assert.deepEqual(projectionParts(a, now, now + 7 * HOUR).resets, []);
  assert.deepEqual(projectionParts(b, now, now + 7 * HOUR).resets, [now + HOUR, now + 6 * HOUR]);
  assert.deepEqual(projectionParts(c, now, now + 7 * HOUR).resets, []);
});

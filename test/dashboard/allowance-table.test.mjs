import test from 'node:test';
import assert from 'node:assert/strict';
globalThis.localStorage = { getItem: () => null, setItem() {}, removeItem() {} };
const { S, accountScope, clock } = await import('../../internal/api/dashboard/core.js');
const { panelContext } = await import('../../internal/api/dashboard/screens/panels.js');
const { tableHtml } = await import('../../internal/api/dashboard/screens/telemetry.js');
const { BUILTINS, cleanView } = await import('../../internal/api/dashboard/screens/views.js');
const HOUR = 3600e3, now = Date.now();
const iso = t => new Date(t).toISOString();
const columns = ['weekLeft', 'shortLeft', 'shortReset', 'nextReset'];
const series = (long, used, reset) => ({ long, utilization: used, reset_at: iso(reset),
  last_at: iso(now), start: iso(now - HOUR), used: [used * 1000, used * 1000], step_seconds: 3600,
  window_seconds: long ? 604800 : 18000, burn_per_hour: .01 });
function fixture() {
  const ids = ['weekly', 'resting', 'ready-low', 'unknown', 'ready-high', 'off'];
  S.ui = {};
  S.data = { accounts: ids.map(id => ({ id, provider: 'claude', email: `${id}@example.test`, disabled: id === 'off' })),
    allowance: {
      weekly: [series(true, 1, now + 2 * 24 * HOUR), series(false, 0, now + 2 * HOUR)],
      resting: [series(true, .2, now + 24 * HOUR), series(false, 1, now + 2 * HOUR)],
      'ready-low': [series(true, .65, now + 24 * HOUR), series(false, .3, now + 2 * HOUR)],
      'ready-high': [series(true, .02, now + 24 * HOUR), series(false, .1, now + 2 * HOUR)],
      off: [series(true, .01, now + 24 * HOUR), series(false, 0, now + 2 * HOUR)],
    } };
}
const render = (cols = columns) => tableHtml({ columns: cols }, panelContext({ key: 'allowance-test',
  window: { start: now - HOUR, end: now + 3 * HOUR }, range: null }, accountScope()));
const order = html => [...html.matchAll(/data-row-acct="([^"]+)"/g)].map(m => m[1]);
function row(html, id) {
  const content = html.split(`data-row-acct="${id}">`)[1].split('<div class="tr ')[0];
  return content.replace(/<[^>]*>/g, ' ');
}
test('allowance defaults include five-hour left and reset columns and they survive saving', () => {
  const v = BUILTINS.find(v => v.id === 'allowance');
  assert.ok(v.columns.includes('shortLeft'));
  assert.ok(v.columns.includes('shortReset'));
  assert.deepEqual(cleanView(v).columns, v.columns);
});
test('remaining allowance sorts first and source order cannot shuffle the table', () => {
  fixture();
  const expected = ['ready-high', 'ready-low', 'unknown', 'resting', 'weekly', 'off'];
  assert.deepEqual(order(render()), expected);
  S.data.accounts.reverse();
  assert.deepEqual(order(render()), expected);
});
test('each account shows its five-hour percentage and reset, with weekly blocks explained', () => {
  fixture();
  const html = render();
  assert.match(html, />5h left</);
  assert.match(html, />5h resets</);
  assert.match(html, />Week resets</);
  assert.match(row(html, 'ready-low'), /70%/);
  assert.ok(row(html, 'ready-low').includes(clock(now + 2 * HOUR)));
  assert.match(row(html, 'weekly'), /Weekly limit used up/);
  assert.ok(!row(html, 'weekly').includes(clock(now + 2 * HOUR)));
  assert.match(row(html, 'unknown'), /No allowance reading yet/);
});
test('five-hour readings remain visible when the weekly reading is missing', () => {
  fixture();
  S.data.allowance.unknown = [series(false, .25, now + 2 * HOUR)];
  const html = render();
  assert.match(row(html, 'unknown'), /75%/);
  assert.ok(row(html, 'unknown').includes(clock(now + 2 * HOUR)));
});
test('usage tables keep their existing row order', () => {
  fixture();
  assert.deepEqual(order(render(['tokens'])), S.data.accounts.map(a => a.id));
});

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
test('allowance defaults include the next bill and resets columns and they survive saving', () => {
  const v = BUILTINS.find(v => v.id === 'allowance');
  assert.ok(v.columns.includes('nextBill'));
  assert.ok(v.columns.includes('resets'));
  assert.deepEqual(cleanView(v).columns, v.columns);
});
test('each account shows when its plan bills next, to the minute when the time is known', () => {
  fixture();
  const bill = now + 3 * 24 * HOUR;
  const by = Object.fromEntries(S.data.accounts.map(a => [a.id, a]));
  by['ready-low'].plan = { renews_on: '2026-11-27', source: 'plan_start', next_bill_at: iso(bill) };
  by['ready-high'].plan = { renews_on: '2026-12-05', source: 'manual', next_bill_at: '' };
  by.resting.plan = { renews_on: '2026-11-27', ends_on: '2026-12-01', source: 'plan_start', next_bill_at: '' };
  const html = render([...columns, 'nextBill']);
  assert.match(html, />Next bill</);
  assert.ok(row(html, 'ready-low').includes(clock(bill)));
  assert.match(html, /Worked out from when the plan started/);
  assert.match(row(html, 'ready-high'), /5 Dec/);
  assert.match(row(html, 'resting'), /Ends .*1 Dec/);
  assert.ok(!row(html, 'resting').includes('27 Nov'));
  assert.match(row(html, 'weekly'), /–\s*$/);
  // The bill column stays while a range swaps the allowance columns.
  const ranged = tableHtml({ columns: [...columns, 'nextBill'] }, panelContext({ key: 'allowance-test',
    window: { start: now - HOUR, end: now + 3 * HOUR }, range: { start: now - HOUR, end: now + HOUR } }, accountScope()));
  assert.match(ranged, />Next bill</);
  assert.ok(row(ranged, 'ready-low').includes(clock(bill)));
  // An account with no reading keeps its message in the allowance columns only.
  by.unknown.plan = { renews_on: '2026-11-27', source: 'plan_start', next_bill_at: iso(bill) };
  const again = render([...columns, 'nextBill']);
  assert.match(row(again, 'unknown'), /No allowance reading yet/);
  assert.ok(row(again, 'unknown').includes(clock(bill)));
});
test('accounts with unused resets are tagged, and the list under the table offers them', async () => {
  fixture();
  const grant = (id, action) => ({ id, label: id, title: 'Full reset', kind: 'full', left: 1, total: 1, usable_now: true,
    clears_titles: ['week', '5-hour window'], advice: { action, reason: action === 'use' ? 'The week is used up.' : 'Plenty of week left.' } });
  S.data.router = { accounts: { weekly: { resets: [grant('g1', 'use')] }, 'ready-high': { resets: [grant('g2', 'hold')] } },
    reset_suggestions: [{ auth_id: 'weekly', grant: { id: 'g1' }, advice: { action: 'use', reason: 'The week is used up.' } }] };
  const html = render([...columns, 'resets']);
  assert.match(html, />Resets</);
  assert.match(row(html, 'weekly'), /Use reset/);
  assert.match(row(html, 'ready-high'), /1 reset/);
  assert.match(row(html, 'ready-low'), /–\s*$/);
  const { resetsSection } = await import('../../internal/api/dashboard/screens/telemetry.js');
  const section = resetsSection({ columns: [...columns, 'resets'] }, accountScope());
  assert.match(section.html, /2 resets ready to use/);
  assert.match(section.html, /1 worth using now/);
  assert.equal((section.html.match(/data-reset-use=/g) || []).length, 2);
  assert.match(section.html, /The week is used up\./);
  // Without the column the page does not list resets.
  assert.equal(resetsSection({ columns }, accountScope()).html, '');
  S.data.router = {};
  assert.match(resetsSection({ columns: ['resets'] }, accountScope()).html, /No resets to use right now/);
});

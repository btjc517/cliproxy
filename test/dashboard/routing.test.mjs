import test from 'node:test';
import assert from 'node:assert/strict';
const mem = new Map();
globalThis.localStorage = { getItem: (k) => (mem.has(k) ? mem.get(k) : null), setItem: (k, v) => mem.set(k, String(v)), removeItem: (k) => mem.delete(k) };
globalThis.window = { dispatchEvent() {}, addEventListener() {} };
globalThis.Event = class { constructor(t) { this.type = t; } };
const { S, queue } = await import('../../internal/api/dashboard/core.js');
const { view } = await import('../../internal/api/dashboard/screens/routing.js');

const DAY = 864e5;
// soon resets first, so soonest-reset puts it first. The scorer ranks late first.
const week = (days) => ({ signals: { 'Anthropic-Ratelimit-Unified-7d-Utilization': '0.4', 'Anthropic-Ratelimit-Unified-7d-Reset': String(Math.floor((Date.now() + days * DAY) / 1000)) } });
const ACCOUNTS = [
  { id: 'soon', provider: 'claude', email: 'soon@x.com', quota: week(1) },
  { id: 'late', provider: 'claude', email: 'late@x.com', quota: week(5) },
  { id: 'cool', provider: 'claude', email: 'cool@x.com', quota: week(3) },
];
const RANKINGS = [{
  provider: 'claude',
  model: 'claude-opus-5-5',
  candidates: [
    { auth_id: 'late', account: 'late@x.com', summary: '60% of week left' },
    { auth_id: 'soon', account: 'soon@x.com', problems: ['already serving 14 sessions'], summary: '' },
    { auth_id: 'cool', account: 'cool@x.com', excluded: true, summary: 'Cooling down until Thu 21:00' },
  ],
}];
const load = (mode) => {
  S.data = {
    accounts: ACCOUNTS,
    routing: { strategy: 'soonest-reset', strategies: ['soonest-reset', 'round-robin'], session_affinity: true },
    router: { mode, rankings: RANKINGS, shadow: { choices: 4, agreed: 1 }, accounts: {} },
  };
};

// The account cells of the queue table rows in order, and which one says Next.
function queueRows(html) {
  const q = html.split('Queue right now')[1].split('class="sech')[0];
  return [...q.matchAll(/<a class="tr [^"]*" href="#\/accounts\/([^"]+)"[^>]*>(.*?)<\/a>/g)]
    .map((m) => ({ id: decodeURIComponent(m[1]), rank: (/<div class="c[^"]*"[^>]*><span class="muted">(\d+)<\/span>/.exec(m[2]) || [])[1] || '', next: m[2].includes('>Next</span>'), text: m[2].replace(/<[^>]+>/g, ' ') }));
}

test('live: the queue and its Next badge follow the scorer, not soonest reset', () => {
  load('live');
  const q = queue('claude');
  assert.equal(q.scored, true);
  assert.deepEqual(q.order.map((a) => a.id), ['late', 'soon']);
  assert.equal(q.next.id, 'late');

  const { html } = view();
  const rows = queueRows(html);
  assert.deepEqual(rows.map((r) => [r.id, r.rank, r.next]), [['late', '1', true], ['soon', '2', false], ['cool', '', false]]);
  // The scorer's reason shows for a ready account it leaves out.
  assert.match(rows[2].text, /Cooling down until Thu 21:00/);
  // The order control names the scorer and offers no strategy menu.
  assert.doesNotMatch(html, /data-strategy/);
  assert.doesNotMatch(html, /Soonest reset first/);
  assert.match(html, /<b>Order<\/b>.*?<div class="v">Scorer<\/div>/s);
  assert.match(html, /Queue right now<\/b><span class="muted">Scorer ranking</);
  assert.match(html, /The queue above follows this ranking/);
});

test('shadow: the queue follows the live strategy, and both sections say which is which', () => {
  load('shadow');
  const q = queue('claude');
  assert.equal(q.scored, false);
  assert.equal(q.next.id, 'soon');

  const { html } = view();
  const rows = queueRows(html);
  assert.deepEqual(rows.filter((r) => r.next).map((r) => r.id), ['soon']);
  assert.match(html, /data-strategy>Soonest reset first/);
  assert.match(html, /Queue right now<\/b><span class="muted">Live order, soonest reset first</);
  assert.match(html, /Scorer, shadow test/);
  assert.match(html, /agreed with the live pick on <b class="strong">1 of 4<\/b>/);
});

test('off: no scorer, no queue note, the strategy picker stays', () => {
  load('off');
  const { html } = view();
  assert.equal(queue('claude').next.id, 'soon');
  assert.match(html, /data-strategy>Soonest reset first/);
  assert.match(html, /Queue right now<\/b><\/div>/);
  assert.doesNotMatch(html, /Scorer/);
});

import test, { mock } from 'node:test';
import assert from 'node:assert/strict';

// Smooth zoom and pan, end to end through the real dashboard: app.js with
// its screens, on a small fake DOM, fake timers and a fake proxy. A gesture
// must redraw only the regions of the screen it changes, draw from the data
// loaded with a margin around the window, and ask the proxy once at most,
// after it settles.

const HOUR = 3600e3, DAY = 864e5;
// Half a minute past a five-minute mark, so the few seconds the tests run
// never move a window's whole five minutes.
const NOW = Date.parse('2026-10-08T14:00:30Z');

// ---------- a fake DOM: elements parsed from html, enough for the screens ----------

const VOID = new Set(['br', 'img', 'input', 'hr', 'meta', 'link', 'wbr', 'source', 'col']);
const kebab = (k) => k.replace(/[A-Z]/g, (c) => '-' + c.toLowerCase());
const decode = (s) => s.replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&');
const TAG = /<!--[\s\S]*?-->|<(\/?)([a-zA-Z][\w:-]*)((?:[^>"']|"[^"]*"|'[^']*')*?)(\/?)>/g;
const ATTR = /([^\s=/]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+)))?/g;
let docRoot = null;
// How often html was set: on #main (a full render) and on a region (a patch).
const sets = { main: 0, region: 0 };

class El {
  constructor(tag = 'div', attrs = {}) {
    this.tagName = tag.toUpperCase();
    this.attrs = { ...attrs };
    this.children = [];
    this.parent = null;
    this.style = {};
    this.listeners = {};
    this.hidden = false;
    this.scrollTop = 0;
    this.scrollLeft = 0;
    this._html = '';
    const el = this;
    this.dataset = new Proxy({}, {
      get: (_, k) => (typeof k === 'string' ? el.attrs['data-' + kebab(k)] : undefined),
      set: (_, k, v) => { el.attrs['data-' + kebab(k)] = String(v); return true; },
      deleteProperty: (_, k) => { delete el.attrs['data-' + kebab(k)]; return true; },
      has: (_, k) => ('data-' + kebab(k)) in el.attrs,
    });
  }
  get classList() {
    const get = () => new Set((this.attrs.class || '').split(/\s+/).filter(Boolean));
    const put = (s) => { this.attrs.class = [...s].join(' '); };
    return {
      add: (...c) => { const s = get(); c.forEach((x) => s.add(x)); put(s); },
      remove: (...c) => { const s = get(); c.forEach((x) => s.delete(x)); put(s); },
      toggle: (c, on) => { const s = get(); const want = on === undefined ? !s.has(c) : !!on; if (want) s.add(c); else s.delete(c); put(s); return want; },
      contains: (c) => get().has(c),
    };
  }
  get className() { return this.attrs.class || ''; }
  set className(v) { this.attrs.class = String(v); }
  get id() { return this.attrs.id || ''; }
  get innerHTML() { return this._html; }
  set innerHTML(h) {
    if (this.attrs.id === 'main') sets.main++;
    if ('data-region' in this.attrs) sets.region++;
    for (const c of this.children) c.parent = null;
    this.children = [];
    this._html = String(h);
    parseInto(this, this._html);
  }
  set textContent(t) { this.innerHTML = ''; this._html = String(t); }
  get textContent() { return this._html; }
  appendChild(c) { if (c.parent) c.parent.removeChild(c); c.parent = this; this.children.push(c); return c; }
  append(...cs) { cs.forEach((c) => this.appendChild(c)); }
  removeChild(c) { this.children = this.children.filter((x) => x !== c); c.parent = null; }
  remove() { if (this.parent) this.parent.removeChild(this); }
  get isConnected() { let e = this; while (e.parent) e = e.parent; return e === docRoot; }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return this.attrs[k] ?? null; }
  removeAttribute(k) { delete this.attrs[k]; }
  addEventListener(t, f) { (this.listeners[t] ||= []).push(f); }
  removeEventListener(t, f) { this.listeners[t] = (this.listeners[t] || []).filter((x) => x !== f); }
  fire(type, ev = {}) { for (const f of this.listeners[type] || []) f({ type, target: this, currentTarget: this, preventDefault() {}, stopPropagation() {}, ...ev }); }
  *walk() { for (const c of this.children) { yield c; yield* c.walk(); } }
  matches(sel) { return sel.split(',').some((p) => matchOne(this, p.trim().split(/\s+/).pop())); }
  querySelectorAll(sel) { return [...this.walk()].filter((e) => e.matches(sel)); }
  querySelector(sel) { for (const e of this.walk()) if (e.matches(sel)) return e; return null; }
  closest(sel) { for (let e = this; e; e = e.parent) if (e.matches(sel)) return e; return null; }
  getBoundingClientRect() { return { left: 0, top: 0, right: 1000, bottom: 100, width: 1000, height: 100 }; }
  get clientWidth() { return 1000; }
  get clientHeight() { return 100; }
  get offsetWidth() { return 40; }
  get offsetHeight() { return 20; }
  get scrollWidth() { return 0; }
  focus() {}
  blur() {}
  setPointerCapture() {}
  releasePointerCapture() {}
  hasPointerCapture() { return false; }
  insertAdjacentHTML() {}
  scrollTo() {}
}

// One compound selector: tag, .class, [attr], [attr="v"], [attr*="v"].
function matchOne(el, sel) {
  const m = /^([a-zA-Z][\w-]*|\*)?(.*)$/.exec(sel);
  if (m[1] && m[1] !== '*' && m[1].toUpperCase() !== el.tagName) return false;
  let rest = m[2];
  while (rest) {
    let t;
    if ((t = /^\.([\w-]+)/.exec(rest))) { if (!el.classList.contains(t[1])) return false; }
    else if ((t = /^\[([\w-]+)(?:(\*?=)(?:"([^"]*)"|'([^']*)'|([^\]]*)))?\]/.exec(rest))) {
      const v = el.attrs[t[1]];
      if (v === undefined) return false;
      const want = t[3] ?? t[4] ?? t[5];
      if (t[2] === '=' && v !== want) return false;
      if (t[2] === '*=' && !v.includes(want)) return false;
    } else return false;
    rest = rest.slice(t[0].length);
  }
  return true;
}

function parseInto(parent, html) {
  const stack = [parent];
  TAG.lastIndex = 0;
  let m;
  while ((m = TAG.exec(html))) {
    if (!m[2]) continue;
    const tag = m[2].toLowerCase();
    if (m[1]) {
      for (let i = stack.length - 1; i > 0; i--) if (stack[i].tagName === tag.toUpperCase()) { stack.length = i; break; }
      continue;
    }
    const attrs = {};
    ATTR.lastIndex = 0;
    let a;
    while ((a = ATTR.exec(m[3]))) attrs[a[1]] = decode(a[2] ?? a[3] ?? a[4] ?? '');
    const el = new El(tag, attrs);
    stack[stack.length - 1].appendChild(el);
    if (!m[4] && !VOID.has(tag)) stack.push(el);
  }
}

docRoot = new El('html');
const body = docRoot.appendChild(new El('body'));
body.appendChild(new El('div', { id: 'app' }));

const mem = new Map();
globalThis.localStorage = { getItem: (k) => (mem.has(k) ? mem.get(k) : null), setItem: (k, v) => mem.set(k, String(v)), removeItem: (k) => mem.delete(k) };
globalThis.document = {
  documentElement: docRoot,
  body,
  visibilityState: 'hidden',
  hidden: true,
  activeElement: null,
  getElementById: (id) => { for (const e of docRoot.walk()) if (e.attrs.id === id) return e; return null; },
  createElement: (t) => new El(t),
  querySelector: (s) => docRoot.querySelector(s),
  querySelectorAll: (s) => docRoot.querySelectorAll(s),
  addEventListener() {},
  removeEventListener() {},
};
globalThis.window = Object.assign(new EventTarget(), { innerWidth: 1400, innerHeight: 900, scrollY: 0, scrollTo() {} });
globalThis.getComputedStyle = () => ({ fontFamily: 'sans-serif' });
globalThis.location = { hash: '#/telemetry/usage', hostname: 'localhost', replace(h) { this.hash = h; } };

// ---------- a fake proxy ----------

const iso = (t) => new Date(t).toISOString().replace(/\.\d{3}Z$/, 'Z');
// The proxy's performance step: the finest giving at most 200 buckets.
function perfHours(v, now) {
  for (const h of [1, 3, 6, 12, 24, 48, 168]) {
    if (h < 24 && v.start < now - 35 * DAY) continue;
    if (Math.ceil((v.end - v.start) / (h * HOUR)) <= 200) return h;
  }
  return 168;
}
// The proxy's padded reply: buckets the window would get alone, over the
// margin asked for (trimmed to a window each side, usage to now), and
// performance figures for the window's own buckets.
function proxyData(url) {
  const P = new URLSearchParams(url.split('?')[1]);
  const now = Date.now();
  const t = (k) => (P.get(k) ? Date.parse(P.get(k)) : NaN);
  const usage = {}, perf = { range: 'custom', ranges: ['24h', '7d'], scopes: {} };
  const counters = () => ({ requests: 3, failed: 0, input_tokens: 1000, output_tokens: 200, cache_read_tokens: 0, cache_write_tokens: 0, api_cost: 0 });
  const us = t('usage_start'), ue = t('usage_end');
  if (Number.isFinite(us)) {
    const from = Number.isFinite(t('usage_pad_start')) ? Math.max(t('usage_pad_start'), us - (ue - us)) : us;
    const to = Number.isFinite(t('usage_pad_end')) ? Math.min(t('usage_pad_end'), ue + (ue - us)) : ue;
    const step = ue - us > 7 * DAY || us < now - 14 * DAY ? DAY : HOUR;
    const starts = [], ends = [];
    for (let x = Math.floor(from / step) * step; x < Math.min(to, now); x += step) { starts.push(iso(x)); ends.push(iso(x + step)); }
    Object.assign(usage, { range: 'custom', bucket_seconds: step / 1000, starts, ends, accounts: { a: starts.map(counters) } });
  }
  const ps = t('perf_start'), pe = t('perf_end');
  if (Number.isFinite(ps)) {
    const h = perfHours({ start: ps, end: pe }, now), step = h * HOUR;
    const from = Number.isFinite(t('perf_pad_start')) ? Math.max(t('perf_pad_start'), ps - (pe - ps)) : ps;
    const to = Number.isFinite(t('perf_pad_end')) ? Math.min(t('perf_pad_end'), pe + (pe - ps)) : pe;
    const series = [];
    for (let x = Math.floor(from / step) * step; x < Math.min(to, now); x += step) series.push({ start: iso(x), requests: 3, failed: 0, ttft_p50_ms: 800, ttft_p90_ms: 1500, ttft_p99_ms: 2000, latency_p50_ms: 900, latency_p90_ms: 1600, latency_p99_ms: 2100, throughput_p50: 40 });
    const scope = () => ({ requests: 99, failed: 1, ttft_ms: { p50: 800, p90: 1500 }, latency_ms: { p50: 900, p90: 1600 }, throughput: { p50: 40 }, series });
    perf.bucket_seconds = h * 3600;
    perf.scopes = { all: scope(), claude: scope(), a: scope() };
    if (P.has('perf_pad_start')) { perf.figure_start = iso(Math.floor(ps / step) * step); perf.figure_end = iso(Math.ceil(Math.min(pe, now) / step) * step); }
  }
  return { accounts: [{ id: 'a', provider: 'claude', email: 'a@x' }], server: { host: 'test' }, summary: { history: { days: [] }, usage_range: usage, performance: perf } };
}

const proxy = { calls: [], hold: false, waiting: [] };
const reply = (body) => ({ ok: true, status: 200, json: async () => JSON.parse(JSON.stringify(body)) });
globalThis.fetch = (url, init = {}) => {
  if (String(url).startsWith('/dashboard/views')) return Promise.resolve(reply({ views: [], default: '', deleted: [], revision: 1 }));
  proxy.calls.push(String(url));
  const body = proxyData(String(url));
  if (!proxy.hold) return Promise.resolve(reply(body));
  return new Promise((resolve, reject) => {
    proxy.waiting.push(() => resolve(reply(body)));
    init.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')));
  });
};

// ---------- the page ----------

mock.timers.enable({ apis: ['setTimeout', 'setInterval', 'Date'], now: NOW });
const { S } = await import('../../internal/api/dashboard/core.js');
await import('../../internal/api/dashboard/app.js');
const { timeState } = await import('../../internal/api/dashboard/screens/timeaxis.js');

const settleMicro = async () => { for (let i = 0; i < 20; i++) await new Promise((r) => setImmediate(r)); };
// Runs timers in frame-sized steps, letting replies resolve between them.
async function run(ms) {
  for (let left = ms; left > 0; left -= 16) { mock.timers.tick(Math.min(16, left)); await settleMicro(); }
}
const plots = () => document.getElementById('main').querySelectorAll('[data-tplot="tv"]');
const wheel = (ev) => { plots()[0].fire('wheel', { deltaMode: 0, deltaX: 0, deltaY: 0, clientX: 500, ...ev }); };
const tv = () => S.ui.time.tv;

await settleMicro();
await run(400);

test('the page draws the Usage view from a padded reply', () => {
  assert.ok(S.data, 'data loaded');
  assert.ok(plots().length >= 3, 'time plots drawn');
  const q = new URLSearchParams(proxy.calls[proxy.calls.length - 1].split('?')[1]);
  for (const k of ['usage_start', 'usage_pad_start', 'perf_start', 'perf_pad_start', 'perf_pad_end']) assert.ok(q.get(k), k);
  // Half a window each side: a week, cut to whole five minutes, asks for
  // 3.5 days before it, give or take those minutes.
  const pad = Date.parse(q.get('usage_start')) - Date.parse(q.get('usage_pad_start'));
  assert.ok(pad >= 3.5 * DAY && pad <= 3.5 * DAY + 10 * 60e3, String(pad));
});

test('a pan inside the margin makes no request', async () => {
  await run(300);
  const before = proxy.calls.length, full = sets.main, el = plots()[0];
  const start = tv().win?.start ?? null;
  for (let i = 0; i < 10; i++) { wheel({ deltaX: -14 }); await run(16); }
  await run(400);
  assert.notEqual(tv().win?.start ?? null, start, 'the window moved');
  assert.equal(proxy.calls.length - before, 0);
  assert.equal(sets.main - full, 0);
  assert.equal(plots()[0], el, 'the plots are the same elements');
});

test('a burst of 30 ctrl-wheel events causes zero full-screen renders and at most one data request after settling', async () => {
  const before = proxy.calls.length, full = sets.main, patches = sets.region, el = plots()[0];
  const w0 = { ...(tv().win || {}) };
  for (let i = 0; i < 30; i++) { wheel({ ctrlKey: true, deltaY: 6 }); await run(16); }
  assert.equal(proxy.calls.length - before, 0, 'nothing asked for while the gesture runs');
  assert.ok(sets.region - patches >= 25 * 3, 'the plots moved on nearly every frame');
  await run(400);
  assert.ok(tv().win.end - tv().win.start > (w0.end - w0.start) * 1.3, 'zoomed out');
  assert.equal(sets.main - full, 0, 'no full render');
  // Past seven days usage comes in days, which the hourly reply cannot draw.
  assert.equal(proxy.calls.length - before, 1, 'one request, once settled');
  assert.equal(plots()[0], el);
});

test('revisiting a cached window makes no request', async () => {
  // Home goes back to the week the page opened on, loaded at the start and
  // replaced since by the wider zoom's reply.
  const before = proxy.calls.length;
  plots()[0].fire('keydown', { key: 'Home' });
  await run(400);
  assert.equal(tv().win, null, 'back on the default window');
  assert.equal(proxy.calls.length - before, 0);
  assert.equal(S.data.summary.performance.bucket_seconds, 3600, "the week's reply, in hours, is back on screen");
});

test('the swap keeps the visible window and selection', async () => {
  const ts = () => timeState('tv', { defaultWindow: (now) => ({ start: now - 7 * DAY, end: now }) });
  const range = { start: NOW - 3 * DAY, end: NOW - 2 * DAY };
  ts().setRange(range);
  await run(32);
  const full = sets.main, el = plots()[0];
  proxy.hold = true;
  // Far out of the margin: the reply is needed.
  for (let i = 0; i < 10; i++) { wheel({ ctrlKey: true, deltaY: 60 }); await run(16); }
  await run(400);
  assert.equal(proxy.waiting.length, 1, 'one request waits');
  const old = S.data, win = { ...tv().win };
  assert.ok(old, 'the old data stays on screen while it loads');
  proxy.hold = false;
  proxy.waiting.shift()();
  await run(64);
  assert.notEqual(S.data, old, 'the new reply is on screen');
  assert.deepEqual(tv().win, win, 'same window');
  assert.deepEqual(tv().range, range, 'same selection');
  assert.equal(sets.main - full, 0, 'swapped without a full render');
  assert.equal(plots()[0], el);
  const overlay = el.children.find((c) => c.classList.contains('trange'));
  assert.equal(overlay?.hidden, false, 'the selection is still drawn');
  ts().setRange(null);
});

test.after(() => mock.timers.reset());

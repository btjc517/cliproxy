import test from 'node:test';
import assert from 'node:assert/strict';
import {
  HOUR, DAY, MIN_SPAN, MAX_SPAN, limitWindow, zoomWindow, panWindow, timeAt, fracOf, snapTime, makeRange, resizeRange, moveRange, timeTicks, windowText, rangeText, endText,
} from '../../internal/api/dashboard/screens/timeaxis.js';
import * as axis from '../../internal/api/dashboard/screens/timeaxis.js';
import { readFileSync } from 'node:fs';
// Missing before the shared label helpers: these fail clearly rather than at import.
const missing = (name) => () => { throw new Error(`timeaxis.js has no ${name}`); };
const spanLabel = axis.spanLabel || missing('spanLabel'), dayText = axis.dayText || missing('dayText'), dateText = axis.dateText || missing('dateText');
import { S } from '../../internal/api/dashboard/core.js';

// Every time in the proxy's zone: UTC keeps the expected labels exact.
S.data = { summary: { timezone: 'UTC' } };
const now = Date.parse('2026-10-08T14:00:00Z');
const week = { start: now - 7 * DAY, end: now };
const hours = { bounds: Array.from({ length: 24 * 8 }, (_, i) => week.start + i * HOUR) };

test('zoom keeps the time under the pointer where it was', () => {
  for (const anchor of [0, 0.3, 0.5, 0.9, 1]) {
    const z = zoomWindow(week, 0.5, anchor);
    assert.equal(z.end - z.start, 3.5 * DAY);
    assert.equal(timeAt(z, anchor), timeAt(week, anchor));
  }
  // Out again by the inverse factor returns the same window.
  const back = zoomWindow(zoomWindow(week, 0.5, 0.3), 2, 0.3);
  assert.equal(back.start, week.start);
  assert.equal(back.end, week.end);
});

test('zoom stops at one hour and 366 days', () => {
  const tiny = zoomWindow(week, 1e-6, 0.5);
  assert.equal(tiny.end - tiny.start, MIN_SPAN);
  const huge = zoomWindow(week, 1e6, 0.5);
  assert.equal(huge.end - huge.start, MAX_SPAN);
  assert.equal(MIN_SPAN, HOUR);
  assert.equal(MAX_SPAN, 366 * DAY);
});

test('pan moves by a share of the span and keeps the span', () => {
  const p = panWindow(week, -0.25);
  assert.equal(p.end - p.start, 7 * DAY);
  assert.equal(p.start, week.start - 1.75 * DAY);
});

test('a window without a forecast cannot run past now, one with a forecast can', () => {
  const ahead = panWindow(week, 0.5);
  const clamped = limitWindow(ahead, { now, future: false });
  assert.equal(clamped.end, now);
  assert.equal(clamped.end - clamped.start, 7 * DAY);
  assert.deepEqual(limitWindow(ahead, { now, future: true }), ahead);
  // A span past the limits is cut back around its centre.
  const wide = limitWindow({ start: now - 800 * DAY, end: now }, { now, future: true });
  assert.equal(wide.end - wide.start, MAX_SPAN);
});

test('selection edges snap to bucket boundaries', () => {
  assert.equal(snapTime(week.start + 20 * 60e3, hours), week.start);
  assert.equal(snapTime(week.start + 40 * 60e3, hours), week.start + HOUR);
  // Past the last bound the last step repeats.
  const last = hours.bounds[hours.bounds.length - 1];
  assert.equal(snapTime(last + 2.4 * HOUR, hours), last + 2 * HOUR);
  assert.equal(snapTime(7 * HOUR + 1, { origin: 0, step: 3 * HOUR }), 6 * HOUR);
  const r = makeRange(week.start + 5.2 * HOUR, week.start + 2.1 * HOUR, hours);
  assert.deepEqual(r, { start: week.start + 2 * HOUR, end: week.start + 5 * HOUR });
  // A click-sized drag still selects one whole bucket.
  const one = makeRange(week.start + 10, week.start + 20, hours);
  assert.deepEqual(one, { start: week.start, end: week.start + HOUR });
});

test('dragging a handle resizes the range from that edge', () => {
  const r = { start: week.start + 10 * HOUR, end: week.start + 20 * HOUR };
  const a = resizeRange(r, 'end', week.start + 24.2 * HOUR, hours);
  assert.deepEqual(a, { range: { start: r.start, end: week.start + 24 * HOUR }, edge: 'end' });
  const b = resizeRange(r, 'start', week.start + 12.4 * HOUR, hours);
  assert.deepEqual(b, { range: { start: week.start + 12 * HOUR, end: r.end }, edge: 'start' });
});

test('a handle dragged past the other edge swaps the edges', () => {
  const r = { start: week.start + 10 * HOUR, end: week.start + 20 * HOUR };
  const a = resizeRange(r, 'end', week.start + 6 * HOUR, hours);
  assert.equal(a.edge, 'start');
  assert.deepEqual(a.range, { start: week.start + 6 * HOUR, end: week.start + 10 * HOUR });
  const b = resizeRange(r, 'start', week.start + 23 * HOUR, hours);
  assert.equal(b.edge, 'end');
  assert.deepEqual(b.range, { start: week.start + 20 * HOUR, end: week.start + 23 * HOUR });
  // Exactly onto the other edge never leaves an empty range.
  const c = resizeRange(r, 'end', r.start, hours);
  assert.ok(c.range.end > c.range.start);
});

test('a handle near the other edge keeps one bucket and does not flip back and forth', () => {
  // 00:00 to 02:00, dragging the start handle to the right a pointer move at a time.
  let range = { start: week.start, end: week.start + 2 * HOUR }, edge = 'start';
  const seen = [];
  for (const h of [1.6, 1.9, 2.0, 2.1, 2.4, 2.6, 2.4, 2.1, 2.0, 1.6, 2.2]) {
    const res = resizeRange(range, edge, week.start + h * HOUR, hours);
    range = res.range; edge = res.edge;
    assert.ok(range.end - range.start >= HOUR, `at ${h} the range keeps a bucket`);
    seen.push([h, (range.start - week.start) / HOUR, (range.end - week.start) / HOUR, edge]);
  }
  // Up to half a bucket past 02:00 the range stays 01:00 to 02:00 on the start handle.
  for (const [h, s, e, ed] of seen.slice(0, 5)) assert.deepEqual([s, e, ed], [1, 2, 'start'], `at ${h}`);
  // Past 02:30 it swaps once: 02:00 to 03:00 on the end handle.
  assert.deepEqual(seen[5].slice(1), [2, 3, 'end']);
  // Coming back within the hour after 02:00, and even half an hour before it, it stays swapped.
  for (const [h, s, e, ed] of seen.slice(6)) assert.deepEqual([s, e, ed], [2, 3, 'end'], `at ${h}`);
  // Only past 01:30 does it swap back.
  const back = resizeRange(range, edge, week.start + 1.4 * HOUR, hours);
  assert.deepEqual([(back.range.start - week.start) / HOUR, (back.range.end - week.start) / HOUR, back.edge], [1, 2, 'start']);
});

test('dragging inside the range moves it and keeps its length', () => {
  const r = { start: week.start + 10 * HOUR, end: week.start + 20 * HOUR };
  const m = moveRange(r, 3.4 * HOUR, hours);
  assert.deepEqual(m, { start: week.start + 13 * HOUR, end: week.start + 23 * HOUR });
});

test('a range is kept in time, so zoom and pan leave it on the same hours', () => {
  const r = { start: week.start + 2 * DAY, end: week.start + 3 * DAY };
  const z = zoomWindow(week, 0.5, 0.2);
  const p = panWindow(z, 0.1);
  for (const v of [week, z, p]) {
    // Where it draws moves with the window, the times it names do not.
    assert.equal(timeAt(v, fracOf(v, r.start)), r.start);
    assert.equal(timeAt(v, fracOf(v, r.end)), r.end);
  }
  assert.notEqual(fracOf(week, r.start), fracOf(z, r.start));
});

test('ticks fall on whole hours, midnights or Mondays and stay few', () => {
  const day1 = { start: now - DAY, end: now };
  const t1 = timeTicks(day1, 8);
  assert.ok(t1.length <= 8 && t1.length >= 4);
  for (const t of t1) assert.equal(t.t % HOUR, 0);
  const tw = timeTicks(week, 8);
  for (const t of tw) assert.equal(t.t % DAY, 0);
  assert.equal(tw.find((t) => t.t === Date.parse('2026-10-05T00:00:00Z'))?.text, '5 Oct');
  const year = timeTicks({ start: now - 300 * DAY, end: now }, 8);
  assert.ok(year.length <= 8);
  for (const t of year) assert.equal(new Date(t.t).getUTCDate(), 1);
});

test('window and range labels read as the boards show them', () => {
  assert.equal(windowText({ start: Date.parse('2026-10-05T00:00:00Z'), end: Date.parse('2026-10-12T00:00:00Z') }, now), '5 to 12 Oct');
  // A window a year long names both years, not "10 to 11 Apr".
  assert.equal(windowText({ start: Date.parse('2026-04-10T12:00:00Z'), end: Date.parse('2027-04-11T12:00:00Z') }, now), '10 Apr 2026 to 11 Apr 2027');
  assert.equal(rangeText({ start: Date.parse('2026-12-30T00:00:00Z'), end: Date.parse('2027-01-02T00:00:00Z') }), '30 Dec 2026 to 2 Jan 2027, 3 days');
  assert.equal(windowText({ start: now - 7 * DAY, end: now }, now), '1 Oct 14:00 to 8 Oct 14:00');
  assert.equal(rangeText({ start: Date.parse('2026-10-09T00:00:00Z'), end: Date.parse('2026-10-12T00:00:00Z') }), '9 to 12 Oct, 3 days');
  assert.equal(rangeText({ start: Date.parse('2026-10-08T14:00:00Z'), end: Date.parse('2026-10-08T18:00:00Z') }), '8 Oct 14:00 to 18:00, 4 hours');
  // A selection's end names the time unless it falls at midnight.
  assert.equal(endText(Date.parse('2026-10-12T00:00:00Z'), now), '12 Oct');
  assert.equal(endText(Date.parse('2026-10-09T06:00:00Z'), now), '9 Oct 06:00');
});

test('every window and range label names both years when they differ', () => {
  // 366 days ending now: a timed label, which once read "7 Oct 14:00 to 8 Oct 14:00".
  assert.equal(windowText({ start: now - 366 * DAY, end: now }, now), '7 Oct 2025 14:00 to 8 Oct 2026 14:00');
  // A short window across New Year.
  assert.equal(windowText({ start: Date.parse('2026-12-31T22:00:00Z'), end: Date.parse('2027-01-01T02:00:00Z') }, now), '31 Dec 2026 22:00 to 1 Jan 2027 02:00');
  // A selection that does not start or end at midnight.
  assert.equal(rangeText({ start: Date.parse('2025-10-08T06:00:00Z'), end: Date.parse('2026-10-08T06:00:00Z') }), '8 Oct 2025 06:00 to 8 Oct 2026 06:00, 365 days');
  // A selection's end in another year than now.
  assert.equal(endText(Date.parse('2027-03-13T06:00:00Z'), now), '13 Mar 2027 06:00');
  assert.equal(endText(Date.parse('2025-10-09T00:00:00Z'), now), '9 Oct 2025');
  // Within one year nothing changes.
  assert.equal(spanLabel(Date.parse('2026-10-09T16:00:00Z'), now, { toNow: true }), '9 Oct 16:00 to now');
  assert.equal(spanLabel(Date.parse('2025-10-09T16:00:00Z'), now, { toNow: true }), '9 Oct 2025 16:00 to now');
  assert.equal(dayText(Date.parse('2027-03-13T06:00:00Z'), now), 'Sat 13 Mar 2027');
  assert.equal(dateText(Date.parse('2026-03-13T06:00:00Z'), now), '13 Mar');
});

test('panel labels outside the window header use the same year rule', async () => {
  const src = (f) => readFileSync(new URL(`../../internal/api/dashboard/screens/${f}`, import.meta.url), 'utf8');
  // Only timeaxis.js formats a day and month; the panels and table take its helpers.
  for (const f of ['panels.js', 'telemetry.js']) {
    assert.doesNotMatch(src(f), /const dm = |dayMonth\(/, `${f} formats dates on its own`);
    assert.doesNotMatch(src(f), /`\$\{day\(t\)\}/, `${f} names a day without the year rule`);
  }
});

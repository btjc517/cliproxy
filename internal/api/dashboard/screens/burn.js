// Allowance left over the week or the last 5 hours: where it is heading, how
// fast each account is using it, and when a provider has none left. The
// allowance panel and the Telemetry table draw from here.
import { S, clock, weekdayTime, accounts, validTime, warnState, isToday, status } from "../core.js";

const HOUR = 3600e3;
const IDLE_AFTER = 90 * 60e3; // the router's burn lookback: no reading for this long is idle
const ms = (iso) => Date.parse(validTime(iso)) || 0;

// Account names for legends and tooltips; an email on both providers gets the provider added.
export function accountLabels() {
  const all = accounts();
  const count = {};
  for (const a of all) count[a.email] = (count[a.email] || 0) + 1;
  return Object.fromEntries(all.map((a) => [a.id, count[a.email] > 1 ? `${a.email} (${a.provider === "codex" ? "Codex" : "Claude"})` : a.email]));
}

// ---------- allowance data ----------

// The account's weekly (long) or 5-hour series from /dashboard/data, or null.
export function allowanceSeries(id, long) {
  return (S.data?.allowance?.[id] || []).find((s) => !!s.long === long) || null;
}

// Where a meter is heading. All shares are percent of the allowance.
export function trajectory(ser, now = Date.now()) {
  if (!ser) return null;
  const leftNow = Math.max(0, 100 - (Number(ser.utilization) || 0) * 100);
  const perHour = ser.burn_per_hour != null ? Math.max(0, ser.burn_per_hour * 100) : null;
  const since = ms(ser.burned_since) || now;
  const coveredH = Math.max(0, (now - since) / HOUR);
  const burned = Math.max(0, (Number(ser.burned) || 0) * 100);
  // A weekly meter is projected at its average over the last day, idle hours
  // included; a 5-hour meter at its recent rate, which is zero once it has
  // had no reading for a while and unknown before that.
  const idle = now - ms(ser.last_at) > IDLE_AFTER;
  let rate = null;
  if (ser.long) rate = coveredH >= 3 ? burned / coveredH : perHour;
  else rate = perHour != null ? perHour : idle ? 0 : null;
  // Exhaustion is not evidence of zero future demand. When the recent
  // lookback contains no burn, use measured drops in this meter's retained
  // history. Missing samples and refill intervals contribute neither burn
  // nor elapsed time. An entirely exhausted history cannot establish demand.
  let rateBasis = ser.long ? "Average over the latest day" : "Recent observed burn";
  if (leftNow <= 0 && rate === 0) {
    const observed = observedHistoryRate(ser, now);
    rate = observed != null && observed > 0 ? observed : null;
    rateBasis = rate == null ? "Not enough consumption history to forecast after reset" : "Average over available meter history, including idle time";
  }
  const reset = ms(ser.reset_at);
  // Nothing left is used up, not running out now.
  const usedUp = leftNow <= 0;
  let runsOut = 0, leftAtReset = null;
  if (rate != null && reset > now) {
    const hours = (reset - now) / HOUR;
    if (!usedUp && rate > 0 && leftNow / rate < hours) runsOut = now + (leftNow / rate) * HOUR;
    leftAtReset = Math.max(0, leftNow - rate * hours);
  }
  return { long: !!ser.long, idle, leftNow, usedUp, perHour, burned, since, coveredH, rate, rateBasis, reset, runsOut, leftAtReset };
}

export function observedHistoryRate(ser, now = Date.now()) {
  const step = Number(ser.step_seconds) * 1000, start = ms(ser.start);
  if (!step || !Array.isArray(ser.used)) return null;
  let consumed = 0, duration = 0;
  for (let i = 1; i < ser.used.length; i++) {
    if (start + i * step > now) break;
    const a = ser.used[i - 1], b = ser.used[i];
    if (a == null || b == null || b < a) continue;
    consumed += (b - a) / 10;
    duration += step;
  }
  return duration > 0 ? consumed / (duration / HOUR) : null;
}

// Allowance left at time t (from now on) at the current rate. It runs down
// from now, refills to 100% at each reset and runs down again at the same rate.
export function projectedAt(tr, now, period, t) {
  if (!tr || !(tr.reset > now) || t < now) return null;
  if (tr.rate == null) {
    if (t < tr.reset) return tr.leftNow === 0 ? 0 : null;
    const reset = period > 0 ? tr.reset + Math.floor((t - tr.reset) / period) * period : tr.reset;
    return t === reset ? 100 : null;
  }
  if (t < tr.reset) return Math.max(0, tr.leftNow - tr.rate * ((t - now) / HOUR));
  const from = period > 0 ? tr.reset + Math.floor((t - tr.reset) / period) * period : tr.reset;
  return Math.max(0, 100 - tr.rate * ((t - from) / HOUR));
}

// The same projection as points for a line from now to end: each stretch
// runs down to 0 at most, and a reset is a vertical step back up to 100%.
export function projectionPoints(tr, now, end, period, xOf) {
  const pts = [];
  if (!(tr.reset > now) || end <= now) return pts;
  if (tr.rate == null) {
    // Exhaustion stays at zero until the known refill. After that, show
    // each known reset point without inventing a slope between resets.
    if (tr.leftNow === 0) {
      pts.push({ x: xOf(now), v: 0 }, { x: xOf(Math.min(end, tr.reset)), v: 0 });
    }
    for (const t of resetsUntil(tr, now, end, period)) {
      pts.push({ x: xOf(t), v: 100, move: t !== tr.reset || tr.leftNow !== 0, marker: true });
    }
    return pts;
  }
  let from = now, v0 = tr.leftNow, next = tr.reset;
  for (;;) {
    const stop = Math.min(next, end);
    pts.push({ x: xOf(from), v: v0 });
    const out = tr.rate > 0 ? from + (v0 / tr.rate) * HOUR : Infinity;
    if (out < stop) pts.push({ x: xOf(out), v: 0 });
    pts.push({ x: xOf(stop), v: Math.max(0, v0 - tr.rate * ((stop - from) / HOUR)) });
    if (next > end) break;
    // The reset: back to 100%. With no known period there is no next one.
    from = next;
    v0 = 100;
    next = period > 0 ? next + period : Infinity;
    if (from >= end) { pts.push({ x: xOf(from), v: 100 }); break; }
  }
  return pts;
}

// The reset times of one account that fall between now and end.
export function resetsUntil(tr, now, end, period) {
  const out = [];
  for (let t = tr.reset; t > now && t <= end; t += period) {
    out.push(t);
    if (!(period > 0)) break;
  }
  return out;
}

// The share left in a series' reading at time t, or null without one.
export function readingAt(ser, t) {
  const step = (Number(ser.step_seconds) || 0) * 1000;
  if (!step) return null;
  const u = ser.used?.[Math.round((t - ms(ser.start)) / step)];
  return u == null ? null : 100 - u / 10;
}

// Stretches, in chart x, where a provider has nothing left on any account it
// can use: readings up to now, the projection after. Accounts that are off,
// blocked or in error cannot serve and are left out. One with no reading, or
// no rate to project, might still have some, so the provider is not dead then.
export function deadZones(providers, long, { now, start, step, n, nowX }) {
  const zones = [];
  const tOf = (x) => start + x * step;
  for (const provider of providers) {
    const pool = accounts().filter((a) => a.provider === provider && !["off", "blocked", "error"].includes(status(a).kind)).map((a) => {
      const ser = allowanceSeries(a.id, long);
      return ser && { ser, tr: trajectory(ser, now), period: (Number(ser.window_seconds) || 0) * 1000 };
    });
    if (!pool.length || pool.some((m) => !m)) continue;
    const spans = [];
    // Up to now: a step counts only when every account read 0 at both of its
    // ends (now itself from the live reading), so a refill inside it is not
    // painted over.
    const zeroAt = (x) => (x >= nowX ? pool.every((m) => m.tr.leftNow <= 0) : pool.every((m) => readingAt(m.ser, tOf(x)) === 0));
    for (let i = 0; i < Math.min(n - 1, nowX); i++) {
      const to = Math.min(i + 1, nowX);
      if (zeroAt(i) && zeroAt(to)) spans.push([i, to]);
    }
    // From now: a used-up account stays at 0 until its reset whatever its
    // rate; otherwise the projection. Both only change course where an
    // account runs out or resets, so test the middle of each stretch
    // between those points.
    const endT = tOf(n - 1);
    const leftAt = (m, t) => (m.tr.leftNow <= 0 && t < m.tr.reset ? 0 : projectedAt(m.tr, now, m.period, t));
    const cuts = new Set([Math.max(0, nowX), n - 1]);
    for (const m of pool) {
      for (const q of projectionPoints(m.tr, now, endT, m.period, (t) => (t - start) / step)) cuts.add(q.x);
      for (const t of resetsUntil(m.tr, now, endT, m.period)) cuts.add((t - start) / step);
    }
    const xs = [...cuts].filter((x) => x >= Math.max(0, nowX) && x <= n - 1).sort((a, b) => a - b);
    for (let k = 0; k < xs.length - 1; k++) {
      const mid = tOf((xs[k] + xs[k + 1]) / 2);
      if (xs[k + 1] > xs[k] && pool.every((m) => leftAt(m, mid) === 0)) spans.push([xs[k], xs[k + 1]]);
    }
    // Open: still nothing left at the chart's last point, so the stretch
    // runs on past the edge rather than ending in a refill there.
    const open = pool.every((m) => leftAt(m, endT) === 0);
    for (const [x0, x1] of spans) {
      const last = zones[zones.length - 1];
      if (last && last.provider === provider && x0 - last.x1 < 1e-6) last.x1 = Math.max(last.x1, x1);
      else zones.push({ provider, x0, x1 });
      const z = zones[zones.length - 1];
      z.open = open && z.x1 >= n - 1;
    }
  }
  return zones;
}

export const pctRate = (v) => (v == null ? "–" : v > 0 && v < 1 ? v.toFixed(1).replace(/\.0$/, "") + "%" : Math.round(v) + "%");

// The rate a projection uses: a share a day for a weekly meter, an hour for a 5-hour one.
export function rateText(tr) {
  if (!tr || tr.rate == null) return "";
  if (!tr.long && tr.perHour == null && tr.idle) return "Idle";
  return tr.long ? pctRate(tr.rate * 24) + " a day" : pctRate(tr.rate) + " an hour";
}

// "Used up", "Runs out Thu 14:00" or "Lasts to reset, about 62% left".
export function outlook(tr, { short = false } = {}) {
  if (!tr) return `<span class="muted">No reading yet</span>`;
  if (tr.usedUp) return warnState("Used up");
  if (tr.rate == null) return `<span class="muted">Too few readings yet</span>`;
  if (tr.runsOut) return warnState("Runs out " + (isToday(tr.runsOut) ? clock(tr.runsOut) : weekdayTime(tr.runsOut)));
  if (tr.leftAtReset == null) return `<span class="muted">No reset time yet</span>`;
  if (short) return `Lasts to reset`;
  return `Lasts to reset, about ${Math.round(tr.leftAtReset)}% left`;
}

// A reset is a refill, never negative consumption. Unknown history stays unknown.
export function allowanceRange(ser, tr, now, from, to) {
  if (!ser || !tr || !(to > from)) return null;
  let used = 0, resets = 0;
  const period = (Number(ser.window_seconds) || 0) * 1000;
  if (from < now) {
    const step = Number(ser.step_seconds) * 1000, start = ms(ser.start), end = Math.min(now, to);
    if (!step || from < start || end > start + (ser.used.length - 1) * step + step) return { used: null, resets: null };
    for (let t = from; t < end;) {
      const next = Math.min(end, start + (Math.floor((t - start) / step) + 1) * step);
      const a = readingAt(ser, t), b = next >= now ? tr.leftNow : readingAt(ser, next);
      if (a == null || b == null) return { used: null, resets: null };
      if (b > a) resets++; else used += a - b;
      t = next;
    }
  }
  if (to > now) {
    if (tr.reset <= now) return { used: null, resets: null };
    const begin = Math.max(now, from);
    resets += resetsUntil(tr, now, to, period).filter((t) => t > begin).length;
    if (tr.rate == null) return { used: null, resets };
    const points = projectionPoints(tr, now, to, period, (t) => t);
    for (let i = 1; i < points.length; i++) {
      const a = points[i - 1], b = points[i];
      if (a.x === b.x) continue;
      const duration = Math.max(0, Math.min(to, b.x) - Math.max(begin, a.x));
      used += Math.max(0, a.v - b.v) * duration / (b.x - a.x);
    }
  }
  return { used, resets };
}

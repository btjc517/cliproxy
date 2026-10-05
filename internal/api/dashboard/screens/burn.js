// Allowance left over the week or the last 5 hours with where it is heading,
// and how fast each account is using it.
import {
  S, esc, clock, day, weekdayTime, resetShort, logo, email, accounts, validTime, limits, left,
  table, timeChart, bindChart, tipRows, warnState, meterCell, dayKey, isToday, status,
} from "../core.js";
import { ACCOUNT_COLORS } from "./common.js";

const HOUR = 3600e3;
const IDLE_AFTER = 90 * 60e3; // the router's burn lookback: no reading for this long is idle
const ms = (iso) => Date.parse(validTime(iso)) || 0;

// Account names for legends and tooltips; an email on both providers gets the provider added.
function accountLabels() {
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
  const reset = ms(ser.reset_at);
  let runsOut = 0, leftAtReset = null;
  if (rate != null && reset > now) {
    const hours = (reset - now) / HOUR;
    if (rate > 0 && leftNow / rate < hours) runsOut = now + (leftNow / rate) * HOUR;
    leftAtReset = Math.max(0, leftNow - rate * hours);
  }
  return { long: !!ser.long, idle, leftNow, perHour, burned, since, coveredH, rate, reset, runsOut, leftAtReset };
}

export const pctRate = (v) => (v == null ? "–" : v > 0 && v < 1 ? v.toFixed(1).replace(/\.0$/, "") + "%" : Math.round(v) + "%");

// The rate a projection uses: a share a day for a weekly meter, an hour for a 5-hour one.
export function rateText(tr) {
  if (!tr || tr.rate == null) return "";
  if (!tr.long && tr.perHour == null && tr.idle) return "Idle";
  return tr.long ? pctRate(tr.rate * 24) + " a day" : pctRate(tr.rate) + " an hour";
}

// "Runs out Thu 14:00" or "Lasts to reset, about 62% left".
export function outlook(tr, { short = false } = {}) {
  if (!tr) return `<span class="muted">No reading yet</span>`;
  if (tr.rate == null) return `<span class="muted">Too few readings yet</span>`;
  if (tr.runsOut) return warnState("Runs out " + (isToday(tr.runsOut) ? clock(tr.runsOut) : weekdayTime(tr.runsOut)));
  if (tr.leftAtReset == null) return `<span class="muted">No reset time yet</span>`;
  if (short) return `Lasts to reset`;
  return `Lasts to reset, about ${Math.round(tr.leftAtReset)}% left`;
}

// Allowance left for the given accounts: history solid, the trajectory dashed to each reset.
// colorOf(id, idx): the colour for an account, when the screen keys it elsewhere too.
export function allowanceChart({ key, ids, long, colorOf = null }) {
  const nm = accountLabels();
  const now = Date.now();
  const list = ids.map((id, idx) => ({ id, ser: allowanceSeries(id, long), color: colorOf ? colorOf(id, idx) : ACCOUNT_COLORS[idx % ACCOUNT_COLORS.length] })).filter((x) => x.ser);
  // A single line gets the main chart colour, not the pale one its place in the list would give it.
  if (list.length === 1 && !colorOf) list[0].color = "var(--chart-1)";
  if (!list.length) {
    return {
      html: `<div class="empty">No allowance readings yet. The proxy reads them from each reply, so they appear once ${ids.length === 1 ? "this account is" : "an account is"} used.</div>`,
      legend: [], mount() {},
    };
  }
  const step = list[0].ser.step_seconds * 1000;
  const gridStart = ms(list[0].ser.start);
  // Labels mark where the proxy's local day (week view) or 4-hour block
  // (5-hour view) changes between grid points, so odd time zones still get them.
  const block = (t) => (long ? dayKey(t) : dayKey(t) + Math.floor(Number(clock(t).slice(0, 2)) / 4));
  // Start at the block of the first reading while history is shorter than the grid.
  const firstAt = Math.min(...list.map((x) => ms(x.ser.first_at) || gridStart));
  let offset = 0;
  if (firstAt > gridStart) {
    // The first grid point inside the first reading's block.
    const b = block(firstAt);
    offset = Math.floor((firstAt - gridStart) / step);
    if (block(gridStart + offset * step) !== b) offset++;
    while (offset > 0 && block(gridStart + (offset - 1) * step) === b) offset--;
  }
  const start = gridStart + offset * step;
  const nowX = (now - start) / step;
  let end = now + (long ? 24 * HOUR : 5 * HOUR);
  const trs = list.map((x) => trajectory(x.ser, now));
  for (const tr of trs) if (tr.reset > end) end = tr.reset;
  end = Math.min(end, now + (long ? 7 * 24 * HOUR : 5 * HOUR));
  const n = Math.floor((end - start) / step) + 1;
  const xOf = (t) => (t - start) / step;

  const lines = list.map((x, k) => {
    const tr = trs[k];
    const values = new Array(n).fill(null);
    x.ser.used.forEach((u, i) => { if (u != null && i >= offset && i - offset < n) values[i - offset] = 100 - u / 10; });
    let proj = [];
    if (tr.rate != null && tr.reset > now) {
      const stop = Math.min(tr.reset, end);
      proj.push({ x: nowX, v: tr.leftNow });
      if (tr.runsOut && tr.runsOut < stop) proj.push({ x: xOf(tr.runsOut), v: 0 });
      proj.push({ x: xOf(stop), v: tr.runsOut && tr.runsOut < stop ? 0 : Math.max(0, tr.leftNow - tr.rate * ((stop - now) / HOUR)) });
    }
    return { id: x.id, color: x.color, label: nm[x.id] || x.id, values, proj, tr, ser: x.ser, nowV: tr.leftNow };
  });

  const labels = [];
  for (let i = 0; i < n; i++) {
    const t = start + i * step;
    if (block(t) === block(t - step)) continue;
    labels.push({ i, text: long ? day(t).split(" ").slice(0, 2).join(" ") : clock(t).slice(0, 2) + ":00" });
  }
  const near = (i) => Math.abs(i - nowX) < (n - 1) * 0.06;
  const xl = labels.filter((l) => !near(l.i));
  xl.push({ i: nowX, text: "Now" });

  const height = 160;
  const cols = Array.from({ length: n }, (_, i) => ({ values: Object.fromEntries(lines.map((l) => [l.id, l.values[i]])) }));
  const series = lines.map((l) => ({ key: l.id, color: l.color, proj: l.proj, nowV: l.nowV }));
  const html = timeChart({ id: key, format: "line", series, cols, height, max: 100, nowX, labels: xl, yfmt: (v) => Math.round(v) + "%" });
  const tip = (i) => {
    const t = start + i * step;
    const future = t > now;
    const rows = [];
    for (const l of lines) {
      let v = null;
      if (!future) v = l.values[i];
      else if (l.tr.rate != null && t <= l.tr.reset) v = l.tr.runsOut && t >= l.tr.runsOut ? 0 : Math.max(0, l.tr.leftNow - l.tr.rate * ((t - now) / HOUR));
      if (v == null) continue;
      rows.push({ k: l.label, v: Math.round(v) + "% left", color: l.color });
    }
    if (!rows.length) return "";
    const head = future ? `${isToday(t) ? "Today" : day(t)} ${clock(t)}, at this rate` : `${isToday(t) ? "Today" : day(t)} ${clock(t)}`;
    return `<div class="h"><span>${esc(head)}</span></div>${tipRows(rows)}`;
  };
  return {
    html,
    legend: lines.map((l) => ({ id: l.id, label: l.label, color: l.color })),
    mount(root) { bindChart(root, key, tip); },
  };
}

// ---------- the allowance table on Usage ----------

// One row per account: colour key, allowance left, burn, where it is heading, reset.
// colorOf(id): the account's colour on this screen, for the swatch and the meter.
export function allowanceTable(ids, long, colorOf) {
  const all = accounts();
  const rows = ids.map((id) => {
    const a = all.find((x) => x.id === id);
    const tr = trajectory(allowanceSeries(id, long));
    const item = long ? limits(a || { id }).week : limits(a || { id }).short;
    const color = colorOf(id);
    const lead = `<span class="lead"><i class="sq" style="background:${color || "transparent"}"></i>${logo(a?.provider)}</span>`;
    const href = "#/accounts/" + encodeURIComponent(id);
    const leftNow = tr ? tr.leftNow : item ? left(item) : null;
    const reset = tr?.reset || item?.reset || 0;
    if (!tr) {
      // No trend: the account's state says more than "no reading" when it has one.
      const st = a ? status(a) : { kind: "" };
      const dash = `<span class="muted">–</span>`;
      if (st.kind === "blocked" || st.kind === "error") return { href, cells: [lead, email(a?.email || id), dash, dash, warnState(st.text), ""] };
      if (st.kind === "off") return { href, cells: [lead, email(a?.email || id), dash, dash, `<span class="muted">Off</span>`, ""] };
      if (st.kind === "usedup") {
        const until = st.until || reset;
        return { href, cells: [lead, email(a?.email || id), meterCell(0, color), dash, warnState("Used up"), until ? esc(resetShort(until)) : ""] };
      }
      return { href, cells: [lead, email(a?.email || id), leftNow == null ? `<span class="muted">No reading</span>` : meterCell(leftNow, color), dash, `<span class="muted">${leftNow == null ? "No reading yet" : "Too few readings yet"}</span>`, reset ? esc(resetShort(reset)) : ""] };
    }
    // Per day is the last 24 hours, or the readings so far scaled to a day; a 5-hour limit shows its rate an hour.
    const perDay = tr.coveredH >= 1 ? (tr.burned / tr.coveredH) * 24 : null;
    const rateCell = long
      ? perDay == null ? `<span class="muted">–</span>` : tr.coveredH >= 20 ? pctRate(perDay) : `${pctRate(perDay)}<span class="muted">from ${Math.round(tr.coveredH)}h</span>`
      : tr.perHour == null ? `<span class="muted">${tr.idle ? "Idle" : "–"}</span>` : pctRate(tr.perHour);
    const heading = tr.rate == null ? `<span class="muted">Too few readings yet</span>`
      : tr.runsOut ? warnState("Runs out " + (isToday(tr.runsOut) ? clock(tr.runsOut) : weekdayTime(tr.runsOut)))
      : tr.leftAtReset == null ? `<span class="muted">No reset time yet</span>` : `${Math.round(tr.leftAtReset)}% left at reset`;
    return { href, cells: [lead, email(a?.email || id), meterCell(tr.leftNow, color), rateCell, heading, tr.reset ? esc(resetShort(tr.reset)) : ""] };
  });
  const cols = [
    { label: "", w: 32, cls: "lead" }, { label: "Account" }, { label: long ? "Week left" : "5 hours left", w: 120 },
    { label: long ? "Per day" : "Per hour", w: 136, r: true }, { label: "Heading for", w: 240 }, { label: "Resets", w: 104, r: true },
  ];
  return table(cols, rows, { empty: "No accounts." });
}

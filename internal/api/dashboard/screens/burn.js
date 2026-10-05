// Usage over time and burn rate: allowance left over the week or the last
// day with where it is heading, tokens and requests by account, and how fast
// each account is using them.
import {
  S, esc, fmt, int, clock, day, weekdayTime, resetShort, logo, email, accounts, tokens, hourly, validTime,
  table, barChart, bindChart, tipRows, warnState, meterCell, menu, seg, icon, dayKey, isToday,
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

// ---------- line chart ----------

// Percent-left lines on a fixed grid. lines: [{color, label, values: [v|null], proj: [{x, v}]}]
// where x is a fractional grid index; nowX marks now.
function lineChart({ id, n, lines, nowX, height = 160, labels }) {
  const W = 1000;
  const X = (i) => (n > 1 ? (i / (n - 1)) * W : 0).toFixed(1);
  const Y = (v) => (height - (Math.max(0, Math.min(100, v)) / 100) * height).toFixed(1);
  const paths = lines.map((l) => {
    let d = "", pen = false;
    l.values.forEach((v, i) => {
      if (v == null) { pen = false; return; }
      d += `${pen ? "L" : "M"}${X(i)} ${Y(v)} `;
      pen = true;
    });
    if (d && l.nowV != null) d += `L${X(nowX)} ${Y(l.nowV)}`;
    const p = l.proj && l.proj.length > 1 ? l.proj.map((q, k) => `${k ? "L" : "M"}${X(q.x)} ${Y(q.v)}`).join(" ") : "";
    return `${d ? `<path d="${d}" stroke="${l.color}" />` : ""}${p ? `<path d="${p}" stroke="${l.color}" stroke-dasharray="4 4" class="proj" />` : ""}`;
  }).join("");
  const xl = labels.map((l) => {
    const pos = n > 1 ? l.i / (n - 1) : 0;
    const cls = pos <= 0.02 ? "first" : pos >= 0.98 ? "last" : "";
    return `<span class="${cls}" style="left:${(pos * 100).toFixed(2)}%">${esc(l.text)}</span>`;
  }).join("");
  const nowPos = n > 1 ? (nowX / (n - 1)) * 100 : 0;
  return `<div class="chart linechart" data-line="${esc(id)}">
    <div class="yax" style="height:${height}px"><span>100%</span><span>50%</span><span>0</span></div>
    <div class="plotwrap">
      <div class="plot" style="height:${height}px">
        <div class="grid" style="top:0"></div><div class="grid" style="top:${height / 2}px"></div>
        <div class="nowline" style="left:${nowPos.toFixed(2)}%"></div>
        ${lines.filter((l) => l.nowV != null).map((l) => `<i class="nowdot" style="left:${nowPos.toFixed(2)}%;top:${(height - (Math.max(0, Math.min(100, l.nowV)) / 100) * height).toFixed(1)}px;background:${l.color}"></i>`).join("")}
        <svg viewBox="0 0 ${W} ${height}" preserveAspectRatio="none" width="100%" height="${height}">${paths}</svg>
      </div>
      <div class="xax">${xl}</div>
    </div>
  </div>`;
}

// Hover for lineChart: a guide at the nearest grid point and a tooltip.
function bindLine(root, id, n, tipFor) {
  const chart = root.querySelector(`[data-line="${id}"]`);
  if (!chart) return;
  const plot = chart.querySelector(".plot");
  let tip = null, guide = null, held = false;
  const clear = () => {
    if (tip) tip.remove();
    if (guide) guide.remove();
    tip = guide = null;
    if (held) S.hold = Math.max(0, S.hold - 1);
    held = false;
  };
  plot.addEventListener("mousemove", (e) => {
    const pr = plot.getBoundingClientRect();
    const i = Math.max(0, Math.min(n - 1, Math.round(((e.clientX - pr.left) / pr.width) * (n - 1))));
    const html = tipFor(i);
    if (!html) return clear();
    if (!held) { S.hold++; held = true; }
    if (!guide) { guide = document.createElement("div"); guide.className = "guide"; plot.appendChild(guide); }
    const gx = n > 1 ? (i / (n - 1)) * pr.width : 0;
    guide.style.left = gx + "px";
    if (!tip) { tip = document.createElement("div"); tip.className = "tip"; plot.appendChild(tip); }
    tip.innerHTML = html;
    const w = 280;
    let x = gx - w - 12;
    if (x < 0) x = gx + 12;
    if (x + w > pr.width) x = Math.max(0, pr.width - w);
    tip.style.left = x + "px";
    tip.style.top = "-14px";
  });
  plot.addEventListener("mouseleave", clear);
}

// Allowance left for the given accounts: history solid, the trajectory dashed to each reset.
export function allowanceChart({ key, ids, long }) {
  const nm = accountLabels();
  const now = Date.now();
  const colorOf = (idx) => (ids.length === 1 ? "var(--chart-1)" : ACCOUNT_COLORS[idx % ACCOUNT_COLORS.length]);
  const list = ids.map((id, idx) => ({ id, ser: allowanceSeries(id, long), color: colorOf(idx) })).filter((x) => x.ser);
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
  const html = lineChart({ id: key, n, lines, nowX, height, labels: xl });
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
    legend: lines.map((l) => ({ label: l.label, color: l.color })),
    mount(root) { bindLine(root, key, n, tip); },
  };
}

// ---------- tokens and requests over time ----------

// Stacked by account (several ids) or one account's bars, hourly for 48 hours or daily for 14 days.
export function volumeChart({ key, ids, metric, grain }) {
  const nm = accountLabels();
  const acc = S.data?.summary?.accounts || {};
  const val = (b) => (!b ? 0 : metric === "tokens" ? tokens(b) : Number(b.requests) || 0);
  const colorOf = (idx) => ACCOUNT_COLORS[idx % ACCOUNT_COLORS.length];
  const series = ids.map((id, idx) => ({ key: id, color: ids.length === 1 ? "var(--chart-1)" : colorOf(idx), label: nm[id] || id }));
  let cols, labels, title;
  if (grain === "hourly") {
    const { per, starts } = hourly(ids, 48);
    cols = starts.map((st, i) => ({ start: st, by: Object.fromEntries(ids.map((id) => [id, per[id]?.[i]])) }));
    labels = [];
    cols.forEach((c, i) => { if (i < cols.length - 2 && clock(c.start) === "00:00") labels.push({ i, text: day(c.start).split(" ")[0] }); else if (i < cols.length - 2 && clock(c.start) === "12:00") labels.push({ i, text: "12:00" }); });
    labels.push({ i: cols.length - 1, text: "Now" });
    title = (c) => `${isToday(c.start) ? "Today" : day(c.start)} ${clock(c.start)} to ${clock(Date.parse(c.start) + HOUR)}`;
  } else {
    const len = Math.max(0, ...ids.map((id) => (acc[id]?.daily || []).length));
    cols = [];
    for (let i = 0; i < len; i++) {
      const by = {};
      let date = "";
      for (const id of ids) {
        const d = acc[id]?.daily || [];
        const b = d[d.length - len + i];
        by[id] = b;
        if (b?.date) date = b.date;
      }
      cols.push({ date, by });
    }
    labels = [];
    cols.forEach((c, i) => { if (i % 2 === 0 && i < cols.length - 1 && c.date) labels.push({ i, text: dayLabel(c.date) }); });
    if (cols.length) labels.push({ i: cols.length - 1, text: "Today" });
    title = (c) => (c.date === dayKey(Date.now()) ? "Today" : dayLabel(c.date, true));
  }
  if (!cols.length || !cols.some((c) => ids.some((id) => val(c.by[id])))) {
    return { html: `<div class="empty">No ${metric} in this period.</div>`, legend: [], mount() {} };
  }
  const html = barChart({ id: key, series, cols: cols.map((c) => ({ values: Object.fromEntries(ids.map((id) => [id, val(c.by[id])])) })), labels, dense: grain === "hourly" });
  const tip = (i) => {
    const c = cols[i];
    if (!c) return "";
    const total = ids.reduce((t, id) => t + val(c.by[id]), 0);
    if (!total) return "";
    const rows = ids.map((id, idx) => ({ id, idx, v: val(c.by[id]) })).filter((r) => r.v).map((r) => ({ k: nm[r.id] || r.id, v: metric === "tokens" ? fmt(r.v) : int(r.v), color: series[r.idx].color }));
    return `<div class="h"><span>${esc(title(c))}</span><span>${esc(metric === "tokens" ? fmt(total) : int(total))}</span></div>${ids.length > 1 ? tipRows(rows) : ""}`;
  };
  return {
    html,
    legend: ids.length > 1 ? series.map((s) => ({ label: s.label, color: s.color })) : [],
    mount(root) { bindChart(root, key, tip); },
  };
}

function dayLabel(date, long = false) {
  const [y, m, d] = String(date).split("-").map(Number);
  const dt = new Date(Date.UTC(y, m - 1, d));
  return dt.toLocaleDateString("en-GB", { timeZone: "UTC", weekday: "short", ...(long ? { day: "numeric", month: "short" } : {}) }).replace(",", "");
}

// ---------- burn tables ----------

export function allowanceBurnTable(ids, long) {
  const all = accounts();
  const rows = ids.map((id) => {
    const a = all.find((x) => x.id === id);
    const ser = allowanceSeries(id, long);
    const tr = trajectory(ser);
    const href = "#/accounts/" + encodeURIComponent(id);
    if (!tr) return { href, cells: [logo(a?.provider), email(a?.email || id), `<span class="muted">No reading yet</span>`, "", "", "", ""] };
    // Per day is the last 24 hours, or the readings so far scaled to a day.
    const perDay = tr.coveredH >= 1 ? (tr.burned / tr.coveredH) * 24 : null;
    const dayCell = perDay == null ? `<span class="muted">–</span>` : tr.coveredH >= 20 ? pctRate(perDay) : `${pctRate(perDay)}<span class="muted">from ${Math.round(tr.coveredH)}h</span>`;
    return {
      href,
      cells: [
        logo(a?.provider), email(a?.email || id), meterCell(tr.leftNow),
        dayCell, tr.perHour == null ? `<span class="muted">${tr.idle ? "Idle" : "–"}</span>` : pctRate(tr.perHour),
        outlook(tr), tr.reset ? esc(resetShort(tr.reset)) : "",
      ],
    };
  });
  const cols = [
    { label: "", w: 16, cls: "ic" }, { label: "Account" }, { label: long ? "Week left" : "5 hours left", w: 120 },
    { label: "Per day", w: 136, r: true }, { label: "Per hour now", w: 104, r: true },
    { label: "At this rate", w: 240 }, { label: "Resets", w: 104, r: true },
  ];
  return table(cols, rows, { empty: "No accounts." });
}

export function volumeBurnTable(ids, metric) {
  const all = accounts();
  const acc = S.data?.summary?.accounts || {};
  const val = (u) => (!u ? 0 : metric === "tokens" ? tokens(u) : Number(u.requests) || 0);
  const f = (v) => (metric === "tokens" ? fmt(v) : int(Math.round(v)));
  const total = { h: 0, d: 0, today: 0 };
  const rows = ids.map((id) => {
    const a = all.find((x) => x.id === id);
    const h = val(acc[id]?.last_24h) / 24, d = val(acc[id]?.last_7d) / 7, today = val(acc[id]?.today);
    total.h += h; total.d += d; total.today += today;
    return { href: "#/accounts/" + encodeURIComponent(id), cells: [logo(a?.provider), email(a?.email || id), f(h), f(d), f(today)] };
  });
  if (ids.length > 1) rows.push({ cls: "total", cells: ["", "All accounts", f(total.h), f(total.d), f(total.today)] });
  const cols = [
    { label: "", w: 16, cls: "ic" }, { label: "Account" },
    { label: "Per hour, last 24 hours", w: 168, r: true }, { label: "Per day, last 7 days", w: 160, r: true }, { label: "Today", w: 104, r: true },
  ];
  return table(cols, rows, { empty: "No accounts." });
}

// ---------- the Usage screen section ----------

const METRICS = [{ id: "allowance", label: "Allowance" }, { id: "tokens", label: "Tokens" }, { id: "requests", label: "Requests" }];

// ids: the accounts the provider tab allows.
export function overTime(ids) {
  const metric = S.ui.otMetric || "allowance";
  const pick = ids.includes(S.ui.otAcct) ? S.ui.otAcct : "";
  const shown = pick ? [pick] : ids;
  const nm = accountLabels();
  let grainSeg, chart, burn, sub;
  if (metric === "allowance") {
    const long = (S.ui.otWindow || "week") === "week";
    grainSeg = seg([{ id: "week", label: "Week" }, { id: "5h", label: "5 hours" }], long ? "week" : "5h", "data-ot-window", "bare");
    chart = allowanceChart({ key: "otChart", ids: shown, long });
    burn = allowanceBurnTable(shown, long);
    sub = long ? "How fast each account uses its weekly allowance, and where it ends up by the reset" : "How fast each account uses its 5-hour allowance, and where it ends up by the reset";
  } else {
    const grain = S.ui.otGrain || "hourly";
    grainSeg = seg([{ id: "hourly", label: "Hourly" }, { id: "daily", label: "Daily" }], grain, "data-ot-grain", "bare");
    chart = volumeChart({ key: "otChart", ids: shown, metric, grain });
    burn = volumeBurnTable(shown, metric);
    sub = metric === "tokens" ? "Tokens used, as an average rate" : "Requests sent, as an average rate";
  }
  const legend = shown.length > 1 && chart.legend.length ? `<div class="legend">${chart.legend.map((l) => `<span><i style="background:${l.color}"></i>${esc(l.label)}</span>`).join("")}</div>` : "";
  const html = `
    <div class="activity overtime">
      <div class="head"><b>Usage over time</b>
        <div class="row gap8">${seg(METRICS, metric, "data-ot-metric", "bare")}<span class="vsep"></span>${grainSeg}<span class="vsep"></span><button class="btn" data-ot-acct><span class="clamp">${esc(pick ? nm[pick] || pick : "All accounts")}</span>${icon("chevronDown", 12)}</button></div>
      </div>
      ${legend}
      ${chart.html}
    </div>
    <div class="sec">
      <div class="sech"><div class="t"><b>Burn rate</b><span class="muted">${esc(sub)}</span></div></div>
      ${burn}
    </div>`;
  const mount = (root) => {
    const rerender = () => window.dispatchEvent(new Event("dash:render"));
    root.querySelectorAll("[data-ot-metric]").forEach((b) => { b.onclick = () => { S.ui.otMetric = b.dataset.otMetric; rerender(); }; });
    root.querySelectorAll("[data-ot-window]").forEach((b) => { b.onclick = () => { S.ui.otWindow = b.dataset.otWindow; rerender(); }; });
    root.querySelectorAll("[data-ot-grain]").forEach((b) => { b.onclick = () => { S.ui.otGrain = b.dataset.otGrain; rerender(); }; });
    const ab = root.querySelector("[data-ot-acct]");
    if (ab) ab.onclick = (e) => menu(e.currentTarget, [
      { a: "All accounts", on: !pick, run: () => { S.ui.otAcct = ""; rerender(); } },
      "sep",
      ...ids.map((id) => ({ a: nm[id] || id, on: pick === id, run: () => { S.ui.otAcct = id; rerender(); } })),
    ], { width: 280, alignLeft: true });
    chart.mount(root);
  };
  return { html, mount };
}

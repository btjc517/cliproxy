// Usage: a year of token activity, period totals, and a breakdown table.
import {
  S, esc, fmt, int, icon, logo, email, accounts, status, warnState, sumUsage, tokens, cacheReuse, pctText, dayKey,
  hourly, clock, table, menu, seg, barChart, bindChart, tipRows,
} from "../core.js";
import { providerTabs, bindProviderTabs, readAt } from "./common.js";

const RANGES = [
  { id: "today", label: "Today", window: "today", days: 1 },
  { id: "24h", label: "Last 24 hours", window: "last_24h", hours: 24 },
  // Longer ranges are whole calendar days, so By account and By day agree.
  { id: "7d", label: "Last 7 days", window: "", days: 7 },
  { id: "14d", label: "Last 14 days", window: "", days: 14 },
];
const EMPTY = () => ({ requests: 0, failed: 0, input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 });
const add = (a, b) => { if (b) for (const k in a) a[k] += Number(b[k]) || 0; return a; };

// Days from the proxy history, filtered to a provider, keyed by YYYY-MM-DD.
function dayMap(prov) {
  const out = new Map();
  for (const d of S.data?.summary?.history?.days || []) {
    const t = EMPTY();
    for (const [p, v] of Object.entries(d.providers || {})) if (prov === "all" || p === prov) add(t, v);
    out.set(d.date, t);
  }
  return out;
}

const parseDay = (k) => { const [y, m, d] = k.split("-").map(Number); return new Date(Date.UTC(y, m - 1, d)); };
const keyOf = (dt) => dt.toISOString().slice(0, 10);
const longDay = (k) => parseDay(k).toLocaleDateString("en-GB", { timeZone: "UTC", weekday: "short", day: "numeric", month: "short" }).replace(",", "");
const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
const monthName = (k) => MONTHS[parseDay(k).getUTCMonth()];

function periods(map, prov) {
  const today = dayKey(Date.now());
  const t = parseDay(today);
  const monday = new Date(t); monday.setUTCDate(t.getUTCDate() - ((t.getUTCDay() + 6) % 7));
  const first = new Date(Date.UTC(t.getUTCFullYear(), t.getUTCMonth(), 1));
  const sums = { today: EMPTY(), week: EMPTY(), month: EMPTY(), life: EMPTY() };
  for (const [k, v] of map) {
    add(sums.life, v);
    if (k === today) add(sums.today, v);
    if (k >= keyOf(monday)) add(sums.week, v);
    if (k >= keyOf(first)) add(sums.month, v);
  }
  const hl = S.data?.summary?.history?.lifetime;
  if (prov === "all" && hl && tokens(hl) > tokens(sums.life)) sums.life = { ...EMPTY(), ...hl };
  return sums;
}

// 53 weeks, Monday first, ending this week.
function weeks(map) {
  const today = dayKey(Date.now());
  const t = parseDay(today);
  const monday = new Date(t); monday.setUTCDate(t.getUTCDate() - ((t.getUTCDay() + 6) % 7));
  const start = new Date(monday); start.setUTCDate(monday.getUTCDate() - 52 * 7);
  const cols = [];
  for (let w = 0; w < 53; w++) {
    const days = [];
    for (let d = 0; d < 7; d++) {
      const dt = new Date(start); dt.setUTCDate(start.getUTCDate() + w * 7 + d);
      const k = keyOf(dt);
      days.push({ k, v: map.get(k) || null, future: k > today });
    }
    cols.push(days);
  }
  return cols;
}

function levels(cols) {
  const vals = cols.flat().map((d) => (d.v ? tokens(d.v) : 0)).filter((v) => v > 0).sort((a, b) => a - b);
  const q = (p) => vals[Math.min(vals.length - 1, Math.floor(p * vals.length))] || 0;
  const cuts = [q(0.2), q(0.4), q(0.6), q(0.8)];
  return (v) => (!v ? 0 : v <= cuts[0] ? 1 : v <= cuts[1] ? 2 : v <= cuts[2] ? 3 : v <= cuts[3] ? 4 : 5);
}

function heat(cols) {
  const lv = levels(cols);
  const grid = cols.map((w, wi) => `<div class="wk">${w.map((d, di) => {
    const v = d.v ? tokens(d.v) : 0;
    return `<i class="${d.future ? "future" : "l" + lv(v)}" data-w="${wi}" data-d="${di}"></i>`;
  }).join("")}</div>`).join("");
  let lastMonth = "", lastAt = -9;
  const months = cols.map((w, wi) => {
    const m = monthName(w[w.length - 1].k);
    if (m === lastMonth) return "";
    lastMonth = m;
    if (wi > 50 || wi - lastAt < 3) return "";
    lastAt = wi;
    return `<span style="left:calc(${wi} * (100% - 16px) / 52)">${esc(m)}</span>`;
  }).join("");
  return `<div class="heatwrap" data-heat><div class="heat">${grid}</div><div class="heatx">${months}</div></div>`;
}

function weeklyChart(cols, cumulative) {
  let run = 0;
  const data = cols.map((w) => {
    const t = EMPTY();
    for (const d of w) add(t, d.v);
    run += tokens(t);
    return { start: w[0].k, t, v: cumulative ? run : tokens(t) };
  });
  let lastMonth = "";
  const labels = [];
  data.forEach((d, i) => {
    const m = monthName(d.start);
    if (m !== lastMonth && i < 51) labels.push({ i, text: m });
    lastMonth = m;
  });
  const html = barChart({ id: "usWeeks", series: [{ key: "v", color: "var(--chart-1)" }], cols: data.map((d) => ({ values: { v: d.v } })), labels, dense: true });
  const tip = (i) => {
    const d = data[i];
    if (!d || !d.v) return "";
    return `<div class="h"><span>Week of ${esc(longDay(d.start))}</span><span>${fmt(cumulative ? d.v : tokens(d.t))}</span></div>${tipRows(cumulative ? [{ k: "That week", v: fmt(tokens(d.t)) }] : breakdown(d.t))}`;
  };
  return { html, tip };
}

const breakdown = (t) => [
  { k: "Read from cache", v: fmt(t.cache_read_tokens) },
  { k: "Written to cache", v: fmt(t.cache_write_tokens) },
  { k: "Output", v: fmt(t.output_tokens) },
  { k: "New input", v: fmt(t.input_tokens) },
];

function reuseMeter(u) {
  const r = cacheReuse(u);
  if (r == null) return "";
  return `<span class="metercell"><span class="meter"><i style="width:${Math.round(r)}%"></i></span><span style="width:36px;text-align:right">${Math.round(r)}%</span></span>`;
}

function usageCells(u) {
  return [int(u.requests), fmt(u.input_tokens), fmt(u.cache_write_tokens), fmt(u.cache_read_tokens), fmt(u.output_tokens), reuseMeter(u)];
}

const NUM_COLS = [
  { label: "Requests", w: 80, r: true },
  { label: "New input", w: 96, r: true },
  { label: "Written to cache", w: 112, r: true },
  { label: "Read from cache", w: 112, r: true },
  { label: "Output", w: 88, r: true },
  { label: "Cache reuse", w: 136, cls: "reusecol" },
];

function accountUsage(a, range) {
  if (range.window) return sumUsage([a.id], range.window);
  const daily = S.data?.summary?.accounts?.[a.id]?.daily || [];
  return daily.slice(-range.days).reduce((t, d) => add(t, d), EMPTY());
}

export function view() {
  const prov = S.ui.usProv || "all";
  const grain = S.ui.usGrain || "daily";
  const by = S.ui.usBy || "account";
  const range = RANGES.find((r) => r.id === (S.ui.usRange || "7d")) || RANGES[2];
  const map = dayMap(prov);
  const sums = periods(map, prov);
  const cols = weeks(map);
  const weekly = grain !== "daily" ? weeklyChart(cols, grain === "cumulative") : null;

  const fig = (label, u) => `<div><span class="muted">${label}</span><span class="bignum"><span class="v">${fmt(tokens(u))}</span></span></div>`;
  const scale = `<div class="scale">Less <i style="background:var(--surface-2)"></i><i style="background:color-mix(in srgb, var(--chart-1) 18%, transparent)"></i><i style="background:var(--chart-p50)"></i><i style="background:var(--chart-p90)"></i><i style="background:var(--chart-1)"></i><i style="background:var(--chart-p99)"></i> More</div>`;

  let tableHtml;
  if (by === "account") {
    const list = accounts().filter((a) => prov === "all" || a.provider === prov);
    const total = EMPTY();
    const rows = list.map((a) => {
      const u = accountUsage(a, range);
      add(total, u);
      const st = status(a);
      const warn = st.kind === "blocked" || st.kind === "error" ? warnState(st.text) : "";
      return { href: "#/accounts/" + encodeURIComponent(a.id), cells: [logo(a.provider), `${email(a.email)}${warn}`, ...usageCells(u)] };
    });
    rows.push({ cls: "total", cells: ["", "All accounts", ...usageCells(total)] });
    tableHtml = table([{ label: "", w: 16, cls: "ic" }, { label: "Account" }, ...NUM_COLS], rows);
  } else if (range.hours) {
    // A rolling window splits across calendar days; each row covers only its part.
    const ids = accounts().filter((a) => prov === "all" || a.provider === prov).map((a) => a.id);
    const { per, starts } = hourly(ids, 48);
    // The same cutoff the proxy uses for last_24h: hours that started under 24 hours ago.
    const gen = Date.parse(S.data?.summary?.generated_at) || Date.now();
    const days = new Map();
    starts.forEach((st, i) => {
      if (!(gen - Date.parse(st) < range.hours * 3600e3)) return;
      const k = dayKey(st);
      const d = days.get(k) || { from: st, u: EMPTY() };
      for (const id of ids) add(d.u, per[id]?.[i]);
      days.set(k, d);
    });
    const today = dayKey(Date.now());
    const rows = [...days.entries()].reverse().map(([k, d]) => ({ cells: [k === today ? "Today" : `${longDay(k)}, from ${clock(d.from)}`, ...usageCells(d.u)] }));
    tableHtml = table([{ label: "Day" }, ...NUM_COLS], rows);
  } else {
    const today = parseDay(dayKey(Date.now()));
    const rows = [];
    for (let i = 0; i < range.days; i++) {
      const dt = new Date(today); dt.setUTCDate(today.getUTCDate() - i);
      const k = keyOf(dt);
      const u = map.get(k) || EMPTY();
      rows.push({ cells: [i === 0 ? "Today" : longDay(k), ...usageCells(u)] });
    }
    tableHtml = table([{ label: "Day" }, ...NUM_COLS], rows);
  }

  const html = `
    <div class="bar">${providerTabs("usProv")}<span class="muted nowrap">${esc(readAt())}</span></div>
    <div class="body">
      <div class="activity">
        <div class="head"><b>Token activity</b>${seg([{ id: "daily", label: "Daily" }, { id: "weekly", label: "Weekly" }, { id: "cumulative", label: "Cumulative" }], grain, "data-grain", "bare")}</div>
        <div class="figsrow"><div class="four">${fig("Today", sums.today)}${fig("This week", sums.week)}${fig("This month", sums.month)}${fig("Lifetime", sums.life)}</div>${grain === "daily" ? scale : ""}</div>
        ${grain === "daily" ? heat(cols) : weekly.html}
      </div>
      <div class="sec last">
        <div class="sech">${seg([{ id: "account", label: "By account" }, { id: "day", label: "By day" }], by, "data-by", "bare")}<span class="muted">${esc(range.label)}</span></div>
        ${tableHtml}
      </div>
      <div class="pad-floater"></div>
    </div>
    <div class="floater"><button class="btn" data-range>${icon("calendar", 14)}<span>${esc(range.label)}</span>${icon("chevronDown", 12)}</button></div>`;

  return {
    html,
    mount(root) {
      const rerender = () => window.dispatchEvent(new Event("dash:render"));
      bindProviderTabs(root, "usProv");
      root.querySelectorAll("[data-grain]").forEach((b) => { b.onclick = () => { S.ui.usGrain = b.dataset.grain; rerender(); }; });
      root.querySelectorAll("[data-by]").forEach((b) => { b.onclick = () => { S.ui.usBy = b.dataset.by; rerender(); }; });
      root.querySelectorAll(".reusecol").forEach((c) => { c.style.paddingLeft = "24px"; });
      root.querySelector("[data-range]").onclick = (e) => menu(e.currentTarget, RANGES.map((r) => ({ a: r.label, on: r.id === range.id, run: () => { S.ui.usRange = r.id; rerender(); } })), { width: 200, alignLeft: true });
      if (weekly) bindChart(root, "usWeeks", weekly.tip);
      const wrap = root.querySelector("[data-heat]");
      if (wrap) {
        wrap.scrollLeft = wrap.scrollWidth;
        let tip = null, hot = null;
        wrap.addEventListener("mousemove", (e) => {
          const cell = e.target.closest("i[data-w]");
          if (!cell || cell === hot) return;
          const d = cols[Number(cell.dataset.w)][Number(cell.dataset.d)];
          if (hot) hot.classList.remove("hot");
          if (d.future) { if (tip) { tip.remove(); tip = null; S.hold = Math.max(0, S.hold - 1); } hot = null; return; }
          hot = cell;
          cell.classList.add("hot");
          const host = wrap.parentElement;
          if (!tip) { tip = document.createElement("div"); tip.className = "tip"; tip.style.width = "232px"; host.appendChild(tip); S.hold++; }
          const u = d.v || EMPTY();
          tip.innerHTML = `<div class="h"><span>${esc(longDay(d.k))}</span><span>${fmt(tokens(u))}</span></div>${tipRows(breakdown(u))}`;
          const hr = host.getBoundingClientRect(), cr = cell.getBoundingClientRect();
          const x = Math.max(0, Math.min(cr.left - hr.left - 116 + 8, hr.width - 232));
          let y = cr.top - hr.top - tip.offsetHeight - 8;
          if (y < 0) y = cr.bottom - hr.top + 8;
          tip.style.left = x + "px";
          tip.style.top = y + "px";
        });
        wrap.addEventListener("mouseleave", () => {
          if (hot) hot.classList.remove("hot");
          hot = null;
          if (tip) { tip.remove(); tip = null; S.hold = Math.max(0, S.hold - 1); }
        });
      }
    },
  };
}

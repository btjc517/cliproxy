// Usage: where each allowance is heading, tokens by account over the chosen
// range, and a year of daily token activity.
import {
  S, esc, fmt, int, logo, email, status, warnState, sumUsage, tokens, cacheReuse, pctText, dayKey, hourly, tipRows, seg, table, figure,
  timeChart, bindChart, chartFormat, formatToggle, bindFormatToggles, timeLabels, bucketTitle, rangeTabs, bindRangeTabs, screenRange,
  providerTitle,
} from "../core.js";
import { providerTabs, bindProviderTabs, accountChips, scopeOf, readAt, ACCOUNT_COLORS, gapNote } from "./common.js";
import { allowanceChart, allowanceTable } from "./burn.js";

const DAY = 864e5;
const EMPTY = () => ({ requests: 0, failed: 0, input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 });
const add = (a, b) => { if (b) for (const k in a) a[k] += Number(b[k]) || 0; return a; };
const rerender = () => window.dispatchEvent(new Event("dash:render"));

const parseDay = (k) => { const [y, m, d] = k.split("-").map(Number); return new Date(Date.UTC(y, m - 1, d)); };
const keyOf = (dt) => dt.toISOString().slice(0, 10);
const longDay = (k) => parseDay(k).toLocaleDateString("en-GB", { timeZone: "UTC", weekday: "short", day: "numeric", month: "short" }).replace(",", "");
const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
const monthName = (k) => MONTHS[parseDay(k).getUTCMonth()];

const RANGE_TEXT = { "24h": "Last 24 hours", "7d": "Last 7 days", "30d": "Last month", "180d": "Last 6 months", all: "All time" };

// ---------- history ----------

// Token totals by day, keyed YYYY-MM-DD: the proxy history for a provider,
// or the picked accounts' own counters when only some are picked. Those are
// the last 14 days, plus whatever daily usage_range the screen has loaded.
function dayMap(sc) {
  const out = new Map();
  if (sc.some) {
    const ur = S.data?.summary?.usage_range;
    const daily = ur && Number(ur.bucket_seconds) === 86400 && Array.isArray(ur.starts) ? ur.starts.map((s) => dayKey(s)) : [];
    const covered = new Set(daily);
    for (const id of sc.ids) {
      for (const d of S.data?.summary?.accounts?.[id]?.daily || []) {
        if (!d.date || covered.has(d.date)) continue;
        out.set(d.date, add(out.get(d.date) || EMPTY(), d));
      }
      (ur?.accounts?.[id] || []).forEach((b, i) => { if (daily[i]) out.set(daily[i], add(out.get(daily[i]) || EMPTY(), b)); });
    }
    return out;
  }
  for (const d of S.data?.summary?.history?.days || []) {
    const t = EMPTY();
    for (const [p, v] of Object.entries(d.providers || {})) if (sc.prov === "all" || p === sc.prov) add(t, v);
    out.set(d.date, t);
  }
  return out;
}

function periods(map, sc) {
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
  if (sc.prov === "all" && hl && tokens(hl) > tokens(sums.life)) sums.life = { ...EMPTY(), ...hl };
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

const breakdown = (t) => [
  { k: "Read from cache", v: fmt(t.cache_read_tokens) },
  { k: "Written to cache", v: fmt(t.cache_write_tokens) },
  { k: "Output", v: fmt(t.output_tokens) },
  { k: "New input", v: fmt(t.input_tokens) },
];

function historySub(map, cols) {
  const first = [...map.entries()].filter(([, v]) => tokens(v) > 0).map(([k]) => k).sort()[0];
  if (!first) return "No activity yet";
  return first < cols[0][0].k ? "Every day of the last year" : `Every day since ${monthName(first)}`;
}

// ---------- tokens by account over the range ----------

// Per-account buckets for the range: the backend's usage_range when it covers
// this range, else what the summary already has (hours for 24 hours, days for 7 days).
function usageBuckets(ids, range) {
  const ur = S.data?.summary?.usage_range;
  if (ur && ur.range === range && S.dataRange === range && Array.isArray(ur.starts) && ur.starts.length) {
    const step = (Number(ur.bucket_seconds) || 3600) * 1000;
    const per = Object.fromEntries(ids.map((id) => [id, ur.accounts?.[id] || []]));
    const starts = ur.starts.map((s) => Date.parse(s));
    const totals = add(EMPTY(), null);
    for (const id of ids) for (const b of per[id]) add(totals, b);
    return { starts, step, per, totals };
  }
  if (range === "24h") {
    const { per, starts } = hourly(ids, 24);
    return { starts: starts.map((s) => Date.parse(s)), step: 3600e3, per, totals: sumUsage(ids, "last_24h") };
  }
  if (range === "7d") {
    const today = parseDay(dayKey(Date.now()));
    const keys = [];
    for (let i = 6; i >= 0; i--) { const dt = new Date(today); dt.setUTCDate(today.getUTCDate() - i); keys.push(keyOf(dt)); }
    const per = {};
    const totals = EMPTY();
    for (const id of ids) {
      const daily = S.data?.summary?.accounts?.[id]?.daily || [];
      per[id] = keys.map((k) => daily.find((d) => d.date === k) || null);
      for (const b of per[id]) add(totals, b);
    }
    // Noon UTC falls on the same calendar day in every time zone the proxy could use.
    return { starts: keys.map((k) => Date.parse(k + "T12:00:00Z")), step: DAY, per, totals };
  }
  return null;
}

const METRICS = [
  { id: "tokens", label: "Tokens", val: (b) => tokens(b), f: fmt },
  { id: "requests", label: "Requests", val: (b) => Number(b.requests) || 0, f: int },
  { id: "cache", label: "Cache reuse" },
  { id: "output", label: "Output", val: (b) => Number(b.output_tokens) || 0, f: fmt },
];

function tokensSection(sc, range, colorOf) {
  const ids = sc.ids;
  const data = usageBuckets(ids, range);
  const metric = METRICS.find((m) => m.id === S.ui.usMetric) || METRICS[0];
  const u = data ? data.totals : null;
  const tile = (m) => {
    const v = !u ? "–" : m.id === "cache" ? pctText(cacheReuse(u)) : m.id === "tokens" ? fmt(tokens(u)) : m.id === "requests" ? int(u.requests) : fmt(u.output_tokens);
    return figure(m.label, esc(v), "", { metric: m.id, on: m.id === metric.id });
  };
  const nm = Object.fromEntries(sc.shown.map((a) => [a.id, a.email]));
  let chart = `<div class="empty">No usage for this range yet. It fills in once the proxy keeps it.</div>`, tip = () => "";
  const chartId = "usage-tokens";
  if (data && data.starts.length) {
    const n = data.starts.length;
    const at = (id, i) => data.per[id]?.[i] || null;
    const sumAt = (i) => ids.reduce((t, id) => add(t, at(id, i)), EMPTY());
    let series, cols;
    if (metric.id === "cache") {
      series = [{ key: "all", color: "var(--chart-1)", label: "Cache reuse", gaps: true }];
      cols = data.starts.map((_, i) => { const r = cacheReuse(sumAt(i)); return { empty: r == null, values: { all: r } }; });
    } else {
      series = ids.map((id) => ({ key: id, color: colorOf(id), label: nm[id] || id }));
      cols = data.starts.map((_, i) => ({ values: Object.fromEntries(ids.map((id) => [id, at(id, i) ? metric.val(at(id, i)) : 0])) }));
    }
    const isCache = metric.id === "cache";
    chart = timeChart({
      id: chartId, format: chartFormat(chartId), series, cols, height: 140, max: isCache ? 100 : null,
      yfmt: isCache ? (v) => Math.round(v) + "%" : (v) => fmt(v), labels: timeLabels(data.starts, data.step), dense: n > 40,
    });
    tip = (i) => {
      const s = sumAt(i);
      if (!s.requests) return isCache ? gapNote(data.starts, i, data.step, (k) => !!sumAt(k).requests) || "" : "";
      const head = `<div class="h"><span>${esc(bucketTitle(data.starts, i, data.step))}</span><span>${esc(isCache ? pctText(cacheReuse(s)) : metric.f(metric.val(s)))}</span></div>`;
      if (isCache || ids.length < 2) return head + tipRows(isCache ? [{ k: "Requests", v: int(s.requests) }] : breakdown(s));
      const rows = ids.map((id) => ({ id, v: at(id, i) ? metric.val(at(id, i)) : 0 })).filter((r) => r.v).map((r) => ({ k: nm[r.id] || r.id, v: metric.f(r.v), color: colorOf(r.id) }));
      return head + tipRows(rows);
    };
  }

  // Table: one row per account and a total.
  const cell0 = (v, f) => (Number(v) ? f(v) : `<span class="muted">0</span>`);
  const reuse = (x, color) => {
    const r = cacheReuse(x);
    if (r == null) return `<span class="muted">–</span>`;
    return `<span class="metercell"><span class="meter"><i style="width:${Math.round(r)}%;background:${color}"></i></span><span>${Math.round(r)}%</span></span>`;
  };
  const cells = (x, color) => [cell0(x.requests, int), cell0(x.input_tokens, fmt), cell0(x.cache_write_tokens, fmt), cell0(x.cache_read_tokens, fmt), cell0(x.output_tokens, fmt), reuse(x, color)];
  const per = (id) => {
    if (!data) return null;
    if (data.totals && ids.length === 1) return data.totals;
    if (range === "24h" && !(S.data?.summary?.usage_range?.range === range && S.dataRange === range)) return sumUsage([id], "last_24h");
    return (data.per[id] || []).reduce((t, b) => add(t, b), EMPTY());
  };
  const rows = sc.shown.map((a) => {
    const x = per(a.id) || EMPTY();
    const st = status(a);
    const warn = st.kind === "blocked" || st.kind === "error" ? warnState(st.text) : "";
    return { href: "#/accounts/" + encodeURIComponent(a.id), cells: [`<span class="lead"><i class="sq" style="background:${colorOf(a.id)}"></i>${logo(a.provider)}</span>`, `${email(a.email)}${warn}`, ...cells(x, colorOf(a.id))] };
  });
  if (ids.length > 1 && u) {
    const label = sc.some ? "Picked accounts" : sc.prov === "all" ? "All accounts" : `All ${providerTitle(sc.prov)} accounts`;
    rows.push({ cls: "total", cells: ["", label, ...cells(u, "var(--icon)")] });
  }
  const cols = [
    { label: "", w: 32, cls: "lead" }, { label: "Account" },
    { label: "Requests", w: 80, r: true, cls: "num" }, { label: "New input", w: 96, r: true, cls: "num" },
    { label: "Written to cache", w: 112, r: true, cls: "num" }, { label: "Read from cache", w: 112, r: true, cls: "num" },
    { label: "Output", w: 88, r: true, cls: "num" }, { label: "Cache reuse", w: 136, cls: "num reusecol" },
  ];
  const sub = RANGE_TEXT[range] + (ids.length > 1 ? ", by account" : "");
  const html = `<div class="usec chartbox">
    <div class="block tight">
      <div class="uhead"><div class="t"><b>Tokens</b><span class="muted">${esc(sub)}</span></div>${data && data.starts.length ? `<div class="ctools">${formatToggle(chartId)}</div>` : ""}</div>
      <div class="uvis"><div class="figtabs">${METRICS.map(tile).join("")}</div>${chart}</div>
    </div>
    ${table(cols, rows, { empty: "No accounts." })}
  </div>`;
  const mount = (root) => {
    root.querySelectorAll(".uvis [data-metric]").forEach((b) => { b.onclick = () => { S.ui.usMetric = b.dataset.metric; rerender(); }; });
    bindChart(root, chartId, tip);
  };
  return { html, mount };
}

// ---------- the screen ----------

export function view() {
  const sc = scopeOf("usProv");
  const ids = sc.ids;
  const range = screenRange("usage");
  // One colour per account across the allowance chart, the tokens chart and both tables.
  const colorOf = (id) => (ids.length === 1 ? "var(--chart-1)" : ACCOUNT_COLORS[Math.max(0, ids.indexOf(id)) % ACCOUNT_COLORS.length]);

  const long = (S.ui.usWindow || "week") === "week";
  const allowance = allowanceChart({ key: "usAllowance", ids, long, colorOf });
  const allowanceSec = `<div class="usec">
    <div class="block">
      <div class="uhead"><div class="t"><b>Allowance</b><span class="muted">${long ? "Where each weekly limit is heading before it resets" : "Where each 5-hour limit is heading before it resets"}</span></div>${seg([{ id: "week", label: "Week" }, { id: "5h", label: "5 hours" }], long ? "week" : "5h", "data-us-window", "bare")}</div>
      ${allowance.html}
    </div>
    ${allowanceTable(ids, long, colorOf)}
  </div>`;

  const tok = tokensSection(sc, range, colorOf);

  const map = dayMap(sc);
  const sums = periods(map, sc);
  const cols = weeks(map);
  const fig = (label, x) => `<div><span class="muted">${label}</span><span class="bignum"><span class="v">${fmt(tokens(x))}</span></span></div>`;
  const scale = `<div class="scale">Less <i style="background:var(--surface-2)"></i><i style="background:color-mix(in srgb, var(--chart-1) 18%, transparent)"></i><i style="background:var(--chart-p50)"></i><i style="background:var(--chart-p90)"></i><i style="background:var(--chart-1)"></i><i style="background:var(--chart-p99)"></i> More</div>`;
  const historySec = `<div class="activity">
    <div class="uhead"><div class="t"><b>History</b><span class="muted">${esc(historySub(map, cols))}</span></div></div>
    <div class="figsrow"><div class="four">${fig("Today", sums.today)}${fig("This week", sums.week)}${fig("This month", sums.month)}${fig("Lifetime", sums.life)}</div>${scale}</div>
    ${heat(cols)}
  </div>`;

  const html = `
    <div class="bar wrap">${providerTabs("usProv")}<div class="end" style="gap:16px"><span class="muted nowrap readat">${esc(readAt())}</span>${rangeTabs("usage")}</div></div>
    ${accountChips("usProv")}
    <div class="body">
      ${allowanceSec}
      ${tok.html}
      ${historySec}
    </div>`;

  return {
    html,
    mount(root) {
      bindProviderTabs(root, "usProv");
      bindRangeTabs(root, "usage");
      bindFormatToggles(root);
      allowance.mount(root);
      tok.mount(root);
      root.querySelectorAll("[data-us-window]").forEach((b) => { b.onclick = () => { S.ui.usWindow = b.dataset.usWindow; rerender(); }; });
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
          const x = d.v || EMPTY();
          tip.innerHTML = `<div class="h"><span>${esc(longDay(d.k))}</span><span>${fmt(tokens(x))}</span></div>${tipRows(breakdown(x))}`;
          const hr = host.getBoundingClientRect(), cr = cell.getBoundingClientRect();
          const left = Math.max(0, Math.min(cr.left - hr.left - 116 + 8, hr.width - 232));
          let y = cr.top - hr.top - tip.offsetHeight - 8;
          if (y < 0) y = cr.bottom - hr.top + 8;
          tip.style.left = left + "px";
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

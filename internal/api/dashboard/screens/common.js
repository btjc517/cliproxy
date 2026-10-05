// Pieces shared by several screens: provider tabs and account chips, the
// usage figure strip with its hourly chart, and session rows.
import {
  S, esc, fmt, int, ms, pctText, rateText, clock, day, seen, icon, logo, tabs, table, figure, timeChart, bindChart, tipRows,
  accounts, hourly, tokens, cacheReuse, perf, sumUsage, names, sessionTitle, sessionHref, warnState,
  chartFormat, formatToggle, bindFormatToggles, timeLabels, bucketTitle, whenShort,
} from "../core.js";

export const PROVIDERS = [
  { id: "all", label: "All" },
  { id: "claude", label: "Claude" },
  { id: "codex", label: "Codex" },
];

const rerender = () => window.dispatchEvent(new Event("dash:render"));

export function providerTabs(key, counts) {
  const active = S.ui[key] || "all";
  const items = PROVIDERS.map((p) => ({ ...p, n: counts ? counts[p.id] : undefined }));
  return tabs(items, active, `data-prov="${key}" data-tab`);
}

// The accounts a screen shows: those of the provider tab, narrowed to the
// picked account chips. None picked and all picked both mean all of them.
export function scopeOf(key) {
  const prov = S.ui[key] || "all";
  const all = accounts().filter((a) => prov === "all" || a.provider === prov);
  const picked = prov === "all" ? [] : (S.ui[key + "Pick"]?.[prov] || []).filter((id) => all.some((a) => a.id === id));
  const some = picked.length > 0 && picked.length < all.length;
  const shown = some ? all.filter((a) => picked.includes(a.id)) : all;
  return { prov, all, shown, ids: shown.map((a) => a.id), picked, some };
}

// The second row under the bar on Claude or Codex: one chip per account.
export function accountChips(key) {
  const sc = scopeOf(key);
  if (sc.prov === "all" || !sc.all.length) return "";
  const on = new Set(sc.picked);
  const chips = sc.all.map((a) => `<button class="chip ${on.has(a.id) ? "on" : ""}" data-chip="${esc(key)}" data-id="${esc(a.id)}" aria-pressed="${on.has(a.id)}">${esc(a.email)}</button>`).join("");
  const m = sc.all.length;
  const end = sc.some
    ? `<span class="muted nowrap">${sc.shown.length} of ${m} accounts</span><button class="btn showall" data-chipall="${esc(key)}">Show all</button>`
    : `<span class="muted nowrap">Showing all ${m} ${m === 1 ? "account" : "accounts"}</span>`;
  return `<div class="chips"><div class="chiplist">${chips}</div><div class="chipsel">${end}</div></div>`;
}

export function bindProviderTabs(root, key) {
  root.querySelectorAll(`[data-prov="${key}"]`).forEach((b) => {
    b.onclick = () => { S.ui[key] = b.dataset.tab; rerender(); };
  });
  const store = () => (S.ui[key + "Pick"] = S.ui[key + "Pick"] || {});
  root.querySelectorAll(`[data-chip="${key}"]`).forEach((b) => {
    b.onclick = () => {
      const prov = S.ui[key];
      const cur = new Set(store()[prov] || []);
      if (cur.has(b.dataset.id)) cur.delete(b.dataset.id);
      else cur.add(b.dataset.id);
      store()[prov] = [...cur];
      rerender();
    };
  });
  const all = root.querySelector(`[data-chipall="${key}"]`);
  if (all) all.onclick = () => { store()[S.ui[key]] = []; rerender(); };
}

// ---------- performance for a set of accounts ----------

const PCT_KEYS = ["ttft_p50_ms", "ttft_p90_ms", "latency_p50_ms", "latency_p90_ms", "throughput_p50", "throughput_p10"];
const SUM_KEYS = ["requests", "failed", "failovers", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens"];

// The stored scope when the accounts match one (all, a provider, one
// account). Otherwise the accounts combined: counts add up, and percentiles
// are a request-weighted mean of each account's, which is close but not exact.
export function perfFor(sc, range = "24h") {
  if (sc.prov === "all") return perf("all", range);
  if (!sc.some) return perf(sc.prov, range);
  if (sc.ids.length === 1) return perf(sc.ids[0], range);
  const list = sc.ids.map((id) => perf(id, range)).filter(Boolean);
  if (!list.length) return null;
  const has = (k, o) => o && Object.prototype.hasOwnProperty.call(o, k);
  const wmean = (rows, f) => {
    let w = 0, s = 0;
    for (const r of rows) { const v = Number(f(r)) || 0, n = Number(r.requests) || 0; if (v > 0 && n > 0) { s += v * n; w += n; } }
    return w ? s / w : 0;
  };
  const pct = (k) => ({ p50: wmean(list, (p) => p[k]?.p50), p90: wmean(list, (p) => p[k]?.p90), p99: wmean(list, (p) => p[k]?.p99), p10: wmean(list, (p) => p[k]?.p10) });
  const len = Math.max(0, ...list.map((p) => (p.series || []).length));
  const series = [];
  for (let i = 0; i < len; i++) {
    const pts = list.map((p) => { const s = p.series || []; return s[s.length - len + i]; }).filter(Boolean);
    const b = { start: pts[0]?.start };
    for (const k of SUM_KEYS) if (pts.some((x) => has(k, x))) b[k] = pts.reduce((t, x) => t + (Number(x[k]) || 0), 0);
    for (const k of PCT_KEYS) if (pts.some((x) => has(k, x))) b[k] = wmean(pts, (x) => x[k]);
    series.push(b);
  }
  const out = { requests: 0, failed: 0, ttft_ms: pct("ttft_ms"), latency_ms: pct("latency_ms"), throughput: pct("throughput"), series };
  for (const p of list) { out.requests += Number(p.requests) || 0; out.failed += Number(p.failed) || 0; }
  if (list.some((p) => has("failovers", p))) out.failovers = list.reduce((t, p) => t + (Number(p.failovers) || 0), 0);
  return out;
}

// A one-line note for a bucket inside a stretch with no requests.
export function gapNote(starts, i, step, has) {
  if (has(i)) return null;
  let a = i, b = i;
  while (a > 0 && !has(a - 1)) a--;
  while (b < starts.length - 1 && !has(b + 1)) b++;
  if (step >= 864e5) return { small: true, html: a === b ? `No requests ${esc(day(starts[a]))}` : `No requests ${esc(day(starts[a]))} to ${esc(day(starts[b]))}` };
  const end = b + 1 < starts.length ? esc(whenShort(starts[b + 1], step)) : "now";
  return { small: true, html: `No requests ${esc(whenShort(starts[a], step))} to ${end}` };
}

export const legendHtml = (items) => items.map((l) => `<span><i style="background:${l.color}"></i>${esc(l.label)}</span>`).join("");

export function readAt(prefix = "Read") {
  return S.readAt ? `${prefix} ${clock(S.readAt)}` : "";
}

export const ACCOUNT_COLORS = ["var(--chart-1)", "var(--chart-p90)", "var(--chart-p50)", "var(--chart-p99)", "#A3A3A3", "#6E9EEF"];

// ---------- usage figures and the 24-hour chart ----------

const METRICS = [
  { id: "requests", label: "Requests" },
  { id: "tokens", label: "Tokens" },
  { id: "cache", label: "Cache reuse" },
  { id: "ttft", label: "First token, median" },
  { id: "throughput", label: "Throughput" },
  { id: "failure", label: "Failure rate" },
];

// ids: accounts in scope; p: their performance over 24 hours (perfFor);
// stackBy: "provider" | "account" | "none"; chart: the id its line or bars choice is kept under.
export function usageStrip({ key, chart: chartId, ids, p, stackBy, legend = true }) {
  const metric = S.ui[key] || "requests";
  const u = sumUsage(ids, "last_24h");
  const values = {
    requests: int(u.requests),
    tokens: fmt(tokens(u)),
    cache: pctText(cacheReuse(u)),
    ttft: p?.ttft_ms?.p50 ? ms(p.ttft_ms.p50) : "–",
    throughput: p?.throughput?.p50 ? String(Math.round(p.throughput.p50)) : "–",
    failure: rateText(u.failed, u.requests),
  };
  const figs = METRICS.map((m) => figure(m.label, esc(values[m.id]), m.id === "throughput" && values.throughput !== "–" ? "tokens/s" : "", { metric: m.id, on: m.id === metric })).join("");

  const chart = buildChart({ chartId, ids, p, stackBy, metric });
  const leg = legend && chart.series.length > 1 ? `<div class="legend">${legendHtml(chart.series)}</div>` : "";
  const tools = chart.drawn ? `<div class="ctools">${formatToggle(chartId)}</div>` : "";
  const html = `<div class="figs chartbox">
    <div class="figrow"><div class="figtabs">${figs}</div><div class="figend">${leg}${tools}</div></div>
    ${chart.html}
  </div>`;
  const mount = (root) => {
    root.querySelectorAll(`.figs [data-metric]`).forEach((b) => {
      b.onclick = () => { S.ui[key] = b.dataset.metric; rerender(); };
    });
    bindFormatToggles(root.querySelector(".figs") || root);
    bindChart(root, chartId, chart.tip);
  };
  return { html, mount };
}

const HOUR = 3600e3;

function buildChart({ chartId, ids, p, stackBy, metric }) {
  const all = accounts().filter((a) => ids.includes(a.id));
  const nm = names();
  const format = chartFormat(chartId);
  if (metric === "ttft" || metric === "throughput") {
    const series = (p?.series || []).slice(-24);
    if (!series.length) return { series: [], html: `<div class="empty">No timing data yet. It fills in as requests arrive.</div>`, tip: () => "" };
    const isT = metric === "ttft";
    const ser = isT
      ? [{ key: "p50", color: "var(--chart-1)", label: "Median", gaps: true }, { key: "p90", color: "var(--chart-p50)", label: "p90", gaps: true }]
      : [{ key: "p50", color: "var(--chart-1)", label: "Median", gaps: true }];
    const starts = series.map((b) => Date.parse(b.start));
    const cols = series.map((b) => ({ empty: !b.requests, values: isT ? { p50: b.ttft_p50_ms, p90: b.ttft_p90_ms } : { p50: b.throughput_p50 } }));
    const html = timeChart({ id: chartId, format, series: ser, cols, overlay: true, yfmt: isT ? (v) => (v ? ms(v) : "0") : (v) => (v ? Math.round(v) + "/s" : "0"), labels: timeLabels(starts, HOUR) });
    const tip = (i) => {
      const b = series[i];
      if (!b) return "";
      if (!b.requests) return gapNote(starts, i, HOUR, (k) => !!series[k].requests) || "";
      const rows = isT
        ? [{ k: "Median", v: ms(b.ttft_p50_ms), color: "var(--chart-1)" }, { k: "p90", v: ms(b.ttft_p90_ms), color: "var(--chart-p50)" }]
        : [{ k: "Median", v: Math.round(b.throughput_p50) + " tokens/s", color: "var(--chart-1)" }];
      return `<div class="h"><span>${esc(bucketTitle(starts, i, HOUR))}</span><span>${int(b.requests)}</span></div>${tipRows([...rows, "hr", { k: "Failed", v: int(b.failed), cls: b.failed ? "warn" : "" }])}`;
    };
    return { series: ser, html, tip, drawn: true };
  }

  const { per, starts: rawStarts } = hourly(ids, 24);
  const starts = rawStarts.map((s) => Date.parse(s));
  let ser;
  if (stackBy === "provider") {
    ser = [{ key: "claude", color: "var(--chart-1)", label: "Claude" }, { key: "codex", color: "var(--chart-p50)", label: "Codex" }];
  } else if (stackBy === "account") {
    ser = all.map((a, i) => ({ key: a.id, color: ACCOUNT_COLORS[i % ACCOUNT_COLORS.length], label: nm[a.id] }));
  } else {
    ser = [{ key: "one", color: "var(--chart-1)", label: "" }];
  }
  const val = (b) => {
    if (!b) return 0;
    if (metric === "tokens") return tokens(b);
    if (metric === "failure") return b.failed || 0;
    return b.requests || 0;
  };
  const buckets = starts.map((s, i) => {
    const byAcct = {};
    const sum = { requests: 0, failed: 0, input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 };
    for (const a of all) {
      const b = per[a.id]?.[i];
      byAcct[a.id] = b;
      if (b) for (const k in sum) sum[k] += b[k] || 0;
    }
    return { start: s, byAcct, sum };
  });
  if (!buckets.length) return { series: [], html: `<div class="empty">No requests in the last 24 hours.</div>`, tip: () => "" };
  let cols;
  if (metric === "cache") {
    ser = [{ key: "one", color: "var(--chart-1)", label: "", gaps: true }];
    cols = buckets.map((b) => ({ empty: cacheReuse(b.sum) == null, values: { one: cacheReuse(b.sum) } }));
  } else {
    cols = buckets.map((b) => {
      const v = {};
      for (const s of ser) v[s.key] = 0;
      for (const a of all) {
        const k = stackBy === "provider" ? a.provider : stackBy === "account" ? a.id : "one";
        v[k] = (v[k] || 0) + val(b.byAcct[a.id]);
      }
      return { values: v };
    });
  }
  const yfmt = metric === "cache" ? (v) => Math.round(v) + "%" : (v) => fmt(v);
  const html = timeChart({ id: chartId, format, series: ser, cols, yfmt, max: metric === "cache" ? 100 : null, labels: timeLabels(starts, HOUR) });
  const tip = (i) => {
    const b = buckets[i];
    if (!b) return "";
    if (!b.sum.requests) return metric === "cache" ? gapNote(starts, i, HOUR, (k) => !!buckets[k].sum.requests) || "" : "";
    const total = metric === "cache" ? pctText(cacheReuse(b.sum)) : metric === "tokens" ? fmt(tokens(b.sum)) : metric === "failure" ? int(b.sum.failed) : int(b.sum.requests);
    const rows = [];
    all.forEach((a, idx) => {
      const x = b.byAcct[a.id];
      if (!x || !x.requests) return;
      const color = stackBy === "provider" ? (a.provider === "claude" ? "var(--chart-1)" : "var(--chart-p50)") : ACCOUNT_COLORS[idx % ACCOUNT_COLORS.length];
      rows.push({ k: nm[a.id], v: metric === "tokens" ? fmt(tokens(x)) : metric === "failure" ? int(x.failed) : int(x.requests), color });
    });
    const foot = [];
    if (metric !== "failure") foot.push({ k: "Failed", v: int(b.sum.failed), cls: b.sum.failed ? "warn" : "" });
    if (metric !== "cache") foot.push({ k: "Cache reuse", v: pctText(cacheReuse(b.sum)) });
    if (metric === "cache") foot.push({ k: "Requests", v: int(b.sum.requests) });
    return `<div class="h"><span>${esc(bucketTitle(starts, i, HOUR))}</span><span>${esc(total)}</span></div>${tipRows(rows.length > 1 || stackBy !== "none" ? [...rows, "hr", ...foot] : foot)}`;
  };
  return { series: ser, html, tip, drawn: true };
}

// ---------- sessions table ----------

export function accountCell(s, nm) {
  const ids = s.auth_ids || [];
  if (ids.length > 1) return warnState(`Switched, ${ids.length} accounts`);
  if (!ids.length) return `<span class="muted">No answer yet</span>`;
  return `<span class="clamp">${esc(nm[ids[0]] || ids[0])}</span>`;
}

export function sessionName(s) {
  const n = s.threads?.length || 0;
  return `<span class="clamp">${esc(sessionTitle(s))}</span>${n ? `<span class="threads">${n} ${n === 1 ? "thread" : "threads"}${icon("chevronRight", 14)}</span>` : ""}`;
}

export function reuseCell(s, withMeter) {
  const r = cacheReuse(s);
  if (r == null) return `<span class="muted">New</span>`;
  if (!withMeter) return `${Math.round(r)}%`;
  return `<span class="metercell"><span class="meter" style="width:56px"><i style="width:${Math.round(r)}%"></i></span><span class="${r ? "" : "muted"}" style="width:32px">${Math.round(r)}%</span></span>`;
}

export function activeSpan(s) {
  const a = Date.parse(s.first_seen), b = Date.parse(s.last_seen);
  const end = Date.now() - b < 2 * 60e3 ? "now" : clock(b);
  if (!a || b - a < 60e3) return end === "now" ? "Now" : clock(b);
  return `${clock(a)} to ${end}`;
}

// cols chosen per screen; "account" can be dropped when the screen is one account.
export function sessionTable(list, { withAccount = true, meter = false, lastSeen = "seen", empty = "No sessions" } = {}) {
  const nm = names();
  const cols = [
    ...(withAccount ? [{ label: "", w: 16, cls: "ic" }] : []),
    { label: "Session" },
    { label: "Machine", w: 112 },
    ...(withAccount ? [{ label: "Account", w: 240 }] : []),
    { label: "Requests", w: 88, r: true },
    { label: "Failed", w: 88, r: true },
    { label: "Cache reuse", w: meter ? 104 : 104, r: !meter },
    { label: lastSeen === "span" ? "Active" : "Last seen", w: lastSeen === "span" ? 112 : 88, r: true },
  ];
  const rows = list.map((s) => ({
    href: sessionHref(s),
    cells: [
      ...(withAccount ? [logo(s.provider)] : []),
      sessionName(s),
      s.machine ? esc(s.machine) : `<span class="muted">–</span>`,
      ...(withAccount ? [accountCell(s, nm)] : []),
      int(s.requests),
      `<span class="${s.failed ? "warn" : "muted"}">${int(s.failed)}</span>`,
      reuseCell(s, meter),
      lastSeen === "span" ? activeSpan(s) : seen(s.last_seen),
    ],
  }));
  return table(cols, rows, { empty });
}

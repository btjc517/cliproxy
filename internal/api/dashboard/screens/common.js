// Pieces shared by several screens: provider tabs, the usage figure strip
// with its hourly chart, and session rows.
import {
  S, esc, fmt, int, ms, pctText, rateText, clock, day, seen, icon, logo, tabs, table, figure, barChart, bindChart, tipRows,
  accounts, hourly, tokens, cacheReuse, perf, sumUsage, names, sessionTitle, sessionHref, warnState,
} from "../core.js";

export const PROVIDERS = [
  { id: "all", label: "All" },
  { id: "claude", label: "Claude" },
  { id: "codex", label: "Codex" },
];

export function providerTabs(key, counts) {
  const active = S.ui[key] || "all";
  const items = PROVIDERS.map((p) => ({ ...p, n: counts ? counts[p.id] : undefined }));
  return tabs(items, active, `data-prov="${key}" data-tab`);
}

export function bindProviderTabs(root, key) {
  root.querySelectorAll(`[data-prov="${key}"]`).forEach((b) => {
    b.onclick = () => { S.ui[key] = b.dataset.tab; window.dispatchEvent(new Event("dash:render")); };
  });
}

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

// ids: accounts in scope; scope: performance scope key; stackBy: "provider" | "account" | "none"
export function usageStrip({ key, ids, scope, stackBy, legend = true }) {
  const metric = S.ui[key] || "requests";
  const u = sumUsage(ids, "last_24h");
  const p = perf(scope);
  const values = {
    requests: int(u.requests),
    tokens: fmt(tokens(u)),
    cache: pctText(cacheReuse(u)),
    ttft: p?.ttft_ms?.p50 ? ms(p.ttft_ms.p50) : "–",
    throughput: p?.throughput?.p50 ? String(Math.round(p.throughput.p50)) : "–",
    failure: rateText(u.failed, u.requests),
  };
  const figs = METRICS.map((m) => figure(m.label, esc(values[m.id]), m.id === "throughput" && values.throughput !== "–" ? "tokens/s" : "", { metric: m.id, on: m.id === metric })).join("");

  const chart = buildChart({ key, ids, scope, stackBy, metric });
  const leg = legend && chart.series.length > 1 ? `<div class="legend">${chart.series.map((s) => `<span><i style="background:${s.color}"></i>${esc(s.label)}</span>`).join("")}</div>` : "";
  const html = `<div class="figs">
    <div class="figrow"><div class="figtabs">${figs}</div>${leg}</div>
    ${chart.html}
  </div>`;
  const mount = (root) => {
    root.querySelectorAll(`.figs [data-metric]`).forEach((b) => {
      b.onclick = () => { S.ui[key] = b.dataset.metric; window.dispatchEvent(new Event("dash:render")); };
    });
    bindChart(root, key, chart.tip);
  };
  return { html, mount };
}

function hourLabel(start) {
  const end = Date.parse(start) + 3600e3;
  return `${day(start).split(" ")[0]} ${clock(start)} to ${clock(end)}`;
}

function xLabels(starts) {
  const n = starts.length;
  const out = [];
  starts.forEach((s, i) => {
    if (i >= n - 2) return;
    const h = Number(clock(s).slice(0, 2));
    if (h % 4 === 0) out.push({ i, text: clock(s) });
  });
  out.push({ i: n - 1, text: "Now" });
  return out;
}

function buildChart({ key, ids, scope, stackBy, metric }) {
  const all = accounts().filter((a) => ids.includes(a.id));
  const nm = names();
  if (metric === "ttft" || metric === "throughput") {
    const p = perf(scope);
    const series = (p?.series || []).slice(-24);
    if (!series.length) return { series: [], html: `<div class="empty">No timing data yet. It fills in as requests arrive.</div>`, tip: () => "" };
    const isT = metric === "ttft";
    const ser = isT
      ? [{ key: "p50", color: "var(--chart-1)", label: "Median" }, { key: "p90", color: "var(--chart-p50)", label: "p90" }]
      : [{ key: "p50", color: "var(--chart-1)", label: "Median" }];
    const cols = series.map((b) => ({ values: isT ? { p50: b.ttft_p50_ms, p90: Math.max(0, (b.ttft_p90_ms || 0) - (b.ttft_p50_ms || 0)) } : { p50: b.throughput_p50 } }));
    const html = barChart({ id: key, series: ser, cols, yfmt: isT ? (v) => (v ? ms(v) : "0") : (v) => (v ? Math.round(v) + "/s" : "0"), labels: xLabels(series.map((b) => b.start)) });
    const tip = (i) => {
      const b = series[i];
      if (!b || !b.requests) return "";
      const rows = isT
        ? [{ k: "Median", v: ms(b.ttft_p50_ms), color: "var(--chart-1)" }, { k: "p90", v: ms(b.ttft_p90_ms), color: "var(--chart-p50)" }]
        : [{ k: "Median", v: Math.round(b.throughput_p50) + " tokens/s", color: "var(--chart-1)" }];
      return `<div class="h"><span>${esc(hourLabel(b.start))}</span><span>${int(b.requests)}</span></div>${tipRows([...rows, "hr", { k: "Failed", v: int(b.failed), cls: b.failed ? "warn" : "" }])}`;
    };
    return { series: ser, html, tip };
  }

  const { per, starts } = hourly(ids, 24);
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
  let cols;
  if (metric === "cache") {
    ser = [{ key: "one", color: "var(--chart-1)", label: "" }];
    cols = buckets.map((b) => ({ values: { one: cacheReuse(b.sum) || 0 } }));
  } else {
    cols = buckets.map((b) => {
      const v = {};
      for (const a of all) {
        const k = stackBy === "provider" ? a.provider : stackBy === "account" ? a.id : "one";
        v[k] = (v[k] || 0) + val(b.byAcct[a.id]);
      }
      return { values: v };
    });
  }
  const yfmt = metric === "cache" ? (v) => Math.round(v) + "%" : (v) => fmt(v);
  const html = barChart({ id: key, series: ser, cols, yfmt, labels: xLabels(starts) });
  const tip = (i) => {
    const b = buckets[i];
    if (!b || !b.sum.requests) return "";
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
    return `<div class="h"><span>${esc(hourLabel(b.start))}</span><span>${esc(total)}</span></div>${tipRows(rows.length > 1 || stackBy !== "none" ? [...rows, "hr", ...foot] : foot)}`;
  };
  return { series: ser, html, tip };
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

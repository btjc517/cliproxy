// Performance: time to first token, full response time, throughput and
// failures, over time or as a spread, overall and per account.
import {
  S, esc, int, ms, icon, logo, email, accounts, status, warnState, perf, rate, rateText, clock, day,
  table, menu, seg, barChart, bindChart, tipRows,
} from "../core.js";
import { providerTabs, bindProviderTabs, readAt } from "./common.js";

const RANGES = [
  { id: "24h", label: "Last 24 hours" },
  { id: "7d", label: "Last 7 days" },
  { id: "14d", label: "Last 14 days" },
];

const perSec = (v) => (v ? (v < 10 ? v.toFixed(1) : Math.round(v)) : "–");

function stat(label, value, unit, sub) {
  return `<div class="stat"><span class="muted">${esc(label)}</span><span class="v">${value}${unit ? `<span class="u">${esc(unit)}</span>` : ""}</span><span class="s">${sub || "&nbsp;"}</span></div>`;
}

function seriesLabels(series, range) {
  const out = [];
  const n = series.length;
  series.forEach((b, i) => {
    if (i >= n - 2) return;
    const t = Date.parse(b.start);
    if (range === "24h") {
      if (Number(clock(t).slice(0, 2)) % 4 === 0) out.push({ i, text: clock(t) });
    } else {
      const prev = i ? series[i - 1].start : "";
      const d = day(t).split(" ")[0];
      if (!prev || day(Date.parse(prev)).split(" ")[0] !== d) out.push({ i, text: d });
    }
  });
  if (range === "14d") for (let k = out.length - 1; k >= 0; k--) if (k % 2) out.splice(k, 1);
  out.push({ i: n - 1, text: "Now" });
  return out;
}

// A bucket ends where the next one starts; on days the clocks change a
// bucket can be an hour longer or shorter than bucket_seconds.
function bucketLabel(series, i) {
  const t = Date.parse(series[i].start);
  const next = series[i + 1] ? Date.parse(series[i + 1].start) : Date.now();
  return `${day(t)} ${clock(t)} to ${series[i + 1] ? clock(next) : "now"}`;
}

function overTime(id, p, which, range) {
  const series = p?.series || [];
  if (!series.some((b) => b.requests)) return { html: `<div class="empty">No timing data in this range yet.</div>`, tip: () => "" };
  const isT = which === "ttft";
  const ser = isT ? [{ key: "p50", color: "var(--chart-1)" }, { key: "p90", color: "var(--chart-p50)" }] : [{ key: "p50", color: "var(--chart-1)" }];
  const cols = series.map((b) => ({ values: isT ? { p50: b.ttft_p50_ms, p90: Math.max(0, (b.ttft_p90_ms || 0) - (b.ttft_p50_ms || 0)) } : { p50: b.throughput_p50 } }));
  const html = barChart({ id, series: ser, cols, height: 120, yfmt: isT ? (v) => (v ? ms(v) : "0") : (v) => (v ? Math.round(v) + "/s" : "0"), labels: seriesLabels(series, range), dense: series.length > 24 });
  const tip = (i) => {
    const b = series[i];
    if (!b || !b.requests) return "";
    const rows = isT
      ? [{ k: "Median", v: ms(b.ttft_p50_ms), color: "var(--chart-1)" }, { k: "p90", v: ms(b.ttft_p90_ms), color: "var(--chart-p50)" }]
      : [{ k: "Median", v: perSec(b.throughput_p50) + " tokens/s", color: "var(--chart-1)" }];
    return `<div class="h"><span>${esc(bucketLabel(series, i))}</span><span>${int(b.requests)}</span></div>${tipRows([...rows, "hr", { k: "Failed", v: int(b.failed), cls: b.failed ? "warn" : "" }])}`;
  };
  return { html, tip };
}

function spread(id, p, which) {
  const hist = (which === "ttft" ? p?.ttft_hist : p?.throughput_hist) || [];
  if (!hist.length) return { html: `<div class="empty">No timing data in this range yet.</div>`, tip: () => "" };
  const isT = which === "ttft";
  const bound = (h) => (isT ? h.le_ms : h.le);
  const label = (v) => (isT ? ms(v) : perSec(v) + "/s");
  const total = hist.reduce((t, h) => t + h.count, 0);
  const cols = hist.map((h) => ({ values: { n: h.count } }));
  const labels = [{ i: 0, text: label(bound(hist[0])) }];
  if (hist.length > 2) labels.push({ i: Math.floor(hist.length / 2), text: label(bound(hist[Math.floor(hist.length / 2)])) });
  if (hist.length > 1) labels.push({ i: hist.length - 1, text: label(bound(hist[hist.length - 1])) });
  const html = barChart({ id, series: [{ key: "n", color: "var(--chart-1)" }], cols, height: 120, yfmt: (v) => String(Math.round(v)), labels, dense: hist.length > 24 });
  const tip = (i) => {
    const h = hist[i];
    if (!h) return "";
    const lo = i ? bound(hist[i - 1]) : 0;
    return `<div class="h"><span>${esc(label(lo))} to ${esc(label(bound(h)))}</span><span>${int(h.count)}</span></div>${tipRows([{ k: "Share of requests", v: Math.round((h.count / total) * 100) + "%" }])}`;
  };
  return { html, tip };
}

function pane(id, title, legend, p, which, range) {
  const mode = S.ui[id + "Mode"] || "time";
  const c = mode === "time" ? overTime(id, p, which, range) : spread(id, p, which);
  const leg = mode === "time" ? legend.map((l) => `<span><i style="background:${l.color}"></i>${esc(l.label)}</span>`).join("") : "";
  return {
    html: `<div class="pane">
      <div class="panehead"><div class="row gap8"><b>${esc(title)}</b><div class="legend" style="padding:0">${leg}</div></div>${seg([{ id: "time", label: "Over time" }, { id: "spread", label: "Spread" }], mode, `data-pmode="${id}" data-v`, "bare")}</div>
      ${c.html}
    </div>`,
    tip: c.tip,
  };
}

export function view() {
  const prov = S.ui.pfProv || "all";
  const range = RANGES.find((r) => r.id === S.range) || RANGES[0];
  const p = perf(prov);
  const has = p && p.requests;
  const t = p?.ttft_ms || {}, l = p?.latency_ms || {}, tp = p?.throughput || {};
  const failRate = has ? rateText(p.failed, p.requests) : "–";

  const stats = `<div class="stats">
    ${stat("First token, median", has && t.p50 ? ms(t.p50) : "–", "", t.p90 ? `p90 ${ms(t.p90)}, p99 ${ms(t.p99)}` : "")}
    ${stat("Full response, median", has && l.p50 ? ms(l.p50) : "–", "", l.p90 ? `p90 ${ms(l.p90)}, p99 ${ms(l.p99)}` : "")}
    ${stat("Throughput, median", has && tp.p50 ? perSec(tp.p50) : "–", has && tp.p50 ? "tokens/s" : "", tp.p10 ? `Slowest 10% under ${perSec(tp.p10)}` : "")}
    ${stat("Failure rate", failRate, "", has ? `${int(p.failed)} of ${int(p.requests)} requests` : "")}
    ${stat("Moved to another account", has ? int(p.failovers || 0) : "–", "", "After a failed attempt")}
  </div>`;

  const a = pane("pfTtft", "Time to first token", [{ label: "Median", color: "var(--chart-1)" }, { label: "p90", color: "var(--chart-p50)" }], p, "ttft", range.id);
  const b = pane("pfTp", "Throughput", [{ label: "Median", color: "var(--chart-1)" }], p, "throughput", range.id);

  const list = accounts().filter((x) => prov === "all" || x.provider === prov);
  const rows = list.map((x) => {
    const q = perf(x.id);
    const st = status(x);
    const warn = st.kind === "blocked" || st.kind === "error" ? warnState(st.text) : "";
    const fr = q && q.requests ? rate(q.failed, q.requests) : null;
    return {
      href: "#/accounts/" + encodeURIComponent(x.id),
      cells: [
        logo(x.provider),
        `${email(x.email)}${warn}`,
        q ? int(q.requests) : `<span class="muted">0</span>`,
        q?.ttft_ms?.p50 ? ms(q.ttft_ms.p50) : "",
        q?.ttft_ms?.p90 ? ms(q.ttft_ms.p90) : "",
        q?.latency_ms?.p50 ? ms(q.latency_ms.p50) : "",
        q?.throughput?.p50 ? perSec(q.throughput.p50) + "/s" : "",
        fr == null ? "" : `<span class="${fr >= 10 ? "warn" : fr === 0 ? "muted" : ""}">${rateText(q.failed, q.requests)}</span>`,
      ],
    };
  });
  const cols = [
    { label: "", w: 16, cls: "ic" }, { label: "Account" }, { label: "Requests", w: 88, r: true }, { label: "First token", w: 96, r: true },
    { label: "p90", w: 80, r: true }, { label: "Full response", w: 112, r: true }, { label: "Throughput", w: 96, r: true }, { label: "Failure rate", w: 96, r: true },
  ];

  const html = `
    <div class="bar">${providerTabs("pfProv")}<span class="muted nowrap">${esc(readAt())}</span></div>
    <div class="body">
      ${stats}
      <div class="panes">${a.html}${b.html}</div>
      <div class="sec last">
        <div class="sech"><div class="t"><b>By account</b></div></div>
        ${table(cols, rows)}
      </div>
      <div class="pad-floater"></div>
    </div>
    <div class="floater"><button class="btn" data-range>${icon("calendar", 14)}<span>${esc(range.label)}</span>${icon("chevronDown", 12)}</button></div>`;

  return {
    html,
    mount(root) {
      const rerender = () => window.dispatchEvent(new Event("dash:render"));
      bindProviderTabs(root, "pfProv");
      root.querySelectorAll("[data-pmode]").forEach((btn) => { btn.onclick = () => { S.ui[btn.dataset.pmode + "Mode"] = btn.dataset.v; rerender(); }; });
      bindChart(root, "pfTtft", a.tip);
      bindChart(root, "pfTp", b.tip);
      root.querySelector("[data-range]").onclick = (e) => menu(e.currentTarget, RANGES.map((r) => ({
        a: r.label, on: r.id === range.id, run: () => { S.range = r.id; window.dispatchEvent(new Event("dash:refresh")); },
      })), { width: 200, alignLeft: true });
    },
  };
}

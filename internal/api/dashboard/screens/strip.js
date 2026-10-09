// The figure strip on Overview and Account: one figure per measure over the
// window, or over the selected range, and the chosen measure drawn below on
// the shared time controller. It loads its own window, the last 24 hours
// until zoomed or panned.
import { S, esc, fmt, int, ms, money, pctText, rateText, accounts, tokens, cacheReuse, apiCost, costKnown, figure, scopeParam, chartFormat, setChartFormat } from "../core.js";
import { accountColor, costNote, costTitle, legendHtml } from "./common.js";
import { accountLabels } from "./burn.js";
import { buildPanel, panelContext, snapGrid } from "./panels.js";
import { usageSum, perfCounts, loadRangePerf } from "./series.js";
import { DAY, timeState, bindTime, selectionLabel, backToNow, zoomHint } from "./timeaxis.js";

const METRICS = [
  { id: "requests", label: "Requests", panel: "requests" },
  { id: "tokens", label: "Tokens", panel: "tokens" },
  { id: "cost", label: "API cost", panel: "cost" },
  { id: "cache", label: "Cache reuse", panel: "cache" },
  { id: "ttft", label: "First token, median", panel: "ttft" },
  { id: "throughput", label: "Throughput", panel: "throughput" },
  { id: "failure", label: "Failure rate", panel: "failures" },
];
const FORMATS = { requests: true, tokens: true, cost: true, failure: true };

// The window the strip draws and loads, for the router.
export const stripTime = (key) => timeState(key, { defaultWindow: (now) => ({ start: now - DAY, end: now }), loads: true });

// key: names the strip's window, selection and chosen measure; sc: the accounts in view.
export function timeStrip({ key, sc }) {
  const ts = stripTime(key);
  const metrics = METRICS.filter((m) => m.id !== "cost" || costKnown());
  const metric = metrics.find((m) => m.id === S.ui[key + "Metric"]) || metrics[0];
  const ctx = panelContext(ts, sc, { hint: zoomHint() });
  const now = ctx.now;
  const from = ts.range ? ts.range.start : ts.window.start;
  const to = Math.min(ts.range ? ts.range.end : ts.window.end, now);
  const u = usageSum(ctx.usage, sc.ids, from, to, now).sum;
  const pc = perfCounts(ctx.perf, from, to);
  const q = ts.range ? ctx.rangePerf?.q : ctx.perf?.q;
  const values = {
    requests: int(u.requests),
    tokens: fmt(tokens(u)),
    cost: money(apiCost(u)),
    cache: pctText(cacheReuse(u)),
    ttft: q?.ttft_ms?.p50 ? ms(q.ttft_ms.p50) : "–",
    throughput: q?.throughput?.p50 ? String(Math.round(q.throughput.p50)) : "–",
    failure: pc.any ? rateText(pc.failed, pc.requests) : "–",
  };
  const figs = metrics.map((m) => figure(m.label, esc(values[m.id]), m.id === "throughput" && values.throughput !== "–" ? "tokens/s" : "", { metric: m.id, on: m === metric, title: m.id === "cost" ? costTitle() : "" })).join("");
  const format = chartFormat(key) === "bars" ? "bars" : "lines";
  const built = buildPanel(metric.panel, { format }, { ...ctx, labels: true });
  const nm = accountLabels();
  const shown = sc.ids.filter((id) => ctx.usage?.accounts?.[id]);
  const usageMetric = ["requests", "tokens", "cost"].includes(metric.id);
  const leg = !ts.range && usageMetric && format === "lines" && shown.length > 1 ? `<div class="legend">${legendHtml(shown.map((id) => ({ label: nm[id] || id, color: accountColor(id) })))}</div>` : "";
  const fmtToggle = FORMATS[metric.id] ? `<div class="po" role="group" aria-label="Chart format">${[["lines", "Lines"], ["bars", "Bars"]].map(([id, label]) => `<button class="${format === id ? "on" : ""}" data-strip-format="${id}" aria-pressed="${format === id}">${label}</button>`).join(`<span aria-hidden="true">·</span>`)}</div>` : "";
  const end = ts.range ? selectionLabel(ts.range, "data-strip-clear") : `${leg}${ts.moved ? backToNow("data-strip-home") : ""}`;
  const provs = accounts().filter((a) => sc.ids.includes(a.id)).map((a) => a.provider);
  const note = metric.id === "cost" ? `<div class="muted cnote">${esc(costNote(provs))}</div>` : "";
  const html = `<div class="figs tstrip" data-strip="${esc(key)}">
    <div class="figrow"><div class="figtabs">${figs}</div><div class="figend">${end}${fmtToggle}</div></div>
    <div class="tstrip-plot">${built.html}</div>${note}
  </div>`;

  const mount = (root) => {
    const box = root.querySelector(`[data-strip="${key}"]`);
    if (!box) return;
    box.querySelectorAll("[data-metric]").forEach((b) => {
      b.onclick = () => { S.ui[key + "Metric"] = b.dataset.metric; window.dispatchEvent(new Event("dash:render")); };
    });
    box.querySelectorAll("[data-strip-format]").forEach((b) => {
      b.onclick = () => { setChartFormat(key, b.dataset.stripFormat === "bars" ? "bars" : "line"); window.dispatchEvent(new Event("dash:render")); };
    });
    const clear = box.querySelector("[data-strip-clear]");
    if (clear) clear.onclick = () => ts.setRange(null);
    const home = box.querySelector("[data-strip-home]");
    if (home) home.onclick = () => ts.setWindow(ts.defaultWindow());
    bindTime(box, {
      key,
      window: ts.window,
      range: ts.range,
      grid: snapGrid(ctx),
      future: false,
      defaultWindow: ts.defaultWindow,
      setWindow: ts.setWindow,
      setRange: ts.setRange,
      probe: (t, el, hovered) => built.probe?.(t, el, hovered) || null,
    });
    if (ts.range) loadRangePerf(ts.range, sc.some ? scopeParam(sc.ids) : "");
  };
  return { html, mount };
}

// Panel types: one measure over time each. A panel draws its plot on the
// shared window, says what it shows in its header figure (for the window, or
// for the selected range), and answers the crosshair with a chip, dots and a
// hover card. Telemetry views stack them; Overview and Account use them too.
import {
  S, esc, fmt, int, ms, money, moneyAxis, pctText, rateText, clock, day, dayKey, isToday, logo, accounts, tokens, cacheReuse, apiCost,
  costKnown, providerTitle, perfCurrent, dataCurrent, scopeParam,
} from "../core.js";
import { accountColor } from "./common.js";
import { allowanceSeries, trajectory, projectedAt, projectionPoints, resetsUntil, readingAt, deadZones, accountLabels } from "./burn.js";
import { model as availModel, available } from "./timeline.js";
import { EMPTY, addC, usageData, displayBuckets, usageSum, bucketAt, bucketsIn, perfScope, perfCounts, perfAt, historyBefore, spanData, perfFigures, usageFigures, loadRangeData } from "./series.js";
import { HOUR, DAY, fracOf, plotWidth, addDays, midnight, timeTicks, endText, spanLabel, dateText, dayText } from "./timeaxis.js";
import { plot, lines, dot, bars, band, card, legend, yTop } from "./tplot.js";

const FORMAT = { key: "format", choices: [["lines", "Lines"], ["bars", "Bars"]], def: "lines" };
const WINDOW = { key: "window", choices: [["week", "Weekly"], ["5h", "5-hour"]], def: "week" };

// "Wed 7 Oct 12:00", "Mon 5 Oct" for a day bucket, "Wed 7 Oct 12:00 to 13:00" for an hour.
export function bucketName(t0, t1) {
  const name = (t) => (isToday(t) ? "Today" : dayText(t));
  if (t1 - t0 >= 23 * HOUR) return name(t0);
  return `${name(t0)} ${clock(t0)} to ${clock(t1)}`;
}
const instant = (t, now) => `${isToday(t) ? "Today" : dayText(t, now)} ${clock(t)}${t > now ? ", at this rate" : ""}`;

// ---------- the registry ----------

// title, options, forecast (draws after now), axis (on the shared time axis),
// needs (the data it reads), height (of its drawing area).
export const PANELS = {
  allowance: { title: "Allowance left", options: [WINDOW], forecast: true, needs: "allowance", height: 180 },
  available: { title: "Available accounts", forecast: true, needs: "allowance" },
  tokens: { title: "Tokens", options: [FORMAT], needs: "usage", height: 140 },
  cost: { title: "API cost", options: [FORMAT], needs: "usage", height: 104 },
  requests: { title: "Requests", options: [FORMAT], needs: "usage", height: 104 },
  output: { title: "Output", options: [FORMAT], needs: "usage", height: 104 },
  cache: { title: "Cache reuse", needs: "usage", height: 104 },
  ttft: { title: "First token", needs: "perf", height: 140 },
  latency: { title: "Full response", needs: "perf", height: 140 },
  throughput: { title: "Throughput", needs: "perf", height: 104 },
  failures: { title: "Failure rate", options: [{ ...FORMAT, def: "bars" }], needs: "perf", height: 84 },
  activity: { title: "Activity", axis: false, needs: "history" },
};
export const PANEL_ORDER = ["tokens", "cost", "requests", "activity", "allowance", "available", "cache", "output", "ttft", "latency", "throughput", "failures"];

// A panel's option value, its own or the default.
export function opt(type, options, key) {
  const def = PANELS[type]?.options?.find((o) => o.key === key);
  const v = options?.[key];
  return def && def.choices.some(([id]) => id === v) ? v : def?.def;
}

// ---------- panel frame ----------

// The header row and plot of one panel. idx is its place on the page, for options.
export function panelHtml(type, idx, built, options) {
  const def = PANELS[type];
  const opts = (def.options || []).map((o) => {
    const cur = opt(type, options, o.key);
    return o.choices.map(([id, label]) => `<button class="${cur === id ? "on" : ""}" data-opt="${idx}" data-key="${o.key}" data-val="${id}" aria-pressed="${cur === id}">${esc(label)}</button>`).join(`<span aria-hidden="true">·</span>`);
  }).join("");
  const right = built.legend ? legend(built.legend)
    : opts ? `<div class="po" role="group" aria-label="${esc(def.title)} options">${opts}</div>`
    : built.note ? `<span class="pnote">${esc(built.note)}</span>` : "";
  return `<section class="panel" data-panel-type="${type}" data-idx="${idx}">
    <div class="ph">
      <div class="pt"><b>${esc(def.title)}</b>${built.figure ? `<span class="pf">${esc(built.figure)}</span>` : ""}${built.qual ? `<span class="pq">${esc(built.qual)}</span>` : ""}</div>
      ${right}
    </div>
    ${built.html}
  </section>`;
}

// ---------- building ----------

const FIXED_TEXT = { "24h": "last 24 hours", "7d": "last 7 days", "30d": "last 30 days", "180d": "last 6 months", all: "all time" };

// What every panel on a page reads: the shared window and selection (a
// timeState), the picked accounts, and the loaded data for them.
export function panelContext(ts, sc, { forecast = false, hint = "" } = {}) {
  const ctx = {
    group: ts.key,
    window: ts.window,
    range: ts.range,
    ticks: timeTicks(ts.window, Math.max(3, Math.min(10, Math.floor(plotWidth() / 110)))),
    now: Date.now(),
    sc,
    scope: sc.some ? scopeParam(sc.ids) : "",
    usage: usageData(),
    perf: perfScope(sc),
    perfCurrent: perfCurrent(),
    loading: !dataCurrent(),
    forecast,
    hint,
  };
  // What every figure reads: the selection's own data, else the window's.
  ctx.span = spanData(ctx);
  ctx.perfExact = ctx.span.exact;
  ctx.perfLabel = perfLabel(ctx);
  return ctx;
}

// What the window's performance figures cover when it is not exactly the
// window: the fixed fallback range, or the window widened to whole buckets.
function perfLabel(ctx) {
  if (ctx.range || ctx.perfExact || !ctx.perf) return "";
  if (S.data?.summary?.performance?.range !== "custom") return FIXED_TEXT[S.dataRange] || "";
  const s = ctx.perf.series;
  if (!ctx.perfCurrent || !s.length) return "";
  const end = s[s.length - 1].t1;
  return spanLabel(s[0].t0, Math.min(end, ctx.now), { toNow: end > ctx.now });
}

// Loads what a page's selection needs, if it has one. Every page with a
// selection calls this when it mounts.
export const loadSelection = (ctx) => { if (ctx.range) loadRangeData(ctx.range, ctx.scope); };

// Bucket edges the selection snaps to: usage buckets, else performance buckets.
export function snapGrid(ctx) {
  const d = ctx.usage;
  if (d && d.starts.length > 1) return { bounds: [...d.starts, d.ends[d.ends.length - 1]] };
  const s = ctx.perf?.series || [];
  if (s.length > 1) return { bounds: [...s.map((b) => b.t0), s[s.length - 1].t1] };
  return { origin: 0, step: HOUR };
}

// ctx: panelContext plus labels (this panel draws the time labels).
export function buildPanel(type, options, ctx) {
  const f = BUILD[type];
  return f ? f(ctx, options || {}) : { html: "" };
}

const BUILD = {
  tokens: (ctx, o) => usagePanel(ctx, o, "tokens", { val: tokens, f: fmt, total: (v) => `${fmt(v)} tokens in total` }),
  cost: (ctx, o) => usagePanel(ctx, o, "cost", { val: apiCost, f: money, axis: moneyAxis, total: (v) => `${money(v)} in total at API prices` }),
  requests: (ctx, o) => usagePanel(ctx, o, "requests", { val: (b) => Number(b.requests) || 0, f: int, axis: fmt, total: (v) => `${int(v)} requests in total` }),
  output: (ctx, o) => usagePanel(ctx, o, "output", { val: (b) => Number(b.output_tokens) || 0, f: fmt, total: (v) => `${fmt(v)} output tokens in total` }),
  cache: cachePanel,
  ttft: (ctx) => percentilePanel(ctx, "ttft"),
  latency: (ctx) => percentilePanel(ctx, "latency"),
  throughput: throughputPanel,
  failures: failuresPanel,
  allowance: allowancePanel,
  available: availablePanel,
  activity: activityPanel,
};

const names = () => accountLabels();
const pastEnd = (ctx) => Math.min(ctx.window.end, ctx.now);

// The stretch a figure describes: the selection, else the window.
function span(ctx) {
  return ctx.range ? { from: ctx.range.start, to: ctx.range.end } : { from: ctx.window.start, to: ctx.window.end };
}

// What a no-future panel says when the selection lies wholly after now.
const futureOnly = (ctx) => (ctx.forecast ? "No data yet. Only allowance has a forecast" : "No data yet");

// ---------- usage measures ----------

function usagePanel(ctx, o, type, m) {
  const v = ctx.window, d = displayBuckets(ctx.usage, v), ids = ctx.sc.ids, nm = names();
  const format = opt(type, o, "format");
  const height = PANELS[type].height;
  const idx = bucketsIn(d, v.start, pastEnd(ctx));
  const at = (id, i) => d.accounts[id]?.[i];
  const valOf = (id, i) => (at(id, i) ? m.val(at(id, i)) : 0);
  const totalAt = (i) => ids.reduce((t, id) => t + valOf(id, i), 0);
  // A bucket's point sits at its middle, or at now while it is still filling.
  const mid = (i) => Math.min((d.starts[i] + d.ends[i]) / 2, ctx.now);
  let content = "", max = 1;
  if (idx.length) {
    if (format === "bars") {
      max = yTop(idx.map(totalAt));
      content = bars(idx.map((i) => ({ t0: d.starts[i], t1: Math.min(d.ends[i], Math.max(d.starts[i] + 1, ctx.now)), segs: ids.map((id) => ({ v: valOf(id, i), color: accountColor(id), acct: id })) })), v, max);
    } else {
      max = yTop(idx.flatMap((i) => ids.map((id) => valOf(id, i))));
      const shown = ids.filter((id) => idx.some((i) => at(id, i)));
      content = lines(shown.map((id) => ({ color: accountColor(id), acct: id, width: 1.7, pts: idx.map((i) => ({ t: mid(i), v: valOf(id, i) })) })), v, max, height);
      const last = idx[idx.length - 1];
      if (d.ends[last] >= ctx.now - HOUR) content += shown.map((id) => dot(mid(last), valOf(id, last), max, accountColor(id), v, "end", id)).join("");
    }
  }
  const empty = !idx.length ? `<div class="tp-empty">${ctx.loading ? "Loading" : "No usage in this window"}</div>` : "";
  const html = plot({ group: ctx.group, panel: type, window: v, ticks: ctx.ticks, height, labels: ctx.labels, hatch: true, y: { max, fmt: m.axis || m.f }, content: content + empty, over: ctx.hint || "" });
  const s = span(ctx);
  let figure = "–", qual = "";
  if (s.from >= ctx.now) { figure = ""; qual = futureOnly(ctx); }
  else {
    const sum = usageFigures(ctx.span, ids, ctx.now);
    if (sum?.any) {
      figure = m.f(m.val(sum.sum));
      if (!sum.covered && sum.first > s.from) qual = `since ${dateText(sum.first, ctx.now)}`;
    }
  }
  const probe = (t, el, hovered) => {
    if (t > ctx.now) return null;
    const i = bucketAt(d, t);
    if (i < 0) return null;
    const total = totalAt(i);
    const dots = format === "lines" ? ids.filter((id) => at(id, i)).map((id) => ({ y: valOf(id, i) / max, color: accountColor(id) })) : [];
    const rows = ids.filter((id) => valOf(id, i)).map((id) => ({ k: nm[id] || id, v: m.f(valOf(id, i)), color: accountColor(id) }));
    return { chip: m.f(total), dots, card: hovered ? card({ title: bucketName(d.starts[i], d.ends[i]), rows, foot: rows.length > 1 ? m.total(total) : "" }) : "" };
  };
  return { html, figure, qual, probe };
}

function cachePanel(ctx) {
  const v = ctx.window, d = displayBuckets(ctx.usage, v), ids = ctx.sc.ids, height = PANELS.cache.height;
  const idx = bucketsIn(d, v.start, pastEnd(ctx));
  const sumAt = (i) => ids.reduce((t, id) => addC(t, d.accounts[id]?.[i]), EMPTY());
  const mid = (i) => Math.min((d.starts[i] + d.ends[i]) / 2, ctx.now);
  const pts = idx.map((i) => ({ t: mid(i), v: cacheReuse(sumAt(i)) }));
  let content = lines([{ color: "var(--chart-1)", width: 1.5, bridge: true, pts }], v, 100, height);
  const lastPt = [...pts].reverse().find((p) => p.v != null);
  if (lastPt && lastPt.t >= ctx.now - 2 * HOUR) content += dot(lastPt.t, lastPt.v, 100, "var(--chart-1)", v);
  const html = plot({ group: ctx.group, panel: "cache", window: v, ticks: ctx.ticks, height, labels: ctx.labels, hatch: true, y: { max: 100, fmt: (x) => Math.round(x) + "%" }, content, over: ctx.hint || "" });
  const s = span(ctx);
  let figure = "–", qual = "of input read from cache";
  if (s.from >= ctx.now) { figure = ""; qual = futureOnly(ctx); }
  else { const sum = usageFigures(ctx.span, ids, ctx.now); if (sum?.any) figure = pctText(cacheReuse(sum.sum)); }
  const probe = (t, el, hovered) => {
    const i = t <= ctx.now ? bucketAt(d, t) : -1;
    if (i < 0) return null;
    const b = sumAt(i), r = cacheReuse(b);
    if (r == null) return { chip: "", card: hovered ? card({ title: bucketName(d.starts[i], d.ends[i]), foot: "No requests" }) : "" };
    return { chip: pctText(r), dots: [{ y: r / 100, color: "var(--chart-1)" }], card: hovered ? card({ title: bucketName(d.starts[i], d.ends[i]), rows: [{ k: "Read from cache", v: fmt(b.cache_read_tokens) }, { k: "Requests", v: int(b.requests) }] }) : "" };
  };
  return { html, figure, qual, probe };
}

// ---------- performance ----------

const PCT_LINES = {
  ttft: [["p50", "ttft_p50_ms", "var(--chart-p50)"], ["p90", "ttft_p90_ms", "var(--chart-p90)"], ["p99", "ttft_p99_ms", "var(--chart-p99)"]],
  latency: [["p50", "latency_p50_ms", "var(--chart-p50)"], ["p90", "latency_p90_ms", "var(--chart-p90)"], ["p99", "latency_p99_ms", "var(--chart-p99)"]],
};

// Percentiles for the figure: for the window as loaded (the qualifier names a
// fixed fallback range), or for the selection once its own reading arrives.
function perfFigure(ctx, pick) {
  const f = perfFigures(ctx.span, ctx.sc);
  return f?.q ? pick(f.q) : null;
}
const perfQual = (ctx, base) => base + (ctx.perfLabel ? `, ${ctx.perfLabel}` : "");

function perfPlot(ctx, type, series, max, yfmt, extra = "") {
  const html = plot({ group: ctx.group, panel: type, window: ctx.window, ticks: ctx.ticks, height: PANELS[type].height, labels: ctx.labels, hatch: true, y: { max, fmt: yfmt }, content: series + extra, over: ctx.hint || "" });
  return html;
}

function percentilePanel(ctx, type) {
  const v = ctx.window, ps = ctx.perf, height = PANELS[type].height;
  const list = (ps?.series || []).filter((b) => b.t1 > v.start && b.t0 < v.end);
  const keys = PCT_LINES[type].filter(([, k]) => list.some((b) => k in b));
  const max = yTop(list.flatMap((b) => keys.map(([, k]) => Number(b[k]) || 0)));
  const mid = (b) => Math.min((b.t0 + b.t1) / 2, ctx.now);
  const ser = keys.map(([, k, color]) => ({ color, width: 1.5, bridge: true, pts: list.map((b) => ({ t: mid(b), v: b.requests ? Number(b[k]) || null : null })) }));
  let dots = "";
  const last = [...list].reverse().find((b) => b.requests);
  if (last && last.t1 >= ctx.now - 2 * ps.step) for (const [, k, color] of keys) if (last[k]) dots += dot(mid(last), last[k], max, color, v);
  const html = perfPlot(ctx, type, lines(ser, v, max, height), max, (x) => (x ? ms(x) : "0"), dots);
  const field = type === "ttft" ? "ttft_ms" : "latency_ms";
  const pct = perfFigure(ctx, (q) => q?.[field] || null);
  const figure = pct?.p50 ? ms(pct.p50) : "–";
  const qual = perfQual(ctx, type === "latency" && pct?.p90 ? `median, p90 ${ms(pct.p90)}` : "median");
  const probe = (t, el, hovered) => {
    const b = t <= ctx.now ? perfAt(ps, t) : null;
    if (!b) return null;
    if (!b.requests) return { chip: "", card: hovered ? card({ title: bucketName(b.t0, b.t1), foot: "No requests" }) : "" };
    const rows = keys.filter(([, k]) => b[k]).map(([label, k, color]) => ({ k: label, v: ms(b[k]), color }));
    return { chip: ms(b[keys[0]?.[1]]), dots: keys.filter(([, k]) => b[k]).map(([, k, color]) => ({ y: b[k] / max, color })), card: hovered ? card({ title: bucketName(b.t0, b.t1), rows, foot: `${int(b.requests)} requests` }) : "" };
  };
  return { html, figure, qual, probe, legend: keys.map(([label, , color]) => ({ label, color })) };
}

const perSec = (x) => (x ? (x < 10 ? x.toFixed(1) : String(Math.round(x))) : "–");

function throughputPanel(ctx) {
  const v = ctx.window, ps = ctx.perf, height = PANELS.throughput.height;
  const list = (ps?.series || []).filter((b) => b.t1 > v.start && b.t0 < v.end);
  const max = yTop(list.map((b) => Number(b.throughput_p50) || 0));
  const mid = (b) => Math.min((b.t0 + b.t1) / 2, ctx.now);
  let content = lines([{ color: "var(--chart-1)", width: 1.5, bridge: true, pts: list.map((b) => ({ t: mid(b), v: b.requests ? Number(b.throughput_p50) || null : null })) }], v, max, height);
  const last = [...list].reverse().find((b) => b.requests && b.throughput_p50);
  if (last && last.t1 >= ctx.now - 2 * ps.step) content += dot(mid(last), last.throughput_p50, max, "var(--chart-1)", v);
  const html = perfPlot(ctx, "throughput", content, max, (x) => (x ? String(Math.round(x)) : "0"));
  const tp = perfFigure(ctx, (q) => q?.throughput || null);
  const figure = tp?.p50 ? perSec(tp.p50) + " tokens/s" : "–";
  const probe = (t, el, hovered) => {
    const b = t <= ctx.now ? perfAt(ps, t) : null;
    if (!b) return null;
    if (!b.requests || !b.throughput_p50) return { chip: "", card: hovered ? card({ title: bucketName(b.t0, b.t1), foot: "No requests" }) : "" };
    const rows = [{ k: "Median", v: perSec(b.throughput_p50) + " tokens/s", color: "var(--chart-1)" }, ...(b.throughput_p10 ? [{ k: "Slowest 10%", v: "under " + perSec(b.throughput_p10) }] : [])];
    return { chip: perSec(b.throughput_p50), dots: [{ y: b.throughput_p50 / max, color: "var(--chart-1)" }], card: hovered ? card({ title: bucketName(b.t0, b.t1), rows, foot: `${int(b.requests)} requests` }) : "" };
  };
  return { html, figure, qual: perfQual(ctx, "median"), probe };
}

function failuresPanel(ctx, o) {
  const v = ctx.window, ps = ctx.perf, height = PANELS.failures.height;
  const format = opt("failures", o, "format");
  const list = (ps?.series || []).filter((b) => b.t1 > v.start && b.t0 < v.end);
  const rateOf = (b) => (b.requests ? ((Number(b.failed) || 0) / b.requests) * 100 : null);
  const max = yTop(list.map((b) => rateOf(b) || 0));
  const color = "var(--chart-fail)";
  let content;
  if (format === "bars") content = bars(list.map((b) => ({ t0: b.t0, t1: Math.min(b.t1, Math.max(b.t0 + 1, ctx.now)), segs: [{ v: rateOf(b) || 0, color }] })), v, max);
  else content = lines([{ color, width: 1.5, bridge: true, pts: list.map((b) => ({ t: Math.min((b.t0 + b.t1) / 2, ctx.now), v: rateOf(b) })) }], v, max, height);
  const html = perfPlot(ctx, "failures", content, max, (x) => (x ? (x < 10 ? Number(x.toFixed(1)) : Math.round(x)) + "%" : "0"));
  const s = span(ctx);
  let figure = "–", qual = "";
  if (s.from >= ctx.now) { figure = ""; qual = futureOnly(ctx); }
  else {
    const c = perfFigures(ctx.span, ctx.sc);
    if (c?.any && !c.requests) qual = "No requests";
    else if (c?.any) {
      figure = rateText(c.failed, c.requests);
      qual = `${int(c.failed)} of ${int(c.requests)} requests${c.failovers != null ? `, ${int(c.failovers)} moved to another account` : ""}`;
    }
  }
  const probe = (t, el, hovered) => {
    const b = t <= ctx.now ? perfAt(ps, t) : null;
    if (!b) return null;
    if (!b.requests) return { chip: "", card: hovered ? card({ title: bucketName(b.t0, b.t1), foot: "No requests" }) : "" };
    const rows = [{ k: "Failed", v: int(b.failed), cls: b.failed ? "warn" : "" }, { k: "Requests", v: int(b.requests) }, ...("failovers" in b ? [{ k: "Moved to another account", v: int(b.failovers) }] : [])];
    return { chip: rateText(b.failed, b.requests), dots: format === "lines" ? [{ y: (rateOf(b) || 0) / max, color }] : [], card: hovered ? card({ title: bucketName(b.t0, b.t1), rows }) : "" };
  };
  return { html, figure, qual, probe };
}

// ---------- allowance ----------

// The accounts the allowance panel draws, with their meter and where it heads.
export function allowanceLines(ids, long, now) {
  const nm = names();
  return ids.map((id) => {
    const ser = allowanceSeries(id, long);
    if (!ser) return null;
    const tr = trajectory(ser, now);
    return { id, ser, tr, period: (Number(ser.window_seconds) || 0) * 1000, color: accountColor(id), label: nm[id] || id };
  }).filter(Boolean);
}

// Allowance left at t: the reading up to now, the projection after.
export const leftAt = (l, t, now) => (t <= now ? (t >= now - 60e3 ? l.tr.leftNow : readingAt(l.ser, t)) : projectedAt(l.tr, now, l.period, t));

// Stretches where a provider has nothing left on any account it can use, in time.
export function deadSpans(lines, long, v, now) {
  if (!lines.length) return [];
  const all = accounts();
  const providers = [...new Set(lines.map((l) => all.find((a) => a.id === l.id)?.provider).filter(Boolean))];
  const rawStep = (Number(lines[0].ser.step_seconds) || 600) * 1000;
  // Samples sit on a grid fixed in time, so a band's edges stay put as the
  // window moves instead of shifting with where it starts.
  const step = Math.ceil(Math.max(rawStep, (v.end - v.start) / 1500) / rawStep) * rawStep;
  const start = Math.floor(v.start / step) * step;
  const n = Math.max(2, Math.ceil((v.end - start) / step) + 1);
  return deadZones(providers, long, { now, start, step, n, nowX: (now - start) / step }).map((z) => ({ provider: z.provider, t0: Math.max(v.start, start + z.x0 * step), t1: start + z.x1 * step, open: z.open }));
}

// "Sun 09:00" for a reset in the coming week, else "Sun 11 Oct".
const resetName = (t, now) => (t - now < 6 * DAY ? day(t).split(" ")[0] + " " + clock(t) : dayText(t, now));

// The width of a reset label's text in the plot's 12px font: measured in
// the page, estimated a little wide elsewhere (tests).
let measure = null;
function textWidth(font) {
  if (typeof document === "undefined") return (s) => s.length * 7;
  measure ||= document.createElement("canvas").getContext("2d");
  if (!measure) return (s) => s.length * 7;
  measure.font = font;
  // Tabular digits run slightly wider than the canvas measures them.
  return (s) => Math.ceil(measure.measureText(s).width * 1.04) + 1;
}

// The reset row's layout, as app.css draws it: the 12px icon and a 4px gap
// before the text; a label starts 6px left of its reset, or, when that
// would cross the plot's right edge, ends 6px right of it (class "end").
export const MARK = { icon: 16, shift: 6, gap: 8 };
export function markBox(x, width, plotW) {
  const end = x - MARK.shift + width > plotW;
  return end ? { a: x + MARK.shift - width, b: x + MARK.shift, end } : { a: x - MARK.shift, b: x - MARK.shift + width, end };
}

// The reset row above the plot: one icon and label per reset, merged with
// its neighbour when their boxes would meet, as "Sun 09:00, 15:00". A label
// names its first few resets, then a count, and keeps at most 12 for its
// hover text, so the work grows with the number of resets, not its square.
function resetMarks(lines, v, now) {
  const w = plotWidth();
  const times = [...new Set(lines.flatMap((l) => resetsUntil(l.tr, now, v.end, l.period)).filter((t) => t >= v.start))].sort((a, b) => a - b);
  const font = typeof document === "undefined" ? "" : `12px ${getComputedStyle(document.body).fontFamily}`;
  const tw = textWidth(font);
  const SHOWN = 3, MAX_TITLES = 12;
  const named = (ts) => ts.map((t, i) => (i && dayKey(t) === dayKey(ts[i - 1]) ? clock(t) : resetName(t, now))).join(", ");
  const group = (x, first, n, text) => ({ x, first, n, text: text ?? named(first.slice(0, SHOWN)) });
  const labelOf = (g) => (g.n <= SHOWN ? g.text : `${g.text}, ${g.n - SHOWN} more`);
  const box = (g) => markBox(g.x, MARK.icon + tw(labelOf(g)), w);
  const merge = (p, g) => {
    if (p.first.length >= MAX_TITLES) return group(p.x, p.first, p.n + g.n, p.text);
    const first = p.first.concat(g.first).slice(0, MAX_TITLES);
    return group(p.x, first, p.n + g.n, p.first.length >= SHOWN ? p.text : undefined);
  };
  // Each reset starts a label; while it meets the label before it, the two
  // become one at the earlier reset, which can then meet the one before.
  const stack = [];
  for (const t of times) {
    let g = group(fracOf(v, t) * w, [t], 1);
    while (stack.length && box(stack[stack.length - 1]).b + MARK.gap > box(g).a) g = merge(stack.pop(), g);
    stack.push(g);
  }
  return stack.map((g) => {
    const text = labelOf(g);
    const listed = g.first.map((t) => `${dayText(t, now)} ${clock(t)}`).join(", ");
    const titles = listed + (g.n > MAX_TITLES ? `, and ${g.n - MAX_TITLES} more` : "");
    return `<span class="${box(g).end ? "end" : ""}" style="left:${(g.x / w * 100).toFixed(3)}%" title="Resets to 100%: ${esc(titles)}"><svg width="12" height="12" viewBox="0 0 24 24" aria-hidden="true"><path d="M16.023 9.348h4.992v-.001M2.985 19.644v-4.992m0 0h4.992m-4.993 0 3.181 3.183a8.25 8.25 0 0 0 13.803-3.7M4.031 9.865a8.25 8.25 0 0 1 13.803-3.7l3.181 3.182m0-4.991v4.99" fill="none" stroke="var(--muted-fg)" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg><b>${esc(text)}</b></span>`;
  }).join("");
}

// The projection drawn: no vertical steps at resets and nothing along zero,
// full strength until the account's second reset, faded after it.
export function projectionParts(l, now, end) {
  const pts = projectionPoints(l.tr, now, end, l.period, (t) => t);
  const second = resetsUntil(l.tr, now, end, l.period)[1] ?? Infinity;
  const strong = [], faded = [], resets = [];
  let prev = null;
  for (const p of pts) {
    if (p.marker || (prev && p.x === prev.x && p.v === 100)) resets.push(p.x);
    const flat = prev && prev.v === 0 && p.v === 0;
    const step = prev && p.x === prev.x;
    const target = p.x > second || (prev && prev.x >= second) ? faded : strong;
    if (!prev || flat || step || p.move) target.push(null);
    if (prev && target === faded && strong.length && strong[strong.length - 1] && prev.x <= second && p.x > second && !flat && !step && !p.move) {
      // Split the stretch that crosses the second reset so both halves meet there.
      const vAt = prev.v + ((p.v - prev.v) * (second - prev.x)) / (p.x - prev.x);
      strong.push({ t: second, v: vAt });
      faded.push(null, { t: second, v: vAt });
    }
    if (!(flat && p.v === 0)) target.push({ t: p.x, v: p.v });
    prev = { x: p.x, v: p.v };
  }
  return { strong, faded, resets: [...new Set(resets)] };
}

// Readings as drawn: a refill starts a new stretch instead of a vertical
// line, and time spent used up is left out instead of drawn along zero.
export function historyLine(pts) {
  const out = [];
  let prev = null;
  for (const p of pts) {
    if (!p) { out.push(null); prev = null; continue; }
    if (prev && prev.v <= 0 && p.v <= 0) { prev = p; continue; }
    if (prev && prev.v <= 0 && p.v > 0) out.push(null);
    out.push(prev && p.v - prev.v > 20 ? { ...p, move: true } : p);
    prev = p;
  }
  return out;
}

function allowancePanel(ctx, o) {
  const v = ctx.window, now = ctx.now, height = PANELS.allowance.height;
  const long = opt("allowance", o, "window") === "week";
  const ls = allowanceLines(ctx.sc.ids, long, now);
  if (!ls.length) {
    const none = ctx.sc.ids.length ? "No allowance readings yet. They appear once an account is used." : "No accounts selected.";
    return { html: plot({ group: ctx.group, panel: "allowance", window: v, ticks: ctx.ticks, height, labels: ctx.labels, y: { max: 100, fmt: (x) => (x ? Math.round(x) + "%" : "0") }, content: `<div class="tp-empty">${esc(none)}</div>`, marks: "" }), figure: "", qual: "", probe: () => null };
  }
  const zones = deadSpans(ls, long, v, now);
  let content = zones.map((z) => band(z.t0, z.t1, v, "dead", `No ${providerTitle(z.provider)} left${z.open ? "" : " until " + resetName(z.t1, now)}`)).join("");
  const sampleStep = Math.max((Number(ls[0].ser.step_seconds) || 600) * 1000, (v.end - v.start) / 1200);
  const series = [];
  let dots = "";
  for (const l of ls) {
    const pts = [];
    const s0 = Date.parse(l.ser.start) || v.start;
    for (let t = Math.max(v.start - sampleStep, s0 - (s0 - v.start) % sampleStep); t < Math.min(now, v.end + sampleStep); t += sampleStep) {
      const r = t >= s0 ? readingAt(l.ser, t) : null;
      pts.push(r == null ? null : { t, v: r });
    }
    pts.push({ t: now, v: l.tr.leftNow });
    series.push({ color: l.color, acct: l.id, width: 1.7, pts: historyLine(pts) });
    if (v.end > now) {
      const pp = projectionParts(l, now, v.end);
      series.push({ color: l.color, acct: l.id, width: 1.5, dash: "5 4", pts: pp.strong });
      if (pp.faded.some(Boolean)) series.push({ color: l.color, acct: l.id, width: 1.5, dash: "5 4", opacity: 0.4, pts: pp.faded });
      // One reset dot per few pixels: zoomed out to months, five-hour resets
      // fall closer than a pixel apart and would add thousands of elements.
      const gap = 6 / Math.max(1, plotWidth()), shown = [];
      for (const t of pp.resets) {
        if (t < v.start || t > v.end) continue;
        if (shown.length && fracOf(v, t) - fracOf(v, shown[shown.length - 1]) < gap) continue;
        shown.push(t);
      }
      dots += shown.map((t) => dot(t, 100, 100, l.color, v, "pt", l.id)).join("");
    }
    if (now >= v.start && now <= v.end) dots += dot(now, l.tr.leftNow, 100, l.color, v, "end", l.id);
  }
  content += lines(series, v, 100, height) + dots;
  const html = plot({ group: ctx.group, panel: "allowance", window: v, ticks: ctx.ticks, height, labels: ctx.labels, y: { max: 100, fmt: (x) => (x ? Math.round(x) + "%" : "0") }, marks: v.end > now ? resetMarks(ls, v, now) : "", content, over: ctx.hint || "" });
  const all = accounts();
  const provOf = (id) => all.find((a) => a.id === id)?.provider;
  let figure, qual;
  if (ctx.range) {
    // The selection's own end, wherever the window is: a pan or zoom must
    // not change what the figure says about it.
    const t = ctx.range.end;
    const have = ls.filter((l) => (leftAt(l, t, now) ?? 0) > 0).length;
    figure = `${have} of ${ls.length}`;
    qual = `have allowance by ${endText(ctx.range.end, now)}`;
  } else {
    const used = ls.filter((l) => l.tr.usedUp);
    if (used.length) {
      const provs = [...new Set(used.map((l) => provOf(l.id)))];
      figure = `${used.length} of ${ls.length}`;
      qual = provs.length === 1 ? `${providerTitle(provs[0])} used up` : "used up";
    } else { figure = `${ls.length} of ${ls.length}`; qual = "have allowance"; }
  }
  const probe = (t, el, hovered) => {
    const vals = ls.map((l) => ({ l, val: leftAt(l, t, now) })).filter((x) => x.val != null);
    if (!vals.length) return null;
    const dead = zones.find((z) => t >= z.t0 && (t < z.t1 || (z.open && t <= z.t1)));
    const warn = dead ? `No ${providerTitle(dead.provider)} allowance left${dead.open ? "" : " until " + resetName(dead.t1, now)}` : "";
    const top = Math.max(...vals.map((x) => x.val));
    return {
      chip: Math.round(top) + "%",
      dots: vals.map((x) => ({ y: x.val / 100, color: x.l.color })),
      card: hovered ? card({ title: instant(t, now), warn, rows: vals.map((x) => ({ k: x.l.label, v: Math.round(x.val) + "% left", color: x.l.color })) }) : "",
      cardTop: 32,
    };
  };
  return { html, figure, qual, probe };
}

// ---------- available accounts ----------

// Each provider's accounts that could take a session over the window, in
// stretches of equal count, from the same model as the Overview timeline.
// until: a later time at() must answer too, such as the end of a selection
// that a zoom left outside the window.
export function availability(ids, v, now, until = 0) {
  // The model runs a little past now even when the window ends at now, so
  // "now" itself always has an answer.
  const fr = { start: v.start, end: Math.max(v.end, now + HOUR, until + 1) };
  const picked = accounts().filter((a) => ids.includes(a.id));
  return ["claude", "codex"].map((provider) => {
    const list = picked.filter((a) => a.provider === provider);
    if (!list.length) return null;
    const models = list.map((a) => availModel(a, now, fr));
    const step = Math.max(10 * 60e3, (v.end - v.start) / 600);
    const cuts = [];
    for (let t = v.start; t < v.end; t += step) cuts.push(t);
    if (now > v.start && now < v.end) cuts.push(now);
    cuts.sort((a, b) => a - b);
    const segs = [];
    cuts.forEach((t, i) => {
      const to = Math.min(v.end, cuts[i + 1] ?? v.end);
      const n = models.filter((m) => available(m, Math.min(v.end - 1, (t + to) / 2), now)).length;
      const last = segs[segs.length - 1];
      if (last && last.n === n) last.to = to;
      else segs.push({ from: t, to, n });
    });
    const at = (t) => models.filter((m) => available(m, t, now)).length;
    return { provider, total: list.length, segs, at };
  }).filter(Boolean);
}

function availablePanel(ctx) {
  const v = ctx.window, now = ctx.now;
  const rows = availability(ctx.sc.ids, v, now, ctx.range?.end || 0);
  const height = Math.max(32, rows.length * 24 + 8);
  const content = rows.map((r, k) => r.segs.map((s, i) => {
    const a = Math.max(0, fracOf(v, s.from)), b = Math.min(1, fracOf(v, s.to));
    if (b <= a) return "";
    const cls = s.n === 0 ? "none" : "";
    // A stretch with none says until when, while it has room.
    const back = s.n === 0 && r.segs[i + 1] ? s.to : 0;
    const px = (b - a) * plotWidth();
    const why = back && px > 240 ? `, no ${providerTitle(r.provider)} allowance until ${resetName(back, now)}` : "";
    // A segment too narrow for its label shows none rather than a cut-off one;
    // the crosshair still reads it.
    const fits = px >= (s.n === 0 ? 60 : (String(s.n).length + String(r.total).length + 4) * 7 + 10);
    const label = !fits ? "" : s.n === 0 ?`<svg width="12" height="12" viewBox="0 0 16 16" aria-hidden="true"><path fill-rule="evenodd" clip-rule="evenodd" d="M6.701 2.25c.577-1 2.02-1 2.598 0l5.196 9a1.5 1.5 0 0 1-1.299 2.25H2.804a1.5 1.5 0 0 1-1.3-2.25l5.197-9ZM8 4a.75.75 0 0 1 .75.75v3a.75.75 0 1 1-1.5 0v-3A.75.75 0 0 1 8 4Zm0 8a1 1 0 1 0 0-2 1 1 0 0 0 0 2Z" fill="currentColor"/></svg><b>None${esc(why)}</b>` : `<b>${s.n} of ${r.total}</b>`;
    return `<div class="av-seg ${cls}" style="top:${6 + k * 24}px;left:${(a * 100).toFixed(3)}%;width:${((b - a) * 100).toFixed(3)}%">${label ? `<span class="av-lbl" data-chip-avoid>${label}</span>` : ""}</div>`;
  }).join("")).join("");
  const logos = `<div class="av-logos">${rows.map((r, k) => `<span style="top:${9 + k * 24}px">${logo(r.provider)}</span>`).join("")}</div>`;
  const html = plot({ group: ctx.group, panel: "available", window: v, ticks: ctx.ticks, height, labels: ctx.labels, y: null, content, over: (ctx.hint || "") }).replace('<div class="tp"', `<div class="tp av"`).replace('<div class="tp-area"', `${logos}<div class="tp-area"`);
  const total = rows.reduce((t, r) => t + r.total, 0);
  const countAt = (t) => rows.reduce((n, r) => n + r.at(t), 0);
  let figure, qual = "";
  if (ctx.range) {
    const t = ctx.range.end;
    figure = `${countAt(t)} of ${total}`;
    qual = t > now ? `by ${dateText(t, now)}, forecast` : `at ${dateText(t, now)} ${clock(t)}`;
  } else {
    figure = `${countAt(now)} of ${total}${v.end > now ? " now" : ""}`;
    const dead = rows.filter((r) => r.at(now) === 0);
    const live = rows.filter((r) => r.at(now) > 0);
    if (dead.length && live.length) {
      const back = dead.map((r) => r.segs.find((s) => s.from >= now && s.n > 0)?.from).filter(Boolean);
      const until = back.length ? Math.min(...back) : 0;
      qual = `${live.map((r) => providerTitle(r.provider)).join(" and ")} only${until ? " until " + resetName(until, now) : ""}`;
    } else if (!live.length && rows.length) qual = "None available";
  }
  const probe = (t, el, hovered) => {
    const n = countAt(t);
    return {
      chip: n === 0 ? "None" : `${n} of ${total}`,
      chipTop: 6,
      card: hovered ? card({ title: instant(t, now), rows: rows.map((r) => ({ lead: `<span class="lg">${logo(r.provider)}</span>`, k: providerTitle(r.provider), v: r.at(t) === 0 ? "None" : `${r.at(t)} of ${r.total}`, cls: r.at(t) === 0 ? "err" : "" })) }) : "",
      cardTop: 0,
    };
  };
  return { html, figure, qual, probe };
}

// ---------- activity ----------

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
const parseKey = (k) => { const [y, m, d] = k.split("-").map(Number); return new Date(Date.UTC(y, m - 1, d)); };

// Token totals per day from the history (provider totals) for the picked
// providers; with some accounts picked, the days the per-account counters know.
function activityDays(sc, now) {
  const map = new Map();
  if (!sc.some) {
    for (const d of S.data?.summary?.history?.days || []) {
      const v = EMPTY();
      for (const [p, x] of Object.entries(d.providers || {})) if (sc.prov === "all" || p === sc.prov) addC(v, x);
      map.set(d.date, v);
    }
    return { map, known: () => true };
  }
  // Picked accounts: only per-account data. Days are known from the last 14
  // daily counters, and from usage buckets of a day or less for the whole
  // days they cover.
  const accts = S.data?.summary?.accounts || {};
  const covered = new Set();
  for (const a of Object.values(accts)) for (const d of a?.daily || []) if (d.date) covered.add(d.date);
  for (const k of covered) map.set(k, EMPTY());
  for (const id of sc.ids) for (const d of accts[id]?.daily || []) if (d.date) addC(map.get(d.date), d);
  const u = usageData();
  if (u && u.step <= DAY) {
    const byDay = new Map();
    u.starts.forEach((t, i) => {
      const k = dayKey(t);
      const v = byDay.get(k) || { v: EMPTY(), from: t, to: t };
      for (const id of sc.ids) addC(v.v, u.accounts[id]?.[i]);
      v.to = u.ends[i];
      byDay.set(k, v);
    });
    for (const [k, x] of byDay) if (x.from <= midnight(k) && x.to >= Math.min(midnight(addDays(k, 1)), now)) map.set(k, x.v);
  }
  return { map, known: (k) => map.has(k) };
}

// Totals for picked accounts from day k0 through today, or null unless
// every one of those days is known: a total must not quietly leave days out.
function knownSince(map, k0, today) {
  const out = EMPTY();
  for (let k = k0; k <= today; k = addDays(k, 1)) {
    if (!map.has(k)) return null;
    addC(out, map.get(k));
  }
  return out;
}

// Exported for tests: the outline around a set of grid cells as one SVG path.
// cells: Set of "w,d" keys; pitch: px between cells; pad: px outside them.
export function cellOutline(cells, pitch = 22, pad = 2) {
  const has = (w, d) => cells.has(w + "," + d);
  const edges = new Map();
  const add = (x0, y0, x1, y1) => edges.set(x0 + "," + y0, [x1, y1]);
  for (const key of cells) {
    const [w, d] = key.split(",").map(Number);
    if (!has(w, d - 1)) add(w, d, w + 1, d);
    if (!has(w + 1, d)) add(w + 1, d, w + 1, d + 1);
    if (!has(w, d + 1)) add(w + 1, d + 1, w, d + 1);
    if (!has(w - 1, d)) add(w, d + 1, w, d);
  }
  const P = (g) => g * pitch - pad;
  let path = "";
  while (edges.size) {
    const [startKey] = edges.keys();
    let [x, y] = startKey.split(",").map(Number);
    path += `M${P(x)} ${P(y)}`;
    for (let guard = 0; guard < 4000; guard++) {
      const k = x + "," + y;
      const next = edges.get(k);
      if (!next) break;
      edges.delete(k);
      [x, y] = next;
      path += ` L${P(x)} ${P(y)}`;
    }
    path += " Z";
  }
  return path;
}

// The window a click on a day opens: that day, midnight to midnight. On a
// page without forecasts today runs from midnight to now (toNow, see
// timeState), even in its first hour: moving the day back to end at now, or
// stretching it to the shortest zoom, would show part of yesterday.
export function dayWindow(k, now, future = false) {
  const start = midnight(k), end = midnight(addDays(k, 1));
  return !future && end > now ? { start, end, toNow: true } : { start, end };
}

function activityPanel(ctx) {
  const sc = ctx.sc;
  const { map, known } = activityDays(sc, ctx.now);
  const today = dayKey(ctx.now);
  const t = parseKey(today);
  const monday = new Date(t); monday.setUTCDate(t.getUTCDate() - ((t.getUTCDay() + 6) % 7));
  const start = new Date(monday); start.setUTCDate(monday.getUTCDate() - 52 * 7);
  const cols = [];
  for (let w = 0; w < 53; w++) {
    const days = [];
    for (let d = 0; d < 7; d++) {
      const dt = new Date(start); dt.setUTCDate(start.getUTCDate() + w * 7 + d);
      const k = dt.toISOString().slice(0, 10);
      days.push({ k, v: map.get(k) || null, future: k > today, unknown: k <= today && !known(k) });
    }
    cols.push(days);
  }
  // The grid starts at the week usage begins, so a short history is not
  // mostly empty weeks. At least 13 weeks show.
  const firstWeek = cols.findIndex((w) => w.some((d) => d.v && tokens(d.v) > 0));
  cols.splice(0, Math.min(firstWeek < 0 ? cols.length : firstWeek, cols.length - 13));
  const vals = cols.flat().map((d) => (d.v ? tokens(d.v) : 0)).filter((x) => x > 0).sort((a, b) => a - b);
  const q = (p) => vals[Math.min(vals.length - 1, Math.floor(p * vals.length))] || 0;
  const cuts = [q(0.2), q(0.4), q(0.6), q(0.8)];
  const lv = (x) => (!x ? 0 : x <= cuts[0] ? 1 : x <= cuts[1] ? 2 : x <= cuts[2] ? 3 : x <= cuts[3] ? 4 : 5);
  // The shared window's days, outlined on the grid.
  const v = ctx.window;
  const inWin = new Set();
  cols.forEach((w, wi) => w.forEach((d, di) => {
    if (d.future) return;
    const a = midnight(d.k), b = midnight(addDays(d.k, 1));
    if (b > v.start && a < Math.min(v.end, ctx.now + 1)) inWin.add(wi + "," + di);
  }));
  const outline = inWin.size ? `<svg class="act-outline" aria-hidden="true"><path d="${cellOutline(inWin)}"/></svg>` : "";
  const grid = cols.map((w, wi) => `<div class="wk">${w.map((d, di) => `<button class="${d.future ? "future" : d.unknown ? "na" : "l" + lv(d.v ? tokens(d.v) : 0)}" data-w="${wi}" data-d="${di}" ${d.future ? "disabled tabindex=\"-1\"" : ""} aria-label="${esc(d.k)}"></button>`).join("")}</div>`).join("");
  let lastMonth = "", lastAt = -9;
  const months = cols.map((w, wi) => {
    const m = MONTHS[parseKey(w[w.length - 1].k).getUTCMonth()];
    if (m === lastMonth) return "";
    lastMonth = m;
    if (wi > cols.length - 2 || wi - lastAt < 3) return "";
    lastAt = wi;
    return `<span style="left:${wi * 22}px">${m}</span>`;
  }).join("");
  // Today, this week, this month and lifetime, as the history reports them.
  const h = S.data?.summary?.history || {};
  const sumFrom = (k0) => { const out = EMPTY(); for (const [k, x] of map) if (k >= k0) addC(out, x); return out; };
  const weekKey = monday.toISOString().slice(0, 10), monthKey = today.slice(0, 8) + "01";
  // Every account: the history's own totals, else the sum of its days. Picked
  // accounts: a total only when every day in it is known, and no lifetime.
  const pick = (x, k0) => {
    if (!sc.some) return sc.prov === "all" && x ? { ...EMPTY(), ...x } : sumFrom(k0);
    return k0 ? knownSince(map, k0, today) : null;
  };
  const sums = [["Today", pick(h.today, today)], ["This week", pick(h.this_week, weekKey)], ["This month", pick(h.this_month, monthKey)], ["Lifetime", pick(h.lifetime, "")]];
  const cost = costKnown();
  const stat = ([label, x]) => `<div class="act-stat"><span class="muted">${label}</span><b>${x ? fmt(tokens(x)) : "–"}</b>${x ? (cost ? `<span class="muted">${esc(money(apiCost(x)))} at API prices</span>` : "") : sc.some ? `<span class="muted">Not kept per account</span>` : ""}</div>`;
  const firstDay = [...map.entries()].filter(([, x]) => tokens(x) > 0).map(([k]) => k).sort()[0];
  const life = sums[3][1];
  const html = `<div class="act">
    <div class="act-grid"><div class="act-scroll" data-act-scroll><div class="act-cells">${grid}${outline}</div><div class="act-months">${months}</div></div></div>
    <div class="act-stats">${sums.map(stat).join("")}</div>
  </div>`;
  const dayCard = (d) => {
    const x = d.v || EMPTY();
    if (d.unknown) return card({ title: longDay(d.k), foot: "No daily data for the picked accounts" });
    const rows = [["Read from cache", x.cache_read_tokens], ["Written to cache", x.cache_write_tokens], ["Output", x.output_tokens], ["New input", x.input_tokens]].map(([k, n]) => ({ k, v: fmt(n) }));
    return card({ title: longDay(d.k), rows, foot: `${fmt(tokens(x))} tokens${cost ? `, ${money(apiCost(x))} at API prices` : ""}` });
  };
  const mount = (root, setWindow) => {
    const sec = root.querySelector('[data-panel-type="activity"]');
    if (!sec) return;
    const scroll = sec.querySelector("[data-act-scroll]");
    const outlineEl = sec.querySelector(".act-outline");
    if (outlineEl) { outlineEl.setAttribute("width", String(cols.length * 22)); outlineEl.setAttribute("height", String(7 * 22)); }
    // Show the outlined window, else this week.
    const first = [...inWin].map((k) => Number(k.split(",")[0])).sort((a, b) => a - b)[0];
    scroll.scrollLeft = first != null && first * 22 < scroll.scrollWidth - scroll.clientWidth ? Math.max(0, first * 22 - scroll.clientWidth / 2) : scroll.scrollWidth;
    let tip = null, held = false;
    const clear = () => { tip?.remove(); tip = null; if (held) { S.hold = Math.max(0, S.hold - 1); held = false; } };
    sec.querySelectorAll(".act-cells button[data-w]").forEach((b) => {
      const d = cols[Number(b.dataset.w)][Number(b.dataset.d)];
      b.onclick = () => { clear(); setWindow(dayWindow(d.k, Date.now(), ctx.forecast)); };
      b.onmouseenter = () => {
        if (!held) { S.hold++; held = true; }
        if (!tip) { tip = document.createElement("div"); tip.className = "tcard"; sec.querySelector(".act-grid").appendChild(tip); }
        tip.innerHTML = dayCard(d);
        const gr = sec.querySelector(".act-grid").getBoundingClientRect(), cr = b.getBoundingClientRect();
        tip.style.left = Math.max(0, Math.min(cr.left - gr.left - 140 + 9, gr.width - 280)) + "px";
        let y = cr.top - gr.top - tip.offsetHeight - 8;
        if (y < 0) y = cr.bottom - gr.top + 8;
        tip.style.top = y + "px";
      };
    });
    sec.querySelector(".act-grid").addEventListener("mouseleave", clear);
  };
  return { html, figure: life ? fmt(tokens(life)) : "–", qual: firstDay ? `since ${Number(firstDay.slice(8))} ${MONTHS[Number(firstDay.slice(5, 7)) - 1]}` : "", note: "Past year, click a day to jump to it", mount, probe: () => null };
}

const longDay = (k) => parseKey(k).toLocaleDateString("en-GB", { timeZone: "UTC", weekday: "short", day: "numeric", month: "short" }).replace(",", "");

// Sums over a stretch for the tables and figure cards, from the same buckets.
export { usageSum, perfCounts, historyBefore };

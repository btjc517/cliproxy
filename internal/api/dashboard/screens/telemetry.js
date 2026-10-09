// Telemetry: one page of stacked panels for the open view. Every panel shares
// the account picker's selection, one time window, one crosshair and one
// range selection; the table under them sums the same buckets per account.
import {
  S, esc, fmt, int, ms, money, clock, day, icon, logo, tokens, cacheReuse, apiCost,
  providerTitle, scopeParam, accountScope, setAccountSelection, warnState,
} from "../core.js";
import { accountColor, readAt } from "./common.js";
import { accountPicker, bindAccountPicker } from "./account-picker.js";
import { pctRate, allowanceRange, resetsUntil } from "./burn.js";
import { PANELS, panelHtml, buildPanel, panelContext, snapGrid, allowanceLines, leftAt, availability } from "./panels.js";
import { usageSum, perfScope, perfCounts, historyBefore, loadRangePerf, RP } from "./series.js";
import { DAY, timeState, forgetTime, bindTime, windowText, selectionLabel, backToNow, zoomHint, endText } from "./timeaxis.js";
import { V, findView, defaultId, sameView, viewWindow, hasForecast, loadViews } from "./views.js";
import { openDisplay, displayOpen, startRename, renameBox, mountRename, setDefaultView, saveDraft, newViewDialog } from "./viewmenus.js";

const KEY = "tv";
const copy = (v) => JSON.parse(JSON.stringify(v));
const rerender = () => window.dispatchEvent(new Event("dash:render"));

// ---------- the open view and its unsaved changes ----------

// The saved view, the view as edited (the draft) and whether they differ.
export function current(id) {
  const saved = findView(V.store, id);
  if (!saved) return null;
  const drafts = (S.ui.drafts ||= {});
  const draft = drafts[id];
  if (draft && sameView(draft, saved) && draft.name === saved.name) delete drafts[id];
  const view = drafts[id] || saved;
  return { saved, view, dirty: !!drafts[id] };
}

// Changes the open view without saving it. change gets a copy to edit.
export function edit(id, change) {
  const c = current(id);
  if (!c) return;
  const next = copy(c.view);
  change(next);
  S.ui.drafts[id] = next;
  if (sameView(next, c.saved) && next.name === c.saved.name) delete S.ui.drafts[id];
  rerender();
}
export const dropDraft = (id) => { if (S.ui.drafts) delete S.ui.drafts[id]; };

// The shared window and selection for a view. Opening another view starts
// it on its own default window with nothing selected, and applies the
// account selection a view was saved with.
export function viewTime(view) {
  if (S.ui.tvFor !== view.id) {
    forgetTime(KEY);
    S.ui.tvFor = view.id;
    if (Array.isArray(view.accounts)) setAccountSelection(view.accounts);
  }
  return timeState(KEY, { defaultWindow: (now) => viewWindow(view.window, now), future: hasForecast(view), loads: true });
}

// The windows this page loads, for the router.
export function wants(params) {
  const c = current(params[0]);
  if (!c) return null;
  const w = viewTime(c.view).window;
  return { usage: w, perf: w };
}

// ---------- the table ----------

const resetName = (t, now) => (t - now < 6 * DAY ? `${day(t).split(" ")[0]} ${clock(t)}` : `${day(t)} ${clock(t)}`);
const ALLOWANCE_COLS = ["weekLeft", "perDay", "heading", "nextReset"];
const USAGE_COLS = ["tokens", "requests", "input", "cacheWrite", "cacheRead", "output", "cost", "cacheReuse"];
const PERF_COLS = ["requests", "ttft50", "ttft90", "throughput", "failures", "failovers"];

// Column labels and widths from the Paper boards. r: right-aligned.
export const COLUMNS = {
  weekLeft: { label: "Week left", w: 120 },
  perDay: { label: "Use per day", w: 140 },
  heading: { label: "Heading for", w: 270 },
  nextReset: { label: "Next reset", w: 240 },
  tokens: { label: "Tokens", w: 100, r: true },
  requests: { label: "Requests", w: 90, r: true },
  input: { label: "New input", w: 104, r: true },
  cacheWrite: { label: "Written to cache", w: 126, r: true },
  cacheRead: { label: "Read from cache", w: 126, r: true },
  output: { label: "Output", w: 92, r: true },
  cost: { label: "API cost", w: 114, r: true, cls: "pr16" },
  cacheReuse: { label: "Cache reuse", w: 106 },
  ttft50: { label: "First token p50", w: 130, r: true },
  ttft90: { label: "First token p90", w: 130, r: true },
  throughput: { label: "Throughput", w: 140, r: true },
  failures: { label: "Failures", w: 110, r: true },
  failovers: { label: "Failovers", w: 118, r: true },
};
const SEL_COLS = { leftAt: { w: 120 }, usedIn: { label: "Used in range", w: 140 }, resetsIn: { label: "Resets in range", w: 510 } };

const cell = (w, html, r = false, cls = "") => `<div class="c ${r ? "r" : ""} ${cls}" style="width:${w}px">${html}</div>`;
const meter = (pct, color, cls = "") => `<span class="pctcell"><span class="meter ${cls}"><i style="width:${Math.max(0, Math.min(100, pct))}%;background:${color}"></i></span><span class="${pct ? "" : "muted"}">${Math.round(pct)}%</span></span>`;
const reuseCell = (x) => { const r = cacheReuse(x); return r == null ? `<span class="muted">–</span>` : `<span class="pctcell"><span class="meter reuse"><i style="width:${Math.round(r)}%"></i></span><span class="muted">${Math.round(r)}%</span></span>`; };
const perSec = (x) => (x ? (x < 10 ? x.toFixed(1) : String(Math.round(x))) + " tokens/s" : "–");

const fullTime = (t) => `${day(t)}, ${clock(t)}`;

// Where an account's week is heading, and under it what follows: after a
// used-up week, how the next one goes at the same rate.
function headingFor(l, now) {
  const tr = l.tr;
  if (tr.rate == null && !tr.usedUp) return `<span class="muted">Too few readings yet</span>`;
  let main, sub = "";
  if (tr.usedUp) main = warnState("Used up");
  else if (tr.runsOut) main = warnState("Runs out " + fullTime(tr.runsOut));
  else main = "Lasts to reset";
  if (tr.usedUp && tr.reset > now && tr.rate > 0 && l.period) {
    const out = tr.reset + (100 / tr.rate) * 3600e3;
    sub = out < tr.reset + l.period ? "Runs out " + fullTime(out) : `${Math.max(0, Math.round(100 - tr.rate * (l.period / 3600e3)))}% left at next reset`;
  } else if (!tr.usedUp && !tr.runsOut && tr.leftAtReset != null) sub = `About ${Math.round(tr.leftAtReset)}% left at reset`;
  return `<span class="two"><span>${main}</span>${sub ? `<span class="muted">${esc(sub)}</span>` : ""}</span>`;
}

function acctCell(a, w) {
  const e = String(a.email || a.id), at = e.indexOf("@");
  const name = at > 0 ? `<b>${esc(e.slice(0, at))}</b><span>${esc(e.slice(at))}</span>` : `<b>${esc(e)}</b>`;
  return `<div class="c acct" style="min-width:${w}px"><span class="lead"><i class="sq" style="background:${accountColor(a.id)}"></i>${logo(a.provider)}</span><span class="email clamp">${name}</span></div>`;
}

// The table for the view's columns: one row per picked account, with an All
// accounts row under usage columns and the local logs before per-account data.
function tableHtml(view, ctx) {
  const cols = view.columns.filter((c) => COLUMNS[c]);
  if (!cols.length) return "";
  const now = ctx.now, r = ctx.range;
  const from = r ? r.start : ctx.window.start, to = Math.min(r ? r.end : ctx.window.end, now);
  const sc = ctx.sc;
  const hasAllow = cols.some((c) => ALLOWANCE_COLS.includes(c));
  // Requests count from performance next to other performance columns, else from usage.
  const perfMode = cols.some((c) => PERF_COLS.includes(c) && c !== "requests");
  const perfCols = perfMode ? cols.filter((c) => PERF_COLS.includes(c)) : [];
  const usageCols = cols.filter((c) => USAGE_COLS.includes(c) && !perfCols.includes(c));
  const acctW = hasAllow ? 380 : perfCols.length ? 412 : 292;
  // Allowance columns become the selection's columns while a range is set.
  const shownCols = [];
  for (const c of cols) {
    if (ALLOWANCE_COLS.includes(c) && r) {
      if (!shownCols.some((x) => x.sel)) shownCols.push({ id: "leftAt", sel: true, ...SEL_COLS.leftAt, label: `Left at ${endText(r.end)}` }, { id: "usedIn", sel: true, ...SEL_COLS.usedIn }, { id: "resetsIn", sel: true, ...SEL_COLS.resetsIn });
    } else shownCols.push({ id: c, ...COLUMNS[c] });
  }
  const allowW = shownCols.filter((c) => c.sel || ALLOWANCE_COLS.includes(c.id)).reduce((t, c) => t + c.w, 0);
  const perfW = shownCols.filter((c) => perfCols.includes(c.id)).reduce((t, c) => t + c.w, 0);
  const head = `<div class="tr head">${`<div class="c acct" style="min-width:${acctW}px">${r ? selectionLabel(r) : esc("Accounts, " + windowText(ctx.window, now))}</div>`}${shownCols.map((c) => cell(c.w, esc(c.label), c.r, c.cls)).join("")}</div>`;

  const sums = usageSum(ctx.usage, sc.ids, from, to, now);
  const lines = new Map(allowanceLines(sc.ids, true, now).map((l) => [l.id, l]));
  const perfSrc = r ? (ctx.rangePerf ? RP.perf : null) : S.data?.summary?.performance;
  const deadNow = availability(sc.ids, { start: now - 1, end: now + 30 * DAY }, now).filter((p) => p.at(now) === 0)
    .map((p) => { const back = p.segs.find((s) => s.from > now && s.n > 0)?.from; return [p.provider, `no ${providerTitle(p.provider)} allowance${back ? " until " + resetName(back, now) : ""}`]; });
  const deadText = new Map(deadNow);

  const allowCells = (a) => {
    const l = lines.get(a.id);
    if (!l) return { span: allowW, html: `<span class="span2">No allowance reading yet</span>` };
    const out = [];
    for (const c of shownCols.filter((x) => x.sel || ALLOWANCE_COLS.includes(x.id))) {
      if (c.id === "weekLeft") out.push(cell(c.w, meter(l.tr.usedUp ? 0 : l.tr.leftNow, l.color)));
      else if (c.id === "perDay") out.push(cell(c.w, l.tr.rate == null ? `<span class="muted">–</span>` : esc(pctRate(l.tr.rate * 24))));
      else if (c.id === "heading") out.push(cell(c.w, headingFor(l, now)));
      else if (c.id === "nextReset") out.push(cell(c.w, l.tr.reset > now ? esc(fullTime(l.tr.reset)) : `<span class="muted">–</span>`));
      else if (c.id === "leftAt") { const v = leftAt(l, r.end, now); out.push(cell(c.w, v == null ? `<span class="muted">–</span>` : meter(v, l.color))); }
      else if (c.id === "usedIn") { const u = allowanceRange(l.ser, l.tr, now, r.start, r.end); out.push(cell(c.w, u?.used == null ? `<span class="muted">–</span>` : Math.round(u.used) + "%")); }
      else if (c.id === "resetsIn") {
        const list = resetsUntil(l.tr, now, r.end, l.period).filter((t) => t >= r.start);
        out.push(cell(c.w, list.length ? esc(list.map((t) => resetName(t, now)).join(", ")) : `<span class="muted">None</span>`));
      }
    }
    return { html: out.join("") };
  };
  const usageCell = (c, x) => {
    if (!x) return cell(COLUMNS[c].w, `<span class="muted">–</span>`, COLUMNS[c].r);
    const v = { tokens: fmt(tokens(x)), requests: int(x.requests), input: fmt(x.input_tokens), cacheWrite: fmt(x.cache_write_tokens), cacheRead: fmt(x.cache_read_tokens), output: fmt(x.output_tokens), cost: money(apiCost(x)) }[c];
    return c === "cacheReuse" ? cell(COLUMNS[c].w, reuseCell(x)) : cell(COLUMNS[c].w, esc(v), COLUMNS[c].r, COLUMNS[c].cls);
  };
  // One account's performance, or for scope "total" the picked accounts'.
  const perfCells = (a, total = false) => {
    const sel = { some: total ? sc.some : false, prov: total ? sc.prov : a.id, ids: total ? sc.ids : [a.id] };
    let q = total ? (r ? ctx.rangePerf?.q : ctx.perf?.q) || null : perfSrc?.scopes?.[a.id] || null;
    let counts = null;
    if (r && !q) {
      // The selection's own reading has not arrived: counts from the window's buckets.
      const c = perfCounts(perfScope(sel), r.start, Math.min(r.end, now));
      if (c.any) counts = c;
    }
    const reqs = q ? Number(q.requests) || 0 : counts ? counts.requests : 0;
    if (!reqs) {
      const extra = total ? "" : deadText.get(a.provider);
      return { span: perfW, html: `<span class="span2">No requests${extra ? ", " + esc(extra) : ""}</span>` };
    }
    const failed = q ? Number(q.failed) || 0 : counts.failed;
    const failovers = q ? q.failovers : counts.hasFailovers ? counts.failovers : undefined;
    const val = {
      requests: int(reqs),
      ttft50: q?.ttft_ms?.p50 ? ms(q.ttft_ms.p50) : "–",
      ttft90: q?.ttft_ms?.p90 ? ms(q.ttft_ms.p90) : "–",
      throughput: perSec(q?.throughput?.p50),
      failures: int(failed),
      failovers: failovers == null ? "–" : int(failovers),
    };
    return { html: shownCols.filter((c) => perfCols.includes(c.id)).map((c) => cell(c.w, esc(val[c.id]), c.r)).join("") };
  };

  const rows = sc.shown.map((a) => {
    let html = acctCell(a, acctW);
    let allowDone = false, perfDone = false;
    const al = hasAllow ? allowCells(a) : null;
    const pf = perfCols.length ? perfCells(a) : null;
    for (const c of shownCols) {
      if (c.sel || ALLOWANCE_COLS.includes(c.id)) {
        if (allowDone) continue;
        allowDone = true;
        html += al.span ? `<div class="c" style="width:${al.span}px">${al.html}</div>` : al.html;
      } else if (perfCols.includes(c.id)) {
        if (perfDone) continue;
        perfDone = true;
        html += pf.span ? `<div class="c" style="width:${pf.span}px">${pf.html}</div>` : pf.html;
      } else html += usageCell(c.id, sums.per[a.id]);
    }
    return `<div class="tr row" data-row-acct="${esc(a.id)}">${html}</div>`;
  });
  // An All accounts row under usage and performance columns, never under allowance.
  if (!hasAllow && sc.shown.length > 1) {
    const pf = perfCols.length ? perfCells(null, true) : null;
    let done = false;
    const cells = shownCols.map((c) => {
      if (!perfCols.includes(c.id)) return usageCell(c.id, sums.sum);
      if (done) return "";
      done = true;
      return pf.span ? `<div class="c" style="width:${pf.span}px">${pf.html}</div>` : pf.html;
    }).join("");
    rows.push(`<div class="tr total"><div class="c acct" style="min-width:${acctW}px">All accounts</div>${cells}</div>`);
  }
  // Earlier history only makes sense for the usage columns.
  const onlyUsage = usageCols.length && !hasAllow && !perfCols.length;
  if (onlyUsage) {
    // Per-account counters start later than the provider history from local logs.
    const first = sums.first ?? to;
    if (first > from + 60e3 && !sc.some) {
      const provs = sc.prov === "all" ? null : [sc.prov];
      const early = historyBefore(first, from, provs);
      if (early) rows.push(`<div class="tr earlier"><div class="c acct" style="min-width:${acctW}px">Earlier, from local logs</div>${shownCols.map((c) => usageCell(c.id, early)).join("")}</div>`);
    }
  }
  if (!sc.shown.length) rows.push(`<div class="empty">No accounts picked.</div>`);
  return `<div class="ttbl" role="table">${head}${rows.join("")}</div>`;
}

// ---------- the page ----------

const STAR = (on) => `<svg width="16" height="16" viewBox="0 0 24 24" aria-hidden="true"><path d="${"M11.48 3.499a.562.562 0 0 1 1.04 0l2.125 5.111a.563.563 0 0 0 .475.345l5.518.442c.499.04.701.663.321.988l-4.204 3.602a.563.563 0 0 0-.182.557l1.285 5.385a.562.562 0 0 1-.84.61l-4.725-2.885a.562.562 0 0 0-.586 0L6.982 20.54a.562.562 0 0 1-.84-.61l1.285-5.386a.562.562 0 0 0-.182-.557l-4.204-3.602a.562.562 0 0 1 .321-.988l5.518-.442a.563.563 0 0 0 .475-.345L11.48 3.5Z"}"/></svg>`;

export function view(ctx) {
  const id = ctx.params[0];
  if (!V.loaded) loadViews().then(rerender);
  const c = current(id);
  if (!c) {
    return { html: `<div class="tbar"><div class="l"><span class="vname">${V.loaded ? "View not found" : "Loading"}</span></div></div><div class="body">${V.loaded ? `<div class="empty">This view was deleted. <a class="linkbtn" href="#/telemetry">Open the default view</a></div>` : ""}</div>` };
  }
  const v = c.view;
  const ts = viewTime(v);
  const sc = accountScope();
  const forecast = hasForecast(v);
  const base = panelContext(ts, sc, { forecast });
  const axisIdx = v.panels.map((p, i) => (PANELS[p.type]?.axis === false ? -1 : i)).filter((i) => i >= 0);
  const firstAxis = axisIdx[0], lastAxis = axisIdx[axisIdx.length - 1];
  const built = v.panels.map((p, i) => buildPanel(p.type, p.options, { ...base, labels: i === lastAxis, hint: i === firstAxis ? zoomHint() : "" }));
  const panels = v.panels.map((p, i) => panelHtml(p.type, i, built[i], p.options)).join("");
  const isDefault = defaultId(V.store) === v.id;
  const name = renameBox(v.id) ?? `<span class="vname" data-rename tabindex="0" role="button" title="Rename this view">${esc(v.name)}</span>`;
  const html = `
    <div class="tbar">
      <div class="l">${name}<button class="star ${isDefault ? "on" : ""}" data-star aria-pressed="${isDefault}" aria-label="${isDefault ? "Default view" : "Set as default view"}" title="${isDefault ? "Opens first under Telemetry" : "Set as default view"}">${STAR(isDefault)}</button></div>
      <div class="r">
        <span class="wlabel">${esc(windowText(ts.window, base.now))}</span>
        ${ts.moved ? backToNow("data-home") : ""}
        <button class="dispbtn ${displayOpen() ? "open" : ""}" data-display data-anchor="display" aria-haspopup="dialog" aria-expanded="${displayOpen()}">${icon("adjust", 14)}<span>Display</span></button>
        ${accountPicker()}
      </div>
    </div>
    ${c.dirty ? `<div class="ubar" role="status"><span>You changed this view</span><div class="acts"><button data-u-reset>Reset</button><button data-u-new>Save as new view</button><button class="save" data-u-save>Save</button></div></div>` : ""}
    <div class="body tele" data-tele>
      ${panels || `<div class="empty">This view has no panels. Add some from Display.</div>`}
      ${tableHtml(v, base)}
      <div class="readrow">${esc(readAt())}</div>
    </div>`;

  return {
    html,
    mount(root) {
      bindAccountPicker(root);
      const tele = root.querySelector("[data-tele]");
      const setWindow = (w) => { ts.setRange(null); ts.setWindow(w); };
      bindTime(tele, {
        key: KEY,
        window: ts.window,
        range: ts.range,
        grid: snapGrid(base),
        future: forecast,
        defaultWindow: ts.defaultWindow,
        setWindow: ts.setWindow,
        setRange: ts.setRange,
        probe(t, el, hovered) {
          const i = v.panels.findIndex((p) => p.type === el.dataset.panel);
          return i >= 0 ? built[i].probe?.(t, el, hovered) || null : null;
        },
      });
      built.forEach((b) => b.mount?.(root, setWindow));
      tele.querySelectorAll("[data-opt]").forEach((b) => {
        b.onclick = () => edit(v.id, (d) => { const p = d.panels[Number(b.dataset.opt)]; if (p) p.options = { ...p.options, [b.dataset.key]: b.dataset.val }; });
      });
      tele.querySelectorAll("[data-clear-range]").forEach((b) => { b.onclick = () => ts.setRange(null); });
      tele.addEventListener("keydown", (e) => { if (e.key === "Escape" && ts.range && !e.defaultPrevented) ts.setRange(null); });
      // A table row lights its account's lines on every panel.
      const marks = () => tele.querySelectorAll("[data-acct]");
      tele.querySelectorAll("[data-row-acct]").forEach((row) => {
        row.addEventListener("mouseenter", () => { const id2 = row.dataset.rowAcct; marks().forEach((m) => { m.style.opacity = m.dataset.acct === id2 ? "" : ".25"; }); });
        row.addEventListener("mouseleave", () => marks().forEach((m) => { m.style.opacity = ""; }));
      });
      const home = root.querySelector("[data-home]");
      if (home) home.onclick = () => { ts.setRange(null); ts.setWindow(ts.defaultWindow()); };
      root.querySelector("[data-display]").onclick = (e) => openDisplay(e.currentTarget, v.id);
      root.querySelector("[data-star]").onclick = () => setDefaultView(v.id);
      const nm = root.querySelector("[data-rename]");
      if (nm) {
        nm.ondblclick = () => startRename(v.id);
        nm.onkeydown = (e) => { if (e.key === "Enter" || e.key === "F2") { e.preventDefault(); startRename(v.id); } };
      }
      mountRename(root);
      const u = (sel, f) => { const b = root.querySelector(sel); if (b) b.onclick = f; };
      u("[data-u-reset]", () => { dropDraft(v.id); forgetTime(KEY); rerender(); });
      u("[data-u-save]", () => saveDraft(v.id));
      u("[data-u-new]", () => newViewDialog(v));
      // A selection needs its own reading for percentiles.
      const perfPanels = v.panels.some((p) => ["ttft", "latency", "throughput"].includes(p.type)) || v.columns.some((col) => PERF_COLS.includes(col) && col !== "requests");
      if (ts.range && perfPanels) loadRangePerf(ts.range, sc.some ? scopeParam(sc.ids) : "");
    },
  };
}

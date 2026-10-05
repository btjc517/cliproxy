// Performance: metric cards in a grid (first token, full response,
// throughput, failures, requests, cache reuse by default), a By account
// table on request, and a popover to pick, order and format the cards.
import {
  S, esc, int, fmt, ms, icon, logo, email, status, warnState, perf, rate, rateText, pctText, tokens, cacheReuse, sumUsage, tipRows,
  table, menu, closeMenu, timeChart, bindChart, chartFormat, setChartFormat, formatToggle, bindFormatToggles, timeLabels, bucketTitle,
  prefs, setPref, plainObject, rangeTabs, bindRangeTabs, screenRange,
} from "../core.js";
import { providerTabs, bindProviderTabs, accountChips, scopeOf, perfFor, readAt, gapNote, legendHtml } from "./common.js";

const METRICS = [
  { id: "ttft", title: "First token" },
  { id: "latency", title: "Full response" },
  { id: "throughput", title: "Throughput" },
  { id: "failures", title: "Failures" },
  { id: "requests", title: "Requests" },
  { id: "cache", title: "Cache reuse" },
  { id: "failovers", title: "Moved to another account" },
  { id: "tokens", title: "Tokens" },
];
const DEFAULT_ON = ["ttft", "latency", "throughput", "failures", "requests", "cache"];
const TABLE_CARDS = 4;
const DAY = 864e5;
const chartKey = (id) => "perf-" + id;
const rerender = () => window.dispatchEvent(new Event("dash:render"));
const GRIP = `<svg width="16" height="16" viewBox="0 0 24 24" aria-hidden="true">${[[9, 6], [15, 6], [9, 12], [15, 12], [9, 18], [15, 18]].map(([x, y]) => `<circle cx="${x}" cy="${y}" r="1.125" fill="var(--icon)"/>`).join("")}</svg>`;
const CHECK = `<svg width="12" height="12" viewBox="0 0 24 24" aria-hidden="true"><path d="M5 12.75 9.5 17.25 19 7.5" fill="none" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"/></svg>`;

// ---------- the viewer's layout ----------

// The stored layout, cleaned: known metrics only, each once, missing ones at the end.
function layout() {
  const p = plainObject(prefs().performance) || {};
  const known = METRICS.map((m) => m.id);
  const saved = Array.isArray(p.order) ? [...new Set(p.order.filter((id) => known.includes(id)))] : [];
  const order = [...saved, ...known.filter((id) => !saved.includes(id))];
  const on = new Set(Array.isArray(p.on) ? p.on.filter((id) => known.includes(id)) : DEFAULT_ON);
  return { order, on, table: p.table === true };
}
function saveLayout(l) { setPref("performance", { order: l.order, on: [...l.on], table: l.table }); }

// ---------- cards ----------

const perSec = (v) => (v ? (v < 10 ? v.toFixed(1) : String(Math.round(v))) : "–");
const each = (v) => (v < 10 ? v.toFixed(1).replace(/\.0$/, "") : int(Math.round(v)));
const has = (series, k) => series.some((b) => b && Object.prototype.hasOwnProperty.call(b, k));
const sumOf = (series, k) => series.reduce((t, b) => t + (Number(b[k]) || 0), 0);
const tokenSums = (series) => ({ input_tokens: sumOf(series, "input_tokens"), output_tokens: sumOf(series, "output_tokens"), cache_read_tokens: sumOf(series, "cache_read_tokens"), cache_write_tokens: sumOf(series, "cache_write_tokens") });

// What one card shows. ctx: { p, series, starts, step, range, ids }.
function cardModel(id, ctx) {
  const { p, series, range, ids } = ctx;
  const any = !!(p && p.requests);
  const t = p?.ttft_ms || {}, l = p?.latency_ms || {}, tp = p?.throughput || {};
  const msY = (v) => (v ? ms(v) : "0");
  // Usage totals for the cards that need tokens when the series has none yet.
  const usage = () => {
    if (has(series, "input_tokens")) return tokenSums(series);
    if (range === "24h") return sumUsage(ids, "last_24h");
    if (range === "7d") return sumUsage(ids, "last_7d");
    return null;
  };
  switch (id) {
    case "ttft": return {
      value: any && t.p50 ? ms(t.p50) : "–", sub: t.p90 ? "p90 " + ms(t.p90) : "",
      legend: [{ label: "Median", color: "var(--chart-1)" }, { label: "p90", color: "var(--chart-p50)" }],
      need: "ttft_p50_ms", gaps: true, overlay: true, yfmt: msY,
      series: [{ key: "ttft_p50_ms", color: "var(--chart-1)", label: "Median", gaps: true }, { key: "ttft_p90_ms", color: "var(--chart-p50)", label: "p90", gaps: true }],
      rows: (b) => [{ k: "Median", v: ms(b.ttft_p50_ms), color: "var(--chart-1)" }, { k: "p90", v: ms(b.ttft_p90_ms), color: "var(--chart-p50)" }],
    };
    case "latency": return {
      value: any && l.p50 ? ms(l.p50) : "–", sub: l.p90 ? "p90 " + ms(l.p90) : "",
      legend: [{ label: "Median", color: "var(--chart-1)" }, { label: "p90", color: "var(--chart-p50)" }],
      need: "latency_p50_ms", gaps: true, overlay: true, yfmt: msY,
      series: [{ key: "latency_p50_ms", color: "var(--chart-1)", label: "Median", gaps: true }, { key: "latency_p90_ms", color: "var(--chart-p50)", label: "p90", gaps: true }],
      rows: (b) => [{ k: "Median", v: ms(b.latency_p50_ms), color: "var(--chart-1)" }, { k: "p90", v: ms(b.latency_p90_ms), color: "var(--chart-p50)" }],
    };
    case "throughput": return {
      value: any && tp.p50 ? perSec(tp.p50) : "–",
      sub: any && tp.p50 ? "tokens/s" + (tp.p10 ? " · slowest 10% under " + perSec(tp.p10) : "") : "",
      need: "throughput_p50", gaps: true, yfmt: (v) => (v ? String(Math.round(v)) : "0"),
      series: [{ key: "throughput_p50", color: "var(--chart-1)", label: "Median", gaps: true }],
      rows: (b) => [{ k: "Median", v: perSec(b.throughput_p50) + " tokens/s", color: "var(--chart-1)" }, ...(b.throughput_p10 ? [{ k: "Slowest 10%", v: "under " + perSec(b.throughput_p10) }] : [])],
    };
    case "failures": return {
      value: any ? rateText(p.failed, p.requests) : "–",
      sub: any ? `${int(p.failed)} of ${int(p.requests)} requests${p.failovers != null ? ` · ${int(p.failovers)} moved to another account` : ""}` : "",
      need: "failed", yfmt: (v) => fmt(v),
      series: [{ key: "failed", color: "var(--warn)", label: "Failed" }],
      head: (b) => int(b.failed),
      rows: (b) => [{ k: "Requests", v: int(b.requests) }, { k: "Failure rate", v: rateText(b.failed, b.requests), cls: b.failed ? "warn" : "" }],
    };
    case "requests": {
      const days = range === "24h" ? 0 : range === "7d" ? 7 : range === "30d" ? 30 : range === "180d" ? 180 : (series.length * ctx.step) / DAY;
      const per = any ? (days ? `${each(p.requests / days)} a day` : `${each(p.requests / 24)} an hour`) : "";
      return {
        value: p ? int(p.requests) : "–", sub: p ? "requests" + (per ? " · " + per : "") : "",
        need: "requests", yfmt: (v) => fmt(v),
        series: [{ key: "requests", color: "var(--chart-1)", label: "Requests" }],
        head: (b) => int(b.requests),
        rows: (b) => [{ k: "Failed", v: int(b.failed), cls: b.failed ? "warn" : "" }],
      };
    }
    case "cache": {
      const u = usage();
      const r = u ? cacheReuse(u) : null;
      return {
        value: pctText(r), sub: r != null ? "of input read from cache" : "",
        need: "cache_read_tokens", gaps: true, max: 100, yfmt: (v) => Math.round(v) + "%",
        series: [{ key: "reuse", color: "var(--chart-1)", label: "Cache reuse", gaps: true }],
        derive: (b) => ({ reuse: cacheReuse(b) }), emptyIf: (b) => cacheReuse(b) == null,
        head: (b) => pctText(cacheReuse(b)),
        rows: (b) => [{ k: "Read from cache", v: fmt(b.cache_read_tokens) }, { k: "Requests", v: int(b.requests) }],
      };
    }
    case "failovers": return {
      value: p && p.failovers != null ? int(p.failovers) : "–", sub: p && p.failovers != null ? "after a failed attempt" : "",
      need: "failovers", yfmt: (v) => fmt(v),
      series: [{ key: "failovers", color: "var(--chart-1)", label: "Moved" }],
      head: (b) => int(b.failovers),
      rows: (b) => [{ k: "Failed", v: int(b.failed), cls: b.failed ? "warn" : "" }, { k: "Requests", v: int(b.requests) }],
    };
    case "tokens": {
      const u = usage();
      return {
        value: u ? fmt(tokens(u)) : "–", sub: u ? `${fmt(u.output_tokens)} output · ${fmt(u.cache_read_tokens)} read from cache` : "",
        need: "input_tokens", yfmt: (v) => fmt(v),
        series: [{ key: "tok", color: "var(--chart-1)", label: "Tokens" }],
        derive: (b) => ({ tok: tokens(b) }),
        head: (b) => fmt(tokens(b)),
        rows: (b) => [{ k: "Read from cache", v: fmt(b.cache_read_tokens) }, { k: "Written to cache", v: fmt(b.cache_write_tokens) }, { k: "Output", v: fmt(b.output_tokens) }, { k: "New input", v: fmt(b.input_tokens) }],
      };
    }
  }
  return null;
}

function card(id, ctx, k, count, height, tableOn) {
  const m = METRICS.find((x) => x.id === id);
  const c = cardModel(id, ctx);
  const key = chartKey(id);
  const { series, starts, step } = ctx;
  // Fields an older proxy does not send leave the chart out instead of drawing zeros.
  const can = series.length && has(series, c.need);
  const traffic = series.some((b) => b.requests);
  let chart, tip = null;
  if (!can || !traffic) {
    // "–" when the data is not there (older proxy, or still loading); a sentence when there was simply no traffic.
    chart = `<div class="nochart" style="height:${height + 20}px">${ctx.p && can ? "No requests in this range" : "–"}</div>`;
  } else {
    const cols = series.map((b) => ({ empty: c.emptyIf ? c.emptyIf(b) : !b.requests, values: c.derive ? c.derive(b) : b }));
    chart = timeChart({ id: key, format: chartFormat(key), series: c.series, cols, height, yfmt: c.yfmt, max: c.max ?? null, overlay: !!c.overlay, labels: timeLabels(starts, step), dense: series.length > 40 });
    tip = (i) => {
      const b = series[i];
      if (!b) return "";
      if (!b.requests) return gapNote(starts, i, step, (j) => !!series[j].requests) || "";
      const right = c.head ? c.head(b) : int(b.requests);
      return `<div class="h"><span>${esc(bucketTitle(starts, i, step))}</span><span>${esc(right)}</span></div>${tipRows(c.rows(b))}`;
    };
  }
  const right = k % 2 === 1;
  const lastRow = k >= count - (count % 2 || 2);
  const tools = `<div class="ctools">${tip ? formatToggle(key) : ""}<button class="iconbtn" style="width:24px;height:24px" data-more="${id}" aria-label="More for ${esc(m.title)}">${icon("more")}</button></div>`;
  const legend = c.legend && tip ? `<div class="legend">${legendHtml(c.legend)}</div>` : "";
  const html = `<div class="pcard chartbox ${right ? "r" : ""} ${lastRow && !tableOn ? "end" : ""} ${k === count - 1 ? "last" : ""}" data-card="${id}">
    <div class="ph">
      <div class="pt1"><b>${esc(m.title)}</b><div class="row gap8">${legend}${tools}</div></div>
      <div class="pv"><span class="v">${esc(c.value)}</span><span class="s">${esc(c.sub)}</span></div>
    </div>
    ${chart}
  </div>`;
  return { html, key, tip };
}

// ---------- by account ----------

function byAccount(sc, range) {
  // Accounts with no requests in the range have nothing to compare, so they are left out.
  const rows = sc.shown.map((a) => ({ a, q: perf(a.id, range) })).filter(({ q }) => q?.requests > 0).sort((x, y) => (y.q?.requests || 0) - (x.q?.requests || 0)).map(({ a, q }) => {
    const st = status(a);
    const warn = st.kind === "blocked" || st.kind === "error" ? warnState(st.text) : "";
    const fr = q && q.requests ? rate(q.failed, q.requests) : null;
    return {
      href: "#/accounts/" + encodeURIComponent(a.id),
      cells: [
        logo(a.provider),
        `${email(a.email)}${warn}`,
        q && q.requests ? int(q.requests) : `<span class="muted">0</span>`,
        q?.ttft_ms?.p50 ? `<span class="cellpair">${ms(q.ttft_ms.p50)}${q.ttft_ms.p90 ? `<span class="muted">p90 ${ms(q.ttft_ms.p90)}</span>` : ""}</span>` : "",
        q?.latency_ms?.p50 ? ms(q.latency_ms.p50) : "",
        q?.throughput?.p50 ? perSec(q.throughput.p50) + "/s" : "",
        fr == null ? "" : `<span class="${fr >= 10 ? "warn" : fr === 0 ? "muted" : ""}">${rateText(q.failed, q.requests)}</span>`,
      ],
    };
  });
  const cols = [
    { label: "", w: 16, cls: "ic" }, { label: "Account" }, { label: "Requests", w: 80, r: true }, { label: "First token", w: 136, r: true },
    { label: "Full response", w: 112, r: true }, { label: "Throughput", w: 104, r: true }, { label: "Failure rate", w: 96, r: true },
  ];
  return `<div class="sec last"><div class="sech base"><b>By account</b><span class="muted">Sorted by requests</span></div>${table(cols, rows, { empty: "No requests in this range." })}</div>`;
}

// ---------- the screen ----------

let live = null; // the mounted screen, so the popover can redraw the grid while it is open

function grid(ctx, l) {
  let shown = l.order.filter((id) => l.on.has(id));
  if (l.table) shown = shown.slice(0, TABLE_CARDS);
  const height = l.table ? 120 : 141;
  const cards = shown.map((id, k) => card(id, ctx, k, shown.length, height, l.table));
  const html = `<div class="pgrid" data-pgrid>${cards.map((c) => c.html).join("") || `<div class="empty">No charts chosen. Pick some with the customise button.</div>`}</div>`;
  const mount = (root) => {
    const g = root.querySelector("[data-pgrid]");
    if (!g) return;
    for (const c of cards) if (c.tip) bindChart(g, c.key, c.tip);
    bindFormatToggles(g, () => redrawGrid());
    g.querySelectorAll("[data-more]").forEach((b) => {
      b.onclick = () => {
        const cardEl = b.closest(".pcard");
        cardEl.classList.add("open");
        menu(b, [{ a: "Hide chart", run: () => { const x = layout(); x.on.delete(b.dataset.more); saveLayout(x); rerender(); } }], { width: 180, onClose: () => cardEl.classList.remove("open") });
      };
    });
  };
  return { html, mount };
}

function redrawGrid() {
  if (!live || !live.root.isConnected) return;
  const old = live.root.querySelector("[data-pgrid]");
  if (!old) return;
  const g = grid(live.ctx, layout());
  old.outerHTML = g.html;
  g.mount(live.root);
}

function customise(anchor) {
  const draw = (box, focusId) => {
    const l = layout();
    box.innerHTML = `<div class="phd"><b>Charts</b><span>Drag to reorder.<br>With the table open, the first ${TABLE_CARDS} show.</span></div>
      <div class="plist" role="list">${l.order.map((id) => {
        const m = METRICS.find((x) => x.id === id);
        const on = l.on.has(id);
        const f = chartFormat(chartKey(id));
        return `<div class="prow ${on ? "" : "off"}" role="listitem" tabindex="0" data-row="${id}" aria-label="${esc(m.title)}, ${on ? "shown" : "hidden"}. Arrow keys move it, space shows or hides it.">
          <span class="grip" data-grip aria-hidden="true">${GRIP}</span>
          <span class="nm">${esc(m.title)}</span>
          <button class="fmtbtn" data-pfmt="${id}" tabindex="-1" aria-label="${f === "line" ? "Line chart. Switch to bars" : "Bar chart. Switch to a line"}" title="${f === "line" ? "Line" : "Bars"}">${icon(f === "line" ? "line" : "usage", 14)}</button>
          <button class="cbox ${on ? "on" : ""}" data-check="${id}" tabindex="-1" role="checkbox" aria-checked="${on}" aria-label="Show ${esc(m.title)}">${on ? CHECK : ""}</button>
        </div>`;
      }).join("")}</div>
      <div class="pfoot"><hr><button data-reset>Reset to default</button></div>`;
    const apply = (l2, keep) => { saveLayout(l2); redrawGrid(); draw(box, keep); };
    const move = (id, by) => {
      const l2 = layout();
      const i = l2.order.indexOf(id), j = i + by;
      if (j < 0 || j >= l2.order.length) return;
      l2.order.splice(i, 1);
      l2.order.splice(j, 0, id);
      apply(l2, id);
    };
    const toggle = (id) => { const l2 = layout(); if (l2.on.has(id)) l2.on.delete(id); else l2.on.add(id); apply(l2, id); };
    box.querySelectorAll("[data-check]").forEach((b) => { b.onclick = () => toggle(b.dataset.check); });
    box.querySelectorAll("[data-pfmt]").forEach((b) => {
      b.onclick = () => {
        const key = chartKey(b.dataset.pfmt);
        setChartFormat(key, chartFormat(key) === "line" ? "bars" : "line");
        redrawGrid();
        draw(box, b.dataset.pfmt);
      };
    });
    box.querySelector("[data-reset]").onclick = () => {
      for (const m of METRICS) setChartFormat(chartKey(m.id), "line");
      setPref("performance", { table: layout().table });
      redrawGrid();
      draw(box);
    };
    const list = box.querySelector(".plist");
    list.querySelectorAll("[data-row]").forEach((row) => {
      const id = row.dataset.row;
      row.onkeydown = (e) => {
        if (e.key === "ArrowUp" || e.key === "ArrowDown") { e.preventDefault(); move(id, e.key === "ArrowUp" ? -1 : 1); }
        else if (e.key === " " || e.key === "Enter") { e.preventDefault(); toggle(id); }
      };
      // Drag by the handle: the row follows the pointer in 32px steps. Moving
      // the row in the DOM drops pointer capture, so the document listens.
      const grip = row.querySelector("[data-grip]");
      grip.onpointerdown = (e) => {
        if (e.button !== 0) return;
        e.preventDefault();
        row.classList.add("drag");
        const rows = () => [...list.querySelectorAll("[data-row]")];
        const onMove = (ev) => {
          const lr = list.getBoundingClientRect();
          const all = rows();
          const target = Math.max(0, Math.min(all.length - 1, Math.floor((ev.clientY - lr.top) / 32)));
          const cur = all.indexOf(row);
          if (target === cur) return;
          const ref = all[target];
          list.insertBefore(row, target > cur ? ref.nextSibling : ref);
        };
        const onUp = () => {
          document.removeEventListener("pointermove", onMove);
          document.removeEventListener("pointerup", onUp);
          document.removeEventListener("pointercancel", onUp);
          const l2 = layout();
          l2.order = rows().map((r) => r.dataset.row);
          apply(l2, id);
        };
        document.addEventListener("pointermove", onMove);
        document.addEventListener("pointerup", onUp);
        document.addEventListener("pointercancel", onUp);
      };
    });
    if (focusId) box.querySelector(`[data-row="${focusId}"]`)?.focus();
  };
  anchor.classList.add("on");
  menu(anchor, [{ html: `<div data-pop></div>`, mount: (el) => draw(el) }], { width: 280, cls: "pop", gap: 6, onClose: () => anchor.classList.remove("on") });
}

export function view() {
  const sc = scopeOf("pfProv");
  const range = screenRange("performance");
  const p = perfFor(sc, range);
  const series = (p?.series || []).filter((b) => b && b.start);
  const step = (Number(S.data?.summary?.performance?.bucket_seconds) || 3600) * 1000;
  const ctx = { p, series, starts: series.map((b) => Date.parse(b.start)), step, range, ids: sc.ids };
  const l = layout();
  const g = grid(ctx, l);

  const html = `
    <div class="bar wrap">${providerTabs("pfProv")}
      <div class="end wide"><span class="muted nowrap readat">${esc(readAt())}</span>${rangeTabs("performance")}
        <div class="views">
          <button class="iconbtn ${l.table ? "on" : ""}" data-table aria-pressed="${l.table}" aria-label="${l.table ? "Hide" : "Show"} the By account table" title="${l.table ? "Hide" : "Show"} table">${icon("table")}</button>
          <button class="iconbtn" data-customise aria-label="Customise charts" title="Customise charts">${icon("adjust")}</button>
        </div>
      </div>
    </div>
    ${accountChips("pfProv")}
    <div class="body">
      ${g.html}
      ${l.table ? byAccount(sc, range) : ""}
    </div>`;

  return {
    html,
    mount(root) {
      live = { root, ctx };
      bindProviderTabs(root, "pfProv");
      bindRangeTabs(root, "performance");
      g.mount(root);
      root.querySelector("[data-table]").onclick = () => { const x = layout(); x.table = !x.table; saveLayout(x); rerender(); };
      root.querySelector("[data-customise]").onclick = (e) => {
        const btn = e.currentTarget;
        const wasOpen = btn.classList.contains("on");
        closeMenu();
        if (!wasOpen) customise(btn);
      };
    },
  };
}

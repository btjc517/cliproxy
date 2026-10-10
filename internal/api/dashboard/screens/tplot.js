// Drawing for every time plot: axes, gridlines, lines, bars, bands, the
// future hatch and the Now line, all on one geometry so stacked plots line
// up. Interaction lives in timeaxis.js; this file only draws.
import { esc, fmt, niceMax } from "../core.js";
import { fracOf, plotWidth } from "./timeaxis.js";
import { region } from "../paint.js";

const pc = (f) => (f * 100).toFixed(3) + "%";
const clamp01 = (f) => Math.max(0, Math.min(1, f));

// Top and bottom space around the drawing area: a row for reset marks above
// the allowance plot, a row of time labels under the bottom plot.
export const plotPad = ({ marks = false, labels = false } = {}) => ({ top: marks ? 28 : 8, bottom: labels ? 28 : 8 });

// One plot. o: {
//   group, panel   names for the controller: data-tplot and data-panel
//   window, ticks  the shared window and its gridline ticks
//   height         the drawing area in px
//   labels         draw the tick labels under it (only the bottom plot)
//   now            draw the Now line and label
//   hatch          hatch the future: measures with nothing after now
//   y              {max, fmt} for three labelled lines, or null for none
//   marks          html for the reset row above the plot
//   content        html drawn inside the area, in window coordinates
//   over           html laid over the area that does not move with a pan
// }
export function plot(o) {
  const pad = plotPad({ marks: o.marks != null, labels: o.labels });
  const v = o.window;
  const total = pad.top + o.height + pad.bottom;
  const now = o.nowAt ?? Date.now();
  const nowF = fracOf(v, now);
  const showNow = o.now !== false && nowF >= 0 && nowF <= 1;
  let ylab = "", hlines = "";
  if (o.y) {
    for (const k of [0, 0.5, 1]) {
      const top = pad.top + o.height * (1 - k);
      hlines += `<i class="tp-h" style="top:${(o.height * (1 - k)).toFixed(1)}px"></i>`;
      ylab += `<span style="top:${(top - 8).toFixed(1)}px">${esc(o.y.fmt(o.y.max * k))}</span>`;
    }
  }
  const vlines = o.ticks.map((tk) => `<i class="tp-v" style="left:${pc(fracOf(v, tk.t))}"></i>`).join("");
  const hatch = o.hatch && nowF < 1 ? `<div class="tp-hatch" style="left:${pc(clamp01(nowF))};right:0"></div>` : "";
  const nowLine = showNow ? `<i class="tp-now" style="left:${pc(nowF)}"></i>` : "";
  let xl = "";
  if (o.labels) {
    const w = plotWidth();
    const near = (t) => showNow && Math.abs(fracOf(v, t) - nowF) * w < 44;
    const items = o.ticks.filter((tk) => !near(tk.t)).map((tk) => ({ f: fracOf(v, tk.t), text: tk.text }));
    if (showNow) items.push({ f: nowF, text: "Now", now: true });
    xl = `<div class="tp-x" style="top:${pad.top + o.height + 6}px">${items.map((it) => {
      const edge = it.f * w < 24 ? "first" : (1 - it.f) * w < 24 ? "last" : "";
      return `<span class="${edge} ${it.now ? "now" : ""}" style="left:${pc(it.f)}">${esc(it.text)}</span>`;
    }).join("")}</div>`;
  }
  // Everything that moves with the window is a region, so a pan or zoom
  // redraws these and leaves the plot area, which the controller owns.
  const key = (part) => `${o.group}|${o.panel}|${part}`;
  return `<div class="tp" style="height:${total}px">
    ${o.y ? region(key("y"), ylab, "div", `class="tp-y"`) : ""}
    ${o.marks != null ? region(key("marks"), o.marks, "div", `class="tp-marks"`) : ""}
    <div class="tp-area" data-tplot="${esc(o.group)}" data-panel="${esc(o.panel)}" style="top:${pad.top}px;height:${o.height}px">
      ${hlines}
      ${region(key("c"), `${vlines}${hatch}${o.content || ""}${nowLine}`, "div", `class="tp-content"`)}
      ${region(key("over"), o.over || "", "div", `style="display:contents"`)}
    </div>
    ${o.labels ? region(key("x"), xl, "div", `style="display:contents"`) : ""}
  </div>`;
}

// The top of a y axis for values, with the 100% axes kept at 100.
export const yTop = (vals, fixed = null) => (fixed != null ? fixed : niceMax(Math.max(0, ...vals.filter((x) => Number.isFinite(x)))));

// Lines. series: [{color, pts: [{t, v} or null for a gap], width, dash,
// opacity, acct, bridge}] where bridge joins gaps with a dotted muted line.
// Values are plotted against max over the drawing height.
export function lines(series, v, max, height) {
  const X = (t) => (fracOf(v, t) * 1000).toFixed(2);
  const Y = (val) => (height - (Math.max(0, Math.min(max, val)) / max) * height).toFixed(2);
  let paths = "";
  for (const s of series) {
    let d = "", bridge = "", last = null, open = false;
    for (const p of s.pts) {
      if (!p || p.v == null) { open = false; continue; }
      if (open && !p.move) d += `L${X(p.t)} ${Y(p.v)} `;
      else {
        if (last && s.bridge) bridge += `M${X(last.t)} ${Y(last.v)} L${X(p.t)} ${Y(p.v)} `;
        d += `M${X(p.t)} ${Y(p.v)} `;
      }
      open = true;
      last = p;
    }
    const attrs = `${s.acct ? `data-acct="${esc(s.acct)}"` : ""} ${s.opacity != null ? `opacity="${s.opacity}"` : ""}`;
    if (bridge) paths += `<path class="bridge" d="${bridge}" ${attrs}/>`;
    if (d) paths += `<path d="${d}" stroke="${s.color}" stroke-width="${s.width || 1.5}" ${s.dash ? `stroke-dasharray="${s.dash}"` : ""} ${attrs}/>`;
  }
  return `<svg class="tp-svg" viewBox="0 0 1000 ${height}" preserveAspectRatio="none" aria-hidden="true">${paths}</svg>`;
}

// A dot on a line: "end" is the larger dot at a line's last reading,
// "pt" the small one for a lone reading or a reset point.
export function dot(t, val, max, color, v, cls = "end", acct = "") {
  const top = (1 - Math.max(0, Math.min(max, val)) / max) * 100;
  return `<i class="tp-dot ${cls}" ${acct ? `data-acct="${esc(acct)}"` : ""} style="left:${pc(fracOf(v, t))};top:${top.toFixed(2)}%;background:${color}"></i>`;
}

// Stacked bars, one per bucket. buckets: [{t0, t1, segs: [{v, color, acct}]}],
// the first segment at the bottom. A bar takes most of its bucket's width,
// at most 32px, and the top segment gets rounded corners.
export function bars(buckets, v, max) {
  const w = plotWidth();
  return buckets.map((b) => {
    const f0 = fracOf(v, b.t0), f1 = fracOf(v, b.t1);
    if (f1 <= 0 || f0 >= 1) return "";
    const px = (f1 - f0) * w;
    const bw = Math.max(1, Math.min(32, px * (px > 12 ? 0.6 : 0.7)));
    const total = b.segs.reduce((t, s) => t + (s.v > 0 ? s.v : 0), 0);
    if (!total) return "";
    const segs = b.segs.filter((s) => s.v > 0).map((s, i, all) => `<i ${s.acct ? `data-acct="${esc(s.acct)}"` : ""} style="height:${((Math.min(s.v, max) / max) * 100).toFixed(3)}%;background:${s.color}${i === all.length - 1 ? ";border-radius:2px 2px 0 0" : ""}"></i>`).join("");
    return `<div class="tp-bar" style="left:calc(${pc((f0 + f1) / 2)} - ${(bw / 2).toFixed(1)}px);width:${bw.toFixed(1)}px">${segs}</div>`;
  }).join("");
}

// A shaded stretch behind the drawing, with an optional label at its top right.
export function band(t0, t1, v, cls, label = "") {
  const a = clamp01(fracOf(v, t0)), b = clamp01(fracOf(v, t1));
  if (b <= a) return "";
  return `<div class="tp-band ${cls}" style="left:${pc(a)};width:${pc(b - a)}">${label ? `<span>${esc(label)}</span>` : ""}</div>`;
}

// A legend of coloured squares, right of a panel title.
export const legend = (items) => `<span class="tp-legend">${items.map((l) => `<span><i style="background:${l.color}"></i>${esc(l.label)}</span>`).join("")}</span>`;

// The hover card used by every plot: a title, an optional warning line,
// rows with a colour square or logo, and an optional muted footer.
export function card({ title, warn = "", rows = [], foot = "" }) {
  const row = (r) => `<div class="r ${r.cls || ""}">${r.lead ?? (r.color ? `<i style="background:${r.color}"></i>` : "")}<span class="k">${esc(r.k)}</span><span class="v">${esc(r.v)}</span></div>`;
  return `<div class="h">${esc(title)}</div>${warn ? `<div class="w">${esc(warn)}</div>` : ""}${rows.map(row).join("")}${foot ? `<div class="f">${esc(foot)}</div>` : ""}`;
}

// A continuous time window shared by Usage graphs. The label is a readout.
import { S, esc, clock, day } from "../core.js";

export const HOUR = 3600e3, DAY = 24 * HOUR, WEEK = 7 * DAY;
export const MIN_SPAN = HOUR, MAX_SPAN = 366 * DAY;
export function defaultWindow(kind, now = Date.now()) {
  return kind === "allowance" ? { start: now - WEEK / 2, end: now + WEEK / 2 } : { start: now - WEEK, end: now };
}
export function windowFor(kind) { return S.ui[kind + "Viewport"] || defaultWindow(kind); }
export function zoomWindow(v, factor, anchor = .5) {
  const span = Math.max(MIN_SPAN, Math.min(MAX_SPAN, (v.end - v.start) * factor));
  const at = v.start + (v.end - v.start) * anchor;
  return { start: at - span * anchor, end: at + span * (1 - anchor) };
}
export const panWindow = (v, fraction) => ({ start: v.start + (v.end - v.start) * fraction, end: v.end + (v.end - v.start) * fraction });
export function spanText(ms) {
  const hours = ms / HOUR;
  const amount = (n, unit) => `${Number(n.toFixed(1))} ${unit}${Math.abs(n - 1) < .05 ? "" : "s"}`;
  if (Math.abs(hours - 168) < .1) return "1 week";
  if (hours < 48) return amount(hours, "hour");
  return amount(hours / 24, "day");
}
export function windowLabel(v) {
  const stamp = (t) => `${day(t)}${v.end - v.start < 2 * DAY ? ", " + clock(t) : ""}`;
  return `<span class="window-readout" aria-label="Visible time window"><span>${esc(stamp(v.start))} to ${esc(stamp(v.end))}</span><b>${esc(spanText(v.end - v.start))}</b></span>`;
}
export function viewportLabels(v, n, now = null, maxTicks = 8) {
  if (typeof document !== "undefined") maxTicks = Math.max(3, Math.min(maxTicks, Math.floor(((document.getElementById("main")?.clientWidth || 1200) - 90) / 120)));
  const span = v.end - v.start;
  const options = [HOUR, 3 * HOUR, 6 * HOUR, 12 * HOUR, DAY, 2 * DAY, 3 * DAY, 4 * DAY, 7 * DAY, 14 * DAY, 30 * DAY, 90 * DAY];
  const step = options.find((s) => span / s <= maxTicks) || 90 * DAY;
  const labels = [];
  for (let t = Math.ceil(v.start / step) * step; t <= v.end; t += step) {
    if (now != null && Math.abs(t - now) < span * Math.max(.055, .35 / maxTicks)) continue;
    labels.push({ i: (t - v.start) / span * (n - 1), text: span < 2 * DAY ? clock(t) : day(t) });
  }
  if (now != null && now >= v.start && now <= v.end) labels.push({ i: (now - v.start) / span * (n - 1), text: "Now" });
  return labels;
}

// Wheel/pinch gestures change the window. A mouse drag inspects a range;
// touch drags pan, two fingers zoom. Arrow keys pan, +/- zoom, Home resets.
export function bindViewport(root, id, kind, v, { clearHover = () => {}, rangeTip = null } = {}) {
  const chart = root.querySelector(`[data-chart="${id}"]`), plot = chart?.querySelector(".plot");
  if (!plot) return;
  plot.tabIndex = 0;
  plot.setAttribute("role", "application");
  plot.setAttribute("aria-label", "Time graph. Pinch or scroll vertically to zoom. Scroll horizontally or use arrow keys to pan. Drag to inspect a range. Plus and minus zoom. Home resets to one week.");
  plot.classList.add("interactive-plot");
  let frame = null, pending = v, brush = null, tip = null, dragging = null, held = false;
  const pointers = new Map();
  const fraction = (x) => { const r = plot.getBoundingClientRect(); return Math.max(0, Math.min(1, (x - r.left) / r.width)); };
  const clearRange = () => {
    brush?.remove(); tip?.remove(); brush = tip = null;
    plot.classList.remove("range-selected", "selecting");
    if (held) S.hold = Math.max(0, S.hold - 1);
    held = false;
  };
  const change = (next, focus = false) => {
    if (kind === "history" && next.end > Date.now()) { const excess = next.end - Date.now(); next = { start: next.start - excess, end: next.end - excess }; }
    clearHover(); clearRange(); pending = next;
    S.ui[kind + "Viewport"] = next;
    if (frame) cancelAnimationFrame(frame);
    frame = requestAnimationFrame(() => {
      if (!plot.isConnected || pointers.size) return;
      window.dispatchEvent(new Event("dash:render"));
      if (focus) root.querySelector(`[data-chart="${id}"] .plot`)?.focus({ preventScroll: true });
      if (kind === "history") window.dispatchEvent(new Event("dash:usage-window"));
    });
  };
  plot.addEventListener("wheel", (e) => {
    e.preventDefault();
    const scale = e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? plot.clientWidth : 1;
    const horizontal = !e.ctrlKey && (e.shiftKey || Math.abs(e.deltaX) > Math.abs(e.deltaY));
    change(horizontal ? panWindow(pending, (e.deltaX || e.deltaY) * scale / plot.clientWidth) : zoomWindow(pending, Math.exp(Math.max(-1, Math.min(1, e.deltaY * scale * .006))), fraction(e.clientX)));
  }, { passive: false });
  plot.addEventListener("keydown", (e) => {
    let next;
    if (e.key === "ArrowLeft" || e.key === "ArrowRight") next = panWindow(v, e.key === "ArrowLeft" ? -.2 : .2);
    if (e.key === "+" || e.key === "=") next = zoomWindow(v, .7);
    if (e.key === "-") next = zoomWindow(v, 1 / .7);
    if (e.key === "Home") next = defaultWindow(kind);
    if (e.key === "Escape") { clearHover(); clearRange(); }
    if (next) { e.preventDefault(); change(next, true); }
  });
  plot.addEventListener("dblclick", () => change(defaultWindow(kind)));
  plot.addEventListener("pointerdown", (e) => {
    if (e.button !== 0) return;
    clearHover(); clearRange();
    pointers.set(e.pointerId, e.clientX);
    plot.setPointerCapture(e.pointerId);
    if (!held) { S.hold++; held = true; }
    plot.classList.add("selecting");
    dragging = { x: e.clientX, f: fraction(e.clientX), touch: e.pointerType === "touch", window: pending, distance: pointers.size === 2 ? Math.abs([...pointers.values()][0] - [...pointers.values()][1]) : 0 };
  });
  plot.addEventListener("pointermove", (e) => {
    if (!dragging || !pointers.has(e.pointerId)) return;
    pointers.set(e.pointerId, e.clientX);
    if (dragging.touch) {
      const xs = [...pointers.values()];
      pending = xs.length === 2 && dragging.distance > 0 ? zoomWindow(dragging.window, dragging.distance / Math.max(1, Math.abs(xs[0] - xs[1])), fraction((xs[0] + xs[1]) / 2)) : panWindow(dragging.window, (dragging.x - e.clientX) / plot.clientWidth);
      const label = root.querySelector(".window-readout");
      if (label) label.outerHTML = windowLabel(pending);
      return;
    }
    if (!rangeTip || Math.abs(e.clientX - dragging.x) < 4) return;
    const a = Math.min(dragging.f, fraction(e.clientX)), b = Math.max(dragging.f, fraction(e.clientX));
    if (!brush) { brush = document.createElement("div"); brush.className = "range-brush"; brush.innerHTML = "<i></i><i></i>"; plot.appendChild(brush); }
    brush.style.left = a * 100 + "%"; brush.style.width = (b - a) * 100 + "%";
    if (!tip) { tip = document.createElement("div"); tip.className = "tip range-tip"; plot.appendChild(tip); }
    tip.innerHTML = rangeTip(v.start + a * (v.end - v.start), v.start + b * (v.end - v.start));
    tip.style.left = Math.max(0, Math.min(a * plot.clientWidth - tip.offsetWidth - 12, plot.clientWidth - tip.offsetWidth)) + "px";
    tip.style.top = "22%";
  });
  const release = (e) => {
    if (!pointers.has(e.pointerId)) return;
    pointers.delete(e.pointerId);
    if (plot.hasPointerCapture(e.pointerId)) plot.releasePointerCapture(e.pointerId);
    if (pointers.size) { dragging = { ...dragging, x: [...pointers.values()][0], window: pending, distance: 0 }; return; }
    const touch = dragging?.touch; dragging = null;
    plot.classList.remove("selecting");
    if (touch) change(pending);
    else if (brush && e.type !== "pointercancel") plot.classList.add("range-selected");
    else clearRange();
  };
  plot.addEventListener("pointerup", release);
  plot.addEventListener("pointercancel", release);
  // Moving to the toolbar releases a selection so polling and tab changes work.
  plot.addEventListener("pointerleave", () => { if (!dragging) clearRange(); });
}

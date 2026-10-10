// One time controller for every chart with a time x axis. A group is a set of
// plots that share a window, a crosshair and a range selection: every panel
// on a Telemetry view, the Overview strip, the Overview timeline, the Account
// charts. The same gestures, keys and visuals work on all of them.
//
// Gestures: pinch or Ctrl/Cmd plus wheel zooms at the pointer; Shift plus
// wheel or a sideways wheel pans; a plain vertical wheel scrolls the page. A
// mouse drag selects a range; its edges have handles that resize it, a drag
// inside it moves it, a click outside clears it. On touch one finger pans
// sideways, a long press then drag selects, two fingers zoom.
import { S, clock, day, dayKey, esc, prefs, setPref } from "../core.js";

export const HOUR = 3600e3, DAY = 24 * HOUR, WEEK = 7 * DAY;
export const MIN_SPAN = HOUR, MAX_SPAN = 366 * DAY;
const LONG_PRESS = 400;
const DRAG_PX = 4;
const HANDLE_PX = 8;

// ---------- window and range math ----------

const clamp = (v, lo, hi) => Math.max(lo, Math.min(hi, v));

// A window of at most MAX_SPAN and at least MIN_SPAN, kept centred when its
// span changes. Without future measures it may not run past now.
export function limitWindow(v, { now = Date.now(), future = true } = {}) {
  const span = clamp(v.end - v.start, MIN_SPAN, MAX_SPAN);
  let start = v.start + (v.end - v.start - span) / 2, end = start + span;
  if (!future && end > now) { start -= end - now; end = now; }
  return { start, end };
}

// Zooms by factor (below 1 zooms in) keeping the time under anchor (0..1) put.
export function zoomWindow(v, factor, anchor = 0.5) {
  const span = clamp((v.end - v.start) * factor, MIN_SPAN, MAX_SPAN);
  const at = v.start + (v.end - v.start) * anchor;
  return { start: at - span * anchor, end: at + span * (1 - anchor) };
}

// Moves the window by a fraction of its span, later for positive fractions.
export const panWindow = (v, fraction) => ({ start: v.start + (v.end - v.start) * fraction, end: v.end + (v.end - v.start) * fraction });

export const timeAt = (v, f) => v.start + f * (v.end - v.start);
export const fracOf = (v, t) => (t - v.start) / (v.end - v.start);

// The bucket boundary nearest t. grid: {bounds: sorted times} or {origin, step}.
// Past the ends of a bounds list the step between its last two entries repeats.
export function snapTime(t, grid) {
  if (!grid) return t;
  const b = grid.bounds;
  if (Array.isArray(b) && b.length > 1) {
    const first = b[0], last = b[b.length - 1];
    if (t <= first) { const st = b[1] - b[0]; return first - Math.round((first - t) / st) * st; }
    if (t >= last) { const st = last - b[b.length - 2]; return last + Math.round((t - last) / st) * st; }
    let lo = 0, hi = b.length - 1;
    while (hi - lo > 1) { const mid = (lo + hi) >> 1; if (b[mid] <= t) lo = mid; else hi = mid; }
    return t - b[lo] <= b[hi] - t ? b[lo] : b[hi];
  }
  if (grid.step > 0) {
    const o = grid.origin || 0;
    return o + Math.round((t - o) / grid.step) * grid.step;
  }
  return t;
}

// A range from two times in any order, snapped, never empty when a grid exists.
export function makeRange(a, b, grid) {
  let start = snapTime(Math.min(a, b), grid), end = snapTime(Math.max(a, b), grid);
  if (end <= start) end = grid ? nextBound(start, grid) : start + 60e3;
  return { start, end };
}

function nextBound(t, grid) {
  const b = grid?.bounds;
  if (Array.isArray(b) && b.length > 1) {
    const i = b.findIndex((x) => x > t);
    return i >= 0 ? b[i] : t + (b[b.length - 1] - b[b.length - 2]);
  }
  return t + (grid?.step || 60e3);
}

function prevBound(t, grid) {
  const b = grid?.bounds;
  if (Array.isArray(b) && b.length > 1) {
    for (let i = b.length - 1; i >= 0; i--) if (b[i] < t) return b[i];
    return t - (b[1] - b[0]);
  }
  return t - (grid?.step || 60e3);
}

// Drags one edge of a range to time t; the other edge stays fixed. The range
// keeps at least one bucket. The edges swap only once the pointer is more
// than half a bucket past the fixed edge, and swap back only once it is half
// a bucket back on the first side, so small moves near the edge never flip
// it. The returned edge says which one the pointer now holds.
export function resizeRange(r, edge, t, grid) {
  const fixed = edge === "start" ? r.end : r.start;
  const after = nextBound(fixed, grid), before = prevBound(fixed, grid);
  const snapped = snapTime(t, grid);
  if (edge === "start") {
    if (t <= fixed + (after - fixed) / 2) return { range: { start: Math.min(snapped, before), end: fixed }, edge };
    return { range: { start: fixed, end: Math.max(snapped, after) }, edge: "end" };
  }
  if (t >= fixed - (fixed - before) / 2) return { range: { start: fixed, end: Math.max(snapped, after) }, edge };
  return { range: { start: Math.min(snapped, before), end: fixed }, edge: "start" };
}

// Moves a range by dt keeping its length, with its start snapped.
export function moveRange(r, dt, grid) {
  const len = r.end - r.start;
  const start = snapTime(r.start + dt, grid);
  return { start, end: start + len };
}

// ---------- time zone aware ticks and labels ----------

// Milliseconds the proxy's time zone is ahead of UTC at t.
export function tzOffset(t) {
  const tz = S.data?.summary?.timezone;
  try {
    const p = new Intl.DateTimeFormat("en-GB", { timeZone: tz || undefined, hourCycle: "h23", year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit" }).formatToParts(new Date(t));
    const g = (k) => Number(p.find((x) => x.type === k)?.value);
    return Date.UTC(g("year"), g("month") - 1, g("day"), g("hour") % 24, g("minute"), g("second")) - Math.floor(t / 1000) * 1000;
  } catch (e) { return -new Date(t).getTimezoneOffset() * 60e3; }
}

const TICK_STEPS = [HOUR, 2 * HOUR, 3 * HOUR, 6 * HOUR, 12 * HOUR, DAY, 2 * DAY, 3 * DAY, 7 * DAY, 14 * DAY, 30 * DAY, 61 * DAY, 91 * DAY];
const dayMonth = (t) => day(t).split(" ").slice(1).join(" "); // "28 Sep"

// The calendar day n days after a YYYY-MM-DD key.
export function addDays(key, n) {
  const [y, m, d] = key.split("-").map(Number);
  return new Date(Date.UTC(y, m - 1, d + n)).toISOString().slice(0, 10);
}

// 00:00 on a calendar day in the proxy's time zone.
export function midnight(key) {
  const [y, m, d] = key.split("-").map(Number);
  let t = Date.UTC(y, m - 1, d);
  for (let k = 0; k < 3; k++) {
    const dk = dayKey(t);
    const [hh, mm] = clock(t).split(":").map(Number);
    let off = hh * 60 + mm;
    if (dk < key) off -= 1440;
    else if (dk > key) off += 1440;
    if (!off) break;
    t -= off * 60e3;
  }
  return t;
}

const dayNumber = (key) => { const [y, m, d] = key.split("-").map(Number); return Date.UTC(y, m - 1, d) / DAY; };

// Ticks at whole local hours, midnights, Mondays or month starts across a
// window, at most maxTicks. Hour ticks name the hour and a midnight names its
// day; longer steps name the day.
export function timeTicks(v, maxTicks = 8) {
  const span = v.end - v.start;
  const step = TICK_STEPS.find((s) => span / s <= maxTicks) || TICK_STEPS[TICK_STEPS.length - 1];
  const out = [];
  const push = (t, text) => { if (t > v.start && t < v.end) out.push({ t, text }); };
  if (step < DAY) {
    const mod = (t) => (((t + tzOffset(t)) % step) + step) % step;
    let t = v.start - mod(v.start);
    for (let guard = 0; t <= v.end && guard < 200; guard++) {
      push(t, clock(t) === "00:00" ? dayMonth(t) : clock(t));
      // Re-align after a clock change so ticks stay on whole local hours.
      let n = t + step;
      const r = mod(n);
      if (r) n += r < step / 2 ? -r : step - r;
      t = n > t ? n : t + step;
    }
    return out;
  }
  const days = Math.round(step / DAY);
  let key = dayKey(v.start);
  for (let guard = 0; guard < 800; guard++, key = addDays(key, 1)) {
    const t = midnight(key);
    if (t > v.end) break;
    const dn = dayNumber(key);
    const on = days >= 30 ? key.endsWith("-01") && (Number(key.slice(5, 7)) - 1) % Math.round(days / 30) === 0
      : days === 7 || days === 14 ? (dn + 3) % days === 0 : dn % days === 0;
    if (on) push(t, dayMonth(t));
  }
  return out;
}

// "Mon 5 Oct" style day of a time, without the weekday: "5 Oct".
const dm = (t) => dayMonth(t);
const sameMonth = (a, b) => day(a).split(" ")[2] === day(b).split(" ")[2];

// "5 to 12 Oct", "28 Sep to 5 Oct", or "1 Oct 14:00 to 8 Oct 14:00" when the
// window ends now or is shorter than three days ("9 Oct 04:00 to 12:00" within a day).
export function windowText(v, now = Date.now()) {
  const timed = Math.abs(v.end - now) < 90e3 || v.end - v.start < 3 * DAY;
  if (timed) return dayKey(v.start) === dayKey(v.end) ? `${dm(v.start)} ${clock(v.start)} to ${clock(v.end)}` : `${dm(v.start)} ${clock(v.start)} to ${dm(v.end)} ${clock(v.end)}`;
  return spanDays(v.start, v.end);
}

function spanDays(a, b) {
  if (dayKey(a) === dayKey(b)) return dm(a);
  return sameMonth(a, b) ? `${dm(a).split(" ")[0]} to ${dm(b)}` : `${dm(a)} to ${dm(b)}`;
}

export function spanText(ms) {
  const hours = ms / HOUR;
  const amount = (n, unit) => `${Number(n.toFixed(1))} ${unit}${Math.abs(n - 1) < 0.05 ? "" : "s"}`;
  if (Math.abs(hours - 168) < 0.1) return "1 week";
  if (hours < 48) return amount(hours, "hour");
  return amount(hours / 24, "day");
}

// A selection's end: "12 Oct" at midnight, else "9 Oct 06:00".
export const endText = (t) => (clock(t) === "00:00" ? dm(t) : `${dm(t)} ${clock(t)}`);

// "9 to 12 Oct, 3 days" or "8 Oct 14:00 to 18:00, 4 hours".
export function rangeText(r) {
  const len = r.end - r.start;
  const whole = (t) => clock(t) === "00:00";
  let text;
  if (len >= DAY && whole(r.start) && whole(r.end)) text = spanDays(r.start, r.end);
  else if (dayKey(r.start) === dayKey(r.end)) text = `${dm(r.start)} ${clock(r.start)} to ${clock(r.end)}`;
  else text = `${dm(r.start)} ${clock(r.start)} to ${dm(r.end)} ${clock(r.end)}`;
  return `${text}, ${spanText(len)}`;
}

// ---------- shared plot geometry ----------

// Every plot leaves the same gutters, so time lines up across stacked panels.
export const GUTTER_L = 42, GUTTER_R = 12;

// The plot width a panel will get, for laying out labels before it is mounted.
export function plotWidth() {
  if (typeof document === "undefined") return 1096;
  const main = document.getElementById("main")?.clientWidth || 1200;
  const pad = window.innerWidth <= 860 ? 32 : 48;
  return Math.max(120, main - 2 - pad - GUTTER_L - GUTTER_R);
}

// ---------- a page's time state ----------

// A window and selection kept per page in S.ui.time[key], so they survive a
// re-render and a refresh. The window is null while it follows its default,
// which moves with now. loads: whether a new window needs new data.
// A stored window may run to now (toNow): from its start to now, or to its
// own end once now passes it. It keeps its start even when that leaves it
// shorter than the shortest zoom, as clicking today at 00:30 should show
// today, not the last hour.
export function upToNow(w, now = Date.now()) {
  return { start: w.start, end: Math.max(Math.min(w.end, now), Math.min(w.start + 60e3, w.end)) };
}

export function timeState(key, { defaultWindow, future = false, loads = false, now = Date.now() }) {
  const all = (S.ui.time ||= {});
  const st = (all[key] ||= { win: null, range: null });
  const def = () => limitWindow(defaultWindow(now), { now, future });
  const stored = (w) => (w.toNow && !future ? upToNow(w, now) : limitWindow(w, { now, future }));
  const changed = () => {
    window.dispatchEvent(new Event("dash:render"));
    if (loads) window.dispatchEvent(new Event("dash:usage-window"));
  };
  return {
    key,
    future,
    window: st.win ? stored(st.win) : def(),
    range: st.range,
    moved: !!st.win,
    defaultWindow: def,
    setWindow(w) {
      const d = def();
      // Back at the default (Home, or zoomed and panned back): follow now again.
      const home = Math.abs(w.start - d.start) < 60e3 && Math.abs(w.end - d.end) < 60e3;
      st.win = home ? null : { start: w.start, end: w.end, ...(w.toNow ? { toNow: true } : {}) };
      changed();
    },
    setRange(r) {
      st.range = r ? { start: r.start, end: r.end } : null;
      window.dispatchEvent(new Event("dash:render"));
    },
    reset() { st.win = null; st.range = null; changed(); },
  };
}

// Forgets a page's window and selection, as when it opens on another view.
export function forgetTime(key) { if (S.ui.time) delete S.ui.time[key]; }

// ---------- zoom hint ----------

export const hintShown = () => !prefs().zoomHintDone;
const hintDone = () => { if (!prefs().zoomHintDone) setPref("zoomHintDone", true); };

// ---------- the controller ----------

// group: {
//   key           unique per page, names the plots: [data-tplot="key"]
//   window        the window drawn now
//   range         {start, end} or null
//   grid          bucket boundaries for snapping, or null
//   future        whether the window may run past now
//   defaultWindow () => window for Home and Back to now
//   setWindow     (window) => void, stores it and redraws
//   setRange      (range or null) => void, stores it and redraws
//   probe         (t, plotEl) => {chip, dots: [{y, color}], card, chipTop} or null, per plot
// }
// Focus taken by a click shows no ring, even after a redraw restores it;
// the first key pressed brings the ring back.
let pointerFocus = false;
if (typeof document !== "undefined") {
  document.addEventListener("keydown", () => {
    pointerFocus = false;
    document.querySelectorAll(".ptr").forEach((el) => el.classList.remove("ptr"));
  }, true);
}

// Plots are any elements with data-tplot; their width is the time axis.
export function bindTime(root, group) {
  const plots = [...root.querySelectorAll(`[data-tplot="${group.key}"]`)];
  if (!plots.length) return () => {};
  let win = group.window, range = group.range ? { ...group.range } : null;
  let held = false, hoverPlot = null, drag = null, frame = 0, pendingWin = null;
  const pointers = new Map();
  const hold = (on) => {
    if (on && !held) { S.hold++; held = true; }
    if (!on && held) { S.hold = Math.max(0, S.hold - 1); held = false; }
  };
  const fracAt = (el, x) => { const r = el.getBoundingClientRect(); return r.width ? (x - r.left) / r.width : 0; };
  const timeOf = (el, x) => timeAt(win, fracAt(el, x));

  // ----- range overlays, drawn the same on every plot -----
  const overlays = plots.map((p) => {
    const o = document.createElement("div");
    o.className = "trange";
    o.hidden = true;
    o.innerHTML = `<i class="trange-h" data-edge="start"><b></b></i><i class="trange-h" data-edge="end"><b></b></i>`;
    p.appendChild(o);
    return o;
  });
  const focusRange = overlays[0];
  focusRange.tabIndex = 0;
  focusRange.setAttribute("role", "slider");
  focusRange.dataset.focusKey = group.key + ":range";
  focusRange.classList.toggle("ptr", pointerFocus);
  const drawRange = (r) => {
    for (const o of overlays) {
      if (!r) { o.hidden = true; continue; }
      const a = fracOf(win, r.start), b = fracOf(win, r.end);
      const l = clamp(a, -0.01, 1.01), w = clamp(b, -0.01, 1.01) - l;
      o.hidden = w <= 0;
      o.style.left = (l * 100).toFixed(3) + "%";
      o.style.width = (w * 100).toFixed(3) + "%";
      o.classList.toggle("cut-l", a < 0);
      o.classList.toggle("cut-r", b > 1);
    }
    if (r) focusRange.setAttribute("aria-label", "Selection, " + rangeText(r) + ". Arrow keys move it, Shift with arrows resizes it, Escape clears it.");
  };
  drawRange(range);

  // ----- crosshair -----
  let marks = [], hidden = [];
  const unhide = () => { for (const el of hidden) el.classList.remove("under-chip"); hidden = []; };
  const clearHover = () => {
    for (const m of marks) m.remove();
    marks = [];
    unhide();
    hoverPlot = null;
    hold(false);
  };
  const showHover = (t, over) => {
    for (const m of marks) m.remove();
    marks = [];
    unhide();
    if (t < win.start || t > win.end) { hoverPlot = null; return hold(false); }
    hold(true);
    hoverPlot = over;
    const f = fracOf(win, t);
    for (const p of plots) {
      const info = group.probe ? group.probe(t, p, p === over) : null;
      const add = (el) => { p.appendChild(el); marks.push(el); return el; };
      const line = add(document.createElement("div"));
      line.className = "tx-line";
      line.style.left = (f * 100).toFixed(3) + "%";
      if (!info) continue;
      for (const d of info.dots || []) {
        const dot = add(document.createElement("i"));
        dot.className = "tx-dot";
        dot.style.cssText = `left:${(f * 100).toFixed(3)}%;top:${((1 - d.y) * 100).toFixed(2)}%;background:${d.color}`;
      }
      const w = p.clientWidth, x = f * w;
      if (info.chip != null && info.chip !== "") {
        const chip = add(document.createElement("div"));
        chip.className = "tx-chip";
        chip.textContent = info.chip;
        chip.style.top = info.chipTop != null ? info.chipTop + "px" : `calc(100% - 22px)`;
        const cw = chip.offsetWidth;
        chip.style.left = (x + 8 + cw > w ? x - 8 - cw : x + 8) + "px";
        // A label the chip lands on hides until the crosshair moves on.
        const cr = chip.getBoundingClientRect();
        for (const el of p.querySelectorAll("[data-chip-avoid]")) {
          const r = el.getBoundingClientRect();
          if (r.right > cr.left && r.left < cr.right && r.bottom > cr.top && r.top < cr.bottom) { el.classList.add("under-chip"); hidden.push(el); }
        }
      }
      if (p === over && info.card) {
        const card = add(document.createElement("div"));
        card.className = "tcard";
        card.innerHTML = info.card;
        const cw = card.offsetWidth, ch = card.offsetHeight;
        card.style.left = (x + 12 + cw > w ? Math.max(0, x - 12 - cw) : x + 12) + "px";
        // Kept inside the group's root, so a clipping scroll box never cuts it.
        const pr = p.getBoundingClientRect(), rr = root.getBoundingClientRect();
        let top = info.cardTop ?? 12;
        if (pr.top + top + ch > rr.bottom) top = Math.min(top, -ch - 6 >= rr.top - pr.top ? -ch - 6 : rr.bottom - pr.top - ch);
        card.style.top = top + "px";
      }
    }
  };

  // ----- window changes -----
  const commitWindow = (next) => {
    win = limitWindow(next, { future: group.future });
    pendingWin = win;
    clearHover();
    // One redraw for a burst of wheel events. A timer, not an animation
    // frame, so a page in a background tab still settles.
    clearTimeout(frame);
    frame = setTimeout(() => {
      frame = 0;
      if (!plots[0].isConnected || pointers.size) return;
      group.setWindow(pendingWin);
    }, 16);
  };
  const zoomAt = (factor, anchor) => { hintDone(); commitWindow(zoomWindow(win, factor, anchor)); };

  for (const p of plots) {
    p.tabIndex = p === plots[0] ? 0 : -1;
    p.dataset.focusKey = group.key + ":plot";
    p.setAttribute("role", "application");
    p.setAttribute("aria-label", "Time graph. Pinch or Ctrl and scroll to zoom, Shift and scroll to pan, drag to select a range. Arrow keys pan, plus and minus zoom, Home goes back to now, Escape clears the selection.");
    p.classList.add("tplot");
    p.classList.toggle("ptr", pointerFocus);

    p.addEventListener("wheel", (e) => {
      const scale = e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? p.clientWidth : 1;
      if (e.ctrlKey || e.metaKey) {
        e.preventDefault();
        // A mouse wheel notch (about 100) zooms by a fifth; a pinch sends many small steps.
        zoomAt(Math.exp(clamp(e.deltaY * scale * 0.002, -0.5, 0.5)), clamp(fracAt(p, e.clientX), 0, 1));
      } else if (e.shiftKey || Math.abs(e.deltaX) > Math.abs(e.deltaY)) {
        e.preventDefault();
        const d = (Math.abs(e.deltaX) > Math.abs(e.deltaY) ? e.deltaX : e.deltaY) * scale;
        commitWindow(panWindow(win, d / Math.max(1, p.clientWidth)));
      }
      // A plain vertical wheel is left alone so the page scrolls.
    }, { passive: false });

    p.addEventListener("pointermove", (e) => {
      if (drag || pointers.size) return;
      if (e.pointerType === "touch") return;
      showHover(timeOf(p, e.clientX), p);
    });
    p.addEventListener("pointerleave", () => { if (!drag) clearHover(); });

    p.addEventListener("pointerdown", (e) => {
      if (e.button !== 0) return;
      if (e.target.closest(".tx-chip, .tcard, a, button")) return;
      clearHover();
      pointerFocus = true;
      for (const el of [...plots, focusRange]) el.classList.add("ptr");
      pointers.set(e.pointerId, { x: e.clientX, y: e.clientY });
      try { p.setPointerCapture(e.pointerId); } catch (err) { /* synthetic events */ }
      hold(true);
      const touch = e.pointerType === "touch";
      if (pointers.size === 2) {
        // Two fingers: pinch to zoom from where they started.
        clearTimeout(drag?.timer);
        const xs = [...pointers.values()].map((q) => q.x);
        drag = { mode: "pinch", plot: p, window: win, dist: Math.abs(xs[0] - xs[1]), mid: fracAt(p, (xs[0] + xs[1]) / 2) };
        return;
      }
      const t = timeOf(p, e.clientX);
      const handle = e.target.closest(".trange-h");
      if (handle && range) {
        drag = { mode: "resize", plot: p, edge: handle.dataset.edge, moved: false, x: e.clientX };
        p.classList.add("resizing");
        e.preventDefault();
        focusRange.focus({ preventScroll: true });
        return;
      }
      const inside = range && t >= range.start && t <= range.end;
      drag = { mode: inside ? "move" : touch ? "touch" : "select", plot: p, t0: t, x: e.clientX, y: e.clientY, moved: false, base: range, window: win };
      if (touch && !inside) {
        // A long press without moving starts a selection; moving first pans.
        drag.timer = setTimeout(() => { if (drag && drag.mode === "touch" && !drag.moved) { drag.mode = "select"; p.classList.add("selecting"); navigator.vibrate?.(10); } }, LONG_PRESS);
      }
      if (!touch) { e.preventDefault(); p.focus({ preventScroll: true }); }
    });

    p.addEventListener("pointermove", (e) => {
      if (!drag || !pointers.has(e.pointerId)) return;
      pointers.set(e.pointerId, { x: e.clientX, y: e.clientY });
      const dp = drag.plot;
      if (drag.mode === "pinch") {
        const xs = [...pointers.values()].map((q) => q.x);
        if (xs.length < 2 || !drag.dist) return;
        win = limitWindow(zoomWindow(drag.window, drag.dist / Math.max(1, Math.abs(xs[0] - xs[1])), drag.mid), { future: group.future });
        drawRange(range);
        shiftPlots(drag.window, win);
        return;
      }
      const dx = e.clientX - drag.x;
      if (!drag.moved && Math.abs(dx) < DRAG_PX && Math.abs(e.clientY - (drag.y ?? e.clientY)) < DRAG_PX) return;
      drag.moved = true;
      if (drag.mode === "touch") {
        clearTimeout(drag.timer);
        drag.mode = "pan";
      }
      if (drag.mode === "pan") {
        win = limitWindow(panWindow(drag.window, -dx / Math.max(1, dp.clientWidth)), { future: group.future });
        shiftPlots(drag.window, win);
        return;
      }
      const t = timeOf(dp, e.clientX);
      if (drag.mode === "select") {
        range = makeRange(drag.t0, t, group.grid);
        dp.classList.add("selecting");
      } else if (drag.mode === "move") {
        range = moveRange(drag.base, t - drag.t0, group.grid);
      } else if (drag.mode === "resize") {
        const res = resizeRange(range, drag.edge, t, group.grid);
        range = res.range;
        drag.edge = res.edge;
      }
      drawRange(range);
    });

    const release = (e) => {
      if (!pointers.has(e.pointerId)) return;
      pointers.delete(e.pointerId);
      try { if (p.hasPointerCapture(e.pointerId)) p.releasePointerCapture(e.pointerId); } catch (err) { /* already released */ }
      if (!drag) { if (!pointers.size) hold(false); return; }
      if (pointers.size) {
        // One finger of a pinch lifted: the other carries on as a pan.
        if (drag.mode === "pinch") { const q = [...pointers.values()][0]; drag = { mode: "pan", plot: p, x: q.x, y: q.y, moved: true, window: win }; }
        return;
      }
      clearTimeout(drag.timer);
      const d = drag;
      drag = null;
      for (const q of plots) q.classList.remove("selecting", "resizing");
      hold(false);
      const cancelled = e.type === "pointercancel";
      if (d.mode === "pan" || d.mode === "pinch") {
        unshiftPlots();
        if (!cancelled || d.mode === "pinch") commitWindow(win); else win = d.window;
        return;
      }
      if (cancelled) { range = group.range; drawRange(range); return; }
      if (!d.moved) {
        // A click outside the selection clears it; a click inside keeps it.
        if (d.mode === "select" || d.mode === "touch") { if (range) { range = null; drawRange(null); group.setRange(null); } }
        return;
      }
      if (d.mode === "select" || d.mode === "move" || d.mode === "resize") group.setRange(range);
    };
    p.addEventListener("pointerup", release);
    p.addEventListener("pointercancel", release);

    p.addEventListener("keydown", (e) => {
      if (e.target !== p) return;
      let next = null;
      if (e.key === "ArrowLeft" || e.key === "ArrowRight") next = panWindow(win, e.key === "ArrowLeft" ? -0.2 : 0.2);
      else if (e.key === "+" || e.key === "=") { hintDone(); next = zoomWindow(win, 0.7); }
      else if (e.key === "-" || e.key === "_") { hintDone(); next = zoomWindow(win, 1 / 0.7); }
      else if (e.key === "Home") next = group.defaultWindow();
      else if (e.key === "Escape" && range) { e.preventDefault(); range = null; drawRange(null); group.setRange(null); return; }
      if (next) { e.preventDefault(); commitWindow(next); }
    });
  }

  // Keys on the focused selection: arrows move it a bucket, Shift resizes its end.
  focusRange.addEventListener("keydown", (e) => {
    if (!range) return;
    const step = (group.grid ? nextBound(range.end, group.grid) - range.end : 0) || (win.end - win.start) / 20;
    let next = null;
    if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
      const dir = e.key === "ArrowLeft" ? -1 : 1;
      next = e.shiftKey ? resizeRange(range, "end", range.end + dir * step, group.grid).range : moveRange(range, dir * step, group.grid);
    } else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); range = null; drawRange(null); group.setRange(null); return; }
    if (next) { e.preventDefault(); e.stopPropagation(); range = next; drawRange(range); group.setRange(range); }
  });

  // While a touch pan or pinch is under way, the drawn content slides and
  // scales instead of redrawing, so the gesture stays smooth and keeps its pointer.
  function shiftPlots(from, to) {
    const scale = (from.end - from.start) / (to.end - to.start);
    for (const p of plots) {
      const w = p.clientWidth;
      const dx = ((from.start - to.start) / (to.end - to.start)) * w;
      for (const c of p.querySelectorAll(":scope > .tp-content")) { c.style.transformOrigin = "0 0"; c.style.transform = `translateX(${dx}px) scaleX(${scale})`; }
    }
    group.onPreview?.(to);
  }
  function unshiftPlots() {
    for (const p of plots) for (const c of p.querySelectorAll(":scope > .tp-content")) c.style.transform = "";
  }

  return clearHover;
}

// The muted pill on the first panel until the viewer has zoomed once.
export const zoomHint = () => (hintShown() ? `<span class="zoomhint">Pinch or Ctrl-scroll to zoom</span>` : "");

// "Back to now" for a group's header, when its window has moved.
export function backToNow(attr) {
  return `<button class="backnow" ${attr}><svg width="14" height="14" viewBox="0 0 24 24" aria-hidden="true"><path d="M17 12H3" fill="none" stroke="var(--icon)" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/><path d="m11 18 6-6-6-6" fill="none" stroke="var(--icon)" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/><path d="M21 5v14" fill="none" stroke="var(--icon)" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg><span>Back to now</span></button>`;
}

// "Selection, 9 to 12 Oct, 3 days" with a Clear link, for a header or table head.
export const selectionLabel = (r, attr = "data-clear-range") => `<span class="sel-label"><span>Selection, ${esc(rangeText(r))}</span><button class="sel-clear" ${attr}>Clear</button></span>`;

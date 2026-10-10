// Redrawing part of a screen. A screen marks the parts that change with its
// time window or its data as regions while it builds its html. A redraw
// builds the screen again and replaces only the regions whose html changed,
// so the time plots, their controller, the scroll position, focus and open
// menus all stay put. A full render (main.innerHTML) is left for route
// changes and for redraws that change anything outside a region.
import { S, esc } from "./core.js";

let collecting = null;
const TOKEN = /\u0000(\d+)\u0000/g;

// A region: an element with data-region="key" holding html. tag and attrs
// make it the element the layout needs (a plot's .tp-content, a display:
// contents wrapper). Keys are unique per screen. While a screen is collected
// it returns a placeholder, so the rest of the page (its skeleton) can be
// compared without the regions in it.
export function region(key, html, tag = "div", attrs = "") {
  const open = `<${tag} data-region="${esc(key)}"${attrs ? " " + attrs : ""}>`, close = `</${tag}>`;
  if (!collecting) return open + html + close;
  collecting.push({ key, html, open, close });
  return `\u0000${collecting.length - 1}\u0000`;
}

// The html for a region that is not a time plot, such as a table: while a
// plot is being zoomed or panned it keeps what it shows and is not built, so
// each frame only redraws the plots and their axes; it catches up when the
// gesture settles (timeState redraws once then).
export const GESTURE_QUIET = 150;
export function kept(key, build) {
  const busy = Date.now() - (S.gestureAt || 0) < GESTURE_QUIET;
  if (busy && painted.regions?.has(key)) return painted.regions.get(key);
  return build();
}

// Runs fn (a screen's view) and adds to its result the page without its
// regions (skeleton) and the html of each region (regions, by key).
export function collectRegions(fn) {
  const prev = collecting;
  const list = (collecting = []);
  let out;
  try {
    out = fn();
  } finally {
    collecting = prev;
  }
  if (!out || typeof out.html !== "string") return out;
  const regions = new Map();
  let clash = false;
  const expand = (s) => s.replace(TOKEN, (_, i) => {
    const r = list[Number(i)];
    const inner = expand(r.html);
    if (regions.has(r.key)) clash = true;
    regions.set(r.key, inner);
    return r.open + inner + r.close;
  });
  out.skeleton = out.html;
  out.html = expand(out.html);
  // Two regions with one key cannot be told apart: such a screen always
  // renders in full.
  if (clash) out.skeleton = null;
  out.regions = regions;
  return out;
}

// Replaces the regions under root whose html differs from last time. Returns
// the number replaced, or -1 when the screen's regions changed (another set
// of keys, or a region missing from the page), when nothing was touched and
// a full render is needed.
export function patchRegions(root, prev, next) {
  if (!prev || !next || prev.size !== next.size) return -1;
  for (const k of next.keys()) if (!prev.has(k)) return -1;
  const els = new Map();
  for (const el of root.querySelectorAll("[data-region]")) els.set(el.dataset.region, el);
  const todo = [];
  for (const [k, html] of next) {
    const el = els.get(k);
    if (!el) return -1;
    if (html !== prev.get(k)) todo.push([el, html]);
  }
  for (const [el, html] of todo) el.innerHTML = html;
  return todo.length;
}

// What is on screen: the route it was drawn for, its skeleton and regions,
// and how many full renders and patches drew it.
export const painted = { key: null, skeleton: "", regions: null, extra: "", full: 0, patches: 0 };

// Draws a collected screen in full.
export function paintFull(main, key, out, extra = "") {
  main.innerHTML = out.html;
  Object.assign(painted, { key, skeleton: out.skeleton ?? null, regions: out.regions || null, extra });
  painted.full++;
}

// Patches a collected screen over the one on screen. false when that cannot
// be done: another route, anything outside the regions changed (extra is
// the page's own additions, such as an error banner), or the regions differ.
export function paintPatch(main, key, out, extra = "") {
  if (painted.key !== key || painted.skeleton == null || out.skeleton !== painted.skeleton || extra !== painted.extra) return false;
  const n = patchRegions(main, painted.regions, out.regions);
  if (n < 0) return false;
  painted.regions = out.regions;
  painted.patches++;
  return true;
}

// The events that redraw the dashboard: dash:patch redraws from the data
// loaded (a new window, a new reply), dash:render redraws in full, and
// dash:usage-window says a window that loads data moved.
export function listen(target, { patch, render, windowMoved }) {
  target.addEventListener("dash:patch", patch);
  target.addEventListener("dash:render", render);
  target.addEventListener("dash:usage-window", windowMoved);
}

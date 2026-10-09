// Overview timeline: each account's weekly allowance left over a window that
// opens on the start of yesterday to 30 days on, and zooms and pans like
// every other time graph. History comes from the allowance series, the rest
// is projected at the current burn: the weekly cycle repeats from each reset
// until the plan ends.
import {
  esc, icon, logo, warnIcon, clock, day, dayKey, weekdayTime, planDay, status, limits, left, plan, queue, providerTitle, validTime,
} from "../core.js";
import { allowanceSeries, trajectory } from "./burn.js";
import { accountColor } from "./common.js";
import { HOUR, DAY, addDays, midnight, fracOf, timeAt, timeTicks, timeState, bindTime, selectionLabel, backToNow } from "./timeaxis.js";
import { card } from "./tplot.js";

const WEEK = 7 * DAY;
const DAYS = 30;          // yesterday, today and 28 days ahead
const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
const KEY = "ovTimeline";

// The same day next month, or the month's last day when it is shorter.
function addMonth(key) {
  const [y, m, d] = key.split("-").map(Number);
  const last = new Date(Date.UTC(y, m + 1, 0)).getUTCDate();
  return new Date(Date.UTC(y, m, Math.min(d, last))).toISOString().slice(0, 10);
}

// The timeline's own window and selection.
export const timelineTime = () => timeState(KEY, {
  defaultWindow: (now) => { const first = addDays(dayKey(now), -1); return { start: midnight(first), end: midnight(addDays(first, DAYS)) }; },
  future: true,
});

// The window, and where a time falls across it.
function frame(v) {
  const x = (t) => Math.max(0, Math.min(1, fracOf(v, t)));
  return { start: v.start, end: v.end, x, at: (f) => timeAt(v, f) };
}

// Midnights across a window and a day either side, for snapping a selection.
function dayBounds(v) {
  const out = [];
  for (let k = addDays(dayKey(v.start), -1), g = 0; g < 400; g++, k = addDays(k, 1)) {
    const t = midnight(k);
    out.push(t);
    if (t > v.end) break;
  }
  return out;
}

// ---------- one account ----------

export function model(a, now, fr) {
  const st = status(a);
  const w = limits(a).week;
  const ser = allowanceSeries(a.id, true);
  const tr = trajectory(ser, now);
  const p = plan(a);
  const m = { a, st, kind: st.kind, plan: p, pts: [], outs: [], resets: [], now: null, drawEnd: 0, hold: null };
  // The plan runs through the whole of its last day: this is its one end boundary.
  m.endAt = p.ends ? midnight(addDays(p.ends, 1)) : Infinity;
  if (st.kind === "off" || st.kind === "blocked" || st.kind === "error") return m;
  // When the account is held out from now: until its retry time, or with no
  // known recovery time, for the rest of the window. This limits availability
  // only; the allowance line still comes from the weekly meter.
  const until = st.until > now ? st.until : 0;
  if (st.kind === "limited") m.hold = [now, until || Infinity];
  else if (st.kind === "usedup" && until) m.hold = [now, until];

  // History: the weekly series inside the window, up to now.
  const s0 = ser ? Date.parse(validTime(ser.start)) || 0 : 0;
  const step = ser ? (Number(ser.step_seconds) || 0) * 1000 : 0;
  if (s0 && step) {
    (ser.used || []).forEach((u, i) => {
      const t = s0 + i * step;
      if (u != null && t >= fr.start - step && t < now) m.pts.push({ t, v: Math.max(0, 100 - u / 10) });
    });
  }
  // Allowance left now, from the weekly meter or the series. A status of used
  // up does not zero it: a long retry can come with allowance still left.
  const v0 = w ? (w.notStarted ? 100 : left(w)) : tr ? tr.leftNow : null;
  if (v0 == null) { m.kind = "noreading"; m.pts = []; return m; }

  // Stretches already at 0% before now.
  let run = null;
  for (const q of m.pts) {
    if (q.v <= 0) { if (!run) run = [q.t, now]; }
    else if (run) { run[1] = q.t; m.outs.push(run); run = null; }
  }
  if (run) m.outs.push(run);

  // The projection: down at the current rate, back to 100% at each weekly
  // reset. A retry time only decides when the account can be used again
  // (see available), it never refills the week.
  const rate = w?.notStarted ? 0 : tr && tr.rate != null ? tr.rate : w?.burn || 0; // percent an hour
  let R = w?.reset || tr?.reset || 0;
  if (w?.notStarted || R <= now) R = 0;
  m.now = v0;
  m.pts.push({ t: now, v: v0 });
  const stopAll = Math.min(fr.end, m.endAt);
  let t0 = now, v = v0;
  for (let guard = 0; guard < 12 && t0 < stopAll; guard++) {
    const stop = Math.min(R || Infinity, stopAll);
    const out = v <= 0 ? t0 : rate > 0 ? t0 + (v / rate) * HOUR : Infinity;
    if (out < stop) {
      if (out > t0) m.pts.push({ t: out, v: 0 });
      m.outs.push([out, stop]);
    }
    m.pts.push({ t: stop, v: Math.max(0, v - (rate * (stop - t0)) / HOUR) });
    // A reset refills the week, unless the plan has ended by then.
    if (!R || stop !== R || R >= m.endAt) { m.drawEnd = stop; break; }
    m.resets.push(R);
    m.pts.push({ t: R, v: 100 });
    t0 = R; v = 100; R += WEEK;
  }
  if (!m.drawEnd) m.drawEnd = stopAll;
  // Merge touching stretches.
  m.outs.sort((x, y) => x[0] - y[0]);
  m.outs = m.outs.reduce((acc, o) => {
    const last = acc[acc.length - 1];
    if (last && o[0] <= last[1] + 60e3) last[1] = Math.max(last[1], o[1]);
    else acc.push([...o]);
    return acc;
  }, []);
  return m;
}

// Percent left at time t, or null outside what is drawn.
export function valueAt(m, t) {
  const p = m.pts;
  if (!p.length || t < p[0].t || t > m.drawEnd) return null;
  let j = 0;
  while (j < p.length - 1 && p[j + 1].t <= t) j++;
  if (j === p.length - 1) return p[j].v;
  const a = p[j], b = p[j + 1];
  return b.t === a.t ? b.v : a.v + ((b.v - a.v) * (t - a.t)) / (b.t - a.t);
}

const held = (m, t) => !!m.hold && t >= m.hold[0] && t < m.hold[1];

// Can the account take a session at time t? The account's status decides
// first: off, blocked or in error never; resting or unavailable not until its
// known recovery time. Then the allowance left, when there is a reading.
export function available(m, t, now) {
  const k = m.st.kind;
  if (k === "off" || k === "blocked" || k === "error" || t >= m.endAt) return false;
  if (t >= now && held(m, t)) return false;
  if (m.kind === "noreading") return true;
  const v = valueAt(m, t);
  if (v == null) return t < now ? k === "ready" : false;
  return v > 0;
}

// The warning for a hold: resting until a time while allowance is left,
// used up when there is none, or unavailable with no recovery time.
export function holdText(m, now) {
  const to = m.hold[1];
  if (to === Infinity) return m.st.text || "Unavailable";
  if (m.now == null || m.now <= 0) return m.st.kind === "usedup" || m.now === 0 ? "Used up" : m.st.text;
  return "Resting until " + (to - now < 6 * 864e5 ? weekdayTime(to) : day(to) + " " + clock(to));
}

// ---------- drawing ----------

const pc = (f) => (f * 100).toFixed(3) + "%";
const FILL = { "var(--chart-1)": 0.12, "var(--chart-p99)": 0.12, "var(--chart-p90)": 0.16, "var(--chart-p50)": 0.35 };

function nameCell(a, color) {
  const e = String(a.email || a.id);
  const at = e.indexOf("@");
  const [user, host] = at > 0 ? [e.slice(0, at), e.slice(at)] : [e, ""];
  return `<div class="lab"><span class="sw"><i class="sq" style="background:${color}"></i></span><span class="nm"><b>${esc(user)}</b>${host ? `<span>${esc(host)}</span>` : ""}</span></div>`;
}

const lab = (x, html, cls = "") => `<span class="tl-lab ${cls}" style="left:${pc(x)}">${html}</span>`;
const warnLab = (x, text) => lab(x, `${warnIcon(12)}<span>${esc(text)}</span>`, "warn");

function renewals(p, fr) {
  if (!p.renews || p.ends) return [];
  let r = p.renews;
  for (let k = 0; k < 120 && midnight(r) < fr.start; k++) r = addMonth(r);
  const out = [];
  for (let k = 0; k < 3 && midnight(r) < fr.end; k++) { out.push(r); r = addMonth(r); }
  return out;
}

function rowHtml(m, color, fr, now, idx) {
  const X = (t) => (fr.x(t) * 1000).toFixed(2);
  const Y = (v) => (44 - (Math.max(0, Math.min(100, v)) / 100) * 24).toFixed(2);
  let svg = `<path d="M0 44 L1000 44" stroke="var(--chart-grid)" />`;
  let over = "";
  // Clip the points to the window.
  let pts = m.pts.filter((q) => q.t >= fr.start);
  if (m.pts.length && m.pts[0].t < fr.start && pts.length) {
    const v = valueAt(m, fr.start);
    if (v != null) pts = [{ t: fr.start, v }, ...pts];
  }
  // Warning bands: stretches at 0% and the hold, merged so overlaps are not drawn twice.
  const bands = [...m.outs, ...(m.hold ? [[m.hold[0], Math.min(m.hold[1], fr.end, m.endAt)]] : [])].sort((x, y) => x[0] - y[0]);
  const merged = [];
  for (const [a, b] of bands) {
    const last = merged[merged.length - 1];
    if (last && a <= last[1]) last[1] = Math.max(last[1], b);
    else merged.push([a, b]);
  }
  for (const [a, b] of merged) {
    const x0 = fr.x(Math.max(a, fr.start)), x1 = fr.x(Math.min(b, fr.end));
    if (x1 <= x0) continue;
    over += `<i class="tl-out" style="left:${pc(x0)};width:${pc(x1 - x0)}"></i>`;
  }
  if (pts.length) {
    const sw = color === "var(--chart-p50)" ? 2 : 1.5;
    const line = (list) => list.map((q, i) => `${i ? "L" : "M"}${X(q.t)} ${Y(q.v)}`).join(" ");
    const fill = `M${X(pts[0].t)} 44 L${line(pts).slice(1)} L${X(pts[pts.length - 1].t)} 44 Z`;
    svg += `<path class="fill" d="${fill}" fill="${color}" fill-opacity="${FILL[color] ?? 0.16}" />`;
    const past = pts.filter((q) => q.t <= now), future = pts.filter((q) => q.t >= now);
    if (past.length > 1) svg += `<path d="${line(past)}" stroke="${color}" stroke-width="${sw}" stroke-linecap="round" stroke-linejoin="round" />`;
    if (future.length > 1) svg += `<path d="${line(future)}" stroke="${color}" stroke-width="${sw}" stroke-linejoin="round" stroke-dasharray="4 3" />`;
    const outLine = m.outs.map(([a, b]) => `M${X(Math.max(a, fr.start))} 43 L${X(Math.min(b, fr.end))} 43`).join(" ");
    if (outLine) svg += `<path d="${outLine}" stroke="var(--warn)" stroke-width="2" />`;
  }
  if (m.now != null) over += `<i class="tl-dot" style="left:${pc(fr.x(now))};top:${Y(m.now)}px;background:${color}"></i>`;

  // Labels: the state, the next reset, the plan's end, then renewals.
  const k = m.kind;
  const nowX = fr.x(now);
  if (k === "blocked") over += warnLab(nowX, "Sign-in blocked, no usage data");
  else if (k === "error") over += warnLab(nowX, m.st.text || "Error");
  else if (k === "off") over += lab(nowX, "Off, never used");
  else if (k === "noreading") over += m.hold ? warnLab(nowX, holdText(m, now)) : lab(nowX, "No reading yet");
  else {
    const cur = m.outs.find(([a, b]) => a <= now && b > now);
    const next = m.outs.find(([a]) => a > now && a < fr.end);
    if (m.hold) over += warnLab(nowX, holdText(m, now));
    else if (cur) over += warnLab(nowX, k === "usedup" ? "Used up" : "Out now");
    if (!cur && next) over += warnLab(fr.x(next[0]), "Out " + (next[0] - now < 6 * 864e5 ? weekdayTime(next[0]) : day(next[0])));
    const r = m.resets.find((t) => t > now && t < fr.end);
    if (r) over += lab(fr.x(r), `${icon("reset", 12)}<span>Resets ${esc(r - now < 6 * 864e5 ? weekdayTime(r) : day(r) + " " + clock(r))}</span>`);
  }
  if (m.plan.ends && m.endAt <= fr.end) {
    const at = Math.max(now, m.endAt);
    if (at >= fr.start && at <= fr.end) over += lab(fr.x(at), `Plan ends ${esc(planDay(m.plan.ends))}`);
  }
  for (const r of renewals(m.plan, fr)) {
    const x = fr.x(midnight(r));
    const text = "Renews " + planDay(r).split(" ").slice(1).join(" ");
    over += `<span class="tl-mark ${x > 0.86 ? "flip" : ""}" style="left:${pc(x)}"><span>${icon("card", 12, "var(--fg)")}${esc(text)}</span></span>`;
  }
  return `<div class="tl-row">${nameCell(m.a, color)}<div class="area" data-tl-row data-row="${idx}" data-tplot="${KEY}"><div class="tp-content"><svg class="tlsvg" viewBox="0 0 1000 48" preserveAspectRatio="none" aria-hidden="true">${svg}</svg>${over}</div></div></div>`;
}

function stripHtml(models, fr, now) {
  // Half-hour steps or finer than 600 across the window, split at now so a
  // hold that starts now starts there.
  const step = Math.max(30 * 60e3, (fr.end - fr.start) / 600);
  const cuts = [];
  for (let t = fr.start; t < fr.end; t += step) cuts.push(t);
  if (now > fr.start && now < fr.end && !cuts.includes(now)) cuts.push(now);
  cuts.sort((a, b) => a - b);
  const segs = [];
  cuts.forEach((t, i) => {
    const to = Math.min(fr.end, cuts[i + 1] ?? fr.end);
    const n = models.filter((m) => available(m, Math.min(fr.end - 1, (t + to) / 2), now)).length;
    const last = segs[segs.length - 1];
    if (last && last.n === n) last.to = to;
    else segs.push({ from: t, to, n });
  });
  const max = Math.max(0, ...segs.map((s) => s.n));
  const total = models.length;
  // None available stands apart from fewer than usual.
  return segs.map((s) => {
    const x0 = fr.x(s.from), x1 = fr.x(s.to);
    const cls = s.n === 0 ? "none" : s.n < max ? "low" : "";
    const data = `data-n="${s.n}" data-total="${total}" data-from="${s.from}" data-to="${s.to}"`;
    return `<div class="${cls}" style="left:${pc(x0)};width:${pc(x1 - x0)}" ${data}><span>${s.n === 0 ? `${warnIcon(12)}<b>None</b>` : `${s.n} of ${total}`}</span></div>`;
  }).join("");
}

// The month and day row over the rows: a day number under every day while
// the window is short enough to name each, else the shared tick labels.
function calendarHtml(fr, v, now) {
  let months = "", days = "";
  const span = v.end - v.start;
  if (span <= 45 * DAY) {
    let lastMonth = "";
    for (let k = dayKey(v.start), g = 0; g < 50; g++, k = addDays(k, 1)) {
      const a = midnight(k), b = midnight(addDays(k, 1));
      if (a >= v.end) break;
      const x0 = fr.x(a), x1 = fr.x(b);
      const m = MONTHS[Number(k.slice(5, 7)) - 1];
      if (m !== lastMonth) { months += `<span class="m" style="left:${pc(x0)}">${m}</span>`; lastMonth = m; }
      const isNow = now >= a && now < b;
      if (x1 - x0 > 0.012) days += `<span style="left:${pc(x0)};width:${pc(x1 - x0)};${isNow ? "visibility:hidden" : ""}">${Number(k.slice(8))}</span>`;
    }
  } else {
    days = timeTicks(v, 10).map((tk) => `<span class="tk" style="left:${pc(fr.x(tk.t))}">${esc(tk.text)}</span>`).join("");
  }
  return `<div class="tl-cal"><div class="lab"></div><div class="area">${months}<div class="days">${days}</div></div></div>`;
}

function layersHtml(fr, v, now) {
  let weeks = "";
  if (v.end - v.start <= 45 * DAY) {
    for (let k = dayKey(v.start), g = 0; g < 50; g++, k = addDays(k, 1)) {
      const t = midnight(k);
      if (t >= v.end) break;
      const [y, m, dd] = k.split("-").map(Number);
      if (t > v.start && new Date(Date.UTC(y, m - 1, dd)).getUTCDay() === 1) weeks += `<i class="tl-week" style="left:${pc(fr.x(t))}"></i>`;
    }
  } else {
    weeks = timeTicks(v, 10).map((tk) => `<i class="tl-week" style="left:${pc(fr.x(tk.t))}"></i>`).join("");
  }
  const nowIn = now >= v.start && now <= v.end;
  const x = pc(fr.x(now));
  return {
    back: `<div class="tl-layer">${weeks}</div>`,
    front: nowIn ? `<div class="tl-layer"><i class="tl-now" style="left:${x}"></i><span class="tl-pill" style="left:${x}">Now</span></div>` : "",
  };
}

const stamp = (t, now) => (Math.abs(t - now) < 6 * DAY ? weekdayTime(t) : `${day(t)} ${clock(t)}`);

// accts: the accounts the account picker leaves, in any order.
export function timeline(accts) {
  if (!accts.length) return { html: "", mount() {} };
  const now = Date.now();
  const ts = timelineTime();
  const v = ts.window;
  const fr = frame(v);
  const rows = [];
  const strips = {};
  let body = "";
  for (const provider of ["claude", "codex"]) {
    const ids = new Set(accts.filter((a) => a.provider === provider).map((a) => a.id));
    if (!ids.size) continue;
    const q = queue(provider);
    const list = [...q.order, ...q.rest].filter((a) => ids.has(a.id));
    const models = list.map((a) => model(a, now, fr));
    strips[provider] = models;
    body += `<div class="tl-grp"><div class="lab">${logo(provider)}<b>${providerTitle(provider)}</b><span class="muted">accounts available</span></div><div class="area tl-strip" data-strip-provider="${provider}" data-tplot="${KEY}"><div class="tp-content">${stripHtml(models, fr, now)}</div></div></div>`;
    models.forEach((m) => {
      body += rowHtml(m, accountColor(m.a.id), fr, now, rows.length);
      rows.push(m);
    });
  }
  const layers = layersHtml(fr, v, now);
  const what = ts.moved ? "Weekly allowance left per account at the current burn, with resets and monthly renewals." : "Next 30 days. Weekly allowance left per account at the current burn, with resets and monthly renewals.";
  const end = ts.range ? selectionLabel(ts.range, "data-tl-clear") : ts.moved ? backToNow("data-tl-home") : "";
  const html = `<div class="tlsec">
    <div class="tlhead"><div class="t"><b>Timeline</b><span class="muted">${what}</span></div>${end ? `<div class="tlend">${end}</div>` : ""}</div>
    <div class="tlwrap"><div class="tl" data-tl>${layers.back}${calendarHtml(fr, v, now)}${body}${layers.front}</div></div>
  </div>`;

  const mount = (root) => {
    const tl = root.querySelector("[data-tl]");
    if (!tl) return;
    // Strip counts that do not fit their stretch are left out. A stretch with
    // none available keeps its warning icon when the word does not fit.
    tl.querySelectorAll(".tl-strip span").forEach((s) => {
      const over = () => s.scrollWidth > s.clientWidth + 1;
      if (over() && s.parentElement.classList.contains("none")) s.classList.add("compact");
      // A centred icon spills both edges evenly, which scrollWidth misses.
      if (over() || (s.classList.contains("compact") && s.clientWidth < 12)) s.classList.add("hide");
    });
    // Labels keep inside the row and never overlap. A label that starts just
    // inside the previous one is nudged right; one that cannot fit is hidden.
    tl.querySelectorAll("[data-tl-row]").forEach((area) => {
      const ar = area.getBoundingClientRect();
      const items = [...area.querySelectorAll(".tl-lab, .tl-mark > span")].map((el) => ({ el, r: el.getBoundingClientRect() })).sort((a, b) => a.r.left - b.r.left);
      let edge = -Infinity;
      for (const it of items) {
        let { left: l, right: r } = it.r;
        let shift = 0;
        if (it.el.classList.contains("tl-lab") && r > ar.right) shift = ar.right - r;
        const gap = edge + 8 - (l + shift);
        if (gap > 0) {
          if (gap > 160 || r + shift + gap > ar.right) { it.el.classList.add("hide"); continue; }
          shift += gap;
        }
        if (shift) it.el.style.transform = `translateX(${shift}px)`;
        edge = r + shift;
      }
    });
    const clear = root.querySelector("[data-tl-clear]");
    if (clear) clear.onclick = () => ts.setRange(null);
    const home = root.querySelector("[data-tl-home]");
    if (home) home.onclick = () => ts.setWindow(ts.defaultWindow());
    // The shared controller: zoom, pan, selection and the crosshair. A row
    // answers with its account's allowance, a strip with its count.
    bindTime(tl, {
      key: KEY,
      window: v,
      range: ts.range,
      grid: v.end - v.start > 3 * DAY ? { bounds: dayBounds(v) } : { origin: 0, step: HOUR },
      future: true,
      defaultWindow: ts.defaultWindow,
      setWindow: ts.setWindow,
      setRange: ts.setRange,
      probe(t, el, hovered) {
        const when = t > now ? `${stamp(t, now)}, at this rate` : stamp(t, now);
        if (el.dataset.stripProvider) {
          const models = strips[el.dataset.stripProvider] || [];
          const n = models.filter((m) => available(m, t, now)).length;
          const text = n === 0 ? "None" : `${n} of ${models.length}`;
          return { chip: text, chipTop: 0, card: hovered ? card({ title: when, rows: [{ lead: `<span class="lg">${logo(el.dataset.stripProvider)}</span>`, k: `${providerTitle(el.dataset.stripProvider)} accounts available`, v: text, cls: n ? "" : "err" }] }) : "", cardTop: 24 };
        }
        const m = rows[Number(el.dataset.row)];
        const val = m ? valueAt(m, t) : null;
        if (val == null) return null;
        const note = held(m, t) ? (m.hold[1] === Infinity ? m.st.text || "Unavailable" : "Resting, not used for new sessions") : "";
        const y = 1 - (44 - (Math.max(0, Math.min(100, val)) / 100) * 24) / 48;
        return {
          chip: val <= 0 ? "Out" : Math.round(val) + "%",
          chipTop: 0,
          dots: [{ y, color: accountColor(m.a.id) }],
          card: hovered ? card({ title: when, warn: note, rows: [{ k: m.a.email || m.a.id, v: val <= 0 ? "Out" : Math.round(val) + "% left", color: accountColor(m.a.id) }] }) : "",
          cardTop: 48,
        };
      },
    });
  };
  return { html, mount };
}

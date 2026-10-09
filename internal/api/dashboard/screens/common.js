// Pieces shared by several screens: account colours, the API cost note, the
// reading time and session rows.
import {
  S, esc, int, clock, day, seen, icon, logo, table, accounts, cacheReuse, names, sessionTitle, sessionHref, warnState, validTime, isToday, plainObject,
} from "../core.js";

export const legendHtml = (items) => items.map((l) => `<span><i style="background:${l.color}"></i>${esc(l.label)}</span>`).join("");

export function readAt(prefix = "Read") {
  return S.readAt ? `${prefix} ${clock(S.readAt)}` : "";
}

// Never grey: grey means muted or off here, so a grey account looks switched off.
export const ACCOUNT_COLORS = ["var(--chart-1)", "var(--chart-p90)", "var(--chart-p50)", "var(--chart-p99)", "var(--chart-5)", "#6E9EEF"];

// One colour per account on every screen. Accounts take the colours in a
// fixed order (email Z to A, then provider, then id), never in the order a
// screen lists them, which follows the router queue and changes. The order
// itself is arbitrary; Z to A matches the colours in the Paper designs.
let colorCache = { data: undefined, map: new Map() };
export function accountColor(id) {
  if (colorCache.data !== S.data) {
    const order = accounts().slice().sort((a, b) =>
      String(b.email ?? "").localeCompare(String(a.email ?? "")) || String(a.provider ?? "").localeCompare(String(b.provider ?? "")) || String(a.id ?? "").localeCompare(String(b.id ?? "")));
    colorCache = { data: S.data, map: new Map(order.map((a, i) => [a.id, ACCOUNT_COLORS[i % ACCOUNT_COLORS.length]])) };
  }
  return colorCache.map.get(id) || ACCOUNT_COLORS[0];
}

// ---------- API cost ----------

const cap = (s) => s.charAt(0).toUpperCase() + s.slice(1);

// "claude-opus-5-5" as "Opus 5.5", "gpt-6-astra" as "GPT-6 Astra"; other ids as given.
function modelTitle(id) {
  const s = String(id || "");
  let m = /^claude-([a-z]+)-(\d+)(?:-(\d+))?$/.exec(s);
  if (m) return `${cap(m[1])} ${m[2]}${m[3] ? "." + m[3] : ""}`;
  m = /^gpt-([\d.]+)(?:-([a-z]+))?$/.exec(s);
  if (m) return `GPT-${m[1]}${m[2] ? " " + cap(m[2]) : ""}`;
  return s;
}

// The line under a chart of API cost: which prices, and how usage from before
// per-request pricing was priced. provs: the providers of the accounts in view.
export function costNote(provs) {
  const p = S.data?.summary?.pricing || {};
  const since = validTime(p.exact_since);
  const fb = plainObject(p.fallback) || {};
  const models = [...new Set(provs.map((k) => fb[k]).filter(Boolean))].map(modelTitle);
  // Proxy usage from before per-request pricing is estimated at the fallback
  // models; older usage from local logs is priced by model when the history has it.
  const parts = [
    since ? `Per request since ${isToday(since) ? "today" : day(since)} ${clock(since)}.` : "",
    models.length ? `Earlier proxy usage estimated at ${models.join(" and ")} prices${p.history_by_model ? ", older usage priced by model from local logs" : ""}.` : "",
  ].filter(Boolean);
  return ["At public API list prices.", ...parts].join(" ");
}

// The hover on an API cost card: what the prices leave out.
export function costTitle() {
  const n = S.data?.summary?.pricing?.not_modelled;
  return n ? "Not modelled: " + n : "";
}

// ---------- sessions table ----------

// Whether the picked accounts served a session. While a whole provider shows,
// so do its sessions that have no answer yet.
export function sessionInScope(s, sc) {
  if (!sc.some) return sc.prov === "all" || s.provider === sc.prov;
  return (s.auth_ids || []).some((id) => sc.ids.includes(id));
}

export function accountCell(s, nm) {
  const ids = s.auth_ids || [];
  if (ids.length > 1) return warnState(`Switched, ${ids.length} accounts`);
  if (!ids.length) return `<span class="muted">No answer yet</span>`;
  return `<span class="clamp">${esc(nm[ids[0]] || ids[0])}</span>`;
}

export function sessionName(s) {
  const n = s.threads?.length || 0;
  return `<span class="clamp">${esc(sessionTitle(s))}</span>${n ? `<span class="threads">${n} ${n === 1 ? "thread" : "threads"}${icon("chevronRight", 14)}</span>` : ""}`;
}

export function reuseCell(s, withMeter) {
  const r = cacheReuse(s);
  if (r == null) return `<span class="muted">New</span>`;
  if (!withMeter) return `${Math.round(r)}%`;
  return `<span class="metercell"><span class="meter" style="width:56px"><i style="width:${Math.round(r)}%"></i></span><span class="${r ? "" : "muted"}" style="width:32px">${Math.round(r)}%</span></span>`;
}

export function activeSpan(s) {
  const a = Date.parse(s.first_seen), b = Date.parse(s.last_seen);
  const end = Date.now() - b < 2 * 60e3 ? "now" : clock(b);
  if (!a || b - a < 60e3) return end === "now" ? "Now" : clock(b);
  return `${clock(a)} to ${end}`;
}

// cols chosen per screen; "account" can be dropped when the screen is one account.
export function sessionTable(list, { withAccount = true, meter = false, lastSeen = "seen", empty = "No sessions" } = {}) {
  const nm = names();
  const cols = [
    ...(withAccount ? [{ label: "", w: 16, cls: "ic" }] : []),
    { label: "Session" },
    { label: "Machine", w: 112 },
    ...(withAccount ? [{ label: "Account", w: 240 }] : []),
    { label: "Requests", w: 88, r: true },
    { label: "Failed", w: 88, r: true },
    { label: "Cache reuse", w: meter ? 104 : 104, r: !meter },
    { label: lastSeen === "span" ? "Active" : "Last seen", w: lastSeen === "span" ? 112 : 88, r: true },
  ];
  const rows = list.map((s) => ({
    href: sessionHref(s),
    cells: [
      ...(withAccount ? [logo(s.provider)] : []),
      sessionName(s),
      s.machine ? esc(s.machine) : `<span class="muted">–</span>`,
      ...(withAccount ? [accountCell(s, nm)] : []),
      int(s.requests),
      `<span class="${s.failed ? "warn" : "muted"}">${int(s.failed)}</span>`,
      reuseCell(s, meter),
      lastSeen === "span" ? activeSpan(s) : seen(s.last_seen),
    ],
  }));
  return table(cols, rows, { empty });
}

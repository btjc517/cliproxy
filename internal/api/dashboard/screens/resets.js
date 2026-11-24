// Resets: the one-off limit resets each account holds, what each one clears,
// the advice on it, a button that uses it, and the record of past uses.
import {
  S, esc, logo, pill, table, menu, closeMenu, api, write, accounts, names, status, limits, left,
  resetShort, resetLong, clock, isToday,
} from "../core.js";
import { region } from "../paint.js";
import { readAt } from "./common.js";

const router = () => S.data?.router || {};

export const resetMode = () => (router().reset_mode === "auto" ? "auto" : "off");

const grants = (a) => router().accounts?.[a.id]?.resets || [];
const unused = (a) => grants(a).filter((g) => g.left > 0);

// The resets worth using now, best first, with their accounts.
export function suggestions() {
  const byId = Object.fromEntries(accounts().map((a) => [a.id, a]));
  return (router().reset_suggestions || []).map((s) => ({ ...s, a: byId[s.auth_id] })).filter((s) => s.a);
}

// What a reset clears, biggest first: the week, a model's week, the 5-hour window.
const clearRank = (t) => (t === "week" ? 0 : /week$/.test(t) ? 1 : 2);
const clearsList = (g) => [...(g.clears_titles || [])].sort((x, y) => clearRank(x) - clearRank(y));

// "the week, the Fable week and the 5-hour window"
function clearsSentence(g) {
  const t = clearsList(g).map((x) => "the " + x);
  if (!t.length) return "the limit";
  return t.length === 1 ? t[0] : t.slice(0, -1).join(", ") + " and " + t[t.length - 1];
}
// "Week, Fable week, 5-hour window"
const clearsShort = (g) => { const s = clearsList(g).join(", "); return s ? s.charAt(0).toUpperCase() + s.slice(1) : ""; };
const title = (g) => g.title || "Usage reset";

// Where the account stands, in a few words: what a reset would change.
function standing(a) {
  const st = status(a);
  const lim = limits(a);
  if (st.kind === "off") return "Off";
  if (st.kind === "blocked" || st.kind === "error") return st.text;
  const week = lim.week;
  const reset = st.until || week?.reset;
  const extraOut = (lim.extra || []).filter((m) => left(m) <= 2).map((m) => m.title);
  if (st.kind === "usedup") return "Week used up" + (reset ? ", back " + resetLong(reset) : "");
  if (extraOut.length) return extraOut.join(", ") + " used up" + (week ? " · " + Math.round(left(week)) + "% of week left" : "");
  if (st.kind === "limited") return st.text;
  return week ? Math.round(left(week)) + "% of week left" : "No reading yet";
}

function adviceCell(g) {
  const adv = g.advice || {};
  const reason = esc(adv.reason || "");
  const tag = adv.action === "use" ? pill("Use now") : pill(adv.action === "wait" ? "Wait" : "Hold", "grey");
  return `<span class="advice" title="${reason}">${tag}<span class="clamp muted">${reason}</span></span>`;
}

// Asks before using a reset: it works once and cannot be undone.
function confirmUse(anchor, a, g) {
  const adv = g.advice || {};
  const lead = adv.action === "use" ? "Worth using now" : adv.action === "wait" ? "Better to wait" : "Better to hold";
  const advice = adv.reason ? `<div class="muted"><span class="strong" style="color:var(--fg)">${lead}.</span> ${esc(adv.reason)}</div>` : "";
  const many = g.total > 1 ? ` ${g.left} of ${g.total} left.` : "";
  menu(anchor, [{
    html: `<div class="field" style="gap:10px"><div class="ui strong">Use the ${esc(title(g).toLowerCase())} on ${esc(a.email)}?</div>
      <div class="muted">Clears ${esc(clearsSentence(g))} now. It works once and cannot be undone.${many}</div>${advice}
      <div class="row gap8" style="justify-content:flex-end"><button class="btn" data-x>Cancel</button><button class="btn primary" data-ok>Use reset</button></div></div>`,
    mount: (el) => {
      el.querySelector("[data-x]").onclick = () => closeMenu();
      el.querySelector("[data-ok]").onclick = () => {
        closeMenu();
        write(async () => {
          const r = await api("/routing/resets/use", { method: "POST", body: JSON.stringify({ auth_id: a.id, grant_id: g.id }) });
          if (r?.result !== "reset") throw new Error(r?.text || "The provider did not apply the reset");
        }, title(g) + " used on " + a.email);
      };
    },
  }], { width: 340 });
}

// One row per unused reset: what it is and clears, the button, the advice and
// when it expires. The button sits second so a phone shows it without
// scrolling the table sideways.
const COLS = [{ label: "", w: 16, cls: "ic" }, { label: "Reset", w: 310, cls: "rname" }, { label: "", w: 96 }, { label: "Advice", cls: "adv" }, { label: "Expires", w: 110 }];

function resetRow(a, g, i) {
  const use = g.advice?.action === "use";
  const button = g.usable_now
    ? `<button class="btn ${use ? "primary" : "outline"} usebtn" data-reset-use="${i}">Use reset</button>`
    : `<span class="muted nowrap" title="${esc(g.advice?.reason || "")}">Not usable now</span>`;
  return {
    cells: [
      "",
      `<span class="clamp" title="${esc(g.label || "")}"><b class="strong">${esc(title(g))}</b><span class="muted"> · ${esc(clearsShort(g))}${g.total > 1 ? ` · ${g.left} of ${g.total} left` : ""}</span></span>`,
      button,
      adviceCell(g),
      g.ends_at ? `<span class="nowrap" title="${esc(resetLong(g.ends_at))}">${esc(resetShort(g.ends_at))}</span>` : `<span class="muted">No expiry</span>`,
    ],
  };
}

// The unused resets of the given accounts, grouped by account. opts.flat: one
// account, so no group rows. Returns {html, count, mount}.
export function resetTable(accts, opts = {}) {
  const list = [];
  const rows = [];
  // Accounts with a reset worth using now come first.
  const worth = (a) => (unused(a).some((g) => g.advice?.action === "use") ? 0 : 1);
  for (const a of [...accts].sort((x, y) => worth(x) - worth(y))) {
    const mine = unused(a).sort((x, y) => (y.advice?.action === "use") - (x.advice?.action === "use"));
    if (!mine.length) continue;
    if (!opts.flat) {
      rows.push({
        group: true,
        html: `<div class="c ic" style="width:16px;flex:0 0 16px">${logo(a.provider)}</div><div class="c g"><a class="ui strong clamp" href="#/accounts/${encodeURIComponent(a.id)}">${esc(a.email)}</a><span class="muted clamp">${esc(standing(a))}</span></div>`,
      });
    }
    for (const g of mine) {
      rows.push(resetRow(a, g, list.length));
      list.push({ a, g });
    }
  }
  return {
    html: list.length ? table(COLS, rows) : "",
    count: list.length,
    mount(root) {
      root.querySelectorAll("[data-reset-use]").forEach((b) => {
        const { a, g } = list[Number(b.dataset.resetUse)];
        b.onclick = (e) => { e.preventDefault(); confirmUse(b, a, g); };
      });
    },
  };
}

// The accounts with no reset to use, one line each, so every account is
// accounted for.
function noneLeft(accts) {
  const rows = accts.filter((a) => !unused(a).length).map((a) => {
    const all = grants(a);
    const read = router().accounts?.[a.id]?.resets_observed_at;
    const why = all.length ? `${all.length} used` : read ? "None granted" : status(a).kind === "blocked" ? "Not read: sign in again" : "Not read yet";
    return { cls: "dim", cells: [logo(a.provider), `<a class="clamp" href="#/accounts/${encodeURIComponent(a.id)}">${esc(a.email)}</a>`, `<span class="muted">${esc(why)}</span>`] };
  });
  if (!rows.length) return "";
  return `<div class="subhead">No reset to use</div>${table([{ label: "", w: 16, cls: "ic" }, { label: "Account", w: 280 }, { label: "Resets" }], rows, { noHead: true })}`;
}

// The record of uses, newest first.
function resetEvents() {
  const events = router().reset_events || [];
  if (!events.length) return "";
  const nm = names();
  const cols = [{ label: "When", w: 110 }, { label: "Account", w: 240 }, { label: "Outcome" }, { label: "By", w: 90 }, { label: "Reset", w: 260 }];
  const rows = events.slice(0, 10).map((e) => ({
    cells: [
      esc(isToday(e.at) ? clock(e.at) : resetShort(e.at)),
      `<span class="clamp">${esc(nm[e.auth_id] || e.account || e.auth_id)}</span>`,
      `<span class="clamp ${e.result === "reset" ? "" : "muted"}" title="${esc(e.text || "")}">${esc(e.text || e.result)}</span>`,
      e.by === "auto" ? "Automatic" : "You",
      `<span class="clamp muted">${esc(e.label || e.grant_id)}</span>`,
    ],
  }));
  return `<div class="sec last"><div class="sech tall"><div class="t"><b>Recent uses</b></div></div>${table(cols, rows)}</div>`;
}

// A small tag for an account elsewhere on the dashboard: it has a reset to
// use. Brand-coloured when the advice is to use one now.
export function resetTag(a) {
  const mine = unused(a);
  if (!mine.length) return "";
  const use = mine.some((g) => g.advice?.action === "use");
  return `<a class="pill ${use ? "" : "grey"}" href="#/resets" title="${use ? "A reset is worth using now" : "Unused resets"}">${use ? "Use reset" : mine.length + (mine.length === 1 ? " reset" : " resets")}</a>`;
}

// One line for Overview when a reset is worth using now.
export function suggestionBanner() {
  const list = suggestions();
  if (!list.length) return "";
  const s = list[0];
  const more = list.length > 1 ? ` and ${list.length - 1} more` : "";
  const g = grants(s.a).find((x) => x.id === s.grant?.id) || {};
  return `<a class="banner hint" href="#/resets"><span class="clamp"><b>Worth using now: the ${esc(title(g).toLowerCase())} on ${esc(s.a.email)}${more}.</b> ${esc(s.advice?.reason || "")}</span><span class="pill">Open resets</span></a>`;
}

// The Resets page.
export function view() {
  const accts = accounts();
  const tbl = resetTable(accts);
  const auto = resetMode() === "auto";
  const use = suggestions().length;
  const headline = tbl.count
    ? `${tbl.count} ${tbl.count === 1 ? "reset" : "resets"} ready to use`
    : "No resets to use right now";
  const sub = tbl.count
    ? (use ? `${use} worth using now. ` : "None needs using right now. ") + "A reset clears an account's limit before it resets by itself. Each works once."
    : "A reset clears an account's limit before it resets by itself. The providers grant them from time to time, and new ones show here within 20 minutes.";

  const html = `
    <div class="bar titled"><span class="title">Resets</span><div class="end">${region("rs|read", esc(readAt("Read")), "span", `class="muted nowrap"`)}</div></div>
    <div class="body">
      ${region("rs|lead", `<div class="resetlead"><div class="bignum"><span class="v">${esc(headline)}</span></div><div class="muted">${esc(sub)}</div></div>`, "div", `style="display:contents"`)}
      <div class="setgrp tight">
        <div class="setrow"><div class="k"><b>Use resets automatically</b><span>Experimental. Uses a reset by itself when the advice is Use now, at most one per provider every 30 minutes.</span></div><div class="v"><button class="toggle ${auto ? "on" : ""}" data-reset-auto role="switch" aria-checked="${auto}" aria-label="Use resets automatically"></button></div></div>
      </div>
      ${region("rs|list", `${tbl.html}${noneLeft(accts)}`, "div", `class="sec"`)}
      ${region("rs|events", resetEvents(), "div", `style="display:contents"`)}
    </div>`;
  return {
    html,
    mount(root) {
      tbl.mount(root);
      root.querySelector("[data-reset-auto]").onclick = () => write(() => api("/config/routing/resets/mode", { method: "PUT", body: JSON.stringify(auto ? "off" : "auto") }), auto ? "Resets are no longer used automatically" : "Resets are now used automatically");
    },
  };
}

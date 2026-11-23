// Usage resets: the one-off limit resets each account has, the advice on each
// one, the control that uses one, and the record of past uses.
import { S, esc, icon, logo, email, pill, table, menu, closeMenu, api, write, accounts, names, resetShort, resetLong, clock, isToday } from "../core.js";

const router = () => S.data?.router || {};
const cap = (s) => (s ? s.charAt(0).toUpperCase() + s.slice(1) : "");

export const resetMode = () => (router().reset_mode === "auto" ? "auto" : "off");

// The resets worth using now, best first, with their accounts.
export function suggestions() {
  const byId = Object.fromEntries(accounts().map((a) => [a.id, a]));
  return (router().reset_suggestions || []).map((s) => ({ ...s, a: byId[s.auth_id] })).filter((s) => s.a);
}

// Every reset of the given accounts with its account: the ones to use first,
// then the unused ones, then the used ones.
function resetList(accts) {
  const rank = (g) => (g.advice?.action === "use" ? 0 : g.left > 0 ? 1 : 2);
  const out = [];
  for (const a of accts) for (const g of router().accounts?.[a.id]?.resets || []) out.push({ a, g });
  return out.sort((x, y) => rank(x.g) - rank(y.g));
}

function adviceCell(g) {
  const adv = g.advice || {};
  const reason = esc(adv.reason || "");
  if (adv.action === "none") return `<span class="muted">${reason}</span>`;
  const tag = adv.action === "use" ? pill("Use now") : pill(adv.action === "wait" ? "Wait" : "Hold", "grey");
  return `<span class="advice" title="${reason}">${tag}<span class="clamp muted">${reason}</span></span>`;
}

const clearsText = (g) => cap((g.clears_titles || []).join(", "));
const leftText = (g) => (g.total > 1 ? `<span class="muted"> · ${g.left} of ${g.total} left</span>` : "");

// Asks before using a reset: it cannot be undone and there are few of them.
function confirmUse(anchor, a, g) {
  const adv = g.advice || {};
  const clears = (g.clears_titles || []).join(" and ");
  const lead = adv.action === "use" ? "Worth using now" : adv.action === "wait" ? "Better to wait" : "Better to hold";
  const advice = adv.reason ? `<div class="muted"><span class="strong" style="color:var(--fg)">${lead}.</span> ${esc(adv.reason)}</div>` : "";
  menu(anchor, [{
    html: `<div class="field" style="gap:10px"><div class="ui strong">Use this reset on ${esc(a.email)}?</div>
      <div class="muted">${esc(g.label || "Usage reset")}. Clears the ${esc(clears || "limit")} now. This cannot be undone.</div>${advice}
      <div class="row gap8" style="justify-content:flex-end"><button class="btn" data-x>Cancel</button><button class="btn primary" data-ok>Use reset</button></div></div>`,
    mount: (el) => {
      el.querySelector("[data-x]").onclick = () => closeMenu();
      el.querySelector("[data-ok]").onclick = () => {
        closeMenu();
        write(async () => {
          const r = await api("/routing/resets/use", { method: "POST", body: JSON.stringify({ auth_id: a.id, grant_id: g.id }) });
          if (r?.result !== "reset") throw new Error(r?.text || "The provider did not apply the reset");
        }, "Reset used on " + a.email);
      };
    },
  }], { width: 320 });
}

// A table of the given accounts' resets. opts.account: the rows are all one
// account's, so the account column is left out.
// Returns {html, count, mount}; html is "" when there is nothing to show.
export function resetTable(accts, opts = {}) {
  const list = resetList(accts);
  if (!list.length) return { html: "", count: 0, mount() {} };
  const cols = [
    // The advice and the control sit next to the account, so a phone shows
    // them without scrolling the table sideways.
    ...(opts.account ? [] : [{ label: "", w: 16, cls: "ic" }, { label: "Account", w: 220 }]),
    { label: "Advice", cls: "adv" }, { label: "", w: 28 },
    { label: "Reset", w: opts.account ? 280 : 230 }, { label: "Clears", w: 150 }, { label: "Expires", w: 100 },
  ];
  const rows = list.map(({ a, g }, i) => ({
    cls: g.left > 0 ? "" : "dim",
    cells: [
      ...(opts.account ? [] : [logo(a.provider), `<a class="clamp" href="#/accounts/${encodeURIComponent(a.id)}">${email(a.email)}</a>`]),
      adviceCell(g),
      g.left > 0 ? `<button class="iconbtn" data-reset-use="${i}" aria-label="Use this reset" title="Use this reset">${icon("reset")}</button>` : "",
      `<span class="clamp" title="${esc(g.label || "")}">${esc(g.label || "Usage reset")}${leftText(g)}</span>`,
      `<span class="clamp">${esc(clearsText(g))}</span>`,
      g.ends_at && g.left > 0 ? `<span class="nowrap" title="${esc(resetLong(g.ends_at))}">${esc(resetShort(g.ends_at))}</span>` : "",
    ],
  }));
  return {
    html: table(cols, rows),
    count: list.filter(({ g }) => g.left > 0).length,
    mount(root) {
      root.querySelectorAll("[data-reset-use]").forEach((b) => {
        const { a, g } = list[Number(b.dataset.resetUse)];
        b.onclick = (e) => { e.preventDefault(); confirmUse(b, a, g); };
      });
    },
  };
}

// The record of uses, newest first.
export function resetEvents() {
  const events = router().reset_events || [];
  if (!events.length) return "";
  const nm = names();
  const cols = [{ label: "When", w: 130 }, { label: "Account", w: 240 }, { label: "Reset", w: 260 }, { label: "By", w: 100 }, { label: "Outcome" }];
  const rows = events.slice(0, 10).map((e) => ({
    cells: [
      esc(isToday(e.at) ? clock(e.at) : resetShort(e.at)),
      `<span class="clamp">${esc(nm[e.auth_id] || e.account || e.auth_id)}</span>`,
      `<span class="clamp">${esc(e.label || e.grant_id)}</span>`,
      e.by === "auto" ? "Automatic" : "You",
      `<span class="clamp ${e.result === "reset" ? "" : "muted"}" title="${esc(e.text || "")}">${esc(e.text || e.result)}</span>`,
    ],
  }));
  return `<div class="subhead">Recent uses</div>${table(cols, rows)}`;
}

// One line for Overview when a reset is worth using now.
export function suggestionBanner() {
  const list = suggestions();
  if (!list.length) return "";
  const s = list[0];
  const more = list.length > 1 ? ` and ${list.length - 1} more` : "";
  return `<a class="banner hint" href="#/routing">${icon("reset", 14, "var(--fg)")}<span class="clamp"><b>A reset is worth using on ${esc(s.a.email)}${more}.</b> ${esc(s.advice?.reason || "")}</span></a>`;
}

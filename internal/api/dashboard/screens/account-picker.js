// The account picker on every screen that filters by account. It edits the one
// shared selection (accountScope in core.js). Built from the Paper board
// "v2 · D · Provider and graph states": a provider menu with each provider's
// accounts in a second menu to its right.
import { S, accounts, accountScope, setAccountSelection, wantScope, esc, logo, icon, email, providerTitle } from "../core.js";
import { accountColor } from "./common.js";

const PROVIDERS = ["claude", "codex"];

const stack = () => `<span class="provider-stack">${logo("codex")}<span class="disc">${logo("claude")}</span></span>`;

// A 16px box in a 20px slot: filled with a tick, filled with a bar, or empty.
function check(state) {
  const inner = state === "false"
    ? '<rect class="empty" x="2" y="2" width="16" height="16" rx="4"/><rect class="ring" x="2.5" y="2.5" width="15" height="15" rx="3.5"/>'
    : `<rect class="box" x="2" y="2" width="16" height="16" rx="4"/><path class="mark" d="${state === "true" ? "M6 9.5L9 12.5L14 7.5" : "M6 10H14"}"/>`;
  return `<svg class="pick-check" width="20" height="20" viewBox="0 0 20 20" aria-hidden="true">${inner}</svg>`;
}

// The closed trigger: provider icons and what the selection covers.
export function accountPicker() {
  const sc = accountScope();
  const provs = [...new Set(sc.shown.map((a) => a.provider))];
  const label = sc.shown.length === sc.all.length ? "All accounts"
    : !sc.some ? providerTitle(sc.prov)
    : sc.shown.length === 1 ? String(sc.shown[0].email).split("@")[0]
    : `${sc.shown.length} accounts`;
  return `<button class="btn account-picker" data-account-picker aria-label="Accounts: ${esc(label)}" aria-haspopup="menu" aria-expanded="false">${provs.length === 1 ? logo(provs[0]) : stack()}<span class="clamp">${esc(label)}</span>${icon("chevronDown", 12)}</button>`;
}

// The open menu's close, so a second press on the trigger closes it.
let active = null;

export function bindAccountPicker(root) {
  const anchor = root.querySelector("[data-account-picker]");
  if (anchor) anchor.onclick = () => (active ? active(true) : open(anchor));
}

function open(anchor) {
  const all = accounts();
  const selected = new Set(accountScope().ids);
  const provs = PROVIDERS.filter((p) => all.some((a) => a.provider === p));
  const groupIds = (p) => all.filter((a) => p === "all" || a.provider === p).map((a) => a.id);
  const state = (ids) => (ids.length && ids.every((id) => selected.has(id)) ? "true" : ids.some((id) => selected.has(id)) ? "mixed" : "false");
  const row = (attrs, s, lead, label, end = "", cls = "") =>
    `<button class="pick-row ${cls}" role="menuitemcheckbox" aria-checked="${s}" ${attrs}>${check(s)}${lead}<span class="grow clamp">${label}</span>${end}</button>`;

  let hovered = "", closed = false;
  const pop = document.createElement("div");
  pop.className = "picker-pop";
  pop.innerHTML = `<div class="picker-menu" role="menu" aria-label="Accounts"></div><div class="picker-menu sub" role="menu" hidden></div>`;
  const [main, sub] = pop.children;
  document.body.appendChild(pop);
  S.hold++;
  anchor.setAttribute("aria-expanded", "true");

  const drawMain = () => {
    main.innerHTML = row('data-provider="all"', state(groupIds("all")), `<span class="pick-icon">${stack()}</span>`, "All accounts")
      + (provs.length ? '<div class="pick-sep" role="separator"></div>' : "")
      + provs.map((p) => row(
        `data-provider="${p}" aria-haspopup="menu" aria-expanded="${hovered === p}"`,
        state(groupIds(p)), `<span class="pick-icon">${logo(p)}</span>`, esc(providerTitle(p)),
        `<span class="muted">${groupIds(p).length}</span>${icon("chevronRight", 16)}`, hovered === p ? "on" : "",
      )).join("");
  };
  const drawSub = () => {
    sub.hidden = !hovered;
    sub.setAttribute("aria-label", hovered ? `${providerTitle(hovered)} accounts` : "");
    sub.innerHTML = hovered ? all.filter((a) => a.provider === hovered).map((a) =>
      row(`data-account="${esc(a.id)}"`, selected.has(a.id) ? "true" : "false", `<i class="sq" style="background:${accountColor(a.id)}"></i>`, email(a.email))).join("") : "";
  };
  // The menu hangs under the trigger, left-aligned when it fits. The account
  // menu sits to its right, its first row level with the provider row; on the
  // left when the right has no room, and underneath on a phone.
  const place = () => {
    const vw = window.innerWidth, vh = window.innerHeight, r = anchor.getBoundingClientRect();
    const mw = main.offsetWidth, mh = main.offsetHeight;
    const left = r.left + mw <= vw - 8 ? r.left : Math.max(8, r.right - mw);
    const top = Math.max(8, Math.min(r.bottom + 6, vh - mh - 8));
    main.style.left = left + "px";
    main.style.top = top + "px";
    const item = hovered && main.querySelector(`[data-provider="${hovered}"]`);
    if (!item) return;
    const sw = sub.offsetWidth, sh = sub.offsetHeight;
    let x = left + mw + 4, y = item.getBoundingClientRect().top - 8;
    if (x + sw > vw - 8) x = left - 4 - sw;
    if (x < 8) { x = Math.max(8, Math.min(left, vw - sw - 8)); y = top + mh + 4; }
    sub.style.left = x + "px";
    sub.style.top = Math.max(8, Math.min(y, vh - sh - 8)) + "px";
  };
  const setHovered = (p) => {
    const next = p === "all" ? "" : p;
    if (next === hovered) return;
    hovered = next;
    main.querySelectorAll("[data-provider]").forEach((b) => {
      if (b.dataset.provider === "all") return;
      b.classList.toggle("on", b.dataset.provider === hovered);
      b.setAttribute("aria-expanded", String(b.dataset.provider === hovered));
    });
    drawSub();
    place();
  };

  const close = (refocus) => {
    if (closed) return;
    closed = true;
    active = null;
    pop.remove();
    S.hold = Math.max(0, S.hold - 1);
    document.removeEventListener("pointerdown", away, true);
    document.removeEventListener("keydown", keydown, true);
    window.removeEventListener("hashchange", onHash);
    window.removeEventListener("resize", place);
    anchor.setAttribute("aria-expanded", "false");
    // Nothing picked falls back to all accounts.
    setAccountSelection([...selected]);
    window.dispatchEvent(new Event("dash:render"));
    if (S.data && wantScope() !== S.dataScope) window.dispatchEvent(new Event("dash:refresh"));
    if (refocus) document.querySelector("[data-account-picker]")?.focus();
  };
  const away = (e) => { if (!pop.contains(e.target) && !anchor.contains(e.target)) close(false); };
  const onHash = () => close(false);
  const keydown = (e) => {
    if (e.key === "Escape" || e.key === "Tab") { if (e.key === "Escape") e.preventDefault(); close(true); return; }
    const t = e.target.closest?.(".pick-row");
    if (!t || !pop.contains(t)) return;
    const p = t.dataset.provider;
    if (e.key === "ArrowRight" && p && p !== "all") {
      e.preventDefault();
      setHovered(p);
      sub.querySelector(".pick-row")?.focus();
    } else if (e.key === "ArrowLeft" && sub.contains(t)) {
      e.preventDefault();
      main.querySelector(`[data-provider="${hovered}"]`)?.focus();
    } else if (e.key === "ArrowDown" || e.key === "ArrowUp" || e.key === "Home" || e.key === "End") {
      e.preventDefault();
      const items = [...t.parentElement.querySelectorAll(".pick-row")];
      const i = items.indexOf(t), n = items.length;
      const to = e.key === "Home" ? 0 : e.key === "End" ? n - 1 : (i + (e.key === "ArrowDown" ? 1 : -1) + n) % n;
      items[to]?.focus();
    }
  };

  // The pointer moves focus, so one row is highlighted at a time. Focusing a
  // provider row opens its accounts; "All accounts" closes them.
  pop.addEventListener("pointerover", (e) => {
    const b = e.target.closest(".pick-row");
    if (b && document.activeElement !== b) b.focus({ preventScroll: true });
  });
  main.addEventListener("focusin", (e) => { const p = e.target.closest?.("[data-provider]")?.dataset.provider; if (p) setHovered(p); });
  // Enter, Space and clicks all arrive here as a click on the row.
  pop.addEventListener("click", (e) => {
    const b = e.target.closest(".pick-row");
    if (!b) return;
    const account = b.dataset.account, provider = b.dataset.provider;
    const ids = account ? [account] : groupIds(provider);
    const remove = ids.length > 0 && ids.every((id) => selected.has(id));
    for (const id of ids) remove ? selected.delete(id) : selected.add(id);
    drawMain();
    drawSub();
    (account ? [...sub.querySelectorAll("[data-account]")].find((x) => x.dataset.account === account) : main.querySelector(`[data-provider="${provider}"]`))?.focus();
  });
  active = close;
  document.addEventListener("pointerdown", away, true);
  document.addEventListener("keydown", keydown, true);
  window.addEventListener("hashchange", onHash);
  window.addEventListener("resize", place);
  drawMain();
  drawSub();
  place();
  main.querySelector(".pick-row")?.focus();
}

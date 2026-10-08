import { S, accounts, esc, logo, icon, email, providerTitle } from "../core.js";
import { accountColor } from "./common.js";

export function usageScope() {
  const all = accounts();
  const selection = S.ui.usAccounts;
  const shown = Array.isArray(selection) ? all.filter((a) => selection.includes(a.id)) : all;
  const providers = [...new Set(shown.map((a) => a.provider))];
  const prov = providers.length === 1 ? providers[0] : "all";
  return { all, shown, ids: shown.map((a) => a.id), prov, some: shown.length !== all.filter((a) => prov === "all" || a.provider === prov).length };
}
const stack = () => `<span class="provider-stack">${logo("codex")}${logo("claude")}</span>`;
export function usagePicker(sc) {
  const providers = [...new Set(sc.shown.map((a) => a.provider))];
  const symbol = providers.length === 1 ? logo(providers[0]) : stack();
  const label = sc.shown.length === 0 ? "None" : sc.shown.length === 1 ? sc.shown[0].email.split("@")[0] : sc.some ? sc.shown.length : "";
  return `<button class="btn usage-picker" data-usage-picker aria-label="Filter accounts" aria-haspopup="menu" aria-expanded="false">${symbol}${label ? `<span class="clamp">${esc(label)}</span>` : ""}${icon("chevronDown", 12)}</button>`;
}
export function bindUsagePicker(root) {
  const anchor = root.querySelector("[data-usage-picker]");
  if (!anchor) return;
  anchor.onclick = () => {
    const all = accounts(), selected = new Set(usageScope().ids);
    let hovered = "", closed = false;
    const popup = document.createElement("div");
    popup.className = "usage-picker-pop"; popup.setAttribute("role", "menu");
    document.body.appendChild(popup); S.hold++;
    anchor.setAttribute("aria-expanded", "true");
    const state = (ids) => ids.length && ids.every((id) => selected.has(id)) ? "true" : ids.some((id) => selected.has(id)) ? "mixed" : "false";
    const check = (s) => `<span class="selection-check ${s === "false" ? "unchecked" : ""}">${s === "true" ? '<svg width="18" height="18" viewBox="0 0 24 24"><path d="M6 12L10 16L18 8" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>' : s === "mixed" ? '<svg width="18" height="18" viewBox="0 0 24 24"><path d="M6 12H18" stroke="currentColor" stroke-width="2" stroke-linecap="round"/></svg>' : ""}</span>`;
    const groupIds = (p) => all.filter((a) => p === "all" || a.provider === p).map((a) => a.id);
    const draw = () => {
      const groups = ["all", ...new Set(all.map((a) => a.provider))];
      popup.innerHTML = `<div class="provider-menu">${groups.map((p) => {
        const s = state(groupIds(p));
        return `<button class="pick-row ${s !== "false" ? "selected" : ""}" role="menuitemcheckbox" aria-checked="${s}" data-provider="${esc(p)}" ${p !== "all" ? 'aria-haspopup="menu"' : ""}>${check(s)}${p === "all" ? stack() : logo(p)}<span class="grow">${p === "all" ? "All accounts" : esc(providerTitle(p))}</span>${p === "all" ? '<span class="arrow-slot"></span>' : icon("chevronLeft", 12)}</button>`;
      }).join("")}</div>${hovered ? `<div class="account-submenu" role="menu" aria-label="${esc(providerTitle(hovered))} accounts">${all.filter((a) => a.provider === hovered).map((a) => {
        const s = selected.has(a.id) ? "true" : "false";
        return `<button class="pick-row ${s === "true" ? "selected" : ""}" role="menuitemcheckbox" aria-checked="${s}" data-account="${esc(a.id)}">${check(s)}<i class="sq" style="background:${accountColor(a.id)}"></i>${email(a.email)}</button>`;
      }).join("")}</div>` : ""}`;
      const r = anchor.getBoundingClientRect();
      popup.style.right = Math.max(8, window.innerWidth - r.right) + "px";
      popup.style.top = Math.min(r.bottom + 6, window.innerHeight - popup.offsetHeight - 8) + "px";
    };
    const close = () => {
      if (closed) return; closed = true;
      popup.remove(); S.hold = Math.max(0, S.hold - 1);
      document.removeEventListener("pointerdown", away, true);
      document.removeEventListener("keydown", keydown, true);
      window.removeEventListener("hashchange", close);
      anchor.setAttribute("aria-expanded", "false");
      S.ui.usAccounts = selected.size === all.length ? null : [...selected];
      window.dispatchEvent(new Event("dash:render"));
      root.querySelector("[data-usage-picker]")?.focus();
    };
    const away = (e) => { if (!popup.contains(e.target)) close(); };
    const keydown = (e) => {
      if (e.key === "Escape" || e.key === "Tab") { close(); return; }
      const group = e.target.closest("[data-provider]")?.dataset.provider;
      if (e.key === "ArrowLeft" && group && group !== "all") { e.preventDefault(); hovered = group; draw(); popup.querySelector("[data-account]")?.focus(); }
      if (e.key === "ArrowRight" && e.target.closest("[data-account]")) { e.preventDefault(); const p = hovered; hovered = ""; draw(); popup.querySelector(`[data-provider="${p}"]`)?.focus(); }
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault(); const items = [...e.target.parentElement.querySelectorAll("button")];
        items[(items.indexOf(e.target) + (e.key === "ArrowDown" ? 1 : -1) + items.length) % items.length]?.focus();
      }
    };
    popup.addEventListener("pointerover", (e) => { const p = e.target.closest("[data-provider]")?.dataset.provider; if (p && hovered !== (p === "all" ? "" : p)) { hovered = p === "all" ? "" : p; draw(); } });
    popup.addEventListener("click", (e) => {
      const row = e.target.closest("button"); if (!row) return;
      const ids = row.dataset.account ? [row.dataset.account] : groupIds(row.dataset.provider);
      const remove = ids.every((id) => selected.has(id));
      for (const id of ids) remove ? selected.delete(id) : selected.add(id);
      const account = row.dataset.account, provider = row.dataset.provider;
      draw();
      [...popup.querySelectorAll("button")].find((b) => account ? b.dataset.account === account : b.dataset.provider === provider)?.focus();
    });
    document.addEventListener("pointerdown", away, true);
    document.addEventListener("keydown", keydown, true);
    window.addEventListener("hashchange", close);
    draw(); popup.querySelector("button")?.focus();
  };
}

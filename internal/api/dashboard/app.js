// Shell, router and data polling for the dashboard.
import { S, esc, icon, applyTheme, closeMenu, fetchData, setScreenWants, dataCurrent } from "./core.js";
import * as overview from "./screens/overview.js";
import * as accounts from "./screens/accounts.js";
import * as account from "./screens/account.js";
import * as telemetry from "./screens/telemetry.js";
import * as sessions from "./screens/sessions.js";
import * as session from "./screens/session.js";
import * as routing from "./screens/routing.js";
import * as settings from "./screens/settings.js";
import { V, allViews, loadViews, redirect, viewHref } from "./screens/views.js";
import { viewMenu, newViewFromSidebar } from "./screens/viewmenus.js";

const NAV = [
  { id: "overview", href: "#/", label: "Overview", icon: "overview" },
  { id: "accounts", href: "#/accounts", label: "Accounts", icon: "accounts" },
  { id: "sessions", href: "#/sessions", label: "Sessions", icon: "sessions" },
  { id: "routing", href: "#/routing", label: "Routing", icon: "routing" },
];
const VIEW_ICON = { allowance: "battery", usage: "usage", performance: "performance" };
const MORE = `<svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">${[3.5, 8, 12.5].map((x) => `<circle cx="${x}" cy="8" r="1.25" fill="var(--icon)"/>`).join("")}</svg>`;

// Nav links, then the Telemetry group: built-in views, saved views in the
// order they were made, and a row to make a new one.
function navLinks() {
  const links = NAV.map((n) => `<a href="${n.href}" data-nav="${n.id}">${icon(n.icon)}<span>${n.label}</span></a>`).join("");
  const views = allViews(V.store).map((v) => `<div class="vrow" data-vrow="${esc(v.id)}"><a href="${viewHref(v.id)}" data-nav="view:${esc(v.id)}">${icon(VIEW_ICON[v.id] || "stack")}<span>${esc(v.name)}</span></a><button class="vmore" data-vmore="${esc(v.id)}" data-anchor="vmore-${esc(v.id)}" aria-label="${esc(v.name)} options" aria-haspopup="menu">${MORE}</button></div>`).join("");
  return `${links}<div class="navlabel">Telemetry</div>${views}<a href="#/telemetry" class="newview" data-newview>${icon("plus")}<span>New view</span></a>`;
}

function route() {
  const parts = location.hash.replace(/^#\/?/, "").split("/").filter(Boolean).map(decodeURIComponent);
  const [a, b] = parts;
  if (!a) return { nav: "overview", screen: overview, params: [] };
  if (a === "accounts" && b === "add") return { nav: "accounts", screen: accounts, params: [], add: true };
  if (a === "accounts" && b) return { nav: "accounts", screen: account, params: [b] };
  if (a === "sessions" && b) return { nav: "sessions", screen: session, params: [b] };
  if (a === "telemetry") return { nav: "view:" + (b || ""), screen: telemetry, params: [b || ""] };
  const map = { accounts, sessions, routing, settings };
  return map[a] ? { nav: a, screen: map[a], params: [] } : { nav: "overview", screen: overview, params: [] };
}

// Old routes and the bare Telemetry route open a view. Saved views are
// known only once loaded, so a route that may name one waits for them.
function redirected() {
  const h = location.hash;
  if (!/^#\/(usage|performance|telemetry)/.test(h)) return false;
  const known = /^#\/(usage|performance)/.test(h) || V.loaded;
  if (!known) { loadViews().then(() => { redirected(); render(); }); return false; }
  const to = redirect(h, V.store);
  if (to && to !== h) { location.replace(to); return true; }
  return false;
}

// The windows the current screen draws, so each load fetches exactly them.
setScreenWants(() => { const r = route(); return r.screen.wants ? r.screen.wants(r.params) : null; });

const app = document.getElementById("app");
let lastKey = "";
let pending = false;
let focusKey = "";
let navSig = "";

function hostName() {
  const h = S.data?.server?.host;
  if (h) return h;
  const n = location.hostname.split(".")[0];
  return /^\d+$/.test(n) || n === "localhost" ? "This machine" : n;
}

function shell() {
  app.innerHTML = `
    <aside class="side">
      <div class="brand">${icon("share", 18, "var(--fg)")}<span class="strong">CLI proxy</span><span class="muted">Personal</span></div>
      <nav class="nav" id="nav"></nav>
      <div class="spacer"></div>
      <div class="nav foot only-wide">
        <a href="#/settings" data-nav="settings">${icon("settings")}<span>Settings</span></a>
        <div class="host">${icon("server")}<span id="hostname"></span><span class="muted">Tailnet</span></div>
      </div>
    </aside>
    <main class="main" id="main"></main>`;
  drawNav();
}

// Redraws the sidebar when the views in it change.
function drawNav() {
  const sig = JSON.stringify(allViews(V.store).map((v) => [v.id, v.name]));
  if (sig === navSig) return;
  navSig = sig;
  const nav = document.getElementById("nav");
  nav.innerHTML = `${navLinks()}<a class="only-narrow" href="#/settings" data-nav="settings">${icon("settings")}<span>Settings</span></a>`;
  nav.querySelectorAll("[data-vmore]").forEach((b) => { b.onclick = (e) => { e.preventDefault(); viewMenu(b, b.dataset.vmore); }; });
  nav.querySelector("[data-newview]").onclick = (e) => { e.preventDefault(); newViewFromSidebar(); };
}

export function render() {
  if (S.hold > 0) { pending = true; return; }
  pending = false;
  if (redirected()) return;
  const r = route();
  const key = location.hash;
  const main = document.getElementById("main");
  focusKey = document.activeElement?.closest?.("#main") ? document.activeElement.dataset.focusKey || focusKey : focusKey;
  const body = main.querySelector(".body");
  const keep = key === lastKey && body ? body.scrollTop : 0;
  const winKeep = key === lastKey ? window.scrollY : 0;
  drawNav();
  const nav = r.nav === "view:" ? "" : r.nav;
  app.querySelectorAll("[data-nav]").forEach((a) => a.classList.toggle("on", a.dataset.nav === nav));
  document.getElementById("hostname").textContent = hostName();
  if (!S.data) {
    main.innerHTML = `<div class="bar titled"><span class="title">Loading</span></div><div class="body">${S.error ? `<div class="banner">${esc(S.error)}</div>` : ""}</div>`;
    return;
  }
  const ctx = { params: r.params, add: r.add, rerender: render };
  let out;
  try {
    out = r.screen.view(ctx);
  } catch (e) {
    console.error(e);
    out = { html: `<div class="bar titled"><span class="title">Something broke</span></div><div class="body"><div class="banner">${esc(e.message)}</div></div>` };
  }
  main.innerHTML = out.html;
  if (S.error) main.querySelector(".body")?.insertAdjacentHTML("afterbegin", `<div class="banner">${esc(S.error)}</div>`);
  const nb = main.querySelector(".body");
  if (nb && keep) nb.scrollTop = keep;
  if (winKeep && window.scrollY !== winKeep) window.scrollTo(0, winKeep);
  lastKey = key;
  if (out.mount) {
    try { out.mount(main, ctx); } catch (e) { console.error(e); }
  }
  // Keyboard focus on a plot or selection survives the redraw.
  if (focusKey) {
    const el = main.querySelector(`[data-focus-key="${focusKey}"]`);
    if (el) el.focus({ preventScroll: true });
    focusKey = "";
  }
}

export async function load() {
  try {
    await fetchData();
    S.error = "";
  } catch (e) {
    S.error = (e.message || String(e)) + (S.data ? ". Showing the last reading." : "");
  }
  render();
  // The stored account selection is known only once accounts have loaded,
  // and a screen's window only once it has drawn, so the first reading may
  // need another.
  if (!S.error && S.data && !dataCurrent()) load();
}

window.addEventListener("hashchange", () => {
  closeMenu();
  focusKey = "";
  S.hold = 0;
  render();
  document.getElementById("main").querySelector(".body")?.scrollTo(0, 0);
  if (S.data && !dataCurrent()) load();
});
window.addEventListener("dash:refresh", load);
window.addEventListener("dash:render", render);
let resizeTimer;
window.addEventListener("resize", () => { clearTimeout(resizeTimer); resizeTimer = setTimeout(render, 100); });
let usageLoadTimer;
window.addEventListener("dash:usage-window", () => {
  clearTimeout(usageLoadTimer);
  usageLoadTimer = setTimeout(() => { if (!dataCurrent()) load(); }, 180);
});
// A held render waits while a button is pressed: replacing the screen between
// mousedown and click would swallow the click. The click runs in the same task
// as pointerup, so the flag clears just after it.
let pressed = false;
document.addEventListener("pointerdown", () => { pressed = true; }, true);
document.addEventListener("pointerup", () => setTimeout(() => { pressed = false; }, 0), true);
document.addEventListener("pointercancel", () => { pressed = false; }, true);
setInterval(() => { if (pending && S.hold === 0 && !pressed) render(); }, 500);
setInterval(load, 15000);
document.addEventListener("visibilitychange", () => { if (!document.hidden) load(); });

applyTheme();
shell();
loadViews().then(() => render());
load();

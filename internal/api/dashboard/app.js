// Shell, router and data polling for the dashboard.
import { S, esc, icon, applyTheme, closeMenu, fetchData, wantRange, wantScope } from "./core.js";
import * as overview from "./screens/overview.js";
import * as accounts from "./screens/accounts.js";
import * as account from "./screens/account.js";
import * as usage from "./screens/usage.js";
import * as performance from "./screens/performance.js";
import * as sessions from "./screens/sessions.js";
import * as session from "./screens/session.js";
import * as routing from "./screens/routing.js";
import * as settings from "./screens/settings.js";

const NAV = [
  { id: "overview", href: "#/", label: "Overview", icon: "overview" },
  { id: "accounts", href: "#/accounts", label: "Accounts", icon: "accounts" },
  { id: "sessions", href: "#/sessions", label: "Sessions", icon: "sessions" },
  { id: "routing", href: "#/routing", label: "Routing", icon: "routing" },
  { id: "usage", href: "#/usage", label: "Usage", icon: "usage", group: "Telemetry" },
  { id: "performance", href: "#/performance", label: "Performance", icon: "performance", group: "Telemetry" },
];

// Nav links, with a label before the first link of each group.
function navLinks() {
  let group = "";
  return NAV.map((n) => {
    const label = n.group && n.group !== group ? `<div class="navlabel">${esc(n.group)}</div>` : "";
    group = n.group || "";
    return `${label}<a href="${n.href}" data-nav="${n.id}">${icon(n.icon)}<span>${n.label}</span></a>`;
  }).join("");
}

function route() {
  const parts = location.hash.replace(/^#\/?/, "").split("/").filter(Boolean).map(decodeURIComponent);
  const [a, b] = parts;
  if (!a) return { nav: "overview", screen: overview, params: [] };
  if (a === "accounts" && b === "add") return { nav: "accounts", screen: accounts, params: [], add: true };
  if (a === "accounts" && b) return { nav: "accounts", screen: account, params: [b] };
  if (a === "sessions" && b) return { nav: "sessions", screen: session, params: [b] };
  const map = { accounts, usage, performance, sessions, routing, settings };
  return map[a] ? { nav: a, screen: map[a], params: [] } : { nav: "overview", screen: overview, params: [] };
}

const app = document.getElementById("app");
let lastKey = "";
let pending = false;

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
      <nav class="nav" id="nav">${navLinks()}<a class="only-narrow" href="#/settings" data-nav="settings">${icon("settings")}<span>Settings</span></a></nav>
      <div class="spacer"></div>
      <div class="nav foot only-wide">
        <a href="#/settings" data-nav="settings">${icon("settings")}<span>Settings</span></a>
        <div class="host">${icon("server")}<span id="hostname"></span><span class="muted">Tailnet</span></div>
      </div>
    </aside>
    <main class="main" id="main"></main>`;
}

export function render() {
  if (S.hold > 0) { pending = true; return; }
  pending = false;
  const r = route();
  const key = location.hash;
  const main = document.getElementById("main");
  const body = main.querySelector(".body");
  const keep = key === lastKey && body ? body.scrollTop : 0;
  app.querySelectorAll("[data-nav]").forEach((a) => a.classList.toggle("on", a.dataset.nav === r.nav));
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
  lastKey = key;
  if (out.mount) out.mount(main, ctx);
}

export async function load() {
  try {
    await fetchData();
    S.error = "";
  } catch (e) {
    S.error = (e.message || String(e)) + (S.data ? ". Showing the last reading." : "");
  }
  render();
}

window.addEventListener("hashchange", () => {
  closeMenu();
  S.hold = 0;
  render();
  document.getElementById("main").querySelector(".body")?.scrollTo(0, 0);
  if (S.data && (wantRange() !== S.dataRange || wantScope() !== S.dataScope)) load();
});
window.addEventListener("dash:refresh", load);
window.addEventListener("dash:render", render);
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
load();

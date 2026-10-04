// Accounts: every signed-in account with its allowance, plan and mode.
// #/accounts/add opens the Add account drawer over it.
import {
  S, esc, int, icon, logo, email, pill, warnState, accounts, status, limits, left, queue, plan, planDay, day,
  meterCell, table, modeMenu, planMenu, modeTitle, MODES, api, setMode, toast,
} from "../core.js";
import { providerTabs, bindProviderTabs } from "./common.js";

function nextIds() {
  return new Set(["claude", "codex"].map((p) => queue(p).next?.id).filter(Boolean));
}

export function accountBadge(a, next) {
  const st = status(a);
  if (st.kind === "blocked" || st.kind === "error") return warnState(st.text);
  if (next.has(a.id)) return pill("Next");
  return "";
}

export function planCell(a) {
  const p = plan(a);
  if (p.ends) return warnState("Ends " + planDay(p.ends), "sm");
  if (p.renews) return esc(planDay(p.renews));
  return `<span class="muted">–</span>`;
}

function weekCells(a) {
  const st = status(a);
  const lim = limits(a);
  const w = lim.week, s = lim.short;
  if (st.kind === "off" || st.kind === "blocked" || st.kind === "error") return ["", "", ""];
  const resetAt = st.kind === "usedup" ? st.until || w?.reset : w?.reset;
  const weekLeft = st.kind === "usedup" ? meterCell(0) : w ? (w.notStarted ? `<span class="muted">New week</span>` : meterCell(left(w))) : `<span class="muted">No reading</span>`;
  const shortLeft = s && st.kind !== "usedup" ? meterCell(left(s)) : "";
  return [weekLeft, resetAt ? esc(day(resetAt)) : "", shortLeft];
}

export function view(ctx) {
  const prov = S.ui.acProv || "all";
  const all = accounts();
  const ordered = ["claude", "codex"].flatMap((p) => { const q = queue(p); return [...q.order, ...q.rest]; });
  const list = ordered.filter((a) => prov === "all" || a.provider === prov);
  const next = nextIds();
  const counts = { all: all.length, claude: all.filter((a) => a.provider === "claude").length, codex: all.filter((a) => a.provider === "codex").length };
  const cols = [
    { label: "", w: 16, cls: "ic" },
    { label: "Account" },
    { label: "Week left", w: 120 },
    { label: "Week resets", w: 104 },
    { label: "Plan renews", w: 128 },
    { label: "5 hours left", w: 120 },
    { label: "Requests today", w: 96, r: true },
    { label: "Mode", w: 104, cls: "modecol" },
    { label: "", w: 20 },
  ];
  const rows = list.map((a) => {
    const [wl, wr, sl] = weekCells(a);
    const today = S.data?.summary?.accounts?.[a.id]?.today?.requests || 0;
    return {
      cls: "click",
      attrs: `data-href="#/accounts/${encodeURIComponent(a.id)}"`,
      cells: [
        logo(a.provider),
        `${email(a.email)}${accountBadge(a, next)}`,
        wl, wr, planCell(a), sl,
        int(today),
        `<button class="select" data-mode="${esc(a.id)}">${esc(modeTitle(a.mode))}${icon("chevronUpDown", 12)}</button>`,
        `<button class="iconbtn" data-more="${esc(a.id)}" aria-label="More" style="width:20px;height:20px">${icon("more")}</button>`,
      ],
    };
  });
  const html = `
    <div class="bar">${providerTabs("acProv", counts)}<a class="btn" href="#/accounts/add">${icon("plus", 14, "var(--fg)")}Add account</a></div>
    <div class="body">
      ${table(cols, rows, { empty: "No accounts signed in yet." })}
      <div class="tfoot">${list.length} of ${all.length}</div>
    </div>
    ${ctx.add ? drawer() : ""}`;
  return {
    html,
    mount(root) {
      bindProviderTabs(root, "acProv");
      root.querySelectorAll(".modecol").forEach((c) => { c.style.paddingLeft = "16px"; });
      root.querySelectorAll("[data-href]").forEach((r) => {
        r.onclick = (e) => { if (!e.target.closest("button")) location.hash = r.dataset.href; };
      });
      root.querySelectorAll("[data-mode]").forEach((b) => { b.onclick = () => modeMenu(b, all.find((a) => a.id === b.dataset.mode)); });
      root.querySelectorAll("[data-more]").forEach((b) => {
        const a = all.find((x) => x.id === b.dataset.more);
        b.onclick = () => planMenu(b, a, [{ a: "Open details", run: () => { location.hash = "#/accounts/" + encodeURIComponent(a.id); } }]);
      });
      if (ctx.add) mountDrawer(root);
    },
  };
}

// ---------- Add account ----------

const PROVIDERS = [{ id: "claude", label: "Claude" }, { id: "codex", label: "Codex" }];

function drawer() {
  const prov = S.ui.addProv || "claude";
  const mode = S.ui.addMode || "rotation";
  const st = S.ui.addState || null;
  const host = S.data?.server?.host || "the proxy";
  let body;
  if (st?.phase === "waiting") {
    const privacy = st.prov === "codex" ? "https://chatgpt.com/#settings/DataControls" : "https://claude.ai/settings/data-privacy-controls";
    body = `<div class="note"><b>Finish signing in in the new tab.</b><span>Sign in as the account you want to add and click Authorize. This page updates by itself when the account is saved.</span><span>Then switch off model training on its <a class="strong" href="${privacy}" target="_blank" rel="noopener">privacy page</a>.</span></div>
      <div><label class="k">If the page after Authorize does not load, copy its address and paste it here</label>
      <div class="row gap8"><input class="input grow" data-paste value="${esc(S.ui.addPaste || "")}" placeholder="http://localhost:.../callback?code=..."><button class="btn outline" data-submit>Send</button></div></div>`;
  } else if (st?.phase === "error") {
    body = `<div class="note"><b class="warn">Sign-in did not finish.</b><span>${esc(st.msg)}</span></div>`;
  } else {
    body = `<div class="muted">Sign in from home with no VPN on, as with cliproxy-login. The sign-in opens in a new tab.</div>`;
  }
  return `<div class="scrim" data-close></div>
  <div class="drawer" role="dialog" aria-label="Add account">
    <div class="dh"><span>Add account</span><button class="iconbtn" data-close aria-label="Close">${icon("close")}</button></div>
    <div class="db">
      <div><label class="k">Provider</label><div class="provseg">${PROVIDERS.map((p) => `<button class="${p.id === prov ? "on" : ""}" data-prov-pick="${p.id}" ${st?.phase === "waiting" ? "disabled" : ""}>${logo(p.id)}${p.label}</button>`).join("")}</div></div>
      <div><label class="k">Mode</label><div class="radios">${MODES.map((m) => `<button class="${m.id === mode ? "on" : ""}" data-mode-pick="${m.id}"><span class="dot"></span><span class="tx"><div class="a">${m.a}</div><div class="b">${m.b}</div></span></button>`).join("")}</div></div>
      ${body}
    </div>
    <div class="df"><button class="btn" data-close>Cancel</button><button class="btn primary" data-start ${st?.phase === "waiting" ? "disabled" : ""}>Sign in on ${esc(host)}${icon("external", 14)}</button></div>
  </div>`;
}

let poll = null;

function stopFlow() {
  if (poll) clearInterval(poll);
  poll = null;
  S.ui.addState = null;
  S.ui.addPaste = "";
}

function mountDrawer(root) {
  const rerender = () => window.dispatchEvent(new Event("dash:render"));
  const close = () => { stopFlow(); location.hash = "#/accounts"; };
  root.querySelectorAll("[data-close]").forEach((b) => { b.onclick = close; });
  root.querySelectorAll("[data-prov-pick]").forEach((b) => { b.onclick = () => { S.ui.addProv = b.dataset.provPick; rerender(); }; });
  root.querySelectorAll("[data-mode-pick]").forEach((b) => { b.onclick = () => { S.ui.addMode = b.dataset.modePick; rerender(); }; });
  const start = root.querySelector("[data-start]");
  start.onclick = async () => {
    const prov = S.ui.addProv || "claude";
    const win = window.open("", "_blank");
    try {
      const before = accounts().map((a) => a.id);
      const res = await api(`/oauth/auth-url?provider=${prov}&is_webui=true`);
      if (!res?.url || !res?.state) throw new Error("The proxy did not return a sign-in link.");
      if (win) { win.opener = null; win.location = res.url; } else window.open(res.url, "_blank", "noopener");
      S.ui.addState = { phase: "waiting", state: res.state, prov, before };
      rerender();
      watch();
    } catch (e) {
      if (win) win.close();
      if (!e.cancelled) { S.ui.addState = { phase: "error", msg: e.message }; rerender(); }
    }
  };
  const paste = root.querySelector("[data-paste]");
  if (paste) paste.oninput = () => { S.ui.addPaste = paste.value; };
  const submit = root.querySelector("[data-submit]");
  if (submit) {
    submit.onclick = async () => {
      const v = root.querySelector("[data-paste]").value.trim();
      if (!v) return;
      try {
        await api("/oauth/callback", { method: "POST", body: JSON.stringify({ redirect_url: v, state: S.ui.addState.state }) });
        toast("Sent. Waiting for the proxy to finish.");
      } catch (e) { toast(e.message, true); }
    };
  }
}

function watch() {
  if (poll) clearInterval(poll);
  poll = setInterval(async () => {
    const st = S.ui.addState;
    if (!st || st.phase !== "waiting") return stopFlow();
    try {
      const r = await api(`/oauth/status?state=${encodeURIComponent(st.state)}`);
      if (r?.status === "ok") {
        clearInterval(poll);
        poll = null;
        await finish(st);
      } else if (r?.status === "error") {
        S.ui.addState = { phase: "error", msg: r.error || "The sign-in failed." };
        clearInterval(poll);
        poll = null;
        window.dispatchEvent(new Event("dash:render"));
      }
    } catch (e) { /* keep waiting; a blip should not end the flow */ }
  }, 2000);
}

async function finish(st) {
  const mode = S.ui.addMode || "rotation";
  const res = await fetch("/dashboard/data?range=" + encodeURIComponent(S.range), { cache: "no-store" });
  S.data = await res.json();
  const added = accounts().find((a) => a.provider === st.prov && !st.before.includes(a.id));
  S.ui.addState = null;
  if (added && mode !== "rotation") {
    try { await setMode(added, mode); } catch (e) { toast("Signed in, but the mode did not change: " + e.message, true); }
  }
  toast(added ? added.email + " added" : "Signed in");
  location.hash = added ? "#/accounts/" + encodeURIComponent(added.id) : "#/accounts";
  window.dispatchEvent(new Event("dash:refresh"));
}

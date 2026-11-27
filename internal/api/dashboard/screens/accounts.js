// Accounts: every signed-in account with its allowance, plan and mode.
// #/accounts/add opens the Add account drawer over it.
import {
  S, esc, int, icon, logo, email, pill, warnState, warnIcon, askKey, accounts, status, limits, left, queue, plan, planDay, day,
  meterCell, table, modeMenu, planMenu, modeTitle, MODES, api, setMode, toast, fetchData, accountScope,
} from "../core.js";
import { accountPicker, bindAccountPicker } from "./account-picker.js";

function nextIds() {
  return new Set(["claude", "codex"].map((p) => queue(p).next?.id).filter(Boolean));
}

export function accountBadge(a, next) {
  const st = status(a);
  if (st.kind === "blocked" || st.kind === "error") {
    if (a.can_reauthenticate) {
      const reason = st.kind === "error" && st.text !== "Error" ? warnState(st.text) : "";
      return `${reason}<button class="btn outline" data-reauth="${esc(a.id)}" title="${esc(st.detail || st.text)}" aria-label="Sign in again as ${esc(a.email)}">${warnIcon(14)}Sign in again</button>`;
    }
    return warnState(st.text);
  }
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
  const keep = new Set(accountScope().ids);
  const all = accounts();
  const ordered = ["claude", "codex"].flatMap((p) => { const q = queue(p); return [...q.order, ...q.rest]; });
  const list = ordered.filter((a) => keep.has(a.id));
  const next = nextIds();
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
    <div class="bar"><div class="end"><a class="btn" href="#/accounts/add">${icon("plus", 14, "var(--fg)")}Add account</a>${accountPicker()}</div></div>
    <div class="body">
      ${table(cols, rows, { empty: "No accounts signed in yet." })}
      <div class="tfoot">${list.length} of ${all.length}</div>
    </div>
    ${ctx.add ? drawer() : ""}`;
  return {
    html,
    mount(root) {
      bindAccountPicker(root);
      root.querySelectorAll(".modecol").forEach((c) => { c.style.paddingLeft = "16px"; });
      root.querySelectorAll("[data-href]").forEach((r) => {
        r.onclick = (e) => { if (!e.target.closest("button")) location.hash = r.dataset.href; };
      });
      root.querySelectorAll("[data-mode]").forEach((b) => { b.onclick = () => modeMenu(b, all.find((a) => a.id === b.dataset.mode)); });
      root.querySelectorAll("[data-more]").forEach((b) => {
        const a = all.find((x) => x.id === b.dataset.more);
        b.onclick = () => planMenu(b, a, [{ a: "Open details", run: () => { location.hash = "#/accounts/" + encodeURIComponent(a.id); } }]);
      });
      root.querySelectorAll("[data-reauth]").forEach((button) => {
        button.onclick = () => {
          const target = all.find((a) => a.id === button.dataset.reauth);
          button.disabled = true;
          location.hash = "#/accounts/add";
          startSignIn(target);
        };
      });
      if (ctx.add) mountDrawer(root);
    },
  };
}

// ---------- Add account ----------

const PROVIDERS = [{ id: "claude", label: "Claude" }, { id: "codex", label: "Codex" }];

function drawer() {
  const target = S.ui.reauthTarget;
  const prov = target?.provider || S.ui.addProv || "claude";
  const mode = S.ui.addMode || "rotation";
  const st = S.ui.addState || null;
  const host = S.data?.server?.host || "the proxy";
  const title = target ? "Sign in again" : "Add account";
  const busy = st?.phase === "starting" || st?.phase === "waiting";
  let body;
  if (st?.phase === "waiting") {
    const privacy = st.prov === "codex" ? "https://chatgpt.com/#settings/DataControls" : "https://claude.ai/settings/data-privacy-controls";
    body = `<div class="note"><b>Finish signing in in the new tab.</b><span>${target ? `Choose ${esc(target.email)} and its original workspace.` : "Sign in as the account you want to add and click Authorize."} On a Mac with the sign-in helper, the tab comes back to the dashboard by itself, and this page updates when the account is saved.</span><span>Then switch off model training on its <a class="strong" href="${privacy}" target="_blank" rel="noopener">privacy page</a>.</span></div>
      ${st.link ? `<a class="btn outline" href="${esc(st.link)}" target="_blank" rel="noopener">Open sign-in page</a>` : ""}
      <div><label class="k">If the tab shows an error page after Authorize instead, copy its address and paste it here</label>
      <div class="row gap8"><input class="input grow" data-paste value="${esc(S.ui.addPaste || "")}" placeholder="http://localhost:.../callback?code=..."><button class="btn outline" data-submit>Send</button></div></div>`;
  } else if (st?.phase === "starting") {
    body = `<div class="muted">Opening sign-in...</div>`;
  } else if (st?.phase === "error") {
    body = `<div class="note"><b class="warn">${esc(st.title || "Sign-in did not finish.")}</b><span>${esc(st.msg)}</span></div>`;
  } else {
    body = `<div class="muted">Sign in from home with no VPN on, as with cliproxy-login. The sign-in opens in a new tab.</div>`;
  }
  return `<div class="scrim" data-close></div>
  <div class="drawer" role="dialog" aria-label="${title}">
    <div class="dh"><span>${title}</span><button class="iconbtn" data-close aria-label="Close">${icon("close")}</button></div>
    <div class="db">
      ${target ? `<div><label class="k">Account</label>${email(target.email)}</div>` : `<div><label class="k">Provider</label><div class="provseg">${PROVIDERS.map((p) => `<button class="${p.id === prov ? "on" : ""}" data-prov-pick="${p.id}" ${busy ? "disabled" : ""}>${logo(p.id)}${p.label}</button>`).join("")}</div></div>
      <div><label class="k">Mode</label><div class="radios">${MODES.map((m) => `<button class="${m.id === mode ? "on" : ""}" data-mode-pick="${m.id}"><span class="dot"></span><span class="tx"><div class="a">${m.a}</div><div class="b">${m.b}</div></span></button>`).join("")}</div></div>`}
      ${body}
    </div>
    <div class="df"><button class="btn" data-close>Cancel</button><button class="btn primary" data-start ${busy ? "disabled" : ""}>Sign in on ${esc(host)}${icon("external", 14)}</button></div>
  </div>`;
}

let poll = null;
let flow = 0; // bumped on every start and stop, so replies for an older flow are dropped

function cancelPending(state) {
  if (state && S.key) api(`/oauth/session?state=${encodeURIComponent(state)}`, { method: "DELETE" }).catch((e) => toast(e.message, true));
}

function stopFlow() {
  flow++;
  cancelPending(S.ui.addState?.state);
  if (poll) clearTimeout(poll);
  poll = null;
  S.ui.addState = null;
  S.ui.addPaste = "";
}

// Leaving the drawer by any route (Back, a nav link, Close) ends the flow,
// including a sign-in link request that has not answered yet.
window.addEventListener("hashchange", () => {
  if (location.hash !== "#/accounts/add") { stopFlow(); S.ui.reauthTarget = null; }
});

function fail(id, msg, title) {
  if (id !== flow) return;
  if (poll) clearTimeout(poll);
  poll = null;
  S.ui.addState = { phase: "error", msg, title };
  window.dispatchEvent(new Event("dash:render"));
}

function mountDrawer(root) {
  const rerender = () => window.dispatchEvent(new Event("dash:render"));
  const close = () => { stopFlow(); location.hash = "#/accounts"; };
  root.querySelectorAll("[data-close]").forEach((b) => { b.onclick = close; });
  root.querySelectorAll("[data-prov-pick]").forEach((b) => { b.onclick = () => { S.ui.addProv = b.dataset.provPick; rerender(); }; });
  root.querySelectorAll("[data-mode-pick]").forEach((b) => { b.onclick = () => { S.ui.addMode = b.dataset.modePick; rerender(); }; });
  root.querySelector("[data-start]").onclick = () => startSignIn(S.ui.reauthTarget);
  const paste = root.querySelector("[data-paste]");
  if (paste) paste.oninput = () => { S.ui.addPaste = paste.value; };
  const submit = root.querySelector("[data-submit]");
  if (submit) {
    submit.onclick = async () => {
      const v = root.querySelector("[data-paste]").value.trim();
      if (!v || !S.ui.addState?.state) return;
      try {
        await api("/oauth/callback", { method: "POST", body: JSON.stringify({ redirect_url: v, state: S.ui.addState.state }) });
        toast("Sent. Waiting for the proxy to finish.");
      } catch (e) { toast(e.message, true); }
    };
  }
}

async function startSignIn(target = null) {
  const prov = target?.provider || S.ui.addProv || "claude";
  stopFlow();
  S.ui.reauthTarget = target;
  S.ui.addState = { phase: "starting" };
  const id = flow;
  let win;
  try {
    if (!S.key && !(await askKey())) return fail(id, "Sign-in was cancelled.");
    if (id !== flow) return;
    win = window.open("", "_blank");
    const before = accounts().map((a) => a.id);
    const query = new URLSearchParams({ provider: prov, is_webui: "true" });
    if (target) query.set("reauth_id", target.id);
    window.dispatchEvent(new Event("dash:render"));
    const res = await api(`/oauth/auth-url?${query}`);
    if (id !== flow) { cancelPending(res?.state); if (win) win.close(); return; }
    if (!res?.url || !res?.state) throw new Error("The proxy did not return a sign-in link.");
    if (win) { win.opener = null; win.location = res.url; }
    S.ui.addState = { phase: "waiting", state: res.state, prov, before, target, link: win ? "" : res.url };
    window.dispatchEvent(new Event("dash:render"));
    watch(id);
  } catch (e) {
    if (win) win.close();
    fail(id, e.cancelled ? "Sign-in was cancelled." : e.message);
  }
}

// One status check at a time, two seconds apart, until the flow ends.
function watch(id) {
  const tick = async () => {
    poll = null;
    const st = S.ui.addState;
    if (id !== flow || st?.phase !== "waiting") return;
    let r;
    try {
      r = await api(`/oauth/status?state=${encodeURIComponent(st.state)}`);
    } catch (e) {
      if (e.auth || e.cancelled) return fail(id, "This page stopped checking because it has no valid management key. Start again to enter it.");
      if (id === flow) poll = setTimeout(tick, 2000); // a network blip should not end the flow
      return;
    }
    if (id !== flow) return;
    if (r?.status === "ok") return finish(st, id);
    if (r?.status === "error") return fail(id, r.error || "The sign-in failed.");
    poll = setTimeout(tick, 2000);
  };
  poll = setTimeout(tick, 2000);
}

async function finish(st, id) {
  const mode = S.ui.addMode || "rotation";
  try {
    await fetchData();
  } catch (e) {
    return fail(id, e.message + ". Close this panel; the account shows up when the page next refreshes.", "Signed in, but this page could not reload.");
  }
  if (id !== flow) return;
  if (st.target) {
    S.ui.addState = null;
    toast(st.target.email + " signed in again");
    location.hash = "#/accounts";
    window.dispatchEvent(new Event("dash:refresh"));
    return;
  }
  const added = accounts().find((a) => a.provider === st.prov && !st.before.includes(a.id));
  if (added && mode !== "rotation") {
    try { await setMode(added, mode); } catch (e) { toast("Signed in, but the mode did not change: " + e.message, true); }
  }
  toast(added ? added.email + " added" : "Signed in");
  // The account is saved either way; only move the page if the drawer is still open.
  if (id === flow) {
    S.ui.addState = null;
    location.hash = added ? "#/accounts/" + encodeURIComponent(added.id) : "#/accounts";
  }
  window.dispatchEvent(new Event("dash:refresh"));
}

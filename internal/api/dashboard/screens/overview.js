// Overview: weekly allowance per provider, the last 24 hours, active sessions.
import {
  S, esc, icon, logo, pill, warnIcon, accounts, status, limits, left, queue, plan, planDay, planDaysAway, resetShort, clock,
  providerTitle, sessions, isToday,
} from "../core.js";
import { providerTabs, bindProviderTabs, readAt, usageStrip, sessionTable } from "./common.js";

function planEnding(acct, reset) {
  const p = plan(acct);
  if (!p.ends) return "";
  const days = planDaysAway(p.ends);
  if (days == null || days < 0) return "";
  const resetDays = reset ? Math.ceil((reset - Date.now()) / 864e5) : 99;
  return days <= Math.max(7, resetDays) ? `<span class="state sm">${warnIcon(14)}Plan ends ${esc(planDay(p.ends))}</span>` : "";
}

function accountColumn(acct, isNext) {
  const st = status(acct);
  const week = limits(acct).week;
  const reset = (t) => (t ? `<span class="reset">${icon("reset", 14)}${esc(resetShort(t))}</span>` : "");
  const mail = `<span class="clamp grow">${esc(acct.email)}</span>`;
  let l1 = "", l2 = mail, end = "";
  if (st.kind === "off") {
    l1 = `<span class="t muted">Off</span>`;
  } else if (st.kind === "blocked" || st.kind === "error") {
    l1 = `<span class="state strong">${warnIcon(14)}<span class="clamp">${esc(st.text)}</span></span>`;
  } else if (st.kind === "usedup") {
    l1 = `<span class="t">Used up</span>`;
    end = planEnding(acct, st.until || week?.reset);
    l2 += reset(st.until || week?.reset);
  } else if (st.kind === "limited") {
    l1 = `<span class="t">${week ? Math.round(left(week)) + "% left" : "Resting"}</span>`;
    l2 += st.until ? `<span class="reset">${icon("reset", 14)}back ${esc(clock(st.until))}</span>` : "";
  } else {
    if (week && week.notStarted) l1 = `<span class="t">New week</span>`;
    else l1 = week ? `<span class="t">${Math.round(left(week))}% left</span>` : `<span class="t muted">No reading yet</span>`;
    if (isNext) l1 += pill("Next");
    end = planEnding(acct, week?.reset);
    l2 += reset(week?.reset);
  }
  return `<div class="acctcol"><div class="l1">${l1}${end ? `<span class="end">${end}</span>` : ""}</div><div class="l2">${l2}</div></div>`;
}

function allowanceGroup(provider) {
  const q = queue(provider);
  const list = [...q.order, ...q.rest];
  if (!list.length) return "";
  const segs = list.map((a) => {
    const st = status(a);
    const w = limits(a).week;
    const p = st.kind === "ready" || st.kind === "limited" ? (w ? left(w) : 0) : 0;
    return `<div><i style="width:${Math.round(p)}%"></i></div>`;
  }).join("");
  return `<div class="grp" style="flex-grow:${list.length}">
    <div style="display:flex;flex-direction:column;gap:4px">
      <div class="figlabel">${logo(provider, "sm")}<span>${providerTitle(provider)}, weekly allowance</span></div>
      <div class="bignum"><span class="v">${q.order.length} of ${list.length}</span><span class="u">accounts available</span></div>
    </div>
    <div class="segbar">${segs}</div>
    <div class="acctcols">${list.map((a) => accountColumn(a, q.next && a.id === q.next.id)).join("")}</div>
  </div>`;
}

export function view() {
  const prov = S.ui.ovProv || "all";
  const accts = accounts().filter((a) => prov === "all" || a.provider === prov);
  const ids = accts.map((a) => a.id);
  const groups = (prov === "all" ? ["claude", "codex"] : [prov]).map(allowanceGroup).filter(Boolean).join("");
  const strip = usageStrip({ key: "ovMetric", ids, scope: prov, stackBy: prov === "all" ? "provider" : "account" });

  const all = sessions().filter((s) => prov === "all" || s.provider === prov);
  const active = all.filter((s) => Date.now() - Date.parse(s.last_seen) < 10 * 60e3);
  const today = all.filter((s) => isToday(s.last_seen)).length;

  const html = `
    <div class="bar">${providerTabs("ovProv")}<span class="muted nowrap">${esc(readAt("Live, read"))}</span></div>
    <div class="body">
      ${groups ? `<div class="allow">${groups}</div>` : `<div class="empty">No accounts signed in.</div>`}
      ${strip.html}
      <div class="sec last">
        <div class="sech">
          <div class="t"><b>Active sessions</b><span class="muted">Seen in the last 10 minutes</span></div>
          <a class="linkbtn" href="#/sessions">All ${today} today ${icon("chevronRight", 14)}</a>
        </div>
        ${sessionTable(active, { empty: "No sessions in the last 10 minutes." })}
      </div>
    </div>`;
  return {
    html,
    mount(root) {
      bindProviderTabs(root, "ovProv");
      strip.mount(root);
    },
  };
}

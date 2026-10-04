// Session details: totals, which accounts served it, token mix and threads.
import {
  S, esc, int, fmt, ms, icon, logo, email, pill, warnState, sessions, sessionTitle, sessionHref, names, accountById, status,
  cacheReuse, pctText, clock, isToday, day, seen, table, toast,
} from "../core.js";

function stat(label, value, cls = "", unit = "") {
  return `<div class="stat"><span class="muted">${esc(label)}</span><span class="v ${cls}">${value}${unit ? `<span class="u">${esc(unit)}</span>` : ""}</span></div>`;
}

export function view(ctx) {
  const id = ctx.params[0];
  const all = sessions();
  let s = all.find((x) => x.id === id);
  if (!s) {
    for (const p of all) {
      const t = (p.threads || []).find((x) => x.id === id);
      if (t) { s = { ...t, threads: [], parent: p }; break; }
    }
  }
  if (!s) {
    return { html: `<div class="bar"><div class="crumbs"><a href="#/sessions">Sessions</a>${icon("chevronRight", 14)}<span class="here">Not found</span></div></div><div class="body"><div class="empty">The proxy keeps the most recent sessions only. This one is no longer in its list.</div></div>` };
  }
  const nm = names();
  const ids = s.auth_ids || [];
  const byAuth = s.by_auth || {};
  const serving = s.serving_auth_id || (ids.length === 1 ? ids[0] : "");
  const first = Date.parse(s.first_seen), last = Date.parse(s.last_seen);
  const live = Date.now() - last < 2 * 60e3;
  const span = `${clock(first)} to ${live ? "now" : clock(last)}`;
  const when = isToday(last) ? "today" : day(last);
  const ttft = s.ttft_ms_p50 ? ms(s.ttft_ms_p50) : "–";

  const acctRows = ids.map((aid) => {
    const a = accountById(aid);
    const st = a ? status(a) : null;
    const tag = aid === serving && live ? pill("Serving now") : "";
    const warn = st && (st.kind === "blocked" || st.kind === "error") ? warnState(st.text) : "";
    const b = byAuth[aid];
    return {
      href: a ? "#/accounts/" + encodeURIComponent(aid) : "",
      cells: [logo(a?.provider || s.provider), `${email(nm[aid] || aid)}${tag}${warn}`, b ? int(b.requests) : "", b ? `<span class="${b.failed ? "warn" : "muted"}">${int(b.failed)}</span>` : ""],
    };
  });
  const acctCols = [{ label: "", w: 16, cls: "ic" }, { label: "Account" }, { label: "Requests", w: 88, r: true }, { label: "Failed", w: 88, r: true }];

  const parts = [
    { k: "Cache read", v: s.cache_read_tokens || 0, color: "var(--chart-1)" },
    { k: "Cache write", v: s.cache_write_tokens || 0, color: "var(--chart-p90)" },
    { k: "New input", v: s.input_tokens || 0, color: "var(--chart-p50)" },
    { k: "Output", v: s.output_tokens || 0, color: "var(--chart-p99)" },
  ].filter((p) => p.v > 0);
  const total = parts.reduce((t, p) => t + p.v, 0);

  const threads = (s.threads || []).slice().sort((a, b) => String(b.last_seen).localeCompare(String(a.last_seen)));
  const threadCols = [{ label: "Thread" }, { label: "Model", w: 200 }, { label: "Requests", w: 88, r: true }, { label: "Failed", w: 88, r: true }, { label: "Last seen", w: 88, r: true }];
  const threadRows = threads.map((t) => ({
    href: sessionHref(t),
    cells: [`<span class="mono clamp">${esc(String(t.id).split(":agent:")[1] || t.id)}</span>`, `<span class="muted clamp">${esc(t.model || "")}</span>`, int(t.requests), `<span class="${t.failed ? "warn" : "muted"}">${int(t.failed)}</span>`, seen(t.last_seen)],
  }));
  const crumbParent = s.parent ? `<a href="${sessionHref(s.parent)}">${esc(sessionTitle(s.parent))}</a>${icon("chevronRight", 14)}` : "";

  const html = `
    <div class="bar">
      <div class="crumbs"><a href="#/sessions">Sessions</a>${icon("chevronRight", 14)}${crumbParent}<span class="here"><span class="clamp">${esc(s.parent ? "Thread " + (String(s.id).split(":agent:")[1] || "") : sessionTitle(s))}</span></span></div>
      <div class="end">
        ${s.machine ? `<span class="row gap6 muted ui">${icon("desktop", 14)}${esc(s.machine)}</span>` : ""}
        <button class="iconbtn" data-copy aria-label="Copy session id" title="Copy session id">${icon("copy")}</button>
      </div>
    </div>
    <div class="body">
      <div class="stats">
        ${stat("Requests", int(s.requests))}
        ${stat("Failed", int(s.failed), s.failed ? "warn" : "")}
        ${stat("Active", esc(span), "", when)}
        ${stat("Cache reuse", pctText(cacheReuse(s)))}
        ${stat("First token, median", ttft)}
      </div>
      <div class="sec">
        <div class="sech"><div class="t"><b>Accounts used</b><span class="muted">${ids.length > 1 ? (ids.length === 2 ? "Switched once" : `Switched ${ids.length - 1} times`) : ids.length ? "One account" : "No answer yet"}</span></div>${s.model ? `<span class="muted mono">${esc(s.model)}</span>` : ""}</div>
        ${table(acctCols, acctRows, { empty: "No account has answered yet." })}
      </div>
      <div class="sec ${threads.length ? "" : "last"}" style="padding:20px 24px 28px">
        <div class="t row gap8" style="margin-bottom:16px;align-items:baseline"><b class="ui strong">Tokens</b><span class="muted">${fmt(total)} in all</span></div>
        <div class="stack">${parts.map((p) => `<i style="flex:${p.v} 1 0;background:${p.color}"></i>`).join("") || `<i style="flex:1;background:var(--surface-2)"></i>`}</div>
        <div class="keys" style="margin-top:12px">${parts.map((p) => `<span><i style="background:${p.color}"></i>${esc(p.k)}<b>${fmt(p.v)}</b></span>`).join("")}</div>
      </div>
      ${threads.length ? `<div class="sec last"><div class="sech"><div class="t"><b>Threads</b><span class="muted">${threads.length} agent ${threads.length === 1 ? "thread" : "threads"}</span></div></div>${table(threadCols, threadRows)}</div>` : ""}
    </div>`;
  return {
    html,
    mount(root) {
      root.querySelector("[data-copy]").onclick = async () => {
        const raw = String(s.id).replace(/^[a-z]+:/, "");
        try { await navigator.clipboard.writeText(raw); toast("Session id copied"); } catch (e) { toast(raw); }
      };
    },
  };
}

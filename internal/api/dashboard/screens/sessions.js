// Sessions: every recent session, filterable, a page at a time.
import { S, esc, icon, sessions, sessionTitle, names, menu, isToday, accountScope } from "../core.js";
import { sessionTable, sessionInScope } from "./common.js";
import { accountPicker, bindAccountPicker } from "./account-picker.js";

const PAGE = 12;
const RANGES = [
  { id: "today", label: "Today", keep: (s) => isToday(s.last_seen) },
  { id: "24h", label: "Last 24 hours", keep: (s) => Date.now() - Date.parse(s.last_seen) < 864e5 },
  { id: "7d", label: "Last 7 days", keep: (s) => Date.now() - Date.parse(s.last_seen) < 7 * 864e5 },
];

export function view() {
  const range = RANGES.find((r) => r.id === (S.ui.seRange || "today")) || RANGES[0];
  const sc = accountScope();
  const q = (S.ui.seQuery || "").trim().toLowerCase();
  const nm = names();
  let list = sessions().filter(range.keep).filter((s) => sessionInScope(s, sc));
  if (q) {
    list = list.filter((s) => [sessionTitle(s), s.id, s.machine, ...(s.auth_ids || []).map((id) => nm[id])].some((v) => String(v || "").toLowerCase().includes(q)));
  }
  const switched = list.filter((s) => (s.auth_ids || []).length > 1);
  const failing = list.filter((s) => s.failed > 0);
  const tab = S.ui.seTab || "all";
  const shown = tab === "switched" ? switched : tab === "failed" ? failing : list;
  const pages = Math.max(1, Math.ceil(shown.length / PAGE));
  const page = Math.min(S.ui.sePage || 0, pages - 1);
  const slice = shown.slice(page * PAGE, page * PAGE + PAGE);
  const tabs = [
    { id: "all", label: "All", n: list.length },
    { id: "switched", label: "Switched account", n: switched.length, warn: switched.length > 0 },
    { id: "failed", label: "With failures", n: failing.length, warn: failing.length > 0 },
  ];
  const searching = S.ui.seSearch || q;

  const html = `
    <div class="bar wrap">
      <div class="tabs">${tabs.map((t) => `<button class="tab ${t.id === tab ? "on" : ""}" data-setab="${t.id}">${t.label} <span class="n ${t.warn ? "warn" : ""}">${t.n}</span></button>`).join("")}</div>
      <div class="end">
        ${searching ? `<input class="input" data-q placeholder="Search sessions" value="${esc(S.ui.seQuery || "")}" style="height:28px;width:200px">` : ""}
        <button class="iconbtn" data-search aria-label="Search">${icon("search")}</button>
        ${accountPicker()}
      </div>
    </div>
    <div class="body">
      ${sessionTable(slice, { meter: true, lastSeen: "span", empty: q ? "No sessions match." : "No sessions in this range." })}
      <div class="tfoot"><span>${slice.length} of ${shown.length}</span>
        <span class="row gap4">
          <button class="iconbtn" data-page="-1" ${page === 0 ? "disabled" : ""} aria-label="Previous page">${icon("chevronLeft")}</button>
          <button class="iconbtn" data-page="1" ${page >= pages - 1 ? "disabled" : ""} aria-label="Next page">${icon("chevronRight")}</button>
        </span>
      </div>
      <div class="pad-floater"></div>
    </div>
    <div class="floater"><button class="btn" data-range>${icon("calendar", 14)}<span>${esc(range.label)}</span>${icon("chevronDown", 14)}</button></div>`;

  return {
    html,
    mount(root) {
      const rerender = () => window.dispatchEvent(new Event("dash:render"));
      root.querySelectorAll("[data-setab]").forEach((b) => { b.onclick = () => { S.ui.seTab = b.dataset.setab; S.ui.sePage = 0; rerender(); }; });
      root.querySelectorAll("[data-page]").forEach((b) => { b.onclick = () => { S.ui.sePage = page + Number(b.dataset.page); rerender(); }; });
      root.querySelector("[data-search]").onclick = () => { S.ui.seSearch = !S.ui.seSearch; if (!S.ui.seSearch) S.ui.seQuery = ""; rerender(); };
      const input = root.querySelector("[data-q]");
      if (input) {
        if (S.ui.seSearch && document.activeElement === document.body) input.focus();
        input.oninput = () => { S.ui.seQuery = input.value; S.ui.sePage = 0; const pos = input.selectionStart; rerender(); const again = document.querySelector("[data-q]"); if (again) { again.focus(); again.setSelectionRange(pos, pos); } };
      }
      bindAccountPicker(root);
      root.querySelector("[data-range]").onclick = (e) => menu(e.currentTarget, RANGES.map((r) => ({ a: r.label, on: r.id === range.id, run: () => { S.ui.seRange = r.id; S.ui.sePage = 0; rerender(); } })), { width: 200, alignLeft: true });
    },
  };
}

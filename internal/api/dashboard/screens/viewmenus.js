// Editing views: the Display menu (panels, table columns, default window),
// saving, the new view dialog, the sidebar's view menu and inline rename.
import { S, esc, icon, toast, accountSelection } from "../core.js";
import { check } from "./account-picker.js";
import { PANELS, PANEL_ORDER } from "./panels.js";
import { forgetTime } from "./timeaxis.js";
import {
  V, BUILTINS, COLUMN_IDS, WINDOWS, MAX_VIEWS, findView, isBuiltin, persist, saveView, saveAsNew, resetView, renameView, duplicateView,
  deleteView, setDefault, viewHref,
} from "./views.js";
import { current, edit, dropDraft, COLUMNS } from "./telemetry.js";

const rerender = () => window.dispatchEvent(new Event("dash:render"));
const GRIP = `<svg width="12" height="16" viewBox="0 0 12 16" aria-hidden="true">${[[4, 4], [8, 4], [4, 8], [8, 8], [4, 12], [8, 12]].map(([x, y]) => `<circle cx="${x}" cy="${y}" r="1.25" fill="var(--icon)"/>`).join("")}</svg>`;

// Stores the views and says where, once, when this browser had to keep them.
async function commit(store) {
  const where = await persist(store);
  if (where === "local") toast("Saved in this browser only. The proxy could not store views.");
  rerender();
  return where;
}

// ---------- saving ----------

export async function saveDraft(id) {
  const c = current(id);
  if (!c) return;
  const store = saveView(V.store, c.view);
  dropDraft(id);
  await commit(store);
}

export async function setDefaultView(id) {
  await commit(setDefault(V.store, id));
}

// ---------- popovers ----------

let pop = null;

function closePop() {
  if (!pop) return;
  const p = pop;
  pop = null;
  p.el.remove();
  document.removeEventListener("mousedown", p.away, true);
  document.removeEventListener("keydown", p.key, true);
  window.removeEventListener("hashchange", p.close);
  p.onClose?.();
}

// A popover under anchor, kept open across re-renders of the page; draw()
// fills it and runs again after each change.
function openPop(anchor, cls, draw, { onClose, alignLeft = false } = {}) {
  closePop();
  const el = document.createElement("div");
  el.className = "dpop " + cls;
  document.body.appendChild(el);
  const place = () => {
    // A re-render replaces the anchor; its replacement carries the same data-anchor.
    const a = (!anchor.isConnected && document.querySelector(`[data-anchor="${anchor.dataset.anchor}"]`)) || anchor;
    const r = a.getBoundingClientRect();
    const w = el.offsetWidth, h = el.offsetHeight;
    let x = alignLeft ? r.left : r.right - w;
    x = Math.max(8, Math.min(x, window.innerWidth - w - 8));
    let y = r.bottom + 4;
    if (y + h > window.innerHeight - 8) y = Math.max(8, r.top - h - 4);
    el.style.left = x + "px";
    el.style.top = y + "px";
  };
  const away = (e) => { if (!el.contains(e.target) && !e.target.closest?.("[data-display], [data-vmore]")) closePop(); };
  const key = (e) => { if (e.key === "Escape") { e.preventDefault(); closePop(); anchor.isConnected && anchor.focus(); } };
  pop = { el, away, key, close: closePop, onClose, draw: () => { draw(el); place(); } };
  document.addEventListener("mousedown", away, true);
  document.addEventListener("keydown", key, true);
  window.addEventListener("hashchange", closePop);
  pop.draw();
  return el;
}

// ---------- Display ----------

let displayFor = "";
export const displayOpen = () => !!pop && !!displayFor;

// Panels the view shows, in order, then the rest in the menu's order.
function panelList(v) {
  const on = v.panels.map((p) => p.type);
  return [...on, ...PANEL_ORDER.filter((t) => !on.includes(t))].map((type) => ({ type, on: on.includes(type) }));
}

export function openDisplay(anchor, id) {
  if (displayOpen() && displayFor === id) { closePop(); return; }
  closePop();
  displayFor = id;
  let sub = "";
  const el = openPop(anchor, "display", (box) => {
    const c = current(id);
    if (!c) { closePop(); return; }
    const v = c.view;
    box.setAttribute("role", "dialog");
    box.setAttribute("aria-label", sub ? "Table columns" : "Display");
    if (sub === "columns") {
      box.innerHTML = `<button class="drow" data-back>${icon("chevronLeft", 14)}<span class="nm">Table columns</span></button><div class="dsep"></div>`
        + COLUMN_IDS.map((col) => { const on = v.columns.includes(col); return `<button class="drow" data-col="${col}" role="menuitemcheckbox" aria-checked="${on}">${check(on ? "true" : "false")}<span class="nm">${esc(COLUMNS[col].label)}</span></button>`; }).join("");
      box.querySelector("[data-back]").onclick = () => { sub = ""; pop.draw(); box.querySelector("[data-cols]")?.focus(); };
      box.querySelectorAll("[data-col]").forEach((b) => {
        b.onclick = () => {
          const col = b.dataset.col;
          edit(id, (d) => { d.columns = d.columns.includes(col) ? d.columns.filter((x) => x !== col) : COLUMN_IDS.filter((x) => x === col || d.columns.includes(x)); });
          pop.draw();
          box.querySelector(`[data-col="${col}"]`)?.focus();
        };
      });
      return;
    }
    const list = panelList(v);
    box.innerHTML = `<div class="dsec">Panels</div>
      <div class="dlist" role="list">${list.map((p, i) => `<div class="drow" data-type="${p.type}" data-i="${i}" tabindex="0" role="checkbox" aria-checked="${p.on}" aria-label="${esc(PANELS[p.type].title)}. Alt and arrow keys move it.">
        <span class="grip" data-grip aria-hidden="true">${GRIP}</span>${check(p.on ? "true" : "false")}<span class="nm">${esc(PANELS[p.type].title)}</span></div>`).join("")}</div>
      <div class="dsep"></div>
      <button class="drow" data-cols aria-haspopup="true">${icon("table", 16)}<span class="nm">Table columns</span><span class="muted">${v.columns.length} ${v.columns.length === 1 ? "column" : "columns"}</span>${icon("chevronRight", 14)}</button>
      <div class="dsep"></div>
      <div class="dsec">Default window</div>
      <div class="dseg" role="radiogroup" aria-label="Default window">${WINDOWS.map(([w, label]) => `<button class="${v.window === w ? "on" : ""}" data-win="${w}" role="radio" aria-checked="${v.window === w}">${esc(label)}</button>`).join("")}</div>
      ${c.saved.overridden ? `<div class="dsep"></div><button class="drow" data-builtin-reset>${icon("reset", 16)}<span class="nm">Reset to built-in</span></button>` : ""}
      ${V.note ? `<div class="dnote">${esc(V.note)}</div>` : ""}`;
    const setPanels = (types) => edit(id, (d) => {
      const opts = new Map(d.panels.map((p) => [p.type, p.options]));
      d.panels = types.map((type) => ({ type, options: opts.get(type) || {} }));
    });
    const onTypes = () => panelList(current(id).view).filter((p) => p.on).map((p) => p.type);
    box.querySelectorAll(".drow[data-type]").forEach((row) => {
      const type = row.dataset.type;
      const toggle = () => {
        const cur = panelList(current(id).view);
        const next = cur.map((p) => (p.type === type ? { ...p, on: !p.on } : p)).filter((p) => p.on).map((p) => p.type);
        setPanels(next);
        pop.draw();
        box.querySelector(`.drow[data-type="${type}"]`)?.focus();
      };
      row.addEventListener("click", (e) => { if (!e.target.closest("[data-grip]")) toggle(); });
      row.addEventListener("keydown", (e) => {
        if (e.key === " " || e.key === "Enter") { e.preventDefault(); toggle(); return; }
        if (e.altKey && (e.key === "ArrowUp" || e.key === "ArrowDown")) {
          e.preventDefault();
          const all = panelList(current(id).view);
          const i = all.findIndex((p) => p.type === type), j = i + (e.key === "ArrowUp" ? -1 : 1);
          if (j < 0 || j >= all.length) return;
          [all[i], all[j]] = [all[j], all[i]];
          moveTo(all);
          box.querySelector(`.drow[data-type="${type}"]`)?.focus();
        } else if (e.key === "ArrowUp" || e.key === "ArrowDown") {
          e.preventDefault();
          (e.key === "ArrowUp" ? row.previousElementSibling : row.nextElementSibling)?.focus();
        }
      });
    });
    // Order: the checked panels in list order. Moving an unchecked row only
    // changes where it lands when checked, so the list keeps it in place.
    const moveTo = (all) => {
      const types = all.filter((p) => p.on).map((p) => p.type);
      if (types.join() !== onTypes().join()) setPanels(types);
      pop.draw();
    };
    dragRows(box.querySelector(".dlist"), (order) => {
      const all = panelList(current(id).view);
      moveTo(order.map((t) => all.find((p) => p.type === t)));
    });
    box.querySelector("[data-cols]").onclick = () => { sub = "columns"; pop.draw(); box.querySelector("[data-back]")?.focus(); };
    box.querySelectorAll("[data-win]").forEach((b) => {
      b.onclick = () => { edit(id, (d) => { d.window = b.dataset.win; }); forgetTime("tv"); pop.draw(); };
    });
    const reset = box.querySelector("[data-builtin-reset]");
    if (reset) reset.onclick = async () => { dropDraft(id); forgetTime("tv"); closePop(); await commit(resetView(V.store, id)); };
  }, { onClose: () => { displayFor = ""; document.querySelector("[data-display]")?.classList.remove("open"); } });
  anchor.classList.add("open");
  anchor.setAttribute("aria-expanded", "true");
  el.querySelector(".drow")?.focus();
}

// Drag to reorder rows by their grip. done(order of data-type) runs on drop.
function dragRows(listEl, done) {
  if (!listEl) return;
  listEl.querySelectorAll("[data-grip]").forEach((grip) => {
    grip.addEventListener("pointerdown", (e) => {
      const row = grip.closest(".drow");
      const rows = [...listEl.children];
      const h = row.offsetHeight;
      const y0 = e.clientY;
      let target = rows.indexOf(row);
      const from = target;
      row.classList.add("drag");
      grip.setPointerCapture(e.pointerId);
      e.preventDefault();
      const move = (ev) => {
        const dy = ev.clientY - y0;
        target = Math.max(0, Math.min(rows.length - 1, from + Math.round(dy / h)));
        row.style.transform = `translateY(${dy}px)`;
        rows.forEach((r, i) => {
          if (r === row) return;
          const shift = from < target && i > from && i <= target ? -h : from > target && i < from && i >= target ? h : 0;
          r.style.transform = shift ? `translateY(${shift}px)` : "";
        });
      };
      const up = () => {
        grip.removeEventListener("pointermove", move);
        grip.removeEventListener("pointerup", up);
        grip.removeEventListener("pointercancel", up);
        rows.forEach((r) => { r.style.transform = ""; });
        row.classList.remove("drag");
        const order = rows.map((r) => r.dataset.type);
        order.splice(target, 0, order.splice(from, 1)[0]);
        if (target !== from) done(order);
      };
      grip.addEventListener("pointermove", move);
      grip.addEventListener("pointerup", up);
      grip.addEventListener("pointercancel", up);
    });
  });
}

// ---------- the new view dialog ----------

// Saves a copy of base as a new view and opens it.
export function newViewDialog(base, { title = "Save as new view", name = "" } = {}) {
  closePop();
  const scrim = document.createElement("div");
  scrim.className = "dlg-scrim";
  let keep = false;
  const defName = name || (base.builtin ? `${base.name} copy` : `${base.name} copy`);
  const draw = () => {
    scrim.innerHTML = `<div class="dlg" role="dialog" aria-modal="true" aria-labelledby="dlg-t">
      <h2 id="dlg-t">${esc(title)}</h2>
      <div class="fld"><label for="dlg-name">Name</label><input id="dlg-name" maxlength="80" value="${esc(scrim.querySelector("#dlg-name")?.value ?? defName)}" autocomplete="off"></div>
      <button class="chk" data-keep role="checkbox" aria-checked="${keep}">${check(keep ? "true" : "false")}<span>Keep the account selection with this view</span></button>
      <div class="btns"><button data-cancel>Cancel</button><button class="pri" data-create>Create view</button></div>
    </div>`;
  };
  draw();
  document.body.appendChild(scrim);
  S.hold++;
  const close = () => { scrim.remove(); S.hold = Math.max(0, S.hold - 1); document.removeEventListener("keydown", onKey, true); rerender(); };
  const create = async () => {
    const nm = scrim.querySelector("#dlg-name").value.trim();
    if (!nm) { scrim.querySelector("#dlg-name").focus(); return; }
    const res = saveAsNew(V.store, base, nm, { keepAccounts: keep, accounts: accountSelection() });
    if (!res.id) { toast(`You can keep up to ${MAX_VIEWS - BUILTINS.length} views. Delete one first.`, true); return; }
    if (findView(V.store, base.id)) dropDraft(base.id);
    close();
    await commit(res.store);
    location.hash = viewHref(res.id);
  };
  const onKey = (e) => {
    if (e.key === "Escape") { e.preventDefault(); close(); }
    else if (e.key === "Enter" && e.target.id === "dlg-name") { e.preventDefault(); create(); }
  };
  document.addEventListener("keydown", onKey, true);
  const bind = () => {
    scrim.querySelector("[data-cancel]").onclick = close;
    scrim.querySelector("[data-create]").onclick = create;
    scrim.querySelector("[data-keep]").onclick = () => { keep = !keep; draw(); bind(); scrim.querySelector("[data-keep]").focus(); };
  };
  bind();
  scrim.addEventListener("mousedown", (e) => { if (e.target === scrim) close(); });
  const input = scrim.querySelector("#dlg-name");
  input.focus();
  input.select();
}

// ---------- inline rename ----------

let renaming = "";

// Starts renaming a view in its header, opening it first when needed.
export function startRename(id) {
  closePop();
  renaming = id;
  if (location.hash !== viewHref(id)) location.hash = viewHref(id);
  else rerender();
}

// The rename field for the header, or null while not renaming this view.
export function renameBox(id) {
  if (renaming !== id) return null;
  const v = current(id)?.view;
  return `<input class="rename" data-rename-input aria-label="View name" maxlength="80" value="${esc(v?.name || "")}">`;
}

export function mountRename(root) {
  const input = root.querySelector("[data-rename-input]");
  if (!input) return;
  S.hold++;
  let done = false;
  const finish = async (save) => {
    if (done) return;
    done = true;
    const id = renaming;
    renaming = "";
    S.hold = Math.max(0, S.hold - 1);
    const name = input.value.trim();
    if (save && name && name !== current(id)?.saved.name) {
      if (S.ui.drafts?.[id]) S.ui.drafts[id].name = name;
      await commit(renameView(V.store, id, name));
    } else rerender();
  };
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter") { e.preventDefault(); finish(true); }
    else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); finish(false); }
  });
  input.addEventListener("blur", () => finish(true));
  input.focus();
  input.select();
}

// ---------- the sidebar's view menu ----------

export function viewMenu(anchor, id) {
  if (pop && pop.forMenu === id) { closePop(); return; }
  const v = findView(V.store, id);
  if (!v) return;
  const row = anchor.closest(".vrow");
  anchor.dataset.anchor = "vmore-" + id;
  openPop(anchor, "vmenu", (box) => {
    box.setAttribute("role", "menu");
    box.innerHTML = `<button class="drow" role="menuitem" data-a="rename">${icon("pencil", 16)}<span class="nm">Rename</span></button>
      <button class="drow" role="menuitem" data-a="duplicate">${icon("copy", 16)}<span class="nm">Duplicate</span></button>
      <button class="drow" role="menuitem" data-a="default">${icon("home", 16)}<span class="nm">Set as default</span></button>
      ${isBuiltin(id) ? (v.overridden ? `<div class="dsep"></div><button class="drow" role="menuitem" data-a="reset">${icon("reset", 16)}<span class="nm">Reset to built-in</span></button>` : "")
        : `<div class="dsep"></div><button class="drow danger" role="menuitem" data-a="delete">${icon("trash", 16)}<span class="nm">Delete</span></button>`}`;
    box.querySelectorAll("[data-a]").forEach((b) => {
      b.onclick = async () => {
        const a = b.dataset.a;
        closePop();
        if (a === "rename") startRename(id);
        else if (a === "duplicate") {
          const res = duplicateView(V.store, id);
          if (!res.id) { toast(`You can keep up to ${MAX_VIEWS - BUILTINS.length} views. Delete one first.`, true); return; }
          await commit(res.store);
          location.hash = viewHref(res.id);
        } else if (a === "default") await commit(setDefault(V.store, id));
        else if (a === "reset") { dropDraft(id); await commit(resetView(V.store, id)); }
        else if (a === "delete") {
          const was = location.hash === viewHref(id);
          dropDraft(id);
          await commit(deleteView(V.store, id));
          toast(`Deleted ${v.name}`);
          if (was) location.hash = "#/telemetry";
        }
      };
    });
    box.querySelector(".drow")?.focus();
  }, { alignLeft: true, onClose: () => row?.classList.remove("open") });
  pop.forMenu = id;
  row?.classList.add("open");
}

// The "New view" row: a copy of the open view, or of Allowance elsewhere.
export function newViewFromSidebar() {
  const m = /^#\/telemetry\/([^/]+)/.exec(location.hash);
  const c = m ? current(decodeURIComponent(m[1])) : null;
  const base = c ? c.view : findView(V.store, "allowance");
  newViewDialog(base, { title: "New view", name: "New view" });
}

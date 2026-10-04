// Shared state, data model, formatting and components for the dashboard screens.

export const S = {
  data: null,        // last /dashboard/data payload
  readAt: 0,         // when it arrived
  error: "",
  key: "",
  range: "24h",      // window chosen on the Performance screen
  dataRange: "",     // window the loaded performance data covers
  ui: {},            // per-screen choices that survive a refresh
  hold: 0,           // >0 while a menu, drawer or hover would be lost by a re-render
};

const KEY_STORE = "cliproxy-dashboard-key";
const THEME_STORE = "cliproxy-dashboard-theme";
try { S.key = localStorage.getItem(KEY_STORE) || ""; } catch (e) { /* storage blocked */ }

// Only the Performance screen picks a window; every other screen shows 24 hours.
export const wantRange = () => (location.hash.startsWith("#/performance") ? S.range : "24h");

export async function fetchData() {
  const want = wantRange();
  const res = await fetch("/dashboard/data?range=" + encodeURIComponent(want), { cache: "no-store" });
  if (!res.ok) throw new Error("Could not read the proxy (" + res.status + ")");
  S.data = await res.json();
  S.dataRange = want;
  S.readAt = Date.now();
}

export function setKey(k) {
  S.key = k || "";
  try { k ? localStorage.setItem(KEY_STORE, k) : localStorage.removeItem(KEY_STORE); } catch (e) { /* storage blocked */ }
}

export function theme() {
  try { return localStorage.getItem(THEME_STORE) || "system"; } catch (e) { return "system"; }
}
export function setTheme(t) {
  try { t === "system" ? localStorage.removeItem(THEME_STORE) : localStorage.setItem(THEME_STORE, t); } catch (e) { /* storage blocked */ }
  applyTheme();
}
export function applyTheme() {
  const t = theme();
  if (t === "light" || t === "dark") document.documentElement.dataset.theme = t;
  else delete document.documentElement.dataset.theme;
}

// ---------- formatting ----------

export const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

export function fmt(n) {
  n = Number(n) || 0;
  const a = Math.abs(n);
  if (a >= 1e9) return (n / 1e9).toFixed(a >= 1e10 ? 1 : 2) + "B";
  if (a >= 1e6) return (n / 1e6).toFixed(1) + "M";
  if (a >= 1e3) return (n / 1e3).toFixed(1) + "k";
  return String(Math.round(n));
}

export function int(n) {
  return (Number(n) || 0).toLocaleString("en-GB");
}

export function ms(v) {
  v = Number(v) || 0;
  if (!v) return "–";
  if (v < 1000) return Math.round(v) + "ms";
  if (v < 60000) return (v / 1000).toFixed(v < 10000 ? 1 : 0).replace(/\.0$/, "") + "s";
  const m = Math.floor(v / 60000), s = Math.round((v % 60000) / 1000);
  return m + "m" + (s ? " " + s + "s" : "");
}

export const pctText = (p) => (p == null || !Number.isFinite(p) ? "–" : Math.round(p) + "%");
export const rate = (num, den) => (den ? (num / den) * 100 : null);
export function rateText(num, den) {
  const r = rate(num, den);
  if (r == null) return "–";
  if (r > 0 && r < 10) return r.toFixed(1).replace(/\.0$/, "") + "%";
  return Math.round(r) + "%";
}

// Go encodes an unset time as year 1; anything before 2001 counts as missing.
export const validTime = (iso) => (iso && Date.parse(iso) > Date.UTC(2001, 0, 1) ? iso : "");
const toMs = (t) => (typeof t === "number" ? t : Date.parse(t));

function tz() { return S.data?.summary?.timezone || undefined; }
function partsOf(t, opts) {
  try { return new Date(toMs(t)).toLocaleString("en-GB", { timeZone: tz(), ...opts }); }
  catch (e) { return new Date(toMs(t)).toLocaleString("en-GB", opts); }
}
export const clock = (t) => partsOf(t, { hour: "2-digit", minute: "2-digit", hour12: false });
export const day = (t) => partsOf(t, { weekday: "short", day: "numeric", month: "short" }).replace(",", "");
export const weekdayTime = (t) => partsOf(t, { weekday: "short" }) + " " + clock(t);
export function dayKey(t) {
  const p = partsOf(t, { year: "numeric", month: "2-digit", day: "2-digit" }); // dd/mm/yyyy
  const [d, m, y] = p.split("/");
  return `${y}-${m}-${d}`;
}
export const isToday = (t) => dayKey(t) === dayKey(Date.now());

// "Sun 09:00" inside the coming week, else "Sun 11 Oct".
export function resetShort(t) {
  if (!t) return "";
  const d = toMs(t) - Date.now();
  return d < 7 * 864e5 ? weekdayTime(t) : day(t);
}
// "Today, 20:00" or "Sun 11 Oct, 09:00".
export function resetLong(t) {
  if (!t) return "";
  return (isToday(t) ? "Today" : day(t)) + ", " + clock(t);
}

// A plan date is a calendar day ("2026-10-18"), shown without a time zone shift.
export function planDay(s) {
  if (!s) return "";
  const [y, m, d] = s.split("-").map(Number);
  return new Date(Date.UTC(y, m - 1, d)).toLocaleDateString("en-GB", { timeZone: "UTC", weekday: "short", day: "numeric", month: "short" }).replace(",", "");
}
export function planDaysAway(s) {
  if (!s) return null;
  const [y, m, d] = s.split("-").map(Number);
  const [ty, tm, td] = dayKey(Date.now()).split("-").map(Number);
  return Math.round((Date.UTC(y, m - 1, d) - Date.UTC(ty, tm - 1, td)) / 864e5);
}
export function inDays(n) {
  if (n == null) return "";
  if (n === 0) return "Today";
  if (n === 1) return "Tomorrow";
  if (n < 0) return n === -1 ? "Yesterday" : -n + " days ago";
  return "In " + n + " days";
}

// "Now", "15:38", or "Sun 4 Oct" for older.
export function seen(t) {
  if (!t) return "";
  const age = Date.now() - toMs(t);
  if (age < 2 * 60e3) return "Now";
  return isToday(t) ? clock(t) : day(t);
}

// ---------- icons (heroicons outline, as in the Paper file) ----------

const P = {
  share: "M7.217 10.907a2.25 2.25 0 1 0 0 2.186m0-2.186c.18.324.283.696.283 1.093s-.103.77-.283 1.093m0-2.186 9.566-5.314m-9.566 7.5 9.566 5.314m0 0a2.25 2.25 0 1 0 3.935 2.186 2.25 2.25 0 0 0-3.935-2.186Zm0-12.814a2.25 2.25 0 1 0 3.933-2.185 2.25 2.25 0 0 0-3.933 2.185Z",
  overview: "M2.25 7.125C2.25 6.504 2.754 6 3.375 6h6c.621 0 1.125.504 1.125 1.125v3.75c0 .621-.504 1.125-1.125 1.125h-6a1.125 1.125 0 0 1-1.125-1.125v-3.75ZM14.25 8.625c0-.621.504-1.125 1.125-1.125h5.25c.621 0 1.125.504 1.125 1.125v8.25c0 .621-.504 1.125-1.125 1.125h-5.25a1.125 1.125 0 0 1-1.125-1.125v-8.25ZM3.75 16.125c0-.621.504-1.125 1.125-1.125h5.25c.621 0 1.125.504 1.125 1.125v2.25c0 .621-.504 1.125-1.125 1.125h-5.25a1.125 1.125 0 0 1-1.125-1.125v-2.25Z",
  accounts: "M15 19.128a9.38 9.38 0 0 0 2.625.372 9.337 9.337 0 0 0 4.121-.952 4.125 4.125 0 0 0-7.533-2.493M15 19.128v-.003c0-1.113-.285-2.16-.786-3.07M15 19.128v.106A12.318 12.318 0 0 1 8.624 21c-2.331 0-4.512-.645-6.374-1.766l-.001-.109a6.375 6.375 0 0 1 11.964-3.07M12 6.375a3.375 3.375 0 1 1-6.75 0 3.375 3.375 0 0 1 6.75 0Zm8.25 2.25a2.625 2.625 0 1 1-5.25 0 2.625 2.625 0 0 1 5.25 0Z",
  usage: "M3 13.125C3 12.504 3.504 12 4.125 12h2.25c.621 0 1.125.504 1.125 1.125v6.75C7.5 20.496 6.996 21 6.375 21h-2.25A1.125 1.125 0 0 1 3 19.875v-6.75ZM9.75 8.625c0-.621.504-1.125 1.125-1.125h2.25c.621 0 1.125.504 1.125 1.125v11.25c0 .621-.504 1.125-1.125 1.125h-2.25a1.125 1.125 0 0 1-1.125-1.125V8.625ZM16.5 4.125c0-.621.504-1.125 1.125-1.125h2.25C20.496 3 21 3.504 21 4.125v15.75c0 .621-.504 1.125-1.125 1.125h-2.25a1.125 1.125 0 0 1-1.125-1.125V4.125Z",
  performance: "m3.75 13.5 10.5-11.25L12 10.5h8.25L9.75 21.75 12 13.5H3.75Z",
  sessions: "M20.25 8.511c.884.284 1.5 1.128 1.5 2.097v4.286c0 1.136-.847 2.1-1.98 2.193-.34.027-.68.052-1.02.072v3.091l-3-3c-1.354 0-2.694-.055-4.02-.163a2.115 2.115 0 0 1-.825-.242m9.345-8.334a2.126 2.126 0 0 0-.476-.095 48.64 48.64 0 0 0-8.048 0c-1.131.094-1.976 1.057-1.976 2.192v4.286c0 .837.46 1.58 1.155 1.951m9.345-8.334V6.637c0-1.621-1.152-3.026-2.76-3.235A48.455 48.455 0 0 0 11.25 3c-2.115 0-4.198.137-6.24.402-1.608.209-2.76 1.614-2.76 3.235v6.226c0 1.621 1.152 3.026 2.76 3.235.577.075 1.157.14 1.74.194V21l4.155-4.155",
  routing: "M7.5 21 3 16.5m0 0L7.5 12M3 16.5h13.5m0-13.5L21 7.5m0 0L16.5 12M21 7.5H7.5",
  settings: "M9.594 3.94c.09-.542.56-.94 1.11-.94h2.593c.55 0 1.02.398 1.11.94l.213 1.281c.063.374.313.686.645.87.074.04.147.083.22.127.325.196.72.257 1.075.124l1.217-.456a1.125 1.125 0 0 1 1.37.49l1.296 2.247a1.125 1.125 0 0 1-.26 1.431l-1.003.827c-.293.241-.438.613-.43.992a7.723 7.723 0 0 1 0 .255c-.008.378.137.75.43.991l1.004.827c.424.35.534.955.26 1.43l-1.298 2.247a1.125 1.125 0 0 1-1.369.491l-1.217-.456c-.355-.133-.75-.072-1.076.124a6.47 6.47 0 0 1-.22.128c-.331.183-.581.495-.644.869l-.213 1.281c-.09.543-.56.94-1.11.94h-2.594c-.55 0-1.019-.398-1.11-.94l-.213-1.281c-.062-.374-.312-.686-.644-.87a6.52 6.52 0 0 1-.22-.127c-.325-.196-.72-.257-1.076-.124l-1.217.456a1.125 1.125 0 0 1-1.369-.49l-1.297-2.247a1.125 1.125 0 0 1 .26-1.431l1.004-.827c.292-.24.437-.613.43-.991a6.932 6.932 0 0 1 0-.255c.007-.38-.138-.751-.43-.992l-1.004-.827a1.125 1.125 0 0 1-.26-1.43l1.297-2.247a1.125 1.125 0 0 1 1.37-.491l1.216.456c.356.133.751.072 1.076-.124.072-.044.146-.086.22-.128.332-.183.582-.495.644-.869l.214-1.28ZM15 12a3 3 0 1 1-6 0 3 3 0 0 1 6 0Z",
  server: "M21.75 17.25v-.228a4.5 4.5 0 0 0-.12-1.03l-2.268-9.64a3.375 3.375 0 0 0-3.285-2.602H7.923a3.375 3.375 0 0 0-3.285 2.602l-2.268 9.64a4.5 4.5 0 0 0-.12 1.03v.228m19.5 0a3 3 0 0 1-3 3H5.25a3 3 0 0 1-3-3m19.5 0a3 3 0 0 0-3-3H5.25a3 3 0 0 0-3 3m16.5 0h.008v.008h-.008v-.008Zm-3 0h.008v.008h-.008v-.008Z",
  reset: "M16.023 9.348h4.992v-.001M2.985 19.644v-4.992m0 0h4.992m-4.993 0 3.181 3.183a8.25 8.25 0 0 0 13.803-3.7M4.031 9.865a8.25 8.25 0 0 1 13.803-3.7l3.181 3.182m0-4.991v4.99",
  chevronRight: "m8.25 4.5 7.5 7.5-7.5 7.5",
  chevronLeft: "M15.75 19.5 8.25 12l7.5-7.5",
  chevronDown: "m19.5 8.25-7.5 7.5-7.5-7.5",
  chevronUpDown: "M8.25 15 12 18.75 15.75 15m-7.5-6L12 5.25 15.75 9",
  plus: "M12 4.5v15m7.5-7.5h-15",
  more: "M6.75 12a.75.75 0 1 1-1.5 0 .75.75 0 0 1 1.5 0ZM12.75 12a.75.75 0 1 1-1.5 0 .75.75 0 0 1 1.5 0ZM18.75 12a.75.75 0 1 1-1.5 0 .75.75 0 0 1 1.5 0Z",
  check: "m4.5 12.75 6 6 9-13.5",
  calendar: "M6.75 3v2.25M17.25 3v2.25M3 18.75V7.5a2.25 2.25 0 0 1 2.25-2.25h13.5A2.25 2.25 0 0 1 21 7.5v11.25m-18 0A2.25 2.25 0 0 0 5.25 21h13.5A2.25 2.25 0 0 0 21 18.75m-18 0v-7.5A2.25 2.25 0 0 1 5.25 9h13.5A2.25 2.25 0 0 1 21 11.25v7.5",
  search: "m21 21-5.197-5.197m0 0A7.5 7.5 0 1 0 5.196 5.196a7.5 7.5 0 0 0 10.607 10.607Z",
  funnel: "M12 3c2.755 0 5.455.232 8.083.678.533.09.917.556.917 1.096v1.044a2.25 2.25 0 0 1-.659 1.591l-5.432 5.432a2.25 2.25 0 0 0-.659 1.591v2.927a2.25 2.25 0 0 1-1.244 2.013L9.75 21v-6.568a2.25 2.25 0 0 0-.659-1.591L3.659 7.409A2.25 2.25 0 0 1 3 5.818V4.774c0-.54.384-1.006.917-1.096A48.32 48.32 0 0 1 12 3Z",
  adjust: "M10.5 6h9.75M10.5 6a1.5 1.5 0 1 1-3 0m3 0a1.5 1.5 0 1 0-3 0M3.75 6H7.5m3 12h9.75m-9.75 0a1.5 1.5 0 0 1-3 0m3 0a1.5 1.5 0 0 0-3 0m-3.75 0H7.5m9-6h3.75m-3.75 0a1.5 1.5 0 0 1-3 0m3 0a1.5 1.5 0 0 0-3 0m-9.75 0h9.75",
  copy: "M15.75 17.25v3.375c0 .621-.504 1.125-1.125 1.125h-9.75a1.125 1.125 0 0 1-1.125-1.125V7.875c0-.621.504-1.125 1.125-1.125H6.75a9.06 9.06 0 0 1 1.5.124m7.5 10.376h3.375c.621 0 1.125-.504 1.125-1.125V11.25c0-4.46-3.243-8.161-7.5-8.876a9.06 9.06 0 0 0-1.5-.124H9.375c-.621 0-1.125.504-1.125 1.125v3.5m7.5 10.375H9.375a1.125 1.125 0 0 1-1.125-1.125v-9.25m12 6.625v-1.875a3.375 3.375 0 0 0-3.375-3.375h-1.5a1.125 1.125 0 0 1-1.125-1.125v-1.5a3.375 3.375 0 0 0-3.375-3.375H9.75",
  desktop: "M9 17.25v1.007a3 3 0 0 1-.879 2.122L7.5 21h9l-.621-.621A3 3 0 0 1 15 18.257V17.25m6-12V15a2.25 2.25 0 0 1-2.25 2.25H5.25A2.25 2.25 0 0 1 3 15V5.25m18 0A2.25 2.25 0 0 0 18.75 3H5.25A2.25 2.25 0 0 0 3 5.25m18 0V12a2.25 2.25 0 0 1-2.25 2.25H5.25A2.25 2.25 0 0 1 3 12V5.25",
  close: "M6 18 18 6M6 6l12 12",
  external: "M13.5 6H5.25A2.25 2.25 0 0 0 3 8.25v10.5A2.25 2.25 0 0 0 5.25 21h10.5A2.25 2.25 0 0 0 18 18.75V10.5m-10.5 6L21 3m0 0h-5.25M21 3v5.25",
  key: "M15.75 5.25a3 3 0 0 1 3 3m3 0a6 6 0 0 1-7.029 5.912c-.563-.097-1.159.026-1.563.43L10.5 17.25H8.25v2.25H6v2.25H2.25v-2.818c0-.597.237-1.17.659-1.591l6.499-6.499c.404-.404.527-1 .43-1.563A6 6 0 1 1 21.75 8.25Z",
};

export function icon(name, size = 16, color = "var(--icon)") {
  return `<svg width="${size}" height="${size}" viewBox="0 0 24 24" aria-hidden="true"><path d="${P[name]}" fill="none" stroke="${color}" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>`;
}

export function warnIcon(size = 14) {
  return `<svg width="${size}" height="${size}" viewBox="0 0 16 16" aria-hidden="true"><path fill-rule="evenodd" clip-rule="evenodd" d="M6.701 2.25c.577-1 2.02-1 2.598 0l5.196 9a1.5 1.5 0 0 1-1.299 2.25H2.804a1.5 1.5 0 0 1-1.3-2.25l5.197-9ZM8 4a.75.75 0 0 1 .75.75v3a.75.75 0 1 1-1.5 0v-3A.75.75 0 0 1 8 4Zm0 8a1 1 0 1 0 0-2 1 1 0 0 0 0 2Z" fill="var(--warn)"/></svg>`;
}

// Provider marks (Claude, OpenAI) from the Paper file, filled with the text colour.
const LOGO = {
  claude: "m4.7144 15.9555 4.7174-2.6471.079-.2307-.079-.1275h-.2307l-.7893-.0486-2.6956-.0729-2.3375-.0971-2.2646-.1214-.5707-.1215-.5343-.7042.0546-.3522.4797-.3218.686.0608 1.5179.1032 2.2767.1578 1.6514.0972 2.4468.255h.3886l.0546-.1579-.1336-.0971-.1032-.0972L6.973 9.8356l-2.55-1.6879-1.3356-.9714-.7225-.4918-.3643-.4614-.1578-1.0078.6557-.7225.8803.0607.2246.0607.8925.686 1.9064 1.4754 2.4893 1.8336.3643.3035.1457-.1032.0182-.0728-.164-.2733-1.3539-2.4467-1.445-2.4893-.6435-1.032-.17-.6194c-.0607-.255-.1032-.4674-.1032-.7285L6.287.1335 6.6997 0l.9957.1336.419.3642.6192 1.4147 1.0018 2.2282 1.5543 3.0296.4553.8985.2429.8318.091.255h.1579v-.1457l.1275-1.706.2368-2.0947.2307-2.6957.0789-.7589.3764-.9107.7468-.4918.5828.2793.4797.686-.0668.4433-.2853 1.8517-.5586 2.9021-.3643 1.9429h.2125l.2429-.2429.9835-1.3053 1.6514-2.0643.7286-.8196.85-.9046.5464-.4311h1.0321l.759 1.1293-.34 1.1657-1.0625 1.3478-.8804 1.1414-1.2628 1.7-.7893 1.36.0729.1093.1882-.0183 2.8535-.607 1.5421-.2794 1.8396-.3157.8318.3886.091.3946-.3278.8075-1.967.4857-2.3072.4614-3.4364.8136-.0425.0304.0486.0607 1.5482.1457.6618.0364h1.621l3.0175.2247.7892.522.4736.6376-.079.4857-1.2142.6193-1.6393-.3886-3.825-.9107-1.3113-.3279h-.1822v.1093l1.0929 1.0686 2.0035 1.8092 2.5075 2.3314.1275.5768-.3218.4554-.34-.0486-2.2039-1.6575-.85-.7468-1.9246-1.621h-.1275v.17l.4432.6496 2.3436 3.5214.1214 1.0807-.17.3521-.6071.2125-.6679-.1214-1.3721-1.9246L14.38 17.959l-1.1414-1.9428-.1397.079-.674 7.2552-.3156.3703-.7286.2793-.6071-.4614-.3218-.7468.3218-1.4753.3886-1.9246.3157-1.53.2853-1.9004.17-.6314-.0121-.0425-.1397.0182-1.4328 1.9672-2.1796 2.9446-1.7243 1.8456-.4128.164-.7164-.3704.0667-.6618.4008-.5889 2.386-3.0357 1.4389-1.882.929-1.0868-.0062-.1579h-.0546l-6.3385 4.1164-1.1293.1457-.4857-.4554.0608-.7467.2307-.2429 1.9064-1.3114Z",
  codex: "M22.2819 9.8211a5.9847 5.9847 0 0 0-.5157-4.9108 6.0462 6.0462 0 0 0-6.5098-2.9A6.0651 6.0651 0 0 0 4.9807 4.1818a5.9847 5.9847 0 0 0-3.9977 2.9 6.0462 6.0462 0 0 0 .7427 7.0966 5.98 5.98 0 0 0 .511 4.9107 6.051 6.051 0 0 0 6.5146 2.9001A5.9847 5.9847 0 0 0 13.2599 24a6.0557 6.0557 0 0 0 5.7718-4.2058 5.9894 5.9894 0 0 0 3.9977-2.9001 6.0557 6.0557 0 0 0-.7475-7.0729zm-9.022 12.6081a4.4755 4.4755 0 0 1-2.8764-1.0408l.1419-.0804 4.7783-2.7582a.7948.7948 0 0 0 .3927-.6813v-6.7369l2.02 1.1686a.071.071 0 0 1 .038.052v5.5826a4.504 4.504 0 0 1-4.4945 4.4944zm-9.6607-4.1254a4.4708 4.4708 0 0 1-.5346-3.0137l.142.0852 4.783 2.7582a.7712.7712 0 0 0 .7806 0l5.8428-3.3685v2.3324a.0804.0804 0 0 1-.0332.0615L9.74 19.9502a4.4992 4.4992 0 0 1-6.1408-1.6464zM2.3408 7.8956a4.485 4.485 0 0 1 2.3655-1.9728V11.6a.7664.7664 0 0 0 .3879.6765l5.8144 3.3543-2.0201 1.1685a.0757.0757 0 0 1-.071 0l-4.8303-2.7865A4.504 4.504 0 0 1 2.3408 7.872zm16.5963 3.8558L13.1038 8.364 15.1192 7.2a.0757.0757 0 0 1 .071 0l4.8303 2.7913a4.4944 4.4944 0 0 1-.6765 8.1042v-5.6772a.79.79 0 0 0-.407-.667zm2.0107-3.0231l-.142-.0852-4.7735-2.7818a.7759.7759 0 0 0-.7854 0L9.409 9.2297V6.8974a.0662.0662 0 0 1 .0284-.0615l4.8303-2.7866a4.4992 4.4992 0 0 1 6.6802 4.66zM8.3065 12.863l-2.02-1.1638a.0804.0804 0 0 1-.038-.0567V6.0742a4.4992 4.4992 0 0 1 7.3757-3.4537l-.142.0805L8.704 5.459a.7948.7948 0 0 0-.3927.6813zm1.0976-2.3654l2.602-1.4998 2.6069 1.4998v2.9994l-2.5974 1.4997-2.6067-1.4997Z",
};

export function logo(provider, cls = "") {
  const d = LOGO[provider];
  if (!d) return `<span class="logo ${cls}"></span>`;
  return `<svg class="logo ${cls}" viewBox="0 0 24 24" aria-label="${provider === "claude" ? "Claude" : "Codex"}"><path d="${d}" fill="currentColor"/></svg>`;
}

export const providerTitle = (p) => (p === "claude" ? "Claude" : p === "codex" ? "Codex" : p);

// ---------- data model ----------

const sig = (signals, name) => {
  if (!signals) return "";
  const want = name.toLowerCase();
  for (const k in signals) if (k.toLowerCase() === want) return String(signals[k]).trim();
  return "";
};
const resetTime = (raw, observedAt, afterSeconds) => {
  if (raw) {
    const n = Number(raw);
    if (Number.isFinite(n) && n > 0) return n * 1000;
    const t = Date.parse(raw);
    if (Number.isFinite(t)) return t;
  }
  if (afterSeconds && observedAt) return Date.parse(observedAt) + Number(afterSeconds) * 1000;
  return 0;
};
const rollForward = (t, windowMs) => {
  if (!t || !windowMs) return t;
  while (t <= Date.now()) t += windowMs;
  return t;
};

export function primeSet() {
  return new Set((S.data?.routing?.prime_after_reset || []).map((p) => String(p).toLowerCase()));
}

// A weekly reading whose reset has passed describes the old week. With priming
// on, the new week has not started yet; otherwise it runs on a fixed cycle.
function weekItem(provider, used, reset, windowMs) {
  if (reset && reset <= Date.now()) {
    return primeSet().has(provider) ? { used: 0, reset: 0, notStarted: true } : { used: 0, reset: rollForward(reset, windowMs) };
  }
  return { used, reset };
}

// Limits from the router's meters (per-model weeks, survive restarts), else
// from the credential's last quota headers.
export function limits(acct) {
  const meters = S.data?.router?.accounts?.[acct.id]?.meters;
  const out = { week: null, short: null, extra: [] };
  if (meters && meters.length) {
    for (const m of meters) {
      const reset = validTime(m.reset_at) ? Date.parse(m.reset_at) : 0;
      const item = { used: (Number(m.utilization) || 0) * 100, reset: reset > Date.now() ? reset : 0, burn: m.burn_per_hour > 0 ? m.burn_per_hour * 100 : 0, title: m.title };
      if (m.long && m.reset_since_reading && primeSet().has(acct.provider)) Object.assign(item, { used: 0, reset: 0, notStarted: true });
      if ((m.name === "7d" || m.name === "secondary") && !out.week) out.week = item;
      else if ((m.name === "5h" || m.name === "primary") && !out.short) out.short = item;
      else out.extra.push(item);
    }
    if (out.week || out.short) return out;
  }
  return signalLimits(acct, out);
}

// Limits from the credential's last quota headers. This is what the live
// soonest-reset router reads, so the queue order uses it.
export function signalLimits(acct, out = { week: null, short: null, extra: [] }) {
  const q = acct.quota || {};
  const s = q.signals || {};
  if (acct.provider === "claude") {
    const w = sig(s, "Anthropic-Ratelimit-Unified-7d-Utilization");
    const wr = resetTime(sig(s, "Anthropic-Ratelimit-Unified-7d-Reset"));
    if (w || wr) out.week = weekItem("claude", Number(w) * 100, wr, 7 * 864e5);
    const f = sig(s, "Anthropic-Ratelimit-Unified-5h-Utilization");
    const fr = resetTime(sig(s, "Anthropic-Ratelimit-Unified-5h-Reset"));
    if (f || fr) out.short = { used: Number(f) * 100, reset: fr > Date.now() ? fr : 0 };
  } else if (acct.provider === "codex") {
    for (const p of ["X-Codex-Primary-", "X-Codex-Secondary-"]) {
      const minutes = Number(sig(s, p + "Window-Minutes"));
      if (!minutes) continue;
      const reset = resetTime(sig(s, p + "Reset-At"), q.observed_at, sig(s, p + "Reset-After-Seconds"));
      const used = Number(sig(s, p + "Used-Percent"));
      if (minutes >= 6 * 24 * 60) out.week = weekItem("codex", used, reset, minutes * 6e4);
      else out.short = { used, reset: reset > Date.now() ? reset : 0 };
    }
  }
  return out;
}

export const left = (item) => (item ? Math.max(0, Math.min(100, 100 - (Number.isFinite(item.used) ? item.used : 0))) : null);

export function accounts() {
  const list = (S.data?.accounts || []).map((a) => {
    const provider = String(a.provider || a.type || "").toLowerCase();
    const priority = Number.isFinite(Number(a.priority)) ? Number(a.priority) : 0;
    const mode = a.mode || (a.disabled ? "off" : priority < 0 ? "reserve" : "rotation");
    return { ...a, provider, priority, mode, email: a.email || a.label || a.name || a.id };
  }).filter((a) => a.provider === "claude" || a.provider === "codex");
  list.sort((x, y) => (x.provider === y.provider ? 0 : x.provider === "claude" ? -1 : 1));
  return list;
}

export function accountById(id) {
  return accounts().find((a) => a.id === id);
}

export function names() {
  const n = {};
  for (const a of accounts()) n[a.id] = a.email;
  return n;
}

// What an account can do right now.
//   kind: ready | off | usedup | limited | blocked | error
export function status(acct) {
  if (acct.disabled || acct.mode === "off") return { kind: "off", text: "Off" };
  const r = S.data?.router?.accounts?.[acct.id];
  if (r?.refused) return { kind: "error", text: r.refused + ", skipped for new sessions", detail: r.refused };
  const retry = validTime(acct.next_retry_after) ? Date.parse(acct.next_retry_after) : 0;
  const lim = limits(acct);
  if (retry && retry > Date.now()) {
    const weekGone = (lim.week && left(lim.week) <= 0) || retry - Date.now() > 6 * 3600e3;
    return weekGone ? { kind: "usedup", text: "Used up", until: retry } : { kind: "limited", text: "Resting until " + clock(retry), until: retry };
  }
  const msg = String(acct.status_message || "");
  if (String(acct.status || "").toLowerCase() === "error") {
    if (/401|403|unauthor|invalid_grant|refresh|sign|login|expired|revoked/i.test(msg)) return { kind: "blocked", text: "Sign-in blocked", detail: msg };
    return { kind: "error", text: msg ? "Error" : "Error", detail: msg };
  }
  if (acct.unavailable) return { kind: "limited", text: "Unavailable" };
  if (lim.week && left(lim.week) <= 0) return { kind: "usedup", text: "Used up" };
  return { kind: "ready", text: "Ready" };
}

// Mirror the live soonest-reset selector: highest priority tier among ready
// accounts, unobserved accounts first, then the earliest weekly reset. It
// reads the quota headers, not the router meters, so this does too.
export function queue(provider) {
  const list = accounts().filter((a) => a.provider === provider);
  const ready = list.filter((a) => status(a).kind === "ready");
  const order = ready.slice().sort((a, b) => {
    if (a.priority !== b.priority) return b.priority - a.priority;
    const ra = signalLimits(a).week?.reset || 0, rb = signalLimits(b).week?.reset || 0;
    if (!ra !== !rb) return ra ? 1 : -1;
    if (ra !== rb) return ra - rb;
    return String(a.id).localeCompare(String(b.id));
  });
  const rest = list.filter((a) => !ready.includes(a));
  return { order, rest, next: order.find((a) => a.priority >= 0) || order[0] || null };
}

export function plan(acct) {
  const p = acct.plan || {};
  const ends = p.ends_on || "";
  const renews = p.renews_on || "";
  return { type: p.type || "", renews, ends, source: p.source || "" };
}

// Usage totals for a set of accounts over one of the summary windows.
export function sumUsage(ids, window) {
  const out = { requests: 0, failed: 0, input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 };
  const acc = S.data?.summary?.accounts || {};
  for (const id of ids) {
    const u = acc[id]?.[window];
    if (!u) continue;
    for (const k in out) out[k] += Number(u[k]) || 0;
  }
  return out;
}
export const tokens = (u) => (u.input_tokens || 0) + (u.output_tokens || 0) + (u.cache_read_tokens || 0) + (u.cache_write_tokens || 0);
export function cacheReuse(u) {
  const read = u.cache_read_tokens || 0;
  const total = read + (u.cache_write_tokens || 0) + (u.input_tokens || 0);
  return total ? (read / total) * 100 : null;
}

// Hourly buckets for the last `n` hours, per account id, oldest first.
export function hourly(ids, n = 24) {
  const acc = S.data?.summary?.accounts || {};
  const per = {};
  let starts = [];
  for (const id of ids) {
    const h = (acc[id]?.hourly || []).slice(-n);
    per[id] = h;
    if (h.length > starts.length) starts = h.map((b) => b.start || b.hour || b.time || "");
  }
  return { per, starts, n: Math.max(starts.length, n) };
}

// range: the window the caller shows. Data loaded for another window reads as none.
export function perf(scope, range = "24h") {
  if (S.dataRange !== range) return null;
  return S.data?.summary?.performance?.scopes?.[scope] || null;
}

// Adds each account's numeric counters from src into dst.
function addByAuth(dst, src) {
  for (const [id, v] of Object.entries(src || {})) {
    if (!v) continue;
    const d = dst[id] || (dst[id] = {});
    for (const [k, n] of Object.entries(v)) if (typeof n === "number") d[k] = (d[k] || 0) + n;
  }
  return dst;
}

// Sessions with agent threads folded into their parent.
export function sessions() {
  const raw = S.data?.summary?.sessions || [];
  const byId = new Map();
  const parentOf = (s) => s.parent_id || (String(s.id).includes(":agent:") ? String(s.id).split(":agent:")[0] : "");
  for (const s of raw) if (!parentOf(s)) byId.set(s.id, { ...s, threads: [], auth_ids: [...(s.auth_ids || [])] });
  for (const s of raw) {
    const pid = parentOf(s);
    if (!pid) continue;
    let p = byId.get(pid);
    if (!p) {
      p = { id: pid, provider: s.provider, auth_ids: [], first_seen: s.first_seen, last_seen: s.last_seen, requests: 0, failed: 0, input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, threads: [], title: "", machine: s.machine || "" };
      byId.set(pid, p);
    }
    p.threads.push(s);
  }
  const out = [...byId.values()].map((p) => {
    const t = { ...p, own: { requests: p.requests, failed: p.failed }, by_auth: addByAuth({}, p.by_auth) };
    for (const c of p.threads) {
      for (const k of ["requests", "failed", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens"]) t[k] = (t[k] || 0) + (c[k] || 0);
      for (const id of c.auth_ids || []) if (!t.auth_ids.includes(id)) t.auth_ids.push(id);
      addByAuth(t.by_auth, c.by_auth);
      if (c.last_seen > t.last_seen) {
        t.last_seen = c.last_seen;
        if (c.serving_auth_id) t.serving_auth_id = c.serving_auth_id;
      }
      if (!t.first_seen || c.first_seen < t.first_seen) t.first_seen = c.first_seen;
      if (!t.machine && c.machine) t.machine = c.machine;
    }
    return t;
  });
  out.sort((a, b) => String(b.last_seen).localeCompare(String(a.last_seen)));
  return out;
}

export const sessionTitle = (s) => s.title || String(s.id).replace(/^[a-z]+:/, "").slice(0, 8);
export const sessionHref = (s) => "#/sessions/" + encodeURIComponent(s.id);

// ---------- components ----------

export function email(addr) {
  const s = String(addr || "");
  const at = s.indexOf("@");
  if (at < 0) return `<span class="email clamp"><b>${esc(s)}</b></span>`;
  return `<span class="email clamp"><b>${esc(s.slice(0, at))}</b><span>${esc(s.slice(at))}</span></span>`;
}

export const pill = (text, cls = "") => `<span class="pill ${cls}">${esc(text)}</span>`;
export const warnState = (text, cls = "") => `<span class="state ${cls}">${warnIcon(14)}<span class="clamp">${esc(text)}</span></span>`;

// Status shown next to an account name: Next pill, or a warning for problems.
export function acctBadge(acct, nextId) {
  const st = status(acct);
  if (st.kind === "off") return `<span class="muted ui">Off</span>`;
  if (st.kind === "blocked" || st.kind === "error") return warnState(st.text);
  if (acct.id === nextId) return pill("Next");
  return "";
}

export function meterCell(pctLeft) {
  if (pctLeft == null) return "";
  const p = Math.max(0, Math.min(100, pctLeft));
  return `<span class="metercell"><span class="meter"><i style="width:${p}%"></i></span><span class="${p <= 0 ? "muted" : ""}">${Math.round(p)}%</span></span>`;
}

export function tabs(items, active, attr = "data-tab") {
  return `<div class="tabs" role="tablist">${items.map((t) => `<button class="tab ${t.id === active ? "on" : ""}" ${attr}="${esc(t.id)}" role="tab">${esc(t.label)}${t.n != null ? ` <span class="n ${t.warn ? "warn" : ""}">${esc(t.n)}</span>` : ""}</button>`).join("")}</div>`;
}

export function seg(items, active, attr, cls = "") {
  return `<div class="seg ${cls}">${items.map((t) => `<button class="${t.id === active ? "on" : ""}" ${attr}="${esc(t.id)}">${esc(t.label)}</button>`).join("")}</div>`;
}

// cols: [{label, w (px) or grow, r (right aligned), cls}]; rows: [{cells: [html], href, cls, attrs}]
export function table(cols, rows, opts = {}) {
  const cell = (c, html, i) => {
    const style = c.w ? `style="width:${c.w}px;flex:0 0 ${c.w}px"` : "";
    return `<div class="c ${c.w ? "" : "g"} ${c.r ? "r" : ""} ${c.cls || ""}" ${style}>${html ?? ""}</div>`;
  };
  const head = opts.noHead ? "" : `<div class="tr head">${cols.map((c, i) => cell(c, esc(c.label || ""), i)).join("")}</div>`;
  const body = rows.map((r) => {
    if (r.group) return `<div class="tr group ${r.cls || ""}">${r.html}</div>`;
    const tag = r.href ? "a" : "div";
    const href = r.href ? ` href="${esc(r.href)}"` : "";
    return `<${tag} class="tr ${r.cls || ""}"${href} ${r.attrs || ""}>${cols.map((c, i) => cell(c, r.cells[i], i)).join("")}</${tag}>`;
  }).join("");
  const empty = !rows.length && opts.empty ? `<div class="empty">${esc(opts.empty)}</div>` : "";
  return `<div class="tblwrap"><div class="tbl">${head}${body}</div></div>${empty}`;
}

export function figure(label, value, unit = "", opts = {}) {
  const tag = opts.metric ? "button" : "div";
  return `<${tag} class="fig ${opts.on ? "on" : ""} ${opts.metric ? "" : "static"}" ${opts.metric ? `data-metric="${esc(opts.metric)}"` : ""}><span class="l">${esc(label)}</span><span class="v">${value}${unit ? `<span class="u">${esc(unit)}</span>` : ""}</span>${opts.sub ? `<span class="muted">${opts.sub}</span>` : ""}</${tag}>`;
}

// Stacked bar chart. series: [{key, color, label}], cols: [{values: {key: n}, tip: html}], labels: [{i, text}]
export function barChart({ id, series, cols, height = 140, yfmt = fmt, labels = [], dense = false }) {
  const totals = cols.map((c) => series.reduce((s, x) => s + (Number(c.values[x.key]) || 0), 0));
  const max = niceMax(Math.max(0, ...totals));
  const n = cols.length;
  const colsHtml = cols.map((c, i) => {
    let segs = "";
    for (let k = series.length - 1; k >= 0; k--) {
      const v = Number(c.values[series[k].key]) || 0;
      if (!v) continue;
      const h = Math.max(1, Math.round((v / max) * height));
      segs += `<i style="height:${h}px;background:${series[k].color}"></i>`;
    }
    return `<div class="col" data-i="${i}">${segs}</div>`;
  }).join("");
  const xl = labels.map((l) => {
    const pos = n > 1 ? (l.i + 0.5) / n : 0.5;
    const cls = l.i === 0 ? "first" : l.i === n - 1 ? "last" : "";
    const left = l.i === 0 ? "0" : l.i === n - 1 ? "100%" : (pos * 100).toFixed(2) + "%";
    return `<span class="${cls}" style="left:${left}">${esc(l.text)}</span>`;
  }).join("");
  return `<div class="chart" data-chart="${esc(id)}">
    <div class="yax" style="height:${height}px"><span>${esc(yfmt(max))}</span><span>${esc(yfmt(max / 2))}</span><span>0</span></div>
    <div class="plotwrap">
      <div class="plot ${dense ? "dense" : ""}" style="height:${height}px">
        <div class="grid" style="top:0"></div><div class="grid" style="top:${height / 2}px"></div>
        ${colsHtml}
      </div>
      <div class="xax">${xl}</div>
    </div>
  </div>`;
}

export function niceMax(v) {
  if (v <= 0) return 1;
  const exp = Math.pow(10, Math.floor(Math.log10(v)));
  for (const m of [1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10]) if (m * exp >= v) return m * exp;
  return 10 * exp;
}

// Hover tooltips for a chart rendered by barChart; tipFor(i) returns the html.
export function bindChart(root, id, tipFor) {
  const chart = root.querySelector(`[data-chart="${id}"]`);
  if (!chart) return;
  const plot = chart.querySelector(".plot");
  let tip = null, hot = null;
  const clear = () => {
    chart.classList.remove("hovering");
    if (hot) hot.classList.remove("hot");
    hot = null;
    if (tip) tip.remove();
    tip = null;
    S.hold = Math.max(0, S.hold - 1);
  };
  plot.addEventListener("mousemove", (e) => {
    const col = e.target.closest(".col");
    if (!col || col === hot) return;
    const html = tipFor(Number(col.dataset.i));
    if (!html) return;
    if (!hot) S.hold++;
    if (hot) hot.classList.remove("hot");
    hot = col;
    col.classList.add("hot");
    chart.classList.add("hovering");
    if (!tip) { tip = document.createElement("div"); tip.className = "tip"; plot.appendChild(tip); }
    tip.innerHTML = html;
    const pr = plot.getBoundingClientRect(), cr = col.getBoundingClientRect();
    const w = 280;
    let x = cr.left - pr.left - w - 12;
    if (x < 0) x = cr.right - pr.left + 12;
    if (x + w > pr.width) x = Math.max(0, pr.width - w);
    tip.style.left = x + "px";
    tip.style.top = "-14px";
  });
  plot.addEventListener("mouseleave", () => { if (hot) clear(); });
}

export function tipRows(rows) {
  return rows.map((r) => r === "hr" ? "<hr>" : `<div class="r"><span class="k">${r.color ? `<i style="background:${r.color}"></i>` : ""}<span class="clamp">${esc(r.k)}</span></span><span class="${r.cls || ""}">${esc(r.v)}</span></div>`).join("");
}

// ---------- popovers, toasts, writes ----------

let openMenu = null;
export function closeMenu() {
  if (!openMenu) return;
  openMenu.el.remove();
  document.removeEventListener("mousedown", openMenu.away, true);
  document.removeEventListener("keydown", openMenu.esc, true);
  openMenu = null;
  S.hold = Math.max(0, S.hold - 1);
}

// Opens a menu under `anchor`. items: [{a, b, on, danger, run}] or "sep" or {html, mount}.
export function menu(anchor, items, opts = {}) {
  closeMenu();
  const el = document.createElement("div");
  el.className = "menu";
  el.style.width = (opts.width || 248) + "px";
  el.innerHTML = items.map((it, i) => {
    if (it === "sep") return `<div class="sep"></div>`;
    if (it.html) return `<div data-i="${i}">${it.html}</div>`;
    return `<button class="mi ${it.on ? "on" : ""} ${it.danger ? "danger" : ""}" data-i="${i}"><span class="tx"><span class="a">${esc(it.a)}</span>${it.b ? `<span class="b">${esc(it.b)}</span>` : ""}</span>${it.on ? icon("check", 14, "var(--fg)") : ""}</button>`;
  }).join("");
  document.body.appendChild(el);
  const r = anchor.getBoundingClientRect();
  const w = el.offsetWidth, h = el.offsetHeight;
  let x = opts.alignLeft ? r.left : r.right - w;
  x = Math.max(8, Math.min(x, window.innerWidth - w - 8));
  let y = r.bottom + 4;
  if (y + h > window.innerHeight - 8) y = Math.max(8, r.top - h - 4);
  el.style.left = x + "px";
  el.style.top = y + "px";
  el.addEventListener("click", (e) => {
    const b = e.target.closest("button.mi");
    if (!b) return;
    const it = items[Number(b.dataset.i)];
    closeMenu();
    if (it?.run) it.run();
  });
  items.forEach((it, i) => { if (it.mount) it.mount(el.querySelector(`[data-i="${i}"]`)); });
  const away = (e) => { if (!el.contains(e.target) && !anchor.contains(e.target)) closeMenu(); };
  const escKey = (e) => { if (e.key === "Escape") closeMenu(); };
  document.addEventListener("mousedown", away, true);
  document.addEventListener("keydown", escKey, true);
  openMenu = { el, away, esc: escKey };
  S.hold++;
  return el;
}

export function toast(msg, err = false) {
  const t = document.createElement("div");
  t.className = "toast" + (err ? " err" : "");
  t.textContent = msg;
  document.body.appendChild(t);
  setTimeout(() => t.remove(), err ? 6000 : 2500);
}

// Asks for the management key once per device; resolves "" when cancelled.
export function askKey() {
  return new Promise((resolve) => {
    const scrim = document.createElement("div");
    scrim.className = "scrim";
    const box = document.createElement("div");
    box.className = "menu";
    box.style.cssText = "width:340px;left:50%;top:30%;transform:translateX(-50%);padding:16px;gap:12px";
    box.innerHTML = `<div class="ui strong">Management key</div><div class="muted">Needed once on this device to change routing, account modes and sign-ins. Viewing needs no key.</div><input class="input" type="password" autocomplete="off" placeholder="Paste the key"><div class="row gap8 right" style="justify-content:flex-end"><button class="btn" data-x>Cancel</button><button class="btn primary" data-ok>Save</button></div>`;
    document.body.append(scrim, box);
    S.hold++;
    const input = box.querySelector("input");
    input.focus();
    const done = (v) => { scrim.remove(); box.remove(); S.hold = Math.max(0, S.hold - 1); resolve(v); };
    box.querySelector("[data-x]").onclick = () => done("");
    scrim.onclick = () => done("");
    box.querySelector("[data-ok]").onclick = () => { const v = input.value.trim(); if (v) setKey(v); done(v); };
    input.onkeydown = (e) => { if (e.key === "Enter") box.querySelector("[data-ok]").click(); if (e.key === "Escape") done(""); };
  });
}

export async function api(path, opts = {}) {
  if (!S.key && !(await askKey())) throw Object.assign(new Error("No key"), { cancelled: true });
  const res = await fetch("/v8/management" + path, { ...opts, headers: { "X-Management-Key": S.key, "Content-Type": "application/json", ...(opts.headers || {}) } });
  if (res.status === 401 || res.status === 403) {
    setKey("");
    throw Object.assign(new Error("The management key was rejected. Enter it again."), { auth: true });
  }
  let body = null;
  try { body = await res.json(); } catch (e) { /* empty body */ }
  if (!res.ok) throw new Error((body && (body.message || body.error)) || path + " returned " + res.status);
  return body;
}

export async function setMode(acct, want) {
  await api("/credentials/status", { method: "PATCH", body: JSON.stringify({ name: acct.name || acct.id, disabled: want === "off" }) });
  if (want !== "off") await api("/credentials/fields", { method: "PATCH", body: JSON.stringify({ name: acct.name || acct.id, priority: want === "reserve" ? -1 : 0 }) });
}

export const MODES = [
  { id: "rotation", a: "Rotation", b: "Takes new sessions" },
  { id: "reserve", a: "Reserve", b: "Only once the others run out" },
  { id: "off", a: "Off", b: "Never used, stays signed in" },
];
export const modeTitle = (m) => (MODES.find((x) => x.id === m) || MODES[0]).a;

// Runs a write, then refreshes. Errors become a toast.
export async function write(fn, done) {
  try {
    await fn();
    if (done) toast(done);
  } catch (e) {
    if (!e.cancelled) toast(e.message || String(e), true);
  }
  window.dispatchEvent(new Event("dash:refresh"));
}

// Mode and plan-date menus shared by Accounts and Account details.
export function modeMenu(anchor, acct) {
  menu(anchor, MODES.map((m) => ({ a: m.a, b: m.b, on: acct.mode === m.id, run: () => { if (m.id !== acct.mode) write(() => setMode(acct, m.id), acct.email + ": " + m.a); } })));
}

export function planMenu(anchor, acct, extra = []) {
  const p = plan(acct);
  const field = (key, label, value, hint) => ({
    html: `<div class="field"><label>${esc(label)}</label><div class="inline"><input class="input" type="date" value="${esc(value)}" data-k="${key}" style="flex:1"><button class="btn outline" data-save="${key}">Save</button></div>${hint ? `<span class="muted">${esc(hint)}</span>` : ""}</div>`,
    mount: (el) => {
      el.querySelector("[data-save]").onclick = () => {
        const v = el.querySelector("input").value;
        closeMenu();
        write(() => api("/credentials/fields", { method: "PATCH", body: JSON.stringify({ name: acct.name || acct.id, [key]: v || null }) }), v ? label + " set to " + planDay(v) : label + " cleared");
      };
    },
  });
  menu(anchor, [
    ...extra,
    ...(extra.length ? ["sep"] : []),
    field("plan_renews_on", "Plan renews on", p.source === "manual" ? p.renews : "", p.source === "token" && p.renews ? "From the sign-in token: " + planDay(p.renews) + ". Set a date to override it." : "The day the subscription bills again."),
    field("plan_ends_on", "Plan ends on", p.ends, "Set this when you have cancelled. Leave empty while it renews."),
  ], { width: 280 });
}

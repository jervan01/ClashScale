"use strict";
/* ================= 工具 ================= */
const $ = (s, r = document) => r.querySelector(s);
const $$ = (s, r = document) => [...r.querySelectorAll(s)];
const esc = s => String(s ?? "").replace(/[&<>"']/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

const ICON = {
  home: '<path d="M3 11l9-8 9 8"/><path d="M5 10v10h5v-6h4v6h5V10"/>',
  nodes: '<circle cx="12" cy="12" r="9"/><path d="M3 12h18"/><path d="M12 3c3.2 3.6 3.2 14.4 0 18"/><path d="M12 3c-3.2 3.6-3.2 14.4 0 18"/>',
  subs: '<path d="M12 3l9 5-9 5-9-5z"/><path d="M3 13l9 5 9-5"/>',
  rules: '<path d="M4 6h9"/><path d="M4 12h16"/><path d="M4 18h12"/><circle cx="18" cy="6" r="2"/>',
  peers: '<rect x="3" y="4" width="18" height="12" rx="2"/><path d="M8 20h8"/><path d="M12 16v4"/>',
  net: '<circle cx="12" cy="5" r="2.5"/><circle cx="5" cy="18" r="2.5"/><circle cx="19" cy="18" r="2.5"/><path d="M11 7.3L6 15.8"/><path d="M13 7.3l5 8.5"/><path d="M7.5 18h9"/>',
  logs: '<path d="M6 3h9l4 4v14H6z"/><path d="M14 3v5h5"/><path d="M9 13h7"/><path d="M9 17h5"/>',
  settings: '<path d="M4 7h9"/><path d="M17 7h3"/><path d="M4 17h3"/><path d="M11 17h9"/><circle cx="15" cy="7" r="2"/><circle cx="9" cy="17" r="2"/>',
  power: '<path d="M12 3v8"/><path d="M6.3 6.8a8 8 0 1 0 11.4 0"/>',
  refresh: '<path d="M20 11a8 8 0 0 0-14-4"/><path d="M4 4v4h4"/><path d="M4 13a8 8 0 0 0 14 4"/><path d="M20 20v-4h-4"/>',
  up: '<path d="M12 19V5"/><path d="M6 11l6-6 6 6"/>',
  down: '<path d="M12 5v14"/><path d="M6 13l6 6 6-6"/>',
  trash: '<path d="M4 7h16"/><path d="M9 7V4h6v3"/><path d="M6 7l1 13h10l1-13"/>',
  copy: '<rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V6a2 2 0 0 1 2-2h9"/>',
  send: '<path d="M21 3L3 10l7 3 3 7z"/><path d="M10 13l11-10"/>',
  pulse: '<path d="M3 12h4l2-6 4 12 2-6h6"/>',
  rdp: '<rect x="3" y="4" width="18" height="13" rx="2"/><path d="M9 21h6"/><path d="M12 17v4"/><path d="M8 10l3 2-3 2"/>',
  exit: '<path d="M14 4h5v16h-5"/><path d="M10 8l-4 4 4 4"/><path d="M6 12h10"/>'
};
const svg = (n, s = 18) => `<svg width="${s}" height="${s}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${ICON[n]}</svg>`;

const fmtBytes = n => {
  const u = ["B", "KB", "MB", "GB", "TB"]; let i = 0; n = Number(n) || 0;
  while (n >= 1024 && i < 4) { n /= 1024; i++; }
  return (i ? n.toFixed(n < 10 ? 2 : 1) : n.toFixed(0)) + " " + u[i];
};
const fmtSpeed = n => fmtBytes(n) + "/s";
const ago = ts => {
  if (!ts) return "从未更新";
  const s = Math.floor(Date.now() / 1000) - ts;
  if (s < 60) return "刚刚更新";
  if (s < 3600) return Math.floor(s / 60) + " 分钟前更新";
  if (s < 86400) return Math.floor(s / 3600) + " 小时前更新";
  return Math.floor(s / 86400) + " 天前更新";
};
const hostOf = u => { try { return new URL(u).host; } catch { return ""; } };
const sleep = ms => new Promise(r => setTimeout(r, ms));

let toastTimer;
function toast(msg, kind = "ok") {
  if (!msg) return;
  const t = $("#toast");
  t.textContent = msg;
  t.className = "show" + (kind === "err" ? " err" : "");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (t.className = ""), kind === "err" ? 5500 : 2600);
}

/* ================= 后端调用 =================
   三个绑定对象：App（外壳）、Clash（代理）、Tailnet（组网）。 */
const backend = s => window.go && window.go.main && window.go.main[s];
async function call(s, fn, ...args) {
  try { return await backend(s)[fn](...args); }
  catch (e) { toast(String((e && e.message) || e), "err"); return null; }
}
// 动作类接口：自动弹出 Result.msg
async function act(s, fn, ...args) {
  const r = await call(s, fn, ...args);
  if (r && typeof r === "object" && "ok" in r && r.msg) toast(r.msg, r.ok ? "ok" : "err");
  return r;
}
const CL = (fn, ...a) => call("Clash", fn, ...a);
const TS = (fn, ...a) => call("Tailnet", fn, ...a);
const AP = (fn, ...a) => call("App", fn, ...a);
const actC = (fn, ...a) => act("Clash", fn, ...a);
const actT = (fn, ...a) => act("Tailnet", fn, ...a);
const actA = (fn, ...a) => act("App", fn, ...a);

async function withBusy(el, label, fn) {
  const old = el.innerHTML;
  el.disabled = true; el.textContent = label;
  try { return await fn(); }
  finally { if (el.isConnected) { el.disabled = false; el.innerHTML = old; } }
}

async function copyText(text, okMsg) {
  const ok = () => toast(okMsg || "已复制");
  try { const r = await backend("App").CopyText(text); if (r && r.ok) return ok(); } catch {}
  try { await navigator.clipboard.writeText(text); return ok(); } catch {}
  toast("复制失败，请手动选中复制", "err");
}

/* ================= 全局状态 ================= */
const S = {
  page: "home",
  cs: null,      // 代理状态
  ts: null,      // 组网状态
  cfg: null,     // 代理设置
  tcfg: null,    // 组网配置
  prefs: null,   // 外壳偏好
  info: null,
  bins: null,
  logs: [], tsLog: "", logTab: "clash",
  up: 0, down: 0, hist: [], tot: { up: 0, down: 0 },
  cur: null, busy: {}, dl: null,
  groups: [], gsel: null, q: "", sort: false, delays: {}, pending: new Set(), testing: false, nodesMsg: "",
  term: "ps", peerQ: "", onlineOnly: false, pings: {},
  doctor: null, doctorBusy: false, netcheck: "", manage: false, editPid: null, showPf: false,
  sig: ""
};

const NAV = [
  { id: "home", label: "总览", icon: "home" },
  { label: "代理 · Mihomo" },
  { id: "nodes", label: "节点", icon: "nodes", cls: "px" },
  { id: "subs", label: "订阅", icon: "subs", cls: "px" },
  { id: "rules", label: "规则", icon: "rules", cls: "px" },
  { label: "组网 · Tailscale" },
  { id: "peers", label: "设备", icon: "peers", cls: "ms" },
  { id: "net", label: "网络", icon: "net", cls: "ms" },
  { label: "系统" },
  { id: "logs", label: "日志", icon: "logs" },
  { id: "settings", label: "设置", icon: "settings" }
];
const BUILTIN = new Set(["DIRECT", "REJECT", "REJECT-DROP", "PASS", "COMPATIBLE"]);

const general = (patch = {}) => ({
  mixedPort: S.cfg.mixedPort, controllerPort: S.cfg.controllerPort, allowLan: S.cfg.allowLan,
  mirror: S.cfg.mirror, useSubRules: S.cfg.useSubRules, autoStartCore: S.cfg.autoStartCore,
  autoSystemProxy: S.cfg.autoSystemProxy, autoUpdateHours: S.cfg.autoUpdateHours,
  ...patch
});
async function reloadCfg() { const c = await CL("GetSettings"); if (c) S.cfg = c; }
async function reloadTcfg() { const c = await TS("GetConfig"); if (c) S.tcfg = c; }
async function reloadClash() { const s = await CL("GetStatus"); if (s) applyClash(s); }
async function reloadTS() { const s = await TS("GetStatus"); if (s) applyTS(s); }

/* ================= 外壳 ================= */
function buildShell() {
  $("#nav").innerHTML = NAV.map(n => n.id
    ? `<button class="nav ${n.cls || ""}" data-act="nav" data-p="${n.id}">${svg(n.icon)}${n.label}</button>`
    : `<div class="nav-label">${n.label}</div>`).join("");
}

const tsOn = () => !!(S.ts && S.ts.daemonRunning && S.ts.backend === "Running");
const tsBusy = () => !!(S.ts && S.ts.connecting && S.ts.backend !== "Running");

function updateChrome() {
  $$(".nav").forEach(b => b.classList.toggle("on", b.dataset.p === S.page));
  const c = S.cs, t = S.ts;
  const cl = c ? `<div><span class="dot ${c.running ? "px" : ""}"></span>代理 ${c.running ? "运行中 · " + c.mixedPort : "未运行"}</div>` : "";
  let tl = "";
  if (t) {
    const txt = tsOn() ? "已连接" + (t.selfIp ? " · " + t.selfIp : "") : tsBusy() ? "连接中…" : t.missingBin ? "缺少组件" : t.daemonRunning ? "未连接" : "未运行";
    tl = `<div><span class="dot ${tsOn() ? "ms" : tsBusy() ? "warn" : ""}"></span>组网 ${esc(txt)}</div>`;
  }
  $("#side-foot").innerHTML = cl + tl + `<div style="margin-top:2px;opacity:.7">v${esc((S.info && S.info.version) || "")}</div>`;
}

const PAGES = {};
function navTo(p) { S.page = p; updateChrome(); PAGES[p](); $("main").scrollTop = 0; stopLogPoll(); if (p === "logs") startLogPoll(); }

// 重新渲染时保留输入框里已输入的内容与焦点（状态事件会触发重绘）
function renderPage() {
  const a = document.activeElement;
  const keep = { vals: {}, focus: a && a.id, s: a && a.selectionStart, e: a && a.selectionEnd };
  $$("#view input[id],#view select[id]").forEach(el => { if (el.type !== "checkbox") keep.vals[el.id] = el.value; });
  PAGES[S.page]();
  for (const [id, v] of Object.entries(keep.vals)) { const el = document.getElementById(id); if (el && el.value !== v) el.value = v; }
  if (keep.focus) {
    const el = document.getElementById(keep.focus);
    if (el) { el.focus(); try { el.setSelectionRange(keep.s, keep.e); } catch {} }
  }
}

/* ================= 下载进度 ================= */
function dlHTML() {
  const d = S.dl;
  if (!d) return "";
  const err = d.stage === "失败";
  return `<div class="dl-line ${err ? "err" : ""}"><span>${esc(d.name)} · ${esc(d.stage)}</span><span>${esc(err ? "" : d.msg || "")}${d.stage === "下载中" && d.percent ? " · " + d.percent.toFixed(0) + "%" : ""}</span></div>
    ${err ? `<div class="dl-line err">${esc(d.msg)}</div>` : `<div class="bar"><i style="width:${Math.min(100, d.percent || 0)}%"></i></div>`}`;
}
function updateDl() { $$("[data-dl]").forEach(el => (el.innerHTML = dlHTML())); }

/* ================= 状态应用 ================= */
function applyClash(s) {
  const prev = S.cs;
  S.cs = s;
  if (prev && prev.running && !s.running) { S.up = S.down = 0; S.hist = []; S.cur = null; S.groups = []; }
  updateChrome();
  const sig = [s.running, s.coreInstalled, s.geoReady, s.version, s.downloading, s.mode, s.systemProxy, s.activeProfile].join("|");
  if (sig !== S.sig) {
    S.sig = sig;
    if (!S.busy.clash && ["home", "nodes", "subs", "rules"].includes(S.page)) renderPage();
  }
}
function applyTS(s) {
  S.ts = s;
  updateChrome();
  if (!S.busy.ts && ["home", "peers", "net"].includes(S.page)) renderPage();
}

"use strict";
/* ================= 事件分发 ================= */
const H = {
  nav: el => navTo(el.dataset.p),
  copy: el => copyText(el.dataset.v),
  url: (el, e) => { e.preventDefault(); AP("OpenURL", el.dataset.u); },
  "term-tab": el => { S.term = el.dataset.v; PAGES.settings(); },
  "copy-cmd": el => copyText(termCmds()[el.dataset.k], "已复制，粘贴到终端按回车即可"),
  theme: el => { themeSet(el.dataset.v); PAGES.settings(); },
  "open-dir": () => AP("OpenDataDir"),
  "log-tab": el => { S.logTab = el.dataset.v; PAGES.logs(); startLogPoll(); },
  "clear-log": () => { S.logs = []; $("#logbox").innerHTML = '<div class="ll">已清空。</div>'; },
  "open-ts-log": () => TS("OpenLog"),

  /* 一键场景 / 体检 */
  async scene(el) {
    if (S.busy.scene) return;
    S.busy.scene = true; S.busy.clash = true; S.busy.ts = true; renderPage();
    await actA("SetScene", el.dataset.v);
    S.busy.scene = S.busy.clash = S.busy.ts = false;
    await Promise.all([reloadClash(), reloadTS()]); renderPage();
  },
  async doctor() {
    S.doctorBusy = true; renderPage();
    S.doctor = (await AP("Doctor")) || S.doctor;
    S.doctorBusy = false; renderPage();
  },
  async fix(el) {
    const r = await withBusy(el, "处理中…", () => actA("FixIssue", el.dataset.id));
    await Promise.all([reloadClash(), reloadTS()]);
    S.doctor = (await AP("Doctor")) || S.doctor; renderPage();
    return r;
  },

  /* 代理 */
  async toggle() {
    if (S.busy.toggle) return;
    S.busy.toggle = true; S.busy.clash = true; renderPage();
    const r = await actC("ToggleCore");
    S.busy.toggle = S.busy.clash = false;
    await reloadClash(); renderPage();
    if (r && r.ok && S.cs.running) refreshCur();
  },
  async "install-all"() { S.dl = { name: "准备中", stage: "下载中", percent: 0, msg: "" }; await reloadClash(); const r = await actC("DownloadAll"); await reloadClash(); if (r && r.ok) S.dl = null; renderPage(); },
  async "dl-core"() { await reloadClash(); const r = await actC("DownloadCore"); await reloadClash(); if (r && r.ok) S.dl = null; renderPage(); },
  async "dl-geo"() { await reloadClash(); const r = await actC("DownloadGeo"); await reloadClash(); if (r && r.ok) S.dl = null; renderPage(); },
  async mode(el) { S.cs.mode = el.dataset.v; renderPage(); await actC("SetMode", el.dataset.v); },
  group(el) { S.gsel = el.dataset.n; drawNodes(); },
  pick: el => pickNode(el.dataset.n),
  test: () => testAll(),
  sort() { S.sort = !S.sort; drawNodes(); },
  "add-prof": el => addProfile(el),
  async "import-file"() { const r = await actC("ImportProfileFile"); if (r && r.ok) { await reloadCfg(); await reloadClash(); renderPage(); } },
  async "use-prof"(el) { const r = await actC("ActivateProfile", el.dataset.id); if (r && r.ok) { await reloadCfg(); await reloadClash(); S.cur = null; PAGES.subs(); } },
  async "upd-prof"(el) { const r = await withBusy(el, "更新中…", () => actC("UpdateProfile", el.dataset.id)); if (r && r.ok) { await reloadCfg(); PAGES.subs(); } },
  async "del-prof"(el) {
    if (!confirm("确定删除这个订阅吗？")) return;
    const r = await actC("DeleteProfile", el.dataset.id);
    if (r && r.ok) { await reloadCfg(); await reloadClash(); PAGES.subs(); }
  },
  async "rules-src"(el) {
    const useSub = el.dataset.v === "sub";
    if (useSub === S.cfg.useSubRules) return;
    const r = await actC("SaveGeneral", general({ useSubRules: useSub }));
    if (r && r.ok) { S.cfg.useSubRules = useSub; PAGES.subs(); }
  },
  "rule-add": () => addRule(),
  "rule-del": el => { const n = [...S.cfg.rules]; n.splice(+el.dataset.i, 1); saveRules(n); },
  "rule-up": el => { const i = +el.dataset.i, n = [...S.cfg.rules]; [n[i - 1], n[i]] = [n[i], n[i - 1]]; saveRules(n); },
  "rule-down": el => { const i = +el.dataset.i, n = [...S.cfg.rules]; [n[i + 1], n[i]] = [n[i], n[i + 1]]; saveRules(n); },
  "save-general": el => saveGeneral(el),

  /* 组网 */
  async "ts-power"() {
    if (S.busy.ts) return;
    const on = S.ts && S.ts.daemonRunning && (S.ts.backend === "Running" || S.ts.connecting);
    S.busy.ts = true; renderPage();
    await actT(on ? "Disconnect" : "Connect");
    S.busy.ts = false; await reloadTS(); renderPage();
  },
  async "ts-profile"(el) {
    if (S.busy.ts || (S.tcfg && S.tcfg.active === el.dataset.id)) return;
    S.busy.ts = true; renderPage();
    const r = await actT("SwitchProfile", el.dataset.id);
    if (r && r.ok) toast("已切换服务器配置");
    S.busy.ts = false; await reloadTcfg(); await reloadTS(); renderPage();
  },
  "ts-login": () => S.ts && S.ts.authUrl && AP("OpenURL", S.ts.authUrl),
  async "ts-key"() {
    const k = ($("#key-input").value || "").trim();
    if (!k) return toast("请先填写 Auth Key", "err");
    S.busy.ts = true; renderPage();
    const r = await actT("ConnectWithKey", k);
    S.busy.ts = false; await reloadTS(); renderPage();
    if (r && r.ok) { const e = $("#key-input"); if (e) e.value = ""; }
  },
  async "ts-daemon"() {
    S.busy.ts = true; renderPage();
    await actT(S.ts && S.ts.daemonRunning ? "StopDaemon" : "StartDaemon");
    S.busy.ts = false; await reloadTS(); renderPage();
  },
  async "install-ts"() {
    S.dl = { name: "Tailscale 组件", stage: "下载中", percent: 0, msg: "准备中…" };
    renderPage();
    const r = await actT("InstallBinaries");
    if (r && r.ok) S.dl = null;
    S.bins = null; await reloadTS(); renderPage();
  },
  async ping(el) { pingPeer(el); },
  async rdp(el) { actT("OpenRDP", el.dataset.ip); },
  async "send-files"(el) { toast("请选择要发送的文件…"); await actT("SendFiles", el.dataset.ip); },
  async "recv-files"() { await actT("ReceiveFiles"); },
  async "exit-set"(el) { await setTSPref({ exitNode: el.dataset.on === "1" ? el.dataset.ip : "" }); },
  async netcheck(el) {
    const r = await withBusy(el, "检测中（约 10 秒）…", () => TS("Netcheck"));
    S.netcheck = r ? r.text : "检测失败"; PAGES.net();
  },
  "pf-add": () => { S.showPf = true; S.editPid = null; PAGES.net(); },
  "pf-edit": el => { S.showPf = true; S.editPid = el.dataset.id; PAGES.net(); },
  "pf-cancel": () => { S.showPf = false; S.editPid = null; PAGES.net(); },
  async "pf-save"() {
    const name = $("#pf-name").value;
    const r = S.editPid ? await actT("RenameProfile", S.editPid, name) : await actT("AddProfile", name, $("#pf-server").value);
    if (r && r.ok) { S.showPf = false; S.editPid = null; await reloadTcfg(); PAGES.net(); }
  },
  async "pf-del"(el) {
    if (!confirm("删除这个服务器配置？它的登录状态也会被清除。")) return;
    const r = await actT("DeleteProfile", el.dataset.id);
    if (r && r.ok) { await reloadTcfg(); PAGES.net(); }
  },
  async "save-tsdir"() {
    const c = Object.assign({}, S.tcfg, { tailscaleDir: $("#f-tsdir").value });
    await actT("SaveConfig", c); await reloadTcfg(); S.bins = null; reloadTS(); PAGES.settings();
  }
};
document.addEventListener("click", e => {
  const el = e.target.closest("[data-act]");
  if (!el || el.tagName === "INPUT" || el.tagName === "SELECT" || el.disabled) return;
  const f = H[el.dataset.act]; if (f) f(el, e);
});
document.addEventListener("change", async e => {
  const el = e.target, a = el.dataset.act;
  if (a === "rule-toggle") {
    saveRules(S.cfg.rules.map((r, i) => i === +el.dataset.i ? { ...r, enabled: el.checked } : r));
  } else if (a === "sysproxy") {
    const r = await actC("SetSystemProxy", el.checked);
    if (!r || !r.ok) el.checked = !el.checked;
  } else if (a === "pref") {
    const next = { ...S.prefs, [el.dataset.k]: el.checked };
    const r = await actA("SavePrefs", next);
    if (r && r.ok) S.prefs = next; else el.checked = !el.checked;
  } else if (a === "tscfg") {
    const next = { ...S.tcfg, [el.dataset.k]: el.checked };
    const r = await actT("SaveConfig", next);
    if (r && r.ok) S.tcfg = next;
  } else if (a === "tspref") {
    setTSPref({ [el.dataset.k]: el.checked });
  } else if (a === "exit-sel") {
    setTSPref({ exitNode: el.value });
  } else if (a === "online-only") {
    S.onlineOnly = el.checked; drawPeers();
  }
});
document.addEventListener("input", e => {
  if (e.target.id === "q") { S.q = e.target.value; drawNodes(); }
  if (e.target.id === "peer-q") { S.peerQ = e.target.value; drawPeers(); }
});
document.addEventListener("keydown", e => {
  if (e.key !== "Enter") return;
  if (e.target.id === "sub-url" || e.target.id === "sub-name") $('[data-act="add-prof"]').click();
  if (e.target.id === "r-payload") addRule();
  if (e.target.id === "key-input") $('[data-act="ts-key"]').click();
});
window.addEventListener("resize", () => { if (S.page === "home") spark(); });

/* ================= 后端事件 ================= */
function bindRuntime() {
  const rt = window.runtime;
  if (!rt || !rt.EventsOn) return;
  rt.EventsOn("clash:state", applyClash);
  rt.EventsOn("ts:state", applyTS);
  rt.EventsOn("clash:ready", () => { if (S.page === "nodes") loadNodes(); if (S.page === "home") refreshCur(); });
  rt.EventsOn("clash:reloaded", async () => { S.cur = null; await reloadCfg(); if (S.page === "nodes") loadNodes(); if (S.page === "home") refreshCur(); });
  rt.EventsOn("clash:profiles", async () => { await reloadCfg(); if (S.page === "subs") PAGES.subs(); });
  rt.EventsOn("clash:traffic", t => {
    S.up = t.up; S.down = t.down; S.tot.up += t.up; S.tot.down += t.down;
    S.hist.push({ up: t.up, down: t.down }); if (S.hist.length > 60) S.hist.shift();
    if (S.page === "home") drawLive();
  });
  rt.EventsOn("clash:log", line => {
    S.logs.push(line); if (S.logs.length > 500) S.logs.shift();
    const box = S.page === "logs" && S.logTab === "clash" ? $("#logbox") : null;
    if (box) {
      const stick = box.scrollTop + box.clientHeight >= box.scrollHeight - 30;
      box.insertAdjacentHTML("beforeend", logLine(line));
      while (box.childElementCount > 300) box.firstElementChild.remove();
      if (stick) box.scrollTop = box.scrollHeight;
    }
  });
  rt.EventsOn("download:progress", d => { S.dl = d; updateDl(); });
}

/* ================= 启动 ================= */
async function init() {
  applyTheme();
  buildShell();
  if (!backend("App")) {
    $("#view").innerHTML = '<div class="card empty"><h2>需要在 Wails 应用中运行</h2><p>请使用 wails dev 或编译后的程序打开。</p></div>';
    return;
  }
  const [cs, cfg, logs, ts, tcfg, prefs, info] = await Promise.all([
    CL("GetStatus"), CL("GetSettings"), CL("GetLogs"), TS("GetStatus"), TS("GetConfig"), AP("GetPrefs"), AP("GetInfo")]);
  S.cs = cs; S.cfg = cfg; S.logs = logs || []; S.ts = ts; S.tcfg = tcfg; S.prefs = prefs; S.info = info;
  bindRuntime();
  S.sig = "";
  applyClash(cs);
  navTo("home");
  // 兜底：事件偶尔丢失时，低频校准一次
  setInterval(() => { reloadClash(); reloadTS(); }, 15000);
}
window.addEventListener("DOMContentLoaded", init);

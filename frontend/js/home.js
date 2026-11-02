"use strict";
/* ================= 总览 ================= */
function curText() {
  const s = S.cs;
  if (!s.coreInstalled) return "需要先安装内核";
  if (!s.running) return "已停止";
  if (s.mode === "direct") return "直连模式";
  if (!s.activeProfile) return "未使用订阅 · 流量直连";
  return S.cur ? `${S.cur.group} → ${S.cur.node}` : "正在读取节点…";
}
async function refreshCur() {
  if (!S.cs.running) return;
  const r = await CL("GetProxies");
  if (!r || !r.ok) return;
  const g = r.groups.find(x => x.selectable) || r.groups[0];
  S.cur = g ? { group: g.name, node: g.now } : null;
  const el = $("#cur-node"); if (el) { el.textContent = curText(); el.title = curText(); }
}

const TS_TEXT = {
  Running: ["已连接", "ok"], Starting: ["连接中…", "warn"], NeedsLogin: ["需要登录", "warn"],
  NeedsMachineAuth: ["等待管理员批准设备", "warn"], Stopped: ["已断开", "bad"], NoState: ["初始化中…", "warn"]
};
function tsStateText() {
  const s = S.ts;
  if (!s) return ["检测中…", "warn"];
  if (s.installing) return ["正在安装组件…", "warn"];
  if (s.missingBin) return ["缺少 Tailscale 组件", "bad"];
  if (!s.daemonRunning) return ["服务未运行", "bad"];
  if (s.connecting && s.backend !== "Running") return ["连接中…", "warn"];
  return TS_TEXT[s.backend] || [s.backend, "warn"];
}
const sceneState = () => (S.cs && S.cs.running ? 1 : 0) + (tsOn() ? 2 : 0); // 0 无 1 仅代理 2 仅组网 3 全开

function heroText() {
  const online = S.ts ? S.ts.peers.filter(p => p.online).length : 0;
  return [
    ["小狗在睡觉 zZ", "代理和组网都还没开，点下面任意一个按钮就醒来。"],
    ["汪！代理已就位", "国内外流量按规则自动分流。"],
    ["汪！组网已连通", `${S.ts && S.ts.tailnet ? S.ts.tailnet + " · " : ""}${online} 台设备在线。`],
    ["代理 + 组网，全开！", `分流与内网互通同时在线，100.x 设备自动直连，不会被代理吞掉。`]
  ][sceneState()];
}

function proxyCard() {
  const s = S.cs, run = s.running, need = !s.coreInstalled;
  const modes = [["rule", "规则"], ["global", "全局"], ["direct", "直连"]];
  const prof = (S.cfg.profiles || []).find(p => p.id === s.activeProfile);
  return `<section class="card mod px">
    <div class="mod-head">
      <div class="ti"><h2>代理</h2><small id="cur-node" title="${esc(curText())}">${esc(curText())}</small></div>
      <button class="power ${run ? "on" : ""} ${S.busy.toggle ? "busy" : ""}" data-act="toggle" ${need || S.busy.toggle ? "disabled" : ""} aria-label="${run ? "停止代理" : "启动代理"}">${svg("power", 26)}</button>
    </div>
    ${need ? `<div class="setup"><div><h3>先装好内核和规则库</h3><p class="hint">自动下载 Mihomo 内核与 GEO 规则库，网络不通时自动换加速镜像。</p></div>
        <button class="btn primary" data-act="install-all" ${s.downloading ? "disabled" : ""}>${s.downloading ? "安装中…" : "一键安装"}</button><div class="dl" data-dl></div></div>`
      : !s.geoReady ? `<div class="setup"><div><h3>规则库还没下载</h3><p class="hint">没有它，「国内直连」类规则无法生效。</p></div>
        <button class="btn primary" data-act="dl-geo" ${s.downloading ? "disabled" : ""}>下载</button><div class="dl" data-dl></div></div>` : ""}
    <div class="kv-line"><span>模式</span><div class="seg">${modes.map(([v, l]) => `<button class="${s.mode === v ? "on" : ""}" data-act="mode" data-v="${v}">${l}</button>`).join("")}</div></div>
    <label class="kv-line"><span>系统代理</span><span class="switch"><input type="checkbox" data-act="sysproxy" ${s.systemProxy ? "checked" : ""}><i></i></span></label>
    <div>
      <div class="speeds"><div><span>下载</span><strong id="sp-down">0 B/s</strong></div><div><span>上传</span><strong id="sp-up">0 B/s</strong></div></div>
      <canvas id="spark"></canvas><div class="totals" id="totals"></div>
    </div>
    <div class="foot-link"><span>端口 ${s.mixedPort} · ${prof ? esc(prof.name) : "未启用订阅"}</span><button class="btn ghost sm" data-act="nav" data-p="nodes">选择节点 →</button></div>
  </section>`;
}

function meshCard() {
  const s = S.ts;
  if (!s) return `<section class="card mod ms"><div class="mod-head"><div class="ti"><h2>组网</h2><small>检测中…</small></div></div></section>`;
  const on = tsOn(), [txt, cls] = tsStateText();
  const profs = (S.tcfg && S.tcfg.profiles) || [];
  const active = S.tcfg && S.tcfg.active;
  const msg = s.error && !s.missingBin ? s.error : s.lastError;
  const waitAuth = s.authUrl && !on;
  const online = s.peers.filter(p => p.online).length;
  const sub = s.version ? "Tailscale " + s.version.split("-")[0] + (s.tailnet ? " · " + s.tailnet : "") : txt;
  return `<section class="card mod ms">
    <div class="mod-head">
      <div class="ti"><h2>组网 <span class="pill ${cls === "ok" ? "ms" : cls === "bad" ? "bad" : "warn"}">${esc(txt)}</span></h2><small>${esc(sub)}</small></div>
      <button class="power ${on ? "on" : ""} ${S.busy.ts ? "busy" : ""}" data-act="ts-power" ${S.busy.ts || s.missingBin ? "disabled" : ""} aria-label="连接或断开组网">${svg("power", 26)}</button>
    </div>
    ${s.missingBin ? `<div class="setup"><div><h3>还没有 Tailscale 组件</h3><p class="hint">从官方安装包里提取 tailscale / tailscaled / wintun，不会安装系统服务。</p></div>
      <button class="btn primary" data-act="install-ts" ${s.installing ? "disabled" : ""}>${s.installing ? "安装中…" : "一键下载"}</button><div class="dl" data-dl></div></div>` : ""}
    <div class="profrow">${profs.map(p => `<button class="ppill ${p.id === active ? "on" : ""}" data-act="ts-profile" data-id="${esc(p.id)}"><b>${esc(p.name)}</b><small>${esc(hostOf(p.server) || "官方服务器")}</small></button>`).join("")}</div>
    ${msg ? `<div class="note err sel">${esc(msg)}</div>` : waitAuth ? `<div class="note warn sel">等待授权：请在浏览器完成登录 <button class="btn sm ms primary" data-act="ts-login">去浏览器登录</button></div>` : ""}
    ${on && s.selfIp ? `<div class="chips"><span class="hint">${esc(s.selfName)}</span>
      <button class="chip" data-act="copy" data-v="${esc(s.selfIp)}" title="点击复制 IP">${esc(s.selfIp)}</button>
      ${s.selfDns ? `<button class="chip" data-act="copy" data-v="${esc(s.selfDns)}" title="点击复制 MagicDNS 域名">${esc(s.selfDns)}</button>` : ""}</div>` : ""}
    ${!on && !s.missingBin ? `<details class="keybox"><summary>使用 Auth Key 登录（备选）</summary>
      <div class="row"><div class="grow"><input type="password" id="key-input" placeholder="tskey-auth-… 或 Headscale 预授权 Key" autocomplete="off" spellcheck="false"></div>
      <button class="btn primary ms" data-act="ts-key">登录</button></div></details>` : ""}
    <div class="kv-line"><span>服务进程：<b>${s.daemonRunning ? (s.ownsDaemon ? "运行中" : "运行中（外部）") : "已停止"}</b></span>
      ${s.missingBin || (s.daemonRunning && !s.ownsDaemon) ? "" : `<button class="btn ghost sm" data-act="ts-daemon">${s.daemonRunning ? "停止服务" : "启动服务"}</button>`}</div>
    <div class="foot-link"><span>${on ? online + " 台设备在线 / 共 " + s.peers.length + " 台" : "连接后可查看设备"}</span><button class="btn ghost sm" data-act="nav" data-p="peers">查看设备 →</button></div>
  </section>`;
}

function doctorCard() {
  const d = S.doctor;
  const ic = { ok: "✓", warn: "!", bad: "✕", info: "i" };
  return `<section class="card">
    <div class="page-head"><div><h2>一键体检</h2><p class="hint" style="margin:0">检查两个模块各自是否就绪，以及它们同时使用时最容易踩的坑。</p></div>
      <button class="btn primary" data-act="doctor" ${S.doctorBusy ? "disabled" : ""}>${S.doctorBusy ? "体检中…" : d ? "重新体检" : "开始体检"}</button></div>
    ${d ? `<div class="checks">${d.map(c => `<div class="chk ${c.level}"><span class="ic">${ic[c.level] || "i"}</span><span class="grp">${esc(c.group)}</span>
      <div><b>${esc(c.title)}</b><div class="d">${esc(c.detail)}</div></div>
      ${c.fix ? `<button class="btn sm primary" data-act="fix" data-id="${esc(c.fix)}">${esc(c.fixLabel || "修复")}</button>` : "<span></span>"}</div>`).join("")}</div>` : ""}
  </section>`;
}

PAGES.home = function () {
  const [h, p] = heroText();
  const st = ["off", "proxy", "mesh", "both"][sceneState()];
  const cur = sceneState();
  const scenes = [["off", "全部关闭", 0], ["proxy", "仅代理", 1], ["mesh", "仅组网", 2], ["all", "全部开启", 3]];
  $("#view").innerHTML = `
    <div class="page-head"><h1>总览</h1></div>
    <section class="hero2">
      <div class="mascot">${dogSVG(st, 130)}</div>
      <div class="hero-text"><h2>${esc(h)}</h2><p>${esc(p)}</p>
        <div class="seg">${scenes.map(([v, l, n]) => `<button class="${cur === n ? "on" : ""}" data-act="scene" data-v="${v}" ${S.busy.scene ? "disabled" : ""}>${l}</button>`).join("")}</div></div>
    </section>
    <div class="duo">${proxyCard()}${meshCard()}</div>
    ${doctorCard()}`;
  updateDl(); drawLive(); refreshCur();
};

function drawLive() {
  const d = $("#sp-down"); if (!d) return;
  d.textContent = fmtSpeed(S.down);
  $("#sp-up").textContent = fmtSpeed(S.up);
  $("#totals").textContent = `本次累计：下载 ${fmtBytes(S.tot.down)} · 上传 ${fmtBytes(S.tot.up)}`;
  spark();
}
function spark() {
  const c = $("#spark"); if (!c) return;
  const dpr = window.devicePixelRatio || 1, w = c.clientWidth, h = c.clientHeight;
  if (!w) return;
  if (c.width !== Math.round(w * dpr)) { c.width = Math.round(w * dpr); c.height = Math.round(h * dpr); }
  const g = c.getContext("2d");
  g.setTransform(dpr, 0, 0, dpr, 0, 0);
  g.clearRect(0, 0, w, h);
  const css = getComputedStyle(document.documentElement);
  const accent = css.getPropertyValue("--px").trim(), mute = css.getPropertyValue("--mute").trim(), line = css.getPropertyValue("--line").trim();
  const N = 60, max = Math.max(2048, ...S.hist.map(p => Math.max(p.up, p.down))) * 1.15;
  g.strokeStyle = line; g.lineWidth = 1; g.beginPath(); g.moveTo(0, h - .5); g.lineTo(w, h - .5); g.stroke();
  const path = key => {
    const n = S.hist.length;
    g.beginPath();
    S.hist.forEach((p, i) => {
      const x = w * (i + N - n) / (N - 1), y = h - 3 - (p[key] / max) * (h - 8);
      i ? g.lineTo(x, y) : g.moveTo(x, y);
    });
  };
  if (S.hist.length > 1) {
    const n = S.hist.length;
    path("down");
    g.strokeStyle = accent; g.lineWidth = 2; g.lineJoin = "round"; g.stroke();
    g.lineTo(w, h); g.lineTo(w * (N - n) / (N - 1), h); g.closePath();
    g.globalAlpha = .14; g.fillStyle = accent; g.fill(); g.globalAlpha = 1;
    path("up"); g.strokeStyle = mute; g.lineWidth = 1.3; g.setLineDash([4, 3]); g.stroke(); g.setLineDash([]);
  }
}

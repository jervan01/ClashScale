"use strict";
/* ================= 日志 ================= */
function logClass(line) {
  if (line.startsWith("[app]")) return /失败|错误/.test(line) ? "err" : "app";
  if (/level=(error|fatal)|\berror\b/i.test(line)) return "err";
  if (/level=warn/i.test(line)) return "warn";
  return "";
}
const logLine = l => `<div class="ll ${logClass(l)}">${esc(l)}</div>`;
let logTimer = null;
function stopLogPoll() { clearInterval(logTimer); logTimer = null; }
function startLogPoll() {
  stopLogPoll();
  logTimer = setInterval(async () => {
    if (S.page !== "logs" || S.logTab !== "ts") return;
    const t = await TS("ReadLog");
    if (t !== null && t !== S.tsLog) { S.tsLog = t; drawTsLog(); }
  }, 3000);
}
function drawTsLog() {
  const box = $("#tslog"); if (!box) return;
  const stick = box.scrollTop + box.clientHeight >= box.scrollHeight - 30;
  box.textContent = S.tsLog || "还没有 tailscaled 日志。";
  if (stick) box.scrollTop = box.scrollHeight;
}
PAGES.logs = function () {
  $("#view").innerHTML = `<div class="page-head"><h1>日志</h1>
      <div class="seg"><button class="${S.logTab === "clash" ? "on" : ""}" data-act="log-tab" data-v="clash">代理 · Mihomo</button>
      <button class="${S.logTab === "ts" ? "on" : ""}" data-act="log-tab" data-v="ts">组网 · tailscaled</button></div></div>
    <section class="card">
      ${S.logTab === "clash"
        ? `<div class="page-head" style="margin-bottom:8px"><span class="hint">内核输出与 ClashScale 事件</span><button class="btn ghost sm" data-act="clear-log">清空</button></div>
           <div class="logbox" id="logbox">${S.logs.slice(-300).map(logLine).join("") || '<div class="ll">还没有日志。</div>'}</div>`
        : `<div class="page-head" style="margin-bottom:8px"><span class="hint">tailscaled.log 末尾，每 3 秒刷新</span><button class="btn ghost sm" data-act="open-ts-log">用记事本打开</button></div>
           <pre class="out logbox" id="tslog" style="max-height:none"></pre>`}
    </section>`;
  const lb = $("#logbox"); if (lb) lb.scrollTop = lb.scrollHeight;
  if (S.logTab === "ts") { TS("ReadLog").then(t => { if (t !== null) { S.tsLog = t; drawTsLog(); const b = $("#tslog"); if (b) b.scrollTop = b.scrollHeight; } }); }
};

/* ================= 主题 ================= */
const themeGet = () => { try { return localStorage.getItem("cs-theme") || "auto"; } catch { return "auto"; } };
function themeSet(v) {
  try { localStorage.setItem("cs-theme", v); } catch {}
  applyTheme();
}
function applyTheme() {
  const v = themeGet();
  if (v === "auto") document.documentElement.removeAttribute("data-theme");
  else document.documentElement.dataset.theme = v;
}

/* ================= 终端代理命令 ================= */
const termCmds = () => {
  const u = `http://127.0.0.1:${S.cfg.mixedPort}`;
  return {
    "ps-on": `$env:HTTP_PROXY="${u}"; $env:HTTPS_PROXY="${u}"; $env:NO_PROXY="localhost,127.0.0.1,100.64.0.0/10,.ts.net"`,
    "ps-off": `Remove-Item Env:HTTP_PROXY,Env:HTTPS_PROXY,Env:NO_PROXY -ErrorAction SilentlyContinue`,
    "cmd-on": `set HTTP_PROXY=${u}\nset HTTPS_PROXY=${u}\nset NO_PROXY=localhost,127.0.0.1,100.64.0.0/10,.ts.net`,
    "cmd-off": `set HTTP_PROXY=\nset HTTPS_PROXY=\nset NO_PROXY=`,
    "git-on": `git config --global http.proxy ${u}\ngit config --global https.proxy ${u}`,
    "git-off": `git config --global --unset http.proxy\ngit config --global --unset https.proxy`
  };
};
function cmdBlock(label, key) {
  return `<div class="cmd"><div class="cmd-head"><b>${label}</b><button class="btn sm" data-act="copy-cmd" data-k="${key}">复制</button></div><pre>${esc(termCmds()[key])}</pre></div>`;
}
function termCard() {
  const tabs = [["ps", "PowerShell"], ["cmd", "CMD"], ["git", "Git"]];
  return `<section class="card"><h2>终端代理</h2>
    <p class="hint">PowerShell、CMD、Git、npm、go 等命令行工具不读取系统代理。复制命令粘贴到终端即可，端口取自已保存的代理端口（${S.cfg.mixedPort}）。已自动排除 tailnet 地址。</p>
    <div class="seg">${tabs.map(([v, l]) => `<button class="${S.term === v ? "on" : ""}" data-act="term-tab" data-v="${v}">${l}</button>`).join("")}</div>
    ${S.term === "git"
      ? cmdBlock("开启（写入全局配置，一直有效，用完记得取消）", "git-on") + cmdBlock("取消", "git-off")
      : cmdBlock("开启（仅当前窗口有效，关闭窗口即失效）", S.term + "-on") + cmdBlock("取消", S.term + "-off")}
  </section>`;
}

/* ================= 设置 ================= */
const swRow = (title, sub, attrs, cls = "") => `<div class="setrow"><div>${title}<small>${sub}</small></div><label class="switch ${cls}"><input type="checkbox" ${attrs}><i></i></label></div>`;

PAGES.settings = function () {
  const c = S.cfg, s = S.cs, p = S.prefs, t = S.tcfg, b = S.bins, th = themeGet();
  const hours = [[0, "关闭"], [6, "每 6 小时"], [12, "每 12 小时"], [24, "每天"], [72, "每 3 天"], [168, "每周"]];
  $("#view").innerHTML = `
    <div class="page-head"><h1>设置</h1></div>
    ${S.info && !S.info.admin ? `<div class="note err">当前没有管理员权限：组网（虚拟网卡）和开机自启将无法工作。请右键 → 以管理员身份运行。</div>` : ""}
    <section class="card"><h2>通用</h2><p class="hint">对整个应用生效。代理与组网的启停、自启互不影响，在下面各自设置。</p>
      ${swRow("开机自动启动", "登录 Windows 后在后台启动，只显示托盘图标（任务计划程序，免 UAC 弹窗）", `data-act="pref" data-k="autoLaunch" ${p.autoLaunch ? "checked" : ""}`)}
      ${swRow("关闭窗口时最小化到托盘", "关闭后点击 × 会直接退出程序", `data-act="pref" data-k="closeToTray" ${p.closeToTray ? "checked" : ""}`)}
      ${swRow("组网智能适配", "代理与组网共存的关键：让 100.64.0.0/10、*.ts.net 与自建服务器自动直连，并让 Mihomo 能解析 MagicDNS 域名。强烈建议保持开启", `data-act="pref" data-k="tailnetAssist" ${p.tailnetAssist ? "checked" : ""}`)}
      <div class="setrow"><div>外观<small>跟随系统或固定深浅色</small></div>
        <div class="seg">${[["auto", "跟随系统"], ["light", "浅色"], ["dark", "深色"]].map(([v, l]) => `<button class="${th === v ? "on" : ""}" data-act="theme" data-v="${v}">${l}</button>`).join("")}</div></div>
    </section>

    <section class="card" style="border-top:3px solid var(--px)"><h2>代理 · Mihomo</h2><p class="hint">修改端口后会自动重启内核。</p>
      <div class="form-grid" style="margin-bottom:6px">
        <label class="field"><span>代理端口（HTTP / SOCKS 混合）</span><input type="number" id="f-mixed" value="${c.mixedPort}" min="1024" max="65535"></label>
        <label class="field"><span>控制端口（仅本机）</span><input type="number" id="f-ctl" value="${c.controllerPort}" min="1024" max="65535"></label>
      </div>
      ${swRow("允许局域网连接", "开启后同一网络的设备可以使用本机代理", `id="f-lan" ${c.allowLan ? "checked" : ""}`)}
      ${swRow("启动本程序时自动运行内核", "需要已安装内核", `id="f-auto" ${c.autoStartCore ? "checked" : ""}`)}
      ${swRow("内核启动后自动开启系统代理", "仅在内核成功运行后才会生效", `id="f-sysproxy" ${c.autoSystemProxy ? "checked" : ""}`)}
      <div class="setrow"><div>订阅自动更新<small>后台定期重新下载订阅，失败会自动重试</small></div>
        <select id="f-upd" style="width:140px">${hours.map(([v, l]) => `<option value="${v}" ${c.autoUpdateHours === v ? "selected" : ""}>${l}</option>`).join("")}</select></div>
      <div class="setrow" style="display:block"><label class="field"><span>GitHub 加速前缀（下载内核和规则库时优先使用，留空则直连）</span>
        <input type="text" id="f-mirror" value="${esc(c.mirror)}" placeholder="https://ghfast.top/"></label></div>
      <div style="margin-top:8px"><button class="btn primary" data-act="save-general">保存代理设置</button></div>
    </section>

    <section class="card" style="border-top:3px solid var(--ms)"><h2>组网 · Tailscale</h2><p class="hint">与代理完全独立：可以只用其中一个。</p>
      ${t ? swRow("启动本程序时自动运行服务", "自动拉起 tailscaled（没装组件时静默跳过）", `data-act="tscfg" data-k="autoDaemon" ${t.autoDaemon ? "checked" : ""}`, "ms")
        + swRow("启动后自动连接", "等同于 tailscale up", `data-act="tscfg" data-k="autoConnect" ${t.autoConnect ? "checked" : ""}`, "ms")
        + swRow("退出时停止服务", "关闭 ClashScale 即断开组网；关闭此项则服务在后台继续运行", `data-act="tscfg" data-k="stopOnExit" ${t.stopOnExit ? "checked" : ""}`, "ms")
        + `<div class="setrow" style="display:block"><label class="field"><span>tailscale.exe 所在目录（可选，留空则自动查找）</span>
          <input type="text" id="f-tsdir" value="${esc(t.tailscaleDir)}" placeholder="例如 C:\\Program Files\\Tailscale"></label>
          <div style="margin-top:8px"><button class="btn primary ms" data-act="save-tsdir">保存</button></div></div>` : ""}
    </section>

    <section class="card"><h2>组件</h2>
      <dl class="kv" style="margin-top:12px">
        <dt>Mihomo 内核</dt><dd>${s.coreInstalled ? esc(s.version || "已安装") : "未安装"}</dd>
        <dt>GEO 规则库</dt><dd>${s.geoReady ? "已就绪" : "未下载"}</dd>
        <dt>tailscale</dt><dd>${b && b.cli ? esc(b.cli) : "未找到"}</dd>
        <dt>tailscaled</dt><dd>${b && b.daemon ? esc(b.daemon) + (b.official ? "（官方客户端目录）" : "") : "未找到"}</dd>
        <dt>wintun.dll</dt><dd>${b && b.wintun ? "已找到" : b && b.official ? "随官方客户端" : "未找到"}</dd>
        <dt>数据目录</dt><dd>${esc(S.info ? S.info.dataDir : s.dataDir)}</dd>
      </dl>
      <div class="btns">
        <button class="btn primary" data-act="dl-core" ${s.downloading ? "disabled" : ""}>${s.coreInstalled ? "更新内核" : "下载内核"}</button>
        <button class="btn" data-act="dl-geo" ${s.downloading ? "disabled" : ""}>更新规则库</button>
        <button class="btn ms primary" data-act="install-ts" ${S.ts && S.ts.installing ? "disabled" : ""}>${b && b.daemon ? "更新 Tailscale 组件" : "下载 Tailscale 组件"}</button>
        <button class="btn ghost" data-act="open-dir">打开数据目录</button>
      </div>
      <div class="dl" data-dl></div>
      <p class="hint" style="margin:12px 0 0">也可以手动放置：mihomo.exe → data\\clash\\；tailscale.exe / tailscaled.exe / wintun.dll → data\\tailscale\\bin\\。</p>
    </section>
    ${termCard()}
    <section class="card"><h2>关于 ClashScale</h2>
      <p class="hint" style="margin:0">版本 ${esc(S.info ? S.info.version : "")} · 代理内核 <a href="#" data-act="url" data-u="https://github.com/MetaCubeX/mihomo" style="color:var(--px)">Mihomo</a> · 组网 <a href="#" data-act="url" data-u="https://tailscale.com" style="color:var(--ms)">Tailscale</a> / <a href="#" data-act="url" data-u="https://github.com/juanfont/headscale" style="color:var(--ms)">Headscale</a>。
      从旧版迁移：把 GoProxyParty 的 data 内容复制到 data\\clash\\，把 Win_Tailscale 的 data 内容复制到 data\\tailscale\\ 即可沿用订阅与登录状态。</p>
    </section>`;
  updateDl();
  if (!S.bins) TS("GetBins").then(r => { if (r) { S.bins = r; if (S.page === "settings") renderPage(); } });
};

async function saveGeneral(btn) {
  const g = general({
    mixedPort: +$("#f-mixed").value, controllerPort: +$("#f-ctl").value,
    allowLan: $("#f-lan").checked, autoStartCore: $("#f-auto").checked, mirror: $("#f-mirror").value.trim(),
    autoSystemProxy: $("#f-sysproxy").checked, autoUpdateHours: +$("#f-upd").value
  });
  const r = await withBusy(btn, "保存中…", () => actC("SaveGeneral", g));
  if (r && r.ok) { await reloadCfg(); PAGES.settings(); }
}

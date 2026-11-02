"use strict";
/* ================= 设备 ================= */
function agoISO(iso) {
  if (!iso) return "";
  const t = Date.parse(iso);
  if (!t) return "";
  const s = Math.floor((Date.now() - t) / 1000);
  if (s < 90) return "刚刚在线";
  if (s < 3600) return Math.floor(s / 60) + " 分钟前在线";
  if (s < 86400) return Math.floor(s / 3600) + " 小时前在线";
  return Math.floor(s / 86400) + " 天前在线";
}

PAGES.peers = function () {
  const s = S.ts;
  if (!s || !tsOn()) {
    $("#view").innerHTML = `<div class="page-head"><h1>设备</h1></div>
      <div class="card empty"><h2>组网还没有连接</h2><p>连接后可以在这里看到同一网络里的所有设备，并 Ping、传文件、远程桌面。</p>
      <button class="btn primary ms" data-act="ts-power" ${s && s.missingBin ? "disabled" : ""}>${s && s.missingBin ? "请先在总览页下载组件" : "连接组网"}</button></div>`;
    return;
  }
  $("#view").innerHTML = `<div class="page-head"><h1>设备</h1>
      <div class="toolbar"><input type="text" id="peer-q" placeholder="搜索设备名 / IP" value="${esc(S.peerQ)}">
        <label class="toolbar" style="gap:6px"><span class="switch ms"><input type="checkbox" data-act="online-only" ${S.onlineOnly ? "checked" : ""}><i></i></span><span class="hint">仅在线</span></label>
        <button class="btn" data-act="recv-files" title="接收其他设备通过 Taildrop 发来的文件">${svg("down", 15)}接收文件</button></div></div>
    <section class="card selfcard">
      <span class="pd on"></span><div><div class="nm">${esc(s.selfName)} <span class="pill ms">本机</span></div>
        <div class="hint">${esc(s.tailnet || "tailnet")}${s.suffix ? " · " + esc(s.suffix) : ""}</div></div>
      <div class="chips" style="margin-left:auto">
        <button class="chip" data-act="copy" data-v="${esc(s.selfIp)}" title="复制 IP">${esc(s.selfIp)}</button>
        ${s.selfDns ? `<button class="chip" data-act="copy" data-v="${esc(s.selfDns)}" title="复制域名">${esc(s.selfDns)}</button>` : ""}</div>
    </section>
    <div class="list" id="peer-list"></div>`;
  drawPeers();
};

function drawPeers() {
  const box = $("#peer-list"); if (!box) return;
  const s = S.ts, q = S.peerQ.trim().toLowerCase();
  const list = s.peers.filter(p => (!S.onlineOnly || p.online) &&
    (!q || (p.name + " " + p.dns + " " + p.ip + " " + p.os + " " + p.owner).toLowerCase().includes(q)));
  box.innerHTML = list.map(p => {
    const pr = S.pings[p.ip];
    const path = !p.online ? (agoISO(p.lastSeen) || "离线")
      : p.direct ? '<span class="pill ms">直连</span>' : p.relay ? `<span class="pill warn">中继 ${esc(p.relay)}</span>` : '<span class="pill">未建立</span>';
    return `<div class="peer ${p.online ? "" : "off"}">
      <span class="pd ${p.online ? "on" : ""}"></span>
      <div class="nm"><span title="${esc(p.name)}">${esc(p.name)}</span>${p.exitNode ? '<span class="pill ms">出口节点中</span>' : p.exitNodeOption ? '<span class="pill">可作出口</span>' : ""}</div>
      <span class="hint">${esc(p.os)}</span>
      <button class="chip" data-act="copy" data-v="${esc(p.ip)}" title="点击复制 IP">${esc(p.ip)}</button>
      <div class="sub">${esc(p.dns)}${p.owner ? " · " + esc(p.owner) : ""}</div>
      <div class="path" style="grid-column:3/-1">${path}</div>
      <div class="acts">
        <button class="btn sm" data-act="ping" data-ip="${esc(p.ip)}" ${p.online ? "" : "disabled"}>${svg("pulse", 14)}Ping</button>
        <button class="btn sm" data-act="copy" data-v="${esc(p.dns || p.ip)}">${svg("copy", 14)}复制域名</button>
        ${p.os === "windows" ? `<button class="btn sm" data-act="rdp" data-ip="${esc(p.ip)}" ${p.online ? "" : "disabled"}>${svg("rdp", 14)}远程桌面</button>` : ""}
        <button class="btn sm" data-act="send-files" data-ip="${esc(p.ip)}" ${p.online ? "" : "disabled"}>${svg("send", 14)}发送文件</button>
        ${p.exitNodeOption ? `<button class="btn sm ${p.exitNode ? "primary ms" : ""}" data-act="exit-set" data-ip="${esc(p.ip)}" data-on="${p.exitNode ? 0 : 1}">${svg("exit", 14)}${p.exitNode ? "取消出口节点" : "用作出口节点"}</button>` : ""}
        ${pr ? `<span class="pingres ${pr.bad ? "bad" : ""}">${esc(pr.text)}</span>` : ""}
      </div></div>`;
  }).join("") || `<div class="card empty"><h2>${s.peers.length ? "没有匹配的设备" : "网络里还没有其他设备"}</h2><p>${s.peers.length ? "换个关键词试试。" : "在另一台设备上登录同一个账号即可看到它。"}</p></div>`;
}

async function pingPeer(el) {
  const ip = el.dataset.ip;
  S.pings[ip] = { text: "Ping 中…" }; drawPeers();
  const r = await TS("Ping", ip);
  S.pings[ip] = !r ? { text: "失败", bad: true } : r.ok
    ? { text: `${r.ms.toFixed(r.ms < 10 ? 1 : 0)} ms · ${r.via.startsWith("DERP") ? "经中继 " + r.via : "直连 " + r.via}` }
    : { text: r.msg || "无回应", bad: true };
  drawPeers();
}

/* ================= 网络 ================= */
const PREF_ROWS = [
  ["acceptRoutes", "接受子网路由", "使用其他设备共享出来的局域网网段（如公司内网）"],
  ["acceptDns", "使用 Tailscale DNS", "启用 MagicDNS，可以直接用设备名访问（如 nas 或 nas.xxx.ts.net）"],
  ["shieldsUp", "屏蔽入站连接", "开启后其他设备无法主动访问本机，本机仍可访问它们"],
  ["advertiseExit", "共享本机为出口节点", "让网络里的其他设备通过本机上网（需要在控制台批准）"]
];

PAGES.net = function () {
  const t = S.tcfg, s = S.ts, p = (t && t.prefs) || {};
  if (!t) { $("#view").innerHTML = ""; return; }
  const exits = s ? s.peers.filter(x => x.exitNodeOption) : [];
  const health = s && s.health && s.health.length ? `<div class="note warn sel">${s.health.map(esc).join("<br>")}</div>` : "";
  $("#view").innerHTML = `
    <div class="page-head"><h1>网络</h1></div>
    <section class="card">
      <h2>服务器配置</h2><p class="hint">官方 Tailscale 与自建 Headscale 一键切换。每个配置独立保存登录状态，切换无需重新登录。</p>
      <div class="list">${t.profiles.map(pf => `
        <div class="prof ${pf.id === t.active ? "active" : ""}" style="--accent:var(--ms)">
          <div class="prof-main"><div class="prof-name">${esc(pf.name)}${pf.id === t.active ? '<span class="pill ms">使用中</span>' : ""}</div>
            <div class="prof-meta">${esc(pf.server || "官方服务器")}${pf.suffix ? " · " + esc(pf.suffix) : ""}</div></div>
          <div class="prof-act">
            ${pf.id === t.active ? "" : `<button class="btn sm" data-act="ts-profile" data-id="${esc(pf.id)}">切换</button>`}
            <button class="btn ghost sm" data-act="pf-edit" data-id="${esc(pf.id)}">重命名</button>
            <button class="btn ghost danger sm" data-act="pf-del" data-id="${esc(pf.id)}">${svg("trash", 15)}删除</button>
          </div></div>`).join("")}</div>
      ${S.showPf ? `<div class="row" style="margin-top:12px">
          <label class="field" style="width:200px"><span>名称</span><input type="text" id="pf-name" maxlength="20" placeholder="例如 公司 Headscale" value="${esc(S.editPid ? (t.profiles.find(x => x.id === S.editPid) || {}).name || "" : "")}"></label>
          ${S.editPid ? "" : `<label class="field grow"><span>控制服务器（留空 = 官方）</span><input type="text" id="pf-server" placeholder="https://hs.example.com" spellcheck="false"></label>`}
          <button class="btn primary ms" data-act="pf-save">保存</button><button class="btn" data-act="pf-cancel">取消</button></div>`
        : `<div style="margin-top:12px"><button class="btn" data-act="pf-add">＋ 添加服务器配置</button></div>`}
    </section>
    <section class="card">
      <h2>连接偏好</h2><p class="hint">${tsOn() ? "修改后立即生效。" : "保存后，下次连接时生效。"}这些设置由 ClashScale 记住，重连、重启后不会丢。</p>
      ${PREF_ROWS.map(([k, l, d]) => `<div class="setrow"><div>${l}<small>${d}</small></div>
        <label class="switch ms"><input type="checkbox" data-act="tspref" data-k="${k}" ${p[k] ? "checked" : ""}><i></i></label></div>`).join("")}
    </section>
    <section class="card">
      <h2>出口节点</h2><p class="hint">让所有上网流量经由 tailnet 里的某台设备出去。与 Clash 同时使用时，Clash 的「直连」流量也会经过出口节点。</p>
      <div class="row">
        <label class="field grow"><span>使用哪台设备</span>
          <select id="exit-sel" data-act="exit-sel"><option value="">不使用出口节点</option>
          ${exits.map(x => `<option value="${esc(x.ip)}" ${p.exitNode === x.ip ? "selected" : ""}>${esc(x.name)} · ${esc(x.ip)}${x.online ? "" : "（离线）"}</option>`).join("")}</select></label>
      </div>
      <div class="setrow"><div>使用出口节点时仍可访问局域网<small>打印机、NAS 等本地设备不受影响</small></div>
        <label class="switch ms"><input type="checkbox" data-act="tspref" data-k="exitLan" ${p.exitLan ? "checked" : ""}><i></i></label></div>
      ${exits.length ? "" : `<p class="hint" style="margin:8px 0 0">目前没有设备提供出口节点。在目标设备上开启「共享为出口节点」并在控制台批准后，会出现在这里。</p>`}
    </section>
    <section class="card">
      <h2>网络诊断</h2><p class="hint">检测 UDP、IPv4/IPv6 与最近的 DERP 中继延迟，排查「为什么连不上 / 为什么走中继」。</p>
      ${health}
      <div class="btns"><button class="btn primary ms" data-act="netcheck">运行 netcheck</button></div>
      ${S.netcheck ? `<pre class="out" style="margin-top:12px">${esc(S.netcheck)}</pre>` : ""}
    </section>`;
};

async function setTSPref(patch) {
  const cur = Object.assign({}, S.tcfg.prefs, patch);
  const r = await actT("SetPrefs", cur);
  if (r && r.ok) S.tcfg.prefs = cur;
  else await reloadTcfg();
  if (S.page === "net" || S.page === "peers") renderPage();
}

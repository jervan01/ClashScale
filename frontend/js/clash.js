"use strict";
/* ================= 节点 ================= */
const curGroup = () => S.groups.find(g => g.name === S.gsel);

PAGES.nodes = function () {
  const s = S.cs;
  if (!s.running) {
    $("#view").innerHTML = `<div class="page-head"><h1>节点</h1></div>
      <div class="card empty"><h2>代理还没有启动</h2><p>启动后可以在这里查看订阅里的节点、测速并选择。</p>
      <button class="btn primary" data-act="toggle" ${s.coreInstalled ? "" : "disabled"}>${s.coreInstalled ? "启动代理" : "请先在总览页安装内核"}</button></div>`;
    return;
  }
  $("#view").innerHTML = `<div class="page-head"><h1>节点</h1>
      <div class="toolbar"><input type="text" id="q" placeholder="搜索节点" value="${esc(S.q)}" aria-label="搜索节点">
      <button class="btn" data-act="sort" id="btn-sort"></button>
      <button class="btn primary" data-act="test" id="btn-test"></button></div></div>
    <div class="tabs" id="tabs"></div><div id="gnote"></div><div class="grid" id="grid"></div>`;
  drawNodes();
  loadNodes();
};
async function loadNodes() {
  const r = await CL("GetProxies");
  if (!r) return;
  if (!r.ok) { S.nodesMsg = r.msg; S.groups = []; drawNodes(); return; }
  S.groups = r.groups;
  r.groups.forEach(g => g.nodes.forEach(n => { if (n.delay > 0 && S.delays[n.name] === undefined) S.delays[n.name] = n.delay; }));
  if (!curGroup()) { const g = S.groups.find(x => x.selectable) || S.groups[0]; S.gsel = g ? g.name : null; }
  drawNodes();
}
let gridQueued = false;
function scheduleGrid() { if (gridQueued) return; gridQueued = true; requestAnimationFrame(() => { gridQueued = false; drawNodes(); }); }

function delayCell(name) {
  if (S.pending.has(name)) return `<span class="delay none">测速中…</span>`;
  const d = S.delays[name];
  if (d === undefined) return `<span class="delay none">未测速</span>`;
  if (d < 0) return `<span class="delay bad">超时</span>`;
  return `<span class="delay ${d < 300 ? "fast" : d < 800 ? "mid" : "slow"}">${d} ms</span>`;
}
function drawNodes() {
  if (S.page !== "nodes" || !$("#grid")) return;
  const g = curGroup();
  $("#tabs").innerHTML = S.groups.map(x =>
    `<button class="tab ${x.name === S.gsel ? "on" : ""}" data-act="group" data-n="${esc(x.name)}" title="${esc(x.name)}">${esc(x.name)}</button>`).join("");
  const bs = $("#btn-sort"), bt = $("#btn-test");
  if (bs) bs.textContent = S.sort ? "按延迟排序 ✓" : "按延迟排序";
  if (bt) { bt.textContent = S.testing ? "测速中…" : "测速全部"; bt.disabled = S.testing || !g; }
  if (!g) {
    $("#grid").innerHTML = "";
    $("#gnote").innerHTML = `<div class="note">${esc(S.nodesMsg || "正在读取节点…")}</div>`;
    return;
  }
  $("#gnote").innerHTML = g.selectable ? "" :
    `<div class="note">「${esc(g.name)}」是自动分组（${esc(g.type)}），由内核按延迟自动选择，当前使用 ${esc(g.now || "—")}。</div>`;
  const q = S.q.trim().toLowerCase();
  let list = g.nodes.filter(n => !q || n.name.toLowerCase().includes(q));
  if (S.sort) {
    const rank = n => { const d = S.delays[n.name]; return d > 0 ? d : d < 0 ? 1e9 : 1e8; };
    list = [...list].sort((a, b) => rank(a) - rank(b));
  }
  $("#grid").innerHTML = list.map(n => `
    <button class="node ${n.name === g.now ? "sel" : ""} ${g.selectable ? "" : "lock"}" data-act="pick" data-n="${esc(n.name)}" title="${esc(n.name)}">
      <span class="n">${esc(n.name)}</span>
      <span class="m"><span>${esc(n.type)}</span>${BUILTIN.has(n.name) ? "" : delayCell(n.name)}</span>
    </button>`).join("") || `<div class="note" style="grid-column:1/-1">没有匹配的节点。</div>`;
}
async function pickNode(name) {
  const g = curGroup();
  if (!g) return;
  if (!g.selectable) return toast("这个分组由内核自动选择，不能手动指定");
  if (g.now === name) return;
  const r = await actC("SelectProxy", g.name, name);
  if (r && r.ok) { g.now = name; S.cur = null; drawNodes(); }
}
async function testAll() {
  const g = curGroup();
  if (!g || S.testing) return;
  const names = g.nodes.map(n => n.name).filter(n => !BUILTIN.has(n));
  if (!names.length) return toast("这个分组里没有可测速的节点");
  S.testing = true; names.forEach(n => S.pending.add(n)); drawNodes();
  let i = 0;
  const worker = async () => {
    while (i < names.length && S.page === "nodes") {
      const n = names[i++];
      const d = await CL("TestDelay", n);
      S.delays[n] = d === null ? -1 : d;
      S.pending.delete(n);
      scheduleGrid();
    }
  };
  await Promise.all(Array.from({ length: 8 }, worker));
  names.forEach(n => S.pending.delete(n));
  S.testing = false; scheduleGrid();
}

/* ================= 订阅 ================= */
function usageHTML(p) {
  if (!p.total) return "";
  const used = p.upload + p.download, pct = Math.min(100, used / p.total * 100);
  const exp = p.expire ? new Date(p.expire * 1000).toLocaleDateString() : "长期有效";
  return `<div class="usage"><div class="bar"><i style="width:${pct.toFixed(1)}%"></i></div>
    <div class="u">已用 ${fmtBytes(used)} / ${fmtBytes(p.total)}（剩余 ${fmtBytes(Math.max(0, p.total - used))}） · ${exp}</div></div>`;
}
PAGES.subs = function () {
  const c = S.cfg, active = c.activeProfile;
  const profCard = p => `
    <div class="prof ${p.id === active ? "active" : ""}">
      <div class="prof-main">
        <div class="prof-name">${esc(p.name)}${p.id === active ? '<span class="pill on">使用中</span>' : ""}${p.local ? '<span class="pill">本地</span>' : ""}</div>
        <div class="prof-meta">${p.local ? "本地文件" : esc(hostOf(p.url))} · ${p.nodeCount} 个节点 · ${p.ruleCount} 条规则 · ${ago(p.updatedAt)}</div>
        ${usageHTML(p)}
      </div>
      <div class="prof-act">
        ${p.id === active ? "" : `<button class="btn sm" data-act="use-prof" data-id="${p.id}">使用</button>`}
        ${p.local ? "" : `<button class="btn ghost sm" data-act="upd-prof" data-id="${p.id}" title="重新下载订阅">${svg("refresh", 15)}更新</button>`}
        <button class="btn ghost danger sm" data-act="del-prof" data-id="${p.id}" title="删除">${svg("trash", 15)}删除</button>
      </div>
    </div>`;
  const hasSub = !!active;
  $("#view").innerHTML = `
    <div class="page-head"><h1>订阅</h1><button class="btn" data-act="import-file">导入本地配置文件</button></div>
    <section class="card">
      <h2>添加订阅</h2><p class="hint">粘贴机场提供的 Clash / Mihomo 订阅链接，导入后自动启用。${c.autoUpdateHours ? "每 " + c.autoUpdateHours + " 小时自动更新一次。" : ""}</p>
      <div class="row">
        <label class="field" style="width:170px"><span>名称（可不填）</span><input type="text" id="sub-name" placeholder="例如：我的机场"></label>
        <label class="field grow"><span>订阅链接</span><input type="text" id="sub-url" placeholder="https://example.com/api/v1/client/subscribe?token=…"></label>
        <button class="btn primary" data-act="add-prof">添加订阅</button>
      </div>
    </section>
    <div class="list">
      <div class="prof ${active ? "" : "active"}">
        <div class="prof-main"><div class="prof-name">内置默认${active ? "" : '<span class="pill on">使用中</span>'}</div>
          <div class="prof-meta">不含节点，流量全部直连；适合只想使用自定义规则时</div></div>
        <div class="prof-act">${active ? '<button class="btn sm" data-act="use-prof" data-id="">使用</button>' : ""}</div>
      </div>
      ${c.profiles.map(profCard).join("")}
    </div>
    <section class="card">
      <h2>分流规则来源</h2><p class="hint">你在「规则」页添加的自定义规则始终排在订阅规则前面，这里决定它们后面接哪一套规则。</p>
      <div class="list">
        <button class="choice ${c.useSubRules ? "on" : ""}" data-act="rules-src" data-v="sub" ${hasSub ? "" : "disabled"}>
          <i class="radio"></i><div><b>使用订阅自带规则</b><span class="d">沿用机场的策略组和分流规则，推荐。</span></div></button>
        <button class="choice ${c.useSubRules ? "" : "on"}" data-act="rules-src" data-v="builtin" ${hasSub ? "" : "disabled"}>
          <i class="radio"></i><div><b>使用内置基础规则</b><span class="d">局域网和国内网站直连，其余走「节点选择」；忽略机场规则。</span></div></button>
      </div>
      ${hasSub ? "" : '<p class="hint" style="margin:10px 0 0">导入并启用订阅后才能选择。</p>'}
    </section>`;
};
async function addProfile(btn) {
  const url = $("#sub-url").value.trim(), name = $("#sub-name").value.trim();
  if (!url) return toast("请先粘贴订阅链接", "err");
  const r = await withBusy(btn, "正在导入…", () => actC("AddProfile", name, url));
  if (r && r.ok) { await reloadCfg(); await reloadClash(); PAGES.subs(); }
}

/* ================= 规则 ================= */
const TYPE_LABEL = { "DOMAIN-SUFFIX": "域名后缀", "DOMAIN-KEYWORD": "域名关键词", "DOMAIN": "完整域名", "IP-CIDR": "IP 段", "GEOIP": "国家/地区", "PROCESS-NAME": "进程名" };
const TARGET_LABEL = { DIRECT: "直连", PROXY: "代理", REJECT: "拦截" };
let managedRules = [];

PAGES.rules = function () {
  const rules = S.cfg.rules;
  const opt = (map, sel) => Object.entries(map).map(([v, l]) => `<option value="${v}" ${v === sel ? "selected" : ""}>${l}</option>`).join("");
  $("#view").innerHTML = `
    <div class="page-head"><h1>规则</h1></div>
    <section class="card">
      <h2>添加自定义规则</h2><p class="hint">从上到下依次匹配，命中即停止。这些规则始终优先于订阅规则。</p>
      <div class="row">
        <label class="field" style="width:150px"><span>类型</span><select id="r-type">${opt(TYPE_LABEL, "DOMAIN-SUFFIX")}</select></label>
        <label class="field grow"><span>内容</span><input type="text" id="r-payload" placeholder="例如：github.com、openai、192.168.0.0/16"></label>
        <label class="field" style="width:110px"><span>策略</span><select id="r-target">${opt(TARGET_LABEL, "PROXY")}</select></label>
        <button class="btn primary" data-act="rule-add">添加</button>
      </div>
    </section>
    ${managedRules.length ? `<section class="card"><h2>组网智能适配 · 自动规则</h2>
      <p class="hint">由 ClashScale 自动维护，永远排在最前面，保证 Tailscale 的 100.x 设备和 MagicDNS 域名不会被代理吞掉。可在「设置」里关闭。</p>
      <div class="list">${managedRules.map(r => `<div class="rule managed"><span class="pill ms">自动</span><span class="rp" title="${esc(r)}">${esc(r)}</span><span class="pill">直连</span></div>`).join("")}</div></section>` : ""}
    <div class="list" id="rule-list">
      ${rules.map((r, i) => `
        <div class="rule ${r.enabled ? "" : "off"}">
          <label class="switch"><input type="checkbox" data-act="rule-toggle" data-i="${i}" ${r.enabled ? "checked" : ""} aria-label="启用规则"><i></i></label>
          <span class="rt">${TYPE_LABEL[r.type] || r.type}</span>
          <span class="rp" title="${esc(r.payload)}">${esc(r.payload)}</span>
          <span class="pill ${r.target === "PROXY" ? "on" : r.target === "REJECT" ? "bad" : ""}">${TARGET_LABEL[r.target]}</span>
          <span class="acts">
            <button class="btn ghost sm" data-act="rule-up" data-i="${i}" ${i === 0 ? "disabled" : ""} title="上移" aria-label="上移">${svg("up", 15)}</button>
            <button class="btn ghost sm" data-act="rule-down" data-i="${i}" ${i === rules.length - 1 ? "disabled" : ""} title="下移" aria-label="下移">${svg("down", 15)}</button>
            <button class="btn ghost danger sm" data-act="rule-del" data-i="${i}" title="删除" aria-label="删除规则">${svg("trash", 15)}</button>
          </span>
        </div>`).join("") || `<div class="card empty"><h2>还没有自定义规则</h2><p>例如让 github.com 走代理、让公司内网域名直连。</p></div>`}
    </div>`;
  CL("ManagedRules").then(r => {
    const n = r || [];
    if (JSON.stringify(n) !== JSON.stringify(managedRules)) { managedRules = n; if (S.page === "rules") PAGES.rules(); }
  });
};
async function saveRules(next) {
  const r = await actC("SaveRules", next);
  if (r && r.ok) S.cfg.rules = next;
  PAGES.rules();
}
function addRule() {
  const payload = $("#r-payload").value.trim();
  if (!payload) return toast("请输入规则内容", "err");
  if (/[,\s]/.test(payload)) return toast("规则内容不能包含逗号或空格", "err");
  saveRules([{ id: "", type: $("#r-type").value, payload, target: $("#r-target").value, enabled: true }, ...S.cfg.rules]);
}

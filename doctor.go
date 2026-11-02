package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Check 是「一键体检」的单项结果。Level: ok / warn / bad / info
type Check struct {
	ID       string `json:"id"`
	Group    string `json:"group"` // 系统 / 代理 / 组网 / 协作
	Level    string `json:"level"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Fix      string `json:"fix"` // 非空表示可一键修复，值是 FixIssue 的参数
	FixLabel string `json:"fixLabel"`
}

// Doctor 检查两个模块各自的就绪情况，以及它们「放在一起」时最容易踩的坑。
func (a *App) Doctor() []Check {
	var out []Check
	add := func(c Check) { out = append(out, c) }

	// ---- 系统 ----
	if isAdmin() {
		add(Check{ID: "admin", Group: "系统", Level: "ok", Title: "管理员权限", Detail: "已获得，可以启动 Tailscale 虚拟网卡并创建开机自启任务"})
	} else {
		add(Check{ID: "admin", Group: "系统", Level: "bad", Title: "没有管理员权限", Detail: "组网需要管理员权限创建虚拟网卡。请右键 → 以管理员身份运行 ClashScale"})
	}

	// ---- 代理 ----
	c := a.clash
	cs := c.snapshot()
	switch {
	case !c.coreInstalled():
		add(Check{ID: "core", Group: "代理", Level: "bad", Title: "Mihomo 内核未安装", Detail: "没有内核就无法代理", Fix: "install-core", FixLabel: "一键安装"})
	default:
		add(Check{ID: "core", Group: "代理", Level: "ok", Title: "Mihomo 内核", Detail: strOr(c.GetStatus().Version, "已安装")})
	}
	if c.coreInstalled() {
		if c.geoReady() {
			add(Check{ID: "geo", Group: "代理", Level: "ok", Title: "GEO 规则库", Detail: "已就绪"})
		} else {
			add(Check{ID: "geo", Group: "代理", Level: "warn", Title: "GEO 规则库缺失", Detail: "「国内直连」类规则无法生效", Fix: "install-geo", FixLabel: "下载规则库"})
		}
	}
	if cs.ActiveProfile == "" {
		add(Check{ID: "sub", Group: "代理", Level: "info", Title: "没有启用订阅", Detail: "目前所有流量直连。到「订阅」页导入机场订阅后才会真正代理"})
	}
	if c.isRunning() {
		if systemProxyIsOurs(cs.MixedPort) || !cs.SystemProxy {
			add(Check{ID: "run", Group: "代理", Level: "ok", Title: "内核运行中", Detail: fmt.Sprintf("端口 %d，%s", cs.MixedPort, map[string]string{"rule": "规则模式", "global": "全局模式", "direct": "直连模式"}[cs.Mode])})
		} else {
			add(Check{ID: "sysproxy", Group: "代理", Level: "warn", Title: "系统代理被其他程序改动", Detail: "系统代理应指向本程序但现在不是，浏览器可能没有走代理", Fix: "reapply-proxy", FixLabel: "重新设置"})
		}
		if cs.Mode != "direct" && cs.ActiveProfile != "" {
			start := time.Now()
			resp, err := plainClient(cs.MixedPort, 6*time.Second).Get("http://www.gstatic.com/generate_204")
			if err == nil {
				resp.Body.Close()
			}
			if err == nil && resp.StatusCode < 400 {
				add(Check{ID: "net", Group: "代理", Level: "ok", Title: "代理连通性", Detail: "经代理访问外网正常，耗时 " + humanDuration(time.Since(start))})
			} else {
				add(Check{ID: "net", Group: "代理", Level: "bad", Title: "代理无法访问外网", Detail: "请到「节点」页测速并换一个节点，或更新订阅"})
			}
		}
	} else if systemProxyIsOurs(cs.MixedPort) {
		add(Check{ID: "stale", Group: "代理", Level: "bad", Title: "系统代理残留", Detail: "系统代理指向本程序，但内核没有运行，会导致上不了网", Fix: "clear-proxy", FixLabel: "立即清除"})
	}

	// ---- 组网 ----
	t := a.ts
	bins := t.GetBins()
	if bins.Cli == "" || bins.Daemon == "" {
		add(Check{ID: "tsbin", Group: "组网", Level: "bad", Title: "Tailscale 组件未找到", Detail: "缺少 tailscale.exe / tailscaled.exe", Fix: "install-ts", FixLabel: "一键下载"})
	} else {
		d := "已找到：" + filepath.Dir(bins.Daemon)
		if bins.Official {
			d += "（官方客户端的安装目录）"
		}
		add(Check{ID: "tsbin", Group: "组网", Level: "ok", Title: "Tailscale 组件", Detail: d})
		if bins.Wintun == "" && !bins.Official {
			add(Check{ID: "wintun", Group: "组网", Level: "warn", Title: "没有找到 wintun.dll", Detail: "它应与 tailscaled.exe 放在同一目录，缺失会导致服务启动后立即退出", Fix: "install-ts", FixLabel: "重新下载组件"})
		}
		ts := t.queryStatus()
		switch {
		case !ts.DaemonRunning:
			add(Check{ID: "tsd", Group: "组网", Level: "info", Title: "组网服务未运行", Detail: "需要时点「连接」会自动启动"})
		case ts.Backend == "Running":
			add(Check{ID: "tsd", Group: "组网", Level: "ok", Title: "已接入网络", Detail: fmt.Sprintf("%s · %s · %d 台设备在线", strOr(ts.Tailnet, "tailnet"), ts.SelfIP, onlineCount(ts.Peers))})
		case ts.Backend == "NeedsLogin":
			add(Check{ID: "tsd", Group: "组网", Level: "warn", Title: "需要登录", Detail: "到「总览」点击连接，在浏览器里完成授权"})
		default:
			add(Check{ID: "tsd", Group: "组网", Level: "warn", Title: "组网状态：" + ts.Backend, Detail: strOr(ts.LastError, "")})
		}
		if ts.DaemonRunning && !ts.OwnsDaemon {
			add(Check{ID: "ext", Group: "组网", Level: "info", Title: "使用的是外部 Tailscale 服务", Detail: "服务不是由本程序启动的（可能是官方客户端），本程序无法停止它，也无法切换服务器配置"})
		}
		for i, h := range ts.Health {
			add(Check{ID: fmt.Sprintf("health%d", i), Group: "组网", Level: "warn", Title: "Tailscale 提示", Detail: h})
		}
	}

	// ---- 协作 ----
	as := a.snapshotPrefs()
	if as.TailnetAssist {
		add(Check{ID: "assist", Group: "协作", Level: "ok", Title: "组网智能适配已开启", Detail: "100.64.0.0/10、*.ts.net" + suffixNote(t.knownSuffix()) + " 自动直连，MagicDNS 域名可正常解析"})
	} else {
		add(Check{ID: "assist", Group: "协作", Level: "warn", Title: "组网智能适配已关闭", Detail: "同时使用代理与组网时，访问 100.x 设备可能被代理吞掉。建议开启", Fix: "enable-assist", FixLabel: "开启"})
	}
	if c.isRunning() && t.lastStatus().Backend == "Running" && t.lastStatus().SelfIP != "" {
		ip := t.lastStatus().SelfIP
		_ = ip
		add(Check{ID: "both", Group: "协作", Level: "ok", Title: "代理与组网同时运行", Detail: "互不影响：国内外流量走代理，tailnet 内流量走虚拟网卡"})
	}
	return out
}

func onlineCount(p []PeerInfo) int {
	n := 0
	for _, x := range p {
		if x.Online {
			n++
		}
	}
	return n
}

func strOr(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

func suffixNote(s string) string {
	if s == "" {
		return ""
	}
	return "、*." + s
}

// FixIssue 执行体检里的一键修复。
func (a *App) FixIssue(id string) Result {
	switch id {
	case "install-core":
		return a.clash.DownloadAll()
	case "install-geo":
		return a.clash.DownloadGeo()
	case "install-ts":
		return a.ts.InstallBinaries()
	case "clear-proxy":
		if err := clearSystemProxy(); err != nil {
			return errRes(err)
		}
		a.clash.update(func(s *Settings) { s.SystemProxy = false })
		a.clash.emitState()
		return okRes("系统代理已清除")
	case "reapply-proxy":
		return errRes(a.clash.applySystemProxy(true))
	case "enable-assist":
		p := a.snapshotPrefs()
		p.TailnetAssist = true
		return a.SavePrefs(p)
	}
	return failRes("未知的修复项")
}

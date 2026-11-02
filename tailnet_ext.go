package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	wr "github.com/wailsapp/wails/v2/pkg/runtime"
)

// ---------- 偏好（对标官方客户端的开关） ----------

func (t *Tailnet) GetPrefs() TSPrefs {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cfg.Prefs
}

// SetPrefs 保存偏好；已连接时立刻用 `tailscale set` 生效，否则下次连接时生效。
func (t *Tailnet) SetPrefs(p TSPrefs) Result {
	p.ExitNode = strings.TrimSpace(p.ExitNode)
	if p.ExitNode != "" && net.ParseIP(p.ExitNode) == nil {
		return failRes("出口节点需要是设备的 IP")
	}
	if p.AdvertiseExit && p.ExitNode != "" {
		return failRes("不能同时「共享本机为出口节点」和「使用其他设备作为出口节点」")
	}
	st := t.queryStatus()
	applied := false
	if st.DaemonRunning && st.Backend == "Running" {
		args := []string{"set",
			fmt.Sprintf("--accept-routes=%t", p.AcceptRoutes),
			fmt.Sprintf("--accept-dns=%t", p.AcceptDNS),
			fmt.Sprintf("--shields-up=%t", p.ShieldsUp),
			fmt.Sprintf("--advertise-exit-node=%t", p.AdvertiseExit),
			"--exit-node=" + p.ExitNode,
		}
		if p.ExitNode != "" {
			args = append(args, fmt.Sprintf("--exit-node-allow-lan-access=%t", p.ExitLAN))
		}
		if err := t.runCLI(15*time.Second, args...); err != nil {
			return failRes(err.Error())
		}
		applied = true
	}
	t.mu.Lock()
	t.cfg.Prefs = p
	t.saveConfigLocked()
	t.mu.Unlock()
	t.kickPoll()
	if applied {
		return okRes("已生效")
	}
	return okRes("已保存，连接后生效")
}

// ---------- Ping ----------

type PingResult struct {
	OK  bool    `json:"ok"`
	Msg string  `json:"msg"`
	Ms  float64 `json:"ms"`
	Via string  `json:"via"` // 直连地址 或 DERP(区域)
}

var pingRe = regexp.MustCompile(`via (\S+) in ([\d.]+)(ms|s)`)

func (t *Tailnet) Ping(ip string) PingResult {
	if net.ParseIP(ip) == nil {
		return PingResult{Msg: "IP 不合法"}
	}
	out, err := t.runCLIOut(20*time.Second, "ping", "--c=2", "--timeout=3s", ip)
	ms := pingRe.FindAllStringSubmatch(out, -1)
	if len(ms) == 0 {
		if err == nil {
			err = errors.New("没有回应")
		}
		return PingResult{Msg: lastLine(out, err.Error())}
	}
	m := ms[len(ms)-1]
	v, _ := strconv.ParseFloat(m[2], 64)
	if m[3] == "s" {
		v *= 1000
	}
	return PingResult{OK: true, Ms: v, Via: m[1]}
}

func lastLine(s, fallback string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	lines := strings.Split(s, "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// ---------- 网络诊断 ----------

type TextResult struct {
	OK   bool   `json:"ok"`
	Text string `json:"text"`
}

func (t *Tailnet) Netcheck() TextResult {
	out, err := t.runCLIOut(40*time.Second, "netcheck")
	return TextResult{OK: err == nil, Text: out}
}

// ---------- 远程桌面 ----------

func (t *Tailnet) OpenRDP(ip string) Result {
	if net.ParseIP(ip) == nil {
		return failRes("IP 不合法")
	}
	return errRes(exec.Command("mstsc", "/v:"+ip).Start())
}

// ---------- Taildrop 文件互传 ----------

func (t *Tailnet) SendFiles(ip string) Result {
	if net.ParseIP(ip) == nil {
		return failRes("IP 不合法")
	}
	if t.host.ctx == nil {
		return failRes("窗口尚未就绪")
	}
	files, err := wr.OpenMultipleFilesDialog(t.host.ctx, wr.OpenDialogOptions{Title: "选择要发送的文件"})
	if err != nil {
		return failRes(err.Error())
	}
	if len(files) == 0 {
		return okRes("")
	}
	args := append([]string{"file", "cp"}, files...)
	args = append(args, ip+":")
	if err := t.runCLI(30*time.Minute, args...); err != nil {
		return failRes("发送失败: " + err.Error())
	}
	return okRes(fmt.Sprintf("已发送 %d 个文件", len(files)))
}

func (t *Tailnet) ReceiveFiles() Result {
	home, err := os.UserHomeDir()
	if err != nil {
		return errRes(err)
	}
	dir := filepath.Join(home, "Downloads", "ClashScale")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return errRes(err)
	}
	out, err := t.runCLIOut(5*time.Minute, "file", "get", "--conflict=rename", dir)
	if err != nil {
		return failRes("接收失败: " + err.Error())
	}
	_ = exec.Command("explorer", dir).Start()
	if out == "" {
		out = "已检查：文件保存在 " + dir
	}
	return okRes(out)
}

// =====================================================================
//  一键下载 Tailscale 组件（从官方 MSI 里提取 tailscale.exe / tailscaled.exe / wintun.dll）
// =====================================================================

func (t *Tailnet) emitProgress(name, stage string, percent float64, msg string) {
	t.emit("download:progress", map[string]interface{}{"name": name, "stage": stage, "percent": percent, "msg": msg})
}

type pkgsIndex struct {
	Version string
	MSIs    map[string]string
}

func (t *Tailnet) resolveMSI() (url, version string) {
	const base = "https://pkgs.tailscale.com/stable/"
	arch := "x64"
	suffix := "amd64"
	if runtime.GOARCH == "arm64" {
		arch, suffix = "arm64", "arm64"
	}
	for _, viaProxy := range t.proxyOrder() {
		b, err := httpGetLimited(plainClient(viaProxy, 15*time.Second), base+"?mode=json", 1<<20)
		if err != nil {
			continue
		}
		var idx pkgsIndex
		if json.Unmarshal(b, &idx) != nil {
			continue
		}
		if name := idx.MSIs[arch]; name != "" {
			return base + name, idx.Version
		}
		for _, name := range idx.MSIs {
			if strings.Contains(name, suffix) {
				return base + name, idx.Version
			}
		}
	}
	return base + "tailscale-setup-latest-" + suffix + ".msi", "latest"
}

// 返回依次尝试的本机代理端口：Clash 在运行时优先借道它（在国内这通常更快更稳），再直连。
func (t *Tailnet) proxyOrder() []int {
	if t.host.clash.isRunning() {
		return []int{t.host.clash.snapshot().MixedPort, 0}
	}
	return []int{0}
}

func (t *Tailnet) InstallBinaries() Result {
	t.mu.Lock()
	if t.installing {
		t.mu.Unlock()
		return failRes("已有下载任务在进行")
	}
	t.installing = true
	t.mu.Unlock()
	t.kickPoll()
	defer func() {
		t.mu.Lock()
		t.installing = false
		t.mu.Unlock()
		t.kickPoll()
	}()

	label := "Tailscale 组件"
	t.emitProgress(label, "下载中", 0, "查询最新版本…")
	url, ver := t.resolveMSI()

	msi := filepath.Join(t.dataDir, "tailscale-setup.msi")
	var dlErr error
	for _, port := range t.proxyOrder() {
		t.emitProgress(label, "下载中", 0, "连接中…")
		dlErr = downloadFile(plainClient(port, 0), label, url, msi, 1<<20, t.emitProgress)
		if dlErr == nil {
			break
		}
	}
	if dlErr != nil {
		t.emitProgress(label, "失败", 0, dlErr.Error())
		return failRes("下载失败: " + dlErr.Error())
	}
	defer os.Remove(msi)

	t.emitProgress(label, "解压中", 100, "")
	tmp := filepath.Join(t.dataDir, "msi-extract")
	_ = os.RemoveAll(tmp)
	defer os.RemoveAll(tmp)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	// 管理员安装模式只解包文件，不会真的安装、不会注册服务
	cmd := exec.CommandContext(ctx, "msiexec")
	hiddenRaw(cmd, fmt.Sprintf(`msiexec /a "%s" /qn TARGETDIR="%s"`, msi, tmp))
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		t.emitProgress(label, "失败", 0, msg)
		return failRes("解包安装包失败: " + msg)
	}

	want := map[string]bool{"tailscale.exe": true, "tailscaled.exe": true, "wintun.dll": true}
	found := map[string]string{}
	_ = filepath.WalkDir(tmp, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n := strings.ToLower(d.Name())
			if want[n] && found[n] == "" {
				found[n] = p
			}
		}
		return nil
	})
	if found["tailscale.exe"] == "" || found["tailscaled.exe"] == "" {
		t.emitProgress(label, "失败", 0, "安装包里没有找到 tailscale(d).exe")
		return failRes("安装包里没有找到 tailscale.exe / tailscaled.exe")
	}

	binDir := filepath.Join(t.dataDir, "bin")
	_ = os.MkdirAll(binDir, 0o755)
	// 正在运行的 tailscaled 无法被覆盖
	if t.queryStatus().DaemonRunning {
		_ = t.stopDaemon()
	}
	for n, p := range found {
		f, err := os.Open(p)
		if err != nil {
			return failRes("读取 " + n + " 失败: " + err.Error())
		}
		err = writeAtomic(filepath.Join(binDir, n), f, 0o755)
		f.Close()
		if err != nil {
			return failRes("写入 " + n + " 失败: " + err.Error())
		}
	}
	t.emitProgress(label, "完成", 100, ver)
	t.kickPoll()
	return okRes("Tailscale " + ver + " 组件已就绪")
}

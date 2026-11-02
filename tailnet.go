package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	wr "github.com/wailsapp/wails/v2/pkg/runtime"
)

// =====================================================================
//  组网模块（Tailscale / Headscale）。直接驱动 tailscale.exe / tailscaled.exe。
//  与代理模块完全独立：没有 Clash 也能单独使用，反之亦然。
// =====================================================================

// TSPrefs 对标官方客户端里的常用开关，由本程序保存，并在每次 `tailscale up` 时显式带上，
// 因此重连 / 重启 / 切换服务器后都不会丢设置。
type TSPrefs struct {
	AcceptRoutes  bool   `json:"acceptRoutes"`  // 接受其他设备共享的子网路由
	AcceptDNS     bool   `json:"acceptDns"`     // 使用 MagicDNS / 控制台下发的 DNS
	ShieldsUp     bool   `json:"shieldsUp"`     // 屏蔽所有入站连接
	AdvertiseExit bool   `json:"advertiseExit"` // 把本机作为出口节点共享给其他设备
	ExitNode      string `json:"exitNode"`      // 使用哪台设备作为出口节点（IP），空 = 不使用
	ExitLAN       bool   `json:"exitLan"`       // 使用出口节点时仍可访问本地局域网
}

// TSConfig 保存在 data/tailscale/config.json（字段与 Win_Tailscale 兼容，可直接沿用旧数据）。
type TSConfig struct {
	AutoDaemon   bool        `json:"autoDaemon"`            // 启动时自动运行 tailscaled
	AutoConnect  bool        `json:"autoConnect"`           // 启动后自动 tailscale up
	StopOnExit   bool        `json:"stopOnExit"`            // 退出时停止由本程序启动的 tailscaled
	LoginServer  string      `json:"loginServer,omitempty"` // 旧版字段，仅用于迁移
	Profiles     []TSProfile `json:"profiles"`
	Active       string      `json:"active"`
	TailscaleDir string      `json:"tailscaleDir"` // 可选：手动指定 tailscale(d).exe 所在目录
	Prefs        TSPrefs     `json:"prefs"`
}

// TSProfile 是一个服务器配置：官方或自建 Headscale。每个配置有独立的登录状态。
type TSProfile struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Server string `json:"server"`           // 留空 = 官方
	Suffix string `json:"suffix,omitempty"` // 最近一次见到的 MagicDNS 后缀（自动记录）
}

type PeerInfo struct {
	ID             string `json:"id"`
	DNS            string `json:"dns"`
	Name           string `json:"name"`
	IP             string `json:"ip"`
	OS             string `json:"os"`
	Online         bool   `json:"online"`
	ExitNode       bool   `json:"exitNode"`       // 正在被本机用作出口节点
	ExitNodeOption bool   `json:"exitNodeOption"` // 该设备提供出口节点能力
	Relay          string `json:"relay"`          // 中继区域（DERP），如 hkg
	Direct         bool   `json:"direct"`         // 是否 P2P 直连
	LastSeen       string `json:"lastSeen"`
	Owner          string `json:"owner"`
}

type TSStatus struct {
	DaemonRunning bool       `json:"daemonRunning"`
	OwnsDaemon    bool       `json:"ownsDaemon"`
	Backend       string     `json:"backend"`
	Connecting    bool       `json:"connecting"`
	AuthURL       string     `json:"authUrl"`
	SelfName      string     `json:"selfName"`
	SelfIP        string     `json:"selfIp"`
	SelfDNS       string     `json:"selfDns"`
	Tailnet       string     `json:"tailnet"`
	Suffix        string     `json:"suffix"`
	Version       string     `json:"version"`
	Health        []string   `json:"health"`
	Peers         []PeerInfo `json:"peers"`
	MissingBin    bool       `json:"missingBin"`
	Installing    bool       `json:"installing"`
	Error         string     `json:"error"`
	LastError     string     `json:"lastError"`
}

type tsPeer struct {
	ID             string
	HostName       string
	DNSName        string
	OS             string
	TailscaleIPs   []string
	Online         bool
	ExitNode       bool
	ExitNodeOption bool
	Relay          string
	CurAddr        string
	LastSeen       string
	UserID         int64
}

type tsStatus struct {
	Version        string
	BackendState   string
	AuthURL        string
	Self           *tsPeer
	MagicDNSSuffix string
	CurrentTailnet *struct {
		Name           string
		MagicDNSSuffix string
	}
	Health []string
	Peer   map[string]*tsPeer
	User   map[string]struct {
		LoginName   string
		DisplayName string
	}
}

type Tailnet struct {
	host    *App
	dataDir string
	exeDir  string

	mu         sync.Mutex
	cfg        TSConfig
	daemon     *exec.Cmd
	upCancel   context.CancelFunc
	authURL    string
	lastErr    string
	installing bool

	last     TSStatus
	lastJSON string
	kick     chan struct{}
}

var urlRe = regexp.MustCompile(`https?://[^\s"']+`)

func newTailnet(host *App, dir string) *Tailnet {
	t := &Tailnet{host: host, dataDir: dir, exeDir: host.exeDir, kick: make(chan struct{}, 1)}
	for _, d := range []string{dir, filepath.Join(dir, "logs"), filepath.Join(dir, "state"), filepath.Join(dir, "bin")} {
		_ = os.MkdirAll(d, 0o755)
	}
	_ = os.MkdirAll(filepath.Join(host.dataDir, "webview"), 0o755)
	t.loadConfig()
	return t
}

// ---------- 生命周期 ----------

func (t *Tailnet) startup() {
	go t.pollLoop()
	go func() {
		t.mu.Lock()
		autoD, autoC := t.cfg.AutoDaemon, t.cfg.AutoConnect
		t.mu.Unlock()
		if !autoD {
			return
		}
		if _, err := t.findBin("tailscaled.exe"); err != nil {
			return // 还没装组件：静默跳过，不打扰只想用代理的人
		}
		if err := t.startDaemon(); err != nil {
			t.setErr(err.Error())
			return
		}
		if autoC {
			if err := t.connect(""); err != nil {
				t.setErr(err.Error())
			}
		}
	}()
}

func (t *Tailnet) shutdown() {
	t.mu.Lock()
	stop := t.cfg.StopOnExit
	t.mu.Unlock()
	if stop {
		_ = t.stopDaemon()
	}
}

func (t *Tailnet) emit(name string, data ...interface{}) { t.host.emit(name, data...) }

func (t *Tailnet) setErr(s string) {
	t.mu.Lock()
	t.lastErr = s
	t.mu.Unlock()
	t.kickPoll()
}

func (t *Tailnet) kickPoll() {
	select {
	case t.kick <- struct{}{}:
	default:
	}
}

// 后台轮询：窗口可见时 3 秒一次，藏在托盘时 10 秒一次；状态有变化才通知前端和托盘。
func (t *Tailnet) pollLoop() {
	for {
		t.pollOnce()
		d := 3 * time.Second
		if !t.host.visible.Load() {
			d = 10 * time.Second
		}
		select {
		case <-time.After(d):
		case <-t.kick:
		}
	}
}

func (t *Tailnet) pollOnce() {
	st := t.GetStatus()
	b, _ := json.Marshal(st)
	t.mu.Lock()
	changed := string(b) != t.lastJSON
	t.lastJSON = string(b)
	t.last = st
	t.mu.Unlock()
	t.learnSuffix(st.Suffix)
	if changed {
		t.emit("ts:state", st)
		t.host.updateTray()
	}
}

// 记住当前服务器配置的 MagicDNS 后缀；它变化时通知代理模块同步规则 / DNS / 系统代理豁免。
func (t *Tailnet) learnSuffix(suffix string) {
	if suffix == "" {
		return
	}
	t.mu.Lock()
	i := t.findProfile(t.cfg.Active)
	changed := i >= 0 && t.cfg.Profiles[i].Suffix != suffix
	if changed {
		t.cfg.Profiles[i].Suffix = suffix
		t.saveConfigLocked()
	}
	t.mu.Unlock()
	if changed {
		go t.host.clash.syncTailnet()
	}
}

func (t *Tailnet) lastStatus() TSStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.last
}

func (t *Tailnet) knownSuffix() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if i := t.findProfile(t.cfg.Active); i >= 0 {
		return t.cfg.Profiles[i].Suffix
	}
	return ""
}

func (t *Tailnet) serverHost() string {
	p := t.activeProfile()
	if p.Server == "" {
		return ""
	}
	if u, err := url.Parse(p.Server); err == nil {
		return u.Hostname()
	}
	return ""
}

// ---------- 配置 ----------

func (t *Tailnet) configPath() string { return filepath.Join(t.dataDir, "config.json") }

func (t *Tailnet) loadConfig() {
	t.cfg = TSConfig{AutoDaemon: true, AutoConnect: true, StopOnExit: true,
		Prefs: TSPrefs{AcceptRoutes: true, AcceptDNS: true}}
	if b, err := os.ReadFile(t.configPath()); err == nil {
		_ = json.Unmarshal(b, &t.cfg)
	}
	if len(t.cfg.Profiles) == 0 {
		// 首次运行 / 从旧版迁移：旧状态文件归入 default 配置，登录不丢失
		if ls := t.cfg.LoginServer; ls != "" {
			t.cfg.Profiles = []TSProfile{
				{ID: "default", Name: "自建 Headscale", Server: ls},
				{ID: "official", Name: "官方 Tailscale"},
			}
		} else {
			t.cfg.Profiles = []TSProfile{{ID: "default", Name: "官方 Tailscale"}}
		}
		t.cfg.LoginServer = ""
		t.cfg.Active = "default"
	}
	if t.findProfile(t.cfg.Active) < 0 {
		t.cfg.Active = t.cfg.Profiles[0].ID
	}
	t.saveConfigLocked()
}

func (t *Tailnet) saveConfigLocked() {
	b, _ := json.MarshalIndent(t.cfg, "", "  ")
	_ = os.WriteFile(t.configPath(), b, 0o644)
}

func (t *Tailnet) findProfile(id string) int {
	for i, p := range t.cfg.Profiles {
		if p.ID == id {
			return i
		}
	}
	return -1
}

func (t *Tailnet) activeProfile() TSProfile {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cfg.Profiles[t.findProfile(t.cfg.Active)]
}

// profilePaths 返回某配置的 state 文件与 statedir（default 沿用旧路径，便于迁移旧数据）。
func (t *Tailnet) profilePaths(p TSProfile) (string, string) {
	if p.ID == "default" {
		return filepath.Join(t.dataDir, "tailscaled.state"), filepath.Join(t.dataDir, "state")
	}
	d := filepath.Join(t.dataDir, "profiles", p.ID)
	_ = os.MkdirAll(filepath.Join(d, "state"), 0o755)
	return filepath.Join(d, "tailscaled.state"), filepath.Join(d, "state")
}

func normServer(s string) (string, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	if s != "" && !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return "", errors.New("控制服务器地址必须以 http:// 或 https:// 开头，留空则使用官方")
	}
	return s, nil
}

func (t *Tailnet) GetConfig() TSConfig {
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.cfg
	c.Profiles = append([]TSProfile{}, t.cfg.Profiles...)
	return c
}

// SaveConfig 只保存开关类设置，配置列表与偏好由专门的方法维护。
func (t *Tailnet) SaveConfig(c TSConfig) Result {
	t.mu.Lock()
	t.cfg.AutoDaemon = c.AutoDaemon
	t.cfg.AutoConnect = c.AutoConnect
	t.cfg.StopOnExit = c.StopOnExit
	t.cfg.TailscaleDir = strings.TrimSpace(c.TailscaleDir)
	t.saveConfigLocked()
	t.mu.Unlock()
	t.kickPoll()
	return okRes("已保存")
}

func (t *Tailnet) AddProfile(name, server string) Result {
	name = strings.TrimSpace(name)
	if name == "" {
		return failRes("请填写名称")
	}
	server, err := normServer(server)
	if err != nil {
		return errRes(err)
	}
	t.mu.Lock()
	t.cfg.Profiles = append(t.cfg.Profiles, TSProfile{ID: "p" + strconv.FormatInt(time.Now().UnixMilli(), 36), Name: name, Server: server})
	t.saveConfigLocked()
	t.mu.Unlock()
	return okRes("已添加")
}

func (t *Tailnet) RenameProfile(id, name string) Result {
	name = strings.TrimSpace(name)
	if name == "" {
		return failRes("请填写名称")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	i := t.findProfile(id)
	if i < 0 {
		return failRes("配置不存在")
	}
	t.cfg.Profiles[i].Name = name
	t.saveConfigLocked()
	return okRes("已重命名")
}

func (t *Tailnet) DeleteProfile(id string) Result {
	t.mu.Lock()
	defer t.mu.Unlock()
	i := t.findProfile(id)
	if i < 0 {
		return failRes("配置不存在")
	}
	if id == t.cfg.Active {
		return failRes("不能删除正在使用的配置，请先切换到其他配置")
	}
	if len(t.cfg.Profiles) <= 1 {
		return failRes("至少保留一个配置")
	}
	if id == "default" {
		_ = os.Remove(filepath.Join(t.dataDir, "tailscaled.state"))
		_ = os.RemoveAll(filepath.Join(t.dataDir, "state"))
	} else {
		_ = os.RemoveAll(filepath.Join(t.dataDir, "profiles", id))
	}
	t.cfg.Profiles = append(t.cfg.Profiles[:i], t.cfg.Profiles[i+1:]...)
	t.saveConfigLocked()
	return okRes("已删除")
}

// SwitchProfile 切换服务器：停止当前 tailscaled，以新配置的独立状态重启并自动连接。
func (t *Tailnet) SwitchProfile(id string) Result {
	t.mu.Lock()
	i := t.findProfile(id)
	cur := t.cfg.Active
	owns := t.daemon != nil
	t.mu.Unlock()
	if i < 0 {
		return failRes("配置不存在")
	}
	if id == cur {
		return okRes("")
	}
	t.cancelUp()
	if owns {
		_ = t.stopDaemon()
	} else if t.queryStatus().DaemonRunning {
		return failRes("当前服务不是由本程序启动的（可能是官方客户端的服务），无法切换，请先停止它")
	}
	t.mu.Lock()
	t.cfg.Active = id
	t.cfg.Prefs.ExitNode = "" // 出口节点属于上一个网络，换服务器后清掉
	t.lastErr = ""
	t.saveConfigLocked()
	t.mu.Unlock()
	go t.host.clash.syncTailnet()
	return errRes(t.connect(""))
}

// ---------- 查找可执行文件 ----------

func (t *Tailnet) binDirs() []string {
	t.mu.Lock()
	custom := t.cfg.TailscaleDir
	t.mu.Unlock()
	var dirs []string
	if custom != "" {
		dirs = append(dirs, custom)
	}
	return append(dirs,
		filepath.Join(t.dataDir, "bin"),
		t.exeDir,
		filepath.Join(t.exeDir, "bin"),
		filepath.Join(t.exeDir, "tailscale"),
		`C:\Program Files\Tailscale`,
	)
}

func (t *Tailnet) findBin(name string) (string, error) {
	for _, d := range t.binDirs() {
		p := filepath.Join(d, name)
		if fileExists(p) {
			return p, nil
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("找不到 %s。请在「设置 → 组件」里一键下载，或把它放到 ClashScale.exe 同目录的 bin 子目录", name)
}

type BinInfo struct {
	Cli      string `json:"cli"`
	Daemon   string `json:"daemon"`
	Wintun   string `json:"wintun"`
	Official bool   `json:"official"` // 找到的是官方客户端安装目录
}

func (t *Tailnet) GetBins() BinInfo {
	var b BinInfo
	b.Cli, _ = t.findBin("tailscale.exe")
	b.Daemon, _ = t.findBin("tailscaled.exe")
	if b.Daemon != "" {
		if p := filepath.Join(filepath.Dir(b.Daemon), "wintun.dll"); fileExists(p) {
			b.Wintun = p
		}
		b.Official = strings.HasPrefix(strings.ToLower(b.Daemon), `c:\program files\tailscale`)
	}
	return b
}

// ---------- tailscaled ----------

func (t *Tailnet) StartDaemon() Result { return errRes(t.startDaemon()) }
func (t *Tailnet) StopDaemon() Result  { return errRes(t.stopDaemon()) }

func (t *Tailnet) startDaemon() error {
	t.mu.Lock()
	if t.daemon != nil {
		t.mu.Unlock()
		return nil
	}
	stopOnExit := t.cfg.StopOnExit
	t.mu.Unlock()

	if st := t.queryStatus(); st.DaemonRunning {
		return nil // 已有守护进程（比如官方服务）在运行
	}

	bin, err := t.findBin("tailscaled.exe")
	if err != nil {
		return err
	}

	logPath := filepath.Join(t.dataDir, "tailscaled.log")
	if fi, err := os.Stat(logPath); err == nil && fi.Size() > 5<<20 {
		_ = os.Rename(logPath, logPath+".old")
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}

	stateFile, stateDir := t.profilePaths(t.activeProfile())
	cmd := exec.Command(bin, "--state="+stateFile, "--statedir="+stateDir)
	cmd.Dir = t.dataDir
	cmd.Env = append(os.Environ(), "TS_LOGS_DIR="+filepath.Join(t.dataDir, "logs"))
	cmd.Stdout = logf
	cmd.Stderr = logf
	hidden(cmd)
	if err := cmd.Start(); err != nil {
		logf.Close()
		return fmt.Errorf("启动 tailscaled 失败: %v（需要管理员权限）", err)
	}
	if stopOnExit {
		bindToJob(cmd) // 程序异常退出时一并回收；用户选择「退出时保留服务」则不绑定
	}

	t.mu.Lock()
	t.daemon = cmd
	t.lastErr = ""
	t.mu.Unlock()

	go func() {
		_ = cmd.Wait()
		logf.Close()
		t.mu.Lock()
		if t.daemon == cmd {
			t.daemon = nil
		}
		t.mu.Unlock()
		t.kickPoll()
	}()

	// 等待 IPC 管道就绪
	for i := 0; i < 40; i++ {
		time.Sleep(300 * time.Millisecond)
		if t.queryStatus().DaemonRunning {
			t.kickPoll()
			return nil
		}
		t.mu.Lock()
		dead := t.daemon == nil
		t.mu.Unlock()
		if dead {
			return errors.New("tailscaled 启动后立即退出，请查看日志（缺少 wintun.dll？）")
		}
	}
	return errors.New("等待 tailscaled 就绪超时，请查看日志")
}

func (t *Tailnet) stopDaemon() error {
	t.cancelUp()
	t.mu.Lock()
	cmd := t.daemon
	t.mu.Unlock()
	if cmd == nil {
		if t.queryStatus().DaemonRunning {
			return errors.New("当前服务不是由本程序启动的（可能是官方服务），未停止")
		}
		return nil
	}
	_ = t.runCLI(8*time.Second, "down")
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	for i := 0; i < 20; i++ { // 等待 Wait 协程清理 t.daemon
		time.Sleep(150 * time.Millisecond)
		t.mu.Lock()
		gone := t.daemon == nil
		t.mu.Unlock()
		if gone {
			break
		}
	}
	if bin, err := t.findBin("tailscaled.exe"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		c := hidden(exec.CommandContext(ctx, bin, "--cleanup"))
		c.Env = append(os.Environ(), "TS_LOGS_DIR="+filepath.Join(t.dataDir, "logs"))
		_ = c.Run()
	}
	t.kickPoll()
	return nil
}

// ---------- tailscale CLI ----------

func (t *Tailnet) runCLIOut(timeout time.Duration, args ...string) (string, error) {
	cli, err := t.findBin("tailscale.exe")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := hidden(exec.CommandContext(ctx, cli, args...)).CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		if s == "" {
			s = err.Error()
		}
		return s, errors.New(s)
	}
	return s, nil
}

func (t *Tailnet) runCLI(timeout time.Duration, args ...string) error {
	_, err := t.runCLIOut(timeout, args...)
	return err
}

func firstIPv4(ips []string) string {
	for _, ip := range ips {
		if strings.Contains(ip, ".") {
			return ip
		}
	}
	if len(ips) > 0 {
		return ips[0]
	}
	return ""
}

func (t *Tailnet) queryStatus() TSStatus {
	st := TSStatus{Backend: "Stopped", Peers: []PeerInfo{}, Health: []string{}}
	cli, err := t.findBin("tailscale.exe")
	if err != nil {
		st.MissingBin = true
		st.Error = err.Error()
		return st
	}
	if _, err := t.findBin("tailscaled.exe"); err != nil {
		st.MissingBin = true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	out, _ := hidden(exec.CommandContext(ctx, cli, "status", "--json")).Output()
	var raw tsStatus
	if json.Unmarshal(out, &raw) != nil || raw.BackendState == "" {
		return st
	}
	st.DaemonRunning = true
	st.Backend = raw.BackendState
	st.AuthURL = raw.AuthURL
	st.Version = raw.Version
	if raw.Health != nil {
		st.Health = raw.Health
	}
	suffix := raw.MagicDNSSuffix
	if raw.CurrentTailnet != nil {
		st.Tailnet = raw.CurrentTailnet.Name
		if raw.CurrentTailnet.MagicDNSSuffix != "" {
			suffix = raw.CurrentTailnet.MagicDNSSuffix
		}
	}
	if raw.Self != nil {
		st.SelfName = raw.Self.HostName
		st.SelfDNS = strings.TrimSuffix(raw.Self.DNSName, ".")
		st.SelfIP = firstIPv4(raw.Self.TailscaleIPs)
		if suffix == "" {
			if i := strings.Index(st.SelfDNS, "."); i > 0 {
				suffix = st.SelfDNS[i+1:]
			}
		}
	}
	st.Suffix = strings.TrimSuffix(suffix, ".")
	for _, p := range raw.Peer {
		pi := PeerInfo{
			ID: p.ID, DNS: strings.TrimSuffix(p.DNSName, "."), Name: p.HostName, OS: p.OS,
			IP: firstIPv4(p.TailscaleIPs), Online: p.Online, ExitNode: p.ExitNode, ExitNodeOption: p.ExitNodeOption,
			Relay: p.Relay, Direct: p.CurAddr != "",
		}
		if !strings.HasPrefix(p.LastSeen, "0001-") {
			pi.LastSeen = p.LastSeen
		}
		if u, ok := raw.User[strconv.FormatInt(p.UserID, 10)]; ok {
			pi.Owner = u.LoginName
		}
		st.Peers = append(st.Peers, pi)
	}
	sort.Slice(st.Peers, func(i, j int) bool {
		if st.Peers[i].Online != st.Peers[j].Online {
			return st.Peers[i].Online
		}
		return strings.ToLower(st.Peers[i].Name) < strings.ToLower(st.Peers[j].Name)
	})
	return st
}

func (t *Tailnet) GetStatus() TSStatus {
	st := t.queryStatus()
	t.mu.Lock()
	st.Connecting = t.upCancel != nil
	st.OwnsDaemon = t.daemon != nil
	st.Installing = t.installing
	st.LastError = t.lastErr
	if st.AuthURL == "" {
		st.AuthURL = t.authURL
	}
	t.mu.Unlock()
	return st
}

// Connect：启动服务（如需要）并执行 tailscale up。首次需要登录时自动打开浏览器。
func (t *Tailnet) Connect() Result { return errRes(t.connect("")) }

// ConnectWithKey 使用 Auth Key 登录（备选方案，无需浏览器）。Key 不会写入磁盘。
func (t *Tailnet) ConnectWithKey(key string) Result {
	key = strings.TrimSpace(key)
	if key == "" {
		return failRes("请填写 Auth Key")
	}
	if t.activeProfile().Server == "" && !strings.HasPrefix(key, "tskey-") {
		return failRes("Auth Key 格式不对，官方 Key 应以 tskey- 开头")
	}
	return errRes(t.connect(key))
}

func (t *Tailnet) connect(key string) error {
	if err := t.startDaemon(); err != nil {
		return err
	}
	t.mu.Lock()
	if t.upCancel != nil {
		t.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.upCancel = cancel
	t.authURL = ""
	t.lastErr = ""
	t.mu.Unlock()

	go t.runUp(ctx, key)
	t.kickPoll()
	return nil
}

// upArgs 生成 `tailscale up` 参数。所有偏好都显式带上，这样既不会触发
// 「需要列出所有非默认参数」的报错，也不会在重连时丢掉用户的设置。
func (t *Tailnet) upArgs(key string, reset, noExit bool) []string {
	t.mu.Lock()
	p := t.cfg.Prefs
	t.mu.Unlock()
	args := []string{"up", "--unattended",
		fmt.Sprintf("--accept-routes=%t", p.AcceptRoutes),
		fmt.Sprintf("--accept-dns=%t", p.AcceptDNS),
		fmt.Sprintf("--shields-up=%t", p.ShieldsUp),
		fmt.Sprintf("--advertise-exit-node=%t", p.AdvertiseExit),
	}
	if !noExit && p.ExitNode != "" && !p.AdvertiseExit {
		args = append(args, "--exit-node="+p.ExitNode, fmt.Sprintf("--exit-node-allow-lan-access=%t", p.ExitLAN))
	} else {
		args = append(args, "--exit-node=")
	}
	if reset {
		args = append(args, "--reset")
	}
	if ls := t.activeProfile().Server; ls != "" {
		args = append(args, "--login-server="+ls)
	}
	if key != "" {
		args = append(args, "--auth-key="+key)
	}
	return args
}

func (t *Tailnet) runUp(ctx context.Context, key string) {
	defer func() {
		t.mu.Lock()
		t.upCancel = nil
		t.authURL = ""
		t.mu.Unlock()
		t.kickPoll()
	}()

	cli, err := t.findBin("tailscale.exe")
	if err != nil {
		t.setErr(err.Error())
		return
	}
	opened := false
	reset, noExit := false, false
	for attempt := 0; attempt < 3; attempt++ {
		cmd := hidden(exec.CommandContext(ctx, cli, t.upArgs(key, reset, noExit)...))
		pr, pw := io.Pipe()
		cmd.Stdout, cmd.Stderr = pw, pw

		var sb strings.Builder
		done := make(chan struct{})
		go func() {
			sc := bufio.NewScanner(pr)
			for sc.Scan() {
				line := sc.Text()
				sb.WriteString(line + "\n")
				if u := urlRe.FindString(line); u != "" && (strings.Contains(u, "/a/") || strings.Contains(u, "/register/") || strings.Contains(u, "/auth/") || strings.Contains(strings.ToLower(line), "visit")) {
					t.mu.Lock()
					t.authURL = u
					t.mu.Unlock()
					t.kickPoll()
					if !opened && t.host.ctx != nil {
						opened = true
						wr.BrowserOpenURL(t.host.ctx, u)
					}
				}
			}
			close(done)
		}()

		if err := cmd.Start(); err != nil {
			pw.Close()
			<-done
			t.setErr(err.Error())
			return
		}
		err := cmd.Wait()
		pw.Close()
		<-done
		if err == nil || ctx.Err() != nil {
			return
		}
		out := strings.TrimSpace(sb.String())
		low := strings.ToLower(out)
		if !reset && strings.Contains(out, "--reset") {
			reset = true // 之前用 CLI 设过非默认参数，加 --reset 重试
			continue
		}
		if !noExit && strings.Contains(low, "exit node") {
			noExit = true // 出口节点已失效（离线 / 不再提供），清掉后重试，别让它卡住连接
			t.mu.Lock()
			t.cfg.Prefs.ExitNode = ""
			t.saveConfigLocked()
			t.mu.Unlock()
			continue
		}
		if out == "" {
			out = err.Error()
		}
		if key != "" {
			out = strings.ReplaceAll(out, key, "tskey-***")
		}
		t.setErr(out)
		return
	}
}

func (t *Tailnet) cancelUp() {
	t.mu.Lock()
	if t.upCancel != nil {
		t.upCancel()
	}
	t.mu.Unlock()
}

func (t *Tailnet) Disconnect() Result {
	t.cancelUp()
	r := errRes(t.runCLI(15*time.Second, "down"))
	t.kickPoll()
	return r
}

func (t *Tailnet) Logout() Result {
	t.cancelUp()
	r := errRes(t.runCLI(15*time.Second, "logout"))
	t.kickPoll()
	return r
}

// setConnected 供「一键场景」使用。
func (t *Tailnet) setConnected(on bool) Result {
	if on {
		if st := t.GetStatus(); st.Backend == "Running" || st.Connecting {
			return okRes("")
		}
		return t.Connect()
	}
	if !t.queryStatus().DaemonRunning {
		return okRes("")
	}
	return t.Disconnect()
}

// ---------- 日志 ----------

func (t *Tailnet) logPath() string { return filepath.Join(t.dataDir, "tailscaled.log") }

// ReadLog 返回 tailscaled.log 末尾的内容。
func (t *Tailnet) ReadLog() string {
	f, err := os.Open(t.logPath())
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	const max = 48 << 10
	start := int64(0)
	if fi.Size() > max {
		start = fi.Size() - max
	}
	buf := make([]byte, fi.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return ""
	}
	s := string(buf)
	if start > 0 {
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
	}
	return s
}

func (t *Tailnet) OpenLog() { _ = exec.Command("notepad", t.logPath()).Start() }

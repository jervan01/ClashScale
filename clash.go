package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// =====================================================================
//  代理模块（Mihomo 内核）。与组网模块完全独立：启停、自启、退出行为互不牵连。
// =====================================================================

type Rule struct {
	ID      string `json:"id"`
	Type    string `json:"type"`    // DOMAIN / DOMAIN-SUFFIX / DOMAIN-KEYWORD / IP-CIDR / GEOIP / PROCESS-NAME
	Payload string `json:"payload"` // ts.net / openai ...
	Target  string `json:"target"`  // DIRECT / PROXY / REJECT
	Enabled bool   `json:"enabled"`
}

type Profile struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Local     bool   `json:"local"` // 本地导入的配置，没有订阅链接
	UpdatedAt int64  `json:"updatedAt"`
	NodeCount int    `json:"nodeCount"`
	RuleCount int    `json:"ruleCount"`
	Upload    int64  `json:"upload"` // 订阅流量信息（来自 Subscription-Userinfo）
	Download  int64  `json:"download"`
	Total     int64  `json:"total"`
	Expire    int64  `json:"expire"` // unix 秒；0 = 长期
}

type Settings struct {
	MixedPort       int       `json:"mixedPort"`
	ControllerPort  int       `json:"controllerPort"`
	Secret          string    `json:"secret"`
	AllowLAN        bool      `json:"allowLan"`
	Mode            string    `json:"mode"` // rule / global / direct
	Mirror          string    `json:"mirror"`
	ActiveProfile   string    `json:"activeProfile"` // 空 = 内置默认配置
	UseSubRules     bool      `json:"useSubRules"`   // true: 使用订阅自带规则；false: 使用内置基础规则
	SystemProxy     bool      `json:"systemProxy"`
	AutoStartCore   bool      `json:"autoStartCore"`   // 启动本程序时自动运行内核
	AutoSystemProxy bool      `json:"autoSystemProxy"` // 内核启动后自动开启系统代理
	AutoUpdateHours int       `json:"autoUpdateHours"` // 订阅自动更新间隔（小时），0 = 关闭
	Rules           []Rule    `json:"rules"`
	Profiles        []Profile `json:"profiles"`
}

// General 是设置页可编辑的字段子集。
type General struct {
	MixedPort       int    `json:"mixedPort"`
	ControllerPort  int    `json:"controllerPort"`
	AllowLAN        bool   `json:"allowLan"`
	Mirror          string `json:"mirror"`
	UseSubRules     bool   `json:"useSubRules"`
	AutoStartCore   bool   `json:"autoStartCore"`
	AutoSystemProxy bool   `json:"autoSystemProxy"`
	AutoUpdateHours int    `json:"autoUpdateHours"`
}

type ClashStatus struct {
	Running       bool   `json:"running"`
	DataDir       string `json:"dataDir"`
	CoreInstalled bool   `json:"coreInstalled"`
	GeoReady      bool   `json:"geoReady"`
	Version       string `json:"version"`
	MixedPort     int    `json:"mixedPort"`
	Mode          string `json:"mode"`
	SystemProxy   bool   `json:"systemProxy"`
	ActiveProfile string `json:"activeProfile"`
	Downloading   bool   `json:"downloading"`
}

type Clash struct {
	host    *App
	dataDir string

	mu sync.Mutex // 保护 st
	st Settings

	procMu        sync.Mutex // 保护进程相关字段
	cmd           *exec.Cmd
	running       bool
	cancelTraffic context.CancelFunc
	version       string

	logMu sync.Mutex
	logs  []string

	dlMu        sync.Mutex
	downloading bool
}

func newClash(host *App, dir string) *Clash {
	c := &Clash{host: host, dataDir: dir}
	_ = os.MkdirAll(filepath.Join(dir, "profiles"), 0o755)
	c.loadSettings()
	return c
}

func (a *Clash) emit(name string, data ...interface{}) { a.host.emit(name, data...) }

func (a *Clash) startup() {
	a.recoverStaleProxy() // 上次关机没清掉的代理，先清
	// 系统代理的初始状态完全由「内核启动后自动开启系统代理」决定
	a.update(func(s *Settings) { s.SystemProxy = s.AutoSystemProxy })
	go func() {
		a.detectVersion()
		a.emitState()
	}()
	if a.snapshot().AutoStartCore && a.coreInstalled() {
		go a.StartCore()
	}
	go a.autoUpdateLoop()
}

// cleanup：先清系统代理（注册表直写，微秒级），再杀内核。
func (a *Clash) cleanup() {
	if systemProxyIsOurs(a.snapshot().MixedPort) {
		_ = clearSystemProxy()
	}
	a.procMu.Lock()
	cmd := a.cmd
	a.procMu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// ---------- 设置持久化 ----------

func defaultSettings() Settings {
	return Settings{
		MixedPort:       7890,
		ControllerPort:  9090,
		Secret:          randomHex(12),
		Mode:            "rule",
		Mirror:          "https://ghfast.top/",
		UseSubRules:     true,
		AutoStartCore:   true,
		AutoSystemProxy: true,
		AutoUpdateHours: 24,
		Rules: []Rule{
			{ID: randomHex(4), Type: "DOMAIN-KEYWORD", Payload: "claude", Target: "PROXY", Enabled: true},
			{ID: randomHex(4), Type: "DOMAIN-KEYWORD", Payload: "openai", Target: "PROXY", Enabled: true},
		},
		Profiles: []Profile{},
	}
}

func (a *Clash) settingsPath() string { return filepath.Join(a.dataDir, "settings.json") }
func (a *Clash) configPath() string   { return filepath.Join(a.dataDir, "config.yaml") }
func (a *Clash) profilePath(id string) string {
	return filepath.Join(a.dataDir, "profiles", id+".yaml")
}
func (a *Clash) fileExists(name string) bool { return fileExists(filepath.Join(a.dataDir, name)) }

func (a *Clash) loadSettings() {
	st := defaultSettings()
	if b, err := os.ReadFile(a.settingsPath()); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	if st.MixedPort == 0 {
		st.MixedPort = 7890
	}
	if st.ControllerPort == 0 {
		st.ControllerPort = 9090
	}
	if st.Secret == "" {
		st.Secret = randomHex(12)
	}
	if st.Mode == "" {
		st.Mode = "rule"
	}
	if st.Rules == nil {
		st.Rules = []Rule{}
	}
	if st.Profiles == nil {
		st.Profiles = []Profile{}
	}
	a.mu.Lock()
	a.st = st
	a.saveLocked()
	a.mu.Unlock()
}

func (a *Clash) saveLocked() {
	if b, err := json.MarshalIndent(a.st, "", "  "); err == nil {
		_ = os.WriteFile(a.settingsPath(), b, 0o644)
	}
}

func (a *Clash) update(f func(s *Settings)) {
	a.mu.Lock()
	f(&a.st)
	a.saveLocked()
	a.mu.Unlock()
}

func (a *Clash) snapshot() Settings {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.st
	s.Rules = append([]Rule{}, a.st.Rules...)
	s.Profiles = append([]Profile{}, a.st.Profiles...)
	return s
}

// ---------- 前端可调用：状态与设置 ----------

func (a *Clash) GetSettings() Settings { return a.snapshot() }

func (a *Clash) GetStatus() ClashStatus {
	st := a.snapshot()
	a.procMu.Lock()
	running, ver := a.running, a.version
	a.procMu.Unlock()
	a.dlMu.Lock()
	dl := a.downloading
	a.dlMu.Unlock()
	return ClashStatus{
		Running:       running,
		DataDir:       a.dataDir,
		CoreInstalled: a.coreInstalled(),
		GeoReady:      a.geoReady(),
		Version:       ver,
		MixedPort:     st.MixedPort,
		Mode:          st.Mode,
		SystemProxy:   st.SystemProxy,
		ActiveProfile: st.ActiveProfile,
		Downloading:   dl,
	}
}

func (a *Clash) emitState() {
	a.emit("clash:state", a.GetStatus())
	a.host.updateTray()
}

func (a *Clash) SaveGeneral(g General) Result {
	for _, p := range []int{g.MixedPort, g.ControllerPort} {
		if p < 1024 || p > 65535 {
			return failRes("端口需要在 1024–65535 之间")
		}
	}
	if g.MixedPort == g.ControllerPort {
		return failRes("代理端口与控制端口不能相同")
	}
	if g.AutoUpdateHours < 0 || g.AutoUpdateHours > 24*30 {
		return failRes("订阅自动更新间隔不合法")
	}
	old := a.snapshot()
	a.update(func(s *Settings) {
		s.MixedPort = g.MixedPort
		s.ControllerPort = g.ControllerPort
		s.AllowLAN = g.AllowLAN
		s.Mirror = strings.TrimSpace(g.Mirror)
		s.UseSubRules = g.UseSubRules
		s.AutoStartCore = g.AutoStartCore
		s.AutoSystemProxy = g.AutoSystemProxy
		s.AutoUpdateHours = g.AutoUpdateHours
	})
	if !a.isRunning() {
		a.emitState()
		return okRes("设置已保存")
	}
	if old.MixedPort != g.MixedPort || old.ControllerPort != g.ControllerPort {
		r := a.restartCore()
		if r.OK {
			return okRes("设置已保存，内核已重启")
		}
		return r
	}
	r := a.applyAndReload()
	if r.OK {
		a.emitState()
		return okRes("设置已保存并生效")
	}
	return r
}

func (a *Clash) SetMode(mode string) Result {
	if mode != "rule" && mode != "global" && mode != "direct" {
		return failRes("未知的模式")
	}
	a.update(func(s *Settings) { s.Mode = mode })
	if a.isRunning() {
		if err := a.api("PATCH", "/configs", map[string]string{"mode": mode}, nil); err != nil {
			return failRes("切换失败: " + err.Error())
		}
		_ = a.api("DELETE", "/connections", nil, nil)
	}
	a.emitState()
	return okRes("")
}

func (a *Clash) SetSystemProxy(on bool) Result {
	a.update(func(s *Settings) { s.SystemProxy = on })
	if a.isRunning() {
		if err := a.applySystemProxy(on); err != nil {
			a.update(func(s *Settings) { s.SystemProxy = !on })
			a.emitState()
			return failRes("设置系统代理失败: " + err.Error())
		}
	}
	a.emitState()
	if on {
		return okRes("系统代理已开启")
	}
	return okRes("系统代理已关闭")
}

// 系统代理的「不走代理」附加项：Tailscale 的 MagicDNS 域名。
func (a *Clash) bypassExtras() []string {
	as := a.host.tailnetAssist()
	if !as.On {
		return nil
	}
	out := []string{"*.ts.net"}
	if as.Suffix != "" && as.Suffix != "ts.net" && !strings.HasSuffix(as.Suffix, ".ts.net") {
		out = append(out, "*."+as.Suffix)
	}
	if as.ServerHost != "" {
		out = append(out, as.ServerHost)
	}
	return out
}

func (a *Clash) applySystemProxy(on bool) error {
	if on {
		return setSystemProxy(a.snapshot().MixedPort, a.bypassExtras())
	}
	return clearSystemProxy()
}

func (a *Clash) clearProxyIfNeeded() {
	if a.snapshot().SystemProxy {
		_ = clearSystemProxy()
	}
}

// 组网信息（MagicDNS 后缀、自建服务器）变化后调用：让规则、DNS、系统代理豁免列表同步。
func (a *Clash) syncTailnet() {
	if !a.isRunning() {
		return
	}
	if a.snapshot().SystemProxy {
		_ = a.applySystemProxy(true)
	}
	_ = a.applyAndReload()
}

// 系统代理指向本程序端口，但端口没人监听 → 是上次没清掉的残留，关掉。
func (a *Clash) recoverStaleProxy() {
	port := a.snapshot().MixedPort
	if !systemProxyIsOurs(port) {
		return
	}
	if portBusy(port) { // 端口有人在用（比如别的代理软件），不动它
		return
	}
	_ = clearSystemProxy()
	a.pushLog("[app] 检测到上次遗留的系统代理，已自动关闭")
}

// ---------- 自定义规则 ----------

var ruleTypes = map[string]bool{
	"DOMAIN": true, "DOMAIN-SUFFIX": true, "DOMAIN-KEYWORD": true,
	"IP-CIDR": true, "GEOIP": true, "PROCESS-NAME": true,
}

var ruleTargets = map[string]bool{"DIRECT": true, "PROXY": true, "REJECT": true}

func (a *Clash) SaveRules(rules []Rule) Result {
	clean := make([]Rule, 0, len(rules))
	for _, r := range rules {
		r.Payload = strings.TrimSpace(r.Payload)
		if !ruleTypes[r.Type] {
			return failRes("不支持的规则类型: " + r.Type)
		}
		if !ruleTargets[r.Target] {
			return failRes("不支持的策略: " + r.Target)
		}
		if r.Payload == "" || strings.ContainsAny(r.Payload, ", \t\r\n") {
			return failRes("规则内容不能为空，且不能包含逗号或空格")
		}
		if r.ID == "" {
			r.ID = randomHex(4)
		}
		clean = append(clean, r)
	}
	a.update(func(s *Settings) { s.Rules = clean })
	return a.applyAndReload()
}

// ManagedRules 返回由「组网智能适配」自动注入的规则（只读展示）。
func (a *Clash) ManagedRules() []string {
	as := a.host.tailnetAssist()
	return tailnetManagedRules(as)
}

// ---------- 日志 ----------

func (a *Clash) pushLog(line string) {
	a.logMu.Lock()
	a.logs = append(a.logs, line)
	if len(a.logs) > 500 {
		a.logs = a.logs[len(a.logs)-500:]
	}
	a.logMu.Unlock()
	a.emit("clash:log", line)
}

func (a *Clash) GetLogs() []string {
	a.logMu.Lock()
	defer a.logMu.Unlock()
	return append([]string{}, a.logs...)
}

func (a *Clash) lastLogs(n int) string {
	a.logMu.Lock()
	defer a.logMu.Unlock()
	start := len(a.logs) - n
	if start < 0 {
		start = 0
	}
	return strings.Join(a.logs[start:], "\n")
}

// ---------- 内核文件与版本 ----------

func (a *Clash) corePath() string { return filepath.Join(a.dataDir, "mihomo.exe") }

func (a *Clash) coreInstalled() bool { return fileExists(a.corePath()) }

func (a *Clash) geoReady() bool {
	return a.fileExists("geosite.dat") && (a.fileExists("geoip.metadb") || a.fileExists("Country.mmdb"))
}

var verRe = regexp.MustCompile(`v\d+\.\d+\.\d+[\w.\-]*`)

func (a *Clash) detectVersion() {
	v := ""
	if a.coreInstalled() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		out, _ := hidden(exec.CommandContext(ctx, a.corePath(), "-v")).Output()
		v = verRe.FindString(string(out))
	}
	a.procMu.Lock()
	a.version = v
	a.procMu.Unlock()
}

func (a *Clash) isRunning() bool {
	a.procMu.Lock()
	defer a.procMu.Unlock()
	return a.running
}

// ---------- 启动 / 停止 ----------

func (a *Clash) StartCore() Result {
	a.procMu.Lock()
	if a.running {
		a.procMu.Unlock()
		return okRes("内核已在运行")
	}
	if !a.coreInstalled() {
		a.procMu.Unlock()
		return failRes("还没有安装内核，请先点击「一键安装」")
	}
	st := a.snapshot()
	for _, p := range []struct {
		port int
		name string
	}{{st.MixedPort, "代理端口"}, {st.ControllerPort, "控制端口"}} {
		if portBusy(p.port) {
			a.procMu.Unlock()
			return failRes(fmt.Sprintf("%s %d 已被其他程序占用。请关闭占用它的程序（比如另一个 Clash 客户端），或在设置里换一个端口", p.name, p.port))
		}
	}
	if err := a.writeConfig(); err != nil {
		a.procMu.Unlock()
		return failRes("生成配置失败: " + err.Error())
	}

	cmd := exec.Command(a.corePath(), "-d", a.dataDir)
	cmd.Dir = a.dataDir
	hidden(cmd)
	w := &lineWriter{fn: a.pushLog}
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Start(); err != nil {
		a.procMu.Unlock()
		return failRes("启动失败: " + err.Error())
	}
	bindToJob(cmd)
	a.cmd = cmd
	a.running = true
	ctx, cancel := context.WithCancel(context.Background())
	a.cancelTraffic = cancel
	a.procMu.Unlock()

	a.pushLog("[app] 内核已启动")
	a.emitState()

	go func() {
		err := cmd.Wait()
		w.Flush()
		a.procMu.Lock()
		if a.cmd == cmd {
			a.running = false
			a.cmd = nil
			if a.cancelTraffic != nil {
				a.cancelTraffic()
				a.cancelTraffic = nil
			}
		}
		a.procMu.Unlock()
		a.pushLog(fmt.Sprintf("[app] 内核已退出 (%v)", err))
		a.clearProxyIfNeeded()
		a.emitState()
	}()

	// 等待控制接口就绪，启动失败时给出明确原因
	ready := false
	for i := 0; i < 40; i++ {
		if !a.isRunning() {
			return failRes("内核启动后立即退出：\n" + a.lastLogs(4))
		}
		if a.api("GET", "/version", nil, nil) == nil {
			ready = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !ready {
		return failRes("内核已启动，但控制接口没有响应，请查看日志")
	}

	go a.trafficLoop(ctx)
	if a.snapshot().SystemProxy {
		if err := a.applySystemProxy(true); err != nil {
			a.pushLog("[app] 设置系统代理失败: " + err.Error())
		}
	}
	a.emit("clash:ready")
	return okRes("内核已启动")
}

func (a *Clash) StopCore() Result {
	a.procMu.Lock()
	cmd, running := a.cmd, a.running
	a.procMu.Unlock()
	if !running || cmd == nil {
		return okRes("内核未运行")
	}
	if err := cmd.Process.Kill(); err != nil {
		return failRes("停止失败: " + err.Error())
	}
	for i := 0; i < 50 && a.isRunning(); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	return okRes("内核已停止")
}

func (a *Clash) ToggleCore() Result {
	if a.isRunning() {
		return a.StopCore()
	}
	return a.StartCore()
}

func (a *Clash) restartCore() Result {
	a.StopCore()
	return a.StartCore()
}

// setRunning 供「一键场景」使用：把内核切到指定状态。
func (a *Clash) setRunning(on bool) Result {
	if on == a.isRunning() {
		return okRes("")
	}
	if on {
		return a.StartCore()
	}
	return a.StopCore()
}

// 重新生成配置，并让运行中的内核热加载；热加载失败则重启。
func (a *Clash) applyAndReload() Result {
	if err := a.writeConfig(); err != nil {
		return failRes("生成配置失败: " + err.Error())
	}
	if !a.isRunning() {
		a.emitState()
		return okRes("已保存")
	}
	err := a.api("PUT", "/configs?force=true", map[string]string{"path": a.configPath()}, nil)
	if err != nil {
		a.pushLog("[app] 热加载失败，改为重启内核: " + err.Error())
		return a.restartCore()
	}
	_ = a.api("DELETE", "/connections", nil, nil)
	a.emit("clash:reloaded")
	a.emitState()
	return okRes("已应用")
}

// ---------- 控制接口 ----------

var apiClient = &http.Client{Timeout: 20 * time.Second}

func (a *Clash) apiRequest(ctx context.Context, method, path string, body interface{}) (*http.Request, error) {
	st := a.snapshot()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method,
		fmt.Sprintf("http://127.0.0.1:%d%s", st.ControllerPort, path), r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+st.Secret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (a *Clash) api(method, path string, body, out interface{}) error {
	req, err := a.apiRequest(context.Background(), method, path, body)
	if err != nil {
		return err
	}
	resp, err := apiClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s", resp.Status, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// ---------- 节点 ----------

type Node struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Delay int    `json:"delay"` // 毫秒；0 表示尚未测速
}

type Group struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Now        string `json:"now"`
	Selectable bool   `json:"selectable"`
	Nodes      []Node `json:"nodes"`
}

type ProxiesResult struct {
	OK     bool    `json:"ok"`
	Msg    string  `json:"msg"`
	Mode   string  `json:"mode"`
	Groups []Group `json:"groups"`
}

func (a *Clash) GetProxies() ProxiesResult {
	if !a.isRunning() {
		return ProxiesResult{Msg: "内核未运行"}
	}
	var raw struct {
		Proxies map[string]struct {
			Name    string   `json:"name"`
			Type    string   `json:"type"`
			Now     string   `json:"now"`
			All     []string `json:"all"`
			History []struct {
				Delay int `json:"delay"`
			} `json:"history"`
		} `json:"proxies"`
	}
	if err := a.api("GET", "/proxies", nil, &raw); err != nil {
		return ProxiesResult{Msg: err.Error()}
	}
	mode := a.snapshot().Mode

	delayOf := func(name string) int {
		p := raw.Proxies[name]
		if n := len(p.History); n > 0 && p.History[n-1].Delay > 0 {
			return p.History[n-1].Delay
		}
		return 0
	}

	var order []string
	if g, ok := raw.Proxies["GLOBAL"]; ok {
		order = g.All
	}
	var groups []Group
	build := func(name string) {
		p, ok := raw.Proxies[name]
		if !ok || len(p.All) == 0 {
			return
		}
		g := Group{Name: name, Type: p.Type, Now: p.Now, Selectable: p.Type == "Selector"}
		for _, n := range p.All {
			child := raw.Proxies[n]
			t := child.Type
			if t == "" {
				t = "Unknown"
			}
			g.Nodes = append(g.Nodes, Node{Name: n, Type: t, Delay: delayOf(n)})
		}
		groups = append(groups, g)
	}
	if mode == "global" {
		build("GLOBAL")
	}
	for _, n := range order {
		build(n)
	}
	if len(groups) == 0 {
		build("GLOBAL")
	}
	return ProxiesResult{OK: true, Mode: mode, Groups: groups}
}

func (a *Clash) SelectProxy(group, name string) Result {
	err := a.api("PUT", "/proxies/"+url.PathEscape(group), map[string]string{"name": name}, nil)
	if err != nil {
		return failRes("切换失败: " + err.Error())
	}
	_ = a.api("DELETE", "/connections", nil, nil) // 断开旧连接，让新节点立刻生效
	a.emit("clash:reloaded")
	return okRes("已切换到 " + name)
}

// TestDelay 返回毫秒延迟；超时或失败返回 -1。
func (a *Clash) TestDelay(name string) int {
	q := url.Values{}
	q.Set("url", "http://www.gstatic.com/generate_204")
	q.Set("timeout", "5000")
	var out struct {
		Delay int `json:"delay"`
	}
	if err := a.api("GET", "/proxies/"+url.PathEscape(name)+"/delay?"+q.Encode(), nil, &out); err != nil || out.Delay <= 0 {
		return -1
	}
	return out.Delay
}

// ---------- 实时流量 ----------

func (a *Clash) trafficLoop(ctx context.Context) {
	for ctx.Err() == nil {
		req, err := a.apiRequest(ctx, "GET", "/traffic", nil)
		if err == nil {
			resp, err := (&http.Client{}).Do(req)
			if err == nil {
				sc := bufio.NewScanner(resp.Body)
				for sc.Scan() {
					var t struct {
						Up   int64 `json:"up"`
						Down int64 `json:"down"`
					}
					if json.Unmarshal(sc.Bytes(), &t) == nil {
						a.emit("clash:traffic", t)
					}
				}
				resp.Body.Close()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(1500 * time.Millisecond):
		}
	}
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	wr "github.com/wailsapp/wails/v2/pkg/runtime"
)

// AppPrefs 是 ClashScale 外壳层的设置（与代理、组网两个模块无关的部分）。
type AppPrefs struct {
	AutoLaunch    bool `json:"autoLaunch"`    // 开机自启（任务计划程序，免 UAC）
	CloseToTray   bool `json:"closeToTray"`   // 点 × 时最小化到托盘
	TailnetAssist bool `json:"tailnetAssist"` // 组网智能适配：自动让 Tailscale 流量绕开代理、修正 MagicDNS
}

type AppInfo struct {
	Version         string `json:"version"`
	DataDir         string `json:"dataDir"`
	ExeDir          string `json:"exeDir"`
	Admin           bool   `json:"admin"`
	AutostartLaunch bool   `json:"autostartLaunch"`
}

// App 是外壳：窗口、托盘、自启、偏好、一键场景、体检。
// 代理（Clash）与组网（Tailnet）是两个互相独立的模块，外壳只负责把它们摆在一起。
type App struct {
	ctx             context.Context
	exeDir          string
	dataDir         string
	autostartLaunch bool

	mu    sync.Mutex
	prefs AppPrefs

	visible     atomic.Bool
	quitting    atomic.Bool
	cleanupOnce sync.Once

	clash *Clash
	ts    *Tailnet
}

func NewApp() *App {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	a := &App{exeDir: filepath.Dir(exe)}
	a.dataDir = resolveDataDir(a.exeDir)
	for _, arg := range os.Args[1:] {
		if arg == "--autostart" {
			a.autostartLaunch = true
		}
	}
	a.visible.Store(!a.autostartLaunch)
	a.loadPrefs()
	a.clash = newClash(a, filepath.Join(a.dataDir, "clash"))
	a.ts = newTailnet(a, filepath.Join(a.dataDir, "tailscale"))
	return a
}

// 便携模式：数据放在程序同级 data 目录；目录不可写时回退到 %AppData%\ClashScale。
func resolveDataDir(exeDir string) string {
	dir := filepath.Join(exeDir, "data")
	if err := os.MkdirAll(dir, 0o755); err == nil {
		probe := filepath.Join(dir, ".write-test")
		if f, err := os.Create(probe); err == nil {
			f.Close()
			os.Remove(probe)
			return dir
		}
	}
	if cfg, err := os.UserConfigDir(); err == nil {
		dir := filepath.Join(cfg, "ClashScale")
		_ = os.MkdirAll(dir, 0o755)
		return dir
	}
	return dir
}

// ---------- 生命周期 ----------

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	if !a.autostartLaunch {
		a.fitWindow()
	}
	if a.snapshotPrefs().AutoLaunch {
		// 刷新任务里记录的 exe 路径，程序挪位置后也不会失效
		go func() { _ = setAutostartTask(a.dataDir, true) }()
	}
	a.clash.startup()
	a.ts.startup()
	a.startTray()
}

func (a *App) shutdown(ctx context.Context) {
	a.cleanup()
	a.ts.shutdown()
	a.stopTray()
}

// cleanup 可重复调用、只执行一次，且必须足够快（关机钩子也会调用）：
// 先清系统代理（注册表直写），再杀内核。
func (a *App) cleanup() {
	a.cleanupOnce.Do(func() { a.clash.cleanup() })
}

func (a *App) emit(name string, data ...interface{}) {
	if a.ctx != nil {
		wr.EventsEmit(a.ctx, name, data...)
	}
}

func (a *App) showWindow() {
	if a.ctx == nil {
		return
	}
	wr.WindowShow(a.ctx)
	wr.WindowUnminimise(a.ctx)
	a.visible.Store(true)
}

func (a *App) quit() {
	a.quitting.Store(true)
	if a.ctx != nil {
		wr.Quit(a.ctx)
	}
}

// 点关闭按钮：开启了「最小化到托盘」就只隐藏窗口。
func (a *App) beforeClose(ctx context.Context) bool {
	if a.quitting.Load() || !a.snapshotPrefs().CloseToTray {
		return false
	}
	wr.WindowHide(ctx)
	a.visible.Store(false)
	return true
}

// fitWindow 按当前屏幕的逻辑尺寸限制窗口大小，避免高缩放比例下窗口超出屏幕。
func (a *App) fitWindow() {
	screens, err := wr.ScreenGetAll(a.ctx)
	if err != nil || len(screens) == 0 {
		return
	}
	sc := screens[0]
	for _, s := range screens {
		if s.IsCurrent {
			sc = s
			break
		}
	}
	w, h := 1080, 740
	if maxH := sc.Size.Height * 88 / 100; sc.Size.Height > 0 && h > maxH {
		h = maxH
	}
	if maxW := sc.Size.Width * 92 / 100; sc.Size.Width > 0 && w > maxW {
		w = maxW
	}
	wr.WindowSetSize(a.ctx, w, h)
	wr.WindowCenter(a.ctx)
}

// ---------- 偏好 ----------

func (a *App) prefsPath() string { return filepath.Join(a.dataDir, "app.json") }

func (a *App) loadPrefs() {
	p := AppPrefs{CloseToTray: true, TailnetAssist: true}
	if b, err := os.ReadFile(a.prefsPath()); err == nil {
		_ = json.Unmarshal(b, &p)
	}
	a.mu.Lock()
	a.prefs = p
	a.savePrefsLocked()
	a.mu.Unlock()
}

func (a *App) savePrefsLocked() {
	if b, err := json.MarshalIndent(a.prefs, "", "  "); err == nil {
		_ = os.WriteFile(a.prefsPath(), b, 0o644)
	}
}

func (a *App) snapshotPrefs() AppPrefs {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.prefs
}

func (a *App) GetPrefs() AppPrefs { return a.snapshotPrefs() }

func (a *App) SavePrefs(p AppPrefs) Result {
	old := a.snapshotPrefs()
	if old.AutoLaunch != p.AutoLaunch {
		if err := setAutostartTask(a.dataDir, p.AutoLaunch); err != nil {
			return failRes(err.Error())
		}
	}
	a.mu.Lock()
	a.prefs = p
	a.savePrefsLocked()
	a.mu.Unlock()
	if old.TailnetAssist != p.TailnetAssist {
		go a.clash.syncTailnet()
	}
	return okRes("已保存")
}

func (a *App) GetInfo() AppInfo {
	return AppInfo{Version: AppVersion, DataDir: a.dataDir, ExeDir: a.exeDir, Admin: isAdmin(), AutostartLaunch: a.autostartLaunch}
}

// ---------- 组网智能适配所需信息 ----------

type assistInfo struct {
	On         bool
	Suffix     string // MagicDNS 后缀，如 tail1234.ts.net 或 Headscale 的 base_domain
	ServerHost string // 自建控制服务器的主机名/IP；官方为空
}

func (a *App) tailnetAssist() assistInfo {
	if !a.snapshotPrefs().TailnetAssist {
		return assistInfo{}
	}
	return assistInfo{On: true, Suffix: a.ts.knownSuffix(), ServerHost: a.ts.serverHost()}
}

// ---------- 杂项（前端可调用） ----------

func (a *App) OpenDataDir() { _ = exec.Command("explorer", a.dataDir).Start() }

func (a *App) OpenURL(u string) {
	if (strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://")) && a.ctx != nil {
		wr.BrowserOpenURL(a.ctx, u)
	}
}

func (a *App) CopyText(s string) Result {
	if a.ctx == nil {
		return failRes("窗口尚未就绪")
	}
	return errRes(wr.ClipboardSetText(a.ctx, s))
}

// ---------- 一键场景 ----------

// SetScene 一键切换「代理 / 组网」的组合。两个模块并行操作，互不影响。
//
//	all=全部开启  proxy=仅代理  mesh=仅组网  off=全部关闭
func (a *App) SetScene(name string) Result {
	var wantProxy, wantMesh bool
	switch name {
	case "all":
		wantProxy, wantMesh = true, true
	case "proxy":
		wantProxy = true
	case "mesh":
		wantMesh = true
	case "off":
	default:
		return failRes("未知的场景")
	}
	var r1, r2 Result
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); r1 = a.clash.setRunning(wantProxy) }()
	go func() { defer wg.Done(); r2 = a.ts.setConnected(wantMesh) }()
	wg.Wait()

	switch {
	case !r1.OK && !r2.OK:
		return failRes("代理：" + r1.Msg + "\n组网：" + r2.Msg)
	case !r1.OK:
		return failRes("代理：" + r1.Msg)
	case !r2.OK:
		return failRes("组网：" + r2.Msg)
	}
	msgs := map[string]string{"all": "汪！代理和组网都已开启", "proxy": "已切换为仅代理", "mesh": "已切换为仅组网", "off": "已全部关闭，小狗去睡觉了"}
	return okRes(msgs[name])
}

func humanDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%d ms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1f s", d.Seconds())
}

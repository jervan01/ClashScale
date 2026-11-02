package main

import (
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// =====================================================================
//  管理员权限
// =====================================================================

func isAdmin() bool { return windows.GetCurrentProcessToken().IsElevated() }

// =====================================================================
//  系统代理（直接读写注册表，不起子进程，关机时也来得及）
// =====================================================================

const inetSubKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// proxyOverrideList 生成「不走代理」列表。
// 注意 Tailscale 的 CGNAT 网段是 100.64.0.0/10，即 100.64.* ~ 100.127.*，必须逐段列出。
func proxyOverrideList(extra []string) string {
	items := []string{"localhost", "127.*", "10.*"}
	for i := 16; i <= 31; i++ {
		items = append(items, fmt.Sprintf("172.%d.*", i))
	}
	items = append(items, "192.168.*", "169.254.*")
	for i := 64; i <= 127; i++ {
		items = append(items, fmt.Sprintf("100.%d.*", i))
	}
	items = append(items, "*.local")
	items = append(items, extra...)
	items = append(items, "<local>")
	return strings.Join(items, ";")
}

// 通知系统与浏览器代理设置已变化，否则需要重启浏览器才生效。
func refreshInternetSettings() {
	proc := syscall.NewLazyDLL("wininet.dll").NewProc("InternetSetOptionW")
	proc.Call(0, 39, 0, 0) // INTERNET_OPTION_SETTINGS_CHANGED
	proc.Call(0, 37, 0, 0) // INTERNET_OPTION_REFRESH
}

func setSystemProxy(port int, bypassExtra []string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, inetSubKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.SetStringValue("ProxyServer", fmt.Sprintf("127.0.0.1:%d", port)); err != nil {
		return err
	}
	if err := k.SetStringValue("ProxyOverride", proxyOverrideList(bypassExtra)); err != nil {
		return err
	}
	if err := k.SetDWordValue("ProxyEnable", 1); err != nil {
		return err
	}
	refreshInternetSettings()
	return nil
}

func clearSystemProxy() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, inetSubKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	err = k.SetDWordValue("ProxyEnable", 0)
	refreshInternetSettings()
	return err
}

// 当前系统代理是否正指向本程序的端口。
func systemProxyIsOurs(port int) bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, inetSubKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	en, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil || en != 1 {
		return false
	}
	srv, _, err := k.GetStringValue("ProxyServer")
	return err == nil && srv == fmt.Sprintf("127.0.0.1:%d", port)
}

// =====================================================================
//  开机自启：任务计划程序（登录触发 + 最高权限）
//  既不会每次开机弹 UAC，也不会因为电池/超时被系统杀掉。
//  （HKCU\Run 无法启动需要管理员权限的程序，所以不用它。）
// =====================================================================

const taskName = "ClashScale"

func autostartInstalled() bool {
	return hidden(exec.Command("schtasks", "/Query", "/TN", taskName)).Run() == nil
}

func setAutostartTask(dataDir string, enable bool) error {
	if !enable {
		if !autostartInstalled() {
			return nil
		}
		if err := hidden(exec.Command("schtasks", "/Delete", "/TN", taskName, "/F")).Run(); err != nil {
			return errors.New("删除自启任务失败: " + err.Error())
		}
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	user := os.Getenv("USERNAME")
	if d := os.Getenv("USERDOMAIN"); d != "" {
		user = d + `\` + user
	}
	x := strings.NewReplacer(
		"{{USER}}", html.EscapeString(user),
		"{{EXE}}", html.EscapeString(exe),
		"{{DIR}}", html.EscapeString(filepath.Dir(exe)),
	).Replace(taskXML)

	// schtasks 需要 UTF-16 LE + BOM
	u := utf16.Encode([]rune(x))
	buf := []byte{0xFF, 0xFE}
	for _, c := range u {
		buf = append(buf, byte(c), byte(c>>8))
	}
	p := filepath.Join(dataDir, "autostart.xml")
	if err := os.WriteFile(p, buf, 0o644); err != nil {
		return err
	}
	defer os.Remove(p)
	if out, err := hidden(exec.Command("schtasks", "/Create", "/TN", taskName, "/XML", p, "/F")).CombinedOutput(); err != nil {
		return fmt.Errorf("创建自启任务失败（请确认以管理员身份运行）: %v %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

const taskXML = `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>{{USER}}</UserId>
      <Delay>PT5S</Delay>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>{{USER}}</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>HighestAvailable</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Enabled>true</Enabled>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>{{EXE}}</Command>
      <Arguments>--autostart</Arguments>
      <WorkingDirectory>{{DIR}}</WorkingDirectory>
    </Exec>
  </Actions>
</Task>
`

// =====================================================================
//  Job Object：本程序无论怎么退出（包括崩溃、被结束任务），
//  由它启动的 mihomo / tailscaled 都会被系统一并回收，
//  不会留下占着端口的孤儿进程。
// =====================================================================

var (
	jobOnce   sync.Once
	jobHandle windows.Handle
)

func initJob() {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(h)
		return
	}
	jobHandle = h
}

// bindToJob 把已启动的子进程放进 Job。失败时静默忽略（只是少一层保险）。
func bindToJob(cmd *exec.Cmd) {
	jobOnce.Do(initJob)
	if jobHandle == 0 || cmd == nil || cmd.Process == nil {
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	_ = windows.AssignProcessToJobObject(jobHandle, h)
}

// =====================================================================
//  关机 / 注销钩子：收到 WM_ENDSESSION 时立刻清掉系统代理，避免下次开机无网。
// =====================================================================

var (
	user32                        = syscall.NewLazyDLL("user32.dll")
	kernel32                      = syscall.NewLazyDLL("kernel32.dll")
	pRegisterClassEx              = user32.NewProc("RegisterClassExW")
	pCreateWindowEx               = user32.NewProc("CreateWindowExW")
	pDefWindowProc                = user32.NewProc("DefWindowProcW")
	pGetMessage                   = user32.NewProc("GetMessageW")
	pTranslateMessage             = user32.NewProc("TranslateMessage")
	pDispatchMessage              = user32.NewProc("DispatchMessageW")
	pGetModuleHandle              = kernel32.NewProc("GetModuleHandleW")
	pSetProcessShutdownParameters = kernel32.NewProc("SetProcessShutdownParameters")
)

const wmEndSession = 0x0016

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type winPoint struct{ X, Y int32 }

type winMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      winPoint
}

// 创建一个不可见的顶层窗口，专门接收 WM_ENDSESSION（关机/注销）。
// 注意不能用 message-only 窗口，它收不到系统广播的会话结束消息。
func installShutdownHook(fn func()) {
	pSetProcessShutdownParameters.Call(0x3FF, 0)

	go func() {
		runtime.LockOSThread()

		proc := syscall.NewCallback(func(hwnd, m, wp, lp uintptr) uintptr {
			if uint32(m) == wmEndSession && wp != 0 {
				fn()
				return 0
			}
			r, _, _ := pDefWindowProc.Call(hwnd, m, wp, lp)
			return r
		})

		className, _ := syscall.UTF16PtrFromString("ClashScaleShutdownWatcher")
		hInst, _, _ := pGetModuleHandle.Call(0)
		wc := wndClassEx{WndProc: proc, Instance: hInst, ClassName: className}
		wc.Size = uint32(unsafe.Sizeof(wc))
		pRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc)))

		hwnd, _, _ := pCreateWindowEx.Call(
			0, uintptr(unsafe.Pointer(className)), 0, 0,
			0, 0, 0, 0, 0, 0, hInst, 0)
		if hwnd == 0 {
			return
		}

		var m winMsg
		for {
			r, _, _ := pGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if int32(r) <= 0 {
				return
			}
			pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			pDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
		}
	}()
}

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

const AppVersion = "1.0.0"

// Result 是所有「动作类」接口的统一返回，前端据此弹出提示。
type Result struct {
	OK  bool   `json:"ok"`
	Msg string `json:"msg"`
}

func okRes(msg string) Result   { return Result{OK: true, Msg: msg} }
func failRes(msg string) Result { return Result{OK: false, Msg: msg} }
func errRes(err error) Result {
	if err == nil {
		return okRes("")
	}
	return failRes(err.Error())
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// hidden 让子进程不弹黑色控制台窗口。
func hidden(cmd *exec.Cmd) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	return cmd
}

// hiddenRaw 同上，但使用原样命令行（msiexec 的 PROPERTY="带空格路径" 需要）。
func hiddenRaw(cmd *exec.Cmd, cmdline string) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000, CmdLine: cmdline}
	return cmd
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// portBusy 检查本机端口是否已被占用。
func portBusy(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return true
	}
	_ = ln.Close()
	return false
}

func writeAtomic(dest string, r io.Reader, perm os.FileMode) error {
	tmp := dest + ".new"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()
	_ = os.Remove(dest)
	return os.Rename(tmp, dest)
}

// ---------- 按行收集子进程输出 ----------

type lineWriter struct {
	mu  sync.Mutex
	buf []byte
	fn  func(string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(w.buf[:i]), "\r")
		w.buf = w.buf[i+1:]
		if line != "" {
			w.fn(line)
		}
	}
	return len(p), nil
}

func (w *lineWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		w.fn(strings.TrimRight(string(w.buf), "\r\n"))
		w.buf = nil
	}
}

// ---------- 下载 ----------

type progressFn func(label, stage string, percent float64, msg string)

// plainClient 不读取环境代理；proxyPort>0 时走本机 Clash 的混合端口。
func plainClient(proxyPort int, timeout time.Duration) *http.Client {
	tr := &http.Transport{
		ResponseHeaderTimeout: 20 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		DisableKeepAlives:     true,
	}
	if proxyPort > 0 {
		if p, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", proxyPort)); err == nil {
			tr.Proxy = http.ProxyURL(p)
		}
	}
	return &http.Client{Transport: tr, Timeout: timeout}
}

type progressWriter struct {
	total, done int64
	last        time.Time
	tick        func(done, total int64)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if time.Since(p.last) > 120*time.Millisecond {
		p.last = time.Now()
		p.tick(p.done, p.total)
	}
	return len(b), nil
}

// downloadFile 下载到 dest（先写 .part，完成后原子改名），并回报进度。
func downloadFile(client *http.Client, label, rawURL, dest string, minSize int64, prog progressFn) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ClashScale/"+AppVersion)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	pw := &progressWriter{total: resp.ContentLength, tick: func(done, total int64) {
		pct := 0.0
		if total > 0 {
			pct = float64(done) / float64(total) * 100
		}
		prog(label, "下载中", pct, fmt.Sprintf("%.1f MB", float64(done)/1048576))
	}}
	_, err = io.Copy(io.MultiWriter(f, pw), resp.Body)
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if fi, e := os.Stat(tmp); e != nil || fi.Size() < minSize {
		os.Remove(tmp)
		return fmt.Errorf("文件异常（大小不足）")
	}
	_ = os.Remove(dest)
	return os.Rename(tmp, dest)
}

func goArch() string { return runtime.GOARCH }

func httpGetLimited(client *http.Client, u string, limit int64) ([]byte, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ClashScale/"+AppVersion)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return b, nil
}

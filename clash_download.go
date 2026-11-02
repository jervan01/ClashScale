package main

import (
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	coreAPI             = "https://api.github.com/repos/MetaCubeX/mihomo/releases/latest"
	coreReleaseBase     = "https://github.com/MetaCubeX/mihomo/releases/download/"
	fallbackCoreVersion = "v1.18.10" // GitHub API 不可达时使用
	geoBase             = "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/"
)

var geoFiles = []string{"geoip.metadb", "geosite.dat"}

var fallbackMirrors = []string{"https://ghfast.top/", "https://gh-proxy.com/"}

func (a *Clash) emitProgress(name, stage string, percent float64, msg string) {
	a.emit("download:progress", map[string]interface{}{"name": name, "stage": stage, "percent": percent, "msg": msg})
}

func (a *Clash) beginDownload() bool {
	a.dlMu.Lock()
	if a.downloading {
		a.dlMu.Unlock()
		return false
	}
	a.downloading = true
	a.dlMu.Unlock()
	a.emitState()
	return true
}

func (a *Clash) endDownload() {
	a.dlMu.Lock()
	a.downloading = false
	a.dlMu.Unlock()
	a.emitState()
}

// 候选地址：用户配置的加速前缀 → 直连 → 内置备用镜像
func (a *Clash) mirrorCandidates(raw string) []string {
	var list []string
	add := func(s string) {
		for _, x := range list {
			if x == s {
				return
			}
		}
		list = append(list, s)
	}
	if m := strings.TrimSpace(a.snapshot().Mirror); m != "" {
		if !strings.HasSuffix(m, "/") {
			m += "/"
		}
		add(m + raw)
	}
	add(raw)
	for _, m := range fallbackMirrors {
		add(m + raw)
	}
	return list
}

func (a *Clash) fetchBytes(raw string) ([]byte, error) {
	var lastErr error
	for _, u := range a.mirrorCandidates(raw) {
		for _, viaProxy := range a.proxyModes() {
			b, err := httpGetLimited(a.httpClient(viaProxy, 10*time.Second), u, 8<<20)
			if err == nil {
				return b, nil
			}
			lastErr = err
		}
	}
	return nil, lastErr
}

func (a *Clash) fetchToFile(label, raw, dest string) error {
	var lastErr error
	for _, u := range a.mirrorCandidates(raw) {
		for _, viaProxy := range a.proxyModes() {
			a.emitProgress(label, "下载中", 0, "连接中…")
			err := downloadFile(a.httpClient(viaProxy, 0), label, u, dest, 1024, a.emitProgress)
			if err == nil {
				return nil
			}
			lastErr = err
			a.pushLog(fmt.Sprintf("[app] 下载失败 %s: %v", u, err))
		}
	}
	return fmt.Errorf("所有下载源都失败了: %v", lastErr)
}

// ---------- 内核 ----------

type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func (a *Clash) resolveCoreAsset() (assetURL, version string) {
	arch := "amd64"
	if goArch() == "arm64" {
		arch = "arm64"
	}
	re := regexp.MustCompile(fmt.Sprintf(`^mihomo-windows-%s-v\d+\.\d+\.\d+\.zip$`, arch))

	if body, err := a.fetchBytes(coreAPI); err == nil {
		var rel ghRelease
		if json.Unmarshal(body, &rel) == nil {
			for _, as := range rel.Assets {
				if re.MatchString(as.Name) {
					return as.URL, rel.TagName
				}
			}
		}
	}
	name := fmt.Sprintf("mihomo-windows-%s-%s.zip", arch, fallbackCoreVersion)
	return coreReleaseBase + fallbackCoreVersion + "/" + name, fallbackCoreVersion
}

func extractZipExe(archive, dest string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("不是有效的 zip: %w", err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !strings.HasSuffix(strings.ToLower(f.Name), ".exe") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		return writeAtomic(dest, rc, 0o755)
	}
	return fmt.Errorf("压缩包里没有找到 exe")
}

func extractGz(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("不是有效的 gz: %w", err)
	}
	defer gr.Close()
	return writeAtomic(dest, gr, 0o755)
}

func (a *Clash) installCore() Result {
	assetURL, version := a.resolveCoreAsset()
	a.pushLog("[app] 内核版本 " + version + " ← " + assetURL)

	archive := filepath.Join(a.dataDir, "core-download.tmp")
	if err := a.fetchToFile("内核", assetURL, archive); err != nil {
		a.emitProgress("内核", "失败", 0, err.Error())
		return failRes("内核下载失败: " + err.Error())
	}
	defer os.Remove(archive)

	a.emitProgress("内核", "解压中", 100, "")
	wasRunning := a.isRunning()
	if wasRunning {
		a.StopCore() // Windows 无法覆盖正在运行的 exe
	}
	var err error
	if strings.HasSuffix(assetURL, ".gz") {
		err = extractGz(archive, a.corePath())
	} else {
		err = extractZipExe(archive, a.corePath())
	}
	if err != nil {
		a.emitProgress("内核", "失败", 0, err.Error())
		return failRes("解压失败: " + err.Error())
	}
	a.detectVersion()
	a.emitProgress("内核", "完成", 100, version)
	if wasRunning {
		a.StartCore()
	}
	return okRes("内核 " + version + " 已安装")
}

func (a *Clash) installGeo() Result {
	for _, name := range geoFiles {
		if err := a.fetchToFile(name, geoBase+name, filepath.Join(a.dataDir, name)); err != nil {
			a.emitProgress(name, "失败", 0, err.Error())
			return failRes(name + " 下载失败: " + err.Error())
		}
		a.emitProgress(name, "完成", 100, "")
	}
	if a.isRunning() {
		return a.applyAndReload()
	}
	_ = a.writeConfig()
	return okRes("规则库已更新")
}

// ---------- 前端入口 ----------

func (a *Clash) DownloadCore() Result {
	if !a.beginDownload() {
		return failRes("已有下载任务在进行")
	}
	defer a.endDownload()
	return a.installCore()
}

func (a *Clash) DownloadGeo() Result {
	if !a.beginDownload() {
		return failRes("已有下载任务在进行")
	}
	defer a.endDownload()
	return a.installGeo()
}

// DownloadAll：首次安装用，内核 + 规则库一次搞定。
func (a *Clash) DownloadAll() Result {
	if !a.beginDownload() {
		return failRes("已有下载任务在进行")
	}
	defer a.endDownload()
	if !a.coreInstalled() {
		if r := a.installCore(); !r.OK {
			return r
		}
	}
	if r := a.installGeo(); !r.OK {
		return r
	}
	return okRes("内核与规则库已就绪")
}

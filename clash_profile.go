package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	wr "github.com/wailsapp/wails/v2/pkg/runtime"
	"gopkg.in/yaml.v3"
)

// httpClient 返回一个不读取环境代理的客户端；viaProxy 为 true 时走本机内核。
func (a *Clash) httpClient(viaProxy bool, timeout time.Duration) *http.Client {
	if viaProxy {
		return plainClient(a.snapshot().MixedPort, timeout)
	}
	return plainClient(0, timeout)
}

// proxyModes：内核没运行只能直连；运行时先直连，失败再借道本机代理。
func (a *Clash) proxyModes() []bool {
	if a.isRunning() {
		return []bool{false, true}
	}
	return []bool{false}
}

type subInfo struct{ Upload, Download, Total, Expire int64 }

// 解析 Subscription-Userinfo: upload=1; download=2; total=3; expire=4
func parseUserinfo(h string) subInfo {
	var si subInfo
	for _, part := range strings.Split(h, ";") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		n, err := strconv.ParseFloat(strings.TrimSpace(kv[1]), 64)
		if err != nil {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(kv[0])) {
		case "upload":
			si.Upload = int64(n)
		case "download":
			si.Download = int64(n)
		case "total":
			si.Total = int64(n)
		case "expire":
			si.Expire = int64(n)
		}
	}
	return si
}

func (a *Clash) fetchSubscription(raw string) ([]byte, subInfo, error) {
	var lastErr error
	for _, viaProxy := range a.proxyModes() {
		req, err := http.NewRequest("GET", raw, nil)
		if err != nil {
			return nil, subInfo{}, err
		}
		// 多数面板按 UA 决定返回格式，伪装成 mihomo 以拿到 Clash YAML
		req.Header.Set("User-Agent", "mihomo/1.19.0 clash.meta ClashScale")
		resp, err := a.httpClient(viaProxy, 30*time.Second).Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != 200 {
			lastErr = fmt.Errorf("服务器返回 %s", resp.Status)
			continue
		}
		return data, parseUserinfo(resp.Header.Get("Subscription-Userinfo")), nil
	}
	return nil, subInfo{}, lastErr
}

func parseSub(data []byte) (nodes, rules int, err error) {
	var m map[string]interface{}
	if e := yaml.Unmarshal(data, &m); e != nil || m == nil {
		return 0, 0, errors.New("订阅内容不是 Clash/Mihomo 的 YAML 格式。请在机场后台选择「Clash」订阅，或在链接末尾加上 &flag=clash 再试")
	}
	ps, _ := m["proxies"].([]interface{})
	if len(ps) == 0 {
		return 0, 0, errors.New("订阅里没有找到任何节点（proxies 为空）")
	}
	rs, _ := m["rules"].([]interface{})
	return len(ps), len(rs), nil
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return "订阅"
}

func (a *Clash) AddProfile(name, rawURL string) Result {
	rawURL = strings.TrimSpace(rawURL)
	name = strings.TrimSpace(name)
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return failRes("订阅链接需要以 http:// 或 https:// 开头")
	}
	data, info, err := a.fetchSubscription(rawURL)
	if err != nil {
		return failRes("下载订阅失败: " + err.Error())
	}
	nodes, rules, err := parseSub(data)
	if err != nil {
		return failRes(err.Error())
	}
	if name == "" {
		name = hostOf(rawURL)
	}
	id := randomHex(4)
	if err := os.WriteFile(a.profilePath(id), data, 0o644); err != nil {
		return failRes("保存订阅失败: " + err.Error())
	}
	p := Profile{ID: id, Name: name, URL: rawURL, UpdatedAt: time.Now().Unix(), NodeCount: nodes, RuleCount: rules,
		Upload: info.Upload, Download: info.Download, Total: info.Total, Expire: info.Expire}
	a.update(func(s *Settings) {
		s.Profiles = append(s.Profiles, p)
		s.ActiveProfile = id // 新导入的订阅直接启用
	})
	if r := a.applyAndReload(); !r.OK {
		return r
	}
	return okRes(fmt.Sprintf("已导入「%s」：%d 个节点，%d 条规则", name, nodes, rules))
}

// ImportProfileFile 从本地 YAML 文件导入配置（没有订阅链接的场景）。
func (a *Clash) ImportProfileFile() Result {
	if a.host.ctx == nil {
		return failRes("窗口尚未就绪")
	}
	path, err := wr.OpenFileDialog(a.host.ctx, wr.OpenDialogOptions{
		Title: "选择 Clash / Mihomo 配置文件",
		Filters: []wr.FileFilter{
			{DisplayName: "YAML 配置 (*.yaml;*.yml;*.txt)", Pattern: "*.yaml;*.yml;*.txt"},
		},
	})
	if err != nil {
		return failRes(err.Error())
	}
	if path == "" {
		return okRes("")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return failRes("读取文件失败: " + err.Error())
	}
	nodes, rules, err := parseSub(data)
	if err != nil {
		return failRes(err.Error())
	}
	name := strings.TrimSuffix(baseName(path), extOf(path))
	id := randomHex(4)
	if err := os.WriteFile(a.profilePath(id), data, 0o644); err != nil {
		return failRes("保存失败: " + err.Error())
	}
	p := Profile{ID: id, Name: name, Local: true, UpdatedAt: time.Now().Unix(), NodeCount: nodes, RuleCount: rules}
	a.update(func(s *Settings) {
		s.Profiles = append(s.Profiles, p)
		s.ActiveProfile = id
	})
	if r := a.applyAndReload(); !r.OK {
		return r
	}
	return okRes(fmt.Sprintf("已导入「%s」：%d 个节点，%d 条规则", name, nodes, rules))
}

func baseName(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func extOf(p string) string {
	b := baseName(p)
	if i := strings.LastIndex(b, "."); i > 0 {
		return b[i:]
	}
	return ""
}

func (a *Clash) RenameProfile(id, name string) Result {
	name = strings.TrimSpace(name)
	if name == "" {
		return failRes("名称不能为空")
	}
	a.update(func(s *Settings) {
		for i := range s.Profiles {
			if s.Profiles[i].ID == id {
				s.Profiles[i].Name = name
			}
		}
	})
	return okRes("已重命名")
}

func (a *Clash) UpdateProfile(id string) Result {
	var target *Profile
	st := a.snapshot()
	for i := range st.Profiles {
		if st.Profiles[i].ID == id {
			target = &st.Profiles[i]
		}
	}
	if target == nil {
		return failRes("找不到这个订阅")
	}
	if target.Local || target.URL == "" {
		return failRes("本地导入的配置没有订阅链接，无法更新")
	}
	data, info, err := a.fetchSubscription(target.URL)
	if err != nil {
		return failRes("更新失败: " + err.Error())
	}
	nodes, rules, err := parseSub(data)
	if err != nil {
		return failRes(err.Error())
	}
	if err := os.WriteFile(a.profilePath(id), data, 0o644); err != nil {
		return failRes("保存订阅失败: " + err.Error())
	}
	a.update(func(s *Settings) {
		for i := range s.Profiles {
			if s.Profiles[i].ID == id {
				p := &s.Profiles[i]
				p.UpdatedAt = time.Now().Unix()
				p.NodeCount = nodes
				p.RuleCount = rules
				if info.Total > 0 || info.Upload > 0 || info.Download > 0 {
					p.Upload, p.Download, p.Total, p.Expire = info.Upload, info.Download, info.Total, info.Expire
				}
			}
		}
	})
	if st.ActiveProfile == id {
		if r := a.applyAndReload(); !r.OK {
			return r
		}
	}
	return okRes(fmt.Sprintf("已更新：%d 个节点，%d 条规则", nodes, rules))
}

func (a *Clash) DeleteProfile(id string) Result {
	wasActive := a.snapshot().ActiveProfile == id
	_ = os.Remove(a.profilePath(id))
	a.update(func(s *Settings) {
		kept := make([]Profile, 0, len(s.Profiles))
		for _, p := range s.Profiles {
			if p.ID != id {
				kept = append(kept, p)
			}
		}
		s.Profiles = kept
		if wasActive {
			s.ActiveProfile = ""
		}
	})
	if wasActive {
		if r := a.applyAndReload(); !r.OK {
			return r
		}
	}
	a.emitState()
	return okRes("已删除")
}

// ActivateProfile：id 为空表示使用内置默认配置（无订阅）。
func (a *Clash) ActivateProfile(id string) Result {
	if id != "" {
		found := false
		for _, p := range a.snapshot().Profiles {
			if p.ID == id {
				found = true
			}
		}
		if !found {
			return failRes("找不到这个订阅")
		}
	}
	a.update(func(s *Settings) { s.ActiveProfile = id })
	r := a.applyAndReload()
	a.emitState()
	return r
}

// ---------- 订阅自动更新 ----------

func (a *Clash) autoUpdateLoop() {
	time.Sleep(45 * time.Second) // 等启动流程（内核、系统代理）先稳定下来
	for {
		a.autoUpdateOnce()
		time.Sleep(20 * time.Minute)
	}
}

func (a *Clash) autoUpdateOnce() {
	st := a.snapshot()
	if st.AutoUpdateHours <= 0 {
		return
	}
	limit := int64(st.AutoUpdateHours) * 3600
	now := time.Now().Unix()
	for _, p := range st.Profiles {
		if p.Local || p.URL == "" || now-p.UpdatedAt < limit {
			continue
		}
		r := a.UpdateProfile(p.ID)
		if r.OK {
			a.pushLog(fmt.Sprintf("[app] 订阅「%s」已自动更新", p.Name))
		} else {
			a.pushLog(fmt.Sprintf("[app] 订阅「%s」自动更新失败: %s", p.Name, r.Msg))
		}
		a.emit("clash:profiles")
	}
}

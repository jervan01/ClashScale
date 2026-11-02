package main

import (
	"fmt"
	"net"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

func namesOf(list interface{}) []string {
	var out []string
	arr, _ := list.([]interface{})
	for _, it := range arr {
		if m, ok := it.(map[string]interface{}); ok {
			if n, ok := m["name"].(string); ok && n != "" {
				out = append(out, n)
			}
		}
	}
	return out
}

// tailnetManagedRules 是「组网智能适配」注入的规则，永远排在最前，用户不可编辑：
//   - Tailscale 的 CGNAT 网段 / IPv6 ULA 与 MagicDNS 域名必须直连，否则 100.x 设备会被代理吞掉
//   - 自建控制服务器（Headscale）本身必须直连，否则「用代理连组网，组网又依赖代理」会死循环
func tailnetManagedRules(as assistInfo) []string {
	if !as.On {
		return nil
	}
	r := []string{
		"IP-CIDR,100.64.0.0/10,DIRECT,no-resolve",
		"IP-CIDR6,fd7a:115c:a1e0::/48,DIRECT,no-resolve",
		"DOMAIN-SUFFIX,ts.net,DIRECT",
	}
	if as.Suffix != "" && as.Suffix != "ts.net" && !strings.HasSuffix(as.Suffix, ".ts.net") {
		r = append(r, "DOMAIN-SUFFIX,"+as.Suffix+",DIRECT")
	}
	if as.ServerHost != "" {
		if ip := net.ParseIP(as.ServerHost); ip != nil {
			if ip.To4() != nil {
				r = append(r, "IP-CIDR,"+as.ServerHost+"/32,DIRECT,no-resolve")
			} else {
				r = append(r, "IP-CIDR6,"+as.ServerHost+"/128,DIRECT,no-resolve")
			}
		} else {
			r = append(r, "DOMAIN,"+as.ServerHost+",DIRECT")
		}
	}
	return r
}

// patchTailnetDNS：mihomo 自己解析「直连」域名时用的是它自己的 DNS，解析不了 MagicDNS 名字
// （例如 mypc.tailnet.ts.net）。给这些域名指定 Tailscale 内置的 100.100.100.100 去解析。
// 只在订阅自带的 dns 已启用时才需要；没启用 dns 时 mihomo 走系统解析，天然能解析。
func patchTailnetDNS(cfg map[string]interface{}, as assistInfo) {
	if !as.On {
		return
	}
	dns, _ := cfg["dns"].(map[string]interface{})
	if dns == nil {
		return
	}
	if en, _ := dns["enable"].(bool); !en {
		return
	}
	domains := []string{"ts.net"}
	if as.Suffix != "" && as.Suffix != "ts.net" && !strings.HasSuffix(as.Suffix, ".ts.net") {
		domains = append(domains, as.Suffix)
	}
	pol, _ := dns["nameserver-policy"].(map[string]interface{})
	if pol == nil {
		pol = map[string]interface{}{}
	}
	for _, d := range domains {
		pol["+."+d] = []string{"100.100.100.100"}
	}
	dns["nameserver-policy"] = pol

	// fake-ip 模式下，这些域名也不应该被分配假 IP
	if ff, ok := dns["fake-ip-filter"].([]interface{}); ok {
		for _, d := range domains {
			ff = append(ff, "+."+d)
		}
		dns["fake-ip-filter"] = ff
	}
	cfg["dns"] = dns
}

// writeConfig 根据设置生成最终的 config.yaml：
//   - 基底：当前订阅（没有则为空配置）
//   - 强制覆盖：端口、模式、控制接口等由本程序管理的字段
//   - 规则：组网适配规则 > 自定义规则 > （订阅规则 或 内置基础规则）
func (a *Clash) writeConfig() error {
	st := a.snapshot()
	as := a.host.tailnetAssist()

	cfg := map[string]interface{}{}
	hasSub := false
	if st.ActiveProfile != "" {
		if b, err := os.ReadFile(a.profilePath(st.ActiveProfile)); err == nil {
			var m map[string]interface{}
			if yaml.Unmarshal(b, &m) == nil && m != nil {
				cfg, hasSub = m, true
			}
		}
	}

	// 程序托管字段
	for _, k := range []string{"port", "socks-port", "redir-port", "tproxy-port", "tun", "external-ui", "external-controller-tls"} {
		delete(cfg, k)
	}
	cfg["mixed-port"] = st.MixedPort
	cfg["allow-lan"] = st.AllowLAN
	cfg["mode"] = st.Mode
	cfg["log-level"] = "info"
	cfg["external-controller"] = fmt.Sprintf("127.0.0.1:%d", st.ControllerPort)
	cfg["secret"] = st.Secret
	cfg["profile"] = map[string]interface{}{"store-selected": true} // 记住每个分组选中的节点

	patchTailnetDNS(cfg, as)

	proxyNames := namesOf(cfg["proxies"])
	subGroups := namesOf(cfg["proxy-groups"])
	useSub := hasSub && (st.UseSubRules || len(proxyNames) == 0) && len(subGroups) > 0

	// 「代理」策略实际指向哪个分组
	mainGroup := "DIRECT"
	switch {
	case useSub:
		mainGroup = subGroups[0]
	case hasSub && len(proxyNames) > 0:
		mainGroup = "节点选择"
		auto := "自动选择"
		cfg["proxy-groups"] = []interface{}{
			map[string]interface{}{
				"name":    mainGroup,
				"type":    "select",
				"proxies": append([]string{auto, "DIRECT"}, proxyNames...),
			},
			map[string]interface{}{
				"name":      auto,
				"type":      "url-test",
				"proxies":   proxyNames,
				"url":       "https://www.gstatic.com/generate_204",
				"interval":  300,
				"tolerance": 50,
			},
		}
	default:
		delete(cfg, "proxy-groups")
	}

	// 规则：组网适配规则最前，其次自定义规则
	rules := append([]string{}, tailnetManagedRules(as)...)
	for _, r := range st.Rules {
		if !r.Enabled {
			continue
		}
		target := r.Target
		if target == "PROXY" {
			target = mainGroup
		}
		line := fmt.Sprintf("%s,%s,%s", r.Type, r.Payload, target)
		if r.Type == "IP-CIDR" || r.Type == "GEOIP" {
			line += ",no-resolve"
		}
		rules = append(rules, line)
	}

	if useSub {
		if arr, ok := cfg["rules"].([]interface{}); ok {
			for _, r := range arr {
				rules = append(rules, fmt.Sprint(r))
			}
		}
		if len(rules) == 0 || !strings.HasPrefix(rules[len(rules)-1], "MATCH") {
			rules = append(rules, "MATCH,"+mainGroup)
		}
	} else {
		rules = append(rules,
			"DOMAIN-SUFFIX,local,DIRECT",
			"IP-CIDR,127.0.0.0/8,DIRECT,no-resolve",
			"IP-CIDR,10.0.0.0/8,DIRECT,no-resolve",
			"IP-CIDR,172.16.0.0/12,DIRECT,no-resolve",
			"IP-CIDR,192.168.0.0/16,DIRECT,no-resolve",
			"IP-CIDR,100.64.0.0/10,DIRECT,no-resolve", // Tailscale / CGNAT
		)
		if a.fileExists("geosite.dat") {
			rules = append(rules, "GEOSITE,cn,DIRECT")
		}
		if a.fileExists("geoip.metadb") || a.fileExists("Country.mmdb") {
			rules = append(rules, "GEOIP,CN,DIRECT,no-resolve")
		}
		rules = append(rules, "MATCH,"+mainGroup)
	}
	cfg["rules"] = rules

	out, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(a.configPath(), out, 0o644)
}

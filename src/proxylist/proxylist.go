// Package proxylist 代理域名白名单:仅列表中的域名后缀走隧道,其余直连。
// 桌面端 PAC 与移动端 fake-ip 引擎共用同一匹配语义。
package proxylist

import "strings"

// Defaults 默认走隧道的域名后缀(手机端可在配置中追加)
func Defaults() []string {
	return []string{
		"github.com",
		"google.com",
		"googleapis.com",
		"gstatic.com",
		"youtube.com",
		"huggingface.co",
		"android.com",
		"dl.google.com", // 已被 google.com 后缀覆盖,保留明示
	}
}

// Match 判断域名是否命中白名单:精确相等或任一后缀项匹配(点边界)。
// 空列表 = 全不放行。
func Match(domain string, list []string) bool {
	d := strings.ToLower(strings.TrimSuffix(strings.ToLower(domain), "."))
	if d == "" {
		return false
	}
	for _, e := range list {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if d == e || strings.HasSuffix(d, "."+e) {
			return true
		}
	}
	return false
}

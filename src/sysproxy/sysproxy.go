package sysproxy

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
)

// PACPort 本地 PAC 文件服务端口（三平台共用），可用 -pac-port 修改（默认 21879）
var PACPort = "21879"

// 本地 PAC 文件服务（三平台共用）: 仅白名单域名走代理，其余直连。
// 幂等：同地址+同清单重复调用直接返回（调用方均持有 webManager.mu，无需额外加锁）
var pacServing string
var pacLn net.Listener

// pacVersion PAC 内容版本号：清单每次变化自增，拼进 PAC URL 迫使浏览器立即重新拉取（绕过 PAC 缓存）
var pacVersion int

// PACURL 当前 PAC 自动配置地址（带版本参数，写进系统代理设置）
func PACURL() string {
	return fmt.Sprintf("http://127.0.0.1:%s/proxy.pac?v=%d", PACPort, pacVersion)
}

func startPACServer(proxyAddr string, domains []string, bypassHosts []string) {
	key := proxyAddr + "|" + strings.Join(bypassHosts, ";") + "|" + strings.Join(domains, ",")
	if pacServing == key {
		return
	}
	if pacLn != nil { // 端口/清单变更：先关旧监听再重建，避免 bind 失败
		pacLn.Close()
		pacLn = nil
		pacServing = ""
	}
	pacVersion++
	pac := buildPAC(proxyAddr, domains, bypassHosts)
	mux := http.NewServeMux()
	mux.HandleFunc("/proxy.pac", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ns-proxy-autoconfig")
		w.Write([]byte(pac))
	})
	ln, err := net.Listen("tcp", "127.0.0.1:"+PACPort)
	if err != nil {
		log.Printf("PAC 服务监听失败(端口 %s 被占用?): %v", PACPort, err)
		return
	}
	pacLn = ln
	pacServing = key
	go http.Serve(ln, mux)
	log.Printf("PAC 服务 http://127.0.0.1:%s/proxy.pac?v=%d（白名单 %d 个后缀）", PACPort, pacVersion, len(domains))
}

// buildPAC 生成 PAC 脚本。注意: PAC 运行在老式 JScript 引擎，不能用 ES6 的 endsWith
func buildPAC(proxyAddr string, domains []string, bypassHosts []string) string {
	var items []string
	for _, b := range bypassHosts { // 服务器各入口地址直连，防止绕地球一圈再连回来
		if b != "" {
			items = append(items, fmt.Sprintf("  if (h == %q) return \"DIRECT\";\n", b))
		}
	}
	for _, s := range domains {
		items = append(items, fmt.Sprintf("  if (sfx(h, %q)) return %q;\n", s, "PROXY "+proxyAddr))
	}
	return `function sfx(h, s) {
  return h == s || (h.length > s.length && h.substr(h.length - s.length - 1) == "." + s);
}
function FindProxyForURL(url, host) {
  var h = host.toLowerCase();
  if (isPlainHostName(h) || shExpMatch(h, "127.*") || shExpMatch(h, "192.168.*") || shExpMatch(h, "10.*")) return "DIRECT";
` + strings.Join(items, "") + `  return "DIRECT";
}`
}

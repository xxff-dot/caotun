//go:build !windows

package sysproxy

import (
	"log"
	"net"
	"os/exec"
	"runtime"
	"strings"
)

// 非 Windows 平台系统代理：
//   - macOS: networksetup（PAC=autoproxyurl，全局=socks 代理）
//   - Linux: GNOME gsettings（PAC=auto 模式，全局=manual 模式 http/https/socks）
//
// 工具缺失（WSL2/无桌面/服务器）时记日志跳过，本地代理端口仍可手动使用。
// 注意: 客户端本地代理无认证，-lhost 0.0.0.0 会向局域网开放。

// HideWindowCmd 非 Windows 平台子进程默认无控制台窗口，空实现
func HideWindowCmd(*exec.Cmd) {}

// ApplyPAC PAC 白名单模式
func ApplyPAC(proxyAddr string, domains []string, ips []string, bypassHosts []string) func() {
	startPACServer(proxyAddr, domains, ips, bypassHosts)
	switch runtime.GOOS {
	case "darwin":
		return applyMac(func(svc string) bool {
			return run("networksetup", "-setautoproxyurl", svc, PACURL()) &&
				run("networksetup", "-setautoproxystate", svc, "on")
		}, getAutoProxy)
	case "linux":
		return applyLinuxPAC(PACURL())
	default:
		log.Printf("当前平台 %s 不支持自动设置系统代理，忽略", runtime.GOOS)
		return func() {}
	}
}

// ApplyAll 全局模式（全部走本地代理）
func ApplyAll(proxyAddr string, bypassHosts []string) func() {
	host, port, err := net.SplitHostPort(proxyAddr)
	if err != nil {
		log.Printf("代理地址解析失败: %v", err)
		return func() {}
	}
	switch runtime.GOOS {
	case "darwin":
		return applyMac(func(svc string) bool {
			return run("networksetup", "-setsocksfirewallproxy", svc, host, port)
		}, getSocksProxy)
	case "linux":
		return applyLinuxAll(host, port)
	default:
		log.Printf("当前平台 %s 不支持自动设置系统代理，忽略", runtime.GOOS)
		return func() {}
	}
}

// ==================== macOS (networksetup) ====================

// applyMac 对每个网络服务做 backup→set→undo 还原。
// getter 返回 (可还原描述, 是否原本已启用)；setter 返回是否成功
func applyMac(set func(svc string) bool, getter func(svc string) (string, bool)) func() {
	type saved struct {
		svc, backup string
		wasOn       bool
	}
	var svcs []saved
	for _, s := range networkServices() {
		backup, wasOn := getter(s)
		svcs = append(svcs, saved{s, backup, wasOn})
		if !set(s) {
			log.Printf("设置 %s 代理失败（可能需要管理员权限），跳过", s)
		}
	}
	return func() {
		for _, s := range svcs {
			restoreMac(s.svc, s.backup, s.wasOn)
		}
	}
}

func networkServices() []string {
	out, err := exec.Command("networksetup", "-listallnetworkservices").Output()
	if err != nil {
		log.Printf("networksetup 不可用: %v", err)
		return nil
	}
	var svcs []string
	for i, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if i == 0 || strings.TrimSpace(ln) == "" { // 首行是说明文字
			continue
		}
		svcs = append(svcs, strings.TrimPrefix(strings.TrimSpace(ln), "*"))
	}
	return svcs
}

// getAutoProxy/getSocksProxy 返回 (还原用参数串, 原本是否启用)
func getAutoProxy(svc string) (string, bool) {
	out, err := exec.Command("networksetup", "-getautoproxyurl", svc).Output()
	if err != nil {
		return "", false
	}
	var enabled bool
	var url string
	for _, ln := range strings.Split(string(out), "\n") {
		f := strings.SplitN(strings.TrimSpace(ln), ": ", 2)
		if len(f) != 2 {
			continue
		}
		switch f[0] {
		case "Enabled":
			enabled = f[1] == "Yes"
		case "URL":
			url = f[1]
			if url == "(null)" {
				url = ""
			}
		}
	}
	return url, enabled
}

func getSocksProxy(svc string) (string, bool) {
	out, err := exec.Command("networksetup", "-getsocksfirewallproxy", svc).Output()
	if err != nil {
		return "", false
	}
	var enabled bool
	var server, port string
	for _, ln := range strings.Split(string(out), "\n") {
		f := SplitN2(strings.TrimSpace(ln))
		switch f[0] {
		case "Enabled":
			enabled = f[1] == "Yes"
		case "Server":
			server = f[1]
		case "Port":
			port = f[1]
		}
	}
	return server + "|" + port, enabled
}

func restoreMac(svc, backup string, wasOn bool) {
	switch {
	case strings.Contains(backup, "|"): // socks: server|port
		p := strings.SplitN(backup, "|", 2)
		if wasOn && p[0] != "" {
			run("networksetup", "-setsocksfirewallproxy", svc, p[0], p[1])
		} else {
			run("networksetup", "-setsocksfirewallproxystate", svc, "off")
		}
	default: // PAC: url（空 = 原本关闭）
		if wasOn && backup != "" {
			run("networksetup", "-setautoproxyurl", svc, backup)
			run("networksetup", "-setautoproxystate", svc, "on")
		} else {
			run("networksetup", "-setautoproxystate", svc, "off")
		}
	}
}

// SplitN2 把 "Key: Value" 拆成 [Key, Value]，无法拆分时返回 ["", ""]
func SplitN2(ln string) [2]string {
	if i := strings.Index(ln, ": "); i >= 0 {
		return [2]string{ln[:i], ln[i+2:]}
	}
	return [2]string{"", ""}
}

// ==================== Linux (GNOME gsettings) ====================

const gschema = "org.gnome.system.proxy"

func haveGsettings() bool {
	if _, err := exec.LookPath("gsettings"); err != nil {
		log.Printf("未检测到 gsettings（GNOME），跳过系统代理；可手动将应用代理指向本地端口")
		return false
	}
	return true
}

// gset/gget 封装 gsettings 读写；get 结果去掉两端引号
func gset(args ...string) bool {
	return run("gsettings", args...)
}

func gget(args ...string) string {
	out, err := exec.Command("gsettings", args...).Output()
	if err != nil {
		return ""
	}
	return strings.Trim(strings.TrimSpace(string(out)), "'")
}

// applyLinuxPAC auto 模式 + autoconfig-url；还原只恢复 mode 与 autoconfig-url
func applyLinuxPAC(pacURL string) func() {
	if !haveGsettings() {
		return func() {}
	}
	oldMode := gget("get", gschema, "mode")
	oldURL := gget("get", gschema, "autoconfig-url")
	if !gset("set", gschema, "autoconfig-url", pacURL) ||
		!gset("set", gschema, "mode", "auto") {
		return func() {}
	}
	log.Printf("GNOME 系统代理已设为 PAC 模式 %s", pacURL)
	return func() {
		gset("set", gschema, "autoconfig-url", oldURL)
		gset("set", gschema, "mode", oldMode)
		log.Printf("GNOME 系统代理已还原（mode=%s）", oldMode)
	}
}

// applyLinuxAll manual 模式 + http/https/socks 指向本地代理；还原恢复 mode
func applyLinuxAll(host, port string) func() {
	if !haveGsettings() {
		return func() {}
	}
	oldMode := gget("get", gschema, "mode")
	if !gset("set", gschema, "mode", "manual") {
		return func() {}
	}
	for _, scheme := range []string{"http", "https", "socks"} {
		gset("set", gschema, scheme+"-host", host)
		gset("set", gschema, scheme+"-port", port)
	}
	log.Printf("GNOME 系统代理已设为全局模式 %s:%s", host, port)
	return func() {
		gset("set", gschema, "mode", oldMode)
		log.Printf("GNOME 系统代理已还原（mode=%s）", oldMode)
	}
}

// run 执行外部命令，失败记日志返回 false
func run(name string, args ...string) bool {
	if err := exec.Command(name, args...).Run(); err != nil {
		log.Printf("%s %v 执行失败: %v", name, args, err)
		return false
	}
	return true
}

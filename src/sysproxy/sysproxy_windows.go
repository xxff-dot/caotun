//go:build windows

package sysproxy

import (
	"encoding/binary"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"
)

// ==================== Windows 系统代理（注册表直写，零依赖） ====================

const (
	inetKeyPath = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`
)

var (
	modadvapi32            = syscall.NewLazyDLL("advapi32.dll")
	procRegOpenKeyExW      = modadvapi32.NewProc("RegOpenKeyExW")
	procRegQueryValueExW   = modadvapi32.NewProc("RegQueryValueExW")
	procRegSetValueExW     = modadvapi32.NewProc("RegSetValueExW")
	procRegDeleteValueW    = modadvapi32.NewProc("RegDeleteValueW")
	procRegCloseKey        = modadvapi32.NewProc("RegCloseKey")
	modwininet             = syscall.NewLazyDLL("wininet.dll")
	procInternetSetOptionW = modwininet.NewProc("InternetSetOptionW")
)

const (
	HKEY_CURRENT_USER   = 0x80000001
	KEY_QUERY_VALUE_SET = 0x0001 | 0x0002 // QUERY_VALUE | SET_VALUE
	REG_SZ              = 1
	REG_DWORD           = 4
	optSettingsChanged  = 39
	optRefresh          = 37
)

// UTF-16 指针 helper（注册表名称/值不含 NUL，错误可忽略）
func utf16p(s string) uintptr {
	p, _ := syscall.UTF16PtrFromString(s)
	return uintptr(unsafe.Pointer(p))
}

func regOpen(path string) (syscall.Handle, error) {
	var h syscall.Handle
	r, _, _ := procRegOpenKeyExW.Call(
		uintptr(HKEY_CURRENT_USER),
		utf16p(path),
		0, uintptr(KEY_QUERY_VALUE_SET), uintptr(unsafe.Pointer(&h)))
	if r != 0 {
		return 0, fmt.Errorf("打开注册表失败: %s", syscall.Errno(r))
	}
	return h, nil
}

func regGet(h syscall.Handle, name string) (typ uint32, val []byte, ok bool) {
	var t uint32
	var size uint32
	r, _, _ := procRegQueryValueExW.Call(
		uintptr(h),
		utf16p(name),
		0, uintptr(unsafe.Pointer(&t)), 0, uintptr(unsafe.Pointer(&size)))
	if r != 0 {
		return 0, nil, false
	}
	buf := make([]byte, size)
	r, _, _ = procRegQueryValueExW.Call(
		uintptr(h),
		utf16p(name),
		0, uintptr(unsafe.Pointer(&t)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if r != 0 {
		return 0, nil, false
	}
	return t, buf, true
}

func regGetString(h syscall.Handle, name string) (string, bool) {
	t, b, ok := regGet(h, name)
	if !ok || t != REG_SZ {
		return "", false
	}
	return syscall.UTF16ToString(bytesToUTF16(b)), true
}

func regGetDword(h syscall.Handle, name string) (uint32, bool) {
	t, b, ok := regGet(h, name)
	if !ok || t != REG_DWORD || len(b) < 4 {
		return 0, false
	}
	return binary.LittleEndian.Uint32(b), true
}

func bytesToUTF16(b []byte) []uint16 {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	return u
}

func regSetString(h syscall.Handle, name, val string) {
	utf16, _ := syscall.UTF16FromString(val)
	b := make([]byte, len(utf16)*2)
	for i, v := range utf16 {
		binary.LittleEndian.PutUint16(b[i*2:], v)
	}
	procRegSetValueExW.Call(
		uintptr(h),
		utf16p(name),
		0, REG_SZ,
		uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)))
}

func regSetDword(h syscall.Handle, name string, val uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], val)
	procRegSetValueExW.Call(
		uintptr(h),
		utf16p(name),
		0, REG_DWORD,
		uintptr(unsafe.Pointer(&b[0])), 4)
}

func regDelValue(h syscall.Handle, name string) {
	procRegDeleteValueW.Call(
		uintptr(h),
		utf16p(name))
}

// 通知 WinINET 配置已变化，立即生效
func refreshProxy() {
	procInternetSetOptionW.Call(0, optSettingsChanged, 0, 0)
	procInternetSetOptionW.Call(0, optRefresh, 0, 0)
}

// 备份用户原有代理设置，退出时精确还原
type proxyBackup struct {
	hadPAC      bool
	pacURL      string
	hadEnable   bool
	enable      uint32
	hadServer   bool
	server      string
	hadOverride bool
	override    string
}

func backupProxy(h syscall.Handle) proxyBackup {
	var b proxyBackup
	if b.pacURL, b.hadPAC = regGetString(h, "AutoConfigURL"); !strings.Contains(b.pacURL, "127.0.0.1:"+PACPort) {
		// 原有 PAC 不是我们设的才需要还原
		if !b.hadPAC {
			b.pacURL = ""
		}
	} else {
		b.hadPAC = false // 原来就是我们的残留，直接清掉
	}
	b.enable, b.hadEnable = regGetDword(h, "ProxyEnable")
	b.server, b.hadServer = regGetString(h, "ProxyServer")
	b.override, b.hadOverride = regGetString(h, "ProxyOverride")
	return b
}

// 局域网/本机地址不走代理（ProxyOverride 语法，<local> 表示不带点的主机名）
const lanBypass = "localhost;127.*;10.*;192.168.*;172.16.*;172.17.*;172.18.*;172.19.*;172.20.*;172.21.*;172.22.*;172.23.*;172.24.*;172.25.*;172.26.*;172.27.*;172.28.*;172.29.*;172.30.*;172.31.*;<local>"

func restoreProxy(h syscall.Handle, b proxyBackup) {
	if b.hadPAC {
		regSetString(h, "AutoConfigURL", b.pacURL)
	} else {
		regDelValue(h, "AutoConfigURL")
	}
	if b.hadEnable {
		regSetDword(h, "ProxyEnable", b.enable)
	} else {
		regDelValue(h, "ProxyEnable")
	}
	if b.hadServer {
		regSetString(h, "ProxyServer", b.server)
	} else {
		regDelValue(h, "ProxyServer")
	}
	if b.hadOverride {
		regSetString(h, "ProxyOverride", b.override)
	} else {
		regDelValue(h, "ProxyOverride")
	}
	refreshProxy()
}

// ApplyPAC PAC 分流模式(白名单): AutoConfigURL 指向本地 PAC 服务，仅清单内域名走代理；服务器各域名/IP 永远直连
func ApplyPAC(proxyAddr string, domains []string, ips []string, bypassHosts []string) func() {
	startPACServer(proxyAddr, domains, ips, bypassHosts)
	h, err := regOpen(inetKeyPath)
	if err != nil {
		log.Fatal(err)
	}
	b := backupProxy(h)
	regSetString(h, "AutoConfigURL", PACURL())
	regSetDword(h, "ProxyEnable", 0)
	override := lanBypass
	if len(bypassHosts) > 0 {
		override = strings.Join(bypassHosts, ";") + ";" + lanBypass
	}
	regSetString(h, "ProxyOverride", override)
	refreshProxy()
	return func() { restoreProxy(h, b); procRegCloseKey.Call(uintptr(h)) }
}

// ApplyAll 全局模式: 所有 HTTP(S) 都走本地代理（服务器自身域名/IP 与局域网除外）
func ApplyAll(proxyAddr string, bypassHosts []string) func() {
	h, err := regOpen(inetKeyPath)
	if err != nil {
		log.Fatal(err)
	}
	b := backupProxy(h)
	regDelValue(h, "AutoConfigURL")
	regSetDword(h, "ProxyEnable", 1)
	regSetString(h, "ProxyServer", proxyAddr)
	override := lanBypass
	if len(bypassHosts) > 0 {
		override = strings.Join(bypassHosts, ";") + ";" + lanBypass
	}
	regSetString(h, "ProxyOverride", override)
	refreshProxy()
	return func() { restoreProxy(h, b); procRegCloseKey.Call(uintptr(h)) }
}

// HideWindowCmd 拉起子进程时隐藏控制台窗口（web 面板 spawn 客户端用）
func HideWindowCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

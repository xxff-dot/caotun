// web 管理面板：管理进程持有客户端子进程与系统代理设置，
// 浏览器打开 http://127.0.0.1:21877 即可启停/切模式/看日志/查服务器流量。
package web

import (
	"bytes"
	"crypto/tls"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"caotun/protocol"
	"caotun/sysproxy"
)

//go:embed web.html
var webHTML []byte

// qrcode.js 手机扫码导入用的二维码生成库（MIT, © Kazuhiko Arase, 内嵌零依赖）
//
//go:embed qrcode.js
var qrcodeJS []byte

// favicon.png 网页面板图标
//
//go:embed favicon.png
var faviconPNG []byte

// Port 面板端口：Origin/Host 校验与默认监听共用
const Port = "21877"

// ==================== 配置 ====================

type webConfig struct {
	DirectAddr   string   `json:"directAddr"`   // 直连地址（灰云域名/IP:443，原生 TLS）
	CdAddr       string   `json:"cdAddr"`       // CDN 地址（橙云域名:8443，WebSocket，管理 API 也走这个端口）
	LocalPort    string   `json:"localPort"`    // 本地代理端口
	Mode         string   `json:"mode"`         // pac / all（off 只存在于运行时状态）
	MaxGB        float64  `json:"maxGB"`        // 服务器端流量配额（仅用于面板显示）
	QuotaDays    int      `json:"quotaDays"`    // 配额周期天数（面板显示用，服务端 -quota-days 为准）
	AuthPassword string   `json:"authPassword"` // 认证密码（必填，与服务端一致；也是面板调服务端管理 API 的凭证）
	Domain       string   `json:"domain"`       // 证书主域（PAC 绕行用；服务端证书域以其启动参数为准）
	WsMode       bool     `json:"wsMode"`       // true = 走 CDN 地址(WebSocket)；false = 走直连地址
	ProxyDomains []string `json:"proxyDomains"` // PAC 白名单：仅这些后缀走隧道，其余直连
	ProxyIps     []string `json:"proxyIps"`     // PAC 额外走隧道的 IP（精确或 * 通配；内网段除外）
}

// DefaultProxyDomains 默认走隧道的域名后缀（面板"配置"卡可增删），覆盖常用国外站点
func DefaultProxyDomains() []string {
	return []string{
		// Google / YouTube
		"google.com", "googleapis.com", "gstatic.com", "googleusercontent.com", "ggpht.com",
		"googlevideo.com", "youtube.com", "ytimg.com", "youtube-nocookie.com",
		// GitHub / 代码托管
		"github.com", "githubusercontent.com", "githubassets.com",
		"github.io", "github.dev", "githubstatus.com", "ghcr.io", "git.io",
		"gitlab.com", "bitbucket.org",
		// AI
		"openai.com", "chatgpt.com", "oaistatic.com", "oaiusercontent.com",
		"anthropic.com", "claude.ai", "perplexity.ai", "poe.com",
		"huggingface.co", "hf.co", "kaggle.com",
		// 社交/媒体
		"x.com", "twitter.com", "twimg.com", "t.co",
		"telegram.org", "t.me",
		"facebook.com", "fb.com", "instagram.com", "fbcdn.net", "cdninstagram.com",
		"whatsapp.com", "whatsapp.net",
		"reddit.com", "redd.it", "redditmedia.com", "redditstatic.com",
		"discord.com", "discord.gg", "discordapp.com", "discordapp.net",
		"twitch.tv", "twitchcdn.net",
		"pinterest.com", "pinimg.com", "imgur.com",
		"pixiv.net",
		// 知识/内容
		"wikipedia.org", "wikimedia.org", "archive.org", "medium.com", "substack.com",
		"stackoverflow.com", "stackexchange.com", "sstatic.net",
		"notion.so", "notion.site", "figma.com", "duckduckgo.com",
		// 开发者生态 / 注册表 / CDN
		"npmjs.com", "npmjs.org", "nodejs.org", "pypi.org", "pythonhosted.org",
		"crates.io", "rust-lang.org", "golang.org", "go.dev", "gopkg.in",
		"maven.org", "gradle.org",
		"docker.io", "docker.com", "gcr.io", "quay.io", "k8s.io", "kubernetes.io",
		"cloudflare.com", "jsdelivr.net", "unpkg.com", "cdnjs.com",
		"vercel.com", "vercel.app", "netlify.com", "netlify.app", "pages.dev", "workers.dev",
		"amazonaws.com",
		// 流媒体
		"netflix.com", "nflxvideo.net", "nflxext.com",
		// 通信/其他
		"proton.me", "signal.org",
	}
}

func defaultWebConfig() webConfig {
	return webConfig{
		DirectAddr:   "direct.example.com:443",
		CdAddr:       "cdn.example.com:8443",
		LocalPort:    "21878",
		Mode:         "pac",
		MaxGB:        20,
		QuotaDays:    30,
		Domain:       "example.com",
		ProxyDomains: DefaultProxyDomains(),
	}
}

func loadWebConfig(dir string) webConfig {
	cfg := defaultWebConfig()
	if b, err := os.ReadFile(filepath.Join(dir, "web.json")); err == nil {
		json.Unmarshal(b, &cfg)
	} else {
		saveWebConfig(dir, cfg)
	}
	return cfg
}

func saveWebConfig(dir string, cfg webConfig) {
	b, _ := json.MarshalIndent(cfg, "", "  ")
	os.WriteFile(filepath.Join(dir, "web.json"), b, 0600)
}

// ==================== 管理器 ====================

type webManager struct {
	mu  sync.Mutex
	dir string
	cfg webConfig

	child    *exec.Cmd // 本管理进程拉起的客户端子进程（nil = 没有托管句柄）
	undoFunc func()    // 当前系统代理还原函数（nil = 未接管系统代理）
}

func newWebManager(dir string) *webManager {
	return &webManager{dir: dir, cfg: loadWebConfig(dir)}
}

func (m *webManager) authPath() string  { return filepath.Join(m.dir, "auth") }
func (m *webManager) pidPath() string   { return filepath.Join(m.dir, "client.pid") }
func (m *webManager) proxyAddr() string { return net.JoinHostPort("127.0.0.1", m.cfg.LocalPort) }

// tunnelAddr 按 WsMode 返回本次使用的隧道地址（直连地址 / CDN 地址）
func (m *webManager) tunnelAddr() string {
	if m.cfg.WsMode {
		return m.cfg.CdAddr
	}
	return m.cfg.DirectAddr
}

// activeTunnelAddr 当前生效的隧道地址（面板显示用）
func (m *webManager) activeTunnelAddr() string { return m.tunnelAddr() }

type pidRecord struct {
	PID        int    `json:"pid"`
	TunnelAddr string `json:"tunnelAddr"`
	LocalPort  string `json:"localPort"`
}

func (m *webManager) writePidFile(pid int) {
	b, _ := json.Marshal(pidRecord{PID: pid, TunnelAddr: m.tunnelAddr(), LocalPort: m.cfg.LocalPort})
	os.WriteFile(m.pidPath(), b, 0600)
}

// 端口探测：客户端是否在服务（孤儿或托管都算）
func (m *webManager) clientAlive() bool {
	c, err := net.DialTimeout("tcp", m.proxyAddr(), 300*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// adminCall 调用服务端管理 API（WS 监听端口上的 /_admin/*，X-Auth 头 = 认证密码）。
// 地址优先 直连域名:WS端口（直达 VPS），连不上回退 CDN 地址（VPS IP 被墙时经 CF 仍可达）
func (m *webManager) adminCall(method, path string, body any, timeout time.Duration) ([]byte, error) {
	m.mu.Lock()
	direct, cd, pass := m.cfg.DirectAddr, m.cfg.CdAddr, m.cfg.AuthPassword
	m.mu.Unlock()
	if strings.TrimSpace(pass) == "" {
		return nil, errors.New("请先在\"配置\"卡设置认证密码")
	}
	_, wsPort, err := net.SplitHostPort(cd)
	if err != nil {
		wsPort = "8443"
	}
	host, _, err := net.SplitHostPort(direct)
	if err != nil {
		host = direct
	}
	addrs := []string{net.JoinHostPort(host, wsPort), cd}
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			// ponytail: 自签/IP 直连下无法常规校验证书；鉴权靠 X-Auth，传输安全由 TLS 信道保证
			InsecureSkipVerify: true,
		}},
	}
	var lastErr error
	for _, addr := range addrs {
		var reqBody io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			reqBody = bytes.NewReader(b)
		}
		req, err := http.NewRequest(method, "https://"+addr+path, reqBody)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-Auth", pass)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil { // 连接类失败换下一个地址（直连被墙时走 CDN）
			lastErr = err
			continue
		}
		out, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			var e struct {
				Error string `json:"error"`
			}
			json.Unmarshal(out, &e)
			if e.Error == "" {
				e.Error = resp.Status
			}
			return nil, errors.New(e.Error)
		}
		return out, nil
	}
	return nil, fmt.Errorf("服务端管理 API 不可达（%s 与 %s 均连不上）: %w", addrs[0], addrs[1], lastErr)
}

// applyProxy 按模式接管系统代理；off = 还原直连
func (m *webManager) applyProxy(mode string) {
	if m.undoFunc != nil {
		func() { // 还原失败不拖垮面板（记日志继续）
			defer func() {
				if r := recover(); r != nil {
					log.Printf("系统代理绕行/还原异常: %v", r)
				}
			}()
			m.undoFunc()
		}()
		m.undoFunc = nil
	}
	// 绕行名单：直连域名 + CDN 域名（两种模式的入口都不走代理）
	bypassHosts := []string{protocol.ServerHost(m.cfg.DirectAddr)}
	if d := strings.TrimSpace(m.cfg.Domain); d != "" && d != bypassHosts[0] {
		bypassHosts = append(bypassHosts, d)
	}
	switch mode {
	case "pac":
		m.undoFunc = sysproxy.ApplyPAC(m.proxyAddr(), m.cfg.ProxyDomains, m.cfg.ProxyIps, bypassHosts)
	case "all":
		m.undoFunc = sysproxy.ApplyAll(m.proxyAddr(), bypassHosts)
	}
}

// spawn 拉起客户端子进程（sysproxy 由管理进程负责，子进程 off）
func (m *webManager) spawn() error {
	// 密码：取面板配置（用户自设，不回显），写入 auth 文件供子进程读取
	pass := strings.TrimSpace(m.cfg.AuthPassword)
	if pass == "" {
		return errors.New("请先在\"配置\"卡设置认证密码")
	}
	if err := os.WriteFile(m.authPath(), []byte(pass), 0600); err != nil {
		return err
	}
	cmd := exec.Command(exePath(),
		"client",
		"-server-addr", m.tunnelAddr(),
		"-lport", m.cfg.LocalPort,
		"-lhost", "127.0.0.1", // 面板本机管理，代理只绑回环
		"-sysproxy", "off")
	if m.cfg.WsMode {
		cmd.Args = append(cmd.Args, "-ws")
	}
	sysproxy.HideWindowCmd(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	m.child = cmd
	m.writePidFile(cmd.Process.Pid)
	return nil
}

// killChild 停掉客户端：优先托管句柄，其次 PID 文件记录（跨面板重启的孤儿）
func (m *webManager) killChild() {
	if m.child != nil && m.child.Process != nil {
		m.child.Process.Kill()
		m.child.Wait()
		m.child = nil
	}
	if b, err := os.ReadFile(m.pidPath()); err == nil {
		var pr pidRecord
		if json.Unmarshal(b, &pr) == nil && pr.PID > 0 {
			if p, err := os.FindProcess(pr.PID); err == nil {
				p.Kill()
			}
		}
		os.Remove(m.pidPath())
	}
	// 等端口释放，最多 2 秒
	for i := 0; i < 20 && m.clientAlive(); i++ {
		time.Sleep(100 * time.Millisecond)
	}
}

// Start 启动代理（含接管：孤儿客户端直接纳入管理，不再 spawn）
func (m *webManager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.clientAlive() {
		// 孤儿/已运行：不重复拉起，只接管系统代理
		if m.undoFunc == nil {
			m.applyProxy(m.cfg.Mode)
		}
		return nil
	}
	if err := m.spawn(); err != nil {
		return err
	}
	// 等客户端监听就绪
	for i := 0; i < 30 && !m.clientAlive(); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if !m.clientAlive() {
		m.killChild()
		return errors.New("客户端启动后未能监听，详见日志")
	}
	m.applyProxy(m.cfg.Mode)
	log.Printf("面板：代理已启动 %s（模式 %s）", m.proxyAddr(), m.cfg.Mode)
	return nil
}

// Stop 停止代理并还原系统代理
func (m *webManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.killChild()
	m.applyProxy("off")
	log.Printf("面板：代理已停止，系统代理已还原")
}

// SetMode 切换模式：子进程与模式无关（纯代理），只重设系统代理，无需重启
func (m *webManager) SetMode(mode string) error {
	if mode != "pac" && mode != "all" && mode != "off" {
		return errors.New("非法模式: " + mode)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.Mode = mode
	saveWebConfig(m.dir, m.cfg)
	if m.clientAlive() || m.undoFunc != nil {
		m.applyProxy(mode)
	}
	return nil
}

// SaveConfig 更新配置；服务器地址变更→重启客户端；证书域名变更→重启服务端重新签发证书
func (m *webManager) SaveConfig(cfg webConfig) error {
	cfg.Mode = m.cfg.Mode // 模式走 SetMode，不接受这里改
	if cfg.MaxGB < 0 {
		return errors.New("流量配额不能为负")
	}
	if len(cfg.ProxyDomains) == 0 {
		cfg.ProxyDomains = DefaultProxyDomains()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	addrChanged := cfg.DirectAddr != m.cfg.DirectAddr ||
		cfg.CdAddr != m.cfg.CdAddr || cfg.LocalPort != m.cfg.LocalPort || cfg.WsMode != m.cfg.WsMode
	passChanged := strings.TrimSpace(cfg.AuthPassword) != strings.TrimSpace(m.cfg.AuthPassword)
	domainsChanged := strings.Join(cfg.ProxyDomains, ",") != strings.Join(m.cfg.ProxyDomains, ",")
	wasRunning := m.clientAlive()

	if (addrChanged || passChanged) && wasRunning {
		m.killChild()
		m.applyProxy("off")
	}
	m.cfg = cfg
	saveWebConfig(m.dir, cfg)
	if domainsChanged && m.undoFunc != nil && m.cfg.Mode == "pac" {
		m.applyProxy("pac") // 清单变了 → 重建 PAC
	}
	if (addrChanged || passChanged) && wasRunning {
		if err := m.spawn(); err != nil { // spawn 会按新配置重写 auth 文件
			return fmt.Errorf("配置已保存，但重启客户端失败: %w", err)
		}
		for i := 0; i < 30 && !m.clientAlive(); i++ {
			time.Sleep(100 * time.Millisecond)
		}
		m.applyProxy(m.cfg.Mode)
	}
	return nil
}

// RotateServerPass 轮换服务端密码：经管理 API 让服务端热轮换（写回其 auth 文件，新连接即刻生效），
// 面板保存新密码并重启本地客户端（旧密码客户端必须重启，否则连错会触发服务端封禁）。
// newPass 为空则由服务端生成随机密码；返回实际生效的密码
func (m *webManager) RotateServerPass(newPass string) (string, error) {
	newPass = strings.TrimSpace(newPass)
	out, err := m.adminCall("POST", "/_admin/pass", map[string]any{"password": newPass}, 20*time.Second)
	if err != nil {
		return "", err
	}
	var resp struct {
		Password string `json:"password"`
	}
	if json.Unmarshal(out, &resp) != nil || resp.Password == "" {
		return "", errors.New("服务端未返回新密码")
	}
	m.mu.Lock()
	m.cfg.AuthPassword = resp.Password
	saveWebConfig(m.dir, m.cfg)
	alive := m.clientAlive()
	m.mu.Unlock()
	if alive {
		m.Stop()
		if err := m.Start(); err != nil {
			return resp.Password, fmt.Errorf("密码已轮换，但客户端重启失败: %w", err)
		}
	}
	return resp.Password, nil
}

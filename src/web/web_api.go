package web

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ==================== HTTP API ====================

// serverCertHandler 查询服务端当前证书（ACME/手动/自签）的签发者与过期时间（转发服务端管理 API）
func (m *webManager) serverCertHandler(w http.ResponseWriter, r *http.Request) {
	out, err := m.adminCall("GET", "/_admin/cert", nil, 60*time.Second)
	if err != nil {
		httpError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}

// passRotateHandler 轮换服务端密码（password 空 = 服务端生成随机密码）
func (m *webManager) passRotateHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	pass, err := m.RotateServerPass(req.Password)
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "password": pass})
}

// statusHandler 面板运行状态
func (m *webManager) statusHandler(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	running := m.clientAlive()
	takeover := running && (m.child == nil || m.undoFunc == nil)
	writeJSON(w, map[string]any{
		"running":   running,
		"takeover":  takeover,
		"mode":      m.cfg.Mode,
		"proxyOn":   m.undoFunc != nil,
		"localPort": m.cfg.LocalPort,
		"server":    m.activeTunnelAddr(),
		"wsMode":    m.cfg.WsMode,
	})
}

func (m *webManager) logHandler(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	if n <= 0 || n > 2000 {
		n = 200
	}
	b, err := os.ReadFile(filepath.Join(m.dir, "client.log"))
	if err != nil {
		writeJSON(w, map[string]any{"lines": []string{}})
		return
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	writeJSON(w, map[string]any{"lines": lines})
}

// serverHandler 服务器流量（经服务端管理 API 读取本地 traffic 计数）
func (m *webManager) serverHandler(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	maxGB := m.cfg.MaxGB
	quotaDays := m.cfg.QuotaDays
	m.mu.Unlock()
	out, err := m.adminCall("GET", "/_admin/traffic", nil, 15*time.Second)
	if err != nil {
		httpError(w, err)
		return
	}
	var tr struct {
		Total       int64     `json:"total"`
		WindowStart time.Time `json:"windowStart"`
	}
	if err := json.Unmarshal(out, &tr); err != nil {
		httpError(w, err)
		return
	}
	used := float64(tr.Total) / 1e9
	windowStart := tr.WindowStart
	daysLeft := 0
	if quotaDays > 0 && !windowStart.IsZero() {
		elapsed := int(time.Since(windowStart).Hours() / 24)
		daysLeft = quotaDays - elapsed
		if daysLeft < 0 {
			daysLeft = 0
		}
	}
	writeJSON(w, map[string]any{
		"usedGB": used, "maxGB": maxGB, "quotaDays": quotaDays,
		"daysLeft": daysLeft, "windowStart": windowStart.Format("2006-01-02"),
	})
}

// securityHandler 查询/切换服务端封禁开关（转发 /_admin/security；POST body {"ban":bool}）
func (m *webManager) securityHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req struct {
			Ban bool `json:"ban"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, err)
			return
		}
		if _, err := m.adminCall("POST", "/_admin/security", map[string]any{"ban": req.Ban}, 15*time.Second); err != nil {
			httpError(w, err)
			return
		}
	}
	out, err := m.adminCall("GET", "/_admin/security", nil, 15*time.Second)
	if err != nil {
		httpError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}

// originGuard Origin/Host 校验中间件（port 为面板实际监听端口）：
// 面板可改系统代理、可经管理 API 运维服务器，必须防恶意网页跨端口调用
func originGuard(port string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		good := "127.0.0.1:" + port
		goodAlt := "localhost:" + port
		if o := r.Header.Get("Origin"); o != "" && o != "http://"+good && o != "http://"+goodAlt {
			http.Error(w, "origin 拒绝", http.StatusForbidden)
			return
		}
		if h := r.Header.Get("Host"); h != "" && h != good && h != goodAlt {
			http.Error(w, "host 拒绝", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
}

// ==================== 入口 ====================

// Run 面板主入口：HTTP 服务 + 退出清理 + 自动拉起代理
func Run(listen, dir string) {
	m := newWebManager(dir)
	_, port, _ := net.SplitHostPort(listen)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(webHTML)
	})
	mux.HandleFunc("/qrcode.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Write(qrcodeJS)
	})
	mux.HandleFunc("/favicon.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(faviconPNG)
	})
	mux.HandleFunc("/api/status", originGuard(port, m.statusHandler))
	mux.HandleFunc("/api/start", originGuard(port, func(w http.ResponseWriter, r *http.Request) {
		if err := m.Start(); err != nil {
			httpError(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	}))
	mux.HandleFunc("/api/stop", originGuard(port, func(w http.ResponseWriter, r *http.Request) {
		m.Stop()
		writeJSON(w, map[string]any{"ok": true})
	}))
	mux.HandleFunc("/api/mode", originGuard(port, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, err)
			return
		}
		if err := m.SetMode(req.Mode); err != nil {
			httpError(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	}))
	mux.HandleFunc("/api/config", originGuard(port, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var cfg webConfig
			if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
				httpError(w, err)
				return
			}
			if err := m.SaveConfig(cfg); err != nil {
				httpError(w, err)
				return
			}
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		writeJSON(w, m.cfg)
	}))
	mux.HandleFunc("/api/log", originGuard(port, m.logHandler))
	mux.HandleFunc("/api/server", originGuard(port, m.serverHandler))
	mux.HandleFunc("/api/cert", originGuard(port, m.serverCertHandler))
	mux.HandleFunc("/api/security", originGuard(port, m.securityHandler))
	mux.HandleFunc("/api/pass/rotate", originGuard(port, m.passRotateHandler))

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		log.Fatalf("面板监听失败: %v", err)
	}
	log.Printf("管理面板已启动 http://%s（仅本机可访问）", listen)

	// 退出时：停子进程 + 还原系统代理
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Printf("面板退出：停止客户端并还原系统代理...")
		m.Stop()
		os.Exit(0)
	}()

	// 面板启动即自动拉起代理（异步拉起，面板先就绪）
	go func() {
		if err := m.Start(); err != nil {
			log.Printf("自动启动代理失败（可在面板手动启动）: %v", err)
		}
	}()

	log.Fatal(http.Serve(ln, mux))
}

// exePath 当前二进制自身（web 模式拉起 client 模式即自身换参）
func exePath() string {
	p, err := os.Executable()
	if err != nil {
		return "caotun"
	}
	return p
}

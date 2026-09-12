// 管理 API：挂在 WS 接入监听（默认 8443）上，按路径分流（/tf 走隧道，/_admin/* 走这里）。
// 面板远程调用（X-Auth 头 = 认证密码），所有操作都是服务端本地文件读写，数据目录 portable。
package server

import (
	crand "crypto/rand"
	"crypto/subtle"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// randomPassword 128-bit 随机密码（32 位 hex，与 main.go serverCmd 同实现）
func randomPassword() string {
	b := make([]byte, 16)
	crand.Read(b) // crypto/rand 读失败仅见于系统级异常，忽略
	return hex.EncodeToString(b)
}

// adminServe 分发管理请求。X-Auth 密码错误与隧道认证一样计入封禁（防公网爆破）
func (s *trafficServer) adminServe(c net.Conn, req *http.Request) {
	ip, _, _ := net.SplitHostPort(c.RemoteAddr().String())
	c.SetDeadline(time.Now().Add(3 * time.Minute))
	if s.banned(ip) {
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.Header.Get("X-Auth")), s.currentAuth()) != 1 {
		log.Printf("管理 API 认证失败 %s %s", ip, req.URL.Path)
		s.recordFail(ip)
		adminReply(c, http.StatusUnauthorized, map[string]any{"error": "密码错误"})
		return
	}
	s.recordOK(ip)
	switch {
	case req.URL.Path == "/_admin/cert" && req.Method == http.MethodGet:
		s.adminCertInfo(c)
	case req.URL.Path == "/_admin/pass" && req.Method == http.MethodPost:
		s.adminPassRotate(c, req)
	case req.URL.Path == "/_admin/traffic" && req.Method == http.MethodGet:
		s.adminTraffic(c)
	default:
		adminReply(c, http.StatusNotFound, map[string]any{"error": "未知管理接口"})
	}
}

// adminCertInfo 列出服务端当前部署的全部证书与有效期（正式证书文件 + 自签兜底/手动证书）。
// 只读：不触发任何签发。证书由服务器上的定时脚本（scripts/shell/issue-cert.sh）维护
func (s *trafficServer) adminCertInfo(c net.Conn) {
	type certInfo struct {
		Name     string   `json:"name"`
		Issuer   string   `json:"issuer"`
		NotAfter string   `json:"notAfter"`
		DaysLeft int      `json:"daysLeft"`
		DNSNames []string `json:"dnsNames"`
	}
	var list []certInfo
	add := func(name string, leaf *x509.Certificate) {
		list = append(list, certInfo{
			Name: name, Issuer: leaf.Issuer.CommonName,
			NotAfter: leaf.NotAfter.Format("2006-01-02 15:04:05"),
			DaysLeft: int(time.Until(leaf.NotAfter).Hours() / 24),
			DNSNames: leaf.DNSNames,
		})
	}
	// 正式证书（定时脚本签发 → 数据目录/fullchain.pem）+ 自签兜底（cert.pem）
	for _, f := range []string{"fullchain.pem", "cert.pem"} {
		if b, err := os.ReadFile(filepath.Join(s.dir, f)); err == nil {
			if leaf, err := parseLeafPEM(b); err == nil {
				add(f, leaf)
			}
		}
	}
	// 手动证书模式（-cert 指定）
	if s.manualCert != "" {
		if b, err := os.ReadFile(s.manualCert); err == nil {
			if leaf, err := parseLeafPEM(b); err == nil {
				add(filepath.Base(s.manualCert), leaf)
			}
		}
	}
	adminReply(c, http.StatusOK, map[string]any{"mode": s.certMode(), "certs": list})
}

// adminPassRotate 轮换认证密码：写回密码文件 + 热更新（新连接即刻生效，存量连接不受影响）。
// password 为空则由服务端生成随机密码，响应里返回实际生效的密码
func (s *trafficServer) adminPassRotate(c net.Conn, req *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if b, err := io.ReadAll(req.Body); err == nil {
		json.Unmarshal(b, &body)
	}
	pass := strings.TrimSpace(body.Password)
	if pass == "" {
		pass = randomPassword()
	}
	if err := s.setAuth(pass); err != nil {
		adminReply(c, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	adminReply(c, http.StatusOK, map[string]any{"ok": true, "password": pass})
}

// adminTraffic 本周期累计流量与周期起始（面板"服务器流量"卡数据源）
func (s *trafficServer) adminTraffic(c net.Conn) {
	s.mu.Lock()
	start := s.windowStart
	s.mu.Unlock()
	adminReply(c, http.StatusOK, map[string]any{"total": atomic.LoadInt64(&s.total), "windowStart": start})
}

func (s *trafficServer) certMode() string {
	switch {
	case s.manualCert != "":
		return "manual"
	case fileCertExists(s.dir):
		return "file"
	default:
		return "self-signed"
	}
}

// fileCertExists 正式证书文件是否就位（fullchain.pem + privkey.pem 成对存在）
func fileCertExists(dir string) bool {
	_, e1 := os.Stat(filepath.Join(dir, "fullchain.pem"))
	_, e2 := os.Stat(filepath.Join(dir, "privkey.pem"))
	return e1 == nil && e2 == nil
}

// parseLeafPEM 取 PEM 流中第一块 CERTIFICATE（证书文件常见私钥/中间证书在前）
func parseLeafPEM(pemBytes []byte) (*x509.Certificate, error) {
	rest := pemBytes
	for {
		blk, rest2 := pem.Decode(rest)
		if blk == nil {
			return nil, errors.New("证书 PEM 解析失败")
		}
		if blk.Type == "CERTIFICATE" {
			return x509.ParseCertificate(blk.Bytes)
		}
		rest = rest2
	}
}

// adminReply 手写 HTTP/1.1 JSON 响应（连接已被预读，用不了 net/http 的 ResponseWriter）
func adminReply(c net.Conn, code int, v any) {
	b, _ := json.Marshal(v)
	fmt.Fprintf(c, "HTTP/1.1 %d %s\r\nContent-Type: application/json\r\nCache-Control: no-store\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		code, http.StatusText(code), len(b), b)
}

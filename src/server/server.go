package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"caotun/protocol"
	"caotun/ws"
)

// ==================== 服务端 ====================

func Run(listen, wsListen, auth, dir string, maxGB float64, quotaDays, maxConns int, certFile, keyFile string) {
	cfg := &tls.Config{}
	switch {
	case certFile != "" || keyFile != "":
		// 手动指定证书（与文件证书模式互斥）：PEM 加载，更换后重启服务端生效
		if certFile == "" || keyFile == "" {
			log.Fatal("-cert 与 -key 必须同时提供")
		}
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			log.Fatalf("加载手动证书失败: %v", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
		log.Printf("证书模式: 手动指定 %s", certFile)
	case certFile == "" && keyFile == "":
		// 文件证书模式：优先 数据目录/fullchain.pem + privkey.pem（由服务器定时签发脚本维护，
		// 文件变化自动热加载，续期免重启）；尚未签发时用自签证书兜底（客户端 TOFU）
		fullchain, privkey := filepath.Join(dir, "fullchain.pem"), filepath.Join(dir, "privkey.pem")
		selfCertPEM, selfKeyPEM, certErr := loadOrCreateCert(dir)
		var selfCert *tls.Certificate
		if certErr == nil {
			if c, e := tls.X509KeyPair(selfCertPEM, selfKeyPEM); e == nil {
				selfCert = &c
			}
		}
		if selfCert == nil {
			log.Printf("自签兜底证书生成失败: %v（IP 直连兜底不可用）", certErr)
		}
		var mu sync.Mutex
		var hotCert *tls.Certificate
		var hotMod time.Time
		cfg.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			fi, err := os.Stat(fullchain)
			if err == nil {
				if pfi, e2 := os.Stat(privkey); e2 == nil {
					mod := fi.ModTime()
					if pfi.ModTime().After(mod) {
						mod = pfi.ModTime()
					}
					mu.Lock()
					defer mu.Unlock()
					if hotCert == nil || mod.After(hotMod) {
						c, e3 := tls.LoadX509KeyPair(fullchain, privkey)
						if e3 != nil {
							log.Printf("证书文件加载失败: %v（回退自签）", e3)
							if selfCert != nil {
								return selfCert, nil
							}
							return nil, e3
						}
						hotCert, hotMod = &c, mod
						log.Printf("已加载证书文件 %s", fullchain)
					}
					return hotCert, nil
				}
			}
			if selfCert != nil { // 尚无正式证书：自签兜底（TOFU）
				return selfCert, nil
			}
			return nil, errors.New("无可用证书")
		}
		log.Printf("证书模式: 文件证书 %s（定时脚本签发、自动热加载；缺失时自签兜底）", fullchain)
	}
	ln, err := tls.Listen("tcp", listen, cfg)
	if err != nil {
		log.Fatalf("监听失败: %v", err)
	}
	log.Printf("服务端已启动 %s（并发上限 %d，流量配额 %g GB / %d 天）", listen, maxConns, maxGB, quotaDays)
	s := newTrafficServer([]byte(auth), dir, maxGB, quotaDays, maxConns)
	s.manualCert = certFile
	// WebSocket 接入（默认 8443 = Cloudflare 免费版可代理的 HTTPS 备用端口，-ws-port 可改）：CDN 模式与面板管理 API 共用
	go serveWS(wsListen, cfg, s)
	for {
		c, err := ln.Accept()
		if err != nil {
			time.Sleep(10 * time.Millisecond) // 监听异常(如 fd 耗尽)时防热循环
			continue
		}
		ip, _, _ := net.SplitHostPort(c.RemoteAddr().String())
		// 已封禁的 IP：直接断开（TLS 握手都不给）
		if s.banned(ip) {
			c.Close()
			continue
		}
		// 并发上限
		if int(atomic.LoadInt64(&s.active)) >= maxConns {
			log.Printf("并发连接已达上限 %d，拒绝 %s", maxConns, ip)
			c.Close()
			continue
		}
		go handleServerConn(c, s, ip)
	}
}

// handleServerConn 单条隧道连接的完整生命周期
func handleServerConn(c net.Conn, s *trafficServer, ip string) {
	auth := s.currentAuth() // 逐连接读取：管理 API 轮换密码后新连接即刻生效
	atomic.AddInt64(&s.active, 1)
	defer func() {
		atomic.AddInt64(&s.active, -1)
		s.persistTotal()
	}()
	defer c.Close()
	// 开 TCP keepalive：对端异常消失(NAT 超时/断网)时回收半开连接，防 goroutine 和并发额度泄漏
	if tc, ok := c.(*tls.Conn); ok {
		if nc, ok := tc.NetConn().(*net.TCPConn); ok {
			nc.SetKeepAlive(true)
			nc.SetKeepAlivePeriod(time.Minute)
		}
	}
	// 握手+认证限时 15 秒，防慢连接占资源
	c.SetDeadline(time.Now().Add(15 * time.Second))
	var nonce [32]byte
	rand.Read(nonce[:])
	if _, err := c.Write(nonce[:]); err != nil {
		return
	}
	mac := hmac.New(sha256.New, auth)
	mac.Write(nonce[:])
	want := mac.Sum(nil)
	got := make([]byte, len(want))
	if _, err := io.ReadFull(c, got); err != nil || !hmac.Equal(want, got) {
		log.Printf("认证失败 %s", c.RemoteAddr())
		s.recordFail(ip)
		return
	}
	s.recordOK(ip)
	// 流量熔断：周期到期自动清零；超限拒绝新隧道（状态码 2）
	s.maybeRollover()
	if s.overQuota() {
		log.Printf("流量配额 %g GB 已用完（当前累计 %g GB），拒绝新隧道", s.maxGB, float64(atomic.LoadInt64(&s.total))/1e9)
		c.Write([]byte{2})
		return
	}
	host, port, err := protocol.ReadTarget(c)
	if err != nil {
		return
	}
	remote, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 10*time.Second)
	if err != nil {
		c.Write([]byte{1})
		return
	}
	defer remote.Close()
	// 用统计连接替换原连接，双向流量计入 total
	cc := &countingConn{Conn: c, s: s}
	c.SetDeadline(time.Time{})
	if _, err := cc.Write([]byte{0}); err != nil {
		return
	}
	log.Printf("隧道 %s → %s:%d", c.RemoteAddr(), host, port)
	go func() {
		if _, err := io.Copy(remote, cc); err != nil && !errors.Is(err, io.EOF) {
			log.Printf("隧道上行结束: %v", err)
		}
	}()
	if _, err := io.Copy(cc, remote); err != nil && !errors.Is(err, io.EOF) {
		log.Printf("隧道下行结束: %v", err)
	}
}

// serveWS WS 接入监听（默认 8443）：/tf 走隧道握手，/_admin/* 走管理 API（面板远程管理服务端用）。
// CDN 可代理该端口，所以面板管理请求在 VPS IP 被墙时也能经 CF 到达
func serveWS(listen string, tlsCfg *tls.Config, s *trafficServer) {
	ln, err := tls.Listen("tcp", listen, tlsCfg)
	if err != nil {
		log.Printf("WS 接入监听失败 %s: %v", listen, err)
		return
	}
	log.Printf("WS 接入已启动 %s（CDN 可代理；/_admin/* 为面板管理 API）", listen)
	for {
		c, err := ln.Accept()
		if err != nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		go func() {
			defer c.Close()
			req, br, err := ws.PeekRequest(c)
			if err != nil {
				log.Printf("[WS] 请求读取失败 %s: %v", c.RemoteAddr(), err) // 含 TLS 握手错误（如证书签发失败）
				return
			}
			// 管理 API 与隧道同一端口：X-Auth 密码鉴权，公网可扫到但无密码进不来
			if strings.HasPrefix(req.URL.Path, "/_admin/") {
				s.adminServe(c, req)
				return
			}
			defer log.Printf("[WS] 连接关闭 %s", c.RemoteAddr())
			ip, _, _ := net.SplitHostPort(c.RemoteAddr().String())
			if s.banned(ip) || int(atomic.LoadInt64(&s.active)) >= s.maxConns {
				return
			}
			wc, err := ws.Accept(br, c, req)
			if err != nil {
				log.Printf("[WS] 握手失败: %v", err)
				return
			}
			handleServerConn(wc, s, ip)
		}()
	}
}

func loadOrCreateCert(dir string) (certPEM, keyPEM []byte, err error) {
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if certPEM, err = os.ReadFile(certPath); err == nil {
		if keyPEM, err = os.ReadFile(keyPath); err == nil {
			return certPEM, keyPEM, nil
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "caotun"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		return nil, nil, err
	}
	log.Printf("已生成自签证书 %s", certPath)
	return certPEM, keyPEM, nil
}

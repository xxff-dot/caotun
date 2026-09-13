package tunnel

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"caotun/protocol"
	"caotun/sysproxy"
	"caotun/ws"
)

// ==================== 客户端 ====================

// Run 客户端主入口：本地代理监听 + 系统代理接管 + 信号处理（桌面专用壳，核心在 Serve）
func Run(serverAddr, listen, auth, sysMode string, insecure bool, dir string, pacDomains, pacIPs []string, useWS bool) {
	if serverAddr == "" {
		log.Fatal("client 模式必须指定 -server 服务器地址")
	}

	// 系统代理：pac = 分流(GitHub 走隧道)；all = 全量
	var undo func() = func() {}
	proxyAddr := localAddr(listen)
	bypassHosts := []string{protocol.ServerHost(serverAddr)}
	switch sysMode {
	case "pac":
		undo = sysproxy.ApplyPAC(proxyAddr, pacDomains, pacIPs, bypassHosts)
		log.Printf("系统代理已设为 PAC 白名单分流（仅配置域名走隧道，其余直连）")
	case "all":
		undo = sysproxy.ApplyAll(proxyAddr, bypassHosts)
		log.Printf("系统代理已设为全局模式（服务器 IP 与局域网除外）")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	defer undo() // 信号或错误退出前恢复系统代理
	if err := Serve(ctx, serverAddr, listen, auth, dir, insecure, useWS); err != nil && ctx.Err() == nil {
		log.Fatalf("客户端退出: %v", err)
	}
}

// NewDialer 生成「目标 host:port → 隧道连接」的拨号器（桌面本地代理与移动端 tun2sock 共用）。
// protectPath 非空时（移动端），每个出站 socket 先经该 unix socket 发给宿主
// 调 VpnConnection.protect() 打免捕获标记，避免隧道自身流量回环进 tun
func NewDialer(serverAddr, auth, dir string, insecure, useWS bool, protectPath, dialIP string) func(host string, port int) (net.Conn, error) {
	fingerPath := filepath.Join(dir, "fingerprint.txt")
	expected := ""
	if !insecure {
		if b, err := os.ReadFile(fingerPath); err == nil {
			expected = strings.TrimSpace(string(b))
		}
	}
	var d net.Dialer
	d.Timeout = 10 * time.Second
	if protectPath != "" {
		p := newProtector(protectPath)
		d.Control = func(network, address string, c syscall.RawConn) error {
			var perr error
			c.Control(func(fd uintptr) {
				if !p.protect(int(fd)) {
					perr = errors.New("protect fd 失败")
				}
			})
			return perr
		}
	}
	dialHost := serverAddr
	if dialIP != "" {
		if _, port, err := net.SplitHostPort(serverAddr); err == nil {
			dialHost = net.JoinHostPort(dialIP, port) // IP 直拨,端口沿用
		}
	}
	return func(host string, port int) (net.Conn, error) {
		return dialTunnelD(&d, dialHost, serverAddr, []byte(auth), expected, fingerPath, insecure, host, port, useWS)
	}
}

// NewDirectDialer 生成「protect 后直连物理网络」的拨号器(移动端 CN 分流直连用)
func NewDirectDialer(protectPath string) func(host string, port int) (net.Conn, error) {
	var d net.Dialer
	d.Timeout = 10 * time.Second
	if protectPath != "" {
		p := newProtector(protectPath)
		d.Control = func(network, address string, c syscall.RawConn) error {
			var perr error
			c.Control(func(fd uintptr) {
				if !p.protect(int(fd)) {
					perr = errors.New("protect fd 失败")
				}
			})
			return perr
		}
	}
	return func(host string, port int) (net.Conn, error) {
		return d.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	}
}

// Serve 本地代理核心：监听 listen，SOCKS5 + HTTP CONNECT 同端口，每连接一条隧道。
// ctx 取消即关闭监听并返回（移动端 VpnExtension 与桌面 Run 共用）
func Serve(ctx context.Context, serverAddr, listen, auth, dir string, insecure bool, useWS bool) error {
	if serverAddr == "" {
		return errors.New("client 模式必须指定 -server 服务器地址")
	}
	dial := NewDialer(serverAddr, auth, dir, insecure, useWS, "", "")

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("本地监听失败: %w", err)
	}
	log.Printf("本地代理已启动 %s（SOCKS5 + HTTP CONNECT）", listen)
	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			time.Sleep(10 * time.Millisecond) // 监听异常(如 fd 耗尽)时防热循环
			continue
		}
		go handleClientConn(c, dial)
	}
}

// 本地监听地址转成给 PAC/系统代理用的 127.0.0.1:端口
func localAddr(listen string) string {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	return net.JoinHostPort("127.0.0.1", port)
}

// 识别首字节: 0x05 = SOCKS5，否则按 HTTP CONNECT 处理
func handleClientConn(c net.Conn, dial func(host string, port int) (net.Conn, error)) {
	defer c.Close()
	br := bufio.NewReader(c)
	first, err := br.Peek(1)
	if err != nil {
		return
	}
	if first[0] == 0x05 {
		socks5Proxy(br, c, dial)
	} else {
		httpConnectProxy(br, c, dial)
	}
}

func socks5Proxy(br *bufio.Reader, c net.Conn, dial func(host string, port int) (net.Conn, error)) {
	ver, err := br.ReadByte()
	if err != nil || ver != 5 {
		return
	}
	n, _ := br.ReadByte()
	methods := make([]byte, n)
	io.ReadFull(br, methods)
	if _, err := c.Write([]byte{5, 0}); err != nil { // 无需认证
		return
	}
	var hdr [4]byte
	if _, err := io.ReadFull(br, hdr[:]); err != nil {
		return
	}
	if hdr[1] != 1 { // 只支持 CONNECT
		c.Write([]byte{5, 7, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	host, port, err := readSocksAddr(br, hdr[3])
	if err != nil {
		return
	}
	rc, err := dial(host, port)
	if err != nil {
		log.Printf("%s 连接 %s:%d 失败: %v", c.RemoteAddr(), host, port, err)
		c.Write([]byte{5, 1, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer rc.Close()
	log.Printf("访问 %s:%d", host, port)
	c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	relay(c, rc)
}

func readSocksAddr(br *bufio.Reader, atyp byte) (string, int, error) {
	var host string
	switch atyp {
	case 1:
		var b [4]byte
		if _, err := io.ReadFull(br, b[:]); err != nil {
			return "", 0, err
		}
		host = net.IP(b[:]).String()
	case 3:
		l, _ := br.ReadByte()
		buf := make([]byte, l)
		if _, err := io.ReadFull(br, buf); err != nil {
			return "", 0, err
		}
		host = string(buf)
	case 4:
		var b [16]byte
		if _, err := io.ReadFull(br, b[:]); err != nil {
			return "", 0, err
		}
		host = net.IP(b[:]).String()
	default:
		return "", 0, fmt.Errorf("未知 SOCKS 地址类型 %d", atyp)
	}
	var p [2]byte
	if _, err := io.ReadFull(br, p[:]); err != nil {
		return "", 0, err
	}
	return host, int(binary.BigEndian.Uint16(p[:])), nil
}

func httpConnectProxy(br *bufio.Reader, c net.Conn, dial func(host string, port int) (net.Conn, error)) {
	line, err := br.ReadString('\n')
	if err != nil {
		return
	}
	parts := strings.Fields(strings.TrimSpace(line))
	if len(parts) < 3 || !strings.EqualFold(parts[0], "CONNECT") {
		c.Write([]byte("HTTP/1.1 405 Method Not Allowed\r\n\r\n"))
		return
	}
	host, portStr, err := net.SplitHostPort(parts[1])
	if err != nil {
		c.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
		return
	}
	port, _ := strconv.Atoi(portStr)
	// 读掉剩余请求头
	for {
		l, err := br.ReadString('\n')
		if err != nil || l == "\r\n" || l == "\n" {
			break
		}
	}
	rc, err := dial(host, port)
	if err != nil {
		log.Printf("%s 连接 %s:%d 失败: %v", c.RemoteAddr(), host, port, err)
		c.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}
	defer rc.Close()
	log.Printf("访问 %s:%d", host, port)
	c.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	relay(c, rc)
}

func relay(a, b net.Conn) {
	go func() {
		if _, err := io.Copy(b, a); err != nil && !errors.Is(err, io.EOF) {
			log.Printf("relay 上行结束: %v", err)
		}
	}()
	if _, err := io.Copy(a, b); err != nil && !errors.Is(err, io.EOF) {
		log.Printf("relay 下行结束: %v", err)
	}
}

// 建立到服务端的隧道并指定目标。
// TLS 校验混合模式：先按标准 CA 校验（服务端配真证书时直通）；
// 失败则回退自签+TOFU 指纹模式。两种模式安全性都成立。
// dialHost 为实际拨号地址（移动端传预解析 IP），sniHost 用于 TLS SNI/证书校验（域名）。
func dialTunnel(serverAddr string, auth []byte, expectedFingerprint, fingerPath string, insecure bool, host string, port int, useWS bool) (net.Conn, error) {
	return dialTunnelD(nil, serverAddr, serverAddr, auth, expectedFingerprint, fingerPath, insecure, host, port, useWS)
}

func dialTunnelD(d *net.Dialer, dialHost, sniHost string, auth []byte, expectedFingerprint, fingerPath string, insecure bool, host string, port int, useWS bool) (net.Conn, error) {
	dial := func(addr string) (net.Conn, error) {
		if d != nil {
			return d.Dial("tcp", addr)
		}
		return net.DialTimeout("tcp", addr, 10*time.Second)
	}
	// 1) 标准 CA 校验尝试（真证书直通）
	if !insecure {
		raw, err := dial(dialHost)
		if err == nil {
			raw.SetDeadline(time.Now().Add(15 * time.Second))
			tc := tls.Client(raw, &tls.Config{ServerName: protocol.ServerHost(sniHost)})
			if err := tc.Handshake(); err == nil {
				raw.SetDeadline(time.Time{})
				var c net.Conn = tc
				if useWS {
					if c, err = ws.ClientConn(tc, protocol.ServerHost(sniHost)); err != nil {
						raw.Close()
						return nil, err
					}
				}
				return finishTunnel(c, raw, auth, host, port)
			}
			raw.Close()
		}
	}
	// 2) 回退: 自签证书 + TOFU 指纹
	raw, err := dial(dialHost)
	if err != nil {
		return nil, err
	}
	raw.SetDeadline(time.Now().Add(15 * time.Second))
	// InsecureSkipVerify 是因为服务端可能用自签证书，安全由 TOFU 指纹校验保证；
	// ServerName 必须带：服务端 autocert 需要 SNI 才能选出证书
	tc := tls.Client(raw, &tls.Config{ServerName: protocol.ServerHost(sniHost), InsecureSkipVerify: true})
	if err := tc.Handshake(); err != nil {
		raw.Close()
		return nil, err
	}
	raw.SetDeadline(time.Time{})

	if !insecure {
		state := tc.ConnectionState()
		if len(state.PeerCertificates) == 0 {
			raw.Close()
			return nil, errors.New("服务端未提供证书")
		}
		sum := sha256.Sum256(state.PeerCertificates[0].Raw)
		fp := hex.EncodeToString(sum[:])
		if expectedFingerprint == "" {
			os.WriteFile(fingerPath, []byte(fp), 0600)
			log.Printf("首次连接，已信任服务端证书指纹 %s...", fp[:16])
		} else if fp != expectedFingerprint {
			raw.Close()
			return nil, errors.New("服务端证书指纹变化，疑似中间人！确认是服务端重装/换证书的话，删除 ~/.caotun/fingerprint.txt 后重试")
		}
	}

	var c net.Conn = tc
	if useWS {
		if c, err = ws.ClientConn(tc, protocol.ServerHost(sniHost)); err != nil {
			raw.Close()
			return nil, err
		}
	}
	return finishTunnel(c, raw, auth, host, port)
}

// finishTunnel TLS 握手完成后的认证 + 发目标 + 等状态确认
func finishTunnel(tc net.Conn, raw net.Conn, auth []byte, host string, port int) (net.Conn, error) {
	// 认证
	var nonce [32]byte
	if _, err := io.ReadFull(tc, nonce[:]); err != nil {
		raw.Close()
		return nil, err
	}
	mac := hmac.New(sha256.New, auth)
	mac.Write(nonce[:])
	if _, err := tc.Write(mac.Sum(nil)); err != nil {
		raw.Close()
		return nil, err
	}
	// 发目标 + 等确认
	if err := protocol.WriteTarget(tc, host, port); err != nil {
		raw.Close()
		return nil, err
	}
	var st [1]byte
	if _, err := io.ReadFull(tc, st[:]); err != nil {
		raw.Close()
		return nil, err
	}
	if st[0] == 2 {
		raw.Close()
		return nil, errors.New("服务端流量配额已用完，等周期重置或在面板重置计数")
	}
	if st[0] != 0 {
		raw.Close()
		return nil, fmt.Errorf("服务端无法正常接入 %s:%d", host, port)
	}
	return tc, nil
}

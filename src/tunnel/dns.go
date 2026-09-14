package tunnel

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"time"

	"caotun/protocol"
)

// StartDNS 本地 DNS 转发：listen 上同时监听 UDP+TCP，查询按序问上游、回第一应答。
// 上游经隧道走 DNS-over-TCP（应答来自 VPS 出口，防污染）；
// 查询的域名为服务器域名时改走直连，断开「拨隧道要解析服务器域名」的递归环。
// 上游须为 IP。阻塞运行，ctx 取消即退出。
func StartDNS(ctx context.Context, listen string, upstreams []string, serverHost string, dial func(host string, port int) (net.Conn, error)) error {
	ups, err := parseUpstreams(upstreams)
	if err != nil {
		return err
	}
	forward := newForwarder(ups, serverHost, dial)
	udpLn, err := net.ListenPacket("udp", listen)
	if err != nil {
		return err
	}
	tcpLn, err := net.Listen("tcp", listen)
	if err != nil {
		udpLn.Close()
		return err
	}
	log.Printf("DNS 转发已启动 %s（上游 %v 经隧道；服务器域名 %s 直连解析）", listen, ups, serverHost)
	go func() {
		<-ctx.Done()
		udpLn.Close()
		tcpLn.Close()
	}()
	go tcpLoop(tcpLn, forward)
	buf := make([]byte, 4096) // ponytail: UDP 单查询单 goroutine；DNS-over-UDP 报文 EDNS0 下一般 ≤4KB
	for {
		n, from, err := udpLn.ReadFrom(buf)
		if err != nil {
			return nil // ctx 取消关闭监听后 ReadFrom 报错，正常退出
		}
		query := append([]byte(nil), buf[:n]...)
		go func() {
			resp, err := forward(query)
			if err != nil {
				log.Printf("DNS 查询 %s 失败: %v", protocol.DNSQname(query), err)
				return
			}
			udpLn.WriteTo(resp, from)
		}()
	}
}

// parseUpstreams 清洗上游列表：去空白、缺端口的补 :53
func parseUpstreams(list []string) ([]string, error) {
	var ups []string
	for _, u := range list {
		if u = strings.TrimSpace(u); u == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(u); err != nil {
			u = net.JoinHostPort(u, "53")
		}
		ups = append(ups, u)
	}
	if len(ups) == 0 {
		return nil, errors.New("DNS 上游为空")
	}
	return ups, nil
}

// newForwarder 生成单次查询转发函数：按序尝试上游，回第一应答
func newForwarder(ups []string, serverHost string, dial func(host string, port int) (net.Conn, error)) func([]byte) ([]byte, error) {
	direct := func(host string, port int) (net.Conn, error) {
		var d net.Dialer
		d.Timeout = 3 * time.Second
		return d.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	}
	return func(query []byte) ([]byte, error) {
		use := dial
		if serverHost != "" && strings.EqualFold(protocol.DNSQname(query), serverHost) {
			use = direct // 服务器域名直连解析，断开递归环
		}
		var lastErr error
		for _, u := range ups {
			h, p, _ := net.SplitHostPort(u)
			port, _ := strconv.Atoi(p)
			rc, err := use(h, port)
			if err != nil {
				lastErr = err
				continue
			}
			rc.SetDeadline(time.Now().Add(5 * time.Second))
			resp, err := protocol.DNSOverTCP(rc, query)
			rc.Close()
			rc.Close()
			if err != nil {
				lastErr = err
				continue
			}
			return resp, nil
		}
		return nil, lastErr
	}
}

// tcpLoop DNS-over-TCP 通道：应答截断/过大时系统解析器会回退 TCP
func tcpLoop(tcpLn net.Listener, forward func([]byte) ([]byte, error)) {
	for {
		c, err := tcpLn.Accept()
		if err != nil {
			return
		}
		go handleTCPDNS(c, forward)
	}
}

// handleTCPDNS 单个 TCP DNS 会话：读一帧查询、转发、回一帧应答、关闭
func handleTCPDNS(c net.Conn, forward func([]byte) ([]byte, error)) {
	defer c.Close()
	var l [2]byte
	if _, err := io.ReadFull(c, l[:]); err != nil {
		return
	}
	query := make([]byte, binary.BigEndian.Uint16(l[:]))
	if _, err := io.ReadFull(c, query); err != nil {
		return
	}
	resp, err := forward(query)
	if err != nil {
		return
	}
	binary.BigEndian.PutUint16(l[:], uint16(len(resp)))
	c.Write(append(l[:], resp...))
}

// Package tun2sock 移动端专用：把 VpnExtension 创建的 tun 网卡 fd 喂进 gvisor 用户态
// TCP/IP 栈，TCP 连接走隧道拨号器；UDP 仅放行 DNS(53)，转成 DNS-over-TCP 走隧道，
// 其余 UDP（QUIC 等）丢弃促其降级 TCP。桌面端不使用本包。
// 栈的搭建方式参考 sing-tun（MIT License, github.com/SagerNet/sing-tun）。
package tun2sock

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"caotun/cnroute"
	"caotun/protocol"
	"caotun/proxylist"

	"github.com/sagernet/gvisor/pkg/buffer"
	"github.com/sagernet/gvisor/pkg/tcpip"
	"github.com/sagernet/gvisor/pkg/tcpip/adapters/gonet"
	"github.com/sagernet/gvisor/pkg/tcpip/header"
	"github.com/sagernet/gvisor/pkg/tcpip/network/ipv4"
	"github.com/sagernet/gvisor/pkg/tcpip/network/ipv6"
	"github.com/sagernet/gvisor/pkg/tcpip/stack"
	"github.com/sagernet/gvisor/pkg/tcpip/transport/tcp"
	"github.com/sagernet/gvisor/pkg/tcpip/transport/udp"
	"github.com/sagernet/gvisor/pkg/waiter"
)

const nicID tcpip.NICID = 1

// Dialer 建立「目标 host:port 的连接」
type Dialer = func(host string, port int) (net.Conn, error)

// Options Serve 的运行参数
type Options struct {
	FD, MTU         int
	Dial            Dialer   // 隧道拨号器:白名单域名走这里(目标可以是域名)
	Direct          Dialer   // 直连拨号器(protect 后走物理网络):其余流量走这里
	ProxyDomains    []string // 代理域名白名单(后缀匹配):仅这些域名进隧道,其余直连
	DirectResolvers []string // 非白名单域名的直连解析上游(默认 223.5.5.5;国内公共 DNS)
	CIDRPath        string   // 可选:外部 CN 段表覆盖内置表(裸 IP 直连判定用)
	DNS             []string // 已弃用:原隧道解析上游,fake-ip 模式不再使用
	Logf            func(string, ...any)
}

// Serve 阻塞读取 tun fd 并处理流量，ctx 取消或 fd 关闭后返回。
// fd 来自鸿蒙 VpnConnection.create()（isBlocking: true，阻塞模式）。
func Serve(ctx context.Context, o Options) error {
	dial := o.Dial
	if dial == nil {
		return fmt.Errorf("tun2sock: dialer 为空")
	}
	logf := o.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	tunFD, mtu := o.FD, o.MTU
	// fake-ip 白名单模式:命中代理域名的查询秒回假 IP(TCP 反查域名进隧道,服务端解析),
	// 其余查询转发国内上游解析真 IP 直连。全程无被污染的解析路径。
	pool := newFakeIPPool(fakeIPCIDR, 65535)
	cn := cnroute.LoadCNMatcher(o.CIDRPath)
	if cn == nil {
		cn = cnroute.LoadCNMatcherDefault() // 外部段表缺失时回落内置表(裸 IP 直连判定仍需 CN 段表)
	}
	match := func(domain string) bool { return proxylist.Match(domain, o.ProxyDomains) }
	logf("代理域名白名单:%d 条", len(o.ProxyDomains))
	f := os.NewFile(uintptr(tunFD), "tun")
	defer f.Close()
	logf("tun2sock 引擎启动(fake-ip %s) fd=%d mtu=%d", fakeIPCIDR, tunFD, mtu)

	ep := &tunEndpoint{f: f, mtu: uint32(mtu), logErr: logf}
	stk := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	if err := stk.CreateNICWithOptions(nicID, ep, stack.NICOptions{Name: "caotun0"}); err != nil {
		return fmt.Errorf("创建网络栈失败: %v", err)
	}
	stk.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: nicID},
		{Destination: header.IPv6EmptySubnet, NIC: nicID},
	})
	stk.SetSpoofing(nicID, true)
	stk.SetPromiscuousMode(nicID, true)
	rxBuf := tcpip.TCPReceiveBufferSizeRangeOption{Min: 16 << 10, Default: 512 << 10, Max: 4 << 20}
	txBuf := tcpip.TCPSendBufferSizeRangeOption{Min: 16 << 10, Default: 512 << 10, Max: 4 << 20}
	stk.SetTransportProtocolOption(tcp.ProtocolNumber, &rxBuf)
	stk.SetTransportProtocolOption(tcp.ProtocolNumber, &txBuf)

	// TCP 分流:假 IP → 反查域名,白名单域名原文进隧道(服务端解析);
	// 哨兵网段 TCP(如 DoT:853)直接拒绝——直连拨它会路由回 tun 造成无限自环;
	// 其余裸 IP:CN/内网走直连,国外一律走隧道。直连拨国外 IP 同样会自环,
	// 曾把引擎拖死(表现为越用越卡直至全部黑洞),故绝不对未知 IP 直连
	tcpFwd := tcp.NewForwarder(stk, 0, 1024, func(r *tcp.ForwarderRequest) {
		dstHost, dstPort := r.ID().LocalAddress.String(), int(r.ID().LocalPort)
		var wq waiter.Queue
		ep, terr := r.CreateEndpoint(&wq)
		if terr != nil {
			r.Complete(true)
			return
		}
		go func() {
			var rc net.Conn
			var err error
			target := dstHost // 日志展示用
			ip := net.ParseIP(dstHost)
			if domain, ok := pool.lookup(ip); ok {
				// 白名单域名:原文进隧道,服务端(境外出口)解析
				target = domain
				rc, err = o.Dial(domain, dstPort)
			} else if inSentinel(ip) {
				ep.Close() // 哨兵网段的 TCP(DoT:853 等):拒绝,促系统回落普通 DNS
				return
			} else if cn.DirectOK(ip) && o.Direct != nil {
				rc, err = o.Direct(dstHost, dstPort)
			} else {
				target = dstHost
				rc, err = o.Dial(dstHost, dstPort)
			}
			if err != nil {
				logf("隧道连接 %s:%d 失败: %v", target, dstPort, err)
				ep.Close()
				return
			}
			gc := gonet.NewTCPConn(&wq, ep)
			logf("隧道已建立 %s:%d", target, dstPort)
			n := relay(gc, rc)
			logf("隧道关闭 %s:%d 上行%d 下行%d", target, dstPort, n.a2b, n.b2a)
		}()
	})
	stk.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpFwd.HandlePacket)

	// UDP：仅放行 DNS(53)
	udpFwd := udp.NewForwarder(stk, func(r *udp.ForwarderRequest) bool {
		if r.ID().LocalPort != 53 {
			return false // ponytail: 只劫持 DNS；QUIC/游戏等 UDP 丢弃降级 TCP，需要全量 UDP 再加 NAT
		}
		var wq waiter.Queue
		ep, terr := r.CreateEndpoint(&wq)
		if terr != nil {
			return true
		}
		go handleDNS(gonet.NewUDPConn(&wq, ep), pool, match, o.Direct, o.DirectResolvers, logf)
		return true
	})
	stk.SetTransportProtocolHandler(udp.ProtocolNumber, udpFwd.HandlePacket)

	// 包计数心跳(进 hilog,便于真机排查捕获状态)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				logf("tun rx=%d tx=%d", ep.rx.Load(), ep.tx.Load())
			}
		}
	}()

	// 读循环：tun 阻塞读，一读一个 IP 包
	errCh := make(chan error, 1)
	go func() { errCh <- ep.readLoop() }()
	select {
	case <-ctx.Done():
		f.Close()
		stk.Close()
		for _, e := range stk.CleanupEndpoints() {
			e.Abort()
		}
		return nil
	case err := <-errCh:
		stk.Close()
		return err
	}
}

// relay 双向裸流转发（两端都 Close 才返回），返回双向字节数
func relay(a, b net.Conn) (stat struct{ a2b, b2a int64 }) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn, cnt *int64) {
		m, _ := io.Copy(dst, src)
		atomic.AddInt64(cnt, m)
		dst.SetDeadline(time.Now()) // 唤醒对向 Copy
		done <- struct{}{}
	}
	go cp(a, b, &stat.a2b)
	go cp(b, a, &stat.b2a)
	<-done
	a.Close()
	b.Close()
	<-done
	return
}

// handleDNS 单个应用 DNS 查询:
//   - 命中白名单 → 秒回假 IP(不解析),TCP 命中假 IP 时域名原文进隧道,服务端解析;
//   - 未命中 → 查询原文转发国内上游(DirectResolvers,直连)解析真 IP,应用直连。
//
// 白名单域名的 AAAA/HTTPS 回 NODATA,逼应用走 IPv4 + 明文 SNI。
func handleDNS(conn *gonet.UDPConn, pool *fakeIPPool, match func(string) bool, direct Dialer, resolvers []string, logf func(string, ...any)) {
	defer conn.Close()
	buf := make([]byte, 4096) // ponytail: 单次查询；DNS-over-UDP 报文 EDNS0 下一般 ≤4KB
	n, dstAddr, err := conn.ReadFrom(buf)
	if err != nil || n == 0 {
		return
	}
	name, qtype, qend := dnsQName(buf[:n])
	if name == "" {
		return
	}
	if !match(name) {
		// 非白名单:查询原文转发国内上游解析真 IP(这些域名未被墙,答案干净),应用直连
		if len(resolvers) == 0 {
			resolvers = []string{"223.5.5.5"}
		}
		for _, up := range resolvers {
			if direct == nil {
				break
			}
			rc, err := direct(up, 53)
			if err != nil {
				continue
			}
			resp, err := dnsProbe(rc, buf[:n], 3*time.Second)
			rc.Close()
			if err == nil && len(resp) >= 12 {
				conn.WriteTo(resp, dstAddr)
				return
			}
		}
		// 直连解析失败:回 NODATA 让应用重试
		conn.WriteTo(nodataAnswer(buf[:n], qend), dstAddr)
		return
	}
	if qtype != 1 { // 白名单域名的 AAAA/HTTPS/TXT:NODATA,逼 IPv4 + 明文 SNI
		conn.WriteTo(nodataAnswer(buf[:n], qend), dstAddr)
		return
	}
	ip := pool.obtain(name)
	conn.WriteTo(syntheticA(buf[:n], qend, ip), dstAddr)
}

// dnsProbe 带单次超时的 DNS-over-TCP 查询
func dnsProbe(rc net.Conn, query []byte, timeout time.Duration) ([]byte, error) {
	rc.SetDeadline(time.Now().Add(timeout))
	return protocol.DNSOverTCP(rc, query)
}

// ==================== tun LinkEndpoint ====================

// tunEndpoint 无链路层头的 tun 网卡端点：入向读 fd 分发，出向写 fd
type tunEndpoint struct {
	mu         sync.RWMutex
	f          *os.File
	mtu        uint32
	rx         atomic.Uint64
	tx         atomic.Uint64
	logErr     func(string, ...any)
	dispatcher stack.NetworkDispatcher
}

func (e *tunEndpoint) readLoop() error {
	buf := make([]byte, 65535)
	for {
		n, err := e.f.Read(buf)
		if err != nil {
			return err
		}
		e.rx.Add(1)
		if n < 20 {
			continue
		}
		var proto tcpip.NetworkProtocolNumber
		switch buf[0] >> 4 {
		case 4:
			proto = header.IPv4ProtocolNumber
		case 6:
			proto = header.IPv6ProtocolNumber
		default:
			continue
		}
		pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
			Payload: buffer.MakeWithData(buf[:n]),
		})
		pkt.NetworkProtocolNumber = proto
		e.mu.RLock()
		d := e.dispatcher
		e.mu.RUnlock()
		if d != nil {
			d.DeliverNetworkPacket(proto, pkt)
		}
		pkt.DecRef()
	}
}

func (e *tunEndpoint) Attach(d stack.NetworkDispatcher) {
	e.mu.Lock()
	e.dispatcher = d
	e.mu.Unlock()
}

func (e *tunEndpoint) IsAttached() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.dispatcher != nil
}

func (e *tunEndpoint) MTU() uint32 { return e.mtu }

func (*tunEndpoint) Capabilities() stack.LinkEndpointCapabilities { return 0 }

func (*tunEndpoint) MaxHeaderLength() uint16 { return 0 }

func (*tunEndpoint) LinkAddress() tcpip.LinkAddress { return "" }

func (*tunEndpoint) Wait() {} // 读循环由 Serve 持有，fd 关闭即退出

func (*tunEndpoint) ARPHardwareType() header.ARPHardwareType { return header.ARPHardwareNone }

// 以下为 LinkEndpoint 新接口的空实现（无链路层，帧头由网络栈自身管理）
func (*tunEndpoint) AddHeader(*stack.PacketBuffer) {}

func (e *tunEndpoint) ParseHeader(*stack.PacketBuffer) bool { return true }

func (*tunEndpoint) Close() {}

func (*tunEndpoint) SetOnCloseAction(func()) {}

func (e *tunEndpoint) SetMTU(mtu uint32) { e.mtu = mtu }

func (*tunEndpoint) SetLinkAddress(tcpip.LinkAddress) {}

// WritePackets 出向：网络栈发往应用的包写回 tun，一写一包。
// 部分 VPN 框架给到的 fd 是非阻塞的：缓冲瞬时打满时 Write 返回 EAGAIN，
// 直接当错误会让 gvisor 掐断连接（表现为下行偶发截断）。这里对 EAGAIN
// 短暂重试；其余错误记录后中止。
func (e *tunEndpoint) WritePackets(pkts stack.PacketBufferList) (int, tcpip.Error) {
	n := 0
	for _, pkt := range pkts.AsSlice() {
		slices := pkt.AsSlices()
		var data []byte
		if len(slices) == 1 {
			data = slices[0]
		} else {
			var join bytes.Buffer
			for _, s := range slices {
				join.Write(s)
			}
			data = join.Bytes()
		}
		retry := 0
		for {
			_, err := e.f.Write(data)
			if err == nil {
				break
			}
			retry++
			if retry > 2000 || !errors.Is(err, syscall.EAGAIN) { // ponytail: EAGAIN 自旋上限 2k 次(~1s)，超出视为真故障
				if e.logErr != nil {
					e.logErr("tun 写包失败(%d 包已写): %v", n, err)
				}
				return n, &tcpip.ErrAborted{}
			}
			time.Sleep(500 * time.Microsecond)
		}
		e.tx.Add(1)
		n++
	}
	return n, nil
}

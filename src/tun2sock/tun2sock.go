// Package tun2sock 移动端专用：把 VpnExtension 创建的 tun 网卡 fd 喂进 gvisor 用户态
// TCP/IP 栈，TCP 连接走隧道拨号器；UDP 仅放行 DNS(53)，转成 DNS-over-TCP 走隧道，
// 其余 UDP（QUIC 等）丢弃促其降级 TCP。桌面端不使用本包。
// 栈的搭建方式参考 sing-tun（MIT License, github.com/SagerNet/sing-tun）。
package tun2sock

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"caotun/protocol"

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
	FD, MTU  int
	Dial     Dialer // 隧道拨号器:非 CN 目标走这里
	Direct   Dialer // 直连拨号器(protect 后走物理网络):CN 目标走这里
	CIDRPath string   // 中国大陆 IPv4 段表文件;空 = 全部走隧道
	DNS      []string // 上游解析器列表(经隧道按序尝试);空 = 默认 8.8.8.8
	Logf     func(string, ...any)
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
	dnss := o.DNS
	if len(dnss) == 0 {
		dnss = []string{"223.5.5.5"} // 默认阿里公共 DNS
	}
	cn := LoadCNMatcher(o.CIDRPath)
	if cn != nil {
		logf("CN 段表已加载:%d 段走直连", len(cn.ranges))
	} else {
		logf("CN 段表未加载,全部流量走隧道")
	}
	f := os.NewFile(uintptr(tunFD), "tun")
	defer f.Close()
	logf("tun2sock 引擎启动 fd=%d mtu=%d dns=%v", tunFD, mtu, dnss)

	ep := &tunEndpoint{f: f, mtu: uint32(mtu)}
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

	// TCP：应用五元组的目标 = 真实目的地，直接拨隧道
	tcpFwd := tcp.NewForwarder(stk, 0, 1024, func(r *tcp.ForwarderRequest) {
		dstHost, dstPort := r.ID().LocalAddress.String(), int(r.ID().LocalPort)
		var wq waiter.Queue
		ep, terr := r.CreateEndpoint(&wq)
		if terr != nil {
			r.Complete(true)
			return
		}
		pick := func() Dialer {
			if cn != nil && cn.IsCN(net.ParseIP(dstHost)) {
				return o.Direct
			}
			return o.Dial
		}
		go func() {
			rc, err := pick()(dstHost, dstPort)
			if err != nil {
				logf("隧道连接 %s:%d 失败: %v", dstHost, dstPort, err)
				ep.Close()
				return
			}
			relay(gonet.NewTCPConn(&wq, ep), rc)
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
		go handleDNS(gonet.NewUDPConn(&wq, ep), o, cn, dnss, logf)
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

// relay 双向裸流转发（两端都 Close 才返回）
func relay(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		io.Copy(dst, src)
		dst.SetDeadline(time.Now()) // 唤醒对向 Copy
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	a.Close()
	b.Close()
	<-done
}

// handleDNS 单个应用 DNS 查询,分流解析(Clash/mosdns 同款):
//  1. CN 探测:按配置列表逐个 protect 直连解析,答案 IP 命中 CN 段 → 采纳
//     (国内站拿到国内 CDN IP,后续 TCP 命中 CN 段表直连,不绕道)
//  2. 未命中 CN 或直连失败:按配置列表逐个经隧道问上游(境外出口,防污染)
func handleDNS(conn *gonet.UDPConn, o Options, cn *CNMatcher, dnss []string, logf func(string, ...any)) {
	defer conn.Close()
	buf := make([]byte, 4096) // ponytail: 单次查询；DNS-over-UDP 报文 EDNS0 下一般 ≤4KB
	n, dstAddr, err := conn.ReadFrom(buf)
	if err != nil || n == 0 {
		return
	}
	query := buf[:n]
	// CN 探测:直连解析,任一答案命中 CN 段即采纳
	if o.Direct != nil {
		for _, host := range dnss {
			rc, err := o.Direct(host, 53)
			if err != nil {
				continue
			}
			resp, err := dnsProbe(rc, query, 4*time.Second)
			rc.Close()
			if err == nil && hasCNAnswer(resp, cn) {
				conn.WriteTo(resp, dstAddr)
				return
			}
		}
	}
	// 隧道兜底:全部经隧道从 VPS 出口解析(防污染)
	var lastErr error
	for _, host := range dnss {
		rc, err := o.Dial(host, 53)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := dnsProbe(rc, query, 5*time.Second)
		rc.Close()
		if err != nil {
			lastErr = err
			continue
		}
		conn.WriteTo(resp, dstAddr)
		return
	}
	if lastErr != nil {
		logf("DNS 查询失败(全部上游 %v): %v", dnss, lastErr)
	}
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

// WritePackets 出向：网络栈发往应用的包写回 tun，一写一包
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
		if _, err := e.f.Write(data); err != nil {
			return n, &tcpip.ErrAborted{}
		}
		e.tx.Add(1)
		n++
	}
	return n, nil
}

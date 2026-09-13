// Package tun2sock 移动端专用：把 VpnExtension 创建的 tun 网卡 fd 喂进 gvisor 用户态
// TCP/IP 栈，TCP 连接走隧道拨号器；UDP 仅放行 DNS(53)，转成 DNS-over-TCP 走隧道，
// 其余 UDP（QUIC 等）丢弃促其降级 TCP。桌面端不使用本包。
// 栈的搭建方式参考 sing-tun（MIT License, github.com/SagerNet/sing-tun）。
package tun2sock

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

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
	CIDRPath string // 中国大陆 IPv4 段表文件;空 = 全部走隧道
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
	cn := LoadCNMatcher(o.CIDRPath)
	if cn != nil {
		logf("CN 段表已加载:%d 段走直连", len(cn.ranges))
	} else {
		logf("CN 段表未加载,全部流量走隧道")
	}
	f := os.NewFile(uintptr(tunFD), "tun")
	defer f.Close()
	logf("tun2sock 引擎启动 fd=%d mtu=%d", tunFD, mtu)

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
		go handleDNS(gonet.NewUDPConn(&wq, ep), o, cn, logf)
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

// handleDNS 单个应用 DNS 查询,分流解析(Clash/mosdns 同款算法):
//  1. 先经 protect 直连问 223.5.5.5(快,国内 CDN 答案)——答案 IP 命中 CN 段 → 采纳,
//     后续该 IP 的 TCP 连接会被 CN 段表判为直连,国内站不绕道
//  2. 未命中 CN(或直连解析失败)→ 经隧道问 8.8.8.8(境外链路干净,防污染)
func handleDNS(conn *gonet.UDPConn, o Options, cn *CNMatcher, logf func(string, ...any)) {
	defer conn.Close()
	buf := make([]byte, 4096) // ponytail: 单次查询；DNS-over-UDP 报文 EDNS0 下一般 ≤4KB
	n, dstAddr, err := conn.ReadFrom(buf)
	if err != nil || n == 0 {
		return
	}
	query := buf[:n]
	if o.Direct != nil {
		if rc, err := o.Direct("223.5.5.5", 53); err == nil {
			resp, err := dnsOverTCP(rc, query)
			rc.Close()
			if err == nil && hasCNAnswer(resp, cn) {
				conn.WriteTo(resp, dstAddr)
				return
			}
		}
	}
	rc, err := o.Dial("8.8.8.8", 53) // 经隧道从 VPS 出口解析,防污染
	if err != nil {
		logf("DNS 隧道连接 %s 失败: %v", dstAddr, err)
		return
	}
	defer rc.Close()
	resp, err := dnsOverTCP(rc, query)
	if err != nil {
		logf("DNS 查询 %s 失败: %v", dstAddr, err)
		return
	}
	conn.WriteTo(resp, dstAddr)
}

// hasCNAnswer 解析 DNS 应答中的 A 记录,任一 IP 命中 CN 段表即返回 true
func hasCNAnswer(resp []byte, cn *CNMatcher) bool {
	if cn == nil || len(resp) < 12 {
		return false
	}
	qd := int(binary.BigEndian.Uint16(resp[4:6]))
	an := int(binary.BigEndian.Uint16(resp[6:8]))
	off := 12
	skipName := func() bool { // 跳过(可能压缩指向的)域名
		for {
			if off >= len(resp) {
				return false
			}
			l := int(resp[off])
			off++
			if l == 0 {
				return true
			}
			if l&0xC0 != 0 {
				off++
				return true
			}
			off += l
		}
	}
	for i := 0; i < qd; i++ {
		if !skipName() {
			return false
		}
		off += 4
	}
	for i := 0; i < an && off+10 <= len(resp); i++ {
		if !skipName() {
			break
		}
		typ := binary.BigEndian.Uint16(resp[off : off+2])
		rdlen := int(binary.BigEndian.Uint16(resp[off+8 : off+10]))
		if typ == 1 && rdlen == 4 && off+10+4 <= len(resp) {
			if cn.IsCN(net.IP(resp[off+10 : off+14])) {
				return true
			}
		}
		off += 10 + rdlen
	}
	return false
}

// dnsOverTCP 一次 DNS-over-TCP 交换：2 字节大端长度前缀包帧。
// ponytail: 只读第一响应即返回；多报文响应（极少见）截断，需要时改为循环读
func dnsOverTCP(rc net.Conn, query []byte) ([]byte, error) {
	if len(query) > 0xFFFF {
		return nil, errors.New("DNS 查询超长")
	}
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], uint16(len(query)))
	rc.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := rc.Write(append(l[:], query...)); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(rc, l[:]); err != nil {
		return nil, err
	}
	resp := make([]byte, binary.BigEndian.Uint16(l[:]))
	if _, err := io.ReadFull(rc, resp); err != nil {
		return nil, err
	}
	return resp, nil
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

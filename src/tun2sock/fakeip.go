// fake-ip 白名单模式:命中代理域名的 DNS 查询秒回假 IP,TCP 命中假 IP 时反查域名,
// 域名原文进隧道由服务端(境外出口)解析;未命中白名单的查询转发给国内上游直连解析,
// 应用拿真实 IP 直连。应用侧全程不做会被污染的解析。
// 网段用 RFC2544 基准测试保留段 198.18.0.0/15,互联网不可路由,不与业务冲突。
package tun2sock

import (
	"encoding/binary"
	"net"
	"sync"
)

// fakeIPCIDR 假 IP 池网段
const fakeIPCIDR = "198.18.0.0/15"

type fakeIPPool struct {
	mu    sync.Mutex
	base  uint32
	size  uint32
	next  uint32
	d2i   map[string]uint32
	i2d   map[uint32]string
	limit int
}

func newFakeIPPool(cidr string, limit int) *fakeIPPool {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil
	}
	ip := ipnet.IP.To4()
	if ip == nil {
		return nil
	}
	ones, bits := ipnet.Mask.Size()
	if bits != 32 {
		return nil
	}
	return &fakeIPPool{
		base:  binary.BigEndian.Uint32(ip),
		size:  uint32(1) << uint32(bits-ones),
		next:  1,
		d2i:   map[string]uint32{},
		i2d:   map[uint32]string{},
		limit: limit,
	}
}

// obtain 取(或分配)域名对应的假 IP
func (p *fakeIPPool) obtain(domain string) uint32 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ip, ok := p.d2i[domain]; ok {
		return ip
	}
	if len(p.d2i) >= p.limit { // ponytail: 满了整体重建,旧 IP 换代由 DNS TTL(60s)兜底
		p.d2i = map[string]uint32{}
		p.i2d = map[uint32]string{}
		p.next = 1
	}
	for {
		off := p.next%(p.size-2) + 1 // 跳过网段首地址(当网关用)
		p.next++
		ip := p.base + off
		if _, used := p.i2d[ip]; used {
			continue
		}
		p.d2i[domain] = ip
		p.i2d[ip] = domain
		return ip
	}
}

// lookup 反查假 IP 对应的域名;非本网段 IP 返回 false
func (p *fakeIPPool) lookup(ip net.IP) (string, bool) {
	if p == nil || ip.To4() == nil {
		return "", false
	}
	u := binary.BigEndian.Uint32(ip.To4())
	if u < p.base || u >= p.base+p.size {
		return "", false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	d, ok := p.i2d[u]
	return d, ok
}

// dnsQName 提取查询报文的域名、查询类型与问题段结束偏移
func dnsQName(pkt []byte) (name string, qtype, qend int) {
	if len(pkt) < 12 {
		return "", 0, 0
	}
	i := 12
	var labels []string
	for {
		if i >= len(pkt) {
			return "", 0, 0
		}
		l := int(pkt[i])
		i++
		if l == 0 {
			break
		}
		if l&0xC0 != 0 || i+l > len(pkt) { // 问题段不允许压缩指针
			return "", 0, 0
		}
		labels = append(labels, string(pkt[i:i+l]))
		i += l
	}
	if i+4 > len(pkt) {
		return "", 0, 0
	}
	return joinLabels(labels), int(binary.BigEndian.Uint16(pkt[i:])), i + 4
}

func joinLabels(labels []string) string {
	out := make([]byte, 0, 64)
	for i, l := range labels {
		if i > 0 {
			out = append(out, '.')
		}
		out = append(out, l...)
	}
	return string(out)
}

// syntheticA 单条 A 应答:回显包头+问题,答案指向假 IP,TTL 60
func syntheticA(q []byte, qend int, ip uint32) []byte {
	resp := make([]byte, 0, qend+16)
	hdr := append([]byte{}, q[:12]...)
	binary.BigEndian.PutUint16(hdr[2:], 0x8180) // QR RD RA
	binary.BigEndian.PutUint16(hdr[6:], 1)      // ANCOUNT
	resp = append(resp, hdr...)
	resp = append(resp, q[12:qend]...)
	ans := make([]byte, 16)
	binary.BigEndian.PutUint16(ans[0:], 0xC00C) // 名字压缩指针指向问题段
	binary.BigEndian.PutUint16(ans[2:], 1)      // A
	binary.BigEndian.PutUint16(ans[4:], 1)      // IN
	binary.BigEndian.PutUint32(ans[6:], 60)     // TTL
	binary.BigEndian.PutUint16(ans[10:], 4)     // RDLENGTH
	ans[12], ans[13], ans[14], ans[15] = byte(ip>>24), byte(ip>>16), byte(ip>>8), byte(ip)
	return append(resp, ans...)
}

// nodataAnswer 空应答(NODATA)
func nodataAnswer(q []byte, qend int) []byte {
	resp := append([]byte{}, q[:qend]...)
	binary.BigEndian.PutUint16(resp[2:], 0x8180)
	binary.BigEndian.PutUint16(resp[6:], 0) // ANCOUNT
	return resp
}

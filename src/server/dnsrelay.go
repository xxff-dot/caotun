package server

import (
	"encoding/binary"
	"io"
	"net"
	"time"
)

// dnsRelay 端口 53 的隧道转发走「本地 UDP 原生查询 + 小缓存」而非逐条 TCP 转发：
// 客户端加载页面会突发几十条并发 DNS(每条一个隧道连接)，逐条 TCP 连公共 DNS
// 会被按源 IP 限流(表现为 read 超时)，页面因此时好时坏；UDP 原生查询无此问题，
// 缓存再把重复域名直接吃掉。帧格式 RFC 7766：2 字节大端长度前缀 + DNS 报文。
func (s *trafficServer) dnsRelay(cc net.Conn, host string) {
	defer cc.Close()
	var l [2]byte
	if _, err := io.ReadFull(cc, l[:]); err != nil {
		return
	}
	n := binary.BigEndian.Uint16(l[:])
	q := make([]byte, n)
	if _, err := io.ReadFull(cc, q); err != nil {
		return
	}
	if len(q) < 12 {
		return
	}
	// 过滤 AAAA(28) 与 HTTPS(65) 查询：VPS 无 IPv6 出口，返回真实 AAAA 只会让
	// 应用优先尝试 v6 直连(绕过隧道且必败)；HTTPS 记录携带 ECH 密钥，会让
	// 浏览器用加密 ClientHello 握手而被 GFW 掐断连接。均回 NODATA 让应用回落
	// 到普通 IPv4 + 明文 SNI(v4-only VPN 标准做法)
	if t := queryType(q); t == 28 || t == 65 {
		if resp := emptyAnswer(q); resp != nil {
			s.dnsCachePut(q, resp)
			dnsWrite(cc, resp)
		}
		return
	}
	if resp, ok := s.dnsCacheGet(q); ok {
		dnsWrite(cc, resp)
		return
	}
	// 固定用境外上游：VPS(境外)→国内 DNS 的 blocked 域名查询会被 GFW 注入假应答，
	// 且假应答常比真应答先到、还会被缓存放大；境外→8.8.8.8 不跨墙，天然干净。
	// host 参数仅作 fallback。
	upstreams := []string{"8.8.8.8", "1.1.1.1", host}
	var resp []byte
	for _, up := range upstreams {
		uc, err := net.DialTimeout("udp", net.JoinHostPort(up, "53"), 3*time.Second)
		if err != nil {
			continue
		}
		uc.SetDeadline(time.Now().Add(4 * time.Second))
		if _, err := uc.Write(q); err != nil {
			uc.Close()
			continue
		}
		buf := make([]byte, 4096)
		nn, err := uc.Read(buf)
		uc.Close()
		if err != nil || nn < 12 {
			continue
		}
		resp = buf[:nn]
		break
	}
	if len(resp) < 12 {
		return
	}
	s.dnsCachePut(q, resp)
	dnsWrite(cc, resp)
}

func dnsWrite(cc net.Conn, resp []byte) {
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], uint16(len(resp)))
	cc.Write(l[:])
	cc.Write(resp)
}

// queryType 提取 DNS 查询的 Question 类型；解析失败返回 0
func queryType(q []byte) int {
	if len(q) < 12 {
		return 0
	}
	i := 12
	for {
		if i >= len(q) {
			return 0
		}
		l := int(q[i])
		i++
		if l == 0 {
			break
		}
		i += l
	}
	if i+4 > len(q) {
		return 0
	}
	return int(binary.BigEndian.Uint16(q[i:]))
}

// emptyAnswer 构造 NODATA 空应答：回显包头+Question，AN/NS/ARCOUNT=0，RCODE=0
func emptyAnswer(q []byte) []byte {
	i := 12
	for {
		if i >= len(q) {
			return nil
		}
		l := int(q[i])
		i++
		if l == 0 {
			break
		}
		i += l
	}
	i += 4 // qtype + qclass
	if i > len(q) {
		return nil
	}
	resp := make([]byte, i)
	copy(resp, q[:i])
	binary.BigEndian.PutUint16(resp[2:], 0x8180) // QR=1 RD=1 RA=1
	binary.BigEndian.PutUint16(resp[6:], 0)      // ANCOUNT=0
	return resp
}

// dnsCacheGet 缓存键不含事务 ID(前 4 字节)，命中时把响应 ID 改写回查询的 ID
func (s *trafficServer) dnsCacheGet(q []byte) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.dnsCache[string(q[2:])]
	if !ok || time.Now().After(e.exp) {
		return nil, false
	}
	out := make([]byte, len(e.resp))
	copy(out, e.resp)
	out[0], out[1] = q[0], q[1]
	return out, true
}

func (s *trafficServer) dnsCachePut(q, resp []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.dnsCache) > 4096 { // 有界：超限整体重建，简单够用
		s.dnsCache = map[string]dnsEntry{}
	}
	cp := make([]byte, len(resp))
	copy(cp, resp)
	s.dnsCache[string(q[2:])] = dnsEntry{resp: cp, exp: time.Now().Add(60 * time.Second)}
}

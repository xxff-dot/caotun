package tun2sock

import (
	"net"
	"testing"

	"caotun/cnroute"
)

func u32ip(v uint32) net.IP {
	return net.IP{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
}

// rawAQuery 手工构造 A 查询(与引擎收到的应用查询同构)
func rawAQuery(domain string) []byte {
	pkt := []byte{0x12, 0x34, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0}
	for _, l := range []string{"www", "example", "com"} {
		pkt = append(pkt, byte(len(l)))
		pkt = append(pkt, l...)
	}
	return append(pkt, 0, 0, 1, 0, 1) // 结尾 + A + IN
}

func TestFakeIPPool(t *testing.T) {
	p := newFakeIPPool("198.18.0.0/15", 65535)
	if p == nil {
		t.Fatal("pool nil")
	}
	a := p.obtain("www.google.com")
	b := p.obtain("www.baidu.com")
	if a == b {
		t.Fatalf("不同域名分到同一假IP: %s", u32ip(a).String())
	}
	if p.obtain("www.google.com") != a {
		t.Fatal("同域名应复用同假IP")
	}
	if d, ok := p.lookup(u32ip(a)); !ok || d != "www.google.com" {
		t.Fatalf("反查失败: %q ok=%v", d, ok)
	}
	if _, ok := p.lookup(net.ParseIP("142.250.1.1")); ok {
		t.Fatal("真IP不应命中假IP池")
	}
}

func TestDNSQName(t *testing.T) {
	q := rawAQuery("www.example.com")
	name, qtype, qend := dnsQName(q)
	if name != "www.example.com" || qtype != 1 || qend != 12+17+4 {
		t.Fatalf("qname=%q qtype=%d qend=%d", name, qtype, qend)
	}
	// 假IP应答:事务ID回显 + 单条A记录 + 可再解析
	resp := syntheticA(q, qend, 0xC6120001)
	if resp[0] != q[0] || resp[1] != q[1] {
		t.Fatal("事务ID未回显")
	}
	if an := int(resp[6])<<8 | int(resp[7]); an != 1 {
		t.Fatalf("ANCOUNT=%d", an)
	}
	ip := net.IP{resp[len(resp)-4], resp[len(resp)-3], resp[len(resp)-2], resp[len(resp)-1]}
	if ip.String() != "198.18.0.1" {
		t.Fatalf("应答IP=%s", ip)
	}
	// NODATA 无 A 记录
	nd := nodataAnswer(q, qend)
	if an := int(nd[6])<<8 | int(nd[7]); an != 0 {
		t.Fatalf("NODATA ANCOUNT=%d", an)
	}
}

func TestFakeIPNotInCNTable(t *testing.T) {
	cn := cnroute.LoadCNMatcher(`E:\tools\tcpforward\mobile\android\app\src\main\res\raw\cn_cidr.txt`)
	if cn == nil {
		t.Skip("本机无段表")
	}
	if cn.IsCN(net.ParseIP("198.18.0.1")) || cn.IsCN(net.ParseIP("198.19.255.254")) {
		t.Fatal("198.18.0.0/15 不应命中 CN 段表(否则假IP流量会走直连)")
	}
}

func TestSentinelGuard(t *testing.T) {
	for _, ip := range []string{"10.111.0.1", "10.111.0.2", "10.111.0.3"} {
		if !inSentinel(net.ParseIP(ip)) {
			t.Fatalf("%s 应命中哨兵网段", ip)
		}
	}
	for _, ip := range []string{"10.111.1.1", "8.8.8.8", "142.250.1.1"} {
		if inSentinel(net.ParseIP(ip)) {
			t.Fatalf("%s 不应命中哨兵网段", ip)
		}
	}
}

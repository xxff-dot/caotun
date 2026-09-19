package tunnel

import (
	"context"
	"errors"
	"net"
	"testing"

	"caotun/cnroute"
)

// newTestRouter 构造注入假 resolver/拨号器与静默日志的路由器。
// 返回 router 供字段注入,以及直连/隧道两个拨号日志切片的指针。
func newTestRouter(t *testing.T, whitelist []string, resolve func(context.Context, string) ([]net.IP, error)) (*router, *[]string, *[]string) {
	if resolve == nil {
		resolve = func(context.Context, string) ([]net.IP, error) {
			return nil, errors.New("测试中不应解析")
		}
	}
	directLog := []string{}
	tunnelLog := []string{}
	r := &router{
		whitelist: whitelist,
		cn:        cnroute.LoadCNMatcherDefault(),
		direct: func(host string, port int) (net.Conn, error) {
			directLog = append(directLog, host)
			return nil, nil
		},
		tunnel: func(host string, port int) (net.Conn, error) {
			tunnelLog = append(tunnelLog, host)
			return nil, nil
		},
		resolve: resolve,
		logf:    func(string, ...any) {},
		cache:   map[string]routeEntry{},
	}
	return r, &directLog, &tunnelLog
}

// mustIP 必须解析成功的 IP
func mustIP(t *testing.T, s string) net.IP {
	ip := net.ParseIP(s)
	if ip == nil {
		t.Fatalf("非法 IP %s", s)
	}
	return ip
}

// TestRouteWhitelistForcesTunnel 白名单域名强制走隧道,且不触发解析
func TestRouteWhitelistForcesTunnel(t *testing.T) {
	r, d, tg := newTestRouter(t, []string{"google.com"}, func(_ context.Context, _ string) ([]net.IP, error) {
		t.Fatal("白名单域名不应触发解析 resolver")
		return nil, nil
	})
	r.dial("www.google.com", 443)
	if len(*tg) == 0 || (*tg)[0] != "www.google.com" {
		t.Fatalf("白名单域名应走隧道, got %v", *tg)
	}
	if len(*d) != 0 {
		t.Fatalf("白名单域名不应直连, got %v", *d)
	}
}

// TestRouteCNIPv4Direct CN IPv4 字面量直连
func TestRouteCNIPv4Direct(t *testing.T) {
	r, d, tg := newTestRouter(t, nil, nil)
	r.dial("223.5.5.5", 443)
	if len(*d) == 0 || (*d)[0] != "223.5.5.5" {
		t.Fatalf("CN IPv4 应直连, got %v", *d)
	}
	if len(*tg) != 0 {
		t.Fatalf("CN IPv4 流量不应走隧道, got %v", *tg)
	}
}

// TestRouteForeignIPv4Tunnel 非 CN IPv4 字面量走隧道
func TestRouteForeignIPv4Tunnel(t *testing.T) {
	r, d, tg := newTestRouter(t, nil, nil)
	r.dial("8.8.8.8", 443)
	if len(*tg) == 0 || (*tg)[0] != "8.8.8.8" {
		t.Fatalf("非 CN IPv4 应走隧道, got %v", *tg)
	}
	if len(*d) != 0 {
		t.Fatalf("非 CN IPv4 不应直连, got %v", *d)
	}
}

// TestRouteDomainCNResolvesDirect 解析为 CN IP 的域名 → 按域名直拨;第二次命中缓存不再解析
func TestRouteDomainCNResolvesDirect(t *testing.T) {
	resolves := 0
	r, d, tg := newTestRouter(t, nil, func(_ context.Context, _ string) ([]net.IP, error) {
		resolves++
		return []net.IP{mustIP(t, "203.208.41.1")}, nil // Google 中国段,在 CN 段表内
	})
	r.dial("example-cn.com", 443)
	r.dial("example-cn.com", 443) // 第二次应命中缓存
	if resolves != 1 {
		t.Fatalf("缓存未生效: resolves=%d", resolves)
	}
	if len(*d) == 0 || (*d)[0] != "example-cn.com" {
		t.Fatalf("CN 域名应按域名直拨, got %v", *d)
	}
	if len(*tg) != 0 {
		t.Fatalf("CN 域名不应走隧道, got %v", *tg)
	}
}

// TestRouteDomainNonCNToTunnel 解析为非 CN → 域名原文走隧道
func TestRouteDomainNonCNToTunnel(t *testing.T) {
	r, d, tg := newTestRouter(t, nil, func(_ context.Context, _ string) ([]net.IP, error) {
		return []net.IP{mustIP(t, "142.250.66.78")}, nil // 国外 IP
	})
	r.dial("example-foreign.com", 443)
	if len(*tg) == 0 || (*tg)[0] != "example-foreign.com" {
		t.Fatalf("非 CN 域名应按域名走隧道, got %v", *tg)
	}
	_ = d
}

// TestRouteDomainResolveFailureToTunnel 解析失败 → 隧道兜底
func TestRouteDomainResolveFailureToTunnel(t *testing.T) {
	r, d, tg := newTestRouter(t, nil, func(_ context.Context, _ string) ([]net.IP, error) {
		return nil, errors.New("SERVFAIL")
	})
	r.dial("example-fail.com", 443)
	if len(*tg) == 0 || (*tg)[0] != "example-fail.com" {
		t.Fatalf("解析失败应走隧道兜底, got %v", *tg)
	}
	if len(*d) != 0 {
		t.Fatalf("解析失败不应直连, got %v", *d)
	}
}

// TestRouteIPv6LiteralTunnel IPv6 字面量走隧道
func TestRouteIPv6LiteralTunnel(t *testing.T) {
	r, d, tg := newTestRouter(t, nil, nil)
	r.dial("2606:4700::6810:85e5", 443)
	if len(*tg) == 0 || (*tg)[0] != "2606:4700::6810:85e5" {
		t.Fatalf("IPv6 字面量应走隧道, got %v", *tg)
	}
	if len(*d) != 0 {
		t.Fatalf("IPv6 不应直连, got %v", *d)
	}
}

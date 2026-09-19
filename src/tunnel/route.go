// 智能分流拨号器:把「国内直连 / 其余走隧道」的判定与拨号统一到客户端进程内。
// 白名单命中 → 强制隧道(快路径,免本地解析);
// 其余域名 → 国内 DNS 解析仅作 CN 判定(结果从不用于拨号,污染假 IP 无害——
// 假 IP 必为国外段,稳定落入"非 CN → 域名原文进隧道",由服务端境外解析,污染不参与连接);
// IPv4 字面量 → CN 段表判定;IPv6 字面量 → 隧道(ATYP=4 协议已支持)。
// 段表未加载/解析失败一律走隧道(安全优先)。
package tunnel

import (
	"context"
	"log"
	"net"
	"sync"
	"time"

	"caotun/cnroute"
	"caotun/proxylist"
)

// Dialer 拨号器类型,与 NewDialer/NewDirectDialer 返回的函数同型:目标 host:port → 连接
type Dialer = func(host string, port int) (net.Conn, error)

// 判定用国内 DNS 上游(与 tun2sock 的 DirectResolvers 默认一致)
var cnResolvers = []string{"223.5.5.5", "119.29.29.29"}

// 判定结果 TTL 与缓存容量上限;超限整体清空(简单防膨胀)
const (
	routeTTL     = 10 * time.Minute
	routeMaxSize = 8192
)

type routeEntry struct {
	deadline time.Time
	direct   bool
}

type router struct {
	whitelist []string
	cn        *cnroute.CNMatcher
	direct    Dialer
	tunnel    Dialer
	resolve   func(ctx context.Context, host string) ([]net.IP, error) // 可注入,测试用
	logf      func(string, ...any)
	mu        sync.Mutex
	cache     map[string]routeEntry
}

// NewRouter 生成智能分流拨号器。
// whitelist: 强制走隧道的域名后缀白名单(proxylist.Match 语义,快路径免本地解析)
// direct/tun: 直连与隧道拨号器
func NewRouter(whitelist []string, direct, tun Dialer) Dialer {
	return newRouter(whitelist, direct, tun, log.Printf).dial
}

// newRouter 内部构造(resolve/logf 可注入,测试用)
func newRouter(whitelist []string, direct, tun Dialer, logf func(string, ...any)) *router {
	r := &router{
		whitelist: whitelist,
		cn:        cnroute.LoadCNMatcherDefault(),
		direct:    direct,
		tunnel:    tun,
		logf:      logf,
		cache:     map[string]routeEntry{},
	}
	r.resolve = r.cnResolve
	return r
}

// dial 分流决策 + 拨号
func (r *router) dial(host string, port int) (net.Conn, error) {
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() != nil && r.cn.DirectOK(ip) {
			return r.direct(host, port) // CN/内网 IPv4 直连
		}
		return r.tunnel(host, port) // 非 CN IPv4 与 IPv6 走隧道(ATYP=4 已支持)
	}
	if proxylist.Match(host, r.whitelist) {
		return r.tunnel(host, port) // 白名单快路径:域名原文进隧道,服务端境外解析
	}
	if r.decide(host) {
		return r.direct(host, port) // 域名直拨,OS 自己解析
	}
	return r.tunnel(host, port)
}

// decide 域名路由判定(带 TTL 缓存):国内上游解析出的 IPv4 全部命中 CN/内网段 → 直连;
// 含非 CN / 无 IPv4 应答 / 解析失败 → 隧道(安全默认)。
func (r *router) decide(host string) bool {
	r.mu.Lock()
	if e, ok := r.cache[host]; ok {
		if time.Now().Before(e.deadline) {
			d := e.direct
			r.mu.Unlock()
			return d
		}
		delete(r.cache, host)
	}
	r.mu.Unlock()

	// 解析在锁外做(最长约 2s),不阻塞其他连接的缓存读写
	ips, err := r.resolve(context.Background(), host)
	direct := false
	if err == nil && len(ips) > 0 {
		direct = true
		for _, ip := range ips {
			if ip4 := ip.To4(); ip4 == nil || !r.cn.DirectOK(ip4) {
				direct = false // 含非 CN 或仅有 AAAA → 隧道
				break
			}
		}
	}

	r.mu.Lock()
	if len(r.cache) >= routeMaxSize {
		r.cache = map[string]routeEntry{} // ponytail: 超限整体清空;按域名 LRU 淘汰仅在实测内存紧张时再加
	}
	r.cache[host] = routeEntry{deadline: time.Now().Add(routeTTL), direct: direct}
	r.mu.Unlock()
	if direct {
		r.logf("分流 %s → 直连", host)
	} else {
		r.logf("分流 %s → 隧道", host)
	}
	return direct
}

// cnResolve 定向国内上游的解析器:忽略系统 DNS 配置,UDP 直查上游列表(判定专用)。
// 上游轮询在 Dial 层完成,首个拨不通自动换下一个;整体 2s 超时。
func (r *router) cnResolve(ctx context.Context, host string) ([]net.IP, error) {
	res := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			var d net.Dialer
			d.Timeout = 1500 * time.Millisecond
			var lastErr error
			for _, s := range cnResolvers {
				c, err := d.DialContext(ctx, "udp", net.JoinHostPort(s, "53"))
				if err == nil {
					return c, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	addrs, err := res.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		ips = append(ips, a.IP)
	}
	return ips, nil
}

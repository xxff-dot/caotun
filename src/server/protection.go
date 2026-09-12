package server

import (
	"encoding/json"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 认证失败 N 次封 IP，防爆破；并发上限防握手洪泛；max-gb 熔断保住按流量计费的账单
const (
	banThreshold = 10 // 认证失败 10 次封 IP（服务端换密码时旧客户端会短暂连错，阈值放宽防误伤）
	banDuration  = 30 * time.Minute
	banWindow    = 5 * time.Minute // 滑动窗口：距上次失败超过 5 分钟则重新计数
)

type banInfo struct {
	fails       int
	lastFail    time.Time
	bannedUntil time.Time
}

// 流量计数持久化结构（traffic.txt；旧版纯数字格式兼容读取，读取时周期从当下起算）
type quotaState struct {
	Total       int64     `json:"total"`
	WindowStart time.Time `json:"windowStart"`
}

type trafficServer struct {
	authV       atomic.Value // 认证密码 []byte（管理 API 可热轮换，逐连接读取）
	dir         string
	maxGB       float64
	quotaDays   int
	maxConns    int
	manualCert  string    // 手动证书路径（-cert，仅查询展示用）
	total       int64     // 本周期累计流量（字节）
	windowStart time.Time // 本周期起始时间
	active      int64     // 当前并发连接数
	mu          sync.Mutex
	bans        map[string]*banInfo
}

func newTrafficServer(auth []byte, dir string, maxGB float64, quotaDays, maxConns int) *trafficServer {
	s := &trafficServer{dir: dir, maxGB: maxGB, quotaDays: quotaDays, maxConns: maxConns,
		windowStart: time.Now(), bans: map[string]*banInfo{}}
	s.authV.Store(auth)
	// 加载历史流量计数：兼容 JSON 与旧版纯数字两种格式
	if b, err := os.ReadFile(filepath.Join(dir, "traffic.txt")); err == nil {
		var qs quotaState
		if json.Unmarshal(b, &qs) == nil && !qs.WindowStart.IsZero() {
			s.total = qs.Total
			s.windowStart = qs.WindowStart
		} else if v, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil {
			s.total = v
		}
	}
	if s.total > 0 {
		log.Printf("历史累计流量 %.2f GB（周期起始 %s）", float64(s.total)/1e9, s.windowStart.Format("2006-01-02 15:04"))
	}
	return s
}

// maybeRollover 配额周期到期则清零重新计数（每条新隧道接入前检查）
func (s *trafficServer) maybeRollover() {
	if s.quotaDays <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.windowStart) > time.Duration(s.quotaDays)*24*time.Hour {
		atomic.StoreInt64(&s.total, 0)
		s.windowStart = time.Now()
		log.Printf("流量周期（%d 天）已到，配额计数已清零重新开始", s.quotaDays)
		s.persistLocked()
	}
}

func (s *trafficServer) banned(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.bans[ip]
	return ok && time.Now().Before(b.bannedUntil)
}

func (s *trafficServer) recordFail(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// map 有界化：条目过多时清理过期记录，防全网扫描把 map 撑大
	if len(s.bans) > 4096 {
		now := time.Now()
		for k, b := range s.bans {
			if now.Sub(b.lastFail) > banDuration {
				delete(s.bans, k)
			}
		}
	}
	b := s.bans[ip]
	if b == nil {
		b = &banInfo{}
		s.bans[ip] = b
	}
	if !b.lastFail.IsZero() && time.Since(b.lastFail) > banWindow {
		b.fails = 0 // 距上次失败较久，重新计数
	}
	b.fails++
	b.lastFail = time.Now()
	if b.fails >= banThreshold {
		b.bannedUntil = time.Now().Add(banDuration)
		b.fails = 0
		log.Printf("IP %s 认证失败过多，封禁 %v", ip, banDuration)
	}
}

func (s *trafficServer) recordOK(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.bans, ip)
}

func (s *trafficServer) overQuota() bool {
	return s.maxGB > 0 && atomic.LoadInt64(&s.total) >= int64(s.maxGB*1e9)
}

// currentAuth 当前认证密码（管理 API 可热轮换，逐连接读取）
func (s *trafficServer) currentAuth() []byte { return s.authV.Load().([]byte) }

// setAuth 轮换认证密码：先落盘再热更新（新连接即刻生效，重启后沿用新密码）
func (s *trafficServer) setAuth(pass string) error {
	if err := os.WriteFile(filepath.Join(s.dir, "auth"), []byte(pass), 0600); err != nil {
		return err
	}
	s.authV.Store([]byte(pass))
	log.Printf("认证密码已轮换（新连接即刻生效）")
	return nil
}

// 每条隧道结束把累计流量落盘；崩溃最多丢一条连接的计量。
// 加锁串行化写文件，防多连接同时结束把 traffic.txt 写撕裂（撕裂=重启后配额清零）
func (s *trafficServer) persistTotal() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.persistLocked()
}

func (s *trafficServer) persistLocked() {
	b, _ := json.Marshal(quotaState{
		Total:       atomic.LoadInt64(&s.total),
		WindowStart: s.windowStart,
	})
	os.WriteFile(filepath.Join(s.dir, "traffic.txt"), b, 0600)
}

// countingConn 统计双向流量到 total
type countingConn struct {
	net.Conn
	s *trafficServer
}

func (c *countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	atomic.AddInt64(&c.s.total, int64(n))
	return n, err
}

func (c *countingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	atomic.AddInt64(&c.s.total, int64(n))
	return n, err
}

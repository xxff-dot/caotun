// Package core 移动端引擎核心:三端(鸿蒙/安卓/iOS)共用的纯 Go 实现。
// 平台导出层(鸿蒙 cgo / 安卓 gomobile / iOS gomobile)只做参数搬运。
package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"caotun/tun2sock"
	"caotun/tunnel"
)

var (
	mu      sync.Mutex
	cancel  context.CancelFunc
	running bool
	lastErr string
	logHook func(string) // 平台注入的日志钩子(如鸿蒙 hilog)
)

// SetLogHook 注入日志钩子;msg 为已格式化的一行日志
func SetLogHook(f func(string)) { logHook = f }

// StartTunCore 启动引擎:fd 为各平台 VPN 框架创建的 tun 文件描述符。
// 返回 0=成功, 1=已在运行, 2=参数无效
func StartTunCore(server, pass, dir string, fd, mtu int64, ws bool, protectPath, dialIP, cnPath, dns string) int64 {
	mu.Lock()
	defer mu.Unlock()
	if running {
		return 1
	}
	if server == "" || pass == "" || fd <= 0 {
		return 2
	}
	os.MkdirAll(dir, 0700) // TOFU 指纹与 CN 段表所在目录
	// 引擎日志落文件(各平台采集不到 .so 的 stderr),供排查
	if f, err := os.OpenFile(dir+"/engine.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600); err == nil {
		fmt.Fprintf(f, "=== %s ===\n", time.Now().Format("2006-01-02 15:04:05"))
	}
	dnsList := SplitCSV(dns)
	if len(dnsList) == 0 {
		dnsList = []string{"223.5.5.5"}
	}
	ctx, stop := context.WithCancel(context.Background())
	cancel = stop
	running = true
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "engine panic: %v\n", r)
			}
			mu.Lock()
			running = false
			mu.Unlock()
		}()
		dial := tunnel.NewDialer(server, pass, dir, false, ws, protectPath, dialIP)
		direct := tunnel.NewDirectDialer(protectPath)
		cnPath := filepath.Join(dir, "cn_cidr.txt")
		err := tun2sock.Serve(ctx, tun2sock.Options{
			FD:       int(fd),
			MTU:      int(mtu),
			Dial:     dial,
			Direct:   direct,
			CIDRPath: cnPath,
			DNS:      dnsList,
			Logf: func(f string, a ...any) {
				fmt.Fprintf(os.Stderr, "%s\n", fmt.Sprintf(f, a...)) // stderr 兜底;各平台可另行采集
			},
		})
		mu.Lock()
		running = false
		if err != nil {
			lastErr = err.Error()
		}
		mu.Unlock()
	}()
	return 0
}

// StopTunCore 停止引擎
func StopTunCore() {
	mu.Lock()
	defer mu.Unlock()
	if cancel != nil {
		cancel()
		cancel = nil
	}
}

// RunningCore 引擎是否运行中
func RunningCore() bool {
	mu.Lock()
	defer mu.Unlock()
	return running
}

// LastErrCore 最近一次引擎错误
func LastErrCore() string {
	mu.Lock()
	defer mu.Unlock()
	return lastErr
}

// SplitCSV 逗号分隔字符串 → 去空白列表
func SplitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if t := strings.TrimSpace(part); t != "" {
			out = append(out, t)
		}
	}
	return out
}

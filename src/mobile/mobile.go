//go:build cgo

// libcaotun.so 的 C 入口：鸿蒙 ArkTS 经 NAPI 桥调这里的导出函数。
// ArkTS 侧持有配置（扫码得到的 host:port、密码）并处理系统授权对话框，Go 侧只管隧道引擎 + tun2sock。
package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"

	"caotun/tun2sock"
	"caotun/tunnel"
)

var (
	mu      sync.Mutex
	cancel  context.CancelFunc
	running bool
	lastErr string
)

//export CaotunStartTun
func CaotunStartTun(server, pass, dir *C.char, fd, mtu, useWS C.int, protectPath, dialIP, cnPath *C.char) C.int {
	mu.Lock()
	defer mu.Unlock()
	if running {
		return 1 // 已在运行
	}
	serverAddr, auth, dirS := C.GoString(server), C.GoString(pass), C.GoString(dir)
	protectS, dialIPS := C.GoString(protectPath), C.GoString(dialIP)
	cnPathS := C.GoString(cnPath)
	os.MkdirAll(dirS, 0700) // TOFU 指纹文件所在目录
	// 引擎日志落文件(hilog 采集不到 .so 的 stderr),供 hdc file recv 排查
	if f, err := os.OpenFile(dirS+"/engine.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600); err == nil {
		log.SetOutput(f)
	}
	ctx, stop := context.WithCancel(context.Background())
	cancel = stop
	running = true
	go func() {
		dial := tunnel.NewDialer(serverAddr, auth, dirS, false, useWS != 0, protectS, dialIPS)
		direct := tunnel.NewDirectDialer(protectS)
		err := tun2sock.Serve(ctx, tun2sock.Options{
			FD:       int(fd),
			MTU:      int(mtu),
			Dial:     dial,
			Direct:   direct,
			CIDRPath: cnPathS, // CN 段表(VpnAbility 从 rawfile 拷到 cache)
			Logf: func(f string, a ...any) {
				msg := fmt.Sprintf(f, a...)
				log.Printf(f, a...) // stderr 兜底
				hiLogf(msg)         // 鸿蒙上经 hilog 输出(hdc shell hilog 可见)
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

//export CaotunStop
func CaotunStop() {
	mu.Lock()
	defer mu.Unlock()
	if cancel != nil {
		cancel()
		cancel = nil
	}
}

//export CaotunRunning
func CaotunRunning() C.int {
	mu.Lock()
	defer mu.Unlock()
	if running {
		return 1
	}
	return 0
}

//export CaotunLastError
func CaotunLastError() *C.char {
	mu.Lock()
	defer mu.Unlock()
	return C.CString(lastErr)
}

func main() {}

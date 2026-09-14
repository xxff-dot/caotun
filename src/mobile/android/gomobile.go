//go:build android

// 安卓导出层:纯 Go 函数经 gomobile bind 生成 caotun.aar,
// Kotlin 侧 caotun.mobile.Mobile.startTun(...) 直接调用。
package mobile

import "caotun/mobile/core"

// StartTun 启动引擎;fd 来自 Android VpnService.Builder.establish()。
// 返回 0=成功, 1=已在运行, 2=参数无效
func StartTun(server, pass, dir string, fd, mtu int64, ws bool, protectPath, dialIP, cnPath, dns string) int64 {
	return core.StartTunCore(server, pass, dir, fd, mtu, ws, protectPath, dialIP, cnPath, dns)
}

// StopTun 停止引擎
func StopTun() {
	core.StopTunCore()
}

// Running 引擎是否运行中
func Running() bool {
	return core.RunningCore()
}

// LastError 最近一次引擎错误(空 = 无)
func LastError() string {
	return core.LastErrCore()
}

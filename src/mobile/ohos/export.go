// 鸿蒙导出层:cgo //export 供 NAPI 桥(napi_init.cpp)调用。
// 字符串经 C 边界搬运;引擎逻辑在 core 包。
//go:build openharmony

package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"caotun/mobile/core"
)

//export CaotunStartTun
func CaotunStartTun(server, pass, dir *C.char, fd, mtu, useWS C.int, protectPath, dialIP, cnPath, dnsList *C.char) C.int {
	return C.int(core.StartTunCore(
		C.GoString(server), C.GoString(pass), C.GoString(dir),
		int64(fd), int64(mtu), useWS != 0,
		C.GoString(protectPath), C.GoString(dialIP), C.GoString(cnPath), C.GoString(dnsList)))
}

//export CaotunStop
func CaotunStop() {
	core.StopTunCore()
}

//export CaotunRunning
func CaotunRunning() C.int {
	if core.RunningCore() {
		return 1
	}
	return 0
}

//export CaotunLastError
func CaotunLastError() *C.char {
	return C.CString(core.LastErrCore())
}

func main() {}

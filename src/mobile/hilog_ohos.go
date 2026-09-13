//go:build openharmony

package main

/*
// hilog 库经 build-ohos.sh 的 CGO_LDFLAGS 以文件路径传入(NDK clang 的 -l 搜索在
// Windows 宿主 + OHOS sysroot 组合下不可用)
#include <stdlib.h>
#include <hilog/log.h>

static void hiLog(const char* msg) {
    OH_LOG_Print(LOG_APP, LOG_INFO, 0xC0A0, "CaotunEngine", "%{public}s", msg);
}
*/
import "C"

import "unsafe"

// hiLogf 引擎日志进 hilog(LOG_APP 域),hdc shell hilog 可见
func hiLogf(msg string) {
	cmsg := C.CString(msg)
	C.hiLog(cmsg)
	C.free(unsafe.Pointer(cmsg))
}

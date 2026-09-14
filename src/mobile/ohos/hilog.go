// hilog 桥:Go 引擎日志进鸿蒙系统日志域,hdc shell hilog 可采集
//go:build openharmony

package main

/*
#include <stdlib.h>
#include <hilog/log.h>

static void hiLog(const char* msg) {
    OH_LOG_Print(LOG_APP, LOG_INFO, 0xC0A0, "CaotunEngine", "%{public}s", msg);
}
*/
import "C"

import (
	"caotun/mobile/core"
	"unsafe"
)

func init() {
	core.SetLogHook(hiLogf)
}

func hiLogf(msg string) {
	cmsg := C.CString(msg)
	C.hiLog(cmsg)
	C.free(unsafe.Pointer(cmsg))
}

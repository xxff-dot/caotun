// NAPI 桥：ArkTS ↔ libcaotun.so(Go 引擎)。
// 只做参数搬运，业务全在 Go 侧（tunnel + tun2sock）。
#include "napi/native_api.h"
#include <cstdlib>

// libcaotun.so 导出的 C 接口（cgo //export）
extern "C" {
int CaotunStartTun(const char* server, const char* pass, const char* dir, int fd, int mtu, int useWS, const char* protectPath, const char* dialIP, const char* cnPath);
void CaotunStop(void);
int CaotunRunning(void);
char* CaotunLastError(void);
}

// 取字符串参数（两段式：先取长度再取值）
static bool GetStr(napi_env env, napi_value v, char* out, size_t cap) {
    size_t n = 0;
    if (napi_get_value_string_utf8(env, v, nullptr, 0, &n) != napi_ok) return false;
    if (n >= cap) return false;
    return napi_get_value_string_utf8(env, v, out, cap, &n) == napi_ok;
}

static napi_value StartTun(napi_env env, napi_callback_info info) {
    size_t argc = 9;
    napi_value args[9];
    napi_get_cb_info(env, info, &argc, args, nullptr, nullptr);
    if (argc < 6) {
        napi_throw_error(env, nullptr, "startTun 需要 6 个参数");
        return nullptr;
    }
    char server[512] = {0}, pass[256] = {0}, dir[512] = {0}, protectPath[512] = {0}, dialIP[64] = {0}, cnPath[512] = {0};
    int32_t fd = 0, mtu = 0, ws = 0;
    if (!GetStr(env, args[0], server, sizeof(server)) ||
        !GetStr(env, args[1], pass, sizeof(pass)) ||
        !GetStr(env, args[2], dir, sizeof(dir)) ||
        napi_get_value_int32(env, args[3], &fd) != napi_ok ||
        napi_get_value_int32(env, args[4], &mtu) != napi_ok ||
        napi_get_value_int32(env, args[5], &ws) != napi_ok ||
        !GetStr(env, args[6], protectPath, sizeof(protectPath)) ||
        !GetStr(env, args[7], dialIP, sizeof(dialIP)) ||
        !GetStr(env, args[8], cnPath, sizeof(cnPath))) {
        napi_throw_error(env, nullptr, "参数类型错误");
        return nullptr;
    }
    int code = CaotunStartTun(server, pass, dir, fd, mtu, ws, protectPath, dialIP, cnPath);
    napi_value res;
    napi_create_int32(env, code, &res);
    return res;
}

static napi_value StopTun(napi_env env, napi_callback_info info) {
    CaotunStop();
    return nullptr;
}

static napi_value Running(napi_env env, napi_callback_info info) {
    napi_value res;
    napi_create_int32(env, CaotunRunning(), &res);
    return res;
}

static napi_value LastError(napi_env env, napi_callback_info info) {
    char* err = CaotunLastError();
    napi_value res;
    napi_create_string_utf8(env, err ? err : "", NAPI_AUTO_LENGTH, &res);
    if (err) free(err); // cgo 侧 C.CString 分配,调用方负责 free
    return res;
}

EXTERN_C_START
static napi_value Init(napi_env env, napi_value exports) {
    napi_property_descriptor desc[] = {
        {"startTun", nullptr, StartTun, nullptr, nullptr, nullptr, napi_default, nullptr},
        {"stopTun", nullptr, StopTun, nullptr, nullptr, nullptr, napi_default, nullptr},
        {"running", nullptr, Running, nullptr, nullptr, nullptr, napi_default, nullptr},
        {"lastError", nullptr, LastError, nullptr, nullptr, nullptr, napi_default, nullptr},
    };
    napi_define_properties(env, exports, sizeof(desc) / sizeof(desc[0]), desc);
    return exports;
}
EXTERN_C_END

static napi_module caotunModule = {
    .nm_version = 1,
    .nm_flags = 0,
    .nm_filename = nullptr,
    .nm_register_func = Init,
    .nm_modname = "entry",
    .nm_priv = nullptr,
    .reserved = {0},
};

extern "C" __attribute__((constructor)) void RegisterCaotunModule(void) {
    napi_module_register(&caotunModule);
}

# libcaotun.so 构建指南

`libcaotun.so` 是鸿蒙客户端的 Go 引擎(隧道协议 + tun2sock + CN 分流),
由 `src/mobile/` 经 cgo 交叉编译而成,被 ArkTS 经 NAPI 调用。

> 只改 ArkTS/UI/配置的开发者**不需要**本文档:`libcaotun.so` 已随仓库提交,
> 直接用 DevEco 打 HAP 即可。本文面向需要**修改 Go 引擎**后重新生成 .so 的开发者。

## 为什么不能直接用上游 Go 交叉编译

| 问题 | 说明 |
|---|---|
| IE-TLS 闪退 | 上游 Go 的 c-shared 在 arm64 把 runtime TLS 写死为 initial-exec 模型(`STATIC_TLS` 标志),鸿蒙 musl libc 拒绝在 dlopen 场景解析,报 `initial-exec TLS resolves to dynamic definition`,进程秒崩 |
| 官方无解 | 上游 issue golang/go#54805,修复 PR #75048 未合入;`-ftls-model` 等链接参数无效(IE 来自 runtime 汇编,不在 C 侧) |
| 唯一通路 | OpenHarmony-SIG 官方 fork **ohos_golang_go**:`GOOS=openharmony` 用 TLSDESC 重定位,musl 完全支持(sing-box#3681 同款问题的解) |

因此 Go 工具链必须用 fork,且构建需 4 个组件。`scripts/shell/setup-ohos-toolchain.sh`
把它们全部自动化(幂等,可重复执行):

```
sh scripts/shell/setup-ohos-toolchain.sh   # 一键准备(首次含工具链编译 5-15 分钟)
sh scripts/shell/build-ohos.sh             # 产出 libcaotun.so(约 1 分钟)
```

## 四个组件

| 组件 | 位置 | 来源 | 用途 |
|---|---|---|---|
| ohos-go 工具链 | `tools/ohos-go124/` | OpenHarmony-SIG fork `release-branch.go1.24`(源码自举,setup 脚本自动做) | `GOOS=openharmony` 编译,TLSDESC |
| gvisor(vendor) | `tools/gvisor/` | sagernet fork `v0.0.0-20250811-sing-box-mod.1`(sing-box 生产验证),go 指令压到 1.24 | tun 网卡 → 用户态 TCP/IP 栈 |
| hilog 链接库 | `tools/hiloglib/libhilog_ndk.z.so` | DevEco NDK sysroot 复制(Windows clang `-l` 搜索不可用,改为文件路径直传) | 引擎日志进 hilog |
| OHOS NDK | DevEco Studio 自带 | `.../sdk/default/openharmony/native` | clang + sysroot |

> gvisor 细节:上游 gvisor.dev 不可用(包名损坏 + 缺 bazel 生成物);
> sagernet fork go 指令为 1.25,fork 工具链是 1.24,故 vendor 副本把 go 指令与
> x/sys、x/time 压到 1.24 可用版本。`src/go.mod` 经 `replace` 指向 `tools/gvisor`。

## Android / iOS 对照

- **Android**:上游官方 Go + gomobile bind 出 `.aar`(bionic 允许 IE-TLS dlopen,不需要 fork);
  VpnService + protect(fd) 与鸿蒙同构
- **iOS**:gomobile `-target=ios` 出 XCFramework,需 macOS + Xcode + 付费开发者账号;
  NEPacketTunnelProvider 不提供 tun fd,需原生 socketpair 桥接一端给引擎

## 验证产物(构建后必查)

```bash
llvm-readelf -d libcaotun.so | grep -iE "STATIC_TLS|SONAME"
llvm-readelf -r libcaotun.so | grep -c TLSDESC
```

- `STATIC_TLS` 必须**不存在**(存在 = 用错工具链,真机必闪退)
- `SONAME` 必须是 `libcaotun.so`(缺了 DT_NEEDED 会嵌入绝对路径)
- TLS 重定位应为 `R_AARCH64_TLSDESC`

真机验证:`hdc shell hilog | grep CaotunEngine` 应看到 `tun2sock 引擎启动`,
且无 `initial-exec TLS` 报错。

## FAQ(踩坑对照)

| 症状 | 原因 |
|---|---|
| `initial-exec TLS resolves to dynamic definition` | 用了上游 Go 编 .so → 换 ohos-go fork |
| `unable to find library -lhilog_ndk.z.so` | Windows 宿主 clang 的 `-l` 搜索不可用 → 走 build-ohos.sh 的文件路径直传 |
| `argument unused during compilation: '-L...'` | `-L` 放进了 CGO_CFLAGS(编译期未用即 -Werror)→ 必须放 CGO_LDFLAGS |
| clang 报路径含空格拆词 | CC 路径转 8.3 短路径(build-ohos.sh 已处理) |
| `make.bat` 报"不是内部或外部命令" | 环境含 `NoDefaultCurrentDirectoryInExePath`,cmd 不搜当前目录 → `call .\make.bat` 显式路径 |
| 真机 `Cannot read property running of undefined` | .so 加载失败,先查上面两项 |
| gvisor 编译报缺 `MaskOf64` 等 | 上游 gvisor zip 缺 bazel 生成物 → 必须用 sagernet fork |

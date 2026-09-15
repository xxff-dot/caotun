# caotun 安卓客户端

与鸿蒙端共用同一 Go 引擎(隧道协议 + tun2sock fake-ip 白名单分流),安卓侧用**上游官方 Go +
gomobile bind** 生成 `caotun.aar`(安卓 bionic 允许 IE-TLS 的 dlopen,不存在鸿蒙的
IE-TLS 闪退问题,因此**不需要** ohos-go fork)。

**预构建产物 `app/libs/caotun.aar` 已随仓库提交**——只是使用/改 Kotlin 层的话,用
Android Studio 构建即可,无需安装 gomobile;改了 Go 引擎代码才需要重新出 AAR(见下)。

## 构建步骤

1. 生成 Go 绑定库(需要 Android NDK + gomobile,见 `scripts/shell/build-android.sh` 头部注释):

   ```bash
   sh scripts/shell/build-android.sh    # 产出 app/libs/caotun.aar
   ```

   注意:gomobile 要求 go.mod 含 `golang.org/x/mobile`(需临时 `go get` 升版构建,
   完成后 `git checkout -- src/go.mod src/go.sum` 还原,鸿蒙工具链要求 go 1.24)。
   详细流程见 [docs/build.md](../../docs/build.md)。

2. 用 Android Studio 打开 `mobile/android/`,或命令行构建(需要 JDK 17/21):

   ```bash
   gradle assembleDebug    # 产物 app/build/outputs/apk/debug/app-debug.apk
   ```

3. `adb install -r app-debug.apk` 安装到真机/模拟器(API 29+)。

## 使用流程(与鸿蒙端一致)

1. PC 面板「手机扫码导入」显示二维码;
2. App「扫码接入」→ 地址密码自动导入 → 弹系统 VPN 授权 → 允许 → 全局代理生效;
3. 首页大圆钮 = 连接/断开开关;服务器列表支持多配置/编辑/删除/切换;
4. 「代理域名」卡片 = 走隧道的白名单(一行一个,默认 8 项,清空恢复默认);「直连 DNS」卡片 = 白名单之外域名的解析上游;
5. 详细使用说明见 [docs/mobile-usage.md](../../docs/mobile-usage.md),分流原理见 [docs/architecture.md](../../docs/architecture.md)。

## 架构对照(与鸿蒙端)

| 功能 | 鸿蒙 | 安卓 |
|---|---|---|
| VPN 接口 | VpnExtensionAbility | VpnService |
| tun fd | VpnConnection.create() | Builder.establish().detachFd() |
| DNS 哨兵 | 10.111.0.2(/30 对端) | 10.111.0.2(/30 对端,必须用对端地址,本机地址的包不进 tun) |
| 防回环 | VpnConnection.protect(fd) | 路由层排除 CN/内网段 + 服务器 IP(/16 聚合防 Binder 超限) |
| 引擎调用 | NAPI(libcaotun.so,cgo) | gomobile 绑定(纯 Go,无 cgo) |
| CN 裸 IP 直连判定 | 内嵌 cn_cidr.txt(引擎内) | 同左(外置 rawfile 仅路由聚合用) |
| 扫码 | ScanKit | zxing-embedded |
| 引擎日志 | hilog(--domain 0xC0A0) | run-as 读 cache/caotun/engine.log |

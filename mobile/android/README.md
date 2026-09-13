# caotun 安卓客户端

与鸿蒙端共用同一 Go 引擎(隧道协议 + tun2sock + CN 分流),安卓侧用**上游官方 Go +
gomobile bind** 生成 `caotun.aar`(安卓 bionic 允许 IE-TLS 的 dlopen,不存在鸿蒙的
IE-TLS 闪退问题,因此**不需要** ohos-go fork)。

## 构建步骤

1. 生成 Go 绑定库(需要 Android NDK + gomobile,见 `scripts/shell/build-android.sh` 头部注释):

   ```bash
   sh scripts/shell/build-android.sh    # 产出 app/libs/caotun.aar
   ```

2. 用 Android Studio 打开 `mobile/android/`(首次会自动生成 gradle wrapper 并同步依赖);
   或命令行 `./gradlew assembleDebug`(需要 JDK 17+)。

3. `Run` 安装到真机/模拟器(API 29+)。

## 使用流程(与鸿蒙端一致)

1. PC 面板「手机扫码导入」显示二维码;
2. App「扫码接入」→ 地址密码自动导入 → 弹系统 VPN 授权 → 允许 → 全局代理生效;
3. 首页大圆钮 = 连接/断开开关;服务器列表支持多配置/编辑/删除/切换;
4. DNS 上游可在首页 DNS 卡片修改(默认 223.5.5.5,多个逗号分隔按序尝试)。

## 架构对照(与鸿蒙端)

| 功能 | 鸿蒙 | 安卓 |
|---|---|---|
| VPN 接口 | VpnExtensionAbility | VpnService |
| tun fd | VpnConnection.create() | Builder.establish().detachFd() |
| 防回环 | VpnConnection.protect(fd) | VpnService.protect(fd) |
| 引擎调用 | NAPI(libcaotun.so,cgo) | gomobile 绑定(纯 Go,无 cgo) |
| CN 分流 | 同一份 cn_cidr.txt + 引擎逻辑 | 同左 |
| 扫码 | ScanKit | zxing-embedded |

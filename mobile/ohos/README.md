# caotun 鸿蒙客户端（HarmonyOS NEXT）

手机端与桌面共用 Go 核心：本工程是 DevEco Studio 壳（ArkTS UI + VpnExtension + NAPI 桥），
隧道引擎与 tun2sock 编译为 `entry/libs/arm64-v8a/libcaotun.so`。

## 构建步骤

1. **一键准备全部依赖**（ohos-go fork 工具链、gvisor vendor、hilog 库；幂等可重复执行）：

   ```
   sh scripts/shell/setup-ohos-toolchain.sh
   ```

   原理与手动步骤、踩坑 FAQ 见 [docs/build-libcaotun.md](../../docs/build-libcaotun.md)。

2. 编译 Go 引擎（脚本自动找 DevEco 的 OHOS NDK 与 ohos-go）：

   ```
   sh scripts/shell/build-ohos.sh
   ```

   验证产物（关键）：`llvm-readelf -d` 不得出现 `STATIC_TLS`，`-r` 应为 `R_AARCH64_TLSDESC`。

3. 用 DevEco Studio 打开 `mobile/ohos/`，等 hvigor 同步完成。
   - 若模板版本字段与你的 DevEco 不匹配，按 IDE 提示改 `build-profile.json5` 的 `compatibleSdkVersion` 即可；
   - 若提示不识别 `"type": "vpn"`，按官方文档在 SDK 的 `toolchains/modulecheck/module.json`
     给 extensionAbilities 的 type 枚举补 `"vpn"` 后清缓存重启（官方指南原话）。

4. 真机运行（API 12+ 的鸿蒙 NEXT 手机）。

## 使用流程（全自动）

1. PC 面板（`caotun web` → http://127.0.0.1:21877）点「手机扫码导入 → 显示二维码」；
2. 手机 App 点「扫码接入」对准二维码——地址、密码自动导入，扫码完成立即拉起 VPN；
3. 首次连接系统弹一次 VPN 授权框，点「允许」，之后状态栏出现钥匙图标即全局代理生效。

## 已知边界

- VPN 服务随调用方进程存活：测试阶段请保持 App 在后台不被系统杀掉（后台任务保活后续再接）；
- 仅放行 TCP + DNS(UDP:53 转 DNS-over-TCP 走隧道)，QUIC 等其他 UDP 丢弃（促其降级 TCP）；
- ICMP（ping）不经隧道，属预期；
- 上架应用市场需通过华为对 VPN 权限的审核；自用调试签名即可。

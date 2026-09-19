# CLAUDE.md

单二进制加密 TCP 转发隧道。`server` / `client` / `web` 三模式共用 `src/main.go` 分发；移动端（安卓/鸿蒙）共用 Go 引擎 `src/mobile/core` + `src/tun2sock`。注释、日志、文档均为中文。

## 常用命令

```bash
cd src
go build ./... && go vet ./... && go test ./...     # 全部在 src/ 下执行
go run . server        # 服务端(默认 :443, WS :8443)
go run . client -server-addr host:443 -auth 密码   # 本地 SOCKS5 :21878
go run . client -server-addr host:443 -auth 密码 -sysproxy all   # 全局智能分流:国内直连,其余带域名走隧道
go run . web           # 管理面板 :21877
```

移动端构建/部署/真机验证：见 `docs/build.md`（含 gomobile、hvigor、签名、坑位）。产物 `caotun.aar` / `libcaotun.so` 已预构建入库，新用户免装工具链。

## 架构

详细设计：`docs/architecture.md`；故障排查：`docs/troubleshooting.md`。

- **分流模型（fake-ip 白名单）**：App 的 DNS 查询进 tun，命中白名单（`src/proxylist`）秒回假 IP（198.18.0.0/15），App 连假 IP 时反查域名、**域名原文进隧道**由 VPS 解析；未命中转发国内 DNS 解析真 IP 直连。本机无被污染解析。
- **自环防线**：直连拨号只允许 CN/内网 IP（`CNMatcher.DirectOK`），哨兵网段 10.111.0.0/30 的非 DNS TCP 直接拒绝（DoT 853 自环曾拖死引擎）。改分流逻辑必须保持这条防线。
- **隧道协议**：TLS（TOFU 指纹兜底）→ nonce/HMAC → 目标地址（ATYP 支持 IPv4/域名）→ **1 字节状态码** → 裸流。WS 模式多一层 WebSocket（CDN 穿透）。新增服务端分支必须先回状态码再读数据，否则协议死锁。
- **目标端口 53**：服务端 `dnsrelay.go` 本地 UDP 查询 + 60s 缓存。
- **服务端**：`protection.go`（IP 封禁/密码轮换/配额熔断）、`admin.go`（`/_admin/*`，web 面板经 HTTPS 调用）。
- **CN 段表**内嵌 `tun2sock/cn_cidr.txt`（go:embed），用于裸 IP 直连判定；`Options.CIDRPath` 可选覆盖。

## 目录

```
src/tun2sock/      移动端引擎:fake-ip、DNS 劫持、TCP 分流、tun 读写(gvisor)
src/tunnel/        桌面/移动客户端:SOCKS5、隧道拨号器、TOFU
src/server/        服务端:握手、转发、dnsRelay、防护、管理 API
src/web/           管理面板(//go:embed web.html)+桌面 PAC 白名单
src/proxylist/     代理域名白名单(默认列表+后缀匹配)
src/mobile/        core(共用引擎核心)/ohos(cgo 导出)/android(gomobile 导出)
mobile/android     安卓工程(VpnService+Compose,/libs/caotun.aar 已入库)
mobile/ohos        鸿蒙工程(VpnExtensionAbility+NAPI,/libs .so 已入库)
```

## 模式间耦合点（改动需同步考虑）

- **密码**：两端一致，默认 `~/.caotun/auth`；面板可经管理 API 热轮换
- **白名单**：两端 UI 编辑 `cache/caotun/proxy_domains.txt`（一行一个后缀），断开重连生效；清空保存 = 恢复默认
- **DNS 哨兵**：tun /30 的**对端地址**（10.111.0.2，两端一致）。用本机地址包不进 tun，引擎收不到 DNS
- **client 子进程**：面板拉起/接管 client（pid 文件）；系统代理 PAC 用 `web.DefaultProxyDomains()`

## 平台注意

- 全仓库强制 LF（`.gitattributes`）；Windows 下禁用 sed/awk 改文件，用 Edit 工具
- 服务端生产跑 Linux VPS（`caotun.service`，二进制 `/root/caotun/caotun`）；重传部署后记得 `chmod +x`
- 安卓调试：debug 包 `run-as com.caotun.app` 可读 `cache/caotun/engine.log`；鸿蒙：`hilog -x --domain 0xC0A0`
- 真机 UI 自动化前先截图确认真实状态（preferences 残留会显示假状态）

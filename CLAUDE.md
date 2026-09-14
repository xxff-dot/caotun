# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目概述

单二进制加密 TCP 转发隧道（服务端 / 客户端 / Web 管理面板是同一个程序，`server` / `client` / `web` 子命令区分）。服务端/桌面端除 `golang.org/x/crypto/acme/autocert` 外全部标准库；移动端（仅 `tun2sock/` 包）依赖 `github.com/sagernet/gvisor` 用户态 TCP/IP 栈。正式证书由服务器上的 `scripts/shell/issue-cert.sh`（acme.sh 定时任务）签发，服务端只认证书文件并自动热加载。注释、日志、README 均为中文。

## 常用命令

**go.mod 在 `src/` 下，所有 go 命令都在 `src/` 目录执行：**

```bash
cd src
go build ./...                 # 编译
go vet ./...                   # 静态检查
go test ./ws/                  # 单元测试（WS 帧编解码）
go test -run TestWSLive ./tunnel/ -v   # 真机全链路测试，需要环境变量：
                               # WS_ADDR=pdl.example.com:8443 WS_PASS=密码
```

打包发布（三平台编译 + UPX + 收集脚本到 dist/）：

```bash
sh scripts/shell/build.sh            # Linux / macOS
scripts\windows\build.bat             # Windows
```

运行（开发期手动起服务端/客户端/面板）：

```bash
go run . server          # 海外 VPS：TLS 隧道 + WS 接入
go run . client -server-addr host:443 -auth 密码   # 本地 SOCKS5+HTTP CONNECT 代理（默认 127.0.0.1:21878）
go run . web             # 管理面板 127.0.0.1:21877
```

参数全集见 README「参数」一节；`scripts/client.conf`、`scripts/server.conf` 是启停脚本用的 KEY=VALUE 配置。

## 架构

三种模式共用一个 `main.go` 分发（`src/main.go`），共享数据目录 `~/.caotun/`（密码文件 `auth`、TOFU 指纹 `fingerprint.txt`、证书、流量计数 `traffic.txt`、面板配置 `web.json`、日志）。

### 包职责（src/ 下，约 2400 行）

- `protocol/` — 隧道协议的地址编解码（SOCKS5 风格 `ATYP + addr + port` 大端）
- `ws/` — 手写最小 WebSocket(RFC6455)，仅二进制帧 ↔ net.Conn 适配（单帧不分段）
- `tunnel/` — 客户端：本地代理（SOCKS5 与 HTTP CONNECT 同端口，首字节识别）、TLS 双模式拨号、TOFU 指纹；`Serve(ctx,...)` 是可取消的代理核心，`NewDialer` 生成「目标→隧道」拨号器（桌面 `Run` 与移动端共用）
- `tun2sock/` — 移动端专用：tun 网卡 fd → gvisor 用户态栈；TCP 全量走隧道、UDP 仅劫持 DNS(53) 转 DNS-over-TCP、其余 UDP 丢弃（`// ponytail` 注释标明扩展点）。gvisor 经 `replace` 指向 `tools/gvisor`（sagernet fork 的 vendor 副本，go 指令压到 1.24 以兼容 ohos-go 工具链；`tools/` 已 gitignore，换机需重放：从模块缓存复制 `gvisor@v0.0.0-20250811-sing-box-mod.1` 并按下述 go.mod 改写）
- `mobile/` — cgo 入口（`//go:build cgo`，编 `libcaotun.so`）：`CaotunStartTun/CaotunStop/CaotunRunning/CaotunLastError` 四个 C 导出，供鸿蒙 NAPI 桥调用；`sh scripts/shell/build-ohos.sh` 用 OHOS NDK clang 交叉编译
- `server/` — 服务端：证书两模式（默认 数据目录 `fullchain.pem` 文件热加载 + 自签兜底、手动 `-cert/-key`）、`admin.go` 管理 API（`/_admin/*`：证书查看/密码轮换/流量，`X-Auth` 鉴权）、`handleServerConn` 握手认证、转发、WS 接入；`protection.go` 为 IP 封禁（5 分钟滑窗 10 次失败封 30 分钟）、密码热轮换与流量配额熔断
- `sysproxy/` — 系统代理平台拆分：Windows 注册表直写 / macOS networksetup / Linux GNOME gsettings；共用本地 PAC 服务（`sysproxy.go`）；非 Windows 为 `//go:build !windows`
- `web/` — 管理面板：`web.html` 经 `//go:embed` 内嵌进二进制（`qrcode.js` 为手机扫码导入的内嵌二维码库）；HTTP API 在 `web_api.go`，服务端操作经 HTTPS 转发到服务端管理 API（无 SSH）；负责拉起/接管 client 子进程（`client.pid`）、管理系统代理；「手机扫码导入」卡片生成 `caotun://host:port?p=密码&ws=0|1&dns=DNS上游CSV` 二维码
- `mobile/ohos/` — 鸿蒙 NEXT 客户端 DevEco 工程（在仓库根 `mobile/` 下，不在 src/）：ArkTS 扫码页（ScanKit）+ `VpnExtensionAbility`（type: "vpn"，建 tun 网卡拿 fd）+ NAPI 桥（`entry/src/main/cpp/`）；构建与已知边界见 `mobile/ohos/README.md`

### 隧道协议（每条连接一次握手）

1. TCP + TLS（客户端先按标准 CA 校验，失败自动回退 TOFU 指纹比对）
2. 【仅 `-ws` 模式】TLS 之上再发 WebSocket Upgrade（`GET /tf`），流量封装为 WS 二进制帧——这是 CDN 模式（可过 Cloudflare 七层代理）
3. 服务端 → 32 字节随机 nonce；客户端 → `HMAC-SHA256(密码, nonce)`
4. 客户端 → 目标地址（`protocol/` 编码）；服务端 → 1 字节状态码（0 成功 / 1 目标连不上 / 2 配额用完）
5. 双向裸流转发，流量计入服务端配额

关键函数：服务端握手在 `src/server/server.go` `handleServerConn`，客户端在 `src/tunnel/client.go` `finishTunnel`。

### 模式间耦合点（改动需连带考虑）

- **密码**：两端一致，默认读写同一路径 `~/.caotun/auth`（服务端首次启动自动生成随机密码）；面板可经管理 API 热轮换（服务端写回 auth 文件 + 面板自动重启客户端）
- **证书指纹**：服务端换证书模式后客户端报"指纹变化"，需删 `fingerprint.txt`
- **client 子进程**：面板与 client 是父子进程 + PID 文件接管孤儿；client 的 `-sysproxy` 参数与面板的系统代理接管/还原逻辑（注册表备份恢复）互相配合，改动需两端同步
- **WS 模式**：client `-ws` 必须对接服务端 `-ws-port`（默认 8443）；直连走服务端 `-port`（默认 443），两条线路可同时可用

### 平台注意

- 仅 Windows/macOS/Linux 桌面支持 `-sysproxy`（WSL2/无桌面环境运行时自动跳过）；服务端代码同 Linux，生产跑在 Linux VPS
- 全仓库强制 LF（`.gitattributes`）

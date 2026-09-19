# 架构

单二进制加密 TCP 转发隧道。本文覆盖全部组件：桌面三模式、服务端子系统、移动端引擎（安卓/鸿蒙）、协议、数据面与安全模型。构建部署见 [build.md](build.md)，故障排查见 [troubleshooting.md](troubleshooting.md)。

## 1. 总体结构

同一个二进制（Go，桌面端零第三方依赖），`src/main.go` 按子命令分发：

```
┌─────────────────────────┐   TLS(→WS/CDN 可选)   ┌────────────────────────────┐
│ client（本地电脑/手机）    │ ────────────────────→ │ server（海外 VPS）           │ ──→ 任意目标 host:port
│ 桌面: 127.0.0.1:21878    │   nonce/HMAC 认证      │ :443 TLS 直连                │   （域名服务端解析）
│ SOCKS5+HTTP CONNECT      │   目标 ATYP+addr+port  │ :8443 WS 接入 + /_admin API │
│ 手机: tun 全局接管        │   ← 1 字节状态码       └────────────────────────────┘
└─────────────────────────┘
┌─────────────────────────┐
│ web（管理面板，桌面）      │ 127.0.0.1:21877：拉起/接管 client 子进程、系统代理管理、
│ 同一个二进制              │ 经服务端 /_admin API 查询流量/证书/轮换密码（无需 SSH）
└─────────────────────────┘
```

版本号唯一出处：`src/main.go` 的 `AppVersion`（`caotun -v` 查看），与安卓 `build.gradle.kts`、鸿蒙 `AppScope/app.json5` 保持一致。

## 2. 分流模型（fake-ip 白名单，移动端核心）

```
App 问 DNS（系统解析器 → tun 哨兵 10.111.0.2:53）
  → 引擎查白名单（proxylist 后缀匹配）：
     命中  → 秒回假 IP（198.18.0.0/15，零上游查询）→ App 连假 IP
             → 引擎反查域名 → 域名原文进隧道（ATYP=domain）→ VPS 解析
     未命中 → 查询原文转发国内上游（DirectResolvers，默认 223.5.5.5，直连）
             → 真 IP 回给 App → App 直连（不经 VPS）
```

- 白名单：`src/proxylist` 默认 8 项（github/google/googleapis/gstatic/youtube/huggingface/android/dl.google.com），后缀匹配（点边界）；App「代理域名」编辑框写 `cache/caotun/proxy_domains.txt`（一行一个），**存在即完整替换默认**；清空保存 = 恢复默认；断开重连生效
- 白名单域名的 AAAA/HTTPS 查询回 NODATA——逼应用走 IPv4 + 明文 SNI（v4-only VPN 标准做法，防 ECH/IPv6 旁路）
- CN 段表（`cn_cidr.txt` go:embed，数据源 gaoyifan/china-operator-ip）仅用于**裸 IP** 的直连判定（`CNMatcher.DirectOK`）
- 防自环（关键不变量）：**直连拨号只允许确定在 VPN 路由之外的地址**（CN 公网/内网，`DirectOK`）；哨兵网段 10.111.0.0/30 的非 DNS TCP（典型 DoT:853）直接拒绝促回落；其余一律走隧道。违反即无限嵌套自环拖死引擎

## 3. 桌面客户端（src/tunnel）

- **本地代理**：单端口同时支持 SOCKS5 与 HTTP CONNECT（首字节识别），经智能分流路由器（`NewRouter`，route.go）统一决策
- **智能分流**（route.go）：白名单域名强制走隧道；其余域名用国内 DNS（223.5.5.5 等，2s 超时，结果 TTL 缓存 10min）仅做 CN 判定——**判定用的 IP 从不用于拨号**，非 CN 域名把原文递给服务端境外解析（污染假 IP 无害）。CN/内网 IPv4 字面量直连，非 CN IPv4 与 IPv6 字面量走隧道。解析失败兜底走隧道
- **隧道拨号**（`NewDialer`）：TLS → nonce/HMAC → 目标（SOCKS5 传入的域名/IP 原样走 ATYP）→ 等状态码
- **TOFU**：先标准 CA 校验，失败回退自签 + SHA-256 指纹比对（`fingerprint.txt`）；线路切换自动重置指纹
- **系统代理**（`sysproxy/`）：`-sysproxy pac|all|off`，Windows 注册表 / macOS networksetup / Linux gsettings，启动接管退出精确还原；`all` = 全局代理 + 客户端内智能分流（国内直连）；PAC 白名单默认 100+ 常用国外域名（`web.DefaultProxyDomains`，面板可增删）
- **本地 DNS 转发**（可选 `-dns`）：`127.0.0.1:53` 作系统 DNS，查询经隧道由服务端出口解析；服务器域名自动直连解析防回环

## 4. Web 管理面板（src/web）

- 拉起/接管 client 子进程（pid 文件，孤儿可接管）；面板强杀后重开显示「已运行(待接管)」
- 系统代理由面板进程托管：启动备份原值 → 写入系统设置 + 起 PAC 服务（21879），退出精确还原
- 配置存 `~/.caotun/web.json`；面板仅绑 127.0.0.1，API 校验 Origin/Host 防 CSRF
- 服务端操作（流量/证书/密码轮换）经 HTTPS 转发到服务端 `/_admin/*`，凭证即认证密码，**面板不需要 SSH**

## 5. 服务端（src/server）

- **接入分流**：同一 TLS 端口按路径分——`/tf` = WS 隧道，`/_admin/*` = 管理 API，其余直连 TLS 隧道
- **握手流水线**（`handleServerConn`）：keepalive → 15s 限时 → nonce → HMAC → 配额熔断检查 → 读目标(ATYP) → 【端口 53 → `dnsRelay`；其余 → 拨目标】→ 回状态码 → 双向 relay。**状态码必须在读应用数据前发出**，否则协议死锁
- **dnsRelay**（端口 53 隧道）：上游 UDP 查询（默认 8.8.8.8,1.1.1.1，境外出口天然防污染）+ 60s 缓存；AAAA/HTTPS 回 NODATA
- **证书三模式**：数据目录 `fullchain.pem`（issue-cert.sh/acme.sh 签发，文件变化热加载）｜手动 `-cert/-key`｜自签 ECDSA P-256 兜底（TOFU）
- **防护**（`protection.go`）：失败 10 次/5 分钟封 IP 30 分钟（握手前断开，连接中断不计入）；`-max-conns` 并发上限；15s 握手限时；keepalive 回收半开连接；`-max-gb`/`-quota-days` 流量熔断（`traffic.txt` 落盘）
- **WS 接入**（`serveWS`）：`GET /tf` 升级校验 `Sec-WebSocket-Accept`，之后二进制帧 ↔ 裸流（`src/ws`，单帧不分段）

## 6. 移动端引擎（src/mobile/core + src/tun2sock）

三端（安卓/鸿蒙/iOS）共用纯 Go 核心，平台层只做参数搬运与 VPN fd 创建：

| | 安卓 | 鸿蒙 |
|---|---|---|
| VPN 框架 | VpnService（`TunService.kt`） | VpnExtensionAbility（`VpnAbility.ets`） |
| 引擎接入 | gomobile `caotun.aar`（`src/mobile/android`） | cgo `libcaotun.so`（`src/mobile/ohos`，NAPI 桥） |
| DNS 哨兵 | `addDnsServer("10.111.0.2")` | `dnsAddresses: ['10.111.0.2']` |
| 直连防自环 | 路由层排除 CN/内网段（/16 聚合防 Binder 超限）+ 服务器 IP /32 | protect socket（`protect.sock` Unix 域握手） |
| 引擎日志 | `cache/caotun/engine.log`（run-as 读） | `hilog --domain 0xC0A0`（logHook） |

- **tun 读写**（`tunEndpoint`）：阻塞读 fd 分发进 gvisor；写包 EAGAIN 重试（部分平台 fd 非阻塞）
- **白名单持久化**：`cache/caotun/proxy_domains.txt`；**服务器预解析**：VPN 拉起前解析服务器域名落盘，防激活后 DNS 依赖自身
- **UDP 策略**：仅劫持 53，其余 UDP 拒绝（ICMP Port Unreachable）促 QUIC 降级 TCP

## 7. 数据目录与端口

`~/.caotun/`（环境变量 `CAOTUN_HOME` 可覆盖）：

| 文件 | 归属 | 说明 |
|---|---|---|
| `auth` | 服务端/客户端 | 认证密码（两端一致；服务端首次启动自动生成） |
| `fingerprint.txt` | 客户端 | TOFU 证书指纹 |
| `web.json` | 面板 | 面板配置（线路/密码/PAC 白名单/代理 IP） |
| `client.pid` / `client.log` 等 | 各模式 | 子进程接管 / 日志（5MB 轮转） |
| `fullchain.pem` 等证书与 `traffic.txt` | 服务端 | 证书热加载 / 配额计数 |

| 端口 | 侧 | 用途 |
|---|---|---|
| 443 | server | TLS 隧道主入口 |
| 8443 | server | WS 接入 + `/_admin/*` 管理 API |
| 21878 / 21879 / 21877 | 桌面客户端 | SOCKS5+HTTP / PAC 服务 / 管理面板 |
| 53/udp+tcp | 手机引擎 | tun 内 DNS 劫持 |

## 8. 安全模型

- 认证：HMAC-SHA256 挑战应答，密码不落进程列表；失败 10 次/5 分钟封 IP 30 分钟（连接中断不计入，防自我误封）
- 传输：TLS 1.3+（正式证书热加载，自签走 TOFU）；WS 模式同样封在 TLS 内
- 面板：仅绑 127.0.0.1 + Origin/Host 校验防 CSRF；本地代理**无认证**（勿 `-lhost 0.0.0.0` 暴露公网）
- 配额：超限拒绝新隧道（状态码 2），周期自动清零，计数落盘崩溃最多丢一条

## 9. 已知取舍

- 每连接一次 TLS 握手 + 认证（浏览器自带连接复用，够用）
- 非白名单的国外域名会被 DNS 污染导致直连失败——加进白名单即可
- fake IP 对 App 是 198.18.x：ping 类 ICMP 工具无意义（引擎不转发 ICMP），验证以浏览器/应用实际流量为准
- 双域名双线路：橙云（CDN）开启时直连必须用灰云域名；换证书后删 `fingerprint.txt` 重试

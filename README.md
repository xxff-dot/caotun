# caotun

单二进制加密 TCP 转发隧道：服务端 / 客户端 / Web 管理面板是同一个程序，`server` / `client` / `web` 子命令区分。**零第三方依赖**，全部标准库（证书签发交由服务器上的 acme.sh 定时脚本，见「证书签发」一节）。

## 功能特性

- **TLS 加密隧道**：真证书（数据目录 `fullchain.pem`，由 issue-cert.sh 定时签发，自动热加载）与自签证书（TOFU 指纹校验）自动兼容——客户端先按标准 CA 校验，失败自动回退 TOFU 指纹
- **HMAC-SHA256 挑战应答认证**：密码固定落盘 `~/.caotun/auth`（0600），进程列表不暴露明文
- **本地代理双协议**：客户端本地端口同时支持 SOCKS5 与 HTTP CONNECT（首字节自动识别）
- **域名服务端解析**：避开本地 DNS 污染
- **WebSocket 传输（CDN 模式）**：流量封装为 WS 帧走 8443 端口，可被 Cloudflare 等七层 CDN 代理，隐藏源站 IP；与直连模式可同时可用，一键切换
- **防攻击 / 流量熔断**：认证失败 10 次（5 分钟滑动窗口）封 IP 30 分钟；并发上限；`-max-gb`/`-quota-days` 流量配额熔断（周期到期自动清零，保护按流量计费的账单）
- **系统代理自动管理（Windows/macOS/Linux GNOME）**：`-sysproxy pac`（白名单分流，推荐）/ `all`（全局），启动接管、退出精确还原（Windows 注册表 / macOS networksetup / Linux gsettings；无桌面环境自动跳过）
- **日志自动轮转**：同时写控制台与 `~/.caotun/<mode>.log`，超 5MB 轮转保留一份 `.old`，磁盘占用封顶

## 工作原理

### 三种模式

```
┌─────────────────────┐   TLS(→WS/CDN)   ┌──────────────────────┐
│ client（本地电脑）     │ ───────────────→ │ server（海外 VPS）     │ ──→ 任意目标 host:port
│ 127.0.0.1:21878      │   HMAC 认证       │ :443 直连 / :8443 WS  │    （域名服务端解析）
│ SOCKS5+HTTP CONNECT  │                  └──────────────────────┘
└─────────────────────┘
┌─────────────────────┐
│ web（管理面板）        │  127.0.0.1:21877，拉起/接管 client 子进程，
│ 同一个二进制          │  管理 Windows 系统代理，经管理 API 查询/运维服务端
└─────────────────────┘
```

### 隧道协议（每条连接一次握手）

1. TCP + TLS 连接（客户端先试标准 CA 校验，失败回退自签 + TOFU 指纹）
2. 【仅 WS 模式】在 TLS 之上发 `GET /tf` WebSocket Upgrade，校验 `Sec-WebSocket-Accept`，之后流量封装为二进制帧（单帧不分段；实现见 [src/ws/ws.go](src/ws/ws.go)，仅 ~200 行）
3. 服务端 → 客户端：32 字节随机 nonce
4. 客户端 → 服务端：`HMAC-SHA256(密码, nonce)`
5. 客户端 → 服务端：目标地址 `ATYP(1B) + 地址 + 端口(2B 大端)`（ATYP：1=IPv4 / 3=域名 / 4=IPv6，SOCKS5 风格）
6. 服务端 → 客户端：1 字节状态码（**0**=成功，**1**=目标连接失败，**2**=流量配额用完）
7. 双向裸流转发，双向流量计入服务端配额统计

协议实现集中在 [src/protocol/protocol.go](src/protocol/protocol.go)（地址编解码）与 [src/server/server.go](src/server/server.go) `handleServerConn`（握手全流程），客户端在 [src/tunnel/client.go](src/tunnel/client.go) `finishTunnel`。

### TLS 证书三种模式

| 启动方式 | 证书 | 客户端校验 |
|---|---|---|
| 默认（无 `-cert`） | 数据目录 `fullchain.pem` + `privkey.pem`（由 issue-cert.sh 定时签发），**文件变化自动热加载，续期免重启** | 标准 CA 证书（Let's Encrypt 等）客户端直接通过 |
| 手动：`-cert 文件 -key 文件` | 用户自备 PEM，成对提供，重启生效 | 同上；非公开受信 CA（如 CF Origin CA）回退 TOFU 指纹 |
| 兜底（无任何证书文件时） | 自签 ECDSA P-256，10 年有效，存 `~/.caotun/cert.pem`，重启指纹不变 | TOFU：首次连接记录 SHA-256 指纹，之后指纹变化即拒绝（防中间人） |

签发/换证书导致"指纹变化"提示时，删除 `~/.caotun/fingerprint.txt` 重试（真证书经标准 CA 校验时不需要）。

### 防护机制（服务端）

| 机制 | 行为 |
|---|---|
| 认证防爆破 | 5 分钟滑动窗口内失败 10 次 → 封 IP 30 分钟（TLS 握手前直接断开）；成功认证即清除记录；封禁表上限 4096 条自动清理过期 |
| 并发上限 | `-max-conns`（默认 100），超限拒绝 |
| 握手限时 | 认证+目标请求 15 秒内未完成即断开，防慢连接占资源 |
| 半开连接回收 | TCP keepalive 60 秒，NAT 超时/断网后自动回收 |
| 流量熔断 | `-max-gb` 超限后拒绝新隧道（状态码 2），已有隧道不断；`-quota-days` 周期到期自动清零；计数落盘 `traffic.txt`，每条连接结束写一次，崩溃最多丢一条连接的计量 |

## 目录结构

```
caotun/
├── src/                    Go 源码（go.mod 在此，构建/测试在 src/ 下执行）
│   ├── main.go             入口：参数解析、模式分发、数据目录、日志初始化
│   ├── logrotate.go        日志轮转（5MB → .old）
│   ├── protocol/           隧道协议：地址编解码（ATYP + addr + port）+ ServerHost
│   ├── ws/                 最小 WebSocket(RFC6455)：二进制帧 ↔ net.Conn 适配
│   ├── sysproxy/           系统代理平台拆分：Windows 注册表 / macOS networksetup / Linux gsettings + 共用 PAC 服务
│   ├── tunnel/             客户端：本地 SOCKS5/HTTP CONNECT 代理、TLS 双模式拨号、TOFU 指纹
│   ├── server/             服务端：TLS 证书（文件热加载/自签兜底）、握手认证、转发、WS 接入、IP 封禁与流量配额
│   └── web/                管理面板：HTTP API、子进程管理、服务端管理 API 转发、PAC 白名单、内嵌前端
├── scripts/                配置在根目录共享，脚本按平台分目录（内容一一对应）
│   ├── client.conf         客户端/面板启动配置（直连地址 / 认证密码 / 端口 / 系统代理 / 面板端口）
│   ├── server.conf         服务端配置（监听 / 证书 / 配额 / 并发 / 轮换）
│   ├── shell/              Linux + macOS 通用：build.sh 打包 + 全套启停脚本（读根目录 conf）
│   └── windows/            Windows 等效 .bat：build.bat 打包 + 全套启停（读 ..\ conf）
├── dist/                   发布包（build 产物，开箱即用）：根放三平台二进制与 conf，
│                           shell/ 与 windows/ 为对应平台全套启停脚本
└── .gitattributes          强制全仓库 LF
```

## 快速开始

### 1. 打包

```bash
sh scripts/shell/build.sh              # Linux / macOS
scripts\windows\build.bat             # Windows：双击或 cmd 运行
```

产物输出 `dist/`（exe/linux UPX 压到约 37%），启停脚本与 client.conf 自动复制进 dist/ 组成完整发布包。

### 2. 部署服务端（海外 VPS，一键引导）

**远程一键安装**（服务器能访问 GitHub 时无需上传任何文件）：

```bash
ssh 用户@服务器IP 'sh <(curl -fsSL https://raw.githubusercontent.com/xxff-dot/caotun/main/scripts/shell/install-server.sh < /dev/null)'
```

国内服务器直连失败时走镜像：

```bash
ssh 用户@服务器IP 'GH_MIRROR=https://gh-proxy.com/ sh <(wget -qO- https://gh-proxy.com/https://raw.githubusercontent.com/xxff-dot/caotun/main/scripts/shell/install-server.sh)'
```

**上传安装包方式**：引导脚本找不到本地文件时也会自动从 GitHub Release 下载；也可以先上传再运行，按提示回答即可（端口/密码/真证书/开机自启，一路回车=全默认）：

```bash
scp -r dist 用户@服务器IP:~/caotun-deploy
ssh 用户@服务器IP 'sh ~/caotun-deploy/shell/install-server.sh'
```

结束时打印认证密码与两端配置方法。全自动（免交互）示例：

```bash
ssh 用户@服务器IP 'PASS=我的密码 CERT_DOMAINS="direct.example.com cdn.example.com" \
  CF_Token=xxx CF_Account_ID=xxx CF_Zone_ID=xxx AUTOSTART=systemd INSTALL_YES=1 \
  sh ~/caotun-deploy/shell/install-server.sh'
```

支持的环境变量：`PASS` `PORT` `WS_PORT` `MAX_GB` `QUOTA_DAYS` `CERT_DOMAINS`（空=自签兜底）`CF_Token/CF_Account_ID/CF_Zone_ID`（DNS-01，橙云域名可用）`CERT_HTTP=1`（HTTP-01，仅灰云）`AUTOSTART=systemd|cron|no` `INSTALL_YES=1`。

卸载（默认保留数据目录；`KEEP_DATA=0` 连数据一起删，`PURGE_ACME=1` 连 acme.sh 一起删）：

```bash
ssh 用户@服务器IP 'sh ~/caotun-deploy/shell/uninstall-server.sh'
```

<details>
<summary>手动部署（不用引导脚本）</summary>

```bash
./caotun server
# 密码：首次启动自动生成随机密码写入 ~/.caotun/auth（0600），之后重启默认沿用；
# -auth 指定密码；-rotate-pass 轮换随机新密码
```

```bash
scp dist/caotun_linux dist/shell/start-server.sh dist/shell/stop-server.sh dist/server.conf 用户@服务器IP:~/
ssh 用户@服务器IP 'mkdir -p ~/caotun && mv start-server.sh ~/caotun/start-caotun.sh && mv stop-server.sh ~/caotun/stop-caotun.sh && chmod +x ~/caotun ~/caotun/start-caotun.sh && sh ~/caotun/start-caotun.sh'
```

</details>

建议同时开 BBR（高丢包线路收益大）：

```bash
grep -q bbr /etc/sysctl.conf || { echo "net.core.default_qdisc=fq" >> /etc/sysctl.conf; echo "net.ipv4.tcp_congestion_control=bbr" >> /etc/sysctl.conf; sysctl -p; }
```

记得放行防火墙/安全组对应 TCP 端口（默认 443 + 8443）。

### 3. 启动客户端

**Web 面板（推荐）**：双击 `scripts\windows\start-web.bat`（或 git-bash 里 `sh scripts/shell/start-web.sh`）。面板以最小化窗口运行（任务栏窗口名 caotun-panel），浏览器自动打开 <http://127.0.0.1:21877>。**停止方法：任务栏点开那个最小化窗口，在里面按 Ctrl+C**（会停止客户端并还原系统代理）。

**命令行**：

```bash
sh scripts/shell/start-client.sh   # 启动：读 client.conf 里的认证密码（必填），随即系统走代理
sh scripts/shell/stop-client.sh    # 停止（优先在 start 窗口按 Ctrl+C，能自动还原系统代理）
```

非 Windows 客户端：`./caotun_linux client -server-addr 域名:443 -lport 21878`（密码写 `~/.caotun/auth` 或 `-auth` 指定），应用里手动指 SOCKS5 到该端口即可。

## Web 管理面板

功能：一键启停客户端、代理模式切换（pac 白名单分流 / all 全局 / off）、接入线路切换（直连 / CDN-WS，配置卡地址框随模式联动显示）、服务器地址等配置编辑、客户端日志查看、服务器累计流量/配额周期查询、**服务端证书有效期查看**（正式/自签的签发者与过期时间）、**服务端认证密码一键轮换**（热生效，双端自动更新）。服务端管理操作全部经**服务端管理 API**（见下节）完成，面板不需要任何 SSH 权限。

- 系统**代理设置由面板进程管理**：启动时接管（备份原值 → 写注册表 + 起 PAC 服务），停止/退出时精确还原；强杀面板导致的残留，重开面板再正常退出即可还原
- 配置存 `~/.caotun/web.json`；换服务器改"配置"卡里的隧道地址即可
- 面板仅绑定 127.0.0.1，所有 API 校验 Origin/Host 防 CSRF；认证密码由用户在"配置"卡自设（必填，与服务端一致），同时作为调用服务端管理 API 的凭证
- 面板强杀后孤儿客户端：重开面板会显示"已运行(待接管)"，点启动即接管
- PAC 白名单默认 100+ 常用国外域名（[src/web/web.go](src/web/web.go) `DefaultProxyDomains`：Google/GitHub/AI/开发者生态等），面板可增删，保存即生效
- **代理 IP 清单**：需要指定 IP（而非域名）走隧道时，在面板"配置"卡「代理 IP」里每行填一个，支持 `*` 通配（如 `52.10.*`），保存即重建 PAC 生效；内网段（127/192.168/10）始终直连不代理

### HTTP API（面板，仅本机）

| 端点 | 说明 |
|---|---|
| `GET /api/status` | 运行状态（是否运行/待接管、当前模式、线路、本地端口） |
| `POST /api/start` / `POST /api/stop` | 启动 / 停止客户端 |
| `POST /api/mode` | 切换代理模式 `{"mode": "pac"|"all"|"off"}` |
| `GET/POST /api/config` | 读 / 写面板配置（web.json） |
| `GET /api/log?n=200` | 客户端日志末 N 行（≤2000） |
| `GET /api/server` | 服务器流量：已用 GB / 配额 / 周期剩余天数（转发服务端管理 API） |
| `GET /api/cert` | 服务端当前证书列表（名称/签发者/过期时间/剩余天数） |
| `POST /api/pass/rotate` | 轮换服务端密码，`{"password":""}` 空 = 服务端生成随机密码，双端热更新 |

### 服务端管理 API（`/_admin/*`，挂在与 WS 相同的 TLS 端口，默认 8443）

服务端自带管理接口，按路径与隧道分流（`/tf` = 隧道，`/_admin/*` = 管理）。鉴权：请求头 `X-Auth: <认证密码>`（错误计入防爆破封禁）。所有操作都是服务端本地文件读写，路径 portable。

| 端点 | 说明 |
|---|---|
| `GET /_admin/cert` | 已部署证书列表（正式证书文件 + 自签兜底/手动证书）：签发者、过期时间、覆盖域名 |
| `POST /_admin/pass` | 轮换认证密码：写回 auth 文件 + 热更新（新连接即刻生效），空密码 = 随机生成，响应返回新密码 |
| `GET /_admin/traffic` | 本周期累计流量与周期起始 |

### 证书签发（issue-cert.sh，服务器本地定时任务）

进程内签发已移除。证书由 **acme.sh 在服务器上签发/续期**（自带每日 cron，到期前自动续），产物写入数据目录，服务端检测到文件变化自动热加载：

```bash
# 推荐走 Cloudflare DNS-01（橙云域名也能签，不占端口；Token 权限 Zone.DNS Edit + Zone.Zone Read）
CF_Token=xxx CF_Account_ID=xxx CF_Zone_ID=xxx sh ~/caotun/issue-cert.sh direct.example.com cdn.example.com
# 或 HTTP-01 standalone（临时占 80 端口，仅灰云域名）
sh ~/caotun/issue-cert.sh direct.example.com --http
```

一次把多个域名签进同一张证书（SAN）。重签 = 重跑脚本；手动续期 = `~/.acme.sh/acme.sh --renew -d 主域名 --force`。

## 参数

按模式子命令组织，每个模式的参数互相独立；`caotun <模式> -h` 可查看实时帮助。

### server

| 参数 | 说明 |
|---|---|
| `-host` | TLS 监听 IP（默认 0.0.0.0 所有网卡；127.0.0.1=仅本机） |
| `-port` | TLS 隧道监听端口（默认 443） |
| `-ws-port` | WebSocket/CDN 接入端口（默认 8443） |
| `-auth` | 认证密码；不指定则读写 `~/.caotun/auth`（首次启动自动生成随机密码） |
| `-rotate-pass` | 启动时生成随机新密码写回 `~/.caotun/auth`（默认沿用现有密码） |
| `-max-conns` | 最大并发连接数（默认 100） |
| `-max-gb` | 流量配额 GB（0 不限；超限拒绝新隧道） |
| `-quota-days` | 配额周期天数（默认 30，到期自动清零；0 不限周期） |
| `-cert` / `-key` | 手动证书/私钥 PEM（成对；不指定则自动用 数据目录/fullchain.pem，缺失时自签兜底） |

### client

| 参数 | 说明 |
|---|---|
| `-server-addr` | 服务端地址 `host:port`（必填） |
| `-lhost` | 本地代理监听 IP（默认 127.0.0.1 仅本机；0.0.0.0=局域网共享，代理无认证，勿暴露公网） |
| `-lport` | 本地代理端口（默认 21878） |
| `-auth` | 认证密码；不指定则读 `~/.caotun/auth` |
| `-sysproxy` | off / pac(白名单分流) / all(全局)，退出自动恢复 |
| `-pac-port` | PAC 脚本下载端口（默认 21879；仅 pac 模式用，代理流量不走此端口） |
| `-ws` | 走 WebSocket/CDN 传输（对接服务端 `-ws-port`） |
| `-insecure` | 跳过证书校验（不建议，仅调试） |

### web

| 参数 | 说明 |
|---|---|
| `-web-port` | 管理面板端口（默认 21877，仅绑定 127.0.0.1） |

## 配置文件

`scripts/client.conf`（客户端启动配置，KEY=VALUE，未注释项随包提供默认值）：

| 键 | 说明 |
|---|---|
| `DIRECT_ADDR` | 直连地址（灰云域名或 IP:443），必填 |
| `AUTH_PASSWORD` | 认证密码（必填，需与服务端一致） |
| `LOCAL_PORT` | 本地代理监听端口（默认 21878） |
| `HOST` | 监听 IP（可选，默认 127.0.0.1 仅本机；局域网共享设 0.0.0.0，注意代理无认证） |
| `SYS_PROXY` | 系统代理模式 off / pac / all（默认 off；pac=白名单分流，all=全局） |
| `WS` | 1=走 WebSocket/CDN 线路（`DIRECT_ADDR` 需换成橙云域名:8443）；默认直连 |
| `INSECURE` | 1=跳过服务端证书指纹校验（不建议，仅调试用） |
| `PAC_PORT` | PAC 服务端口（默认 21879；SYS_PROXY=pac 时自动起） |

`scripts/server.conf`（服务端配置）：

| 键 | 说明 |
|---|---|
| `HOST` | TLS/WS 监听 IP（默认 0.0.0.0 所有网卡；127.0.0.1=仅本机） |
| `PORT` | TLS 隧道监听端口（默认 443） |
| `WS_PORT` | WebSocket/CDN 接入端口（默认 8443） |
| `CERT` / `KEY` | 手动指定证书/私钥 PEM 路径（成对填；默认无需配置，issue-cert.sh 产物自动加载） |
| `MAX_GB` / `QUOTA_DAYS` | 流量配额 GB 与周期天数（到期自动清零；MAX_GB=0 不限） |
| `MAX_CONNS` | 最大并发连接数（默认 100） |
| `ROTATE` | 1=启动时轮换随机密码写回 数据目录/auth（默认沿用现有密码；命令行 `-r` 等效） |

## 数据目录 `~/.caotun/`

| 文件 | 归属 | 说明 |
|---|---|---|
| `web.json` | 面板 | 面板配置（双线路地址、认证密码、PAC 白名单与代理 IP 清单等） |
| `auth` | 服务端/客户端 | **认证密码文件（两端同路径，两端必须一致）**：服务端首次启动自动生成随机密码写在此处并日志提示；之后重启默认沿用（`-rotate-pass` 轮换）；客户端由 start-client.sh 或面板写入用户设置的密码 |
| `client.pid` | 面板 | 客户端子进程 PID（接管孤儿进程用） |
| `fingerprint.txt` | 客户端 | TOFU 服务端证书指纹；报"指纹变化"且确认是服务端换证书后删除此文件重试 |
| `cert.pem` / `key.pem` | 服务端 | 自签兜底证书（无正式证书时用；重启指纹不变） |
| `fullchain.pem` / `privkey.pem` | 服务端 | 正式证书（issue-cert.sh 签发/续期自动写入，服务端热加载） |
| `traffic.txt` | 服务端 | 配额计数 JSON `{total, windowStart}`（兼容旧版纯数字格式） |
| `client.log` / `server.log` / `web.log` | 各模式 | 日志（5MB 轮转，保留一份 `.old`） |

## 端口一览

| 端口 | 侧 | 用途 |
|---|---|---|
| 443 | server | TLS 隧道主入口（`server.conf PORT` / `-port`） |
| 8443 | server | WebSocket 接入（CDN 可代理的 HTTPS 备用端口，`WS_PORT` / `-ws-port` 可改） |
| 21878 | client | 本地代理（SOCKS5 + HTTP CONNECT 同端口；`LOCAL_PORT` / `-lport` 可改） |
| 21879 | client | 本地 PAC 脚本服务（pac 模式自动起，`PAC_PORT` / `-pac-port` 可改） |
| 21877 | web | 管理面板（默认，仅绑 127.0.0.1，`-web-port` 可改） |

## 平台兼容

| 平台 | 客户端 | 服务端 | 说明 |
|---|---|---|---|
| Windows | ✅ | 可编译（代码同 linux） | 注册表 + PAC 服务（生产使用中） |
| Linux | ✅（WSL 实测回环+真实穿透） | ✅（生产运行中） | GNOME gsettings（PAC=auto / 全局=manual）；无桌面（WSL2/服务器）自动跳过 |
| macOS (arm64) | 编译产物提供，未实测 | 同左 | networksetup（PAC / 全局=socks） |

## 测试

```bash
cd src && go test ./ws/            # 单元测试（WS 帧编解码）
WS_ADDR=pdl.example.com:8443 WS_PASS=密码 go test -run TestWSLive ./tunnel/ -v   # 真机全链路：TLS(CA)→WS→认证→目标→HTTP 回读
```

## 设计取舍与已知限制

- 每条连接一次 TLS 握手 + 认证，实现最简；浏览器对 CONNECT 连接自带复用，够用
- 未做 UDP 转发、流量混淆、TUN 全局接管；被墙干扰时再说
- git 不吃 Windows 系统代理，走隧道需一次性配置：
  `git config --global http.https://github.com.proxy socks5://127.0.0.1:21878`
- 证书由服务器本地脚本签发（issue-cert.sh），签发失败看脚本输出与 `~/.acme.sh/` 日志；服务端只认证书文件，无黑盒
- 双域名双模式注意：**橙云（CDN）开启时直连必须用灰云域名**——橙云域名会被 CF 强制七层代理，纯 TLS 隧道过不去；切换/换证书后客户端报"指纹变化"，删除 `~/.caotun/fingerprint.txt` 重试

## 附录

### systemd 常驻（服务端）

引导脚本选 systemd 自启时会自动写入以下单元（供手动参考）：

```ini
[Unit]
Description=caotun
After=network.target

[Service]
ExecStart=/home/用户/caotun server -port 443 -ws-port 8443 -max-gb 20 -quota-days 30
Restart=always

[Install]
WantedBy=multi-user.target
```

### 部署示例（占位信息，按实际替换）

| 项 | 值 |
|---|---|
| 服务器 | `root@1.2.3.4`（海外 VPS） |
| 服务端二进制 | `/root/caotun` |
| 域名/端口 | `direct.example.com:443`（直连 TLS）+ `cdn.example.com:8443`（CDN WS，CF 橙云） |
| 证书 | 服务器上 `sh issue-cert.sh` 签发/自动续期（acme.sh）；面板可查看有效期 |
| 认证密码 | 用户自设固定密码（面板"配置"卡）；`POST /_admin/pass` 或 `-r` 轮换 |
| 流量配额 | 20G / 30 天（计数存 `~/.caotun/traffic.txt`） |
| 服务端启停 | `sh /root/start-caotun.sh [密码] [域名]` / `sh /root/stop-caotun.sh`（仓库副本在 `scripts/`） |
| 优化 | 服务器已开 BBR |

**流量配额用完 / 想重置计数：**

```bash
ssh root@1.2.3.4 'sh /root/stop-caotun.sh; rm -f /root/.caotun/traffic.txt; sh /root/start-caotun.sh'
```

// caotun —— 单二进制加密 TCP 转发隧道（唯一第三方依赖 golang.org/x/crypto/acme/autocert）
//
// 用法:
//
//	caotun server [参数]   服务端（海外 VPS）
//	caotun client [参数]   客户端（本地 SOCKS5/HTTP CONNECT 代理）
//	caotun web [参数]      Web 管理面板
//	caotun help            总帮助；caotun <模式> -h 查看该模式参数
//
// 客户端本地端口同时支持 SOCKS5 与 HTTP CONNECT 两种代理协议。
package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"

	"caotun/server"
	"caotun/sysproxy"
	"caotun/tunnel"
	"caotun/web"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "server":
		serverCmd(os.Args[2:])
	case "client":
		clientCmd(os.Args[2:])
	case "web":
		webCmd(os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "未知模式: %s\n可用模式: server / client / web，详见 caotun help\n", os.Args[1])
		os.Exit(2)
	}
}

func usage() {
	fmt.Print(`caotun —— 单二进制加密 TCP 转发隧道

用法:
  caotun server [参数]   服务端（海外 VPS）
  caotun client [参数]   客户端（本地 SOCKS5/HTTP CONNECT 代理）
  caotun web [参数]      Web 管理面板（仅绑定 127.0.0.1）
  caotun <模式> -h       查看该模式全部参数

示例:
  caotun server                                    # 首次启动自动生成随机密码写入 ~/.caotun/auth
  caotun client -server-addr 域名:443 -auth 密码 -sysproxy pac
  caotun web
`)
}

// commonInit 各模式共用：数据目录 + 日志轮转；返回数据目录与固定密码文件路径
func commonInit(mode string) (dir, authPath string) {
	d, err := configDir()
	if err != nil {
		log.Fatal(err)
	}
	// 日志同时写控制台+文件（自动轮转）
	setupLogFile(filepath.Join(d, mode+".log"), 5<<20)
	return d, filepath.Join(d, "auth")
}

// randomPassword 128-bit 随机密码（32 位 hex）
func randomPassword() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		log.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// ==================== server ====================

func serverCmd(args []string) {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	host := fs.String("host", "0.0.0.0", "TLS 监听 IP（0.0.0.0=所有网卡；127.0.0.1=仅本机）")
	port := fs.String("port", "443", "TLS 隧道监听端口")
	wsPort := fs.String("ws-port", "8443", "WebSocket 接入端口（CDN 模式）")
	auth := fs.String("auth", "", "认证密码；不指定则读写 ~/.caotun/auth（首次自动生成）")
	rotatePass := fs.Bool("rotate-pass", false, "启动时生成随机新密码写回 ~/.caotun/auth")
	maxConns := fs.Int("max-conns", 100, "最大并发连接数")
	maxGB := fs.Float64("max-gb", 0, "流量配额 GB（0 不限；超限拒绝新隧道）")
	quotaDays := fs.Int("quota-days", 30, "配额周期天数（到期自动清零；0 不限周期）")
	cert := fs.String("cert", "", "手动证书 PEM（与 -key 成对；不指定则用 数据目录/fullchain.pem，缺失时自签兜底）")
	key := fs.String("key", "", "手动私钥 PEM")
	fs.Parse(args)

	dir, authPath := commonInit("server")
	if *rotatePass && *auth != "" {
		log.Fatal("-auth 与 -rotate-pass 不可同时使用")
	}
	if *auth == "" {
		b, err := os.ReadFile(authPath)
		if *rotatePass || os.IsNotExist(err) {
			// -rotate-pass 显式轮换；或首次启动无密码文件——生成随机密码并写回
			*auth = randomPassword()
			if werr := os.WriteFile(authPath, []byte(*auth), 0600); werr != nil {
				log.Fatalf("写入密码文件失败: %v", werr)
			}
			log.Printf("已生成随机认证密码写入 %s（客户端需使用同一密码）", authPath)
		} else if err != nil {
			log.Fatalf("读取密码文件失败: %v", err)
		} else if *auth = strings.TrimSpace(string(b)); *auth == "" {
			log.Fatalf("密码文件为空: %s（写入密码或用 -rotate-pass 生成）", authPath)
		}
	}
	server.Run(net.JoinHostPort(*host, *port), net.JoinHostPort(*host, *wsPort), *auth, dir, *maxGB, *quotaDays, *maxConns, *cert, *key)
}

// ==================== client ====================

func clientCmd(args []string) {
	fs := flag.NewFlagSet("client", flag.ExitOnError)
	serverAddr := fs.String("server-addr", "", "服务端地址 host:port（必填）")
	lhost := fs.String("lhost", "127.0.0.1", "本地代理监听 IP（所有代理流量走这里；设 0.0.0.0 供局域网共享，注意代理无认证）")
	lport := fs.String("lport", "21878", "本地代理监听端口（所有代理流量走这里）")
	auth := fs.String("auth", "", "认证密码；不指定则读 ~/.caotun/auth")
	sysMode := fs.String("sysproxy", "off", "自动设置系统代理 off / pac(白名单分流) / all(全局)；退出时自动恢复")
	pacPort := fs.String("pac-port", "21879", "PAC 脚本下载端口（仅 -sysproxy pac 用，供系统拉取分流脚本；代理流量不走此端口）")
	insecure := fs.Bool("insecure", false, "跳过服务端证书校验（不建议）")
	useWS := fs.Bool("ws", false, "走 WebSocket 传输（CDN 模式，对接服务端 -ws-port 端口）")
	fs.Parse(args)

	sysproxy.PACPort = *pacPort
	dir, authPath := commonInit("client")
	if *auth == "" {
		b, err := os.ReadFile(authPath)
		if err != nil {
			log.Fatalf("未设置认证密码: 用 -auth 指定，或写入 %s", authPath)
		}
		if *auth = strings.TrimSpace(string(b)); *auth == "" {
			log.Fatalf("密码文件为空: %s", authPath)
		}
	}
	tunnel.Run(*serverAddr, net.JoinHostPort(*lhost, *lport), *auth, *sysMode, *insecure, dir, web.DefaultProxyDomains(), *useWS)
}

// ==================== web ====================

func webCmd(args []string) {
	fs := flag.NewFlagSet("web", flag.ExitOnError)
	webPort := fs.String("web-port", web.Port, "面板监听端口（仅绑定 127.0.0.1）")
	fs.Parse(args)

	dir, _ := commonInit("web")
	web.Run("127.0.0.1:"+*webPort, dir)
}

// configDir 数据目录（~/.caotun）：面板配置、密码、指纹、日志、证书、流量计数
// configDir 数据目录（~/.caotun）：面板配置、密码、指纹、日志、证书、流量计数。
// 环境变量 CAOTUN_HOME 可显式指定（systemd 等无 HOME 的运行环境必须设置）
func configDir() (string, error) {
	if v := os.Getenv("CAOTUN_HOME"); v != "" {
		if err := os.MkdirAll(v, 0700); err != nil {
			return "", err
		}
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(home, ".caotun")
	if err := os.MkdirAll(d, 0700); err != nil {
		return "", err
	}
	return d, nil
}

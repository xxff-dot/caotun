package tunnel

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"caotun/protocol"
	"caotun/ws"
)

// 真机 WS 全协议测试: TLS(CA) → WS 升级 → nonce/HMAC → 目标 → 状态 → HTTP 数据回读
// 运行: WS_PASS=密码 WS_ADDR=cdn.example.com:8443 go test -run TestWSLive -v .
func TestWSLive(t *testing.T) {
	addr := os.Getenv("WS_ADDR")
	pass := os.Getenv("WS_PASS")
	if addr == "" || pass == "" {
		t.Skip("需要 WS_ADDR/WS_PASS 环境变量")
	}
	raw, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		t.Fatalf("拨号失败: %v", err)
	}
	defer raw.Close()
	raw.SetDeadline(time.Now().Add(20 * time.Second))
	tc := tls.Client(raw, &tls.Config{ServerName: protocol.ServerHost(addr)})
	if err := tc.Handshake(); err != nil {
		t.Fatalf("TLS 握手失败: %v", err)
	}
	t.Log("TLS 握手 OK")
	ws, err := ws.ClientConn(tc, protocol.ServerHost(addr))
	if err != nil {
		t.Fatalf("WS 升级失败: %v", err)
	}
	t.Log("WS 升级 OK")
	var nonce [32]byte
	if _, err := io.ReadFull(ws, nonce[:]); err != nil {
		t.Fatalf("读 nonce 失败: %v", err)
	}
	mac := hmac.New(sha256.New, []byte(pass))
	mac.Write(nonce[:])
	if _, err := ws.Write(mac.Sum(nil)); err != nil {
		t.Fatalf("写 HMAC 失败: %v", err)
	}
	t.Log("认证 OK")
	if err := protocol.WriteTarget(ws, "example.com", 80); err != nil {
		t.Fatalf("写目标失败: %v", err)
	}
	var st [1]byte
	if _, err := io.ReadFull(ws, st[:]); err != nil {
		t.Fatalf("读状态失败: %v", err)
	}
	if st[0] != 0 {
		t.Fatalf("服务端拒绝, 状态码 %d", st[0])
	}
	t.Log("目标接入 OK, 发送 HTTP 请求...")
	ws.Write([]byte("GET / HTTP/1.0\r\nHost: example.com\r\n\r\n"))
	raw.SetDeadline(time.Now().Add(20 * time.Second))
	head, _ := io.ReadAll(io.LimitReader(ws, 4096))
	s := string(head)
	if strings.Contains(s, "HTTP/1.") {
		t.Logf("数据回读 OK: %.60s", s)
		return
	}
	t.Fatalf("未收到 HTTP 响应: %d 字节: %q", len(head), s)
}

// 与 binary.BigEndian 保持一致的端口编码 sanity（防止 writeTarget 参数顺序错误）
func TestPortEncoding(t *testing.T) {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], 443)
	if b[0] != 0x01 || b[1] != 0xbb {
		t.Fatal("端口编码错误")
	}
}

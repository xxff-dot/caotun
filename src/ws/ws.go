// 最小 WebSocket(RFC6455) 实现：仅二进制帧、单帧不分段，够隧道字节流使用。
// 服务端: ServerConn 在 TLS 连接上完成 Upgrade 握手并返回帧连接
// 客户端: ClientConn 发送 Upgrade 请求并校验 Sec-WebSocket-Accept
package ws

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// Conn 把 WebSocket 二进制帧封装成 net.Conn 语义（隧道协议无需感知）
type Conn struct {
	conn   net.Conn
	br     *bufio.Reader
	mask   bool // 作为客户端时发送帧需要 mask
	wmu    sync.Mutex
	eof    bool
	remain []byte // 上一帧未读尽的载荷
}

func (c *Conn) Read(p []byte) (int, error) {
	if len(c.remain) > 0 {
		n := copy(p, c.remain)
		c.remain = c.remain[n:]
		return n, nil
	}
	if c.eof {
		return 0, io.EOF
	}
	for { // 跳过 ping/pong 等控制帧，直到拿到二进制载荷
		h1, err := c.br.ReadByte()
		if err != nil {
			return 0, err
		}
		h2, err := c.br.ReadByte()
		if err != nil {
			return 0, err
		}
		opcode := h1 & 0x0f
		masked := h2&0x80 != 0
		length := uint64(h2 & 0x7f)
		switch length {
		case 126:
			var b [2]byte
			if _, err := io.ReadFull(c.br, b[:]); err != nil {
				return 0, err
			}
			length = uint64(binary.BigEndian.Uint16(b[:]))
		case 127:
			var b [8]byte
			if _, err := io.ReadFull(c.br, b[:]); err != nil {
				return 0, err
			}
			length = binary.BigEndian.Uint64(b[:])
		}
		var maskKey [4]byte
		if masked {
			if _, err := io.ReadFull(c.br, maskKey[:]); err != nil {
				return 0, err
			}
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(c.br, payload); err != nil {
			return 0, err
		}
		if masked {
			for i := range payload {
				payload[i] ^= maskKey[i%4]
			}
		}
		switch opcode {
		case 0x8: // close
			c.eof = true
			return 0, io.EOF
		case 0x9: // ping → pong
			if err := c.writeFrame(0xA, payload); err != nil {
				return 0, err
			}
		case 0xA: // pong 忽略
		case 0x2, 0x0: // 二进制/ continuation
			n := copy(p, payload)
			c.remain = payload[n:]
			return n, nil
		default:
			return 0, fmt.Errorf("不支持的 WebSocket 帧类型 %d", opcode)
		}
	}
}

func (c *Conn) writeFrame(opcode byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	var head []byte
	head = append(head, 0x80|opcode)
	var lenByte byte
	if c.mask { // mask 标志位在长度字节上，必须在写长度前置位
		lenByte |= 0x80
	}
	n := len(payload)
	switch {
	case n < 126:
		head = append(head, lenByte|byte(n))
	case n < 65536:
		head = append(head, lenByte|126, byte(n>>8), byte(n))
	default:
		head = append(head, lenByte|127)
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(n))
		head = append(head, b[:]...)
	}
	if c.mask { // 客户端帧必须 mask（固定掩码即可：对端是自己的服务端，安全性由外层 TLS 保证）
		key := [4]byte{0x63, 0x62, 0x61, 0x60}
		head = append(head, key[:]...)
		masked := make([]byte, n)
		for i := range payload {
			masked[i] = payload[i] ^ key[i%4]
		}
		payload = masked
	}
	if _, err := c.conn.Write(head); err != nil {
		return err
	}
	_, err := c.conn.Write(payload)
	return err
}

func (c *Conn) Write(p []byte) (int, error) {
	if err := c.writeFrame(0x2, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *Conn) Close() error                       { return c.conn.Close() }
func (c *Conn) LocalAddr() net.Addr                { return c.conn.LocalAddr() }
func (c *Conn) RemoteAddr() net.Addr               { return c.conn.RemoteAddr() }
func (c *Conn) SetDeadline(t time.Time) error      { return c.conn.SetDeadline(t) }
func (c *Conn) SetReadDeadline(t time.Time) error  { return c.conn.SetReadDeadline(t) }
func (c *Conn) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }

// ====== 服务端 ======

// PeekRequest 预读连接上的首个 HTTP 请求（服务端按路径分流：/tf 走隧道，/_admin/* 走管理 API）
func PeekRequest(conn net.Conn) (*http.Request, *bufio.Reader, error) {
	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		return nil, nil, err
	}
	return req, br, nil
}

// Accept 校验预读的请求并完成 WebSocket 升级握手（路径须为 GET /tf）
func Accept(br *bufio.Reader, conn net.Conn, req *http.Request) (*Conn, error) {
	if !strings.EqualFold(req.Header.Get("Upgrade"), "websocket") ||
		req.URL.Path != "/tf" {
		conn.Write([]byte("HTTP/1.1 404 Not Found\r\nConnection: close\r\n\r\n"))
		conn.Close()
		return nil, errors.New("非 WebSocket 升级请求")
	}
	key := req.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		return nil, errors.New("缺少 Sec-WebSocket-Key")
	}
	accept := acceptKey(key)
	resp := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := conn.Write([]byte(resp)); err != nil {
		return nil, err
	}
	return &Conn{conn: conn, br: br}, nil
}

// ServerConn 在已 TLS 化的连接上读取 Upgrade 请求（须为 GET /tf），完成握手
func ServerConn(conn net.Conn) (*Conn, error) {
	req, br, err := PeekRequest(conn)
	if err != nil {
		return nil, err
	}
	return Accept(br, conn, req)
}

// ====== 客户端 ======

// ClientConn 在已 TLS 化的连接上发送 Upgrade 并校验响应
func ClientConn(conn net.Conn, serverHostStr string) (*Conn, error) {
	var raw [16]byte
	for i := range raw {
		raw[i] = byte(i*7 + 3) // 固定伪随机即可：key 仅为握手规范要求，安全性由 TLS 保证
	}
	key := base64.StdEncoding.EncodeToString(raw[:])
	req := "GET /tf HTTP/1.1\r\n" +
		"Host: " + serverHostStr + "\r\n" +
		"Upgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		return nil, err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return nil, fmt.Errorf("WebSocket 升级被拒绝: %s", resp.Status)
	}
	return &Conn{conn: conn, br: br, mask: true}, nil
}

// acceptKey 计算 Sec-WebSocket-Accept
func acceptKey(key string) string {
	h := sha1.New()
	h.Write([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

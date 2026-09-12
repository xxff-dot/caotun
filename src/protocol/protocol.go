package protocol

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
)

// ==================== 协议 ====================
//
// 认证（每条连接）: 服务端发 32 字节随机 nonce → 客户端回 HMAC-SHA256(密码, nonce)
// 之后客户端发目标地址: ATYP(1B) + 地址 + 端口(2B 大端)，服务端回 1 字节状态（0=成功）
// 然后进入双向裸流转发。

// ServerHost 从 host:port 地址取主机部分（IP 或域名），解析失败原样返回
func ServerHost(serverAddr string) string {
	host, _, err := net.SplitHostPort(serverAddr)
	if err != nil {
		return serverAddr
	}
	return host
}

// ReadTarget 读取目标地址（服务端侧）
func ReadTarget(r io.Reader) (string, int, error) {
	var atyp [1]byte
	if _, err := io.ReadFull(r, atyp[:]); err != nil {
		return "", 0, err
	}
	var host string
	switch atyp[0] {
	case 1: // IPv4
		var b [4]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return "", 0, err
		}
		host = net.IP(b[:]).String()
	case 3: // 域名（服务端负责解析，避开本地 DNS 污染）
		var l [1]byte
		if _, err := io.ReadFull(r, l[:]); err != nil {
			return "", 0, err
		}
		buf := make([]byte, l[0])
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", 0, err
		}
		host = string(buf)
	case 4: // IPv6
		var b [16]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return "", 0, err
		}
		host = net.IP(b[:]).String()
	default:
		return "", 0, fmt.Errorf("未知地址类型 %d", atyp[0])
	}
	var p [2]byte
	if _, err := io.ReadFull(r, p[:]); err != nil {
		return "", 0, err
	}
	return host, int(binary.BigEndian.Uint16(p[:])), nil
}

// WriteTarget 写目标地址（客户端侧）
func WriteTarget(w io.Writer, host string, port int) error {
	var buf []byte
	if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
		buf = append(buf, 1)
		buf = append(buf, ip.To4()...)
	} else if ip != nil {
		buf = append(buf, 4)
		buf = append(buf, ip.To16()...)
	} else {
		buf = append(buf, 3, byte(len(host)))
		buf = append(buf, []byte(host)...)
	}
	var p [2]byte
	binary.BigEndian.PutUint16(p[:], uint16(port))
	buf = append(buf, p[:]...)
	_, err := w.Write(buf)
	return err
}

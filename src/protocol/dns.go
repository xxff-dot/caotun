package protocol

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
)

// DNSOverTCP 一次 DNS-over-TCP(RFC 7766) 交换：2 字节大端长度前缀包帧。
// 桌面端 tunnel 本地 DNS 转发与移动端 tun2sock 共用。
// ponytail: 只读第一响应即返回；多报文响应（极少见）截断，需要时改为循环读
func DNSOverTCP(rc net.Conn, query []byte) ([]byte, error) {
	if len(query) > 0xFFFF {
		return nil, errors.New("DNS 查询超长")
	}
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], uint16(len(query)))
	if _, err := rc.Write(append(l[:], query...)); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(rc, l[:]); err != nil {
		return nil, err
	}
	resp := make([]byte, binary.BigEndian.Uint16(l[:]))
	if _, err := io.ReadFull(rc, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// DNSQname 提取 DNS 查询报文 Question 段的域名（无法解析返回空串）。
// 仅用于特判「查询的域名是不是服务器域名」，不做完整报文解析
func DNSQname(query []byte) string {
	if len(query) < 12 {
		return ""
	}
	var sb strings.Builder
	for i := 12; i < len(query); {
		l := int(query[i])
		i++
		if l == 0 {
			break
		}
		if i+l > len(query) {
			return ""
		}
		if sb.Len() > 0 {
			sb.WriteByte('.')
		}
		sb.Write(query[i : i+l])
		i += l
	}
	return sb.String()
}

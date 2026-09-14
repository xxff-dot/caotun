package protocol

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
)

// TestDNSOverTCP 验证 DNS-over-TCP 长度前缀帧的收发往返
func TestDNSOverTCP(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	go func() {
		defer server.Close()
		var l [2]byte
		if _, err := io.ReadFull(server, l[:]); err != nil {
			return
		}
		query := make([]byte, binary.BigEndian.Uint16(l[:]))
		if _, err := io.ReadFull(server, query); err != nil {
			return
		}
		// 原样回等长应答（只验证帧格式，不解析 DNS 语义）
		binary.BigEndian.PutUint16(l[:], uint16(len(query)))
		server.Write(append(l[:], query...))
	}()

	resp, err := DNSOverTCP(client, []byte("query-bytes"))
	if err != nil {
		t.Fatalf("DNSOverTCP: %v", err)
	}
	if string(resp) != "query-bytes" {
		t.Fatalf("应答不匹配: %q", resp)
	}
}

// TestDNSQname 验证 Question 段域名提取
func TestDNSQname(t *testing.T) {
	build := func(name string) []byte {
		q := make([]byte, 12) // DNS 头部 12 字节
		for _, label := range splitLabels(name) {
			q = append(q, byte(len(label)))
			q = append(q, label...)
		}
		return append(q, 0, 0, 1, 0, 1) // 根标签 + QTYPE/QCLASS
	}
	for _, c := range []struct{ in, want string }{
		{"dl.google.com", "dl.google.com"},
		{"a.b", "a.b"},
		{"x", "x"},
	} {
		if got := DNSQname(build(c.in)); got != c.want {
			t.Errorf("DNSQname(%s) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := DNSQname([]byte("short")); got != "" {
		t.Errorf("短报文应返回空串, got %q", got)
	}
}

func splitLabels(name string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(name); i++ {
		if i == len(name) || name[i] == '.' {
			if i > start {
				out = append(out, name[start:i])
			}
			start = i + 1
		}
	}
	return out
}

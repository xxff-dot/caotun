package tun2sock

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

	resp, err := dnsOverTCP(client, []byte("query-bytes"))
	if err != nil {
		t.Fatalf("dnsOverTCP: %v", err)
	}
	if string(resp) != "query-bytes" {
		t.Fatalf("应答不匹配: %q", resp)
	}
}

package tun2sock

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

// TestDNSProbe 验证 dnsProbe 包装：超时设置 + 委托 protocol.DNSOverTCP 的帧收发往返
func TestDNSProbe(t *testing.T) {
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
		// 原样回等长应答（只验证超时与帧往返；帧实现本身已上移 protocol 包）
		binary.BigEndian.PutUint16(l[:], uint16(len(query)))
		server.Write(append(l[:], query...))
	}()

	resp, err := dnsProbe(client, []byte("query-bytes"), 2*time.Second)
	if err != nil {
		t.Fatalf("dnsProbe: %v", err)
	}
	if string(resp) != "query-bytes" {
		t.Fatalf("应答不匹配: %q", resp)
	}
}

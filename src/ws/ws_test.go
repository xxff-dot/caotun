package ws

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"testing"
	"time"
)

// 环回测试：模拟客户端 Upgrade 握手 → 双向帧收发（含 >64KB 大载荷与 mask 路径）
func TestWSFrameLoopback(t *testing.T) {
	c1, c2 := net.Pipe() // c1=服务端侧, c2=客户端侧
	defer c1.Close()
	defer c2.Close()

	// 客户端侧：发 Upgrade 请求 → 解析 101 → 得到 Conn(mask=true)。与真实客户端一样边发边读
	wsCCh := make(chan *Conn, 1)
	errCh := make(chan error, 1)
	go func() {
		req := "GET /tf HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n"
		if _, err := c2.Write([]byte(req)); err != nil {
			errCh <- err
			return
		}
		br := bufio.NewReader(c2)
		var headerBuf bytes.Buffer
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				errCh <- err
				return
			}
			headerBuf.WriteString(line)
			if line == "\r\n" {
				break
			}
		}
		if !bytes.Contains(headerBuf.Bytes(), []byte("101")) {
			t.Errorf("期望 101, 得到: %s", headerBuf.String())
			errCh <- io.ErrUnexpectedEOF
			return
		}
		wsCCh <- &Conn{conn: c2, br: br, mask: true}
	}()

	// 服务端侧握手（会写 101，需要客户端在读，故客户端逻辑放 goroutine）
	wsS, err := ServerConn(c1)
	if err != nil {
		t.Fatalf("服务端握手失败: %v", err)
	}
	var wsC *Conn
	select {
	case wsC = <-wsCCh:
	case err := <-errCh:
		t.Fatalf("客户端握手失败: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("客户端握手超时")
	}

	// 1) 服务端→客户端 128KB 大载荷（跨多个 64KB 长度编码 + 分帧读取）
	big := make([]byte, 128*1024)
	rand.Read(big)
	bigDone := make(chan error, 1)
	go func() {
		_, err := wsS.Write(big)
		bigDone <- err
	}()
	got := make([]byte, len(big))
	if _, err := io.ReadFull(wsC, got); err != nil {
		t.Fatalf("客户端读取失败: %v", err)
	}
	if err := <-bigDone; err != nil {
		t.Fatalf("服务端写入失败: %v", err)
	}
	if !bytes.Equal(got, big) {
		t.Fatalf("大载荷不匹配: 期望 %d 字节, 实际 %d 字节", len(big), len(got))
	}

	// 2) 客户端→服务端 mask 路径（必须覆盖 >=126 字节：回归 mask 位曾错打到长度低字节的 bug）
	for _, size := range []int{18, 126, 300, 70000} {
		msg := make([]byte, size)
		rand.Read(msg)
		go func(m []byte) { wsC.Write(m) }(msg)
		got2 := make([]byte, len(msg))
		if _, err := io.ReadFull(wsS, got2); err != nil {
			t.Fatalf("服务端读取失败(%dB): %v", size, err)
		}
		if !bytes.Equal(got2, msg) {
			t.Fatalf("mask 载荷不匹配(%dB)", size)
		}
	}
}

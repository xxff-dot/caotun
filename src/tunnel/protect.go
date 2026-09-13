// protect 客户端：移动端把出站 socket 的 fd 号发给宿主(ArkTS),
// 由宿主调 VpnConnection.protect() 打免捕获标记,防止隧道自身流量回环进 tun。
// 协议:发 fd 号文本 → 收 "ok"。与 ClashBox 的 protect 通道同思路。
package tunnel

import (
	"bufio"
	"net"
	"strconv"
	"sync"
	"time"
)

type protector struct {
	path string
	mu   sync.Mutex
	conn net.Conn
	rd   *bufio.Reader
}

func newProtector(path string) *protector {
	return &protector{path: path}
}

// protect 发送 fd 号并等待宿主确认;通道断了自动重连一次
func (p *protector) protect(fd int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	ok := p.send(fd)
	if !ok {
		p.closeLocked()
		ok = p.send(fd)
	}
	return ok
}

func (p *protector) send(fd int) bool {
	if p.conn == nil {
		conn, err := net.DialTimeout("unix", p.path, 3*time.Second)
		if err != nil {
			return false
		}
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		p.conn = conn
		p.rd = bufio.NewReader(conn)
	}
	if _, err := p.conn.Write([]byte(strconv.Itoa(fd))); err != nil {
		return false
	}
	var ack [2]byte
	if _, err := ioReadFullShort(p.rd, ack[:]); err != nil {
		return false
	}
	return ack[0] == 'o' && ack[1] == 'k'
}

func (p *protector) closeLocked() {
	if p.conn != nil {
		p.conn.Close()
		p.conn = nil
		p.rd = nil
	}
}

// ioReadFullShort 小工具:读满 n 字节
func ioReadFullShort(rd *bufio.Reader, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		m, err := rd.Read(buf[n:])
		if err != nil {
			return n, err
		}
		n += m
	}
	return n, nil
}

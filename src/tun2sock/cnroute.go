// 中国大陆 IPv4 段表:启动时从文本文件加载(CIDR 每行一段),
// 排序后二分查找判定目标 IP 是否大陆直连。数据源:
// github.com/gaoyifan/china-operator-ip (china.txt),随构建更新
package tun2sock

import (
	"bufio"
	"encoding/binary"
	"net"
	"os"
	"sort"
)

type cnRange struct{ start, end uint32 }

type CNMatcher struct {
	ranges []cnRange
}

// LoadCNMatcher 加载 CN 段表;文件缺失/为空返回 nil(全部走隧道)
func LoadCNMatcher(path string) *CNMatcher {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	m := &CNMatcher{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := trimCnLine(sc.Text())
		if line == "" {
			continue
		}
		_, ipnet, err := net.ParseCIDR(line)
		if err != nil || ipnet.IP.To4() == nil {
			continue
		}
		ip4 := ipnet.IP.To4()
		start := binary.BigEndian.Uint32(ip4)
		ones, bits := ipnet.Mask.Size()
		if bits != 32 || ones == 0 {
			continue
		}
		end := start | (^uint32(0) >> ones)
		m.ranges = append(m.ranges, cnRange{start, end})
	}
	if len(m.ranges) == 0 {
		return nil
	}
	sort.Slice(m.ranges, func(i, j int) bool { return m.ranges[i].start < m.ranges[j].start })
	// 合并重叠段
	merged := m.ranges[:1]
	for _, r := range m.ranges[1:] {
		last := &merged[len(merged)-1]
		if r.start <= last.end+1 {
			if r.end > last.end {
				last.end = r.end
			}
			continue
		}
		merged = append(merged, r)
	}
	m.ranges = merged
	return m
}

// IsCN 判定 IPv4 地址是否大陆直连段
func (m *CNMatcher) IsCN(ip net.IP) bool {
	if m == nil || ip.To4() == nil {
		return false
	}
	u := binary.BigEndian.Uint32(ip.To4())
	i := sort.Search(len(m.ranges), func(i int) bool { return m.ranges[i].end >= u })
	return i < len(m.ranges) && m.ranges[i].start <= u
}

func trimCnLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '#' {
			return ""
		}
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\r' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

// hasCNAnswer 解析 DNS 应答中的 A 记录,任一 IP 命中 CN 段表即返回 true
func hasCNAnswer(resp []byte, cn *CNMatcher) bool {
	if cn == nil || len(resp) < 12 {
		return false
	}
	qd := int(binary.BigEndian.Uint16(resp[4:6]))
	an := int(binary.BigEndian.Uint16(resp[6:8]))
	off := 12
	skipName := func() bool { // 跳过(可能压缩指向的)域名
		for {
			if off >= len(resp) {
				return false
			}
			l := int(resp[off])
			off++
			if l == 0 {
				return true
			}
			if l&0xC0 != 0 { // 压缩指针(2 字节)
				off++
				return true
			}
			off += l
		}
	}
	for i := 0; i < qd; i++ {
		if !skipName() {
			return false
		}
		off += 4
	}
	for i := 0; i < an && off+10 <= len(resp); i++ {
		if !skipName() {
			break
		}
		typ := binary.BigEndian.Uint16(resp[off : off+2])
		rdlen := int(binary.BigEndian.Uint16(resp[off+8 : off+10]))
		if typ == 1 && rdlen == 4 && off+10+4 <= len(resp) {
			if cn.IsCN(net.IP(resp[off+10 : off+14])) {
				return true
			}
		}
		off += 10 + rdlen
	}
	return false
}

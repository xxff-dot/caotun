// 中国大陆 IPv4 段表:内置随构建更新(数据源 github.com/gaoyifan/china-operator-ip),
// 启动解析后排序,二分查找判定目标 IP 是否大陆直连。Options.CIDRPath 可选外部文件覆盖。
package tun2sock

import (
	"bufio"
	"embed"
	"encoding/binary"
	"io"
	"net"
	"os"
	"sort"
)

//go:embed cn_cidr.txt
var cnCIDREmbed embed.FS

type cnRange struct{ start, end uint32 }

type CNMatcher struct {
	ranges []cnRange
}

// LoadCNMatcher 加载 CN 段表;外部文件缺失/为空返回 nil(调用方应回落内置表)
func LoadCNMatcher(path string) *CNMatcher {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	return loadCNFrom(f)
}

// LoadCNMatcherDefault 内置段表(随构建更新)
func LoadCNMatcherDefault() *CNMatcher {
	f, err := cnCIDREmbed.Open("cn_cidr.txt")
	if err != nil {
		return nil
	}
	defer f.Close()
	return loadCNFrom(f)
}

func loadCNFrom(r io.Reader) *CNMatcher {
	m := &CNMatcher{}
	sc := bufio.NewScanner(r)
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

// DirectOK 判定裸 IP 直连是否安全:仅 CN 公网段或内网/保留段可以直连。
// 其他 IP(国外等)直连拨号会被路由回 tun 造成自环,必须走隧道。
// 段表未加载时一律 false(全走隧道,安全优先)。
func (m *CNMatcher) DirectOK(ip net.IP) bool {
	if m == nil || ip.To4() == nil {
		return false
	}
	if b := ip.To4(); b[0] == 10 || (b[0] == 172 && b[1]&0xF0 == 16) || (b[0] == 192 && b[1] == 168) ||
		b[0] == 127 || (b[0] == 169 && b[1] == 254) || (b[0] == 100 && b[1]&0xFC == 64) {
		return true // 内网/回环/链路本地/CGNAT:路由在 tun 之外
	}
	return m.IsCN(ip)
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

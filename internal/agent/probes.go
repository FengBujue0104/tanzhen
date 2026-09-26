package agent

import (
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// 三网探测目标。探测走 TCP 握手而不是 ICMP：许多 VPS 的防火墙允许出站
// 80/443 却丢掉普通用户的 ICMP，TCP 得到的数字才是用户实际感受到的延迟。
//
// 默认目标为各省 CDN 节点主机名（百度智能云 / 白山云 / 火山 / 华为等），
// 格式 {province}-{ct|cu|cm}-v4.ip.zstaticcdn.com，端口优先 :80。
// 方法来源：NodeSeek「全国各省份三网 TCP-Ping IPv4 地址」
// https://www.nodeseek.com/post-68572-1
// 文档见 README「三网探测」一节。
//
// 可用环境变量覆盖候选主机（逗号分隔），例如：
//
//	TANZHEN_PROBE_HOSTS_CT=bj-ct-v4.ip.zstaticcdn.com,sh-ct-v4.ip.zstaticcdn.com
//	TANZHEN_PROBE_HOSTS_CU=...
//	TANZHEN_PROBE_HOSTS_CM=...
var DefaultTargets = defaultProbeTargets()

// Representative provinces: north / east / south / west / central, kept small
// so pickHost stays fast on every cache miss.
var defaultProvinces = []string{"bj", "sh", "gd", "js", "zj", "sc", "hb"}

// probePorts is the dial order for TCP-Ping. Prefer :80 (CDN convention from
// the NodeSeek post), then :443, then :53 only as a last resort for legacy
// ISP-DNS IP fallbacks. Tests may swap this slice for an ephemeral port.
var probePorts = []int{80, 443, 53}

func defaultProbeTargets() []ProbeTarget {
	return []ProbeTarget{
		{ISP: "ct", Name: "电信", Hosts: hostsForISP("ct")},
		{ISP: "cu", Name: "联通", Hosts: hostsForISP("cu")},
		{ISP: "cm", Name: "移动", Hosts: hostsForISP("cm")},
	}
}

func hostsForISP(isp string) []string {
	envKey := "TANZHEN_PROBE_HOSTS_" + strings.ToUpper(isp)
	if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	hosts := make([]string, 0, len(defaultProvinces))
	for _, p := range defaultProvinces {
		hosts = append(hosts, fmt.Sprintf("%s-%s-v4.ip.zstaticcdn.com", p, isp))
	}
	return hosts
}

type ProbeTarget struct {
	ISP   string
	Name  string
	Hosts []string
}

type ProbeResult struct {
	LatencyMs float64 // -1 if all failed
	LossPct   float64 // 0-100
}

// hostCache remembers the lowest-latency reachable host per ISP so a 30s tick
// does not re-race every CDN candidate + DNS every time.
const hostCacheTTL = 8 * time.Minute

type hostCacheEntry struct {
	host string
	at   time.Time
}

var (
	hostCacheMu sync.Mutex
	hostCache   = map[string]hostCacheEntry{}
)

// clearHostCache is for tests.
func clearHostCache() {
	hostCacheMu.Lock()
	hostCache = map[string]hostCacheEntry{}
	hostCacheMu.Unlock()
}

// ProbeISP runs count probes against the first reachable host for the ISP.
// Probing serially, not concurrently, keeps the result comparable: concurrent
// dials contend for the same uplink and inflate each other's latency.
func ProbeISP(t ProbeTarget, count int) ProbeResult {
	if count < 1 {
		count = 4
	}
	host := cachedPickHost(t.ISP, t.Hosts)
	if host == "" {
		return ProbeResult{LatencyMs: -1, LossPct: 100}
	}
	var ok int
	samples := make([]float64, 0, count)
	for i := 0; i < count; i++ {
		ms, err := tcpPingPrefer(host, 2*time.Second)
		if err == nil {
			ok++
			samples = append(samples, ms)
		}
		// A pause between probes: this runs on the agent's own tick, and
		// back-to-back dials report the connection cache, not the network.
		if i < count-1 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	loss := float64(count-ok) / float64(count) * 100
	if ok == 0 {
		return ProbeResult{LatencyMs: -1, LossPct: loss}
	}
	return ProbeResult{LatencyMs: median(samples), LossPct: loss}
}

func cachedPickHost(isp string, hosts []string) string {
	hostCacheMu.Lock()
	if e, ok := hostCache[isp]; ok && time.Since(e.at) < hostCacheTTL && e.host != "" {
		h := e.host
		hostCacheMu.Unlock()
		return h
	}
	hostCacheMu.Unlock()

	h := pickHost(hosts)
	if h == "" {
		return ""
	}
	hostCacheMu.Lock()
	hostCache[isp] = hostCacheEntry{host: h, at: time.Now()}
	hostCacheMu.Unlock()
	return h
}

// median returns the median of a non-empty slice (copy-sorted). For an even
// count it averages the two middle values.
func median(samples []float64) float64 {
	n := len(samples)
	if n == 0 {
		return 0
	}
	s := append([]float64(nil), samples...)
	sort.Float64s(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// pickHost probes every candidate concurrently and keeps the lowest latency
// among those that answered. Failed hosts sort last so a dead candidate
// (latency recorded as 0) never wins over a slow but reachable one.
func pickHost(hosts []string) string {
	if len(hosts) == 0 {
		return ""
	}
	if len(hosts) == 1 {
		return hosts[0]
	}
	type cand struct {
		host string
		ms   float64
		ok   bool
	}
	results := make([]cand, len(hosts))
	var wg sync.WaitGroup
	for i, h := range hosts {
		results[i].host = h
		wg.Add(1)
		go func(i int, h string) {
			defer wg.Done()
			if ms, err := tcpPingPrefer(h, 900*time.Millisecond); err == nil {
				results[i].ms = ms
				results[i].ok = true
			}
		}(i, h)
	}
	wg.Wait()

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].ok != results[j].ok {
			return results[i].ok // reachable first
		}
		if !results[i].ok {
			return false // both failed: keep stable relative order
		}
		return results[i].ms < results[j].ms
	})
	if results[0].ok {
		return results[0].host
	}
	// Nothing answered: keep the original order so the fallback host is stable.
	return hosts[0]
}

// tcpPingPrefer dials probePorts in order (:80 → :443 → :53 by default).
func tcpPingPrefer(host string, timeout time.Duration) (float64, error) {
	var last error
	for _, port := range probePorts {
		ms, err := tcpPing(host, port, timeout)
		if err == nil {
			return ms, nil
		}
		last = err
	}
	if last == nil {
		last = fmt.Errorf("unreachable: %s", host)
	}
	return 0, last
}

func tcpPing(host string, port int, timeout time.Duration) (float64, error) {
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return 0, err
	}
	_ = conn.Close()
	return float64(time.Since(start).Microseconds()) / 1000.0, nil
}

// ProbeAll runs CT/CU/CM probes concurrently. The three ISPs are independent
// paths, so running them together costs the same wall clock as one.
func ProbeAll(targets []ProbeTarget, count int) (ct, cu, cm ProbeResult) {
	if len(targets) == 0 {
		targets = DefaultTargets
	}
	var wg sync.WaitGroup
	results := make([]ProbeResult, len(targets))
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t ProbeTarget) {
			defer wg.Done()
			results[i] = ProbeISP(t, count)
		}(i, t)
	}
	wg.Wait()
	for i, t := range targets {
		switch t.ISP {
		case "ct":
			ct = results[i]
		case "cu":
			cu = results[i]
		case "cm":
			cm = results[i]
		}
	}
	return
}

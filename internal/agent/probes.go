package agent

import (
	"fmt"
	"net"
	"sort"
	"sync"
	"time"
)

// 三网探测目标。探测走 TCP 握手而不是 ICMP：许多 VPS 的防火墙允许出站
// 80/443 却丢掉普通用户的 ICMP，TCP 得到的数字才是用户实际感受到的延迟。
// 文档见 README「三网探测」一节。
var DefaultTargets = []ProbeTarget{
	{ISP: "ct", Name: "电信", Hosts: []string{"183.60.83.19", "202.96.128.86"}},  // DNSPod / 广州电信 DNS
	{ISP: "cu", Name: "联通", Hosts: []string{"202.106.0.20", "221.5.88.88"}},    // 北京联通 / 广东联通 DNS
	{ISP: "cm", Name: "移动", Hosts: []string{"211.136.192.6", "221.130.33.52"}}, // 移动 DNS
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

// ProbeISP runs count probes against the first reachable host for the ISP.
// Probing serially, not concurrently, keeps the result comparable: concurrent
// dials contend for the same uplink and inflate each other's latency.
func ProbeISP(t ProbeTarget, count int) ProbeResult {
	if count < 1 {
		count = 4
	}
	host := pickHost(t.Hosts)
	if host == "" {
		return ProbeResult{LatencyMs: -1, LossPct: 100}
	}
	var ok int
	var sum float64
	for i := 0; i < count; i++ {
		ms, err := tcpPing(host, 80, 2*time.Second)
		if err != nil {
			ms, err = tcpPing(host, 443, 2*time.Second)
		}
		if err == nil {
			ok++
			sum += ms
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
	return ProbeResult{LatencyMs: sum / float64(ok), LossPct: loss}
}

// pickHost probes every candidate concurrently and keeps the lowest median
// latency. Doing this in series costs up to 1.6s on a network that drops SYNs,
// which is exactly the case where the agent has least spare time.
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
	}
	results := make([]cand, len(hosts))
	var wg sync.WaitGroup
	for i, h := range hosts {
		results[i].host = h
		wg.Add(1)
		go func(i int, h string) {
			defer wg.Done()
			if ms, err := tcpPing(h, 443, 900*time.Millisecond); err == nil {
				results[i].ms = ms
				return
			}
			if ms, err := tcpPing(h, 80, 900*time.Millisecond); err == nil {
				results[i].ms = ms
			}
		}(i, h)
	}
	wg.Wait()

	sort.Slice(results, func(i, j int) bool { return results[i].ms < results[j].ms })
	if results[0].ms > 0 {
		return results[0].host
	}
	// Nothing answered: keep the original order so the fallback host is stable.
	return hosts[0]
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

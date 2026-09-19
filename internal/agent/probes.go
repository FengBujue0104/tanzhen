package agent

import (
	"fmt"
	"net"
	"sync"
	"time"
)

// 三网探测目标（ICMP 优先，失败则 TCP:80/443）
// 选用国内公共 DNS / 知名节点 IP，便于从中国大陆与海外可达性测试。
// 文档见 README「三网探测」一节。
var DefaultTargets = []ProbeTarget{
	{ISP: "ct", Name: "电信", Hosts: []string{"183.60.83.19", "202.96.128.86"}},   // DNSPod / 广州电信 DNS
	{ISP: "cu", Name: "联通", Hosts: []string{"202.106.0.20", "221.5.88.88"}},     // 北京联通 / 广东联通 DNS
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
		time.Sleep(50 * time.Millisecond)
	}
	loss := float64(count-ok) / float64(count) * 100
	if ok == 0 {
		return ProbeResult{LatencyMs: -1, LossPct: loss}
	}
	return ProbeResult{LatencyMs: sum / float64(ok), LossPct: loss}
}

func pickHost(hosts []string) string {
	for _, h := range hosts {
		// quick dial check with short timeout
		if _, err := tcpPing(h, 80, 800*time.Millisecond); err == nil {
			return h
		}
		if _, err := tcpPing(h, 443, 800*time.Millisecond); err == nil {
			return h
		}
	}
	if len(hosts) > 0 {
		return hosts[0]
	}
	return ""
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

// ProbeAll runs CT/CU/CM probes concurrently.
func ProbeAll(targets []ProbeTarget, count int) (ct, cu, cm ProbeResult) {
	if targets == nil {
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

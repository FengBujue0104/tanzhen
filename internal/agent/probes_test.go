package agent

import (
	"net"
	"strings"
	"testing"
	"time"
)

func TestMedian(t *testing.T) {
	cases := []struct {
		in   []float64
		want float64
	}{
		{nil, 0},
		{[]float64{10}, 10},
		{[]float64{10, 20}, 15},
		{[]float64{30, 10, 20}, 20},
		{[]float64{1, 100, 2, 3}, 2.5},
		{[]float64{5, 1, 9, 3, 7}, 5},
	}
	for _, c := range cases {
		orig := append([]float64(nil), c.in...)
		got := median(c.in)
		if got != c.want {
			t.Errorf("median(%v)=%v want %v", c.in, got, c.want)
		}
		for i := range orig {
			if c.in[i] != orig[i] {
				t.Errorf("median mutated input: before %v after %v", orig, c.in)
				break
			}
		}
	}
}

func startLocalTCP(t *testing.T) (host string, port int, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				close(done)
				return
			}
			_ = c.Close()
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port, func() { _ = ln.Close(); <-done }
}

func withProbePort(t *testing.T, port int) {
	t.Helper()
	prev := probePorts
	probePorts = []int{port}
	t.Cleanup(func() { probePorts = prev })
}

func TestPickHostFailedSortLast(t *testing.T) {
	clearHostCache()
	host, port, stop := startLocalTCP(t)
	defer stop()
	withProbePort(t, port)

	// Dead candidates first; reachable must still win (old bug: ms==0 sorted first).
	got := pickHost([]string{"203.0.113.1", "203.0.113.2", host})
	if got != host {
		t.Fatalf("pickHost=%q want reachable %q (failed must sort last)", got, host)
	}

	got = pickHost([]string{host, "203.0.113.1"})
	if got != host {
		t.Fatalf("pickHost=%q want %q", got, host)
	}

	// All fail → stable first candidate.
	got = pickHost([]string{"203.0.113.10", "203.0.113.11"})
	if got != "203.0.113.10" {
		t.Fatalf("all-fail pickHost=%q want first candidate", got)
	}
}

func TestProbeISPLocalListener(t *testing.T) {
	clearHostCache()
	host, port, stop := startLocalTCP(t)
	defer stop()
	withProbePort(t, port)

	r := ProbeISP(ProbeTarget{ISP: "test", Name: "test", Hosts: []string{host}}, 5)
	if r.LatencyMs < 0 {
		t.Fatalf("ProbeISP failed against local listener :%d: %+v", port, r)
	}
	if r.LossPct != 0 {
		t.Fatalf("expected 0 loss, got %+v", r)
	}
	if r.LatencyMs > 500 {
		t.Fatalf("local loopback latency suspiciously high: %vms (port %d)", r.LatencyMs, port)
	}
}

func TestProbeISPAllFail(t *testing.T) {
	clearHostCache()
	prev := probePorts
	probePorts = []int{1} // nothing listening
	t.Cleanup(func() { probePorts = prev })

	r := ProbeISP(ProbeTarget{ISP: "x", Name: "x", Hosts: []string{"203.0.113.50"}}, 2)
	if r.LatencyMs != -1 || r.LossPct != 100 {
		t.Fatalf("want total fail, got %+v", r)
	}
}

func TestHostsForISPEnvOverride(t *testing.T) {
	t.Setenv("TANZHEN_PROBE_HOSTS_CT", " a.example,b.example , ")
	got := hostsForISP("ct")
	want := []string{"a.example", "b.example"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("hostsForISP env override=%v want %v", got, want)
	}
}

func TestDefaultCDNHostsShape(t *testing.T) {
	t.Setenv("TANZHEN_PROBE_HOSTS_CT", "")
	t.Setenv("TANZHEN_PROBE_HOSTS_CU", "")
	t.Setenv("TANZHEN_PROBE_HOSTS_CM", "")
	for _, isp := range []string{"ct", "cu", "cm"} {
		h := hostsForISP(isp)
		if len(h) < 3 {
			t.Fatalf("%s: want >=3 provinces, got %v", isp, h)
		}
		for _, host := range h {
			if !strings.Contains(host, "-"+isp+"-") || !strings.HasSuffix(host, ".ip.zstaticcdn.com") {
				t.Fatalf("%s host %q missing CDN pattern", isp, host)
			}
		}
	}
}

func TestHostCacheHit(t *testing.T) {
	clearHostCache()
	hostCacheMu.Lock()
	hostCache["ct"] = hostCacheEntry{host: "cached.example", at: time.Now()}
	hostCacheMu.Unlock()
	got := cachedPickHost("ct", []string{"203.0.113.1", "203.0.113.2"})
	if got != "cached.example" {
		t.Fatalf("cache miss: got %q", got)
	}
}

func TestSelectedProvincesEnv(t *testing.T) {
	t.Setenv("TANZHEN_PROBE_PROVINCES", " BJ, sh ,gd,bj, bad!, ")
	t.Setenv("TANZHEN_PROBE_HOSTS_CT", "")
	got := selectedProvinces()
	want := []string{"bj", "sh", "gd"}
	if len(got) != len(want) {
		t.Fatalf("selectedProvinces=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("selectedProvinces=%v want %v", got, want)
		}
	}
	h := hostsForISP("ct")
	if len(h) != 3 || h[0] != "bj-ct-v4.ip.zstaticcdn.com" || h[2] != "gd-ct-v4.ip.zstaticcdn.com" {
		t.Fatalf("hosts from provinces=%v", h)
	}
}

func TestSelectedProvincesEmptyFallsBack(t *testing.T) {
	t.Setenv("TANZHEN_PROBE_PROVINCES", " , !!, ")
	got := selectedProvinces()
	if len(got) != len(defaultProvinces) {
		t.Fatalf("fallback want %d got %v", len(defaultProvinces), got)
	}
}

func TestHostsOverrideBeatsProvinces(t *testing.T) {
	t.Setenv("TANZHEN_PROBE_PROVINCES", "bj,sh")
	t.Setenv("TANZHEN_PROBE_HOSTS_CT", "custom.example")
	got := hostsForISP("ct")
	if len(got) != 1 || got[0] != "custom.example" {
		t.Fatalf("HOSTS override lost: %v", got)
	}
}

func TestProbeDisabled(t *testing.T) {
	t.Setenv("TANZHEN_PROBE_DISABLE", "")
	if ProbeDisabled() {
		t.Fatal("empty should be false")
	}
	t.Setenv("TANZHEN_PROBE_DISABLE", "1")
	if !ProbeDisabled() {
		t.Fatal("1 should disable")
	}
	t.Setenv("TANZHEN_PROBE_DISABLE", "YES")
	if !ProbeDisabled() {
		t.Fatal("YES should disable")
	}
}

func TestProbeIntervalFromEnv(t *testing.T) {
	t.Setenv("TANZHEN_PROBE_INTERVAL", "")
	t.Setenv("TANZHEN_PROBE_EVERY", "")
	if got := ProbeIntervalFromEnv(DefaultProbeInterval); got != DefaultProbeInterval {
		t.Fatalf("default=%v", got)
	}
	t.Setenv("TANZHEN_PROBE_EVERY", "45s")
	if got := ProbeIntervalFromEnv(DefaultProbeInterval); got != 45*time.Second {
		t.Fatalf("EVERY alias=%v", got)
	}
	t.Setenv("TANZHEN_PROBE_INTERVAL", "90s")
	if got := ProbeIntervalFromEnv(DefaultProbeInterval); got != 90*time.Second {
		t.Fatalf("INTERVAL preferred=%v", got)
	}
}

func TestProbeCountFromEnv(t *testing.T) {
	t.Setenv("TANZHEN_PROBE_COUNT", "")
	if ProbeCountFromEnv(4) != 4 {
		t.Fatal("default")
	}
	t.Setenv("TANZHEN_PROBE_COUNT", "8")
	if ProbeCountFromEnv(4) != 8 {
		t.Fatal("count 8")
	}
	t.Setenv("TANZHEN_PROBE_COUNT", "0")
	if ProbeCountFromEnv(4) != 4 {
		t.Fatal("zero invalid")
	}
}

func TestClampProbeInterval(t *testing.T) {
	if ClampProbeInterval(5*time.Second) != MinProbeInterval {
		t.Fatal("should clamp below floor")
	}
	if ClampProbeInterval(60*time.Second) != 60*time.Second {
		t.Fatal("should keep above floor")
	}
	if ClampProbeInterval(0) != 0 {
		t.Fatal("zero stays zero")
	}
}

func TestLoadTargetsHonorsProvinces(t *testing.T) {
	t.Setenv("TANZHEN_PROBE_PROVINCES", "zj")
	t.Setenv("TANZHEN_PROBE_HOSTS_CT", "")
	t.Setenv("TANZHEN_PROBE_HOSTS_CU", "")
	t.Setenv("TANZHEN_PROBE_HOSTS_CM", "")
	ts := LoadTargets()
	if len(ts) != 3 {
		t.Fatalf("want 3 ISPs, got %d", len(ts))
	}
	for _, tgt := range ts {
		if len(tgt.Hosts) != 1 || !strings.HasPrefix(tgt.Hosts[0], "zj-") {
			t.Fatalf("%s hosts=%v", tgt.ISP, tgt.Hosts)
		}
	}
}

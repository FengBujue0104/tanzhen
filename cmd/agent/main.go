package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/FengBujue0104/tanzhen/internal/agent"
)

func main() {
	hubURL := flag.String("hub", envOr("HUB_URL", ""), "Hub base URL, e.g. http://1.2.3.4:8080")
	token := flag.String("token", envOr("TOKEN", ""), "Node agent token")
	tokenFile := flag.String("token-file", envOr("TOKEN_FILE", ""),
		"Read the token from this file instead of the command line, so it cannot leak into /proc/cmdline or a task list")
	interval := flag.Duration("interval", durationEnv("TANZHEN_INTERVAL", 2*time.Second), "Report interval")
	probeEvery := flag.Duration("probe-every", agent.ProbeIntervalFromEnv(agent.DefaultProbeInterval),
		"三网 probe interval (env: TANZHEN_PROBE_INTERVAL / TANZHEN_PROBE_EVERY; floor 10s)")
	probeCount := flag.Int("probe-count", agent.ProbeCountFromEnv(agent.DefaultProbeCount),
		"Samples per ISP per 三网 round (env: TANZHEN_PROBE_COUNT)")
	probeProvinces := flag.String("probe-provinces", envOr("TANZHEN_PROBE_PROVINCES", ""),
		"Comma-separated province codes for CDN targets, e.g. bj,sh,gd (env: TANZHEN_PROBE_PROVINCES)")
	probeDisable := flag.Bool("probe-disable", agent.ProbeDisabled(),
		"Skip 三网 probes entirely (env: TANZHEN_PROBE_DISABLE=1)")
	flag.Parse()

	if *hubURL == "" {
		log.Fatal("hub URL is required (--hub or HUB_URL)")
	}
	*hubURL = strings.TrimRight(*hubURL, "/")

	tok, err := resolveToken(*token, *tokenFile)
	if err != nil {
		log.Fatal(err)
	}
	if tok == "" {
		log.Fatal("token is required (--token / --token-file, or TOKEN / TOKEN_FILE)")
	}

	// Flags win over Ambient env for provinces/disable so Windows scheduled
	// tasks (no EnvironmentFile) and one-click installers can push settings
	// through argv alone. LoadTargets / ProbeDisabled read the process env.
	if *probeProvinces != "" {
		_ = os.Setenv("TANZHEN_PROBE_PROVINCES", *probeProvinces)
	}
	if *probeDisable {
		_ = os.Setenv("TANZHEN_PROBE_DISABLE", "1")
	}

	if !*probeDisable {
		clamped := agent.ClampProbeInterval(*probeEvery)
		if clamped != *probeEvery {
			log.Printf("probe-every %s below floor %s; clamping", *probeEvery, agent.MinProbeInterval)
			*probeEvery = clamped
		}
	}
	if *probeCount < 1 {
		*probeCount = 1
	}

	col := agent.NewCollector()
	rep := agent.NewReporter(*hubURL, tok)

	// Warm up the network counters and the per-interface map so the first real
	// report already has a rate and the hub never draws a flat zero.
	_ = col.Collect()
	time.Sleep(500 * time.Millisecond)

	// The probe results are shared between the probe goroutine and the report
	// loop, so they travel under a mutex. The -1 latency placeholders keep the
	// first heartbeat honest ("no answer yet") rather than reporting 0ms.
	var pmu sync.Mutex
	probes := [3]agent.ProbeResult{{LatencyMs: -1}, {LatencyMs: -1}, {LatencyMs: -1}}

	doProbe := func() {
		ct, cu, cm := agent.ProbeAll(nil, *probeCount)
		pmu.Lock()
		probes = [3]agent.ProbeResult{ct, cu, cm}
		pmu.Unlock()
		log.Printf("三网探测: 电信=%.0fms/%.0f%% 联通=%.0fms/%.0f%% 移动=%.0fms/%.0f%%",
			ct.LatencyMs, ct.LossPct, cu.LatencyMs, cu.LossPct, cm.LatencyMs, cm.LossPct)
	}

	done := make(chan struct{})
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	if *probeDisable {
		log.Printf("三网探测已禁用 (零探测流量)")
	} else {
		// Run one round before the first report so it carries real numbers rather
		// than the placeholders. It is on the startup path here, never on the tick.
		targets := agent.LoadTargets()
		var provHint string
		if p := os.Getenv("TANZHEN_PROBE_PROVINCES"); p != "" {
			provHint = " provinces=" + p
		} else if len(targets) > 0 && len(targets[0].Hosts) > 0 {
			provHint = " hosts_per_isp=" + strconv.Itoa(len(targets[0].Hosts))
		}
		log.Printf("三网探测: interval=%s count=%d%s", *probeEvery, *probeCount, provHint)
		doProbe()

		// Probing blocks the goroutine that runs it for the whole round — over 20s
		// when a carrier is unreachable. Doing that inside the report loop stalled
		// the heartbeat: a 30s probe interval became a 50s report cadence, and the
		// ticker's buffered tick then fired two reports back to back, the second
		// measuring a zero-byte network delta that the hub drew as a dip to 0. So
		// the rounds run here instead, on their own cadence.
		go func() {
			for {
				select {
				case <-done:
					return
				case <-time.After(*probeEvery):
					doProbe()
				}
			}
		}()
	}

	log.Printf("tanzhen agent v%s → %s (interval=%s probe_every=%s disable=%v)",
		agent.Version, *hubURL, *interval, *probeEvery, *probeDisable)

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	report := func() {
		hb := col.Collect()
		pmu.Lock()
		hb.LatencyCT, hb.LossCT = probes[0].LatencyMs, probes[0].LossPct
		hb.LatencyCU, hb.LossCU = probes[1].LatencyMs, probes[1].LossPct
		hb.LatencyCM, hb.LossCM = probes[2].LatencyMs, probes[2].LossPct
		pmu.Unlock()
		if err := rep.Send(&hb); err != nil {
			log.Printf("report error: %v", err)
		}
	}
	report()

	for {
		select {
		case <-ticker.C:
			report()
		case <-stop:
			log.Println("bye")
			close(done)
			return
		}
	}
}

// resolveToken prefers the file form. Both are accepted because a hand-run
// agent is fine with --token, but a service manager's unit file shows up in
// ps output where a file path shows nothing.
func resolveToken(flagToken, file string) (string, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	return strings.TrimSpace(flagToken), nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func durationEnv(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

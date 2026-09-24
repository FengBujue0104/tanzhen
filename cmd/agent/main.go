package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
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
	interval := flag.Duration("interval", 2*time.Second, "Report interval")
	probeEvery := flag.Duration("probe-every", 30*time.Second, "三网 probe interval")
	probeCount := flag.Int("probe-count", 4, "Packets per 三网 probe")
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
	// Run one round before the first report so it carries real numbers rather
	// than the placeholders. It is on the startup path here, never on the tick.
	doProbe()

	done := make(chan struct{})
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

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

	log.Printf("tanzhen agent v%s → %s (interval=%s probe_every=%s)",
		agent.Version, *hubURL, *interval, *probeEvery)

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

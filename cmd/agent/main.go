package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/FengBujue0104/tanzhen/internal/agent"
)

func main() {
	hubURL := flag.String("hub", env("HUB_URL", ""), "Hub base URL, e.g. http://1.2.3.4:8080")
	token := flag.String("token", env("TOKEN", ""), "Node agent token")
	interval := flag.Duration("interval", 2*time.Second, "Report interval")
	probeEvery := flag.Duration("probe-every", 30*time.Second, "三网 probe interval")
	diskPath := flag.String("disk", env("DISK_PATH", ""), "Disk path to monitor")
	flag.Parse()

	if *hubURL == "" || *token == "" {
		log.Fatal("hub URL and token are required (--hub / --token or HUB_URL / TOKEN)")
	}
	*hubURL = strings.TrimRight(*hubURL, "/")

	col := agent.NewCollector(*diskPath)
	rep := agent.NewReporter(*hubURL, *token)

	// warm-up net counters
	_ = col.Collect()
	time.Sleep(500 * time.Millisecond)

	var lastProbe time.Time
	var ct, cu, cm agent.ProbeResult
	ct.LatencyMs, cu.LatencyMs, cm.LatencyMs = -1, -1, -1

	doProbe := func() {
		ct, cu, cm = agent.ProbeAll(nil, 4)
		lastProbe = time.Now()
		log.Printf("三网探测: 电信=%.0fms/%.0f%% 联通=%.0fms/%.0f%% 移动=%.0fms/%.0f%%",
			ct.LatencyMs, ct.LossPct, cu.LatencyMs, cu.LossPct, cm.LatencyMs, cm.LossPct)
	}
	doProbe()

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	log.Printf("tanzhen agent v%s → %s (interval=%s)", agent.Version, *hubURL, *interval)

	report := func() {
		if time.Since(lastProbe) >= *probeEvery {
			doProbe()
		}
		hb := col.Collect()
		hb.LatencyCT, hb.LossCT = ct.LatencyMs, ct.LossPct
		hb.LatencyCU, hb.LossCU = cu.LatencyMs, cu.LossPct
		hb.LatencyCM, hb.LossCM = cm.LatencyMs, cm.LossPct
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
			return
		}
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

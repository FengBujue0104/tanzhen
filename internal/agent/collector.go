package agent

import (
	"runtime"
	"sync"
	"time"

	"github.com/FengBujue0104/tanzhen/internal/models"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
)

type Collector struct {
	mu       sync.Mutex
	prevNet  map[string]net.IOCountersStat
	prevTime time.Time
	diskPath string
}

func NewCollector(diskPath string) *Collector {
	if diskPath == "" {
		diskPath = "/"
		if runtime.GOOS == "windows" {
			diskPath = "C:"
		}
	}
	return &Collector{diskPath: diskPath, prevNet: map[string]net.IOCountersStat{}}
}

func (c *Collector) Collect() models.Heartbeat {
	hb := models.Heartbeat{
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		AgentVer: Version,
	}

	if pct, err := cpu.Percent(0, false); err == nil && len(pct) > 0 {
		hb.CPUUsage = pct[0]
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		hb.MemTotal = vm.Total
		hb.MemUsed = vm.Used
		hb.MemUsage = vm.UsedPercent
	}
	if sw, err := mem.SwapMemory(); err == nil {
		hb.SwapTotal = sw.Total
		hb.SwapUsed = sw.Used
	}
	if du, err := disk.Usage(c.diskPath); err == nil {
		hb.DiskTotal = du.Total
		hb.DiskUsed = du.Used
		hb.DiskUsage = du.UsedPercent
	}
	if hi, err := host.Info(); err == nil {
		hb.Hostname = hi.Hostname
		hb.Uptime = int64(hi.Uptime)
	}

	c.fillNet(&hb)
	return hb
}

func (c *Collector) fillNet(hb *models.Heartbeat) {
	counters, err := net.IOCounters(true)
	if err != nil {
		return
	}
	now := time.Now()
	var totalSent, totalRecv uint64
	cur := make(map[string]net.IOCountersStat, len(counters))
	for _, n := range counters {
		if n.Name == "lo" || n.Name == "lo0" {
			continue
		}
		cur[n.Name] = n
		totalSent += n.BytesSent
		totalRecv += n.BytesRecv
	}
	hb.NetTotalUp = totalSent
	hb.NetTotalDown = totalRecv

	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.prevTime.IsZero() {
		dt := now.Sub(c.prevTime).Seconds()
		if dt > 0.1 {
			var up, down uint64
			for name, n := range cur {
				if p, ok := c.prevNet[name]; ok {
					if n.BytesSent >= p.BytesSent {
						up += n.BytesSent - p.BytesSent
					}
					if n.BytesRecv >= p.BytesRecv {
						down += n.BytesRecv - p.BytesRecv
					}
				}
			}
			hb.NetUp = uint64(float64(up) / dt)
			hb.NetDown = uint64(float64(down) / dt)
		}
	}
	c.prevNet = cur
	c.prevTime = now
}

var Version = "0.1.0"

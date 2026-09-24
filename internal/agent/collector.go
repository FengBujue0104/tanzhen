package agent

import (
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/FengBujue0104/tanzhen/internal/models"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

// pseudoFSTypes are mounts that carry no user data. Reporting them would fill
// the UI with /dev, /sys and cgroup entries that are pinned near 100%.
var pseudoFSTypes = map[string]bool{
	"proc": true, "sysfs": true, "devtmpfs": true, "devpts": true,
	"tmpfs": true, "ramfs": true, "cgroup": true, "cgroup2": true,
	"securityfs": true, "debugfs": true, "tracefs": true, "pstore": true,
	"bpf": true, "configfs": true, "fusectl": true, "hugetlbfs": true,
	"mqueue": true, "autofs": true, "binfmt_misc": true, "rpc_pipefs": true,
	"nsfs": true, "fuse.gvfsd-fuse": true, "fuse.portal": true,
	"squashfs": true, "anon_inodefs": true, "pipefs": true,
	"sockfs": true, "bdev": true, "rootfs": true, "efivarfs": true,
	"selinuxfs": true, "nfsd": true, "9p": true, "cpio": true,
	// overlay is deliberately absent: on LXC containers it *is* the root
	// filesystem, and the container's own bind mounts are caught below.
}

// pseudoMountPrefixes covers mounts the fstype alone does not disqualify —
// read-only boot partitions, snap loopbacks, docker's bind mounts and the
// configuration filesystems some images mount separately.
var pseudoMountPrefixes = []string{
	"/proc", "/sys", "/dev", "/run", "/snap", "/boot/efi", "/var/lib/docker",
	"/etc",
}

// minDiskBytes hides tiny ram disks and loop mounts that are not real storage.
const minDiskBytes = 1 << 30 // 1 GiB

type Collector struct {
	mu       sync.Mutex
	prevNet  map[string]net.IOCountersStat
	prevTime time.Time

	// Read once: it cannot change while the process runs, and host.Info() is
	// slow enough on some containers to be worth caching.
	hostOnce sync.Once
	hostInfo host.InfoStat

	coresOnce sync.Once
	cores     int
}

func NewCollector() *Collector {
	return &Collector{prevNet: map[string]net.IOCountersStat{}}
}

// Collect gathers one heartbeat sample.
func (c *Collector) Collect() models.Heartbeat {
	hb := models.Heartbeat{
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		AgentVer: Version,
		CPUCores: c.coreCount(),
	}

	// cpu.Percent blocks for its whole sampling window, so start it in the
	// background and let it overlap the disk and host probes.
	percentCh := c.cpuPercentAsync()

	c.host(&hb)
	c.memory(&hb)
	c.disks(&hb)

	if pct, ok := <-percentCh; ok {
		hb.CPUUsage = pct
	}
	c.fillNet(&hb)
	return hb
}

func (c *Collector) cpuPercentAsync() <-chan float64 {
	ch := make(chan float64, 1)
	go func() {
		pct, err := cpu.Percent(0, false)
		if err != nil || len(pct) == 0 {
			ch <- 0
			return
		}
		ch <- pct[0]
	}()
	return ch
}

func (c *Collector) coreCount() int {
	c.coresOnce.Do(func() {
		if n, err := cpu.Counts(true); err == nil && n > 0 {
			c.cores = n
		} else {
			c.cores = runtime.NumCPU()
		}
	})
	return c.cores
}

func (c *Collector) host(hb *models.Heartbeat) {
	c.hostOnce.Do(func() {
		if hi, err := host.Info(); err == nil {
			c.hostInfo = *hi
		}
	})
	hi := c.hostInfo

	hb.Hostname = hi.Hostname
	hb.Uptime = int64(hi.Uptime)
	hb.Kernel = hi.KernelVersion
	hb.Distro = distroName(hi.Platform, hi.PlatformVersion)

	if procs, err := process.Pids(); err == nil {
		hb.Processes = len(procs)
	}
	if avg, err := load.Avg(); err == nil && avg != nil {
		hb.Load = models.LoadAvg{L1: avg.Load1, L5: avg.Load5, L15: avg.Load15}
	}
}

// distroName joins the platform and release into the label shown in the UI.
// Windows reports its build number here ("10.0.19045"), Linux the major
// release ("12").
func distroName(platform, version string) string {
	s := strings.TrimSpace(platform + " " + version)
	if s == "" {
		return runtime.GOOS
	}
	return s
}

func (c *Collector) memory(hb *models.Heartbeat) {
	if vm, err := mem.VirtualMemory(); err == nil {
		hb.MemTotal = vm.Total
		hb.MemUsed = vm.Used
		hb.MemUsage = usedPct(vm.Used, vm.Total)
	}
	if sw, err := mem.SwapMemory(); err == nil {
		hb.SwapTotal = sw.Total
		hb.SwapUsed = sw.Used
	}
}

// usedPct is the percentage of total that used represents. gopsutil's own
// UsedPercent is not used for memory: on Windows it is the OS load figure from
// GlobalMemoryStatusEx, a whole number that never matches the used/total pair
// reported beside it (74 against 74.11), and the dashboard shows both. Deriving
// it here keeps the card's headline number and its byte counts the same value
// on every platform.
func usedPct(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}

// disks lists every real filesystem. Primary is the largest, and its numbers
// are mirrored into the flat Disk* fields so an older hub keeps working.
func (c *Collector) disks(hb *models.Heartbeat) {
	parts, err := disk.Partitions(false)
	if err != nil {
		return
	}
	hb.Disks = make([]models.DiskInfo, 0, len(parts))
	for _, p := range parts {
		if isPseudoFS(p.Fstype, p.Mountpoint) {
			continue
		}
		du, err := disk.Usage(p.Mountpoint)
		if err != nil || du.Total < minDiskBytes {
			continue
		}
		hb.Disks = append(hb.Disks, models.DiskInfo{
			Mount:   p.Mountpoint,
			Device:  p.Device,
			FSType:  p.Fstype,
			Total:   du.Total,
			Used:    du.Used,
			UsedPct: du.UsedPercent,
		})
	}
	// Largest first, so the primary disk is index 0; ties broken by mount point
	// keeps the choice (and the ordering) stable between reports.
	sort.Slice(hb.Disks, func(i, j int) bool {
		if hb.Disks[i].Total != hb.Disks[j].Total {
			return hb.Disks[i].Total > hb.Disks[j].Total
		}
		return hb.Disks[i].Mount < hb.Disks[j].Mount
	})

	// A filesystem mounted twice (container bind mounts) appears once per mount
	// point; dedupe on device so the list is one entry per real disk.
	seenDevice := make(map[string]bool, len(hb.Disks))
	dedup := hb.Disks[:0]
	for _, d := range hb.Disks {
		if d.Device != "" {
			if seenDevice[d.Device] {
				continue
			}
			seenDevice[d.Device] = true
		}
		dedup = append(dedup, d)
	}
	hb.Disks = dedup

	if len(hb.Disks) > 0 {
		p := hb.Disks[0]
		hb.DiskMount, hb.DiskTotal, hb.DiskUsed, hb.DiskUsage = p.Mount, p.Total, p.Used, p.UsedPct
	}
}

func isPseudoFS(fstype, mount string) bool {
	if fstype == "" || pseudoFSTypes[fstype] {
		return true
	}
	for _, pre := range pseudoMountPrefixes {
		if mount == pre || strings.HasPrefix(mount, pre) {
			return true
		}
	}
	return false
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
		if isLoopback(n.Name) {
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
		// Skip implausibly short windows: a rate derived from a 100ms gap is
		// noise the hub's sparkline would remember for two minutes.
		if dt > 0.25 {
			var up, down uint64
			for name, n := range cur {
				p, ok := c.prevNet[name]
				if !ok {
					continue
				}
				// A counter that went backwards is an interface reset, not
				// negative traffic.
				if n.BytesSent >= p.BytesSent {
					up += n.BytesSent - p.BytesSent
				}
				if n.BytesRecv >= p.BytesRecv {
					down += n.BytesRecv - p.BytesRecv
				}
			}
			hb.NetUp = uint64(float64(up) / dt)
			hb.NetDown = uint64(float64(down) / dt)
		}
	}
	c.prevNet = cur
	c.prevTime = now
}

// isLoopback skips lo/lo0 and Windows' loopback pseudo-interface, whose
// counters are unbounded and would swamp the real uplinks.
func isLoopback(name string) bool {
	switch name {
	case "lo", "lo0", "Loopback", "Loopback Pseudo-Interface 1":
		return true
	}
	return strings.HasPrefix(strings.ToLower(name), "loopback")
}

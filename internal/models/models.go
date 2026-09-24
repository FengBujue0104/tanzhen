package models

import "time"

// Heartbeat is the JSON payload agents POST to the hub.
type Heartbeat struct {
	CPUUsage  float64 `json:"cpu_usage"`  // percent 0-100
	CPUCores  int     `json:"cpu_cores"`  // logical cores
	Load      LoadAvg `json:"load"`       // 1/5/15 min ({} on Windows)
	MemTotal  uint64  `json:"mem_total"`  // bytes
	MemUsed   uint64  `json:"mem_used"`   // bytes
	MemUsage  float64 `json:"mem_usage"`  // percent
	SwapTotal uint64  `json:"swap_total"` // bytes
	SwapUsed  uint64  `json:"swap_used"`  // bytes

	// Disks lists every real mounted filesystem. Primary is the largest one,
	// also mirrored into Disk* for backwards compatibility.
	Disks     []DiskInfo `json:"disks,omitempty"`
	DiskMount string     `json:"disk_mount"`
	DiskTotal uint64     `json:"disk_total"`
	DiskUsed  uint64     `json:"disk_used"`
	DiskUsage float64    `json:"disk_usage"`

	NetUp   uint64 `json:"net_up"`   // bytes/s uplink
	NetDown uint64 `json:"net_down"` // bytes/s downlink
	// Cumulative counters since boot/host start. The hub derives per-period
	// traffic from these.
	NetTotalUp   uint64 `json:"net_total_up"`
	NetTotalDown uint64 `json:"net_total_down"`

	// 三网 latency (ms) and packet loss (%); -1 = probe failed / N/A
	LatencyCT float64 `json:"latency_ct"` // 电信
	LatencyCU float64 `json:"latency_cu"` // 联通
	LatencyCM float64 `json:"latency_cm"` // 移动
	LossCT    float64 `json:"loss_ct"`
	LossCU    float64 `json:"loss_cu"`
	LossCM    float64 `json:"loss_cm"`

	Uptime    int64  `json:"uptime"`    // agent host uptime seconds
	Processes int    `json:"processes"` // best-effort process count
	Hostname  string `json:"hostname"`  //
	OS        string `json:"os"`        // GOOS: linux / windows
	Distro    string `json:"distro"`    // "Debian GNU/Linux 12" etc.
	Arch      string `json:"arch"`      //
	Kernel    string `json:"kernel"`    //
	AgentVer  string `json:"agent_ver"` //
}

// DiskInfo is one mounted filesystem.
type DiskInfo struct {
	Mount   string  `json:"mount"`   // "/", "/data", "C:"
	Device  string  `json:"device"`  // "/dev/sda1", "C:"
	FSType  string  `json:"fs_type"` // "ext4", "ntfs"
	Total   uint64  `json:"total"`   // bytes
	Used    uint64  `json:"used"`    // bytes
	UsedPct float64 `json:"used_pct"`
}

// LoadAvg holds the three load averages.
type LoadAvg struct {
	L1  float64 `json:"l1"`
	L5  float64 `json:"l5"`
	L15 float64 `json:"l15"`
}

// NodeMeta is hub-side editable metadata.
type NodeMeta struct {
	TrafficQuota  uint64 `json:"traffic_quota"`  // bytes; 0 = 不统计 / 不限
	TrafficPeriod int    `json:"traffic_period"` // days; 0 = 不重置, 30 = 每月
	Bandwidth     string `json:"bandwidth"`      // e.g. "1Gbps"
	RenewalDate   string `json:"renewal_date"`   // e.g. "2026-12-01"
	Price         string `json:"price"`          // e.g. "¥30/月"
	Location      string `json:"location"`       // e.g. "香港"
	Provider      string `json:"provider"`       // e.g. "阿里云"
	Note          string `json:"note"`           // free text
}

// Traffic is the hub-computed quota view for a node.
type Traffic struct {
	Quota      uint64     `json:"quota"`       // bytes, 0 = 未设置
	Unlimited  bool       `json:"unlimited"`   // true when no quota configured
	PeriodDays int        `json:"period_days"` //
	ResetAt    *time.Time `json:"reset_at,omitempty"`
	Used       uint64     `json:"used"`      // up+down since period start
	UsedUp     uint64     `json:"used_up"`   //
	UsedDown   uint64     `json:"used_down"` //
	Remaining  int64      `json:"remaining"` // bytes; -1 when unlimited
	Pct        float64    `json:"pct"`       // 0-100; -1 when unlimited
}

// Sample is one point of the in-metric history ring.
type Sample struct {
	T    int64   `json:"t"`    // unix seconds
	CPU  float64 `json:"cpu"`  //
	Mem  float64 `json:"mem"`  //
	Up   uint64  `json:"up"`   // bytes/s
	Down uint64  `json:"down"` // bytes/s
}

// NodeStatus is the public view of a node.
type NodeStatus struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Online    bool       `json:"online"`
	LastSeen  time.Time  `json:"last_seen"`
	CreatedAt time.Time  `json:"created_at"`
	Meta      NodeMeta   `json:"meta"`
	Traffic   Traffic    `json:"traffic"`
	Metrics   *Heartbeat `json:"metrics,omitempty"`
	History   []Sample   `json:"history,omitempty"`
}

// CreateNodeRequest for admin API.
type CreateNodeRequest struct {
	Name string   `json:"name"`
	Meta NodeMeta `json:"meta"`
}

// CreateNodeResponse returned after creating a node.
type CreateNodeResponse struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Token      string `json:"token"`
	InstallCmd string `json:"install_cmd"`
	WinCmd     string `json:"win_cmd"`
	InstallURL string `json:"install_url"`
	WinURL     string `json:"win_url"`
	AgentBin   string `json:"agent_bin"`
	HubURL     string `json:"hub_url"`
}

// UpdateNodeRequest for PATCH.
type UpdateNodeRequest struct {
	Name *string   `json:"name,omitempty"`
	Meta *NodeMeta `json:"meta,omitempty"`
}

// TrafficState is the persisted traffic accounting baseline.
type TrafficState struct {
	BaseUp   uint64    `json:"base_up"`   // cumulative counter at period start
	BaseDown uint64    `json:"base_down"` //
	ResetAt  time.Time `json:"reset_at"`  // next period boundary
}

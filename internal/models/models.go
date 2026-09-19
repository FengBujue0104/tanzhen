package models

import "time"

// Heartbeat is the JSON payload agents POST to the hub.
type Heartbeat struct {
	CPUUsage    float64 `json:"cpu_usage"`    // percent 0-100
	MemTotal    uint64  `json:"mem_total"`    // bytes
	MemUsed     uint64  `json:"mem_used"`     // bytes
	MemUsage    float64 `json:"mem_usage"`    // percent
	SwapTotal   uint64  `json:"swap_total"`   // bytes
	SwapUsed    uint64  `json:"swap_used"`    // bytes
	DiskTotal   uint64  `json:"disk_total"`   // bytes
	DiskUsed    uint64  `json:"disk_used"`    // bytes
	DiskUsage   float64 `json:"disk_usage"`   // percent
	NetUp       uint64  `json:"net_up"`       // bytes/s uplink
	NetDown     uint64  `json:"net_down"`     // bytes/s downlink
	NetTotalUp  uint64  `json:"net_total_up"` // cumulative bytes
	NetTotalDown uint64 `json:"net_total_down"`
	// 三网 latency (ms) and packet loss (%); -1 = probe failed / N/A
	LatencyCT   float64 `json:"latency_ct"`   // 电信
	LatencyCU   float64 `json:"latency_cu"`   // 联通
	LatencyCM   float64 `json:"latency_cm"`   // 移动
	LossCT      float64 `json:"loss_ct"`
	LossCU      float64 `json:"loss_cu"`
	LossCM      float64 `json:"loss_cm"`
	Uptime      int64   `json:"uptime"`       // agent host uptime seconds
	Hostname    string  `json:"hostname"`
	OS          string  `json:"os"`
	Arch        string  `json:"arch"`
	AgentVer    string  `json:"agent_ver"`
}

// NodeMeta is hub-side editable metadata.
type NodeMeta struct {
	TrafficRemain string `json:"traffic_remain"` // e.g. "500GB" or "无限"
	Bandwidth     string `json:"bandwidth"`      // e.g. "1Gbps"
	RenewalDate   string `json:"renewal_date"`   // e.g. "2026-12-01"
	Price         string `json:"price"`          // e.g. "¥30/月"
	Location      string `json:"location"`       // e.g. "香港"
	Provider      string `json:"provider"`
	Note          string `json:"note"`
}

// NodeStatus is the public view of a node.
type NodeStatus struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Online        bool      `json:"online"`
	LastSeen      time.Time `json:"last_seen"`
	CreatedAt     time.Time `json:"created_at"`
	Meta          NodeMeta  `json:"meta"`
	Metrics       *Heartbeat `json:"metrics,omitempty"`
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
}

// UpdateNodeRequest for PATCH.
type UpdateNodeRequest struct {
	Name *string   `json:"name,omitempty"`
	Meta *NodeMeta `json:"meta,omitempty"`
}

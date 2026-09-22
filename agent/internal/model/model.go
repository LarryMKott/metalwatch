// Package model 是 Agent 侧的采集数据模型，对应 backend/proto/agent.proto 的消息定义。
//
// 之所以单独成包：collect 与 report 都需要这些类型，放任一侧都会形成 import 环。
// proto 代码生成（make pb）落地后，本包类型将与 pb 消息互相转换，字段名保持一致。
package model

// Sample 是一次指标采样。
// Labels 严禁放入序列号、时间戳等高基数值（服务端会拒绝，见 docs/03 §3）。
type Sample struct {
	Name   string
	Labels map[string]string
	Value  float64
}

// DiskInfo 是磁盘的采集结果。
type DiskInfo struct {
	Device            string
	Model             string
	Serial            string
	MediaType         string // HDD / SSD / NVMe
	CapacityBytes     uint64
	TempCelsius       int
	SmartStatus       string // OK / PreFail / Fail
	ReallocatedSector uint64
	PowerOnHours      uint64
	WearPercent       float64
}

// RaidArray 是 RAID 阵列状态。
type RaidArray struct {
	Controller     string
	Array          string
	Level          string
	State          string // Optimal / Degraded / Failed / Rebuilding
	RebuildPercent float64
	Disks          int
	FailedDisks    int
}

// Component 是硬件清单中的一个部件。
type Component struct {
	Category      string // cpu / memory / disk / nic / raid_controller / psu / fan
	Slot          string
	Name          string
	Vendor        string
	Serial        string
	Firmware      string
	CapacityBytes uint64
}

// Inventory 是静态硬件清单（指纹未变化时可不上报）。
type Inventory struct {
	Fingerprint string
	Components  []Component
	BIOSVersion string
	Mainboard   string
	BMCFirmware string
}

// Report 是一次完整上报的载荷。
type Report struct {
	Mode      string // agent
	Metrics   []Sample
	Disks     []DiskInfo
	RAID      []RaidArray
	Inventory *Inventory
}

// HostIdentity 是注册时上报的机器标识。
type HostIdentity struct {
	Hostname     string
	PrimaryIP    string
	SMBIOSUUID   string
	OSType       string
	OSVersion    string
	AgentVersion string
	Collect      []string
}

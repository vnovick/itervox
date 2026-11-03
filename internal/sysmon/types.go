package sysmon

import "time"

// Activity modes (agent.activity_audit).
const (
	ModeOff     = "off"
	ModeSampled = "sampled"
	ModePrecise = "precise"
)

// SampleInterval is the shortest gap between two samples of one run.
const SampleInterval = 2 * time.Second

// ChildCommand is one live process of a run, for the "top child commands"
// list: its name and a short, redacted argument string.
type ChildCommand struct {
	PID        int     `json:"pid"`
	Name       string  `json:"name"`
	Args       string  `json:"args,omitempty"`
	CPUPercent float64 `json:"cpuPercent"`
	RSSBytes   uint64  `json:"rssBytes"`
}

// RunUsage is one run's resource use: the latest sample plus the peaks and
// cumulative totals since the run started. CPUPercent is percent of one core
// (200 = two cores busy). RSS is summed over the run's processes, so pages
// shared between them count more than once.
type RunUsage struct {
	RunID      string `json:"runId,omitempty"`
	Identifier string `json:"identifier"`
	Live       bool   `json:"live"`
	// Remote marks a run on an SSH worker host: only the local ssh client
	// is visible to this daemon.
	Remote        bool    `json:"remote,omitempty"`
	CPUPercent    float64 `json:"cpuPercent"`
	CPUSeconds    float64 `json:"cpuSeconds"`
	RSSBytes      uint64  `json:"rssBytes"`
	PeakRSSBytes  uint64  `json:"peakRssBytes"`
	Processes     int     `json:"processes"`
	PeakProcesses int     `json:"peakProcesses"`
	// DiskReadBytes / DiskWriteBytes are storage I/O; DiskIOKnown is false
	// where the OS does not report it per process (macOS).
	DiskReadBytes  uint64         `json:"diskReadBytes"`
	DiskWriteBytes uint64         `json:"diskWriteBytes"`
	DiskIOKnown    bool           `json:"diskIoKnown"`
	NetConnections int            `json:"netConnections"`
	TopCommands    []ChildCommand `json:"topCommands,omitempty"`
	Samples        int            `json:"samples"`
	StartedAt      time.Time      `json:"startedAt,omitzero"`
	SampledAt      time.Time      `json:"sampledAt,omitzero"`
	FinishedAt     time.Time      `json:"finishedAt,omitzero"`
}

// RunSummary is what run history keeps of a finished run's resource use.
type RunSummary struct {
	CPUSeconds     float64 `json:"cpuSeconds"`
	PeakCPUPercent float64 `json:"peakCpuPercent"`
	LastRSSBytes   uint64  `json:"lastRssBytes"`
	PeakRSSBytes   uint64  `json:"peakRssBytes"`
	PeakProcesses  int     `json:"peakProcesses"`
	DiskReadBytes  uint64  `json:"diskReadBytes,omitempty"`
	DiskWriteBytes uint64  `json:"diskWriteBytes,omitempty"`
	Commands       int     `json:"commands,omitempty"`
	FilesWritten   int     `json:"filesWritten,omitempty"`
	Connections    int     `json:"connections,omitempty"`
	ActivityMode   string  `json:"activityMode,omitempty"`
	Samples        int     `json:"samples"`
}

// RunsTotal sums the live runs' latest samples.
type RunsTotal struct {
	Runs       int     `json:"runs"`
	CPUPercent float64 `json:"cpuPercent"`
	RSSBytes   uint64  `json:"rssBytes"`
	Processes  int     `json:"processes"`
}

// DiskStatus is the free space of the volume holding the workspaces.
type DiskStatus struct {
	Path       string `json:"path"`
	FreeBytes  uint64 `json:"freeBytes"`
	TotalBytes uint64 `json:"totalBytes"`
}

// Memory pressure levels.
const (
	PressureOK       = "ok"
	PressureModerate = "moderate"
	PressureHigh     = "high"
)

// MachineStatus is the host's load, memory and workspace disk, with the
// configured low-disk thresholds applied.
type MachineStatus struct {
	// Load1/5/15 are the load averages; LoadKnown is false where the OS
	// has none (Windows).
	Load1     float64 `json:"load1"`
	Load5     float64 `json:"load5"`
	Load15    float64 `json:"load15"`
	LoadKnown bool    `json:"loadKnown"`
	CPUCount  int     `json:"cpuCount"`
	// MemTotalBytes / MemAvailableBytes; MemPressure is ok, moderate or
	// high (from Linux PSI when available, else from the available share).
	MemTotalBytes     uint64      `json:"memTotalBytes"`
	MemAvailableBytes uint64      `json:"memAvailableBytes"`
	MemPressure       string      `json:"memPressure"`
	Disk              *DiskStatus `json:"disk,omitempty"`
	// DiskWarnBytes / DiskPauseBytes are the configured thresholds (0 =
	// off); DiskLow / DiskCritical say which one free space is below.
	DiskWarnBytes  uint64    `json:"diskWarnBytes"`
	DiskPauseBytes uint64    `json:"diskPauseBytes"`
	DiskLow        bool      `json:"diskLow"`
	DiskCritical   bool      `json:"diskCritical"`
	SampledAt      time.Time `json:"sampledAt,omitzero"`
}

// SystemStatus is the snapshot's system block: the machine, the live runs'
// totals and the activity mode in effect.
type SystemStatus struct {
	Machine      MachineStatus `json:"machine"`
	Runs         RunsTotal     `json:"runs"`
	ActivityMode string        `json:"activityMode"`
	// ActivityModeReason says why precise mode fell back to sampled.
	ActivityModeReason string `json:"activityModeReason,omitempty"`
	Platform           string `json:"platform"`
}

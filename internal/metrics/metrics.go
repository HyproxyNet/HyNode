package metrics

import (
	"context"
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
)

// Status holds HyBoard-compatible system resource usage.
// JSON layout: {"cpu": number, "mem": {"total", "used"}, "swap": {"total", "used"}, "disk": {"total", "used"}}.
type Status struct {
	CPU  float64     `json:"cpu"`
	Mem  MemoryUsage `json:"mem"`
	Swap MemoryUsage `json:"swap"`
	Disk DiskUsage   `json:"disk"`
}

// MemoryUsage reports total and used bytes.
type MemoryUsage struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

// DiskUsage reports total and used bytes.
type DiskUsage struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

// Metrics holds extended node telemetry merged with state-store data.
type Metrics struct {
	Uptime            int64     `json:"uptime"`
	Goroutines        int       `json:"goroutines"`
	ActiveConnections int64     `json:"active_connections"`
	TotalConnections  int64     `json:"total_connections"`
	TotalUsers        int       `json:"total_users"`
	InboundSpeed      float64   `json:"inbound_speed"`
	OutboundSpeed     float64   `json:"outbound_speed"`
	CPUUsagePerCore   []float64 `json:"cpu_usage_per_core"`
	LoadAvg           LoadAvg   `json:"load_avg"`
	KernelStatus      string    `json:"kernel_status"`
}

// LoadAvg holds 1/5/15 minute load averages.
type LoadAvg struct {
	Avg1  float64 `json:"avg1"`
	Avg5  float64 `json:"avg5"`
	Avg15 float64 `json:"avg15"`
}

// SpeedSnapshot tracks aggregate network IO between Collect calls.
// Create one via NewSpeedSnapshot and reuse it across report intervals.
type SpeedSnapshot struct {
	mu       sync.Mutex
	lastIn   uint64
	lastOut  uint64
	lastTime time.Time
	ready    bool
}

// NewSpeedSnapshot returns a ready-to-use SpeedSnapshot.
func NewSpeedSnapshot() *SpeedSnapshot {
	return &SpeedSnapshot{}
}

// update records new cumulative byte counters and returns the per-second
// inbound and outbound speeds since the previous call.
func (s *SpeedSnapshot) update(inBytes, outBytes uint64) (inSpeed, outSpeed float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	if !s.ready {
		s.lastIn = inBytes
		s.lastOut = outBytes
		s.lastTime = now
		s.ready = true
		return 0, 0
	}

	elapsed := now.Sub(s.lastTime).Seconds()
	if elapsed > 0 {
		inSpeed = float64(inBytes-s.lastIn) / elapsed
		outSpeed = float64(outBytes-s.lastOut) / elapsed
	}

	s.lastIn = inBytes
	s.lastOut = outBytes
	s.lastTime = now
	return inSpeed, outSpeed
}

// Collect gathers system metrics via gopsutil and returns HyBoard-compatible
// Status and Metrics. It is designed to be called once per report interval
// (default 60 s).
//
// Parameters:
//   - ctx        – cancellation context.
//   - speed      – a SpeedSnapshot that persists between calls for bandwidth tracking.
//   - stateMetrics – the map returned by state.Store.Metrics(); keys: uptime,
//     active_connections, total_connections, total_users.
//   - kernelStatus – free-form string describing the proxy kernel runtime state.
func Collect(ctx context.Context, speed *SpeedSnapshot, stateMetrics map[string]any, kernelStatus string) (Status, Metrics) {
	var (
		st  Status
		mt  Metrics
	)

	// --- CPU total (non-blocking, since-boot average on first call) ---
	if pcts, err := cpu.PercentWithContext(ctx, 0, false); err == nil && len(pcts) > 0 {
		st.CPU = pcts[0]
	}

	// --- CPU per core ---
	if pcts, err := cpu.PercentWithContext(ctx, 0, true); err == nil {
		mt.CPUUsagePerCore = pcts
	}

	// --- Memory ---
	if v, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		st.Mem = MemoryUsage{Total: v.Total, Used: v.Used}
	}

	// --- Swap ---
	if v, err := mem.SwapMemoryWithContext(ctx); err == nil {
		st.Swap = MemoryUsage{Total: v.Total, Used: v.Used}
	}

	// --- Disk (root filesystem) ---
	if v, err := disk.UsageWithContext(ctx, "/"); err == nil {
		st.Disk = DiskUsage{Total: v.Total, Used: v.Used}
	}

	// --- Load averages ---
	if avg, err := load.AvgWithContext(ctx); err == nil {
		mt.LoadAvg = LoadAvg{Avg1: avg.Load1, Avg5: avg.Load5, Avg15: avg.Load15}
	}

	// --- Network speed (aggregate across all interfaces) ---
	if counters, err := net.IOCountersWithContext(ctx, false); err == nil && len(counters) > 0 {
		inSpd, outSpd := speed.update(counters[0].BytesRecv, counters[0].BytesSent)
		mt.InboundSpeed = inSpd
		mt.OutboundSpeed = outSpd
	}

	// --- Goroutines ---
	mt.Goroutines = runtime.NumGoroutine()

	// --- Kernel status ---
	mt.KernelStatus = kernelStatus

	// --- Merge state-store metrics ---
	if v, ok := stateMetrics["uptime"]; ok {
		if u, ok := v.(int64); ok {
			mt.Uptime = u
		}
	}
	if v, ok := stateMetrics["active_connections"]; ok {
		if c, ok := v.(int64); ok {
			mt.ActiveConnections = c
		}
	}
	if v, ok := stateMetrics["total_connections"]; ok {
		if c, ok := v.(int64); ok {
			mt.TotalConnections = c
		}
	}
	if v, ok := stateMetrics["total_users"]; ok {
		if u, ok := v.(int); ok {
			mt.TotalUsers = u
		}
	}

	return st, mt
}

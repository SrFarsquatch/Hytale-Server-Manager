package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type ResourceMetrics struct {
	CPUPercent float64 `json:"cpuPercent"`
	CPUAvailable bool `json:"cpuAvailable"`
	ContainerMemoryBytes int64 `json:"containerMemoryBytes"`
	ContainerMemoryLimitBytes int64 `json:"containerMemoryLimitBytes"`
	MemoryAvailable bool `json:"memoryAvailable"`
	DiskUsedBytes int64 `json:"diskUsedBytes"`
	DiskTotalBytes int64 `json:"diskTotalBytes"`
	DiskFreeBytes int64 `json:"diskFreeBytes"`
	DiskAvailable bool `json:"diskAvailable"`
	SampledAt string `json:"sampledAt"`
}

func (a *App) resourceMetrics() ResourceMetrics {
	m := ResourceMetrics{SampledAt: time.Now().UTC().Format(time.RFC3339)}
	if usage, ok := readContainerCPUUsageNS(); ok {
		now := time.Now()
		a.resourceMu.Lock()
		if !a.lastCPUSampleAt.IsZero() && now.After(a.lastCPUSampleAt) && usage >= a.lastCPUUsageNS {
			elapsed := uint64(now.Sub(a.lastCPUSampleAt).Nanoseconds())
			if elapsed > 0 {
				m.CPUPercent = cpuPercent(usage-a.lastCPUUsageNS, elapsed, readContainerCPUCapacity())
				m.CPUAvailable = true
				a.lastCPUPercent, a.hasCPUPercent = m.CPUPercent, true
			}
		} else if a.hasCPUPercent { m.CPUPercent, m.CPUAvailable = a.lastCPUPercent, true }
		a.lastCPUUsageNS, a.lastCPUSampleAt = usage, now
		a.resourceMu.Unlock()
	}
	if used, limit, ok := readContainerMemoryWithLimit(); ok { m.ContainerMemoryBytes, m.ContainerMemoryLimitBytes, m.MemoryAvailable = used, limit, true }
	if used, total, free, err := diskUsage(a.dataDir); err == nil { m.DiskUsedBytes, m.DiskTotalBytes, m.DiskFreeBytes, m.DiskAvailable = used, total, free, true }
	return m
}

func readContainerCPUUsageNS() (uint64, bool) {
	if b, err := os.ReadFile("/sys/fs/cgroup/cpu.stat"); err == nil { if n, ok := parseCPUStatUsage(string(b)); ok { return n, true } }
	for _, p := range []string{"/sys/fs/cgroup/cpuacct/cpuacct.usage", "/sys/fs/cgroup/cpu,cpuacct/cpuacct.usage"} {
		if b, err := os.ReadFile(p); err == nil { if n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64); err == nil { return n, true } }
	}
	return 0, false
}

func parseCPUStatUsage(content string) (uint64, bool) {
	for _, line := range strings.Split(content, "\n") {
		f := strings.Fields(line); if len(f) == 2 && f[0] == "usage_usec" { n, err := strconv.ParseUint(f[1], 10, 64); if err == nil { return n * 1000, true } }
	}
	return 0, false
}

func readContainerCPUCapacity() float64 {
	if b, err := os.ReadFile("/sys/fs/cgroup/cpu.max"); err == nil { if n, ok := parseCPUQuota(string(b)); ok { return n } }
	for _, prefix := range []string{"/sys/fs/cgroup/cpu/", "/sys/fs/cgroup/cpu,cpuacct/"} {
		qb, qe := os.ReadFile(filepath.Join(prefix, "cpu.cfs_quota_us")); pb, pe := os.ReadFile(filepath.Join(prefix, "cpu.cfs_period_us"))
		if qe == nil && pe == nil { q, e1 := strconv.ParseInt(strings.TrimSpace(string(qb)), 10, 64); p, e2 := strconv.ParseInt(strings.TrimSpace(string(pb)), 10, 64); if e1 == nil && e2 == nil { if q <= 0 { return float64(maxInt(runtime.NumCPU(), 1)) }; if p > 0 { return float64(q)/float64(p) } } }
	}
	return float64(maxInt(runtime.NumCPU(), 1))
}

func parseCPUQuota(content string) (float64, bool) {
	f := strings.Fields(content); if len(f) != 2 { return 0, false }
	p, err := strconv.ParseFloat(f[1], 64); if err != nil || p <= 0 { return 0, false }
	if f[0] == "max" { return float64(maxInt(runtime.NumCPU(), 1)), true }
	q, err := strconv.ParseFloat(f[0], 64); if err != nil || q <= 0 { return 0, false }; return q/p, true
}

func cpuPercent(usageDeltaNS, elapsedNS uint64, capacityCores float64) float64 {
	if elapsedNS == 0 || capacityCores <= 0 { return 0 }
	p := float64(usageDeltaNS)/float64(elapsedNS)/capacityCores*100; if p < 0 { return 0 }; if p > 100 { return 100 }; return p
}

func readContainerMemoryWithLimit() (int64, int64, bool) {
	for _, p := range []string{"/sys/fs/cgroup/memory.current", "/sys/fs/cgroup/memory/memory.usage_in_bytes"} {
		if b, err := os.ReadFile(p); err == nil { if n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil && n >= 0 { return n, readEffectiveMemoryLimit(), true } }
	}
	return 0, 0, false
}

func diskUsage(path string) (used, total, free int64, err error) {
	if strings.TrimSpace(path) == "" { return 0, 0, 0, errors.New("disk path is empty") }
	var s syscall.Statfs_t; if err := syscall.Statfs(filepath.Clean(path), &s); err != nil { return 0, 0, 0, err }
	b := int64(s.Bsize); total = int64(s.Blocks)*b; used = (int64(s.Blocks)-int64(s.Bfree))*b; free = int64(s.Bavail)*b
	if used < 0 { used = 0 }; if total < 0 { total = 0 }; if free < 0 { free = 0 }; return used, total, free, nil
}

func maxInt(a, b int) int { if a > b { return a }; return b }
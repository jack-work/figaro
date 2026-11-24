package toolcgroup

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Limits arrange the tool cgroups under one parent ([tools.cgroup] in the
// daemon's config; the numbers come from there, never from code). The zero
// value keeps the flat layout: tools-<aria> beside the daemon, no limits.
type Limits struct {
	// ParentCPUMax is the parent's cpu.max ("700000 100000" = 7 CPUs, or "max").
	ParentCPUMax string
	// ParentMemoryHigh is the parent's memory.high ("24G", or "max"); written only
	// when the memory controller is delegated to the daemon's unit.
	ParentMemoryHigh string
	// ToolCPUMax and ToolCPUWeight go on every <parent>/<aria>.
	ToolCPUMax    string
	ToolCPUWeight int
	// DaemonCPUWeight goes on <unit>/daemon, the daemon's own leaf.
	DaemonCPUWeight int
}

// Parented reports whether the tools run under one parent cgroup.
func (l Limits) Parented() bool {
	return l.ParentCPUMax != "" || l.ParentMemoryHigh != "" || l.ToolCPUMax != "" || l.ToolCPUWeight != 0 || l.DaemonCPUWeight != 0
}

var (
	cpuMaxRe     = regexp.MustCompile(`^(max|[1-9][0-9]*)( [1-9][0-9]*)?$`)
	memoryHighRe = regexp.MustCompile(`^(max|[1-9][0-9]*[KMGT]?)$`)
)

// cpuMaxValid accepts what the kernel accepts for cpu.max: "max" or a quota of
// at least 1000 µs, optionally with a period of 1000 to 1000000 µs.
func cpuMaxValid(v string) bool {
	m := cpuMaxRe.FindStringSubmatch(v)
	if m == nil {
		return false
	}
	if m[1] != "max" {
		if q, err := strconv.Atoi(m[1]); err != nil || q < 1000 {
			return false
		}
	}
	if m[2] != "" {
		if p, err := strconv.Atoi(strings.TrimSpace(m[2])); err != nil || p < 1000 || p > 1000000 {
			return false
		}
	}
	return true
}

// Check refuses a value the kernel would refuse, so a bad config stops the
// daemon at start instead of leaving the tools unlimited.
func (l Limits) Check() error {
	for _, v := range []struct{ key, val string }{{"parent_cpu_max", l.ParentCPUMax}, {"tool_cpu_max", l.ToolCPUMax}} {
		if v.val != "" && !cpuMaxValid(v.val) {
			return fmt.Errorf("[tools.cgroup] %s = %q: want \"max\" or \"<quota> <period>\" in microseconds, quota >= 1000, period 1000 to 1000000", v.key, v.val)
		}
	}
	if l.ParentMemoryHigh != "" && !memoryHighRe.MatchString(l.ParentMemoryHigh) {
		return fmt.Errorf("[tools.cgroup] parent_memory_high = %q: want \"max\" or bytes with an optional K, M, G or T", l.ParentMemoryHigh)
	}
	for _, v := range []struct {
		key string
		val int
	}{{"tool_cpu_weight", l.ToolCPUWeight}, {"daemon_cpu_weight", l.DaemonCPUWeight}} {
		if v.val != 0 && (v.val < 1 || v.val > 10000) {
			return fmt.Errorf("[tools.cgroup] %s = %s: want 1 to 10000", v.key, strconv.Itoa(v.val))
		}
	}
	return nil
}

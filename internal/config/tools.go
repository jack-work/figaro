package config

// ToolsConfig is the [tools] section: how the daemon houses its seats' tool shells.
type ToolsConfig struct {
	Cgroup ToolCgroupConfig `toml:"cgroup"`
}

// ToolCgroupConfig is [tools.cgroup]. With any key set (and [authz] peer_check on),
// the daemon moves itself to <unit>/daemon and makes every tool cgroup
// <unit>/tools/<aria> under one parent that carries these limits; with none set
// the tools stay flat (<unit>/tools-<aria>) and unlimited. Needs the unit's
// cgroup delegated (Delegate=cpu, plus memory for parent_memory_high).
type ToolCgroupConfig struct {
	// ParentCPUMax is the tools parent's cpu.max: "<quota> <period>" in µs ("700000 100000" = 7 CPUs) or "max".
	ParentCPUMax string `toml:"parent_cpu_max"`
	// ParentMemoryHigh is the tools parent's memory.high ("24G") or "max".
	ParentMemoryHigh string `toml:"parent_memory_high"`
	// ToolCPUMax is every tool cgroup's cpu.max ("150000 100000" = 1.5 CPUs).
	ToolCPUMax string `toml:"tool_cpu_max"`
	// ToolCPUWeight is every tool cgroup's cpu.weight (1-10000).
	ToolCPUWeight int `toml:"tool_cpu_weight"`
	// DaemonCPUWeight is <unit>/daemon's cpu.weight (1-10000).
	DaemonCPUWeight int `toml:"daemon_cpu_weight"`
}

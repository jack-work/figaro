package config

type ToolsConfig struct {
	Cgroup ToolCgroupConfig `toml:"cgroup"`
}

type ToolCgroupConfig struct {
	ParentCPUMax     string `toml:"parent_cpu_max"`
	ParentMemoryHigh string `toml:"parent_memory_high"`
	ToolCPUMax       string `toml:"tool_cpu_max"`
	ToolCPUWeight    int    `toml:"tool_cpu_weight"`
	DaemonCPUWeight  int    `toml:"daemon_cpu_weight"`
}

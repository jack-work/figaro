package otel

import "testing"

// The env outranks the config, so a pool daemon has to ask this and not the
// config alone before it starts.
func TestLogsAtDebugFollowsTheEnvThenTheConfig(t *testing.T) {
	t.Cleanup(func() { SetConfiguredLevel("") })
	for _, c := range []struct {
		env, config string
		want        bool
	}{
		{"", "", false},
		{"", "debug", true},
		{"DEBUG", "info", true},
		{"info", "debug", false},
		{"warn", "", false},
	} {
		t.Setenv("FIGARO_LOG_LEVEL", c.env)
		SetConfiguredLevel(c.config)
		if got := LogsAtDebug(); got != c.want {
			t.Errorf("env %q, config %q: debug %v", c.env, c.config, got)
		}
	}
}

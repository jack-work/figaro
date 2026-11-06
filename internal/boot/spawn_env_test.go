package boot

import (
	"strings"
	"testing"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// The daemon a bare figaro call auto-spawns is the live one when the runtime
// dir is the default. A FIGARO_CONFIG_DIR or FIGARO_STATE_DIR left exported in
// a shell must not become its config or state (it did on 11-05: the live
// daemon ran with a test's config dir). A dev shell names its own runtime dir
// and is untouched; an explicit allow lets it through.
func TestALiveDaemonIsNotSpawnedWithAnotherConfigOrState(t *testing.T) {
	base := map[string]string{"HOME": "/home/g", "XDG_RUNTIME_DIR": "/run/user/1000"}
	with := func(kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	for name, c := range map[string]struct {
		env  map[string]string
		want string
	}{
		"nothing set":                         {with(), ""},
		"config dir set to the default":       {with("FIGARO_CONFIG_DIR", "/home/g/.config/figaro/"), ""},
		"config dir under XDG_CONFIG_HOME":    {with("XDG_CONFIG_HOME", "/x", "FIGARO_CONFIG_DIR", "/x/figaro"), ""},
		"a test's config dir":                 {with("FIGARO_CONFIG_DIR", "/var/tmp/figaro-dev-config-notroupe"), "FIGARO_CONFIG_DIR=/var/tmp/figaro-dev-config-notroupe"},
		"a test's state dir":                  {with("FIGARO_STATE_DIR", "/var/tmp/s"), "FIGARO_STATE_DIR=/var/tmp/s"},
		"runtime dir named, but the default":  {with("FIGARO_RUNTIME_DIR", "/run/user/1000/figaro", "FIGARO_CONFIG_DIR", "/tmp/c"), "FIGARO_CONFIG_DIR=/tmp/c"},
		"a dev shell's own runtime dir":       {with("FIGARO_RUNTIME_DIR", "/var/tmp/figaro-dev-x/run", "FIGARO_CONFIG_DIR", "/var/tmp/figaro-dev-x/config"), ""},
		"allowed":                             {with("FIGARO_CONFIG_DIR", "/tmp/c", AllowConfigDirEnv, "1"), ""},
		"allow that is not truthy":            {with("FIGARO_CONFIG_DIR", "/tmp/c", AllowConfigDirEnv, "0"), "FIGARO_CONFIG_DIR=/tmp/c"},
		"no XDG_RUNTIME_DIR, default tmp dir": {map[string]string{"HOME": "/home/g", "FIGARO_CONFIG_DIR": "/tmp/c"}, "FIGARO_CONFIG_DIR=/tmp/c"},
	} {
		err := SpawnRefusal(envOf(c.env))
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: refused: %v", name, err)
		case c.want != "" && err == nil:
			t.Errorf("%s: spawned", name)
		case c.want != "" && !strings.Contains(err.Error(), c.want):
			t.Errorf("%s: %v does not name %s", name, err, c.want)
		case c.want != "" && !strings.Contains(err.Error(), AllowConfigDirEnv):
			t.Errorf("%s: %v does not say how to override", name, err)
		}
	}
}

// A daemon told to read a config dir that is not there would run on bare
// defaults and look healthy; it refuses instead. The default dir may be
// absent (a first run creates it).
func TestADaemonRefusesAConfigDirThatIsNotThere(t *testing.T) {
	dir := t.TempDir()
	if err := MissingConfigDir(envOf(map[string]string{})); err != nil {
		t.Fatalf("unset: %v", err)
	}
	if err := MissingConfigDir(envOf(map[string]string{"FIGARO_CONFIG_DIR": dir})); err != nil {
		t.Fatalf("present: %v", err)
	}
	err := MissingConfigDir(envOf(map[string]string{"FIGARO_CONFIG_DIR": dir + "/gone"}))
	if err == nil || !strings.Contains(err.Error(), dir+"/gone") {
		t.Fatalf("missing: %v", err)
	}
}

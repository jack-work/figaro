package daemon

import (
	"errors"
	"testing"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

const thin = "/usr/bin:/bin"
const managerPath = "/home/u/.local/state/hush/bin:/home/u/go/bin:/usr/bin"

func managerSays(path string, err error, called *bool) userManager {
	return func(string) (string, error) {
		*called = true
		return path, err
	}
}

func TestDaemonPathOrder(t *testing.T) {
	live := map[string]string{"PATH": thin, "XDG_RUNTIME_DIR": "/run/user/1000"}
	cases := []struct {
		name       string
		env        map[string]string
		configured []string
		mgrPath    string
		mgrErr     error
		want       string
		source     pathSource
		askManager bool
		wantErr    bool
	}{
		{"config wins over the manager", live, []string{"/opt/a", "/usr/bin"}, managerPath, nil, "/opt/a:/usr/bin", pathFromConfig, false, false},
		{"the manager's PATH replaces the spawner's whole", live, nil, managerPath, nil, managerPath, pathFromManager, true, false},
		{"FIGARO_RUNTIME_DIR at the default is still the user's daemon", map[string]string{"PATH": thin, "XDG_RUNTIME_DIR": "/run/user/1000", "FIGARO_RUNTIME_DIR": "/run/user/1000/figaro/"}, nil, managerPath, nil, managerPath, pathFromManager, true, false},
		{"no XDG_RUNTIME_DIR keeps its own", map[string]string{"PATH": thin}, nil, managerPath, nil, thin, pathFromInherited, false, false},
		{"a dev root's runtime dir keeps its own", map[string]string{"PATH": thin, "XDG_RUNTIME_DIR": "/run/user/1000", "FIGARO_DEV_ROOT": "/var/tmp/d", "FIGARO_RUNTIME_DIR": "/var/tmp/d/runtime"}, nil, managerPath, nil, thin, pathFromInherited, false, false},
		{"a system service's runtime dir keeps its own", map[string]string{"PATH": thin, "XDG_RUNTIME_DIR": "/run/figaro", "FIGARO_RUNTIME_DIR": "/run/figaro"}, nil, managerPath, nil, thin, pathFromInherited, false, false},
		{"no user manager keeps its own, quietly", live, nil, "", errNoUserManager, thin, pathFromInherited, true, false},
		{"an unreadable manager keeps its own and says so", live, nil, "", errors.New("auth"), thin, pathFromInherited, true, true},
		{"a manager without PATH keeps its own", live, nil, "", nil, thin, pathFromInherited, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var asked bool
			got, source, err := daemonPath(envOf(c.env), c.configured, managerSays(c.mgrPath, c.mgrErr, &asked))
			if got != c.want || source != c.source {
				t.Fatalf("got %q from %s, want %q from %s", got, source, c.want, c.source)
			}
			if asked != c.askManager {
				t.Fatalf("asked the manager = %v, want %v", asked, c.askManager)
			}
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, want error %v", err, c.wantErr)
			}
		})
	}
}

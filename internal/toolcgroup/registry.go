package toolcgroup

import (
	"fmt"
	"path"
	"strings"
	"sync"
)

type Registry struct {
	mu   sync.RWMutex
	m    map[string]string
	unit string
	pid  int
}

func (r *Registry) Guard(unit string, pid int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.unit, r.pid = path.Clean(unit), pid
}

func (r *Registry) Suspect(cgroup string, pid int) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cgroup = path.Clean(cgroup)
	if r.unit != "" && (cgroup == r.unit || strings.HasPrefix(cgroup, r.unit+"/")) && pid != r.pid {
		return "it runs in the daemon's unit " + r.unit + " (" + cgroup + ") in no figaro's cgroup and is not the daemon"
	}
	return ""
}

func NewRegistry() *Registry { return &Registry{m: map[string]string{}} }

func (r *Registry) Register(cgroup, figaro string) (func(), error) {
	cgroup = path.Clean(cgroup)
	if !strings.HasPrefix(cgroup, "/") || cgroup == "/" {
		return nil, fmt.Errorf("toolcgroup: %q is not a cgroup below the root", cgroup)
	}
	if figaro == "" {
		return nil, fmt.Errorf("toolcgroup: %s names no figaro", cgroup)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if held, ok := r.m[cgroup]; ok && held != figaro {
		return nil, fmt.Errorf("toolcgroup: %s already holds the tools of figaro %s", cgroup, held)
	}
	r.m[cgroup] = figaro
	return func() {
		r.mu.Lock()
		if r.m[cgroup] == figaro {
			delete(r.m, cgroup)
		}
		r.mu.Unlock()
	}, nil
}

func (r *Registry) Lookup(cgroup string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.m) == 0 {
		return "", false
	}
	for p := cgroup; len(p) > 1; {
		if f, ok := r.m[p]; ok {
			return f, true
		}
		i := strings.LastIndexByte(p, '/')
		if i <= 0 {
			break
		}
		p = p[:i]
	}
	return "", false
}

func ParseProcCgroup(raw []byte) (string, bool) {
	for len(raw) > 0 {
		line := raw
		if i := indexByte(raw, '\n'); i >= 0 {
			line, raw = raw[:i], raw[i+1:]
		} else {
			raw = nil
		}
		if len(line) > 3 && line[0] == '0' && line[1] == ':' && line[2] == ':' {
			return string(line[3:]), true
		}
	}
	return "", false
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

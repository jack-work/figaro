package activation

import (
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
)

const firstFD = 3

const (
	envPID     = "LISTEN_PID"
	envFDs     = "LISTEN_FDS"
	envNames   = "LISTEN_FDNAMES"
	envPidfdID = "LISTEN_PIDFDID"
)

type Sockets struct {
	files map[string]*os.File
}

func (s *Sockets) Active() bool { return s != nil && s.files != nil }

func (s *Sockets) Names() []string {
	if s == nil {
		return nil
	}
	names := make([]string, 0, len(s.files))
	for name := range s.files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (s *Sockets) Has(name string) bool {
	if s == nil {
		return false
	}
	_, ok := s.files[name]
	return ok
}

func (s *Sockets) Close() error {
	if s == nil {
		return nil
	}
	var err error
	for name, f := range s.files {
		delete(s.files, name)
		err = errors.Join(err, f.Close())
	}
	return err
}

func (s *Sockets) Listener(name string) (net.Listener, bool, error) {
	if s == nil {
		return nil, false, nil
	}
	f, ok := s.files[name]
	if !ok {
		return nil, false, nil
	}
	delete(s.files, name)
	defer f.Close()
	ln, err := net.FileListener(f)
	if err != nil {
		return nil, true, fmt.Errorf("activation socket %q: %w", name, err)
	}
	if ul, ok := ln.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	return ln, true, nil
}

func Take(getenv func(string) string, unsetenv func(string) error, pid int) (*Sockets, error) {
	raw := getenv(envFDs)
	owner := getenv(envPID)
	names := getenv(envNames)
	for _, k := range []string{envPID, envFDs, envNames, envPidfdID} {
		_ = unsetenv(k)
	}
	if raw == "" || owner == "" {
		return &Sockets{}, nil
	}
	target, err := strconv.Atoi(owner)
	if err != nil {
		return &Sockets{}, fmt.Errorf("%s=%q is not a pid", envPID, owner)
	}
	if target != pid {
		return &Sockets{}, nil
	}
	count, err := strconv.Atoi(raw)
	if err != nil || count < 0 {
		return &Sockets{}, fmt.Errorf("%s=%q is not a count", envFDs, raw)
	}
	for i := 0; i < count; i++ {
		closeOnExec(firstFD + i)
	}
	var labels []string
	if names != "" {
		labels = strings.Split(names, ":")
	}
	s := &Sockets{files: make(map[string]*os.File, count)}
	for i := 0; i < count; i++ {
		fd := firstFD + i
		name := "unknown"
		if i < len(labels) && labels[i] != "" {
			name = labels[i]
		}
		f := os.NewFile(uintptr(fd), name)
		if _, dup := s.files[name]; dup {
			_ = f.Close()
			_ = s.Close()
			for j := i + 1; j < count; j++ {
				if rest := os.NewFile(uintptr(firstFD+j), ""); rest != nil {
					_ = rest.Close()
				}
			}
			return nil, fmt.Errorf("systemd passed two sockets named %q; give each its own FileDescriptorName", name)
		}
		s.files[name] = f
	}
	return s, nil
}

func TakeEnv() (*Sockets, error) {
	return Take(os.Getenv, os.Unsetenv, os.Getpid())
}

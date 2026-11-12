package sandbox

import (
	"context"
	"fmt"
	"io/fs"

	api "github.com/jack-work/figaro/api/sandbox"
)

type socketless struct{ box string }

func (s socketless) refuse(what string) error {
	return fmt.Errorf("box %s has no exec socket: %s is refused, since nothing executes in a leased box", s.box, what)
}

func (s socketless) Exec(context.Context, api.ExecRequest) (api.Process, error) {
	return nil, s.refuse("exec")
}

func (s socketless) Sessions() api.Sessions { return s }

func (s socketless) FS() api.FS { return s }

func (s socketless) Isolation() api.Isolation { return api.Isolation{Provider: "box"} }

func (s socketless) Close() error { return nil }

func (s socketless) Start(api.ExecRequest) (api.Session, error) { return nil, s.refuse("a session") }

func (s socketless) Get(string) (api.Session, error) { return nil, s.refuse("a session") }

func (s socketless) List() []api.SessionInfo { return nil }

func (s socketless) Remove(string) error { return s.refuse("a session") }

func (s socketless) ReadFile(string) ([]byte, error) { return nil, s.refuse("a read") }

func (s socketless) WriteFile(string, []byte, fs.FileMode) error { return s.refuse("a write") }

func (s socketless) Stat(string) (fs.FileInfo, error) { return nil, s.refuse("a stat") }

func (s socketless) MkdirAll(string, fs.FileMode) error { return s.refuse("a mkdir") }

func (s socketless) Rename(string, string) error { return s.refuse("a rename") }

func (s socketless) Update(string, func([]byte) ([]byte, error)) error { return s.refuse("a write") }

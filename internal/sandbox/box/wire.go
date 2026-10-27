package box

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"time"

	"github.com/jack-work/figaro/api/sandbox"
	"github.com/jack-work/jkrpc"
)

const (
	mOpen    = "box.open"
	mExec    = "box.exec"
	mSignal  = "box.signal"
	mStdin   = "box.stdin"
	mStart   = "box.session.start"
	mGet     = "box.session.get"
	mList    = "box.session.list"
	mRemove  = "box.session.remove"
	mPoll    = "box.session.poll"
	mLog     = "box.session.log"
	mInfo    = "box.session.info"
	mRead    = "box.fs.read"
	mWrite   = "box.fs.write"
	mStat    = "box.fs.stat"
	mMkdir   = "box.fs.mkdirall"
	mRename  = "box.fs.rename"
	mSwap    = "box.fs.swap"
	nChunk   = "box.chunk"
	nExit    = "box.exit"
	errCode  = -32050
	sigKill  = "kill"
	sigInt   = "interrupt"
	sigClose = "cancel"
)

type openReq struct {
	Scope     string            `json:"scope"`
	Workspace string            `json:"workspace,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
}

type openResp struct {
	Workspace string            `json:"workspace"`
	Isolation sandbox.Isolation `json:"isolation"`
}

type execReq struct {
	Handle    string            `json:"handle"`
	Command   string            `json:"command"`
	Cwd       string            `json:"cwd,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	TimeoutMS int64             `json:"timeout_ms,omitempty"`
	PTY       bool              `json:"pty,omitempty"`
}

func (r execReq) request() sandbox.ExecRequest {
	return sandbox.ExecRequest{Command: r.Command, Cwd: r.Cwd, Env: r.Env, Timeout: time.Duration(r.TimeoutMS) * time.Millisecond, PTY: r.PTY}
}

func wireExec(handle string, req sandbox.ExecRequest) execReq {
	return execReq{Handle: handle, Command: req.Command, Cwd: req.Cwd, Env: req.Env, TimeoutMS: req.Timeout.Milliseconds(), PTY: req.PTY}
}

type procResp struct {
	Session string              `json:"session,omitempty"`
	PTY     bool                `json:"pty,omitempty"`
	Info    sandbox.SessionInfo `json:"info"`
}

type procReq struct {
	Handle  string `json:"handle,omitempty"`
	Session string `json:"session,omitempty"`
	Signal  string `json:"signal,omitempty"`
	Data    []byte `json:"data,omitempty"`
}

type chunkNote struct {
	Handle string `json:"handle"`
	Data   []byte `json:"data"`
}

type exitNote struct {
	Handle string         `json:"handle"`
	Result sandbox.Result `json:"result"`
}

type dataResp struct {
	Data []byte `json:"data"`
}

type pathReq struct {
	Path string      `json:"path"`
	To   string      `json:"to,omitempty"`
	Data []byte      `json:"data,omitempty"`
	Perm fs.FileMode `json:"perm,omitempty"`
	Old  []byte      `json:"old,omitempty"`
}

type statResp struct {
	Name    string      `json:"name"`
	Size    int64       `json:"size"`
	Mode    fs.FileMode `json:"mode"`
	ModTime time.Time   `json:"mod_time"`
}

func wireStat(fi fs.FileInfo) statResp {
	return statResp{Name: fi.Name(), Size: fi.Size(), Mode: fi.Mode(), ModTime: fi.ModTime()}
}

type fileInfo struct{ s statResp }

func (f fileInfo) Name() string       { return f.s.Name }
func (f fileInfo) Size() int64        { return f.s.Size }
func (f fileInfo) Mode() fs.FileMode  { return f.s.Mode }
func (f fileInfo) ModTime() time.Time { return f.s.ModTime }
func (f fileInfo) IsDir() bool        { return f.s.Mode.IsDir() }
func (f fileInfo) Sys() any           { return nil }

var errConflict = errors.New("box: file changed during update")

var kinds = []struct {
	name string
	err  error
}{
	{"not_exist", fs.ErrNotExist},
	{"exist", fs.ErrExist},
	{"permission", fs.ErrPermission},
	{"outside_workspace", sandbox.ErrOutsideWorkspace},
	{"no_session", sandbox.ErrNoSession},
	{"conflict", errConflict},
}

func toWire(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	for _, k := range kinds {
		if errors.Is(err, k.err) {
			data, _ := json.Marshal(k.name)
			return &jkrpc.Error{Code: errCode, Message: msg, Data: data}
		}
	}
	return &jkrpc.Error{Code: errCode, Message: msg}
}

func fromWire(err error) error {
	var je *jkrpc.Error
	if !errors.As(err, &je) || je.Code != errCode {
		return err
	}
	var name string
	_ = json.Unmarshal(je.Data, &name)
	for _, k := range kinds {
		if k.name == name {
			if je.Message == k.err.Error() {
				return k.err
			}
			return wireErr{msg: je.Message, kind: k.err}
		}
	}
	return errors.New(je.Message)
}

type wireErr struct {
	msg  string
	kind error
}

func (e wireErr) Error() string { return e.msg }
func (e wireErr) Unwrap() error { return e.kind }

func pathErr(op, path string, err error) error {
	if err == nil {
		return nil
	}
	for _, k := range kinds {
		if errors.Is(err, k.err) {
			return &os.PathError{Op: op, Path: path, Err: k.err}
		}
	}
	return err
}

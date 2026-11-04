package formsim_test

import (
	"encoding/json"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkrpc"
)

// The server the simulator runs is the real single writer: store.Form over a
// MemFormLog that outlives a crash. This file is the only one that knows the
// store's surface, so a change there lands here and nowhere else.

type commit = store.Commit

type server struct {
	log  *store.MemFormLog
	form *store.Form

	onCommit func(commit)
}

func newServer(onCommit func(commit)) *server {
	s := &server{log: &store.MemFormLog{}, onCommit: onCommit}
	s.open()
	return s
}

func (s *server) open() {
	f, err := store.OpenForm(s.log)
	if err != nil {
		panic(err)
	}
	s.form = f
	f.OnCommit(s.onCommit)
}

func (s *server) crash() {
	s.form.Close()
	s.open()
}

func (s *server) close() { s.form.Close() }

func (s *server) state() (form.Snapshot, uint64) { return s.form.Snapshot() }

// set answers the way the hub's figaro.set handler does: an applied write is
// broadcast before the answer, a refusal is a typed error.
func (s *server) set(req rpc.SetRequest) (*rpc.SetResponse, error) {
	intent := store.Ensure
	if req.Assert {
		intent = store.Assert
	}
	v, err := s.form.ApplyWrite(store.FormWrite{Patch: req.Patch, IfVersion: req.IfVersion, Intent: intent,
		Origin: store.Origin{Client: req.Client, Seq: req.Seq}})
	if err != nil {
		return nil, &jkrpc.Error{Code: -32000, Message: err.Error(), Data: seqData(req.Seq)}
	}
	switch {
	case v.Duplicate:
		return &rpc.SetResponse{OK: true, Outcome: rpc.OutcomeDuplicate, Version: v.Version}, nil
	case v.Applied.IsIdentity():
		return &rpc.SetResponse{OK: true, Outcome: rpc.OutcomeUnchanged, Version: v.Version}, nil
	}
	return &rpc.SetResponse{OK: true, Outcome: rpc.OutcomeApplied, Version: v.Version}, nil
}

func (s *server) load(client string) *rpc.FormResponse {
	snap, version := s.form.Snapshot()
	return &rpc.FormResponse{Snapshot: snap, Version: version, LastSeq: s.form.LastSeq("", client), Origin: store.Origin{Client: client}.Tag()}
}

func seqData(seq uint64) json.RawMessage {
	b, _ := json.Marshal(map[string]uint64{"seq": seq})
	return b
}

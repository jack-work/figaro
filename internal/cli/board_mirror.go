package cli

import (
	"context"

	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/api/rpc"
)

func (m *formMirror) catchUp(resp *rpc.FormResponse) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !resp.Delta {
		m.snap, m.version = resp.Snapshot, resp.Version
		return
	}
	for _, p := range resp.Patches {
		if p.Version <= m.version {
			continue
		}
		m.snap = m.snap.Apply(p.Patch)
		m.version = p.Version
	}
	if resp.Version > m.version {
		m.version = resp.Version
	}
}

func (in *interactiveInput) subjectBoard() *formMirror {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.board
}

func (in *interactiveInput) seedBoard(gen uint64) {
	cli := in.aria()
	board := in.subjectBoard()
	if cli == nil || board == nil {
		return
	}
	go func() {
		_, version, _ := board.state()
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		resp, err := cli.FormSince(ctx, version)
		if err != nil || !in.current(gen) {
			return
		}
		board.catchUp(resp)
		in.recomputeLimit()
	}()
}

func (in *interactiveInput) applyBoardDelta(gen uint64, d rpc.FormDelta) {
	board := in.subjectBoard()
	if board == nil || !in.current(gen) {
		return
	}
	switch board.apply(d) {
	case formResync:
		in.seedBoard(gen)
		return
	case formIncompatible:
		return
	}
	in.recomputeLimit()
}

func (in *interactiveInput) recomputeLimit() {
	in.models.ensure()
	in.mu.Lock()
	board := in.board
	if board == nil {
		in.mu.Unlock()
		return
	}
	snap, _, _ := board.state()
	limit := models.ContextLimit(in.models.snapshot(), snap)
	if in.lt.status.setLimit(limit) {
		in.lt.render()
	}
	in.mu.Unlock()

	if limit > 0 {
		return
	}
	if _, pinned := models.Override(snap); pinned {
		return
	}
	if provider, model := models.Subject(snap); provider != "" && model != "" {
		in.models.want(provider, model)
	}
}

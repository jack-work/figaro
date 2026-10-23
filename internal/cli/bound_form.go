package cli

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jack-work/figaro/api/form"
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

func (in *interactiveInput) subjectBoundForm() *formMirror {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.bound
}

func (in *interactiveInput) seedBoundForm(gen uint64) {
	cli := in.aria()
	bound := in.subjectBoundForm()
	if cli == nil || bound == nil {
		return
	}
	go func() {
		_, version, _ := bound.state()
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		resp, err := cli.FormSince(ctx, version)
		if err != nil || !in.current(gen) {
			return
		}
		bound.catchUp(resp)
		in.boundFormMoved()
	}()
}

func (in *interactiveInput) applyBoundFormDelta(gen uint64, d rpc.FormDelta) {
	bound := in.subjectBoundForm()
	if bound == nil || !in.current(gen) {
		return
	}
	switch bound.apply(d) {
	case formResync:
		in.seedBoundForm(gen)
		return
	case formIncompatible:
		return
	}
	in.boundFormMoved()
}

func (in *interactiveInput) boundFormMoved() {
	in.syncComments()
	in.recomputeLimit()
}

func (in *interactiveInput) syncComments() {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.bound == nil {
		return
	}
	snap, _, _ := in.bound.state()
	if in.lt.tr.setComments(readComments(snap)) {
		in.lt.render()
	}
}

func (in *interactiveInput) recomputeLimit() {
	in.models.ensure()
	in.mu.Lock()
	bound := in.bound
	if bound == nil {
		in.mu.Unlock()
		return
	}
	snap, _, _ := bound.state()
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

func (in *interactiveInput) writeBoundForm(ctx context.Context, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return in.patchBoundForm(ctx, func(base form.Snapshot) form.Patch {
		return form.Build(base, map[string]json.RawMessage{key: raw}, nil)
	})
}

func (in *interactiveInput) removeBoundForm(ctx context.Context, key string) error {
	return in.patchBoundForm(ctx, func(base form.Snapshot) form.Patch {
		return form.Build(base, nil, []string{key})
	})
}

func (in *interactiveInput) patchBoundForm(ctx context.Context, build func(form.Snapshot) form.Patch) error {
	cli := in.aria()
	if cli == nil {
		return errors.New("no aria on screen")
	}
	var base form.Snapshot
	if bound := in.subjectBoundForm(); bound != nil {
		base, _, _ = bound.state()
	}
	_, err := cli.Set(ctx, build(base), 0)
	return err
}

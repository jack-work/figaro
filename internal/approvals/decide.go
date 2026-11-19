package approvals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"sync"
	"time"
)

var ErrStale = errors.New("the form changed since it was read")

type Decision struct {
	Approver string
	Filing   string
	ID       string
	Hash     string
	Version  uint64
}

type Entry struct {
	Time     time.Time `json:"time"`
	Action   string    `json:"action"`
	Approver string    `json:"approver"`
	Filing   string    `json:"filing"`
	ID       string    `json:"id"`
	Verb     string    `json:"verb,omitempty"`
	Target   string    `json:"target,omitempty"`
	Hash     string    `json:"hash,omitempty"`
	Result   string    `json:"result"`
}

type Audit struct {
	mu sync.Mutex
	w  io.Writer
}

func NewAudit(w io.Writer) *Audit { return &Audit{w: w} }

func (a *Audit) Record(e Entry) {
	b, _ := json.Marshal(e)
	a.mu.Lock()
	defer a.mu.Unlock()
	_, _ = a.w.Write(append(b, '\n'))
}

type Decider struct {
	Forms Forms
	Audit *Audit
	Now   func() time.Time
}

func (d *Decider) Approve(ctx context.Context, dec Decision) (string, error) {
	e := Entry{Time: d.Now().UTC(), Action: "approve", Approver: dec.Approver, Filing: dec.Filing, ID: dec.ID, Hash: dec.Hash}
	result, err := d.approve(ctx, dec, &e)
	e.Result = result
	if err != nil {
		e.Result = "refused: " + err.Error()
	}
	d.Audit.Record(e)
	return result, err
}

func (d *Decider) approve(ctx context.Context, dec Decision, e *Entry) (string, error) {
	p, err := Build(ctx, d.Forms, dec.Filing, dec.ID, d.Now())
	if err != nil {
		return "", err
	}
	e.Verb, e.Target = p.Verb, p.Target
	if p.Hash != dec.Hash {
		return "", ErrHashChanged
	}
	set := map[string]json.RawMessage{}
	for _, l := range p.Lines {
		set[l.Key] = l.New
	}
	if p.Target == p.Filing {
		rset, remove, err := d.withoutRequest(ctx, p)
		if err != nil {
			return "", err
		}
		maps.Copy(set, rset)
		if err := d.Forms.Set(ctx, p.Filing, set, remove, p.FilingVersion); err != nil {
			return "", fmt.Errorf("%w: %v", ErrStale, err)
		}
		return "applied", nil
	}
	result := "already applied; request removed"
	if !p.Applied {
		if err := d.Forms.Set(ctx, p.Target, set, nil, p.TargetVersion); err != nil {
			return "", fmt.Errorf("%w: target %s: %v", ErrStale, p.Target, err)
		}
		result = "applied"
	}
	rset, remove, err := d.withoutRequest(ctx, p)
	if err == nil {
		err = d.Forms.Set(ctx, p.Filing, rset, remove, p.FilingVersion)
	}
	if err != nil {
		return result + "; request left pending, approving it again only removes it", fmt.Errorf("%w: removing the request: %v", ErrStale, err)
	}
	return result, nil
}

func (d *Decider) Deny(ctx context.Context, dec Decision) (string, error) {
	e := Entry{Time: d.Now().UTC(), Action: "deny", Approver: dec.Approver, Filing: dec.Filing, ID: dec.ID, Result: "denied"}
	err := d.deny(ctx, dec)
	if err != nil {
		e.Result = "refused: " + err.Error()
	}
	d.Audit.Record(e)
	return e.Result, err
}

func (d *Decider) deny(ctx context.Context, dec Decision) error {
	rset, remove, err := d.withoutRequest(ctx, Plan{Filing: dec.Filing, ID: dec.ID, FilingVersion: dec.Version})
	if err != nil {
		return err
	}
	if err := d.Forms.Set(ctx, dec.Filing, rset, remove, dec.Version); err != nil {
		return fmt.Errorf("%w: %v", ErrStale, err)
	}
	return nil
}

func (d *Decider) withoutRequest(ctx context.Context, p Plan) (map[string]json.RawMessage, []string, error) {
	snap, version, err := d.Forms.Read(ctx, p.Filing)
	if err != nil {
		return nil, nil, err
	}
	if version != p.FilingVersion {
		return nil, nil, ErrStale
	}
	entries, err := elicitOf(snap)
	if err != nil {
		return nil, nil, err
	}
	if _, ok := entries[p.ID]; !ok {
		return nil, nil, ErrNoRequest
	}
	delete(entries, p.ID)
	if len(entries) == 0 {
		return map[string]json.RawMessage{}, []string{ElicitKey}, nil
	}
	b, err := json.Marshal(entries)
	if err != nil {
		return nil, nil, err
	}
	return map[string]json.RawMessage{ElicitKey: b}, nil, nil
}

package approvals

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
)

const (
	king  = "@96447061"
	seat  = "0123456789abcdef0123456789abcdef"
	other = "@2b4fe37f"
)

var now = time.Date(2026, 11, 19, 15, 0, 0, 0, time.UTC)

type fakeForm struct {
	snap    form.Snapshot
	version uint64
}

type fakeForms struct {
	forms   map[string]*fakeForm
	outfits map[string]string
	sets    int
	failOn  string
	onSet   func(id string)
}

func newFake() *fakeForms {
	return &fakeForms{forms: map[string]*fakeForm{
		king:  {snap: form.FromMap(map[string]json.RawMessage{"mantra": json.RawMessage(`"rule"`)}), version: 10},
		seat:  {snap: form.FromMap(map[string]json.RawMessage{"system": json.RawMessage(`{"provider":"anthropic","model":"claude-sonnet-4-5"}`)}), version: 20},
		other: {snap: form.FromMap(map[string]json.RawMessage{"charter": json.RawMessage(`"x"`)}), version: 30},
	}, outfits: map[string]string{"opus5": "d1", "succession-standard": "d2"}}
}

func (f *fakeForms) Read(_ context.Context, id string) (form.Snapshot, uint64, error) {
	ff, ok := f.forms[id]
	if !ok {
		return form.Snapshot{}, 0, fmt.Errorf("no form %s", id)
	}
	return ff.snap, ff.version, nil
}

func (f *fakeForms) Set(_ context.Context, id string, set map[string]json.RawMessage, remove []string, ifVersion uint64) error {
	if f.failOn == id {
		return errors.New("crash")
	}
	ff, ok := f.forms[id]
	if !ok {
		return fmt.Errorf("no form %s", id)
	}
	if ifVersion != ff.version {
		return fmt.Errorf("form %s is at %d, not %d", id, ff.version, ifVersion)
	}
	ff.snap = ff.snap.Apply(form.Build(form.Snapshot{}, set, remove))
	ff.version++
	f.sets++
	if f.onSet != nil {
		f.onSet(id)
	}
	return nil
}

func (f *fakeForms) Outfit(_ context.Context, name string) (string, bool, error) {
	d, ok := f.outfits[name]
	return d, ok, nil
}

func (f *fakeForms) file(t *testing.T, on, id string, req map[string]any) {
	t.Helper()
	ff := f.forms[on]
	cur := map[string]json.RawMessage{}
	if raw, ok := ff.snap.Get(ElicitKey); ok {
		_ = json.Unmarshal(raw, &cur)
	}
	if _, ok := req["expires"]; !ok {
		req["expires"] = now.Add(time.Hour).Format(time.RFC3339)
	}
	b, _ := json.Marshal(req)
	cur[id] = b
	all, _ := json.Marshal(cur)
	ff.snap = ff.snap.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{ElicitKey: all}, nil))
	ff.version++
}

func modelReq(target string) map[string]any {
	return map[string]any{"verb": VerbFigaroModel, "target": target, "reason": "audit",
		"set": map[string]any{"system.provider": "anthropic", "system.model": "claude-opus-4-5"}}
}

func decider(f Forms, audit *bytes.Buffer) *Decider {
	return &Decider{Forms: f, Audit: NewAudit(audit), Now: func() time.Time { return now }}
}

func get(t *testing.T, f *fakeForms, id, key string) string {
	t.Helper()
	raw, _ := f.forms[id].snap.Get(key)
	return string(raw)
}

func TestApproveAppliesTargetThenRemovesRequest(t *testing.T) {
	f := newFake()
	f.file(t, king, "r1", modelReq(seat))
	p, err := Build(context.Background(), f, king, "r1", now)
	if err != nil {
		t.Fatal(err)
	}
	var audit bytes.Buffer
	res, err := decider(f, &audit).Approve(context.Background(), Decision{Approver: "gluck", Filing: king, ID: "r1", Hash: p.Hash})
	if err != nil || res != "applied" {
		t.Fatalf("approve = %q, %v", res, err)
	}
	if got := get(t, f, seat, "system.model"); got != `"claude-opus-4-5"` {
		t.Fatalf("model = %s", got)
	}
	if _, ok := f.forms[king].snap.Get(ElicitKey); ok {
		t.Fatal("request still on the King form")
	}
	if !strings.Contains(audit.String(), p.Hash) || strings.Contains(audit.String(), "claude-opus") {
		t.Fatalf("audit line must carry the hash and no values: %s", audit.String())
	}
}

func TestHashMismatchRefused(t *testing.T) {
	f := newFake()
	f.file(t, king, "r1", modelReq(seat))
	var audit bytes.Buffer
	_, err := decider(f, &audit).Approve(context.Background(), Decision{Filing: king, ID: "r1", Hash: strings.Repeat("0", 64)})
	if !errors.Is(err, ErrHashChanged) {
		t.Fatalf("err = %v", err)
	}
	if f.sets != 0 {
		t.Fatal("a refused approve wrote")
	}
	if !strings.Contains(audit.String(), "refused") {
		t.Fatal("refusal not audited")
	}
}

func TestEditAfterRenderChangesHash(t *testing.T) {
	f := newFake()
	f.file(t, king, "r1", modelReq(seat))
	p, _ := Build(context.Background(), f, king, "r1", now)
	edited := modelReq(seat)
	edited["set"] = map[string]any{"system.provider": "anthropic", "system.model": "claude-haiku-4-5"}
	f.file(t, king, "r1", edited)
	_, err := decider(f, &bytes.Buffer{}).Approve(context.Background(), Decision{Filing: king, ID: "r1", Hash: p.Hash})
	if !errors.Is(err, ErrHashChanged) || f.sets != 0 {
		t.Fatalf("err = %v, sets = %d", err, f.sets)
	}
}

func TestStaleCASRefused(t *testing.T) {
	f := newFake()
	f.file(t, king, "r1", modelReq(seat))
	p, _ := Build(context.Background(), f, king, "r1", now)
	d := decider(f, &bytes.Buffer{})
	stale := &raceForms{fakeForms: f, bump: seat}
	d.Forms = stale
	_, err := d.Approve(context.Background(), Decision{Filing: king, ID: "r1", Hash: p.Hash})
	if !errors.Is(err, ErrStale) {
		t.Fatalf("err = %v", err)
	}
	if got := get(t, f, seat, "system.model"); got != `"claude-sonnet-4-5"` {
		t.Fatalf("stale approve wrote the target: %s", got)
	}
}

type raceForms struct {
	*fakeForms
	bump string
}

func (r *raceForms) Set(ctx context.Context, id string, set map[string]json.RawMessage, remove []string, ifVersion uint64) error {
	if id == r.bump {
		r.forms[id].version++
	}
	return r.fakeForms.Set(ctx, id, set, remove, ifVersion)
}

func TestDenyStaleVersionRefused(t *testing.T) {
	f := newFake()
	f.file(t, king, "r1", modelReq(seat))
	_, err := decider(f, &bytes.Buffer{}).Deny(context.Background(), Decision{Filing: king, ID: "r1", Version: f.forms[king].version - 1})
	if !errors.Is(err, ErrStale) || f.sets != 0 {
		t.Fatalf("err = %v", err)
	}
	if _, err := decider(f, &bytes.Buffer{}).Deny(context.Background(), Decision{Filing: king, ID: "r1", Version: f.forms[king].version}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.forms[king].snap.Get(ElicitKey); ok {
		t.Fatal("deny left the request")
	}
}

func TestUnknownVerbRefused(t *testing.T) {
	f := newFake()
	f.file(t, king, "r1", map[string]any{"verb": "shell", "target": seat, "set": map[string]any{"cmd": "true"}})
	if _, err := Build(context.Background(), f, king, "r1", now); !errors.Is(err, ErrUnknownVerb) {
		t.Fatalf("err = %v", err)
	}
}

func TestCrashBetweenWritesThenReapproveOnlyRemoves(t *testing.T) {
	f := newFake()
	f.file(t, king, "r1", modelReq(seat))
	p, _ := Build(context.Background(), f, king, "r1", now)
	f.failOn = king
	_, err := decider(f, &bytes.Buffer{}).Approve(context.Background(), Decision{Filing: king, ID: "r1", Hash: p.Hash})
	if err == nil {
		t.Fatal("want the removal to fail")
	}
	if get(t, f, seat, "system.model") != `"claude-opus-4-5"` {
		t.Fatal("target not written before the crash")
	}
	f.failOn = ""
	p2, err := Build(context.Background(), f, king, "r1", now)
	if err != nil || !p2.Applied {
		t.Fatalf("re-render: applied=%v err=%v", p2.Applied, err)
	}
	seatVersion := f.forms[seat].version
	res, err := decider(f, &bytes.Buffer{}).Approve(context.Background(), Decision{Filing: king, ID: "r1", Hash: p2.Hash})
	if err != nil || res != "already applied; request removed" {
		t.Fatalf("re-approve = %q, %v", res, err)
	}
	if f.forms[seat].version != seatVersion {
		t.Fatal("re-approve wrote the target again")
	}
	if _, ok := f.forms[king].snap.Get(ElicitKey); ok {
		t.Fatal("request not removed")
	}
}

func TestSameFormIsOnePatch(t *testing.T) {
	f := newFake()
	f.file(t, king, "r1", map[string]any{"verb": VerbRoleOutfits, "target": king, "set": map[string]any{"system.grants.outfits": []string{"opus5"}}})
	p, err := Build(context.Background(), f, king, "r1", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decider(f, &bytes.Buffer{}).Approve(context.Background(), Decision{Filing: king, ID: "r1", Hash: p.Hash}); err != nil {
		t.Fatal(err)
	}
	if f.sets != 1 {
		t.Fatalf("sets = %d, want one atomic patch", f.sets)
	}
	if get(t, f, king, "system.grants.outfits") != `["opus5"]` {
		t.Fatal("grant not written")
	}
}

func TestTemplatesRefuseBadValues(t *testing.T) {
	cases := map[string]map[string]any{
		"shell in model":  {"verb": VerbFigaroModel, "target": seat, "set": map[string]any{"system.provider": "anthropic", "system.model": "$(touch /tmp/pwn)"}},
		"extra key":       {"verb": VerbFigaroModel, "target": seat, "set": map[string]any{"system.provider": "anthropic", "system.model": "m", "system.tools": []string{"bash"}}},
		"role target":     modelReq(other),
		"bidi":            {"verb": VerbFigaroModel, "target": seat, "set": map[string]any{"system.provider": "anthropic", "system.model": "opus\u202e"}},
		"glob outfit":     {"verb": VerbRoleOutfits, "target": other, "set": map[string]any{"system.grants.outfits": []string{"*"}}},
		"missing outfit":  {"verb": VerbRoleOutfits, "target": other, "set": map[string]any{"system.grants.outfits": []string{"nope"}}},
		"ttl with spaces": {"verb": VerbFigaroTTL, "target": seat, "set": map[string]any{"system.ttl": "8h; rm -rf ~"}},
		"unknown field":   {"verb": VerbFigaroTTL, "target": seat, "set": map[string]any{"system.ttl": "8h"}, "patch": "x"},
		"expiry too far":  {"verb": VerbFigaroTTL, "target": seat, "set": map[string]any{"system.ttl": "8h"}, "expires": now.Add(48 * time.Hour).Format(time.RFC3339)},
		"expired":         {"verb": VerbFigaroTTL, "target": seat, "set": map[string]any{"system.ttl": "8h"}, "expires": now.Add(-time.Minute).Format(time.RFC3339)},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			f.file(t, king, "r1", req)
			if _, err := Build(context.Background(), f, king, "r1", now); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestRequestFieldsNeverReachAShell(t *testing.T) {
	f := newFake()
	payload := "$(touch /tmp/pwn); `id` | sh"
	f.file(t, king, "r1", map[string]any{"verb": VerbFigaroTTL, "target": seat, "reason": payload, "state": payload,
		"set": map[string]any{"system.ttl": "8h"}})
	p, err := Build(context.Background(), f, king, "r1", now)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(p)
	if strings.Contains(string(b), "pwn") {
		t.Fatal("request prose reached the plan")
	}
	pkgs, err := parser.ParseDir(token.NewFileSet(), ".", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			for _, imp := range file.Imports {
				if imp.Path.Value == `"os/exec"` || imp.Path.Value == `"syscall"` {
					t.Fatalf("%s imports %s: nothing here may start a process", filepath.Base(name), imp.Path.Value)
				}
			}
		}
	}
}

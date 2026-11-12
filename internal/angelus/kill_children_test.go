package angelus_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func killAs(r grantRig, req rpc.KillRequest) (rpc.KillResponse, error) {
	out, err := r.call(asOwner(), rpc.MethodKill, req)
	if err != nil {
		return rpc.KillResponse{}, err
	}
	return out.(rpc.KillResponse), nil
}

func absent(r grantRig, id string) bool {
	_, ok := r.backend.Node(id)
	return !ok
}

func reapedIn(t *testing.T, r grantRig, id string) bool {
	t.Helper()
	meta, err := r.backend.Meta(id)
	if err != nil {
		t.Fatal(err)
	}
	return meta != nil && meta.ReapedAtMS > 0
}

func scopesOf(t *testing.T, r grantRig, id string) string {
	t.Helper()
	snap, err := r.backend.FormState(id)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := snap.Get(form.ScopesKey)
	return string(raw)
}

func narrow(t *testing.T, r grantRig, id string, scopes ...string) {
	t.Helper()
	raw, _ := json.Marshal(scopes)
	if _, err := r.backend.ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{form.ScopesKey: raw}, nil)); err != nil {
		t.Fatal(err)
	}
}

func TestKeepChildrenLiftsTheChildrenToTheParent(t *testing.T) {
	for _, now := range []bool{false, true} {
		r, p, x, c := guardChain(t)
		d := ariasFork(t, r, x)
		g := ariasFork(t, r, c)
		resp, err := killAs(r, rpc.KillRequest{FigaroID: x, KeepChildren: true, Now: now})
		if err != nil {
			t.Fatalf("now=%v: %v", now, err)
		}
		if lifted := slices.Sorted(slices.Values(resp.Lifted)); !slices.Equal(lifted, slices.Sorted(slices.Values([]string{c, d}))) {
			t.Fatalf("now=%v: lifted %v, want C and D", now, resp.Lifted)
		}
		if now && !absent(r, x) || !now && !reapedIn(t, r, x) {
			t.Fatalf("now=%v: X survived its kill", now)
		}
		for _, kid := range []string{c, d} {
			if absent(r, kid) || reapedIn(t, r, kid) {
				t.Fatalf("now=%v: --keep-children took %s", now, kid)
			}
			if up := presentedUnder(t, r, kid); up != p {
				t.Fatalf("now=%v: %s is drawn under %q, want P %s", now, kid, up, p)
			}
		}
		if absent(r, g) || presentedUnder(t, r, g) != c {
			t.Fatalf("now=%v: the grandchild did not stay under C", now)
		}
	}
}

func TestKeepChildrenAtTheTopLiftsToTheTop(t *testing.T) {
	r, p, x, c := guardChain(t)
	top := presentedUnder(t, r, p)
	if _, err := killAs(r, rpc.KillRequest{FigaroID: p, KeepChildren: true, Now: true}); err != nil {
		t.Fatal(err)
	}
	if !absent(r, p) {
		t.Fatal("P survived its kill")
	}
	if up := presentedUnder(t, r, x); up != top {
		t.Fatalf("X is drawn under %q, want the top %q", up, top)
	}
	if up := presentedUnder(t, r, c); up != x {
		t.Fatalf("C left X for %q", up)
	}
}

// P4: C is narrowed only by X. Lifted out from under X, it keeps that
// narrowing in its own scopes.
func TestALiftedChildKeepsTheNarrowingItInheritedThroughTheKilledFigaro(t *testing.T) {
	for _, now := range []bool{false, true} {
		r, _, x, c := guardChain(t)
		narrow(t, r, x, "read:*", "kill:*")
		before, _ := r.a.Policies.Effective(c)
		if before.String() != "read:*, kill:*" {
			t.Fatalf("control: C holds %s through X", before)
		}
		if _, err := killAs(r, rpc.KillRequest{FigaroID: x, KeepChildren: true, Now: now}); err != nil {
			t.Fatal(err)
		}
		if got := scopesOf(t, r, c); got != `["read:*","kill:*"]` {
			t.Fatalf("now=%v: C carries %s, want X's narrowing", now, got)
		}
		after, _ := r.a.Policies.Effective(c)
		if after.String() != before.String() {
			t.Fatalf("now=%v: C widened from %s to %s", now, before, after)
		}
	}
}

func TestARefusedLiftRefusesTheWholeKill(t *testing.T) {
	cases := []struct {
		name   string
		grants []string
		want   string
	}{
		{"no adopt on the receiving parent", []string{"read:*", "kill:aria:X", "kill:aria:C"}, "receiving parent P"},
		{"no kill on the child", []string{"read:*", "kill:aria:X", "adopt:*"}, "kill on aria:C"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, p, x, c := guardChain(t)
			narrow(t, r, x, "read:*")
			var grants []string
			for _, g := range tc.grants {
				grants = append(grants, strings.NewReplacer("aria:X", "aria:"+x, "aria:C", "aria:"+c).Replace(g))
			}
			grantTo(t, r, "user:mallory", grants...)
			want := strings.NewReplacer(" P", " "+p, "aria:C", "aria:"+c).Replace(tc.want)
			for _, now := range []bool{false, true} {
				_, err := r.call(mallory(), rpc.MethodKill, rpc.KillRequest{FigaroID: x, KeepChildren: true, Now: now})
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("now=%v: mallory's --keep-children = %v, want refused: %s", now, err, want)
				}
				if absent(r, x) || reapedIn(t, r, x) || presentedUnder(t, r, c) != x {
					t.Fatalf("now=%v: a refused lift left the kill half done", now)
				}
			}
			grantTo(t, r, "user:mallory", "read:*", "kill:aria:"+x, "kill:aria:"+c, "adopt:*")
			if _, err := r.call(mallory(), rpc.MethodKill, rpc.KillRequest{FigaroID: x, KeepChildren: true, Now: true}); err != nil {
				t.Fatalf("control: with kill on both and adopt: %v", err)
			}
			if presentedUnder(t, r, c) != p {
				t.Fatal("control: C was not lifted")
			}
		})
	}
}

func TestKeepChildrenWithRIsRefused(t *testing.T) {
	r, _, x, c := guardChain(t)
	_, err := killAs(r, rpc.KillRequest{FigaroID: x, Recursive: true, KeepChildren: true, GraceMS: time.Hour.Milliseconds()})
	if err == nil || !strings.Contains(err.Error(), "--keep-children") {
		t.Fatalf("-r with --keep-children = %v, want refused", err)
	}
	if absent(r, x) || absent(r, c) || reapedIn(t, r, x) {
		t.Fatal("a refused kill took something")
	}
}

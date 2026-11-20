package angelus_test

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authn"
	"github.com/jack-work/figaro/internal/authz"
)

var updateParity = flag.Bool("update-parity", false, "rewrite testdata/box_door_parity.golden")

// A figaro asks the daemon the same way from inside its box as from its
// own tool outside: the box door's claim is its identity, and the same
// Call and Patch hooks judge it. With the box's role granting what the
// owner's grants, every method on every target must decide the same on
// both sides. The golden lists each pair; a row whose sides differ is the
// box door doing something its figaro could not do outside, or the
// reverse, and must be named in boxOnlyLimits with its reason.

func parityPolicy() string {
	return `
[roles.all]
permissions = ["*:*"]

[[bind]]
role = "all"
principals = ["user:` + strconv.Itoa(os.Getuid()) + `", "` + authn.BoxPrincipal("judge").String() + `"]
`
}

const (
	importLimit = "a box may not import: an export carries any form key (topology_handlers importAria)"
	castLimit   = "a role cast from a box points only at an aria in that box (holdCastInBox)"
	leaseLimit  = "worker.say speaks for the box's own lease, so its resource is lease:<box>; outside there is no lease to name"
)

var boxOnlyLimits = map[string]string{
	rpc.MethodImport:           importLimit,
	rpc.MethodImport + " C":    importLimit,
	rpc.MethodImport + " S":    importLimit,
	rpc.MethodImport + " O":    importLimit,
	"cast mantra on O":         castLimit,
	rpc.MethodWorkerSay:        leaseLimit,
	rpc.MethodWorkerSay + " C": leaseLimit,
	rpc.MethodWorkerSay + " S": leaseLimit,
	rpc.MethodWorkerSay + " O": leaseLimit,
	"worker.say":               leaseLimit,
}

type parityPair struct {
	figaro string
	k      eqCase
}

func parityPairs() []parityPair {
	var out []parityPair
	for _, f := range []string{"P", "C"} {
		for _, m := range authz.DeclaredMethods() {
			for _, target := range []string{"", "C", "O"} {
				name := m
				if target != "" {
					name += " " + target
				}
				out = append(out, parityPair{f, eqCase{name: name, method: m, params: func(t *testing.T, w *eqWorld) json.RawMessage {
					if target == "" {
						return json.RawMessage(`{}`)
					}
					return withAria(t, w.id(target), map[string]any{})
				}}})
			}
		}
	}
	for _, f := range []string{"P", "C", "S", "R"} {
		for _, k := range eqCases() {
			out = append(out, parityPair{f, k})
		}
	}
	return out
}

func (w *eqWorld) paritySide(t *testing.T, inBox bool, figaro string, k eqCase) string {
	t.Helper()
	params := claimed(t, w.id(figaro), k.params(t, w))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out any
	var err error
	if inBox {
		var raw json.RawMessage
		err = w.box.Call(ctx, k.method, params, &raw)
		if err == nil && k.rows {
			out, err = decodeRows(k.method, raw)
		}
	} else {
		fn, ok := w.h[k.method]
		if !ok {
			return "unserved"
		}
		out, err = fn(authn.WithToolOf(authn.WithDoor(asOwnerCtx(ctx), authn.DoorLocal), w.id(figaro)), params)
	}
	got := w.decided(out, err, k)
	if strings.HasPrefix(got, "refused: [-32601] method not found") {
		return "unserved"
	}
	got = strings.Replace(got, "refused: [-32000] ", "refused: ", 1)
	got = strings.ReplaceAll(got, "user:<uid>", "<subject>")
	return strings.ReplaceAll(got, authn.BoxPrincipal("judge").String(), "<subject>")
}

func newParityWorld(t *testing.T) *eqWorld {
	t.Helper()
	w := newEqWorldUnder(t, parityPolicy(), "")
	root, err := w.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	w.names[root.FigaroID] = "R"
	for _, id := range []string{w.parent, w.child, w.sibling, root.FigaroID} {
		if _, err := w.h[rpc.MethodSet](asOwner(), withAria(t, id, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{"system.grants.outfits": []string{"mock", "judged"}})})); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

func asOwnerCtx(ctx context.Context) context.Context {
	return authn.WithPrincipal(ctx, rpc.Principal{Kind: rpc.PrincipalUser, ID: strconv.Itoa(os.Getuid())})
}

func TestTheBoxDoorDecidesAsTheFigarosOwnTool(t *testing.T) {
	golden := filepath.Join("testdata", "box_door_parity.golden")
	pairs := parityPairs()
	rows := make([]string, len(pairs))
	differ := make([]string, len(rows))
	t.Run("rows", func(t *testing.T) {
		for n, p := range pairs {
			f, k := p.figaro, p.k
			t.Run("", func(t *testing.T) {
				t.Parallel()
				outside := newParityWorld(t).paritySide(t, false, f, k)
				inside := newParityWorld(t).paritySide(t, true, f, k)
				mark := "="
				if outside != inside {
					mark, differ[n] = "≠", k.name
				}
				rows[n] = fmt.Sprintf("%s | as %s | %-32s | outside: %s\n%s | as %s | %-32s | inside:  %s\n", mark, f, k.name, outside, mark, f, k.name, inside)
			})
		}
	})
	var b strings.Builder
	for i, r := range rows {
		b.WriteString(r)
		if differ[i] == "" {
			continue
		}
		if _, named := boxOnlyLimits[differ[i]]; !named {
			t.Errorf("the box door and the figaro's own tool decide differently and no limit names why:\n%s", r)
		}
	}
	if *updateParity {
		if err := os.WriteFile(golden, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v: run with -update-parity", err)
	}
	if string(want) != b.String() {
		t.Error("the parity table moved; run with -update-parity and read the diff")
	}
}

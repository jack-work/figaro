package angelus_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func TestAForkIntoALogListsAsALog(t *testing.T) {
	r := logRig(t, defaultPolicy, nil)
	model, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	bare := form.Build(form.Snapshot{}, map[string]json.RawMessage{form.KindKey: json.RawMessage(`"log"`)}, []string{"system.provider", "system.model"})
	out, err := r.call(asOwner(), rpc.MethodFork, rpc.ForkRequest{FigaroID: model.FigaroID, Patch: &bare})
	if err != nil {
		t.Fatal(err)
	}
	child := out.(rpc.ForkResponse).Alternative
	time.Sleep(200 * time.Millisecond)
	m, _ := r.backend.Meta(child)
	t.Logf("sidecar kind=%q", m.Kind)
	if row := listedRow(t, r, child); row.Kind != form.KindLog {
		t.Fatalf("a fork into a log lists as kind %q", row.Kind)
	}
}

func TestAForkOfALogIntoAFigaroListsAsAFigaro(t *testing.T) {
	r := logRig(t, defaultPolicy, nil)
	id := mintLog(t, r, asOwner())
	for _, s := range []string{"a", "b"} {
		if err := appendTo(t, r, asOwner(), id, s); err != nil {
			t.Fatal(err)
		}
	}
	waitLogParts(t, r, id, 2)
	unlog := form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.provider": json.RawMessage(`"mock"`)}, []string{form.KindKey})
	out, err := r.call(asOwner(), rpc.MethodFork, rpc.ForkRequest{FigaroID: id, Patch: &unlog})
	if err != nil {
		t.Fatal(err)
	}
	child := out.(rpc.ForkResponse).Alternative
	time.Sleep(200 * time.Millisecond)
	snap, _ := r.backend.FormState(child)
	m, _ := r.backend.Meta(child)
	t.Logf("form log=%v sidecar kind=%q turns=%d", form.IsLog(snap), m.Kind, m.TurnCount)
	if row := listedRow(t, r, child); row.Kind == form.KindLog {
		t.Fatalf("a figaro forked from a log lists as kind %q (form log=%v)", row.Kind, form.IsLog(snap))
	}
}

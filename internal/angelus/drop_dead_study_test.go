package angelus

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
)

// An id no form answers to is read against a study list only for the
// studier itself or the owner; another figaro learns nothing from drop.
func TestOnlyTheStudierHasADeadStudyResolved(t *testing.T) {
	h, _ := resolveHandlers(t)
	studier := mintConversation(t, h)
	other := mintConversation(t, h)
	formID, _, err := h.angelus.Backend.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"brief": json.RawMessage(`"watched"`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	dead := formID
	if _, err := h.studyForHub(studier, dead, nil, false); err != nil {
		t.Fatal(err)
	}
	ref := dead[:9]

	if got, ok := h.studiedByCaller(authz.WithToolOf(context.Background(), studier), rpc.MethodDrop, studier, ref); !ok || got != dead {
		t.Fatalf("the studier's own drop resolved %q, %v; want %q", got, ok, dead)
	}
	if got, ok := h.studiedByCaller(authz.WithToolOf(context.Background(), other), rpc.MethodDrop, studier, ref); ok {
		t.Fatalf("another figaro's drop resolved %q against the studier's list", got)
	}
}

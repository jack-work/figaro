package angelus

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

// Studying a figaro reads it for as long as the study stands, so the call is
// gated on the read `state show --id` needs, and a caller without it hears
// what it hears for a figaro that never existed.
func TestStudyOfAFigaroTheCallerCannotReadIsHidden(t *testing.T) {
	r := newFateRig(t)
	study := func(ref string) map[string]any { return map[string]any{"figaro_id": r.guests, "form_id": ref} }
	show := func(ref string) map[string]any { return map[string]any{"figaro_id": ref} }

	r.hidden(rpc.MethodForm, show, r.other, neverID)
	r.hidden(rpc.MethodStudy, study, r.other, neverID)
	want := r.call(r.guest, rpc.MethodForm, show(r.other)).Error()
	got := r.call(r.guest, rpc.MethodStudy, study(r.other)).Error()
	if strings.Replace(want, rpc.MethodForm, rpc.MethodStudy, 1) != got {
		t.Fatalf("study and state show refuse differently:\n  study: %s\n  show:  %s", got, want)
	}

	if err := r.call(r.owner, rpc.MethodStudy, study(r.other)); err != nil {
		t.Fatalf("control: a caller with read studies the figaro: %v", err)
	}
}

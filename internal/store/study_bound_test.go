package store

import (
	"strings"
	"testing"
)

func twoFigaros(t *testing.T) (*XwalBackend, string, string) {
	t.Helper()
	be, _, _ := librettoFixture(t)
	outfit, err := be.CreateOutfit("pair", setPatch(map[string]string{"system.model": "m"}))
	if err != nil {
		t.Fatal(err)
	}
	parent, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	child, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	return be, parent, child
}

// The libretto opens a bound form the way it opens an unbound one, so a
// figaro's form is followed without a second mechanism.
func TestAFigaroStudiesAnotherFigarosBoundForm(t *testing.T) {
	be, parent, child := twoFigaros(t)
	if err := RequireStudyTarget(be, child, parent); err != nil {
		t.Fatalf("study of another figaro refused: %v", err)
	}
	if _, err := be.StudyForm(child, parent); err != nil {
		t.Fatal(err)
	}
	lib, err := be.Libretto(parent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := be.ApplyForm(parent, setPatch(map[string]string{"comments.c1.body": "why?"})); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the libretto to follow the parent's bound form", func() bool {
		raw, ok := lib.State().Get("comments.c1.body")
		return ok && string(raw) == `"why?"`
	})
	if got := lib.Refs(); got != 1 {
		t.Fatalf("refs = %d, want 1", got)
	}
	audit, err := be.ReconcileLibrettos()
	if err != nil {
		t.Fatal(err)
	}
	if audit.Corrected != 0 {
		t.Fatalf("the sweep disagreed with a bound-form study: %+v", audit)
	}
}

func TestAFigaroCannotStudyItself(t *testing.T) {
	be, parent, child := twoFigaros(t)
	if err := RequireStudyTarget(be, child, parent); err != nil {
		t.Fatalf("control: another figaro is studiable: %v", err)
	}
	err := RequireStudyTarget(be, child, child)
	if err == nil || !strings.Contains(err.Error(), "does not study its own bound form") {
		t.Fatalf("self-study: %v", err)
	}
}

func TestCastStillTakesOnlyUnboundForms(t *testing.T) {
	be, parent, _ := twoFigaros(t)
	role, _, err := be.CreateForm("", setPatch(map[string]string{"name": "role"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := RequireCastTarget(be, role); err != nil {
		t.Fatalf("control: an unbound form is castable: %v", err)
	}
	err = RequireCastTarget(be, parent)
	if err == nil || !strings.Contains(err.Error(), "figaro, not an unbound form") {
		t.Fatalf("cast of a figaro: %v", err)
	}
}

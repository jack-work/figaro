package cli

import (
	"context"
	"strings"
	"testing"
)

const studyMe, studyParent = "aaaa1111aaaa1111aaaa1111aaaa1111", "bbbb2222bbbb2222bbbb2222bbbb2222"

// The form slot of study and drop takes a bare figaro id; cast's does not.
func TestTheStudySlotTakesAFigaro(t *testing.T) {
	for _, verb := range []string{"study", "drop"} {
		aria, target, err := resolveStudyArgs(context.Background(), nil, []string{studyMe, studyParent}, false, verb)
		if err != nil || aria != studyMe || target != studyParent {
			t.Fatalf("%s <me> <figaro>: %q %q %v", verb, aria, target, err)
		}
	}
	_, _, err := resolveStudyArgs(context.Background(), nil, []string{studyMe, studyParent}, false, "cast")
	if err == nil || !strings.Contains(err.Error(), "takes an unbound form") {
		t.Fatalf("cast <me> <figaro>: %v", err)
	}
	if _, target, err := resolveStudyArgs(context.Background(), nil, []string{studyMe, "@f00d"}, false, "cast"); err != nil || target != "@f00d" {
		t.Fatalf("control: cast <me> <@form>: %q %v", target, err)
	}
}

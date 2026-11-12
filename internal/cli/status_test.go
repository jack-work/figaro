package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func TestPrintStatusPanelShowsContextCapacityAndTokenCost(t *testing.T) {
	out, err := os.CreateTemp(t.TempDir(), "status")
	if err != nil {
		t.Fatal(err)
	}

	printStatusPanel(out, &rpc.FigaroInfoResponse{
		ID:            "dac6cb6d",
		Mantra:        "ship a polished app studio",
		ContextTokens: 12000,
		ContextLimit:  128000,
		ContextExact:  true,
		TokensIn:      10000,
		TokensOut:     5000,
	}, false, 0, form.Snapshot{})
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"mantra:",
		"ship a polished app studio",
		"context:",
		"12.0k/128.0k 9.4%",
		"cost:",
		"15.0k tok",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("status panel missing %q:\n%s", want, text)
		}
	}
}

func TestPrintStatusPanelOmitsOutfitParentAsForkOrigin(t *testing.T) {
	out, err := os.CreateTemp(t.TempDir(), "status")
	if err != nil {
		t.Fatal(err)
	}
	printStatusPanel(out, &rpc.FigaroInfoResponse{
		ID:         "dac6cb6d",
		Parent:     "default-outfit",
		BranchedLT: 0,
		Vector:     []int{0},
	}, true, 0, form.Snapshot{})
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "forked-from") {
		t.Fatalf("top-level outfit parent must not be shown as a fork:\n%s", body)
	}
}

func statusPanelOf(t *testing.T, f *rpc.FigaroInfoResponse, forkTurn uint64) string {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "status")
	if err != nil {
		t.Fatal(err)
	}
	printStatusPanel(out, f, true, forkTurn, form.Snapshot{})
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// A placed fork answers to one figaro and was cut from another: forked-from
// names the cut. A daemon older than the field sends none, and the parent
// stands in as it did before.
func TestStatusForkedFromNamesTheForkPointNotThePlacement(t *testing.T) {
	placed := statusPanelOf(t, &rpc.FigaroInfoResponse{
		ID: "b0b0b0b0", Parent: "1ead1ead", ForkPoint: "f125f125", BranchedLT: 7, Vector: []int{0, 0, 1},
	}, 1)
	if !strings.Contains(placed, "f125f125:1") || strings.Contains(placed, "1ead1ead:1") {
		t.Fatalf("forked-from must name the fork point f125f125:1:\n%s", placed)
	}
	unplaced := statusPanelOf(t, &rpc.FigaroInfoResponse{
		ID: "b0b0b0b0", Parent: "1ead1ead", BranchedLT: 7, Vector: []int{0, 0, 1},
	}, 3)
	if !strings.Contains(unplaced, "1ead1ead:3") {
		t.Fatalf("with no fork point the parent is the cut:\n%s", unplaced)
	}
}

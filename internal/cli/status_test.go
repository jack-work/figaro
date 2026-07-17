package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/rpc"
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
	}, false)
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

func TestPrintStatusPanelOmitsLoadoutParentAsForkOrigin(t *testing.T) {
	out, err := os.CreateTemp(t.TempDir(), "status")
	if err != nil {
		t.Fatal(err)
	}
	printStatusPanel(out, &rpc.FigaroInfoResponse{
		ID:         "dac6cb6d",
		Parent:     "default-loadout",
		BranchedLT: 0,
		Vector:     []int{0},
	}, true)
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "forked-from") {
		t.Fatalf("top-level loadout parent must not be shown as a fork:\n%s", body)
	}
}

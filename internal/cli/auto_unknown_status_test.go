package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func TestAutoUnknownCapacityStatusNeverInventsAPercentage(t *testing.T) {
	for _, tokens := range []int{0, 52_000} {
		out, err := os.CreateTemp(t.TempDir(), "status")
		if err != nil {
			t.Fatal(err)
		}
		printStatusPanel(out, &rpc.FigaroInfoResponse{ID: "auto-context", ContextLimit: 0, ContextTokens: tokens}, false, 0, form.Snapshot{})
		if err = out.Close(); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(out.Name())
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if !strings.Contains(text, "capacity unknown") {
			t.Fatalf("tokens %d: missing unknown capacity: %s", tokens, text)
		}
		if strings.Contains(text, "%") {
			t.Fatalf("tokens %d: invented context percentage: %s", tokens, text)
		}
	}
}

func TestUnknownLimitNeverPrintsAPercentage(t *testing.T) {
	for _, tokens := range []int{1, 52_000, 2_000_000} {
		got := formatContextUsage(tokens, 0, true)
		if !strings.HasSuffix(got, "(capacity unknown)") || strings.Contains(got, "%") {
			t.Fatalf("formatContextUsage(%d, 0) = %q", tokens, got)
		}
	}
}

package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func TestAutoUnknownCapacityStatusNeverInventsAPercentage(t *testing.T) {
	out, err := os.CreateTemp(t.TempDir(), "status")
	if err != nil {
		t.Fatal(err)
	}
	printStatusPanel(out, &rpc.FigaroInfoResponse{ID: "auto-context", ContextLimit: 0}, false, 0, form.Snapshot{})
	if err = out.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "capacity unknown") {
		t.Fatalf("missing unknown capacity: %s", text)
	}
	if strings.Contains(text, "0.0%") {
		t.Fatalf("invented context percentage: %s", text)
	}
}

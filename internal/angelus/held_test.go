package angelus

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestTheSweepLogsAKeptNodeOncePerReason(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	var h heldNodes
	sweep := func(by, why string) {
		if by == "" {
			h.release("n1")
			return
		}
		h.keep("n1", by, why, "kept: "+why)
	}
	for range 3 {
		sweep("f1", "a shell is bound")
	}
	if n := strings.Count(buf.String(), "kept:"); n != 1 {
		t.Fatalf("three sweeps for one reason logged %d times:\n%s", n, buf.String())
	}
	first, ok := h.get("n1")
	if !ok || first.By != "f1" || first.Why != "a shell is bound" {
		t.Fatalf("held %+v %v", first, ok)
	}
	sweep("f2", "a shell is bound")
	if n := strings.Count(buf.String(), "kept:"); n != 2 {
		t.Fatalf("a new holder was not logged: %d", n)
	}
	sweep("", "")
	if _, ok := h.get("n1"); ok {
		t.Fatal("a node released by the sweep still shows held")
	}
	sweep("f2", "a shell is bound")
	if n := strings.Count(buf.String(), "kept:"); n != 3 {
		t.Fatalf("held again after a release was not logged: %d", n)
	}
}

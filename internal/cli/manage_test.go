package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/cli/figtree"
	"github.com/jack-work/figaro/internal/term"
)

func TestRenderListRowsUsesCompactHierarchyOnNarrowTerminals(t *testing.T) {
	rows := []figtree.Row{{
		Branch: "│ └─",
		Marker: "▸",
		Label:  "a very long aria mantra that must not wrap",
		Fields: map[string]string{fieldID: "dac6cb6d", fieldAge: "4m", fieldMsgs: "12"},
	}}

	got := renderListRows(rows, 48, false)
	if strings.Contains(got, "OUTFIT") {
		t.Fatalf("narrow list must not render a table: %q", got)
	}
	if !strings.Contains(got, "└─▸") || !strings.Contains(got, "dac6cb6d") {
		t.Fatalf("compact row lost hierarchy or id: %q", got)
	}
	if !strings.Contains(got, "4m") || !strings.Contains(got, "12msg") {
		t.Fatalf("compact row lost age or message count: %q", got)
	}
	for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
		if term.VisibleLen(line) > 48 {
			t.Fatalf("compact row wrapped width %d: %q", 48, line)
		}
	}
}

func TestRenderListRowsUsesReducedColumnsOnMediumTerminals(t *testing.T) {
	rows := []figtree.Row{{
		Marker: "○",
		Label:  "orchard",
		Fields: map[string]string{
			fieldID: "1af9efd8", fieldOutfit: "default-production-outfit",
			fieldAge: "2h", fieldMsgs: "42", fieldCtx: "19k",
		},
	}}

	got := renderListRows(rows, 120, false)
	if !strings.Contains(got, "OUTFIT") || strings.Contains(got, "FORK") {
		t.Fatalf("medium list must use reduced columns: %q", got)
	}
	for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
		if term.VisibleLen(line) > 120 {
			t.Fatalf("medium row wrapped width %d: %q", 120, line)
		}
	}
}

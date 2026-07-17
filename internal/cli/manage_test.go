package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/term"
)

func TestRenderListRowsUsesCompactHierarchyOnNarrowTerminals(t *testing.T) {
	rows := []listRow{{
		aria: "│ └─▸ a very long aria mantra that must not wrap",
		id:   "dac6cb6d",
		age:  "4m",
		msgs: "12",
	}}

	got := renderListRows(rows, 48, false)
	if strings.Contains(got, "LOADOUT") {
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
	rows := []listRow{{
		aria:    "○ orchard",
		id:      "1af9efd8",
		loadout: "default-production-loadout",
		age:     "2h",
		msgs:    "42",
		ctx:     "19k",
	}}

	got := renderListRows(rows, 120, false)
	if !strings.Contains(got, "LOADOUT") || strings.Contains(got, "FORK") {
		t.Fatalf("medium list must use reduced columns: %q", got)
	}
	for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
		if term.VisibleLen(line) > 120 {
			t.Fatalf("medium row wrapped width %d: %q", 120, line)
		}
	}
}

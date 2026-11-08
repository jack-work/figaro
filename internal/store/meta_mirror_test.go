package store

import "testing"

// A fork's sidecar is seeded from its parent's, mantra included, and only a
// live agent's end of turn wrote it again, so `figaro set --id <fork> mantra
// B` on a dormant fork changed the form while ls, status and @arias kept the
// parent's A. A commit that sets or removes the mantra or the cwd now writes
// the sidecar too; a commit that touches neither leaves it alone.
func TestACommitToTheMantraOrCwdReachesTheSidecar(t *testing.T) {
	be, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	id, _, err := be.ForkWith("", 0, patchOf(t, map[string]string{"mantra": `"A"`}))
	if err != nil {
		t.Fatal(err)
	}
	if err := be.SetMeta(id, &AriaMeta{Mantra: "A", Cwd: "/seeded", MessageCount: 3}); err != nil {
		t.Fatal(err)
	}
	meta := func() AriaMeta {
		t.Helper()
		m, err := be.Meta(id)
		if err != nil || m == nil {
			t.Fatalf("meta: %v %v", m, err)
		}
		return *m
	}

	mustPatch(t, be, id, patchOf(t, map[string]string{"mantra": `"B"`}))
	if m := meta(); m.Mantra != "B" || m.Cwd != "/seeded" || m.MessageCount != 3 {
		t.Errorf("after set mantra B: mantra %q cwd %q count %d, want B, /seeded, 3", m.Mantra, m.Cwd, m.MessageCount)
	}
	mustPatch(t, be, id, patchOf(t, map[string]string{"system.cwd": `"/elsewhere"`}))
	if m := meta(); m.Cwd != "/elsewhere" || m.Mantra != "B" {
		t.Errorf("after set system.cwd: mantra %q cwd %q", m.Mantra, m.Cwd)
	}
	mustPatch(t, be, id, patchOf(t, map[string]string{"unrelated": `1`}))
	if m := meta(); m.Mantra != "B" || m.Cwd != "/elsewhere" {
		t.Errorf("an unrelated key moved the sidecar: mantra %q cwd %q", m.Mantra, m.Cwd)
	}
	mustPatch(t, be, id, dropOf("mantra"))
	if m := meta(); m.Mantra != "" {
		t.Errorf("after unset mantra the sidecar still says %q", m.Mantra)
	}
}

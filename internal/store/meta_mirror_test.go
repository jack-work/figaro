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

// A dormant figaro whose provider or model is set lists what its next turn
// will use. An unset leaves the sidecar's last value: what the turn would
// fall back to (config default_model, then the provider's own default) is
// only known by resolving providers, which is the agent's to do, and its end
// of turn writes the resolved name over whatever is here.
func TestACommitToTheProviderOrModelReachesTheSidecar(t *testing.T) {
	be, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	id, _, err := be.ForkWith("", 0, patchOf(t, map[string]string{"system.provider": `"anthropic"`, "system.model": `"m1"`}))
	if err != nil {
		t.Fatal(err)
	}
	if err := be.SetMeta(id, &AriaMeta{Provider: "anthropic", Model: "m1", Mantra: "kept"}); err != nil {
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

	mustPatch(t, be, id, patchOf(t, map[string]string{"system.provider": `"copilot"`, "system.model": `"m2"`}))
	if m := meta(); m.Provider != "copilot" || m.Model != "m2" || m.Mantra != "kept" {
		t.Errorf("after set: provider %q model %q mantra %q, want copilot, m2, kept", m.Provider, m.Model, m.Mantra)
	}
	mustPatch(t, be, id, dropOf("system.model"))
	if m := meta(); m.Model != "m2" || m.Provider != "copilot" {
		t.Errorf("after unset system.model: provider %q model %q, want the last values kept", m.Provider, m.Model)
	}
}

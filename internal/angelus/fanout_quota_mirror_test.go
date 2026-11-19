package angelus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/store"
)

func forgetQuotaMirror(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, "schema.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if _, ok := schema["quota-mirror"]; !ok {
		t.Fatalf("schema.json carries no quota-mirror mark to forget: %s", raw)
	}
	delete(schema, "quota-mirror")
	if raw, err = json.Marshal(schema); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// v0.75.3 took system.fanout.quota as a plain form key, mirrored nothing into
// the sidecar, and copied it into every fork. After the upgrade the cache
// read no quota at all; mirroring every form would have capped each copy and
// overdrawn the figaro that was forked.
func TestAQuotaWrittenBeforeTheMirrorComesUpCappedAndAForksCopyDoesNot(t *testing.T) {
	root := t.TempDir()
	b, err := store.NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	outfit, err := b.CreateOutfit("d", form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.model": json.RawMessage(`"m"`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	capped, _ := b.CreateConversation(outfit)
	plain, _ := b.CreateConversation(outfit)
	setQuota(t, b, capped, 5)
	_, copied, err := b.Fork(capped)
	if err != nil {
		t.Fatal(err)
	}
	_, granted, err := b.Fork(capped)
	if err != nil {
		t.Fatal(err)
	}
	setQuota(t, b, granted, 1)
	for _, id := range []string{capped, copied, granted} {
		if err := b.UpdateMeta(id, func(m *store.AriaMeta) { m.FanoutQuota = nil }); err != nil {
			t.Fatal(err)
		}
	}
	if snap, _ := b.FormState(copied); !hasKey(snap, form.FanoutQuotaKey) {
		t.Fatal("the fork did not copy its parent's quota, as v0.75.3 did")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	forgetQuotaMirror(t, root)

	b, err = store.NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	a := &Angelus{Registry: NewRegistry(), Backend: b, Hubs: newHubs()}
	c := a.fanoutCounts()
	if got := c.budget(capped); !got.capped || got.quota != 5 || got.used != 3 {
		t.Fatalf("a quota of 5 written before the mirror came up as %+v, want 3 of 5", got)
	}
	if got := c.budget(granted); !got.capped || got.quota != 1 {
		t.Fatalf("a quota of 1 the fork set itself came up as %+v", got)
	}
	if got := c.budget(copied); got.capped {
		t.Fatalf("a quota the fork only copied from its parent came up capped: %+v", got)
	}
	if snap, _ := b.FormState(copied); hasKey(snap, form.FanoutQuotaKey) {
		t.Fatal("the copied quota is still on the fork's form, where the next outfit change would mirror it")
	}
	if got := c.budget(plain); got.capped {
		t.Fatalf("a figaro with no quota came up capped: %+v", got)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "schema.json"))
	var schema struct {
		QuotaMirror bool `json:"quota-mirror"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil || !schema.QuotaMirror {
		t.Fatalf("the open did not mark the store mirrored: %s", raw)
	}
}

func hasKey(snap form.Snapshot, key string) bool {
	_, ok := snap.Get(key)
	return ok
}

package angelus

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jack-work/jkauthz"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/store"
)

// system.retention is owner-held, and the owner may name who else sets it;
// the grant never reaches the list of grantees itself.
func TestTheOwnerCanWidenWhoSetsRetention(t *testing.T) {
	gary := jkauthz.Principal{Kind: "user", ID: "gary"}
	ctx := authz.WithSubject(context.Background(), jkauthz.Subject{Principal: gary})
	raw, _ := json.Marshal([]string{gary.String()})
	granted := form.Snapshot{}.SetPath(store.KeyRetentionWriters, raw)

	if retentionGranted(ctx, form.Snapshot{}, store.KeyRetentionMode) {
		t.Fatal("granted with no writers named")
	}
	if !retentionGranted(ctx, granted, store.KeyRetentionMode) {
		t.Fatal("a named writer was refused system.retention.mode")
	}
	if retentionGranted(ctx, granted, store.KeyRetentionWriters) {
		t.Fatal("a named writer may widen the list")
	}
	if retentionGranted(ctx, granted, "system.model") {
		t.Fatal("the grant leaked past system.retention")
	}
	other := authz.WithSubject(context.Background(), jkauthz.Subject{Principal: jkauthz.Principal{Kind: "user", ID: "eve"}})
	if retentionGranted(other, granted, store.KeyRetentionMode) {
		t.Fatal("an unnamed caller was granted")
	}
	if _, held := form.OwnerHeld(store.KeyRetentionMode); !held {
		t.Fatal("system.retention.mode is not owner-held")
	}
}

// The grant is honoured where a caller's keys are judged: a principal the
// owner named sets system.retention.mode through the same check every set,
// fork and birth patch passes, and still may not widen the list.
func TestANamedWriterPassesTheCallerKeyCheck(t *testing.T) {
	hs, _, _ := demoteHandlers(t)
	gary := jkauthz.Principal{Kind: "user", ID: "gary"}
	ctx := authz.WithSubject(authz.WithDoor(context.Background(), authz.DoorWeb), jkauthz.Subject{Principal: gary})
	mode := form.Build(form.Snapshot{}, map[string]json.RawMessage{store.KeyRetentionMode: json.RawMessage(`"none"`)}, nil)
	if err := hs.h.refuseCallerKeys(ctx, lineage{}, form.Snapshot{}, &mode); err == nil {
		t.Fatal("a caller nobody named set system.retention.mode")
	}
	raw, _ := json.Marshal([]string{gary.String()})
	named := form.Snapshot{}.SetPath(store.KeyRetentionWriters, raw)
	if err := hs.h.refuseCallerKeys(ctx, lineage{}, named, &mode); err != nil {
		t.Fatalf("a named writer was refused system.retention.mode: %v", err)
	}
	widen := form.Build(named, map[string]json.RawMessage{store.KeyRetentionWriters: json.RawMessage(`["user:gary","user:eve"]`)}, nil)
	if err := hs.h.refuseCallerKeys(ctx, lineage{}, named, &widen); err == nil {
		t.Fatal("a named writer widened the list")
	}
}

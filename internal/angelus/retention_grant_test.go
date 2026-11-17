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

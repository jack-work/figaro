package angelus_test

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/workerpool"
)

// A release's purge goes through the kill, which applies the delete set
// it checked: a node added under the lease's figaro between the check of
// its kill (after the role's) and the apply refuses that purge whole. Nothing goes, the row stays
// releasing, and the release asked again takes the figaro and the node.
func TestAReleasePurgesNothingWhenItsDeleteSetChangesUnderIt(t *testing.T) {
	r := leasingRig(t)
	lease, err := r.lease(asBroker(), `{"pool":"groups","key":"chat:10"}`)
	if err != nil {
		t.Fatal(err)
	}
	var child string
	r.hs.AfterKillCheckForTest(func() {
		if ariaExists(r.a, lease.Role) {
			return
		}
		r.hs.AfterKillCheckForTest(nil)
		var err error
		child, _, err = r.backend.ForkWith(lease.Aria, 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{"mantra": json.RawMessage(`"late"`)}, nil))
		if err != nil {
			t.Error(err)
		}
	})
	if err := r.release(asBroker(), `{"pool":"groups","key":"chat:10","generation":1}`); err == nil {
		t.Fatal("a release whose delete set changed under it answered released")
	}
	if child == "" {
		t.Fatal("the kill's check was never reached")
	}
	for name, id := range map[string]string{"the lease's figaro": lease.Aria, "the node added under it": child} {
		if !ariaExists(r.a, id) {
			t.Errorf("%s went with a refused purge", name)
		}
	}
	if row, _ := r.a.Workers.Ledger.Find("groups", "chat:10", 1); row.State != workerpool.Releasing {
		t.Errorf("the row is %s, want releasing", row.State)
	}
	if err := r.release(asBroker(), `{"pool":"groups","key":"chat:10","generation":1}`); err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]string{"the lease's figaro": lease.Aria, "the node added under it": child} {
		if ariaExists(r.a, id) {
			t.Errorf("%s outlived the second release", name)
		}
	}
}

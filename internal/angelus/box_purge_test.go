package angelus_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
)

const judgePurgesInAnHour = "purge_grace = \"1h\"\n"

// A guest's chat lives in a box whose transcripts must not outlast an hour
// once reaped or killed; the daemon's week and three days stay for the rest.
func TestABoxPurgesOnItsOwnGraceAndOthersKeepTheDaemons(t *testing.T) {
	d := boxDoorRigConfigured(t, defaultPolicy, "true", judgePurgesInAnHour)
	boxed, err := d.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	outside, err := d.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{boxed.FigaroID, outside.FigaroID} {
		age(t, d.a, id, 72*time.Hour, 48*time.Hour, map[string]string{"system.keepalive": "0"})
	}
	d.a.SweepLifetimesNow()
	for _, id := range []string{boxed.FigaroID, outside.FigaroID} {
		if !reaped(t, d.a, id) {
			t.Fatalf("%s was not soft-deleted", id)
		}
	}
	for _, id := range []string{boxed.FigaroID, outside.FigaroID} {
		backdateReaped(t, d.a, id, 2*time.Hour)
	}
	d.a.SweepLifetimesNow()
	if ariaExists(d.a, boxed.FigaroID) {
		t.Error("the box aria outlived its box's purge grace")
	}
	if !reaped(t, d.a, outside.FigaroID) {
		t.Error("an aria outside the box was purged on the box's grace")
	}
}

func TestAKilledBoxAriaIsPurgedWithinItsBoxsGrace(t *testing.T) {
	d := boxDoorRigConfigured(t, defaultPolicy, "true", judgePurgesInAnHour)
	kill := func(id string, grace time.Duration) time.Duration {
		t.Helper()
		out, err := d.call(asOwner(), rpc.MethodKill, rpc.KillRequest{FigaroID: id, GraceMS: grace.Milliseconds()})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(out)
		var resp rpc.KillResponse
		_ = json.Unmarshal(raw, &resp)
		return time.Until(time.UnixMilli(resp.PurgeAt))
	}
	for name, grace := range map[string]time.Duration{"the default grace": 0, "a longer grace asked for": 30 * 24 * time.Hour} {
		boxed, err := d.create(asOwner(), "judged")
		if err != nil {
			t.Fatal(err)
		}
		if left := kill(boxed.FigaroID, grace); left > time.Hour || left < 50*time.Minute {
			t.Errorf("%s: a killed box aria purges in %v, want about an hour", name, left)
		}
	}
	outside, err := d.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	if left := kill(outside.FigaroID, 0); left < 71*time.Hour {
		t.Errorf("an aria outside the box purges in %v, want the daemon's three days", left)
	}
}

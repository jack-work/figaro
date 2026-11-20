package angelus_test

import (
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func listedRow(t *testing.T, r grantRig, id string) rpc.FigaroInfoResponse {
	t.Helper()
	out, err := r.call(asOwner(), rpc.MethodList, rpc.ListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range out.(rpc.ListResponse).Figaros {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("%s is not listed", id)
	return rpc.FigaroInfoResponse{}
}

func TestAListedLogSaysLogAndCountsItsEntriesAsTurns(t *testing.T) {
	r := logRig(t, defaultPolicy, nil)
	id := mintLog(t, r, asOwner())
	if row := listedRow(t, r, id); row.Kind != form.KindLog || row.TurnCount != 0 {
		t.Fatalf("a dormant unwritten log lists as kind %q, %d turns", row.Kind, row.TurnCount)
	}
	for _, text := range []string{"a", "b", "c"} {
		if err := appendTo(t, r, asOwner(), id, text); err != nil {
			t.Fatal(err)
		}
	}
	waitLogParts(t, r, id, 3)
	deadline := time.Now().Add(5 * time.Second)
	for {
		row := listedRow(t, r, id)
		if row.Kind != form.KindLog {
			t.Fatalf("a live log lists as kind %q", row.Kind)
		}
		if row.TurnCount == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a log of 3 entries lists %d turns", row.TurnCount)
		}
		time.Sleep(5 * time.Millisecond)
	}
	model, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	if row := listedRow(t, r, model.FigaroID); row.Kind == form.KindLog {
		t.Fatal("a figaro lists as a log")
	}
}

func listedRowEventually(t *testing.T, r grantRig, id string, turns int) rpc.FigaroInfoResponse {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		row := listedRow(t, r, id)
		if row.TurnCount == turns || time.Now().After(deadline) {
			return row
		}
		time.Sleep(5 * time.Millisecond)
	}
}

package store

import (
	"testing"

	"github.com/jack-work/figaro/internal/store/xwal"
)

func departureOf(token, to string, ids ...string) Departure {
	var c xwal.Component
	for i, id := range ids {
		n := xwal.ComponentNode{Key: "n" + id, Kind: "conversation", Trunk: id}
		if i > 0 {
			n.From = "n" + ids[0]
		}
		c.Nodes = append(c.Nodes, n)
	}
	return Departure{Token: token, To: to, Component: c, Facts: map[string]MovedFacts{ids[0]: {MintedBy: "user:1000"}}}
}

// What a node remembers about arias that left survives a restart: a pending
// departure stays pending, a final one forwards, and an aborted one forwards
// nothing.
func TestDeparturesSurviveAReopen(t *testing.T) {
	root := t.TempDir()
	m, err := OpenMoves(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []Departure{departureOf("t1", "nb", "a", "b"), departureOf("t2", "nc", "c"), departureOf("t3", "nd", "d")} {
		if err := m.Depart(d); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Settle("t1"); err != nil {
		t.Fatal(err)
	}
	if err := m.Abort("t3"); err != nil {
		t.Fatal(err)
	}
	if err := m.Depart(departureOf("t4", "nb", "a")); err == nil {
		t.Fatal("an aria departed twice")
	}

	m, err = OpenMoves(root)
	if err != nil {
		t.Fatal(err)
	}
	if ts, ok := m.Tombstone("b"); !ok || ts.To != "nb" || ts.State != MoveFinal {
		t.Fatalf("b: %+v %v", ts, ok)
	}
	if ts, ok := m.Tombstone("a"); !ok || ts.Facts.MintedBy != "user:1000" {
		t.Fatalf("a lost its facts: %+v", ts)
	}
	if ts, ok := m.Tombstone("c"); !ok || ts.State != MovePending {
		t.Fatalf("c: %+v %v", ts, ok)
	}
	if _, ok := m.Tombstone("d"); ok {
		t.Fatal("an aborted move forwards")
	}
	if err := m.Depart(departureOf("t5", "nb", "d")); err != nil {
		t.Fatalf("an aborted move holds its aria: %v", err)
	}
	if err := m.Cancel("t3"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Tombstone("d"); !ok {
		t.Fatal("cancelling an old aborted move dropped a newer departure of the same aria")
	}
}

// An aria that comes back drops its forwarding address; the rest of its old
// tree keeps forwarding.
func TestAReturningAriaIsReclaimed(t *testing.T) {
	root := t.TempDir()
	m, err := OpenMoves(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Depart(departureOf("t1", "nb", "a", "b")); err != nil {
		t.Fatal(err)
	}
	if err := m.Settle("t1"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reclaim([]string{"b"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Tombstone("b"); ok {
		t.Fatal("b came back and still forwards")
	}
	if _, ok := m.Tombstone("a"); !ok {
		t.Fatal("a stopped forwarding")
	}
	if err := m.Reclaim([]string{"a"}); err != nil {
		t.Fatal(err)
	}
	m, err = OpenMoves(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Departures()) != 0 || len(m.MatchTombstones("", -1)) != 0 {
		t.Fatalf("an emptied departure was kept: %+v", m.Departures())
	}
}

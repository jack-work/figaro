package store

import (
	"testing"

	"github.com/jack-work/figwal/xwal"
)

type tp struct {
	S string `json:"s"`
}

func openTestXwal(t *testing.T, dir string) *xwal.XWAL {
	t.Helper()
	xw, err := xwal.Open(dir, xwal.Config{
		Main: "ir",
		Channels: []xwal.ChannelSpec{
			{Name: "ir", Kind: xwal.ChannelLog},
			{Name: "translations", Kind: xwal.ChannelLog},
		},
	})
	if err != nil {
		t.Fatalf("xwal open: %v", err)
	}
	return xw
}

func TestXwalLog_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	xw := openTestXwal(t, dir)
	defer xw.Close()

	ir := newXwalLog[tp](xw, "ir", true)
	tr := newXwalLog[tp](xw, "translations", false)

	for i := 1; i <= 3; i++ {
		e, err := ir.Append(Entry[tp]{Payload: tp{S: "msg"}})
		if err != nil {
			t.Fatalf("ir append: %v", err)
		}
		if e.LT != uint64(i) || e.FigaroLT != uint64(i) {
			t.Fatalf("ir LT/FigaroLT = %d/%d, want %d", e.LT, e.FigaroLT, i)
		}
		if _, err := tr.Append(Entry[tp]{FigaroLT: e.LT, Payload: tp{S: "wire"}, Fingerprint: "anthropic/v0"}); err != nil {
			t.Fatalf("tr append: %v", err)
		}
	}

	if got := ir.Read(); len(got) != 3 || got[2].Payload.S != "msg" {
		t.Fatalf("ir.Read = %+v", got)
	}
	got, ok := tr.Lookup(2)
	if !ok || got.FigaroLT != 2 || got.Fingerprint != "anthropic/v0" || got.Payload.S != "wire" {
		t.Fatalf("tr.Lookup(2) = (%+v,%v)", got, ok)
	}
	if tail, ok := ir.PeekTail(); !ok || tail.LT != 3 {
		t.Fatalf("ir.PeekTail = (%+v,%v)", tail, ok)
	}
	if s := tr.ScanFromEnd(2); len(s) != 2 || s[0].LT != 3 || s[1].LT != 2 {
		t.Fatalf("tr.ScanFromEnd(2) = %+v", s)
	}

	// Clear wipes translations; reusable after.
	if err := tr.Clear(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, ok := tr.Lookup(2); ok {
		t.Fatal("translation survived Clear")
	}
	if _, err := tr.Append(Entry[tp]{FigaroLT: 3, Payload: tp{S: "again"}, Fingerprint: "v1"}); err != nil {
		t.Fatalf("append after clear: %v", err)
	}
	if g, ok := tr.Lookup(3); !ok || g.Fingerprint != "v1" {
		t.Fatalf("post-clear lookup = (%+v,%v)", g, ok)
	}
}

func TestXwalLog_SurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	xw := openTestXwal(t, dir)
	ir := newXwalLog[tp](xw, "ir", true)
	e, _ := ir.Append(Entry[tp]{Payload: tp{S: "persisted"}})
	tr := newXwalLog[tp](xw, "translations", false)
	tr.Append(Entry[tp]{FigaroLT: e.LT, Payload: tp{S: "w"}, Fingerprint: "fp"})
	xw.Close()

	xw2 := openTestXwal(t, dir)
	defer xw2.Close()
	tr2 := newXwalLog[tp](xw2, "translations", false)
	if g, ok := tr2.Lookup(e.LT); !ok || g.Payload.S != "w" || g.Fingerprint != "fp" {
		t.Fatalf("after reopen Lookup = (%+v,%v)", g, ok)
	}
}

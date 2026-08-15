package store

import "testing"

func TestBenchFixtureActuallyReturnsRows(t *testing.T) {
	c, lineage := benchCache(2000)
	defer c.Close()
	got, err := c.Range(lineage, 1500, 1564)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Range(1500,1564) returned %d units", len(got))
	if len(got) != 64 {
		t.Fatalf("fixture returns %d units, not 64: the benchmark measured an empty read", len(got))
	}
}

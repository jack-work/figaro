package segment

import "testing"

func TestValueHashIsValueStable(t *testing.T) {
	a, err := ValueHash([]byte(`{"a":1,"b":2}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := ValueHash([]byte(`{ "b" : 2 ,  "a" : 1 }`))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("expected equal hashes, got %s vs %s", a, b)
	}
}

func TestValueHashDiffersForDifferentValues(t *testing.T) {
	a, _ := ValueHash([]byte(`{"a":1}`))
	b, _ := ValueHash([]byte(`{"a":2}`))
	if a == b {
		t.Fatal("expected different hashes")
	}
}

func TestValueHashLengthIs16(t *testing.T) {
	h, err := ValueHash([]byte(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != hashHexLen {
		t.Fatalf("got %d, want %d", len(h), hashHexLen)
	}
}

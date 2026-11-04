package form

import (
	"encoding/json"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The memo gauge rises when an object Value decodes its members and falls back
// once that Value is collected: it measures what stays resident, not what was
// ever decoded.
func TestMemoBytesFollowsTheValueLifetime(t *testing.T) {
	before, _ := MemoBytes()
	v := NewValue(json.RawMessage(`{"a":"xxxxxxxxxxxxxxxxxxxxxxxx","b":{"c":1}}`))
	if _, ok := v.members(); !ok {
		t.Fatal("not an object")
	}
	during, _ := MemoBytes()
	if during <= before {
		t.Fatalf("memo gauge did not rise: before %d, during %d", before, during)
	}
	runtime.KeepAlive(v)
	v = Value{}
	deadline := time.Now().Add(5 * time.Second)
	for {
		runtime.GC()
		after, _ := MemoBytes()
		if after <= before {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("memo gauge never fell back: before %d, after %d", before, after)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCanonBytesCountEqualityMemo(t *testing.T) {
	_, before := MemoBytes()
	a := NewValue(json.RawMessage(`{"b":1,"a":2}`))
	b := NewValue(json.RawMessage(`{"a":2,"b":1}`))
	if !a.Equal(b) {
		t.Fatal("semantic equality broke")
	}
	_, during := MemoBytes()
	if during <= before {
		t.Fatalf("canonical gauge did not rise: before %d, during %d", before, during)
	}
	runtime.KeepAlive(a)
	runtime.KeepAlive(b)
}

// ApproxBytes must grow with the data it holds, or doctor mem cannot point at
// the form that holds the heap.
func TestApproxBytesGrowsWithTheData(t *testing.T) {
	small := FromMap(map[string]json.RawMessage{"a": json.RawMessage(`1`)})
	big := FromMap(map[string]json.RawMessage{
		"a":        json.RawMessage(`1`),
		"skills.x": json.RawMessage(`"` + string(make([]byte, 4096)) + `"`),
	})
	if small.ApproxBytes() <= 0 || big.ApproxBytes() < small.ApproxBytes()+4096 {
		t.Fatalf("snapshot sizes: small %d, big %d", small.ApproxBytes(), big.ApproxBytes())
	}
	p := big.Diff(small)
	if p.ApproxBytes() < 4096 {
		t.Fatalf("patch carrying 4 KB sized %d", p.ApproxBytes())
	}
	if (Patch{}).ApproxBytes() != 0 {
		t.Fatal("identity patch should size zero")
	}
}

// Issue #59: comparing two big skill bodies used to keep a canonical copy of
// each for the life of the value. Strings compare by decoding, and anything
// past the memo bound is canonicalized and dropped, so equality leaves nothing
// resident.
func TestEqualOnBigValuesKeepsNoCanonicalCopy(t *testing.T) {
	_, before := MemoBytes()
	body := strings.Repeat("x", 64<<10)
	a := NewValue(json.RawMessage(strconv.Quote(body)))
	b := NewValue(json.RawMessage(strconv.Quote(body + "y")))
	if a.Equal(b) {
		t.Fatal("different strings compared equal")
	}
	obj := `{"k":"` + body + `","j":1}`
	c := NewValue(json.RawMessage(obj))
	d := NewValue(json.RawMessage(`{"j":1,"k":"` + body + `"}`))
	if !c.Equal(d) {
		t.Fatal("key order broke semantic equality on a big object")
	}
	_, after := MemoBytes()
	if after != before {
		t.Fatalf("canonical gauge moved by %d for big values", after-before)
	}
	runtime.KeepAlive([]Value{a, b, c, d})
}

func TestStringEqualityIsSemantic(t *testing.T) {
	cases := []struct {
		a, b string
		eq   bool
	}{
		{`"A"`, `"\u0041"`, true},
		{` "a"`, `"a"`, true},
		{`"a"`, `"b"`, false},
		{`"a"`, `1`, false},
		{`"a`, `"a"`, false},
	}
	for _, c := range cases {
		if got := NewValue(json.RawMessage(c.a)).Equal(NewValue(json.RawMessage(c.b))); got != c.eq {
			t.Errorf("%s == %s: got %v, want %v", c.a, c.b, got, c.eq)
		}
	}
}

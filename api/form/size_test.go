package form

import (
	"encoding/json"
	"runtime"
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

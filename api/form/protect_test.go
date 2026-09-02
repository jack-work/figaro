package form

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCheckWritable(t *testing.T) {
	sysKey := ""
	for _, k := range WellKnownKeys() {
		if k.Mode == KeySystemManaged {
			sysKey = k.Key
			break
		}
	}
	if sysKey == "" {
		t.Skip("no system-managed key in the catalog")
	}

	p := Patch{Set: map[string]json.RawMessage{sysKey: json.RawMessage(`"x"`)}}
	if err := CheckWritable(p, false); err == nil {
		t.Fatalf("%s is system-managed and was accepted from an unprivileged caller", sysKey)
	} else if !strings.Contains(err.Error(), sysKey) {
		t.Fatalf("the refusal must name the key: %v", err)
	}
	if err := CheckWritable(p, true); err != nil {
		t.Fatalf("a privileged caller must be able to write it: %v", err)
	}

	rm := Patch{Remove: []string{sysKey}}
	if err := CheckWritable(rm, false); err == nil {
		t.Fatal("removing a system-managed key must be refused too")
	}

	ok := Patch{Set: map[string]json.RawMessage{"mantra": json.RawMessage(`"hello"`)}}
	if err := CheckWritable(ok, false); err != nil {
		t.Fatalf("an ordinary key must pass: %v", err)
	}

	cwd := Patch{Set: map[string]json.RawMessage{"system.cwd": json.RawMessage(`"/tmp"`)}}
	if err := CheckWritable(cwd, false); err != nil {
		t.Fatalf("system.cwd is client state (figaro cd): %v", err)
	}

	// The rule applies to what a patch WRITES. A patch touching nothing
	// protected is fine no matter what the board already holds.
	if err := CheckWritable(Patch{}, false); err != nil {
		t.Fatalf("an empty patch must pass: %v", err)
	}
}

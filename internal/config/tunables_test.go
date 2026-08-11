package config

import (
	"strings"
	"testing"
)

// An absent [store] table must give exactly today's geometry: the knob is
// new, the behavior of every store already on disk is not.
func TestStoreSegmentSizeDefault(t *testing.T) {
	l, err := loadWith(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := l.SegmentSize(); got != defaultSegmentSize {
		t.Errorf("SegmentSize = %d, want %d", got, defaultSegmentSize)
	}
	var noConfig *Loaded // nil-safe: a store opened without config, same policy
	if got := noConfig.SegmentSize(); got != defaultSegmentSize {
		t.Errorf("nil SegmentSize = %d, want %d", got, defaultSegmentSize)
	}
}

func TestStoreSegmentSizeExplicit(t *testing.T) {
	l, err := loadWith(t, "[store]\nsegment_size = 8388608\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := l.SegmentSize(); got != 8388608 {
		t.Errorf("SegmentSize = %d, want 8388608", got)
	}
}

// A segment below the floor is not a slow store, it is an append that fails
// the first time an image is inlined: so the refusal must say so.
func TestStoreSegmentSizeFloor(t *testing.T) {
	for _, tomlText := range []string{"[store]\nsegment_size = 4096\n", "[store]\nsegment_size = 0\n"} {
		_, err := loadWith(t, tomlText)
		if err == nil {
			t.Fatalf("%q: expected a validation error, got nil", tomlText)
		}
		if !strings.Contains(err.Error(), "ONE whole record") || !strings.Contains(err.Error(), "base64") {
			t.Errorf("%q: error does not explain the record-size constraint: %v", tomlText, err)
		}
	}
}

// The live-emit window is smoothness policy, not correctness: 0 is legal
// (emit every chunk), negative is not (it would read as "never throttle",
// which 0 already means).
func TestStreamEmitInterval(t *testing.T) {
	l, err := loadWith(t, "")
	if err != nil {
		t.Fatal(err)
	}
	var noConfig *Loaded
	if l.StreamEmitIntervalMs() != 90 || noConfig.StreamEmitIntervalMs() != 90 {
		t.Errorf("default emit interval = %d/%d, want 90", l.StreamEmitIntervalMs(), noConfig.StreamEmitIntervalMs())
	}
	if l, err := loadWith(t, "[cli]\nstream_emit_interval_ms = 0\n"); err != nil || l.StreamEmitIntervalMs() != 0 {
		t.Errorf("explicit 0: got %v, %v", l, err)
	}
	if _, err := loadWith(t, "[cli]\nstream_emit_interval_ms = -1\n"); err == nil {
		t.Error("negative interval: expected a validation error, got nil")
	}
}

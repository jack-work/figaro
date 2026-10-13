package cli

import (
	"errors"
	"testing"
	"time"
)

// The test uses its own limit so the suite does not wait for the production five
// seconds.
func TestBoundedKeyringGivesUp(t *testing.T) {
	start := time.Now()
	_, err := boundedKeyringWithin(20*time.Millisecond, func() (string, error) {
		select {}
	})
	if !errors.Is(err, errKeyringTimeout) {
		t.Fatalf("err = %v, want errKeyringTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("waited %s, should have given up at 20ms", elapsed)
	}
}

func TestBoundedKeyringPassesTheAnswerThrough(t *testing.T) {
	v, err := boundedKeyringWithin(time.Second, func() (string, error) { return "hunter2", nil })
	if err != nil || v != "hunter2" {
		t.Fatalf("got (%q, %v)", v, err)
	}
	want := errors.New("no such collection")
	if _, err := boundedKeyringWithin(time.Second, func() (string, error) { return "", want }); !errors.Is(err, want) {
		t.Fatalf("err = %v, want it passed through", err)
	}
}

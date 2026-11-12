package boot

import (
	"errors"
	"os"
	"strconv"
	"testing"
)

func gateFrom(t *testing.T, write []byte) *HushGate {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	t.Setenv(HushGateEnv, strconv.Itoa(int(r.Fd())))
	if write != nil {
		w.Write(write)
	}
	w.Close()
	g := TakeHushGate()
	t.Cleanup(func() { daemonHushGate = nil })
	if g == nil {
		t.Fatal("TakeHushGate returned nil with the variable set")
	}
	if _, ok := os.LookupEnv(HushGateEnv); ok {
		t.Fatalf("%s is still set: the daemon's children would inherit a stale gate", HushGateEnv)
	}
	return g
}

func TestHushGateOpensOnlyOnTheOpenByte(t *testing.T) {
	if !gateFrom(t, []byte{HushGateOpen}).Await() {
		t.Fatal("the open byte did not open the gate")
	}
}

func TestHushGateStaysShutWhenTheClientClosesWithoutWriting(t *testing.T) {
	if gateFrom(t, nil).Await() {
		t.Fatal("a closed pipe opened the gate: a hush failure would leave the daemon running")
	}
}

func TestHushGateStaysShutOnAnyOtherByte(t *testing.T) {
	if gateFrom(t, []byte{'n'}).Await() {
		t.Fatal("a stray byte opened the gate")
	}
}

func TestNoHushGateMeansNoWait(t *testing.T) {
	os.Unsetenv(HushGateEnv)
	g := TakeHushGate()
	t.Cleanup(func() { daemonHushGate = nil })
	if g != nil {
		t.Fatal("a gate appeared from nowhere")
	}
	if !g.Await() {
		t.Fatal("a daemon started without a gate must not wait for one")
	}
}

func TestTheDaemonAsksNoHushBeforeTheGateOpens(t *testing.T) {
	t.Setenv("FIGARO_HUSH_APP", "figaro-test-hushgate-unit")
	t.Setenv("FIGARO_HUSH_DIR", t.TempDir())
	resetHush()
	t.Cleanup(resetHush)
	gateFrom(t, nil)
	t.Cleanup(func() { daemonHushGate = nil })
	if _, err := strategyHush(); err == nil || !errors.Is(err, ErrHushGateShut) {
		t.Fatalf("strategyHush behind a shut gate: %v; the daemon would race the client's agent", err)
	}
}

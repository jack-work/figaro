package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

type kindedAria struct {
	log  bool
	sent *rpc.QuaRequest
}

func (k kindedAria) ID() string { return "a1" }

func (k kindedAria) QuaWith(_ context.Context, req rpc.QuaRequest) (int, bool, error) {
	*k.sent = req
	return 0, false, nil
}

func (k kindedAria) Form(context.Context) (*rpc.FormResponse, error) {
	var snap form.Snapshot
	if k.log {
		snap = form.Snapshot{}.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{form.KindKey: json.RawMessage(`"log"`)}, nil))
	}
	return &rpc.FormResponse{Snapshot: snap}, nil
}

func seeDaemon(t *testing.T, caps ...rpc.Capability) {
	t.Helper()
	resetDaemonSeen(t)
	recordDaemon("the daemon", daemonIdentity{build: "old", release: "0.77.4", protocol: 1, caps: caps}, "new")
}

func sendExact(log bool, text string) (rpc.QuaRequest, error) {
	var got rpc.QuaRequest
	_, _, err := quaWith(context.Background(), kindedAria{log: log, sent: &got}, rpc.QuaRequest{Text: text, Exact: true})
	return got, err
}

func TestAnOlderDaemonTakesStdinToAFigaroTrimmedAsBefore(t *testing.T) {
	seeDaemon(t)
	got, err := sendExact(false, "question\n\n")
	if err != nil || got.Text != "question" || got.Exact {
		t.Fatalf("stdin to a figaro on an older daemon = %+v, %v; want trimmed, not exact", got, err)
	}
	if _, err := sendExact(false, " \n"); err == nil || !strings.Contains(err.Error(), "held no prompt") {
		t.Fatalf("blank stdin to a figaro on an older daemon = %v, want refused before sending", err)
	}
}

func TestAnOlderDaemonRefusesStdinToALogByName(t *testing.T) {
	seeDaemon(t)
	if _, err := sendExact(true, "entry\n"); err == nil || !strings.Contains(err.Error(), string(rpc.CapLogExact)) {
		t.Fatalf("stdin to a log on an older daemon = %v, want refused for lacking %s", err, rpc.CapLogExact)
	}
}

func TestALogEntryMustBeValidUTF8AndAtMost15MiB(t *testing.T) {
	seeDaemon(t, rpc.CapLogExact)
	if _, err := sendExact(true, "bad\xff\xfe"); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("invalid UTF-8 to a log = %v, want refused, never rewritten", err)
	}
	if _, err := sendExact(true, strings.Repeat("x", maxLogEntryBytes+1)); err == nil || !strings.Contains(err.Error(), "15 MiB") {
		t.Fatalf("an entry over 15 MiB = %v, want refused", err)
	}
	if _, err := sendExact(true, strings.Repeat("<", 3_000_000)); err == nil || !strings.Contains(err.Error(), "15 MiB") {
		t.Fatalf("3 MB of '<', 18 MB escaped on the wire = %v, want refused", err)
	}
	if _, err := sendExact(true, strings.Repeat("<", 2_000_000)); err != nil {
		t.Fatalf("2 MB of '<' fits escaped: %v", err)
	}
	if got, err := sendExact(true, strings.Repeat("x", maxLogEntryBytes-2)); err != nil || len(got.Text) != maxLogEntryBytes-2 || !got.Exact {
		t.Fatalf("an entry of exactly 15 MiB on the wire: %v", err)
	}
	if got, err := sendExact(false, "bad\xff\n\n"); err != nil || !got.Exact {
		t.Fatalf("invalid UTF-8 to a figaro = %+v, %v: a figaro's prompt is the daemon's call", got, err)
	}
}

func TestACurrentDaemonGetsStdinExactForAnyTarget(t *testing.T) {
	seeDaemon(t, rpc.CapLogExact)
	for _, log := range []bool{false, true} {
		if got, err := sendExact(log, "q\n\n"); err != nil || got.Text != "q\n\n" || !got.Exact {
			t.Fatalf("log=%v: %+v, %v", log, got, err)
		}
	}
}

func TestABlankStdinIsRefusedBeforeMintingAFigaroButNotALog(t *testing.T) {
	plain, err := parseDress("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	plain.exact = true
	if !blankStdin(plain, " \n") || plain.mintsLog() {
		t.Fatal("a blank stdin minting a figaro is not refused")
	}
	logged, err := parseDress("", "system.kind=log", "")
	if err != nil {
		t.Fatal(err)
	}
	logged.exact = true
	if !logged.mintsLog() {
		t.Fatal("system.kind=log does not mint a log")
	}
	if blankStdin(plain, "q") {
		t.Fatal("a prompt counted as blank")
	}
}

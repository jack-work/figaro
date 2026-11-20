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

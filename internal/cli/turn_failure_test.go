package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

// gluck, 0.57.1: three turns failed on a rate limit and neither status nor
// the bar of a pager attached afterwards said so. The daemon leaves the
// reason on the aria's form; every view reads it there.

func failedForm(turn uint64, reason string) form.Snapshot {
	r, _ := json.Marshal(reason)
	n, _ := json.Marshal(turn)
	return form.Snapshot{}.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		form.FailureReasonKey: r, form.FailureTurnKey: n,
	}, nil))
}

const rateLimitReason = "anthropic: 7-day rate limit reached; resets Tue 10 Nov 03:00 EST"

func TestStatusNamesTheLastTurnsFailure(t *testing.T) {
	var out bytes.Buffer
	printStatusPanel(&out, &rpc.FigaroInfoResponse{ID: "dac6cb6d", State: "idle"}, false, 0, failedForm(7, rateLimitReason))
	if !strings.Contains(out.String(), "last-turn:") || !strings.Contains(out.String(), "turn 7 failed: "+rateLimitReason) {
		t.Fatalf("status does not name the failed turn:\n%s", out.String())
	}
	out.Reset()
	printStatusPanel(&out, &rpc.FigaroInfoResponse{ID: "dac6cb6d", State: "idle"}, false, 0, form.Snapshot{})
	if strings.Contains(out.String(), "last-turn") {
		t.Fatalf("status names a failure on an aria with none:\n%s", out.String())
	}
}

func TestAPagerAttachedAfterAFailureShowsItOnTheBar(t *testing.T) {
	in, lt := navInput(t, &countingWriter{}, true)
	in.bound = &formMirror{}
	in.bound.reset(failedForm(7, rateLimitReason), 1, 3)
	in.boundFormMoved()
	if got := lt.status.failure(); got != "error: "+rateLimitReason {
		t.Fatalf("the bar's error reads %q", got)
	}
}

// The reason is the upstream's words: on a terminal they must be text, never
// escapes.
func TestTheFailureReasonReachesTheTerminalAsText(t *testing.T) {
	hostile := "anthropic: \x1b]0;owned\x07\x1b[2Jboom\nsecond line"
	var out bytes.Buffer
	printStatusPanel(&out, &rpc.FigaroInfoResponse{ID: "dac6cb6d", State: "idle"}, false, 0, failedForm(7, hostile))
	if strings.ContainsAny(out.String(), "\x1b\x07") {
		t.Fatalf("status wrote the reason's escapes to the terminal: %q", out.String())
	}
	f, _ := failureOf(failedForm(7, hostile))
	if strings.ContainsAny(f.line(), "\x1b\x07\n") {
		t.Fatalf("show would write the reason's escapes to the terminal: %q", f.line())
	}
	in, lt := navInput(t, &countingWriter{}, true)
	in.bound = &formMirror{}
	in.bound.reset(failedForm(7, hostile), 1, 3)
	in.boundFormMoved()
	if strings.ContainsAny(lt.status.failure(), "\x1b\x07\n") {
		t.Fatalf("the bar carries the reason's escapes: %q", lt.status.failure())
	}
}

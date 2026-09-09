package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

// A stream's story is not a status code. The detail line is where an operator
// reads whether the socket delivered anything and when.
func TestStreamLineNamesTheShapeOfTheStream(t *testing.T) {
	line := streamLine(&rpc.StreamStats{
		Status: "incomplete", ErrClass: "unexpected_eof",
		Events: 134, UnknownEvents: 2, RespBytes: 49356,
		FirstEventMS: 210, FirstToolMS: 1200, FirstArgumentMS: 1300,
		ArgumentDeltas: 12, ArgumentBytes: 3400, ArgumentUnmatched: 1,
	})
	for _, want := range []string{
		"events=134", "unknown=2", "resp=48.2KiB", "first=210ms",
		"tool=1.2s", "arg=1.3s", "argdeltas=12/3.3KiB", "unmatched=1",
		"err=unexpected_eof",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("stream line missing %q:\n%s", want, line)
		}
	}
}

// A plain HTTP round earns no second line, and a clean stream stays quiet
// about the fields that only matter when something went wrong.
func TestStreamLineIsQuietWhenThereIsNothingToSay(t *testing.T) {
	if got := streamLine(nil); got != "" {
		t.Errorf("nil stream produced a line: %q", got)
	}
	line := streamLine(&rpc.StreamStats{Status: "ok", Events: 9, RespBytes: 100, FirstEventMS: 12})
	for _, unwanted := range []string{"unmatched", "unknown", "err=", "argdeltas"} {
		if strings.Contains(line, unwanted) {
			t.Errorf("clean stream line mentions %q:\n%s", unwanted, line)
		}
	}
}

// The verdict, in words: a socket that ended early, tool arguments that
// matched no call, and a schema that moved are three different incidents.
func TestStreamTroubleSummaryNamesEachFailure(t *testing.T) {
	rounds := []rpc.ProviderRound{
		{Seq: 1, Aria: "94f0752b", Method: "WS", DurationMS: 4000,
			Stream: &rpc.StreamStats{Status: "incomplete", ErrClass: "unexpected_eof", Events: 40}},
		{Seq: 2, Aria: "94f0752b", Method: "WS", DurationMS: 900,
			Stream: &rpc.StreamStats{Status: "ok", Events: 12, ArgumentUnmatched: 3, UnknownEvents: 2}},
	}
	out := captureStdout(t, func() { summarizeProviderTrouble(rounds) })
	for _, want := range []string{
		"1 stream(s) ended without completing",
		"unexpected_eof",
		"3 tool-argument delta(s) arrived for no open call",
		"2 event(s) the parser did not recognize",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
}

// An open socket that has said nothing is the incident people actually open
// the doctor for.
func TestStreamTroubleSummaryNamesASilentOpenStream(t *testing.T) {
	rounds := []rpc.ProviderRound{
		{Seq: 1, Aria: "94f0752b", Method: "WS", InFlight: true,
			Stream: &rpc.StreamStats{Events: 0}},
	}
	out := captureStdout(t, func() { summarizeProviderTrouble(rounds) })
	if !strings.Contains(out, "delivered no events yet") {
		t.Errorf("a silent open stream must be named:\n%s", out)
	}
}

// A healthy stream says nothing, same rule as a healthy round-trip.
func TestStreamTroubleSummarySilentWhenHealthy(t *testing.T) {
	rounds := []rpc.ProviderRound{
		{Seq: 1, Method: "WS", DurationMS: 900,
			Stream: &rpc.StreamStats{Status: "ok", Events: 40, ArgumentDeltas: 6}},
	}
	if out := captureStdout(t, func() { summarizeProviderTrouble(rounds) }); out != "" {
		t.Errorf("expected silence for a healthy stream, got:\n%s", out)
	}
}

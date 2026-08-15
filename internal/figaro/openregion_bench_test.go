package figaro

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/toolout"
	"github.com/jack-work/figaro/internal/uiir"
)

// The subject: composeTurn, the per-frame recomposition of the OPEN region.
//
// The measurement these benchmarks exist to take is stated in
// ~/notes/layered-cache-design.md and plans/storm-triage.md (S6). Nothing
// here asserts a fix; they establish the shape of the cost before one.

// openRegionRounds builds an open region of r completed tool rounds. Each
// round is two messages, as the agent appends them: an assistant message
// carrying one tool_invoke, then a tool_result message carrying outLines
// lines of output. lastRunning leaves the final round's result absent, which
// is the state every frame of a live turn is actually composed in.
func openRegionRounds(startLT uint64, r, outLines int, lastRunning bool) []store.Entry[message.Message] {
	line := strings.Repeat("x", 79)
	body := strings.Repeat(line+"\n", outLines)
	var rows []store.Entry[message.Message]
	lt := startLT
	for i := 0; i < r; i++ {
		id := fmt.Sprintf("call_%d", i)
		lt++
		rows = append(rows, store.Entry[message.Message]{
			LT: lt, FigaroLT: lt,
			Payload: message.Message{
				Role: message.RoleOutput,
				Content: []message.Content{
					{Type: message.ContentProse, Text: "running the tool now"},
					{Type: message.ContentToolInvoke, ToolCallID: id, ToolName: "bash",
						Arguments: map[string]any{"command": "ls -la /some/path"}},
				},
			},
		})
		if lastRunning && i == r-1 {
			break
		}
		lt++
		rows = append(rows, store.Entry[message.Message]{
			LT: lt, FigaroLT: lt,
			Payload: message.Message{
				Role: message.RoleToolResult,
				Content: []message.Content{
					{Type: message.ContentToolResult, ToolCallID: id, Text: body},
				},
			},
		})
	}
	return rows
}

func openRegionAgent(r, outLines int, lastRunning bool) *Agent {
	// One prompt message precedes the turn; turnStartLT points at it, so the
	// open region is exactly what openRegionRounds appended after it.
	rows := []store.Entry[message.Message]{{
		LT: 1, FigaroLT: 1,
		Payload: message.Message{Role: message.RoleInput,
			Content: []message.Content{message.TextContent("do the thing")}},
	}}
	rows = append(rows, openRegionRounds(1, r, outLines, lastRunning)...)
	p := uiir.New(nil)
	p.ResetTools()
	return &Agent{
		figLog:      &benchmarkLog{rows: rows},
		turnStartLT: 1,
		gov:         toolout.New(liveOutputTail),
		argPartials: map[string]string{},
		proj:        p,
	}
}

// BenchmarkComposeTurnOpenRegion measures ONE frame against an open region of
// r completed tool rounds. The claim under test is that the per-frame cost is
// O(open region) rather than O(what changed since the last frame).
func BenchmarkComposeTurnOpenRegion(b *testing.B) {
	for _, r := range []int{1, 4, 16, 64} {
		b.Run(fmt.Sprintf("rounds=%d/outlines=200", r), func(b *testing.B) {
			a := openRegionAgent(r, 200, true)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				pre, suf, _ := a.composeTurn(nil)
				nodes := append(append([]livedoc.Node(nil), pre...), suf...)
				runtime.KeepAlive(nodes)
			}
		})
	}
}

// BenchmarkComposeTurnBigDump isolates tailBound's exposure to the FULL
// result text: compose clamps the rendered output to the last 200 source
// lines, but it reaches that clamp by splitting every line the tool ever
// produced, on every frame, for the remainder of the turn.
func BenchmarkComposeTurnBigDump(b *testing.B) {
	for _, lines := range []int{200, 2_000, 20_000} {
		b.Run(fmt.Sprintf("rounds=4/outlines=%d", lines), func(b *testing.B) {
			a := openRegionAgent(4, lines, true)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				pre, suf, _ := a.composeTurn(nil)
				nodes := append(append([]livedoc.Node(nil), pre...), suf...)
				runtime.KeepAlive(nodes)
			}
		})
	}
}

// BenchmarkOpenTurnLifetime is the number that matches the heap profile: a
// whole agentic turn, framesPerRound frames emitted while each round runs.
// Per-op cost here is the cost of the TURN, and it grows quadratically in the
// number of rounds because every frame recomposes every round before it.
func BenchmarkOpenTurnLifetime(b *testing.B) {
	const framesPerRound = 11 // one second of streaming at the default emit interval
	for _, r := range []int{4, 16, 64} {
		b.Run(fmt.Sprintf("rounds=%d", r), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				a := openRegionAgent(0, 200, false)
				log := a.figLog.(*benchmarkLog)
				for round := 1; round <= r; round++ {
					log.rows = append(log.rows[:1],
						openRegionRounds(1, round, 200, true)...)
					for f := 0; f < framesPerRound; f++ {
						pre, suf, _ := a.composeTurn(nil)
				nodes := append(append([]livedoc.Node(nil), pre...), suf...)
				runtime.KeepAlive(nodes)
					}
					log.rows = append(log.rows[:1],
						openRegionRounds(1, round, 200, false)...)
					pre, suf, _ := a.composeTurn(nil)
				nodes := append(append([]livedoc.Node(nil), pre...), suf...)
				runtime.KeepAlive(nodes)
				}
			}
		})
	}
}

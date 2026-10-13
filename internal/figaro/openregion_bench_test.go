package figaro

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/toolout"
	"github.com/jack-work/figaro/internal/uiir"
)

// composeTurn's per-frame recomposition of the open region.

// openRegionRounds builds r completed tool rounds; lastRunning leaves the
// final result absent, the state every live frame is composed in.
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
	// turnStartLT points at the prompt, so the open region is what was appended after it.
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

// BenchmarkComposeTurnOpenRegion measures one frame against r completed rounds.
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

// BenchmarkComposeTurnBigDump isolates tailBound's exposure to the full result text.
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

// BenchmarkOpenTurnLifetime prices a whole turn: framesPerRound frames per round.
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

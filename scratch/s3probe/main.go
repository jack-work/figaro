// Command s3probe is a HARNESS: it asks whether composed UI IR ALIASES the
// decoded fig-IR strings it was projected from, and by how much it amplifies.
//
// The suspicion (S3) is that a resident composed turn pins the decoded record
// it came from, so trimming the IR window frees nothing. Aliasing in Go is
// total: a string header sharing a backing array keeps the WHOLE array alive,
// so a 200-line preview sliced out of a 5000-line tool result would pin all
// 5000 lines.
//
//	go run ./scratch/s3probe -msgs 200 -lines 5000
package main

import (
	"flag"
	"fmt"
	"runtime"
	"strings"

	"github.com/jack-work/figaro/internal/compose"
	"github.com/jack-work/figaro/internal/message"
)

func mib(b uint64) float64 { return float64(b) / (1 << 20) }

func heapNow() uint64 {
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}

func bigOutput(lines, i int) string {
	var b strings.Builder
	for l := 0; l < lines; l++ {
		fmt.Fprintf(&b, "msg %d line %d: the quick brown fox jumps over the lazy dog\n", i, l)
	}
	return b.String()
}

func main() {
	msgs := flag.Int("msgs", 200, "tool round-trips to build")
	lines := flag.Int("lines", 5000, "lines of tool output per round (compose caps at 200)")
	flag.Parse()

	base := heapNow()

	// The decoded fig IR: an assistant tool_invoke plus its tool_result, the
	// shape a bash-heavy turn actually has.
	var ir []message.Message
	for i := 0; i < *msgs; i++ {
		id := fmt.Sprintf("call_%d", i)
		ir = append(ir, message.Message{
			Role: message.RoleOutput, LogicalTime: uint64(2*i + 1),
			Content: []message.Content{{
				Type: message.ContentToolInvoke, ToolCallID: id, ToolName: "bash",
				Arguments: map[string]interface{}{"command": "seq 1 100000"},
			}},
		})
		ir = append(ir, message.Message{
			Role: message.RoleInput, LogicalTime: uint64(2*i + 2),
			Content: []message.Content{{
				Type: message.ContentToolResult, ToolCallID: id, Text: bigOutput(*lines, i),
			}},
		})
	}
	afterIR := heapNow()

	nodes := compose.Nodes(ir, nil, nil)
	afterCompose := heapNow()

	// Now trim the IR window: drop every decoded record. If the composed nodes
	// alias them, nothing comes back.
	ir = nil
	runtime.KeepAlive(nodes)
	afterTrim := heapNow()

	composedBytes := 0
	for _, n := range nodes {
		composedBytes += len(n.Markdown) + len(n.Output) + len(n.Input)
	}

	fmt.Printf("decoded fig IR        %7.2f MiB\n", mib(afterIR-base))
	fmt.Printf("+ composed UI IR      %7.2f MiB  (nodes report %.2f MiB of text)\n",
		float64(int64(afterCompose)-int64(afterIR))/(1<<20), float64(composedBytes)/(1<<20))
	fmt.Printf("after dropping the IR %7.2f MiB retained by the nodes alone\n", mib(afterTrim-base))
	fmt.Printf("\namplification: %.2fx the text the nodes actually carry\n",
		float64(afterTrim-base)/float64(composedBytes))
	runtime.KeepAlive(nodes)
}

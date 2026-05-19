package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/largo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/pacer"
	"github.com/jack-work/figaro/internal/rpc"
)

// newTestRenderer constructs a streamRenderer wired to a safeBuf.
// Returns the renderer plus the buffer so tests can inspect the
// rendered output.
func newTestRenderer(t *testing.T) (*streamRenderer, *safeBuf) {
	t.Helper()
	out := &safeBuf{}
	sw, err := largo.NewWriter(out, largo.Options{})
	require.NoError(t, err)
	pace := pacer.New(sw, pacer.Options{})
	r := newStreamRenderer(context.Background(), sw, pace)
	t.Cleanup(func() {
		pace.Close()
	})
	return r, out
}

// notify is a tiny helper to construct + dispatch a wire notification.
func notify(t *testing.T, r *streamRenderer, method string, params interface{}) {
	t.Helper()
	b, err := json.Marshal(params)
	require.NoError(t, err)
	r.Handle(method, b)
}

// TestStreamRenderer_TwentyToolBatch is the regression test for the
// scenario that broke previously: twenty parallel reads, output
// arriving from the dispatcher before per-tool tool_start has fired,
// and tool_end events ticking openInvokes back to zero between
// invokes. The renderer must:
//
//   - open a solo for tool 1, upgrade to batch on tool 2
//   - extend the batch with rows for tools 3..20 (no orphan solos)
//   - route every chunk of tool_output by tool_call_id (no raw dump)
//   - keep the batch open until BOTH message_end AND all tool_ends
//     have arrived (no premature finalize)
func TestStreamRenderer_TwentyToolBatch(t *testing.T) {
	r, out := newTestRenderer(t)

	const n = 20
	const fileBody = "FILECONTENT_LINE_X"

	// Phase 1: the model streams 20 tool_use blocks, all "read" with
	// the same path arg. We send for each: invoke_start, one delta,
	// invoke_ready.
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("tc_%02d", i)
		notify(t, r, rpc.MethodToolInvokeStart, rpc.ToolInvokeStartParams{
			ToolCallID: id, ToolName: "read",
		})
		notify(t, r, rpc.MethodToolInvokeDelta, rpc.ToolInvokeDeltaParams{
			ToolCallID: id, PartialJSON: `{"path":"/tmp/x.md"}`,
		})
		notify(t, r, rpc.MethodToolInvokeReady, rpc.ToolInvokeReadyParams{
			ToolCallID: id, ToolName: "read",
			Arguments: map[string]interface{}{"path": "/tmp/x.md"},
		})
	}

	// Phase 2: dispatcher fires tool_start + tool_output (one chunk
	// per tool) + tool_end for each tool, all BEFORE message_end.
	// This mirrors the speculative-dispatch reality with Slice A.
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("tc_%02d", i)
		notify(t, r, rpc.MethodToolStart, rpc.ToolStartParams{
			ToolCallID: id, ToolName: "read",
			Arguments: map[string]interface{}{"path": "/tmp/x.md"},
		})
		notify(t, r, rpc.MethodToolOutput, rpc.ToolOutputParams{
			ToolCallID: id, ToolName: "read", Chunk: fileBody,
		})
		notify(t, r, rpc.MethodToolEnd, rpc.ToolEndParams{
			ToolCallID: id, ToolName: "read", Result: fileBody,
		})
	}

	// Phase 3: model finishes. message_end + message + done.
	notify(t, r, rpc.MethodMessageEnd, rpc.MessageEndParams{
		StopReason: "tool_invoke",
	})
	notify(t, r, rpc.MethodMessage, rpc.MessageParams{})
	notify(t, r, rpc.MethodDone, rpc.DoneParams{Reason: "stop"})

	<-r.Done()

	rendered := stripANSIRendered(out.String())
	t.Logf("rendered (first 600 chars):\n%s", rendered[:min(600, len(rendered))])

	// The batch header must reflect the full count, not 2.
	assert.NotContains(t, rendered, "batch (2)",
		"batch header stuck at (2) — third+ tools weren't appended")
	// We do want it to mention the full size somewhere.
	assert.Contains(t, rendered, "(20)",
		"batch header should reflect the actual tool count (20)")

	// FILECONTENT should not appear as raw dump (it should be
	// buffered inside the row, only visible on error or omitted on
	// success). We assert it's NOT plastered as bare lines outside
	// any batch frame. The batch's success path collapses output;
	// we'll check no FILECONTENT lines bleed above the batch frame.
	idx := strings.Index(rendered, "batch")
	if idx > 0 {
		preBatch := rendered[:idx]
		assert.NotContains(t, preBatch, fileBody,
			"tool_output leaked into the pre-batch area; should have been buffered/routed")
	}
}

// TestStreamRenderer_PrematureFinalize is the specific test for the
// openTools-hits-zero-too-early bug. With tool 1 reaching tool_end
// before tool 2's tool_start (possible under speculative dispatch),
// the batch must NOT finalize. Only message_end + all-tools-ended
// triggers the finalize.
func TestStreamRenderer_PrematureFinalize(t *testing.T) {
	r, _ := newTestRenderer(t)

	// Two invokes authored.
	notify(t, r, rpc.MethodToolInvokeStart, rpc.ToolInvokeStartParams{
		ToolCallID: "tc_1", ToolName: "read",
	})
	notify(t, r, rpc.MethodToolInvokeStart, rpc.ToolInvokeStartParams{
		ToolCallID: "tc_2", ToolName: "read",
	})

	// Tool 1 fully runs (start+end) before tool 2 even starts.
	// Pre-fix this would have nil'd the batch.
	notify(t, r, rpc.MethodToolStart, rpc.ToolStartParams{
		ToolCallID: "tc_1", ToolName: "read",
	})
	notify(t, r, rpc.MethodToolEnd, rpc.ToolEndParams{
		ToolCallID: "tc_1", ToolName: "read", Result: "ok",
	})

	assert.NotNil(t, r.batch,
		"batch finalized after tool 1's end while tool 2 still pending — premature finalize bug")
	assert.False(t, r.roundCommitted,
		"round must not be committed before message_end")

	// Now finish tool 2 and message_end (in either order).
	notify(t, r, rpc.MethodToolStart, rpc.ToolStartParams{
		ToolCallID: "tc_2", ToolName: "read",
	})
	notify(t, r, rpc.MethodToolEnd, rpc.ToolEndParams{
		ToolCallID: "tc_2", ToolName: "read", Result: "ok",
	})

	// Still no message_end → still not committed.
	assert.NotNil(t, r.batch, "batch finalized before message_end")

	notify(t, r, rpc.MethodMessageEnd, rpc.MessageEndParams{
		StopReason: "tool_invoke",
	})

	// Now it should be finalized.
	assert.Nil(t, r.batch, "batch should be finalized after message_end + all tool_ends")
}

// TestStreamRenderer_OutputBeforeStart ensures tool_output arriving
// before MessageEnd is buffered rather than streamed directly to the
// solo placeholder. This prevents the leak-above-batch bug: if the
// CLI later upgrades to batch, anything already streamed to the solo
// would land above the batch frame.
func TestStreamRenderer_OutputBeforeStart(t *testing.T) {
	r, _ := newTestRenderer(t)

	notify(t, r, rpc.MethodToolInvokeStart, rpc.ToolInvokeStartParams{
		ToolCallID: "tc_a", ToolName: "read",
	})

	// Output arrives during the streaming window. Buffer it.
	notify(t, r, rpc.MethodToolOutput, rpc.ToolOutputParams{
		ToolCallID: "tc_a", ToolName: "read", Chunk: "EARLY_OUTPUT_A",
	})

	assert.Equal(t, []string{"EARLY_OUTPUT_A"}, r.pendingOutput["tc_a"],
		"output should buffer pre-MessageEnd so a possible upgrade-to-batch can transplant it")

	// MessageEnd commits solo and flushes.
	notify(t, r, rpc.MethodMessageEnd, rpc.MessageEndParams{
		StopReason: "tool_invoke",
	})
	assert.True(t, r.soloCommitted, "soloCommitted must flip on MessageEnd")
	assert.NotContains(t, r.pendingOutput, "tc_a",
		"buffer should be empty after MessageEnd flushed the solo")

	// Cleanup.
	notify(t, r, rpc.MethodToolEnd, rpc.ToolEndParams{
		ToolCallID: "tc_a", ToolName: "read", Result: "done-a",
	})
}

// TestStreamRenderer_SoloOutputThenUpgrade captures the real-world
// interleaving the user reported: tool 1's invoke_start arrives, then
// tool 1's output starts streaming (because the dispatcher executes
// speculatively), and only THEN does tool 2's invoke_start arrive
// to trigger upgradeToBatch. The bug: tool 1's output was written
// directly to the terminal via solo.Write, so by the time the batch
// frame opens, the file content is already in scrollback ABOVE the
// batch frame.
//
// Correct behavior: solo output is buffered until either MessageEnd
// confirms solo (commit and flush) or a second invoke arrives
// (upgradeToBatch transplants the buffer into row 0). Never write
// directly to the terminal while we might still upgrade.
func TestStreamRenderer_SoloOutputThenUpgrade(t *testing.T) {
	r, out := newTestRenderer(t)

	// Tool 1 invoke + start + a chunk of output.
	notify(t, r, rpc.MethodToolInvokeStart, rpc.ToolInvokeStartParams{
		ToolCallID: "tc_1", ToolName: "read",
	})
	notify(t, r, rpc.MethodToolInvokeReady, rpc.ToolInvokeReadyParams{
		ToolCallID: "tc_1", ToolName: "read",
		Arguments:  map[string]interface{}{"path": "/tmp/x.md"},
	})
	notify(t, r, rpc.MethodToolStart, rpc.ToolStartParams{
		ToolCallID: "tc_1", ToolName: "read",
		Arguments:  map[string]interface{}{"path": "/tmp/x.md"},
	})
	notify(t, r, rpc.MethodToolOutput, rpc.ToolOutputParams{
		ToolCallID: "tc_1", ToolName: "read",
		Chunk:      "FILE_CONTENT_OF_TC1",
	})

	// Now tool 2 arrives — we upgrade to batch.
	notify(t, r, rpc.MethodToolInvokeStart, rpc.ToolInvokeStartParams{
		ToolCallID: "tc_2", ToolName: "read",
	})

	// At this moment the batch frame has just opened. Look at the
	// rendered buffer: FILE_CONTENT_OF_TC1 should NOT appear above
	// the batch frame.
	rendered := stripANSIRendered(out.String())
	t.Logf("rendered after upgrade:\n%s", rendered)
	batchIdx := strings.Index(rendered, "batch")
	require.Greater(t, batchIdx, 0, "expected a batch frame to be drawn")
	preBatch := rendered[:batchIdx]
	assert.NotContains(t, preBatch, "FILE_CONTENT_OF_TC1",
		"solo's output leaked above the batch frame; should have been buffered until upgrade")

	// Finish both tools.
	notify(t, r, rpc.MethodToolEnd, rpc.ToolEndParams{
		ToolCallID: "tc_1", ToolName: "read", Result: "FILE_CONTENT_OF_TC1",
	})
	notify(t, r, rpc.MethodToolStart, rpc.ToolStartParams{
		ToolCallID: "tc_2", ToolName: "read",
	})
	notify(t, r, rpc.MethodToolEnd, rpc.ToolEndParams{
		ToolCallID: "tc_2", ToolName: "read", Result: "ok",
	})
	notify(t, r, rpc.MethodMessageEnd, rpc.MessageEndParams{
		StopReason: "tool_invoke",
	})
}

// TestStreamRenderer_SingleToolStreams verifies the happy solo path:
// one tool, output streams during execution, MessageEnd confirms solo,
// and the output is flushed to the solo at that point (not earlier
// and not into a buffer that leaks out as orphan).
func TestStreamRenderer_SingleToolStreams(t *testing.T) {
	r, out := newTestRenderer(t)

	notify(t, r, rpc.MethodToolInvokeStart, rpc.ToolInvokeStartParams{
		ToolCallID: "tc_1", ToolName: "read",
	})
	notify(t, r, rpc.MethodToolInvokeReady, rpc.ToolInvokeReadyParams{
		ToolCallID: "tc_1", ToolName: "read",
		Arguments:  map[string]interface{}{"path": "/tmp/x.md"},
	})
	notify(t, r, rpc.MethodToolStart, rpc.ToolStartParams{
		ToolCallID: "tc_1", ToolName: "read",
		Arguments:  map[string]interface{}{"path": "/tmp/x.md"},
	})
	notify(t, r, rpc.MethodToolOutput, rpc.ToolOutputParams{
		ToolCallID: "tc_1", ToolName: "read",
		Chunk:      "SOLO_OUTPUT_PAYLOAD",
	})
	notify(t, r, rpc.MethodToolEnd, rpc.ToolEndParams{
		ToolCallID: "tc_1", ToolName: "read", Result: "SOLO_OUTPUT_PAYLOAD",
	})
	notify(t, r, rpc.MethodMessageEnd, rpc.MessageEndParams{
		StopReason: "tool_invoke",
	})
	notify(t, r, rpc.MethodMessage, rpc.MessageParams{})
	notify(t, r, rpc.MethodDone, rpc.DoneParams{Reason: "stop"})
	<-r.Done()

	rendered := stripANSIRendered(out.String())
	assert.Contains(t, rendered, "SOLO_OUTPUT_PAYLOAD",
		"solo output should render eventually; got %q", rendered)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

package compose

// PROBE, not yet a pinned test. Answers one question: can a message whose
// rendering depends on a map the memo key cannot see (partials, argPartials,
// timings) ever land inside the memoized prefix?
//
// The invariant is stated WITHOUT reference to Incremental, so it tests the
// SPLIT RULE rather than the implementation:
//
//	for frames f < g, recomposing msgs[:bound_f] with frame g's maps must
//	yield exactly what it yielded at frame f.
//
// If that ever fails, a memoized node is stale on screen: a lie, not a miss.

import (
	"fmt"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/message"
)

type probeResult struct {
	violations []string // prefix recomposed differently under later inputs
	liveFrames int      // frames whose prefix depends on a live map
	maxBound   int
	frames     int
}

// probeScript runs a frame script against a boundary rule and reports whether
// the prefix ever changes under later inputs.
func probeScript(t *testing.T, name string, frames []frame, boundary func([]message.Message) int) probeResult {
	t.Helper()
	var (
		res       probeResult
		prevBound int
		prevNodes = map[int][]byte{} // bound -> fingerprint of prefix composition
	)
	res.frames = len(frames)
	for i, f := range frames {
		bound := boundary(f.msgs)
		if bound > res.maxBound {
			res.maxBound = bound
		}
		if bound < prevBound {
			res.violations = append(res.violations, fmt.Sprintf(
				"%s frame %d (%s): stable boundary WENT BACKWARDS %d -> %d", name, i, f.what, prevBound, bound))
		}
		prevBound = bound

		if n := liveDependentMsgs(f.msgs[:bound], f.partials, f.argPartials, f.timings); n > 0 {
			res.liveFrames++
			t.Logf("%s frame %d (%s): %d message(s) in the prefix depend on a live map", name, i, f.what, n)
		}

		// The split rule: every earlier bound, recomposed with THIS frame's
		// maps and messages, must be byte-identical to what it was.
		for b := 1; b <= bound; b++ {
			fp := fingerprint(Nodes(f.msgs[:b], f.partials, f.argPartials, f.timings))
			if was, ok := prevNodes[b]; ok && string(was) != string(fp) {
				res.violations = append(res.violations, fmt.Sprintf(
					"%s frame %d (%s): prefix of %d messages RECOMPOSED DIFFERENTLY\n  was: %s\n  now: %s",
					name, i, f.what, b, trunc(string(was)), trunc(string(fp))))
				delete(prevNodes, b) // report once per bound
				continue
			}
			prevNodes[b] = fp
		}
	}
	t.Logf("%s: %d frames, max stable boundary %d, %d frames with a live-map dependency inside the prefix, %d violations",
		name, res.frames, res.maxBound, res.liveFrames, len(res.violations))
	return res
}

// liveDependentMsgs counts messages in the prefix carrying a tool_invoke whose
// result is NOT in the prefix -- exactly the node whose Output/Input/timings
// come from a map the memo key does not read.
func liveDependentMsgs(prefix []message.Message, partials, argPartials map[string]string, timings map[string]ToolTiming) int {
	results := map[string]bool{}
	for _, m := range prefix {
		for _, c := range m.Content {
			if c.Type == message.ContentToolResult && c.ToolCallID != "" {
				results[c.ToolCallID] = true
			}
		}
	}
	n := 0
	for _, m := range prefix {
		for _, c := range m.Content {
			if c.Type != message.ContentToolInvoke || results[c.ToolCallID] {
				continue
			}
			n++
			break
		}
	}
	return n
}

func fingerprint(nodes []livedoc.Node) []byte {
	return []byte(fmt.Sprintf("%+v", nodes))
}

func trunc(s string) string {
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}

func TestSplitRuleHoldsOnATurn(t *testing.T) {
	res := probeScript(t, "turnScript(4)", turnScript(4), stableBoundary)
	for _, v := range res.violations {
		t.Error(v)
	}
}

// THE CANARY. The probe above passes; a probe that has never failed is not
// evidence. M1 is the mutant the campaign already tried at the pty level: a
// boundary that claims every message is settled. It must make the probe
// scream, and it must specifically drag live-map-dependent messages into the
// prefix -- otherwise the probe is not measuring what it claims.
func TestSplitRuleProbeCanFail(t *testing.T) {
	m1 := func(msgs []message.Message) int { return len(msgs) }
	res := probeScript(t, "M1(everything settled)", turnScript(4), m1)
	if len(res.violations) == 0 {
		t.Fatalf("the probe found NOTHING under a boundary that claims every message is settled; it cannot fail, so it is not evidence")
	}
	if res.liveFrames == 0 {
		t.Fatalf("M1 put no live-map-dependent message in the prefix; the probe's second meter reads zero when it should read many")
	}
	t.Logf("canary OK: %d violations, %d frames with a live-map dependency", len(res.violations), res.liveFrames)
}

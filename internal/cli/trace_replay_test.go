package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/rpc"
)

// driveTrace replays a render trace captured by FIGARO_WIRE_LOG (one JSON object
// per line: {"op":"init","w","h"} then {"op":"frame","method","params"} and
// {"op":"tick"} in the exact order the live client applied them) through the
// REAL liveRegion into a finite VT. Because the client serializes every frame
// and spinner tick on one mutex, the file order IS the apply order — so this
// reproduces the client's exact rendering deterministically, with no agent and
// no tokens. Returns the full VT transcript.
func driveTrace(tracePath string) ([]string, int, int) {
	f, err := os.Open(tracePath)
	if err != nil {
		panic(err)
	}
	defer f.Close()

	var buf bytes.Buffer
	var lr *liveRegion
	var v *vt
	w, h := 80, 24
	frames := 0
	flush := func() {
		if v != nil {
			v.feed(buf.String())
		}
		buf.Reset()
	}
	prevRole := ""

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev struct {
			Op     string          `json:"op"`
			W      int             `json:"w"`
			H      int             `json:"h"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		switch ev.Op {
		case "init":
			w, h = ev.W, ev.H
			lr = newLiveRegion(&buf, w, 10)
			lr.height = h
			v = newVTH(w, h, true)
			buf.WriteString(autowrapOff)
			flush()
		case "tick":
			if lr != nil {
				lr.tickSpin()
				flush()
			}
		case "frame":
			if lr == nil { // trace without an init line: assume a default viewport
				lr = newLiveRegion(&buf, w, 10)
				lr.height = h
				v = newVTH(w, h, true)
			}
			switch ev.Method {
			case rpc.MethodLogSnapshot:
				var e rpc.SnapshotEntry
				if json.Unmarshal(ev.Params, &e) == nil {
					if e.Role == "assistant" {
						lr.bookend = func() string { return "BOOKEND" }
					} else {
						lr.bookend = nil
					}
					prevRole = e.Role
					_ = prevRole
					lr.snapshot(e.Nodes)
				}
			case rpc.MethodNodeOpen:
				var e rpc.NodeOpenEntry
				if json.Unmarshal(ev.Params, &e) == nil {
					lr.applyOp(livedoc.Op{Kind: livedoc.OpOpen, Index: e.Index, Node: &e.Node})
				}
			case rpc.MethodNodePatch:
				var e rpc.NodePatchEntry
				if json.Unmarshal(ev.Params, &e) == nil {
					lr.applyOp(livedoc.Op{Kind: livedoc.OpPatch, Index: e.Index, Field: e.Field, At: e.At, Del: e.Del, Ins: e.Ins})
				}
			case rpc.MethodNodeSet:
				var e rpc.NodeSetEntry
				if json.Unmarshal(ev.Params, &e) == nil {
					lr.applyOp(livedoc.Op{Kind: livedoc.OpSet, Index: e.Index, Status: e.Status, Name: e.Name, Args: e.Args})
				}
			case rpc.MethodLogCommit:
				lr.commit()
			}
			frames++
			flush()
		}
	}
	if v == nil {
		return nil, 0, 0
	}
	return v.screen(), frames, len(v.screen())
}

var reTraceHeader = regexp.MustCompile(`^(?:✓|✗|◦|⠋|⠙|⠹|⠸|⠼|⠴|⠦|⠧|⠇|⠏)\s+(write|edit|bash)\s+(\S.*)$`)
var reTraceWrote = regexp.MustCompile(`Wrote \d+ bytes to (\S+)`)

// TestReplayCapturedTrace replays FIGARO_REPLAY_TRACE (a real capture) through
// the painter and asserts no output-bleed / duplicate headers. Skipped unless
// the env var points at a trace file. Usage:
//
//	FIGARO_REPLAY_TRACE=/tmp/trace.jsonl go test ./internal/cli/ -run TestReplayCapturedTrace -v
func TestReplayCapturedTrace(t *testing.T) {
	path := os.Getenv("FIGARO_REPLAY_TRACE")
	if path == "" {
		t.Skip("set FIGARO_REPLAY_TRACE=<trace.jsonl> to replay a captured trace")
	}
	screen, frames, rows := driveTrace(path)
	t.Logf("replayed %d frames -> %d transcript rows", frames, rows)

	hdrCount := map[string]int{}
	var bleed []string
	cur := ""
	for _, l := range screen {
		s := strings.TrimSpace(liveStrip(l))
		if m := reTraceHeader.FindStringSubmatch(s); m != nil {
			cur = m[1] + " " + strings.Fields(m[2])[0]
			if m[1] == "write" || m[1] == "edit" {
				hdrCount[cur]++
			}
		}
		if m := reTraceWrote.FindStringSubmatch(s); m != nil && cur != "" {
			parts := strings.Fields(cur)
			if parts[0] == "bash" || (len(parts) > 1 && parts[1] != m[1]) {
				bleed = append(bleed, cur+" -> "+m[1])
			}
		}
	}
	for h, n := range hdrCount {
		if n > 1 {
			t.Errorf("duplicate header: %s x%d", h, n)
		}
	}
	for _, b := range bleed {
		t.Errorf("output bleed: header [%s]", b)
	}
	if t.Failed() {
		t.Logf("---- transcript ----\n%s", strings.Join(screen, "\n"))
	}
}

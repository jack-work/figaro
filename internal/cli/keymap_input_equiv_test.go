package cli

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// The input-level fixture and starting states for input.golden: every byte,
// every navigation encoding and a handful of CSI-u chords, through the real
// read loop, recording everything a keystroke can move (the pager, the
// verbosity and listen flags, the clipboard, the copy machine, the disconnect
// channel, cancellation, whether the loop stopped, and the bytes held back).
//
// Mouse gestures are not swept here: a pointer is not a chord. They are
// asserted against the painted frame in transcript_mouse_test.go.

type inputProbe struct {
	in        *interactiveInput
	lt        *livelogTurn
	tc        *recordingTerminal
	cancelled *atomic.Bool
}

func newInputProbe(tb testing.TB, open bool) *inputProbe {
	in, lt := navInput(tb, &countingWriter{}, open)
	tc := newRecordingTerminal()
	var cancelled atomic.Bool
	in.tc = tc
	in.cancel = func() { cancelled.Store(true) }
	return &inputProbe{in: in, lt: lt, tc: tc, cancelled: &cancelled}
}

var inputStates = map[string]func(testing.TB) *inputProbe{
	"incipit": func(tb testing.TB) *inputProbe { return newInputProbe(tb, false) },
	"transcript": func(tb testing.TB) *inputProbe {
		return newInputProbe(tb, true)
	},
	"transcript+sel": func(tb testing.TB) *inputProbe {
		p := newInputProbe(tb, true)
		p.lt.tr.selectNode(1, false)
		p.lt.tr.render()
		return p
	},
	"search": func(tb testing.TB) *inputProbe {
		p := newInputProbe(tb, true)
		p.in.consume([]byte("/ms"))
		return p
	},
	"panel": func(tb testing.TB) *inputProbe {
		p := newInputProbe(tb, true)
		p.in.consume([]byte("h"))
		return p
	},
	"jump": func(tb testing.TB) *inputProbe {
		p := newInputProbe(tb, true)
		p.in.consume([]byte(":12"))
		return p
	},
	"fork": func(tb testing.TB) *inputProbe {
		p := newInputProbe(tb, true)
		p.in.consume([]byte("f"))
		return p
	},
	"leader": func(tb testing.TB) *inputProbe {
		p := newInputProbe(tb, true)
		p.in.consume([]byte(" "))
		return p
	},
}

// inputSignature snapshots everything a keystroke can move. It reads under the
// render lock: a key can leave background work behind (the queued-panel fetch,
// the history-search worker), and those write the same fields.
// base is the offset an inert key leaves in this state; see offToken.
func inputSignature(p *inputProbe, stop bool, rest []byte, base int) string {
	p.in.mu.Lock()
	defer p.in.mu.Unlock()
	tr := p.lt.tr
	copyFailed, copying := p.in.copyFailed, p.in.copyCancel != nil
	clip, _ := p.tc.clipboard.Load().(string)
	if len(clip) > 12 {
		clip = fmt.Sprintf("%d bytes", len(clip))
	}
	return fmt.Sprintf("stop=%v rest=%q act=%v %s fol=%v srch=%v q=%q h=%v s=%v Q=%v pre=%s sel=%v verb=%v disc=%d canc=%v clip=%q cpfail=%v cping=%v jmp=%v jq=%q",
		stop, string(rest), tr.active, offToken(tr, base), tr.follow, tr.inSearch, tr.query,
		tr.showing("help"), tr.showing("status"), tr.showing("queue"), prefixToken(tr), tr.selection.active,
		p.in.set.verbose, len(p.in.disconnectCh), p.cancelled.Load(), clip, copyFailed, copying,
		tr.boxOpen(), tr.cmdline.String())
}

// settleProbe waits out the background paging and any selection copy the
// keystroke kicked off, so the signature is deterministic.
func settleProbe(tb testing.TB, p *inputProbe) {
	for {
		p.in.mu.Lock()
		done := p.in.pageDone
		p.in.mu.Unlock()
		if done == nil {
			break
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			tb.Fatal("history prefetch never finished")
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		p.in.mu.Lock()
		busy := p.in.copyCancel != nil || p.in.searchCancel != nil
		p.in.mu.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			tb.Fatal("a background worker (copy or search) never finished")
		}
		time.Sleep(time.Millisecond)
	}
}

// The keys swept, by the name the oracle records them under.
func inputSweepKeys() []struct {
	name string
	data string
} {
	var keys []struct {
		name string
		data string
	}
	add := func(name, data string) {
		keys = append(keys, struct {
			name string
			data string
		}{name, data})
	}
	for b := 0; b < 128; b++ {
		add(fmt.Sprintf("0x%02x", b), string([]byte{byte(b)}))
	}
	for _, n := range []navKey{navUp, navDown, navPageUp, navPageDown, navHome, navEnd} {
		add("nav:"+inputOracleNavName(n), inputNavSeq(n))
	}
	add("csiu ^n", "\x1b[110;5u")
	add("csiu ^N+shift", "\x1b[110;6u")
	add("csiu ^p", "\x1b[112;5u")
	add("csiu ^p+alt", "\x1b[112;7u")
	add("csiu ^d", "\x1b[100;5u")
	add("csiu ^l", "\x1b[108;5u")
	add("csiu ^t", "\x1b[116;5u")
	add("csiu ^o", "\x1b[111;5u")
	// M-m is verbose tool output (it was ^O). A meta chord, so the byte
	// sweep above cannot reach it, and a binding no sweep reaches is a
	// binding this oracle does not defend.
	add("alt m", "\x1bm")
	add("alt ^n fallback", "\x1b\x0e")
	add("alt ^p fallback", "\x1b\x10")
	return keys
}

func inputOracleNavName(n navKey) string {
	switch n {
	case navUp:
		return "Up"
	case navDown:
		return "Down"
	case navPageUp:
		return "PgUp"
	case navPageDown:
		return "PgDn"
	case navHome:
		return "Home"
	case navEnd:
		return "End"
	}
	return "?"
}

func inputNavSeq(n navKey) string {
	switch n {
	case navUp:
		return "\x1b[A"
	case navDown:
		return "\x1b[B"
	case navPageUp:
		return "\x1b[5~"
	case navPageDown:
		return "\x1b[6~"
	case navHome:
		return "\x1b[H"
	case navEnd:
		return "\x1b[F"
	}
	return ""
}

// inputInertOffset is the input loop's inertOffset: where the offset sits
// after a key this state does not bind, which every cell is measured against.
func inputInertOffset(tb testing.TB, build func(testing.TB) *inputProbe) int {
	for _, k := range inputSweepKeys() {
		p := build(tb)
		p.in.consume([]byte(k.data))
		settleProbe(tb, p)
		p.in.mu.Lock()
		off := p.lt.tr.offset
		p.in.mu.Unlock()
		return off
	}
	return 0
}

// sweepInput is sweepPager's twin for the real input loop: every key of the
// sweep through one state. Shared by recording and checking, for the reason
// stated on sweepPager.
func sweepInput(tb testing.TB, build func(testing.TB) *inputProbe) (cells map[string]string, base int) {
	base = inputInertOffset(tb, build)
	cells = make(map[string]string)
	for _, k := range inputSweepKeys() {
		p := build(tb)
		rest, stop := p.in.consume([]byte(k.data))
		settleProbe(tb, p)
		cells[k.name] = inputSignature(p, stop, rest, base)
	}
	return cells, base
}

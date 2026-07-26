package cli

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Behavioural equivalence at the input level: what the read loop does with a
// key, in every mode, must be what it did before the keymap.
//
// Generated the same way as the pager oracle in keymap_equiv_test.go — by
// running 45bee38 over every byte, every navigation encoding and a handful of
// CSI-u chords in five starting states, and recording everything a keystroke
// can move: the pager, the verbosity and listen flags, the clipboard, the
// copy state machine, the disconnect channel, whether the context was
// cancelled, whether the loop stopped, and what bytes were held back for the
// next read.
// ---------------------------------------------------------------------------

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
		p.in.consume([]byte("?"))
		return p
	},
}

// inputSignature snapshots everything a keystroke can move. It reads under the
// render lock: a key can leave background work behind (the queued-panel fetch,
// the history-search worker), and those write the same fields.
func inputSignature(p *inputProbe, stop bool, rest []byte) string {
	p.in.mu.Lock()
	defer p.in.mu.Unlock()
	tr := p.lt.tr
	copyFailed, copying := p.in.copyFailed, p.in.copyCancel != nil
	clip, _ := p.tc.clipboard.Load().(string)
	if len(clip) > 12 {
		clip = fmt.Sprintf("%d bytes", len(clip))
	}
	return fmt.Sprintf("stop=%v rest=%q act=%v off=%d fol=%v srch=%v q=%q h=%v s=%v Q=%v g=%v sel=%v verb=%v disc=%d canc=%v clip=%q cpfail=%v cping=%v",
		stop, string(rest), tr.active, tr.offset, tr.follow, tr.inSearch, tr.query,
		tr.showHelp, tr.showStatus, tr.showQueued, tr.pendG, tr.selection.active,
		p.in.set.verbose, len(p.in.disconnectCh), p.cancelled.Load(), clip, copyFailed, copying)
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

var inputOracle = []struct {
	state string
	inert string
	keys  map[string]string
}{
	{"incipit", "stop=false rest=\"\" act=false off=0 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false", map[string]string{
		"0x03":            "stop=true rest=\"\" act=false off=0 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=true clip=\"\" cpfail=false cping=false",
		"0x04":            "stop=true rest=\"\" act=false off=0 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=1 canc=false clip=\"\" cpfail=false cping=false",
		"0x0a":            "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x0c":            "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x0d":            "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x0e":            "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x0f":            "stop=false rest=\"\" act=true off=759 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=true disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x10":            "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x14":            "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x21":            "stop=false rest=\"\" act=true off=748 fol=true srch=false q=\"\" h=false s=true Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x2f":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x3f":            "stop=false rest=\"\" act=true off=762 fol=true srch=false q=\"\" h=true s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x47":            "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x51":            "stop=false rest=\"\" act=true off=745 fol=true srch=false q=\"\" h=false s=false Q=true g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x64":            "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x67":            "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=true sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x6a":            "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x6b":            "stop=false rest=\"\" act=true off=741 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x75":            "stop=false rest=\"\" act=true off=721 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x79":            "stop=false rest=\"\" act=false off=0 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"aria0001\" cpfail=false cping=false",
		"alt ^n fallback": "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"alt ^p fallback": "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^N+shift":   "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^d":         "stop=true rest=\"\" act=false off=0 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=1 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^l":         "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^n":         "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^o":         "stop=false rest=\"\" act=true off=759 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=true disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^p":         "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^p+alt":     "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^t":         "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"nav:Down":        "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"nav:End":         "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"nav:Home":        "stop=false rest=\"\" act=true off=0 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"nav:PgDn":        "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"nav:PgUp":        "stop=false rest=\"\" act=true off=721 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"nav:Up":          "stop=false rest=\"\" act=true off=741 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
	}},
	{"panel", "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false", map[string]string{
		"0x03":            "stop=true rest=\"\" act=true off=762 fol=true srch=false q=\"\" h=true s=false Q=false g=false sel=false verb=false disc=0 canc=true clip=\"\" cpfail=false cping=false",
		"0x04":            "stop=true rest=\"\" act=true off=762 fol=true srch=false q=\"\" h=true s=false Q=false g=false sel=false verb=false disc=1 canc=false clip=\"\" cpfail=false cping=false",
		"0x0c":            "stop=false rest=\"\" act=true off=762 fol=true srch=false q=\"\" h=true s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x0e":            "stop=false rest=\"\" act=true off=2 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x0f":            "stop=false rest=\"\" act=true off=779 fol=true srch=false q=\"\" h=true s=false Q=false g=false sel=false verb=true disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x10":            "stop=false rest=\"\" act=true off=741 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x14":            "stop=false rest=\"\" act=true off=762 fol=true srch=false q=\"\" h=true s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x1b":            "stop=false rest=\"\\x1b\" act=true off=762 fol=true srch=false q=\"\" h=true s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x21":            "stop=false rest=\"\" act=true off=748 fol=true srch=false q=\"\" h=false s=true Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x2f":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x51":            "stop=false rest=\"\" act=true off=745 fol=true srch=false q=\"\" h=false s=false Q=true g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x64":            "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x67":            "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=true sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x6a":            "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x6b":            "stop=false rest=\"\" act=true off=741 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x71":            "stop=true rest=\"\" act=true off=762 fol=true srch=false q=\"\" h=true s=false Q=false g=false sel=false verb=false disc=1 canc=false clip=\"\" cpfail=false cping=false",
		"0x75":            "stop=false rest=\"\" act=true off=721 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x79":            "stop=false rest=\"\" act=true off=762 fol=true srch=false q=\"\" h=true s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"aria0001\" cpfail=false cping=false",
		"alt ^n fallback": "stop=false rest=\"\" act=true off=2 fol=false srch=false q=\"\" h=true s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"alt ^p fallback": "stop=false rest=\"\" act=true off=761 fol=false srch=false q=\"\" h=true s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^N+shift":   "stop=false rest=\"\" act=true off=2 fol=false srch=false q=\"\" h=true s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^d":         "stop=true rest=\"\" act=true off=762 fol=true srch=false q=\"\" h=true s=false Q=false g=false sel=false verb=false disc=1 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^l":         "stop=false rest=\"\" act=true off=762 fol=true srch=false q=\"\" h=true s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^n":         "stop=false rest=\"\" act=true off=2 fol=false srch=false q=\"\" h=true s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^o":         "stop=false rest=\"\" act=true off=779 fol=true srch=false q=\"\" h=true s=false Q=false g=false sel=false verb=true disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^p":         "stop=false rest=\"\" act=true off=761 fol=false srch=false q=\"\" h=true s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^p+alt":     "stop=false rest=\"\" act=true off=761 fol=false srch=false q=\"\" h=true s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^t":         "stop=false rest=\"\" act=true off=762 fol=true srch=false q=\"\" h=true s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"nav:Down":        "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"nav:Home":        "stop=false rest=\"\" act=true off=0 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"nav:PgDn":        "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"nav:PgUp":        "stop=false rest=\"\" act=true off=721 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"nav:Up":          "stop=false rest=\"\" act=true off=741 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
	}},
	{"search", "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false", map[string]string{
		"0x03":            "stop=true rest=\"\" act=true off=742 fol=true srch=true q=\"ms\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=true clip=\"\" cpfail=false cping=false",
		"0x04":            "stop=true rest=\"\" act=true off=742 fol=true srch=true q=\"ms\" h=false s=false Q=false g=false sel=false verb=false disc=1 canc=false clip=\"\" cpfail=false cping=false",
		"0x08":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"m\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x0a":            "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"ms\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x0c":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x0d":            "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"ms\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x0f":            "stop=false rest=\"\" act=true off=759 fol=true srch=true q=\"ms\" h=false s=false Q=false g=false sel=false verb=true disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x1b":            "stop=false rest=\"\\x1b\" act=true off=742 fol=true srch=true q=\"ms\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x20":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms \" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x21":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms!\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x22":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms\\\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x23":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms#\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x24":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms$\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x25":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms%\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x26":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms&\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x27":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms'\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x28":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms(\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x29":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms)\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x2a":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms*\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x2b":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms+\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x2c":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms,\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x2d":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms-\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x2e":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms.\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x2f":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms/\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x30":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms0\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x31":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms1\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x32":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms2\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x33":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms3\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x34":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms4\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x35":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms5\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x36":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms6\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x37":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms7\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x38":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms8\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x39":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms9\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x3a":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms:\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x3b":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms;\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x3c":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms<\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x3d":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms=\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x3e":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms>\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x3f":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms?\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x40":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms@\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x41":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msA\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x42":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msB\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x43":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msC\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x44":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msD\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x45":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msE\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x46":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msF\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x47":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msG\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x48":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msH\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x49":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msI\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x4a":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msJ\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x4b":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msK\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x4c":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msL\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x4d":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msM\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x4e":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msN\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x4f":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msO\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x50":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msP\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x51":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msQ\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x52":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msR\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x53":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msS\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x54":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msT\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x55":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msU\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x56":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msV\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x57":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msW\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x58":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msX\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x59":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msY\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x5a":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msZ\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x5b":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms[\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x5c":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms\\\\\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x5d":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms]\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x5e":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms^\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x5f":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms_\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x60":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms`\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x61":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msa\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x62":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msb\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x63":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msc\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x64":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msd\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x65":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"mse\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x66":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msf\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x67":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msg\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x68":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msh\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x69":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msi\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x6a":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msj\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x6b":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msk\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x6c":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msl\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x6d":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msm\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x6e":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msn\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x6f":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"mso\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x70":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msp\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x71":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msq\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x72":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msr\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x73":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"mss\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x74":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"mst\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x75":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msu\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x76":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msv\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x77":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msw\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x78":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msx\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x79":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msy\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x7a":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"msz\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x7b":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms{\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x7c":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms|\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x7d":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms}\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x7e":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms~\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x7f":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"m\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"alt ^n fallback": "stop=false rest=\"\" act=true off=2 fol=false srch=true q=\"ms\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"alt ^p fallback": "stop=false rest=\"\" act=true off=741 fol=false srch=true q=\"ms\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^N+shift":   "stop=false rest=\"\" act=true off=2 fol=false srch=true q=\"ms\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^d":         "stop=true rest=\"\" act=true off=742 fol=true srch=true q=\"ms\" h=false s=false Q=false g=false sel=false verb=false disc=1 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^l":         "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"ms\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^n":         "stop=false rest=\"\" act=true off=2 fol=false srch=true q=\"ms\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^o":         "stop=false rest=\"\" act=true off=759 fol=true srch=true q=\"ms\" h=false s=false Q=false g=false sel=false verb=true disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^p":         "stop=false rest=\"\" act=true off=741 fol=false srch=true q=\"ms\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^p+alt":     "stop=false rest=\"\" act=true off=741 fol=false srch=true q=\"ms\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
	}},
	{"transcript", "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false", map[string]string{
		"0x03":            "stop=true rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=true clip=\"\" cpfail=false cping=false",
		"0x04":            "stop=true rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=1 canc=false clip=\"\" cpfail=false cping=false",
		"0x0c":            "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x0e":            "stop=false rest=\"\" act=true off=2 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x0f":            "stop=false rest=\"\" act=true off=759 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=true disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x10":            "stop=false rest=\"\" act=true off=741 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x1b":            "stop=false rest=\"\\x1b\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x21":            "stop=false rest=\"\" act=true off=748 fol=true srch=false q=\"\" h=false s=true Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x2f":            "stop=false rest=\"\" act=true off=742 fol=true srch=true q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x3f":            "stop=false rest=\"\" act=true off=762 fol=true srch=false q=\"\" h=true s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x51":            "stop=false rest=\"\" act=true off=745 fol=true srch=false q=\"\" h=false s=false Q=true g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x64":            "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x67":            "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=true sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x6a":            "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x6b":            "stop=false rest=\"\" act=true off=741 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x71":            "stop=true rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=1 canc=false clip=\"\" cpfail=false cping=false",
		"0x75":            "stop=false rest=\"\" act=true off=721 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x79":            "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"aria0001\" cpfail=false cping=false",
		"alt ^n fallback": "stop=false rest=\"\" act=true off=2 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"alt ^p fallback": "stop=false rest=\"\" act=true off=741 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^N+shift":   "stop=false rest=\"\" act=true off=2 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^d":         "stop=true rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=1 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^l":         "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^n":         "stop=false rest=\"\" act=true off=2 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^o":         "stop=false rest=\"\" act=true off=759 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=true disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^p":         "stop=false rest=\"\" act=true off=741 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^p+alt":     "stop=false rest=\"\" act=true off=741 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"nav:Down":        "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"nav:Home":        "stop=false rest=\"\" act=true off=0 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"nav:PgDn":        "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"nav:PgUp":        "stop=false rest=\"\" act=true off=721 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"nav:Up":          "stop=false rest=\"\" act=true off=741 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
	}},
	{"transcript+sel", "stop=false rest=\"\" act=true off=2 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false", map[string]string{
		"0x03":     "stop=false rest=\"\" act=true off=2 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=true cping=false",
		"0x04":     "stop=true rest=\"\" act=true off=2 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=1 canc=false clip=\"\" cpfail=false cping=false",
		"0x0c":     "stop=false rest=\"\" act=true off=2 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x0f":     "stop=false rest=\"\" act=true off=2 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=true disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x1b":     "stop=false rest=\"\\x1b\" act=true off=2 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x21":     "stop=false rest=\"\" act=true off=2 fol=false srch=false q=\"\" h=false s=true Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x2f":     "stop=false rest=\"\" act=true off=2 fol=false srch=true q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x3f":     "stop=false rest=\"\" act=true off=2 fol=false srch=false q=\"\" h=true s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x47":     "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x51":     "stop=false rest=\"\" act=true off=2 fol=false srch=false q=\"\" h=false s=false Q=true g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x64":     "stop=false rest=\"\" act=true off=22 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x67":     "stop=false rest=\"\" act=true off=2 fol=false srch=false q=\"\" h=false s=false Q=false g=true sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x6a":     "stop=false rest=\"\" act=true off=3 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x6b":     "stop=false rest=\"\" act=true off=1 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x71":     "stop=true rest=\"\" act=true off=2 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=1 canc=false clip=\"\" cpfail=false cping=false",
		"0x75":     "stop=false rest=\"\" act=true off=0 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"0x79":     "stop=false rest=\"\" act=true off=2 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=true cping=false",
		"csiu ^d":  "stop=true rest=\"\" act=true off=2 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=1 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^l":  "stop=false rest=\"\" act=true off=2 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"csiu ^o":  "stop=false rest=\"\" act=true off=2 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=true disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"nav:Down": "stop=false rest=\"\" act=true off=3 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"nav:End":  "stop=false rest=\"\" act=true off=742 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"nav:Home": "stop=false rest=\"\" act=true off=0 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"nav:PgDn": "stop=false rest=\"\" act=true off=22 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"nav:PgUp": "stop=false rest=\"\" act=true off=0 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
		"nav:Up":   "stop=false rest=\"\" act=true off=1 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false",
	}},
}

// TestKeymap_InputBehaviourIsUnchanged sweeps every key through the real input
// loop in every mode and compares against the frozen oracle. A key missing
// from a row is an assertion too: it must leave the state an unbound control
// byte would have left.
func TestKeymap_InputBehaviourIsUnchanged(t *testing.T) {
	for _, row := range inputOracle {
		build, ok := inputStates[row.state]
		if !ok {
			t.Fatalf("oracle names state %q, which the harness does not build", row.state)
		}
		t.Run(row.state, func(t *testing.T) {
			for _, k := range inputSweepKeys() {
				p := build(t)
				rest, stop := p.in.consume([]byte(k.data))
				settleProbe(t, p)
				want, special := row.keys[k.name]
				if !special {
					want = row.inert
				}
				if got := inputSignature(p, stop, rest); got != want {
					verdict := "differs from the pre-refactor input loop"
					if !special {
						verdict = "was inert before the refactor and is not now"
					}
					t.Errorf("%s in %s mode %s:\n got %s\nwant %s", k.name, row.state, verdict, got, want)
				}
			}
		})
	}
}

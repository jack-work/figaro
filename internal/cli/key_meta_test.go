package cli

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Meta, Ctrl and the ':' box, through the REAL input loop.
//
// Everything here is a byte string a terminal actually sends, fed to
// interactiveInput.consume. That is the layer the bugs live at: a binding can
// be perfect in the table and dead in a pty because the encoding the terminal
// chose has no row.
// ---------------------------------------------------------------------------

// cmdBox opens the pager and the ':' box, and types `seed` into it.
func cmdBox(t *testing.T, seed string) (*interactiveInput, *livelogTurn) {
	t.Helper()
	in, lt := navInput(t, &countingWriter{}, true)
	feed(t, in, ":"+seed)
	if lt.transcriptMode() != modeJump {
		t.Fatalf("the ':' box did not open (mode %v)", lt.transcriptMode())
	}
	return in, lt
}

func cmdLine(lt *livelogTurn) string { return lt.tr.cmdline.String() }

func cmdCursor(lt *livelogTurn) int { return lt.tr.cmdline.cursor }

// TestMetaEscapePrefix: ESC <byte> is Alt+<byte> INSIDE the box, and the two
// keystrokes it looks like everywhere else.
func TestMetaEscapePrefix(t *testing.T) {
	in, lt := cmdBox(t, "send --id abc12345")
	feed(t, in, "\x1bb") // M-b: back one word
	if got := cmdCursor(lt); got != 10 {
		t.Fatalf("M-b left the cursor at %d, want 10", got)
	}
	feed(t, in, "\x1bd") // M-d: kill the word forward
	if got := cmdLine(lt); got != "send --id " {
		t.Fatalf("M-d gave %q", got)
	}
	feed(t, in, "\x1bf") // M-f over nothing: the end of the line
	if got := cmdCursor(lt); got != 10 {
		t.Fatalf("M-f left the cursor at %d, want 10", got)
	}
}

// TestMetaCSIu: the same chords in the encoding a modern terminal sends.
func TestMetaCSIu(t *testing.T) {
	in, lt := cmdBox(t, "one two")
	feed(t, in, "\x1b[98;3u") // M-b
	if got := cmdCursor(lt); got != 4 {
		t.Fatalf("CSI-u M-b left the cursor at %d, want 4", got)
	}
	feed(t, in, "\x1b[117;3u") // M-u: upcase the word
	if got := cmdLine(lt); got != "one TWO" {
		t.Fatalf("CSI-u M-u gave %q", got)
	}
}

// TestMetaShiftFolds: Alt+Shift+B is Alt+b, and M-< keeps its shifted byte.
func TestMetaShiftFolds(t *testing.T) {
	in, lt := cmdBox(t, "one two")
	feed(t, in, "\x1bB")
	if got := cmdCursor(lt); got != 4 {
		t.Fatalf("M-B left the cursor at %d, want 4 (it must fold onto M-b)", got)
	}
}

// TestCtrlArrowsWalkWords: Ctrl+Left/Right are the word motions, which is what
// every distro's inputrc makes them.
func TestCtrlArrowsWalkWords(t *testing.T) {
	in, lt := cmdBox(t, "one two")
	feed(t, in, "\x1b[1;5D")
	if got := cmdCursor(lt); got != 4 {
		t.Fatalf("Ctrl+Left left the cursor at %d, want 4", got)
	}
	feed(t, in, "\x1b[1;5C")
	if got := cmdCursor(lt); got != 7 {
		t.Fatalf("Ctrl+Right left the cursor at %d, want 7", got)
	}
}

// TestPlainArrowsWalkCharacters: the hole this pass closed. Left and Right
// used to be dropped at the decoder, so the box could not be walked with the
// arrow keys at all.
func TestPlainArrowsWalkCharacters(t *testing.T) {
	in, lt := cmdBox(t, "abc")
	feed(t, in, "\x1b[D\x1b[D")
	if got := cmdCursor(lt); got != 1 {
		t.Fatalf("two Lefts left the cursor at %d, want 1", got)
	}
	feed(t, in, "\x1b[C")
	if got := cmdCursor(lt); got != 2 {
		t.Fatalf("Right left the cursor at %d, want 2", got)
	}
	feed(t, in, "X")
	if got := cmdLine(lt); got != "abXc" {
		t.Fatalf("typing after an arrow gave %q, want the cursor honoured", got)
	}
}

// TestEscOutsideTheBoxIsStillEsc: the ESC-prefix rule is gated on a binding
// existing in the CURRENT mode, so nothing outside the ':' box changed. In the
// pager, ESC then 'j' is a cleared selection and a line of scroll -- not M-j.
func TestEscOutsideTheBoxIsStillEsc(t *testing.T) {
	// '/' opens the search box, which is a mode change anyone can see: if the
	// pair had been eaten as "M-/", nothing would have happened at all.
	in, lt := navInput(t, &countingWriter{}, true)
	feed(t, in, "\x1b/")
	if lt.transcriptMode() != modeSearch {
		t.Fatalf("Esc then '/' did not reach the search box (mode %v)", lt.transcriptMode())
	}
	// And the same pair inside the box IS a chord: M-/ is unbound there, so it
	// is swallowed whole rather than closing the box and typing a slash.
	in2, lt2 := cmdBox(t, "ab")
	feed(t, in2, "\x1b/")
	if lt2.transcriptMode() != modeJump {
		t.Fatal("Esc then '/' closed the ':' box")
	}
	if got := cmdLine(lt2); got != "ab" {
		t.Fatalf("in the box, an unbound Meta chord must be swallowed; got %q", got)
	}
}

// TestCtrlBracketClosesTheBox: Ctrl-[ IS Escape. A terminal that reports it as
// a distinct key must not turn it into a dead key.
func TestCtrlBracketClosesTheBox(t *testing.T) {
	for _, tc := range []struct {
		name string
		seq  string
	}{
		{"raw esc", "\x1b"},
		{"csi-u ctrl-[", "\x1b[91;5u"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, lt := cmdBox(t, "send")
			feed(t, in, tc.seq)
			if lt.transcriptMode() != modeTranscript {
				t.Fatalf("%s did not close the box (mode %v)", tc.name, lt.transcriptMode())
			}
			if got := cmdLine(lt); got != "" {
				t.Fatalf("the line survived the box: %q", got)
			}
		})
	}
}

// TestCtrlDInTheBox is the headline of this pass: ^D edits inside the box,
// closes it on an empty line, and detaches on the press after that. The escape
// hatch is moved, not removed.
func TestCtrlDInTheBox(t *testing.T) {
	for _, tc := range []struct {
		name string
		seq  string
	}{
		{"raw", "\x04"},
		{"csi-u", "\x1b[100;5u"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, lt := cmdBox(t, "abc")
			feed(t, in, "\x01") // ^A: to the start of the line
			feed(t, in, tc.seq)
			if got := cmdLine(lt); got != "bc" {
				t.Fatalf("^D gave %q, want delete-forward", got)
			}
			feed(t, in, "\x0b") // ^K: empty the line
			if got := cmdLine(lt); got != "" {
				t.Fatalf("^K gave %q", got)
			}
			feed(t, in, tc.seq)
			if lt.transcriptMode() != modeTranscript {
				t.Fatal("^D on an empty box did not close it")
			}
			// And now it is the process's key again.
			if _, stop := in.consume([]byte(tc.seq)); !stop {
				t.Fatal("^D outside the box no longer detaches")
			}
		})
	}
}

// TestCtrlCInTheBoxAbandonsTheLine: ^C is readline's abort here, and the
// session's interrupt one press later.
func TestCtrlCInTheBoxAbandonsTheLine(t *testing.T) {
	in, lt := cmdBox(t, "send -- oops")
	feed(t, in, "\x03")
	if lt.transcriptMode() != modeTranscript {
		t.Fatal("^C did not close the box")
	}
	if got := cmdLine(lt); got != "" {
		t.Fatalf("^C left %q behind", got)
	}
	if _, stop := in.consume([]byte("\x03")); !stop {
		t.Fatal("^C outside the box no longer interrupts")
	}
}

// TestCtrlTTransposesInTheBox: ^T is the pager's "open the transcript" key
// everywhere else, and transpose-chars in here.
func TestCtrlTTransposesInTheBox(t *testing.T) {
	in, lt := cmdBox(t, "oepn")
	feed(t, in, "\x02\x02") // ^B ^B: cursor between 'e' and 'p'
	feed(t, in, "\x14")
	if got := cmdLine(lt); got != "open" {
		t.Fatalf("^T gave %q", got)
	}
}

// TestKillAndYankThroughTheLoop: ^W then ^Y, in the encodings a terminal
// sends, because the ring is only useful if both halves are reachable.
func TestKillAndYankThroughTheLoop(t *testing.T) {
	in, lt := cmdBox(t, "open abc12345")
	feed(t, in, "\x17") // ^W
	if got := cmdLine(lt); got != "open " {
		t.Fatalf("^W gave %q", got)
	}
	feed(t, in, "\x19") // ^Y
	if got := cmdLine(lt); got != "open abc12345" {
		t.Fatalf("^Y gave %q", got)
	}
	// The CSI-u spelling of ^Y reaches the same row.
	feed(t, in, "\x1b[121;5u")
	if got := cmdLine(lt); got != "open abc12345abc12345" {
		t.Fatalf("CSI-u ^Y gave %q", got)
	}
}

// TestCtrlUnderscoreUndoes: ^_ arrives as a bare 0x1f, and as CSI-u Ctrl+-.
func TestCtrlUnderscoreUndoes(t *testing.T) {
	in, lt := cmdBox(t, "send hello")
	feed(t, in, "\x17") // ^W: kill "hello"
	if got := cmdLine(lt); got != "send " {
		t.Fatalf("^W gave %q", got)
	}
	feed(t, in, "\x1f")
	if got := cmdLine(lt); got != "send hello" {
		t.Fatalf("^_ gave %q", got)
	}
	feed(t, in, "\x1b[45;5u") // Ctrl+- , the other way to say ^_
	if got := cmdLine(lt); got != "" {
		t.Fatalf("a second undo gave %q, want the typing undone in one step", got)
	}
}

// TestHistorySearchThroughTheLoop: ^R, typing, and the prompt that says so.
func TestHistorySearchThroughTheLoop(t *testing.T) {
	in, lt := cmdBox(t, "")
	lt.tr.cmdline.remember("open alpha")
	lt.tr.cmdline.remember("send -- hello")

	feed(t, in, "\x12") // ^R
	feed(t, in, "alp")
	if got := cmdLine(lt); got != "open alpha" {
		t.Fatalf("^R alp found %q", got)
	}
	rows := lt.tr.inputDrawerLines()
	if len(rows) == 0 || !strings.Contains(rows[0], "reverse-i-search") {
		t.Fatalf("the box does not say it is searching: %q", rows)
	}
	feed(t, in, "\x1b") // Esc: keep the line, end the search
	if lt.transcriptMode() != modeJump {
		t.Fatal("Esc out of a search closed the whole box")
	}
	if got := cmdLine(lt); got != "open alpha" {
		t.Fatalf("Esc out of a search left %q", got)
	}
	feed(t, in, "\x1b") // and now Esc closes the box
	if lt.transcriptMode() != modeTranscript {
		t.Fatal("a second Esc did not close the box")
	}
}

// TestCtrlNPStillSelectNodesInThePager: the per-mode reduction cuts both ways.
// ^N is history in the box and node selection outside it, in both encodings.
func TestCtrlNPStillSelectNodesInThePager(t *testing.T) {
	in, lt := navInput(t, &countingWriter{}, true)
	feed(t, in, "\x1b[110;5u") // CSI-u ^N
	if !lt.tr.selection.active {
		t.Fatal("^N no longer selects a node in the pager")
	}
}

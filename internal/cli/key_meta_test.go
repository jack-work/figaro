package cli

import (
	"strings"
	"testing"
)

// Byte strings a terminal actually sends, through interactiveInput.consume.

func cmdBox(t *testing.T, seed string) (*interactiveInput, *livelogTurn) {
	t.Helper()
	in, lt := navInput(t, &countingWriter{}, true)
	feed(t, in, ":"+seed)
	if lt.transcriptMode() != modeBox {
		t.Fatalf("the ':' box did not open (mode %v)", lt.transcriptMode())
	}
	return in, lt
}

func cmdLine(lt *livelogTurn) string { return lt.tr.cmdline.String() }

func cmdCursor(lt *livelogTurn) int { return lt.tr.cmdline.cursor }

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

func TestMetaShiftFolds(t *testing.T) {
	in, lt := cmdBox(t, "one two")
	feed(t, in, "\x1bB")
	if got := cmdCursor(lt); got != 4 {
		t.Fatalf("M-B left the cursor at %d, want 4 (it must fold onto M-b)", got)
	}
}

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

// The ESC-prefix rule is gated per mode: in the pager ESC then 'j' is two keys.
func TestEscOutsideTheBoxIsStillEsc(t *testing.T) {
	// '/' opens the search box: a visible mode change.
	in, lt := navInput(t, &countingWriter{}, true)
	feed(t, in, "\x1b/")
	if lt.transcriptMode() != modeSearch {
		t.Fatalf("Esc then '/' did not reach the search box (mode %v)", lt.transcriptMode())
	}
	// Inside the box the pair is a chord: M-/ is unbound, so it is swallowed whole.
	in2, lt2 := cmdBox(t, "ab")
	feed(t, in2, "\x1b/")
	if lt2.transcriptMode() != modeBox {
		t.Fatal("Esc then '/' closed the ':' box")
	}
	if got := cmdLine(lt2); got != "ab" {
		t.Fatalf("in the box, an unbound Meta chord must be swallowed; got %q", got)
	}
}

// Ctrl-[ is Escape.
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

// ^D edits in the box, closes it on an empty line, and detaches on the press after.
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
			if _, stop := in.consume([]byte(tc.seq)); !stop {
				t.Fatal("^D outside the box no longer detaches")
			}
		})
	}
}

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

func TestCtrlTTransposesInTheBox(t *testing.T) {
	in, lt := cmdBox(t, "oepn")
	feed(t, in, "\x02\x02") // ^B ^B: cursor between 'e' and 'p'
	feed(t, in, "\x14")
	if got := cmdLine(lt); got != "open" {
		t.Fatalf("^T gave %q", got)
	}
}

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
	if lt.transcriptMode() != modeBox {
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

func TestCtrlNPStillSelectNodesInThePager(t *testing.T) {
	in, lt := navInput(t, &countingWriter{}, true)
	feed(t, in, "\x1b[110;5u") // CSI-u ^N
	if !lt.tr.selection.active {
		t.Fatal("^N no longer selects a node in the pager")
	}
}

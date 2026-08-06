//go:build windows

package term

import (
	"os"

	"golang.org/x/sys/windows"
)

// MakeRaw puts the console into raw VT-input mode: no line editing, echo, or
// signal generation, and navigation/mouse events encoded as terminal sequences.
//
// It arms the OUTPUT handle too, and that half was missing. Nothing in figaro
// ever set ENABLE_VIRTUAL_TERMINAL_PROCESSING, so the renderer's escapes were
// only honoured when something else had already turned it on. Windows Terminal
// does (measured: stdout mode 0x0007); a bare conhost does not (0x0003), and
// there \x1b[?1049h is inert — the pager's frames land in the PRIMARY buffer
// as ordinary text and the whole transcript is left in the user's scrollback.
// Console mode belongs to the console, not the process, so anything sharing it
// (a tool's child process) can clear the flag mid-session, which is why the
// symptom comes and goes on a terminal that started out fine.
//
// DISABLE_NEWLINE_AUTO_RETURN does NOT ride along, though it addresses the
// same defect at the other edge of the screen: without it the console advances
// the cursor the instant the last cell of a row is written, instead of
// deferring the wrap the way every UNIX terminal does. render.Prose lands
// glamour at EXACTLY the viewport width, so a full-width row cost two rows and
// the painter's one-row-per-line cursor math drifted (measured under conhost at
// width 120: 2 rows as found, 1 with the flag; Windows Terminal was already 1).
// But the same flag also stops a bare LF from implying a carriage return, which
// staircases every fmt.Println, so it is armed per painted session instead --
// see ArmDeferredWrap and vtOutputMode below.
//
// The restore puts both handles back, and it is the same closure the renderer
// already unwinds — so this costs no new lifecycle.
func MakeRaw(fd int) (func(), error) {
	h := windows.Handle(fd)
	var old uint32
	if err := windows.GetConsoleMode(h, &old); err != nil {
		return nil, err
	}
	if err := windows.SetConsoleMode(h, rawInputMode(old)); err != nil {
		return nil, err
	}
	return func() { _ = windows.SetConsoleMode(h, old) }, nil
}

// ArmOutput turns on VT output processing and deferred right-edge wrap for
// stdout, returning the restore.
//
// It is SEPARATE from MakeRaw, and called EARLIER, because the two answer
// different questions: MakeRaw is about the console we READ (and only runs on
// an interactive TTY path), while this is about the console we WRITE. figaro
// writes its first escapes — autowrapOff+cursorHide — before any raw-mode
// session exists, and paths that never take raw mode at all (`figaro show`
// rendering markdown, a non-interactive listen) still emit ANSI. Arming inside
// MakeRaw left every one of those unarmed, which on a bare conhost means the
// escapes print as literal text.
//
// A non-console stdout (piped, redirected, a service with no console) is NOT
// an error: there is simply nothing to arm, and a failed SetConsoleMode is a
// normal condition. Both degrade to a no-op restore rather than a failure,
// because a redirected stdout is the ordinary case for `figaro list -j | jq`.
func ArmOutput() func() {
	h := windows.Handle(os.Stdout.Fd())
	var old uint32
	if err := windows.GetConsoleMode(h, &old); err != nil {
		return func() {}
	}
	if err := windows.SetConsoleMode(h, vtOutputMode(old)); err != nil {
		return func() {}
	}
	return func() { _ = windows.SetConsoleMode(h, old) }
}

func rawInputMode(mode uint32) uint32 {
	mode &^= windows.ENABLE_LINE_INPUT | windows.ENABLE_ECHO_INPUT | windows.ENABLE_PROCESSED_INPUT
	mode |= windows.ENABLE_VIRTUAL_TERMINAL_INPUT
	return mode
}

// cookedInput is the trio a line-oriented prompt needs: the console does the
// editing, echoes what is typed, and turns Ctrl-C into an interrupt.
const cookedInput = windows.ENABLE_LINE_INPUT | windows.ENABLE_ECHO_INPUT | windows.ENABLE_PROCESSED_INPUT

// cookedInputMode is rawInputMode's inverse: line editing, echo and Ctrl-C
// back on, VT input off, everything else the console arrived with untouched.
func cookedInputMode(mode uint32) uint32 {
	mode |= cookedInput
	mode &^= windows.ENABLE_VIRTUAL_TERMINAL_INPUT
	return mode
}

// ArmCookedInput puts the console we READ into line mode for the duration of a
// plain prompt, and returns the restore.
//
// A prompt must not inherit its input mode. figaro itself clears those three
// flags for every interactive session (MakeRaw), and console mode belongs to
// the console, not the process: one session killed without unwinding leaves
// them clear for everything that touches the console afterwards. Then the next
// prompt echoes nothing, and Enter delivers a bare \r that no ReadString('\n')
// will ever see. SanitizeInput heals that at startup; this owns it per read, so
// neither depends on the other.
//
// A non-console stdin (piped, redirected) degrades to a no-op restore, as in
// ArmOutput — `echo y | figaro …` is an ordinary case, not an error.
func ArmCookedInput() func() {
	h := windows.Handle(os.Stdin.Fd())
	var old uint32
	if err := windows.GetConsoleMode(h, &old); err != nil {
		return func() {}
	}
	if err := windows.SetConsoleMode(h, cookedInputMode(old)); err != nil {
		return func() {}
	}
	return func() { _ = windows.SetConsoleMode(h, old) }
}

// SanitizeInput repairs a console left in raw mode by a figaro that died
// without unwinding — a crash, a taskkill, a window closed mid-session.
//
// It is deliberately NOT paired with a restore: the state it replaces is
// wreckage, and handing it back to the shell is how the wreckage propagates
// (PSReadLine saves and restores whatever it finds around each prompt, so a
// poisoned console stays poisoned until the window closes, and the next
// figaro's MakeRaw dutifully saves RAW as the mode to return to).
//
// It only acts when ALL THREE cooked flags are clear, which no console hands
// out and only a raw-mode session produces. A console with any of them set is
// somebody's deliberate state and is left alone.
func SanitizeInput() {
	h := windows.Handle(os.Stdin.Fd())
	var old uint32
	if err := windows.GetConsoleMode(h, &old); err != nil {
		return // not a console: nothing to repair
	}
	if old&cookedInput != 0 {
		return
	}
	_ = windows.SetConsoleMode(h, cookedInputMode(old))
}

// DISABLE_NEWLINE_AUTO_RETURN is deliberately NOT set here. It defers the
// right-edge wrap, which the painter wants — but it also stops a bare LF from
// implying a carriage return, and every line-oriented command (`list`, `show`,
// `status`) writes bare \n through fmt.Println. Armed globally at startup it
// staircased all of them rightward until they wrapped, which reads as "figaro
// ignores my terminal width". The painter arms it for itself: ArmDeferredWrap.
func vtOutputMode(mode uint32) uint32 {
	return mode | windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING
}

// ArmDeferredWrap defers the right-edge wrap for the duration of a PAINTED
// session, and returns the restore.
//
// The painter lands rows at EXACTLY the viewport width and does its own
// one-row-per-line cursor math, so it needs the console to hold the cursor on
// the last cell instead of wrapping the instant that cell is written (measured
// under conhost at width 120: 2 rows without the flag, 1 with; Windows Terminal
// was already 1). It is scoped to the painter because the same flag breaks
// every command that emits ordinary newlines.
//
// A non-console stdout degrades to a no-op restore, as in ArmOutput.
func ArmDeferredWrap() func() {
	h := windows.Handle(os.Stdout.Fd())
	var old uint32
	if err := windows.GetConsoleMode(h, &old); err != nil {
		return func() {}
	}
	if err := windows.SetConsoleMode(h, old|windows.DISABLE_NEWLINE_AUTO_RETURN); err != nil {
		return func() {}
	}
	return func() { _ = windows.SetConsoleMode(h, old) }
}

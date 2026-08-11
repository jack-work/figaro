package term

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// stdinReader is the ONE buffered reader over stdin, shared by every prompt.
//
// Each prompt used to build its own bufio.Reader, which is a byte thief: the
// reader fills its buffer from the console, the caller takes one line, and the
// rest is dropped on the floor with the reader. Two prompts in a row (the
// provider picker followed by the enterprise domain) could therefore lose
// whatever the user typed ahead. One reader for the process lifetime, and
// nothing is ever read twice or lost.
var (
	stdinOnce sync.Once
	stdinBuf  *bufio.Reader
)

func sharedStdin() *bufio.Reader {
	stdinOnce.Do(func() { stdinBuf = bufio.NewReader(os.Stdin) })
	return stdinBuf
}

// ReadLine writes prompt to stderr and reads one line from stdin, without the
// trailing line terminator.
//
// It exists because `bufio.Reader.ReadString('\n')` is a hang waiting to happen
// on Windows. Console mode belongs to the console, not the process: an
// interactive figaro that dies without unwinding its raw-mode restore (a crash,
// a taskkill, a closed window mid-session) leaves ENABLE_LINE_INPUT,
// ENABLE_ECHO_INPUT and ENABLE_PROCESSED_INPUT cleared for every process that
// touches that console afterwards, and PSReadLine faithfully restores the
// broken mode around each of its own prompts, so it survives until the window
// is closed. In that state Enter delivers a bare \r, ReadString('\n') waits for
// a byte that will never arrive, nothing echoes, and Ctrl-C arrives as the byte
// 0x03 rather than an interrupt. MEASURED symptom: `figaro login copilot`
// appears hung at its first prompt and takes two Ctrl-C to leave.
//
// So: \r ends a line as surely as \n does, ArmCookedInput restores echo and
// line editing for the duration of the read, and neither depends on the other
// being enough.
func ReadLine(prompt string) (string, error) {
	if prompt != "" {
		fmt.Fprint(os.Stderr, prompt)
	}
	defer ArmCookedInput()()
	return readLine(sharedStdin())
}

// readLine is the terminator-agnostic half, split out so it can be tested
// without a console.
func readLine(r *bufio.Reader) (string, error) {
	var b strings.Builder
	for {
		c, err := r.ReadByte()
		if err != nil {
			// A last line with no terminator is still a line.
			if err == io.EOF && b.Len() > 0 {
				return b.String(), nil
			}
			return b.String(), err
		}
		switch c {
		case '\n':
			return b.String(), nil
		case '\r':
			// CRLF: drop the paired LF so it can't surface as a phantom
			// empty line at the NEXT prompt. Only from bytes already
			// buffered: peeking past them would block on a lone \r and
			// reintroduce exactly the hang this function exists to kill.
			if r.Buffered() > 0 {
				if p, perr := r.Peek(1); perr == nil && p[0] == '\n' {
					_, _ = r.ReadByte()
				}
			}
			return b.String(), nil
		default:
			b.WriteByte(c)
		}
	}
}

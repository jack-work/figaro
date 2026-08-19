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

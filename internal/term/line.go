package term

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// One buffered reader over stdin, shared by every prompt.
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

func readLine(r *bufio.Reader) (string, error) {
	var b strings.Builder
	for {
		c, err := r.ReadByte()
		if err != nil {
			if err == io.EOF && b.Len() > 0 {
				return b.String(), nil
			}
			return b.String(), err
		}
		switch c {
		case '\n':
			return b.String(), nil
		case '\r':
			// Drop a paired LF, but only from buffered bytes: peeking past them
			// blocks on a lone \r.
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

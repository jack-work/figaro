package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/term"
)

type stderrConsole struct{}

func (stderrConsole) Note(s string)      { fmt.Fprintln(os.Stderr, "       "+dim(s)) }
func (stderrConsole) Link(u string)      { fmt.Fprintln(os.Stderr, "       "+cyan(u)) }
func (stderrConsole) Code(c string)      { fmt.Fprintln(os.Stderr, "       Code: "+cyan(c)) }
func (stderrConsole) Done(s string)      { fmt.Fprintln(os.Stderr, "       "+green("✓")+" "+s) }
func (stderrConsole) Open(u string) bool { return auth.OpenBrowser(u) }

func (stderrConsole) Ask(_ context.Context, prompt string) (string, error) {
	line, err := term.ReadLine("       " + prompt)
	return strings.TrimSpace(line), err
}

func (stderrConsole) AskSecret(_ context.Context, prompt string) ([]byte, error) {
	fmt.Fprint(os.Stderr, "       "+prompt)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return b, err
}

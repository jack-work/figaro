//go:build windows

package term

import "os"

func Interactive(in, out *os.File) bool {
	return IsTerminal(int(in.Fd())) && IsTerminal(int(out.Fd()))
}

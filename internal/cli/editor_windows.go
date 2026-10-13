//go:build windows

package cli

import "syscall"

// No process groups on a console: the terminal is handed over by suspending our
// own reads.
func editorProcAttr() *syscall.SysProcAttr { return nil }

func reclaimTerminal() {}

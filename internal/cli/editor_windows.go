//go:build windows

package cli

import "syscall"

// Windows has no process groups on a console in the UNIX sense: the child
// shares the console and the terminal is handed over by suspending our own
// reads, which the caller has already done.
func editorProcAttr() *syscall.SysProcAttr { return nil }

func reclaimTerminal() {}

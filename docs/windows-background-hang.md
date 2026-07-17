# Backgrounded Commands Never Terminate on Windows

## Symptom

Commands backgrounded via the bash tool's yield/background mechanism
never show as completed on Windows, even if the command itself finishes
quickly. The session stays in "running" state indefinitely. Works on Linux.

## Root Cause

`cmd.Wait()` in Go's `os/exec` blocks until BOTH conditions are met:
1. The process has exited
2. All stdout/stderr pipe copy goroutines have finished (pipe EOF)

On Windows, pipe handles are inherited by child processes by default.
When `bash.exe` (Git Bash) spawns subprocesses (the actual command),
those children inherit the stdout/stderr pipe write handles. Even after
bash exits, the pipe stays open because the grandchild still holds the
write handle. `cmd.Wait()` blocks forever waiting for EOF on the pipe.

On Linux, pipe file descriptors are also inherited, but processes are
more likely to close inherited FDs or get reaped cleanly. The behavior
difference is in how aggressively Windows keeps pipe handles alive across
process boundaries.

## Where in Code

- `internal/tool/exec_local.go` line ~120: `cmd.Wait()` in the goroutine
  inside `Start()` (the background executor path)
- `internal/tool/session.go` line ~90: `supervise()` blocks on
  `proc.Wait()` which blocks on `cmd.Wait()`
- The `hardTimeout` in `supervise()` will eventually kill the process,
  but `DefaultHardTimeout` is 30 minutes

## Possible Fixes

1. **Non-inheritable pipes (Windows-specific).** Create the stdout/stderr
   pipes manually with `SECURITY_ATTRIBUTES.bInheritHandle = FALSE`, then
   pass only the write end to the child via `SysProcAttr.AdditionalInheritedHandles`.
   This prevents grandchildren from holding the pipe open. Requires
   platform-specific pipe creation code.

2. **Timeout on Wait after process exit.** After `cmd.Process.Wait()`
   returns (process exited), give the pipe copy goroutines a grace window
   (e.g., 5 seconds) then forcibly close the pipes. This would require
   forking Go's exec.Cmd or using `cmd.Process.Wait()` (which doesn't
   wait for pipes) instead of `cmd.Wait()`.

3. **Use `cmd.Process.Wait()` instead of `cmd.Wait()`.** This returns
   immediately when the process exits, ignoring pipe state. But then we
   lose any output that was still buffering. Acceptable for the background
   path where output streams live via the `onChunk` callback anyway.

4. **Close pipe handles from the parent side.** After detecting the
   process has exited (via `cmd.Process.Wait()`), forcibly close the
   read end of the pipes. The copy goroutines will get a read error
   and terminate, unblocking `cmd.Wait()`.

## Recommendation

Fix #3 (use `cmd.Process.Wait()` for the background path) is the
simplest and most correct for the background use case. The background
session already streams output via `onChunk`; the only thing `cmd.Wait()`
adds over `cmd.Process.Wait()` is draining any remaining buffered output,
which can be handled with a short grace window.

# Running Figaro on Windows

> Status: **not supported today.** This document is a port plan, not a recipe.
> Figaro is Linux/macOS-only as it stands. Below is what a Windows port would
> have to address, and which files to touch.

## TL;DR

If you want to *run* Figaro on Windows right now, the only realistic path is
**WSL2**. Build and run the binary inside a WSL distro and it will Just Work —
the Linux build tags and unix sockets are happy there. Native Windows is a
real port, not a flag flip.

## Why it doesn't build on Windows today

The codebase has several Linux/macOS-only assumptions baked in. Each is small
on its own; together they are the port.

### 1. `golang.org/x/sys/unix` is imported unconditionally

`internal/angelus/angelus.go` imports `golang.org/x/sys/unix` to call
`unix.Kill(pid, 0)` for liveness probing of bound shell PIDs. The file even
flags this:

```go
// NOTE: golang.org/x/sys/unix is Linux/macOS only. For future Windows
// support, PID monitoring will need a build-tagged alternative using
// golang.org/x/sys/windows or os.FindProcess + signal probing.
"golang.org/x/sys/unix"
```

**Fix:** split `isAlive` into `angelus_unix.go` / `angelus_windows.go` with
build tags. On Windows use `OpenProcess` + `GetExitCodeProcess`
(via `golang.org/x/sys/windows`) to detect a live PID.

### 2. The `bash` tool literally runs `bash`

`internal/tool/bash.go`:

```go
cmd := exec.Command("bash", "-c", req.Command)
cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
...
syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
```

Two Windows problems:

- No `bash` on a stock Windows box (Git Bash, MSYS2, or WSL provides one).
- `Setpgid` and `syscall.Kill(-pid, ...)` are POSIX. On Windows you'd use a
  Job Object (`CreateJobObject` + `AssignProcessToJobObject` +
  `TerminateJobObject`) so the whole process tree dies on cancel.

**Fix:** factor `killProcessGroup` and the `SysProcAttr` setup into
`bash_unix.go` / `bash_windows.go`. Decide a shell policy: `cmd.exe`,
PowerShell, or require Git Bash on `PATH`. Recommend PowerShell as the
default with an opt-out.

### 3. Daemon detach is `setsid`

`cmd/figaro/detach_unix.go` is already build-tagged `!windows` and returns
`&syscall.SysProcAttr{Setsid: true}`. There is **no `detach_windows.go`**, so
the angelus fork won't compile on Windows.

**Fix:** add `cmd/figaro/detach_windows.go`:

```go
//go:build windows

package main

import "syscall"

func detachAttr() *syscall.SysProcAttr {
    return &syscall.SysProcAttr{
        CreationFlags: 0x00000008 | 0x00000200, // DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
    }
}
```

### 4. SIGTERM / SIGKILL in the rest path

`cmd/figaro/main.go` uses `syscall.Kill(pid, syscall.SIGTERM)` and
`syscall.SIGKILL` to nudge the angelus during `figaro rest`. These don't
exist on Windows.

**Fix:** abstract `killAngelus(pid, force bool)` into platform files. On
Windows, `os.Process.Kill()` is the closest equivalent (it's
`TerminateProcess`); a graceful path can use a named event or just RPC the
angelus over its socket and only fall back to `Kill` after a timeout — which
is mostly what the unix path does already.

### 5. Unix domain sockets everywhere

The whole transport story is unix sockets:

- `$XDG_RUNTIME_DIR/figaro/angelus.sock`
- `$XDG_RUNTIME_DIR/figaro/figaros/<id>.sock`

`internal/transport/transport.go` already abstracts `unix` vs `tcp` schemes,
which helps. Two options:

- **Easy (and fine):** Windows 10 1803+ supports `AF_UNIX` natively and Go's
  `net.Listen("unix", path)` works. Pick a path under `%LOCALAPPDATA%` (see
  §6) and call it a day.
- **Belt-and-braces:** switch the angelus + figaro listeners to TCP on
  `127.0.0.1:0` (random port) and persist the chosen address in a small
  endpoint file the CLI reads. The transport layer is already endpoint-aware,
  so this is a configuration change, not a refactor.

The unix-socket path is simpler and the abstraction is already there. Start
there; fall back to loopback TCP only if you hit ACL grief.

### 6. XDG paths

Hard-coded XDG layout in several places:

| Path | Where |
|------|-------|
| `~/.config/figaro/` | `internal/config/config.go`, `cmd/figaro/main.go` (chalkboard overrides, auth) |
| `~/.local/state/figaro/` | `cmd/figaro/main.go` (`stateDir`), aria backend |
| `$XDG_RUNTIME_DIR/figaro/` (or `$TMPDIR/figaro`) | `angelusRuntimeDir()` in `cmd/figaro/main.go` |

Windows has no `$XDG_RUNTIME_DIR`; `os.TempDir()` falls back, but the right
move is to honour the platform conventions:

- Config → `%APPDATA%\figaro\` (e.g. `os.UserConfigDir()`)
- State / logs → `%LOCALAPPDATA%\figaro\`
- Sockets / runtime → `%LOCALAPPDATA%\figaro\run\`

**Fix:** introduce a single `paths` package (`internal/paths/`) that returns
config / state / runtime dirs, with `paths_unix.go` keeping the current XDG
behaviour and `paths_windows.go` using `os.UserConfigDir()` +
`os.UserCacheDir()`. Then thread it through the four or five callsites that
currently do `os.UserHomeDir() + filepath.Join(...)` by hand.

### 7. File permissions

Plenty of `0o600` / `0o700` calls (auth tokens, aria store, OTel logs). On
Windows these are largely advisory — `os.MkdirAll` and `os.WriteFile` honour
them only loosely. Functionally fine; security-wise you'd want to set proper
ACLs on the auth directory if you care. Out of scope for a first port.

### 8. Multi-call binary (`q`, `l`)

The `q` and `l` shortcuts are user-installed shell aliases (per `flake.nix`
comment), so this is mostly a docs thing on Windows: ship a `q.cmd` /
`q.ps1` that calls `figaro -- %*`.

### 9. Line endings — already handled

`internal/tool/linending.go` detects and round-trips CRLF correctly, so the
`read` / `write` / `edit` tools won't shred Windows files. *Una cosa di meno.*

### 10. Terminal rendering

`largo` and `term.IsTerminal` work on Windows 10+ (VT processing is on by
default in modern terminals). Should be fine in Windows Terminal and
PowerShell 7. Legacy `cmd.exe` may render escape sequences as garbage; tell
users to use Windows Terminal.

## Build & install on Windows (once the port lands)

```powershell
# Prereq: Go 1.25+
git clone https://github.com/jack-work/figaro
cd figaro\main
go build -o figaro.exe .\cmd\figaro
.\figaro.exe login anthropic
.\figaro.exe -- buongiorno
```

`CGO_ENABLED=0` is already the default in `flake.nix` and there's no cgo in
the tree, so cross-compilation from Linux works too:

```bash
GOOS=windows GOARCH=amd64 go build -o figaro.exe ./cmd/figaro
```

— but the binary still won't *run* until the port issues above are fixed.

## Recommended path forward

If someone wants to do this:

1. Add `internal/paths/` and migrate every hard-coded `~/.config` /
   `~/.local/state` / `XDG_RUNTIME_DIR` site to it. Pure refactor, no
   behaviour change on Linux/macOS.
2. Split `internal/angelus/angelus.go` into `_unix.go` / `_windows.go` for
   `isAlive`. Switch the import to `golang.org/x/sys/windows` on Windows.
3. Add `cmd/figaro/detach_windows.go` with the `CREATE_NEW_PROCESS_GROUP`
   flag. Split the `figaro rest` kill helpers similarly.
4. Split `internal/tool/bash.go` for process-group kill. Decide on a shell
   (PowerShell recommended) and rename the tool's user-facing description if
   the shell isn't actually bash. Keep the timeout / streaming logic shared.
5. CI: add a `windows-amd64` build job to whatever runs `go build ./...`.
   Most tests should pass; `bash_test.go` will need a `_unix.go` build tag or
   a PowerShell variant.

Estimate: a focused weekend for a working build, plus a second pass for the
bash-tool process-group story to feel right.

## Until then

WSL2. *Pronto, prontissimo.*

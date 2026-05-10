# CLI Architecture Dossier

## Current Shape

`internal/cli` is a 3,500-line flat package. 19 `runXxx` functions dispatched via a switch statement in `Run()`. No command metadata, no introspection. Three calling conventions coexist:

```
func runXxx(loaded *config.Loaded)                    // manage, system, chalkboard, inspect
func runXxx(loaded *config.Loaded, args []string)     // aria, search, qua
func runXxx(loaded *config.Loaded, prompt string)     // prompt, new, plain
```

### What's here

| File | Lines | Role |
|------|-------|------|
| `cli.go` | 118 | Switch dispatcher |
| `usage.go` | 53 | `printUsage`, `die`, `extractPrompt` |
| `config.go` | 117 | `mustLoadConfig`, `mustHush`, `ensureHush`, `buildChalkboard`, `buildPromptChalkboard` |
| `angelus_client.go` | 111 | `ensureAngelus`, `mustConnectAngelus`, `mustCreateAndBind`, `ariaBackend` |
| `prompt.go` | 153 | `runPrompt`, `runQua`, `runNewPrompt`, `promptAria`, `resolveOrCreate` |
| `plain.go` | 244 | `runPlainPrompt`, `runExecPrompt`, `plainPrompt` |
| `stream.go` | 361 | `mustPromptFigaro` — the rendering state machine |
| `tool_solo.go` | 237 | Single-tool animated spinner |
| `tool_batch.go` | 426 | Parallel-batch row painter |
| `manage.go` | 121 | `runList`, `runKill`, `runAttend`, `runDetach` |
| `chalkboard.go` | 344 | `runSet`, `runUnset`, `runChalkboard`, deep path ops |
| `aria.go` | 324 | `runAria` — history renderer |
| `inspect.go` | 64 | `runRehydrate` |
| `search.go` | 110 | `runSearch` (durable derivations) |
| `system.go` | 164 | `runRest`, `runModels`, `runLogin` |
| `provider.go` | 144 | Provider construction |
| `angelus.go` | 72 | `runAngelus` (internal daemon mode) |

### Session Resolution Pattern (duplicated 6×)

```go
acli := mustConnectAngelus(loaded)   // ensure daemon + connect
defer acli.Close()
r, err := acli.Resolve(ctx, os.Getppid())  // find pid-bound aria
if !r.Found { die(...) }
ep := transport.Endpoint{...}
fcli, err := figaro.DialClient(ep, nil)    // connect to the figaro actor
defer fcli.Close()
```

This appears in: `prompt.go`, `chalkboard.go` (×3: `mustFetchChalkboardKey`, `mustCallSet`, `runChalkboard`), `inspect.go`, `search.go`, `manage.go:runDetach`.

### Command Dependency Tiers

| Tier | What's needed | Commands |
|------|---------------|----------|
| **None** | Config only | `login`, `models`, `stop` |
| **Angelus** | Daemon connection | `list`, `kill`, `attend` |
| **Session** | Angelus + resolved figaro | `<prompt>`, `new`, `set`, `unset`, `state`, `rehydrate`, `derive`, `aria` |
| **Ephemeral** | Angelus + throwaway figaro | `plain`, `x` |
| **Internal** | Full daemon runtime | `--angelus` |

---

## Architectural Problems

### 1. No command definition layer

Commands exist only as switch cases + function names. There's no struct describing a command's name, aliases, accepted flags, required args, tier, or help text. Consequences:
- Can't generate completions (nothing to introspect)
- Can't generate per-command `--help` (no metadata)
- Can't do did-you-mean (no command list to match against)
- Adding a command means editing the switch + writing a function + updating `printUsage` — three locations, no compile-time check they agree

### 2. Session resolution is middleware, not library calls

Six call sites repeat the resolve-connect dance. When the server protocol changes (you said this is coming), all six break independently. This should be a single `withSession(loaded, func(fcli *figaro.Client, ariaID string) error)` or a `Context` that carries the resolved session.

### 3. Output has no abstraction

25+ raw ANSI escapes, no `NO_COLOR` check, no configurable palette, no JSON-output mode toggle. The tool animation system (`tool_solo`, `tool_batch`) is genuinely well-built but hardcodes colors at the call site. A minimal `term` package would:
- Check `NO_COLOR` / `FORCE_COLOR` / TTY once at init
- Expose semantic functions: `Dim`, `Error`, `Success`, `Running`
- Allow palette override via config
- Gate all ANSI behind a single `Enabled` bool

### 4. Arg parsing is ad-hoc and inconsistent

- Some commands read `os.Args[2:]` (bypassing the dispatcher's slice)
- Some commands manually loop for flags (`-n`, `--dry-run`)
- Some expand bundled short flags (`-alv`), others don't
- Unknown flags are silently ignored in most commands, caught in one
- No shared pattern for "parse these known flags + positional args, reject unknowns, handle `--help`"

### 5. The rendering state machine is the right design but isn't separable

`mustPromptFigaro` in `stream.go` is a correct, well-documented state machine. But it's entangled with session setup, config reading, and otel spans. If you want a second frontend (or want `plainPrompt` to share rendering infrastructure), you have to fork the whole function.

### 6. `--angelus` lives in user namespace

The re-exec mechanism works (spawn self with `--angelus` to start daemon). But it occupies the same flag namespace as user flags. When you add `--help`, `--version`, `--json`, etc., you need to be careful this internal flag doesn't collide. Env var (`_FIGARO_DAEMON=1`) or underscore subcommand (`figaro _daemon`) are the conventional solutions.

---

## Proposed Architecture

Three new internal packages, one refactored `internal/cli`:

```
internal/
├── cli/
│   ├── cmd/              ← command definitions (one file per command group)
│   │   ├── prompt.go     ← figaro --, new, plain, x
│   │   ├── session.go    ← attend, detach, list, kill, stop
│   │   ├── state.go      ← set, unset, state, rehydrate, derive
│   │   ├── system.go     ← login, models
│   │   └── aria.go       ← aria (history viewer)
│   ├── render/           ← stream rendering (extracted from stream.go)
│   │   ├── stream.go     ← mustPromptFigaro state machine
│   │   ├── tool_solo.go
│   │   ├── tool_batch.go
│   │   └── plain.go      ← plainPrompt
│   ├── cli.go            ← Router.Run() entrypoint
│   └── context.go        ← session resolution middleware
├── term/                 ← reusable terminal output package
│   ├── color.go          ← semantic palette, NO_COLOR, FORCE_COLOR
│   ├── width.go          ← terminal width detection
│   ├── spinner.go        ← braille spinner utility (extracted)
│   └── output.go         ← Writer with format modes (text/json/plain)
└── cmdkit/               ← reusable command framework (extractable to other projects)
    ├── command.go        ← Command struct, flag defs, aliases
    ├── router.go         ← dispatch, --help, --version, did-you-mean, completions
    ├── flags.go          ← lightweight flag parsing with unknown-flag rejection
    └── complete.go       ← bash/zsh/fish completion generation
```

### The `cmdkit.Command` type

```go
type Command struct {
    Name        string
    Aliases     []string
    Short       string            // one-line for help listing
    Long        string            // per-command --help body
    Usage       string            // usage line template
    Flags       []Flag
    Args        ArgSpec           // positional arg rules
    Hidden      bool              // internal commands (_daemon)
    Run         func(ctx *Context) error
}

type Context struct {
    Args     []string          // positional args after flag parsing
    Flags    map[string]string // parsed flags
    RawArgs  []string          // everything after --
    Out      *term.Output      // color-aware, format-aware writer
}
```

### The session middleware

```go
// In internal/cli/context.go

type Session struct {
    Loaded   *config.Loaded
    Angelus  *angelus.Client
    Figaro   *figaro.Client
    AriaID   string
    Endpoint transport.Endpoint
}

// WithSession resolves the pid-bound session, calls fn, and cleans up.
func WithSession(loaded *config.Loaded, fn func(s *Session) error) error { ... }

// WithAngelus connects to the daemon only (for list, kill, etc.)
func WithAngelus(loaded *config.Loaded, fn func(acli *angelus.Client) error) error { ... }
```

Commands declare their tier; the router calls the appropriate middleware before invoking `Run`.

### The `term` package

```go
// Reusable across projects. No figaro-specific knowledge.

var Enabled bool // set at init from NO_COLOR + FORCE_COLOR + IsTerminal

func Dim(s string) string
func Red(s string) string
func Green(s string) string
func Cyan(s string) string

func Width() int  // cached terminal width

type Output struct {
    w      io.Writer
    format Format  // Text | JSON | Plain
}
func (o *Output) Println(s string)
func (o *Output) Table(headers []string, rows [][]string)
func (o *Output) JSON(v any)
```

---

## Decisions (from your responses)

| Item | Decision |
|------|----------|
| `-s`/`--search` | Rename to `figaro derive` subcommand |
| `rest` | Rename to `figaro stop` |
| `--angelus` | Move to env var or `_daemon` subcommand |
| `label`, `context` | Remove from usage (dead commands) |
| `chalkboard`/`state` | Document alias; migrate to `state` over time |
| `--json` on `list`/`models` | Add |
| Exit codes | Differentiate: 1=runtime, 2=usage, 130=interrupt |
| `figaro x` confirmation | Add explicit `[y/N]` prompt |
| Did-you-mean | Levenshtein against command list (trivial with a registry) |
| Shell completions | Generate from command registry |
| Per-command `--help` | Generate from Command.Long + Command.Flags |
| `NO_COLOR` / `FORCE_COLOR` | Centralize in `term` package |
| Configurable palette | Config-driven semantic colors in `term` |
| Status line width bug | Use `runewidth` instead of `len()` |
| Daemon startup feedback | Spinner/message during `ensureAngelus` poll |
| Help grouping | Commands grouped by tier in top-level help |

---

## Implementation Order

### Phase 1: Extract foundations (no behavior change)

1. **Create `internal/term`** — move color/width/TTY logic out. Replace all 25+ raw ANSI strings with calls. Add `NO_COLOR`/`FORCE_COLOR`. This is a mechanical find-replace with a test that the output bytes don't change when colors are enabled.

2. **Create `internal/cmdkit`** — define the `Command` type and a `Router` that dispatches from a registry. Port the existing switch statement into a slice of `Command` literals. Each command's `Run` still calls the existing `runXxx` — it's just a routing indirection, not a rewrite. Gain: `--help`, did-you-mean, completion generation fall out immediately.

3. **Normalize arg threading** — every `runXxx` receives `(loaded, args []string)`. No more `os.Args[2:]`. The router provides the tail. One mechanical pass, compile-verifiable.

### Phase 2: Session middleware + output (moderate refactor)

4. **Introduce `Session` / `WithSession` / `WithAngelus`** — the 6× resolve pattern becomes a one-liner in each command. When your server protocol changes, you change one function.

5. **`--json` flag on `list`, `models`, `state`, `derive`** — trivial once `term.Output` exists (check `ctx.Out.Format == JSON`, marshal, done).

6. **Extract `internal/cli/render`** — move `mustPromptFigaro`, `toolSoloState`, `toolBatchState`, `plainPrompt` into a sub-package. They receive a `render.Config` (stream CPS, palette, etc.) and a transport endpoint. The command handlers become: resolve session → call render function.

### Phase 3: Polish (UX/contract fixes)

7. **Rename commands** — `rest` → `stop`, `-s` → `derive`. Add the old names as hidden aliases for one release cycle (print deprecation to stderr).

8. **Move `--angelus`** to `_FIGARO_DAEMON=1` env var.

9. **Add `--version`** with build-time `ldflags`.

10. **Shell completions** — `figaro completion bash|zsh|fish` generates a script from the command registry. Standard pattern; the registry already has everything needed.

11. **Grouped help output** — the router's `printUsage` iterates commands by group tag, prints headers.

---

## Design Properties for Future-Proofing

**Server protocol changes**: All RPC interaction is behind `Session` / `WithAngelus`. New methods, changed signatures, additional handshakes — one file changes, all commands get it.

**New commands**: Add a `Command` literal to the appropriate file in `cmd/`. The router handles dispatch, help, completions automatically.

**New output formats**: Commands write to `ctx.Out`. Adding a `--yaml` or `--toml` mode means adding a `Format` variant, not touching every command.

**Extraction to other projects**: `cmdkit` and `term` have zero figaro imports. They're generic CLI infrastructure. A future project can vendor or module-import them directly.

**Multiple frontends**: The `render` sub-package takes a transport endpoint and a config. A TUI, a web socket bridge, or a test harness can call the same rendering pipeline with a different output writer.

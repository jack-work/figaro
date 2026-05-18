# Completion

Shell-completion subsystem for figaro. Two layers:

- **cmdkit** (`internal/cmdkit/complete.go`) owns the dispatch protocol and emits the shell scripts.
- **cli** (`internal/cli/complete_*.go`) owns the candidate sources.

## Install

```
figaro completion install [bash|zsh|fish]
```

Writes a script to the shell's autoload path. The script is generated
by the *currently installed binary* — upgrading figaro does not
rewrite the script. Re-run install after any change to the completion
surface.

## Dispatch protocol

The generated script calls a hidden subcommand on every tab:

```
figaro __complete <verb> -- <tokens-before-cursor>
```

`__complete` looks up the verb, invokes its `CompleteArgs` callback,
and prints one candidate per line on stdout. Unknown verb, missing
callback, dial errors, timeouts — all return silently with exit 0.
Completion must never appear broken.

### CompleteContext

The callback receives:

- `Args []string` — tokens before the cursor, with the dispatcher's
  own `--` boundary marker stripped. A user-typed `--` is preserved.
- `PastSeparator bool` — true iff a `--` token appears in `Args`.
- `Extra interface{}` — mirrors `Router.Extra` (typically `*config.Loaded`).

The dispatcher strips at most one leading `--` from `Args` so a user's
`--` sitting in second position (the prompt-body separator in
`verb -- <body>`) survives.

### Bare-prompt sentinel

When the verb position is literally `--` (i.e. the user invoked
`figaro -- <body>` or an alias of it), the shell-side script
substitutes the verb with `__bare_prompt` before calling `__complete`.
The dispatcher recognizes this sentinel, forces `PastSeparator=true`,
and routes to the callback registered via `Router.SetBarePromptComplete`.

In fish, the generated script also suppresses the subcommand list in
the bare-prompt form via an `__<name>_is_bare_prompt` guard, then
adds a parallel rule that routes to the dynamic pool at the
subcommand position.

## Candidate sources

All sources fetch over RPC, never the local filesystem. This keeps
the CLI deployable against a remote angelus.

### Aria ids (`complete_aria.go`)

`softFetchAriaIDs` calls `angelus.Client.List` with a 300ms deadline.
Returns sorted ids, or nil on any failure.

Two helpers consume it:

- `completeAriaIDsAfterFlag(inner)` — emits aria ids when the
  previous token is `--id`; otherwise delegates to `inner` (which may
  be nil).
- `completeAriaIDsPositionalOrFlag` — emits aria ids when the cursor
  is at the first positional slot (no prior args) or right after
  `--id`. Used by `attend`, `kill`, `status`.

### Chalkboard keys (`complete_chalkboard.go`)

`completeChalkboardKeys` returns the union of:

- well-known keys from `chalkboard.WellKnownKeys()`, with templated
  keys (e.g. `system.environment.<name>`) expanded per the env
  allowlist.
- live snapshot keys from `softFetchLiveKeys`, which calls
  `figaro.Client.Chalkboard` for the pid-bound aria with a 300ms
  deadline.

Used by `set` and `unset`.

### Prompt context (`complete_promptctx.go`)

`completePromptContext` returns chalkboard keys + CWD entries:

- `listCWD` reads the working directory via `os.ReadDir`. Directories
  get a trailing `/`. Hidden entries are filtered. Names containing
  shell-unsafe characters (whitespace, quotes, glob chars, etc.) are
  dropped because the bash/zsh scripts feed candidates through
  `compgen -W`, which word-splits on IFS.
- Fish's native file completion is **not** disabled, so it
  supplements our filtered list with dotfiles and quoted names.

`completePromptOrIDFlag` composes the above with aria-id-after-flag
handling. Used by `send`, `plain`, `x`, `new`.

`completePromptContext` is also wired as the bare-prompt callback in
`buildRouter`, so `q ` and `figaro -- ` see the same pool.

## Per-command wiring

| Command | CompleteArgs |
|------------------|----------------------------------------------------|
| `show` | `completeAriaIDsAfterFlag(nil)` |
| `send` | `completePromptOrIDFlag` |
| `new` | `completePromptOrIDFlag` |
| `plain` | `completePromptOrIDFlag` |
| `x` | `completePromptOrIDFlag` |
| `attend` | `completeAriaIDsPositionalOrFlag` |
| `kill` | `completeAriaIDsPositionalOrFlag` |
| `status` | `completeAriaIDsPositionalOrFlag` |
| `state` | `completeAriaIDsAfterFlag(nil)` |
| `set` | `completeAriaIDsAfterFlag(completeChalkboardKeys)` |
| `unset` | `completeAriaIDsAfterFlag(completeChalkboardKeys)` |
| `rehydrate` | `completeAriaIDsAfterFlag(nil)` |
| bare prompt (`--`) | `completePromptContext` |

## Known limitations

- Filenames with whitespace or shell metacharacters are silently
  dropped from `listCWD`. Fix requires reworking the bash/zsh scripts
  to use `mapfile` instead of `compgen -W`.
- Hidden files are never suggested by `listCWD`. Fish covers them via
  native file completion; bash/zsh do not.
- `softFetchAriaIDs` and `softFetchLiveKeys` bound at 300ms. A
  sluggish daemon can make tab feel laggy but never blocks long.
- Upgrading figaro does not regenerate the on-disk completion script.
  Re-run `figaro completion install` after any change to the
  completion surface.

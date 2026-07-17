package chalkboard

// The chalkboard is open-schema by design (see chalkboard.go): keys
// are arbitrary, values are raw JSON. WellKnownKeys is a *partial*
// schema — a curated list of keys the harness reads or writes today,
// used to drive CLI completion and as a discoverability surface.
//
// Future direction: tighten this into a real partial schema (per-key
// value shape, namespace rules) while keeping the surrounding map
// open. Anything not in the catalog remains valid; the catalog is
// advisory, never enforced.

// KeyMode classifies a known key by who normally writes it.
type KeyMode int

const (
	// KeyUserSettable: meant to be set by `figaro set`. Configures
	// harness behavior.
	KeyUserSettable KeyMode = iota

	// KeySystemManaged: written by the harness (providers, derive
	// pipeline). Read-only from the agent's perspective.
	// derived metrics). Visible in `figaro state`; setting by hand is
	// rarely meaningful and may be overwritten.
	KeySystemManaged

	// KeyEphemeralPerTurn: rewritten on every prompt from CLI-side
	// context (cwd, datetime, allowlisted env vars). Setting by hand
	// is overwritten on the next prompt.
	KeyEphemeralPerTurn
)

// KeyDoc describes a known chalkboard key.
type KeyDoc struct {
	// Key is the dotted path. A trailing "<name>" marks a templated
	// namespace (e.g. "system.environment.<name>") — completion
	// should treat the literal segment as a placeholder.
	Key string

	// Short is a one-line description shown in completion menus.
	Short string

	// Mode classifies the writer (see KeyMode).
	Mode KeyMode
}

// WellKnownKeys returns the curated catalog of chalkboard keys the
// harness reads or writes. Order is stable; callers may filter by Mode.
func WellKnownKeys() []KeyDoc {
	return []KeyDoc{
		{Key: "system.credo", Short: "Credo source (string or {content,filePath,frontmatter}); providers read this as the system prompt", Mode: KeyUserSettable},
		{Key: "system.tags", Short: "Per-LT annotations (e.g. system.tags[42].cache_control)", Mode: KeyUserSettable},
		{Key: "system.cache_control", Short: `Auto cache-marker policy; ON by default (short). "none" disables; "5m"/"1h" force a TTL`, Mode: KeyUserSettable},
		{Key: "system.thinking_budget", Short: "Extended-thinking token budget for budget-based models (>=1024 enables; unset/0 = off)", Mode: KeyUserSettable},
		{Key: "system.model", Short: "Active provider model; switchable between turns", Mode: KeyUserSettable},
		{Key: "system.max_tokens", Short: "Maximum output tokens for the next provider response", Mode: KeyUserSettable},
		{Key: "system.context_tier", Short: `Copilot context-budget tier: "default" or "long_context"`, Mode: KeyUserSettable},
		{Key: "system.max_context_tokens", Short: "Optional local cap for replayed prompt context tokens", Mode: KeyUserSettable},
		{Key: "system.thinking_effort", Short: "Reasoning effort for models that support it", Mode: KeyUserSettable},
		{Key: "system.reasoning_context", Short: `Copilot Responses reasoning retention: "auto", "current_turn", or "all_turns"`, Mode: KeyUserSettable},
		{Key: "system.reasoning_summary", Short: `Copilot Responses readable reasoning summary: "auto", "concise", or "detailed"`, Mode: KeyUserSettable},
		{Key: "system.verbosity", Short: "Copilot Responses text verbosity", Mode: KeyUserSettable},
		{Key: "system.temperature", Short: "Copilot Responses sampling temperature (0 through 2; mutually exclusive with top_p)", Mode: KeyUserSettable},
		{Key: "system.top_p", Short: "Copilot Responses nucleus sampling (greater than 0 through 1; mutually exclusive with temperature)", Mode: KeyUserSettable},
		{Key: "system.parallel_tool_calls", Short: "Whether Copilot Responses may emit parallel function calls", Mode: KeyUserSettable},
		{Key: "system.environment.<name>", Short: "Allowlisted env var capture", Mode: KeyUserSettable},

		{Key: "system.cwd", Short: "Canonical working directory (set at create time)", Mode: KeySystemManaged},
		{Key: "model", Short: "Active model ID", Mode: KeySystemManaged},
		{Key: "root", Short: "Project root path", Mode: KeySystemManaged},
		{Key: "token_budget", Short: "Context window usage indicator", Mode: KeySystemManaged},
		{Key: "truncation", Short: "Last tool truncation notice", Mode: KeySystemManaged},

		{Key: "cwd", Short: "Per-turn shell working directory", Mode: KeyEphemeralPerTurn},
		{Key: "datetime", Short: "Per-turn wall-clock time", Mode: KeyEphemeralPerTurn},
	}
}

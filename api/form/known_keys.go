package form

// The form is open-schema; WellKnownKeys is a partial catalog of the keys the
// harness reads or writes.

// KeyMode classifies a known key by who writes it.
type KeyMode int

const (
	KeyUserSettable KeyMode = iota
	// KeySystemManaged keys are written by the harness and may be
	// overwritten if set by hand.
	KeySystemManaged
	// KeyEphemeralPerTurn keys are rewritten on every prompt.
	KeyEphemeralPerTurn
)

type KeyDoc struct {
	// Key is the dotted path; a "<name>" segment is a placeholder.
	Key   string
	Short string
	Mode  KeyMode
}

// WellKnownKeys is in stable order.
func WellKnownKeys() []KeyDoc {
	return []KeyDoc{
		{Key: "duke-title", Short: `What this aria calls its END USER (default "user"); set it in an outfit and a human's prompts are attributed to that name`, Mode: KeyUserSettable},
		{Key: "system.credo", Short: "Credo source (string or {content,filePath,frontmatter}); providers read this as the system prompt", Mode: KeyUserSettable},
		{Key: "system.tags", Short: "Per-LT annotations (e.g. system.tags[42].cache_control)", Mode: KeyUserSettable},
		{Key: "system.cache_control", Short: `Auto cache-marker policy; ON by default (short). "none" stops SIGNALLING (implicit-caching providers cache anyway); "5m"/"1h" force a TTL`, Mode: KeyUserSettable},
		{Key: "system.cache_markers", Short: `How a route is marked: "auto" (default), "blocks", "top-level", "none"`, Mode: KeyUserSettable},
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
		{Key: "system.study_incantation", Short: `What to SAY at a study lifecycle event: {"onstudy":…,"onupdate":…,"ondrop":…}, any subset, strings`, Mode: KeyUserSettable},
		{Key: "system.fork_incantation", Short: `What to say to a branch at birth: a string, or {"onfork":…}. Unset means silence, which is the historical behaviour`, Mode: KeyUserSettable},
		{Key: "system.cwd", Short: "Working directory the tools resolve relative paths against (`figaro cd`)", Mode: KeyUserSettable},

		{Key: "system.forked_from", Short: "The trunk this branch was forked from (stamped at birth)", Mode: KeySystemManaged},
		{Key: "system.tombstone", Short: "Set when a form is deleted: the record subscribers hear the death through, after which the form is sealed", Mode: KeySystemManaged},
		{Key: "system.studies", Short: "The forms this figaro observes. Written by the study verb only: each entry is refcounted on a shared libretto, and a hand-written one would declare a study nothing counted", Mode: KeySystemManaged},
		{Key: "model", Short: "Active model ID", Mode: KeySystemManaged},
		{Key: "token_budget", Short: "Context window usage indicator", Mode: KeySystemManaged},
		{Key: "truncation", Short: "Last tool truncation notice", Mode: KeySystemManaged},

		{Key: "datetime", Short: "Per-turn wall-clock time", Mode: KeyEphemeralPerTurn},
	}
}

// IntrinsicKeys catalogs a non-persistent builtin form addressed as
// `<host>/<name>`; no set reaches these keys.
func IntrinsicKeys(intrinsic string) []KeyDoc {
	switch intrinsic {
	case "runtime":
		return []KeyDoc{
			{Key: "turn.state", Short: `What the aria is DOING: "idle", "accepted", "committing", "thinking", "tooling"`, Mode: KeySystemManaged},
			{Key: "turn.id", Short: "The in-flight turn id", Mode: KeySystemManaged},
			{Key: "turn.since", Short: "Unix millis of the last transition", Mode: KeySystemManaged},
			{Key: "turn.reason", Short: "The verdict of the turn that just ended; carried only into idle", Mode: KeySystemManaged},
			{Key: "inflight", Short: "Messages accepted and not yet answered", Mode: KeySystemManaged},
			{Key: "epoch", Short: "The inbox generation queue ids belong to", Mode: KeySystemManaged},
			{Key: "model", Short: "Active model id", Mode: KeySystemManaged},
		}
	case "queue":
		return []KeyDoc{
			{Key: "epoch", Short: "The generation these ids belong to; they restart with it", Mode: KeySystemManaged},
			{Key: "len", Short: "How many messages are in flight", Mode: KeySystemManaged},
			{Key: "order", Short: "FIFO order, as ids. A form is a map and a map has none", Mode: KeySystemManaged},
			{Key: "items.<id>.text", Short: "The message, verbatim", Mode: KeySystemManaged},
			{Key: "items.<id>.sender", Short: "Who asked", Mode: KeySystemManaged},
			{Key: "items.<id>.state", Short: `Where it is: "queued", "committing", "committed", "merged", "dropped", "drained"`, Mode: KeySystemManaged},
			{Key: "items.<id>.at", Short: "Accepted-at, unix millis", Mode: KeySystemManaged},
			{Key: "items.<id>.merged", Short: "Ids an interrupt folded INTO this message", Mode: KeySystemManaged},
			{Key: "items.<id>.into", Short: "For a merged message, the surviving id", Mode: KeySystemManaged},
			{Key: "items.<id>.turn", Short: "For a committed message, the turn it became", Mode: KeySystemManaged},
		}
	}
	return nil
}

func IntrinsicNames() []string { return []string{"runtime", "queue"} }

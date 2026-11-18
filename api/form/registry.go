package form

import (
	"math"
	"slices"
	"strings"
)

type Writer int

const (
	WriterSelf Writer = iota
	WriterOwner
	WriterLineage
	WriterRuntime
)

var writerNames = [...]string{"self", "owner", "lineage", "runtime"}

func (w Writer) String() string {
	if w < 0 || int(w) >= len(writerNames) {
		return "unknown"
	}
	return writerNames[w]
}

func Writers() []Writer {
	return []Writer{WriterSelf, WriterOwner, WriterLineage, WriterRuntime}
}

type Spec struct {
	Path      string
	Writer    Writer
	Exact     bool
	Ephemeral bool
	Operator  bool
	Shape     Shape
	Short     string
}

var registry = withLegacyRuntimeRows([]Spec{
	{Path: "duke-title", Short: `What this aria calls its END USER (default "user"); set it in an outfit and a human's prompts are attributed to that name`, Writer: WriterSelf, Shape: ShapeString()},
	{Path: "target-aria", Short: "The figaro a role form points at: its holder. Protected: only the owner or an ancestor figaro writes it, and cast checks its target", Writer: WriterLineage, Shape: ShapeString()},
	{Path: "succession-policy", Short: "Owner-held; how a role's holder hands it over: the outfit the daemon dresses a successor in as the owner, thresholds, variants, the handoff key", Writer: WriterOwner, Shape: ShapeObject()},
	{Path: "system.credo", Short: "Credo source (string or {content,filePath,frontmatter}); providers read this as the system prompt. Fixed at birth: set by the outfit or birth patch, refused once the figaro has spoken (fork -S or succeed for another)", Writer: WriterOwner, Shape: ShapeStringOrObject()},
	{Path: "system.provider", Short: "Provider this figaro talks to (anthropic, copilot, ...); switching mid-conversation is the owner's call", Writer: WriterOwner, Shape: ShapeString()},
	{Path: "system.tags", Short: "Per-LT annotations (e.g. system.tags[42].cache_control)", Writer: WriterSelf, Shape: ShapeAny()},
	{Path: "system.cache_control", Short: `Auto cache-marker policy; ON by default (short). "none" stops SIGNALLING (implicit-caching providers cache anyway); "5m"/"1h" force a TTL`, Writer: WriterSelf, Shape: ShapeEnumFold("none", "off", "false", "disabled", "short", "5m", "long", "1h", "60m")},
	{Path: "system.cache_markers", Short: `How a route is marked: "auto" (default), "blocks", "top-level", "none"`, Writer: WriterSelf, Shape: ShapeEnumFold("auto", "blocks", "block", "explicit", "top-level", "toplevel", "top_level", "automatic", "none", "off", "false")},
	{Path: "system.thinking_budget", Short: "Extended-thinking token budget for budget-based models (>=1024 enables; unset/0 = off)", Writer: WriterSelf, Shape: ShapeInt(0, math.MaxInt64)},
	{Path: "system.model", Short: "Active provider model; switchable between turns", Writer: WriterOwner, Shape: ShapeString()},
	{Path: "system.max_tokens", Short: "Maximum output tokens for the next provider response", Writer: WriterOwner, Shape: ShapeInt(1, math.MaxInt64)},
	{Path: "system.max_request_bytes", Short: "Largest request body figaro sends to the provider, in bytes; over it the oldest images are elided first (default: max_request_bytes in providers/<name>.toml; 0 = no limit)", Writer: WriterSelf, Shape: ShapeByteCount()},
	{Path: "system.first_byte_timeout", Short: `How long a provider request may wait for its first byte before it counts as a stream cut and is resent (Go duration, default "90s"; "0" turns it off)`, Writer: WriterSelf, Shape: ShapeTimeout()},
	{Path: "system.context_tier", Short: `Copilot context-budget tier: "default" or "long_context"`, Writer: WriterSelf, Shape: ShapeEnum("default", "long_context")},
	{Path: "system.max_context_tokens", Short: "Optional local cap for replayed prompt context tokens", Writer: WriterOwner, Shape: ShapeInt(1, math.MaxInt64)},
	{Path: "system.thinking_effort", Short: "Reasoning effort for models that support it", Writer: WriterSelf, Shape: ShapeString()},
	{Path: "system.reasoning_context", Short: `Copilot Responses reasoning retention: "auto", "current_turn", or "all_turns"`, Writer: WriterSelf, Shape: ShapeEnum("auto", "current_turn", "all_turns")},
	{Path: "system.reasoning_summary", Short: `Copilot Responses readable reasoning summary: "auto", "concise", or "detailed"`, Writer: WriterSelf, Shape: ShapeEnum("auto", "concise", "detailed")},
	{Path: "system.verbosity", Short: "Copilot Responses text verbosity", Writer: WriterSelf, Shape: ShapeString()},
	{Path: "system.temperature", Short: "Copilot Responses sampling temperature (0 through 2; mutually exclusive with top_p)", Writer: WriterSelf, Shape: ShapeNumber(0, 2, false)},
	{Path: "system.top_p", Short: "Copilot Responses nucleus sampling (greater than 0 through 1; mutually exclusive with temperature)", Writer: WriterSelf, Shape: ShapeNumber(0, 1, true)},
	{Path: "system.parallel_tool_calls", Short: "Whether Copilot Responses may emit parallel function calls", Writer: WriterSelf, Shape: ShapeBool()},
	{Path: "system.environment.<name>", Short: "Allowlisted env var capture", Writer: WriterOwner, Shape: ShapeString()},
	{Path: "system.study_incantation", Short: `What to SAY at a study lifecycle event: {"onstudy":…,"onupdate":…,"ondrop":…}, any subset, strings; a bare string is onstudy alone`, Writer: WriterSelf, Shape: ShapeIncantation(studyIncantationFields...)},
	{Path: "system.fork_incantation", Short: `What to say to a branch at birth: a string, or {"onfork":…}. Unset means silence, which is the historical behaviour`, Writer: WriterSelf, Shape: ShapeIncantation(forkIncantationFields...)},
	{Path: "system.decide.provider", Short: "The provider `figaro decide` sends through: its base_url and credential (rou2, azure, ...); set in an outfit, core.toml for every aria", Writer: WriterSelf, Shape: ShapeString()},
	{Path: "system.decide.model", Short: "The decision model `figaro decide` asks (openai/gpt-6-luna, explabs/jev-latest, decision-1); never a conversation model", Writer: WriterSelf, Shape: ShapeString()},
	{Path: "system.decide.base_url", Short: "Where decisions go when the provider's base_url serves turns elsewhere (Azure's decision-1 at .../providers/microsoft/v1); the credential stays the provider's", Writer: WriterOwner, Shape: ShapeString()},
	{Path: "system.decide.api", Short: "The decision API `figaro decide` speaks for this aria: systemone (jev, decision-1) or openai (luna); default inferred from the model", Writer: WriterSelf, Shape: ShapeEnum("systemone", "openai")},
	{Path: "system.methods.enabled", Short: "Master switch for methods (default true); false and no method runs", Writer: WriterOwner, Shape: ShapeBool()},
	{Path: "system.methods.<name>", Short: `A method: on each inquiry the decision model picks one of {options} from the rendered {context} ({{inquiry}}, {{inquiry~N}}) and writes it to {key}; {score, default, enabled, event, keep_cache} optional; keep_cache (default true) holds a method whose change would break the prompt cache`, Writer: WriterOwner, Shape: ShapeObject()},
	{Path: "system.tools", Short: "Owner-held allowlist of tool names the aria may use; [] means none, unset means all. Owner-held does not bind an unboxed aria whose tools include bash or process (issue #57)", Writer: WriterOwner, Shape: ShapeStringList()},
	{Path: "system.cwd", Short: "Working directory the tools resolve relative paths against (`figaro cd`)", Writer: WriterSelf, Shape: ShapeString()},
	{Path: "system.retirement", Short: `Owner-held {context_threshold, fallback_tokens, ttl, action, page, timeout, ramp, ramp_turns}: fallback_tokens is an absolute token threshold when the context capacity is unknown; otherwise context_threshold applies. At the end of a turn whose last request carried at least context_threshold input tokens (a count, or "60%" of the context window), the aria retires itself (system.retired = "context"); ttl counts from then. action = "succeed" hands over first: a warning at 80%, one final handoff turn, a successor minted, cast into every role the aria holds and prompted to ramp on, and the aria retires only when that first turn ends; a failure or timeout leaves it unretired and tells page (an aria or @role; default the holder of its drawn parent)`, Writer: WriterOwner, Shape: ShapeRetirement()},
	{Path: SuccessionWarnedKey, Short: "Set once the aria has been told to start handing over (80% of its retirement threshold)", Writer: WriterRuntime, Exact: true, Shape: ShapeAny()},
	{Path: "system.succession_failed", Short: "Owner-held; why the aria's succession stopped. While set no new succession starts; unset it to let the next turn try again", Writer: WriterOwner, Shape: ShapeString()},
	{Path: "system.sandbox", Short: "Protected; the sandbox the aria's tools run in (a box name, or unset for local). A figaro cannot set its own: only the owner or an ancestor figaro of the aria ([authz] protected_keys)", Writer: WriterLineage, Shape: ShapeString()},
	{Path: "system.scopes", Short: "Protected; the aria's scopes as a JSON array of verb:object strings, narrowing its parent's and enforced on calls that name it. Providers read the effective set from figaro.scopes, not this key. Same writers as system.sandbox; a figaro gives only what it holds. system.grants is its old name: still read, no longer written", Writer: WriterLineage, Shape: ShapeScopes()},
	{Path: RetiredGrantsKey, Short: "Retired name of system.scopes: still read, never written; a write is refused with the new name", Writer: WriterLineage, Shape: ShapeScopes()},
	{Path: FanoutMaxChildrenKey, Short: "Protected; how many children the figaro may fork (non-negative integer). Same writers as system.scopes", Writer: WriterLineage, Shape: maxChildrenShape},
	{Path: ChildPolicyKey, Short: `Protected; a template applied in the birth patch of every fork of this figaro: an object of key paths to values, applied key by key (a nested object names the key paths below it; scopes, fanout, incantations; owner-held keys such as system.model only when the owner writes them). Applied at birth, never kept linked. Unset, a fork made by a figaro gets system.scopes = ["send:parent", "read:aria:self", "write:aria:self#@self", "delete:aria:self#@self"]; a fork made by the owner inherits as before. Same writers as system.scopes`, Writer: WriterLineage, Shape: ShapeObject()},
	{Path: FanoutMintKey, Short: "Protected; true lets the figaro mint a new one with figaro new. Same writers as system.scopes", Writer: WriterLineage, Shape: mintShape},
	{Path: "system.keep", Short: "Owner-held; true and `figaro gc` never chooses this aria, nor anything whose delete would take it", Writer: WriterOwner, Shape: ShapeTruthy()},
	{Path: CheckpointMarksKey, Short: "Logical times this form's state is checkpointed at, keyed by version: the tail mark moves with the writes, and past system.checkpoint.max_marks the least recently used goes", Writer: WriterRuntime, Exact: true, Shape: ShapeAny()},
	{Path: "system.checkpoint.every_patches", Short: "Move the tail checkpoint every N records (floor 64); unset takes [store] checkpoint_every_patches", Writer: WriterSelf, Shape: ShapeInt(math.MinInt64, math.MaxInt64)},
	{Path: "system.checkpoint.every_seconds", Short: "Move the tail checkpoint every N seconds when something was written (floor 5); unset takes [store] checkpoint_every_seconds", Writer: WriterSelf, Shape: ShapeInt(math.MinInt64, math.MaxInt64)},
	{Path: "system.checkpoint.max_marks", Short: "How many checkpoints the form keeps (floor 2, default 5)", Writer: WriterSelf, Shape: ShapeInt(math.MinInt64, math.MaxInt64)},
	{Path: "system.retention.mode", Short: `"marks" drops this form's records below its lowest live checkpoint mark, "none" keeps every record; unset takes [store] retention_mode, and with that unset the store-wide sweep decides. Owner-held; refused on a conversation-bound form`, Writer: WriterOwner, Shape: ShapeEnum("marks", "none")},
	{Path: "system.retention.writers", Short: "Figaro ids or principals the owner lets set system.retention.* besides itself; only the owner sets this", Writer: WriterOwner, Shape: ShapeStringList()},
	{Path: ForkedFromKey, Short: "The trunk this branch was forked from (stamped at birth)", Writer: WriterRuntime, Exact: true, Shape: ShapeString()},
	{Path: TombstoneKey, Short: "Set when a form is deleted: the record subscribers hear the death through, after which the form is sealed", Writer: WriterRuntime, Exact: true, Shape: ShapeAny()},
	{Path: StudiesKey, Short: "The forms this figaro observes. Written by the study verb only: each entry is refcounted on a shared libretto, and a hand-written one would declare a study nothing counted", Writer: WriterRuntime, Exact: true, Shape: ShapeAny()},
	{Path: "model", Short: "Active model ID", Writer: WriterRuntime, Exact: true, Shape: ShapeAny()},
	{Path: "token_budget", Short: "Context window usage indicator", Writer: WriterRuntime, Exact: true, Shape: ShapeAny()},
	{Path: "truncation", Short: "Last tool truncation notice", Writer: WriterRuntime, Exact: true, Shape: ShapeAny()},
	{Path: FailureReasonKey, Short: "Why the last turn failed, as the provider or the daemon said it; removed by the next turn that ends without error", Writer: WriterRuntime, Exact: true, Shape: ShapeAny()},
	{Path: FailureCategoryKey, Short: `The last failure's kind: "rate_limited", "auth", "provider_error", "timeout", "refused" or "other"`, Writer: WriterRuntime, Exact: true, Shape: ShapeAny()},
	{Path: FailureTurnKey, Short: "The turn that failed", Writer: WriterRuntime, Exact: true, Shape: ShapeAny()},
	{Path: FailureAtKey, Short: "When it failed, unix millis", Writer: WriterRuntime, Exact: true, Shape: ShapeAny()},
	{Path: "datetime", Short: "Per-turn wall-clock time", Writer: WriterSelf, Ephemeral: true, Shape: ShapeAny()},
	{Path: "system.ttl", Short: `Owner-held; a lifetime counted from creation, after which the sweep deletes the node and its subtree (Go duration plus d and w; 0 or unset means none)`, Writer: WriterOwner, Shape: ShapeLifetime()},
	{Path: "system.reap_if_empty", Short: `Owner-held; true and the sweep reaps a figaro that never had a message`, Writer: WriterOwner, Shape: ShapeTruthy()},
	{Path: "system.keepalive", Short: `Owner-held; a node whose lifetime is spent survives while it was used within this window (default one day; 0 reaps at the deadline)`, Writer: WriterOwner, Shape: ShapeLifetime()},
	{Path: RetireRequestKey, Short: `Owner-held; the request to retire a figaro, with the reason (e.g. "manual"); a box may set it on the figaros inside it. The daemon records it at runtime.retired and never keeps it here; removing it unretires`, Writer: WriterOwner, Shape: ShapeTruthy()},
	{Path: RetiredKey, Short: `Set when a figaro retires, with the reason ("manual", "context"); written by the daemon from system.retired or its own retirement`, Writer: WriterRuntime, Exact: true, Shape: ShapeTruthy()},
	{Path: RuntimeRoot, Writer: WriterRuntime, Shape: ShapeObject()},
	{Path: "system.methods", Writer: WriterOwner, Shape: ShapeObject()},
	{Path: "system.fanout", Writer: WriterSelf, Operator: true, Shape: ShapeObject()},
	{Path: "system.environment", Writer: WriterOwner, Shape: ShapeObject()},
	{Path: "system.retention", Writer: WriterOwner, Shape: ShapeObject()},
})

func withLegacyRuntimeRows(rows []Spec) []Spec {
	shapes := make(map[string]Shape, len(rows))
	for _, s := range rows {
		shapes[s.Path] = s.Shape
	}
	for _, r := range runtimeRenames {
		if r.Old == RetireRequestKey {
			continue
		}
		shape, ok := shapes[r.New]
		if !ok {
			shape = ShapeAny()
		}
		rows = append(rows, Spec{Path: r.Old, Short: "Old name of " + r.New + ": read for one release, never written", Writer: WriterRuntime, Exact: true, Shape: shape})
	}
	return rows
}

func Registry() []Spec { return slices.Clone(registry) }

func (s Spec) placeholder() bool { return strings.Contains(s.Path, "<") }

func under(path, root string) bool {
	if !strings.HasPrefix(path, root) {
		return false
	}
	if len(path) == len(root) {
		return true
	}
	return path[len(root)] == '.' || path[len(root)] == '['
}

func (s Spec) covers(path string) bool {
	if s.Exact {
		return path == s.Path
	}
	return under(path, s.Path)
}

var roots = func() map[Writer][]Spec {
	m := map[Writer][]Spec{}
	for _, s := range registry {
		if s.placeholder() {
			continue
		}
		nested := false
		for _, o := range registry {
			if o.Path != s.Path && o.Writer == s.Writer && !o.Exact && !o.placeholder() && under(s.Path, o.Path) {
				nested = true
				break
			}
		}
		if !nested {
			m[s.Writer] = append(m[s.Writer], s)
		}
	}
	return m
}()

func OperatorPaths() []string {
	var out []string
	for _, s := range registry {
		if s.Operator {
			out = append(out, s.Path)
		}
	}
	return out
}

func RootsOf(w Writer) []string {
	out := make([]string, len(roots[w]))
	for i, s := range roots[w] {
		out[i] = s.Path
	}
	return out
}

func rootOf(w Writer, path string) (string, bool) {
	for _, s := range roots[w] {
		if s.covers(path) {
			return s.Path, true
		}
	}
	return "", false
}

func Ephemeral(path string) bool {
	for _, s := range registry {
		if s.Ephemeral && s.covers(path) {
			return true
		}
	}
	return false
}

func WriterOf(path string) Writer {
	for _, w := range []Writer{WriterRuntime, WriterOwner, WriterLineage} {
		if _, ok := rootOf(w, path); ok {
			return w
		}
	}
	return WriterSelf
}

package rpc

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Caller identity on the wire.
//
// A figaro should know WHICH ARIA invoked it. Before this, the only signal was
// FIGARO_ARIA in the callee's environment — and an agent injects that into
// every bash tool call, so a shell-out is statically attended to the aria that
// spawned it (see skills/figaro/SKILL.md, "Knowing yourself"). Two problems
// made that unusable as a credential:
//
//   - nothing rode on the wire, so a SERVER learned its caller only by
//     convention and environment inheritance;
//   - the env var cannot be turned off. A credential you cannot disable is not
//     a credential — there is no state in which the server is entitled to
//     doubt it, and therefore no authentication happening.
//
// So the identity is presented explicitly, in a reserved params field, and the
// server decides whether to believe it (see internal/authz).
//
// WHY PARAMS AND NOT THE ENVELOPE. JSON-RPC has no header. The natural home
// would be a top-level `meta` object beside jsonrpc/id/method/params, but the
// envelope belongs to jkrpc, an EXTERNAL module:
//
//	type Message struct { JSONRPC string; ID *int64; Method string
//	                      Params, Result json.RawMessage; Error *Error }
//
// It has no such field, and its API hands neither side access to one:
// Client.Call takes (method, params, result) and HandlerFunc receives only
// (ctx, params). Adding an envelope slot therefore costs a jkrpc API change, a
// module release, and a signature change to every handler in figaro — to carry
// one string that params can carry today. The reserved key is the cheap,
// reversible choice; if the wire ever grows real headers this moves there
// behind the same two functions.
//
// The key is spelled like an HTTP header on purpose: it names the slot for
// what it is, and `x-internal-` says out loud that it is not part of the
// public contract.
const CallerKey = "x-internal-figaro-id"

// CallerLabelKey is the params field carrying an ASSERTED caller reference —
// who is calling, when the caller is not an aria.
//
// IT IS NOT A CREDENTIAL AND MUST NEVER REACH AN AUTHORIZATION DECISION.
// Anyone who can set an environment variable can set this to anything; if a
// policy ever keyed on it, every rule would be one `FIGARO_CALLER=…` away from
// being bypassed. It exists for ATTRIBUTION only: so the model can tell who is
// talking to it. See authz.Identity, where it lands in Label rather than
// FigaroID precisely so the type system keeps them apart.
const CallerLabelKey = "x-caller"

// AriaLabelPrefix is reserved. An authenticated aria renders as "aria <id>";
// an asserted label renders bare. If a label could begin with this prefix, a
// human setting FIGARO_CALLER="aria 76062b18" would be indistinguishable from
// the real aria in the model's context — confidently misinformed, which is
// worse than unattributed. SanitizeLabel strips it.
const AriaLabelPrefix = "aria "

// MaxCallerLabelLen bounds an asserted label, in RUNES. It is caller-supplied
// text that lands in the model's context on every message, so it is capped
// rather than trusted to be reasonable.
//
// Runes, not bytes: a byte cap cuts a multi-byte rune in half, and the invalid
// tail that leaves is not merely ugly. encoding/json replaces invalid UTF-8
// with U+FFFD, so the value on the wire would differ from the value sanitized,
// and a second sanitize pass would produce a third answer — which is exactly
// what a fuzzer found here.
const MaxCallerLabelLen = 64

// Caller is the decode side of CallerKey. Embed it in a request struct that
// wants the identity typed, or use CallerOf to read it out of raw params
// without knowing the request's shape at all — which is what the server-side
// authenticator does, because it runs before dispatch has chosen a type.
type Caller struct {
	FigaroID string     `json:"x-internal-figaro-id,omitempty"`
	Caller   *CallerRef `json:"x-caller,omitempty"`
}

// CallerRef is who is calling, when the caller is not an aria. It is a TYPED
// OBJECT rather than a string on purpose.
//
// The common case is "the end user is typing" — the DUKE — and the CLI cannot
// name them: the name belongs to the aria being addressed, not to the shell.
// So the CLI sends a PLACEHOLDER and the server resolves it against the target
// aria's chalkboard (see DukeTitleKey). That is what keeps the user's name out
// of shell config entirely.
//
// A placeholder must not collide with anything a human could type, and a
// reserved string is a poor way to guarantee that — someone eventually types
// the reserved word. A separate BOOL cannot be reached from a string at all:
// FIGARO_CALLER only ever populates Label, so no value of it can produce
// Duke:true. The guarantee is structural, not lexical.
type CallerRef struct {
	// Duke marks the caller as the end user, to be named by the target aria's
	// chalkboard rather than by the caller. Set only by an INTERACTIVE CLI, so
	// an aria shelling out cannot accidentally speak as its master.
	Duke bool `json:"duke,omitempty"`

	// Label is an explicit asserted name (FIGARO_CALLER), which OVERRIDES the
	// duke placeholder when set. Sanitized; never a credential.
	Label string `json:"label,omitempty"`
}

// Empty reports whether the ref names nobody.
func (c *CallerRef) Empty() bool {
	return c == nil || (!c.Duke && c.Label == "")
}

// DukeTitleKey is the chalkboard key naming the end user for an aria. Set it
// in a loadout ("gluck") and every prompt that aria receives from a human
// terminal is attributed to that name.
//
// "Duke" is the harness's word for the END USER — the person the agent serves,
// as distinct from an aria (another figaro) or an anonymous script.
const DukeTitleKey = "duke-title"

// DefaultDukeTitle is what an aria calls its end user when its chalkboard does
// not say. Deliberately generic: a wrong name is worse than a plain one.
const DefaultDukeTitle = "user"

// SanitizeLabel makes an asserted label safe to render and safe to store.
//
// Three things, each for a reason:
//   - control characters are stripped, because the label is interpolated into
//     terminal rows and into the model's context; an embedded newline or escape
//     would break the first and could forge structure in the second;
//   - the reserved "aria " prefix is removed, so an assertion cannot dress
//     itself as an authenticated identity (see AriaLabelPrefix);
//   - the result is truncated to MaxCallerLabelLen.
//
// Returns "" when nothing usable survives, which callers treat as unknown.
func SanitizeLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	// Repeatedly, so "aria aria x" cannot smuggle one through.
	for {
		trimmed := strings.TrimSpace(strings.TrimPrefix(s, AriaLabelPrefix))
		if trimmed == s {
			break
		}
		s = trimmed
	}
	if utf8.RuneCountInString(s) > MaxCallerLabelLen {
		s = strings.TrimSpace(string([]rune(s)[:MaxCallerLabelLen]))
	}
	return s
}

// interactive records whether this process is a human at a terminal. The CLI
// arms it once at startup (SetInteractive), the same way it computes its
// binding policy once, so every client dialled afterwards agrees.
//
// It gates the DUKE placeholder and nothing else: a non-interactive
// invocation — a script, or an aria's own shell-out — presents no duke, so a
// figaro cannot speak as its master by accident. An aria that deliberately
// allocates itself a terminal can still do it; that is a known gap, accepted
// until real authentication closes it, and it is why none of this is allowed
// near an authorization decision.
var interactive bool

// SetInteractive arms the duke placeholder. Call once, at startup.
func SetInteractive(v bool) { interactive = v }

// CallerRefFromEnv is the reference this process presents.
//
// FIGARO_CALLER is an OVERRIDE: when set it names the caller explicitly and
// suppresses the placeholder, which is what makes a script able to say who it
// is acting for. Otherwise an interactive terminal presents the duke, and
// anything else presents nothing at all.
func CallerRefFromEnv() *CallerRef {
	if label := LabelFromEnv(); label != "" {
		return &CallerRef{Label: label}
	}
	if interactive {
		return &CallerRef{Duke: true}
	}
	return nil
}

// LabelFromEnv is the asserted label this process presents, from FIGARO_CALLER.
//
// An interactive shell sets it (a fish config guarded on interactivity, say);
// a script leaves it unset and is simply unattributed. It is sanitized here so
// nothing downstream has to remember to.
func LabelFromEnv() string {
	return SanitizeLabel(os.Getenv("FIGARO_CALLER"))
}

// CallerRefOf reads the asserted caller reference out of raw params, with its
// label sanitized. Like CallerOf it never errors — an unusable ref is an absent
// one, and whether absence is acceptable is the policy's decision.
func CallerRefOf(params json.RawMessage) *CallerRef {
	if len(params) == 0 {
		return nil
	}
	var c Caller
	if err := json.Unmarshal(params, &c); err != nil || c.Caller == nil {
		return nil
	}
	ref := *c.Caller
	ref.Label = SanitizeLabel(ref.Label)
	if ref.Empty() {
		return nil
	}
	return &ref
}

// LabelOf is the asserted label alone, ignoring the duke placeholder — the
// placeholder has no name until a server resolves it against an aria.
func LabelOf(params json.RawMessage) string {
	ref := CallerRefOf(params)
	if ref == nil {
		return ""
	}
	return ref.Label
}

// CallerFromEnv is the credential a CLI invocation presents: the aria that
// spawned this process, per FIGARO_ARIA, validated.
//
// This is deliberately NOT the target-selection rule. Target selection is
// `--id > FIGARO_ARIA > pid binding` (see internal/cli/binding.go) and answers
// "which aria am I talking ABOUT". Caller identity answers "which aria am I",
// and only FIGARO_ARIA can answer it: --id is an argument the caller chose,
// and a pid binding says which aria a shell is ATTENDING, not that the shell
// is one. Conflating them would let `figaro fork --id X` claim to *be* X.
//
// Empty means "no figaro caller" — a human at a terminal, or an external
// script. That is a legitimate answer, not a failure.
func CallerFromEnv() string {
	id := strings.TrimSpace(os.Getenv("FIGARO_ARIA"))
	if id == "" {
		return ""
	}
	if err := ValidateAriaID(id); err != nil {
		return ""
	}
	return id
}

// WithCaller marshals params and splices callerID in under CallerKey,
// returning the bytes to send. An empty callerID marshals params unchanged, so
// a human-driven CLI puts nothing extra on the wire.
//
// nil params become a fresh object holding only the identity: several methods
// take no arguments and pass nil (figaro.context, figaro.chalkboard), and they
// must still be able to say who is calling. That is exactly why the identity
// is injected generically here rather than embedded in each request struct —
// there is no struct to embed it in when params is nil.
//
// Values are carried as json.RawMessage and re-marshaled verbatim, so this is
// not a lossy re-encode of the payload: nothing is reformatted, retyped, or
// dropped. Key order changes (Go sorts map keys); no figaro method depends on
// params key order.
//
// Non-object params are an error rather than a silent pass-through. Every
// figaro method takes an object or nothing, so a scalar or array here is a
// programming mistake, and quietly dropping a credential is the worst possible
// response to one.
func WithCaller(params any, callerID string, ref *CallerRef) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal params: %w", err)
	}
	if ref != nil {
		clean := *ref
		clean.Label = SanitizeLabel(clean.Label)
		ref = &clean
		if ref.Empty() {
			ref = nil
		}
	}
	if callerID == "" && ref == nil {
		return raw, nil
	}
	fields := map[string]json.RawMessage{}
	if trimmed := strings.TrimSpace(string(raw)); trimmed != "" && trimmed != "null" {
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, fmt.Errorf("params must be a JSON object to carry %s: %w", CallerKey, err)
		}
	}
	if callerID != "" {
		id, mErr := json.Marshal(callerID)
		if mErr != nil {
			return nil, fmt.Errorf("marshal caller id: %w", mErr)
		}
		fields[CallerKey] = id
	}
	if ref != nil {
		rb, mErr := json.Marshal(ref)
		if mErr != nil {
			return nil, fmt.Errorf("marshal caller ref: %w", mErr)
		}
		fields[CallerLabelKey] = rb
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("re-marshal params: %w", err)
	}
	return out, nil
}

// CallerOf reads the presented identity out of raw params, or "" when absent,
// malformed, or not an object. It never errors: an unreadable credential is
// simply an absent one, and the authorization policy — not the decoder — is
// what decides whether absence is acceptable for a given method.
func CallerOf(params json.RawMessage) string {
	if len(params) == 0 {
		return ""
	}
	var c Caller
	if err := json.Unmarshal(params, &c); err != nil {
		return ""
	}
	if c.FigaroID == "" {
		return ""
	}
	if err := ValidateAriaID(c.FigaroID); err != nil {
		return ""
	}
	return c.FigaroID
}

// Attribution is the ONE place the rendered form of a sender is decided, so
// the model, the transcript, the inline view and `figaro show` cannot drift
// into disagreeing about who spoke.
//
// An authenticated aria renders "aria <id>"; an asserted label renders BARE.
// The asymmetry is load-bearing: SanitizeLabel reserves the "aria " prefix, so
// an assertion can never dress itself as an aria. Empty means UNKNOWN, and
// callers must render nothing at all rather than "unknown" or a blank line —
// most messages in an existing log have no sender and should look untouched.
func Attribution(figaroID, label string) string {
	if figaroID != "" {
		return AriaLabelPrefix + figaroID
	}
	return SanitizeLabel(label)
}

// SenderFrom renders the attribution carried by a request's params.
//
// Attribution is deliberately NOT gated on the authn provider: a human at a
// terminal is never authenticated and is exactly the caller a confused aria
// most needs named. Disabling the provider withholds AUTHORITY (see
// authz.AriaHeader), not identity.
//
// It is therefore trust-on-assertion, like the credential itself. Anything
// that can set FIGARO_ARIA can claim that id. That is honest for a 0600 unix
// socket and is why nothing here feeds an authorization decision — the policy
// reads authz.Identity, which distinguishes proof from assertion; this only
// says whose name to print.
func SenderFrom(params json.RawMessage, dukeTitle func() string) string {
	if id := CallerOf(params); id != "" {
		return AriaLabelPrefix + id
	}
	ref := CallerRefOf(params)
	if ref == nil {
		return ""
	}
	// An explicit label wins over the placeholder: FIGARO_CALLER is an
	// override, and a caller that named itself has said something the target
	// aria's chalkboard cannot know.
	if ref.Label != "" {
		return ref.Label
	}
	if !ref.Duke {
		return ""
	}
	// The duke is named by the ARIA BEING ADDRESSED, not by the caller. That
	// is the whole point: the user's name lives in a loadout, not in a shell.
	if dukeTitle != nil {
		if t := SanitizeLabel(dukeTitle()); t != "" {
			return t
		}
	}
	return DefaultDukeTitle
}

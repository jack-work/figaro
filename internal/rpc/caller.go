package rpc

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode"
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

// CallerLabelKey is the params field carrying an ASSERTED caller label — a
// free-form name for a caller that is not an aria (a human at a terminal,
// typically sourced from FIGARO_CALLER by an interactive shell).
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

// MaxCallerLabelLen bounds an asserted label. It is caller-supplied text that
// lands in the model's context on every message, so it is capped rather than
// trusted to be reasonable.
const MaxCallerLabelLen = 64

// Caller is the decode side of CallerKey. Embed it in a request struct that
// wants the identity typed, or use CallerOf to read it out of raw params
// without knowing the request's shape at all — which is what the server-side
// authenticator does, because it runs before dispatch has chosen a type.
type Caller struct {
	FigaroID string `json:"x-internal-figaro-id,omitempty"`
	Label    string `json:"x-caller,omitempty"`
}

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
	if len(s) > MaxCallerLabelLen {
		s = strings.TrimSpace(s[:MaxCallerLabelLen])
	}
	return s
}

// LabelFromEnv is the asserted label this process presents, from FIGARO_CALLER.
//
// An interactive shell sets it (a fish config guarded on interactivity, say);
// a script leaves it unset and is simply unattributed. It is sanitized here so
// nothing downstream has to remember to.
func LabelFromEnv() string {
	return SanitizeLabel(os.Getenv("FIGARO_CALLER"))
}

// LabelOf reads the asserted label out of raw params, sanitized. Like CallerOf
// it never errors — an unusable label is an absent one.
func LabelOf(params json.RawMessage) string {
	if len(params) == 0 {
		return ""
	}
	var c Caller
	if err := json.Unmarshal(params, &c); err != nil {
		return ""
	}
	return SanitizeLabel(c.Label)
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
func WithCaller(params any, callerID, label string) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal params: %w", err)
	}
	label = SanitizeLabel(label)
	if callerID == "" && label == "" {
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
	if label != "" {
		lb, mErr := json.Marshal(label)
		if mErr != nil {
			return nil, fmt.Errorf("marshal caller label: %w", mErr)
		}
		fields[CallerLabelKey] = lb
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

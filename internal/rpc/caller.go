package rpc

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
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

// Caller is the decode side of CallerKey. Embed it in a request struct that
// wants the identity typed, or use CallerOf to read it out of raw params
// without knowing the request's shape at all — which is what the server-side
// authenticator does, because it runs before dispatch has chosen a type.
type Caller struct {
	FigaroID string `json:"x-internal-figaro-id,omitempty"`
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
func WithCaller(params any, callerID string) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal params: %w", err)
	}
	if callerID == "" {
		return raw, nil
	}
	id, err := json.Marshal(callerID)
	if err != nil {
		return nil, fmt.Errorf("marshal caller id: %w", err)
	}
	fields := map[string]json.RawMessage{}
	if trimmed := strings.TrimSpace(string(raw)); trimmed != "" && trimmed != "null" {
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, fmt.Errorf("params must be a JSON object to carry %s: %w", CallerKey, err)
		}
	}
	fields[CallerKey] = id
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

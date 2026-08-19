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
const CallerKey = "x-internal-figaro-id"

// CallerLabelKey is the params field carrying an ASSERTED caller reference -
// who is calling, when the caller is not an aria.
const CallerLabelKey = "x-caller"

// AriaLabelPrefix is reserved. An authenticated aria renders as "aria <id>";
// an asserted label renders bare. If a label could begin with this prefix, a
// human setting FIGARO_CALLER="aria 76062b18" would be indistinguishable from
// the real aria in the model's context: confidently misinformed, which is
// worse than unattributed. SanitizeLabel strips it.
const AriaLabelPrefix = "aria "

// MaxCallerLabelLen bounds an asserted label, in RUNES. It is caller-supplied
// text that lands in the model's context on every message, so it is capped
// rather than trusted to be reasonable.
const MaxCallerLabelLen = 64

// Caller is the decode side of CallerKey. Embed it in a request struct that
// wants the identity typed, or use CallerOf to read it out of raw params
// without knowing the request's shape at all: which is what the server-side
// authenticator does, because it runs before dispatch has chosen a type.
type Caller struct {
	FigaroID string     `json:"x-internal-figaro-id,omitempty"`
	Caller   *CallerRef `json:"x-caller,omitempty"`
}

// CallerRef is who is calling, when the caller is not an aria. It is a TYPED
// OBJECT rather than a string on purpose.
type CallerRef struct {
	// Duke marks the caller as the end user, to be named by the target aria's
	// form rather than by the caller. Set only by an INTERACTIVE CLI, so
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

// DukeTitleKey is the form key naming the end user for an aria. Set it
// in an outfit ("gluck") and every prompt that aria receives from a human
// terminal is attributed to that name.
const DukeTitleKey = "duke-title"

// DefaultDukeTitle is what an aria calls its end user when its form does
// not say. Deliberately generic: a wrong name is worse than a plain one.
const DefaultDukeTitle = "user"

// SanitizeLabel makes an asserted label safe to render and safe to store.
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
var interactive bool

// SetInteractive arms the duke placeholder. Call once, at startup.
func SetInteractive(v bool) { interactive = v }

// CallerRefFromEnv is the reference this process presents.
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
func LabelFromEnv() string {
	return SanitizeLabel(os.Getenv("FIGARO_CALLER"))
}

// CallerRefOf reads the asserted caller reference out of raw params, with its
// label sanitized. Like CallerOf it never errors, an unusable ref is an absent
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

// LabelOf is the asserted label alone, ignoring the duke placeholder: the
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
// simply an absent one, and the authorization policy: not the decoder: is
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
func Attribution(figaroID, label string) string {
	if figaroID != "" {
		return AriaLabelPrefix + figaroID
	}
	return SanitizeLabel(label)
}

// SenderFrom renders the attribution carried by a request's params.
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
	// aria's form cannot know.
	if ref.Label != "" {
		return ref.Label
	}
	if !ref.Duke {
		return ""
	}
	// The duke is named by the ARIA BEING ADDRESSED, not by the caller. That
	// is the whole point: the user's name lives in an outfit, not in a shell.
	if dukeTitle != nil {
		if t := SanitizeLabel(dukeTitle()); t != "" {
			return t
		}
	}
	return DefaultDukeTitle
}

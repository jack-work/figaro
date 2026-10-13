package rpc

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

// CallerKey is the params field carrying the presented aria identity.
const CallerKey = "x-internal-figaro-id"

// CallerLabelKey is the params field carrying an asserted caller reference,
// for a caller that is not an aria.
const CallerLabelKey = "x-caller"

// AriaLabelPrefix is stripped by SanitizeLabel so no asserted label can pose
// as an aria.
const AriaLabelPrefix = "aria "

// MaxCallerLabelLen is in runes; the label lands in the model's context on
// every message.
const MaxCallerLabelLen = 64

// Caller is the decode side of CallerKey and CallerLabelKey.
type Caller struct {
	FigaroID string     `json:"x-internal-figaro-id,omitempty"`
	Caller   *CallerRef `json:"x-caller,omitempty"`
}

type CallerRef struct {
	// Duke marks the caller as the end user, named by the target aria's form.
	// Set only by an interactive CLI, so an aria shelling out cannot speak as
	// its master.
	Duke bool `json:"duke,omitempty"`

	// Label is an asserted name (FIGARO_CALLER) that overrides the duke
	// placeholder. Sanitized; never a credential.
	Label string `json:"label,omitempty"`
}

func (c *CallerRef) Empty() bool {
	return c == nil || (!c.Duke && c.Label == "")
}

// DukeTitleKey is the form key naming an aria's end user.
const DukeTitleKey = "duke-title"

const DefaultDukeTitle = "user"

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

var interactive bool

// SetInteractive arms the duke placeholder; call once, at startup.
func SetInteractive(v bool) { interactive = v }

func CallerRefFromEnv() *CallerRef {
	if label := LabelFromEnv(); label != "" {
		return &CallerRef{Label: label}
	}
	if interactive {
		return &CallerRef{Duke: true}
	}
	return nil
}

func LabelFromEnv() string {
	return SanitizeLabel(os.Getenv("FIGARO_CALLER"))
}

// CallerRefOf never errors: an unusable ref is an absent one, and policy
// decides whether absence is acceptable.
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

// LabelOf ignores the duke placeholder, which has no name until a server
// resolves it against an aria.
func LabelOf(params json.RawMessage) string {
	ref := CallerRefOf(params)
	if ref == nil {
		return ""
	}
	return ref.Label
}

// CallerFromEnv is the validated FIGARO_ARIA, or "".
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

// WithCaller splices callerID and ref into params. With neither, params
// marshal unchanged.
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

// CallerOf never errors: an unreadable credential is an absent one, and
// policy decides whether absence is acceptable.
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

// SenderFrom renders the attribution a request's params assert; the duke is
// named by the aria being addressed, not by the caller.
func SenderFrom(params json.RawMessage, dukeTitle func() string) string {
	if id := CallerOf(params); id != "" {
		return id
	}
	ref := CallerRefOf(params)
	if ref == nil {
		return ""
	}
	if ref.Label != "" {
		return ref.Label
	}
	if !ref.Duke {
		return ""
	}
	if dukeTitle != nil {
		if t := SanitizeLabel(dukeTitle()); t != "" {
			return t
		}
	}
	return DefaultDukeTitle
}

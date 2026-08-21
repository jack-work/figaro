package form

// INCANTATIONS: the words the harness says to a figaro at a lifecycle event.

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
)

// StudyIncantationKey and ForkIncantationKey are where the phrases live.
// ForkedFromKey is the birth mark a fork incantation speaks about; it is
// named here rather than spelled at each reader, because a key written in one
// place and matched in three is a key that will eventually disagree with
// itself.
const (
	StudyIncantationKey = "system.study_incantation"
	ForkIncantationKey  = "system.fork_incantation"
	ForkedFromKey       = "system.forked_from"
)

// StudyIncantation is the phrase per study lifecycle event. Any field may be
// empty, which means "say nothing extra at that event".
type StudyIncantation struct {
	OnStudy  string `json:"onstudy,omitempty"`
	OnUpdate string `json:"onupdate,omitempty"`
	OnDrop   string `json:"ondrop,omitempty"`
}

// IsEmpty reports whether this incantation would add anything at all.
func (s StudyIncantation) IsEmpty() bool {
	return s.OnStudy == "" && s.OnUpdate == "" && s.OnDrop == ""
}

// ForkIncantation is the phrase a branch is shown at birth. It accepts two
// spellings, because one of them is what a human reaches for first:
type ForkIncantation struct {
	OnFork string `json:"onfork,omitempty"`
}

func (f ForkIncantation) IsEmpty() bool { return f.OnFork == "" }

// studyIncantationFields is the closed set, for naming typos in the warning.
var studyIncantationFields = []string{"onstudy", "onupdate", "ondrop"}

// ReadStudyIncantation reads system.study_incantation off a board.
func ReadStudyIncantation(snap Snapshot) StudyIncantation {
	raw, ok := snap.Get(StudyIncantationKey)
	if !ok {
		return StudyIncantation{}
	}
	// THE FAST PATH IS THE CORRECT ONE. A well-formed incantation decodes
	// straight into the struct: no intermediate map, no per-field unmarshal,
	// no diagnostics to assemble for problems that do not exist. Only a value
	// that fails this decode pays for the explanation of why, and a strict
	// decoder is what makes "fails" include the typo case.
	var fast StudyIncantation
	if strictDecode(raw, &fast) == nil {
		fast.OnStudy = strings.TrimSpace(fast.OnStudy)
		fast.OnUpdate = strings.TrimSpace(fast.OnUpdate)
		fast.OnDrop = strings.TrimSpace(fast.OnDrop)
		return fast
	}
	fields, ok := incantationObject(raw, StudyIncantationKey, studyIncantationFields)
	if !ok {
		return StudyIncantation{}
	}
	return StudyIncantation{
		OnStudy:  fields["onstudy"],
		OnUpdate: fields["onupdate"],
		OnDrop:   fields["ondrop"],
	}
}

// strictDecode decodes one JSON object into out, refusing unknown fields. The
// refusal is the point: it routes a typo to the diagnostic path instead of
// silently dropping it, which is the failure mode that leaves a human staring
// at a key that "does nothing".
func strictDecode(raw json.RawMessage, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	// Trailing content means the value was not one object.
	if dec.More() {
		return errTrailingJSON
	}
	return nil
}

var errTrailingJSON = errors.New("incantation: trailing content after the object")

// ReadForkIncantation reads system.fork_incantation off a board, accepting
// either a bare string or an object carrying onfork.
func ReadForkIncantation(snap Snapshot) ForkIncantation {
	raw, ok := snap.Get(ForkIncantationKey)
	if !ok {
		return ForkIncantation{}
	}
	if s, isString := incantationString(raw); isString {
		return ForkIncantation{OnFork: strings.TrimSpace(s)}
	}
	var fast ForkIncantation
	if strictDecode(raw, &fast) == nil {
		return ForkIncantation{OnFork: strings.TrimSpace(fast.OnFork)}
	}
	fields, ok := incantationObject(raw, ForkIncantationKey, []string{"onfork"})
	if !ok {
		return ForkIncantation{}
	}
	return ForkIncantation{OnFork: fields["onfork"]}
}

// incantationString reports whether raw is a JSON string, and its value.
func incantationString(raw json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// incantationObject decodes one incantation object into its coherent string
// fields. A value that is not an object is refused wholesale; a field that is
// not a string is dropped alone, so one bad key cannot silence the rest.
// Unknown keys are named, because the failure they cause otherwise ("I set it
// and nothing happened") is the hardest kind to debug.
func incantationObject(raw json.RawMessage, key string, known []string) (map[string]string, bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		slog.Warn("incantation ignored: not an object",
			"key", key,
			"want", "{"+strings.Join(known, ", ")+"} with string values",
			"got", jsonKind(raw),
			"err", err)
		return nil, false
	}
	out := make(map[string]string, len(obj))
	for name, value := range obj {
		lower := strings.ToLower(strings.TrimSpace(name))
		if !containsString(known, lower) {
			slog.Warn("incantation field ignored: unknown key",
				"key", key, "field", name, "known", strings.Join(known, ", "))
			continue
		}
		s, isString := incantationString(value)
		if !isString {
			slog.Warn("incantation field ignored: not a string",
				"key", key, "field", name, "got", jsonKind(value))
			continue
		}
		if s = strings.TrimSpace(s); s != "" {
			out[lower] = s
		}
	}
	return out, true
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// jsonKind names a raw value's type for a human reading a warning. It is the
// difference between "incantation ignored" and "incantation ignored: you gave
// it an array".
func jsonKind(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "empty"
	}
	switch trimmed[0] {
	case '{':
		return "object"
	case '[':
		return "array"
	case '"':
		return "string"
	case 't', 'f':
		return "boolean"
	case 'n':
		return "null"
	}
	return "number"
}

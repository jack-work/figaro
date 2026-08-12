package form

// INCANTATIONS: the words the harness says to a figaro at a lifecycle event.
//
// The machinery states facts. A study block says which form moved and what it
// now holds; a fork says which trunk a branch came from. What those facts MEAN
// to a particular figaro is not the harness's business, and hard-coding a
// sentence about it into the renderer makes every aria wear one outfit's idea
// of the moment.
//
// So the sentence is data. `system.study_incantation` and
// `system.fork_incantation` live on the BOUND form: an aria's own board, where
// every other system setting lives, and nowhere else. A studied form does not
// get to put words in its observer's mouth, and an outfit that wants to can
// simply set the key at birth like any other.
//
// TOLERANCE IS DELIBERATE AND ASYMMETRIC. A malformed incantation must never
// cost a turn: the phrase is decoration on a fact the model still receives, so
// a bad type is logged and skipped, key by key, and everything coherent beside
// it still speaks. A strict parse here would mean a typo in an outfit silently
// breaking every fork in it.
//
// TODO(notifications): these warnings go to the daemon log, which nobody
// reads. They belong in a `fig notifications` verb that shows a human the
// things their configuration did wrong without making them tail a file. Not
// now: logging is enough to debug with, and the verb is a surface of its own.

import (
	"encoding/json"
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
//
//	system.fork_incantation = "you are a branch; the trunk continues without you"
//	system.fork_incantation = {"onfork": "…"}
//
// The bare string is the object with onfork set. Both parse; neither is
// deprecated.
type ForkIncantation struct {
	OnFork string `json:"onfork,omitempty"`
}

func (f ForkIncantation) IsEmpty() bool { return f.OnFork == "" }

// studyIncantationFields is the closed set, for naming typos in the warning.
var studyIncantationFields = []string{"onstudy", "onupdate", "ondrop"}

// ReadStudyIncantation reads system.study_incantation off a board.
//
// Returns the zero value when the key is absent, which is the common case and
// costs one lookup. Every incoherence is logged with the key that caused it
// and the shape that was expected, and skipped.
func ReadStudyIncantation(snap Snapshot) StudyIncantation {
	raw, ok := snap.Get(StudyIncantationKey)
	if !ok {
		return StudyIncantation{}
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

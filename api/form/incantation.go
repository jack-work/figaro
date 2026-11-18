package form

import (
	"encoding/json"
	"log/slog"
	"strings"
)

const (
	StudyIncantationKey = "system.study_incantation"
	ForkIncantationKey  = "system.fork_incantation"
)

// StudyIncantation is the phrase per study lifecycle event; an empty field
// says nothing.
type StudyIncantation struct {
	OnStudy  string `json:"onstudy,omitempty"`
	OnUpdate string `json:"onupdate,omitempty"`
	OnDrop   string `json:"ondrop,omitempty"`
}

func (s StudyIncantation) IsEmpty() bool {
	return s.OnStudy == "" && s.OnUpdate == "" && s.OnDrop == ""
}

// ForkIncantation is the phrase a branch is shown at birth.
type ForkIncantation struct {
	OnFork string `json:"onfork,omitempty"`
}

func (f ForkIncantation) IsEmpty() bool { return f.OnFork == "" }

var studyIncantationFields = []string{"onstudy", "onupdate", "ondrop"}

func ReadStudyIncantation(snap Snapshot) StudyIncantation {
	raw, ok := snap.Get(StudyIncantationKey)
	if !ok {
		return StudyIncantation{}
	}
	if s, isString := asString(raw); isString {
		return StudyIncantation{OnStudy: strings.TrimSpace(s)}
	}
	fields, ok := incantationFields(raw, StudyIncantationKey, studyIncantationFields)
	if !ok {
		return StudyIncantation{}
	}
	return StudyIncantation{
		OnStudy:  fields["onstudy"],
		OnUpdate: fields["onupdate"],
		OnDrop:   fields["ondrop"],
	}
}

var forkIncantationFields = []string{"onfork"}

func ReadForkIncantation(snap Snapshot) ForkIncantation {
	raw, ok := snap.Get(ForkIncantationKey)
	if !ok {
		return ForkIncantation{}
	}
	if s, isString := asString(raw); isString {
		return ForkIncantation{OnFork: strings.TrimSpace(s)}
	}
	fields, ok := incantationFields(raw, ForkIncantationKey, forkIncantationFields)
	if !ok {
		return ForkIncantation{}
	}
	return ForkIncantation{OnFork: fields["onfork"]}
}

func incantationFields(raw json.RawMessage, key string, known []string) (map[string]string, bool) {
	if checkIncantation(raw, known) != nil {
		return incantationObject(raw, key, known)
	}
	var obj map[string]json.RawMessage
	_ = json.Unmarshal(raw, &obj)
	out := make(map[string]string, len(obj))
	for name, value := range obj {
		if s, _ := asString(value); strings.TrimSpace(s) != "" {
			out[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(s)
		}
	}
	return out, true
}

func incantationString(raw json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// incantationObject refuses a non-object wholesale, drops a non-string field
// alone, and names unknown keys in the warning.
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

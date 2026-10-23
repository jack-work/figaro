package methods

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/decide"
)

const (
	FormKey      = "system.methods"
	EventInquiry = "inquiry"
	enabledField = "enabled"
)

type Method struct {
	Name      string
	Key       string
	Event     string
	Template  Template
	Options   []decide.Option
	Score     bool
	KeepCache bool
	Default   *string
}

type spec struct {
	Enabled   json.RawMessage `json:"enabled"`
	Key       string          `json:"key"`
	Event     string          `json:"event"`
	Context   string          `json:"context"`
	Options   json.RawMessage `json:"options"`
	Score     json.RawMessage `json:"score"`
	KeepCache json.RawMessage `json:"keep_cache"`
	Default   *string         `json:"default"`
}

func Read(snap form.Snapshot) ([]Method, []error) {
	raw, ok := snap.Get(FormKey)
	if !ok {
		return nil, nil
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return nil, []error{fmt.Errorf("%s: not an object", FormKey)}
	}
	if on, err := flag(members[enabledField], true); err != nil {
		return nil, []error{fmt.Errorf("%s.%s: %w", FormKey, enabledField, err)}
	} else if !on {
		return nil, nil
	}
	names := make([]string, 0, len(members))
	for name := range members {
		if name != enabledField {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var out []Method
	var errs []error
	for _, name := range names {
		m, on, err := readMethod(name, members[name])
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("%s.%s: %w", FormKey, name, err))
		case on:
			out = append(out, m)
		}
	}
	return out, errs
}

func readMethod(name string, raw json.RawMessage) (Method, bool, error) {
	var s spec
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Method{}, false, err
	}
	on, err := flag(s.Enabled, true)
	if err != nil {
		return Method{}, false, fmt.Errorf("enabled: %w", err)
	}
	if !on {
		return Method{}, false, nil
	}
	score, err := flag(s.Score, false)
	if err != nil {
		return Method{}, false, fmt.Errorf("score: %w", err)
	}
	keep, err := flag(s.KeepCache, true)
	if err != nil {
		return Method{}, false, fmt.Errorf("keep_cache: %w", err)
	}
	m := Method{Name: name, Key: strings.TrimSpace(s.Key), Event: strings.TrimSpace(s.Event), Score: score, KeepCache: keep}
	if m.Key == "" {
		return m, false, errors.New("no key to set")
	}
	if m.Event == "" {
		m.Event = EventInquiry
	}
	if m.Event != EventInquiry {
		return m, false, fmt.Errorf("event %q: only %q is supported", m.Event, EventInquiry)
	}
	if m.Template, err = Parse(s.Context); err != nil {
		return m, false, fmt.Errorf("context: %w", err)
	}
	ordered := true
	if m.Options, ordered, err = readOptions(s.Options); err != nil {
		return m, false, fmt.Errorf("options: %w", err)
	}
	if m.Score && !ordered {
		return m, false, errors.New("score reads the options as a scale, so they must be an array, lowest first")
	}
	if s.Default != nil {
		if !hasOption(m.Options, *s.Default) {
			return m, false, fmt.Errorf("default %q is not one of the options", *s.Default)
		}
		m.Default = s.Default
	}
	return m, true, nil
}

func readOptions(raw json.RawMessage) ([]decide.Option, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, false, errors.New("missing")
	}
	var opts []decide.Option
	ordered := trimmed[0] == '['
	if ordered {
		var labels []string
		if err := json.Unmarshal(trimmed, &labels); err != nil {
			return nil, false, errors.New("an array must hold strings")
		}
		for _, l := range labels {
			opts = append(opts, decide.Option{Label: strings.TrimSpace(l)})
		}
	} else {
		var named map[string]string
		if err := json.Unmarshal(trimmed, &named); err != nil {
			return nil, false, errors.New("want an array of names, or an object of name to description")
		}
		for label, criterion := range named {
			opts = append(opts, decide.Option{Label: strings.TrimSpace(label), Criterion: strings.TrimSpace(criterion)})
		}
		sort.Slice(opts, func(i, j int) bool { return opts[i].Label < opts[j].Label })
	}
	seen := map[string]bool{}
	for _, o := range opts {
		if o.Label == "" {
			return nil, false, errors.New("an option has no name")
		}
		if seen[o.Label] {
			return nil, false, fmt.Errorf("%q appears twice", o.Label)
		}
		seen[o.Label] = true
	}
	if len(opts) < 2 {
		return nil, false, errors.New("need two or more")
	}
	return opts, ordered, nil
}

func hasOption(opts []decide.Option, label string) bool {
	for _, o := range opts {
		if o.Label == label {
			return true
		}
	}
	return false
}

func flag(raw json.RawMessage, absent bool) (bool, error) {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return absent, nil
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
	}
	return false, fmt.Errorf("want true or false, got %s", raw)
}

func (m Method) Question(inquiries []string) decide.Question {
	return decide.Question{Ask: m.Template.Render(inquiries), Options: m.Options, Score: m.Score}
}

func Effective(bound, defaults form.Snapshot) form.Snapshot {
	set := map[string]json.RawMessage{}
	for _, snap := range []form.Snapshot{defaults, bound} {
		for key, raw := range snap.All() {
			if strings.HasPrefix(key, FormKey+".") {
				set[key] = raw
			}
		}
	}
	return form.Snapshot{}.Apply(form.Build(form.Snapshot{}, set, nil))
}

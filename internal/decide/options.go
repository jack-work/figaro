package decide

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	apidecide "github.com/jack-work/figaro/api/decide"
	"github.com/jack-work/figaro/api/form"
)

type option struct {
	label     string
	criterion string
}

func expand(choices []string, board form.Snapshot) ([]option, []string, error) {
	var opts []option
	var refs []string
	seen := map[string]bool{}
	add := func(o option, prefix string) {
		if seen[o.label] && prefix != "" {
			o.label = prefix + "." + o.label
		}
		if seen[o.label] {
			return
		}
		seen[o.label] = true
		opts = append(opts, o)
	}
	for _, c := range choices {
		key, isRef := apidecide.Ref(c)
		if !isRef {
			add(option{label: c, criterion: c}, "")
			continue
		}
		raw, ok := board.Get(key)
		if !ok {
			return nil, nil, fmt.Errorf("@%s: no such key on the form", key)
		}
		refs = append(refs, key)
		children, isObject, err := orderedChildren(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("@%s: %w", key, err)
		}
		if !isObject {
			add(option{label: key, criterion: valueText(raw)}, "")
			continue
		}
		if len(children) == 0 {
			return nil, nil, fmt.Errorf("@%s: an empty object has no options", key)
		}
		for _, ch := range children {
			add(option{label: ch.name, criterion: valueText(ch.raw)}, key)
		}
	}
	return opts, refs, nil
}

type child struct {
	name string
	raw  json.RawMessage
}

func orderedChildren(raw json.RawMessage) ([]child, bool, error) {
	trimmedRaw := bytes.TrimSpace(raw)
	if len(trimmedRaw) == 0 || trimmedRaw[0] != '{' {
		return nil, false, nil
	}
	dec := json.NewDecoder(bytes.NewReader(trimmedRaw))
	if _, err := dec.Token(); err != nil {
		return nil, true, err
	}
	var out []child
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, true, err
		}
		name, _ := tok.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, true, err
		}
		out = append(out, child{name: name, raw: v})
	}
	return out, true, nil
}

func valueText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var compact bytes.Buffer
	if json.Compact(&compact, raw) == nil {
		return compact.String()
	}
	return strings.TrimSpace(string(raw))
}

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/sdk"
)

type wireFormSource struct {
	*formMirror
	aria *sdk.Aria
}

func (s wireFormSource) Edit(p form.Patch) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := s.aria.Set(ctx, p, 0)
	return err
}

var errFormReadOnly = errors.New("this form is read-only in the pit")

func (v *formView) Edit(p form.Patch) error {
	if e, ok := v.source().(formEditor); ok {
		return e.Edit(p)
	}
	return errFormReadOnly
}

func (v *formView) editable() bool {
	_, ok := v.source().(formEditor)
	return ok
}

const formChangeMax = 64 << 10

func (v *formView) changeLine(path, subject string) (string, string) {
	if !v.editable() {
		return "", errFormReadOnly.Error()
	}
	v.mu.Lock()
	var n *formNode
	for _, r := range v.rows {
		if r.path == path {
			n = r
			break
		}
	}
	v.mu.Unlock()
	if n == nil {
		return "", ""
	}
	if !n.leaf() {
		return "", "c changes a value: open the branch and pick a key"
	}
	raw := n.value
	if len(raw) > formChangeMax {
		return "", fmt.Sprintf("%s is %d KB: too big to change on one line", path, len(raw)>>10)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		compact.Reset()
		compact.Write(raw)
	}
	line := "form set "
	if v.aria != "" && v.aria != subject {
		line += "--id " + v.aria + " "
	}
	return line + shellQuote(path) + " " + shellQuote(compact.String()), ""
}

func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t'\"\\") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (v *formView) Key(b byte) bool {
	if b != 'c' || v.change == nil {
		return false
	}
	v.change()
	return true
}

func pitChange(t *transcript) {
	v, ok := t.pit.live.(*formView)
	if !ok {
		return
	}
	row, ok := t.pit.selected()
	if !ok {
		return
	}
	line, note := v.changeLine(valuePath(row.id), t.subjectID())
	if line == "" {
		if note != "" {
			t.setCommandNoteAt(note, alertError)
		}
		return
	}
	pagerCommandBox(t)
	t.cmdline.set(line)
}

func shownFormEdit(argv []string, base form.Snapshot) (form.Patch, string, bool) {
	if len(argv) == 0 {
		return form.Patch{}, "", false
	}
	verb, args := argv[0], argv[1:]
	if (verb == "form" || verb == "state") && len(args) > 0 {
		verb, args = args[0], args[1:]
		if verb == "delete" {
			verb = "unset"
		}
	}
	var rest []string
	id := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--id" || a == "-i":
			if i+1 >= len(args) {
				return form.Patch{}, "", false
			}
			id = args[i+1]
			i++
		case strings.HasPrefix(a, "--id="):
			id = strings.TrimPrefix(a, "--id=")
		case strings.HasPrefix(a, "-"):
			return form.Patch{}, "", false
		default:
			rest = append(rest, a)
		}
	}
	switch verb {
	case "set":
		if len(rest) != 2 || strings.ContainsAny(rest[0], "=[]{") {
			return form.Patch{}, "", false
		}
		value := json.RawMessage(rest[1])
		if !json.Valid(value) {
			value, _ = json.Marshal(rest[1])
		}
		return form.Build(base, map[string]json.RawMessage{rest[0]: value}, nil), id, true
	case "unset":
		var keys []string
		for _, a := range rest {
			for _, k := range strings.Split(a, ",") {
				if k = strings.TrimSpace(k); k != "" {
					keys = append(keys, k)
				}
			}
		}
		if len(keys) == 0 || strings.ContainsAny(strings.Join(keys, ""), "[]") {
			return form.Patch{}, "", false
		}
		return form.Build(base, nil, keys), id, true
	}
	return form.Patch{}, "", false
}

func (in *interactiveInput) editShownForm(argv []string) bool {
	in.mu.Lock()
	v, ok := in.lt.tr.pit.live.(*formView)
	subject := in.figaroID
	in.mu.Unlock()
	if !ok || !v.editable() {
		return false
	}
	patch, id, ok := shownFormEdit(argv, v.source().Shown())
	if !ok {
		return false
	}
	if id == "" {
		id = subject
	}
	if id != v.aria {
		return false
	}
	typed := strings.Join(argv, " ")
	go func() {
		if patch.IsIdentity() {
			in.note(typed + ": unchanged")
			return
		}
		if err := v.Edit(patch); err != nil {
			in.note(typed + ": " + err.Error())
			return
		}
		in.note(typed + ": ok")
	}()
	return true
}

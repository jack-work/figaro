package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/form/replica"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/sdk"
)

const (
	formEditDebounce = 150 * time.Millisecond
	formEditMaxWait  = time.Second
	formFlushOnClose = 2 * time.Second
)

type ariaTransport struct {
	aria   *sdk.Aria
	loaded func(*rpc.FormResponse)
}

func (t ariaTransport) Set(ctx context.Context, req rpc.SetRequest) (*rpc.SetResponse, error) {
	return t.aria.SetWith(ctx, req)
}

func (t ariaTransport) Load(ctx context.Context, client string) (*rpc.FormResponse, error) {
	resp, err := t.aria.FormFor(ctx, client)
	if err == nil && resp != nil && t.loaded != nil {
		t.loaded(resp)
	}
	return resp, err
}

type replicaSource struct{ *replica.Replica }

func (s replicaSource) Edit(p form.Patch) error {
	s.Replica.Edit(p)
	return nil
}

func newReplicaClient() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "pit-" + hex.EncodeToString(b[:])
}

type formKicker struct {
	ch   chan struct{}
	done chan struct{}
	once sync.Once
}

func newFormKicker(repaint func()) *formKicker {
	k := &formKicker{ch: make(chan struct{}, 1), done: make(chan struct{})}
	go func() {
		for {
			select {
			case <-k.ch:
				if repaint != nil {
					repaint()
				}
			case <-k.done:
				return
			}
		}
	}()
	return k
}

func (k *formKicker) kick() {
	select {
	case k.ch <- struct{}{}:
	default:
	}
}

func (k *formKicker) close() { k.once.Do(func() { close(k.done) }) }

var errFormReadOnly = errors.New("this form is read-only in the pit")

func (v *formView) Edit(p form.Patch) error {
	e, ok := v.source().(formEditor)
	if !ok {
		return errFormReadOnly
	}
	v.mu.Lock()
	access := v.access
	v.mu.Unlock()
	if !access.Writable() {
		return errFormReadOnly
	}
	for _, ent := range p.Entries() {
		if d, denied := access.Refusal(ent.Key); denied {
			return fmt.Errorf("%s is read-only here: %s", ent.Key, d.Reason)
		}
	}
	v.mu.Lock()
	if v.stopped {
		notice := v.notice
		v.mu.Unlock()
		return errors.New(notice)
	}
	if v.refused {
		v.notice, v.refused = "", false
	}
	v.mu.Unlock()
	return e.Edit(p)
}

func (v *formView) refuse(err error) {
	v.mu.Lock()
	v.notice, v.refused = "an edit was refused and taken back: "+err.Error(), true
	v.mu.Unlock()
}

func (v *formView) editable() bool {
	if _, ok := v.source().(formEditor); !ok {
		return false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.access.Writable()
}

func (v *formView) setAccess(a *rpc.FormAccess) {
	v.mu.Lock()
	v.access = a
	v.items = nil
	v.mu.Unlock()
}

func (v *formView) setView(view *rpc.FormView) {
	v.mu.Lock()
	v.view = view
	v.items = nil
	v.mu.Unlock()
}

func (v *formView) present(resp *rpc.FormResponse) {
	v.setAccess(resp.Access)
	v.setView(resp.View)
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
	v.mu.Lock()
	d, denied := v.access.Refusal(path)
	v.mu.Unlock()
	if denied {
		return "", path + " is read-only here: " + d.Reason
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
	if b == 'c' && v.change != nil {
		v.change()
		return true
	}
	if v.act == nil {
		return false
	}
	a, ok := v.actionFor(b)
	if ok {
		v.act(a)
	}
	return ok
}

func actionKeyIs(a rpc.ViewAction, b byte) bool {
	if a.Key == "Enter" {
		return b == '\r' || b == '\n'
	}
	return a.Key == string(b)
}

func (v *formView) actionFor(b byte) (rpc.ViewAction, bool) {
	v.mu.Lock()
	view := v.view
	v.mu.Unlock()
	if view == nil {
		return rpc.ViewAction{}, false
	}
	for _, a := range view.Actions {
		if actionKeyIs(a, b) {
			return a, true
		}
	}
	return rpc.ViewAction{}, false
}

func (v *formView) OwnsKey(b byte) bool {
	_, ok := v.actionFor(b)
	return ok
}

func (v *formView) rowKeyOf(path string) (viewRowKey, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	k, ok := v.rowKeys[path]
	return k, ok
}

func (v *formView) armAction(a rpc.ViewAction, row string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	key := a.Key + "\x00" + row
	if !a.Confirm || v.armed == key {
		v.armed = ""
		return true
	}
	v.armed = key
	return false
}

func actionCall(a rpc.ViewAction, k viewRowKey, subject string) (json.RawMessage, string, error) {
	fields := map[string]json.RawMessage{}
	if len(a.Params) > 0 {
		if err := json.Unmarshal(a.Params, &fields); err != nil {
			return nil, "", err
		}
	}
	for field, arg := range a.Args {
		val := k.row
		if arg == rpc.ArgGroup {
			val = k.group
		}
		raw, err := json.Marshal(val)
		if err != nil {
			return nil, "", err
		}
		fields[field] = raw
	}
	target := k.row
	if k.aria != "" {
		target = k.aria
	}
	if a.Target == rpc.TargetSubject {
		target = subject
	}
	if target == "" {
		return nil, "", fmt.Errorf("%s: no aria to act on", a.Label)
	}
	params, err := json.Marshal(fields)
	if err != nil {
		return nil, "", err
	}
	return params, target, nil
}

type actOn func(ctx context.Context, method string, params json.RawMessage, figaroID string) error

func pitAct(in *interactiveInput, a rpc.ViewAction, send actOn) {
	t := in.lt.tr
	v, ok := t.pit.live.(*formView)
	if !ok {
		return
	}
	sel, ok := t.pit.selected()
	if !ok {
		return
	}
	k, ok := v.rowKeyOf(valuePath(sel.id))
	if !ok {
		t.setCommandNoteAt(a.Label+": pick a row", alertError)
		return
	}
	params, target, err := actionCall(a, k, t.subjectID())
	if err != nil {
		t.setCommandNoteAt(err.Error(), alertError)
		return
	}
	name := k.row
	if k.group != "" {
		name = k.group + "/" + k.row
	}
	if a.Local == rpc.ViewAttend {
		if t.attendAria == nil {
			t.note("this session cannot attend (no shell to bind)")
			return
		}
		t.attendAria(target)
		return
	}
	if !v.armAction(a, name) {
		t.setCommandNote("press " + a.Key + " again to " + a.Label + " " + name)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := send(ctx, a.Method, params, target); err != nil {
			in.note(a.Label + " " + name + ": " + err.Error())
			return
		}
		in.note(a.Label + " " + name + ": done")
	}()
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
		if _, optimistic := v.source().(pendingReader); optimistic {
			in.note(typed + ": sent")
			return
		}
		in.note(typed + ": ok")
	}()
	return true
}

func (in *interactiveInput) actOnDaemon(ctx context.Context, method string, params json.RawMessage, figaroID string) error {
	acli, err := in.angelus()
	if err != nil {
		return err
	}
	_, err = acli.ActOn(ctx, method, params, figaroID)
	return err
}

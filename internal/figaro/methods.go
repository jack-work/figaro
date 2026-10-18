package figaro

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/decide"
	"github.com/jack-work/figaro/internal/methods"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/turns"
)

const inquiryWalkLimit = 512

func (a *Agent) runInquiryMethods(ctx context.Context, inquiry string) {
	if a.decider == nil {
		return
	}
	snap := a.form.Snapshot()
	var defaults form.Snapshot
	if a.defaultForm != nil {
		defaults = a.defaultForm()
	}
	ms, errs := methods.Read(methods.Effective(snap, defaults))
	for _, err := range errs {
		slog.Warn("method ignored", "aria", a.id, "err", err)
	}
	ms = a.cacheSafe(onEvent(ms, methods.EventInquiry), snap)
	if len(ms) == 0 {
		return
	}
	inquiries := append([]string{inquiry}, a.inquiriesBefore(methods.Depth(ms))...)
	set := map[string]json.RawMessage{}
	for _, o := range methods.RunAll(ctx, a.decider, decide.Source{Board: snap}, ms, inquiries) {
		slog.Info("method", "aria", a.id, "method", o.Method.Name, "key", o.Method.Key,
			"verdict", string(o.Verdict), "value", o.Value, "elapsed", o.Elapsed, "err", o.Err)
		if o.Verdict == methods.Kept {
			continue
		}
		if cur := snap.Lookup(o.Method.Key); cur != nil && *cur == o.Value {
			continue
		}
		v, _ := json.Marshal(o.Value)
		set[o.Method.Key] = v
	}
	if len(set) == 0 {
		return
	}
	if _, _, err := a.applyFormPatch(form.Build(snap, set, nil), 0, false, "method"); err != nil {
		slog.Error("method form append", "aria", a.id, "err", err)
	}
}

func onEvent(ms []methods.Method, event string) []methods.Method {
	out := ms[:0:0]
	for _, m := range ms {
		if m.Event == event {
			out = append(out, m)
		}
	}
	return out
}

func (a *Agent) cacheSafe(ms []methods.Method, snap form.Snapshot) []methods.Method {
	out := ms[:0:0]
	for _, m := range ms {
		if m.KeepCache && !a.keepsCacheOnChange(m.Key, snap) {
			slog.Debug("method held: its key would break the prompt cache here", "aria", a.id, "method", m.Name, "key", m.Key)
			continue
		}
		out = append(out, m)
	}
	return out
}

func (a *Agent) keepsCacheOnChange(key string, snap form.Snapshot) bool {
	if !strings.HasPrefix(key, "system.") {
		return true
	}
	keeper, ok := a.provider().(provider.CacheKeeper)
	return ok && keeper.KeepsCacheOnChange(key, snap)
}

func (a *Agent) inquiriesBefore(n int) []string {
	if n == 0 {
		return nil
	}
	tail, ok := a.figLog.PeekTail()
	if !ok {
		return nil
	}
	var out []string
	walked := 0
	for before := tail.LT + 1; len(out) < n && walked < inquiryWalkLimit && before > 1; {
		page, _ := a.figLog.ReadPage(0, before, 32)
		if len(page) == 0 {
			break
		}
		before = page[0].LT
		out = appendInquiries(out, page, n)
		walked += len(page)
	}
	return out
}

func appendInquiries(out []string, page []store.Entry[message.Message], n int) []string {
	for i := len(page) - 1; i >= 0 && len(out) < n; i-- {
		if turns.Opens(page[i].Payload) {
			out = append(out, turns.Text(page[i].Payload))
		}
	}
	return out
}

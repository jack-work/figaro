package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/jack-work/figaro/api/rpc"
)

func listSendTo(t *transcript) {
	row, ok := t.pit.selected()
	if !ok {
		return
	}
	to, handle := t.rowAddress(row)
	if to == "" {
		t.note("that row is not a figaro")
		return
	}
	if t.sendTo == nil {
		t.note("this session cannot send")
		return
	}
	if t.ask != nil {
		t.note("answer the open question first")
		return
	}
	send := t.sendTo
	t.pose(&boxQuestion{
		prompt: "> " + handle + " ",
		title:  []string{composerGlyph + " to " + handle + " · Enter send · Esc cancel"},
		pit:    pitSendTo,
		once:   true,
		answer: func(text string) {
			if strings.TrimSpace(text) == "" {
				return
			}
			send(to, handle, text)
		},
	})
}

func (t *transcript) rowAddress(row pitRow) (to, handle string) {
	if row.to != "" {
		handle = row.yank
		if handle == "" {
			handle = ariaName(strings.TrimPrefix(row.to, "@"))
		}
		if strings.HasPrefix(row.to, "@") {
			handle = "@" + handle
		}
		return row.to, handle
	}
	if v, ok := t.pit.live.(*formView); ok && row.id != "" {
		return formRowAddress(v, row.id)
	}
	if av, ok := t.pit.live.(interface{ AriaOf(string) string }); ok && row.id != "" {
		if id := av.AriaOf(row.id); rpc.ValidateAriaID(id) == nil {
			return id, ariaName(id)
		}
	}
	return "", ""
}

func formRowAddress(v *formView, id string) (to, handle string) {
	node := ""
	if m, inner := v.split(id); m != nil {
		v, id, node = m.view, inner, m.node
	}
	k, ok := v.rowKeyOf(valuePath(id))
	if !ok || k.aria == "" {
		return "", ""
	}
	to = qualifiedIn(node, k.aria)
	return to, ariaName(to)
}

func (in *interactiveInput) sendToRow(to, handle, text string) {
	in.commandAsync(func(ctx context.Context) (string, error) {
		env, err := in.verbEnv()
		if err != nil {
			return "", fmt.Errorf("send to %s: %w", handle, err)
		}
		if _, _, err := sendVerb(ctx, env, sendPlan{spec: to, prompt: text}, nil, ""); err != nil {
			return "", fmt.Errorf("send to %s: %s", handle, strings.ReplaceAll(wireErrorText(err), "\n", " "))
		}
		return "sent to " + handle, nil
	})
}

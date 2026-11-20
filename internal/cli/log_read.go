package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/sdk"
)

type logEntry struct {
	Turn      uint64 `json:"turn"`
	LT        uint64 `json:"lt"`
	At        int64  `json:"at"`
	Sender    string `json:"sender,omitempty"`
	Principal string `json:"principal,omitempty"`
	Text      string `json:"text"`
	Images    int    `json:"images,omitempty"`
}

type entryReader interface {
	Read(ctx context.Context, req rpc.ReadRequest) (aria.Page, error)
	IRRange(ctx context.Context, figaroID string, from, before uint64) (*rpc.IRResponse, error)
}

type angelusEntries struct{ *sdk.Angelus }

func (a angelusEntries) IRRange(ctx context.Context, id string, from, before uint64) (*rpc.IRResponse, error) {
	return a.IRBefore(ctx, id, from, before, 0)
}

func readEntries(ctx context.Context, r entryReader, id string, from, until uint64) ([]logEntry, error) {
	var lo, hi uint64
	at := aria.Anchor{Turn: from}
	for {
		page, err := r.Read(ctx, rpc.ReadRequest{FigaroID: id, At: at, Until: aria.Anchor{Turn: until}, Limit: 1 << 20})
		if err != nil {
			return nil, err
		}
		for _, p := range page.Parts {
			if len(p.LTs) == 0 {
				continue
			}
			if lo == 0 || p.LTs[0] < lo {
				lo = p.LTs[0]
			}
			if last := p.LTs[len(p.LTs)-1]; last > hi {
				hi = last
			}
		}
		if !page.More.After || page.Next == nil || *page.Next == at || (until > 0 && page.Next.Turn >= until) {
			break
		}
		at = *page.Next
	}
	if lo == 0 {
		return nil, nil
	}
	var out []logEntry
	for next := lo; next <= hi; {
		resp, err := r.IRRange(ctx, id, next, hi+1)
		if err != nil {
			return nil, err
		}
		for _, e := range resp.Entries {
			var m message.Message
			if err := json.Unmarshal(e.Payload, &m); err != nil {
				return nil, fmt.Errorf("lt %d: %w", e.LT, err)
			}
			if m.Role != message.RoleInput || m.Steering || message.IsCeremonial(m) {
				continue
			}
			if m.TurnID < from || (until > 0 && m.TurnID >= until) {
				continue
			}
			out = append(out, entryOf(e.LT, m))
		}
		if resp.NextFrom == 0 || resp.NextFrom <= next {
			break
		}
		next = resp.NextFrom
	}
	return out, nil
}

func entryOf(lt uint64, m message.Message) logEntry {
	e := logEntry{Turn: m.TurnID, LT: lt, At: m.Timestamp, Principal: m.Principal}
	var text strings.Builder
	for _, c := range m.Content {
		switch c.Type {
		case message.ContentProse:
			text.WriteString(c.Text)
			if e.Sender == "" {
				e.Sender = c.Sender
			}
			if c.Principal != "" {
				e.Principal = c.Principal
			}
		case message.ContentImage:
			e.Images++
		}
	}
	e.Text = text.String()
	return e
}

func writeEntries(w io.Writer, entries []logEntry, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		for _, e := range entries {
			if err := enc.Encode(e); err != nil {
				return err
			}
		}
		return nil
	}
	for _, e := range entries {
		by := e.Sender
		if by == "" {
			by = e.Principal
		}
		fmt.Fprintf(w, "[%d] %s %s\n%s\n\n", e.Turn, time.UnixMilli(e.At).Format(time.RFC3339), by, disarmLogText(e.Text))
	}
	return nil
}

func runRead(loaded *config.Loaded, ref string, from, until uint64, asJSON bool) error {
	if err := needCapability(rpc.CapLogExact, "figaro read"); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	acli := mustConnectAngelus(loaded)
	defer acli.Close()
	id := ref
	if id == "" {
		r, err := resolveBinding(ctx, acli, shellPID)
		if err != nil {
			return fmt.Errorf("resolve: %w", err)
		}
		if !r.Found {
			return fmt.Errorf("no figaro bound to this shell")
		}
		id = r.FigaroID
	} else {
		var err error
		if id, err = doorID(ctx, acli, ref); err != nil {
			return err
		}
	}
	entries, err := readEntries(ctx, angelusEntries{acli}, id, from, until)
	if err != nil {
		return fmt.Errorf("read %s: %w", id, err)
	}
	return writeEntries(stdout, entries, asJSON)
}

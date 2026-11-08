package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/store"
)

type quarantiner interface {
	ScanQuarantine() ([]store.Quarantined, error)
	ReleaseQuarantine(id, channel string, offset int64, apply bool) (store.QuarantineRelease, error)
}

func (h *handlers) quarantiner() (quarantiner, error) {
	q, ok := h.angelus.Backend.(quarantiner)
	if !ok {
		return nil, fmt.Errorf("this store keeps no quarantine")
	}
	return q, nil
}

func quarantineEntry(q store.Quarantined) rpc.QuarantineEntry {
	return rpc.QuarantineEntry{Figaro: q.Trunk, Node: q.Node, Channel: q.Channel, Segment: q.Segment, Offset: q.Offset, Shared: q.Shared}
}

func (h *handlers) quarantineList(ctx context.Context, params json.RawMessage) (interface{}, error) {
	q, err := h.quarantiner()
	if err != nil {
		return nil, err
	}
	found, err := q.ScanQuarantine()
	if err != nil {
		return nil, err
	}
	out := rpc.QuarantineListResponse{Entries: []rpc.QuarantineEntry{}}
	for _, f := range found {
		out.Entries = append(out.Entries, quarantineEntry(f))
	}
	return out, nil
}

func (h *handlers) quarantineRelease(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.QuarantineReleaseRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if req.Figaro == "" {
		return nil, fmt.Errorf("quarantine release names a figaro")
	}
	q, err := h.quarantiner()
	if err != nil {
		return nil, err
	}
	if req.Confirm {
		done, err := h.holdAsleep(req.Figaro)
		if err != nil {
			return nil, err
		}
		defer done()
	}
	rel, err := q.ReleaseQuarantine(req.Figaro, req.Channel, req.At, req.Confirm)
	if err != nil {
		return nil, err
	}
	resp := rpc.QuarantineReleaseResponse{
		QuarantineEntry: quarantineEntry(rel.Quarantined),
		BytesCut:        rel.BytesCut, LaterSegments: rel.LaterSegments,
		Aside: rel.Aside, Trimmed: rel.Trimmed, Applied: rel.Applied,
	}
	if rel.Applied {
		who := authz.PrincipalText(authz.PrincipalOf(ctx))
		slog.Warn("quarantine released: segment cut at its corrupt frame",
			"figaro", req.Figaro, "channel", rel.Channel, "segment", rel.Segment, "offset", rel.Offset,
			"bytes_cut", rel.BytesCut, "later_segments", len(rel.LaterSegments), "aside", rel.Aside, "by", who)
		note := fmt.Sprintf("released %s\nby %s\nfigaro %s\nchannel %s\nsegment %s\ncut at byte %d (%d bytes and %d later segments set aside here)\n",
			time.Now().UTC().Format(time.RFC3339), who, req.Figaro, rel.Channel, rel.Segment, rel.Offset, rel.BytesCut, len(rel.LaterSegments))
		if err := os.WriteFile(filepath.Join(rel.Aside, "RELEASED"), []byte(note), 0o600); err != nil {
			resp.Warning = fmt.Sprintf("released, but the RELEASED note beside the copy was not written: %v", err)
		}
	}
	return resp, nil
}

func (h *handlers) holdAsleep(id string) (func(), error) {
	h.restoreMu.Lock()
	defer h.restoreMu.Unlock()
	_, waking := h.restoring[id]
	if waking || h.angelus.Registry.Get(id) != nil {
		return nil, fmt.Errorf("%s is awake; release it once it has gone dormant, or after `figaro stop`", id)
	}
	if h.releasing[id] {
		return nil, fmt.Errorf("%s is already being released", id)
	}
	if h.releasing == nil {
		h.releasing = map[string]bool{}
	}
	h.releasing[id] = true
	return func() {
		h.restoreMu.Lock()
		delete(h.releasing, id)
		h.restoreMu.Unlock()
	}, nil
}

package angelus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jack-work/jkauthz"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/slotpool"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
)

const sayTimeout = 10 * time.Second

var sayClient = &http.Client{
	Timeout:       sayTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func (h *handlers) toolsIn(sandboxName, figaroID string, otherwise func() *tool.Registry) *tool.Registry {
	loaded, _ := h.settings()
	if loaded == nil || !leasedBoxOfSlot(sandboxName, poolSlots(loaded)) {
		return otherwise()
	}
	pool, _ := poolOfLeasedBox(loaded, sandboxName)
	return h.poolTools(pool, sandboxName, figaroID)
}

func (h *handlers) poolTools(pool config.PoolConfig, box, figaroID string) *tool.Registry {
	reg := tool.NewRegistry()
	reg.MustRegister(&tool.SayTool{Say: func(ctx context.Context, text string) error {
		return h.sayAs(ctx, box, figaroID, text)
	}})
	if pool.SearchURL != "" {
		reg.MustRegister(&tool.SearchTool{Search: func(ctx context.Context, query string, count int) ([]tool.SearchResult, error) {
			return h.searchAs(ctx, box, figaroID, query, count)
		}})
	}
	return reg
}

func (h *handlers) sayAs(ctx context.Context, box, figaroID, text string) error {
	say := h.angelus.Handlers[rpc.MethodSlotSay]
	if say == nil {
		return errors.New("chat: this daemon says nothing")
	}
	params, err := rpc.WithCaller(rpc.SlotSayRequest{Text: text}, figaroID, nil)
	if err != nil {
		return err
	}
	ctx = authz.WithSubject(inProcess(ctx), jkauthz.Subject{Principal: authz.BoxPrincipal(box)})
	_, err = say(ctx, params)
	return err
}

func (h *handlers) slotSay(ctx context.Context, params json.RawMessage) (any, error) {
	if authz.DoorOf(ctx) != authz.DoorInProcess {
		return nil, fmt.Errorf("%s is served to a leased figaro's own tool, through no door", rpc.MethodSlotSay)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(params, &fields); err != nil {
		return nil, fmt.Errorf("%s: %w", rpc.MethodSlotSay, err)
	}
	delete(fields, rpc.CallerKey)
	bare, _ := json.Marshal(fields)
	var req rpc.SlotSayRequest
	if err := decodeExactly(bare, &req); err != nil {
		return nil, fmt.Errorf("%s takes text and nothing else: %w", rpc.MethodSlotSay, err)
	}
	box, boxed := callerBox(ctx)
	slots := h.angelus.Slots
	if !boxed || slots == nil {
		return nil, fmt.Errorf("%s: the caller holds no lease", rpc.MethodSlotSay)
	}
	row, ok := slots.leaseOf(box)
	if !ok || row.State != slotpool.Live || row.SayBearer == "" {
		return nil, fmt.Errorf("%s: lease %s is not live", rpc.MethodSlotSay, box)
	}
	if err := h.speaksFor(box, authz.FigaroOf(ctx)); err != nil {
		return nil, fmt.Errorf("%s: %w", rpc.MethodSlotSay, err)
	}
	loaded, _ := h.settings()
	pool, ok := loaded.Pool(row.Pool)
	if !ok || !pool.Enabled {
		return nil, fmt.Errorf("%s: pool %s is not enabled", rpc.MethodSlotSay, row.Pool)
	}
	if err := slots.spend(box, "say", perMinute("say_per_minute", pool.SayBound())); err != nil {
		return nil, fmt.Errorf("%s: %w", rpc.MethodSlotSay, err)
	}
	body, _ := json.Marshal(map[string]any{"key": row.Key, "generation": row.Generation, "text": req.Text})
	post, err := http.NewRequestWithContext(ctx, http.MethodPost, pool.SayURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rpc.MethodSlotSay, err)
	}
	post.Header.Set("Content-Type", "application/json")
	post.Header.Set("Authorization", "Bearer "+row.SayBearer)
	client := slots.SayClient
	if client == nil {
		client = sayClient
	}
	resp, err := client.Do(post)
	if err != nil {
		return nil, fmt.Errorf("%s: the broker was not reached", rpc.MethodSlotSay)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%s: the broker answered %d", rpc.MethodSlotSay, resp.StatusCode)
	}
	return rpc.SlotSayResponse{Said: true}, nil
}

func (h *handlers) speaksFor(box, figaroID string) error {
	if figaroID == "" {
		return errors.New("the call names no figaro")
	}
	n, ok := h.angelus.Backend.Node(figaroID)
	if !ok || n.Kind != store.KindConversation {
		return fmt.Errorf("%s is no figaro here", figaroID)
	}
	snap, err := h.angelus.Backend.FormState(figaroID)
	if err != nil {
		return fmt.Errorf("the box of %s cannot be read", figaroID)
	}
	if s := snap.Lookup("system.sandbox"); s == nil || *s != box {
		return fmt.Errorf("%s does not run in lease %s", figaroID, box)
	}
	return nil
}

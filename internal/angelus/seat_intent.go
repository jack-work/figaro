package angelus

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/jack-work/figaro/internal/hierarchy"
)

type seatRecord struct {
	Predecessor string `json:"predecessor"`
	Successor   string `json:"successor"`
	Was         string `json:"was,omitempty"`
	ByHand      bool   `json:"by_hand,omitempty"`
}

type seatIntents struct {
	mu   sync.Mutex
	path string
	recs map[string]seatRecord
	left []seatRecord
}

func seatsPath(successionsPath string) string {
	if successionsPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(successionsPath), "seats.json")
}

func newSeats(path string) *seatIntents {
	s := &seatIntents{path: path, recs: map[string]seatRecord{}}
	if path == "" {
		return s
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Error("seats: read", "path", path, "err", err)
		}
		return s
	}
	if err := json.Unmarshal(raw, &s.left); err != nil {
		slog.Error("seats: decode", "path", path, "err", err)
		s.left = nil
	}
	for _, r := range s.left {
		s.recs[r.Successor] = r
	}
	return s
}

func (s *seatIntents) begin(r seatRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs[r.Successor] = r
	return s.saveLocked()
}

func (s *seatIntents) end(successor string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.recs, successor)
	if err := s.saveLocked(); err != nil {
		slog.Error("seats: forget a finished seat", "successor", successor, "err", err)
	}
}

func (s *seatIntents) takeLeft() []seatRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	left := s.left
	s.left = nil
	return left
}

func (s *seatIntents) saveLocked() error {
	if s.path == "" {
		return nil
	}
	recs := make([]seatRecord, 0, len(s.recs))
	for _, r := range s.recs {
		recs = append(recs, r)
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Successor < recs[j].Successor })
	raw, err := json.Marshal(recs)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (h *handlers) seatIntent(successor, pred string) seatRecord {
	topo := h.angelus.topology()
	r := seatRecord{Predecessor: pred, Successor: successor}
	r.Was, _ = topo.Drawn(successor)
	_, r.ByHand = topo.DrawnByHand(successor)
	return r
}

func (h *handlers) unseat(r seatRecord) error {
	topo := h.angelus.topology()
	if have, _ := topo.Drawn(r.Successor); have != r.Predecessor {
		return nil
	}
	plan := hierarchy.Plan{Op: hierarchy.Arrange, Parents: map[string]string{r.Successor: r.Was}}
	if !r.ByHand {
		plan = hierarchy.Plan{Op: hierarchy.Arrange, Restore: []string{r.Successor}}
	}
	by, err := h.angelus.topologyPrincipal(h.ownerContext())
	if err != nil {
		return err
	}
	_, err = topo.Apply(h.ownerContext(), by, plan)
	return err
}

func (h *handlers) finishSeats() {
	if h.seats == nil {
		return
	}
	topo := h.angelus.topology()
	for _, r := range h.seats.takeLeft() {
		pred, _ := topo.Drawn(r.Predecessor)
		succ, _ := topo.Drawn(r.Successor)
		switch {
		case pred == r.Successor:
			slog.Info("seats: a seat cut short by a stop had finished", "predecessor", r.Predecessor, "successor", r.Successor)
		case succ == r.Predecessor:
			if err := h.unseat(r); err != nil {
				slog.Error("seats: a seat cut short by a stop could not be undone", "predecessor", r.Predecessor, "successor", r.Successor, "err", err)
				break
			}
			slog.Warn("seats: undid a seat cut short by a stop; nothing seats without its caller", "predecessor", r.Predecessor, "successor", r.Successor)
		default:
			slog.Warn("seats: a seat cut short by a stop was overtaken; the tree is left as it is", "predecessor", r.Predecessor, "successor", r.Successor)
		}
		h.seats.end(r.Successor)
	}
}

package angelus

import (
	"context"
	"log/slog"
	"math"
	"runtime/debug"
	"runtime/metrics"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

const (
	memPressureHigh   = 0.85
	memPressureLow    = 0.75
	memPressureSample = 30 * time.Second
	memPressureRuns   = 3
)

type heapSample struct {
	live   uint64
	cycles uint64
}

func readHeap() heapSample {
	s := []metrics.Sample{
		{Name: "/gc/heap/live:bytes"},
		{Name: "/gc/cycles/total:gc-cycles"},
	}
	metrics.Read(s)
	var h heapSample
	if s[0].Value.Kind() == metrics.KindUint64 {
		h.live = s[0].Value.Uint64()
	}
	if s[1].Value.Kind() == metrics.KindUint64 {
		h.cycles = s[1].Value.Uint64()
	}
	return h
}

func memLimit() int64 { return debug.SetMemoryLimit(-1) }

func underPressure(live uint64, limit int64, frac float64) bool {
	if limit <= 0 || limit == math.MaxInt64 {
		return false
	}
	return float64(live) >= frac*float64(limit)
}

func (a *Angelus) fillFormMem(st *rpc.MemStatus) {
	h := readHeap()
	st.HeapLiveBytes = h.live
	st.MemPressure = underPressure(h.live, st.MemLimitBytes, memPressureHigh)
	if fr, ok := a.Backend.(interface{ FormResidency() store.FormResidency }); ok {
		r := fr.FormResidency()
		st.ResidentForms = r.Forms
		st.SubscribedForms = r.SubscribedForms
		st.LibrettoForms = r.LibrettoForms
		st.FormWindowPatches = r.WindowPatches
		st.FormStateBytes = r.StateBytes
		st.FormPatchBytes = r.PatchBytes
		st.LibrettoFormBytes = r.LibrettoBytes
	}
	st.ValueMemoBytes, st.ValueCanonBytes = form.MemoBytes()
	st.OpenSegments, st.SegmentHeaderBytes = store.OpenSegments()
}

func (a *Angelus) watchMemPressure(ctx context.Context) {
	t := time.NewTicker(memPressureSample)
	defer t.Stop()
	var runs int
	var warned bool
	prev := readHeap()
	prevAt := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			h := readHeap()
			limit := memLimit()
			rate := float64(h.cycles-prev.cycles) / now.Sub(prevAt).Seconds()
			prev, prevAt = h, now
			switch {
			case underPressure(h.live, limit, memPressureHigh):
				runs++
				if runs >= memPressureRuns && !warned {
					warned = true
					slog.Warn("heap live is near the memory limit; the collector runs continuously",
						"live_bytes", h.live, "limit_bytes", limit, "gc_per_s", rate,
						"hint", "figaro doctor mem")
				}
			case warned && !underPressure(h.live, limit, memPressureLow):
				warned, runs = false, 0
				slog.Info("heap live is back under the memory limit",
					"live_bytes", h.live, "limit_bytes", limit, "gc_per_s", rate)
			default:
				runs = 0
			}
		}
	}
}

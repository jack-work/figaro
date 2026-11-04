package store

import (
	"fmt"
	"os"
	"runtime"
	"runtime/metrics"
	"runtime/pprof"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/store/segment"
)

func memworkInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

func liveHeap() uint64 {
	runtime.GC()
	runtime.GC()
	s := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
	metrics.Read(s)
	return s[0].Value.Uint64()
}

func skillsPatch(prefix string, skills, size int) map[string]string {
	kv := make(map[string]string, skills)
	body := strings.Repeat(prefix, size/len(prefix)+1)[:size]
	for i := range skills {
		kv[fmt.Sprintf("skills.s%02d.content", i)] = body
		kv[fmt.Sprintf("skills.s%02d.filePath", i)] = fmt.Sprintf("/skills/s%02d.md", i)
	}
	return kv
}

// Issue #59's shape, in one process: dressed arias whose forms carry skill
// bodies, re-dressed over and over, some of them studied. It reports the live
// heap after GC against what FormResidency and the Value memos account for.
// Off unless FIGARO_MEMWORK is set; sizes come from FIGARO_MEMWORK_*.
func TestMemWorkloadIssue59(t *testing.T) {
	if os.Getenv("FIGARO_MEMWORK") == "" {
		t.Skip("set FIGARO_MEMWORK=1 to run the issue #59 workload")
	}
	forms := memworkInt("FIGARO_MEMWORK_FORMS", 60)
	skills := memworkInt("FIGARO_MEMWORK_SKILLS", 20)
	size := memworkInt("FIGARO_MEMWORK_SKILL_BYTES", 4096)
	churn := memworkInt("FIGARO_MEMWORK_CHURN", 200)
	studied := memworkInt("FIGARO_MEMWORK_STUDIED", 20)

	base := liveHeap()
	be, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { be.Close() })

	outfit, err := be.CreateOutfit("work", setPatch(skillsPatch("a", skills, size)))
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, forms)
	for i := range ids {
		if ids[i], err = be.CreateConversation(outfit); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < studied && i+1 < len(ids); i++ {
		if _, err := be.StudyForm(ids[len(ids)-1-i], ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	for round := range churn {
		prefix := "a"
		if round%2 == 0 {
			prefix = "b"
		}
		p := setPatch(skillsPatch(prefix, skills, size))
		for _, id := range ids {
			if _, err := be.ApplyForm(id, p); err != nil {
				t.Fatal(err)
			}
		}
		be.SweepCacheBudgets()
	}
	took := time.Since(start)

	be.SweepCacheBudgets()
	live := liveHeap() - base
	r := be.FormResidency()
	segCache := SegmentCacheBytesForTest()
	openSegs, headers := OpenSegments()
	memo, canon := form.MemoBytes()
	accounted := uint64(r.StateBytes + r.PatchBytes + memo + canon + segCache + headers)
	t.Logf("workload: forms=%d skills=%dx%dB churn=%d studied=%d (%s, %d writes)",
		forms, skills, size, churn, studied, took.Round(time.Millisecond), forms*churn)
	t.Logf("live heap (after GC, minus baseline) = %.1f MiB; segment payload cache %.1f MiB", float64(live)/(1<<20), mib(segCache))
	t.Logf("residency: forms=%d subscribed=%d librettos=%d window-patches=%d open-segments=%d headers=%.1f MiB",
		r.Forms, r.SubscribedForms, r.LibrettoForms, r.WindowPatches, openSegs, mib(headers))
	t.Logf("accounted: state=%.1f MiB patches=%.1f MiB (librettos %.1f) memo=%.1f MiB canonical=%.1f MiB segments=%.1f MiB -> %.1f MiB (%.0f%% of live)",
		mib(r.StateBytes), mib(r.PatchBytes), mib(r.LibrettoBytes), mib(memo), mib(canon), mib(segCache),
		float64(accounted)/(1<<20), 100*float64(accounted)/float64(live))
	if out := os.Getenv("FIGARO_MEMWORK_PROFILE"); out != "" {
		f, err := os.Create(out)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := pprof.Lookup("heap").WriteTo(f, 0); err != nil {
			t.Fatal(err)
		}
	}
}

func mib(n int64) float64 { return float64(n) / (1 << 20) }

func SegmentCacheBytesForTest() int64 { return segment.CachedBytes() }

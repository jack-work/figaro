package store

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/stretchr/testify/require"
)

// Measures forks running at once, on a store on real disk: set
// FIGARO_FORK_BENCH=1 and point TMPDIR at a disk-backed directory, since a
// tmpfs makes every fsync free and hides the cost a birth pays.
func TestConcurrentForkLatency(t *testing.T) {
	if os.Getenv("FIGARO_FORK_BENCH") == "" {
		t.Skip("set FIGARO_FORK_BENCH=1 to measure")
	}
	be, err := NewXwalBackend(t.TempDir(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { be.Close() })
	outfit, err := be.CreateOutfit("bench", message.Patch{})
	require.NoError(t, err)

	const parents = 8
	ids := make([]string, parents)
	for p := range ids {
		id, err := be.CreateConversation(outfit)
		require.NoError(t, err)
		log, err := be.OpenFigIR(id)
		require.NoError(t, err)
		for i := 0; i < 40; i++ {
			role := message.RoleInput
			if i%2 == 1 {
				role = message.RoleOutput
			}
			_, err := log.Append(arialog.Entry[message.Message]{Payload: message.Message{
				Role:    role,
				TurnID:  uint64(i/2 + 1),
				Content: []message.Content{{Type: message.ContentProse, Text: fmt.Sprintf("%d:%d", p, i)}},
			}})
			require.NoError(t, err)
		}
		writeBoard(t, be, id, 0, 12)
		ids[p] = id
	}

	rounds := 12
	for _, width := range []int{1, 4, 8} {
		var lat []time.Duration
		var walls []time.Duration
		var mu sync.Mutex
		for r := 0; r < rounds; r++ {
			var wg sync.WaitGroup
			start := time.Now()
			for g := 0; g < width; g++ {
				wg.Add(1)
				go func(g int) {
					defer wg.Done()
					raw, _ := json.Marshal(fmt.Sprintf("r%dg%d", r, g))
					patch := form.Build(form.Snapshot{}, map[string]json.RawMessage{"bench.birth": raw}, nil)
					t0 := time.Now()
					_, _, err := be.ForkWith(ids[g%parents], 0, patch)
					d := time.Since(t0)
					if err != nil {
						t.Error(err)
						return
					}
					mu.Lock()
					lat = append(lat, d)
					mu.Unlock()
				}(g)
			}
			wg.Wait()
			walls = append(walls, time.Since(start))
		}
		t.Logf("width=%d forks=%d p50=%v p99=%v wall_p50=%v", width, len(lat), pct(lat, 50), pct(lat, 99), pct(walls, 50))
	}
}

func pct(ds []time.Duration, p int) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := (len(s)*p + 99) / 100
	if i < 1 {
		i = 1
	}
	if i > len(s) {
		i = len(s)
	}
	return s[i-1]
}

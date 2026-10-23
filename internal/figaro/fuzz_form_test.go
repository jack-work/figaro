package figaro_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
)

// Stress harness for the unkeyed form channel: ApplyForm serializes against
// nothing, so a set can land mid-turn; each IR record stamps the bound form version
// it was written at. No sleeps: turns are awaited on turn.done, and the inbox's
// FIFO is the barrier proving every queued set drained.

// fuzzTurnTimeout is a liveness guard, not synchronisation; every queued set
// is an fsync, and a loaded disk needs the room.
const fuzzTurnTimeout = 20 * time.Second

// gateProvider parks inside Send until the test opens the gate. The channels
// are shared across instances so a test can release whichever provider won the rebind.
type gateProvider struct {
	name    string
	entered chan string   // one send per Send entry, carrying the instance name
	gate    chan struct{} // one token released per Send
	sends   atomic.Int32
}

func newGate() (entered chan string, gate chan struct{}) {
	return make(chan string, 64), make(chan struct{}, 64)
}

func (p *gateProvider) Name() string        { return p.name }
func (p *gateProvider) Fingerprint() string { return p.name + "/gate" }
func (p *gateProvider) SetModel(string)     {}
func (p *gateProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}

func (p *gateProvider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	p.sends.Add(1)
	select {
	case p.entered <- p.name:
	default:
	}
	select {
	case <-p.gate:
	case <-ctx.Done():
	case <-time.After(fuzzTurnTimeout):
	}
	msg := message.Message{
		Role:       message.RoleOutput,
		Content:    []message.Content{message.TextContent("ok from " + p.name)},
		StopReason: message.StopEnd,
	}
	bus.PushMessageEnd(string(msg.StopReason))
	bus.PushFigaro(msg)
	return nil
}

func openGate(gate chan struct{}, n int) {
	for i := 0; i < n; i++ {
		gate <- struct{}{}
	}
}

// fuzzAgent is backed: only a backed aria routes set through ApplyForm and stamps FormChannelVersion.
func fuzzAgent(t *testing.T, prov provider.Provider, factory figaro.ProviderFactory) (*figaro.Agent, store.Backend, string) {
	t.Helper()
	backend, id := backedConv(t, t.TempDir())
	snap, err := backend.FormState(id)
	require.NoError(t, err)
	cb, err := form.Open("")
	require.NoError(t, err)
	cb.Apply(snap.AsPatch())
	a := figaro.NewAgent(figaro.Config{
		Projector:       uiir.New(nil),
		ID:              id,
		Provider:        prov,
		ProviderFactory: factory,
		Backend:         backend,
		Form:            cb,
	})
	t.Cleanup(a.Kill)
	return a, backend, id
}

// awaitTurnDone is the only barrier for "the turn finished".
func awaitTurnDone(t *testing.T, ch <-chan rpc.Notification) {
	t.Helper()
	deadline := time.After(fuzzTurnTimeout)
	for {
		select {
		case n, ok := <-ch:
			if !ok {
				t.Fatal("notification channel closed before turn.done")
			}
			if n.Method == rpc.MethodTurnDone {
				return
			}
		case <-deadline:
			t.Fatal("timeout waiting for turn.done")
		}
	}
}

func awaitEntered(t *testing.T, entered <-chan string) string {
	t.Helper()
	select {
	case name := <-entered:
		return name
	case <-time.After(fuzzTurnTimeout):
		t.Fatal("timeout waiting for the provider round to start")
		return ""
	}
}

func setKV(a *figaro.Agent, key, val string) error {
	raw, err := json.Marshal(val)
	if err != nil {
		return err
	}
	_, _, err = a.Set(form.Build(form.Snapshot{}, map[string]json.RawMessage{key: raw}, nil), 0)
	return err
}

func snapMap(s form.Snapshot) map[string]string {
	out := map[string]string{}
	for k, v := range s.All() {
		out[k] = string(v)
	}
	return out
}

// barrier submits a prompt and waits for turn.done: Set and SubmitPrompt share
// one FIFO, so a completed turn proves every earlier set applied.
func barrier(t *testing.T, a *figaro.Agent, ch <-chan rpc.Notification, gate chan struct{}, text string) {
	t.Helper()
	submitPrompt(a, text)
	openGate(gate, 1)
	awaitTurnDone(t, ch)
}

func TestFuzzFormUnkeyed(t *testing.T) {
	t.Run("SetWhileTurnInFlight", func(t *testing.T) {
		entered, gate := newGate()
		prov := &gateProvider{name: "gate", entered: entered, gate: gate}
		a, backend, id := fuzzAgent(t, prov, nil)
		ch, unsub := subscribeChan(a)
		defer unsub()

		const (
			writers       = 4
			setsPerWriter = 40
		)

		submitPrompt(a, "the long turn")
		awaitEntered(t, entered) // the round is genuinely in flight now

		var wg sync.WaitGroup
		errs := make(chan error, writers*setsPerWriter+setsPerWriter)
		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for i := 0; i < setsPerWriter; i++ {
					key := fmt.Sprintf("fuzz.w%d.k%d", w, i)
					if err := setKV(a, key, fmt.Sprintf("v%d", i)); err != nil {
						errs <- err
						return
					}
				}
			}(w)
		}
		// One writer straight at the channel: ApplyForm takes no lock against the
		// turn, so it must complete while the round is parked. Its writes never reach
		// the in-memory bound form, so only the durable bound form is asserted.
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < setsPerWriter; i++ {
				raw, _ := json.Marshal(fmt.Sprintf("d%d", i))
				if _, err := backend.ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{fmt.Sprintf("fuzz.direct.k%d", i): raw}, nil)); err != nil {
					errs <- err
					return
				}
			}
		}()
		wg.Wait() // must return with the provider round still parked
		close(errs)
		for err := range errs {
			require.NoError(t, err)
		}

		openGate(gate, 1) // let the in-flight round finish
		awaitTurnDone(t, ch)

		barrier(t, a, ch, gate, "barrier")

		live := a.Snapshot()
		durable, err := backend.FormState(id)
		require.NoError(t, err)
		for w := 0; w < writers; w++ {
			for i := 0; i < setsPerWriter; i++ {
				key := fmt.Sprintf("fuzz.w%d.k%d", w, i)
				want := fmt.Sprintf("%q", fmt.Sprintf("v%d", i))
				got, ok := live.Get(key)
				require.True(t, ok, "live bound form is missing %s", key)
				require.Equal(t, want, string(got), "live bound form value for %s", key)
				got, ok = durable.Get(key)
				require.True(t, ok, "durable bound form is missing %s", key)
				require.Equal(t, want, string(got), "durable bound form value for %s", key)
			}
		}
		for i := 0; i < setsPerWriter; i++ {
			key := fmt.Sprintf("fuzz.direct.k%d", i)
			got, ok := durable.Get(key)
			require.True(t, ok, "durable bound form is missing the mid-turn direct write %s", key)
			require.Equal(t, fmt.Sprintf("%q", fmt.Sprintf("d%d", i)), string(got))
		}
		assert.EqualValues(t, 2, prov.sends.Load(), "one gated turn plus the barrier turn")
	})

	t.Run("ProviderSwitchAcrossAndMidTurn", func(t *testing.T) {
		// A provider switch is just a patch; the contract is no panic, no deadlock,
		// turn.done every time, and the bound form's provider serving after the boundary.
		entered, gate := newGate()
		var mu sync.Mutex
		insts := map[string]*gateProvider{}
		factory := func(name string, _ provider.Knobs) (provider.Provider, error) {
			mu.Lock()
			defer mu.Unlock()
			p, ok := insts[name]
			if !ok {
				p = &gateProvider{name: name, entered: entered, gate: gate}
				insts[name] = p
			}
			return p, nil
		}
		inst := func(name string) *gateProvider {
			mu.Lock()
			defer mu.Unlock()
			return insts[name]
		}

		alpha, err := factory("alpha", provider.Knobs{})
		require.NoError(t, err)
		a, _, _ := fuzzAgent(t, alpha, factory)
		require.NoError(t, setKV(a, "system.provider", "alpha"))
		ch, unsub := subscribeChan(a)
		defer unsub()

		names := []string{"beta", "gamma", "delta", "beta"}
		for i, next := range names {
			submitPrompt(a, fmt.Sprintf("round %d", i))
			awaitEntered(t, entered)
			require.NoError(t, setKV(a, "system.provider", next))
			require.NoError(t, setKV(a, "system.model", fmt.Sprintf("m-%d", i)))
			openGate(gate, 1)
			awaitTurnDone(t, ch)

			barrier(t, a, ch, gate, fmt.Sprintf("after %d", i))
			require.NotNil(t, inst(next), "provider %q was never built", next)
			assert.Positive(t, inst(next).sends.Load(), "provider %q never served a round", next)
			assert.Equal(t, next, a.Info().Provider, "status must report the live provider")
		}
	})

	t.Run("InteriorForkTakesTheBoundFormAsOfThatTurn", func(t *testing.T) {
		// The fork inherits the bound form as of the forked turn, not as it stands now.
		entered, gate := newGate()
		prov := &gateProvider{name: "gate", entered: entered, gate: gate}
		a, backend, id := fuzzAgent(t, prov, nil)
		ch, unsub := subscribeChan(a)
		defer unsub()

		const turns = 6
		type mark struct {
			lt    uint64
			bound map[string]string
		}
		marks := make([]mark, 0, turns)

		log, err := backend.OpenFigIR(id)
		require.NoError(t, err)

		for k := 0; k < turns; k++ {
			for j := 0; j < 3; j++ {
				require.NoError(t, setKV(a, fmt.Sprintf("turn%d.pre%d", k, j), fmt.Sprintf("v%d", j)))
			}
			submitPrompt(a, fmt.Sprintf("turn %d", k))
			awaitEntered(t, entered)
			require.NoError(t, setKV(a, fmt.Sprintf("turn%d.mid", k), "in-flight"))
			openGate(gate, 1)
			awaitTurnDone(t, ch)
			barrier(t, a, ch, gate, fmt.Sprintf("settle %d", k))

			entries := log.Read()
			require.NotEmpty(t, entries)
			bound, err := backend.FormState(id)
			require.NoError(t, err)
			marks = append(marks, mark{lt: entries[len(entries)-1].LT, bound: snapMap(bound)})
		}

		at := marks[2]
		cont, alt, err := backend.ForkAt(id, at.lt)
		require.NoError(t, err)
		require.Equal(t, id, cont, "the trunk id is stable across a fork")
		require.NotEmpty(t, alt)
		require.NotEqual(t, id, alt)

		altBoundForm, err := backend.FormState(alt)
		require.NoError(t, err)
		nowBoundForm, err := backend.FormState(id)
		require.NoError(t, err)

		assert.Equal(t, at.bound, snapMap(altBoundForm),
			"an interior fork must inherit the bound form as of turn 2, not the current one")
		assert.NotEqual(t, snapMap(nowBoundForm), snapMap(altBoundForm),
			"turns 3..5 wrote keys the fork must not have inherited")
		for _, key := range []string{"turn5.pre0", "turn5.mid", "turn4.mid"} {
			assert.False(t, altBoundForm.Has(key), "fork inherited %s from after its fork point", key)
		}
		for _, key := range []string{"turn0.pre0", "turn2.pre2", "turn2.mid"} {
			assert.True(t, altBoundForm.Has(key), "fork is missing %s from before its fork point", key)
		}
	})

	t.Run("ConcurrentPatchVersionsAreUniqueAndMonotonic", func(t *testing.T) {
		// The version a patch comes back with is its durable index: unique per
		// writer, never backwards, and the fold holds every key.
		backend, id := backedConv(t, t.TempDir())

		const (
			writers        = 8
			patchesPerHand = 25
		)
		type result struct {
			versions []uint64
			err      error
		}
		results := make([]result, writers)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				var got []uint64
				<-start // release all writers at once, no sleeps
				for i := 0; i < patchesPerHand; i++ {
					raw, _ := json.Marshal(fmt.Sprintf("w%d-i%d", w, i))
					v, err := backend.ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{fmt.Sprintf("conc.w%d.k%d", w, i): raw}, nil))
					if err != nil {
						results[w] = result{versions: got, err: err}
						return
					}
					got = append(got, v)
				}
				results[w] = result{versions: got}
			}(w)
		}
		close(start)
		wg.Wait()

		seen := map[uint64]string{}
		var all []uint64
		for w, r := range results {
			require.NoError(t, r.err)
			require.Len(t, r.versions, patchesPerHand)
			for i, v := range r.versions {
				require.NotZero(t, v, "a persisted patch must carry a version")
				if prev, dup := seen[v]; dup {
					t.Fatalf("version %d handed to two patches: %s and w%d.k%d", v, prev, w, i)
				}
				seen[v] = fmt.Sprintf("w%d.k%d", w, i)
				if i > 0 {
					assert.Greater(t, v, r.versions[i-1],
						"writer %d saw a version go backwards at %d", w, i)
				}
				all = append(all, v)
			}
		}
		sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
		for i := 1; i < len(all); i++ {
			require.Equal(t, all[i-1]+1, all[i],
				"versions are append positions: they must form one contiguous run")
		}

		bound, err := backend.FormState(id)
		require.NoError(t, err)
		for w := 0; w < writers; w++ {
			for i := 0; i < patchesPerHand; i++ {
				key := fmt.Sprintf("conc.w%d.k%d", w, i)
				got, ok := bound.Get(key)
				require.True(t, ok, "bound form is missing %s", key)
				require.Equal(t, fmt.Sprintf("%q", fmt.Sprintf("w%d-i%d", w, i)), string(got))
			}
		}
	})
}

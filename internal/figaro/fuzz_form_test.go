package figaro_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
)

// ---------------------------------------------------------------------------
// A stress harness for the UNKEYED form channel.
//
// The board no longer keys its patches to a main LT: ApplyForm appends
// with no reference to the timeline and serializes against nothing, so a `set`
// can land while a turn is in flight. The association runs the other way --
// each IR record stamps the board version it was written at (Entry.FormChannelVersion)
// -- and that stamp is what an interior fork inherits.
//
// Everything here drives real Agents over a real XwalBackend with a stub
// provider. No provider round-trip, no sleeps as synchronisation: turns are
// awaited on rpc.MethodTurnDone, and the inbox's FIFO discipline is used as the
// barrier that proves every queued `set` has been drained (a prompt submitted
// after N sets cannot be serviced before them).
// ---------------------------------------------------------------------------

// fuzzTurnTimeout is a LIVENESS guard, not synchronisation: a wedged test
// must fail on an assertion rather than hang the package.
//
// Raised from 5s because the WAL change made every queued `set` a mandatory
// fsync, and SetWhileTurnInFlight drains 160 of them inside one turn. At ~3
// ms each that is half a second on an idle box and several times that when
// `go test ./...` has a dozen packages competing for the same disk -- which
// is where it timed out twice, on two different builds, while passing at
// -count=5 and -count=8 in isolation. The deadline was chosen before
// durability was mandatory and nobody moved it.
const fuzzTurnTimeout = 20 * time.Second

// gateProvider is a stub provider that parks inside Send until the test opens
// the gate. That park IS the "turn in flight" window every subtest below needs;
// nothing else about it is interesting. The channels are shared across every
// instance a factory hands out, so a test can release "whichever provider is
// serving" without knowing which one won the rebind.
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
	// Park until the test releases this round. The deadline is a liveness
	// guard, not synchronisation: a wedged test must fail on an assertion,
	// not hang the package.
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
	entry, err := in.FigLog.Append(store.Entry[message.Message]{Payload: msg})
	if err != nil {
		return err
	}
	msg.LogicalTime = entry.LT
	bus.PushMessageEnd(string(msg.StopReason))
	bus.PushFigaro(msg)
	return nil
}

// open releases n parked (or future) rounds.
func openGate(gate chan struct{}, n int) {
	for i := 0; i < n; i++ {
		gate <- struct{}{}
	}
}

// fuzzAgent builds a BACKED agent (real XwalBackend, real form channel)
// around the given provider/factory. Backed is the point: only a backed aria
// routes `set` through ApplyForm and stamps FormChannelVersion on its IR.
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
		SocketPath:      filepath.Join(t.TempDir(), "figaro.sock"),
		Provider:        prov,
		ProviderFactory: factory,
		Backend:         backend,
		Form:            cb,
	})
	t.Cleanup(a.Kill)
	return a, backend, id
}

// awaitTurnDone drains notifications until turn.done. It is the only barrier
// the harness uses for "the turn finished".
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

// awaitEntered waits for a provider round to actually be in flight.
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

// setKV is one fuzz patch.
func setKV(a *figaro.Agent, key, val string) error {
	raw, err := json.Marshal(val)
	if err != nil {
		return err
	}
	_, _, err = a.Set(form.Patch{Set: map[string]json.RawMessage{key: raw}}, 0)
	return err
}

// snapMap flattens a snapshot for comparison.
func snapMap(s form.Snapshot) map[string]string {
	out := map[string]string{}
	for k, v := range s.All() {
		out[k] = string(v)
	}
	return out
}

// barrier submits a prompt and waits for its turn.done. Because Agent.Set and
// SubmitPrompt share one FIFO inbox, a completed turn proves every set enqueued
// before the prompt has already been applied. This is the harness's ordering
// primitive -- there are no sleeps.
func barrier(t *testing.T, a *figaro.Agent, ch <-chan rpc.Notification, gate chan struct{}, text string) {
	t.Helper()
	submitPrompt(a, text)
	openGate(gate, 1)
	awaitTurnDone(t, ch)
}

func TestFuzzFormUnkeyed(t *testing.T) {
	t.Run("SetWhileTurnInFlight", func(t *testing.T) {
		// A `set` landing MID-TURN, over and over, from several goroutines.
		// The turn must still complete and the board must carry every patch.
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
		// And one writer straight at the channel, bypassing the inbox. THIS is
		// the unkeyed path in the narrow sense: ApplyForm reads no
		// timeline and takes no lock against the turn, so it must complete
		// while the round is parked rather than queue behind it. Its writes
		// never reach the agent's in-memory board (nothing told it), so only
		// the durable board is asserted for these keys.
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < setsPerWriter; i++ {
				raw, _ := json.Marshal(fmt.Sprintf("d%d", i))
				if _, err := backend.ApplyForm(id, message.Patch{
					Set: map[string]json.RawMessage{fmt.Sprintf("fuzz.direct.k%d", i): raw},
				}); err != nil {
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

		// FIFO barrier: this turn cannot run until every set above drained.
		barrier(t, a, ch, gate, "barrier")

		live := a.Snapshot()
		durable, err := backend.FormState(id)
		require.NoError(t, err)
		for w := 0; w < writers; w++ {
			for i := 0; i < setsPerWriter; i++ {
				key := fmt.Sprintf("fuzz.w%d.k%d", w, i)
				want := fmt.Sprintf("%q", fmt.Sprintf("v%d", i))
				got, ok := live.Get(key)
				require.True(t, ok, "live board is missing %s", key)
				require.Equal(t, want, string(got), "live board value for %s", key)
				got, ok = durable.Get(key)
				require.True(t, ok, "durable board is missing %s", key)
				require.Equal(t, want, string(got), "durable board value for %s", key)
			}
		}
		for i := 0; i < setsPerWriter; i++ {
			key := fmt.Sprintf("fuzz.direct.k%d", i)
			got, ok := durable.Get(key)
			require.True(t, ok, "durable board is missing the mid-turn direct write %s", key)
			require.Equal(t, fmt.Sprintf("%q", fmt.Sprintf("d%d", i)), string(got))
		}
		assert.EqualValues(t, 2, prov.sends.Load(), "one gated turn plus the barrier turn")
	})

	t.Run("ProviderSwitchAcrossAndMidTurn", func(t *testing.T) {
		// system.provider is form state like any other key, so a switch
		// is just a patch -- including one written while a round is parked.
		// The contract under test is narrow and structural: no panic, no
		// deadlock, every turn reaches turn.done, and the board's provider is
		// the one serving once the round boundary has passed.
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
			// MID-TURN switch: park the round, repatch system.provider, release.
			submitPrompt(a, fmt.Sprintf("round %d", i))
			awaitEntered(t, entered)
			require.NoError(t, setKV(a, "system.provider", next))
			require.NoError(t, setKV(a, "system.model", fmt.Sprintf("m-%d", i)))
			openGate(gate, 1)
			awaitTurnDone(t, ch)

			// BETWEEN-TURN switch: the next round must bind the new name.
			barrier(t, a, ch, gate, fmt.Sprintf("after %d", i))
			require.NotNil(t, inst(next), "provider %q was never built", next)
			assert.Positive(t, inst(next).sends.Load(), "provider %q never served a round", next)
			assert.Equal(t, next, a.Info().Provider, "status must report the live provider")
		}
	})

	t.Run("InteriorForkTakesTheBoardAsOfThatTurn", func(t *testing.T) {
		// Many patches interleaved with many turns, then a fork at an interior
		// turn. The fork must inherit the board AS OF that turn -- the version
		// stamped on its IR record -- not the board as it stands now.
		entered, gate := newGate()
		prov := &gateProvider{name: "gate", entered: entered, gate: gate}
		a, backend, id := fuzzAgent(t, prov, nil)
		ch, unsub := subscribeChan(a)
		defer unsub()

		const turns = 6
		type mark struct {
			lt    uint64
			board map[string]string
		}
		marks := make([]mark, 0, turns)

		log, err := backend.Open(id)
		require.NoError(t, err)

		for k := 0; k < turns; k++ {
			// A few patches before the turn...
			for j := 0; j < 3; j++ {
				require.NoError(t, setKV(a, fmt.Sprintf("turn%d.pre%d", k, j), fmt.Sprintf("v%d", j)))
			}
			submitPrompt(a, fmt.Sprintf("turn %d", k))
			awaitEntered(t, entered)
			// ...and one landing while the round is in flight.
			require.NoError(t, setKV(a, fmt.Sprintf("turn%d.mid", k), "in-flight"))
			openGate(gate, 1)
			awaitTurnDone(t, ch)
			// FIFO barrier so the mid-turn patch is certainly applied before
			// we record what "the board as of turn k" means.
			barrier(t, a, ch, gate, fmt.Sprintf("settle %d", k))

			entries := log.Read()
			require.NotEmpty(t, entries)
			board, err := backend.FormState(id)
			require.NoError(t, err)
			marks = append(marks, mark{lt: entries[len(entries)-1].LT, board: snapMap(board)})
		}

		// Fork at an INTERIOR turn, not the tail.
		at := marks[2]
		cont, alt, err := backend.ForkAt(id, at.lt)
		require.NoError(t, err)
		require.Equal(t, id, cont, "the trunk id is stable across a fork")
		require.NotEmpty(t, alt)
		require.NotEqual(t, id, alt)

		altBoard, err := backend.FormState(alt)
		require.NoError(t, err)
		nowBoard, err := backend.FormState(id)
		require.NoError(t, err)

		assert.Equal(t, at.board, snapMap(altBoard),
			"an interior fork must inherit the board as of turn 2, not the current one")
		assert.NotEqual(t, snapMap(nowBoard), snapMap(altBoard),
			"turns 3..5 wrote keys the fork must not have inherited")
		for _, key := range []string{"turn5.pre0", "turn5.mid", "turn4.mid"} {
			assert.False(t, altBoard.Has(key), "fork inherited %s from after its fork point", key)
		}
		for _, key := range []string{"turn0.pre0", "turn2.pre2", "turn2.mid"} {
			assert.True(t, altBoard.Has(key), "fork is missing %s from before its fork point", key)
		}
	})

	t.Run("ConcurrentPatchVersionsAreUniqueAndMonotonic", func(t *testing.T) {
		// The version a patch comes back with IS its durable index in the
		// form channel. Concurrent writers must each get their own, the
		// numbers must never go backwards for a single writer, and the folded
		// board must contain every key.
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
					v, err := backend.ApplyForm(id, message.Patch{
						Set: map[string]json.RawMessage{fmt.Sprintf("conc.w%d.k%d", w, i): raw},
					})
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

		board, err := backend.FormState(id)
		require.NoError(t, err)
		for w := 0; w < writers; w++ {
			for i := 0; i < patchesPerHand; i++ {
				key := fmt.Sprintf("conc.w%d.k%d", w, i)
				got, ok := board.Get(key)
				require.True(t, ok, "board is missing %s", key)
				require.Equal(t, fmt.Sprintf("%q", fmt.Sprintf("w%d-i%d", w, i)), string(got))
			}
		}
	})
}

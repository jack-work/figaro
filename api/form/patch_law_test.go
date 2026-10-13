package form

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"
)

// The laws, over random values and random edits:
//
//	apply(diff(A,B), A) = B
//	inverse(diff(A,B)) = diff(B,A)
//	diff(A,A)          = Identity
//	apply(merge(P,Q),A) = apply(Q, apply(P,A))

const lawTrials = 4000

func TestLawApplyDiff(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < lawTrials; i++ {
		a := randValue(rng, 3)
		b := mutate(rng, a, 3)
		got, err := Diff(a, b).Apply(a)
		require.NoError(t, err, "trial %d\n a=%s\n b=%s", i, a.Raw(), b.Raw())
		requireSameJSON(t, b, got, "trial %d: apply(diff(A,B),A) != B\n a=%s\n b=%s", i, a.Raw(), b.Raw())
	}
}

func TestLawIdentity(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	for i := 0; i < lawTrials; i++ {
		a := randValue(rng, 3)
		require.True(t, Diff(a, a).IsIdentity(), "trial %d: diff(A,A) is not Identity: %s", i, a.Raw())
	}
}

func TestLawInverse(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for i := 0; i < lawTrials; i++ {
		a := randValue(rng, 3)
		b := mutate(rng, a, 3)

		back, err := Diff(a, b).Inverse().Apply(b)
		require.NoError(t, err, "trial %d\n a=%s\n b=%s", i, a.Raw(), b.Raw())
		requireSameJSON(t, a, back, "trial %d: inverse(diff(A,B)) applied to B != A\n a=%s\n b=%s", i, a.Raw(), b.Raw())
	}
}

func TestPatchRoundTripsThroughJSON(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	for i := 0; i < lawTrials; i++ {
		a := randValue(rng, 3)
		b := mutate(rng, a, 3)
		p := Diff(a, b)

		raw, err := json.Marshal(p)
		require.NoError(t, err)
		var back Patch
		require.NoError(t, json.Unmarshal(raw, &back))

		got, err := back.Apply(a)
		require.NoError(t, err, "trial %d: %s", i, raw)
		requireSameJSON(t, b, got, "trial %d: a patch changed meaning across the wire\n %s", i, raw)
	}
}

func requireSameJSON(t *testing.T, want, got Value, msg string, args ...any) {
	t.Helper()
	if want.Equal(got) {
		return
	}
	require.JSONEq(t, string(want.Raw()), string(got.Raw()), fmt.Sprintf(msg, args...))
}

func randValue(rng *rand.Rand, depth int) Value {
	if depth <= 0 {
		return randScalar(rng)
	}
	switch rng.Intn(6) {
	case 0, 1, 2:
		return randScalar(rng)
	case 3, 4:
		n := rng.Intn(4)
		m := map[string]json.RawMessage{}
		for i := 0; i < n; i++ {
			m[randKey(rng)] = randValue(rng, depth-1).Raw()
		}
		b, _ := json.Marshal(m)
		return NewValue(b)
	default:
		n := rng.Intn(4)
		items := make([]KeyedValue, 0, n)
		used := map[string]bool{}
		for i := 0; i < n; i++ {
			k := randKey(rng)
			if used[k] {
				continue
			}
			used[k] = true
			items = append(items, KeyedValue{Key: k, Value: randValue(rng, depth-1)})
		}
		b, _ := json.Marshal(items)
		return NewValue(b)
	}
}

func randScalar(rng *rand.Rand) Value {
	switch rng.Intn(5) {
	case 0:
		return NewValue(json.RawMessage(`null`))
	case 1:
		return NewValue(json.RawMessage(fmt.Sprintf("%d", rng.Intn(100))))
	case 2:
		if rng.Intn(2) == 0 {
			return NewValue(json.RawMessage(`true`))
		}
		return NewValue(json.RawMessage(`false`))
	default:
		b, _ := json.Marshal(fmt.Sprintf("s%d", rng.Intn(50)))
		return NewValue(b)
	}
}

func randKey(rng *rand.Rand) string { return fmt.Sprintf("k%d", rng.Intn(6)) }

func mutate(rng *rand.Rand, v Value, depth int) Value {
	if depth <= 0 || rng.Intn(4) == 0 {
		return randValue(rng, depth)
	}
	if obj, ok := asObject(v); ok {
		out := map[string]json.RawMessage{}
		for k, val := range obj {
			switch rng.Intn(5) {
			case 0: // drop
			case 1:
				out[k] = mutate(rng, val, depth-1).Raw()
			default:
				out[k] = val.Raw()
			}
		}
		if rng.Intn(2) == 0 {
			out[randKey(rng)] = randValue(rng, depth-1).Raw()
		}
		b, _ := json.Marshal(out)
		return NewValue(b)
	}
	if items, ok := asList(v); ok {
		var out []KeyedValue
		used := map[string]bool{}
		for _, it := range items {
			switch rng.Intn(5) {
			case 0: // drop
			case 1:
				out = append(out, KeyedValue{Key: it.Key, Value: mutate(rng, it.Value, depth-1)})
				used[it.Key] = true
			default:
				out = append(out, it)
				used[it.Key] = true
			}
		}
		if rng.Intn(2) == 0 {
			if k := randKey(rng); !used[k] {
				out = append(out, KeyedValue{Key: k, Value: randValue(rng, depth-1)})
			}
		}
		if len(out) > 1 && rng.Intn(2) == 0 {
			i, j := rng.Intn(len(out)), rng.Intn(len(out))
			out[i], out[j] = out[j], out[i]
		}
		b, _ := json.Marshal(out)
		return NewValue(b)
	}
	return randValue(rng, depth)
}

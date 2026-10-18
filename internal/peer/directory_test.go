package peer_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/peer"
)

type askLog struct {
	mu    sync.Mutex
	asked []string
}

func (l *askLog) ask(owners map[string][]string, down ...string) peer.Ask {
	return func(_ context.Context, node string, ids []string) ([]string, error) {
		l.mu.Lock()
		l.asked = append(l.asked, node)
		l.mu.Unlock()
		for _, d := range down {
			if d == node {
				return nil, &peer.Unreachable{Node: node, Err: errors.New("connection refused")}
			}
		}
		var out []string
		for _, id := range ids {
			for _, o := range owners[node] {
				if o == id {
					out = append(out, id)
				}
			}
		}
		return out, nil
	}
}

func (l *askLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.asked)
}

// A miss asks every peer once; the answer is cached, so the next lookup
// asks nobody.
func TestDirectoryLearnsFromAMiss(t *testing.T) {
	var log askLog
	d := peer.NewCache([]string{"b", "c"}, log.ask(map[string][]string{"c": {"x"}}))
	node, err := d.Locate(context.Background(), "x")
	require.NoError(t, err)
	require.Equal(t, "c", node)
	require.Equal(t, 2, log.count())

	node, err = d.Locate(context.Background(), "x")
	require.NoError(t, err)
	require.Equal(t, "c", node)
	require.Equal(t, 2, log.count())

	d.Forget("x")
	_, ok := d.Owner("x")
	require.False(t, ok)
}

// Births and listings fill the cache without asking.
func TestDirectoryLearnsWithoutAsking(t *testing.T) {
	var log askLog
	d := peer.NewCache([]string{"b"}, log.ask(nil))
	d.Learn("born-on-b", "b")
	node, err := d.Locate(context.Background(), "born-on-b")
	require.NoError(t, err)
	require.Equal(t, "b", node)
	require.Zero(t, log.count())
}

// Nobody owns it: say so. Somebody might, but is down: say who.
func TestDirectoryNamesWhatItCouldNotAsk(t *testing.T) {
	var log askLog
	d := peer.NewCache([]string{"b", "c"}, log.ask(nil))
	_, err := d.Locate(context.Background(), "x")
	require.ErrorIs(t, err, peer.ErrNoOwner)

	d = peer.NewCache([]string{"b", "c"}, log.ask(nil, "c"))
	_, err = d.Locate(context.Background(), "x")
	require.ErrorIs(t, err, peer.ErrNoOwner)
	var unreachable *peer.Unreachable
	require.ErrorAs(t, err, &unreachable)
	require.Equal(t, "c", unreachable.Node)
}

// An old 8-hex id minted on two nodes is never guessed: the caller is told
// to qualify it, and nothing is cached.
func TestDirectoryRefusesToGuessACollision(t *testing.T) {
	var log askLog
	d := peer.NewCache([]string{"b", "c"}, log.ask(map[string][]string{"b": {"abcd1234"}, "c": {"abcd1234"}}))
	_, err := d.Locate(context.Background(), "abcd1234")
	require.ErrorContains(t, err, "abcd1234 is on nodes b and c: name it as <node>/abcd1234")
	_, ok := d.Owner("abcd1234")
	require.False(t, ok)
}

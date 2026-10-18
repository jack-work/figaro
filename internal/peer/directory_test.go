package peer_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/peer"
)

type askLog struct {
	mu    sync.Mutex
	asked []string
}

func (l *askLog) ask(owners map[string][]string, down ...string) peer.Ask {
	return func(_ context.Context, node string, refs []string) (rpc.NodeResolveResponse, error) {
		l.mu.Lock()
		l.asked = append(l.asked, node)
		l.mu.Unlock()
		for _, d := range down {
			if d == node {
				return rpc.NodeResolveResponse{}, &peer.Unreachable{Node: node, Err: errors.New("connection refused")}
			}
		}
		out := map[string]string{}
		for _, ref := range refs {
			for _, o := range owners[node] {
				if strings.HasPrefix(o, ref) {
					out[ref] = o
				}
			}
		}
		return rpc.NodeResolveResponse{IDs: out}, nil
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
	d := peer.NewCache([]string{"b", "c"}, log.ask(map[string][]string{"c": {"x"}}), 64)
	node, id, err := d.Locate(context.Background(), "x")
	require.NoError(t, err)
	require.Equal(t, "c", node)
	require.Equal(t, "x", id)
	require.Equal(t, 2, log.count())

	node, _, err = d.Locate(context.Background(), "x")
	require.NoError(t, err)
	require.Equal(t, "c", node)
	require.Equal(t, 2, log.count())

	d.Forget("x")
	_, ok := d.Owner("x")
	require.False(t, ok)
}

// A prefix is resolved by its owner, and the full id is what is cached.
func TestDirectoryResolvesAPrefixAtTheOwner(t *testing.T) {
	var log askLog
	d := peer.NewCache([]string{"b", "c"}, log.ask(map[string][]string{"b": {"5f3e9a01c2d4"}}), 64)
	node, id, err := d.Locate(context.Background(), "5f3e9a")
	require.NoError(t, err)
	require.Equal(t, "b", node)
	require.Equal(t, "5f3e9a01c2d4", id)
	owner, ok := d.Owner("5f3e9a01c2d4")
	require.True(t, ok)
	require.Equal(t, "b", owner)
}

// Births and listings fill the cache without asking.
func TestDirectoryLearnsWithoutAsking(t *testing.T) {
	var log askLog
	d := peer.NewCache([]string{"b"}, log.ask(nil), 64)
	d.Learn("born-on-b", "b")
	node, _, err := d.Locate(context.Background(), "born-on-b")
	require.NoError(t, err)
	require.Equal(t, "b", node)
	require.Zero(t, log.count())
}

// Nobody owns it: say so. Somebody might, but is down: say who, and do not
// call it a miss.
func TestDirectoryNamesWhatItCouldNotAsk(t *testing.T) {
	var log askLog
	d := peer.NewCache([]string{"b", "c"}, log.ask(nil), 64)
	_, _, err := d.Locate(context.Background(), "x")
	require.ErrorIs(t, err, peer.ErrNoOwner)

	d = peer.NewCache([]string{"b", "c"}, log.ask(nil, "c"), 64)
	_, _, err = d.Locate(context.Background(), "x")
	require.NotErrorIs(t, err, peer.ErrNoOwner, "c may be the owner; that is not the same as no owner")
	var unreachable *peer.Unreachable
	require.ErrorAs(t, err, &unreachable)
	require.Equal(t, "c", unreachable.Node)
}

// An old 8-hex id minted on two nodes is never guessed: the caller is told
// to qualify it, and nothing is cached.
func TestDirectoryRefusesToGuessACollision(t *testing.T) {
	var log askLog
	d := peer.NewCache([]string{"b", "c"}, log.ask(map[string][]string{"b": {"abcd1234"}, "c": {"abcd1234"}}), 64)
	_, _, err := d.Locate(context.Background(), "abcd1234")
	require.ErrorContains(t, err, "abcd1234 is on nodes b and c: name it as <node>/abcd1234")
	_, ok := d.Owner("abcd1234")
	require.False(t, ok)
}

// The directory holds at most its size, dropping the owner looked up least
// recently; a dropped owner is asked for again, not lost.
func TestDirectoryIsBounded(t *testing.T) {
	var log askLog
	d := peer.NewCache([]string{"b"}, log.ask(map[string][]string{"b": {"x1", "x2", "x3"}}), 2)
	d.Learn("x1", "b")
	d.Learn("x2", "b")
	_, ok := d.Owner("x1")
	require.True(t, ok)
	d.Learn("x3", "b")
	require.Equal(t, 2, d.Len())
	_, ok = d.Owner("x2")
	require.False(t, ok, "the least recently used owner survived")
	_, ok = d.Owner("x1")
	require.True(t, ok, "a recent lookup did not keep its owner")

	node, _, err := d.Locate(context.Background(), "x2")
	require.NoError(t, err)
	require.Equal(t, "b", node)
	require.Equal(t, 1, log.count())
	require.Equal(t, 2, d.Len())
}

// An old home answers with where the aria went; with no owner answering, the
// directory takes the forwarding address and remembers it.
func TestAMovedAnswerLocatesTheNewHome(t *testing.T) {
	ask := func(_ context.Context, node string, refs []string) (rpc.NodeResolveResponse, error) {
		if node == "b" {
			return rpc.NodeResolveResponse{Moved: map[string]rpc.Moved{refs[0]: {ID: "x1aaaa", Node: "c"}}}, nil
		}
		return rpc.NodeResolveResponse{}, &peer.Unreachable{Node: node, Err: errors.New("connection refused")}
	}
	d := peer.NewCache([]string{"b", "c"}, ask, 64)
	node, id, err := d.Locate(context.Background(), "x1")
	require.NoError(t, err)
	require.Equal(t, "c", node)
	require.Equal(t, "x1aaaa", id)
	owner, ok := d.Owner("x1aaaa")
	require.True(t, ok)
	require.Equal(t, "c", owner)
}

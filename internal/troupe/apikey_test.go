package troupe

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/apikey"
	"github.com/stretchr/testify/require"
)

func (s *estate) mint(by, principal string, at, ttl time.Duration) (Entry, apikey.Key) {
	s.t.Helper()
	_, k, err := apikey.New(rand.Reader)
	require.NoError(s.t, err)
	e, err := SignAPIKey(s.ikey[by], s.inter[by], "estate", k.ID, principal, k.Hash[:], s.now.Add(at+ttl), []string{"a", "b"}, "user:owner", s.now.Add(at))
	require.NoError(s.t, err)
	return e, k
}

func (s *estate) setters(version uint64, nodes ...string) Entry {
	s.t.Helper()
	quoted := make([]string, len(nodes))
	for i, n := range nodes {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	e, err := SignRootPolicy(s.rk, "estate", version, "root:estate", "setters = ["+strings.Join(quoted, ", ")+"]\n", s.now.Add(time.Duration(version)*time.Millisecond))
	require.NoError(s.t, err)
	return e
}

func (s *estate) revokeKey(node, issuer, id string, at time.Duration) Entry {
	s.t.Helper()
	k := newKey(s.t)
	leaf, err := IssueNode(s.ikey[issuer], s.inter[issuer], "estate", node, k.Public(), s.now, NodeTTL)
	require.NoError(s.t, err)
	e, err := SignAPIKeyRevoke(k, leaf, s.inter[issuer], "estate", node, id, "service:ci@troupe:estate", s.now.Add(at))
	require.NoError(s.t, err)
	return e
}

func TestAMintedKeyIsLiveUntilItExpires(t *testing.T) {
	s := newEstate(t)
	s.intermediate("a")
	e, k := s.mint("a", "service:ci@troupe:estate", 0, time.Hour)
	v := s.ledger(s.setters(1, "a"), e).View()
	r, ok := v.APIKeys[k.ID]
	require.True(t, ok)
	require.Equal(t, "service:ci@troupe:estate", r.Principal)
	require.Equal(t, "a", r.Signer)
	require.Equal(t, "user:owner", r.By)
	require.True(t, k.Matches(r.Hash))
	require.Equal(t, []string{"a", "b"}, r.Nodes)
	require.True(t, r.Live(s.now))
	require.False(t, r.Live(s.now.Add(time.Hour)))
	require.NotZero(t, v.KeysDigest)
}

// Every field of a key entry is under the signature, and only a node
// intermediate of this troupe signs one, for a principal of this troupe.
func TestForgedAndTamperedKeysAreRefused(t *testing.T) {
	s := newEstate(t)
	s.intermediate("a")
	good, _ := s.mint("a", "service:ci@troupe:estate", 0, time.Hour)
	tamper := map[string]func(*Entry){
		"principal": func(e *Entry) { e.Principal = "service:root@troupe:estate" },
		"hash":      func(e *Entry) { e.Hash = append([]byte{e.Hash[0] ^ 1}, e.Hash[1:]...) },
		"not after": func(e *Entry) { e.NotAfter += 1 },
		"by":        func(e *Entry) { e.By = "user:mallory" },
		"key":       func(e *Entry) { e.Key = "aaaaaaaaaa" },
		"node":      func(e *Entry) { e.Node = "b" },
		"door":      func(e *Entry) { e.Door = "x.lan:1" },
		"nodes":     func(e *Entry) { e.Nodes = []string{"a", "b", "c"} },
		"all nodes": func(e *Entry) { e.Nodes = []string{apikey.AllNodes} },
	}
	for name, f := range tamper {
		e := good
		e.Hash = append([]byte(nil), good.Hash...)
		f(&e)
		_, err := NewLedger("estate", s.root).Merge(e)
		require.Error(t, err, name)
	}

	other := newEstate(t)
	other.intermediate("a")
	foreign, _ := other.mint("a", "service:ci@troupe:estate", 0, time.Hour)
	_, err := NewLedger("estate", s.root).Merge(foreign)
	require.Error(t, err, "signed under another root")

	for name, p := range map[string]string{
		"another troupe": "service:ci@troupe:other",
		"an issuer":      "user:jack@auth.kelliher.info",
		"a node":         "node:ci@troupe:estate",
	} {
		e, _ := s.mint("a", p, 0, time.Hour)
		_, err := NewLedger("estate", s.root).Merge(e)
		require.Error(t, err, name)
	}

	long, _ := s.mint("a", "service:ci@troupe:estate", 0, apikey.MaxTTL+time.Hour)
	_, err = NewLedger("estate", s.root).Merge(long)
	require.Error(t, err, "past the ttl ceiling")

	join := s.join("a", "b", 0)
	join.Key = "aaaaaaaaaa"
	_, err = NewLedger("estate", s.root).Merge(join)
	require.Error(t, err, "a join carrying a key")
}

func TestKeyEntriesSurviveJSON(t *testing.T) {
	s := newEstate(t)
	s.intermediate("a")
	e, k := s.mint("a", "user:alice@troupe:estate", 0, time.Hour)
	r := s.revokeKey("b", "a", k.ID, time.Minute)
	raw, err := json.Marshal([]Entry{e, r})
	require.NoError(t, err)
	var out []Entry
	require.NoError(t, json.Unmarshal(raw, &out))
	added, err := NewLedger("estate", s.root).Merge(out...)
	require.NoError(t, err)
	require.Len(t, added, 2)
}

// A revoke wins whatever order the two arrive in, and the earliest revoke
// is the one recorded.
func TestARevokeWinsOverItsKeyInEitherOrder(t *testing.T) {
	s := newEstate(t)
	s.intermediate("a")
	e, k := s.mint("a", "service:ci@troupe:estate", 0, time.Hour)
	late, early := s.revokeKey("b", "a", k.ID, 2*time.Minute), s.revokeKey("c", "a", k.ID, time.Minute)
	for _, order := range [][]Entry{{e, late, early}, {early, late, e}, {late, e, early}} {
		r := s.ledger(order...).View().APIKeys[k.ID]
		require.NotNil(t, r.Revoked)
		require.Equal(t, "c", r.Revoked.Node)
		require.False(t, r.Live(s.now))
	}
}

// A revoke must be signed by the node it names, and that node's certificate
// must chain to the troupe root.
func TestARevokeIsSignedByTheNodeThatServedIt(t *testing.T) {
	s := newEstate(t)
	s.intermediate("a")
	_, k := s.mint("a", "service:ci@troupe:estate", 0, time.Hour)
	r := s.revokeKey("b", "a", k.ID, 0)
	r.Node = "c"
	_, err := NewLedger("estate", s.root).Merge(r)
	require.Error(t, err)
	r = s.revokeKey("b", "a", k.ID, 0)
	r.Key = "bbbbbbbbbb"
	_, err = NewLedger("estate", s.root).Merge(r)
	require.Error(t, err)
}

// Revoking a node at the root voids the keys its intermediate signed.
func TestARootRevokeVoidsTheKeysItsNodeSigned(t *testing.T) {
	s := newEstate(t)
	s.intermediate("a")
	s.intermediate("b")
	ea, ka := s.mint("a", "service:ci@troupe:estate", 0, time.Hour)
	eb, kb := s.mint("b", "service:cd@troupe:estate", 0, time.Hour)
	v := s.ledger(s.setters(1, "a", "b"), ea, eb, s.revoke("b", time.Minute)).View()
	require.True(t, v.APIKeys[ka.ID].Live(s.now))
	require.False(t, v.APIKeys[kb.ID].Live(s.now))
	require.NotEmpty(t, v.APIKeys[kb.ID].Void)
}

// Two entries under one id leave neither usable: a node that clashes an id
// can deny that key, which it could already do by revoking it.
func TestTwoKeysUnderOneIDAreBothVoid(t *testing.T) {
	s := newEstate(t)
	s.intermediate("a")
	e, k := s.mint("a", "service:ci@troupe:estate", 0, time.Hour)
	_, other := s.mint("a", "service:ci@troupe:estate", 0, time.Hour)
	clash, err := SignAPIKey(s.ikey["a"], s.inter["a"], "estate", k.ID, "service:cd@troupe:estate", other.Hash[:], s.now.Add(time.Hour), []string{"a"}, "user:owner", s.now)
	require.NoError(t, err)
	v := s.ledger(e, clash).View()
	require.False(t, v.APIKeys[k.ID].Live(s.now))
}

// Expired keys and old revokes leave the ledger, and gossip cannot bring
// them back.
func TestExpiredKeysArePrunedAndNotTakenBack(t *testing.T) {
	s := newEstate(t)
	s.intermediate("a")
	e, k := s.mint("a", "service:ci@troupe:estate", 0, time.Hour)
	r := s.revokeKey("b", "a", k.ID, time.Minute)
	l := s.ledger(e, r)
	require.Empty(t, l.Prune(s.now.Add(2*time.Hour)))
	gone := l.Prune(s.now.Add(time.Hour + PruneGrace + time.Second))
	require.Equal(t, []string{e.ID()}, gone)
	require.NotContains(t, l.View().APIKeys, k.ID)
	l.now = func() time.Time { return s.now.Add(time.Hour + PruneGrace + time.Second) }
	added, err := l.Merge(e)
	require.NoError(t, err)
	require.Empty(t, added)
	require.Equal(t, []string{r.ID()}, l.Prune(s.now.Add(time.Minute+apikey.MaxTTL+PruneGrace+time.Second)))
	require.Empty(t, l.View().APIKeys)
	require.Zero(t, l.counts[APIKey]+l.counts[APIKeyRevoke])
}

// Keys have their own cap, so minting cannot crowd out joins, and a full
// key ledger still takes joins.
func TestKeysAreCappedApartFromJoins(t *testing.T) {
	s := newEstate(t)
	s.intermediate("a")
	l := NewLedger("estate", s.root)
	for i := range MaxAPIKeys {
		l.entries[fmt.Sprint("k", i)] = verified{Entry: Entry{Kind: APIKey}}
	}
	l.counts[APIKey] = MaxAPIKeys
	e, _ := s.mint("a", "service:ci@troupe:estate", 0, time.Hour)
	added, err := l.Merge(e)
	require.Error(t, err)
	require.Empty(t, added)
	added, err = l.Merge(s.join("a", "a", 0))
	require.NoError(t, err)
	require.Len(t, added, 1)
}

func TestTheKeysDigestMovesWithKeysAndRootRevokesOnly(t *testing.T) {
	s := newEstate(t)
	s.intermediate("a")
	l := s.ledger(s.join("a", "a", 0))
	before := l.View().KeysDigest
	_, err := l.Merge(s.join("a", "b", time.Minute))
	require.NoError(t, err)
	require.Equal(t, before, l.View().KeysDigest)
	e, _ := s.mint("a", "service:ci@troupe:estate", 0, time.Hour)
	_, err = l.Merge(e)
	require.NoError(t, err)
	withKey := l.View().KeysDigest
	require.NotEqual(t, before, withKey)
	_, err = l.Merge(s.revoke("c", time.Minute))
	require.NoError(t, err)
	require.NotEqual(t, withKey, l.View().KeysDigest)
}

// A key names the nodes it is good on, signed with it: sorted, each once,
// or all of them, and never none.
func TestAKeysAudienceIsCheckedAndSigned(t *testing.T) {
	s := newEstate(t)
	s.intermediate("a")
	_, k, err := apikey.New(rand.Reader)
	require.NoError(t, err)
	sign := func(nodes []string) error {
		e, err := SignAPIKey(s.ikey["a"], s.inter["a"], "estate", k.ID, "service:ci@troupe:estate", k.Hash[:], s.now.Add(time.Hour), nodes, "user:owner", s.now)
		require.NoError(t, err)
		_, err = NewLedger("estate", s.root).Merge(e)
		return err
	}
	require.NoError(t, sign([]string{apikey.AllNodes}))
	require.NoError(t, sign([]string{"a"}))
	for name, nodes := range map[string][]string{
		"none":        nil,
		"empty":       {},
		"unsorted":    {"b", "a"},
		"twice":       {"a", "a"},
		"all and one": {apikey.AllNodes, "a"},
		"not an id":   {"a/b"},
	} {
		require.Error(t, sign(nodes), name)
	}
	r := s.revokeKey("b", "a", k.ID, 0)
	r.Nodes = []string{"a"}
	_, err = NewLedger("estate", s.root).Merge(r)
	require.Error(t, err, "a revoke carries no audience")
}

// A key counts only when the root signs it or a setter of the policy in
// force does: an intermediate the policy does not name cannot mint for a
// principal the policy grants, and a node that stops being a setter takes
// its keys with it.
func TestOnlyTheRootOrASetterMintsAKeyThatCounts(t *testing.T) {
	s := newEstate(t)
	s.intermediate("a")
	s.intermediate("b")
	byA, ka := s.mint("a", "service:ci@troupe:estate", 0, time.Hour)
	byB, kb := s.mint("b", "service:ci@troupe:estate", 0, time.Hour)
	_, kr, err := apikey.New(rand.Reader)
	require.NoError(t, err)
	byRoot, err := SignRootAPIKey(s.rk, "estate", kr.ID, "service:ci@troupe:estate", kr.Hash[:], s.now.Add(time.Hour), []string{apikey.AllNodes}, "root:estate", s.now)
	require.NoError(t, err)

	v := s.ledger(byA, byB, byRoot).View()
	require.Contains(t, v.APIKeys[ka.ID].Void, "no troupe policy")
	require.False(t, v.APIKeys[kb.ID].Live(s.now))
	require.True(t, v.APIKeys[kr.ID].Live(s.now), "the root needs no setter")
	require.Equal(t, RootSigner, v.APIKeys[kr.ID].Signer)

	l := s.ledger(s.setters(1, "a"), byA, byB, byRoot)
	v = l.View()
	require.True(t, v.APIKeys[ka.ID].Live(s.now))
	require.Contains(t, v.APIKeys[kb.ID].Void, "not a setter in policy version 1")
	require.True(t, v.APIKeys[kr.ID].Live(s.now))

	before := v.KeysDigest
	_, err = l.Merge(s.setters(2, "b"))
	require.NoError(t, err)
	v = l.View()
	require.False(t, v.APIKeys[ka.ID].Live(s.now), "a is no longer a setter")
	require.True(t, v.APIKeys[kb.ID].Live(s.now))
	require.NotEqual(t, before, v.KeysDigest, "key watchers hear of a change of setters")

	forged := byRoot
	forged.Chain = byA.Chain
	_, err = NewLedger("estate", s.root).Merge(forged)
	require.Error(t, err)
	named, err := SignRootAPIKey(s.rk, "estate", kr.ID, "service:ci@troupe:estate", kr.Hash[:], s.now.Add(time.Hour), []string{apikey.AllNodes}, "root:estate", s.now)
	require.NoError(t, err)
	named.Node = "a"
	_, err = NewLedger("estate", s.root).Merge(named)
	require.Error(t, err, "a root-signed key names no node")
}

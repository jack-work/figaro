package troupe

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type estate struct {
	t     testing.TB
	rk    ed25519.PrivateKey
	root  *x509.Certificate
	now   time.Time
	inter map[string]*x509.Certificate
	ikey  map[string]ed25519.PrivateKey
}

func newEstate(t testing.TB) *estate {
	t.Helper()
	rk := newKey(t)
	root, err := RootCert("estate", rk)
	require.NoError(t, err)
	return &estate{t: t, rk: rk, root: root, now: time.Now(), inter: map[string]*x509.Certificate{}, ikey: map[string]ed25519.PrivateKey{}}
}

func (s *estate) intermediate(node string) {
	s.t.Helper()
	k := newKey(s.t)
	c, err := IssueIntermediate(s.rk, s.root, "estate", node, k.Public(), s.now, IntermediateTTL)
	require.NoError(s.t, err)
	s.inter[node], s.ikey[node] = c, k
}

func (s *estate) join(by, node string, at time.Duration) Entry {
	s.t.Helper()
	e, err := SignJoin(s.ikey[by], s.inter[by], "estate", node, node+".lan:7444", "user:owner", s.now.Add(at))
	require.NoError(s.t, err)
	return e
}

func (s *estate) leave(node, issuer string, at time.Duration) Entry {
	s.t.Helper()
	return s.leaveAs(node, node, issuer, at)
}

func (s *estate) leaveAs(holder, node, issuer string, at time.Duration) Entry {
	s.t.Helper()
	k := newKey(s.t)
	leaf, err := IssueNode(s.ikey[issuer], s.inter[issuer], "estate", holder, k.Public(), s.now, NodeTTL)
	require.NoError(s.t, err)
	e, err := SignLeave(k, leaf, s.inter[issuer], "estate", node, s.now.Add(at))
	require.NoError(s.t, err)
	return e
}

func (s *estate) renewer(node, by string) Entry {
	s.t.Helper()
	e, err := SignRootRenewer(s.rk, "estate", node, by, s.now)
	require.NoError(s.t, err)
	return e
}

func (s *estate) revoke(node string, at time.Duration) Entry {
	s.t.Helper()
	e, err := SignRevoke(s.rk, "estate", node, s.now.Add(at))
	require.NoError(s.t, err)
	return e
}

func (s *estate) ledger(es ...Entry) *Ledger {
	s.t.Helper()
	l := NewLedger("estate", s.root)
	_, err := l.Merge(es...)
	require.NoError(s.t, err)
	return l
}

func members(v View) []string {
	return slices.Sorted(maps.Keys(v.Members))
}

// The founder announces itself by signing its own join with its
// intermediate; that marks it as a node that can admit others.
func TestAFounderSigningItsOwnJoinIsAMemberThatCanSign(t *testing.T) {
	s := newEstate(t)
	s.intermediate("a")
	l := s.ledger(s.join("a", "a", 0))
	v := l.View()
	require.Equal(t, []string{"a"}, members(v))
	require.Equal(t, "a", v.Members["a"].AdmittedBy)
	require.Equal(t, "a.lan:7444", v.Members["a"].Door)
	require.Equal(t, []string{"a"}, v.Signers(s.now))
	require.NotZero(t, v.Digest)
}

// A node admitted by another holds a leaf only: it is a member, and it is
// not offered as a signer.
func TestAJoinedNodeIsAMemberButNotASigner(t *testing.T) {
	s := newEstate(t)
	s.intermediate("a")
	v := s.ledger(s.join("a", "a", 0), s.join("a", "b", time.Minute)).View()
	require.Equal(t, []string{"a", "b"}, members(v))
	require.Equal(t, "a", v.Members["b"].AdmittedBy)
	require.Equal(t, []string{"a"}, v.Signers(s.now))
}

// Entries travel as JSON between nodes and through the form that stores
// them; a round trip must keep them verifiable.
func TestEntriesSurviveJSON(t *testing.T) {
	s := newEstate(t)
	s.intermediate("a")
	in := []Entry{s.join("a", "a", 0), s.leave("a", "a", time.Hour), s.revoke("b", time.Hour)}
	raw, err := json.Marshal(in)
	require.NoError(t, err)
	var out []Entry
	require.NoError(t, json.Unmarshal(raw, &out))
	l := NewLedger("estate", s.root)
	added, err := l.Merge(out...)
	require.NoError(t, err)
	require.Len(t, added, 3)
	require.Equal(t, in[0].ID(), out[0].ID())
}

// Every field a peer could alter is under the signature, and only the
// troupe's own keys sign.
func TestForgedAndTamperedEntriesAreRefused(t *testing.T) {
	s := newEstate(t)
	s.intermediate("a")
	good := s.join("a", "b", 0)

	other := newEstate(t)
	other.intermediate("a")
	k := newKey(t)
	leaf, err := IssueNode(s.ikey["a"], s.inter["a"], "estate", "b", k.Public(), s.now, NodeTTL)
	require.NoError(t, err)
	byLeaf, err := SignJoin(k, leaf, "estate", "c", "c:1", "user:owner", s.now)
	require.NoError(t, err)
	byInterRevoke := s.revoke("b", 0)
	byInterRevoke.Sig = ed25519.Sign(s.ikey["a"], byInterRevoke.message())

	cases := map[string]Entry{
		"door changed":           func() Entry { e := good; e.Door = "evil:1"; return e }(),
		"node changed":           func() Entry { e := good; e.Node = "c"; return e }(),
		"time changed":           func() Entry { e := good; e.At++; return e }(),
		"another troupe's root":  other.join("a", "b", 0),
		"signed by a leaf":       byLeaf,
		"revoke not by the root": byInterRevoke,
		"leave for another node": s.leaveAs("b", "c", "a", 0),
		"join without a door": func() Entry {
			e, _ := SignJoin(s.ikey["a"], s.inter["a"], "estate", "c", "", "user:owner", s.now)
			return e
		}(),
		"wrong troupe name": func() Entry {
			e, _ := SignJoin(s.ikey["a"], s.inter["a"], "other", "c", "c:1", "user:owner", s.now)
			return e
		}(),
		"node id with a slash": func() Entry {
			e, _ := SignJoin(s.ikey["a"], s.inter["a"], "estate", "c/d", "c:1", "user:owner", s.now)
			return e
		}(),
		"leave carrying a door": func() Entry { e := s.leave("b", "a", 0); e.Door = "x:1"; return e }(),
		"unknown kind":          func() Entry { e := good; e.Kind = "promote"; return e }(),
		"owner changed":         func() Entry { e := good; e.Owner = "user:mallory"; return e }(),
		"join without an owner": func() Entry { e, _ := SignJoin(s.ikey["a"], s.inter["a"], "estate", "c", "c:1", "", s.now); return e }(),
		"owner that is no principal": func() Entry {
			e, _ := SignJoin(s.ikey["a"], s.inter["a"], "estate", "c", "c:1", "jack", s.now)
			return e
		}(),
		"leave carrying an owner": func() Entry { e := s.leave("b", "a", 0); e.Owner = "user:x"; return e }(),
		"revoke with a chain":     func() Entry { e := s.revoke("b", 0); e.Chain = good.Chain; return e }(),
		"join with an extra cert": func() Entry { e := good; e.Chain = append(e.Chain, leaf.Raw); return e }(),
	}
	for name, e := range cases {
		l := NewLedger("estate", s.root)
		added, err := l.Merge(e)
		require.Error(t, err, name)
		require.Empty(t, added, name)
		require.Empty(t, l.Entries(), name)
	}
}

// An entry is checked at the time it was signed: a join stays valid after
// the intermediate that signed it has expired.
func TestAnEntryIsVerifiedAtItsOwnTime(t *testing.T) {
	s := newEstate(t)
	s.now = time.Now().Add(-2 * time.Hour)
	k := newKey(t)
	inter, err := IssueIntermediate(s.rk, s.root, "estate", "a", k.Public(), s.now, time.Hour)
	require.NoError(t, err)
	s.inter["a"], s.ikey["a"] = inter, k
	old := s.join("a", "b", 0)
	require.True(t, time.Now().After(s.inter["a"].NotAfter))
	l := s.ledger(s.join("a", "a", 0), old)
	require.Contains(t, l.View().Members, "b")
	require.Empty(t, l.View().Signers(time.Now()), "an expired intermediate signs nothing new")

	late, err := SignJoin(s.ikey["a"], s.inter["a"], "estate", "c", "c:1", "user:owner", s.inter["a"].NotAfter.Add(time.Hour))
	require.NoError(t, err)
	_, err = l.Merge(late)
	require.Error(t, err, "a join dated after its intermediate expired")
}

// Leaving is final for that node id: a join of the same node, signed
// before or after, does not bring it back.
func TestALeaveBeatsAJoinOfTheSameNodeWhateverTheOrder(t *testing.T) {
	s := newEstate(t)
	s.intermediate("a")
	v := s.ledger(s.join("a", "a", 0), s.join("a", "b", 0), s.renewer("b", "a"), s.leave("b", "a", time.Minute), s.join("a", "b", time.Hour)).View()
	require.Equal(t, []string{"a"}, members(v))
	require.Equal(t, Leave, v.Gone["b"])
}

// A root revoke is a compromise: the node is out, and so is every node
// whose only admission it signed. A node admitted elsewhere stays, and a
// node it admitted may be admitted again by another.
func TestARootRevokeTakesTheJoinsItsNodeSigned(t *testing.T) {
	s := newEstate(t)
	s.intermediate("a")
	s.intermediate("d")
	es := []Entry{s.join("a", "a", 0), s.join("d", "d", 0), s.join("a", "b", 0), s.join("d", "c", 0)}
	require.Equal(t, []string{"a", "b", "c", "d"}, members(s.ledger(es...).View()))

	l := s.ledger(append(es, s.revoke("a", time.Hour))...)
	v := l.View()
	require.Equal(t, []string{"c", "d"}, members(v))
	require.Equal(t, Revoke, v.Gone["a"])
	require.Equal(t, []string{"d"}, v.Signers(s.now))

	_, err := l.Merge(s.join("d", "b", 2*time.Hour))
	require.NoError(t, err)
	require.Equal(t, []string{"b", "c", "d"}, members(l.View()))
	require.Equal(t, "d", l.View().Members["b"].AdmittedBy)
}

// A root revoke outranks a leave already recorded for the same node, so
// the joins it signed fall too.
func TestARevokeAfterALeaveStillCascades(t *testing.T) {
	s := newEstate(t)
	s.intermediate("a")
	v := s.ledger(s.join("a", "a", 0), s.join("a", "b", 0), s.leave("a", "a", time.Minute), s.revoke("a", time.Hour)).View()
	require.Empty(t, members(v))
	require.Equal(t, Revoke, v.Gone["a"])
}

// The newest join names the door a node answers on.
func TestTheLatestJoinNamesTheDoor(t *testing.T) {
	s := newEstate(t)
	s.intermediate("a")
	moved, err := SignJoin(s.ikey["a"], s.inter["a"], "estate", "a", "a.tail:7444", "user:owner", s.now.Add(time.Hour))
	require.NoError(t, err)
	v := s.ledger(moved, s.join("a", "a", 0)).View()
	require.Equal(t, "a.tail:7444", v.Members["a"].Door)
	require.Equal(t, s.now.Truncate(time.Millisecond), v.Members["a"].Since.Truncate(time.Millisecond))
}

// Merging is a set union: any order, any repetition, any split across
// rounds gives the same members and the same digest.
func TestMergeIsOrderFreeAndIdempotent(t *testing.T) {
	s := newEstate(t)
	for _, n := range []string{"a", "d", "e"} {
		s.intermediate(n)
	}
	es := []Entry{
		s.join("a", "a", 0), s.join("d", "d", 0), s.join("e", "e", 0),
		s.join("a", "b", 1*time.Minute), s.join("d", "c", 2*time.Minute), s.join("e", "f", 3*time.Minute), s.join("a", "g", 4*time.Minute),
		s.renewer("c", "d"), s.leave("c", "d", 5*time.Minute), s.revoke("e", 6*time.Minute), s.join("d", "f", 7*time.Minute), s.join("a", "a", 8*time.Minute),
	}
	want := s.ledger(es...).View()
	require.Equal(t, []string{"a", "b", "d", "f", "g"}, members(want))

	r := rand.New(rand.NewPCG(1, 2))
	for range 200 {
		shuffled := append([]Entry(nil), es...)
		shuffled = append(shuffled, es[r.IntN(len(es))], es[r.IntN(len(es))])
		r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		l := NewLedger("estate", s.root)
		cut := r.IntN(len(shuffled) + 1)
		_, err := l.Merge(shuffled[:cut]...)
		require.NoError(t, err)
		_, err = l.Merge(shuffled[cut:]...)
		require.NoError(t, err)
		got := l.View()
		require.Equal(t, want.Digest, got.Digest)
		require.Equal(t, want.Members, got.Members)
		require.Equal(t, want.Gone, got.Gone)
	}
}

func TestTheDigestIsZeroOnlyForNothing(t *testing.T) {
	s := newEstate(t)
	s.intermediate("a")
	require.Zero(t, NewLedger("estate", s.root).View().Digest)
	one := s.ledger(s.join("a", "a", 0)).View().Digest
	two := s.ledger(s.join("a", "a", 0), s.join("a", "b", 0)).View().Digest
	require.NotZero(t, one)
	require.NotEqual(t, one, two)
}

// A full ledger takes no more joins, but a leave or a revoke still lands,
// so a signer that floods the ledger can still be revoked.
func TestAFullLedgerRefusesJoinsButNotRevokes(t *testing.T) {
	s := newEstate(t)
	s.intermediate("a")
	l := NewLedger("estate", s.root)
	for i := range MaxEntries {
		l.entries[fmt.Sprint(i)] = verified{}
	}
	added, err := l.Merge(s.join("a", "a", 0))
	require.Error(t, err)
	require.Empty(t, added)
	added, err = l.Merge(s.revoke("a", 0), s.leave("b", "a", 0))
	require.NoError(t, err)
	require.Len(t, added, 2)
}

// Every node has an owner, the principal that joined it, signed into its
// join so no peer can rewrite it; the newest join names it, as it names the
// door.
func TestAJoinRecordsTheNodesOwner(t *testing.T) {
	s := newEstate(t)
	s.intermediate("a")
	guest, err := SignJoin(s.ikey["a"], s.inter["a"], "estate", "g", "g.lan:7443", "user:guest@auth.test", s.now)
	require.NoError(t, err)
	v := s.ledger(s.join("a", "a", 0), guest).View()
	require.Equal(t, "user:owner", v.Members["a"].Owner)
	require.Equal(t, "user:guest@auth.test", v.Members["g"].Owner)
}

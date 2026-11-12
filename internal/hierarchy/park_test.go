package hierarchy_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/hierarchy"
)

type parkGuard struct {
	guarded   map[string]bool
	consents  map[string]bool
	refused   map[string]bool
	gone      map[string]bool
	until     time.Time
	sawCaller []string
	sawMoves  [][]hierarchy.Move
}

func newParkGuard(guarded ...string) *parkGuard {
	g := &parkGuard{guarded: map[string]bool{}, consents: map[string]bool{"user:holder": true}, refused: map[string]bool{}, gone: map[string]bool{}}
	for _, p := range guarded {
		g.guarded[p] = true
	}
	return g
}

func (g *parkGuard) Check(_ context.Context, by hierarchy.Principal, p hierarchy.Plan) (hierarchy.Verdict, error) {
	g.sawCaller = append(g.sawCaller, by.Name)
	g.sawMoves = append(g.sawMoves, p.Moves)
	if g.refused[by.Name] {
		return hierarchy.Verdict{}, errors.New("revoked")
	}
	var waits []string
	for _, m := range p.Moves {
		if !g.guarded[m.To] {
			continue
		}
		if p.Consent != nil && p.Consent.Parent == m.To {
			continue
		}
		waits = append(waits, m.To)
	}
	if len(waits) > 1 {
		return hierarchy.Verdict{}, hierarchy.ErrManyParents
	}
	if len(waits) == 1 {
		return hierarchy.Verdict{Park: waits[0], Accepts: []string{"user:holder"}, Until: g.until}, nil
	}
	return hierarchy.Verdict{}, nil
}

func (g *parkGuard) Readmit(_ context.Context, by hierarchy.Principal) (hierarchy.Principal, error) {
	if g.gone[by.Name] {
		return by, errors.New("its key was revoked")
	}
	return by, nil
}

func (g *parkGuard) MayConsent(_ context.Context, by hierarchy.Principal, _ string) error {
	if !g.consents[by.Name] {
		return fmt.Errorf("%s may not adopt", by.Name)
	}
	return nil
}

var (
	proposer = hierarchy.Principal{Name: "user:proposer"}
	holder   = hierarchy.Principal{Name: "user:holder"}
	stranger = hierarchy.Principal{Name: "user:stranger"}
)

func parkCD(t *testing.T, h *hierarchy.Hierarchy) hierarchy.Proposal {
	t.Helper()
	_, err := h.Apply(context.Background(), proposer, hierarchy.Plan{Op: hierarchy.Arrange, Parents: map[string]string{"C": "D"}})
	var parked *hierarchy.ParkedError
	if !errors.As(err, &parked) || !errors.Is(err, hierarchy.ErrParked) {
		t.Fatalf("drawing C under a guarded D = %v, want parked", err)
	}
	return parked.Proposal
}

func TestAMoveWithoutConsentWaitsOnItsReceivingParent(t *testing.T) {
	f := chain()
	h := hierarchy.New(f, newParkGuard("D"))
	prop := parkCD(t, h)
	if f.writes != 0 {
		t.Fatal("a parked move wrote an edge")
	}
	if prop.Parent != "D" || prop.By.Name != proposer.Name || len(prop.Plan.Moves) != 1 {
		t.Fatalf("proposal %+v, want C>D by the proposer on D", prop)
	}
	if got := h.Parked("D"); len(got) != 1 || got[0].ID != prop.ID {
		t.Fatalf("Parked(D) = %+v, want the proposal", got)
	}
	if got := h.Parked("X"); len(got) != 0 {
		t.Fatalf("Parked(X) = %+v, want nothing", got)
	}
}

func TestAcceptReRunsTheGuardAsTheProposerAndApplies(t *testing.T) {
	f := chain()
	g := newParkGuard("D")
	h := hierarchy.New(f, g)
	prop := parkCD(t, h)
	g.sawCaller = nil
	if _, err := h.Accept(context.Background(), holder, "D", prop.ID); err != nil {
		t.Fatal(err)
	}
	if f.over["C"] != "D" {
		t.Fatalf("after accept C is drawn under %q, want D", f.over["C"])
	}
	if len(g.sawCaller) != 1 || g.sawCaller[0] != proposer.Name {
		t.Fatalf("accept checked as %v, want the proposer", g.sawCaller)
	}
	if len(h.Parked("D")) != 0 {
		t.Fatal("an accepted proposal still waits")
	}
}

func TestAnAcceptorWithoutAdoptLeavesTheProposalWaiting(t *testing.T) {
	f := chain()
	h := hierarchy.New(f, newParkGuard("D"))
	prop := parkCD(t, h)
	if _, err := h.Accept(context.Background(), stranger, "D", prop.ID); err == nil {
		t.Fatal("a stranger accepted")
	}
	if f.writes != 0 || len(h.Parked("D")) != 1 {
		t.Fatalf("writes %d, waiting %d: want nothing written and the proposal kept", f.writes, len(h.Parked("D")))
	}
	if err := h.Refuse(context.Background(), stranger, "D", prop.ID); err == nil || len(h.Parked("D")) != 1 {
		t.Fatalf("a stranger's refuse = %v; the proposal must stay", err)
	}
	if _, err := h.Accept(context.Background(), holder, "X", prop.ID); !errors.Is(err, hierarchy.ErrNoProposal) {
		t.Fatalf("accept on the wrong parent = %v, want ErrNoProposal", err)
	}
}

func TestARevokedProposerIsRefusedAtAccept(t *testing.T) {
	f := chain()
	g := newParkGuard("D")
	h := hierarchy.New(f, g)
	prop := parkCD(t, h)
	g.refused[proposer.Name] = true
	if _, err := h.Accept(context.Background(), holder, "D", prop.ID); err == nil {
		t.Fatal("accept applied a move its proposer may no longer make")
	}
	if f.writes != 0 {
		t.Fatal("a refused accept wrote an edge")
	}
}

// G6: the tree moved after the park, so accept plans again; the Moves seen
// at park time are never applied.
func TestAcceptAfterTheTreeMovedPlansAgain(t *testing.T) {
	f := chain()
	g := newParkGuard("D")
	h := hierarchy.New(f, g)
	prop := parkCD(t, h)
	f.over["C"] = "P"
	f.writes++
	if _, err := h.Accept(context.Background(), holder, "D", prop.ID); err != nil {
		t.Fatal(err)
	}
	last := g.sawMoves[len(g.sawMoves)-1]
	if len(last) != 1 || last[0].From != "P" || last[0].To != "D" {
		t.Fatalf("accept checked %+v, want C planned again from P to D", last)
	}
	if prop.Plan.Moves[0].From == "P" {
		t.Fatal("the test did not move C between park and accept")
	}
}

func TestAProposerWhoPinnedAVersionGetsStaleNotARePlan(t *testing.T) {
	f := chain()
	h := hierarchy.New(f, newParkGuard("D"))
	_, err := h.Apply(context.Background(), proposer, hierarchy.Plan{Op: hierarchy.Arrange, Parents: map[string]string{"C": "D"}, IfVersion: h.Version()})
	var parked *hierarchy.ParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("pinned arrange = %v, want parked", err)
	}
	f.writes++
	if _, err := h.Accept(context.Background(), holder, "D", parked.Proposal.ID); !errors.Is(err, hierarchy.ErrStale) {
		t.Fatalf("accept of a pinned plan after the tree moved = %v, want ErrStale", err)
	}
	if f.over["C"] == "D" {
		t.Fatal("a stale pinned plan was applied")
	}
}

func TestARefusalIsFinal(t *testing.T) {
	f := chain()
	h := hierarchy.New(f, newParkGuard("D"))
	prop := parkCD(t, h)
	if err := h.Refuse(context.Background(), holder, "D", prop.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Accept(context.Background(), holder, "D", prop.ID); !errors.Is(err, hierarchy.ErrNoProposal) {
		t.Fatalf("accept after refuse = %v, want ErrNoProposal", err)
	}
	if f.writes != 0 {
		t.Fatal("a refused proposal wrote an edge")
	}
}

func TestAnExpiredProposalIsDroppedSilently(t *testing.T) {
	f := chain()
	now := time.Unix(1_000_000, 0)
	h := hierarchy.New(f, newParkGuard("D")).WithClock(func() time.Time { return now })
	prop := parkCD(t, h)
	now = now.Add(hierarchy.ProposalTTL)
	if got := h.Parked("D"); len(got) != 0 {
		t.Fatalf("an expired proposal is still listed: %+v", got)
	}
	if _, err := h.Accept(context.Background(), holder, "D", prop.ID); !errors.Is(err, hierarchy.ErrNoProposal) {
		t.Fatalf("accept after expiry = %v, want ErrNoProposal", err)
	}
}

func parkAs(h *hierarchy.Hierarchy, by hierarchy.Principal, parent string) error {
	_, err := h.Apply(context.Background(), by, hierarchy.Plan{Op: hierarchy.Arrange, Parents: map[string]string{"C": parent}})
	if errors.Is(err, hierarchy.ErrQueueFull) || !errors.Is(err, hierarchy.ErrParked) {
		return err
	}
	return nil
}

func proposerNo(n int) hierarchy.Principal {
	return hierarchy.Principal{Name: fmt.Sprintf("user:proposer-%d", n)}
}

// El Inspector XIV, P2: accept admits the proposer again. One whose key
// was revoked since the park is refused, and the proposal is gone.
func TestAProposerNoLongerAdmittedIsRefusedAtAccept(t *testing.T) {
	f := chain()
	g := newParkGuard("D")
	h := hierarchy.New(f, g)
	prop := parkCD(t, h)
	g.gone[proposer.Name] = true
	if _, err := h.Accept(context.Background(), holder, "D", prop.ID); !errors.Is(err, hierarchy.ErrProposerGone) {
		t.Fatalf("accept for a proposer no longer admitted = %v, want ErrProposerGone", err)
	}
	if f.writes != 0 {
		t.Fatal("a proposer no longer admitted had its move applied")
	}
	if got := h.Parked("D"); len(got) != 0 {
		t.Fatalf("the refused proposal still waits: %+v", got)
	}
}

// El Inspector XIV, P2: a proposal lives no longer than its proposer's
// admission: the connection's age limit or its token's expiry.
func TestAProposalLapsesWithItsProposersAdmission(t *testing.T) {
	f := chain()
	now := time.Unix(1_000_000, 0)
	g := newParkGuard("D")
	g.until = now.Add(time.Hour)
	h := hierarchy.New(f, g).WithClock(func() time.Time { return now })
	prop := parkCD(t, h)
	if !prop.Expires.Equal(g.until) {
		t.Fatalf("the proposal expires %s, want with its proposer's admission at %s", prop.Expires, g.until)
	}
	now = g.until
	if _, err := h.Accept(context.Background(), holder, "D", prop.ID); !errors.Is(err, hierarchy.ErrNoProposal) {
		t.Fatalf("accept once the admission lapsed = %v, want ErrNoProposal", err)
	}
	if _, err := h.Apply(context.Background(), proposer, hierarchy.Plan{Op: hierarchy.Arrange, Parents: map[string]string{"C": "D"}}); !errors.Is(err, hierarchy.ErrProposerGone) {
		t.Fatalf("parking with a lapsed admission = %v, want ErrProposerGone", err)
	}
}

func TestTheQueueOnOneParentIsBounded(t *testing.T) {
	f := chain()
	h := hierarchy.New(f, newParkGuard("D"))
	for n := range hierarchy.ProposalsPerParent {
		if err := parkAs(h, proposerNo(n), "D"); err != nil {
			t.Fatalf("proposal %d on D: %v", n, err)
		}
	}
	if err := parkAs(h, proposerNo(hierarchy.ProposalsPerParent), "D"); !errors.Is(err, hierarchy.ErrQueueFull) {
		t.Fatalf("proposal %d on D = %v, want ErrQueueFull", hierarchy.ProposalsPerParent+1, err)
	}
	if n := len(h.Parked("D")); n != hierarchy.ProposalsPerParent {
		t.Fatalf("%d wait on D, want %d", n, hierarchy.ProposalsPerParent)
	}
}

// El Inspector XIV, P4: one proposer cannot fill a parent's queue alone.
func TestOneProposerHoldsAFewPlacesOnAParent(t *testing.T) {
	f := chain()
	h := hierarchy.New(f, newParkGuard("D"))
	for n := range hierarchy.ProposalsPerProposerOnParent {
		if err := parkAs(h, proposer, "D"); err != nil {
			t.Fatalf("proposal %d on D: %v", n, err)
		}
	}
	if err := parkAs(h, proposer, "D"); !errors.Is(err, hierarchy.ErrQueueFull) {
		t.Fatalf("proposal %d by one proposer on D = %v, want ErrQueueFull", hierarchy.ProposalsPerProposerOnParent+1, err)
	}
	if err := parkAs(h, stranger, "D"); err != nil {
		t.Fatalf("another proposer on D: %v", err)
	}
}

// El Inspector XIV, P4: nor every parent on the node.
func TestOneProposerHoldsABoundedNumberOverall(t *testing.T) {
	f := chain()
	parents := hierarchy.ProposalsPerProposer/hierarchy.ProposalsPerProposerOnParent + 1
	var guarded []string
	for n := range parents {
		q := fmt.Sprintf("Q%d", n)
		f.stored[q] = "stump"
		guarded = append(guarded, q)
	}
	h := hierarchy.New(f, newParkGuard(guarded...))
	for n := range hierarchy.ProposalsPerProposer {
		if err := parkAs(h, proposer, guarded[n/hierarchy.ProposalsPerProposerOnParent]); err != nil {
			t.Fatalf("proposal %d: %v", n, err)
		}
	}
	last := guarded[parents-1]
	if err := parkAs(h, proposer, last); !errors.Is(err, hierarchy.ErrQueueFull) {
		t.Fatalf("proposal %d by one proposer = %v, want ErrQueueFull", hierarchy.ProposalsPerProposer+1, err)
	}
	if err := parkAs(h, stranger, last); err != nil {
		t.Fatalf("another proposer on %s: %v", last, err)
	}
}

func TestADeleteNeverParks(t *testing.T) {
	f := chain()
	h := hierarchy.New(f, guardVerdict(hierarchy.Verdict{Park: "P"}))
	if _, err := h.Apply(context.Background(), proposer, hierarchy.Plan{Op: hierarchy.Kill, Node: "C"}); !errors.Is(err, hierarchy.ErrParked) {
		t.Fatalf("kill under a parking verdict = %v, want refused as parked", err)
	}
	if f.removed != nil || len(h.Parked("P")) != 0 {
		t.Fatal("a delete was taken or held as a proposal")
	}
}

func TestACallerCannotHandItselfConsent(t *testing.T) {
	f := chain()
	h := hierarchy.New(f, newParkGuard("D"))
	_, err := h.Apply(context.Background(), proposer, hierarchy.Plan{Op: hierarchy.Arrange, Parents: map[string]string{"C": "D"},
		Consent: &hierarchy.Consent{Parent: "D", By: holder}})
	if !errors.Is(err, hierarchy.ErrParked) || f.writes != 0 {
		t.Fatalf("apply with a forged consent = %v, writes %d: want parked", err, f.writes)
	}
}

type guardVerdict hierarchy.Verdict

func (g guardVerdict) Check(context.Context, hierarchy.Principal, hierarchy.Plan) (hierarchy.Verdict, error) {
	return hierarchy.Verdict(g), nil
}

func (guardVerdict) MayConsent(context.Context, hierarchy.Principal, string) error { return nil }

func (guardVerdict) Readmit(_ context.Context, by hierarchy.Principal) (hierarchy.Principal, error) {
	return by, nil
}

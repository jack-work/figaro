package angelus

import (
	"context"
	"errors"
	"testing"

	"github.com/jack-work/jkauthz"

	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/hierarchy"
)

// El Inspector XIV, P2: a caller who reached this node through a peer is
// admitted again only while that peer is a member, and then by its own
// credential when it carries one.
func TestACallerThroughANodeIsAdmittedOnlyWhileTheNodeIsAMember(t *testing.T) {
	sub := jkauthz.Subject{Principal: jkauthz.Principal{Kind: jkauthz.KindUser, ID: "mallory"}}
	by := hierarchy.Principal{Name: "user:mallory", Subject: authz.TopologyCaller{Subject: sub}}
	readmit := func(by hierarchy.Principal) error {
		_, err := by.Subject.(authz.TopologyCaller).Readmit(context.Background())
		return err
	}
	if err := readmit(throughNode(by, fateMembers{}, fateNode)); err != nil {
		t.Fatalf("through a member: %v", err)
	}
	if err := readmit(throughNode(by, fateMembers{}, "gone-node")); err == nil {
		t.Fatal("a caller through a node that is no longer a member was admitted")
	}
	revoked := errors.New("its key was revoked")
	c := by.Subject.(authz.TopologyCaller)
	c.Readmit = func(context.Context) (jkauthz.Subject, error) { return jkauthz.Subject{}, revoked }
	by.Subject = c
	if err := readmit(throughNode(by, fateMembers{}, fateNode)); !errors.Is(err, revoked) {
		t.Fatalf("through a member with a revoked key = %v, want the key's refusal", err)
	}
}

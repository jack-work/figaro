package angelus

import (
	"context"
	"testing"

	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/jkauthz"
)

// A birth stats the cwd it is sent on the host; a box's paths are not the
// host's, so the daemon never looks one up.
func TestABoxsBirthCwdIsNeverLookedUpOnTheHost(t *testing.T) {
	box := authz.WithSubject(context.Background(), jkauthz.Subject{Principal: authz.BoxPrincipal("judge")})
	if got := hostCwdOf(box, "/etc"); got != "" {
		t.Fatalf("a box's cwd reaches birthCwd as %q", got)
	}
	owner := authz.WithSubject(context.Background(), jkauthz.Subject{Principal: jkauthz.Principal{Kind: jkauthz.KindUser, ID: "1000"}})
	if got := hostCwdOf(owner, "/etc"); got != "/etc" {
		t.Fatalf("the owner's cwd became %q", got)
	}
}

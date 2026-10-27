package authz

import (
	"context"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkauthz"
)

func TestABoxPrincipalNamesItsBoxAndNothingElse(t *testing.T) {
	if got := BoxPrincipal("judge").String(); got != "sandbox:judge@local:box" {
		t.Fatalf("principal %s", got)
	}
	for _, c := range []struct {
		p    jkauthz.Principal
		want string
	}{
		{BoxPrincipal("judge"), "judge"},
		{jkauthz.Principal{Kind: jkauthz.KindSandbox, ID: "judge@auth.kelliher.info"}, ""},
		{jkauthz.Principal{Kind: jkauthz.KindSandbox, ID: "judge"}, ""},
		{jkauthz.Principal{Kind: jkauthz.KindUser, ID: "judge@local:box"}, ""},
		{jkauthz.Principal{Kind: jkauthz.KindSandbox, ID: "judge@local:box", Node: "spain"}, ""},
		{jkauthz.Principal{Kind: jkauthz.KindSandbox, ID: "@local:box"}, ""},
	} {
		if got, _ := BoxOf(c.p); got != c.want {
			t.Errorf("BoxOf(%s) = %q, want %q", c.p, got, c.want)
		}
	}
	if _, err := IssuerScope("https://" + BoxScope); err == nil {
		t.Fatal("an issuer can take the box scope")
	}
}

func TestTheBoxDoorStampsItsBoxWhoeverConnects(t *testing.T) {
	ctx, err := BoxAdmit("judge")(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := SubjectOf(ctx)
	if err != nil || s.Principal != BoxPrincipal("judge") {
		t.Fatalf("subject %+v %v", s, err)
	}
}

func TestTheDefaultPolicyHoldsABoxToItsOwnBox(t *testing.T) {
	f := facts()
	f.boxed = map[string]string{"dddd0001": "judge", "dddd0002": "other", "aaaa0002": ""}
	p := NewPolicies(me, f)
	p.SetBoxes([]string{"judge", "other"})
	if err := p.Reload(""); err != nil {
		t.Fatal(err)
	}
	judge := subject(BoxPrincipal("judge"))
	for _, v := range []rpc.Verb{rpc.VerbRead, rpc.VerbListen, rpc.VerbSend, rpc.VerbFork, rpc.VerbSet, rpc.VerbKill} {
		if d := p.Allow(judge, v, Aria("dddd0001")); !d.Allowed {
			t.Errorf("%s on an aria in its box: %s", v, d.Reason())
		}
		for _, outside := range []string{"dddd0002", "aaaa0002"} {
			if p.Allow(judge, v, Aria(outside)).Allowed {
				t.Errorf("%s on %s, outside its box", v, outside)
			}
		}
	}
	if p.Allow(judge, rpc.VerbAdmin, Aria("dddd0001")).Allowed || p.Allow(judge, rpc.VerbAdmin, Daemon).Allowed {
		t.Error("admin for a box")
	}
	if !p.Allow(judge, rpc.VerbCreate, Daemon).Allowed {
		t.Error("a box cannot create")
	}
	if !p.Allow(judge, rpc.VerbUse, Sandbox("judge")).Allowed {
		t.Error("a box cannot use itself")
	}
	for _, s := range []string{"local", "other", "bwrap"} {
		if p.Allow(judge, rpc.VerbUse, Sandbox(s)).Allowed {
			t.Errorf("box judge may use %s", s)
		}
	}
	spoof := subject(jkauthz.Principal{Kind: jkauthz.KindSandbox, ID: "judge@auth.kelliher.info"})
	if p.Allow(spoof, rpc.VerbRead, Aria("dddd0001")).Allowed || p.Allow(spoof, rpc.VerbUse, Sandbox("judge")).Allowed {
		t.Error("an issuer's sandbox:judge passes as the box")
	}
	unconfigured := subject(BoxPrincipal("ghost"))
	f.boxed["dddd0003"] = "ghost"
	if p.Allow(unconfigured, rpc.VerbRead, Aria("dddd0003")).Allowed {
		t.Error("a box with no door is bound")
	}
}

func TestNoBoxesLeaveTheDefaultAsItWas(t *testing.T) {
	if DefaultPolicy(me, nil) != DefaultPolicy(me, []string{}) {
		t.Fatal("an empty box list changed the default")
	}
}

func TestTheBoxDoorIsSeatedByPolicyAndNeverTrusted(t *testing.T) {
	if DoorBox.String() != "box" || !DoorBox.SeatsOnlyPolicySubjects() || DoorBox.Trusted() {
		t.Fatalf("door %s: seats-only %v, trusted %v", DoorBox, DoorBox.SeatsOnlyPolicySubjects(), DoorBox.Trusted())
	}
}

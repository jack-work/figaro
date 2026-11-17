package cli

import (
	"strings"
	"testing"
)

func TestPlanForkReplay(t *testing.T) {
	plan, err := planFork([]string{"abc:12", "--replay", "-S", "system.thinking_effort=high", "-j"})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.opts.replay || plan.spec != "abc:12" || !plan.opts.json {
		t.Fatalf("plan = %+v", plan)
	}
	if _, err := planFork([]string{"abc:12.-1", "--replay"}); err != nil {
		t.Fatalf("turn 12's question is turn 12: %v", err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"abc", "--replay"}, "<id>:<turn>"},
		{[]string{"abc.40", "--replay"}, "<id>:<turn>"},
		{[]string{"abc:12.3", "--replay"}, "<id>:<turn>"},
		{[]string{"abc:12", "--replay", "--", "other words"}, "drop the prompt"},
		{[]string{"abc:12", "--replay", "-f"}, "does not apply to --replay"},
	} {
		_, err := planFork(tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q", tc.args, err, tc.want)
		}
	}
}

func TestReplayIsForkOnly(t *testing.T) {
	if _, _, err := extractSendFlags([]string{"--replay", "--", "hi"}); err == nil {
		t.Fatal("send took --replay")
	}
	if _, err := prepareFork([]string{"abc:12", "--replay"}, pagerSurface); err == nil || !strings.Contains(err.Error(), "--replay") {
		t.Fatalf("the pager took --replay: %v", err)
	}
}

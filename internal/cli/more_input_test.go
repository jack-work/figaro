package cli

import (
	"strings"
	"testing"

	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

func TestMoreCombinesTranscriptAndStatusDetails(t *testing.T) {
	in, lt, _ := morePager(t)
	lt.status.model = "fixture-model"
	lt.status.finishTurn("end_turn")
	for _, want := range []bool{true, false, true} {
		feed(t, in, "m")
		if in.set.more != want || lt.status.barVerbose() != want {
			t.Fatalf("m: transcript details=%v status details=%v, want both %v", in.set.more, lt.status.barVerbose(), want)
		}
		_, rows := lt.tr.footerRows(lt.tr.index.total, 20)
		status := strings.Join(rows, "\n")
		if strings.Contains(status, "fixture-model") != want || strings.Contains(status, "done") != want {
			t.Fatalf("m: model and state verbosity do not match more=%v: %q", want, status)
		}
	}
}

func TestMoreRemainsTextInEditors(t *testing.T) {
	for _, prefix := range []string{"/", ":", ">"} {
		t.Run(prefix, func(t *testing.T) {
			in, lt, _ := morePager(t)
			feed(t, in, prefix)
			feed(t, in, "m")
			if in.set.more || lt.status.barVerbose() {
				t.Fatal("editor text toggled transcript or status details")
			}
			tr := lt.tr
			if prefix == "/" {
				if tr.query != "m" {
					t.Fatalf("search query=%q, want m", tr.query)
				}
			} else if got := tr.shownEditor().String(); got != "m" {
				t.Fatalf("editor text=%q, want m", got)
			}
		})
	}
}

func TestInputHeadingKeepsTurnWithoutMore(t *testing.T) {
	for _, more := range []bool{false, true} {
		tr := coordFixture(t, more)
		rows := plainRows(tr.lines())
		for _, turn := range []string{"7", "8"} {
			found := false
			for _, row := range rows {
				if strings.HasPrefix(row, "> input") && strings.HasSuffix(row, " "+turn) {
					found = true
				}
			}
			if !found {
				t.Fatalf("more=%v: input heading lacks turn %s:\n%s", more, turn, strings.Join(rows, "\n"))
			}
		}
	}
}

func TestPagerFooterHasOneDoubleRangeRuleAndStatus(t *testing.T) {
	tr := coordFixture(t, false)
	for _, w := range []int{24, 60, 100} {
		ft := ldrender.NewFakeTerminal(w, 20)
		tr.out = ft
		tr.resize(w, 20)
		tr.follow = true
		tr.renderFrame()
		screen := plainRows(ft.Screen())
		rule := screen[len(screen)-2]
		if len(screen) != 20 || ldrender.Width(rule) != w {
			t.Fatalf("w=%d: frame has %d rows, range rule has %d columns", w, len(screen), ldrender.Width(rule))
		}
		if !strings.HasPrefix(rule, ruleDouble) || !strings.HasSuffix(rule, "live") || strings.Contains(rule, ruleSingle) {
			t.Fatalf("w=%d: bottom range rule=%q", w, rule)
		}
		if strings.Contains(screen[len(screen)-1], ruleDouble) {
			t.Fatalf("w=%d: extra rule occupies the status row: %q", w, screen[len(screen)-1])
		}
	}
}

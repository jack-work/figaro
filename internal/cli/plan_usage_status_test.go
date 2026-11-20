package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/models"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/sdk/aria"
)

func pct(v float64) *float64 { return &v }

func TestPlanUsageLines(t *testing.T) {
	defer term.SetColorMode(term.ColorNever)()
	east := time.FixedZone("EST", -5*3600)
	now := time.Date(2026, 11, 19, 19, 0, 0, 0, east)
	inTwoHours := time.Date(2026, 11, 20, 2, 20, 0, 0, time.UTC)
	inFiveDays := time.Date(2026, 11, 24, 19, 20, 0, 0, time.UTC)
	past := time.Date(2026, 11, 19, 20, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		u    *models.PlanUsage
		want []string
	}{
		{"no plan usage", nil, nil},
		{"no windows", &models.PlanUsage{Plan: "anthropic-max"}, nil},
		{"absent percentage is unknown, not zero", &models.PlanUsage{Windows: []models.PlanWindow{{Window: "five_hour", Status: models.PlanAllowed}}}, nil},
		{"one window, reset in local time", &models.PlanUsage{Windows: []models.PlanWindow{{Window: "five_hour", UsedPercent: pct(37), ResetAt: inTwoHours}}}, []string{"5h 37% (resets 21:20)"}},
		{"two windows, a far reset names the day", &models.PlanUsage{Windows: []models.PlanWindow{
			{Window: "five_hour", UsedPercent: pct(37), ResetAt: inTwoHours},
			{Window: "seven_day", UsedPercent: pct(12), ResetAt: inFiveDays},
		}}, []string{"5h 37% (resets 21:20)", "7d 12% (resets Tue 14:20)"}},
		{"reset in the past is dropped", &models.PlanUsage{Windows: []models.PlanWindow{{Window: "seven_day", UsedPercent: pct(12), ResetAt: past}}}, []string{"7d 12%"}},
		{"stale", &models.PlanUsage{Windows: []models.PlanWindow{{Window: "five_hour", UsedPercent: pct(80), Stale: true}}}, []string{"5h 80% (stale)"}},
		{"warning", &models.PlanUsage{Windows: []models.PlanWindow{{Window: "five_hour", UsedPercent: pct(91), Status: models.PlanAllowedWarning}}}, []string{"5h 91%"}},
		{"rejected", &models.PlanUsage{Windows: []models.PlanWindow{{Window: "five_hour", UsedPercent: pct(100), Status: models.PlanRejected, ResetAt: inTwoHours}}}, []string{"5h rejected (resets 21:20)"}},
		{"unnamed window verbatim", &models.PlanUsage{Windows: []models.PlanWindow{{Window: "seven_day_opus", UsedPercent: pct(3)}}}, []string{"seven_day_opus 3%"}},
	} {
		var got []string
		for _, r := range planUsageLines(c.u, now) {
			got = append(got, r.text)
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestPlanUsageWarningIsHighlightedAndRejectedIsPlain(t *testing.T) {
	now := time.Date(2026, 11, 19, 19, 0, 0, 0, time.UTC)
	warn, _ := planWindowLine(models.PlanWindow{Window: "five_hour", UsedPercent: pct(91), Status: models.PlanAllowedWarning}, now)
	if warn.tone != toneWarn || strings.Contains(warn.text, "\x1b") {
		t.Fatalf("warning row = %+v, want plain text with the warn tone", warn)
	}
	rejected, _ := planWindowLine(models.PlanWindow{Window: "five_hour", UsedPercent: pct(100), Status: models.PlanRejected}, now)
	if rejected != (panelRow{text: "5h rejected"}) {
		t.Fatalf("rejected = %+v", rejected)
	}
}

// pitText strips escapes, so a colour painted into the text never reached the
// screen: the status pit must carry the warning as the row's tone.
func TestStatusPitCarriesTheWarningTone(t *testing.T) {
	tr := newTranscript(ldrender.NewFakeTerminal(100, 40), 100, 40, &ariaView{settings: &renderSettings{}}, aria.NewClient(), "aria1234", time.Unix(0, 0))
	tr.status.setPlanUsage(&models.PlanUsage{Windows: []models.PlanWindow{
		{Window: "five_hour", UsedPercent: pct(37)},
		{Window: "seven_day", UsedPercent: pct(91), Status: models.PlanAllowedWarning},
	}})
	tr.openStatusPit()
	toned := map[string]pitTone{}
	for _, r := range tr.pit.list().rows {
		toned[strings.TrimSpace(r.text)] = r.tone
	}
	if tone, ok := toned["usage     5h 37%"]; !ok || tone != toneNone {
		t.Fatalf("5h row missing or toned: %v", toned)
	}
	if tone, ok := toned["7d 91%"]; !ok || tone != toneWarn {
		t.Fatalf("7d warning row missing or untoned: %v", toned)
	}
}

func TestStatusPanelShowsPlanUsageOnlyWhenKnown(t *testing.T) {
	defer term.SetColorMode(term.ColorNever)()
	s := newSessionStatus("f", time.Now())
	s.update(aria.Metrics{ContextTokens: 1000, ContextExact: true})
	s.setLimit(1_000_000)
	without := strings.Join(s.panelLines(), "\n")
	if strings.Contains(without, "usage") {
		t.Fatalf("usage row without plan usage:\n%s", without)
	}
	if !s.setPlanUsage(&models.PlanUsage{Windows: []models.PlanWindow{{Window: "five_hour", UsedPercent: pct(37)}, {Window: "seven_day", UsedPercent: pct(12)}}}) {
		t.Fatal("setPlanUsage reported no change")
	}
	rows := s.panelLines()
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "  usage     5h 37%\n            7d 12%") {
		t.Fatalf("usage rows missing:\n%s", joined)
	}
	if len(rows) != len(strings.Split(without, "\n"))+2 {
		t.Fatalf("plan usage added %d rows, want 2", len(rows)-len(strings.Split(without, "\n")))
	}
}

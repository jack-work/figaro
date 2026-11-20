package cli

import (
	"testing"
	"time"

	"github.com/jack-work/figaro/api/models"
)

func TestAPlanWindowWithoutANameIsNotDrawn(t *testing.T) {
	if row, ok := planWindowLine(models.PlanWindow{UsedPercent: pct(40)}, time.Now()); ok {
		t.Fatalf("an unnamed window drew %q", row.text)
	}
}

func TestAWarningJustShortOfTheLimitDoesNotReadAsFull(t *testing.T) {
	now := time.Now()
	for used, want := range map[float64]string{99.4: "5h 99% used", 99.5: "5h 99.5% used", 99.96: "5h 99.9% used", 100: "5h 100% used"} {
		row, _ := planWindowLine(models.PlanWindow{Window: "five_hour", UsedPercent: pct(used), Status: models.PlanAllowedWarning}, now)
		if row.text != want {
			t.Errorf("warning at %v%% = %q, want %q", used, row.text, want)
		}
	}
}

// Gluck read "7d 98%" as what was left; the percent is what is used.
func TestAPlanWindowSaysItsPercentIsUsed(t *testing.T) {
	row, _ := planWindowLine(models.PlanWindow{Window: "seven_day", UsedPercent: pct(98)}, time.Now())
	if row.text != "7d 98% used" {
		t.Fatalf("a 7d window at 98%% = %q, want it to say used", row.text)
	}
}

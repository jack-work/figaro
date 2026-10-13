package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
)

type settingsSpy struct {
	irWindow, irBudget, transBudget int
}

func (s *settingsSpy) SetIRWindow(n int)          { s.irWindow = n }
func (s *settingsSpy) SetIRBudget(n int)          { s.irBudget = n }
func (s *settingsSpy) SetTranslationBudget(n int) { s.transBudget = n }

func TestMemorySettingsReachTheirEnforcementPoints(t *testing.T) {
	dir := t.TempDir()
	body := `
[memory]
ir_window              = 321
ir_window_mb           = 7
translation_window_mb  = 5
form_patch_window      = 64
actor_linger_ms        = 250
handle_idle_minutes    = 3
segment_cache_mb       = 9
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	applyStoreSettings(loaded)
	spy := &settingsSpy{}
	if !applyCacheSettings(loaded, spy) {
		t.Fatal("the backend interface check refused a backend that implements all three")
	}

	// Enforcement points, not accessors.
	if got := store.FormLingerForTest(); got != 250*time.Millisecond {
		t.Errorf("actor linger at the enforcement point = %v, want 250ms", got)
	}
	if got := store.HandleIdleForTest(); got != 3*time.Minute {
		t.Errorf("handle idle at the enforcement point = %v, want 3m", got)
	}
	if got := store.PatchWindowForTest(); got != 64 {
		t.Errorf("form patch window at the enforcement point = %d, want 64", got)
	}
	if got := store.SegmentCacheBudget(); got != 9<<20 {
		t.Errorf("segment cache budget at the enforcement point = %d, want %d",
			got, 9<<20)
	}
	if spy.irWindow != 321 {
		t.Errorf("ir window = %d, want 321", spy.irWindow)
	}
	if spy.irBudget != 7<<20 {
		t.Errorf("ir budget = %d, want %d", spy.irBudget, 7<<20)
	}
	if spy.transBudget != 5<<20 {
		t.Errorf("translation budget = %d, want %d", spy.transBudget, 5<<20)
	}
}

func TestCacheSettingsSkipABackendWithoutWindows(t *testing.T) {
	var loaded *config.Loaded
	if applyCacheSettings(loaded, struct{}{}) {
		t.Fatal("a backend with no setters reported that it took the settings")
	}
}

// Compares the enforcement point before and after, so a neighbouring test cannot
// satisfy it.
func TestAnUnsetKeyLeavesTheOwnersBound(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[memory]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	before := store.SegmentCacheBudget()
	applyStoreSettings(loaded)
	if after := store.SegmentCacheBudget(); after != before {
		t.Errorf("an unset segment_cache_mb moved the budget %d -> %d", before, after)
	}

	spy := &settingsSpy{irWindow: -1, irBudget: -1, transBudget: -1}
	if !applyCacheSettings(loaded, spy) {
		t.Fatal("the backend interface check refused a backend that implements all three")
	}
	if spy.irWindow != -1 || spy.irBudget != -1 || spy.transBudget != -1 {
		t.Errorf("an empty config called a setter: window=%d budget=%d trans=%d",
			spy.irWindow, spy.irBudget, spy.transBudget)
	}
}

// An explicit zero (unbounded) is not the same as an unset key.
func TestAnExplicitZeroIsHonoured(t *testing.T) {
	dir := t.TempDir()
	body := "[memory]\nir_window_mb = 0\ntranslation_window_mb = 0\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	spy := &settingsSpy{irBudget: -1, transBudget: -1}
	if !applyCacheSettings(loaded, spy) {
		t.Fatal("the backend interface check refused a backend that implements all three")
	}
	if spy.irBudget != 0 || spy.transBudget != 0 {
		t.Errorf("explicit zeros did not reach the store: budget=%d trans=%d",
			spy.irBudget, spy.transBudget)
	}
}

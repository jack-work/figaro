package cli

// From a config FILE to the place each value is ENFORCED.
//
// The plan asks for exactly this test and it did not exist. Two knobs in this
// project have shipped configured-but-unwired - the IR window defaulted to
// off, and figwal's IdleUnload read nothing - and a parser test cannot catch
// either, because the parse was never the broken half.

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

	// Enforcement points, not accessors: these are the variables the store
	// actually reads when it opens a handle, builds a form, or trims.
	if got := store.FormLingerForTest(); got != 250*time.Millisecond {
		t.Errorf("actor linger at the enforcement point = %v, want 250ms", got)
	}
	if got := store.HandleIdleForTest(); got != 3*time.Minute {
		t.Errorf("handle idle at the enforcement point = %v, want 3m", got)
	}
	if got := store.PatchWindowForTest(); got != 64 {
		t.Errorf("form patch window at the enforcement point = %d, want 64", got)
	}
	// figwal's own budget, read back from figwal rather than from the loader:
	// this is the one bound that lives in another module, which is exactly
	// the trip a parser test cannot make.
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

// A backend that implements none of the cache setters must be REFUSED
// quietly rather than panicking: an ephemeral or test backend has no window
// to bound, which is the reason the interface is optional.
func TestCacheSettingsSkipABackendWithoutWindows(t *testing.T) {
	var loaded *config.Loaded // nil-safe accessors, defaults throughout
	if applyCacheSettings(loaded, struct{}{}) {
		t.Fatal("a backend with no setters reported that it took the settings")
	}
}

package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A BARE BACKEND IS BOUNDED. Until 2026-08-19 it was not: irBudget and
// transBudget defaulted to 0 (unbounded) and only internal/cli's daemon wiring
// set them, so doctor, every test, and any embedding that forgot the wiring
// retained every decoded entry forever. Nothing in the tree could go red about
// it, because the layer that owned the bytes had no opinion about them.
//
// The assertion is the ARTIFACT -- bytes actually resident after writing well
// past the budget -- not the field, because a field can be set by a
// constructor and then never reach the handle that decodes.
func TestBareBackendIsBounded(t *testing.T) {
	// 400 entries of 8 KiB is ~3.2 MiB encoded, and the estimate charges
	// irDecodeInflation on top: several times the 4 MiB budget either way.
	be, id := realAria(t, 400, 8<<10)

	log, err := be.Open(id)
	require.NoError(t, err)
	require.NotEmpty(t, log.Read(), "the aria must have history to hold")

	// The ceiling is the budget PLUS the compaction slack, which is the bound
	// cachedLog actually enforces: it trims in batches, so residency peaks at
	// budget + budget*slackNum/slackDen and never above. Asserting the budget
	// alone would be asserting a policy this cache does not implement.
	ceiling := DefaultIRBudgetBytes + DefaultIRBudgetBytes*slackNum/slackDen
	resident := be.ResidentIRBytes()
	require.NotZero(t, resident, "a read must leave something resident")
	require.LessOrEqual(t, resident, ceiling,
		"a backend nobody configured must hold its own bound")
}

// And the CLI still tunes: a caller that sets a budget replaces the default
// rather than being ignored by it.
func TestConfiguredBudgetReplacesTheDefault(t *testing.T) {
	be, err := NewXwalBackend(t.TempDir(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { be.Close() })

	be.SetIRBudget(1 << 20)
	be.SetTranslationBudget(2 << 20)

	be.mu.Lock()
	ir, trans := be.irBudget, be.transBudget
	be.mu.Unlock()
	require.Equal(t, 1<<20, ir)
	require.Equal(t, 2<<20, trans)
}

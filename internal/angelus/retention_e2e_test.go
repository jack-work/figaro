package angelus_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/sdk"
)

type formBearer interface {
	ApplyForm(string, form.Patch) (uint64, error)
	ForkWith(string, uint64, message.Patch, ...store.BirthOption) (string, uint64, error)
	FormAt(string) (store.FormAt, error)
	FormStateAt(string, uint64) (form.Snapshot, error)
}

func longForm(t *testing.T, a *angelus.Angelus, acli *sdk.Angelus, ctx context.Context) (formID, child string, pin uint64, born string) {
	return longFormTB(t, a, acli, ctx)
}

func longFormTB(t testing.TB, a *angelus.Angelus, acli *sdk.Angelus, ctx context.Context) (formID, child string, pin uint64, born string) {
	t.Helper()
	b := a.Backend.(formBearer)
	seed := form.Build(form.Snapshot{}, map[string]json.RawMessage{"seed": json.RawMessage(`0`)}, nil)
	made, err := acli.FormCreate(ctx, "", nil, &seed)
	require.NoError(t, err)
	formID = made.FormID
	pad := strings.Repeat("z", 64<<10)
	write := func(from, n int) {
		for i := from; i < from+n; i++ {
			raw, _ := json.Marshal(fmt.Sprintf("%d-%s", i, pad))
			_, err := b.ApplyForm(formID, form.Build(form.Snapshot{}, map[string]json.RawMessage{fmt.Sprintf("k%d", i): raw}, nil))
			require.NoError(t, err)
		}
	}
	write(0, 40)
	at, err := b.FormAt(formID)
	require.NoError(t, err)
	child, _, err = b.ForkWith(formID, 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{"mine": json.RawMessage(`1`)}, nil))
	require.NoError(t, err)
	raw, _ := at.Snapshot.MarshalJSON()
	write(40, 40)
	return formID, child, at.Version, string(raw)
}

func gcFixture(t *testing.T, mode string) (*angelus.Angelus, *sdk.Angelus, context.Context) {
	t.Helper()
	a, acli, ctx, _ := daemonFixtureConfig(t, "[store]\nsegment_size = 1048576\ngc = \""+mode+"\"\n")
	return a, acli, ctx
}

func TestGCReportsFormHistoryUnderDryRun(t *testing.T) {
	a, acli, ctx := gcFixture(t, "dry-run")
	formID, _, _, _ := longForm(t, a, acli, ctx)

	resp, err := acli.GC(ctx, false)
	require.NoError(t, err)
	require.Equal(t, "dry-run", resp.Retention)
	require.True(t, resp.FormsPlanned, "under dry-run a plain `figaro gc` must only plan")
	require.Len(t, resp.Forms, 1)
	require.Equal(t, formID, resp.Forms[0].ID)
	require.Positive(t, resp.Forms[0].Segments)

	again, err := acli.GC(ctx, false)
	require.NoError(t, err)
	require.Equal(t, resp.Forms, again.Forms, "the dry run dropped something")
}

func TestGCDropsFormHistoryWhenOn(t *testing.T) {
	a, acli, ctx := gcFixture(t, "on")
	formID, child, pin, born := longForm(t, a, acli, ctx)

	plan, err := acli.GC(ctx, true)
	require.NoError(t, err)
	require.True(t, plan.FormsPlanned)
	require.Len(t, plan.Forms, 1)

	done, err := acli.GC(ctx, false)
	require.NoError(t, err)
	require.False(t, done.FormsPlanned)
	require.Equal(t, plan.Forms, done.Forms)
	require.Equal(t, formID, done.Forms[0].ID)

	st, err := a.Backend.(formBearer).FormStateAt(child, pin)
	require.NoError(t, err)
	raw, _ := st.MarshalJSON()
	require.Equal(t, born, string(raw), "the aria born from the form lost the version it was born at")

	after, err := acli.GC(ctx, false)
	require.NoError(t, err)
	require.Empty(t, after.Forms)
}

func TestGCLeavesFormHistoryWhenOff(t *testing.T) {
	a, acli, ctx := gcFixture(t, "off")
	longForm(t, a, acli, ctx)
	resp, err := acli.GC(ctx, false)
	require.NoError(t, err)
	require.Equal(t, "off", resp.Retention)
	require.Empty(t, resp.Forms)
}

func TestABadGCSettingIsReportedAndRetainsNothing(t *testing.T) {
	a, acli, ctx := gcFixture(t, "sometimes")
	longForm(t, a, acli, ctx)
	resp, err := acli.GC(ctx, false)
	require.NoError(t, err)
	require.Contains(t, resp.FormsErr, "sometimes")
	require.Empty(t, resp.Forms)
}

// Unset, gc keeps every fork point: nothing is planned or dropped.
func TestGCIsOffWhenUnset(t *testing.T) {
	a, acli, ctx, _ := daemonFixtureConfig(t, "[store]\nsegment_size = 1048576\n")
	longForm(t, a, acli, ctx)
	resp, err := acli.GC(ctx, false)
	require.NoError(t, err)
	require.Equal(t, "off", resp.Retention)
	require.Empty(t, resp.Forms)
}

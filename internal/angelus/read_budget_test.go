package angelus

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/stretchr/testify/require"
)

func TestDormantReadAppliesPageBudget(t *testing.T) {
	backend, id := benchStore(t, 16)
	pageBudget, maxBudget := 200, 800
	for _, settings := range []*config.Loaded{nil, {Config: config.Config{Wire: config.WireConfig{PageBudget: &pageBudget, PageBudgetMax: &maxBudget}}}} {
		h := &handlers{angelus: &Angelus{Backend: backend, Settings: settings}}
		for _, backward := range []bool{false, true} {
			for _, budget := range []int{0, -1, 300, 1 << 30} {
				req := rpc.ReadRequest{Backward: backward, Limit: budget}
				raw, err := json.Marshal(req)
				require.NoError(t, err)
				got, handled, err := h.readFromStore(id, rpc.MethodRead, raw)
				require.NoError(t, err)
				require.True(t, handled)
				page := got.(aria.Page)
				require.NotEmpty(t, page.Parts)
				want, err := h.reader().Page(id, req.At, settings.ClampPageBudget(budget), backward)
				require.NoError(t, err)
				require.Equal(t, want, page)
			}
		}
	}
}

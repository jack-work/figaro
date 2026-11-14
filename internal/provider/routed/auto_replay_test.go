package routed

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/stretchr/testify/require"
)

func TestRemovedAutoAliasCannotBecomeAnOrdinaryReplay(t *testing.T) {
	for _, alias := range []string{"route@auto", "custom-alias"} {
		p := mustNew(t, Spec{Name: "removed-auto", BaseURL: func() string { return "http://127.0.0.1:1" }, Default: Chat})
		p.cat = newCatalogCache(map[string]models.Entry{alias: {Auto: &models.AutoRoute{Generation: "g1"}}})
		raw, _ := json.Marshal(alias)
		in := provider.SendInput{Snapshot: form.FromMap(map[string]json.RawMessage{"system.model": raw})}
		err := p.Send(context.Background(), in, &captureBus{})
		require.Error(t, err)
		require.True(t, provider.ReplayForbidden(err))
		p.cat = newCatalogCache(map[string]models.Entry{})
		err = p.Send(context.Background(), in, &captureBus{})
		require.ErrorContains(t, err, "catalog entry is unavailable")
		require.True(t, provider.ReplayForbidden(err))
	}
}

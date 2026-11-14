package routed

import (
	"context"
	"fmt"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAutoCatalogUsesTheMinimumAndPreservesUnknown(t *testing.T) {
	for _, capacity := range []string{"128000", "null"} {
		t.Run(capacity, func(t *testing.T) {
			status := "known"
			if capacity == "null" {
				status = "unknown"
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"data":[{"id":"auto","context_length":1000000,"supported_endpoints":["/responses"],"rou2_auto":{"generation":"g1","context_status":%q,"candidates":[{"model":"a","apis":["responses"],"context_length":1000000},{"model":"b","apis":["messages"],"context_length":%s}]}}]}`, status, capacity)
			}))
			defer srv.Close()
			p := gatewayAt(t, "autocontext"+status, "preset = \"gateway\"\nbase_url = \""+srv.URL+"/v1\"\n[models.auto]\ncontext = 2000000\nlong = 4000000\n")
			entries, err := p.Models(context.Background())
			require.NoError(t, err)
			require.Len(t, entries, 1)
			entry := entries[0]
			require.NotNil(t, entry.Auto)
			require.Equal(t, "g1", entry.Auto.Generation)
			require.Len(t, entry.Auto.Candidates, 2)
			want := 128000
			if capacity == "null" {
				want = 0
			}
			require.Equal(t, want, entry.Window(""))
			require.Equal(t, want, entry.Window("long_context"))
		})
	}
}

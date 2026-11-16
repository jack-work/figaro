package provider

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStampCallerPromotesDaemonNodeOnlyWhenCallerOptedIn(t *testing.T) {
	base := WithDaemonNode(context.Background(), "node-uuid-1")

	unopted := http.Header{
		FigaroIDHeader:   {"forged-id"},
		FigaroNodeHeader: {"forged-node"},
	}
	StampCaller(base, unopted)
	require.Equal(t, []string{"forged-id"}, unopted.Values(FigaroIDHeader))
	require.Equal(t, []string{"forged-node"}, unopted.Values(FigaroNodeHeader))

	opted := WithFigaroID(base, "aria-123")
	stamped := http.Header{
		FigaroIDHeader:   {"forged-id"},
		FigaroNodeHeader: {"forged-node"},
	}
	StampCaller(opted, stamped)
	require.Equal(t, []string{"aria-123"}, stamped.Values(FigaroIDHeader))
	require.Equal(t, []string{"node-uuid-1"}, stamped.Values(FigaroNodeHeader))

	nodeOnly := WithFigaroNode(context.Background(), "node-label-2")
	decideHdr := http.Header{FigaroNodeHeader: {"forged-node"}}
	StampCaller(nodeOnly, decideHdr)
	require.Empty(t, decideHdr.Values(FigaroIDHeader))
	require.Equal(t, []string{"node-label-2"}, decideHdr.Values(FigaroNodeHeader))
}

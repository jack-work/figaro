package peer

import (
	"crypto/x509"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A sighting from gossip carries no certificate, so it keeps the leaf life
// the last handshake recorded rather than forgetting it.
func TestALaterSightingKeepsTheLeafLifeAHandshakeRecorded(t *testing.T) {
	v := &Versions{}
	now := time.Now()
	v.Saw("b", 3, 3, 0)
	v.Presented("b", &x509.Certificate{NotBefore: now, NotAfter: now.Add(30 * 24 * time.Hour)})
	v.Saw("b", 4, 4, 0)
	s, ok := v.Seen("b")
	require.True(t, ok)
	require.Equal(t, uint64(4), s.Version)
	require.Equal(t, 30*24*time.Hour, s.LeafLife)
}

package authz

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestADelegatedSubjectIsNeverTheOwnerEvenWhenItsPrincipalIsTheOwners(t *testing.T) {
	ctx, err := delegationsFor(t, gluckText).Apply(gatewaySession(t), "gluck")
	require.NoError(t, err)
	sub, err := SubjectOf(ctx)
	require.NoError(t, err)
	_, delegated := DelegatedVia(sub)
	require.True(t, delegated)
	require.False(t, IsOwner(sub.Principal, sub))
	require.False(t, PassesUnpoliced(DoorWeb, sub.Principal, sub))
}

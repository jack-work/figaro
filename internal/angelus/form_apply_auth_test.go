package angelus_test

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

func TestManagedRoleIdentityRefusalDoesNotPrecedeAuthorization(t *testing.T) {
	r, id := managedRole(t)
	for _, ref := range []string{id, "@ffffffffffffffffffffffffffffffff"} {
		_, err := r.call(as("user", "guest"), rpc.MethodFormApply, rpc.FormApplyRequest{
			FigaroID: ref, Outfits: []string{"role"}, Managed: []string{"charter.purpose"}, Check: true,
		})
		if err == nil || strings.Contains(err.Error(), "exact existing") {
			t.Fatalf("identity validation exposed a target before authorization: %s: %v", ref, err)
		}
	}
}

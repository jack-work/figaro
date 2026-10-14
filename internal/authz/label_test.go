package authz

import (
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

// x-caller comes from an environment variable; if it could stand in for an
// aria id every rule would be one FIGARO_CALLER away from bypass.
func TestLabelCannotAuthorize(t *testing.T) {
	params, err := rpc.WithCaller(rpc.ForkRequest{FigaroID: "aria0001"}, "", &rpc.CallerRef{Label: "aria0001"})
	if err != nil {
		t.Fatalf("WithCaller: %v", err)
	}
	id := AriaHeader{}.Authenticate(user, params)
	if id.FigaroID != "" {
		t.Fatalf("a label leaked into FigaroID: %+v", id)
	}
	if id.Label != "aria0001" {
		t.Fatalf("label lost: %+v", id)
	}
}

package angelus

import (
	"context"
	"slices"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

// A CLI of another build decides by what the daemon reports here: protocol,
// release and capabilities, on the local socket's status and on the endpoint's hello.
func TestStatusAndHelloReportProtocolReleaseAndCapabilities(t *testing.T) {
	hs := authzHandlers(t)
	hs.h.angelus.Release = "0.52.4"
	out, err := hs.Map[rpc.MethodStatus](inProcess(context.Background()), nil)
	if err != nil {
		t.Fatal(err)
	}
	st := out.(rpc.StatusResponse)
	if st.Protocol != rpc.ProtocolVersion || st.Release != "0.52.4" {
		t.Fatalf("status protocol %d release %q", st.Protocol, st.Release)
	}
	if !slices.Contains(st.Capabilities, rpc.CapQuaImages) {
		t.Fatalf("status capabilities %v lack %q", st.Capabilities, rpc.CapQuaImages)
	}
	hello, err := hs.Map[rpc.MethodHello](inProcess(context.Background()), []byte(`{"version":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := hello.(rpc.HelloResponse).Capabilities; !slices.Equal(got, st.Capabilities) {
		t.Fatalf("hello capabilities %v, status %v", got, st.Capabilities)
	}
}

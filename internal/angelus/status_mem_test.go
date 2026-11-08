package angelus

import (
	"context"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

// Every CLI command asks status for the daemon's build before it does
// anything, and the memory accounting stops the world to read. A status that
// says skip_mem must not pay for it; one that does not keeps it, so an older
// CLI's `doctor mem` still gets its answer.
func TestStatusSkipsTheMemoryAccountingWhenAsked(t *testing.T) {
	hs := authzHandlers(t)
	out, err := hs.Map[rpc.MethodStatus](inProcess(context.Background()), []byte(`{"skip_mem":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if st := out.(rpc.StatusResponse); st.Mem != nil || st.Build != hs.h.angelus.Build || st.Protocol != rpc.ProtocolVersion {
		t.Fatalf("skip_mem status: mem %v build %q protocol %d", st.Mem, st.Build, st.Protocol)
	}
	for _, params := range []string{``, `{}`} {
		out, err := hs.Map[rpc.MethodStatus](inProcess(context.Background()), []byte(params))
		if err != nil {
			t.Fatal(err)
		}
		if out.(rpc.StatusResponse).Mem == nil {
			t.Fatalf("status %q dropped the memory accounting an older CLI asks for", params)
		}
	}
}

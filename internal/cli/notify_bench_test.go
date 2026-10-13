package cli

import (
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/sdk/aria"
)

// THE FENCE IS ON THE HOTTEST PATH A PAGER HAS: every frame of every stream
// passes through notifyHandler. This measures one frame through it on a
// claimed subject: an aria page (the identity check is a string compare under
// a lock the handler already takes) and an intrinsic form delta (where the
// check takes the lock once more). And the dropped case, which is what a frame
// costs when it arrives during a hop.
func BenchmarkNotifyFrame(b *testing.B) {
	in := fenceSession(b)
	in.intrinsics = newIntrinsicMirrors() // as runsession wires a real session
	const id = "ariaAAAA"
	gen := atomic.AddUint64(&in.subjectGen, 1)
	if _, _, ok := in.claimSubject(gen, id, "", nil, 0); !ok {
		b.Fatal("could not claim")
	}
	in.lt.apply(aria.Page{Parts: partsFor(1, 20, "T")})
	page, _ := json.Marshal(aria.Page{Parts: partsFor(20, 1, "T")})
	delta, _ := json.Marshal(rpc.FormDelta{Intrinsic: "runtime"})

	handler := in.notifyHandler(gen, id)
	b.Run("frame=aria", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			handler(rpc.MethodAriaFrame, page)
		}
	})
	b.Run("frame=form-delta", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			handler(rpc.MethodFormDelta, delta)
		}
	})
	early := in.notifyHandler(gen, "ariaBBBB")
	b.Run("frame=aria-before-claim", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			early(rpc.MethodAriaFrame, page)
		}
	})
}

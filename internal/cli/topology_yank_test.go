package cli

import (
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

func yankTopology(t *testing.T, f *fakeMounts) (*interactiveInput, *recordingTerminal, *formView) {
	t.Helper()
	in, term, _ := yankInput(t)
	f.hint = ariasLikeHint
	f.forms["@things"] = ariasLike()
	f.forms["far/@things"] = ariasLike()
	v := indexView(f)
	v.subject = attending("ccc")
	seekAttended(v, f.open)
	in.enterTranscript()
	in.mu.Lock()
	in.installLivePit("troupe", v, false)
	in.lt.tr.render()
	in.mu.Unlock()
	return in, term, v
}

func selectRow(t *testing.T, in *interactiveInput, id string) {
	t.Helper()
	in.mu.Lock()
	defer in.mu.Unlock()
	in.lt.tr.pit.refreshLive(in.lt.tr.w)
	in.lt.tr.pit.focus(id)
	if row, _ := in.lt.tr.pit.selected(); row.id != id {
		t.Fatalf("selected %q, want %q", row.id, id)
	}
}

// In the topology y copies a figaro's handle, which is what one types to
// reach it, and M-y its full id; on a peer's row both carry the peer.
func TestTopologyYankCopiesTheHandleAndMetaYTheID(t *testing.T) {
	f := newFakeMounts()
	in, term, v := yankTopology(t, f)
	selectRow(t, in, "here"+mountSep+"arias.top.forks.mid.forks.me")
	term.send([]byte{'y'})
	waitClipboard(t, term, "me")
	term.send([]byte("\x1by"))
	waitClipboard(t, term, "ccc")

	v.Activate("far")
	v.Items(100)
	f.waitOpened(t, 2)
	selectRow(t, in, "far"+mountSep+"arias.other")
	term.send([]byte{'y'})
	waitClipboard(t, term, "far"+rpc.NodeSeparator+"other")
	term.send([]byte("\x1by"))
	waitClipboard(t, term, "far"+rpc.NodeSeparator+"ddd")
}

// A row whose key is not the handle (M-l names rows by a handle's first word)
// carries the handle in a leaf; y copies that.
func TestARowsHandleLeafIsWhatYCopies(t *testing.T) {
	in, term, _ := yankInput(t)
	src := things("pushup.id", `"aaa"`, "pushup.handle", `"pushup-turtle"`, "pushup.title", `"steering"`)
	v := &formView{src: src, open: map[string]bool{}}
	v.setView(&rpc.FormView{Rows: []string{"*"}, RowAria: "id", Label: "title"})
	in.enterTranscript()
	in.mu.Lock()
	in.installLivePit("recent", v, false)
	in.lt.tr.render()
	in.mu.Unlock()
	selectRow(t, in, "pushup")
	term.send([]byte{'y'})
	waitClipboard(t, term, "pushup-turtle")
	term.send([]byte("\x1by"))
	waitClipboard(t, term, "aaa")
}

package cli

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/quote"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

const (
	commentsKey      = "comments"
	commentGlyph     = "✎"
	commentIndent    = 2
	commentBodyShift = 6
)

type commentEntry struct {
	key  string
	by   string
	text string
}

type comment struct {
	id     string
	token  string
	at     quote.Range
	turn   int
	thread []commentEntry
}

type blockKey struct {
	lt    uint64
	block int
}

type commentIndex struct {
	list    []comment
	byBlock map[blockKey][]int
	sig     string
}

type commentRecord struct {
	At     string                        `json:"at"`
	Turn   int                           `json:"turn,omitempty"`
	Thread map[string]commentEntryRecord `json:"thread,omitempty"`
}

type commentEntryRecord struct {
	By   string `json:"by,omitempty"`
	Text string `json:"text"`
}

func readComments(board form.Snapshot) *commentIndex {
	idx := &commentIndex{byBlock: map[blockKey][]int{}}
	raw, ok := board.Get(commentsKey)
	if !ok {
		return idx
	}
	var records map[string]commentRecord
	if json.Unmarshal(raw, &records) != nil {
		return idx
	}
	for id, rec := range records {
		r, _, err := quote.Parse(strings.TrimSpace(rec.At))
		if err != nil {
			continue
		}
		c := comment{id: id, token: quote.Format(r), at: r, turn: rec.Turn}
		for key, e := range rec.Thread {
			c.thread = append(c.thread, commentEntry{key: key, by: e.By, text: e.Text})
		}
		sort.Slice(c.thread, func(i, j int) bool { return c.thread[i].key < c.thread[j].key })
		idx.list = append(idx.list, c)
	}
	sort.SliceStable(idx.list, func(i, j int) bool {
		a, b := idx.list[i], idx.list[j]
		if coordLess(a.at.Start, b.at.Start) != coordLess(b.at.Start, a.at.Start) {
			return coordLess(a.at.Start, b.at.Start)
		}
		return a.id < b.id
	})
	var sig strings.Builder
	for i, c := range idx.list {
		end := endBlockOf(c.at)
		idx.byBlock[end] = append(idx.byBlock[end], i)
		sig.WriteString(c.id)
		sig.WriteString(c.token)
		for _, e := range c.thread {
			sig.WriteString("\x00" + e.key + "\x00" + e.by + "\x00" + e.text)
		}
		sig.WriteString("\x01")
	}
	idx.sig = sig.String()
	return idx
}

func coordLess(a, b quote.Coord) bool {
	if a.LT != b.LT {
		return a.LT < b.LT
	}
	if a.Block != b.Block {
		return a.Block < b.Block
	}
	return a.Offset < b.Offset
}

func keyLess(a, b blockKey) bool {
	if a.lt != b.lt {
		return a.lt < b.lt
	}
	return a.block < b.block
}

func endBlockOf(r quote.Range) blockKey {
	if !r.Block {
		return blockKey{lt: r.Start.LT}
	}
	return blockKey{lt: r.End.LT, block: r.End.Block}
}

func (c comment) covering(k blockKey) (lo, hi int, ok bool) {
	r := c.at
	if !r.Block {
		return 0, -1, k.lt == r.Start.LT
	}
	start := blockKey{lt: r.Start.LT, block: r.Start.Block}
	end := blockKey{lt: r.End.LT, block: r.End.Block}
	if keyLess(k, start) || keyLess(end, k) {
		return 0, 0, false
	}
	lo, hi = 0, -1
	if !r.Offsets {
		return lo, hi, true
	}
	if k == start {
		lo = r.Start.Offset
	}
	if k == end {
		hi = r.End.Offset
	}
	return lo, hi, true
}

func (c comment) head() commentEntry {
	if len(c.thread) == 0 {
		return commentEntry{}
	}
	return c.thread[0]
}

func blockKeyOf(m aria.Message, block int) (blockKey, bool) {
	if block == ldrender.BlockInquiry {
		if m.InquiryLT == 0 {
			return blockKey{}, false
		}
		return blockKey{lt: m.InquiryLT}, true
	}
	i := block
	if i < 0 || i >= len(m.Nodes) {
		return blockKey{}, false
	}
	return nodeBlockKey(m.Nodes[i])
}

func nodeBlockKey(n livedoc.Node) (blockKey, bool) {
	if len(n.Src) == 0 {
		return blockKey{}, false
	}
	src := n.Src[0]
	if n.Type == livedoc.NodeTool {
		src = n.Src[len(n.Src)-1]
	}
	return blockKey{lt: src.LT, block: src.Block}, true
}

func (t *transcript) setComments(idx *commentIndex) bool {
	if idx == nil {
		idx = &commentIndex{byBlock: map[blockKey][]int{}}
	}
	if t.comments != nil && t.comments.sig == idx.sig {
		return false
	}
	t.comments = idx
	for ref := range t.entryOpen {
		if _, ok := t.commentOf(ref); !ok {
			delete(t.entryOpen, ref)
		}
	}
	t.invalidateRows()
	t.repairSelection()
	return true
}

func (idx *commentIndex) at(m aria.Message, block int) []int {
	if idx == nil || len(idx.list) == 0 {
		return nil
	}
	k, ok := blockKeyOf(m, block)
	if !ok {
		return nil
	}
	return idx.byBlock[k]
}

func entryHash(c comment, e commentEntry) uint64 {
	return nodeHash(livedoc.Node{Type: livedoc.NodeProse, Markdown: c.id + "\x00" + e.key + "\x00" + entryText(e)})
}

func entryText(e commentEntry) string {
	if e.by == "" {
		return e.text
	}
	return e.by + ": " + e.text
}

func commentText(c comment) string {
	var b strings.Builder
	for i, e := range c.thread {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(entryText(e))
	}
	return b.String()
}

func (t *transcript) blockKeyOfRef(ref nodeRef) (blockKey, bool) {
	if ref.index == inquiryNode {
		m, ok := t.messageOf(ref.turn)
		if !ok || m.InquiryLT == 0 {
			return blockKey{}, false
		}
		return blockKey{lt: m.InquiryLT}, true
	}
	n, ok := t.nodeNear(ref)
	if !ok {
		return blockKey{}, false
	}
	return nodeBlockKey(n)
}

func commentAddress(c comment, reply int) string {
	if reply > 0 {
		return "#" + c.id + "." + strconv.Itoa(reply)
	}
	return "#" + c.id
}

func (t *transcript) turnOfLT(lt uint64) int {
	turn := 0
	visit := func(m aria.Message) {
		if turn != 0 {
			return
		}
		if m.InquiryLT == lt {
			turn = m.Turn
			return
		}
		for _, n := range m.Nodes {
			for _, s := range n.Src {
				if s.LT == lt {
					turn = m.Turn
					return
				}
			}
		}
	}
	t.forEachMessage(visit)
	if open := t.openMessage(); open != nil {
		visit(*open)
	}
	return turn
}

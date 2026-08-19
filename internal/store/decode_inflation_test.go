package store

import (
	"encoding/json"
	"os"
	"runtime"
	"sort"
	"testing"

	"github.com/jack-work/figaro/internal/message"
)

// THE DECODE INFLATION, RE-MEASURED ON REAL HISTORY.
//
// irDecodeInflation = 5 sizes EVERY per-aria budget in this store: the window's
// gate and its accounting are both denominated in "encoded bytes x inflation",
// so if the factor is wrong every budget means something other than what it
// says. Its provenance is two real arias measured months ago (4.0x and 5.3x)
// and plans/tree-shaped-log.md's audit lists it as INHERITED AND NEVER
// REPRODUCED ON THIS MACHINE.
//
// SKIPPED UNLESS FIGARO_REAL_STORE POINTS AT A STORE. It reads real history, so
// it cannot be part of the suite: it is an instrument you run, and it prints
// what it found.
//
// METHOD: the keep-versus-drop heap delta, with the two corrections the fork
// seam paid for -- the writing backend closed and the store reopened so no memo
// is a second holder, the segment payload cache disabled so no raw frames are
// counted, and the object under test built in a frame that has RETURNED so a
// dead pointer in a live stack slot cannot keep it alive.
func TestDecodeInflationOnRealHistory(t *testing.T) {
	root := os.Getenv("FIGARO_REAL_STORE")
	if root == "" {
		t.Skip("set FIGARO_REAL_STORE to a store root to run this instrument")
	}

	prev := SegmentCacheBudget()
	SetSegmentCacheBudget(0)
	defer SetSegmentCacheBudget(prev)

	st, err := OpenXwalStore(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ids := st.ConversationIDs()
	if len(ids) == 0 {
		t.Skip("no arias in the store")
	}

	type row struct {
		id          string
		records     int
		encoded     int
		resident    uint64
		payload     int
		zeroEncoded int
	}
	var rows []row

	for _, id := range ids {
		enc, n := encodedBytesOf(st, id)
		if n < 200 || enc < 1<<20 {
			continue // too small to measure against GC noise
		}
		resident := residentCostOf(st, id)
		payload, zeroEnc := payloadBytesOf(st, id)
		rows = append(rows, row{id: id, records: n, encoded: enc, resident: resident,
			payload: payload, zeroEncoded: zeroEnc})
	}
	if len(rows) == 0 {
		t.Skip("no aria large enough to measure")
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].encoded > rows[j].encoded })

	t.Logf("irDecodeInflation is %d; the store's own constant", irDecodeInflation)
	var sumEnc, sumRes float64
	for i, r := range rows {
		if i >= 8 {
			break
		}
		t.Logf("  %s  %5d rec  encoded %9d  payload %9d  resident %9d  resident/encoded x%.2f  resident/payload x%.2f  (%d entries carry no encoded size)",
			r.id, r.records, r.encoded, r.payload, r.resident,
			float64(r.resident)/float64(r.encoded), float64(r.resident)/float64(r.payload), r.zeroEncoded)
		sumEnc += float64(r.encoded)
		sumRes += float64(r.resident)
	}
	t.Logf("  WEIGHTED OVER THE %d LARGEST: x%.2f", min(8, len(rows)), sumRes/sumEnc)
}

// encodedBytesOf sums the encoded size the store records per entry, and -- as
// AN INDEPENDENT SECOND INSTRUMENT -- the payload bytes those entries actually
// carry. A heap delta and a sum of string lengths fail in different directions,
// so where they agree the number is not an artifact of either.
func encodedBytesOf(st *XwalStore, id string) (bytes, n int) {
	log := newXwalLog[message.Message](st, id, chanIR, true)
	for _, e := range log.Read() {
		bytes += e.EncodedBytes
		n++
	}
	return bytes, n
}

// payloadBytesOf sums the STRING CONTENT of an aria's decoded IR: the bytes
// that must exist in the heap whatever the struct overhead is.
func payloadBytesOf(st *XwalStore, id string) (payload, zeroEncoded int) {
	log := newXwalLog[message.Message](st, id, chanIR, true)
	for _, e := range log.Read() {
		if e.EncodedBytes == 0 {
			zeroEncoded++
		}
		for _, c := range e.Payload.Content {
			payload += len(c.Text) + len(c.Data) + len(c.ToolName) + len(c.ToolCallID)
		}
	}
	return payload, zeroEncoded
}

// residentCostOf is what one aria's decoded IR window costs the heap, measured
// as what dropping it frees. The window is built inside a function that returns
// so nothing but the returned handle can reach it.
func residentCostOf(st *XwalStore, id string) uint64 {
	build := func() *cachedLog[message.Message] {
		inner := newXwalLog[message.Message](st, id, chanIR, true)
		c := newWindowedLog[message.Message](inner, 0, 0, irDecodeInflation, irEntrySize)
		_ = c.Read()
		return c
	}
	c := build()
	with := heapAfterGC()
	runtime.KeepAlive(c)
	c = nil
	without := heapAfterGC()
	if with < without {
		return 0
	}
	return with - without
}

// AND THE TRANSLATION CHANNEL, whose estimate takes the payload bytes THEMSELVES
// (transEntrySize sums len(raw)+16, inflation 1). If the IR's factor of 5 is
// wrong and this one is right, that is a sharper finding than "the estimates are
// off": it says the two channels were calibrated by different methods and only
// one of them was checked.
func TestTranslationInflationOnRealHistory(t *testing.T) {
	root := os.Getenv("FIGARO_REAL_STORE")
	if root == "" {
		t.Skip("set FIGARO_REAL_STORE to a store root to run this instrument")
	}
	provider := os.Getenv("FIGARO_REAL_PROVIDER")
	if provider == "" {
		provider = "anthropic"
	}

	prev := SegmentCacheBudget()
	SetSegmentCacheBudget(0)
	defer SetSegmentCacheBudget(prev)

	st, err := OpenXwalStore(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	type row struct {
		id       string
		records  int
		encoded  int
		estimate int
		resident uint64
	}
	var rows []row
	for _, id := range st.ConversationIDs() {
		enc, est, n := transEncodedOf(st, id, provider)
		if n < 50 || enc < 1<<20 {
			continue
		}
		rows = append(rows, row{id: id, records: n, encoded: enc, estimate: est,
			resident: transResidentOf(st, id, provider)})
	}
	if len(rows) == 0 {
		t.Skipf("no aria with a large %s translation channel", provider)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].encoded > rows[j].encoded })

	var sumEnc, sumRes, sumEst float64
	for i, r := range rows {
		if i >= 6 {
			break
		}
		t.Logf("  %s  %5d rec  encoded %9d  ESTIMATE %9d  resident %9d  res/enc x%.2f  res/EST x%.2f",
			r.id, r.records, r.encoded, r.estimate, r.resident,
			float64(r.resident)/float64(r.encoded), float64(r.resident)/float64(r.estimate))
		sumEnc += float64(r.encoded)
		sumRes += float64(r.resident)
		sumEst += float64(r.estimate)
	}
	t.Logf("  WEIGHTED: res/enc x%.2f   res/ESTIMATE x%.2f   (%s)", sumRes/sumEnc, sumRes/sumEst, provider)
}

func transEncodedOf(st *XwalStore, id, provider string) (encoded, estimate, n int) {
	log := newXwalLog[[]json.RawMessage](st, id, transChannel(provider), false)
	for _, e := range log.Read() {
		encoded += e.EncodedBytes
		estimate += transEntrySize(e)
		n++
	}
	return encoded, estimate, n
}

func transResidentOf(st *XwalStore, id, provider string) uint64 {
	build := func() *cachedLog[[]json.RawMessage] {
		inner := newXwalLog[[]json.RawMessage](st, id, transChannel(provider), false)
		c := newWindowedLog[[]json.RawMessage](inner, 0, 0, 1, transEntrySize)
		_ = c.Read()
		return c
	}
	c := build()
	with := heapAfterGC()
	runtime.KeepAlive(c)
	c = nil
	without := heapAfterGC()
	if with < without {
		return 0
	}
	return with - without
}

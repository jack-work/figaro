package xwal

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

var (
	opaquePayloadOne = []byte(`{"role":"assistant","content":[{"type":"tool_use","input":{"z":1,"a":2},"name":"lookup"}],"model":"provider"}`)
	opaquePayloadTwo = []byte(`{"role":"assistant", "content":[{"text":"done","type":"text","meta":{"z":3,"a":4}}]}`)
	opaquePayloadAlt = []byte(`{"role":"assistant","content":[{"type":"text","text":"alternate"}],"stop_reason":"end_turn"}`)
)

func TestOpaqueFrameDecodesLegacyAndOpaque(t *testing.T) {
	legacy := encodeFrame(7, opaquePayloadOne, []byte(`"meta"`), 0)
	record, err := decodeRecord(1, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(record.Payload, opaquePayloadOne) {
		t.Fatalf("legacy payload = %q", record.Payload)
	}

	opaque := encodeChannelFrame(8, opaquePayloadTwo, []byte(`"meta"`), true, 0)
	if bytes.Contains(opaque, []byte(`"role"`)) {
		t.Fatalf("opaque frame embeds raw payload: %s", opaque)
	}
	record, err = decodeRecord(2, opaque)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(record.Payload, opaquePayloadTwo) {
		t.Fatalf("opaque payload = %q, want %q", record.Payload, opaquePayloadTwo)
	}
}

func TestOpaqueChannelExactBytesAcrossCachedReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, trunk := seedTrunk(t, dir)
	spec := ChannelSpec{
		Name: "translations/provider", Kind: ChannelLog, Opaque: true,
	}
	if err := f.ensureChannel(spec); err != nil {
		t.Fatal(err)
	}
	_, mainLT, err := f.Append(trunk, 0, []byte(`"turn"`), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range [][]byte{opaquePayloadOne, opaquePayloadTwo} {
		if _, err := f.AppendChannel(trunk, spec.Name, mainLT, payload, nil); err != nil {
			t.Fatal(err)
		}
	}

	assertOpaqueChannelRecords(t, f, trunk, spec.Name, mainLT,
		[][]byte{opaquePayloadOne, opaquePayloadTwo})

	head, err := f.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	head.Close()
	head, err = f.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	record, err := head.ReadAt(spec.Name, 2)
	head.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(record.Payload, opaquePayloadTwo) {
		t.Fatalf("cached reopen payload = %q", record.Payload)
	}

	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openTrunks(dir, withChannelSpec(trunksCfg(), spec))
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, reopened)
	assertOpaqueChannelRecords(t, reopened, trunk, spec.Name, mainLT,
		[][]byte{opaquePayloadOne, opaquePayloadTwo})

	data, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		t.Fatal(err)
	}
	var man manifest
	if err := json.Unmarshal(data, &man); err != nil {
		t.Fatal(err)
	}
	for _, channel := range man.Channels {
		if channel.Name == spec.Name {
			if !channel.Opaque {
				t.Fatalf("manifest channel is not opaque: %+v", channel)
			}
			return
		}
	}
	t.Fatalf("manifest missing channel %q", spec.Name)
}

func TestOpaqueChannelExactBytesAcrossForks(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, trunk := seedTrunk(t, dir)
	const channel = "translations/provider"
	if err := f.ensureChannel(ChannelSpec{Name: channel, Kind: ChannelLog, Opaque: true}); err != nil {
		t.Fatal(err)
	}
	_, firstMainLT, err := f.Append(trunk, 0, []byte(`"turn-1"`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.AppendChannel(trunk, channel, firstMainLT, opaquePayloadOne, nil); err != nil {
		t.Fatal(err)
	}
	_, secondMainLT, err := f.Append(trunk, 0, []byte(`"turn-2"`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.AppendChannel(trunk, channel, secondMainLT, opaquePayloadTwo, nil); err != nil {
		t.Fatal(err)
	}

	tailSibling, err := f.ForkTail(trunk)
	if err != nil {
		t.Fatal(err)
	}
	assertOpaqueLatest(t, f, trunk, channel, secondMainLT, opaquePayloadTwo)
	assertOpaqueLatest(t, f, tailSibling, channel, secondMainLT, opaquePayloadTwo)

	interiorSibling, err := f.ForkAt(trunk, firstMainLT)
	if err != nil {
		t.Fatal(err)
	}
	assertOpaqueLatest(t, f, interiorSibling, channel, firstMainLT, opaquePayloadOne)
	assertOpaqueLatest(t, f, trunk, channel, secondMainLT, opaquePayloadTwo)
	assertOpaqueLatest(t, f, tailSibling, channel, secondMainLT, opaquePayloadTwo)

	if _, err := f.AppendChannel(interiorSibling, channel, firstMainLT, opaquePayloadAlt, nil); err != nil {
		t.Fatal(err)
	}
	assertOpaqueChannelRecords(t, f, interiorSibling, channel, firstMainLT,
		[][]byte{opaquePayloadOne, opaquePayloadAlt})
	assertOpaqueLatest(t, f, trunk, channel, secondMainLT, opaquePayloadTwo)
	assertOpaqueLatest(t, f, tailSibling, channel, secondMainLT, opaquePayloadTwo)
}

func TestEnsureChannelRejectsOpaqueChange(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, _ := seedTrunk(t, dir)
	before, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		t.Fatal(err)
	}
	err = f.ensureChannel(ChannelSpec{Name: "translations", Kind: ChannelLog, Opaque: true})
	if err == nil {
		t.Fatal("EnsureChannel changed an existing channel to opaque")
	}
	after, readErr := os.ReadFile(filepath.Join(dir, manifestName))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("EnsureChannel rewrote the manifest after an opaque mismatch")
	}
}

func assertOpaqueChannelRecords(
	t *testing.T,
	f *Trunks,
	trunk, channel string,
	minMainLT uint64,
	want [][]byte,
) {
	t.Helper()
	head, err := f.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	records, err := head.RecordsFrom(channel, minMainLT, 0)
	head.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != len(want) {
		t.Fatalf("records = %+v, want %d", records, len(want))
	}
	for i := range want {
		if !bytes.Equal(records[i].Payload, want[i]) {
			t.Fatalf("records[%d] = %q, want %q", i, records[i].Payload, want[i])
		}
	}
	assertOpaqueLatest(t, f, trunk, channel, minMainLT, want[len(want)-1])
}

func assertOpaqueLatest(
	t *testing.T,
	f *Trunks,
	trunk, channel string,
	minMainLT uint64,
	want []byte,
) {
	t.Helper()
	record, ok, err := f.LatestChannelRecord(trunk, channel, minMainLT)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("no latest record for %s.%s at %d", trunk, channel, minMainLT)
	}
	if !bytes.Equal(record.Payload, want) {
		t.Fatalf("latest = %q, want %q", record.Payload, want)
	}
}

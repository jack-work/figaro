package xwal

import (
	"encoding/json"
	"testing"
)

// jsonMerge is a chalkboard-style reducer: patches are {"set":{...},
// "remove":[...]} applied to a flat JSON object.
func jsonMerge(state, patch []byte) ([]byte, error) {
	m := map[string]json.RawMessage{}
	if len(state) > 0 {
		if err := json.Unmarshal(state, &m); err != nil {
			return nil, err
		}
	}
	var p struct {
		Set    map[string]json.RawMessage `json:"set"`
		Remove []string                   `json:"remove"`
	}
	if err := json.Unmarshal(patch, &p); err != nil {
		return nil, err
	}
	for k, v := range p.Set {
		m[k] = v
	}
	for _, k := range p.Remove {
		delete(m, k)
	}
	return json.Marshal(m)
}

func triune(t *testing.T, dir string) (*XWAL, Config) {
	t.Helper()
	cfg := Config{
		Main:        "ir",
		Registry:    map[string]Reducer{"jsonmerge": {Reduce: jsonMerge, Initial: []byte("{}")}},
		SegmentSize: 256, // small, to force watermark rotation across entries
		Channels: []ChannelSpec{
			{Name: "ir", Kind: ChannelLog},
			{Name: "translations", Kind: ChannelLog},
			{Name: "chalkboard", Kind: ChannelReducible, Reducer: "jsonmerge"},
		},
	}
	x, err := Open(dir, cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return x, cfg
}

func TestXWAL_ReopenManifest(t *testing.T) {
	dir := t.TempDir()
	x, _ := triune(t, dir)
	if _, err := x.AppendMain([]byte("a"), nil); err != nil {
		t.Fatal(err)
	}
	x.Close()

	// Reopen with only the registry (no channel specs): manifest drives it.
	x2, err := Open(dir, Config{Registry: map[string]Reducer{"jsonmerge": {Reduce: jsonMerge, Initial: []byte("{}")}}})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer x2.Close()
	if x2.Main() != "ir" {
		t.Fatalf("main = %q, want ir", x2.Main())
	}
	chans := x2.Channels()
	if len(chans) != 3 {
		t.Fatalf("channels = %d, want 3", len(chans))
	}
	var cb *ChannelInfo
	for i := range chans {
		if chans[i].Name == "chalkboard" {
			cb = &chans[i]
		}
	}
	if cb == nil || cb.Kind != ChannelReducible || cb.Reducer != "jsonmerge" {
		t.Fatalf("chalkboard not restored as reducible: %+v", cb)
	}
}

func field(t *testing.T, obj []byte, key string) string {
	t.Helper()
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal(obj, &m); err != nil {
		t.Fatalf("unmarshal state %s: %v", obj, err)
	}
	return string(m[key])
}

func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

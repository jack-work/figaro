package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

type fakeFormDoor map[string]map[string]json.RawMessage

func (d fakeFormDoor) ListGlobal(context.Context) (*rpc.ListResponse, error) {
	out := &rpc.ListResponse{Figaros: []rpc.FigaroInfoResponse{{ID: "null", State: "anchor"}}}
	for id := range d {
		out.Figaros = append(out.Figaros, rpc.FigaroInfoResponse{ID: id})
	}
	return out, nil
}

func (d fakeFormDoor) Form(_ context.Context, id string) (*rpc.FormResponse, error) {
	return &rpc.FormResponse{Snapshot: form.FromMap(d[id])}, nil
}

func TestTheShapeAuditCountsFaultsPerKeyAcrossForms(t *testing.T) {
	door := fakeFormDoor{
		"a1": {"system.max_tokens": json.RawMessage(`0`), "system.ttl": json.RawMessage(`"2d"`)},
		"a2": {"system.max_tokens": json.RawMessage(`-3`), "system.ttl": json.RawMessage(`"soon"`)},
		"@f": {"system.max_tokens": json.RawMessage(`8192`)},
	}
	rep, err := auditFormShapes(context.Background(), []formReader{door, door, door})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Forms != 3 {
		t.Fatalf("read %d forms, want 3 (the anchor is skipped, the unbound form is not)", rep.Forms)
	}
	counts := map[string]int{}
	for _, f := range rep.Faults {
		counts[f.Path] = f.Count
	}
	if counts["system.max_tokens"] != 2 || counts["system.ttl"] != 1 || len(counts) != 2 {
		t.Fatalf("faults %v", counts)
	}
	var out bytes.Buffer
	if err := printShapeAudit(&out, rep, false); err != nil || !strings.Contains(out.String(), "3 forms read") {
		t.Fatalf("%v\n%s", err, out.String())
	}
}

func TestFormKeysListsEveryRegistryRow(t *testing.T) {
	var out bytes.Buffer
	if err := printFormKeys(&out, true); err != nil {
		t.Fatal(err)
	}
	var rows []formKeyRow
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(form.Registry()) {
		t.Fatalf("%d rows, registry has %d", len(rows), len(form.Registry()))
	}
	for _, r := range rows {
		if r.Shape == "" || r.Writer == "" {
			t.Errorf("%+v", r)
		}
	}
}

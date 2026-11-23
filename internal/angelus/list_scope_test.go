package angelus_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

const listerPolicy = doorPolicy + `
[roles.lister]
permissions = [LISTER]

[[bind]]
role = "lister"
principals = ["user:lister"]
`

type listRig struct {
	grantRig
	lister      context.Context
	scratch     string
	other, deep string
}

func newListRig(t *testing.T, grants string) listRig {
	t.Helper()
	r := listRig{grantRig: newRigWith(t, strings.Replace(listerPolicy, "LISTER", grants, 1))}
	r.lister = as("user", "lister")
	mint := func(mantra string) string {
		c, err := r.create(asOwner(), "mock")
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(mantra)
		p := form.Build(form.Snapshot{}, map[string]json.RawMessage{"mantra": raw}, nil)
		if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, c.FigaroID, rpc.SetRequest{Patch: p})); err != nil {
			t.Fatal(err)
		}
		return c.FigaroID
	}
	r.scratch = mint("scratch work")
	r.other = mint("PRIVATE-MANTRA")
	r.deep = ariasFork(t, r.grantRig, r.other)
	return r
}

func (r listRig) rows(t *testing.T, ctx context.Context) map[string]rpc.FigaroInfoResponse {
	t.Helper()
	out, err := r.call(ctx, rpc.MethodList, rpc.ListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]rpc.FigaroInfoResponse{}
	for _, f := range out.(rpc.ListResponse).Figaros {
		rows[f.ID] = f
	}
	return rows
}

func (r listRig) refused(t *testing.T, ctx context.Context, id string) {
	t.Helper()
	for method, req := range map[string]any{
		rpc.MethodRead:   rpc.ReadRequest{},
		rpc.MethodForm:   rpc.FormRequest{},
		rpc.MethodFollow: rpc.FollowRequest{},
		rpc.MethodQueued: rpc.QueuedRequest{},
		rpc.MethodQua:    rpc.QuaRequest{},
	} {
		_, err := r.h[method](ctx, withAria(t, id, req))
		if err == nil {
			t.Errorf("%s on %s answered for a caller with no read", method, id)
		} else if !strings.Contains(err.Error(), "not found") {
			t.Errorf("%s on %s = %v, want not found", method, id, err)
		}
	}
}

func (r listRig) listedWithoutDetail(t *testing.T, row rpc.FigaroInfoResponse) {
	t.Helper()
	if row.Cwd != "" || row.Provider != "" || row.Model != "" || row.ContextTokens != 0 || row.MessageCount != 0 || len(row.BoundPIDs) != 0 || row.OutfitName != "" {
		t.Errorf("a listed row carries more than its listing: %+v", row)
	}
}

// Defends the listing grant: list:* shows every row of figaro.list, @arias,
// @recent and @topology with its mantra, and opens no transcript, form,
// follow, queue or send on any of them.
func TestAListingGrantShowsEveryRowAndReadsNone(t *testing.T) {
	r := newListRig(t, `"list:*"`)
	rows := r.rows(t, r.lister)
	for _, id := range []string{r.scratch, r.other, r.deep} {
		row, ok := rows[id]
		if !ok {
			t.Fatalf("figaro.list lacks %s", id)
		}
		r.listedWithoutDetail(t, row)
		if row.State == "" {
			t.Errorf("%s listed with no state", id)
		}
	}
	if rows[r.other].Mantra != "PRIVATE-MANTRA" {
		t.Errorf("mantra = %q", rows[r.other].Mantra)
	}
	if rows[r.deep].Parent != r.other {
		t.Errorf("a listed fork loses its parent: %+v", rows[r.deep])
	}
	if _, err := readArias(t, r.grantRig, r.lister, rpc.TopologyFormID); err != nil {
		t.Errorf("@topology: %v", err)
	}
	if err := setTopology(t, r.grantRig, r.lister, map[string]string{r.deep: r.scratch}); err == nil {
		t.Error("a listing grant rearranged the tree")
	}
	for _, formID := range []string{rpc.AriasFormID, rpc.RecentFormID} {
		snap, err := readArias(t, r.grantRig, r.lister, formID)
		if err != nil {
			t.Fatalf("%s: %v", formID, err)
		}
		var seen strings.Builder
		for k, v := range snap.All() {
			seen.WriteString(k + "=" + string(v) + "\n")
		}
		ids := seen.String()
		for _, id := range []string{r.scratch, r.other, r.deep} {
			if !strings.Contains(ids, id) {
				t.Errorf("%s lacks %s: %s", formID, id, ids)
			}
		}
	}
	for _, id := range []string{r.scratch, r.other, r.deep} {
		r.refused(t, r.lister, id)
	}
}

// Defends composition: list:* plus read on one lineage lists everything and
// reads that lineage only.
func TestAListingGrantWithALineageGrantReadsOnlyTheLineage(t *testing.T) {
	r := newListRig(t, `"list:*"`)
	rewriteLister(t, r.grantRig, `"list:*", "read:lineage:`+r.other+`"`)
	rows := r.rows(t, r.lister)
	if len(rows) < 3 {
		t.Fatalf("rows = %d, want every aria", len(rows))
	}
	r.listedWithoutDetail(t, rows[r.scratch])
	if rows[r.other].Provider == "" || rows[r.deep].Provider == "" {
		t.Errorf("the lineage read is listed bare: %+v %+v", rows[r.other], rows[r.deep])
	}
	if _, err := r.h[rpc.MethodRead](r.lister, withAria(t, r.deep, rpc.ReadRequest{})); err != nil {
		t.Errorf("read inside the lineage: %v", err)
	}
	if _, err := readArias(t, r.grantRig, r.lister, r.other); err != nil {
		t.Errorf("form inside the lineage: %v", err)
	}
	r.refused(t, r.lister, r.scratch)
}

// Defends the old shape: read on one lineage alone still lists that lineage
// and nothing else, so listing is opt-in.
func TestALineageGrantAloneStillListsOnlyTheLineage(t *testing.T) {
	r := newListRig(t, `"read:aria:x"`)
	rewriteLister(t, r.grantRig, `"read:lineage:`+r.other+`"`)
	rows := r.rows(t, r.lister)
	if _, ok := rows[r.scratch]; ok || len(rows) != 2 {
		t.Errorf("rows = %v, want the lineage only", rows)
	}
}

// Defends the scoped form: list:lineage:X lists X's lineage and no more.
func TestAListingGrantCanBeScopedToALineage(t *testing.T) {
	r := newListRig(t, `"list:aria:x"`)
	rewriteLister(t, r.grantRig, `"list:lineage:`+r.other+`"`)
	rows := r.rows(t, r.lister)
	if _, ok := rows[r.scratch]; ok || len(rows) != 2 {
		t.Errorf("rows = %v, want the lineage only", rows)
	}
	r.refused(t, r.lister, r.deep)
}

func rewriteLister(t *testing.T, r grantRig, grants string) {
	t.Helper()
	text := strings.Replace(listerPolicy, "LISTER", grants, 1)
	lendPolicy(t, r, text)
}

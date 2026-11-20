package store

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

func routeIn(t *testing.T, be *XwalBackend, id string) string {
	t.Helper()
	snap, err := be.FormState(id)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := snap.Get(form.RouteKey)
	return string(raw)
}

func TestABornFigaroCarriesItsRoute(t *testing.T) {
	be, _, parent := newStampFixture(t)
	defer be.Close()
	if got := routeIn(t, be, parent); got != `["mock"]` {
		t.Fatalf("parent route = %s", got)
	}
	child, _, err := be.ForkWith(parent, 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.provider": json.RawMessage(`"rou2"`), "system.model": json.RawMessage(`"explabs/glm-5.3"`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := routeIn(t, be, child); got != `["rou2","explabs","glm-5.3"]` {
		t.Fatalf("child route = %s", got)
	}
	inherits, _, err := be.ForkWith(child, 0, stampBirthPatch("/x"))
	if err != nil {
		t.Fatal(err)
	}
	if got := routeIn(t, be, inherits); got != `["rou2","explabs","glm-5.3"]` {
		t.Fatalf("inherited route = %s", got)
	}
}

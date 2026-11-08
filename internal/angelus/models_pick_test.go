package angelus_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/modelcat"
	"github.com/jack-work/figaro/internal/provider"
)

type listing struct{ list []provider.ModelInfo }

func (p listing) Name() string        { return "listing" }
func (p listing) Fingerprint() string { return "listing/v1" }
func (p listing) SetModel(string)     {}
func (p listing) Send(context.Context, provider.SendInput, provider.Bus) error {
	return errors.New("not a sender")
}
func (p listing) Models(context.Context) ([]provider.ModelInfo, error) { return p.list, nil }

func withModels(t *testing.T, d doorRig) {
	t.Helper()
	cat := modelcat.New(modelcat.Config{Build: func(string) (provider.Provider, error) {
		return listing{list: []provider.ModelInfo{{ID: "m-2.5", Entry: models.Entry{Context: models.Context{Default: 200_000}}}}}, nil
	}})
	d.a.Models = cat
	cat.Want("mock", "m-2.5")
	deadline := time.Now().Add(2 * time.Second)
	for cat.Limit("mock", "m-2.5") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the catalog never listed mock/m-2.5")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestTheOwnerPicksAListedModel(t *testing.T) {
	d := boxDoorRig(t, "true")
	withModels(t, d)
	made, err := d.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.call(asOwner(), rpc.MethodModelsPick, rpc.ModelsPickRequest{FigaroID: made.FigaroID, Provider: "mock", Model: "m-2.5"}); err != nil {
		t.Fatal(err)
	}
	if v, _ := keyOn(t, d.grantRig, made.FigaroID, models.ModelKey); string(v) != `"m-2.5"` {
		t.Errorf("system.model = %s, want the pick", v)
	}
	for _, bad := range []rpc.ModelsPickRequest{
		{FigaroID: made.FigaroID, Provider: "mock", Model: "not-listed"},
		{FigaroID: made.FigaroID, Provider: "nowhere", Model: "m-2.5"},
		{FigaroID: "../x", Provider: "mock", Model: "m-2.5"},
	} {
		if _, err := d.call(asOwner(), rpc.MethodModelsPick, bad); err == nil {
			t.Errorf("pick %+v was accepted", bad)
		}
	}
}

// Picking sets owner-held keys, so a box cannot pick for its own aria, by the
// view's action or by the method itself.
func TestABoxCannotPickAModel(t *testing.T) {
	d := boxDoorRig(t, "true")
	withModels(t, d)
	inBox, err := d.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	err = d.dial(t).Call(context.Background(), rpc.MethodModelsPick, rpc.ModelsPickRequest{FigaroID: inBox.FigaroID, Provider: "mock", Model: "m-2.5"}, nil)
	if err == nil || !strings.Contains(err.Error(), "owner-held") {
		t.Errorf("a box picked a model: %v", err)
	}
	if v, _ := keyOn(t, d.grantRig, inBox.FigaroID, models.ModelKey); string(v) == `"m-2.5"` {
		t.Error("the box's pick landed")
	}
}

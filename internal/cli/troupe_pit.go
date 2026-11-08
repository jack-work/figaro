package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/sdk"
)

func (in *interactiveInput) troupeAtRoot(v *formView) { v.opener = in.mountForm }

func (in *interactiveInput) troupeAtAttended(v *formView) {
	v.opener = in.mountForm
	v.seeks = true
}

func (in *interactiveInput) mountForm(addr string) (*formView, actOn, func(), <-chan struct{}, error) {
	node, _ := rpc.SplitNode(addr)
	ctx, cancel := context.WithCancel(context.Background())
	if node == "" {
		view, closeView, done, err := openFormViewOn(ctx, cancel, ariaAddr(addr), addr, "", in.renderLocked, func() {})
		return view, in.actOnDaemon, closeView, done, err
	}
	ep, err := in.troupeDoor()
	if err != nil {
		cancel()
		return nil, nil, nil, nil, err
	}
	view, closeView, done, err := openFormViewOn(ctx, cancel, sdk.Addr{Door: ep, ID: addr}, addr, "", in.renderLocked, func() {})
	send := func(ctx context.Context, method string, params json.RawMessage, target string) error {
		acli, err := sdk.DialAngelus(ep)
		if err != nil {
			return err
		}
		defer acli.Close()
		_, err = acli.ActOn(ctx, method, params, target)
		return err
	}
	return view, send, closeView, done, err
}

var troupeBearer = func(tr boot.Troupe) transport.Bearer {
	if r := boot.TroupeResolver(tr); r != nil {
		return r
	}
	return nil
}

func (in *interactiveInput) troupeDoor() (transport.Endpoint, error) {
	name := ""
	if in.loaded != nil {
		name = in.loaded.Config.Troupe.Name
	}
	if name == "" {
		return transport.Endpoint{}, fmt.Errorf("this node names no troupe ([troupe] name)")
	}
	tr, err := boot.LoadTroupe(in.loaded, name)
	if err != nil || !tr.Remote() {
		return transport.Endpoint{}, fmt.Errorf("no door for clients in troupes/%s.toml: figaro troupe login %s", name, name)
	}
	bearer := troupeBearer(tr)
	if bearer == nil {
		return transport.Endpoint{}, fmt.Errorf("figaro troupe login %s", name)
	}
	if _, err := bearer.Resolve(); err != nil {
		return transport.Endpoint{}, fmt.Errorf("figaro troupe login %s", name)
	}
	return transport.WSSEndpoint(tr.URL, bearer), nil
}

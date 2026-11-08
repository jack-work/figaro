package cli

import (
	"context"
	"encoding/json"

	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/sdk"
)

func (in *interactiveInput) troupeAtRoot(v *formView) { v.opener = in.mountForm }

func (in *interactiveInput) troupeAtAttended(v *formView) { seekAttended(v, in.mountForm) }

func seekAttended(v *formView, open mountOpener) {
	v.opener = open
	v.seeks = true
	v.loadMount(v.revealMarked())
}

func (in *interactiveInput) mountForm(addr string) (*formView, actOn, func(), <-chan struct{}, error) {
	a, err := doorFor(addr)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	view, closeView, done, err := openFormViewOn(ctx, cancel, a, addr, "", in.renderLocked, func() {})
	if foreignNode(addr) == "" {
		return view, in.actOnDaemon, closeView, done, err
	}
	send := func(ctx context.Context, method string, params json.RawMessage, target string) error {
		acli, err := sdk.DialAngelus(a.Door)
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

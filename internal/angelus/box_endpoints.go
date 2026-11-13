package angelus

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"sort"
	"time"

	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
)

type boxEndpointSpec struct {
	box      string
	field    string
	path     string
	endpoint authz.Endpoint
}

func (s boxEndpointSpec) key() string { return s.box + "." + s.field }

type openBoxEndpoint struct {
	spec boxEndpointSpec
	shut context.CancelCauseFunc
	done chan struct{}
}

type boxEndpoints struct {
	ctx  context.Context
	open map[string]*openBoxEndpoint
}

type EndpointShut struct {
	Reason string
	grace  time.Duration
}

func (e *EndpointShut) Error() string { return e.Reason }

func (e *EndpointShut) noticeGrace() time.Duration {
	if e.grace > 0 {
		return e.grace
	}
	return lapseNoticeGrace
}

func wantedBoxEndpoints(loaded *config.Loaded) (map[string]boxEndpointSpec, map[string]error) {
	want := map[string]boxEndpointSpec{}
	refused := map[string]error{}
	endpoints, hosts := loaded.BoxEndpoints(), loaded.HostEndpoints()
	for _, name := range loaded.BoxesWithEndpoints() {
		if err := loaded.BoxEndpointWithoutLists(name); err != nil {
			refused[name] = err
			continue
		}
		d := boxEndpointSpec{box: name, field: "door", path: endpoints[name], endpoint: authz.EndpointBox}
		want[d.key()] = d
		if host, ok := hosts[name]; ok {
			h := boxEndpointSpec{box: name, field: "host_door", path: host, endpoint: authz.EndpointHost}
			want[h.key()] = h
		}
	}
	return want, refused
}

func (a *Angelus) OpenBoxEndpoints(ctx context.Context, loaded *config.Loaded) error {
	a.boxEndpointsMu.Lock()
	defer a.boxEndpointsMu.Unlock()
	if a.boxEndpoints == nil {
		endpoints, shut := context.WithCancelCause(context.WithoutCancel(ctx))
		context.AfterFunc(ctx, func() { shut(&EndpointShut{Reason: stoppingReason, grace: stopNoticeGrace}) })
		a.boxEndpoints = &boxEndpoints{ctx: endpoints, open: map[string]*openBoxEndpoint{}}
	}
	return a.boxEndpoints.apply(a, loaded)
}

func (a *Angelus) ReloadBoxEndpoints(loaded *config.Loaded) error {
	a.boxEndpointsMu.Lock()
	defer a.boxEndpointsMu.Unlock()
	if a.boxEndpoints == nil {
		return nil
	}
	return a.boxEndpoints.apply(a, loaded)
}

func (a *Angelus) checkBoxEndpointPath(loaded *config.Loaded, s boxEndpointSpec) error {
	if s.path == filepath.Clean(a.SocketPath) {
		return fmt.Errorf("sandbox.boxes.%s.%s: %s is the CLI door", s.box, s.field, s.path)
	}
	if web, ok, _ := loaded.WebEndpoint(); ok && filepath.Clean(web.Path) == s.path {
		return fmt.Errorf("sandbox.boxes.%s.%s: %s is the web door", s.box, s.field, s.path)
	}
	return nil
}

func (d *boxEndpoints) apply(a *Angelus, loaded *config.Loaded) error {
	want, refused := wantedBoxEndpoints(loaded)
	for _, s := range sortedSpecs(want) {
		if err := a.checkBoxEndpointPath(loaded, s); err != nil {
			return err
		}
	}

	held := map[string]string{}
	for k, o := range d.open {
		held[o.spec.path] = k
	}
	var closing []*openBoxEndpoint
	closeReason := map[string]string{}
	for _, k := range sortedOpenKeys(d.open) {
		o := d.open[k]
		w, ok := want[k]
		switch {
		case ok && w == o.spec:
			continue
		case ok:
			closeReason[k] = fmt.Sprintf("box %s's %s moved to %s", o.spec.box, o.spec.field, w.path)
		case refused[o.spec.box] != nil:
			closeReason[k] = refused[o.spec.box].Error()
		default:
			closeReason[k] = fmt.Sprintf("box %s's %s left the config", o.spec.box, o.spec.field)
		}
		closing = append(closing, o)
	}
	var early, late []boxEndpointSpec
	for _, s := range sortedSpecs(want) {
		if o, ok := d.open[s.key()]; ok && o.spec == s {
			continue
		}
		if _, taken := held[s.path]; taken {
			late = append(late, s)
		} else {
			early = append(early, s)
		}
	}
	for name, err := range refused {
		if _, was := d.open[name+".door"]; !was {
			slog.Error("box door refused", "box", name, "err", err)
		}
	}

	opened, err := listenBoxEndpoints(early)
	if err != nil {
		return err
	}
	for _, o := range closing {
		d.shut(o, closeReason[o.spec.key()])
	}
	more, err := listenBoxEndpoints(late)
	if err != nil {
		closeListeners(opened)
		var restored []string
		for _, o := range closing {
			back, rerr := listenBoxEndpoints([]boxEndpointSpec{o.spec})
			if rerr != nil {
				slog.Error("box door not restored after a refused reload", "box", o.spec.box, "field", o.spec.field, "path", o.spec.path, "err", rerr)
				continue
			}
			d.serve(a, loaded, back[0], "restored")
			restored = append(restored, o.spec.key())
		}
		return fmt.Errorf("%w; the doors it closed reopened: %v", err, restored)
	}
	for _, l := range append(opened, more...) {
		verb := "opened"
		if _, was := closeReason[l.spec.key()]; was {
			verb = "reopened"
		}
		d.serve(a, loaded, l, verb)
	}
	return nil
}

var listenBoxEndpoint = ListenUnix

type boxListener struct {
	spec boxEndpointSpec
	ln   net.Listener
}

func listenBoxEndpoints(specs []boxEndpointSpec) ([]boxListener, error) {
	var out []boxListener
	for _, s := range specs {
		ln, err := listenBoxEndpoint(s.path, 0o660)
		if err != nil {
			closeListeners(out)
			return nil, fmt.Errorf("sandbox.boxes.%s.%s: %w", s.box, s.field, err)
		}
		out = append(out, boxListener{spec: s, ln: ln})
	}
	return out, nil
}

func closeListeners(ls []boxListener) {
	for _, l := range ls {
		l.ln.Close()
	}
}

func (d *boxEndpoints) serve(a *Angelus, loaded *config.Loaded, l boxListener, verb string) {
	s := l.spec
	if b, _ := loaded.Box(s.box); s.endpoint == authz.EndpointBox && (b.AnyOutfit() || b.AnyProvider()) {
		slog.Info("box door opens with an open list", "box", s.box, "any_outfit", b.AnyOutfit(), "any_provider", b.AnyProvider())
	}
	ctx, shut := context.WithCancelCause(d.ctx)
	o := &openBoxEndpoint{spec: s, shut: shut, done: make(chan struct{})}
	d.open[s.key()] = o
	slog.Info("box door "+verb, "box", s.box, "door", s.endpoint.String(), "path", s.path, "principal", authz.BoxPrincipal(s.box).String())
	go func() {
		defer close(o.done)
		if err := a.Serve(ctx, l.ln, s.endpoint, authz.BoxAdmit(s.box)); err != nil {
			slog.Error("box door", "box", s.box, "door", s.endpoint.String(), "err", err)
		}
	}()
}

func (d *boxEndpoints) shut(o *openBoxEndpoint, reason string) {
	o.shut(&EndpointShut{Reason: reason})
	<-o.done
	delete(d.open, o.spec.key())
	slog.Info("box door closed", "box", o.spec.box, "door", o.spec.endpoint.String(), "path", o.spec.path, "reason", reason)
}

func endpointShut(ctx context.Context) *EndpointShut {
	var s *EndpointShut
	if errors.As(context.Cause(ctx), &s) {
		return s
	}
	return nil
}

func sortedSpecs(m map[string]boxEndpointSpec) []boxEndpointSpec {
	out := make([]boxEndpointSpec, 0, len(m))
	for _, s := range m {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key() < out[j].key() })
	return out
}

func sortedOpenKeys(m map[string]*openBoxEndpoint) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

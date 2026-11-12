package angelus

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"sort"

	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
)

type boxDoorSpec struct {
	box   string
	field string
	path  string
	door  authz.Door
}

func (s boxDoorSpec) key() string { return s.box + "." + s.field }

type openBoxDoor struct {
	spec boxDoorSpec
	shut context.CancelCauseFunc
	done chan struct{}
}

type boxDoors struct {
	ctx  context.Context
	open map[string]*openBoxDoor
}

type DoorShut struct{ Reason string }

func (e *DoorShut) Error() string { return e.Reason }

func wantedBoxDoors(loaded *config.Loaded) (map[string]boxDoorSpec, map[string]error) {
	want := map[string]boxDoorSpec{}
	refused := map[string]error{}
	doors, hosts := loaded.BoxDoors(), loaded.HostDoors()
	for _, name := range loaded.DooredBoxes() {
		if err := loaded.BoxDoorWithoutLists(name); err != nil {
			refused[name] = err
			continue
		}
		d := boxDoorSpec{box: name, field: "door", path: doors[name], door: authz.DoorBox}
		want[d.key()] = d
		if host, ok := hosts[name]; ok {
			h := boxDoorSpec{box: name, field: "host_door", path: host, door: authz.DoorHost}
			want[h.key()] = h
		}
	}
	return want, refused
}

func (a *Angelus) OpenBoxDoors(ctx context.Context, loaded *config.Loaded) error {
	a.boxDoorsMu.Lock()
	defer a.boxDoorsMu.Unlock()
	if a.boxDoors == nil {
		a.boxDoors = &boxDoors{ctx: ctx, open: map[string]*openBoxDoor{}}
	}
	return a.boxDoors.apply(a, loaded)
}

func (a *Angelus) ReloadBoxDoors(loaded *config.Loaded) error {
	a.boxDoorsMu.Lock()
	defer a.boxDoorsMu.Unlock()
	if a.boxDoors == nil {
		return nil
	}
	return a.boxDoors.apply(a, loaded)
}

func (a *Angelus) checkBoxDoorPath(loaded *config.Loaded, s boxDoorSpec) error {
	if s.path == filepath.Clean(a.SocketPath) {
		return fmt.Errorf("sandbox.boxes.%s.%s: %s is the CLI door", s.box, s.field, s.path)
	}
	if web, ok, _ := loaded.WebDoor(); ok && filepath.Clean(web.Path) == s.path {
		return fmt.Errorf("sandbox.boxes.%s.%s: %s is the web door", s.box, s.field, s.path)
	}
	return nil
}

func (d *boxDoors) apply(a *Angelus, loaded *config.Loaded) error {
	want, refused := wantedBoxDoors(loaded)
	for _, s := range sortedSpecs(want) {
		if err := a.checkBoxDoorPath(loaded, s); err != nil {
			return err
		}
	}

	held := map[string]string{}
	for k, o := range d.open {
		held[o.spec.path] = k
	}
	var closing []*openBoxDoor
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
	var early, late []boxDoorSpec
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

	opened, err := listenBoxDoors(early)
	if err != nil {
		return err
	}
	for _, o := range closing {
		d.shut(o, closeReason[o.spec.key()])
	}
	more, err := listenBoxDoors(late)
	if err != nil {
		closeListeners(opened)
		var restored []string
		for _, o := range closing {
			back, rerr := listenBoxDoors([]boxDoorSpec{o.spec})
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

var listenBoxDoor = ListenUnix

type boxListener struct {
	spec boxDoorSpec
	ln   net.Listener
}

func listenBoxDoors(specs []boxDoorSpec) ([]boxListener, error) {
	var out []boxListener
	for _, s := range specs {
		ln, err := listenBoxDoor(s.path, 0o660)
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

func (d *boxDoors) serve(a *Angelus, loaded *config.Loaded, l boxListener, verb string) {
	s := l.spec
	if b, _ := loaded.Box(s.box); s.door == authz.DoorBox && (b.AnyOutfit() || b.AnyProvider()) {
		slog.Info("box door opens with an open list", "box", s.box, "any_outfit", b.AnyOutfit(), "any_provider", b.AnyProvider())
	}
	ctx, shut := context.WithCancelCause(d.ctx)
	o := &openBoxDoor{spec: s, shut: shut, done: make(chan struct{})}
	d.open[s.key()] = o
	slog.Info("box door "+verb, "box", s.box, "door", s.door.String(), "path", s.path, "principal", authz.BoxPrincipal(s.box).String())
	go func() {
		defer close(o.done)
		if err := a.Serve(ctx, l.ln, s.door, authz.BoxAdmit(s.box)); err != nil {
			slog.Error("box door", "box", s.box, "door", s.door.String(), "err", err)
		}
	}()
}

func (d *boxDoors) shut(o *openBoxDoor, reason string) {
	o.shut(&DoorShut{Reason: reason})
	<-o.done
	delete(d.open, o.spec.key())
	slog.Info("box door closed", "box", o.spec.box, "door", o.spec.door.String(), "path", o.spec.path, "reason", reason)
}

func doorShutReason(ctx context.Context) (string, bool) {
	var s *DoorShut
	if errors.As(context.Cause(ctx), &s) {
		return s.Reason, true
	}
	return "", false
}

func sortedSpecs(m map[string]boxDoorSpec) []boxDoorSpec {
	out := make([]boxDoorSpec, 0, len(m))
	for _, s := range m {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key() < out[j].key() })
	return out
}

func sortedOpenKeys(m map[string]*openBoxDoor) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/sdk"
)

type rerouteTarget struct {
	holder  string
	role    string
	outfits string
	pinned  map[string]json.RawMessage
}

func runReroute(loaded *config.Loaded, id, spec string) error {
	t, err := rerouteTargetOf(loaded, id)
	if err != nil {
		return err
	}
	if spec != "" {
		t.outfits, t.pinned = spec, nil
	}
	if t.outfits == "" {
		return fmt.Errorf("reroute: %s names no outfit to read a route from: pass -O <outfit>", ariaName(t.role))
	}
	names, err := parseNames(t.outfits)
	if err != nil {
		return fmt.Errorf("reroute: %w", err)
	}
	req := rpc.RerouteRequest{FigaroID: t.holder, Outfits: names}
	if len(t.pinned) > 0 {
		pins := form.Build(form.Snapshot{}, t.pinned, nil)
		req.Pins = &pins
	}
	var resp *rpc.SetResponse
	var landed form.Snapshot
	WithSessionFor(loaded, t.holder, func(s *Session) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if resp, err = s.Angelus.Reroute(ctx, req); err != nil {
			return nil
		}
		if f, ferr := s.Figaro.Form(ctx); ferr == nil {
			landed = f.Snapshot
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reroute: %w", err)
	}
	from := "outfit " + t.outfits
	if t.role != "" {
		from = "role " + ariaName(t.role) + " (" + from + ")"
	}
	result := setResult{figaroID: t.holder, resp: resp}
	fmt.Fprintf(stderrw, "%s figaro %s to %s from %s%s\n",
		result.verb("rerouted"), ariaName(t.holder), routeLabel(landed), from, result.at())
	return nil
}

func routeLabel(snap form.Snapshot) string {
	str := func(k string) string {
		if v := snap.Lookup(k); v != nil {
			return *v
		}
		return ""
	}
	label := str("system.provider")
	if m := str("system.model"); m != "" {
		label += "/" + m
	}
	return label
}

func rerouteTargetOf(loaded *config.Loaded, id string) (rerouteTarget, error) {
	var t rerouteTarget
	var snap form.Snapshot
	WithSessionFor(loaded, id, func(s *Session) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := s.Figaro.Form(ctx)
		if err != nil {
			die("reroute: read %s: %s", ariaName(s.AriaID), err)
		}
		snap, t.holder = resp.Snapshot, s.AriaID
		return nil
	})
	str := func(k string) string {
		if v := snap.Lookup(k); v != nil {
			return *v
		}
		return ""
	}
	holder := str("target-aria")
	if !strings.HasPrefix(t.holder, "@") && holder == "" {
		t.outfits = str("system.outfit_name")
		if t.outfits == "" {
			t.outfits = "default"
		}
		return t, nil
	}
	t.role = t.holder
	if holder == "" {
		return t, fmt.Errorf("reroute: role %s holds no figaro", ariaName(t.role))
	}
	t.holder, t.outfits = holder, str("succession-policy.outfit")
	t.pinned = map[string]json.RawMessage{}
	for _, k := range form.RouteKeys {
		if v, ok := snap.Get(k); ok && string(v) != "null" {
			t.pinned[k] = v
		}
	}
	return t, nil
}

func noteKeptRoute(ctx context.Context, id string, names []string) {
	if len(names) == 0 || id == "" || foreignNode(id) != "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	acli, err := sdk.DialAngelus(doorEndpoint())
	if err != nil {
		return
	}
	defer acli.Close()
	spec := strings.Join(names, ",")
	wears, err := acli.OutfitForm(ctx, spec)
	if err != nil {
		return
	}
	f, err := sdk.DialAria(ariaAddr(id), nil)
	if err != nil {
		return
	}
	defer f.Close()
	resp, err := f.Form(ctx)
	if err != nil || resp.Snapshot.Lookup("system.provider") == nil {
		return
	}
	for _, k := range form.RouteKeys {
		a, have := resp.Snapshot.Get(k)
		b, named := wears.Get(k)
		if named && (!have || !form.NewValue(a).Equal(form.NewValue(b))) {
			fmt.Fprintf(stderrw, "figaro: %s keeps its route %s; figaro reroute -O %s moves it\n", ariaName(id), routeLabel(resp.Snapshot), spec)
			return
		}
	}
}

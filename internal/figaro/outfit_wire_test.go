package figaro_test

import (
	"testing"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/outfit"
)

func TestTheWireCarriesARefusalOnANotFoundLayer(t *testing.T) {
	c := &outfit.Closure{Name: "reserved", Refused: []config.OutfitSource{{Path: "/u/reserved.toml", Root: "/u"}}}
	got := figaro.OutfitClosureWire(c)
	if len(got.Refused) != 1 || got.Refused[0].Path != "/u/reserved.toml" || got.Refused[0].Origin != "user" {
		t.Fatalf("refused = %+v", got.Refused)
	}
}

func TestTheWireCarriesAnIgnoredOverlayOnANotFoundLayer(t *testing.T) {
	c := &outfit.Closure{Name: "helper", Overlaid: []config.OutfitSource{{Path: "/u/helper.toml", Root: "/u"}}}
	got := figaro.OutfitClosureWire(c)
	if len(got.Overlaid) != 1 || got.Overlaid[0].Path != "/u/helper.toml" {
		t.Fatalf("overlaid = %+v", got.Overlaid)
	}
}

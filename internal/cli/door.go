package cli

import (
	"os"
	"sync"

	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/sdk"
)

type door struct {
	ep         transport.Endpoint
	configured bool
	label      string
}

type doorKey struct{ troupe, configDir, runtimeDir string }

var (
	doorMu     sync.Mutex
	doorCached map[doorKey]door
)

func resolveDoor() door {
	k := doorKey{os.Getenv("FIGARO_TROUPE"), config.DefaultConfigDir(), boot.AngelusRuntimeDir()}
	doorMu.Lock()
	defer doorMu.Unlock()
	if d, ok := doorCached[k]; ok {
		return d
	}
	d := chooseDoor(k.configDir)
	if doorCached == nil {
		doorCached = map[doorKey]door{}
	}
	doorCached[k] = d
	return d
}

func chooseDoor(configDir string) door {
	loaded, _ := config.Load(configDir)
	tr, ok, err := boot.SelectedTroupe(loaded)
	if err != nil {
		die("%s", err)
	}
	if !ok {
		return door{ep: transport.UnixEndpoint(boot.AngelusSocketPath())}
	}
	d := door{configured: true, label: tr.URL}
	if tr.Name != "" {
		d.label = tr.Name + " (" + tr.URL + ")"
	}
	if d.ep, err = troupeEndpoint(tr); err != nil {
		die("%s", err)
	}
	return d
}

func doorEndpoint() transport.Endpoint { return resolveDoor().ep }

func ariaAddr(id string) sdk.Addr {
	return sdk.Addr{Door: doorEndpoint(), ID: id}
}

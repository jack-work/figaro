package cli

import (
	"os"
	"sync"

	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/sdk"
)

type endpoint struct {
	ep         transport.Endpoint
	configured bool
	label      string
}

type endpointKey struct{ troupe, configDir, runtimeDir string }

var (
	endpointMu     sync.Mutex
	endpointCached map[endpointKey]endpoint
)

func resolveEndpoint() endpoint {
	refuseOffTape("the daemon")
	k := endpointKey{os.Getenv("FIGARO_TROUPE"), config.DefaultConfigDir(), boot.AngelusRuntimeDir()}
	endpointMu.Lock()
	defer endpointMu.Unlock()
	if d, ok := endpointCached[k]; ok {
		return d
	}
	d := chooseEndpoint(k.configDir)
	if endpointCached == nil {
		endpointCached = map[endpointKey]endpoint{}
	}
	endpointCached[k] = d
	return d
}

func chooseEndpoint(configDir string) endpoint {
	loaded, _ := config.Load(configDir)
	tr, ok, err := boot.SelectedTroupe(loaded)
	if err != nil {
		die("%s", err)
	}
	if !ok {
		return endpoint{ep: transport.UnixEndpoint(boot.AngelusSocketPath())}
	}
	d := endpoint{configured: true, label: tr.URL}
	if tr.Name != "" {
		d.label = tr.Name + " (" + tr.URL + ")"
	}
	if d.ep, err = endpointOfTroupe(tr); err != nil {
		die("%s", err)
	}
	return d
}

func daemonEndpoint() transport.Endpoint { return resolveEndpoint().ep }

func ariaAddr(id string) sdk.Addr {
	a, err := endpointFor(id)
	if err != nil {
		die("%s", err)
	}
	return a
}

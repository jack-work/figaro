package cli

import (
	"net/url"
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

type doorKey struct{ endpoint, configDir, runtimeDir string }

var (
	doorMu     sync.Mutex
	doorCached map[doorKey]door
)

func resolveDoor() door {
	k := doorKey{os.Getenv("FIGARO_ENDPOINT"), config.DefaultConfigDir(), boot.AngelusRuntimeDir()}
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
	e, ok, err := boot.SelectedEndpoint(loaded)
	if err != nil {
		die("%s", err)
	}
	if !ok {
		return door{ep: transport.UnixEndpoint(boot.AngelusSocketPath())}
	}
	d := door{configured: true, label: e.URL}
	if e.Name != "" {
		d.label = e.Name + " (" + e.URL + ")"
	}
	if u, _ := url.Parse(e.URL); u.Scheme == "unix" {
		d.ep = transport.UnixEndpoint(u.Path)
		return d
	}
	d.ep = transport.WSSEndpoint(e.URL, boot.EndpointResolver(e))
	return d
}

func doorEndpoint() transport.Endpoint { return resolveDoor().ep }

func ariaAddr(id string) sdk.Addr {
	return sdk.Addr{Door: doorEndpoint(), ID: id}
}

package cli

import (
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/sdk"
)

func doorEndpoint() transport.Endpoint {
	return transport.UnixEndpoint(boot.AngelusSocketPath())
}

func ariaAddr(id string) sdk.Addr {
	return sdk.Addr{Door: doorEndpoint(), ID: id}
}

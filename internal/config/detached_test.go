package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const liveNode = `default_outfit = "mock"
[telemetry]
otlp_endpoint = "http://127.0.0.1:4380"
dir = "/home/node/.local/state/figaro"
[cli]
troupe = "kelliher"
[troupe]
name = "kelliher"
provider = "figaro"
[door.tls]
listen = "100.64.0.1:7443"
[door.web]
path = "/run/figaro/web.sock"
authn = "upstream"
[authz.upstream]
issuer = "https://auth.test"
[sandbox.boxes.judge]
exec = "/run/figaro/judge.sock"
door = "/run/figaro/judge-door.sock"
[peers.b2c1d7a0-0000-4000-8000-000000000002]
endpoint = "tls://10.77.0.2:7443"
identity = "spiffe://kelliher/node/b2c1d7a0-0000-4000-8000-000000000002"
ca = "ca.crt"
cert = "node.crt"
key = "node.key"
`

// A dev shell that reads the live node's config must not start a second
// copy of the node: detached, the daemon sees no troupe, no node endpoint, no
// web or box endpoint socket of the live node's, no static peers, and no
// troupes/ to dial through. Everything else in the config still applies.
func TestADetachedConfigIsNotTheNode(t *testing.T) {
	t.Setenv(DetachedEnv, "1")
	l, err := loadWith(t, liveNode)
	require.NoError(t, err)
	require.True(t, l.Detached)
	require.Equal(t, "mock", l.Config.DefaultOutfit)
	name, err := l.TroupeName()
	require.NoError(t, err)
	require.Empty(t, name)
	require.Empty(t, l.Config.CLI.Troupe)
	_, tls, err := l.TLSEndpoint()
	require.NoError(t, err)
	require.False(t, tls, "the node door")
	_, web, err := l.WebEndpoint()
	require.NoError(t, err)
	require.False(t, web, "the web door")
	require.Empty(t, l.BoxEndpoints(), "a box door")
	box, ok := l.Box("judge")
	require.True(t, ok, "the box itself stays: an outfit may still run in it")
	require.Equal(t, "/run/figaro/judge.sock", box.Exec)
	require.Empty(t, l.Config.Peers)
	require.Empty(t, l.TroupesDir())
	require.Empty(t, l.TelemetryOTLPEndpoint(), "the live node's collector")
	require.Empty(t, l.Config.Telemetry.Dir, "the live node's telemetry files")
}

func TestWithoutTheEnvTheConfigIsTheNode(t *testing.T) {
	t.Setenv(DetachedEnv, "")
	l, err := loadWith(t, liveNode)
	require.NoError(t, err)
	require.False(t, l.Detached)
	name, _ := l.TroupeName()
	require.Equal(t, "kelliher", name)
	_, tls, err := l.TLSEndpoint()
	require.NoError(t, err)
	require.True(t, tls)
	require.NotEmpty(t, l.BoxEndpoints())
	require.NotEmpty(t, l.TroupesDir())
	require.Equal(t, "http://127.0.0.1:4380", l.TelemetryOTLPEndpoint())
}

// Dev runs exported pager latency into the live figscope and spoiled its
// release comparisons. FIGARO_OTLP_ENDPOINT overrides the config: off (or
// empty) exports nothing, anything else is the endpoint.
func TestTheOTLPEnvOverridesTheConfig(t *testing.T) {
	t.Setenv(DetachedEnv, "")
	l, err := loadWith(t, liveNode)
	require.NoError(t, err)
	for env, want := range map[string]string{"off": "", "": "", "http://10.0.0.9:4318": "http://10.0.0.9:4318"} {
		t.Setenv(OTLPEndpointEnv, env)
		require.Equal(t, want, l.TelemetryOTLPEndpoint(), "FIGARO_OTLP_ENDPOINT=%q", env)
	}
}

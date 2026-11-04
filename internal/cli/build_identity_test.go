package cli

import (
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/boot"
)

// Two identity schemes coexist, a git revision and a module version, and
// neither converts into the other: refuse within a scheme, warn across one.
func TestBuildIdentityKind(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "unknown"},
		{"8e92e2677ac0", "revision"},
		{"b7e91d23", "revision"},
		{"deadbeef", "revision"},
		{"v0.17.0", "module"},
		{"v1.2.3-rc1", "module"},
		{"(devel)", "other"},
		{"unknown", "other"},
	}
	for _, tc := range cases {
		if got := boot.BuildIdentityKind(tc.in); got != tc.want {
			t.Errorf("boot.BuildIdentityKind(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMixedSchemesAreNotRefused(t *testing.T) {
	daemon, cli := "8e92e2677ac0", "v0.17.0"
	if boot.BuildIdentityKind(daemon) == boot.BuildIdentityKind(cli) {
		t.Fatalf("fixture is wrong: %q and %q must be different schemes", daemon, cli)
	}
	if boot.BuildIdentityKind("8e92e2677ac0") != boot.BuildIdentityKind("b7e91d23aaaa") {
		t.Fatal("two revisions must share a scheme, so an inequality between them is real")
	}
	if boot.BuildIdentityKind("v0.17.0") != boot.BuildIdentityKind("v0.16.1") {
		t.Fatal("two module versions must share a scheme, so an inequality between them is real")
	}
}

// A daemon that reports no protocol (0.52.3 and older) is judged by build
// alone, exactly as before the contract.
func TestBuildHandshakeMatrix(t *testing.T) {
	cases := []struct {
		name         string
		daemon, mine string
		want         handshakeVerdict
	}{
		{"identical revisions", "8e92e2677ac0", "8e92e2677ac0", handshakeOK},
		{"identical module versions", "v0.17.0", "v0.17.0", handshakeOK},
		{"both unknown", "", "", handshakeOK},
		{"cli unknown", "8e92e2677ac0", "", handshakeCLIUnknown},
		{"daemon predates the check", "", "8e92e2677ac0", handshakeDaemonOld},
		{"nix daemon, proxy cli", "8e92e2677ac0", "v0.17.0", handshakeMixedSchemes},
		{"proxy daemon, nix cli", "v0.17.0", "8e92e2677ac0", handshakeMixedSchemes},
		{"two revisions", "8e92e2677ac0", "b7e91d23aaaa", handshakeRefuse},
		{"two module versions", "v0.17.0", "v0.16.1", handshakeRefuse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildHandshake(daemonIdentity{build: tc.daemon}, tc.mine, "0.52.4"); got != tc.want {
				t.Fatalf("buildHandshake(%q, %q) = %v, want %v", tc.daemon, tc.mine, got, tc.want)
			}
		})
	}
}

// The contract: a daemon that reports the CLI's protocol is accepted across
// builds within a minor release, warned about across minors, and refused only
// on another protocol. Dev builds, which carry no release, keep the build rule.
func TestBuildHandshakeAcrossReleases(t *testing.T) {
	const a, b = "8e92e2677ac0", "b7e91d23aaaa"
	cases := []struct {
		name       string
		d          daemonIdentity
		mine, mrel string
		want       handshakeVerdict
	}{
		{"same build", daemonIdentity{build: a, release: "0.52.4", protocol: rpc.ProtocolVersion}, a, "0.52.4", handshakeOK},
		{"next patch daemon", daemonIdentity{build: b, release: "0.52.5", protocol: rpc.ProtocolVersion}, a, "0.52.4", handshakeOK},
		{"previous patch daemon", daemonIdentity{build: b, release: "0.52.4", protocol: rpc.ProtocolVersion}, a, "0.52.5", handshakeOK},
		{"v-prefixed release", daemonIdentity{build: b, release: "v0.52.6", protocol: rpc.ProtocolVersion}, a, "0.52.4", handshakeOK},
		{"another minor", daemonIdentity{build: b, release: "0.53.0", protocol: rpc.ProtocolVersion}, a, "0.52.4", handshakeOtherMinor},
		{"another protocol", daemonIdentity{build: b, release: "0.52.5", protocol: rpc.ProtocolVersion + 1}, a, "0.52.4", handshakeProtocolMismatch},
		{"another protocol, same minor", daemonIdentity{build: b, release: "0.52.4", protocol: rpc.ProtocolVersion + 1}, a, "0.52.4", handshakeProtocolMismatch},
		{"dev daemon with a protocol", daemonIdentity{build: b, protocol: rpc.ProtocolVersion}, a, "0.52.4", handshakeRefuse},
		{"dev cli against a release", daemonIdentity{build: b, release: "0.52.4", protocol: rpc.ProtocolVersion}, a, "", handshakeRefuse},
		{"unknown cli against a release", daemonIdentity{build: b, release: "0.52.4", protocol: rpc.ProtocolVersion}, "", "", handshakeCLIUnknown},
		{"pre-protocol daemon of another release", daemonIdentity{build: b, release: ""}, a, "0.52.4", handshakeRefuse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildHandshake(tc.d, tc.mine, tc.mrel); got != tc.want {
				t.Fatalf("buildHandshake(%+v, %q, %q) = %v, want %v", tc.d, tc.mine, tc.mrel, got, tc.want)
			}
		})
	}
}

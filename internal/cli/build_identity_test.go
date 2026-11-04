package cli

import (
	"strings"
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

func resetDaemonSeen(t *testing.T) {
	t.Helper()
	daemonSeen.Lock()
	daemonSeen.known, daemonSeen.same, daemonSeen.who = false, false, ""
	daemonSeen.d, daemonSeen.needs = daemonIdentity{}, nil
	daemonSeen.Unlock()
	t.Cleanup(func() {
		daemonSeen.Lock()
		daemonSeen.known, daemonSeen.same, daemonSeen.needs = false, false, nil
		daemonSeen.Unlock()
	})
}

// An older daemon drops an unknown field without a word: images sent to a
// daemon that does not report qua.images would run the prompt without them.
func TestImagesAreRefusedByNameToADaemonWithoutTheCapability(t *testing.T) {
	cases := []struct {
		name    string
		d       daemonIdentity
		same    bool
		refused bool
	}{
		{"reports qua.images", daemonIdentity{build: "b", release: "0.52.5", protocol: 1, caps: []rpc.Capability{rpc.CapQuaImages}}, false, false},
		{"reports nothing", daemonIdentity{build: "b", release: "0.52.5", protocol: 1}, false, true},
		{"same build needs no list", daemonIdentity{build: "a"}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetDaemonSeen(t)
			mine := "b2"
			if tc.same {
				mine = tc.d.build
			}
			sawDaemon("the daemon", tc.d, mine)
			err := needCapability(rpc.CapQuaImages, "images on a prompt")
			if (err != nil) != tc.refused {
				t.Fatalf("needCapability = %v, refused want %v", err, tc.refused)
			}
			if tc.refused && !strings.Contains(err.Error(), string(rpc.CapQuaImages)) {
				t.Fatalf("the refusal must name the capability: %v", err)
			}
		})
	}
}

// A capability needed before the daemon is reached is checked when it is.
func TestANeedRegisteredBeforeTheHandshakeIsCheckedAtIt(t *testing.T) {
	resetDaemonSeen(t)
	if err := needCapability(rpc.CapQuaImages, "images on a prompt"); err != nil {
		t.Fatalf("no daemon seen yet, nothing to refuse: %v", err)
	}
	recordDaemon("the daemon", daemonIdentity{build: "b", release: "0.52.5", protocol: 1}, "a")
	if err := settlePendingNeeds(); err == nil {
		t.Fatal("a daemon without qua.images must refuse the images registered before it was seen")
	}
}

// The first handshake settles a need registered before it and clears it, so
// a second handshake in the same process does not judge an old request.
func TestTheFirstHandshakeClearsThePendingNeeds(t *testing.T) {
	resetDaemonSeen(t)
	if err := needCapability(rpc.CapQuaImages, "images on a prompt"); err != nil {
		t.Fatalf("no daemon seen yet, nothing to refuse: %v", err)
	}
	recordDaemon("the daemon", daemonIdentity{build: "b", release: "0.52.5", protocol: 1, caps: []rpc.Capability{rpc.CapQuaImages}}, "a")
	if err := settlePendingNeeds(); err != nil {
		t.Fatalf("a daemon with qua.images refused the pending images: %v", err)
	}
	daemonSeen.Lock()
	left := len(daemonSeen.needs)
	daemonSeen.Unlock()
	if left != 0 {
		t.Fatalf("%d need(s) still pending after the first handshake", left)
	}
	recordDaemon("the daemon", daemonIdentity{build: "c", release: "0.52.4", protocol: 1}, "a")
	if err := settlePendingNeeds(); err != nil {
		t.Fatalf("a second handshake judged the first request's need: %v", err)
	}
}

// A need is the current request's, not the process's: after one image went
// to a daemon that takes images, a reconnect to one that does not survives the
// handshake and refuses only the next image.
func TestAReconnectToADaemonWithoutImagesRefusesOnlyTheNextImage(t *testing.T) {
	resetDaemonSeen(t)
	withImages := daemonIdentity{build: "b", release: "0.52.5", protocol: 1, caps: []rpc.Capability{rpc.CapQuaImages}}
	without := daemonIdentity{build: "c", release: "0.52.4", protocol: 1}
	sawDaemon("the daemon", withImages, "a")
	img := rpc.QuaRequest{Text: "total?", Images: []rpc.QuaImage{{MimeType: "image/png", Data: "AA"}}}
	if err := refuseUnknownFields(img); err != nil {
		t.Fatalf("a daemon with qua.images refused an image: %v", err)
	}
	recordDaemon("the daemon", without, "a")
	if err := settlePendingNeeds(); err != nil {
		t.Fatalf("the reconnect's handshake failed on a need from an earlier request: %v", err)
	}
	if err := refuseUnknownFields(rpc.QuaRequest{Text: "hi"}); err != nil {
		t.Fatalf("a text prompt after the reconnect was refused: %v", err)
	}
	if err := refuseUnknownFields(img); err == nil || !strings.Contains(err.Error(), string(rpc.CapQuaImages)) {
		t.Fatalf("the next image to a daemon without %s: %v", rpc.CapQuaImages, err)
	}
}

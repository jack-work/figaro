package cli

import "testing"

// The build handshake must compare LIKE WITH LIKE.
//
// Two identity schemes coexist and neither converts into the other:
//
//	revision  a git SHA, a nix build, or `go build` in a checkout
//	module    vX.Y.Z: `go install <module>/cmd/figaro@vX.Y.Z`, because the
//	                      proxy ships a zip and records no VCS metadata at all
//
// b551b32 made a proxy-built binary report its module version instead of
// nothing, which is the real fix: two DIFFERENT proxy builds used to read as
// "both unknown" and pass in silence, and silence is what makes a stale
// fig.exe against a fresh angelus look like a hung daemon.
//
// But it also made a nix daemon and a proxy CLI of the SAME RELEASE compare
// unequal, and the code refused on any inequality. That combination is
// legitimate and would have been bricked permanently: the user's only tools
// are the two binaries that now refuse each other.
//
// So: refuse within a scheme, warn across one.
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
		// "(devel)" never reaches here: moduleVersion filters it: but if it
		// ever did it must not be mistaken for either real scheme.
		{"(devel)", "other"},
		{"unknown", "other"},
	}
	for _, tc := range cases {
		if got := buildIdentityKind(tc.in); got != tc.want {
			t.Errorf("buildIdentityKind(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestMixedSchemesAreNotRefused is the ruling in assertions: the pair that
// must NOT die is a git-revision daemon beside a module-version CLI, while
// two values of the SAME scheme remain a real mismatch.
//
// CANARY: delete the `buildIdentityKind(...) != buildIdentityKind(...)` case
// from checkDaemonBuild and the mixed pair falls through to die().
func TestMixedSchemesAreNotRefused(t *testing.T) {
	daemon, cli := "8e92e2677ac0", "v0.17.0"
	if buildIdentityKind(daemon) == buildIdentityKind(cli) {
		t.Fatalf("fixture is wrong: %q and %q must be different schemes", daemon, cli)
	}
	if buildIdentityKind("8e92e2677ac0") != buildIdentityKind("b7e91d23aaaa") {
		t.Fatal("two revisions must share a scheme, so an inequality between them is real")
	}
	if buildIdentityKind("v0.17.0") != buildIdentityKind("v0.16.1") {
		t.Fatal("two module versions must share a scheme, so an inequality between them is real")
	}
}

// TestBuildHandshakeMatrix pins the DECISION, not just the classifier: my
// first attempt tested only buildIdentityKind, so neutering the mixed-scheme
// case in checkDaemonBuild left it green. A canary that cannot fail is not a
// canary, and a green reading from it is worse than a red one.
//
// CANARY (watched): change the handshakeMixedSchemes case to `default:` and
// the "nix daemon beside a proxy CLI" row fails with
// `got handshakeRefuse, want handshakeMixedSchemes`: the lockout, reproduced.
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
		// The pair this ruling exists for: legitimate, and it must NOT die.
		{"nix daemon, proxy cli", "8e92e2677ac0", "v0.17.0", handshakeMixedSchemes},
		{"proxy daemon, nix cli", "v0.17.0", "8e92e2677ac0", handshakeMixedSchemes},
		// Within a scheme a difference is real: the wire may differ.
		{"two revisions", "8e92e2677ac0", "b7e91d23aaaa", handshakeRefuse},
		{"two module versions", "v0.17.0", "v0.16.1", handshakeRefuse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildHandshake(tc.daemon, tc.mine); got != tc.want {
				t.Fatalf("buildHandshake(%q, %q) = %v, want %v", tc.daemon, tc.mine, got, tc.want)
			}
		})
	}
}

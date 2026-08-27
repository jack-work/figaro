package gateway

// AUTHENTICATION AT THE DOOR, and the envelope rewrite that follows it.
//
// The gateway does not mint figaro identities and does not verify anything
// cryptographic. It reads whoever the front door already established -- a
// reverse proxy's headers, or a shared secret -- and turns that into an
// authz.Identity the daemon's existing policy can key on.
//
// That is a deliberate ceiling. A figaro identity is a handle on an agent
// that runs bash; issuing differentiated credentials without sandboxing
// would grant privilege no policy can contain, because containment has to
// happen below the RPC surface. Until then the gateway authenticates PEERS
// and BROWSERS, never figaros.

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
)

// authenticator turns a request into an identity, or refuses it.
type authenticator interface {
	// authenticate returns the caller's identity. An error is a REFUSAL:
	// the upgrade never happens and no tunnel is opened.
	authenticate(r *http.Request) (authz.Identity, error)
}

func newAuthenticator(c Config) (authenticator, error) {
	if len(c.Authn) == 0 {
		return nil, fmt.Errorf("no authenticator configured")
	}
	var chain []authenticator
	for _, a := range c.Authn {
		switch a {
		case AuthnNone:
			chain = append(chain, anonymousAuth{})
		case AuthnUpstream:
			chain = append(chain, upstreamAuth{})
		case AuthnDoorkey:
			chain = append(chain, doorkeyAuth{secret: c.Doorkey})
		default:
			return nil, fmt.Errorf("unknown authn %q", a)
		}
	}
	var base authenticator = anyOf(chain)
	if len(chain) == 1 {
		base = chain[0]
	}
	if len(c.RequireGroups) > 0 {
		return requireGroups{next: base, want: c.RequireGroups}, nil
	}
	return base, nil
}

// anyOf tries each authenticator and takes the first that succeeds.
//
// TWO CREDENTIAL SHAPES REACH ONE DOOR, and that is not an accident of
// configuration -- it is what the platform does. Caddy forward-auths only
// requests WITHOUT a bearer:
//
//	@no_bearer not header Authorization Bearer*
//	forward_auth @no_bearer …
//
// so a browser arrives carrying Remote-User (upstream authenticates it) and
// a machine arrives carrying a bearer that Caddy passed straight through
// (doorkey authenticates it). One listener, two populations.
//
// THE SECURITY CONSEQUENCE, stated plainly: a caller CHOOSES which
// credential to present, so the door is exactly as strong as its weakest
// authenticator. That is why Serve admits an exposure only when EVERY
// configured authenticator is legal on it, rather than when any one is.
type anyOf []authenticator

func (list anyOf) authenticate(r *http.Request) (authz.Identity, error) {
	var first error
	for _, a := range list {
		id, err := a.authenticate(r)
		if err == nil {
			return id, nil
		}
		if first == nil {
			first = err
		}
	}
	return authz.Identity{}, first
}

// requireGroups is the ADMISSION gate: authentication says who you are, this
// says whether you may be here at all.
//
// It exists because of a gap that is easy to miss and was live on spain. The
// platform derives an lldap group from a site's requiredGroups and CREATES
// it, and it derives a 2FA rule from the hostname -- but the Authelia rule
// it writes is `policy: two_factor` with no subject restriction. So the group
// exists, the config names it, and NOTHING CHECKS IT: every user who can
// pass 2FA reaches the backend.
//
// That is the documented division of labour (the proxy authenticates, the
// app authorizes), and every other service on the platform does its own
// group check. This is figaro's.
type requireGroups struct {
	next authenticator
	want []string
}

func (g requireGroups) authenticate(r *http.Request) (authz.Identity, error) {
	id, err := g.next.authenticate(r)
	if err != nil {
		return id, err
	}
	for _, w := range g.want {
		for _, have := range id.Groups {
			if have == w {
				return id, nil
			}
		}
	}
	return authz.Identity{}, fmt.Errorf(
		"%s is not in %s: this door admits those groups and no others",
		id.String(), strings.Join(g.want, " or "))
}

// anonymousAuth admits everyone. Legitimate only on a unix socket, which the
// refusal table enforces -- reaching the inode already required being the
// right uid, so the kernel did the authentication.
type anonymousAuth struct{}

func (anonymousAuth) authenticate(*http.Request) (authz.Identity, error) {
	return authz.Identity{Label: "gateway"}, nil
}

// upstreamAuth reads what a reverse proxy established.
//
// It VERIFIES NOTHING, and cannot: these are plain headers. Its safety rests
// entirely on the proxy being the only route to the listener, which is why
// the refusal table will not let it serve a routable address. Anything that
// reaches the port otherwise -- another local uid, a rebound browser, a
// compromised aria's own shell -- can claim to be an administrator.
type upstreamAuth struct{}

func (upstreamAuth) authenticate(r *http.Request) (authz.Identity, error) {
	user := strings.TrimSpace(r.Header.Get("Remote-User"))
	if user == "" {
		// The proxy is supposed to set this on every authenticated request.
		// Its absence means we are NOT behind the proxy we think we are, and
		// guessing would be the whole bypass.
		return authz.Identity{}, fmt.Errorf(
			"no Remote-User: this listener expects a reverse proxy to authenticate")
	}
	var groups []string
	for _, g := range strings.Split(r.Header.Get("Remote-Groups"), ",") {
		if g = strings.TrimSpace(g); g != "" {
			groups = append(groups, g)
		}
	}
	return authz.Identity{Authenticated: true, Label: user, Groups: groups}, nil
}

// doorkeyAuth requires a shared secret. It makes no distinction between
// callers -- everyone who holds the key is the same principal -- which is
// exactly why it is safe to ship before sandboxing exists: it grants the
// privilege the holder already had, and draws no line it cannot defend.
type doorkeyAuth struct{ secret string }

func (d doorkeyAuth) authenticate(r *http.Request) (authz.Identity, error) {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		// NEVER a query parameter. A token in a URL lands in access logs,
		// in Referer headers, and in browser history.
		return authz.Identity{}, fmt.Errorf("no bearer token")
	}
	// Constant time: a length-independent compare would leak the secret one
	// byte at a time to anyone who can time the handshake.
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), []byte(d.secret)) != 1 {
		return authz.Identity{}, fmt.Errorf("bad bearer token")
	}
	return authz.Identity{Authenticated: true, Label: "doorkey",
		Groups: []string{"figaro-admin"}}, nil
}

// ─────────────────────────────────────────────────────────────────────────
// THE ENVELOPE REWRITE
//
// figaro carries the CALLING ARIA's id in a reserved params field, and
// authz.AriaHeader trusts it on assertion -- honest for a unix socket whose
// security model is 0600, because a local process could claim any id anyway.
//
// The moment that socket is reachable through a gateway the argument dies:
// what nothing stops a LOCAL process from claiming, nothing stops a REMOTE
// one from claiming either. A remote caller could name itself any aria,
// satisfy a self-targeted policy rule, and impersonate another figaro -- or
// the DUKE -- in a transcript that renders sender names as trustworthy.
//
// So every frame arriving through the gateway has its attribution replaced
// with what the gateway itself authenticated.
//
// THIS IS NOT RETYPING THE CONTRACT. The payload is never re-encoded: params
// are decoded as map[string]json.RawMessage, the attribution keys are
// replaced, and every other field is written back byte-for-byte as it
// arrived. Only key order changes, and key order carries no meaning.

// attributionKeys are the params fields that assert who is calling. They are
// stripped on ingress whether or not the gateway has anything to put back.
var attributionKeys = []string{rpc.CallerKey, rpc.CallerLabelKey}

// rewriteEnvelope replaces a frame's attribution with the authenticated
// identity. It returns the params to forward.
func rewriteEnvelope(params json.RawMessage, id authz.Identity) (json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if len(params) > 0 && !isNullJSON(params) {
		if err := json.Unmarshal(params, &obj); err != nil {
			// Not an object -- some methods send nothing, and a non-object
			// carries no attribution to forge. Pass it through untouched
			// rather than inventing a shape the daemon did not expect.
			return params, nil
		}
	}
	if obj == nil {
		obj = map[string]json.RawMessage{}
	}

	// STRIP FIRST, unconditionally. A caller that sends an aria id we do not
	// overwrite would keep it, which is the whole hole.
	for _, k := range attributionKeys {
		delete(obj, k)
	}

	// The gateway never asserts a figaro id: it has not authenticated an
	// aria, and stamping one would be the same lie in the other direction.
	// The LABEL is attribution only, and says how the caller arrived.
	if label := id.Label; label != "" {
		b, err := json.Marshal("via gateway: " + label)
		if err != nil {
			return nil, err
		}
		obj[rpc.CallerLabelKey] = b
	}
	return json.Marshal(obj)
}

func isNullJSON(b json.RawMessage) bool {
	return strings.TrimSpace(string(b)) == "null"
}

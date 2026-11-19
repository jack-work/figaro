package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/approvals"
	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/sdk"
)

var approvalsVerb = &cmdkit.Command{
	Name:  "approvals",
	Group: "System",
	Short: "Serve the owner approvals page: elicit.* requests on role forms, approved by template patch",
	Usage: "approvals serve --listen <addr> --forms <@role,...> --admin <user> --origin <url> --peer <ip,...> [--edge-secret-file <file>] [--audit <file>]",
	Long: `A figaro asks for an owner-held change by writing elicit.<id> on its role
form. This page lists those requests and offers Approve and Deny. Approve
builds the patch from a fixed per-verb template and the live values, shows it
with its sha256, and applies it with compare-and-set on the versions read:
first the target's keys, then the removal of the request. Request text is
shown, never applied.

It acts as you, the owner: run it from your own systemd --user manager,
never from inside a figaro. Only the proxy in front of it is believed: a
request must come from a --peer address and carry the edge secret in the
X-Figaro-Approvals-Edge header (at least 32 bytes, read from
--edge-secret-file, default $CREDENTIALS_DIRECTORY/edge). Only then is its
Remote-User read.

  verbs  figaro.model  system.provider + system.model on a figaro
         figaro.ttl    system.ttl on a figaro
         role.model    system.provider + system.model on a role form
                       (takes effect at the next succession)`,
	Flags: []cmdkit.FlagDef{
		{Long: "listen", Description: "address to serve on, e.g. 127.0.0.1:7482"},
		{Long: "forms", Description: "comma-separated role forms whose elicit.* requests are listed"},
		{Long: "admin", Description: "the Remote-User the proxy must name"},
		{Long: "origin", Description: "the Origin a POST must carry, e.g. https://order.kelliher.info"},
		{Long: "peer", Description: "comma-separated addresses the proxy connects from"},
		{Long: "edge-secret-file", Description: "file holding the secret the proxy sends (default $CREDENTIALS_DIRECTORY/edge)"},
		{Long: "audit", Description: "append-only audit file (default ~/.local/state/figaro-approvals/audit.log)"},
	},
	ArgsMin: 1,
	Run: func(ctx *cmdkit.RunContext) error {
		if ctx.Args[0] != "serve" {
			return fmt.Errorf("approvals: want serve, not %q", ctx.Args[0])
		}
		listen, admin, origin := ctx.Flag("listen"), ctx.Flag("admin"), ctx.Flag("origin")
		var forms []string
		for _, f := range strings.Split(ctx.Flag("forms"), ",") {
			if f = strings.TrimSpace(f); f != "" {
				forms = append(forms, f)
			}
		}
		if listen == "" || admin == "" || origin == "" || len(forms) == 0 {
			return fmt.Errorf("approvals serve needs --listen, --forms, --admin and --origin")
		}
		peers, err := parsePeers(ctx.Flag("peer"))
		if err != nil {
			return err
		}
		secret, err := readEdgeSecret(ctx.Flag("edge-secret-file"))
		if err != nil {
			return err
		}
		audit, err := openAudit(ctx.Flag("audit"))
		if err != nil {
			return err
		}
		defer audit.Close()
		d := &approvals.Decider{
			Forms: daemonForms{door: doorEndpoint()},
			Audit: approvals.NewAudit(io.MultiWriter(audit, os.Stdout)),
			Now:   time.Now,
		}
		srv := &http.Server{Addr: listen, Handler: approvals.NewServer(d, forms, admin, origin, secret, peers).Handler(),
			ReadHeaderTimeout: 10 * time.Second}
		return srv.ListenAndServe()
	},
}

func parsePeers(raw string) ([]netip.Addr, error) {
	var out []netip.Addr
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		a, err := netip.ParseAddr(p)
		if err != nil {
			return nil, fmt.Errorf("--peer %q: %w", p, err)
		}
		out = append(out, a.Unmap())
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("approvals serve needs --peer: the proxy's address")
	}
	return out, nil
}

func readEdgeSecret(path string) ([]byte, error) {
	if path == "" {
		dir := os.Getenv("CREDENTIALS_DIRECTORY")
		if dir == "" {
			return nil, fmt.Errorf("approvals serve needs --edge-secret-file or a LoadCredential named edge")
		}
		path = filepath.Join(dir, "edge")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("edge secret: %w", err)
	}
	b = []byte(strings.TrimSpace(string(b)))
	if len(b) < 32 {
		return nil, fmt.Errorf("edge secret in %s is shorter than 32 bytes", path)
	}
	return b, nil
}

func openAudit(path string) (*os.File, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(home, ".local", "state", "figaro-approvals", "audit.log")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
}

type daemonForms struct{ door transport.Endpoint }

func (d daemonForms) aria(ctx context.Context, id string) (*sdk.Aria, error) {
	a, err := sdk.DialAngelus(d.door)
	if err != nil {
		return nil, err
	}
	defer a.Close()
	resp, err := a.Attach(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("attach %s: %w", id, err)
	}
	if resp.FigaroID != id {
		return nil, fmt.Errorf("%s resolved to %s: refusing to act on another form", id, resp.FigaroID)
	}
	addr, err := doorFor(resp.FigaroID)
	if err != nil {
		return nil, err
	}
	return sdk.DialAria(addr, nil)
}

func (d daemonForms) Read(ctx context.Context, id string) (form.Snapshot, uint64, error) {
	c, err := d.aria(ctx, id)
	if err != nil {
		return form.Snapshot{}, 0, err
	}
	defer c.Close()
	resp, err := c.Form(ctx)
	if err != nil {
		return form.Snapshot{}, 0, err
	}
	return resp.Snapshot, resp.Version, nil
}

func (d daemonForms) Set(ctx context.Context, id string, set map[string]json.RawMessage, remove []string, ifVersion uint64) error {
	c, err := d.aria(ctx, id)
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.SetWith(ctx, rpc.SetRequest{Patch: form.Build(form.Snapshot{}, set, remove), IfVersion: ifVersion, Assert: true})
	return err
}

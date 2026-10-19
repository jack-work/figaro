package cli

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"golang.org/x/term"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/troupe"
	"github.com/jack-work/figaro/sdk"
)

type joinDoor interface {
	TroupeIdentity(ctx context.Context, troupe string) (*rpc.TroupeIdentityResponse, error)
	TroupeSign(ctx context.Context, req rpc.TroupeSignRequest) (*rpc.TroupeSignResponse, error)
}

type joinOpts struct {
	Delegate    string
	Door        string
	Listen      string
	RootSHA256  string
	Yes         bool
	Interactive bool
	In          io.Reader
	Out         io.Writer
	UID         int
}

type joinSite struct {
	loaded   *config.Loaded
	stateDir string
	node     string
}

func (s joinSite) doors(o joinOpts) (door, listen string, err error) {
	existing, ok, err := s.loaded.TLSDoor()
	if err != nil {
		return "", "", err
	}
	if ok && existing.Cert != "" {
		return "", "", errors.New("[door.tls] names certificate files; a troupe member serves on troupe keys, so remove cert, key and client_ca first")
	}
	door, listen = o.Door, o.Listen
	if door == "" && ok {
		door = existing.Advertise
	}
	if door == "" {
		return "", "", errors.New("--door host:port is required: the address other nodes dial to reach this one")
	}
	host, _, err := net.SplitHostPort(door)
	if err != nil || host == "" || net.ParseIP(host).IsUnspecified() {
		return "", "", fmt.Errorf("--door %q is not a host:port another node can dial", door)
	}
	if listen == "" && ok {
		listen = existing.Listen
	}
	if listen == "" {
		listen = door
	}
	if _, _, err := net.SplitHostPort(listen); err != nil {
		return "", "", fmt.Errorf("--listen %q: %w", listen, err)
	}
	return door, listen, nil
}

func (s joinSite) fits(name string) error {
	if err := troupe.ValidName(name); err != nil {
		return err
	}
	have, err := s.loaded.TroupeName()
	if err != nil {
		return err
	}
	if have != "" && have != name {
		return fmt.Errorf("this node is in troupe %s; it joins no other", have)
	}
	if len(s.loaded.Config.Peers) > 0 {
		return errors.New("[peers] belongs to the static provider; a node that joins takes its members from the troupe, so remove [peers] first")
	}
	return nil
}

func trustRoot(id *rpc.TroupeIdentityResponse, held []byte, o joinOpts) (string, error) {
	sum := sha256.Sum256(id.Root)
	hexSum := hex.EncodeToString(sum[:])
	if !strings.EqualFold(id.RootSHA256, hexSum) {
		return "", errors.New("the door's root does not match the hash it sent with it")
	}
	if held != nil && string(held) != string(id.Root) {
		return "", errors.New("this node already holds a different root for that troupe")
	}
	switch {
	case o.RootSHA256 != "":
		if !strings.EqualFold(strings.ReplaceAll(o.RootSHA256, ":", ""), hexSum) {
			return "", fmt.Errorf("the troupe's root is sha256 %s, not the %s you pinned", hexSum, o.RootSHA256)
		}
	case held != nil, o.Yes:
	case !o.Interactive:
		return "", fmt.Errorf("the troupe's root is sha256 %s: pass --root-sha256 %s to pin it, or --yes to trust it", hexSum, hexSum)
	default:
		fmt.Fprintf(o.Out, "Trust this root for troupe %s? [y/N] ", id.Troupe)
		if a := strings.ToLower(strings.TrimSpace(readLine(o.In))); a != "y" && a != "yes" {
			return "", errors.New("the root was not trusted; nothing was signed or written")
		}
	}
	return hexSum, nil
}

func troupeJoin(ctx context.Context, d joinDoor, site joinSite, name string, o joinOpts) error {
	if err := site.fits(name); err != nil {
		return err
	}
	if err := checkDelegate(o); err != nil {
		return err
	}
	door, listen, err := site.doors(o)
	if err != nil {
		return err
	}
	id, err := d.TroupeIdentity(ctx, name)
	if err != nil {
		return err
	}
	if id.Node == site.node {
		return errors.New("that door is this node's own: join through another member")
	}
	fmt.Fprintf(o.Out, "troupe %s, through node %s\n  you are   %s\n  root      sha256 %s\n  signers   %s\n",
		id.Troupe, id.Node, principalOrNobody(id.Principal), id.RootSHA256, strings.Join(id.Signers, ", "))
	dir := troupe.DirFor(site.stateDir, name)
	var held []byte
	if c, err := dir.Root(); err == nil {
		held = c.Raw
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if _, err := trustRoot(id, held, o); err != nil {
		return err
	}
	lend, err := askDelegate(name, o)
	if err != nil {
		return err
	}

	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	req := rpc.TroupeSignRequest{Troupe: name, Node: site.node, PublicKey: pub, Door: door}
	if old, err := dir.Node(); err == nil {
		if signer, ok := old.PrivateKey.(crypto.Signer); ok && len(old.Certificate) == 2 {
			sig, err := troupe.SignRejoin(signer, name, site.node, pub, door)
			if err != nil {
				return err
			}
			req.Rejoin = &rpc.TroupeRejoin{Chain: old.Certificate, Sig: sig}
		}
	}
	resp, err := d.TroupeSign(ctx, req)
	if err != nil {
		return err
	}
	leaf, issuer, root, err := troupe.ParseSigned(resp.Chain, resp.Root)
	if err != nil {
		return err
	}
	if string(root.Raw) != string(id.Root) {
		return errors.New("the door signed under another root than the one it showed")
	}
	if err := troupe.CheckLeaf(leaf, issuer, root, name, site.node, pub, time.Now()); err != nil {
		return fmt.Errorf("the certificate the door returned: %w", err)
	}
	es, err := troupe.Decode(resp.Entries)
	if err != nil {
		return err
	}
	l := troupe.NewLedger(name, root)
	if _, err := l.Merge(es...); err != nil {
		fmt.Fprintf(o.Out, "  (some entries did not verify and were dropped: %v)\n", err)
	}
	me, ok := l.View().Members[site.node]
	if !ok {
		return errors.New("the door signed a certificate but its entries do not record this node's join")
	}

	if err := dir.WriteRoot(root); err != nil {
		return err
	}
	if err := dir.WriteNode(leaf, issuer, key); err != nil {
		return err
	}
	if err := dir.WriteJoined(l.Entries()); err != nil {
		return err
	}
	snippet, err := joinConfig(site.loaded.ConfigPath, name, listen, door, o.UID, id.Principal)
	fmt.Fprintf(o.Out, "joined troupe %s as node %s, admitted by %s; certificate until %s\n",
		name, site.node, me.AdmittedBy, leaf.NotAfter.UTC().Format("2006-01-02 15:04Z"))
	if err != nil {
		fmt.Fprintf(o.Out, "could not write %s (%v); add this yourself:\n\n%s\n", site.loaded.ConfigPath, err, snippet)
		return err
	}
	fmt.Fprintf(o.Out, "wrote to %s:\n\n%s\n", site.loaded.ConfigPath, snippet)
	if err := writeDelegation(o.Out, site.loaded, name, lend, selfPrincipal(o.UID, id.Principal)); err != nil {
		return err
	}
	fmt.Fprintln(o.Out, "restart the daemon to take it up: figaro stop")
	return nil
}

func principalOrNobody(p rpc.Principal) string {
	if t := authz.PrincipalText(p); t != "" {
		return t
	}
	return "(no principal)"
}

func peercredFor(uid int, p rpc.Principal) string {
	text := authz.PrincipalText(p)
	if p.Kind != rpc.PrincipalUser || text == "" || p.ID == strconv.Itoa(uid) {
		return ""
	}
	return text
}

func joinConfig(path, name, listen, door string, uid int, p rpc.Principal) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "[troupe]\nname = %q\nprovider = %q\n\n[door.tls]\nlisten = %q\n", name, config.ProviderFigaro, listen)
	if door != listen {
		fmt.Fprintf(&b, "advertise = %q\n", door)
	}
	principal := peercredFor(uid, p)
	if principal != "" {
		fmt.Fprintf(&b, "\n[authz.peercred.uids]\n%q = %q\n", strconv.Itoa(uid), principal)
	}
	snippet := b.String()

	raw := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := toml.Unmarshal(data, &raw); err != nil {
			return snippet, fmt.Errorf("parse %s: %w", path, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return snippet, err
	}
	tr := table(raw, "troupe")
	tr["name"], tr["provider"] = name, config.ProviderFigaro
	delete(tr, "version")
	tlsDoor := table(table(raw, "door"), "tls")
	tlsDoor["listen"] = listen
	delete(tlsDoor, "advertise")
	if door != listen {
		tlsDoor["advertise"] = door
	}
	if principal != "" {
		table(table(table(raw, "authz"), "peercred"), "uids")[strconv.Itoa(uid)] = principal
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return snippet, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config.toml.*")
	if err != nil {
		return snippet, err
	}
	defer os.Remove(tmp.Name())
	if err := toml.NewEncoder(tmp).Encode(raw); err != nil {
		tmp.Close()
		return snippet, err
	}
	if err := tmp.Close(); err != nil {
		return snippet, err
	}
	return snippet, os.Rename(tmp.Name(), path)
}

func table(m map[string]any, key string) map[string]any {
	if t, ok := m[key].(map[string]any); ok {
		return t
	}
	t := map[string]any{}
	m[key] = t
	return t
}

func troupeEndpoint(tr boot.Troupe) (transport.Endpoint, error) {
	u, err := url.Parse(tr.URL)
	if err != nil {
		return transport.Endpoint{}, fmt.Errorf("troupe %q: url %q: %w", tr.Name, tr.URL, err)
	}
	switch u.Scheme {
	case "unix":
		return transport.UnixEndpoint(u.Path), nil
	case "wss":
		return transport.WSSEndpoint(tr.URL, boot.TroupeResolver(tr)), nil
	}
	return transport.Endpoint{}, fmt.Errorf("troupe %q: scheme %q is not one of unix, wss", tr.Name, u.Scheme)
}

func runTroupeJoin(loaded *config.Loaded, name string, o joinOpts) {
	tr, err := boot.LoadTroupe(loaded, name)
	if err != nil {
		die("%s (troupe join reaches a member through troupes/%s.toml)", err, name)
	}
	ep, err := troupeEndpoint(tr)
	if err != nil {
		die("%s", err)
	}
	if err := os.MkdirAll(boot.AriaRoot(), 0o700); err != nil {
		die("%s", err)
	}
	node, err := store.NodeIDAt(boot.AriaRoot())
	if err != nil {
		die("node id: %s", err)
	}
	acli, err := sdk.DialAngelus(ep)
	if err != nil {
		die("troupe %s: %s", name, err)
	}
	defer acli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	o.In, o.Out, o.UID = os.Stdin, stdout, os.Getuid()
	o.Interactive = term.IsTerminal(int(os.Stdin.Fd()))
	if err := troupeJoin(ctx, acli, joinSite{loaded: loaded, stateDir: boot.StateDir(), node: node}, name, o); err != nil {
		die("troupe join %s: %s", name, err)
	}
}

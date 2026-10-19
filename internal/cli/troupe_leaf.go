package cli

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/term"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/troupe"
)

type leafDoor interface {
	TroupeIdentity(ctx context.Context, troupe string) (*rpc.TroupeIdentityResponse, error)
	TroupeEntries(ctx context.Context, req rpc.TroupeEntriesRequest) (*rpc.TroupeEntriesResponse, error)
}

type leafOpts struct {
	For       string
	PublicKey string
	Door      string
	Out       string
}

var leafFiles = []string{"root.crt", "node.crt", "joined.json"}

func troupeLeafFor(ctx context.Context, d leafDoor, in io.Reader, out io.Writer, stateDir, interKey, name string, o leafOpts, now time.Time) error {
	if u, err := uuid.Parse(o.For); err != nil || u.String() != o.For {
		return fmt.Errorf("--for %q is not a node id (a lowercase uuid, as figaro troupe ls shows)", o.For)
	}
	pub, err := parsePublicKey(o.PublicKey)
	if err != nil {
		return err
	}
	if host, _, err := net.SplitHostPort(o.Door); err != nil || host == "" || net.ParseIP(host).IsUnspecified() {
		return fmt.Errorf("--door %q is not a host:port the troupe can dial", o.Door)
	}
	dir, err := leafDir(o.Out)
	if err != nil {
		return err
	}
	key, err := readRoot(in, name)
	if err != nil {
		return err
	}
	defer clear(key)
	root, err := troupe.RootCert(name, key)
	if err != nil {
		return err
	}
	d0 := troupe.DirFor(stateDir, name)
	held, err := d0.Root()
	if err != nil {
		return fmt.Errorf("this node holds no root for troupe %s: issue from the member that does", name)
	}
	if string(held.Raw) != string(root.Raw) {
		return fmt.Errorf("that key is not troupe %s's root", name)
	}
	inter, ikey, err := d0.Intermediate()
	if interKey != "" {
		inter, ikey, err = d0.IntermediateWithKey(interKey)
	}
	if err != nil {
		return fmt.Errorf("this node's intermediate, which issues the leaf: %w", err)
	}
	defer clear(ikey)
	id, err := d.TroupeIdentity(ctx, name)
	if err != nil {
		return err
	}
	if id.Node == o.For {
		return errors.New("--for names this node; its daemon issues its own certificate")
	}
	owner := authz.PrincipalText(id.Principal)
	if err := troupe.ValidOwner(owner); err != nil {
		return fmt.Errorf("no principal to record as the node's owner: %w", err)
	}
	leaf, err := troupe.IssueNode(ikey, inter, name, o.For, pub, now, troupe.NodeTTL)
	if err != nil {
		return err
	}
	join, err := troupe.SignJoin(ikey, inter, name, o.For, o.Door, owner, now)
	if err != nil {
		return err
	}
	renews, err := troupe.SignRootRenewer(key, name, o.For, id.Node, now)
	if err != nil {
		return err
	}
	resp, err := d.TroupeEntries(ctx, rpc.TroupeEntriesRequest{Troupe: name, Entries: troupe.Encode([]troupe.Entry{join, renews})})
	if err != nil {
		return err
	}
	entries, err := json.Marshal(resp.Entries)
	if err != nil {
		return err
	}
	contents := [][]byte{
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root.Raw}),
		append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: inter.Raw})...),
		entries,
	}
	for i, data := range contents {
		if err := writeExclusive(filepath.Join(dir, leafFiles[i]), data); err != nil {
			for _, f := range leafFiles[:i] {
				_ = os.Remove(filepath.Join(dir, f))
			}
			return err
		}
	}
	fmt.Fprintf(out, "troupe %s: root sha256 %x\nnode %s: leaf sha256 %x until %s, for public key sha256 %x, issued by %s, which the root now names its renewer\nwrote %s, %s and %s into %s; they go into that node's troupe dir\n",
		name, sha256.Sum256(root.Raw), o.For, sha256.Sum256(leaf.Raw), leaf.NotAfter.Format("2006-01-02 15:04Z07:00"), sha256.Sum256(pub), id.Node,
		leafFiles[0], leafFiles[1], leafFiles[2], dir)
	writeReach(out, resp.Reached, resp.Unreachable)
	return nil
}

func parsePublicKey(s string) (ed25519.PublicKey, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("--public-key wants the %d-byte Ed25519 key in hex, as figaro troupe keygen prints it", ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

func leafDir(dir string) (string, error) {
	if dir == "" {
		return "", errors.New("--out <dir> receives the node's certificate")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	for _, f := range leafFiles {
		if _, err := os.Lstat(filepath.Join(dir, f)); err == nil {
			return "", fmt.Errorf("%s already holds %s; issue into an empty directory", dir, f)
		}
	}
	return dir, nil
}

func troupeKeygen(out io.Writer, o keyOpts) error {
	dir, created, err := issueDir(o.Out, o.AllowDisk)
	if err != nil {
		return err
	}
	pub, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		return err
	}
	defer clear(key)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	defer clear(der)
	data := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	defer clear(data)
	if err := writeExclusive(filepath.Join(dir, "node.key"), data); err != nil {
		if created {
			_ = os.Remove(dir)
		}
		return err
	}
	fmt.Fprintf(out, "wrote %s (0600); deliver it as the node's credential and remove it\npublic key %s\npublic key sha256 %x\n",
		filepath.Join(dir, "node.key"), hex.EncodeToString(pub), sha256.Sum256(pub))
	return nil
}

func runTroupeLeaf(loaded *config.Loaded, name string, o leafOpts) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		dieUsage("troupe leaf reads the root key on stdin: op read <ref> | figaro troupe leaf %s --for <node-id> --public-key <hex> --door <host:port> --out <dir>", name)
	}
	interKey := ""
	if id := loaded.TroupeIntermediateKeyCredential(); id != "" {
		path, err := boot.CredentialPath(id)
		if err != nil {
			die("troupe.intermediate_key_credential %s", err)
		}
		interKey = path
	}
	acli := localAngelus()
	defer acli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := troupeLeafFor(ctx, acli, os.Stdin, stdout, boot.StateDir(), interKey, name, o, time.Now()); err != nil {
		die("troupe leaf %s: %s", name, err)
	}
}

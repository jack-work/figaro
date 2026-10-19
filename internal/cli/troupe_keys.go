package cli

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	"golang.org/x/term"

	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/troupe"
)

const maxRootPEM = 64 << 10

func readRoot(r io.Reader, name string) (ed25519.PrivateKey, error) {
	if err := troupe.ValidName(name); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(r, maxRootPEM))
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	return troupe.ParseRoot(raw)
}

func troupeCreate(r io.Reader, stateDir, name, node string, now time.Time) (string, error) {
	key, err := readRoot(r, name)
	if err != nil {
		return "", err
	}
	defer clear(key)
	root, err := troupe.RootCert(name, key)
	if err != nil {
		return "", err
	}
	d := troupe.DirFor(stateDir, name)
	held, err := d.Root()
	switch {
	case err == nil && !bytes.Equal(held.Raw, root.Raw):
		return "", fmt.Errorf("troupe %s already exists here with another root (%s)", name, d.RootPath())
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return "", err
	}
	return issueIntermediate(d, key, name, node, now)
}

func troupeIntermediate(r io.Reader, stateDir, name, node string, now time.Time) (string, error) {
	key, err := readRoot(r, name)
	if err != nil {
		return "", err
	}
	defer clear(key)
	root, err := troupe.RootCert(name, key)
	if err != nil {
		return "", err
	}
	d := troupe.DirFor(stateDir, name)
	held, err := d.Root()
	switch {
	case err == nil && !bytes.Equal(held.Raw, root.Raw):
		return "", fmt.Errorf("this key is not troupe %s's root (%s)", name, d.RootPath())
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return "", err
	}
	return issueIntermediate(d, key, name, node, now)
}

func issueIntermediate(d troupe.Dir, rootKey ed25519.PrivateKey, name, node string, now time.Time) (string, error) {
	root, err := troupe.RootCert(name, rootKey)
	if err != nil {
		return "", err
	}
	_, ik, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	inter, err := troupe.IssueIntermediate(rootKey, root, name, node, ik.Public(), now, troupe.IntermediateTTL)
	if err != nil {
		return "", err
	}
	if err := d.WriteRoot(root); err != nil {
		return "", err
	}
	if err := d.WriteIntermediate(inter, ik); err != nil {
		return "", err
	}
	return fmt.Sprintf("troupe %s: root %x\nnode %s: intermediate until %s (%s)\nset [troupe] name = %q in config.toml, then figaro stop; the daemon issues its node certificate\n",
		name, sha256.Sum256(root.Raw), node, inter.NotAfter.Format("2006-01-02"), d.IntermediatePath(), name), nil
}

func runTroupeIssueFor(name string, o keyOpts) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		dieUsage("troupe intermediate --for reads the root key on stdin: op read <ref> | figaro troupe intermediate %s --for %s --out <dir>", name, o.For)
	}
	out, err := troupeIntermediateFor(os.Stdin, boot.StateDir(), name, o, time.Now())
	if err != nil {
		die("%s", err)
	}
	fmt.Fprint(stdout, out)
}

func runTroupeKeyVerb(verb, name string) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		dieUsage("troupe %s reads the root key on stdin: op read <ref> | figaro troupe %s %s", verb, verb, name)
	}
	if err := os.MkdirAll(boot.AriaRoot(), 0o700); err != nil {
		die("%s", err)
	}
	node, err := store.NodeIDAt(boot.AriaRoot())
	if err != nil {
		die("node id: %s", err)
	}
	issue := troupeIntermediate
	if verb == "create" {
		issue = troupeCreate
	}
	out, err := issue(os.Stdin, boot.StateDir(), name, node, time.Now())
	if err != nil {
		die("%s", err)
	}
	fmt.Fprint(stdout, out)
}

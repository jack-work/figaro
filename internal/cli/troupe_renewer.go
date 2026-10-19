package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/term"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/troupe"
)

func troupeRenewer(ctx context.Context, d revokeDoor, in io.Reader, out io.Writer, site joinSite, name, node, renewer string) error {
	key, err := readRoot(in, name)
	if err != nil {
		return err
	}
	defer clear(key)
	root, err := troupe.RootCert(name, key)
	if err != nil {
		return err
	}
	held, err := troupe.DirFor(site.stateDir, name).Root()
	if err != nil {
		return fmt.Errorf("this node holds no root for troupe %s: name a renewer from a member", name)
	}
	if string(held.Raw) != string(root.Raw) {
		return fmt.Errorf("that key is not troupe %s's root", name)
	}
	e, err := troupe.SignRootRenewer(key, name, node, renewer, time.Now())
	if err != nil {
		return err
	}
	resp, err := d.TroupeEntries(ctx, rpc.TroupeEntriesRequest{Troupe: name, Entries: troupe.Encode([]troupe.Entry{e})})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "node %s's certificates may now come from %s's intermediate, as the root says\n", node, renewer)
	writeReach(out, resp.Reached, resp.Unreachable)
	return nil
}

func runTroupeRenewer(loaded *config.Loaded, name, node, renewer string) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		dieUsage("troupe renewer reads the root key on stdin: op read <ref> | figaro troupe renewer %s %s %s", name, node, renewer)
	}
	acli := localAngelus()
	defer acli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := troupeRenewer(ctx, acli, os.Stdin, stdout, localSite(loaded), name, node, renewer); err != nil {
		die("troupe renewer %s %s %s: %s", name, node, renewer, err)
	}
}

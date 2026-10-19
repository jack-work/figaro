package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/BurntSushi/toml"
	"golang.org/x/term"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/troupe"
	"github.com/jack-work/figaro/sdk"
)

type leaveDoor interface {
	TroupeLeave(ctx context.Context, troupe string) (*rpc.TroupeLeaveResponse, error)
}

type revokeDoor interface {
	TroupeEntries(ctx context.Context, req rpc.TroupeEntriesRequest) (*rpc.TroupeEntriesResponse, error)
}

func troupeLeave(ctx context.Context, d leaveDoor, site joinSite, name string, o joinOpts) error {
	have, err := site.loaded.TroupeName()
	if err != nil {
		return err
	}
	if have != name {
		return fmt.Errorf("this node is not in troupe %s", name)
	}
	if !o.Yes {
		if !o.Interactive {
			return errors.New("leaving is final for this node id: pass --yes to leave without a terminal")
		}
		fmt.Fprintf(o.Out, "Node %s leaves troupe %s for good; this node id cannot join it again. Leave? [y/N] ", site.node, name)
		line, _ := bufio.NewReader(o.In).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			return errors.New("not left")
		}
	}
	resp, err := d.TroupeLeave(ctx, name)
	if err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "node %s signed its leave of troupe %s\n", resp.Node, name)
	writeReach(o.Out, resp.Reached, resp.Unreachable)
	if len(resp.Reached) == 0 && len(resp.Unreachable) > 0 {
		fmt.Fprintln(o.Out, "no member heard it; each refuses this node once its certificate expires")
	}
	if err := os.RemoveAll(string(troupe.DirFor(site.stateDir, name))); err != nil {
		return err
	}
	if err := dropTroupeConfig(site.loaded.ConfigPath); err != nil {
		fmt.Fprintf(o.Out, "could not edit %s (%v): remove [troupe] and [door.tls] yourself\n", site.loaded.ConfigPath, err)
		return err
	}
	fmt.Fprintf(o.Out, "removed this node's troupe keys and [troupe] and [door.tls] from %s; restart the daemon: figaro stop\n", site.loaded.ConfigPath)
	return nil
}

func writeReach(out io.Writer, reached []string, missed []rpc.NodeUnreachable) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, n := range reached {
		fmt.Fprintf(w, "  %s\treached\n", n)
	}
	for _, u := range missed {
		fmt.Fprintf(w, "  %s\tnot reached: %s\n", u.Node, u.Error)
	}
	_ = w.Flush()
}

func dropTroupeConfig(path string) error {
	raw := map[string]any{}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := toml.Unmarshal(data, &raw); err != nil {
		return err
	}
	delete(raw, "troupe")
	if door, ok := raw["door"].(map[string]any); ok {
		delete(door, "tls")
		if len(door) == 0 {
			delete(raw, "door")
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config.toml.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := toml.NewEncoder(tmp).Encode(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func troupeRevoke(ctx context.Context, d revokeDoor, in io.Reader, out io.Writer, site joinSite, name, node string) error {
	key, err := readRoot(in, name)
	if err != nil {
		return err
	}
	root, err := troupe.RootCert(name, key)
	if err != nil {
		return err
	}
	held, err := troupe.DirFor(site.stateDir, name).Root()
	if err != nil {
		return fmt.Errorf("this node holds no root for troupe %s: revoke from a member", name)
	}
	if string(held.Raw) != string(root.Raw) {
		return fmt.Errorf("that key is not troupe %s's root", name)
	}
	e, err := troupe.SignRevoke(key, name, node, time.Now())
	if err != nil {
		return err
	}
	resp, err := d.TroupeEntries(ctx, rpc.TroupeEntriesRequest{Troupe: name, Entries: troupe.Encode([]troupe.Entry{e})})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "node %s is revoked from troupe %s, with every join its intermediates signed\n", node, name)
	writeReach(out, resp.Reached, resp.Unreachable)
	return nil
}

func localAngelus() *sdk.Angelus {
	acli, err := sdk.DialAngelus(doorEndpoint())
	if err != nil {
		die("the daemon must be running: %s", err)
	}
	return acli
}

func localSite(loaded *config.Loaded) joinSite {
	node, err := store.NodeIDAt(boot.AriaRoot())
	if err != nil {
		die("node id: %s", err)
	}
	return joinSite{loaded: loaded, stateDir: boot.StateDir(), node: node}
}

func runTroupeLeave(loaded *config.Loaded, name string, o joinOpts) {
	acli := localAngelus()
	defer acli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	o.In, o.Out = os.Stdin, stdout
	o.Interactive = term.IsTerminal(int(os.Stdin.Fd()))
	if err := troupeLeave(ctx, acli, localSite(loaded), name, o); err != nil {
		die("troupe leave %s: %s", name, err)
	}
}

func runTroupeRevoke(loaded *config.Loaded, name, node string) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		dieUsage("troupe revoke reads the root key on stdin: op read <ref> | figaro troupe revoke %s %s", name, node)
	}
	acli := localAngelus()
	defer acli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := troupeRevoke(ctx, acli, os.Stdin, stdout, localSite(loaded), name, node); err != nil {
		die("troupe revoke %s %s: %s", name, node, err)
	}
}

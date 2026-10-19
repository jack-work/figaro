package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/troupe"
	"golang.org/x/term"
)

type policyDoor interface {
	TroupePolicy(ctx context.Context, troupe string) (*rpc.TroupePolicyResponse, error)
	TroupePolicySet(ctx context.Context, troupe, body string) (*rpc.TroupePolicySetResponse, error)
}

func troupePolicyShow(ctx context.Context, d policyDoor, out io.Writer, name string, jsonOut bool) error {
	resp, err := d.TroupePolicy(ctx, name)
	if err != nil {
		return err
	}
	if jsonOut {
		return json.NewEncoder(out).Encode(resp)
	}
	if resp.Troupe == "" {
		fmt.Fprintln(out, "this node is in no troupe")
		return nil
	}
	if resp.Provider != "figaro" {
		where := "none configured ([troupe] policy)"
		if resp.Path != "" {
			where = resp.Path
		}
		fmt.Fprintf(out, "troupe %s (static): policy %s\n", resp.Troupe, where)
	} else if resp.InForce == nil {
		fmt.Fprintf(out, "troupe %s: no troupe policy yet; figaro troupe policy set %s <file>\n", resp.Troupe, resp.Troupe)
	} else {
		v := resp.InForce
		fmt.Fprintf(out, "troupe %s: policy version %d, signed by %s for %s, %s\n", resp.Troupe, v.Version, policySigner(*v), v.By, day(v.At))
	}
	if resp.LoadError != "" {
		fmt.Fprintf(out, "  does not load on this node, so it grants nothing here: %s\n", resp.LoadError)
	}
	for _, r := range resp.Rivals {
		fmt.Fprintf(out, "  lost the tie at version %d: signed by %s for %s, %s\n", r.Version, policySigner(r), r.By, day(r.At))
	}
	for _, r := range resp.Voided {
		fmt.Fprintf(out, "  void, its signer is revoked: version %d by %s\n", r.Version, policySigner(r))
	}
	for _, r := range resp.Refused {
		fmt.Fprintf(out, "  not adopted, %s is not a setter of the version before it: version %d\n", policySigner(r), r.Version)
	}
	fmt.Fprintf(out, "  never granted by the troupe, only by each node's own policy: %s\n", strings.Join(resp.LocalOnly, "; "))
	if len(resp.Nodes) > 0 {
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  NODE\tPOLICY\tSEEN")
		for _, n := range resp.Nodes {
			version, seen := "-", "this node"
			switch {
			case !n.Self && n.SeenAt == 0:
				version, seen = "?", "not since this daemon started"
			case !n.Self:
				seen = ago(time.Since(time.UnixMilli(n.SeenAt)))
			}
			if n.Policy != 0 {
				version = fmt.Sprint(n.Policy)
			}
			fmt.Fprintf(w, "  %s\t%s\t%s\n", n.Node, version, seen)
		}
		_ = w.Flush()
	}
	if resp.Body != "" {
		fmt.Fprintf(out, "\n%s", resp.Body)
		if !strings.HasSuffix(resp.Body, "\n") {
			fmt.Fprintln(out)
		}
	}
	return nil
}

func troupePolicySet(ctx context.Context, d policyDoor, in io.Reader, out io.Writer, name, file string) error {
	var body []byte
	var err error
	if file == "-" {
		body, err = io.ReadAll(io.LimitReader(in, troupe.MaxPolicyBody+1))
	} else {
		body, err = os.ReadFile(file)
	}
	if err != nil {
		return err
	}
	resp, err := d.TroupePolicySet(ctx, name, string(body))
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "troupe %s policy version %d, signed by %s\n", name, resp.Version, resp.Signer)
	writeReach(out, resp.Reached, resp.Unreachable)
	return nil
}

func policySigner(v rpc.TroupePolicyVersion) string {
	if v.Root {
		return "the root"
	}
	return "node " + v.Signer
}

type rootPolicyDoor interface {
	TroupePolicy(ctx context.Context, troupe string) (*rpc.TroupePolicyResponse, error)
	TroupeEntries(ctx context.Context, req rpc.TroupeEntriesRequest) (*rpc.TroupeEntriesResponse, error)
}

func troupeRootPolicySet(ctx context.Context, d rootPolicyDoor, in io.Reader, out io.Writer, stateDir, name, file string) error {
	if file == "-" {
		return fmt.Errorf("with --root the root key is on stdin, so the policy is a file")
	}
	body, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	if len(body) > troupe.MaxPolicyBody {
		return fmt.Errorf("a troupe policy holds at most %d bytes", troupe.MaxPolicyBody)
	}
	if err := authz.CheckTroupeDocument(body, name, nil); err != nil {
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
	held, err := troupe.DirFor(stateDir, name).Root()
	if err != nil {
		return fmt.Errorf("this node holds no root for troupe %s: set the policy from a member", name)
	}
	if string(held.Raw) != string(root.Raw) {
		return fmt.Errorf("that key is not troupe %s's root", name)
	}
	cur, err := d.TroupePolicy(ctx, name)
	if err != nil {
		return err
	}
	version := uint64(1)
	if cur.InForce != nil {
		version = cur.InForce.Version + 1
	}
	e, err := troupe.SignRootPolicy(key, name, version, "root:"+name, string(body), time.Now())
	if err != nil {
		return err
	}
	resp, err := d.TroupeEntries(ctx, rpc.TroupeEntriesRequest{Troupe: name, Entries: troupe.Encode([]troupe.Entry{e})})
	if err != nil {
		return err
	}
	setters, _ := troupe.PolicySetters(string(body))
	fmt.Fprintf(out, "troupe %s policy version %d, signed by the root; setters: %v\n", name, version, setters)
	writeReach(out, resp.Reached, resp.Unreachable)
	return nil
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func runTroupePolicy(loaded *config.Loaded, args []string, jsonOut, withRoot bool) {
	acli := localAngelus()
	defer acli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	switch {
	case len(args) == 2 && args[0] == "show":
		if err := troupePolicyShow(ctx, acli, stdout, args[1], jsonOut); err != nil {
			die("troupe policy show %s: %s", args[1], err)
		}
	case len(args) == 3 && args[0] == "set" && withRoot:
		if term.IsTerminal(int(os.Stdin.Fd())) {
			dieUsage("troupe policy set --root reads the root key on stdin: op read <ref> | figaro troupe policy set %s <file> --root", args[1])
		}
		if err := troupeRootPolicySet(ctx, acli, os.Stdin, stdout, localSite(loaded).stateDir, args[1], args[2]); err != nil {
			die("troupe policy set %s: %s", args[1], err)
		}
	case withRoot:
		dieUsage("--root goes with troupe policy set <name> <file>")
	case len(args) == 3 && args[0] == "set":
		if err := troupePolicySet(ctx, acli, os.Stdin, stdout, args[1], args[2]); err != nil {
			die("troupe policy set %s: %s", args[1], err)
		}
	default:
		dieUsage("troupe policy: want show <name> or set <name> <file|->")
	}
}

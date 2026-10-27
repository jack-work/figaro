package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/jkauthz"
)

const (
	delegateAll  = "all"
	delegateNone = "none"
)

func checkDelegate(o joinOpts) error {
	switch o.Delegate {
	case delegateAll, delegateNone:
		return nil
	case "":
		if o.Interactive {
			return nil
		}
		return errors.New("--delegate all or --delegate none is required: what this node lends the troupe policy. all lets the troupe's grants apply here as if this node's own policy made them; none lets them apply nowhere until you lend rights in policy.toml")
	}
	return fmt.Errorf("--delegate %q: want all or none", o.Delegate)
}

func askDelegate(name string, o joinOpts) (string, error) {
	if o.Delegate != "" {
		return o.Delegate, nil
	}
	fmt.Fprintf(o.Out, "What does this node lend troupe %s's policy?\n  all   its grants apply here as if this node's own policy made them\n  none  they apply nowhere here until you lend rights to troupe:%s in policy.toml\n(use on sandbox:local and admin on the troupe stay this node's own either way)\nall or none? ", name, name)
	switch a := strings.ToLower(strings.TrimSpace(readLine(o.In))); a {
	case delegateAll, delegateNone:
		return a, nil
	}
	return "", errors.New("neither all nor none; nothing was signed or written")
}

func readLine(in io.Reader) string {
	var b strings.Builder
	one := make([]byte, 1)
	for {
		n, err := in.Read(one)
		if n == 1 {
			if one[0] == '\n' {
				break
			}
			b.WriteByte(one[0])
		}
		if err != nil {
			break
		}
	}
	return b.String()
}

func selfPrincipal(uid int, p rpc.Principal) string {
	if mapped := peercredFor(uid, p); mapped != "" {
		return mapped
	}
	return "user:" + strconv.Itoa(uid)
}

const lentRole = "lent-to-troupe"

func writeDelegation(out io.Writer, loaded *config.Loaded, name, lend, self string) error {
	troupeP := authz.TroupePrincipal(name).String()
	if lend == delegateNone {
		fmt.Fprintf(out, "this node lends troupe %s nothing: its policy grants nothing here until policy.toml binds %s to a role\n", name, troupeP)
		return nil
	}
	path := loaded.PolicyPath()
	var existing []byte
	if path != "" {
		var err error
		if existing, err = os.ReadFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if policyBinds(existing, troupeP) {
		fmt.Fprintf(out, "%s already binds %s; left as it is\n", path, troupeP)
		return nil
	}
	var add string
	if len(existing) == 0 {
		add = authz.DefaultPolicy(mustPrincipal(self), loaded.DooredBoxes())
	}
	add += fmt.Sprintf("\n[roles.%s]\ngrants = [{ verbs = [\"*\"], on = [\"*\"] }]\n\n[[bind]]\nrole = %q\nprincipals = [%q]\n", lentRole, lentRole, troupeP)
	if path == "" {
		path = filepath.Join(loaded.ConfigDir, "policy.toml")
		if err := setPolicyPath(loaded.ConfigPath, "policy.toml"); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(add); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(out, "this node lends troupe %s everything; added to %s:\n%s\n", name, path, add)
	return nil
}

func mustPrincipal(s string) jkauthz.Principal {
	p, err := jkauthz.ParsePrincipal(s)
	if err != nil {
		return jkauthz.Principal{Kind: jkauthz.KindUser, ID: s}
	}
	return p
}

func policyBinds(policy []byte, principal string) bool {
	var doc struct {
		Bind []struct {
			Principals []string `toml:"principals"`
		} `toml:"bind"`
	}
	if _, err := toml.Decode(string(policy), &doc); err != nil {
		return false
	}
	for _, b := range doc.Bind {
		for _, p := range b.Principals {
			if p == principal {
				return true
			}
		}
	}
	return false
}

func setPolicyPath(configPath, rel string) error {
	raw := map[string]any{}
	if data, err := os.ReadFile(configPath); err == nil {
		if err := toml.Unmarshal(data, &raw); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	table(raw, "authz")["policy"] = rel
	tmp, err := os.CreateTemp(filepath.Dir(configPath), ".config.toml.*")
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
	return os.Rename(tmp.Name(), configPath)
}

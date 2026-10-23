package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"

	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/config"
	providerPkg "github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/sdk"
)

func lookupProvider(name string) (*providerPkg.Registration, error) {
	reg := providerPkg.Lookup(name)
	if reg == nil {
		return nil, fmt.Errorf("no provider %q (figaro provider ls)", name)
	}
	return reg, nil
}

func providerSources(loaded *config.Loaded, name string, store oauthStore) ([]boot.CredentialSource, error) {
	reg, err := lookupProvider(name)
	if err != nil {
		return nil, err
	}
	var names []string
	if store != nil && boot.ConsultsHushOAuth(loaded, reg) {
		if names, err = store.OAuthList(); err != nil {
			return nil, fmt.Errorf("hush: %w", err)
		}
	}
	return boot.CredentialSources(loaded, reg, names)
}

type logoutReport struct {
	Removed   []boot.CredentialSource `json:"removed"`
	Remaining []boot.CredentialSource `json:"remaining"`
}

func providerLogout(loaded *config.Loaded, name string, store oauthStore) (logoutReport, error) {
	var rep logoutReport
	srcs, err := providerSources(loaded, name, store)
	if err != nil {
		return rep, err
	}
	for _, s := range srcs {
		if !s.Owned {
			rep.Remaining = append(rep.Remaining, s)
			continue
		}
		switch s.Kind {
		case boot.SourceOAuth:
			err = store.OAuthDelete(name)
		case boot.SourceSealedKey:
			err = removeSealedKey(s.Where)
		}
		if err != nil {
			return rep, fmt.Errorf("remove %s: %w", describeSource(s), err)
		}
		rep.Removed = append(rep.Removed, s)
	}
	return rep, nil
}

var sealedKeyLine = regexp.MustCompile(`^\s*api_key\s*=\s*"AGE-ENC\[[^"]*"\s*(#.*)?$`)

func removeSealedKey(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.SplitAfter(string(data), "\n")
	kept := lines[:0:0]
	removed := 0
	inTable := false
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "[") {
			inTable = true
		}
		if !inTable && sealedKeyLine.MatchString(strings.TrimRight(l, "\r\n")) {
			removed++
			continue
		}
		kept = append(kept, l)
	}
	if removed != 1 {
		return fmt.Errorf("%s: expected one sealed api_key line, found %d; edit it by hand", path, removed)
	}
	rest := strings.Join(kept, "")
	if strings.TrimSpace(rest) == "" {
		return os.Remove(path)
	}
	return os.WriteFile(path, []byte(rest), 0o600)
}

func describeSource(s boot.CredentialSource) string {
	switch s.Kind {
	case boot.SourceEnv:
		return "environment " + s.Where
	case boot.SourceKeyFile:
		return "api_key_file " + s.Where
	case boot.SourceCredDir:
		return "credential file " + s.Where
	case boot.SourcePlainKey:
		return "plaintext api_key in " + s.Where
	case boot.SourceSealedKey:
		return "sealed api_key in " + s.Where
	case boot.SourceOAuth, boot.SourceService:
		return s.Where
	}
	return s.Kind + " " + s.Where
}

func leaveHint(s boot.CredentialSource) string {
	switch s.Kind {
	case boot.SourceEnv:
		return "unset " + strings.TrimPrefix(s.Where, "$") + " where it is exported"
	case boot.SourceKeyFile:
		return "remove api_key_file from the provider file, or the file it names"
	case boot.SourceCredDir:
		return "the service manager supplies it; drop the LoadCredential line"
	case boot.SourcePlainKey:
		return "you wrote it; delete the api_key line"
	case boot.SourceService:
		return "the daemon mints it; delete the service_token line"
	}
	return ""
}

func runProviderLogin(loaded *config.Loaded, name string) {
	reg, err := lookupProvider(name)
	if err != nil {
		dieUsage("%s", err)
	}
	if st, err := boot.ServiceTokenOf(loaded, name); err != nil || st != "" {
		if err != nil {
			die("%s", err)
		}
		die("%s authenticates as service token %s ([service_tokens.%s] in config.toml); there is nothing to log in", name, st, st)
	}
	switch {
	case reg.Login != nil:
		err = reg.Login(loaded)
	case reg.OAuth != nil:
		err = runOAuthLogin(name, name, *reg.OAuth,
			"figaro set system.provider "+name+"; figaro set system.model <model>")
	default:
		ensureHush()
		err = runAPIKeyInline(loaded, name)
	}
	if err != nil {
		die("%s", err)
	}
	relistProvider(name)
}

func relistProvider(name string) {
	acli, err := sdk.DialAngelus(transport.UnixEndpoint(boot.AngelusSocketPath()))
	if err != nil {
		return
	}
	defer acli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	_, _ = acli.ModelsRefresh(ctx, false, name)
}

func runProviderLogout(loaded *config.Loaded, name string, jsonOut bool) {
	reg, err := lookupProvider(name)
	if err != nil {
		dieUsage("%s", err)
	}
	var store oauthStore = noOAuth{}
	if boot.ConsultsHushOAuth(loaded, reg) {
		if store, err = cliOAuthStore(true); err != nil {
			die("hush: %s", err)
		}
	}
	rep, err := providerLogout(loaded, name, store)
	if err != nil {
		die("%s", err)
	}
	relistProvider(name)
	if jsonOut {
		_ = json.NewEncoder(stdout).Encode(rep)
	} else {
		printLogout(name, rep)
	}
	if len(rep.Remaining) > 0 {
		exitNow(1)
	}
}

func printLogout(name string, rep logoutReport) {
	if len(rep.Removed) == 0 && len(rep.Remaining) == 0 {
		fmt.Fprintf(stdout, "%s: not signed in\n", name)
		return
	}
	for _, s := range rep.Removed {
		fmt.Fprintf(stdout, "removed  %s\n", describeSource(s))
	}
	if len(rep.Removed) > 0 {
		fmt.Fprintf(stdout, "         figaro's copy is gone; what %s issued stays valid there until you revoke it\n", name)
	}
	for _, s := range rep.Remaining {
		fmt.Fprintf(stdout, "kept     %s: %s\n", describeSource(s), leaveHint(s))
	}
	if len(rep.Remaining) > 0 {
		fmt.Fprintf(stderrw, "%s is still signed in through %s\n", name, describeSource(rep.Remaining[0]))
	}
}

type providerRow struct {
	Name    string                  `json:"name"`
	Sources []boot.CredentialSource `json:"sources"`
}

func runProviderList(loaded *config.Loaded, name string, jsonOut bool) {
	names := providerPkg.Names()
	if name != "" {
		if _, err := lookupProvider(name); err != nil {
			dieUsage("%s", err)
		}
		names = []string{name}
	}
	store, hushErr := cliOAuthStore(false)
	rows := make([]providerRow, 0, len(names))
	for _, n := range names {
		srcs, err := providerSources(loaded, n, store)
		if err != nil {
			die("%s", err)
		}
		rows = append(rows, providerRow{Name: n, Sources: srcs})
	}
	if jsonOut {
		_ = json.NewEncoder(stdout).Encode(rows)
	} else if name != "" {
		printProviderDetail(rows[0])
	} else {
		printProviderTable(rows)
	}
	if hushErr != nil {
		fmt.Fprintf(stderrw, "hush unavailable (%s): oauth credentials not shown\n", hushErr)
	}
}

func printProviderTable(rows []providerRow) {
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PROVIDER\tIN USE\tALSO")
	for _, r := range rows {
		inUse, also := "-", "-"
		if len(r.Sources) > 0 {
			inUse = describeSource(r.Sources[0])
		}
		if len(r.Sources) > 1 {
			rest := make([]string, 0, len(r.Sources)-1)
			for _, s := range r.Sources[1:] {
				rest = append(rest, describeSource(s))
			}
			also = strings.Join(rest, "; ")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", r.Name, inUse, also)
	}
	_ = w.Flush()
}

func printProviderDetail(r providerRow) {
	if len(r.Sources) == 0 {
		fmt.Fprintf(stdout, "%s: no credential (figaro provider login %s)\n", r.Name, r.Name)
		return
	}
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	for i, s := range r.Sources {
		mark := ""
		if i == 0 {
			mark = "in use"
		}
		owner := "logout removes it"
		if !s.Owned {
			owner = leaveHint(s)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", mark, describeSource(s), owner)
	}
	_ = w.Flush()
}

func runProviderVerb(loaded *config.Loaded, args []string, jsonOut bool) error {
	verb, rest := args[0], args[1:]
	switch verb {
	case "ls":
		if len(rest) > 1 {
			dieUsage("provider ls takes at most one provider")
		}
		name := ""
		if len(rest) == 1 {
			name = rest[0]
		}
		runProviderList(loaded, name, jsonOut)
	case "login", "logout":
		if len(rest) != 1 {
			dieUsage("provider %s takes one provider (figaro provider ls)", verb)
		}
		if verb == "login" {
			runProviderLogin(loaded, rest[0])
		} else {
			runProviderLogout(loaded, rest[0], jsonOut)
		}
	default:
		dieUsage("provider: want login, logout or ls, not %q", verb)
	}
	return nil
}

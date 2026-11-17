package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/sdk"
)

const (
	roleModeCheck = "check"
	roleModeApply = "apply"
)

type roleManifest struct {
	Roles map[string]manifestRole `toml:"roles"`
}

type manifestRole struct {
	ID      string   `toml:"id"`
	Outfits []string `toml:"outfits"`
	Managed []string `toml:"managed"`
	Mode    string   `toml:"mode"`
}

type plannedRole struct {
	name    string
	request rpc.FormApplyRequest
	check   bool
}

type manifestResult struct {
	Role string `json:"role"`
	rpc.FormApplyResponse
	Error string `json:"error,omitempty"`
}

var exactRoleID = regexp.MustCompile(`^@([0-9a-f]{8}|[0-9a-f]{32})$`)

const formApplyDeadline = 30 * time.Second

func runFormApply(id, outfits, managed string, check, asJSON bool) error {
	if id == "" || outfits == "" || strings.TrimSpace(managed) == "" {
		return fmt.Errorf("form apply requires --id, --outfit and --managed; it never creates a role")
	}
	if !exactRoleID.MatchString(id) {
		return fmt.Errorf("form apply: --id %q is not an exact role id (@ and 32 hex digits, or 8 for a legacy role)", id)
	}
	dress, err := parseDress(outfits, "", "")
	if err != nil {
		return err
	}
	paths := strings.Split(managed, ",")
	for i := range paths {
		paths[i] = strings.TrimSpace(paths[i])
	}
	ctx, cancel := context.WithTimeout(context.Background(), formApplyDeadline)
	defer cancel()
	client, err := dialFormApply(ctx)
	if err != nil {
		return err
	}
	defer client.Close()
	result, err := applyRole(ctx, client, rpc.FormApplyRequest{FigaroID: id, Outfits: dress.names, Managed: paths}, check)
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(stdout).Encode(result)
	}
	printApplyResult("", result)
	return nil
}

func runFormApplyManifest(path string, check, asJSON bool) error {
	roles, err := loadRoleManifest(path, check)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), formApplyDeadline)
	defer cancel()
	client, err := dialFormApply(ctx)
	if err != nil {
		return err
	}
	defer client.Close()
	failed := 0
	enc := json.NewEncoder(stdout)
	for _, role := range roles {
		result, err := applyRole(ctx, client, role.request, role.check)
		row := manifestResult{Role: role.name, FormApplyResponse: result}
		if err != nil {
			failed++
			row.FigaroID, row.Error = role.request.FigaroID, err.Error()
		}
		switch {
		case asJSON:
			if err := enc.Encode(row); err != nil {
				return err
			}
		case err != nil:
			fmt.Fprintf(stdout, "%s %s: error: %s\n", role.name, row.FigaroID, row.Error)
		default:
			printApplyResult(role.name+" ", result)
		}
	}
	switch {
	case failed > 0 && check:
		return fmt.Errorf("form apply: %d of %d roles failed their check", failed, len(roles))
	case failed > 0:
		return fmt.Errorf("form apply: %d of %d roles failed; re-run with --check before retrying", failed, len(roles))
	}
	return nil
}

func loadRoleManifest(path string, forceCheck bool) ([]plannedRole, error) {
	var manifest roleManifest
	meta, err := toml.DecodeFile(path, &manifest)
	if err != nil {
		return nil, fmt.Errorf("form apply manifest: %w", err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, key := range undecoded {
			keys[i] = key.String()
		}
		return nil, fmt.Errorf("form apply manifest: unknown keys %s", strings.Join(keys, ", "))
	}
	if len(manifest.Roles) == 0 {
		return nil, fmt.Errorf("form apply manifest: no roles declared")
	}
	names := make([]string, 0, len(manifest.Roles))
	for name := range manifest.Roles {
		names = append(names, name)
	}
	sort.Strings(names)
	seen := map[string]string{}
	planned := make([]plannedRole, 0, len(names))
	for _, name := range names {
		role := manifest.Roles[name]
		if !exactRoleID.MatchString(role.ID) {
			return nil, fmt.Errorf("form apply manifest: %s: id %q is not an exact role id (@ and 32 hex digits, or 8 for a legacy role)", name, role.ID)
		}
		if other, dup := seen[role.ID]; dup {
			return nil, fmt.Errorf("form apply manifest: %s and %s both declare %s", other, name, role.ID)
		}
		seen[role.ID] = name
		if len(role.Outfits) == 0 || len(role.Managed) == 0 {
			return nil, fmt.Errorf("form apply manifest: %s needs outfits and managed paths", name)
		}
		dress, err := parseDress(strings.Join(role.Outfits, ","), "", "")
		if err != nil {
			return nil, fmt.Errorf("form apply manifest: %s: %w", name, err)
		}
		mode := role.Mode
		if mode == "" {
			mode = roleModeCheck
		}
		if mode != roleModeCheck && mode != roleModeApply {
			return nil, fmt.Errorf("form apply manifest: %s: mode %q is neither check nor apply", name, role.Mode)
		}
		planned = append(planned, plannedRole{
			name:    name,
			request: rpc.FormApplyRequest{FigaroID: role.ID, Outfits: dress.names, Managed: slices.Clone(role.Managed)},
			check:   forceCheck || mode == roleModeCheck,
		})
	}
	return planned, nil
}

func dialFormApply(ctx context.Context) (*sdk.Angelus, error) {
	client, err := sdk.DialAngelus(doorEndpoint())
	if err != nil {
		return nil, fmt.Errorf("form apply: daemon unavailable; application deferred: %w", err)
	}
	hello, err := client.Hello(ctx)
	if err != nil {
		client.Close()
		return nil, err
	}
	if !slices.Contains(hello.Capabilities, rpc.CapFormApply) {
		client.Close()
		return nil, fmt.Errorf("form apply requires daemon capability %s; no changes were made", rpc.CapFormApply)
	}
	return client, nil
}

func applyRole(ctx context.Context, client *sdk.Angelus, req rpc.FormApplyRequest, check bool) (rpc.FormApplyResponse, error) {
	req.Check, req.IfVersion, req.ExpectedOutfit = true, 0, ""
	checked, err := client.FormApply(ctx, req)
	if err != nil {
		return rpc.FormApplyResponse{}, err
	}
	if check {
		return *checked, nil
	}
	req.Check, req.IfVersion, req.ExpectedOutfit = false, checked.Version, checked.Outfit
	applied, err := client.FormApply(ctx, req)
	if err != nil {
		return rpc.FormApplyResponse{}, fmt.Errorf("form apply: %w; re-read with --check before retrying", err)
	}
	return *applied, nil
}

func printApplyResult(prefix string, result rpc.FormApplyResponse) {
	fmt.Fprintf(stdout, "%s%s: %s (version %d)\n", prefix, result.FigaroID, result.Outcome, result.Version)
	if len(result.Set) > 0 {
		fmt.Fprintf(stdout, "  set: %s\n", strings.Join(result.Set, ", "))
	}
	if len(result.Remove) > 0 {
		fmt.Fprintf(stdout, "  remove: %s\n", strings.Join(result.Remove, ", "))
	}
}

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/sdk"
)

func runFormApply(id, outfits, managed string, check, asJSON bool) error {
	if id == "" || outfits == "" || strings.TrimSpace(managed) == "" {
		return fmt.Errorf("form apply requires --id, --outfit and --managed; it never creates a role")
	}
	dress, err := parseDress(outfits, "", "")
	if err != nil {
		return err
	}
	paths := strings.Split(managed, ",")
	for i := range paths {
		paths[i] = strings.TrimSpace(paths[i])
	}
	client, err := sdk.DialAngelus(doorEndpoint())
	if err != nil {
		return fmt.Errorf("form apply: daemon unavailable; application deferred: %w", err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hello, err := client.Hello(ctx)
	if err != nil {
		return err
	}
	if !slices.Contains(hello.Capabilities, rpc.CapFormApply) {
		return fmt.Errorf("form apply requires daemon capability %s; no changes were made", rpc.CapFormApply)
	}
	req := rpc.FormApplyRequest{FigaroID: id, Outfits: dress.names, Managed: paths, Check: true}
	result, err := client.FormApply(ctx, req)
	if err != nil {
		return err
	}
	if !check {
		req.Check, req.IfVersion, req.ExpectedOutfit = false, result.Version, result.Outfit
		result, err = client.FormApply(ctx, req)
		if err != nil {
			return fmt.Errorf("form apply: %w; re-read with --check before retrying", err)
		}
	}
	if asJSON {
		return json.NewEncoder(stdout).Encode(result)
	}
	fmt.Fprintf(stdout, "%s: %s (version %d)\n", result.FigaroID, result.Outcome, result.Version)
	if len(result.Set) > 0 {
		fmt.Fprintf(stdout, "set: %s\n", strings.Join(result.Set, ", "))
	}
	if len(result.Remove) > 0 {
		fmt.Fprintf(stdout, "remove: %s\n", strings.Join(result.Remove, ", "))
	}
	return nil
}

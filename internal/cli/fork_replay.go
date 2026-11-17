package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/sdk"
)

func refuseReplayPlan(plan forkPlan) error {
	_, at, err := parseTarget(plan.spec)
	if err != nil {
		return err
	}
	if at.turn == 0 || at.lt > 0 || (at.hasNode && at.node != inquiryNode) {
		return errors.New("--replay re-runs one past turn: name it as <id>:<turn>")
	}
	if plan.hasPrompt() {
		return errors.New("--replay re-sends the turn's own inquiry; drop the prompt")
	}
	if bad := forkPromptOnlyFlags(plan.opts); bad != "" {
		return fmt.Errorf("%s does not apply to --replay, which submits and exits; follow the branch with `figaro listen <id>`", bad)
	}
	return nil
}

func runForkReplay(loaded *config.Loaded, plan forkPlan) {
	target, at, err := parseTarget(plan.spec)
	if err != nil {
		dieUsage("fork: %s", err)
	}
	WithAngelus(loaded, func(acli *sdk.Angelus) error {
		ctx := context.Background()
		if target == "" {
			r, rerr := resolveBinding(ctx, acli, shellPID)
			if rerr != nil || !r.Found {
				dieUsage("fork: no aria bound to this shell (name one: <id>:<turn> --replay)")
			}
			target = r.FigaroID
		}
		if err := refuseUnknownFields(rpc.ForkRequest{FigaroID: target, AtTurn: at.turn, Replay: true}); err != nil {
			die("fork: %s", err)
		}
		resp, err := acli.Replay(ctx, target, at.turn, plan.opts.outfit.names, plan.opts.outfit.patch)
		if err != nil {
			die("fork: %s", err)
		}
		if plan.opts.json {
			return json.NewEncoder(stdout).Encode(struct {
				AriaID string `json:"aria_id"`
				Handle string `json:"handle"`
				Parent string `json:"parent"`
				Turn   uint64 `json:"turn"`
				Mode   string `json:"mode"`
			}{resp.Alternative, ariaHandle(resp.Alternative), resp.Parent, at.turn, "replay"})
		}
		fmt.Fprintf(stderrw, "replaying %s turn %d on %s\n  follow: figaro listen %s\n",
			resp.Parent, at.turn, resp.Alternative, resp.Alternative)
		return nil
	})
}

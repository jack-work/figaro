package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jack-work/figaro/sdk"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/config"
)

// d is the birth outfit when this mints an aria, and rides the prompt as a form
// patch when the shell is already bound.
func runPrompt(loaded *config.Loaded, d dressing, prompt string, set renderSettings) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	ppid := shellPID

	resp, err := resolveBinding(ctx, acli, ppid)
	if err != nil {
		die("resolve: %s", err)
	}

	var figaroID string
	var figaroEP sdk.Addr

	if resp.Found {
		// A pending fork-point binding (attend <id>:<LT>) is one-shot: the
		// rebind clears it.
		if resp.AtMainLT > 0 {
			runSendForkAt(loaded, resp.FigaroID, forkPoint{lt: resp.AtMainLT}, false, false, prompt, set, d)
			return
		}
		figaroID = resp.FigaroID
		figaroEP = ariaAddr(figaroID)
		// Attending a role resolves the holder per call.
		var rerr error
		figaroID, figaroEP, rerr = redirectRole(ctx, loaded, acli, figaroID, figaroEP)
		if rerr != nil {
			die("%s", rerr)
		}
	} else {
		figaroID, figaroEP = mustCreate(ctx, acli, loaded, d)
		attendNew(ctx, acli, figaroID)
	}
	mustPromptFigaro(ctx, figaroEP, figaroID, prompt, loaded, set, d)
}

// Under jsonMode the aria is created, prompted fire-and-forget, and one JSON line
// is printed.
func runNewPrompt(loaded *config.Loaded, prompt string, d dressing, set renderSettings) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	ppid := shellPID
	unbindBinding(ctx, acli, ppid)

	figaroID, figaroEP := mustCreate(ctx, acli, loaded, d)
	attendNew(ctx, acli, figaroID)

	if set.jsonMode {
		fcli, derr := sdk.DialAria(figaroEP, func(string, json.RawMessage) {})
		if derr != nil {
			die("connect figaro: %s", derr)
		}
		defer fcli.Close()
		qctx, qcancel := context.WithTimeout(ctx, 10*time.Second)
		if _, _, qerr := fcli.Qua(qctx, prompt, buildPromptForm(d)); qerr != nil {
			qcancel()
			dieWithClosure(qerr, "prompt: %s", qerr)
		}
		qcancel()
		enc := json.NewEncoder(stdout)
		_ = enc.Encode(struct {
			AriaID string `json:"aria_id"`
			Handle string `json:"handle"`
			Mode   string `json:"mode"`
		}{AriaID: figaroID, Handle: ariaName(figaroID), Mode: "new"})
		return
	}

	// Nothing was bound, so the id goes to stderr for callers to capture.
	if bindingDisabled() {
		fmt.Fprintf(stderrw, "created %s\n", ariaName(figaroID))
	}
	mustPromptFigaro(ctx, figaroEP, figaroID, prompt, loaded, set, d)
}

// submitAndExit is the tail of every --json path.
func submitAndExit(ctx context.Context, loaded *config.Loaded, ariaID, prompt string, d dressing) {
	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	ep, err := resolveAria(ctx, acli, ariaID)
	if err != nil {
		die("%s", err)
	}

	fcli, derr := sdk.DialAria(ep, func(string, json.RawMessage) {})
	if derr != nil {
		die("connect figaro: %s", derr)
	}
	defer fcli.Close()

	qctx, qcancel := context.WithTimeout(ctx, 10*time.Second)
	defer qcancel()
	if _, _, qerr := fcli.Qua(qctx, prompt, buildPromptForm(d)); qerr != nil {
		dieWithClosure(qerr, "prompt: %s", qerr)
	}
}

// Forks the trunk at the turn, then sends to the alternative (rebinding the shell)
// or, with stay, to the original trunk.
func runSendForkAt(loaded *config.Loaded, trunkID string, at forkPoint, stay, asJSON bool, prompt string, set renderSettings, d dressing) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	ppid := shellPID
	if trunkID == "" {
		r, err := resolveBinding(ctx, acli, ppid)
		if err != nil || !r.Found {
			die("send: no trunk bound to this shell (try: <id>:<turn> or attend <id>)")
		}
		trunkID = r.FigaroID
	}

	// The coordinate goes on the wire as named; the server owns the turn-to-LT
	// translation.
	fr, err := waitForFork(ctx, acli, trunkID, at, dressing{}, prompt)
	if err != nil {
		die("send: fork %s at %s: %s", trunkID, at, err)
	}
	if fr.OwnerNote != "" {
		fmt.Fprintf(stderrw, "%s\n", fr.OwnerNote)
	}

	target := fr.Alternative
	if stay {
		target = trunkID
		if !asJSON {
			fmt.Fprintf(stderrw, "forked %s at %s -> %s (parked; staying on %s)\n", ariaName(trunkID), at, ariaName(fr.Alternative), ariaName(trunkID))
		}
	} else {
		// Registry.Bind rebinds in place; no Unbind needed first.
		if err := bindBinding(ctx, acli, ppid, fr.Alternative, 0); err != nil {
			fmt.Fprintf(stderrw, "warning: could not attend %s: %s\n", fr.Alternative, err)
		}
		if !asJSON {
			fmt.Fprintf(stderrw, "forked %s at %s -> attending %s\n", ariaName(trunkID), at, ariaName(fr.Alternative))
		}
	}

	if asJSON {
		enc := json.NewEncoder(stdout)
		_ = enc.Encode(struct {
			AriaID       string `json:"aria_id"`
			Handle       string `json:"handle"`
			Parent       string `json:"parent"`
			Alternative  string `json:"alternative"`
			Continuation string `json:"continuation"`
			Turn         uint64 `json:"turn"`
			Node         *int   `json:"node,omitempty"`
			Mode         string `json:"mode"`
		}{
			AriaID:       target,
			Handle:       ariaName(target),
			Parent:       fr.Parent,
			Alternative:  fr.Alternative,
			Continuation: fr.Continuation,
			Turn:         at.turn,
			Node:         at.nodeJSON(),
			Mode:         "fork-send",
		})
		// --json prints the object and nothing else.
		submitAndExit(ctx, loaded, target, prompt, d)
		return
	}

	ep, err := resolveAria(ctx, acli, target)
	if err != nil {
		die("%s", err)
	}
	mustPromptFigaro(ctx, ep, ep.ID, prompt, loaded, set, d)
}

func promptAria(loaded *config.Loaded, ariaID, prompt string, set renderSettings, d dressing) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	ep, err := resolveAria(ctx, acli, ariaID)
	if err != nil {
		die("%s", err)
	}
	mustPromptFigaro(ctx, ep, ep.ID, prompt, loaded, set, d)
}

// Aria ids are system-minted: an unknown id is an error, not a new conversation.
func resolveAria(ctx context.Context, acli *sdk.Angelus, ariaID string) (sdk.Addr, error) {
	attachCtx, attachCancel := context.WithTimeout(ctx, 10*time.Second)
	resp, err := acli.Attach(attachCtx, ariaID)
	attachCancel()
	if err == nil {
		return ariaAddr(resp.FigaroID), nil
	}
	if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "not in tree") {
		return sdk.Addr{}, fmt.Errorf("no such aria %q (ids are system-minted; run `figaro` to start one)", ariaID)
	}
	return sdk.Addr{}, fmt.Errorf("attach %q: %w", ariaID, err)
}

// An empty dressing is the configured default_outfit, resolved server-side.
func runNewFromOutfit(loaded *config.Loaded, d dressing, set renderSettings) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	acli := mustConnectAngelus(loaded)
	defer acli.Close()
	ppid := shellPID
	unbindBinding(ctx, acli, ppid)
	figaroID, _ := mustCreate(ctx, acli, loaded, d)
	attendNew(ctx, acli, figaroID)
	if set.jsonMode {
		enc := json.NewEncoder(stdout)
		_ = enc.Encode(struct {
			AriaID string `json:"aria_id"`
			Handle string `json:"handle"`
			Mode   string `json:"mode"`
		}{AriaID: figaroID, Handle: ariaName(figaroID), Mode: "new"})
		return
	}
	dressed := "the default outfit"
	if !d.IsEmpty() {
		dressed = "outfit " + d.label()
	}
	fmt.Fprintf(stderrw, "created %s under %s (attended; no prompt sent)\n", ariaName(figaroID), dressed)
}

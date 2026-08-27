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

	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/config"
)

// runPrompt resolves the shell-bound figaro and prompts it. spec dresses the
// aria: it is the birth outfit when this call mints one, and rides the prompt
// as a form patch when the shell is already bound (see promptOutfit).
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
	var figaroEP transport.Endpoint

	if resp.Found {
		// Bound at a pending fork-point (attend <id>:<LT>): this prompt forks
		// there and moves to the new branch (one-shot: the rebind clears it).
		if resp.AtMainLT > 0 {
			runSendForkAt(loaded, resp.FigaroID, forkPoint{lt: resp.AtMainLT}, false, false, prompt, set)
			return
		}
		figaroID = resp.FigaroID
		figaroEP = transport.Endpoint{Scheme: resp.Endpoint.Scheme, Address: resp.Endpoint.Address}
		// Attending a ROLE: the prompt reaches whoever holds it, resolved
		// per call. Attending a plain form refuses by name (redirectRole).
		var rerr error
		figaroID, figaroEP, rerr = redirectRole(ctx, loaded, acli, figaroID, figaroEP)
		if rerr != nil {
			die("%s", rerr)
		}
	} else {
		figaroID, figaroEP = mustCreate(ctx, acli, loaded, d)
		attendNew(ctx, acli, figaroID)
	}
	prompt = expandAtRefsForEndpoint(ctx, figaroEP, prompt)
	mustPromptFigaro(ctx, figaroEP, figaroID, prompt, loaded, set)
}

// runNewPrompt creates a fresh figaro and prompts it. Under jsonMode
// the streaming render is skipped: the aria is created, prompted via a
// fire-and-forget Qua, and a single JSON line is emitted on stdout.
func runNewPrompt(loaded *config.Loaded, prompt string, d dressing, set renderSettings) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	ppid := shellPID
	unbindBinding(ctx, acli, ppid)

	figaroID, figaroEP := mustCreate(ctx, acli, loaded, d)
	attendNew(ctx, acli, figaroID)
	prompt = expandAtRefsForEndpoint(ctx, figaroEP, prompt)

	if set.jsonMode {
		fcli, derr := sdk.DialAria(figaroEP, func(string, json.RawMessage) {})
		if derr != nil {
			die("connect figaro: %s", derr)
		}
		defer fcli.Close()
		qctx, qcancel := context.WithTimeout(ctx, 10*time.Second)
		if _, _, qerr := fcli.Qua(qctx, prompt, buildPromptForm()); qerr != nil {
			qcancel()
			dieWithClosure(qerr, "prompt: %s", qerr)
		}
		qcancel()
		enc := json.NewEncoder(stdout)
		_ = enc.Encode(struct {
			AriaID string `json:"aria_id"`
			Mode   string `json:"mode"`
		}{AriaID: figaroID, Mode: "new"})
		return
	}

	// In no-bind mode nothing was bound to the shell, so print the id
	// to stderr so callers can capture it (mirrors `send -f`'s notice).
	if bindingDisabled() {
		fmt.Fprintf(stderrw, "created %s\n", figaroID)
	}
	mustPromptFigaro(ctx, figaroEP, figaroID, prompt, loaded, set)
}

// submitAndExit queues a prompt on an existing aria and returns without
// attaching to the stream: the tail of every --json path. Kept in one
// place so "what --json does" cannot drift between send, new and fork.
func submitAndExit(ctx context.Context, loaded *config.Loaded, ariaID, prompt string) {
	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	ep, err := resolveAria(ctx, acli, ariaID)
	if err != nil {
		die("%s", err)
	}
	prompt = expandAtRefsForEndpoint(ctx, ep, prompt)

	fcli, derr := sdk.DialAria(ep, func(string, json.RawMessage) {})
	if derr != nil {
		die("connect figaro: %s", derr)
	}
	defer fcli.Close()

	qctx, qcancel := context.WithTimeout(ctx, 10*time.Second)
	defer qcancel()
	if _, _, qerr := fcli.Qua(qctx, prompt, buildPromptForm()); qerr != nil {
		dieWithClosure(qerr, "prompt: %s", qerr)
	}
}

// runSendForkAt implements `send <trunk>:<turn>`: fork the trunk at the turn's
// first LT (imperative interior fork, empty alternative), then send the prompt
// to the trunk we end up attended to. By default we rebind this shell to the
// new alternative and send there; with stay (--attend=false) we leave the shell
// on the original trunk and send there (the alternative is parked at the turn).
func runSendForkAt(loaded *config.Loaded, trunkID string, at forkPoint, stay, asJSON bool, prompt string, set renderSettings) {
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

	// The coordinate goes on the wire AS NAMED. This used to resolve the
	// turn to an LT here and then send that LT in the turn field, so the
	// server read a logical time as a turn number -- and since an LT is far
	// larger than the turn count, `send <id>:<turn>` failed every time with
	// "aria has no turn N". The server owns the translation.
	fr, err := waitForFork(ctx, acli, trunkID, at, dressing{})
	if err != nil {
		die("send: fork %s at %s: %s", trunkID, at, err)
	}
	if fr.OwnerNote != "" {
		fmt.Fprintf(stderrw, "%s\n", fr.OwnerNote)
	}

	target := fr.Alternative
	if stay {
		target = trunkID // parked alternative; shell stays on the original
		if !asJSON {
			fmt.Fprintf(stderrw, "forked %s at %s -> %s (parked; staying on %s)\n", trunkID, at, fr.Alternative, trunkID)
		}
	} else {
		// Registry.Bind rebinds in place; no Unbind needed first.
		if err := bindBinding(ctx, acli, ppid, fr.Alternative, 0); err != nil {
			fmt.Fprintf(stderrw, "warning: could not attend %s: %s\n", fr.Alternative, err)
		}
		if !asJSON {
			fmt.Fprintf(stderrw, "forked %s at %s -> attending %s\n", trunkID, at, fr.Alternative)
		}
	}

	if asJSON {
		enc := json.NewEncoder(stdout)
		_ = enc.Encode(struct {
			AriaID       string `json:"aria_id"`
			Parent       string `json:"parent"`
			Alternative  string `json:"alternative"`
			Continuation string `json:"continuation"`
			Turn         uint64 `json:"turn"`
			Node         *int   `json:"node,omitempty"`
			Mode         string `json:"mode"`
		}{
			AriaID:       target,
			Parent:       fr.Parent,
			Alternative:  fr.Alternative,
			Continuation: fr.Continuation,
			Turn:         at.turn,
			Node:         at.nodeJSON(),
			Mode:         "fork-send",
		})
		// --json submits and exits. This used to print the object and then
		// stream the rendered turn to the SAME stdout, so `| jq` got one
		// object followed by rendered prose. The object is the whole output.
		submitAndExit(ctx, loaded, target, prompt)
		return
	}

	ep, err := resolveAria(ctx, acli, target)
	if err != nil {
		die("%s", err)
	}
	prompt = expandAtRefsForEndpoint(ctx, ep, prompt)
	mustPromptFigaro(ctx, ep, target, prompt, loaded, set)
}

// promptAria sends a prompt to a named aria.
func promptAria(loaded *config.Loaded, ariaID, prompt string, set renderSettings) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	ep, err := resolveAria(ctx, acli, ariaID)
	if err != nil {
		die("%s", err)
	}
	prompt = expandAtRefsForEndpoint(ctx, ep, prompt)
	mustPromptFigaro(ctx, ep, ariaID, prompt, loaded, set)
}

// resolveAria attaches to an existing named aria. Aria ids are
// system-minted, so an unknown id is an error, a new conversation
// comes from the no-id flow (`figaro`), not by naming one here.
func resolveAria(ctx context.Context, acli *sdk.Angelus, ariaID string) (transport.Endpoint, error) {
	attachCtx, attachCancel := context.WithTimeout(ctx, 10*time.Second)
	resp, err := acli.Attach(attachCtx, ariaID)
	attachCancel()
	if err == nil {
		ep := transport.Endpoint{
			Scheme:  resp.Endpoint.Scheme,
			Address: resp.Endpoint.Address,
		}
		if err := waitForSocket(ep.Address, 3*time.Second); err != nil {
			return transport.Endpoint{}, err
		}
		return ep, nil
	}
	if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "not in tree") {
		return transport.Endpoint{}, fmt.Errorf("no such aria %q (ids are system-minted; run `figaro` to start one)", ariaID)
	}
	return transport.Endpoint{}, fmt.Errorf("attach %q: %w", ariaID, err)
}

// waitForSocket waits until the socket accepts a connection. Checking only
// for a path races a restored agent when a stale socket file survived a
// daemon restart.
func waitForSocket(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := transport.DialRaw(transport.UnixEndpoint(path))
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("socket was never dialed")
	}
	return fmt.Errorf("figaro socket %s did not accept connections within %s: %w", path, timeout, lastErr)
}

// runUnattend drops this shell's aria binding (`figaro attend null`). New
// conversations then use the default outfit. Idempotent when unbound.
func runUnattend(loaded *config.Loaded) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	acli := mustConnectAngelus(loaded)
	defer acli.Close()
	ppid := shellPID
	bound := ""
	if r, err := resolveBinding(ctx, acli, ppid); err == nil && r.Found {
		bound = r.FigaroID
	}
	_ = unbindBinding(ctx, acli, ppid)
	if bound != "" {
		fmt.Fprintf(stderrw, "home: unattended %s; new conversations use the default outfit\n", bound)
	} else {
		fmt.Fprintln(stderrw, "no aria bound to this shell")
	}
}

// runNewFromOutfit mints a fresh aria under d, binds it, and returns
// without prompting (`figaro new [-O X]`). An empty spec is the configured
// default_outfit, resolved server-side. A prompt needs the `--` boundary.
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
			Mode   string `json:"mode"`
		}{AriaID: figaroID, Mode: "new"})
		return
	}
	dressed := "the default outfit"
	if !d.IsEmpty() {
		dressed = "outfit " + d.label()
	}
	fmt.Fprintf(stderrw, "created %s under %s (attended; no prompt sent)\n", figaroID, dressed)
}

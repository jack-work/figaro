package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/internal/transport"
)

// sendOpts captures the parsed flag state of the send command.
type sendOpts struct {
	id           string
	ephemeral    bool
	raw          bool // --raw / -r: raw stream, no ANSI/markdown
	verbatim     bool // --verbatim / -v: dump raw wire frames as JSON
	expandTools  bool // --expand / -o: show full tool inputs (Ctrl-O toggles live)
	showThinking bool // --thinking / -t: show thinking blocks (Ctrl-T toggles live)
	exec         bool
	dryRun       bool // --exec only
	skipYes      bool // --exec only
}

// extractSendFlags scans a PassRaw arg list for the send command's
// recognized flags: --id, --ephemeral/-e, --exec/-x, --dry-run/-n,
// --yes/-y. Returns the parsed opts and the residual args (which
// still include the `--` boundary and the prompt body).
//
// Bundled short flags (e.g. -ex, -ey) are expanded. Everything
// after `--` is untouched.
func extractSendFlags(args []string) (sendOpts, []string, error) {
	var opts sendOpts
	rest := make([]string, 0, len(args))

	// First pass: expand bundled bool short flags so -ex -> -e -x,
	// but stop at `--`.
	expanded := make([]string, 0, len(args))
	for i, a := range args {
		if a == "--" {
			expanded = append(expanded, args[i:]...)
			break
		}
		// Bundle expansion: -<letters> where all letters are known
		// bool shorts.
		if len(a) > 2 && a[0] == '-' && a[1] != '-' {
			letters := a[1:]
			allBool := true
			for _, r := range letters {
				switch r {
				case 'e', 'r', 'v', 'o', 't', 'x', 'n', 'y':
					// known bool short
				default:
					allBool = false
				}
			}
			if allBool {
				for _, r := range letters {
					expanded = append(expanded, "-"+string(r))
				}
				continue
			}
		}
		expanded = append(expanded, a)
	}

	i := 0
	for i < len(expanded) {
		a := expanded[i]
		if a == "--" {
			rest = append(rest, expanded[i:]...)
			return opts, rest, nil
		}
		switch {
		case a == "--id":
			if i+1 >= len(expanded) || expanded[i+1] == "--" {
				return opts, nil, fmt.Errorf("--id requires a value")
			}
			if opts.id != "" {
				return opts, nil, fmt.Errorf("--id given more than once")
			}
			opts.id = expanded[i+1]
			if err := validateSendID(opts.id); err != nil {
				return opts, nil, err
			}
			i += 2
			continue
		case strings.HasPrefix(a, "--id="):
			if opts.id != "" {
				return opts, nil, fmt.Errorf("--id given more than once")
			}
			opts.id = strings.TrimPrefix(a, "--id=")
			if opts.id == "" {
				return opts, nil, fmt.Errorf("--id requires a value")
			}
			if err := validateSendID(opts.id); err != nil {
				return opts, nil, err
			}
			i++
			continue
		case a == "--ephemeral", a == "-e":
			opts.ephemeral = true
			i++
			continue
		case a == "--raw", a == "-r":
			opts.raw = true
			i++
			continue
		case a == "--verbatim", a == "-v":
			opts.verbatim = true
			i++
			continue
		case a == "--expand", a == "-o":
			opts.expandTools = true
			i++
			continue
		case a == "--thinking", a == "-t":
			opts.showThinking = true
			i++
			continue
		case a == "--exec", a == "-x":
			opts.exec = true
			i++
			continue
		case a == "--dry-run", a == "-n":
			opts.dryRun = true
			i++
			continue
		case a == "--yes", a == "-y":
			opts.skipYes = true
			i++
			continue
		}
		rest = append(rest, a)
		i++
	}
	return opts, rest, nil
}

// validateSendID wraps rpc.ValidateAriaID with a friendlier error
// prefix. Pulled out so extractSendFlags reads cleanly.
func validateSendID(id string) error {
	if err := rpc.ValidateAriaID(id); err != nil {
		return fmt.Errorf("--id %q: %w", id, err)
	}
	return nil
}

// runSend is the unified send dispatcher. Branches:
//
//	--ephemeral + --id    -> error (contradictory)
//	--exec                -> bash wrapper; --raw is silently ignored
//	                         (the script governs its own output)
//	--ephemeral           -> one-shot in-memory aria, killed after
//	--raw                 -> raw stream, no ANSI/markdown
//	(no flags)            -> bound/named aria, interactive stream
//
// Persistence (--ephemeral) and formatting (--raw) are orthogonal.
func runSend(loaded *config.Loaded, rawArgs []string) {
	opts, rest, err := extractSendFlags(rawArgs)
	if err != nil {
		die("send: %s", err)
	}
	prompt := extractPrompt(rest)
	if prompt == "" {
		die("usage: figaro send [--id <id>] [-e|--ephemeral] [-r|--raw] [-v|--verbatim] [-x|--exec] [-n] [-y] -- <prompt>")
	}

	if opts.ephemeral && opts.id != "" {
		die("send: --ephemeral and --id are contradictory")
	}
	if (opts.dryRun || opts.skipYes) && !opts.exec {
		die("send: -n / -y only meaningful with --exec")
	}

	set := renderSettings{expandTools: opts.expandTools, showThinking: opts.showThinking}
	switch {
	case opts.verbatim:
		runSendVerbatim(loaded, opts, prompt)
	case opts.exec:
		runSendExec(loaded, opts, prompt)
	case opts.ephemeral && opts.raw:
		runSendEphemeralRaw(loaded, prompt)
	case opts.ephemeral:
		runSendEphemeralRich(loaded, prompt, set)
	case opts.raw:
		runSendRaw(loaded, opts.id, prompt)
	default:
		// Today's interactive send: pid-bound or --id named.
		if opts.id == "" {
			runPrompt(loaded, prompt, set)
			return
		}
		promptAria(loaded, opts.id, prompt, set)
	}
}

// runSendEphemeralRaw spins an ephemeral aria, streams raw output
// to stdout, kills it. Today's `figaro plain` with no --id.
func runSendEphemeralRaw(loaded *config.Loaded, prompt string) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	createResp, err := createWithFirstRun(ctx, loaded, func() (*rpc.CreateResponse, error) { return acli.CreateEphemeral(ctx, "", nil) })
	if err != nil {
		die("create figaro: %s", err)
	}
	figaroID := createResp.FigaroID
	figaroEP := transport.Endpoint{Scheme: createResp.Endpoint.Scheme, Address: createResp.Endpoint.Address}
	defer func() {
		killCtx, killCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer killCancel()
		_ = acli.Kill(killCtx, figaroID)
	}()
	waitForSocket(figaroEP.Address, 3*time.Second)

	prompt = expandAtRefsForEndpoint(ctx, figaroEP, prompt)
	exitCode := plainPrompt(ctx, figaroEP, prompt, os.Stdout)
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

// runSendEphemeralRich spins an ephemeral aria, interactive (rich)
// stream, kills it. Useful for one-off conversations the user wants
// to see formatted but not persist.
func runSendEphemeralRich(loaded *config.Loaded, prompt string, set renderSettings) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	createResp, err := createWithFirstRun(ctx, loaded, func() (*rpc.CreateResponse, error) { return acli.CreateEphemeral(ctx, "", nil) })
	if err != nil {
		die("create figaro: %s", err)
	}
	figaroID := createResp.FigaroID
	figaroEP := transport.Endpoint{Scheme: createResp.Endpoint.Scheme, Address: createResp.Endpoint.Address}
	defer func() {
		killCtx, killCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer killCancel()
		_ = acli.Kill(killCtx, figaroID)
	}()
	waitForSocket(figaroEP.Address, 3*time.Second)

	prompt = expandAtRefsForEndpoint(ctx, figaroEP, prompt)
	mustPromptFigaro(ctx, figaroEP, figaroID, prompt, loaded, set)
}

// runSendRaw streams raw output from a persistent aria (bound or
// named). The aria is left alive; only the formatting is raw.
func runSendRaw(loaded *config.Loaded, ariaID, prompt string) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	_, figaroEP, err := resolveTargetEndpoint(ctx, loaded, acli, ariaID, true)
	if err != nil {
		die("%s", err)
	}

	prompt = expandAtRefsForEndpoint(ctx, figaroEP, prompt)
	exitCode := plainPrompt(ctx, figaroEP, prompt, os.Stdout)
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

// runSendVerbatim dumps the raw wire frames (one JSON object per line:
// {"method","params"}) with no formatting — the literal protocol stream.
// Ephemeral when -e, else the bound/named aria (left alive).
func runSendVerbatim(loaded *config.Loaded, opts sendOpts, prompt string) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	var figaroEP transport.Endpoint
	if opts.ephemeral {
		createResp, err := createWithFirstRun(ctx, loaded, func() (*rpc.CreateResponse, error) { return acli.CreateEphemeral(ctx, "", nil) })
		if err != nil {
			die("create figaro: %s", err)
		}
		figaroEP = transport.Endpoint{Scheme: createResp.Endpoint.Scheme, Address: createResp.Endpoint.Address}
		defer func() {
			killCtx, killCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer killCancel()
			_ = acli.Kill(killCtx, createResp.FigaroID)
		}()
		waitForSocket(figaroEP.Address, 3*time.Second)
	} else {
		_, ep, err := resolveTargetEndpoint(ctx, loaded, acli, opts.id, true)
		if err != nil {
			die("%s", err)
		}
		figaroEP = ep
	}

	prompt = expandAtRefsForEndpoint(ctx, figaroEP, prompt)
	if exitCode := verbatimPrompt(ctx, figaroEP, prompt, os.Stdout); exitCode != 0 {
		os.Exit(exitCode)
	}
}

// runSendExec implements the --exec branch. Ephemeral when no --id,
// otherwise scoped to the named aria (auto-created if missing).
func runSendExec(loaded *config.Loaded, opts sendOpts, instruction string) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	var figaroEP transport.Endpoint
	if opts.ephemeral || opts.id == "" {
		createResp, err := createWithFirstRun(ctx, loaded, func() (*rpc.CreateResponse, error) { return acli.CreateEphemeral(ctx, "", nil) })
		if err != nil {
			die("create figaro: %s", err)
		}
		figaroEP = transport.Endpoint{Scheme: createResp.Endpoint.Scheme, Address: createResp.Endpoint.Address}
		defer func() {
			killCtx, killCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer killCancel()
			_ = acli.Kill(killCtx, createResp.FigaroID)
		}()
		waitForSocket(figaroEP.Address, 3*time.Second)
	} else {
		_, ep, err := resolveTargetEndpoint(ctx, loaded, acli, opts.id, true)
		if err != nil {
			die("%s", err)
		}
		figaroEP = ep
	}

	instruction = expandAtRefsForEndpoint(ctx, figaroEP, instruction)
	prompt := "You will write a bash script. Output ONLY raw bash, " +
		"no markdown fences, no prose, no commentary, no explanations. " +
		"The script will be executed verbatim via `bash -c`. " +
		"Instruction: " + instruction

	var buf bytes.Buffer
	exitCode := plainPrompt(ctx, figaroEP, prompt, &buf)
	if exitCode != 0 {
		os.Exit(exitCode)
	}

	script := stripBashFences(buf.String())
	if strings.TrimSpace(script) == "" {
		die("figaro send --exec: empty script from agent")
	}

	if opts.dryRun {
		fmt.Print(script)
		if !strings.HasSuffix(script, "\n") {
			fmt.Println()
		}
		return
	}

	if !opts.skipYes && term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintln(os.Stderr, "--- figaro send --exec: about to execute ---")
		fmt.Fprintln(os.Stderr, script)
		fmt.Fprintln(os.Stderr, "--- press enter to run, ctrl-c to abort ---")
		bufio.NewReader(os.Stdin).ReadString('\n')
	}

	sh := exec.Command("bash", "-c", script)
	sh.Stdin = os.Stdin
	sh.Stdout = os.Stdout
	sh.Stderr = os.Stderr
	if err := sh.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		die("figaro send --exec: bash: %s", err)
	}
}

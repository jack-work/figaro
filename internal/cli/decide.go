package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	apidecide "github.com/jack-work/figaro/api/decide"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/sdk"
)

const decideTimeout = 2 * time.Minute

type decidePlan struct {
	id       string
	choices  []string
	model    string
	context  string
	budget   int
	json     bool
	question string
}

var decideFlagDefs = []cmdkit.FlagDef{
	{Long: "id", Description: "Read this aria's conversation and form (default: the one bound to this shell)"},
	{Long: "choices", Short: "C", Description: "Options, comma separated: -C=\"option 1\",\"option 2\"; @key names a form key"},
	{Long: "model", Short: "m", Description: "Decision model for this call (default: system.decide.model, then providers/decisions.toml)"},
	{Long: "context", Description: "Text to decide over; - reads stdin"},
	{Long: "budget", Description: "Cap the context tokens sent"},
	{Long: "json", Short: "j", IsBool: true, Description: "Emit the response as JSON"},
}

func parseDecideArgs(args []string) (decidePlan, error) {
	var p decidePlan
	var words []string
	value := func(i *int, a, name string) (string, error) {
		if eq := strings.IndexByte(a, '='); eq >= 0 {
			return a[eq+1:], nil
		}
		if *i+1 >= len(args) {
			return "", fmt.Errorf("%s needs a value", name)
		}
		*i++
		return args[*i], nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			words = append(words, args[i+1:]...)
			i = len(args)
		case a == "--id" || strings.HasPrefix(a, "--id="):
			v, err := value(&i, a, "--id")
			if err != nil {
				return p, err
			}
			p.id = v
		case a == "-C" || strings.HasPrefix(a, "-C=") || a == "--choices" || strings.HasPrefix(a, "--choices="):
			v, err := value(&i, a, "-C")
			if err != nil {
				return p, err
			}
			p.choices = append(p.choices, apidecide.Choices(v)...)
		case strings.HasPrefix(a, "-C") && len(a) > 2:
			p.choices = append(p.choices, apidecide.Choices(a[2:])...)
		case a == "-m" || a == "--model" || strings.HasPrefix(a, "--model="):
			v, err := value(&i, a, "--model")
			if err != nil {
				return p, err
			}
			p.model = v
		case a == "--context" || strings.HasPrefix(a, "--context="):
			v, err := value(&i, a, "--context")
			if err != nil {
				return p, err
			}
			p.context = v
		case a == "--budget" || strings.HasPrefix(a, "--budget="):
			v, err := value(&i, a, "--budget")
			if err != nil {
				return p, err
			}
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return p, fmt.Errorf("--budget takes a positive number of tokens, not %q", v)
			}
			p.budget = n
		case a == "-j" || a == "--json":
			p.json = true
		case strings.HasPrefix(a, "-") && len(a) > 1:
			return p, fmt.Errorf("unknown flag %s", a)
		default:
			words = append(words, a)
		}
	}
	p.question = strings.TrimSpace(strings.Join(words, " "))
	p.choices = apidecide.Dedupe(p.choices)
	if p.question == "" && len(p.choices) == 0 {
		return p, fmt.Errorf("nothing to decide: give a question after --")
	}
	return p, nil
}

func (p decidePlan) request(aria string) rpc.DecideRequest {
	return rpc.DecideRequest{
		FigaroID: aria,
		Question: p.question,
		Choices:  p.choices,
		Model:    p.model,
		Context:  p.context,
		Budget:   p.budget,
	}
}

func runDecide(loaded *config.Loaded, args []string, in io.Reader, out io.Writer) error {
	p, err := parseDecideArgs(args)
	if err != nil {
		return fmt.Errorf("decide: %w", err)
	}
	if p.context == "-" {
		b, err := io.ReadAll(in)
		if err != nil {
			return fmt.Errorf("decide: read stdin: %w", err)
		}
		p.context = string(b)
	}
	var resp *rpc.DecideResponse
	var callErr error
	WithAngelus(loaded, func(acli *sdk.Angelus) error {
		ctx, cancel := context.WithTimeout(context.Background(), decideTimeout)
		defer cancel()
		aria, err := decideTarget(ctx, acli, p)
		if err != nil {
			callErr = err
			return nil
		}
		resp, callErr = acli.Decide(ctx, p.request(aria))
		return nil
	})
	if callErr != nil {
		return fmt.Errorf("decide: %s", wireErrorText(callErr))
	}
	if p.json {
		return json.NewEncoder(out).Encode(resp)
	}
	_, err = io.WriteString(out, apidecide.Render(*resp))
	return err
}

func decideTarget(ctx context.Context, acli *sdk.Angelus, p decidePlan) (string, error) {
	if p.id != "" {
		return p.id, nil
	}
	r, err := resolveBinding(ctx, acli, shellPID)
	if err != nil || !r.Found {
		return "", nil
	}
	return r.FigaroID, nil
}

func registerDecide(r *cmdkit.Router) {
	r.Register(&cmdkit.Command{
		Name:  "decide",
		Group: "Prompt",
		Short: "Ask a decision model a yes/no or multiple-choice question",
		Usage: "decide [--id <id>] [-C <a,b,...>] [-m <model>] [--context <text|->] [-j] -- <question>",
		Long: `Asks a System One decision model (jev) one typed question and prints its
scored answer. Nothing is written to any aria: bound to one, decide only READS
its conversation and form, and never waits on a running turn.

  figaro decide -- is the retry loop idempotent?
  figaro decide -C=ship,revert,wait -- which way?
  figaro decide -C @skills -- which skill fits this task?
  figaro decide -C="option 1","option 2" -- which?
  figaro -A decide --context - -- does this log show a crash? < run.log
  figaro decide -- '<412.0:23-1180>! is this step size the learning rate?'

Options come from -C (comma separated, quotes keep commas) and from a last
line of the question that starts with ??. With no options, or yes/no, the
answer is one probability. @key names a form key: a key with children makes
each child an option, carrying its value as JSON; a leaf is one option.

The context is the aria's newest messages, as many as fit once the question
and its options are counted. A leading quote coordinate is the FOCUS: the
passage is sent whole and the window is centred on it instead. Only the form
keys the options name are sent.

Unbound (or under -A, which ignores the binding) it is stateless: the
question, --context and the model's own training are all it has.

The model is -m, else the aria's system.decide.model, else model in
providers/decisions.toml, else jev-latest.`,
		PassRaw: true,
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			outw := ctx.Out
			if outw == nil {
				outw = os.Stdout
			}
			inr := ctx.In
			if inr == nil {
				inr = os.Stdin
			}
			return runDecide(ld, ctx.RawArgs, inr, outw)
		},
		CompleteFlags: decideFlagDefs,
	})
}

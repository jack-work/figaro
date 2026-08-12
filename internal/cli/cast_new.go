package cli

// AUTOCASTING: minting a figaro and a role in one gesture.
//
// The pair was always two commands, and the second one was always the same
// two commands:
//
//	fig new -O reviewer          then   fig cast @role
//	fig form new -S name=x       then   fig cast <aria> @thatform
//
// `-C` folds the second into the first, from either end. `fig new -C` mints
// the figaro and casts it; `fig cast` while attending a FORM mints the figaro
// for it. Same operation, two entrances, one implementation.
//
// WHICH THING -O AND -S DRESS depends on which thing does not exist yet, and
// that is the only reading in which each invocation is unambiguous:
//
//	fig new -C @role -O sonn5    the ROLE exists; -O dresses the ARIA
//	fig new -CO reviewer         no role named; -O mints the ROLE
//	fig cast -O reviewer         (attending an aria) -O mints the ROLE
//	fig cast -O sonn5            (attending a form)  -O dresses the ARIA
//
// PARTIAL FAILURE IS A STATE, NOT A CRASH. Two objects are created across two
// writers and there is no transaction across them. When the figaro exists and
// the casting did not land, that is reported as exactly that, with the id, so
// nothing is orphaned silently and a human can finish the job by hand.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/rpc"
)

// castOutcome is what an autocast did, step by step, so a caller can report a
// partial as the described state it is.
type castOutcome struct {
	AriaID   string `json:"aria_id"`
	RoleID   string `json:"role_id,omitempty"`
	Minted   bool   `json:"minted_figaro"`
	Studied  bool   `json:"studied"`
	Patched  bool   `json:"patched"`
	CastOK   bool   `json:"cast"`
	Err      string `json:"error,omitempty"`
	MintedBy string `json:"-"`
}

// autocast casts an existing aria into a role: either a named form, or one
// minted from the dressing. It never dies; the outcome carries the verdict,
// because by the time it is called the figaro already exists and killing the
// process would strand it without saying so.
func autocast(loaded *config.Loaded, ariaID, formID string, role dressing) castOutcome {
	out := castOutcome{AriaID: ariaID, RoleID: formID}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	WithSessionFor(loaded, ariaID, func(s *Session) error {
		resp, cerr := s.Figaro.Cast(ctx, rpc.CastRequest{
			FormID:    formID,
			Outfits:   role.names,
			RolePatch: role.patch,
		})
		if cerr != nil {
			// Swallowed on purpose: WithSessionFor's error path dies, and by
			// here the figaro exists. A partial has to be REPORTED, not
			// exited on, or the aria is stranded without its id ever being
			// printed.
			out.Err = cerr.Error()
			return nil
		}
		out.RoleID, out.Studied, out.Patched = resp.RoleID, resp.Studied, resp.Patched
		out.CastOK = true
		return nil
	})
	return out
}

// reportCast prints an autocast's verdict. A partial exits non-zero while
// still naming everything that did land: the aria is real and addressable
// whether or not the role is.
func reportCast(out castOutcome, asJSON bool) {
	if asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(out)
		if !out.CastOK {
			os.Exit(1)
		}
		return
	}
	if !out.CastOK {
		if out.Minted {
			die("cast: figaro %s was minted, but the casting failed: %s", out.AriaID, out.Err)
		}
		die("cast: %s", out.Err)
	}
	fmt.Printf("cast %s into %s\n", out.AriaID, out.RoleID)
	fmt.Fprintf(os.Stderr,
		"the role follows: `fig send --id %s -- …` reaches %s until it is repointed\n",
		out.RoleID, out.AriaID)
}

// castFormArg pulls the role id out of a `new -C` argument list: the one bare
// `@`-sigiled positional before the prompt boundary. Anything else there is a
// grammar error rather than a guess, because the alternative reading (a
// prompt without `--`) is one this verb already refuses.
func castFormArg(rest []string) (string, error) {
	form := ""
	for _, a := range rest {
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, "@") {
			if form != "" {
				return "", fmt.Errorf("name one role, not two (%s and %s)", form, a)
			}
			form = a
		}
	}
	return form, nil
}

// mintFigaroFor creates an aria, attends this shell to it, and returns its id.
// The `fig new` path, reused so that an autocast-born figaro is born exactly
// like a hand-made one.
func mintFigaroFor(loaded *config.Loaded, d dressing) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	acli := mustConnectAngelus(loaded)
	defer acli.Close()
	unbindBinding(ctx, acli, shellPID)
	id, _ := mustCreateAndBindOutfit(ctx, acli, loaded, shellPID, d)
	if id == "" {
		return "", fmt.Errorf("the daemon minted no aria")
	}
	return id, nil
}

// runNewCast is `fig new -C`: mint the figaro, then cast it.
//
// With a role id, the role exists and the dressing dresses the ARIA. Without
// one, the dressing mints the ROLE and the aria takes the default outfit:
// there is nothing else -O could mean when the thing being named does not
// exist yet.
func runNewCast(loaded *config.Loaded, roleID string, d dressing, prompt string, set renderSettings) {
	ariaDress, roleDress := d, dressing{}
	if roleID == "" {
		if d.IsEmpty() {
			die("new -C: name a role or mint one: fig new -C <@form> | fig new -CO <names> [-CS k=v]")
		}
		ariaDress, roleDress = dressing{}, d
	}

	ariaID, err := mintFigaroFor(loaded, ariaDress)
	if err != nil {
		die("new -C: %s", err)
	}

	out := autocast(loaded, ariaID, roleID, roleDress)
	out.Minted = true
	if !out.CastOK || set.jsonMode {
		reportCast(out, set.jsonMode)
		if prompt == "" {
			return
		}
	} else {
		reportCast(out, false)
	}
	if prompt != "" {
		// The shell was rebound to the new aria by the mint, so the bare
		// prompt path reaches it: one entry point, not a second spelling of
		// send that could drift from it.
		runPrompt(loaded, dressing{}, prompt, set)
	}
}

// runCastFromAttendedForm is `fig cast` with a form attended and no aria
// named: the figaro that will play the role does not exist yet, so mint it
// and cast it into what this shell is standing in front of.
func runCastFromAttendedForm(loaded *config.Loaded, formID string, d dressing, asJSON bool) {
	ariaID, err := mintFigaroFor(loaded, d)
	if err != nil {
		die("cast: attending %s, but minting a figaro for it failed: %s", formID, err)
	}
	fmt.Fprintf(os.Stderr, "attending %s: minted %s to play it\n", formID, ariaID)
	out := autocast(loaded, ariaID, formID, dressing{})
	out.Minted = true
	reportCast(out, asJSON)
}

// attendedForm reports the form this shell is attending, if it is attending a
// form rather than an aria. The `@` sigil is what makes this lexical instead
// of a lookup.
func attendedForm(ctx context.Context, acli *angelus.Client) string {
	r, err := resolveBinding(ctx, acli, shellPID)
	if err != nil || r == nil || !r.Found {
		return ""
	}
	if strings.HasPrefix(r.FigaroID, "@") {
		return r.FigaroID
	}
	return ""
}

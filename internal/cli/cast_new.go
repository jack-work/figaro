package cli

// AUTOCASTING: minting a figaro and a role in one gesture.

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jack-work/figaro/sdk"
	"os"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
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

// liftRoleArg removes the role positional from an argument list and returns
// it: the one bare `@`-sigiled word before the prompt boundary.
func liftRoleArg(args []string) (string, []string, error) {
	role := ""
	kept := make([]string, 0, len(args))
	for i, a := range args {
		if a == "--" {
			kept = append(kept, args[i:]...)
			break
		}
		if strings.HasPrefix(a, "@") {
			if role != "" {
				return "", nil, fmt.Errorf("name one role, not two (%s and %s)", role, a)
			}
			role = a
			continue
		}
		kept = append(kept, a)
	}
	return role, kept, nil
}

// mintFigaroFor creates an aria and returns its id. The `fig new` path,
// reused so that an autocast-born figaro is born exactly like a hand-made one.
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

// attendAfterCast puts this shell where the casting left the interesting
// thing.
func attendAfterCast(loaded *config.Loaded, roleID string, minted, stay bool) {
	if stay || !minted || roleID == "" || bindingDisabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	acli := mustConnectAngelus(loaded)
	defer acli.Close()
	if err := bindBinding(ctx, acli, shellPID, roleID, 0); err != nil {
		fmt.Fprintf(os.Stderr, "cast: minted %s but could not attend it: %s\n", roleID, err)
		return
	}
	fmt.Fprintf(os.Stderr, "attending %s (--stay to keep your previous attendance)\n", roleID)
}

// restoreAttendance puts the shell back where it was. The mint rebinds as a
// side effect of being a mint, so a verb that should not have moved the shell
// has to move it back.
func restoreAttendance(loaded *config.Loaded, id string) {
	if id == "" || bindingDisabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	acli := mustConnectAngelus(loaded)
	defer acli.Close()
	_ = bindBinding(ctx, acli, shellPID, id, 0)
}

// runNewCast is `fig new -C`: mint the figaro, then cast it.
func runNewCast(loaded *config.Loaded, roleID string, d dressing, prompt string, set renderSettings, stay bool) {
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

	mintedRole := roleID == ""
	out := autocast(loaded, ariaID, roleID, roleDress)
	out.Minted = true
	if out.CastOK {
		attendAfterCast(loaded, out.RoleID, mintedRole, stay)
	}
	if !out.CastOK || set.jsonMode {
		reportCast(out, set.jsonMode)
		if prompt == "" {
			return
		}
	} else {
		reportCast(out, false)
	}
	if prompt != "" {
		// The shell is attending either the aria or its new role, and a role
		// redirects to its holder, so the bare prompt path reaches the same
		// figaro either way: one entry point, not a second spelling of send
		// that could drift from it.
		runPrompt(loaded, dressing{}, prompt, set)
	}
}

// runCastFromAttendedForm is `fig cast` with a form attended and no aria
// named: the figaro that will play the role does not exist yet, so mint it
// and cast it into what this shell is standing in front of.
func runCastFromAttendedForm(loaded *config.Loaded, formID string, d dressing, asJSON, stay bool) {
	ariaID, err := mintFigaroFor(loaded, d)
	if err != nil {
		die("cast: attending %s, but minting a figaro for it failed: %s", formID, err)
	}
	fmt.Fprintf(os.Stderr, "attending %s: minted %s to play it\n", formID, ariaID)
	out := autocast(loaded, ariaID, formID, dressing{})
	out.Minted = true
	// No role was minted here: the form was already in front of us. The mint
	// rebound this shell to the aria as a side effect, so put it back.
	if !stay {
		restoreAttendance(loaded, formID)
	}
	reportCast(out, asJSON)
}

// attendedForm reports the form this shell is attending, if it is attending a
// form rather than an aria. The `@` sigil is what makes this lexical instead
// of a lookup.
func attendedForm(ctx context.Context, acli *sdk.Angelus) string {
	r, err := resolveBinding(ctx, acli, shellPID)
	if err != nil || r == nil || !r.Found {
		return ""
	}
	if strings.HasPrefix(r.FigaroID, "@") {
		return r.FigaroID
	}
	return ""
}

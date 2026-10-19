package cli

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

// castOutcome reports a partial as the described state it is.
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

// autocast never dies: by the time it is called the figaro exists, and exiting
// would strand it unreported.
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
			// WithSessionFor's error path dies; a partial must be
			// reported, not exited on.
			out.Err = cerr.Error()
			return nil
		}
		out.RoleID, out.Studied, out.Patched = resp.RoleID, resp.Studied, resp.Patched
		out.CastOK = true
		return nil
	})
	return out
}

// A partial exits non-zero while still naming everything that landed.
func reportCast(out castOutcome, asJSON bool) {
	if asJSON {
		_ = json.NewEncoder(stdout).Encode(out)
		if !out.CastOK {
			os.Exit(1)
		}
		return
	}
	if !out.CastOK {
		if out.Minted {
			die("cast: figaro %s was minted, but the casting failed: %s", ariaName(out.AriaID), out.Err)
		}
		die("cast: %s", out.Err)
	}
	fmt.Fprintf(stdout, "cast %s into %s\n", ariaName(out.AriaID), ariaName(out.RoleID))
	fmt.Fprintf(stderrw,
		"the role follows: `fig send --id %s -- …` reaches %s until it is repointed\n",
		out.RoleID, out.AriaID)
}

// liftRoleArg removes the one bare `@`-sigiled word before the prompt boundary.
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

// mintFigaroFor does not attend: attendAfterCast decides that once for the cast family.
func mintFigaroFor(loaded *config.Loaded, d dressing) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	acli := mustConnectAngelus(loaded)
	defer acli.Close()
	id, _ := mustCreate(ctx, acli, loaded, d)
	if id == "" {
		return "", fmt.Errorf("the daemon minted no aria")
	}
	return id, nil
}

func attendAfterCast(loaded *config.Loaded, roleID string, minted, stay bool) {
	if stay || !minted || roleID == "" || bindingDisabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	acli := mustConnectAngelus(loaded)
	defer acli.Close()
	if err := bindBinding(ctx, acli, shellPID, roleID, 0); err != nil {
		fmt.Fprintf(stderrw, "cast: minted %s but could not attend it: %s\n", ariaName(roleID), err)
		return
	}
	fmt.Fprintf(stderrw, "attending %s (--stay to keep your previous attendance)\n", ariaName(roleID))
}

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
		// A role redirects to its holder, so the bare prompt path reaches the
		// same figaro either way.
		runPrompt(loaded, dressing{}, prompt, set)
	}
}

func runCastFromAttendedForm(loaded *config.Loaded, formID string, d dressing, asJSON, stay bool) {
	ariaID, err := mintFigaroFor(loaded, d)
	if err != nil {
		die("cast: attending %s, but minting a figaro for it failed: %s", formID, err)
	}
	fmt.Fprintf(stderrw, "attending %s: minted %s to play it\n", ariaName(formID), ariaName(ariaID))
	out := autocast(loaded, ariaID, formID, dressing{})
	out.Minted = true
	// The shell stays on the form it was attending: the aria was minted to
	// play the role.
	_ = stay
	reportCast(out, asJSON)
}

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

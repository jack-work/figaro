package cli

// The study/cast family: `fig study`, `fig drop`, `fig cast`.
// Grammar (plans/forms-and-roles-v2.md §3): the FORM is always the last
// positional; -O occupies the form slot (cast only); with one positional
// the aria comes from attendance, and for cast, from an auto-fork of
// the default form when no aria is available.

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

// resolveStudyArgs applies the positional grammar shared by study/drop/
// cast: [<aria>] <form>, with kind validation naming its slot errors.
func resolveStudyArgs(ctx context.Context, acli *sdk.Angelus, args []string, formInFlags bool, verb string) (ariaID, formID string, err error) {
	switch {
	case len(args) == 2:
		ariaID, formID = args[0], args[1]
	case len(args) == 1 && formInFlags:
		ariaID = args[0]
	case len(args) == 1 && verb == "study" && !strings.HasPrefix(args[0], "@"):
		// The form is always the last positional, so a lone one is the form.
		// A lone positional that CANNOT be a form is the aria, and for study
		// that means "list what this aria studies" without having to attend
		// it first. The sigil is what makes this lexical rather than a guess.
		ariaID = args[0]
	case len(args) == 1:
		formID = args[0]
	case len(args) == 0 && !formInFlags && verb != "cast-list":
		// form must come from somewhere
	}
	if ariaID == "" {
		if r, rerr := resolveBinding(ctx, acli, shellPID); rerr == nil && r != nil && r.Found && !strings.HasPrefix(r.FigaroID, "@") {
			ariaID = r.FigaroID
		}
	}
	if ariaID != "" && strings.HasPrefix(ariaID, "@") {
		return "", "", fmt.Errorf("%s: %s is a form, but this slot takes the FIGARO doing the %s (the form goes last)", verb, ariaID, verbGerund(verb))
	}
	if formID != "" && !strings.Contains(formID, "@") && formID != "" {
		return "", "", fmt.Errorf("%s: %s is a figaro, but this slot takes an unbound form (@id)", verb, formID)
	}
	return ariaID, formID, nil
}

func runStudy(loaded *config.Loaded, args []string, drop, asJSON bool) {
	verb := "study"
	if drop {
		verb = "drop"
	}
	acli := mustConnectAngelus(loaded)
	defer acli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ariaID, formID, err := resolveStudyArgs(ctx, acli, args, false, verb)
	if err != nil {
		die("%s", err)
	}
	if ariaID == "" {
		die("%s: no figaro attended; attend one or name it: fig %s <aria> <@form>", verb, verb)
	}
	if formID == "" && drop {
		die("drop: name the form: fig drop [<aria>] <@form>")
	}
	WithSessionFor(loaded, ariaID, func(s *Session) error {
		var resp *rpc.StudyResponse
		var err error
		if drop {
			resp, err = s.Figaro.Drop(ctx, formID)
		} else {
			resp, err = s.Figaro.Study(ctx, formID)
		}
		if err != nil {
			die("%s: %s", verb, err)
		}
		if asJSON {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"aria_id": ariaID, "studies": resp.Studies})
		}
		if formID == "" {
			fmt.Printf("%s studies: %s\n", ariaID, strings.Join(resp.Studies, ", "))
		} else {
			fmt.Printf("%s %s %s (studies: %d)\n", ariaID, verbPast(verb), formID, len(resp.Studies))
		}
		return nil
	})
}

func verbGerund(v string) string {
	switch v {
	case "drop":
		return "dropping"
	case "cast":
		return "casting"
	}
	return v + "ing"
}

func verbPast(v string) string {
	if v == "drop" {
		return "dropped"
	}
	return "studies"
}

// runCast performs one casting call. With -O the role is minted BY the
// figaro's actor loop, born cast; with no aria available the figaro
// itself is minted first from the default form (the fig new path,
// unattended): figaro-minted-but-role-failed is reported as the
// partial it is.
func runCast(loaded *config.Loaded, args []string, outfits, set, del string, asJSON, stay bool) {
	acli := mustConnectAngelus(loaded)
	defer acli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	minting := strings.TrimSpace(outfits) != "" || strings.TrimSpace(set) != "" || strings.TrimSpace(del) != ""

	// ATTENDING A FORM, with no aria named: the figaro that will play it does
	// not exist yet. Mint one and cast it, which is the other entrance to
	// `new -C` and the same operation from the other end. Here the dressing
	// dresses the ARIA: the form is already standing in front of us, so there
	// is nothing else -O could be for.
	if len(args) == 0 {
		if attended := attendedForm(ctx, acli); attended != "" {
			d := mustDress(outfits, set, del)
			runCastFromAttendedForm(loaded, attended, d, asJSON, stay)
			return
		}
	}

	ariaID, formID, err := resolveStudyArgs(ctx, acli, args, minting, "cast")
	if err != nil {
		die("%s", err)
	}
	var role dressing
	if minting {
		if formID != "" {
			die("cast: -O/-S mint the role and OCCUPY the form slot; drop %s or the dressing", formID)
		}
		d, derr := parseDress(outfits, set, del)
		if derr != nil {
			die("cast: %s", derr)
		}
		role = d
	}
	if formID == "" && role.IsEmpty() {
		die("cast: name a role or mint one: fig cast [<aria>] <@form> | fig cast [<aria>] -O <names> [-S k=v]")
	}

	minted := ""
	if ariaID == "" {
		created, cerr := acli.Create(ctx, nil, nil)
		if cerr != nil {
			die("cast: no figaro attended and minting one failed: %s", cerr)
		}
		ariaID = created.FigaroID
		minted = ariaID
		fmt.Fprintf(os.Stderr, "no figaro attended: minted %s from the default form (unattended)\n", ariaID)
	}

	WithSessionFor(loaded, ariaID, func(s *Session) error {
		resp, cerr := s.Figaro.Cast(ctx, rpc.CastRequest{FormID: formID, Outfits: role.names, RolePatch: role.patch})
		if cerr != nil {
			if minted != "" {
				// The partial, spelled out: the figaro exists, the casting
				// failed. -j callers see both facts.
				if asJSON {
					_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
						"figaro_id": minted, "cast": false, "error": cerr.Error(),
					})
					os.Exit(1)
				}
				die("cast: figaro %s was minted, but the casting failed: %s", minted, cerr)
			}
			return cerr
		}
		if asJSON {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{
				"aria_id": ariaID, "role_id": resp.RoleID,
				"studied": resp.Studied, "patched": resp.Patched,
				"minted_figaro": minted != "",
			})
		}
		// A role this call MINTED is what the shell should end up in front
		// of; a role that already existed leaves attendance alone.
		attendAfterCast(loaded, resp.RoleID, minting, stay)
		fmt.Printf("cast %s into %s", ariaID, resp.RoleID)
		if resp.Studied {
			fmt.Printf(" (now studying it)")
		}
		fmt.Println()
		fmt.Fprintf(os.Stderr, "the role follows: `fig send --id %s -- …` reaches %s until it is repointed\n", resp.RoleID, ariaID)
		return nil
	})
}

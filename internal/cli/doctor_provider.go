package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/transport"
)

// runDoctorProvider answers the question no figaro surface could answer:
// "this aria is not responding - what is the provider actually doing?"
//
// `status` said idle. `queue` was empty. The log said nothing. The trace file
// had the answer - a 429 with an hour-long Retry-After - but only for turns
// that had already ended, because spans export on end, and only to someone
// willing to write a JSON scanner. Meanwhile the request that was STILL
// hanging appeared nowhere at all.
//
// This reads the transport's own ring: rows are written at departure and
// completed in place, so an in-flight request is visible while it is the
// problem rather than after it is history.
func runDoctorProvider(ariaID, count string, asJSON bool) error {
	limit := 20
	if count != "" {
		n, err := strconv.Atoi(count)
		if err != nil || n < 0 {
			return fmt.Errorf("-n takes a count, got %q", count)
		}
		limit = n
	}

	cli, err := angelus.DialClient(transport.UnixEndpoint(angelusSocketPath()))
	if err != nil {
		return fmt.Errorf("no angelus running: %w", err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := cli.ProviderLedger(ctx, ariaID, limit)
	if err != nil {
		return fmt.Errorf("the running angelus cannot report provider round-trips; `figaro stop` and retry: %w", err)
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp)
	}

	if len(resp.Rounds) == 0 {
		if resp.Retained == 0 {
			fmt.Println("no provider round-trips recorded yet")
		} else {
			fmt.Printf("no round-trips for that filter (%d retained overall)\n", resp.Retained)
		}
		printSessions(resp.Sessions)
		return nil
	}

	fmt.Printf("%-12s %-9s %-6s %-9s %-9s %-8s %s\n",
		"time", "aria", "status", "duration", "req", "retry", "endpoint")
	for _, r := range resp.Rounds {
		when := time.UnixMilli(r.StartedAtMS).Format("15:04:05.000")
		aria := r.Aria
		if aria == "" {
			aria = "-"
		}

		status, dur := "…", "in flight"
		if !r.InFlight {
			status = strconv.Itoa(r.Status)
			if r.Status == 0 {
				status = "err"
			}
			dur = humanMillis(r.DurationMS)
		} else {
			// An in-flight row's age is the number that matters: a request
			// out for four minutes is the story.
			dur = "in flight " + humanMillis(time.Since(time.UnixMilli(r.StartedAtMS)).Milliseconds())
		}

		retry := "-"
		if r.RetryAfterS > 0 {
			retry = (time.Duration(r.RetryAfterS) * time.Second).String()
		}

		fmt.Printf("%-12s %-9s %-6s %-9s %-9s %-8s %s\n",
			when, aria, status, dur, humanBytes(r.ReqBytes), retry, shortEndpoint(r.URL))
	}

	summarizeProviderTrouble(resp.Rounds)
	printSessions(resp.Sessions)
	return nil
}

// printSessions shows the derived credentials the daemon holds: one row per
// CREDENTIAL, with the number of token sources sharing it.
//
// The row exists because sharing was previously unobservable. Copilot
// session tokens were cached on the provider instance, and provider
// instances are per-aria, so every conversation exchanged its own token
// against api.github.com and nothing in figaro said so - until GitHub
// answered a burst with 403 and figaro rendered that as "no provider
// connected". "bindings" is the number that would have named the bug:
// bindings climbing with arias while exchanges stays at 1 is the fix
// working.
func printSessions(sessions []rpc.SessionCredential) {
	if len(sessions) == 0 {
		return
	}
	fmt.Println()
	fmt.Printf("%-28s %-14s %-9s %-9s %s\n", "session credential", "fingerprint", "exchanges", "shared by", "expires")
	for _, s := range sessions {
		fp := s.Fingerprint
		if fp == "" {
			fp = "(none)"
		}
		exp := "-"
		if s.ExpiresAtMS > 0 {
			exp = time.UnixMilli(s.ExpiresAtMS).Format("15:04:05")
			if left := time.Until(time.UnixMilli(s.ExpiresAtMS)); left > 0 {
				exp += fmt.Sprintf(" (%s left)", left.Round(time.Second))
			} else {
				exp += " (stale)"
			}
		}
		fmt.Printf("%-28s %-14s %-9d %-9d %s\n", s.Key, fp, s.Exchanges, s.Bindings, exp)
	}
}

// summarizeProviderTrouble names the diagnosis rather than leaving it in the
// table. The table is evidence; an operator wants the verdict.
func summarizeProviderTrouble(rounds []rpc.ProviderRound) {
	var refused, inFlight int
	var worstRetry int64
	var reset string
	for _, r := range rounds {
		if r.InFlight {
			inFlight++
		}
		if r.Status == 429 || r.Status == 529 {
			refused++
			if r.RetryAfterS > worstRetry {
				worstRetry = r.RetryAfterS
			}
			for _, k := range sortedRateLimitKeys(r.RateLimit) {
				if strings.Contains(k, "reset") && reset == "" {
					reset = r.RateLimit[k]
				}
			}
		}
	}
	if refused == 0 && inFlight == 0 {
		return
	}
	fmt.Println()
	if refused > 0 {
		fmt.Printf("%d of %d round-trips were refused for quota.\n", refused, len(rounds))
		if worstRetry > 0 {
			fmt.Printf("the provider asked for up to %s; a wait that long is a usage window,\n",
				(time.Duration(worstRetry) * time.Second).String())
			fmt.Println("not a throttle, and no amount of retrying will shorten it.")
		}
		if reset != "" {
			fmt.Printf("limit resets %s\n", reset)
		}
	}
	if inFlight > 0 {
		fmt.Printf("%d request(s) still in flight.\n", inFlight)
	}
}

func sortedRateLimitKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func humanMillis(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return (time.Duration(ms) * time.Millisecond).Round(100 * time.Millisecond).String()
}

// shortEndpoint keeps the part of a URL that distinguishes one call from
// another; the host repeats on every row and earns no column.
func shortEndpoint(raw string) string {
	if i := strings.Index(raw, "://"); i >= 0 {
		raw = raw[i+3:]
	}
	if i := strings.Index(raw, "/"); i >= 0 {
		return raw[i:]
	}
	return raw
}

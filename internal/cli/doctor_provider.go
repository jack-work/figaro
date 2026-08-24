package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jack-work/figaro/sdk"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
)

// runDoctorProvider answers the question no figaro surface could answer:
// "this aria is not responding - what is the provider actually doing?"
func runDoctorProvider(ariaID, count string, asJSON bool) error {
	limit := 20
	if count != "" {
		n, err := strconv.Atoi(count)
		if err != nil || n < 0 {
			return fmt.Errorf("-n takes a count, got %q", count)
		}
		limit = n
	}

	cli, err := sdk.DialAngelus(transport.UnixEndpoint(angelusSocketPath()))
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
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp)
	}

	if len(resp.Rounds) == 0 {
		if resp.Retained == 0 {
			fmt.Fprintln(stdout, "no provider round-trips recorded yet")
		} else {
			fmt.Fprintf(stdout, "no round-trips for that filter (%d retained overall)\n", resp.Retained)
		}
		return nil
	}

	fmt.Fprintf(stdout, "%-12s %-9s %-6s %-9s %-9s %-8s %s\n",
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

		fmt.Fprintf(stdout, "%-12s %-9s %-6s %-9s %-9s %-8s %s\n",
			when, aria, status, dur, humanBytes(r.ReqBytes), retry, shortEndpoint(r.URL))
	}

	summarizeProviderTrouble(resp.Rounds)
	return nil
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
	fmt.Fprintln(stdout)
	if refused > 0 {
		fmt.Fprintf(stdout, "%d of %d round-trips were refused for quota.\n", refused, len(rounds))
		if worstRetry > 0 {
			fmt.Fprintf(stdout, "the provider asked for up to %s; a wait that long is a usage window,\n",
				(time.Duration(worstRetry) * time.Second).String())
			fmt.Fprintln(stdout, "not a throttle, and no amount of retrying will shorten it.")
		}
		if reset != "" {
			fmt.Fprintf(stdout, "limit resets %s\n", reset)
		}
	}
	if inFlight > 0 {
		fmt.Fprintf(stdout, "%d request(s) still in flight.\n", inFlight)
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

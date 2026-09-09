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
			if r.Stream != nil {
				// A stream has no HTTP status; its verdict is its own.
				status = r.Stream.Status
				if status == "" {
					status = "err"
				}
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
		if line := streamLine(r.Stream); line != "" {
			fmt.Fprintf(stdout, "%-12s %-9s %s\n", "", "", line)
		}
	}

	summarizeProviderTrouble(resp.Rounds)
	return nil
}

// streamLine is the second row a streaming attempt earns: an HTTP status says
// nothing about a socket that opened, delivered nine events and stopped.
// Counts, sizes and timings only - never a payload, an id, or an error text.
func streamLine(s *rpc.StreamStats) string {
	if s == nil {
		return ""
	}
	parts := []string{fmt.Sprintf("events=%d", s.Events)}
	if s.UnknownEvents > 0 {
		parts = append(parts, fmt.Sprintf("unknown=%d", s.UnknownEvents))
	}
	if s.RespBytes > 0 {
		parts = append(parts, "resp="+compactBytes(s.RespBytes))
	}
	if s.FirstEventMS > 0 {
		parts = append(parts, "first="+humanMillis(s.FirstEventMS))
	}
	if s.FirstToolMS > 0 {
		parts = append(parts, "tool="+humanMillis(s.FirstToolMS))
	}
	if s.FirstArgumentMS > 0 {
		parts = append(parts, "arg="+humanMillis(s.FirstArgumentMS))
	}
	if s.ArgumentDeltas > 0 {
		parts = append(parts, fmt.Sprintf("argdeltas=%d/%s", s.ArgumentDeltas, compactBytes(s.ArgumentBytes)))
	}
	if s.ArgumentUnmatched > 0 {
		parts = append(parts, fmt.Sprintf("unmatched=%d", s.ArgumentUnmatched))
	}
	if s.TypesDropped > 0 {
		parts = append(parts, fmt.Sprintf("types-dropped=%d", s.TypesDropped))
	}
	if s.ErrClass != "" {
		parts = append(parts, "err="+s.ErrClass)
	}
	return "stream " + strings.Join(parts, " ")
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
	streams := summarizeStreamTrouble(rounds)
	if refused == 0 && inFlight == 0 && len(streams) == 0 {
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
	for _, line := range streams {
		fmt.Fprintln(stdout, line)
	}
}

// summarizeStreamTrouble reads the failure modes a status code cannot express:
// a socket that ended badly, tool arguments that matched no call, events the
// parser did not recognize, and a stream that has been silent since it opened.
func summarizeStreamTrouble(rounds []rpc.ProviderRound) []string {
	var (
		lines     []string
		broken    int
		classes   []string
		seen      = map[string]bool{}
		unmatched int64
		unknown   int64
		silent    int
	)
	for _, r := range rounds {
		s := r.Stream
		if s == nil {
			continue
		}
		unmatched += s.ArgumentUnmatched
		unknown += s.UnknownEvents
		if r.InFlight {
			if s.Events == 0 {
				silent++
			}
			continue
		}
		if s.ErrClass != "" || (s.Status != "ok" && s.Status != "completed") {
			broken++
			class := s.ErrClass
			if class == "" {
				class = s.Status
			}
			if class != "" && !seen[class] {
				seen[class] = true
				classes = append(classes, class)
			}
		}
	}
	if broken > 0 {
		line := fmt.Sprintf("%d stream(s) ended without completing", broken)
		if len(classes) > 0 {
			sort.Strings(classes)
			line += " (" + strings.Join(classes, ", ") + ")"
		}
		lines = append(lines, line+".")
	}
	if unmatched > 0 {
		lines = append(lines, fmt.Sprintf(
			"%d tool-argument delta(s) arrived for no open call; the arguments may have been\ncorrelated late or by a later done event, or they may be missing.", unmatched))
	}
	if unknown > 0 {
		lines = append(lines, fmt.Sprintf(
			"%d event(s) the parser did not recognize; the provider's schema may have moved.", unknown))
	}
	if silent > 0 {
		lines = append(lines, fmt.Sprintf(
			"%d open stream(s) have delivered no events yet.", silent))
	}
	return lines
}

// compactBytes is humanBytes without the space, for a dense metric line.
func compactBytes(n int64) string { return strings.ReplaceAll(humanBytes(n), " ", "") }

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

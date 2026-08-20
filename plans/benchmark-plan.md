# THE FULL BENCHMARK, DESIGNED BEFORE IT IS RUN (ede92072, 2026-08-20)

Gluck asked for "a full benchmark of all scenarios that encompasses
performance, system resource consumption and allocation, latency, etc.", and
to hold until the work is done. This is the DESIGN, put up for shooting at
first, because an hour of machine time spent on the wrong protocol is how this
campaign produced a -43.8% that had to be retracted.

NOTHING HERE HAS BEEN RUN.

## THE PROTOCOL, WHICH IS NOT NEGOTIABLE AND WAS ALL PAID FOR ONCE

  1. DETERMINISTIC COUNTS FIRST, TIMINGS SECOND. Allocations, bytes, entries
     served, run visits, file descriptors, requests. Every claim this campaign
     had to walk back was a wall-clock ratio; every claim that survived was a
     count.
  2. TWO ARMS IN ONE BINARY AND ONE RUN wherever the comparison is between two
     shapes, with an IN-RUN CONTROL that should not move. A cross-run
     comparison on this box measures the box.
  3. AN A/A CONTROL BEFORE ANY A/B, and ABBA (counterbalanced) order, not
     alternation. Interleaving is not counterbalancing.
  4. UNDER /var/tmp/figaro-bench.lock, because two benchmarks on this machine
     measure each other.
  5. HEAP DELTAS ARE RUN THREE TIMES AND THE SPREAD IS REPORTED. A single-shot
     heap delta is an unrepeated measurement; the first run after warm-up
     lies by 1.5x.
  6. WHEN A RATIO FLATTERS, CHECK THE EXPONENT. A speedup is not proof of the
     shape intended.
  7. EVERY ARM STATES WHAT WOULD FALSIFY IT, BEFORE IT RUNS.

## THE SCENARIOS

### A. THE SEND PATH, WHICH IS WHAT CHANGED TODAY

    A1  request body encoding      allocs, B/op, ns/op at 1k / 10k / 50k rows,
                                   buffered vs streamed. HAVE: 1k and 10k.
                                   MISSING: 50k, and a REAL row corpus (the
                                   fixture rows are 130 B; real ones are not).
    A2  whole-history read         provider.Translations: allocs and B/op per
                                   message. HAVE: 2 allocs, 32.768 B/msg.
    A3  catch-up, steady state     entries visited when the write path has
                                   already translated everything. Should be
                                   ZERO encodes; the count is the assertion.
    A4  catch-up, cold provider    a first send on a 2,556-record aria: how
                                   many encodes, how long, how much heap.
    A5  peak RSS across one send   the quantity streaming exists for. Measured
                                   as PSS around a send driven at the local
                                   sink, buffered vs streamed, on the LARGEST
                                   real aria. Falsifier: if peak does not
                                   move, streaming buys nothing and the
                                   default should go back.

### B. LATENCY, SEPARATED FROM THE NETWORK

    B1  time to first byte ON THE WIRE, from figaro's own clock: send start ->
        first request byte written. Streamed should START EARLIER (no marshal
        before the request opens) and that is a real user-visible number.
    B2  time to last request byte, same run. Streamed may finish later; the
        difference between B1 and B2 is the shape of the trade.
    B3  time to first TOKEN against the local sink, which answers instantly,
        so the number is figaro's own overhead and not Anthropic's queue.
    B4  the same three against the REAL endpoint, n=10, reported with the
        spread and NOT as a mean, because the network owns the variance.

### C. THE STORE, WHICH IS WHERE THE REGRESSION LIVED

    C1  warm tail read             tree.Range vs the flat window: the ratio
                                   already retired at 1.13x. Re-run, because
                                   the citation is inherited.
    C2  hopping reader             entries served from below at a binding
                                   budget, with the unbinding control row.
    C3  fork prefix sharing        ANSWERED BEFORE THE SUITE RAN, so it is
                                   struck from this plan rather than
                                   scheduled: 296 of 296 strings on the fig IR
                                   and 76 of 76 blocks per provider on the
                                   translations, 0 MINTED, after the fork-base
                                   fix. Sharing BELOW the base is complete and
                                   unmoved; above it, what the old code
                                   "shared" was another lineage's rows.
    C4  eviction sweep             run visits per eviction and per full sweep
                                   at R = 256 / 1024 / 4096. Counts.
    C5  decode inflation           res/enc and res/payload on the real store,
                                   three repeats. The 4.4x overstatement raise
                                   is still open and this is its evidence.

### D. THE DAEMON, AS A SYSTEM

    D1  boot to first listing      wall clock and PSS, cold store copy.
    D2  idle arena                 PSS at +5s, +45s after a full listing.
    D3  file descriptors           held after one and two listings.
    D4  a long session             N sends on one aria, PSS after each, to
                                   catch growth that a single send cannot show.
    D5  many arias                 the real store's 720, listed and paged.

### E. WHAT I EXPECT TO BE WORSE, SAID BEFORE MEASURING

    - The streamed body adds a goroutine and a pipe per request. At one
      request per turn that is noise, and if it shows up anywhere it will be
      in B3 (overhead against an instant sink), not in A1.
    - The fork-base rebase adds one channel-info read per lineage step per
      cut. It is on the peek path. If C1 or C2 regresses, THAT is the cause
      and it is a cache away from being fixed.

## THE HOST QUESTION, RECONNOITRED (f921a944, 2026-08-20)

Gluck asked whether spain or another tailscale box could give a quiet hour.
Measured by logging in, not read off a config:

    SPAIN   Intel N100, 4 cores, NO SMT, 15Gi + 7.7Gi zram. / and /var/tmp are
            REAL ext4 on NVMe. Idle load 0.09, background CPU ~2.3% of four
            cores. 14 live services; kfin-sync (~6h) and logrotate (hourly)
            WILL fire mid-run. No go binary, but `nix shell nixpkgs#go
            nixpkgs#gcc` builds the tree (cgo needs the gcc).

    FIXED-WORK JITTER (sha256 of 32MB x12)
        spain   129-131 ms   +-1.5%
        gluck    19-25  ms   +-30%     at load 0.2, i.e. idle-ish

    REAL BENCHMARK SPREAD (internal/angelus, -count=5, max/min-1)
        arm                      gluck    spain
        AriaReaderForm (61ns)     3.7%    18.7%
        Context/600              46.2%    14.1%
        Context/10000            14.9%     1.7%
        ReaderPage/10000            -      0.5%

    Spain is 4.1x SLOWER in wall clock (729s vs 178s for the package).

THE RECOMMENDATION IS NOT UNIFORM, AND THE REPORT'S OWN NUMBERS ARE WHY.
Spain is better on the LONG arms and WORSE on the SHORT one. This plan has
both -- a ~40-60ns point read and multi-second scans -- so a single host is
not obviously right for all of it. The stated cause of the short-arm spread is
frequency ramping under the powersave governor; THAT IS A HYPOTHESIS AND THE
EXPERIMENT IS QUEUED (governor=performance, same arm, same count).

AND ABSOLUTE NUMBERS DO NOT CROSS HOSTS at 4.1x. Only same-host A/B deltas
mean anything, which is the discipline this file already imposes for a
different reason.

NOISE THAT SURVIVES ON SPAIN, named rather than waved at: cloudflared and
tailscaled must stay up (tailscaled is the access path); journald writes 68.5k
lines/day to the same NVMe; an N100 is a 6W part whose sustained-load
behaviour was NOT characterized; zram swap makes a memory-heavy arm pay
compression CPU; and 4 cores without SMT contend far sooner than the 5800X.

UNVERIFIABLE: gioco answers on the LAN with no open ports at all, and
bedrock-linux refuses the key. Nothing is known about either.

## WHAT THIS NEEDS FROM GLUCK BEFORE IT RUNS

  1. A REAL-ROW CORPUS. Every A-arm on synthetic 130-byte rows measures the
     fixture. The real store has the corpus and every probe here already runs
     on a COPY -- I want to confirm that is still the standing permission.
  2. AN HOUR OF THE BOX, quiet. The bench lock serializes me against other
     arias, but D-arms measure PSS and a busy machine moves it.
  3. WHETHER B4 (the real endpoint, n=10 per arm) is worth the API spend.
     Everything else is free.

# Pre-existing failure: DetachedTailAdvancesAndScreenHoldsStill

Filed 2026-08-15 by aria 53289ae2, during the S6 campaign, so it stops
living in a chat channel. NOT caused by that campaign — attributed by
canary, see below.

## What fails

`internal/cli/tmuxsmoke_detached_tail_test.go`, run as

    FIGARO_TMUX_SMOKE=1 go test ./internal/cli/ \
      -run TestSmoke_DetachedTailAdvancesAndScreenHoldsStill -v

stops at its own vacuity guard, tmuxsmoke_detached_tail_test.go:169:

    detached one notch and the live block is not on screen at all;
    this measurement would be vacuous

Sequence: a 90-tick streaming bash tool, wait for ticks, `C-t` to open
the pager (chrome confirmed present), then `Up` to detach ONE notch.
After that single notch, either no tick or no live-block row is on
screen, so the test refuses to measure.

## Why it is not the S6 work

Canaried across two demonstrably different binaries:

    md5 674b287b7ce608b8461a444b8a817ec6   HEAD of feat/layered-cache
    md5 50e64d73e040a7115ebc96694972eb8f   b734fcd0 (main, before the
                                           campaign's first commit)

Same failure, same guard, both arms. Two arms that AGREE are usually one
binary — proving the md5s differed is what makes the agreement mean
something.

What the run does prove about the new composer: the test only reaches
that guard AFTER asserting ticks were flowing on screen, and they were.
The live region updates correctly; the failure is downstream, in the
DETACHED PAGER VIEW.

## Where it belongs

The CLI/client fold refactor's queue (heldInquiry -> turn header,
plans/ui-ir-tree.md). Whoever picks that up owns this: the suspicion to
start from is that detaching by one notch moves the viewport off the
live block rather than holding it, which is the same class of bug as
"the tail advances while the screen holds still" that the test was
written to pin.

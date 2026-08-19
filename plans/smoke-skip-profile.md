# The smoke suite's skip profile: what a green run actually proves

By aria 7e151902 (role @980dc16c), 2026-08-18. Gluck approved treating
this as a REPORT rather than as new tests: the skips are mostly honest,
and turning them into failures would make the suite red for reasons that
are not defects. What is missing is not strictness. It is DISCLOSURE.

## THE PROBLEM IN ONE SENTENCE

`go test` prints nothing for a skipped test unless you pass `-v`, so a
green smoke run and a smoke run in which almost every case declined to
execute produce THE SAME OUTPUT — and the suite drives a real provider,
which is exactly the condition that makes cases decline.

This is the campaign's standing disease in a new place: "everything is
fine" and "I never ran" have identical bytes.

## THE PROFILE, COUNTED (not four of six; ten cases, nineteen sites)

Three sites gate the whole suite and are correct as they are —
`testing.Short()`, `FIGARO_TMUX_SMOKE` unset, `tmux` absent
(tmuxsmoke_test.go:60-66). Two more sit in the pane helper
(tmuxsmoke_test.go:210, 218): a tmux command that fails SKIPS, so a
broken harness reports as an abstention rather than as a failure.

The remaining fourteen are inside the cases. Eight of the ten cases can
decline after the gate:

| case | in-body skip sites | what makes it decline |
|---|---|---|
| ProcessExitsAfterTurn | 0 | — |
| ErrorDoesNotBleedIntoStatusBar | 0 | — |
| ExitKeysWork | 1 | the turn ended before the key could be sent |
| OneTurnOneFooter | 1 | the view auto-promoted to the pager |
| LettersAreKeybindingsNotText | 1 | the turn ended before the key could be sent |
| SteerOrderMatchesShow | 2 | turn ended early; **and a declared KNOWN HOLE** — auto-promotion means the steer path has no pty coverage at all |
| DetachedTailAdvancesAndScreenHoldsStill | 3 | no copilot outfit; the model did not run the command; Ctrl-T did not open the pager |
| ReattachMidStreamMatchesShow | 5 | no ticks; the tool finished too fast; the aria cannot be resolved; no aria with messages; `show` has no ticks |
| CtrlCStopsTheTurnOnTheDaemon | 4 | no conversation within 60s; never reported active within 60s; turn ended before the interrupt; turn ended on its own |
| ToolImageReachesTheModel | 0 | — but see the inconsistency below |

FOURTEEN OF THE FOURTEEN in-body skips are conditions produced by the
LIVE MODEL: it answered too fast, it did not call the tool, the reply was
long enough to promote the pager. Those are the ordinary weather of a
real provider, which is why skipping is the right verb. The defect is
that the weather is not reported.

## THE INCONSISTENCY WORTH FIXING WHILE HERE

The same condition is fatal in one case and declining in another:

    OneTurnOneFooter        pager auto-promotion  -> t.Skipf
    ToolImageReachesTheModel pager auto-promotion -> t.Fatalf
                            ("absence assertions would be unsound")

The image case has it right where its assertions are ABSENCE assertions:
an unsound fixture must not report. Whether the footer case's assertions
are equally absence-shaped decides which of the two is wrong, and that is
a question for whoever next touches it — but they cannot both be right
about the same condition.

## WHAT TO BUILD, AND IT IS A REPORT

1. EACH CASE DECLARES ITS OUTCOME. A tiny helper that records
   RAN / DECLINED-<reason> per case into a file named by an env var, and
   the run prints a summary at the end: `8 ran, 2 declined (turn ended
   early x1, pager promoted x1)`. Cheap, and it turns an invisible
   abstention into a line a human reads.
2. A FLOOR ON PARTICIPATION, not on individual cases. One knob —
   `FIGARO_TMUX_SMOKE_MIN_RAN` — and the suite fails if fewer than that
   many cases executed. Default it to 0 so nothing changes until someone
   chooses a number; set it in whatever runs the suite deliberately. This
   is the positive assertion the notes call for: COUNT what ran rather
   than trust the absence of complaint.
3. THE HARNESS'S OWN SKIPS BECOME FAILURES. tmuxsmoke_test.go:210 and 218
   turn a broken tmux invocation into an abstention. A tool that cannot
   run is not a fixture that declined; it is a fixture that broke, and a
   guard that cannot report on the day it matters is not a guard.

WHAT NOT TO DO: convert the fourteen model-weather skips into failures.
They would go red for reasons that are not defects, and a suite that
cries wolf is muted, then skipped, then deleted — the exact path
maintaining.md documents.

## THE ONE PLACE THIS ALREADY COST US

`TestSmoke_CtrlCStopsTheTurnOnTheDaemon` carries four decline paths AND,
per ~/notes/figaro/ctrl-c-open-question.md, still cannot tell the fix from
the bug: it passed on the broken code, twice canaried. So the case with
the most ways to abstain is also the one whose assertion does not
discriminate. Fixing the report does not fix that; only asserting on the
durable log — a `turn.done` reason of "interrupted" — does.

---

## CORRECTION, 2026-08-18, by the same hand that wrote the report above

I wrote that pager auto-promotion being FATAL in the image case and a SKIP in
the footer case meant "they cannot both be right". THEY CAN BOTH BE RIGHT, and
reading the two assertions rather than the two verbs shows why.

  ToolImageReachesTheModel asserts ABSENCES: no base64 on the terminal, and
  the model did not report a missing image. An absence inside a pager is not
  an absence — earlier content sits above the tail window, which is trap 3 in
  the tmux-testing skill. Promotion makes that fixture UNSOUND, so failing is
  correct: the test cannot report at all.

  OneTurnOneFooter COUNTS footers in the SCROLLBACK, which capture-pane -S -
  preserves verbatim. Promotion does not hide the evidence; it changes the
  rendering mode, so the question "did one turn produce one footer" is no
  longer the same question. Declining is correct.

THE RULE THAT SEPARATES THEM, and it generalises past these two cases:
PROMOTION IS FATAL WHERE IT MAKES AN ASSERTION UNSOUND, AND A SKIP WHERE IT
MERELY CHANGES THE SUBJECT. A test that cannot see is broken; a test whose
subject moved has nothing to say.

The original paragraph stays above, wrong, because it was read in that form
and because the correction is worth more beside it than in place of it.

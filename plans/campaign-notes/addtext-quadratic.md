# The live prose path is quadratic in reply length

Aria 3a9225b1, 2026-08-18. Found by an instrument built for something else,
which is the point: nothing had ever priced the round loop.

## The mechanism

`internal/figaro/turn.go`, `asm.addText`, on every prose delta:

    s.msg.Content[n-1].Text += text

Go strings are immutable, so `+=` allocates a NEW string of the full
accumulated length and copies it. Consecutive same-kind deltas coalesce into
one block (which is why the block count does NOT grow), so that one block's
text is reallocated on every delta of a streaming reply.

Total bytes for N deltas of width w is w * N(N+1)/2 -- **quadratic in the
number of deltas, for a linear amount of text.**

## The measurement, and the model closes on it

`BenchmarkRoundLoopDeltas16/64/256`, w=64 chars, agent rebuilt per iteration:

    deltas   measured B/op   model (baseline + w*N(N+1)/2)   error
        16         179,616   170,912 +     8,704             +0.0%
        64         318,816   170,912 +   133,120             +4.9%
       256       2,447,121   170,912 + 2,105,344             +7.5%

    marginal cost per delta:   16->64    2,900 B
                               64->256  11,085 B   <- 3.8x, on 4x the deltas

Allocations grow only 600 -> 821 across the same range: roughly ONE per delta.
**Linear allocations, quadratic bytes** is the signature of exactly this
concatenation and of nothing else.

The pre-registered prediction was "superlinear B/op on both sides of stage 5,
and if it is linear my reading of addText is wrong". It is superlinear, and
the closed-form series predicts the measured values to within 7.5% without
any fitted parameter.

## What it costs in production terms

A 10 KB assistant reply streamed in 1,000 deltas allocates and copies roughly
**5 MB** of intermediate strings to produce 10 KB of text. Tool output is
clamped by `tailBound`; **prose is not**.

## What it is NOT

- **Not stage 5's.** Stage 5 hoisted `asm` onto the turn; it did not touch
  `addText`. The cost is identical on both sides and will dominate any delta-
  axis comparison without being caused by anything under test.
- **Not a measurement artefact.** The agent is rebuilt per iteration, so per-op
  cost is independent of b.N (179,913 / 179,640 / 179,179 B at 50x/200x/800x).
- **Not new.** It survived the entire memory campaign, because the campaign
  measured the COMPOSE path and nothing priced the ROUND LOOP.

## The fix, which is not mine to make

**THE PER-FRAME COST HAZARD WAS RAISED AND THEN WALKED BACK, AND THE WALK-BACK
IS VERIFIED.** 6defe6f9 warned that a Builder moves cost from per-delta to
per-frame, since `asm.message()` is read per frame by `composeTurn` at ~11fps.
They then checked the source and withdrew it; confirmed here independently in
`/usr/lib/go/src/strings/builder.go`:

    func (b *Builder) String() string {
        return unsafe.String(unsafe.SliceData(b.buf), len(b.buf))
    }

    func (b *Builder) grow(n int) {
        buf := bytealg.MakeNoZero(2*cap(b.buf)+n)[:len(b.buf)]
        copy(buf, b.buf)
        b.buf = buf
    }

`String()` is **zero-copy**, it aliases the buffer. A later `Write` that grows
reallocates, leaving any previously returned string pointing at the old array
with its own length and contents intact; one that does not grow appends beyond
that string's length, which it cannot see. So materialize-on-read is cheap and
the per-frame cost is near zero.

**THE REAL HAZARD IS ALIASING, NOT COST.** A Builder-backed `Content.Text`
handed to the composer aliases a buffer the drain loop is still appending to,
and this codebase already has a law about precisely that shape,
`cached_log.go`: *"logView is the published window: immutable once stored, so a
reader takes it with one atomic load and holds no lock at all."* That is what
whoever takes this must test. The per-frame arithmetic is not the question;
whether a held view can be mutated underneath its holder is.

Still: **whoever fixes it prices the destination path first.** The warning is
sound as a discipline even though this particular instance of it dissolved,
this campaign has been burned by a fix that looked free because nobody measured
where the cost went. (091d162e and 6defe6f9, 2026-08-18.)

A `strings.Builder` on the open block, or an append-only `[]string` joined
once at `PushFigaro`. Two exist in `turn.go` already; neither is on this path.
Routed to 091d162e as a candidate stage rather than taken, and 6defe6f9
declined to touch it for the same reason: inventing a stage because something
shiny turned up is how a plan stops meaning anything.


---

## ATTRIBUTION CORRECTION, 2026-08-18: the linear term was mislabelled

Published in the stage-5 decomposition: *"the one allocation removed per bus
event is `mergeTurnTools`' map, made once per event."* **That label is wrong.**
091d162e checked with escape analysis; verified here independently:

    $ go build -gcflags=-m ./internal/figaro/
    turn_repair.go:45:31: make(map[string]turnTool, len(previous)) DOES NOT ESCAPE
    turn_repair.go:44:37: append escapes to heap      <- partialAssistant
    turn_repair.go:45:50: append escapes to heap      <- toolsFromAssistant

`mergeTurnTools`' map is **stack-allocated and was never a heap allocation at
all.** The linear per-event term is `partialAssistant`'s `out.Content` append,
which does escape; the tool-axis term is `toolsFromAssistant`'s append.

**The measured totals are unchanged**, ~1.0 allocation removed per event,
~38 per tool, no per-byte component, and the three-mechanism account still
covers all three axes with no residue. Only the NAME on the linear term was
wrong.

**Why an attribution error is the more expensive kind:** the next hand would
have optimised a map that is never allocated, measured no improvement, and
concluded the MODEL was wrong when only the LABEL was.

**THE RULE THIS COST, now standing: when a prediction is about allocations,
ASK THE COMPILER BEFORE PREDICTING.** `-gcflags=-m` costs seconds. Reading the
source tells you what is *written*; escape analysis tells you what is
*allocated*, and this campaign spent four predictions, three of the
executor's magnitudes and one of my labels, learning that those are different
questions.


### The rule has a boundary, found the day after it was adopted

**`-gcflags=-m` answers WHICH LINES allocate. It says nothing about HOW MANY
TIMES they run.** The unverified fixed ~35 term in the stage-5 decomposition is
a CALL-COUNT question, not an escape question, and reaching for escape analysis
to settle it would be the right tool on the wrong shape, the same class of
error as reading `make(map...)` and calling it an allocation because it is
spelled like one.

What would settle it: `-memprofile` with `alloc_objects` attributed by call
site, or a differential count with one call site removed.

6defe6f9's hypothesis, recorded as a hypothesis: before stage 5, `noteAssistant`
had TWO call sites,

    turn.go:519  a.noteAssistant(asmMsg.message())   per BUS EVENT  -> the linear term
    turn.go:523  a.noteAssistant(&staged.Payload)    per ROUND      -> fixed-term shaped

Site 523 fires once per round regardless of event count, so it is fixed by
construction. Whether it accounts for 35 allocations is **unknown**: one call
through `partialAssistant` + `toolsFromAssistant` should be a handful, not 35,
so if 523 is the whole story the arithmetic does not close.

**NOT CHASED, deliberately.** A small term with an honest "unverified" beside it
is worth more than a confident one. This is recorded so the next hand starts
from a named hypothesis and a named method rather than from the label I nearly
asserted.

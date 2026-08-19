# The lost study: a guarded durable write, and an unguarded mirror behind it

Aria 7e151902, 2026-08-18. STEP 1 ONLY, as ordered: name the window, do not
build the harness and do not fix it. The window WAS found, and it matches
the observed failure exactly.

## THE FAILURE

`TestConcurrentCastsOfOneFigaro` (internal/figaro/self_cast_test.go:105),
once in twenty-one runs, on a box at loadavg 16-22:

    []string{7 roles} does not contain "@89026c1c"
    "a concurrent cast lost its study: the role points at a figaro that
     does not know it"

## WHAT WAS TRACED, AND WHAT IS SOUND

THE DURABLE HALF IS CORRECT and was ruled out by reading it end to end:

  store/study.go StudyForm      reads (studies, version) as ONE atomic load
                                (studiesAndVersion -> FormAt), writes with
                                setStudies(..., ifVersion), retries on
                                ErrFormMoved, 32 attempts, with backoff.
  store/form.go  runBatch       ONE drainer. Each write is reduced against
                                the RUNNING state of the batch, not the
                                state the batch began at.
  store/form.go  reduceOne:520  `if w.ifVersion != 0 && st.version !=
                                w.ifVersion` -> ErrFormMoved. The compare is
                                against the true latest version, inside the
                                single writer, atomically with the append.

So a stale reader cannot win the guard, and a batch cannot let two writers
pass on the same version. THE COMPARE-AND-SET IS SOUND.

## THE WINDOW, IN THE MIRROR

internal/figaro/study.go, `declareStudy`:

    studies, changed, err = sb.StudyForm(a.id, formID)   // guarded, correct
    raw, _ := json.Marshal(studies)
    a.form.Apply(form.Patch{Set: {StudiesKey: raw}})     // UNGUARDED

The second line writes THE WHOLE SET into the agent's in-memory board with
NO version guard, and internal/form/state.go's Apply is a plain
read-modify-publish:

    cur := s.load()
    next := cur.snapshot.Apply(p)
    s.publish(board{snapshot: next, dirty: true})

Its own doc states the contract it is now given: "ONE WRITER, MANY READERS
... A second writer would need CompareAndSwap on the update path. There is
exactly one, so Apply stores unconditionally."

THERE IS NO LONGER EXACTLY ONE. store/study.go's own comment records why:
"Taking the cast off that loop (the self-cast deadlock) replaced
serialization with optimism." Cast now runs on the CALLER's goroutine, so N
concurrent casts are N concurrent writers of `a.form`.

THE INTERLEAVING, which produces exactly the observed message:

    cast A: StudyForm -> returns the set with 7 members (A's own included)
    cast B: StudyForm -> returns the set with 8 members
    cast B: a.form.Apply(8 members)     published
    cast A: a.form.Apply(7 members)     loads 8, SETS the key to its own 7

A's write is not stale by version -- it carries no version at all. It is a
whole-value Set computed before B's write existed, and the last writer wins.
The durable board still holds 8; the MIRROR holds 7. `Agent.StudyList` reads
the mirror (`StudiesFromSnapshot(a.form.Snapshot())`), which is what the test
asserts on.

WHY THIS IS NOT COSMETIC: the mirror is what a TURN renders from.
declareStudy's own comment says it "refreshes the agent's OWN board mirror
... a durable write it does not hear about is a write the next turn will not
see." A lost mirror entry is a role the aria observes on disk and does not
observe in its prompt.

## THE SAME SHAPE, ONE LAYER OVER, NOT RULED OUT

`a.backend.SetObservedForms(a.id, studies)` (study.go:86, 102) and the hub's
copy (angelus/study_hub.go) hand a whole slice to XwalStore.SetObservedForms,
which takes a mutex and stores it wholesale -- also last-writer-wins over a
value computed earlier. The failing assertion is explained without it, so it
is NOT the diagnosis; it is a second instance of the same pattern and the
successor should weigh it rather than inherit my scope.

## WHAT I DID NOT DO

No reproduction attempt, no stress harness, no fix. Per the order: the
instrument comes first and RED, then the fix. The count to assert is ALL N
present, not a sampled contains-check, under -race, with the count reported
on failure -- a race that is one-in-twenty at N=8 may be one-in-two at N=64,
and the count says which.

REPETITION WAS THE WEAKEST INSTRUMENT HERE and was not used: the window is a
property of the code, and reading the read-modify-write found it in one pass
where a thousand runs would have argued about luck.

## SUCCESSOR'S ADDENDUM (aria 6ec565b5, 2026-08-18 21:15) -- NOT AN EDIT ABOVE

The window above was correct and is now closed on branch fix/lost-study-mirror
(ad3fb738 the instrument, 8bc497a1 the fix), off feat/layered-cache@ee31b71f.

THE LEVER WAS THE OPPOSITE OF THE REFLEX. Sweeping N (8,32,64,256 x 10 runs,
-race, box at loadavg 27-34) produced ZERO failures. GOMAXPROCS=1 at N=8
produced 2 in 10; GOMAXPROCS=1 at N=64 produced zero. The loss needs ONE
caster DESCHEDULED at a seam microseconds wide, so more parallelism spreads
the same seam thinner. TO REPRODUCE A NARROW INTERLEAVING, REDUCE THE
PARALLELISM. The committed instrument therefore pins GOMAXPROCS and buys
confidence in ROUNDS, not width: 8 of 8 invocations red before the fix, 1-3
rounds lost per 24, every loss from the mirror and never from the board.

THE SECOND INSTANCE (SetObservedForms, study.go:86,102 and study_hub.go) is
FIXED TOO, not left standing: the agent declares the observed set from inside
the same version-guarded step that publishes the mirror, and the hub declares
only when its call actually wrote. Safe because resumeStudies runs once at
NewAgent, before any cast, so every already-durable set has been declared.

A THIRD INSTANCE WAS FOUND, one the note did not name:
applyControlPatchVerdict published the REQUESTED patch where the form writer
had returned the APPLIED one. It looked benign and I said so in writing; it
was LATENT. With ptree.Set's Equal no-op removed as a canary, a re-set of
{"k":[1,2]} as {"k":[ 1 , 2 ]} left the log holding [1,2] and the mirror
holding [ 1 , 2 ] -- semantically equal, byte-different, and for array- and
object-valued keys that reaches the wire. Two independent suppressions were
agreeing by coincidence. Fixed, and TestMirrorAndLogAgreeOnBytes is the alarm
on the coincidence: red under that canary before the fix, green under the SAME
canary after it.

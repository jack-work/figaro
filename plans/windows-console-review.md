# Review request — four Windows-found defects on `fix/version-stamp`

Branch: `fix/version-stamp`, four commits on top of `v0.17.0` (`b7e91d23`).
Nothing is merged. Diff against `origin/main` is **+201 / −102**, of which
production is +98 / −67.

**You are reviewing this on Linux. Three of the four defects were found on
Windows and one of them cannot be exercised on your machine at all.** Read the
"What you can and cannot run" section before you start disbelieving a test.

| | commit | package |
|---|---|---|
| 1 | `b551b32` | `internal/cli` — a proxy-installed binary knows its own version |
| 2 | `bdb487e` | `internal/livelog/aria` — every frame names who asked |
| 3 | `e167ee9` | `internal/cli`, `internal/term` — dead code removal |
| 4 | `7b78583` | `internal/term` — arm the console we write to |

---

## How this started

The user reported that figaro "would not start", then that a freshly installed
`v0.17.0` did not contain the v0.17.0 changes. Both reports were true and
neither had the cause it appeared to have.

The installed binary **was** v0.17.0 (`go version -m` reports
`mod github.com/jack-work/figaro v0.17.0`; `queue`/`hup`/`cut` are present).
`figaro version` printed `figaro unknown`. So did a two-week-old `fig.exe` that
was still on `PATH` at `v0.3.4`, and the two of them were indistinguishable to
the handshake — which is defect 1.

---

## 1 — A proxy-installed binary reported no identity, and the handshake went silent

`semver` is injected by `flake.nix`. `commit` is injected by `-ldflags`.
`go install <module>/cmd/figaro@vX.Y.Z` sets neither, and records **no VCS
settings at all** — the proxy ships a zip, not a checkout. So the install path
most users actually take produced a binary that could not name itself.

The cosmetic half is `figaro version` printing `unknown`. The load-bearing half
is `buildRevision()` returning `""`, because `checkDaemonBuild` reads
`""` on both sides as "both unknown, nothing provable" and **passes in
silence** — by design, and correctly, for the case it was written for. The case
it was not written for is two *different* proxy builds, which is exactly what a
stale `fig.exe` against a fresh angelus is. That pair renders nothing, and the
silence is what makes it look like a hung daemon.

**Fix:** fall back to `debug.BuildInfo.Main.Version` when there is no revision.
Two binaries reporting the same module version are the same source, so it is a
real identity, not a placeholder.

**Review question.** A nix-built daemon reports a git SHA; a `go install`
client now reports `v0.17.0`. They will not compare equal, so that pair now
`die`s where it used to warn. I believe refusing is right — it is a genuinely
mixed pair and the failure it prevents is a blank screen — but it is a
behaviour change and it is yours to accept or reject.

---

## 2 — The live UI showed who asked, then stopped showing it

**Not Windows-specific. Fully reproducible on Linux, and the one commit here
you can review normally.**

`Server.OpenInquiry` broadcasts the question with its `InquirySegments`. Every
frame after it restates the question, because a part must never be partial
about the turn it describes — but `inquiryOfLocked` returned the **text only**.
`Update` and `Close` therefore restated an *unattributed* copy of the same
question.

The client holds what a part last said (`heldInquiry`, `client.go`), so the
first streaming frame overwrote the attributed inquiry with `segments: nil`.
The user's own words: the sender "appeared in the input inquiry in the
transcript ui, but it disappeared when I interacted with the ui."

Two things stayed correct and made this look like a Windows problem:

- `figaro show` re-derives segments from the IR (`compose.InquirySegmentsOf`),
  so the committed record was right throughout.
- A **steer** is a node, and a node carries its own `Sender`. So steering was
  attributed and inquiries were not, which reads like a partial deployment.

**Fix:** `inquiryOfLocked` returns `(string, []InquirySegment)`; both call sites
destructure it. Seven lines.

`TestEveryFrameKeepsWhoAskedTheQuestion` fails on pristine `v0.17.0`
(`frame 2 carried the question "who are you" with 0 segments, want 1`) and
passes with the fix. **Please run the revert check yourself** — it is the whole
argument for the commit.

---

## 3 — Dead code

`golang.org/x/tools/cmd/deadcode -test ./...` reported four unreachable
functions; it now reports nothing.

- `livelogTurn.screenMoved` — `transcript.screenMoved` is the one with callers
  and a canary test. Only the forwarder is gone.
- `livelogTurn.transcriptSearching` — `transcriptSearchingHistory` is what the
  input loop actually asks.
- `livelogTurn.turnFinished`
- `transcript.turnVoice` — a duplicate of `aria.turnRole`, which has a consumer.

Plus `CursorUp`, `CursorDown`, `EraseLine`, `WrapCount` in `internal/term`,
each of which had exactly one caller: its own test. They are the rune-counting
era that `clip()` and `hardWrap()` retired when they started counting cells.

**This is the commit to be suspicious of.** `deadcode` is a static reachability
analysis and can be wrong about a method that satisfies an interface invoked
dynamically. I checked each by name across the tree and found no other
reference, but a second pair of eyes on `livelogTurn`'s method set is cheap
insurance.

---

## 4 — figaro never enabled VT processing on the console it writes to

**This is the one you cannot reproduce.** Everything below was measured on
Windows with a throwaway Go probe, in both a bare `conhost` and Windows
Terminal + pwsh 7.

`MakeRaw` set `ENABLE_VIRTUAL_TERMINAL_INPUT` on stdin and stopped. **Nothing
in figaro ever set `ENABLE_VIRTUAL_TERMINAL_PROCESSING` on stdout.** The
renderer's escapes worked only where something else had already turned it on.

Measured, stdout handle, same binary:

| host | stdout mode | `\x1b[?1049h` honoured | full-width row + CRLF |
|---|---|---|---|
| conhost | `0x0003` | **no** | **2 rows** |
| Windows Terminal | `0x0007` | yes | 1 row |
| either, after the fix | `0x000F` | yes | 1 row |

Where the flag is off, `?1049h` is **inert**: the pager's frames land in the
**primary** buffer as ordinary text. That is the user's report of the full
transcript being left in their scrollback on disconnect. `transcript.go`
already describes this shape in a comment — "conhost without VT processing,
where the pager's frames land in the primary buffer as ordinary text, which is
the Windows symptom" — and the previous mitigation was to delete the `\x1b[2J`
from `leave()`. That made the symptom survivable; it did not address why the
alt screen was not honoured.

`DISABLE_NEWLINE_AUTO_RETURN` rides along because it is the same defect at the
other edge. Without it the console advances the cursor the instant the last
cell of a row is written, instead of deferring the wrap the way every UNIX
terminal does. `render.Prose` deliberately lands glamour at **exactly** the
viewport width (`rendererFor`: `wrap := width + 2`, compensating for the dark
style's 2-column margin) and `clip()` permits exactly `width` cells. Both are
correct on UNIX. Under conhost a full-width row cost two, so a thinking block
or a table row wrapped when it fit and the incipit's one-row-per-line cursor
math drifted a row per full-width line.

### Two honest caveats

**a. The intermittency is indicated, not proven.** The user sees the scrollback
bug in Windows Terminal, which starts at `0x0007` and works. My explanation is
that console mode belongs to the *console*, not the process: anything sharing
it — a bash tool call, a spawned CLI — can clear the flag mid-session, and
figaro never re-asserts it. That matches "often", and arming on every `MakeRaw`
defends against it. I could not reproduce the clearing directly. If you think
the mode should be re-asserted at `transcript.enter()` as well, say so; I kept
it to one site to keep the diff small.

**b. I threw away my own first fix, and the reason matters.** I initially
"fixed" the wrap by reserving the last column on Windows (`Width()` returning
`cols-1`). The table above is why it is not in this branch: **Windows Terminal
was already correct at 1 row**, so reserving a column would have cost the
user's actual terminal a column to fix a bug it did not have. Three files
became ten lines in `MakeRaw`. If you review the earlier reasoning anywhere,
that is the conclusion it was superseded by.

### Why it lives in `MakeRaw`

No new call site and no new lifecycle: the restore is the closure the renderer
already unwinds. The alternative — arming at process start — would need its own
teardown, and `os.Exit` skips defers (see `1fe6ac9`), so it would be the
riskier placement, not the safer one. Console mode outliving the process is the
hazard here; hanging the restore on something that already runs is the
mitigation.

---

## What you can and cannot run

```sh
go build ./... && go vet ./... && go test ./...   # commits 1-3
GOOS=windows go build ./... && GOOS=windows go vet ./...   # typechecks commit 4
```

- **`internal/term/cbreak_windows.go` and its test are `//go:build windows`.**
  They will not compile, run, or appear in coverage on your machine.
  `GOOS=windows go vet ./internal/term/` is the most you can do. The test is
  pure mode arithmetic (`vtOutputMode`) precisely so that the reviewable part
  does not need a console.
- **Commit 2 is fully reviewable on Linux** and is the one carrying a proven
  regression test. Please run the revert check.
- **Five tests fail on Windows on pristine `v0.17.0`**, unrelated to this
  branch — `TestAngelusClientPresentsCallerOnBothParamShapes`,
  `TestAngelusClientOmitsCallerWhenUnset`,
  `TestColdSelectSeedsFromViewportWithoutScrolling`,
  `TestAgentClientPresentsCallerAcrossTheSecondHop`,
  `TestReadSubscribeAfterInterrupt_SameAgent`, `TestSchemaRefusesNewerStore`.
  Causes include a hardcoded `/var/tmp` and unix-socket path assumptions. I
  baselined them before and after; the branch changes neither set. **They should
  pass for you**, and that difference is expected — do not read a green Linux
  run as evidence the Windows behaviour was fixed.

## What I am asking for

1. The handshake behaviour change in commit 1 — accept, or narrow it so a
   semver never compares against a SHA.
2. The revert check on commit 2.
3. A second opinion on the `livelogTurn` method removals in commit 3.
4. Whether commit 4 should also re-assert the console mode at
   `transcript.enter()`, given that caveat (a) is unproven.

Nothing here is urgent and nothing is merged.

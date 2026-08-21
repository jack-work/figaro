# API Coherence — the RPC surface, and the CLI's dependence on it

Opened 2026-08-21 by 44754e14 (forked from ede92072), at Gluck's request:
"reorganize and consolidate and simplify the rpc api design and the cli's
dependence on it", plus "attendance is very random regarding whether a created
form / aria is attended".

## 1. The prior art, found and dated

**`plans/rpc-surface-tightening.md`, commit `fd83b09a`, 2026-05-05** — one of
four A/B/C/D refactor plans (inbox, rpc, dead-code, bootstrap). Its thesis:

> Shrink the figaro JSON-RPC surface to its minimum viable shape: five
> methods, one dispatch interface, no compatibility shim.

Its five: `figaro.qua`, `figaro.set`, `figaro.reload_config`,
`figaro.interrupt`, `figaro.chalkboard`. It removed `set_model` and
`set_label` ("clients write `system.model` via `figaro.set`"), declared the
break unshimmed ("old clients fail loudly"), and extracted an `AgentServer`
interface so dispatch left `protocol.go`.

**It landed.** `figaro.qua` is the wire name today; `SetModel`/`SetLabel` are
gone; `internal/figaro/server.go` owns the method table; the figaro-side
`protocol.go` is **14 lines** — the plan hoped for ~60.

**Then the plan was deleted** by `4b5f40b7` (2026-05-10, "migrate plans/ and
docs/ to figaro-la/docs"). It is on no branch of this repo. Its sibling,
`docs/cli-architecture-dossier.md` (`1cffcaaf`), is what produced `cmdkit` and
router-based dispatch — the same fate.

**And the surface re-accreted.** Five methods on the figaro socket in May;
today:

| | count |
|---|---|
| method constants in `internal/rpc/methods.go` | **40** |
| handlers in the angelus table (`authz.Guard(map…)`) | **24** |
| `internal/angelus/protocol.go` | **2,011 lines** |
| `internal/rpc/methods.go` | **1,012 lines** |
| distinct client methods the CLI calls | **30** (`acli.*` 21, `fcli.*` 9) |

The tightening was done once and has not been done since. This plan is the
second pass, and its job is to leave behind a *rule* that makes a third pass
unnecessary — the May plan left a shape, not a law, so the shape drifted.

## 2. Where the incoherence actually is

### 2.1 Two read families, and the router is implemented twice

Three reads exist on both sockets:

| what | angelus socket | figaro socket |
|---|---|---|
| a window of sealed history | `aria.page` | `figaro.read` |
| fig IR + render metrics | `aria.context` | `figaro.context` |
| the durable board | `aria.form` | `figaro.form` |

The daemon already routes: `rpc.MethodNeedsAgent` plus `hub.go:260` serve a
read from the store when no agent is live. **The CLI routes too** — it picks
`acli.AriaPage` or `fcli.Read` at the call site. So a client must know whether
an agent is live, which is the one fact the daemon exists to hide, and the
routing rule lives in two places that can disagree.

### 2.2 Names say WHERE, not WHAT

`aria.page` and `figaro.read` differ by socket, not by meaning. A reader of the
constant list cannot tell what is a verb, what is a push, and what is the same
verb twice.

### 2.3 Attendance is a side effect of minting, not an outcome of a verb

Measured from the call sites today:

| verb | does this shell end up attending? | where the rule lives |
|---|---|---|
| `fig new` / bare prompt, unbound shell | **yes** — unbind, then create-and-bind | `prompt.go:64-74` |
| `fig form new` | **no** — "mints an unbound form" | `form_family.go:24` |
| `fig fork` (own bound aria) | **yes**, unless `--stay` | `fork.go:185` |
| `fig fork <someone else's>` | **no** — "a fan-out never steals this shell" | `fork.go:186` |
| `fig cast @role` that mints | **yes**, unless `--stay` | `cast_new.go:123` |
| `fig cast` from an attended form | **yes** — mints an aria "to play it" | `cast_new.go:193` |
| `fig attend <id>` | **yes**, and refuses when binding is off | `manage.go:895` |

Five rules, five sites. The tell is `restoreAttendance`, whose own comment
says it: *"The mint rebinds as a side effect of being a mint, so a verb that
should not have moved the shell has to move it back."* A verb undoing its
helper's side effect is the definition of the thing to fix.

**Proposed law (needs Gluck's ruling):** attendance is decided by the verb,
never by the mint. `mintFigaroFor`/`mustCreateAndBindOutfit` stop binding.
Every birth verb then states its own outcome, and the whole rule is one line
per verb:

- a verb that creates a thing **for you** attends it (`new`, `fork` of your
  own, `cast` that mints);
- a verb that creates a thing **beside** you does not (`fork` of another's,
  `send -e`);
- `--stay` suppresses the move, everywhere, with one implementation;
- `attend` is the only verb whose *purpose* is the move.

Open: does `fig form new` attend? Forms are attendable today (`attendedForm`,
and `fig cast` reads it), so "no" is a special case, not a species difference.
I recommend it attends like any other birth, but this is a semantic ruling and
it is Gluck's.

## 3. Proposed shape

1. **One read surface.** Delete `aria.page` / `aria.context` / `aria.form` as
   *client-facing* names. The angelus keeps ONE entry point per read verb, and
   `MethodNeedsAgent` + the hub stay the only router. The CLI loses its
   `acli` vs `fcli` choice for reads.
2. **One vocabulary.** Constants grouped by *kind* — verbs, pushes, admin —
   with the socket an implementation detail of the client, not a prefix.
3. **Attendance as a verb outcome**, per §2.3.
4. **A law that keeps it tight**: every new method must state which existing
   method it is NOT (the May plan's discipline, written into the plan file so
   the next accretion has to argue with it).

## 4. Sequence — one idea per commit, green at every step

| # | step | falsifier |
|---|---|---|
| 1 | this plan | — |
| 2 | attendance: strip the bind out of the mint helpers; each birth verb states its outcome | a live script that asserts the shell's binding after each of the 7 verbs |
| 3 | collapse the read families to one client-facing surface | `fig show`/`listen`/`status` byte-identical before and after, live and dormant |
| 4 | regroup `methods.go` by kind; delete what nothing calls | `deadcode -test` as the oracle, as W9 did (−492 lines then) |
| 5 | slim `angelus/protocol.go` — the handler table is a table, the handlers move out | line count, and no behaviour change |

## 5. Open questions for Gluck

1. Does `fig form new` attend? (§2.3)
2. Is a wire break acceptable again? The May plan said "old clients fail
   loudly against new servers" and that is still the cheapest path — but the
   daemon and the CLI are now upgraded separately (`nix profile upgrade`
   leaves a running daemon), so a break is felt as a broken session rather
   than an error. `checkDaemonBuild` exists; does it refuse, or just warn?
3. Priority against the boot-sweep work (`fix/daemon-lifecycle`, ede92072).

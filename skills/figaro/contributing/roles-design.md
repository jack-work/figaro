# Roles: the design

A role is how one aria is addressed by what it DOES rather than by which
conversation it is. For someone changing `internal/figaro/study.go`,
`internal/cli/cast_family.go`, or the redirect in `internal/cli/target.go`.
Read [forms-design.md](forms-design.md) first: a role is a form, and every
invariant there holds here.

## 1. A role is a duck type, not a kind

An unbound form carrying `target-aria` is a role. There is no role kind, no
role marker, no conversion. The consequences are deliberate:

- Any form becomes a role the moment someone points it at an aria, and stops
  being one when the key is removed.
- Exclusivity with bound forms is enforced in the READERS, not in the
  schema. `cast` refuses a bound target, and `target-aria` is never read off
  a bound form, so a hand-set key on a figaro's board is inert. The key
  itself cannot be forbidden, because a form's keys are the user's.
- Roles do not chain. If `target-aria` names another form, resolution
  reports that rather than following it.

## 2. Resolution is LATE, and per call

`target-aria` is read at the moment of each invocation, never cached into a
binding, never resolved at attend time. Repointing a role therefore takes
effect on the next call, with no restart and nothing to invalidate.

The redirect applies to the FIGARO namespace only. The form namespace never
redirects, because a role is also a form that people need to read and
patch, and if `fig form @role` followed the pointer there would be no way
to see the role at all.

| verb | on a role |
|---|---|
| `fig send`, bare `q` | redirect to `target-aria` |
| `fig listen` | redirect, with a banner naming both ids |
| `fig hup`, queue and turn verbs | redirect |
| `fig form`, `fig form set`, `fig form delete`, `fig form listen` | the ROLE itself |
| `fig fork` | fork the FORM |
| `fig bind` | birth a figaro from the form |

A repointing mid-listen is not chased. The banner names the target it
attached to, and a re-run picks up the new one. Chasing it would mean a
stream whose subject changes without the reader being told.

## 3. Cast is a casting call

`fig cast [<aria>] <@form>` does two things that must not half-happen: the
aria STUDIES the role, and the role POINTS at the aria.

Serialization USED to be the figaro's own actor loop, and that cost more than
it bought: a cast issued from inside the aria's own turn waited for a loop
that was waiting for the turn that issued it, the SELF-CAST DEADLOCK, and
"create a role as step one" asks for exactly that shape.

Since phase 9 a cast runs on the caller's goroutine, and what the loop bought
is paid for where the writes are: the study is a version-guarded
read-modify-write on the board (retried on conflict, in the store's own
two-participant write), and `target-aria` is a patch on the ROLE form's
single writer. Two concurrent casts cannot lose each other's work, and two
casts producing two roles that both point here is what was asked for, not a
race.

The loop registers the study, then CROSS-CALLS out to the role form's own
writer to set `target-aria`. That is safe by `store.Form`'s contract: the
form writer does I/O and nothing else, and never calls back.

With `-O` or `-S` the role is MINTED by the call, born already cast: the
fork of the null form carries `outfit ⊕ {target-aria: me}` in its birth
patch, so there is no separate patch step to half-fail. If the figaro had to
be minted first and the role fork then failed, the response says so in
words, and `-j` carries both outcomes.

`fig study` and `fig drop` are callable on their own. They carry no
transactional guarantee. That is cast's job.

### The two entrances

The pair (a figaro and the role it plays) can be minted from either end, and
both entrances land in the same `autocast` in `internal/cli/cast_new.go`:

- `fig new -C [<@form>]`: the aria does not exist yet. The role positional is
  lifted out of the argv BEFORE the prompt parser sees it, because `new`
  refuses bare arguments and that refusal is right for everything except this
  one. The `@` sigil makes the lift lexical, and nothing after `--` is
  touched.
- `fig cast` with a FORM attended and no arguments: the role does not exist
  yet... rather, it does, and the FIGARO does not. Mint one and cast it.

Which thing the dressing dresses follows from which thing is missing, and
that is the only reading in which each invocation is unambiguous: with a role
named, `-O`/`-S` dress the ARIA; with no role named, they mint the ROLE.

Two objects across two writers is not a transaction, so the partial is a
REPORTED state and never an exit. By the time the cast runs the figaro
exists; `WithSessionFor`'s dying error path is deliberately not used, because
an aria whose id is never printed is an orphan.

## 4. The positional grammar

The form is always the LAST positional; `-O`/`-S` occupy the form slot.

```
fig cast <aria> <@form>      two positionals
fig cast <aria> -O <names>   one positional and a spec: the positional is the aria
fig cast <@form>             one positional alone: it is the form
fig cast -O <names>          no aria available, so one is minted from the default form
fig cast                     attending a @form: mint a figaro to play it
```

Every slot is self-checking. Kind validation names the error ("`x` names a
figaro, but this slot takes an unbound form"), and the `@` sigil makes
misuse lexical before it is semantic.

## 5. Observation, and why the rendering is the design

A cast aria studies the role, so the role's changes reach the model. That
path is [forms-design.md](forms-design.md) §8: pull at the stamp, derived
at translate time.

The RENDERING is part of the design, not decoration, and two rules were
bought at the haiku tier in a storm of fifty observers:

1. **One block per form, folded to its result.** A window carrying three
   patches used to render three bare `{"set":{...}}` envelopes. A model
   reading two blocks that both set `brief` cannot tell which is current,
   and one of them answered from the first: it reported the value the form
   held BEFORE the change it was being asked about. An intermediate value
   inside one window is not information, it is a trap.
2. **The body is structure, not prose.** One compact object naming the form
   and what moved. The skills contextualize it; the reminder states it.

```
<system-reminder name="study:@a5af1a83">
{"changes":2,"form":"@a5af1a83","removed":["stale"],"set":{"brief":"go"}}
</system-reminder>
```

`system.*` is skipped, as the board's own renderer skips it. A window of
nothing but system keys renders no block at all.

Determinism is load-bearing: encoded bytes land in the per-LT cache, so
members are sorted, keys are sorted by `encoding/json`, and the fold is a
pure function of the patch list.

## 6. The succession flow, worked

```sh
R=$(fig form new -S name=deploy-warden -j | jq -r .form_id)
fig cast $A $R                  # A studies R; R points at A
fig send --id $R -- "status?"   # answered by A
fig set --id $R brief "canary"  # A sees it on its next turn
fig cast $B $R                  # succession: R now points at B
fig send --id $R -- "status?"   # answered by B, no restart
fig drop $A $R                  # A stops observing; later patches are silent
```

`target-aria` is ONE pointer. Casting N arias into one role is N
repointings, and the last one wins. That is the semantic, not a bug: a role
is a seat, and a seat holds one occupant. Many arias may STUDY a role
without being cast into it, which is the fan-out shape.

## 7. What is proven, and what is not

Against a real provider, in `nix develop .#share-hush`, driven by
`~/notes/figaro/tests/roles/role-flow.sh` (opus, then three concurrent
sonnets, 14 checks each, all green):

- mint, cast, and the cast verdict
- the form namespace does not redirect; the figaro namespace does
- a patch to the role reaches the MODEL, and a second patch reaches it as a
  transition rather than a restatement
- repointing takes effect on the next call
- after `drop`, a later patch is not observed

Measured, not asserted: observation costs a warm translate 164ns at zero
observed forms, 329ns at one, 479ns at eight, and 4.3µs with 3.5KB at
fifty (`internal/provider/observation_bench_test.go`). A casting call is
~33.5µs; a role-targeted invocation pays one hub-served form read, ~22.8µs.

Not proven, and worth saying: delta-fanout completeness under storm volume
(the listener is a TUI, so correctness is e2e-tested but loss under load is
not), and cross-machine behaviour of any kind.

## 8. Invariants

1. `target-aria` is read late, per call, and never cached in a binding.
2. The form namespace never redirects.
3. Roles do not chain.
4. A bound form is never a role.
5. Cast never parks, and no longer needs the actor loop: each of its two
   writes is serialized by the form that owns it.
6. A minted role is born cast, in one fork.
7. The studied-form block is one per form, folded, structural, deterministic.

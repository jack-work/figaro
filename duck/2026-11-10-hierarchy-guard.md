# Hierarchy and the guard that constrains it

Status: accepted (Gluck's plan, 2026-11-10). Built in steps, below.
Source: Gluck's Telegram to King XII, 11-10 ~21:30-22:xx (verbatim at
`~/notes/spain/program/telegram-2026-11-10-reparent-layers.md`), El
Inspector XIV's read of reparent, and succession-tree (v0.63.0).

## Gluck's words

> Only the owner may reparent, but i think a reparent should only allow
> reparent to a child under its own ownership. Had that fact already
> naturally fallen out of existing restrictions? Id perhaps like to require
> that the receiving parent accept, like a two phase commit or something.
> [...] This feels like we should be separating the layer representing the
> hierarchy with the authorization layer, which constrains hierarchical
> operations. Constraints ought to be abstract perhaps via hooks that can
> allow the authorization layer to be injected as a dependency that conforms
> to its shape.

> If we could structure it that way today as part of this change or in fast
> follows, that would be great.

## Did it fall out? No

There are two trees.

- The storage tree is fork lineage. Grants follow it: a figaro's effective
  grants are its own intersected with its storage parent's effective.
- The drawn tree is what `ls` shows. promote, demote, the daemon's orphan
  re-home, and the @topology drawn override change it. The TTL sweep and
  `kill -r` walk it.

Up to 0.62.1, promote and demote check `set` on the moved figaro only. The
other node in the swap is never checked. That holds today only because the
owner has `*:*` locally. Under troupe grants, anyone with `set` on X could
promote X over its parent without any right on the parent, and so change
what a later `kill -r` or sweep takes. El Inspector XIV rates it low today
(grants follow storage, `kill -r` authorizes every node it takes, the sweep
never crosses a sandbox, and on spain `set` comes with `kill`). He will refuse
any grant that gives `set` without `kill` until this lands.

succession-tree (0.63.0) adds a storage re-parent: a move under a parent
that already holds the child's fork point, with no copying. Only the owner
re-parents storage. Anyone else's move is drawn only (El Inspector XIII,
S1), because a storage move can widen grants and can make a box's figaro an
ancestor that may write protected keys. S1 checks the caller only. Nothing
checks who owns the receiving parent. That is the gap Gluck names.

## Two layers

**Hierarchy** (the trees). It knows nodes, parents and owners as opaque
strings, and nothing about verbs, grants or principals' rights. Every
operation is expressed as a plan and applied in one place:

```go
type Move struct {
    Node, From, To string
    Tree           Tree // Storage or Drawn
}

type Plan struct {
    Op     Op     // Promote, Demote, Rehome, Arrange, Reparent, Kill, Sweep
    Moves  []Move // every node whose parent changes, in either tree
    Delete []string
}

type Verdict struct {
    Park    string   // non-empty: hold the plan on this receiving parent
    Accepts []string // who may accept it
}

type Guard interface {
    Check(ctx context.Context, by Principal, p Plan) (Verdict, error)
}

func (h *Hierarchy) Apply(ctx context.Context, by Principal, p Plan) error
```

`Apply` calls the guard itself, so no handler can skip it. Handlers build
plans and call `Apply`, and nothing else in the package writes an edge. The
orphan re-home runs as the system principal through the same path. The
hierarchy package imports no authz package. `scripts/check-boundary.sh`
enforces that, so the rule is in CI from the first commit.

**Authorization** (grants, policy, owners). It implements `Guard` and is
passed in when the daemon starts. A different node, a test, or a later policy
model supplies its own.

## The default guard

1. **The kill floor (El Inspector XIV).** The caller needs `kill` on every
   node whose drawn position changes. For promote that is the node and each
   ancestor it climbs past. For demote it is the node and every child that
   is re-homed. A move changes when a node gets deleted, so it is held to
   the verb that deletes.
2. **Storage re-parent: the owner, under his own (Gluck).** The caller must
   be the moved node's owner, and the receiving parent must have the same
   owner. A non-owner's move is drawn only, as in S1.
3. **The receiving parent consents (Gluck; King XII's `adopt`).** The caller
   needs `adopt` on every receiving parent. Without it, the guard returns
   `Park`: the plan waits on the receiving parent until its holder or owner
   accepts, then commits through `Apply` again. When both sides have the same
   owner, it is accepted automatically, so Gluck's own arranging costs nothing.
4. **Deletes take exactly what was authorized (El Inspector XIV).** `kill -r`
   and the TTL sweep compute their set, the guard checks `kill` on each node,
   and the store removes only those nodes (`RemoveAriaIf`). A node that moved
   in between is not taken. This closes the gap between checking and deleting.
5. **The system principal** may re-home orphans only: a `Rehome` plan, drawn
   tree, never a storage move.

Grants never read the drawn tree. Storage moves change effective grants only
under rule 2, which is an act by the owner on his own figaros.

## Steps

1. **Plans and the guard** (lane promote-guard, after the troupe re-sign and
   W20, on v0.63.0). Every topology operation is a plan applied through
   `Apply`. The default guard carries rules 1, 2, 4 and 5. Boundary rule in
   CI. Tests: a principal with `set` on X only is refused promote over P and
   demote onto C. A storage move under a parent with a different owner is
   drawn only. A canary that drops either check turns its test red.
2. **adopt and two-phase accept** (fast follow). The `adopt` verb, parking
   on the receiving parent, accept and refuse, and automatic acceptance for
   the same owner. Rule 3.
3. **Naming**, if Gluck rules (below).

El Inspector reads each step. El Cantero takes the hierarchy/authz split as
its first worked example of separating layers.

## Open: is "grant" the word?

King XII's answer: a role holds permissions (`verb:object`), a binding grants
a role to principals, and `system.grants` on a figaro narrows it the way an
OAuth scope does. He suggests `[roles.x] permissions`, `[[bind]]` unchanged,
and `system.scopes`.

The Engineer agrees. The rename breaks every policy file, every troupe
policy, and every board that carries `system.grants`, plus jkauthz's parser
names. Cheapest timing is before troupe policy v2 is signed. After that it
costs another re-sign window. Gluck rules.

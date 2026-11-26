# The peer check: your tool shell is you, never Gluck

With `[authz] peer_check = true` (the default on Linux), the daemon starts
every tool a figaro runs in that figaro's own cgroup,
`<the daemon's scope>/tools-<figaro id>`, and attributes each CLI
connection by the cgroup of the process on the other end. A call from your
bash tool, or from anything it starts (`setsid`, a double fork, a script, a
`figla run` armed there), is your figaro's call:

- `FIGARO_ARIA` is ignored for it. Unsetting it does not make you Gluck, and
  setting it to another id does not make you that figaro.
- It is never the owner. `FIGARO_CALLER` and the terminal's duke title are
  dropped; it is sent and judged as your figaro.
- Gluck at a terminal is unaffected.

`peer_check = false` restores the old behaviour, where the local socket
trusted every same-uid caller as the owner and the claim was honour only.

## What a tool shell may still do

- Everything it could do as its figaro before: open keys (`mantra`, your own
  notes), `send`, `fork`, `study` a role or an unbound form, `cast` within
  the placement rules, `kill` what its grants allow.
- Protected keys (`system.scopes`, `system.sandbox`, `system.fanout.*`) on
  its descendants, as an ancestor figaro.
- Shorten a lifetime, on itself, a descendant, a figaro it forks, or a
  figaro its written grant lets it retire (`set:*` in `system.scopes`):
  - `figaro fork -S system.ttl=2h ...`: a finite ttl at birth;
  - `figaro set --id <child> system.ttl 30m`: finite, and no later than
    the current one;
  - `figaro retire --id <child> --ttl 10m`, and `figaro succeed`'s
    abdication of itself.
- `figaro succeed`: the successor takes the holder's `system.ttl` from the
  daemon, so it holds the seat as long as the holder did without setting
  anything. A forked successor takes it at the fork; one named with
  `--successor` takes it when `figaro.seat` seats it, if the holder has not
  retired and the caller is the holder or Gluck. The holder may name only its
  own fork; no other figaro may seat a successor in a holder's place.

## What it may not, and the move for each

| Refused from a tool shell | The move |
| --- | --- |
| `system.ttl 0`, removing a ttl, a later deadline, unretiring | Ask Gluck; he runs it from his terminal. A seat's successor needs nothing: the succeed fork carries the holder's ttl. |
| Any other owner-held key on any figaro, its own included (`system.model`, `system.provider`, `system.tools`, `system.credo`, `system.keepalive`, `system.keep`, `system.retention`, ...), in `set` or in a `-S` birth patch | Pick an outfit that carries it (`-O`), since outfits are Gluck's; or ask Gluck to set it. |
| Protected keys on a figaro that is not your descendant, or on a role form | Gluck from his terminal. `~/notes/spain/bin/domesday-grant.sh` is one of these: run it from Gluck's terminal, never from a figaro's tool. |
| `figaro study <figaro id>` (another figaro's bound form) | Study the role it holds (`figaro study @role`) or an unbound form it writes to; or ask Gluck to add the study. |
| Owner-only topology moves, managed apply's operator paths | Gluck from his terminal. |
| `figaro succeed` on a role you do not hold | The holder runs it from its own shell, or Gluck from his terminal. |

## What it does not stop

The tools run as Gluck's uid. A tool that means to leave can still do so:
write its pid into another cgroup's `cgroup.procs` in Gluck's tree, start a
command through `systemd-run --user`, type into Gluck's pane with
`tmux send-keys`, or edit the store and config files directly. The check
stops a figaro that drops its claim by accident or by habit; confining one
that works against it needs a box.

A daemon that runs in a system-level unit (not under `user@UID.service`)
needs `Delegate=yes` on that unit, or it may not create the tool cgroups
and every tool refuses to start, naming the fix.

## One parent for every tool shell: `[tools.cgroup]`

By default each figaro's tools get `<unit>/tools-<aria>` beside the daemon,
with no limits. Set any key of `[tools.cgroup]` and the daemon, at start,
moves itself to `<unit>/daemon` (uncapped) and makes every tool cgroup
`<unit>/tools/<aria>` under one parent that carries the limits:

```toml
[tools.cgroup]
parent_cpu_max = "700000 100000"   # all tool shells together: 7 CPUs
parent_memory_high = "24G"         # needs Delegate= to include memory
tool_cpu_max = "150000 100000"     # each figaro's tools: 1.5 CPUs
tool_cpu_weight = 100
daemon_cpu_weight = 1000
```

A value the kernel would refuse stops the daemon at start. A limit the kernel
refuses at run time is logged and the tools still run, attributed.

Every process the unit holds at start moves to `<unit>/daemon` with the
daemon. Tool cgroups an earlier daemon left (`<unit>/tools-<aria>`, still
holding shells after a restart) are adopted: their processes move to
`<unit>/tools/<aria>`; a leaf that stays populated keeps its aria and the
per-tool cap until its shells exit. In this layout any caller inside the
unit that belongs to no figaro and is not the daemon is refused: never taken
for the owner.

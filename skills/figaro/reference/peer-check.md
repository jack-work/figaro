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
- Shorten a lifetime, on itself, a descendant, or a figaro it forks:
  - `figaro fork -S system.ttl=2h ...`: a finite ttl at birth;
  - `figaro set --id <child> system.ttl 30m`: finite, and no later than
    the current one;
  - `figaro retire --id <child> --ttl 10m`, and `figaro succeed`'s
    abdication of itself.
- `figaro succeed`: the successor forked with the seat takes the holder's
  `system.ttl` from the daemon, so it holds the seat as long as the holder did
  without setting anything.

## What it may not, and the move for each

| Refused from a tool shell | The move |
| --- | --- |
| `system.ttl 0`, removing a ttl, a later deadline, unretiring | Ask Gluck; he runs it from his terminal. A seat's successor needs nothing: the succeed fork carries the holder's ttl. |
| Any other owner-held key on any figaro, its own included (`system.model`, `system.provider`, `system.tools`, `system.credo`, `system.keepalive`, `system.keep`, `system.retention`, ...), in `set` or in a `-S` birth patch | Pick an outfit that carries it (`-O`), since outfits are Gluck's; or ask Gluck to set it. |
| Protected keys on a figaro that is not your descendant, or on a role form | Gluck from his terminal. `~/notes/spain/bin/domesday-grant.sh` is one of these: run it from Gluck's terminal, never from a figaro's tool. |
| `figaro study <figaro id>` (another figaro's bound form) | Study the role it holds (`figaro study @role`) or an unbound form it writes to; or ask Gluck to add the study. |
| Owner-only topology moves, managed apply's operator paths | Gluck from his terminal. |

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

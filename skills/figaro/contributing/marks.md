# Marks: the timing sink the transcript benchmarks read

Set `FIGARO_MARKS=<file>` on a CLI or a daemon and every instrumented site
appends one JSON line per event. Unset, the cost is one atomic load per site.
Both processes write the same file when given the same path; the reader joins
them on `t`.

Every line carries `t` (unix nanoseconds), `seq` (per process), `proc`
(`cli` or `angelus`), `pid`, and `m`, the name below. Durations are `ms`
as a float.

## Names

| m | proc | fields | meaning |
|---|---|---|---|
| `key` | cli | `chord`, `mode` | a keystroke reached the dispatcher; the start of every user-initiated measurement |
| `hop` | cli | `from`, `to`, `gen`, `ms` | one subject change, keystroke to seeded window; the phases below add up to it |
| `hop.dial` | cli | `to`, `gen`, `ms` | connecting to the new aria |
| `hop.read` | cli | `kind`, `ms`, `parts`, `err` | one read on the pager's behalf: `seed` (retarget), `enter` (cold pager), `catchup`, `history` (scrolling up), `page` (a jump) |
| `wire.rx`, `wire.tx` | cli | `conn`, `bytes` | bytes on one aria connection, as they pass |
| `aria.frame` | cli | `bytes`, `parts`, `live`, `deltas` | one `figaro.aria` notification arrived |
| `frame` | cli | `aria` or `view`, `bytes`, `full`, `content`, `tail`, `rows` | the terminal was written: a painted frame. `content` says the transcript had rows; `tail` says the view was following |
| `frame.quiet` | cli | `aria` or `view` | a render produced nothing the screen did not already hold |
| `submit.qua` | cli | `to`, `len`, `ms`, `active`, `err` | the prompt RPC, from call to reply |
| `queue.row` | cli | `id`, `state` | the queue intrinsic showed a row in this state |
| `queue.draw` | cli | `rows` | the drawer's contents CHANGED, and this many rows go on the screen: `0` is the drawer emptying |
| `runtime` | cli, angelus | `state` (+ `aria` on the daemon) | the turn disposition changed |
| `token.first` | angelus | `aria`, `turn` | the provider's first delta of a round |
| `fanout` | angelus | `aria`, `method` | one notification left the agent |
| `mem` | cli | `rss`, `heap_alloc`, `heap_inuse`, `sys`, `gc`, `goroutines`, `window_msgs`, `rowcache_rows` | sampled once a second and at exit |

## Derived metrics

- TTFCP (time to first contentful paint): for a hop, `frame{content:true}` with
  the new `aria` minus the `key` that started it. For a send: the first `frame`
  after `submit.qua` whose bytes rose.
- TTT (time to tail): first `frame{tail:true}` for the new aria minus the key.
- Hop cost: `hop.ms`, split by `hop.dial.ms` and each `hop.read.ms`; bytes are
  the `wire.rx` sum on the connection the hop opened.
- Frame jitter: intervals between consecutive `frame` lines while `runtime` is
  `thinking` or `tooling`; report p50, p95, max, and frames per second.
  `frame.quiet` counts renders that changed nothing.
- Submit latency: `key` (Enter) to `submit.qua` done; to the first `queue.row`;
  to `runtime{accepted}`; to `runtime{thinking}`; to `token.first` (daemon);
  to the first `frame` carrying it.
- Memory: `mem` over time; the daemon's RSS comes from `/proc/<pid>/statm`
  read by the harness.

## Profiles

`FIGARO_CLI_PPROF=<dir>` writes a CPU profile of the whole CLI session and a
heap profile at exit. `FIGARO_PPROF=1` on the daemon serves `net/http/pprof`
on `<runtime>/pprof.sock`. SIGUSR2 to a running pager profiles ten seconds.

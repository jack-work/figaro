# Telemetry

Three sinks, always on: `traces.jsonl`, `logs.jsonl` and `metrics.jsonl`
under `[telemetry] dir` (default the state dir), 16 MB plus one `.1` each.
`[telemetry] otlp_endpoint = "http://host:4318"` sends the same three
signals over OTLP/HTTP as well: batched spans, a bounded log queue, delta
metrics every 15 s (up/down counters and gauges cumulative). A dead
collector costs neither turns nor the files. `FIGARO_OTLP_ENDPOINT`
overrides the config: `off` exports nothing, any other value is the
endpoint. Every flake dev shell sets it to `off`, and a detached dev shell
drops the copied `[telemetry]` table as well, so a dev run never reaches the
live collector or the live files.

The resource carries `host.name`, `figaro.process` (`angelus` or `cli`) and
`service.version`: the release (`0.58.3`) for a release build, `0.58.3+dev`
for a dev-shell build, `0.58.3+dirty` for a dirty tree, and `dev-<rev>` (or
`dev`) for a build nothing stamped. Compare releases on plain `X.Y.Z` only.

Nothing below carries prompt or reply text: counts, sizes, timings,
names and error classes only.

## Spans a collector can rely on

`cli.submit` (client): from the keystroke (the composer) or the call (CLI
verbs) to the first painted frame carrying the reply. Events `rpc.acked`,
`reply.first_frame`, `reply.first_paint`; `cli.painted` false when the
process exits or resubmits first. Its W3C traceparent rides
`QuaRequest.traceparent`.

`figaro.qua` (daemon, one per turn): child of the first folded prompt's
`cli.submit`, links to the others. `figaro.id`, `figaro.turn`,
`figaro.model`, `figaro.provider`, `figaro.prompts`,
`figaro.queue_wait_ms`; tool events `agent.tool.*` with `tool_call_id`.

`figaro.methods`: inquiry methods before the prompt is appended.

`figaro.round` (one per provider request, child of `figaro.qua`):
`provider`, `model`, `provider.transport` (routed providers),
`gen_ai.response.finish_reason`, `gen_ai.usage.input_tokens`,
`.output_tokens`, `.cache_read.input_tokens`,
`.cache_creation.input_tokens`, `round.output_deltas`,
`round.tool_arg_deltas`, `round.first_output_ms`, `round.stream_ms`,
`round.itl_bounds_ms` with `round.itl_counts` (gaps between streamed
deltas; the last count is above the last bound), `round.output_span_ms`
and `round.streamed`, `error.type` on failure.

`round.output_span_ms` is the time from the first to the last output delta
that carried text, thinking or tool arguments, at microsecond precision; an
empty delta (a tool call's opening `input_json_delta` of "") does not start
it. `round.streamed` is false when the round has at least 32 output tokens
and that span is under 1 ms per 10 of them (faster than 10k tokens/s, past
any real decode): the output arrived in one burst, and its span says nothing
about generation speed. Copilot's messages
route does this to tool arguments (traced on 11-08: the opening empty delta
at once, nothing for 2.7 to 5.9 s, then every argument within about 20 ms);
its text streamed. Leave rounds with `round.streamed=false` out of TPOT.
Timestamped events: `provider.request_built`, `http.body_sent` (upload
done, `http.req_bytes`), `http.request` (response headers),
`http.first_byte`, `first_thinking`, `first_text`, `first_tool`,
`first_tool_arg`, `last_output`, `http.body_done` (`http.resp_bytes`),
`provider.retry` and `provider.reroute` (`error.type`).

## Metrics

Registered in `internal/otel/otel.go`. Daemon health: `figaro.mem.*`,
`figaro.live`, `figaro.gc.count`, `figaro.gc.pause_total`,
`figaro.wake.duration` (`woke_by`), `figaro.hibernate.duration`,
`figaro.decide.resting_routes` (`route`), `figaro.rpc.duration`
(`method`, `status`), `figaro.wal.sync.*`.

The collector that reads all of this is figscope
(`git@spain:jack/figscope.git`).

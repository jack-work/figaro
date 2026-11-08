# Telemetry

Three sinks, always on: `traces.jsonl`, `logs.jsonl` and `metrics.jsonl`
under `[telemetry] dir` (default the state dir), 16 MB plus one `.1` each.
`[telemetry] otlp_endpoint = "http://host:4318"` sends the same three
signals over OTLP/HTTP as well: batched spans, a bounded log queue, delta
metrics every 15 s (up/down counters and gauges cumulative). A dead
collector costs neither turns nor the files. The resource carries
`host.name` and `figaro.process` (`angelus` or `cli`).

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
deltas; the last count is above the last bound), `error.type` on failure.
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

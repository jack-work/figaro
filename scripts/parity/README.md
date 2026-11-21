# Pager parity harness

One scenario file, `scenarios.json`, drives the real pager here and figaro web
in zanni (`web/parity`). Each step names an action (`do`) and the structured
facts to assert (`expect`); the pager and the browser must satisfy the same ones.

Run the pager side (isolated daemon, private tmux server, fake Anthropic
provider, throwaway hush vault removed afterwards):

    python3 scripts/parity/pager.py --out /var/tmp/parity-$(date +%F) [--scenario send] [--keep]

Output per step in `<out>/pager/<scenario>/`: `<step>.txt` and `.ansi`
(`capture-pane`, plain and with `-e`), `<step>.facts.json` (read from
`figaro show -j`, `state show -j`, `list -j`, `queue ls -j`). `results.json`
lists pass or fail per step.

Pair with the web screenshots into one page:

    python3 scripts/parity/report.py <out>

Facts: `turns` (by turn number: inquiry, nodes with type, role, tool name and
status, text or output substrings, `open`, form deltas with old, new and header
glyph), `running`, `state`, `tree.edges`, `pit` (rows in order, footer, glyph),
`screen.contains`, `glyphs_on_screen` (names from `glyphs.json`).
`glyphs.json` is the pager's table; `go test ./api/glyph` fails when it drifts,
`PARITY_GLYPHS_WRITE=1 go test ./api/glyph` rewrites it.

`fork` at_turn N keeps the turns before N: `:fork -f :2` leaves turn 1.

The provider script lives in `scenarios.json` under `provider`: a rule picks
by prompt text, a round by how many assistant messages follow it (the tool loop).
`hold` delays a round, `pace` spaces the streamed chunks, `fail` answers the
first N requests with an HTTP error.

Pager facts not read structurally: pit rows, footer text and the delta header
glyph come from the captured screen, since no `-j` surface exposes them.

# Reservations

A lane owns the globs in its row and nothing else. The integrator rejects a
PR that touches a glob its lane does not own. Seams (`api/sandbox`,
`api/rpc/door.go`) are frozen after wave 0; change them through the
integrator.

## Wave 1

| lane | owns | builds against |
|---|---|---|
| `boundary` | `cmd/**`, `internal/cli/{angelus,provider,doctor*,show_window}.go`, `internal/livelog/**`, `sdk/**` | `api/` |
| `sandbox` | `internal/tool/**`, `internal/sandbox/local/**`, `internal/angelus/restore.go:139`, `internal/angelus/topology_handlers.go:136` | `api/sandbox` |
| `figwal` | `internal/store/{segment,disk,log,xwal,tree}/**`, `figwal/{wal,forest,tree}/**` | `figwal` |
| `snap` | `figwal/snap/**` | `figwal` |
| `door` | `internal/authz/**`, `api/rpc/**`, `internal/angelus/{hub,protocol}.go` | `api/rpc/door.go` |

## Wave 2 (after the parent merges)

| lane | parent | owns |
|---|---|---|
| `cli-split` | boundary | `internal/cli/transcript*`, `internal/cli/stream.go`, `internal/cli/livelog_bridge.go`, `internal/pager/**`, `internal/input/**` |
| `sandbox-uid` | sandbox | `internal/sandbox/uid/**` |
| `figbox` | sandbox, door | `cmd/figbox/**`, `internal/sandbox/box/**`, `internal/cli/angelus_client.go` |
| `tenants` | figwal, snap | `internal/store/*.go` |
| `federation` | door | `internal/angelus/peers.go`, `internal/cli/peer*.go`, `nix/**` |

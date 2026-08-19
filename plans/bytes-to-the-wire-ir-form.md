# DISK <-> WIRE: THE FIG IR CHANNEL AND THE FORM CHANNELS, AS AN ORDERED CALL TREE

Aria 6ec565b5. Section 3 of Gluck's enumeration.

    REF:     feat/delta-seam @ 8fe7b895  (go.mod pins figwal
             v0.18.1-0.20260819002022-d3ea52f23e52 -- every figwal line below is
             from THAT version, read in the module cache)
    COMMITTED ON: enum/ir-form off feat/layered-cache, WHICH IS NOT THE REF IT
             DESCRIBES. Said plainly because a document that names one tree and
             lives in another is how a claim goes stale without anything going
             red. The deltas that matter: XwalStore.openNode is unexported on
             layered-cache and exported here; the projection's warm start
             (Previous) is deleted on the described ref and alive on the one
             this file sits on; layered-cache pins figwal v0.18.0, so its
             segment/xwal line numbers may differ from the ones above.
    METHOD:  [R] = READ by hand.  [A] = confirmed by 9ed3f561's callpath (VTA).
             Every frame is [R] until the harness emits its tree; the
             copied/reshaped/by-reference column is [R] permanently -- no
             callgraph infers it.
    ORDER:   execution order, parent to child. A callee invoked at two call
             sites appears TWICE.
    HOLES:   named at the bottom. Unnamed gaps are worse than a short list.

## TRACK A -- READ: THE FIG IR CHANNEL, DISK TO THE ENCODE BOUNDARY

```
figaro.Agent.runTurn                                    figaro/turn.go:415         [R]
+- figaro.Agent.formAccessor                            figaro/agent.go:1084       [R] returns nil when backend==nil -> THE EPHEMERAL BRANCH
+- figaro.Agent.studyAccessors                          figaro/agent.go:1158       [R] see TRACK B'
+- newDeferredAppendLog(a.figLog)                       figaro/turn_repair.go:275  [R] wrapper; BY REFERENCE, no copy
+- provider.Provider.Send                               DISPATCH [CANDIDATE 1/4] anthropic/anthropic.go:961
|                                                       [2/4] anthropicsdk [3/4] copilot/responses.go [4/4] openaichat
|  +- <provider>.cacheFor                               anthropic/anthropic.go:98  [R] nil when CacheOpen==nil (EPHEMERAL: no cache at all)
|  +- <provider>.catchUp                                anthropic/anthropic.go:1180 [R]
|     +- provider.ProjectIncrementally                  provider/projection.go:115 [R]
|        +- store.TailAfter                             store/log.go:96            [R] chooses the span
|        |  +- store.Log[T].Read / ReadFrom             DISPATCH on store.Log[message.Message]
|        |     [CANDIDATE 1/4] cachedLog[T].Read        store/cached_log.go:133    [R] BY REFERENCE: slice of the published window
|        |     |  +- (resident)                         --                          [R] [HIT -- COMMON CASE] no I/O below this line
|        |     |  +- (trimmed) inner.ReadFrom           store/cached_log.go:159    [R] [MISS] falls through to the xwal log
|        |     [CANDIDATE 2/4] xwalLog[T].Read          store/xwal_log.go:82       [R]
|        |     |  +- xwalLog.openOnce                   store/xwal_log.go:72       [R] opens+closes a HOT SHARED handle per call
|        |     |  |  +- XwalStore.openNode              store/xwal_store.go:463    [R] (exported OpenNode on this ref; unexported on layered-cache)
|        |     |  |     +- xwal.Trunks.Head             figwal xwal/trunks.go:1048 [R] borrowHotUntracked -> shared *XWAL
|        |     |  +- xwal.XWAL.ReadAt                   figwal xwal/xwal.go:1834   [R]
|        |     |  |  +- log.Log.Read                    figwal log/log.go          [R] pending buffer first, then inner
|        |     |  |  |  +- disk.Log.findSegment/ReadIndex figwal disk/log.go:424   [R]
|        |     |  |  |     +- segment.Segment.ReadIndex figwal segment/segment.go:269 [R] **THE BRANCH GLUCK'S TREE MUST SHOW**
|        |     |  |  |        +- Segment.cachedPayloads figwal segment/cache.go:113 [R] [HIT -- COMMON CASE] returns the RESIDENT [][]byte.
|        |     |  |  |        |                                                     NO SYSCALL. Global budget: segment.SetCacheBudget,
|        |     |  |  |        |                                                     eviction by idle epoch. RAISED to Gluck.
|        |     |  |  |        +- codec.JSONLCodec.ReadFrame figwal segment/codec.go:186 [R] [MISS]
|        |     |  |  |           +- make([]byte, n)     figwal segment/codec.go:190 [R] COPIED: a fresh buffer per frame
|        |     |  |  |           +- os.File.ReadAt      (runtime)                   [R] io.ReaderAt on *os.File
|        |     |  |  |              +- internal/poll.FD.Pread (runtime)             [R]
|        |     |  |  |                 +- syscall.Pread (runtime)                   [R] THE ONLY SYSCALL ON THIS PATH, MISS ONLY
|        |     |  |  +- xwal.decodeRecordFrom           figwal xwal/xwal.go:2286   [A] RESHAPED: frame -> xwal.Record (m, payload, meta, cursors)
|        |     |  +- store.decodeRecord[message.Message] store/xwal_log.go:52      [A] RESHAPED+COPIED: json.Unmarshal -> Entry[message.Message].
|        |     |                                                                    THE BYTES STOP BEING BYTES HERE.
|        |     [CANDIDATE 3/4] MemLog[T].Read           store/mem_log.go           [R] EPHEMERAL arias and tests
|        |     [CANDIDATE 4/4] deferredAppendLog.Read   figaro/turn_repair.go:283  [R] delegates to 1-3
|        +- (per record) provider.lookupCached          provider/projection.go:444 [R] fd15d2a0's track -- the cache cursor
|        +- (per record, MISS) config.Form.PatchesBetween provider/projection.go:241 [R] -> TRACK B
|        +- (per record) config.Encode(msg, snap)       provider/projection.go:297 [R] **FUNC VALUE IN A STRUCT FIELD -- HOLE 1**
|           +- <provider>.encode                        per provider               [R] RESHAPED -> []json.RawMessage (provider-native)
|              +- THREE OF FOUR PROVIDERS DECODE AND RE-ENCODE THE CACHED BYTES EVERY TURN (fd15d2a0's finding);
|                 copilot/responses embeds them as json.RawMessage and marshals ONCE.
+- (body assembly, transport)                           fd15d2a0's track           -- ends at net/http -> syscall.Write
```

## TRACK B -- READ: THE FORM CHANNELS (THE BOARD), TO THE SAME BOUNDARY

```
provider.ProjectIncrementally                           provider/projection.go:115 [R]
+- config.Form.PatchesBetween(lastForm, entry.FormChannelVersion)  projection.go:241  DISPATCH on provider.Form
|  [CANDIDATE 1/2] figaro.formView.PatchesBetween       figaro/agent.go:1248       [R] COPIED: allocates the delta slice
|  |  +- slog.Debug("form patches between", ...)        figaro/agent.go:1250       [R] LOG, error arm only
|  |  +- store.XwalBackend.FormPatchesBetween           store/xwal_backend.go:379  [R]
|  |     +- store.Form.PatchesBetween                   store/form.go:269          [R] BY REFERENCE into the resident patch window
|  |        +- slog.Warn("form patches from log", ...)  store/form.go:303          [R] LOG, fallback arm
|  |        +- (fallback) FormLog.RangePatches          DISPATCH on store.FormLog
|  |           [1/3] xwalFormLog.RangePatches           store/form.go:717          [R] -> xwal.XWAL.ReadAt -> (TRACK A's figwal subtree)
|  |           [2/3] stumpFormLog.RangePatches          store/topology_form.go:63  [R] topology AND EVERY LIBRETTO
|  |           [3/3] MemFormLog.RangePatches            store/form.go:628          [R] test/memory
|  [CANDIDATE 2/2] figaro.librettoView.PatchesBetween   figaro/agent.go:1191       [R] -> TRACK B'
+- config.Boards -> store.SnapshotCursor.At             store/snapshot_cursor.go:95 [R] "the board AT a version"
|  +- store.FormSnapshotSource                          store/form_snapshot.go:64  [R] segment header + bounded fold
+- config.Encode(msg, snap)                             projection.go:297          [R] board + delta rendered by the encoder
```

THE FORM CHANNEL IS REPLAYED ONCE AT OPEN (`store.OpenForm`, store/form.go:143)
and served from memory thereafter -- so at render time the board is usually
NOT read from disk at all. A THIRD copy of the same information is
`internal/form.State`, the agent's in-memory board, which reaches the wire as
`SendInput.Snapshot` (model selection, `system.environment.*`, reminders) and
is not read from the channel at render time. It is the copy that lost a study
tonight.

## TRACK B' -- THE STUDIED MEMBERS

```
figaro.Agent.studyAccessors                             figaro/agent.go:1158       [R]
+- StudiesFromSnapshot(a.form.Snapshot())               figaro/study.go:41         [R] reads the IN-MEMORY mirror, not the channel
+- librettoBackend.Libretto(fid)                        store/xwal_backend.go      [R] shared instance; a second Form over the stump would freeze
+- librettoView.PatchesBetween                          figaro/agent.go:1191       [R] COPIED + FILTERED (bookkeeping keys stripped)
   +- store.Libretto.PatchesBetween                     store/libretto.go:142      [R] the libretto's OWN Form -> stumpFormLog (TRACK B [2/3])
```

## TRACK C -- WRITE: BACK DOWN TO A SEGMENT (the direction fd15d2a0 flagged as missing)

```
figaro.Agent.appendMsg / writeStudyMark / applyControlPatchVerdict            [R]
+- store.Log[T].Append                                  DISPATCH (cachedLog -> xwalLog -> MemLog)
|  +- cachedLog.Append                                  store/cached_log.go:257    [R] admits to the window AND
|  +- xwalLog.Append                                    store/xwal_log.go:387      [R] RESHAPED: json.Marshal(payload)
|     +- xwal.Trunks.Append                             figwal xwal/trunks.go      [R] poison gate + dirty bookkeeping
|        +- log.Log.Write                               figwal log/log.go:166      [R] writes to the PENDING BUFFER; readable immediately
|           +- (lag over maxLag) log.Log.SyncThrough    figwal log/log.go:224      [R] [BRANCH: only when pendingBytes > maxLag]
|              +- disk.Log.Write                        figwal disk/log.go         [R]
|              |  +- segment.Segment.Append             figwal segment/segment.go:198 [R]
|              |     +- codec.Frame                     figwal segment/codec.go    [R] RESHAPED: payload -> framed bytes
|              |     +- os.File.WriteAt -> syscall.Pwrite (runtime)                [R]
|              |     +- Segment.extendBlock             figwal segment/segment.go:220 [R] **THE WRITE POPULATES THE READ CACHE**
|              +- disk.Log.Sync -> Segment.Sync -> os.File.Sync -> syscall.Fsync   [R] the fsync every durable claim rests on
+- (form patches) store.Form.applyEffect                store/form.go:375          [R] ONE DRAINER; reduce is atomic with the append
   +- json.Marshal(applied)                             store/form.go:547          [R] RESHAPED
   +- FormLog.AppendPatch                               store/form.go:551          [R] DISPATCH [1/3 xwalFormLog 2/3 stumpFormLog 3/3 MemFormLog]
      +- (same figwal subtree as above)
```

## HOLES

1. **`config.Encode` is a func value in a struct field**, bound per provider,
   called at projection.go:297. Every path in TRACKS A and B ends there. If VTA
   cannot flow the closure through `ProjectionConfig`, THE LAST EDGE BEFORE THE
   WIRE IS INVISIBLE. Unresolved at the time of writing.
2. **Provider selection is registration at init** (`provider.Lookup`, each
   `reg.go`). A graph rooted at a test sees none of it.
3. **Reflection**: every RESHAPED row is `encoding/json`; edges into
   `MarshalJSON`/`UnmarshalJSON` (including `form.Snapshot`'s custom pair) are
   not calls.
4. **Goroutine edges**: the libretto fold and the form drainer run on their own
   goroutines. The `go` edge is a call; the byte crossing is not.
5. **NOT A HOLE ANY MORE**: figwal resolves with full file:line under a cut of
   `./internal/store/...`, and generic instantiations are distinguished
   (`decodeRecord[message.Message]` vs `decodeRecord[[]json.RawMessage]`).

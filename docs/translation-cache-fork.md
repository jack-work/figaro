# Translation Cache Inheritance on Fork

## Finding

The translation cache (`translations/<provider>`) does NOT inherit on fork.
The xwal forks all channels present at fork time, but the translation channel
is added lazily via `ensureChannel`/`AddChannel` (not part of the initial
`storeConfig().Channels` list). When a child aria opens its translation cache,
`ensureChannel` creates a NEW empty channel rather than inheriting the parent's.

The `ir` and `chalkboard` channels inherit correctly because they are declared
in the xwal Config at tree creation time.

## Impact

Every forked child re-encodes the entire inherited conversation on its first
turn. For a 1000-message parent, this adds ~20s of pure encoding overhead.
Subsequent turns are fast (the in-memory snapCache kicks in).

## Root Cause

`translations/<provider>` is not in `storeConfig().Channels`. It's created on
first use via `XwalBackend.ensureChannel` → `xw.AddChannel`. The xwal only
forks channels that exist on the node at fork time. Since the translation
channel is added after the loadout/conversation node was created (on the first
provider Send), it misses the fork.

## Possible Fixes

1. **Add translation channels to the config at creation time.** Requires
   knowing the provider name upfront (dynamic: "anthropic", "copilot").
   Could register known providers' channels when the loadout is created.

2. **Copy the parent's translation channel to the child post-fork.** After
   `ForkTail`/`ForkAt`, check if the parent has translation channels and
   `AddChannel` + backfill them on the child. The xwal's fork mechanism
   would handle the inheritance if the channel existed before the fork.

3. **Ensure translation channels exist before any fork.** On first Send,
   `ensureChannel` runs. If we also ensure it on the loadout node (the
   common ancestor), all descendants inherit it.

4. **headSnap optimization (partially implemented).** Even without cache
   inheritance, use `in.Snapshot` (the current chalkboard state from
   `ChalkboardState`/`StateAt`) to skip the patch replay for cached
   entries. This was added in the current branch but doesn't help when
   the translation cache itself is empty (all entries need encoding).

## Current State

The in-memory `snapCache` makes second+ turns instant (~200ms). The
`headSnap` optimization avoids patch replay for cached entries. The
remaining gap is the first turn after fork/daemon-restart when the
translation cache is empty: all inherited messages must be re-encoded.
Fix #2 or #3 would close this gap.

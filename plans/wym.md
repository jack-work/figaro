# WYM: What you mean? (Also clarifications)

1. The one that could change the design ( [q12] ). Exact intervals mean the libretto's actor extends the open end as the source moves: one libretto patch per source patch, per observing figaro. Fifty observers on a busy form is fifty libretto writes per patch. The cheaper alternative is to write the end only on drop, leaving  null  while open, but then the translator cannot bound a window from the libretto alone and the IR has to go back to stamping per-form source versions, undoing the reframe. I want exact intervals with the benchmark built first (1, 8, 50 observing librettos, measuring libretto patches and fsyncs per source patch). Confirm, or take open intervals?
   - Wym by "exact intervals"?
   - wym by "extends the open end" and "open"?
   - wym by "exact intervals"? and "open intervals"?
   - I get what you mean by one libretto patch per source patch, per observing figaro, though. Here is my answer: Each form observed by any figaro is represented with only one libretto, which holds the derivation for exactly one form.
     - librettos should NOT fork with figaro. We've wavered on this, I'm back with no-fork.
     - fig study should update the libretto for that studied form. It's effectively a copy of only the data we need from the form.
     - other derived forms can validly copy state and consume multiple different forms. Libretto is strictly one libretto per studied form, NOT per studied figaro.
     - figaro study should update a reserved bound form value, study-set. That should be a hash map mapping the studied form to its libretto.
     - updates to the bound form happen on a different actor loop, so we are good in that regard wrt concurrency.
     - the studies map can only be updated via the study verb.
     - ref count of studied figaros should be updated in the libretto.
     - the study verb / study rpc api should be special, it should ensure that the ref count and the figaro studies hash map are consistent. we should have a way to do an atomic patch. Go ahead and design this as you see fit according to prior art. It should not block the figaro actor loop, but can block the bound form loop and the libretto loop to ensure the counter stays consistent.
     - drop should do the inverse.
     - now multiple figaros can share a libretto. We have consistent ref counting. We are but one step away from removing the copy altogether. The future would be when we implement compaction. The libretto would record the LTs of the source form, and would also contain the ref counter, and we could derive compaction based on the ref form.
     - the key of the libretto should probably be well defined. we can encode it as libretto::<formid> or something. that way, a form can know the libretto it corresponds to for future ref counting compaction work.
     - I suppose that this design also means that the figaro only needs to keep a hash set including the form id, and can infer the libretto:: prefix from the fact that the form ids are found in the studies collection.
     - copy the state for now, because I don't want to implement real compaction in this pass, but leave a note saying once compaction is available we can update the librettos to record LT ranges.
     - in the meantime, forms can be deleted, and the libretto should simply record the deletion and stop listening.
     - if a form with the same id is created again, the system should be able to handle it, since patches are fully persisted.
     - deletion of a form should transmit to the libretto. the bound form ref to that libretto may remain. The figaro will see no patches since the deletion, but semantically it still studies the dead-form, in case it ever comes back. drop on a dead form should be legal, it will just remove the subscription from the libretto and update the bound form to no longer reference the libretto / studied form in a transactional atomic patch.
2. Sync failure ( [q01] ). N records appended, the fsync fails, nothing published, and figwal's flusher may still write them later so "failed" does not mean "did not happen". I want to poison the form: refuse further writes, let the next open recover from what is durable. Agree? And does a poisoned form take the daemon down, since it means the store is failing?
   - on fsync failure, we just reject the patch. It should not get appended in memory until after it is fsynced to disk. It is a true wal.
   - we should disable the flusher. figwal is a true wal. that code can be removed wholesale, though perhaps we keep it around for now if we release figwal and want some configs to still use it. All of figaros usage should be a mandatory fsync-before-reduce / update the in memory window.
3. fsync before publish on all log operations includes the IR, yes. We are reversing decisions made in the past when I didn't know what a wal was. I realize this might slow perf, but we should be able to get it back through clever optimization.
4. Let's handle these as a fast-follow. I want to know more about these locks. Write a document describing them in addition to continuing with your plan.
5. No, we'll do everything in incantations before merging to main.  Treat that branch like it's the bona fide copy of source.  If anything goes to main first we will rebase, but this is my primary focus so it probably wont happen.

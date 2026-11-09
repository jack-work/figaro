# tree: the one window shape

The design lives in the package comment of `figwal/tree`. This note keeps
the one rule a consumer has to hold that the code cannot state.

## Installing Evicted: it takes no lock

`Evicted` fires outside every tree lock so a lower layer can clear its
fast pointer. The inversion runs the other way and is the hazard: a consumer
that calls `Put` while holding its own write lock can have eviction pick one
of its own runs, so `Evicted` runs with that lock held. A hook that needs
the lock deadlocks -- and only under budget pressure with concurrent
readers, which is the shape that reaches production first.

The payload cache is safe by accident of design: `Segment` clears an
atomic.Pointer and takes nothing. Consumers should hold that line
deliberately -- **an Evicted hook does a pointer swap and nothing else**. If
it needs more, publish a successor value instead of mutating the held one.

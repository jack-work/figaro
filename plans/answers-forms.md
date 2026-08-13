1. q13 -- lets scrap the form projection now. Whole-form is the only option (for now). For librettos. For api level derived forms, the projection stands, since those can be created by the user as often as they'd like, even duplicates of the same derivation (consider deduping later via value comparisons, but for now, the librettos are just whole-form projections only for those figaros listening)

2. @libretto::formid I guess. Deterministic id derived from the source id is what I want. I'd like to allow unbound forms to have a user-specified name. Only the bound forms/figaros/arias should require system generated ids. For now, bound forms can retain their system-generated ids, but in the future we'll want to change that and allow forms to be created from a client specified key.

3. gate is fine.  don't remove. But figaro should exclusively use no-flush, fsync-before-publish semantics.  No exceptions.

----

non-blocking:
- pure reduce precedes fsync is proper.  that is what I expected.
- N cursors in the IR record is fine.  They are all pretty small anyway.
- i think this work, but let's talk through it -- after you are done.  I need to see the code to decide but I think I'm comfortable with two-participant write, but not two phase commit.  Though a two phase commit is probably okay too, but we'll save it for follow up work.

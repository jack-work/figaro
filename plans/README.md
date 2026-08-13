# plans

| file | what it is |
|---|---|
| [durable-forms.md](durable-forms.md) | The design of record for the state layer: what and why. Principles, the primitive, the protocol, deletion, the topology form, derived forms, the API refactor. |
| [state-layer-implementation.md](state-layer-implementation.md) | How, at the level of function bodies. The phase table, the code, the lock rule, the validation bank. |
| [progress.md](progress.md) | The running log: what landed, with commit refs, deviations, measurements, traps and the next-steps queue. **Read this first.** |
| [lock-audit.md](lock-audit.md) | Every mutex in the packages this touches, the rule that decides each, and the two fast-follows. |
| [form-view-perf.md](form-view-perf.md) | The earlier perf work: the patch view that replaced a copy, with before and after. |
| [form-projection-followups.md](form-projection-followups.md) | Older followups; §1 is closed by form-view-perf. |
| [forms-and-roles-v2.md](forms-and-roles-v2.md) | The forms and roles brief. Parts of §7 are overturned by durable-forms; see there. |
| answers-forms.md, wym.md | Gluck's rulings, verbatim. |

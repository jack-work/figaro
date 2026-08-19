# Figaro todo

some todo items
- the write file output should be streamed to console the same way that bash is
- autocomplete is still extremely slow.  my aria list is not so long that it ought to take that long.  Not sure whats going on
- bookend consistency issues.  should always display aria id.  Should consistently separate messages rather than randomly.  Probably an opportunity to systemetize; i assume lots of ad hoc impls
- there should be some loading spinner indicating that figaro is thinking

- sometimes, only observed in sequences tool call blocks immediately interleaved with thinking blocks, text is duplicated, and overwritten by subsequent input.  Its an improvement over the last UI, but still annoying.

- changing pane size in tmux has inconsistent behavior based on whether transcript is open when the resize occurs (works fine) but 

- transcript mode should be more efficient.  rerender on close should not cause tearing.
    - transcript mode should be pageinated properly.  should not keep the entire transcript in memory.  should keep a few pages open.
- transcript mode should have more robust search functionality
    - selections should be highlighted
    - j/k should be remapped to standard visual block selection, with similar semantics to vim or tmux with vim keybindings
        - v should highlight at character.  V should highlight the entire line. y should yank.  esc shouild exit selection
        - ctrl+p/n should go up block by block (as in the blocks that constitute a figaro message) (should use visual highlight on each node)
        - ctrl+shift+p/n should go up message by message (many blocks in the assistant response, )
        - ctrl+j/k can go up message by message (since there are many blocks to a figaro)
        - e/y should move the screen up or down a bit without moving the cursor with vim dynamics
    - enter on supported block should expand its input params
    - o on a supported block should show the full output that is available (not sure if we discard bash output.  might be fine to for perf reasons, or can write to file)

- expose setting to bash tool for cases
    - where figaro should show the user the full output
    - ^, and also should not see the result of the operation
    - this is impossible to do otherwise since the output should be shown in the figaro stdout / transcript TUI

- fig send -e and fig kill should be soft-deletes and reversible depending on globally configurable delay

- angelus should also map a fig attend history stack to the process in addition to the currently attended figaro, with a configured size limit, where oldest entries are dropped on overflow.
    - stacks should also be attendable by other processes.

- figaro default behavior should be to bind shell to new fork, stay should keep figaro at the current trunk.

- inconsistent newline UI.  sometimes there is a newline before text in a message following separator, sometimes there is not.

- fig attend ~ doesn't really work.  ~ needs quotes which makes the syntax weird.  we can disable the functionality completely.  "fig new", without any aria can have the same semantics aswhat fig attend "~" does

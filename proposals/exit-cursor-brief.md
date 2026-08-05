On windows, scrollback sometimes is written on close to positions in my terminal the succeed my cursors location. that is incorrect. we need to make sure the cursor postion for the next prompt is located before written scrollback content on windows. that will be hard to validate but with thorough searches in brave on the internet you should be able to pinpoint the bug and fix it.

Additionally, and unfortunately it is hard to repro, but perhaps under the same circumstances, nothing apparent is written to native scrollback on close, or if it is, it is occluded by the resultant terminal, and all scrollback content lives above the visible portion of the window buffer. In any case, it is not visible in my terminal as id have expected. Id expect the cursor position to be located at the bottom of the window buffer or as far beyond as any content was present originally, but immediately preceded by the tail buffer length configured for writeback on disconnect of transcript mode. This inconsistency is frustrating. You may be able to reproduce with tmux in a dev shell. it seems to occur for large responses, possibly with long wait time after they are opened. Search the internet based on your findings in the code and report back to me when it is completed and ready to be tested. Note this may require we maintain state of the scrollback content or cursor position prior to entering the transcript mode. Not sure.

But "cursor position", I mean the place where my keyboard input goes, below my prompt, like so:

gluck in ~/dev/figaro-qua on  main on 󱇶 on 󱌃 3c3269a1 ◐ 38.5% took 13s
❯

Additionally, post status bar tail content on close should be removed entirely. the status should be fully encodable in the status bar, where disconnected is seen. We needn't the latter two messages, the disconnected - turn continues and follow: figaro listen ...
──────────────────────────────────────────────────────────────────── aria 5fd16081 ───
disconnected ⠸ · ctx ~10.3k/1.0m 1.0% · cost 643 tok · 02:30:56

─── [disconnected — turn continues] ──────────────────────────────────────────────────
follow: figaro listen 5fd16081

When you are done, a new task that you can take after I review the previously commissioned task. Read it and perform research and be prepared to carry it forward on my command, if you have context:

I think the status indicator needs a little work in general. content that overflows the status bar cannot be seen in full. I think we need a hotkey to show it, and it should cause the status bar height to increase. Furthermore, the queued items should be place underneath the status bar rather than over it. They should cause the status bar to grow the number of lines necessary to see all queued items. Capital Q can be used to view the queued items in a list, truncated to a few rows at max. Q again should focus the queue and allow navigation keybindings to toggle through the queue. Enter on any of these items should expand it and let it be readable in the few rows of space beneath the status bar. It should vanish when the queue is consumed. This might require a protocol change to the queue so that updates to the queue are pushed via a similar semantics to other delta updates. postulate what might make for a good semantics and api for this.

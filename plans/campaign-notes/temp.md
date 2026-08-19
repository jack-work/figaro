❯ first, this all works really well, can you merge and push this to remote main?  or ff if it's just behind.  and cut a new tag.  then, couple small qol things:  [Pasted text #1 +3 lines]
  these separators after close are different lengthed. There is also a full newline between the status line and the transcript info: [Pasted text #3 +3 lines].  there really shouldn't be the
  whole extra newline there.  Furthermore, I think we should allow "?" during transcript mode. figaro dev-da2abf499885 → v0.3.0 available  ·  run: nix profile upgrade figaro   # or your flake
  input, then: nix profile upgrade '.*' <-- also this message should be more cleverly hidden, and perhaps only shown in the help menu.  And also, ─── 9384f058 · 10:08:19
  ──────────────────────────────────────────────────────────────────────────────
  ^ that status line, and the various status lines in transcript mode, should be converged as much as possible.  I want output to appear nearly identical in incipit vs. transcript mode, and
  typically only one status line is needed.  That means the ^D to quit, the help indicator, and the aria id, and the time (and various other details) should all be combined into that footer.
  When the ? is triggered, is it possible to do an alt screen on top of the alt screen?  Im considering a floating window, where the text proceeds behind it, or if that's challenging then
  simply a new alt screen that has the help menu that is wiped on q or ESC, or maybe the bottom status section just gets large enough to display all the help section, or as much as possible, while the output still goes by above.  Idk.  Maybe lets go for simple for now.  Please provide your honest recommendation, and cunsult the ~/.config/figaro/skills/cli-design.md for help designing the cli

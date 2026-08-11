---
name: treebear
description: Explains the "treebear" Git workflow pattern, a bare repository with git worktrees as children. Use when the user mentions treebear, bare repo worktree setups, or asks about managing multiple branches via worktrees.
---

# Treebear

Treebear = bare Git repo + worktrees as child directories. Clone with `git clone --bare`, fix the fetch refspec (`git config remote.origin.fetch '+refs/heads/*:refs/remotes/origin/*'`), then `git worktree add <path> <branch>` for each branch you want checked out. All worktrees share one object store, so you get simultaneous branches without multiple clones or stashing. Keep the bare root named `repo.git` so it's clearly not a working tree. The bare repo is the tree trunk; the worktrees are branches. Bear with it. 🐻🌳

# Git Worktrees for Concurrent Development

Git worktrees let you check out multiple branches in separate directories that
share one repository history. This enables multiple Claude Code instances (or
developers) to work on independent tasks in parallel without stepping on each
other.

## How worktrees work

A worktree is a linked working copy of a repository. Each worktree:

- Has its own checked-out branch and working directory.
- Shares the `.git` object store with the main repo (commits, history, remotes, hooks, config).
- Cannot share a branch with another worktree — each must have a unique branch checked out.
- Is lightweight (no full clone, no extra remote bandwidth).

## Single-machine workflow

The simplest case: one machine, one or more concurrent tasks.

### Layout

```
/workspaces/k8s-pv-orphan-exporter/                          # main checkout (main branch)
/workspaces/k8s-pv-orphan-exporter/.claude/worktrees/
  ├── workitem-add-local-path-scanner/                       # worktree on workitem/add-local-path-scanner
  └── workitem-add-nfs-scanner/                              # worktree on workitem/add-nfs-scanner
```

`.claude/` is gitignored, so worktrees never show up as untracked files in the
main checkout.

### Lifecycle

1. **Start a task** — create a worktree and branch in one step:
   ```bash
   git worktree add .claude/worktrees/workitem-add-local-path-scanner \
     -b workitem/add-local-path-scanner
   ```
2. **Do the work** — all edits, commits, and pushes happen inside the worktree directory.
3. **Push and PR** — push the branch and open a draft PR.
4. **Cleanup** — after the PR merges, remove the worktree:
   ```bash
   git worktree remove .claude/worktrees/workitem-add-local-path-scanner
   ```
   To list and prune stale worktrees:
   ```bash
   git worktree list
   git worktree prune
   ```

### Parallel instances

Multiple Claude Code instances can run simultaneously. Each creates its own
worktree, so:

- File edits in one worktree don't affect another.
- Each instance commits and pushes its own branch independently.
- No merge conflicts arise until branches are merged into `main`.

## Multi-machine workflow

When developing across multiple machines (e.g., starting on a laptop,
continuing on a desktop), worktrees require some extra steps because they are
**local-only** — they never get pushed to the remote.

### What travels between machines

| Travels (via `git push/fetch`) | Local-only (per machine) |
|--------------------------------|--------------------------|
| Branches                       | Worktrees                |
| Commits                        | Worktree directory layout |
| Tags                           | `.git/worktrees/` metadata |

### Switching machines

#### On the first machine

1. Work inside a worktree as normal.
2. Commit and push the branch:
   ```bash
   git push -u origin workitem/add-local-path-scanner
   ```

#### On the second machine

The branch exists on the remote, but there's no worktree for it yet.

1. **Fetch the branch:**
   ```bash
   git fetch origin
   ```
2. **Create a worktree for the existing remote branch:**
   ```bash
   git worktree add .claude/worktrees/workitem-add-local-path-scanner \
     workitem/add-local-path-scanner
   ```
   If the local branch doesn't exist, git creates one tracking the remote automatically.
3. **Work in the worktree:**
   ```bash
   cd .claude/worktrees/workitem-add-local-path-scanner
   ```

#### Continuing back on the first machine

If the other machine pushed new commits:

```bash
cd .claude/worktrees/workitem-add-local-path-scanner
git pull
```

The worktree is still there from before — just pull the latest changes.

### Continuing existing branches

When asked to continue work on an existing branch:

1. Check whether the branch exists on the remote: `git branch -r`.
2. If it does, create a worktree for it rather than starting a new branch.
3. Pull the latest changes before starting work.

## Common commands

```bash
# List all worktrees
git worktree list

# Create a worktree for a new branch
git worktree add .claude/worktrees/my-feature -b workitem/my-feature

# Create a worktree for an existing remote branch
git fetch origin
git worktree add .claude/worktrees/my-feature workitem/my-feature

# Remove a worktree (after the branch is merged)
git worktree remove .claude/worktrees/my-feature

# Clean up stale worktree references
git worktree prune
```

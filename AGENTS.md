# Agent Instructions

This file is the project-specific guide for working on the `forge` generator itself. `CLAUDE.md` is a symlink to this file so Claude Code and Codex read the same instructions.

## WHAT

- `forge` is a Go `1.24.x` CLI that scaffolds AI-native repositories from embedded assets.
- `cmd/forge/main.go` owns the command surface: `init`, `sync-allowlist`, `update`, `upgrade`, and `completion`.
- `internal/init/` orchestrates project creation; `internal/scaffold/` writes and composes assets; `internal/allowlist/` owns the managed block reconciler; `internal/update/` owns maintainer refresh; `internal/upgrade/` owns infrastructure file propagation.
- `templates/common/` holds assets that every generated repo receives.
- `templates/golden/<stack>/` holds the shipped v1 stack snapshots and overlays.
- `sources.yaml` is the maintainer-side recipe registry for refreshing vendored snapshots.
- `docs/PRD.md`, `docs/SPEC.md`, `docs/adr/`, and `CONTEXT.md` are the authoritative intent, behavior, decision, and vocabulary documents.

## WHY

- This repo exists to make `forge init` produce a complete working repository with no manual follow-up.
- The generator MUST preserve the core product invariants: empty-directory init, generated repos that work without `forge` installed, a deny-only guard, and one shared gate pipeline used locally and in CI.
- Root `AGENTS.md` is for contributors working on the generator. Generated repositories get their own instructions from `templates/common/AGENTS.md.tmpl`.
- Contributors MUST keep generator behavior, shipped templates, and contributor-facing docs aligned so downstream repos do not drift from the documented workflow.

## HOW

- Run `bd prime` first and use `bd` for all task tracking in this repo.
- If work is not already tracked, create a Beads issue before editing files.
- Keep shell commands non-interactive: prefer `cp -f`, `mv -f`, `rm -f`, and other batch-safe forms.
- Use the README for the full contributor workflow and command catalog: [`README.md`](./README.md).

### Read First

- Read `docs/SPEC.md` before changing behavior.
- Read `docs/PRD.md` when validating user value or scope.
- Read `docs/adr/` before changing a protected invariant or established approach.
- Read `CONTEXT.md` when a term or boundary is ambiguous.

### Common Commands

```bash
bd ready
bd show <id>
bd update <id> --claim
mise run test
mise run build
GOCACHE=$PWD/.cache/go-build go test ./... -count=1
go build ./cmd/forge
```

### File Ownership Cues

- Edit root `AGENTS.md` when changing how contributors should work on `forge`.
- Edit `templates/common/AGENTS.md.tmpl` when changing what scaffolded repos should tell agents.
- Edit `templates/common/` for shared generated assets.
- Edit `templates/golden/` and `sources.yaml` together when changing shipped stack behavior or maintainer refresh inputs.

### Change Placement

- Put generator logic in `internal/` packages, not templates.
- Put generated-repo defaults in `templates/common/` unless they are stack-specific.
- Put stack-specific output in `templates/golden/<stack>/`.
- Put maintainer refresh intent in `sources.yaml` and keep overlays deterministic.

### Verification Expectations

- Prefer behavior-first changes: update the nearest test before or alongside code.
- When scaffold output changes, verify both focused package tests and the full suite.
- Treat documentation as part of the feature when commands, workflow, or invariants change.

### Session Completion

- Close or update the relevant Beads issue.
- Run the appropriate verification commands.
- Commit and push all finished work before ending the session.

<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:ca08a54f -->
## Beads Issue Tracker

This project uses **bd (beads)** for issue tracking. Run `bd prime` to see full workflow context and commands.

### Quick Reference

```bash
bd ready
bd show <id>
bd update <id> --claim
bd close <id>
```

### Rules

- Use `bd` for ALL task tracking — do NOT use TodoWrite, TaskCreate, or markdown TODO lists
- Run `bd prime` for detailed command reference and session close protocol
- Use `bd remember` for persistent knowledge — do NOT use MEMORY.md files

## Session Completion

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

1. File follow-up issues for anything not completed.
2. Run quality gates for any code or template change.
3. Update issue status in Beads.
4. Push to remote:
   ```bash
   git pull --rebase
   bd dolt push
   git push
   git status
   ```
5. Verify the branch is fully pushed before stopping.
<!-- END BEADS INTEGRATION -->

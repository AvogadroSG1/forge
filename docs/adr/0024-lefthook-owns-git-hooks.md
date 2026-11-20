# Lefthook owns `.git/hooks`; Beads runs as a Lefthook command

**Status:** accepted · 2026-11-20 · supersedes in part ADR-0003 (the "chain mode after `bd init`"
decision) and ADR-0019 (the `repairBeadsHookChain` step)

## Context

`forge init` has to end with both Lefthook (the gate pipeline: secret-scan, lint, fmt, test) and
Beads (`bd hooks`, which keeps its database in step with git operations) wired into git.
ADR-0003 chose "lefthook install in chain mode after `bd init`, preserving beads' hooks", and
ADR-0019 later added `repairBeadsHookChain`, which renamed `.beads/hooks/<hook>` to
`<hook>.lefthook` and wrote a wrapper that ran `<hook>.old` and then `<hook>.lefthook`.

That arrangement depended on how `bd init` installs hooks. `bd init` sets
`core.hooksPath=.beads/hooks`; `lefthook install --force` then wrote its own scripts into that
directory and left Beads' originals as `*.old`, which the repair step re-chained. It worked only
for `pre-commit` and `pre-push` and only if `lefthook install` was never re-run without
`--force`.

## Experiment

Run on 2026-11-20 with `bd` 1.3.4 and `lefthook` 2.2.2 in scratch repositories outside the forge
checkout:

- `bd init` sets `core.hooksPath=.beads/hooks`. `bd init --skip-hooks` leaves it unset and
  installs no hooks.
- Proposed alternative (marker sections): `lefthook install`, then `bd hooks install`, which
  injects `# --- BEGIN BEADS INTEGRATION ---` sections into the scripts in `.git/hooks`. This does
  **not** hold. Lefthook re-syncs whenever `lefthook.yml` changes, and the next `lefthook install`
  rewrote `pre-commit` and `pre-push`: zero Beads markers were left. It also leaves `*.backup`
  files.
- Lefthook as sole owner works. With `bd init --skip-hooks` and a `lefthook.yml` whose commands
  call `bd hooks run <hook> {0}`, `git commit`, `git push` and `git checkout -b` each ran Beads
  exactly once, `.git/hooks` held only Lefthook's scripts, `core.hooksPath` stayed unset, and a
  plain `lefthook install` (no `--force`, no warning) twice in a row left the hook files
  byte-identical.
- bd's own shim treats exit code 3 ("database not initialized", e.g. a fresh clone) as "skip" and
  exports `BD_GIT_HOOK=1`. `bd hooks run pre-push` gave the same result with and without stdin
  (the pushed refs); stdin only matters to chained user hooks, which this layout has none of, so
  `use_stdin` is not needed.

## Decision

Lefthook owns `.git/hooks`. Beads is one more Lefthook command.

- `forge init` runs `bd init --skip-hooks` and a plain `lefthook install` (no `--force`).
  `repairBeadsHookChain`, `repairBeadsHook` and `chainedHookWrapper` are deleted, and so is the
  "lefthook chain repair" step.
- Every shipped `lefthook.yml` (all twelve overlays stay byte-identical) has a `beads` command
  under `pre-commit` and `pre-push`, and new `post-merge`, `post-checkout` and
  `prepare-commit-msg` hooks that carry only that command:

  ```yaml
  run: |
    if command -v bd >/dev/null 2>&1; then
      BD_GIT_HOOK=1 bd hooks run <hook> {0} || { rc=$?; [ "$rc" -eq 3 ] || exit "$rc"; }
    fi
  ```

  The `command -v` guard keeps a repo without `bd` working; exit 3 does not block git, matching
  bd's own shim. The shim's `BEADS_HOOK_TIMEOUT` is not reproduced: a hung `bd` blocks the git
  operation until interrupted.

## Considered options

- **Marker sections (`bd hooks install` on top of `lefthook install`).** Rejected: the Beads
  sections do not survive the next `lefthook install`, and it leaves `*.backup` files.
- **Keep chain mode and `repairBeadsHookChain`.** Rejected: it covers two hooks, needs `--force`,
  and re-implements what Lefthook does natively.
- **`core.hooksPath=.beads/hooks` with Lefthook invoked from there.** Rejected: Lefthook then
  fights Beads for the same directory, which is the original problem.

## Consequences

- One owner, one config file: adding or removing a hook is a `lefthook.yml` edit followed by
  `lefthook install`.
- `bd hooks install` MUST NOT be run in a forge repo; it would put Beads sections back in
  scripts Lefthook rewrites.
- **Existing repos are not migrated by `forge upgrade`.** `lefthook.yml` is overlay-owned and git
  config is not forge's to edit. A repo generated before this ADR still has
  `core.hooksPath=.beads/hooks` and the `*.old` / `*.lefthook` chain. To migrate by hand:

  ```bash
  git config --unset core.hooksPath
  rm -f .beads/hooks/*.old .beads/hooks/*.lefthook
  # add the `beads` commands from templates/golden/<stack>/.forge-overlay/lefthook.yml
  lefthook install
  ```

  Remove the remaining generated files in `.beads/hooks` that `bd init` wrote
  (`pre-commit`, `pre-push`, ...), or leave them: with `core.hooksPath` unset git no longer
  reads them.
- `agent-fitness-functions client onboard` still runs after `lefthook install`. Its interaction
  with a repo that has no `.beads/hooks` was not verified here (the tool was not available).

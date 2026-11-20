# Enforced language opinions via one shared gate pipeline

**Status:** accepted · 2026-06-17

> **Superseded in part by ADR-0024 (2026-11-20).** The "lefthook installs in chain mode after `bd init`, preserving beads' git hooks" decision, and the consequence that implementation must verify beads' hooks survive `lefthook install`, no longer hold. Lefthook now owns `.git/hooks` and runs Beads as a command; `bd init` runs with `--skip-hooks`. The rest of this ADR stands.

## Decision

Templates don't just *include* lint/format/test tools — they **enforce** them. The
author's language guideline files (`golang.md`, `python.md`, `csharp.md`) are the
**canonical source of truth** for which tools a template's `.forge-overlay/` installs; a
lightweight template test asserts conformance. v1 ships **only** the languages with
guideline files: **Go, Python, C#** (TS/Rust/Bash deferred until their guidelines exist).

Enforcement uses **one gate definition, multiple callers**:
- **`mise.toml`** holds both `[tools]` (toolchain + lefthook) and `[tasks]` (lint/test/fmt/ci).
- **lefthook** runs fast checks (secret-scan + lint + format) on `pre-commit` and full
  tests on `pre-push`, calling the `mise` tasks.
- **GitHub Actions** calls the same `mise run ci` target.
- lefthook installs in **chain mode after `bd init`**, preserving beads' git hooks.
- The **secret-scan is a single shared script** invoked by both the agent guard hook
  (PreToolUse) and lefthook pre-commit.

## Considered Options

- **Config-present only (L1)** — ship tool configs, run nothing automatically. Rejected:
  recreates "remember to wire it up," the exact fatigue forge exists to kill.
- **Inline CI gates** — duplicate gate logic in `ci.yml` and `lefthook.yml`. Rejected
  after discussion: reintroduces local/CI drift the rest of the design eliminates.
- **Templates without guideline source of truth** — let TS/Rust/Bash encode ecosystem
  defaults with no personal `.md`. Rejected for v1: a scaffolded repo that can't be
  checked against a written standard can silently contradict the author's own rules.

## Consequences

- Every shipped template traces to a written guideline; adding a language means writing
  its guideline first (a clean, repeatable expansion pattern).
- "Day one it just works": a fresh repo enforces standards locally and in CI before any
  feature code exists.
- One definition of "the gates" (`mise` tasks) shared by lefthook and CI — no drift.
- Implementation must verify beads' git hooks survive `lefthook install` (chain mode
  behavior varies by hook type).

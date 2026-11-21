# TypeScript toolchain: eslint + prettier + vitest, lockfile not vendored

**Status:** accepted · 2026-07-11

## Context

The typescript stacks (ADR-0013) need one opinionated tool table so a generated frontend
is wired for work on day one — no "Nx or plain?", no "RxJS or signals?", no "Jest or
Vitest?" left to the user. The table must satisfy the ADR-0010 conformance floor via a new
canonical `typescript.md` guideline, and the choices must hold across three frameworks
with different idioms.

## Decision

**1. Tool table (the typescript guideline floor):** format `prettier` · lint `eslint`
(flat config; `typescript-eslint`, plus `angular-eslint` / `eslint-plugin-svelte` per
framework) · test `vitest` · type-check a uniform `typecheck` npm script (`tsc --noEmit`
for Vite and Angular; `svelte-check` for SvelteKit) · audit `npm audit --audit-level=high`.

**2. npm scripts are the indirection layer.** Every frontend defines `fmt`, `lint`,
`typecheck`, `test` scripts in `package.json`; mise tasks (root or standalone) only call
`npm run <script>`. The mise layer stays framework-agnostic; framework idiom lives where
npm expects it. Angular's `test` script is `ng test --watch=false` — since Angular 21,
`ng test` IS vitest, so all three frameworks converge on one runner.

**3. Vanilla never vendors `node_modules` or `package-lock.json`.** The lockfile is
generated at init by `npm install` (consistent with `go mod tidy` / `pip install` [now `uv sync`, ADR-0025, 2026-11-21] already
requiring network during init). Recipe rows use `strip_paths` to drop installs, caches
(`.angular`, `.svelte-kit`), and scaffolder `.gitignore`s before snapshotting.

**4. Framework conventions are decided, not configurable:** Angular = standalone
components + signals-first (RxJS reserved for genuinely event-streamed state); SvelteKit =
runes + SvelteKit conventions; Vite vanilla-ts = prescribed `src/{api,components,pages,lib}`
with transport isolated in `src/api`.

## Considered Options

- **Biome instead of eslint+prettier.** Rejected: foreign to two of the three frameworks —
  Angular ships prettier and angular-eslint is the ecosystem lint standard; SvelteKit's
  official add-ons are prettier/eslint plugins. Per-framework idiom with a shared floor
  beats a uniform-but-foreign tool.
- **Jest / Karma.** Rejected: Karma is deprecated; Angular 21 defaults to vitest, so vitest
  is the single runner with zero extra configuration anywhere.
- **Nx / Turborepo for structure.** Rejected in ADR-0013.
- **Vendoring `package-lock.json` for hermetic init.** Rejected: a stale lockfile rots
  faster than any other vendored asset and breaks `npm ci` on registry drift; the walking
  skeleton's offline guarantee applies after install (vitest/eslint/tsc are local).

## Consequences

- SPEC §16.1's hermetic smoke proves composition + lifecycle wiring with a stubbed npm; the
  real-toolchain proof moves to a network-gated tier (`FORGE_SMOKE_NETWORK=1`). This is a
  deliberate amendment, not a silent weakening.
- `npm audit` stays inside `tasks.ci` even though it needs the registry — mirroring
  `pip-audit` and `govulncheck`, which also need fresh databases to be meaningful.
- The author must install `typescript.md` at the canonical guideline path (draft shipped in
  `docs/handoffs/typescript-guideline-draft.md`) before the conformance floor passes.

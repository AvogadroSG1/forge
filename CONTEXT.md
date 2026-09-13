# CONTEXT — forge

Glossary of domain terms for the `forge` scaffolding system. Definitions only — no implementation details, no decisions (those live in `docs/adr/`).

## Terms

### forge
The scaffolding CLI. One command initializes a fully configured project (git, agentic config, beads, skills) with zero follow-up steps.

### Golden snapshot
A pinned, vendored copy of a recipe's output captured at maintainer time, unpacked and templated at init. Not a live invocation. The recipe is most often a single ecosystem-native scaffolder run (e.g. `cobra-cli init`), but may be a multi-step recipe — a pinned git checkout plus dependency pins (e.g. `go-api-chi` = `golang-standards/project-layout` at a SHA + `go get` of chi/zap/viper/testify). Either way the captured output is the vanilla layer beneath the overlay.

### Walking skeleton
The acceptance guarantee on a shipped stack's *composed* output (vanilla layer + overlay): after `forge init` the repo runs end-to-end and has at least one real passing test — a thin vertical slice that actually walks (router up, logger wired, one green handler test for an API stack). It is an emergent property of vanilla+overlay composed, **not** a third layer. The vanilla/overlay split is preserved beneath it so `forge update` can refresh the vanilla layer without re-vetting the wiring.

### Allowlist
The curated set of Bash command prefixes the author has vetted as safe to run without a confirmation prompt. Convenience-oriented and **always growing**. Refreshes from a canonical source embedded in the `forge` binary, written into a versioned managed block in each project. Distinct from the deny floor.

### Deny floor
The small, stable, safety-oriented set of rules that block irreversible or dangerous commands. Enforced by the guard hook. Rarely changes. Distinct from the allowlist; shares a single canonical source file (different sections) but refreshes on its own cadence.

### Managed block
A delimited region (`// BEGIN FORGE ALLOW v:N` … `// END FORGE ALLOW` entries in `.claude/settings.local.json`) that the reconciler may rewrite in place, leaving surrounding hand-edited content untouched. Mirrors the existing beads integration block in `AGENTS.md`.

### Reconciler
A `forge` subcommand (`sync-allowlist`) that rewrites a project's managed block from the canonical embedded source. Triggered notify-only: a SessionStart hook detects staleness and prompts the author to run it; it never auto-mutates the repo.

### Guard hook
A self-contained PreToolUse hook shipped in-repo (`.claude/hooks/guard`), wired identically by both Claude Code and Codex via their `PreToolUse` events (both honor `exit 2` / deny). Enforces the deny floor only — it is a **deny-only net**, never an allow-decider. Runs in every permission mode; auto mode bypasses the confirmation prompt but never the guard.

### Constituent
One command within a compound shell line (split on `&&`, `||`, `;`, pipes). The guard judges each constituent for deny rules; one denied constituent blocks the whole line. The guard does NOT approve compounds — auto-run of a compound depends solely on an allow glob matching the whole line.

### Secret-exposure guard
A target-aware deny rule that blocks any command line **referencing a secret path token** (`.env*`, `*.pem`, `*.key`, `credentials`, `*.tfstate`, `.ssh/`, `.aws/`, …) **regardless of the binary** used (`cat`, `awk`, `git show HEAD:.env`, `python -c …`) — a path-token scan, **not** a command-name list (a name list is trivially bypassed; see ADR-0002). Also blocks unfiltered environment dumps (`env`, `printenv`, `set`). Path patterns are configurable at the top of the guard. Realized as deny rules **D9** (path-token) + **D10** (env-dump), implemented in `secret-scan.sh` (SPEC §11.3, §12). Distinct from content secret-scan (`scan-staged` mode), which guards commits.

### Overlay (.forge-overlay/)
The single layer on top of a vanilla golden snapshot that adds the author's vetted
*opinions*: formatter, linter, test framework, mocking framework, coverage tool, type
checker, audit/security tooling, recommended packages, gate wiring, CI. The snapshot is
inert scaffolding; the overlay is where the value lives. Its tool/package choices are
governed by (and tested against) the canonical language guideline files. There is exactly
**one** overlay per stack — the earlier name "security overlay" is retired; audit/security
tooling is one *part* of the overlay, not a separate layer. `forge update` regenerates the
snapshot beneath it but never touches the overlay.

### Telemetry bootstrap
The observability slice of every shipped stack's overlay: one telemetry module that installs
OpenTelemetry tracer + meter providers and `service.name` unconditionally, attaches OTLP/HTTP
exporters **only** when `OTEL_EXPORTER_OTLP_ENDPOINT` (browser: `VITE_…` / Angular
`environment.otlpEndpoint`) is set, plus a real test that proves a span and a metric flow through
in-memory exporters. A vetted extra (ADR-0010), refresh-immune like the rest of the overlay. (See ADR-0020, SPEC §9.4.)

### Gate
An automated quality check (lint, format, test) defined once as a `mise` task and invoked
by multiple callers (lefthook locally, GitHub Actions in CI) so the definition never
drifts. Fast gates (lint/format) run on pre-commit; full tests run on pre-push and in CI.

### Guideline file (canonical)
`~/peter_code/ai_support/guidelines/{golang,python,csharp,typescript,rust,bash}.md` — the
author's written language standards. The **minimum source of truth** (the *floor*) for what a
template's overlay installs: the overlay MUST implement every guideline MUST/SHOULD and MAY add
vetted extras the guideline is silent on. A lightweight conformance test asserts the floor — it
fails when a guideline MUST has no corresponding overlay tool, not when the overlay ships an
extra. A template may only ship for a language that has a guideline file (v1: Go, Python, C#).
The conformance test reads byte-for-byte snapshots of these files vendored in-repo at
`test/testdata/guidelines/`, not the canonical path itself, so `go test ./...` stays hermetic on
machines (including CI) without `~/peter_code/ai_support/` (**ADR-0018**); a separate drift test
checks the vendored snapshots against the canonical path when it exists.

### Skill seed vs. APM manifest
The **seed** (`templates/seed/skills.json.tmpl`) is forge's embedded default skill list, rendered in memory at init to build `instill init --skills` — never scaffolded into the repo. The **APM manifest pair** (`apm.yml` + `apm.lock.yaml`, written by `instill`/`apm`) is the committed, portable declaration of which skills the repo uses (the lockfile). `.apm/` is the compiled skill content — machine-regenerable and gitignored; `instill sync` (`apm install` + `apm compile`) regenerates it on clone (the node_modules).

### Recipe
The ordered, pinned step list in a `sources.yaml` row that produces a stack's vanilla layer. A row carries a `kind` discriminator (`scaffolder` | `recipe`) and ordered `steps[]` of two verbs — `checkout` (clone a pinned git ref, with an optional `strip:` of paths) and `run` (invoke a pinned scaffolder/toolchain command). A single-scaffolder stack is simply a **one-step recipe**, so the writer and the `update` interpreter never branch on shape. Distinct from the **golden snapshot**, which is the *captured output* of running a recipe.

### Vanilla layer
The inert, recipe-produced scaffolding beneath the overlay — the portion of a golden snapshot that `forge update` may regenerate. Distinct from the **golden snapshot** (the whole pinned artifact) and the **overlay** (the vetted opinions layered on top, never regenerated by `update`).

### Seam
A cross-component coupling whose correctness is owned by **exactly one named test**. In the work graph, a seam is realized as a **Feature** whose `all-children` gate *is* the seam test (e.g. the walking-skeleton Feature's "init composes + runs offline" gate spans the contract, writer, assets, and entrypoint). An unguarded seam — a coupling with no owning test — is a defect, surfaced like a contradiction. (See SPEC §18, the Seam Inventory.)

### Refresh seam
The specific seam where `.forge-overlay/` files layer onto vanilla dirs, checked by `forge update` after regenerating vanilla: an **orphan** (overlay file whose vanilla parent dir disappeared) is a **hard fail**; a **collision** (new vanilla file at an overlay-occupied path) is a **loud warn** (overlay-wins composition still yields a correct result). `update` never auto-mutates the overlay. (See ADR-0006.)

### Local-release
The proof that a scaffolded project works on this machine: lint passes, tests pass, the
thing runs. Operationally equivalent to `mise run ci` exiting 0 using real tools (not
stubs). Each stack's overlay MUST define a `ci` task that represents the full local-release
contract. Distinct from the structural verification (files exist) — local-release proves
the *code* works; structural tests prove the *repo* is complete.

### Fast gate (template verification)
Scaffold → `mise install` → local-release for one CLI stack per language (Go CLI, Python
CLI, C# CLI). Runs in forge's own CI and blocks the branch. Proves that the three
foundational stacks produce working projects on every push.

### Slow gate (template verification)
Scaffold → `mise install` → local-release for all stacks in the matrix. Runs async out of
the branch-blocking path. Proves that every shipped stack produces a working project.
Acceptable tradeoff: API stack breakage is learned late.

### Structural verification
A test that asserts the composition machinery produces all expected files — including those
not exercised by local-release (CI config, AGENTS.md, lefthook.yml, etc.). Currently covers
`go-cli-cobra` as the canary for composition logic. Complements local-release: structure
proves the repo is *complete*, local-release proves the code *works*.

---

> **Note on "floor".** The word is used in two distinct senses, deliberately: the **deny floor** (the stable set of *safety* rules the guard blocks) and the **guideline floor** (the *minimum* set of tools an overlay must install). They are unrelated mechanisms that share a metaphor. The PRD's "safety floor" is the deny-floor sense.

### Frontend fragment
A typescript frontend stack (`vite-ts`, `sveltekit`, `angular`) composed a second time
under `web/` in a fullstack repo. The same golden tree serves standalone `frontend`
projects at the repo root; in fragment mode its root gate files are skipped because the
backend overlay owns the repo's gates (ADR-0013).

### Native fullstack stack
A single-tree `fullstack` stack in the backend's own language — `go-web-templ`,
`python-web-jinja`, `csharp-blazor`. It IS the fullstack (server-rendered UI wired to the
same `/health` contract), so the frontend question is skipped for it.

### Root gate files
The files that define a repo's gate pipeline — `mise.toml`, `lefthook.yml`,
`.github/workflows/ci.yml`. Exactly one set exists per generated repo; in fullstack mode
the backend overlay's templated copies own them and the frontend fragment's copies are
dropped at composition.

### Backend port
The port a backend stack's walking skeleton listens on (chi 8080, uvicorn 8000, Kestrel
5000). Drives the derived `APIBaseURL` a fullstack frontend's API client targets by
default; overridable at runtime via `VITE_API_BASE_URL` or the Angular environment file.

### Personalization profile
The developer's machine-level customizations (workspace references, personal agent plugins,
custom CLI allowlists, external path permissions) stored outside individual repos (e.g. in
`~/.config/forge/`) and layered into generated harness target configs during `forge init`
and `forge sync-allowlist`.

### Harness target
A specific agent environment whose project-level configuration `forge` scaffolds and
reconciles (`opencode.jsonc` for OpenCode, `.claude/` for Claude Code, `.codex/` for Codex).

### Ownership class
The declaration of who may write a given managed file, fixed per file, never inferred at write
time: **wholly-owned** (nothing else writes it; `forge upgrade` blind byte-copies it),
**co-owned** (another tool or `forge init` itself also has a legitimate claim on part of the
file; `forge upgrade` reconciles rather than overwrites), or **delegated** (owned entirely by
another tool — `bd`, `instill` — and never written by `forge` at all). See SPEC §3.1.

### Co-owned file
A managed file where more than one writer has a legitimate claim: `.claude/settings.json` and
`.codex/hooks.json` (bd and other tools append their own hook entries; reconciled by **owned
entry** identity, ADR-0016) and `opencode.jsonc` (rendered once from init-time params, then
never touched again; ADR-0017). Distinct from a **wholly-owned** file, which `forge upgrade`
may always overwrite unconditionally.

### Owned entry
The unit of ownership inside a co-owned hook-config file: one command string appearing in the
canonical embedded template's `hooks` object. Ownership in `internal/hookcfg.Reconcile` is
per-entry, never per-matcher-group and never per-file — a matcher group can hold both a
forge-owned entry and a third-party entry side by side, and only the former is ever rewritten
(ADR-0016).

### Forge manifest
`.forge/manifest.json`, a committed JSON record (`schemaVersion`, `infraVersion`, and the init
params `language`/`frontend`/`includePersonal`/`stack`) written by `forge init` and kept current
by `forge upgrade`. Lets `forge upgrade` re-render init-time-parameterized files (`opencode.jsonc`)
using the params a repo was actually scaffolded with, instead of only knowing a bare version
integer (ADR-0017).

### Legacy infra marker
`.forge-infra-version`, a bare integer file that predates the **forge manifest**. Kept as a
fallback for repos scaffolded before the manifest existed and for `forge` binaries older than
v4 that don't know the manifest format. When both files are present, the manifest's
`infraVersion` is authoritative (ADR-0017).

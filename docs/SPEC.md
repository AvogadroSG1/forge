# forge — Definitive Specification

**Status:** canonical · 2026-06-20 · **Author:** Peter O'Connor with Claude Code (databricks-claude-opus-4-8)

This is the single authoritative specification of *how* `forge` works. It supersedes the five
mini-specs listed in §0. It cites — and does **not** restate — the decision records in
[`docs/adr/`](./adr/); when a section summarizes a decision in one line, the ADR is the full
truth. Product intent lives in [`PRD.md`](./PRD.md); domain vocabulary lives in
[`CONTEXT.md`](../CONTEXT.md).

---

## 0. Status, supersession & how to read this

### 0.1 Superseded documents

The following mini-specs are **superseded by this document** and have been archived to
`docs/superpowers/specs/_superseded/` for history. Do not implement from them.

| Archived spec | Subsumed into |
|---|---|
| `2026-06-16-mkproj-scaffolding-system-design.md` | §1, §3, §9, §11, §14, §17 (mined for engine, file manifest, day-one baseline) |
| `2026-06-18-allowlist-deny-floor-seed.md` | §11 (security model — full D1–D16 floor, seed contents, sandbox) |
| `2026-06-18-shared-secret-scan-design.md` | §12 (shared secret-scan) |
| `2026-06-19-mkproj-cli-prompt-render-contract.md` | §5, §6, §7, §10, §13, §15 |
| `2026-06-19-mkproj-init-lifecycle-and-topology.md` | §4, §8, §13, §16 |

The two `docs/handoffs/` documents and the `docs/superpowers/plans/` value-stream plan are
point-in-time artifacts, not specs; they are retained as-is.

### 0.2 Authority order

When two statements conflict, authority runs: **ADRs > this SPEC > everything else.** This SPEC
de-stales the older specs; where it cites an ADR, the ADR wins.

### 0.3 Decision records cited by this spec

ADR-0001 (per-project allowlist, embedded source) · ADR-0002 (deny-only guard hook) ·
ADR-0003 (one shared gate pipeline) · ADR-0004 (init fail-fast, no rollback) · ADR-0005
(stacks as walking-skeleton recipes) · ADR-0006 (update determinism & refresh seam) · ADR-0007
(engine: Go + text/template + embed.FS) · ADR-0008 (strict empty-dir precondition) · ADR-0009
(remote default gh, Phase-3-last, never auto-delete) · ADR-0010 (guideline is the conformance
floor, not the ceiling) · ADR-0012 (skill provisioning via APM-backed instill) · ADR-0013
(frontend dimension: fragments under web/) · ADR-0014 (typescript toolchain) · ADR-0020 (env-gated
OpenTelemetry bootstrap in every stack).

---

## 1. Problem, goal & the one verification test

Every new project costs 10–15 minutes of identical, drift-prone manual setup. `forge` collapses
that to one command (PRD §1–§2). The **single canonical acceptance test for the product**: run
init in an empty folder; the result must be indistinguishable from a hand-built project, with no
manual steps after. Every section below ends in Given/When/Then scenarios; if they pass, the
slice works.

A key framing finding (system-design §1): **`instill` and `bd` already perform most of the
scaffolding work**, so `forge` *orchestrates* them rather than reimplementing them.

---

## 2. Glossary

The domain vocabulary is maintained in [`CONTEXT.md`](../CONTEXT.md) and is not duplicated here.
Terms used throughout this spec — *golden snapshot, walking skeleton, overlay, allowlist, deny
floor, managed block, reconciler, guard hook, gate, guideline file* — are defined there. This
spec is implementation truth; `CONTEXT.md` is the language.

---

## 3. Architecture & engine

`forge` is a standalone Go binary using `text/template` + `embed.FS`, distributed as an offline,
vendored installed binary (**ADR-0007**). It **owns** templating, verbatim copy, symlink creation,
permissions, and guard-hook installation; it **delegates** to `bd` and `instill` for what they
already do well.

```mermaid
flowchart TD
    A["forge (picker: language → type → stack → frontend?)"] --> P1
    subgraph P1["Phase 1 — Scaffold (forge owns)"]
        B["git init + identity"] --> C["Render .tmpl files (text/template, missingkey=error)"]
        C --> D["Copy verbatim files (hooks, settings, base gitignore)"]
        D --> E["Compose golden snapshot: vanilla then overlay-wins"]
        E --> F["CLAUDE.md → AGENTS.md symlink"]
        F --> G["Write settings.local.json allow/deny (Layer A)"]
        G --> H["Install guard hook (PreToolUse, Layer B)"]
    end
    subgraph P2["Phase 2 — Delegate"]
        H --> I["bd init → .beads/, git hooks, AGENTS.md beads block"]
        I --> J["instill bootstrap / init / sync → apm.yml + apm.lock.yaml + .apm/"]
        J --> K["lefthook install (chain mode, after bd init)"]
    end
    subgraph P3["Phase 3 — Remote (optional, last)"]
        K --> L{"--remote?"}
        L -->|gh| M["gh repo create + push"]
        L -->|url| N["git remote add + push"]
        L -->|none| O["local initial commit only"]
    end
```

The engine choice, distribution, and offline/vendored posture are recorded in **ADR-0007**.

### 3.1 Ownership classes — who may write a managed file

Every managed file `forge init` scaffolds and `forge upgrade` propagates falls into exactly one
ownership class. The class determines how `forge upgrade` is allowed to write it — never the
reverse; ownership is declared once, per file, not inferred at write time.

| Class | Files | Upgrade behavior |
|---|---|---|
| **Wholly-owned** | `.claude/hooks/guard`, `.claude/hooks/secret-scan.sh`, `.opencode/plugins/forge-hooks.js` | Nothing else ever writes to these. Blind byte-copy from the embedded template, unconditionally. |
| **Co-owned (hook config)** | `.claude/settings.json`, `.codex/hooks.json` | Other tools (`bd`, notably) append their own entries into the same top-level `hooks` object. Reconciled by owned-entry identity (`internal/hookcfg.Reconcile`, **ADR-0016**) — never blind-overwritten. |
| **Co-owned (init-rendered)** | `opencode.jsonc` | Templated with init-time parameters that only exist in memory during `forge init`. `forge upgrade` renders it from `.forge/manifest.json`'s persisted params **only when the file is missing**; once present, upgrade never touches it again (**ADR-0017**). |
| **Delegated** | `.beads/` (via `bd`), `.apm/` (via `instill`) | Owned entirely by the delegate tool. `forge` does not write their contents; it only repairs directory permissions it is responsible for (`.beads` 0700) and detects drift. |

Co-owned files are the load-bearing case: a blind overwrite of either co-owned class is the exact
defect ADR-0016 and ADR-0017 close (destroyed third-party hook entries; raw `{{ }}` template
syntax written to disk). See **§19** for the full upgrade-path behavior.

---

## 4. Init lifecycle

*(Authoritative: init-lifecycle spec §1; cites ADR-0004, ADR-0008, ADR-0009.)*

### 4.1 Precondition — strictly empty directory

`forge init` refuses to run unless the target directory is empty, ignoring only inert cruft
(`.DS_Store`). No `--force`/`--in-place` in v1 (**ADR-0008**).

### 4.2 Ordering invariants (load-bearing)

The ordered mutation is: Phase 1 (render/copy/link) → guard install → `bd init` → `instill
bootstrap`/`init`/`sync` → `lefthook install` → Phase 3 remote. `instill bootstrap` (which
ensures the `apm` CLI) is the one non-fatal step: if it fails, the skills phase is skipped and
init continues — the repo self-heals later via `instill bootstrap && instill sync` (ADR-0012).
Four orderings are load-bearing:

- **Guard installs before `bd init`**, so beads' own git-hook install runs under the guard.
- **`lefthook install` runs after `bd init`** in chain/non-clobber mode, preserving beads' hooks
  (verification owned by `485`; cites ADR-0003).
- **`upgrade.Stamp` writes `.forge/manifest.json` and `.forge-infra-version` immediately after
  the Phase 1 scaffold writer succeeds, before Phase 3's `git add`/`commit`** (**ADR-0017**), so a
  freshly scaffolded repo is born at the current infrastructure version — never v0 — and both
  files ride the initial scaffold commit.
- **Phase 3 runs last** (**ADR-0009**), so the first commit/push passes the full gate pipeline.

Immediately after `bd init`, forge tightens `.beads` to 0700 (git does not preserve directory
modes). `forge upgrade` also repairs this drift when detected — see **§19**.

### 4.3 Failure posture — fail-fast, no rollback

On any step failure, init stops at that step, leaves the partial state, and prints the failed
step plus the single recovery command (recursive-force-delete the dir, then retry). No
transactional rollback — the empty-dir precondition bounds the blast radius to a disposable
directory (**ADR-0004**).

### 4.4 Phase 3 remote failure — leave & instruct, never auto-delete

`gh repo create` fails → report, remain a complete local-only repo, tell the user how to add a
remote manually. Repo created but first push fails → leave the remote, print its URL and the
`git push -u origin <branch>` retry, mention `gh repo delete` as the user's option. forge
**never** auto-deletes a remote (**ADR-0009**).

```gherkin
Scenario: Init refuses a non-empty directory
  Given the current directory contains a file
  When the user runs `forge init`
  Then it exits non-zero with "directory not empty" and creates nothing

Scenario: Local-step failure leaves a recoverable partial and no remote
  Given an empty dir and an interactive run targeting --remote gh
  When `instill init` fails during Phase 2
  Then init stops with "init failed at step 'instill init'", names the dir and the
       recursive-force-delete recovery command, and no GitHub repo was created

Scenario: Remote created but first push fails (gate or network)
  Given Phase 3 created the GitHub repo successfully
  When the initial `git push` fails
  Then forge reports the URL and reason, prints the push retry, does NOT delete the remote,
       and leaves the local repo complete
```

---

## 5. CLI & command surface

*(Authoritative: CLI/render contract §2.)*

- `forge` — **defaults to `init`** (bare invocation == `forge init`).
- `forge init` — scaffold a new project in the current dir (Phases 1–3).
- `forge update` — maintainer-only snapshot refresh (§15).
- `forge sync-allowlist [--check]` — reconciler (§13).
- `forge upgrade [--check]` — infrastructure file propagation (§19).

```gherkin
Scenario: Bare invocation defaults to init
  Given an empty directory
  When the user runs `forge`
  Then it behaves identically to `forge init`
```

---

## 6. Variable schema & prompt flow

*(Authoritative: CLI/render contract §1, §3.)*

**Seed (prompted/flagged, un-derivable):** project name, language, type, stack, frontend
(fullstack on an api backend only), api-base-url (frontend-only projects), remote choice.
**Author identity** auto-defaults from `git config --global user.{name,email}`, prompted only if
absent.

**Derived (computed from the project-name slug; each has an override flag, never a prompt):**

| Variable | Derivation | Override flag |
|---|---|---|
| `BdPrefix` | lowercased, non-alphanumeric-stripped slug | `--bd-prefix` |
| `ModulePath` | `github.com/<gh-user>/<ProjectName>` when `--remote gh`; else editable placeholder | `--module-path` |
| Go module / Python package (`snake_case`) / C# namespace (`PascalCase`) | normalized slug | (language-specific) |
| `NpmPackage` | kebab slug (== RepoSlug) | — |
| `BackendPort` | per stack: chi 8080 · uvicorn 8000 · Kestrel 5000 | — |
| `APIBaseURL` | fullstack: `http://localhost:<BackendPort>`; frontend-only: prompted | `--api-base-url` |

**Prompt order:** project name → language → type → stack → frontend (only when type is
`fullstack` and the stack is a FullstackBackend api stack; a native fullstack stack IS the
fullstack, so the question is skipped) → api-base-url (only when type is `frontend`; TTY
default `http://localhost:8080`; for fullstack it is derived from the backend port and
`--api-base-url` is a silent override) → author identity (confirm-or-edit the git-config
default) → remote (last; the one outward-facing action). Type is filtered to what exists for
the chosen language; stack to language × type. **When a question has exactly one valid
choice** (typescript → `frontend`), it auto-selects silently — even without a TTY — rather
than demanding a flag with only one legal value. Only shipped v1 stacks appear (§9).

**Three-state precedence (per input):** (1) flag present → use it, skip the prompt silently; (2)
flag absent + TTY → prompt; (3) flag absent + no TTY → **error naming the missing flag** (never a
default). Invalid flag value → fail immediately listing valid choices, never drop to a prompt.

```gherkin
Scenario: Derivations from a single project-name seed
  Given the project name "My Cool API"
  When forge resolves the variable set
  Then BdPrefix == "mycoolapi" And the Python package == "my_cool_api"
       And the C# namespace == "MyCoolApi"

Scenario: Missing required value with no TTY fails loudly
  Given no --stack flag and no TTY
  When forge runs
  Then it exits non-zero naming --stack as the missing flag

Scenario: Only v1 stacks are selectable
  Given an interactive run with language=Go
  When the stack picker renders
  Then only Go v1 stacks (go-cli-cobra, go-api-chi) are offered
```

---

## 7. Render contract

*(Authoritative: CLI/render contract §4; cites ADR-0007.)*

- **Engine:** Go `text/template` with `Option("missingkey=error")` — an unresolved variable is a
  hard failure, never `<no value>`.
- **Renderable marker:** filename ends in `.tmpl` → render and strip the suffix. Every other file
  is copied **verbatim** (byte-for-byte).
- **File roles:** `render` (`.tmpl`), `verbatim`, `link` (symlink), `delegate` (produced by
  `bd`/`instill`/native tooling).
- The guard hook and `secret-scan.sh` are **`verbatim`** — security-critical scripts are
  identical and independently auditable across every repo; never templated.

```gherkin
Scenario: Phase-1 writer produces a clean tree (iha definition-of-done)
  Given a fixture variable set and the embedded templates/ tree
  When the Phase-1 scaffold writer runs into a temp dir
  Then no rendered file contains "<no value>" or a residual "{{"
       And every verbatim file is byte-identical to its source
       And CLAUDE.md is a symlink whose target is AGENTS.md
       And .claude/hooks/guard and .claude/hooks/secret-scan.sh are mode 0755

Scenario: A missing variable fails the render
  Given a template referencing {{.Undefined}}
  When the writer renders it
  Then init fails with an error naming the missing key And no partial file is written
```

---

## 8. Deterministic asset composition

*(Authoritative: init-lifecycle spec §2, §3, §5.)*

### 8.1 `.gitignore` merge

Output = base section, a banner, then the language section — concatenated verbatim, base first.
No interleaving, no sorting, **no dedup** (git treats duplicate patterns as harmless). Each
section is byte-identical to its source, so the same inputs always produce byte-identical output.

### 8.2 Managed-block topology

Three managed blocks across two files, coexisting by unique named marker. Each reconciler
rewrites only between its own markers; outside-block and inter-block content is sacred.

| File | Block | Owner | Reconciled? |
|---|---|---|---|
| `AGENTS.md` | beads block | `bd` | bd's own cadence |
| `AGENTS.md` | `FORGE CONVENTIONS` (Co-Authored-By, conventional-comments, <300-line PR norm) | forge | **render-once** — no version, no reconciler (v1) |
| `.claude/settings.local.json` | `FORGE ALLOW v:N` | `forge sync-allowlist` | versioned + reconciled |

Only the allowlist block carries version + reconciler machinery (keeps `rom` scoped to one block).

### 8.3 APM manifest topology

`instill` delegates skill install/compile to Microsoft APM (ADR-0012). The committed contract is
the pair `apm.yml` (selected skill + MCP dependencies, written by `instill init`) and
`apm.lock.yaml` (APM's lockfile). `instill sync` runs `apm install` + `apm compile`, rendering
agent-facing content into `.apm/` (gitignored, regenerated on every SessionStart). Forge's
default skill selection lives in the embedded `templates/seed/skills.json.tmpl`, rendered
in memory during init to build the `--skills` list — never scaffolded into the repo. One
manifest feeds every agent harness APM compiles for; no per-agent manifest, no drift.

```gherkin
Scenario: Gitignore merge is deterministic and sectioned
  Given the base .gitignore and the vendored Python github/gitignore
  When the Phase-1 writer produces the project .gitignore for a Python stack
  Then output is the base section, a "# ===== python ... =====" banner, then the python section,
       each section byte-identical to source, and running the merge twice is byte-identical

Scenario: Multiple managed blocks coexist without clobbering
  Given an AGENTS.md with a beads block, a FORGE CONVENTIONS block, and hand-written prose
  When `forge sync-allowlist` runs (targets settings.local.json, not AGENTS.md)
  Then AGENTS.md is untouched And only the allowlist block in settings.local.json is rewritten

Scenario: One manifest feeds both agents' skill content
  Given a committed apm.yml and apm.lock.yaml
  When `instill sync` runs on a fresh clone
  Then apm install + apm compile regenerate .apm/ with the same skill set for every agent
```

---

## 9. Template catalog & overlay model

*(Authoritative: ADR-0005; de-staled system-design §4.)*

Each stack is a **golden snapshot** (vanilla, recipe-produced) plus exactly **one**
`.forge-overlay/` (the vetted opinions). The earlier name "security overlay" is **retired** —
audit/security tooling is one *part* of the single overlay, not a separate layer. The composed
output (vanilla + overlay) must be a **walking skeleton**: it runs end-to-end with ≥1 real passing
test (CONTEXT.md; ADR-0005). The walking skeleton is an emergent property of composition, not a
third layer.

### 9.1 v1 catalog — twelve stacks

| Stack key | Language | Type | Notes |
|---|---|---|---|
| `go-cli-cobra` | Go | CLI | |
| `go-api-chi` | Go | API | FullstackBackend |
| `python-cli-typer` | Python | CLI | |
| `python-fastapi` | Python | API | FullstackBackend |
| `csharp-cli` | C# | CLI | |
| `csharp-webapi` | C# | API | FullstackBackend |
| `go-web-templ` | Go | fullstack | templ + HTMX, chi-served |
| `python-web-jinja` | Python | fullstack | FastAPI + Jinja2 + HTMX |
| `csharp-blazor` | C# | fullstack | Blazor Web App, Interactive Auto |
| `vite-ts` | TypeScript | frontend | Vite vanilla-ts, prescribed src/ layout |
| `sveltekit` | TypeScript | frontend | SvelteKit minimal, runes |
| `angular` | TypeScript | frontend | Angular CLI, standalone + signals |

The three `frontend` stacks double as the `--frontend` fragment choices for fullstack
projects on a FullstackBackend api stack (ADR-0013). Rust and Bash remain **deferred**
until their guideline files exist (ADR-0003); the catalog boundary is executable (`01b`)
so unsupported stacks are non-selectable.

### 9.2 Sourcing — `sources.yaml` recipe schema

A stack's vanilla layer is the output of a **recipe** (which may be multi-step), declared in
`sources.yaml` with one uniform schema: `{kind: scaffolder|recipe, ordered steps[], gitignore:
<stem>, normalize: [...], resolved: {written by update only}}`. A single-scaffolder stack is a
one-step recipe. `go-api-chi` is `kind: recipe` (pinned checkout of `golang-standards/project-layout`
+ `go mod init` + `go get` of pinned chi/zap/viper/testify); its wired `main.go` + handler test
live in the **overlay**, not vanilla. gitignore mapping: Go→`Go`, Python→`Python`, C#→`VisualStudio`, TypeScript→`Node`,
each resolved against one repo-wide pinned SHA (top-level `gitignore_repo` key). JS recipe rows
add a `strip_paths` normalize rule (drops `node_modules`, lockfiles, scaffolder caches and
`.gitignore`s before snapshotting; ADR-0014). Full schema and rationale: **ADR-0005**.

### 9.3 Packaging boundary

Everything under `templates/golden/<key>/.forge-overlay/` is the vetted, refresh-immune layer;
everything else under the stack dir is vanilla/refreshable. Overlay files may layer *into* vanilla
dirs — init composes **vanilla-first, then overlay-wins** on path collision, stripping the
`.forge-overlay/` prefix. The walking-skeleton test ships in the overlay next to the wiring it
exercises (non-vacuous-test guarantee; §16).

**Fullstack composition (ADR-0013):** when `--frontend` is set, the chosen typescript stack's
vanilla + overlay compose a second time under `web/`, skipping the fragment's **root gate
files** (`mise.toml`, `lefthook.yml`, `.github/`) — the backend overlay's templated
`mise.toml` owns the whole repo's gates, adding node tools and `web-*` tasks via its
`{{if .Frontend}}` half. The `.gitignore` merge appends a `Node` section after the backend
language section.

### 9.4 Observability baseline

*(Authoritative: ADR-0020.)*

Every shipped stack's overlay carries a **telemetry bootstrap**: one telemetry module plus a real
test, shipped as a vetted extra (ADR-0010, §10.2) with no guideline edit.

- **Module per stack.** Go: `internal/telemetry/` (`cmd/telemetry.go` on cobra). Python:
  `<package>/telemetry.py` (`src/<package>/` on typer). C#: `Telemetry.cs` +
  `TelemetryServiceCollectionExtensions.cs` (extension in namespace
  `Microsoft.Extensions.DependencyInjection` so the verbatim webapi `Program.cs` needs no `using`).
  TypeScript: `src/lib/telemetry.ts` (vite-ts, sveltekit) / `src/app/telemetry.ts` (angular),
  built on `@opentelemetry/sdk-trace-web` because the three TS stacks are browser bundles.
- **Env contract.** Tracer + meter providers, `service.name` and W3C propagation are installed
  unconditionally; `service.name` is `OTEL_SERVICE_NAME`, else the repo slug. OTLP/HTTP exporters
  attach **only** when `OTEL_EXPORTER_OTLP_ENDPOINT` is set (browser stacks:
  `VITE_OTEL_EXPORTER_OTLP_ENDPOINT`, Angular `environment.otlpEndpoint`). Unset means providers
  only — no exporter, no retry noise. Signals are traces + metrics; logs are a filed follow-up.
  Python stacks add a `mise run serve` (`run-cli` on typer) task that runs the app under
  `opentelemetry-instrument`, with the same endpoint guard exporting `OTEL_*_EXPORTER=none` when
  no collector is configured.
- **Non-vacuous test.** The module's test injects an in-memory span exporter and metric reader,
  starts one span, records one metric, and asserts both arrive — the observability slice of the
  walking-skeleton guarantee (§16). HTTP stacks additionally assert a `/health` request yields
  a `GET /health` span; CLI stacks assert the command span.
- **Refresh seam note.** Telemetry files are overlay files. `internal/telemetry/` is an orphan
  on `forge update` exactly like `internal/httpapi/`; every other placement has a vanilla
  parent dir. Dependencies are pinned in the stack manifests (`go.mod.tmpl`,
  `pyproject.toml.tmpl`, `*.csproj.tmpl`, overlay `package.json.tmpl`), the existing precedent
  for overlay tools. Owning test: `TestGoldenStacksShipTelemetryBootstrap` (§18).

### 9.5 System-wide OTel collector — `mise run otel`

*(Authoritative: ADR-0021. Amends 9.4's exporter default under `mise`.)*

Every stack, common rather than golden, additionally ships three assets that give the §9.4
bootstrap somewhere to send data:

- `templates/common/mise/conf.d/otel.toml` → `.config/mise/conf.d/otel.toml`. `mise` merges every
  file under a project's `.config/mise/conf.d/*.toml` on top of its own `mise.toml`, so this one
  forge-owned file applies to all twelve stacks with no per-stack `mise.toml` edit. It carries:
  - **`[env]`**, always set (not endpoint-gated): `OTEL_EXPORTER_OTLP_ENDPOINT`,
    `OTEL_EXPORTER_OTLP_PROTOCOL`, and per-signal `OTEL_EXPORTER_OTLP_{TRACES,METRICS,LOGS}_ENDPOINT`,
    plus `VITE_OTEL_EXPORTER_OTLP_ENDPOINT` for the browser stacks. This is the amendment: every
    process `mise` launches now takes §9.4's "endpoint set" branch. The §9.4 in-code gate is
    unchanged; outside `mise` the environment is unset and a generated repo stays silent exactly
    as §9.4 describes.
  - **`[tasks]`** `otel` (alias `otel:up`), `otel:down`, `otel:status`, `otel:logs`. `otel` copies
    `.otel/*` to `${XDG_DATA_HOME:-$HOME/.local/share}/forge-otel/` and runs
    `docker compose -p forge-otel up -d --wait --force-recreate otel-collector`, then polls the
    health endpoint. `--force-recreate` is required: `collector.yaml` is bind-mounted, so an edit
    does not change the Compose config hash and a plain `up` would keep the old pipeline running.
    Re-running `otel` therefore restarts the shared collector every time; telemetry from every
    repo pauses briefly while SDKs buffer or retry (accepted trade-off, ADR-0021). `depends_on`
    still runs the one-shot volume init first. `otel` runs in the repo root via
    `dir = "{{config_root}}"` (resolved by `mise`, not a shell) and copies from relative `.otel/`
    paths; no `run` body interpolates `{{config_root}}` or any other path template. Every `run` body MUST be POSIX `sh` (`set -eu`, no bashisms), since `mise`'s default
    inline shell is `sh`, which is `dash` on Debian/Ubuntu. Owning tests:
    `TestOtelTasksRunUnderDash`, `TestOtelTasksPathMetacharactersInert`,
    `TestOtelTasksNoConfigRootInRunBodies`, `TestOtelTasksUpForceRecreates`. The live Docker
    lifecycle test `TestOtelLifecycleRecreatesOnConfigChange` (a config edit plus re-run yields a
    new container that exports through the new pipeline) is gated behind `FORGE_DOCKER_TESTS=1`
    and skips otherwise.
- `templates/common/otel/compose.yaml` → `.otel/compose.yaml`: Docker Compose project
  `forge-otel`, image `otel/opentelemetry-collector-contrib` (pinned), `container_name:
  forge-otel-collector`, `restart: unless-stopped`, ports bound to `127.0.0.1` only (`4317` gRPC,
  `4318` HTTP, `13133` health), a `busybox` one-shot init step that `chown`s the named volume
  `forge-otel-data` to the collector's non-root uid before it starts. The `otel-collector`
  service MUST declare `logging: {driver: local, options: {max-size: "10m", max-file: "3"}}`
  (option values are YAML strings) so the `debug` exporter's stdout is bounded to about 30 MB of
  Docker logs; together with the file exporters' rotation (`max_megabytes: 50`, `max_backups: 3`,
  about 200 MB per signal) host disk use is bounded (ADR-0021). Owning tests:
  `TestOtelComposeCollectorLogsBounded` (parsed template) and
  `TestOtelComposeConfigResolvesLogging` (`docker compose config` output).
- `templates/common/otel/collector.yaml` → `.otel/collector.yaml`: `otlp` receiver (grpc + http,
  CORS allowing `http://localhost:*` / `http://127.0.0.1:*` for browser stacks),
  `memory_limiter`/`batch` processors, `debug` + `file/{traces,metrics,logs}` exporters writing
  `/data/{traces,metrics,logs}.jsonl` in the volume, `health_check` extension.

Because the copy target and Compose project name are fixed, `mise run otel` from any forge repo on
the machine converges on one shared collector; a second repo's copy overwrites the first's
(last-writer-wins — identical across repos on the same forge version). No UI ships; `docker logs
forge-otel-collector` (or `mise run otel:logs`) and the `.jsonl` files are the verification path.
These three files are part of the managed/upgrade set (§19): `forge upgrade` propagates them to
existing repos.

---

## 10. Enforced gate pipeline

*(Authoritative: ADR-0003; CLI/render contract §7; init-lifecycle §6.)*

One gate definition, multiple callers — no local/CI drift. `mise.toml` holds `[tools]` (toolchain
+ lefthook) and `[tasks]` (`lint`/`fmt`/`test`/`ci`). **lefthook** runs fast checks
(secret-scan + lint + format) on `pre-commit` and full tests on `pre-push`, calling the mise tasks.
**GitHub Actions** calls the same `mise run ci` (§ below). lefthook installs in chain mode after
`bd init`.

### 10.1 Overlay tool table (de-staled — xUnit, pyright, no NUnit/mypy)

Every overlay tool traces to a canonical guideline file; the conformance test (`ebp`) enforces the
**floor** (§10.2). The **Telemetry** column is a vetted extra above the floor (§9.4, ADR-0020).

| | Format | Lint | Test | Mock | Coverage | Type | Audit | Telemetry |
|---|---|---|---|---|---|---|---|---|
| **Go** | `gofmt` | `golangci-lint` | `testing` + `go-cmp` | *(none — idiomatic fakes)* | `go test -cover` | — | `govulncheck` | `otel` + `otelhttp` + runtime metrics |
| **Python** | `ruff format` | `ruff` | `pytest` | `pytest-mock` | `pytest-cov` | `pyright` | `pip-audit` | `opentelemetry-distro` / `opentelemetry-instrument` + `instrumentation-fastapi` |
| **C#** | `dotnet format` | StyleCop.Analyzers | **xUnit** | NSubstitute | coverlet | nullable refs | `dotnet list package --vulnerable` | `OpenTelemetry.Extensions.Hosting` + AspNetCore/Http/Runtime instrumentation |
| **TypeScript** | `prettier` | `eslint` (typescript-eslint / angular-eslint / eslint-plugin-svelte) | `vitest` | *(vi stubs)* | vitest coverage | `tsc --noEmit` / `svelte-check` | `npm audit --audit-level=high` | `sdk-trace-web` + `sdk-metrics` + `instrumentation-fetch` |

### 10.2 Conformance semantics — guideline is the floor, not the ceiling

The guideline file is the **minimum** source of truth (CONTEXT.md; **ADR-0010**). The `ebp`
conformance test **fails when a guideline MUST has no corresponding overlay tool** (the overlay is
missing something required). It does **not** fail when the overlay ships a vetted extra the
guideline is silent on. Consequently `FluentAssertions` (C#) and `pytest-mock` (Python), if present
as vetted extras, require **no guideline edit**.

`ebp` reads byte-for-byte snapshots of the six canonical guideline files vendored in-repo at
`test/testdata/guidelines/` — it MUST NOT resolve guidelines at the maintainer's machine-local
canonical path (`~/peter_code/ai_support/guidelines/`), so the test suite stays hermetic on any
checkout, including CI runners (**ADR-0018**). A separate drift test
(`TestVendoredGuidelinesMatchCanonicalSource`) byte-compares the vendored snapshots to the
canonical source when it is present on the machine (or `FORGE_CANONICAL_GUIDELINES_DIR` is set) and
skips otherwise; refreshing a snapshot after a guideline edit is a `cp -f` from the canonical file
to the vendored path.

### 10.3 CI workflow shape

One **language-agnostic** `ci.yml`, identical across all stacks: checkout → install mise →
`mise install` → `mise run ci`. No inline lint/test/format. Triggers `on: [push, pull_request]`;
no scheduled run in v1. Verification asserts every shipped overlay's `mise.toml` defines a `ci`
task that `mise run ci` resolves to — the contract coupling `ud1` to `x2k` (see Seam Inventory §18).

```gherkin
Scenario: Overlay satisfies the guideline floor (ebp)
  Given a scaffolded Python project with its overlay applied
  When the ebp conformance test parses python.md against the overlay
  Then every MUST/SHOULD tool in python.md (ruff, pyright, pytest, pytest-cov, pip-audit) is present
       And a vetted extra absent from python.md does NOT fail the test
       And a shipped template with no guideline-backed language DOES fail the test

Scenario: One CI workflow calls mise run ci and references a real task
  Given any shipped v1 stack
  When its scaffolded .github/workflows/ci.yml is inspected
  Then it checks out, installs mise, runs `mise install`, then `mise run ci`,
       contains no inline lint/test/format, and the stack's mise.toml defines a resolvable `ci` task
```

---

## 11. Security model — allowlist & deny floor

*(Authoritative: allowlist/deny-floor seed spec; cites ADR-0001, ADR-0002. De-stales system-design
§6: colon-glob form, deny-only guard, D1–D16, path-token D9.)*

### 11.1 Two distinct concepts, one source file

**Allowlist** — convenience, always-growing, author-vetted command prefixes that remove the
confirmation prompt; per-project versioned managed block; canonical definition embedded in the
binary; reconciled notify-only (ADR-0001). **Deny floor** — stable, safety-oriented, blocks the
irreversible; enforced by the guard hook as a **deny-only net** (ADR-0002). Both live in one
canonical embedded source file (separate sections), refreshing on independent cadences.

### 11.2 Two enforcement layers + posture

- **Layer A — `settings.local.json` permissions** (declarative, coarse): allow/deny globs.
- **Layer B — guard hook** (PreToolUse, deny-only): splits compounds, blocks if any constituent
  is denied, **never approves**. Wired identically by Claude Code and Codex (both honor `exit 2`).
  Runs in every permission mode; auto mode bypasses the prompt but never the guard.
- **Glob format:** `Bash(tool:*)` (colon form). The param-style `Bash(command:rm *)` is silently
  ignored by Claude Code — never use it. `*` matches across spaces (ADR-0002).
- **Compounds auto-approve natively** when every constituent matches an allow rule (the native
  matcher is shell-operator-aware). The guard never approves compounds (ADR-0002). The seed's job
  is to make the allowlist complete; compounds then work for free.
- **Default posture: allow by default.** Commands matching neither allow nor deny **run** — the
  deny floor is a safety net, not an allowlist jail.

### 11.3 Deny floor — D1–D16

| # | Rule | Decision |
|---|---|---|
| D1 | `rm -rf` / recursive-force delete (any flag order) | block |
| D2 | `git rm -rf` / mass cached removal | block |
| D3 | `git push --force`/`-f` to a protected branch (`main master develop release/* production`) | block |
| D4 | history-rewriting push to a protected branch | block |
| D5 | `DROP DATABASE`/`DROP SCHEMA`/`dropdb`/`TRUNCATE` (no WHERE) | block |
| D6 | `mkfs*`, `dd of=/dev/*`, `> /dev/sd*`, fork bombs, `chmod -R 777 /`, `chmod 777` | block |
| D7 | `git reset --hard` / `git clean -fdx` / `git checkout .` discarding work | block |
| D8 | `git commit --no-verify`/`-n`, `--no-gpg-sign` (never bypass commit hooks) | block |
| D9 | **Secret path-token scan:** any command line referencing a secret path token (`.env`, `.env.*`, `*.pem`, `*.key`, `id_rsa*`, `id_ed25519*`, `credentials`, `*secret*`, `*.tfstate`, `.ssh/`, `.aws/`, `.gnupg/`, git-ref forms `HEAD:.env`) — **regardless of binary** (`cat`/`awk`/`python -c`/`git show`/`tar -O`/`base64`/`$(<…)`) | block |
| D10 | **Env-dump verbs:** bare `env`, `printenv`, `set`, `export -p`, `declare -p/-x`, `typeset -p`, `compgen -v`, `/proc/*/environ`, `launchctl getenv` | block |
| D11 | **Explicit exfil channels:** `curl` request-data/form/upload/config/query-expansion options; `wget` POST/body data options; `nc`, `ncat`, `socat`, `scp`, `sftp`, `rsync`; clipboard (`pbcopy`/`xclip`/`xsel`/`wl-copy`); `/dev/tcp/`+`/dev/udp/`; DNS-exfil. Ordinary HTTP reads and authentication probes are not denied (ADR-0015). | block |
| D12 | **System/process control:** `sudo`, `kill`/`killall`/`pkill`, `shutdown`/`reboot` | block |
| D13 | **Remote shells:** `ssh` | block |
| D14 | **Destructive docker:** `docker rm`/`rmi`/`system prune` | block |
| D15 | **Shell history:** `history`, `fc -l` | block |
| D16 | **Interpreter-class (belt-and-suspenders):** `bash -c`, `sh -c`, `zsh -c`, `eval`, `python -c`, `python3 -c`, `node -e`, `perl -e`, `ruby -e` | block |

> **D9 is a path-token scan, not a command-name list** (ADR-0002). A name list is trivially
> bypassed (`python3 -c 'open(".env")'`, `awk '1' .env`, `git show HEAD:.env`) and is explicitly
> rejected. **Documented irreducible gaps** (a string hook cannot close these): obfuscated paths
> inside interpreter one-liners, raw `git cat-file -p <sha>` / `git stash show -p` (no path token),
> hard-link/inode reuse, secrets already exported before the session. The OS sandbox is the backstop.

### 11.4 Seed contents

- **Universal allow (every project):** version control (`git status/diff/log/add/commit/...`,
  `gh pr/api/repo create/...`), toolchain (`bd:*`, `instill:*`, `apm:*`, `mise:*`, `lefthook:*`),
  file ops (`ls/cat/head/tail/mkdir/cp/mv/ln/chmod/touch/find/...`), text processing
  (`grep/rg/fd/jq/sed/awk/sort/...`), misc shell (`echo/printf/date/xargs/timeout/bash`),
  native Claude tools (`Read Glob Grep Edit Write WebSearch`), and `Skill(*)` so every
  instill-installed skill is usable without a permission prompt (ADR-0012).
- **Per-stack slice (only the project's stack):** Go → `Bash(go:*)`; Python →
  `python/python3/uv/pip/pytest/ruff/pyright/...`; C# → `Bash(dotnet:*)`.
- **Personal section** (tagged block; stripped by default, included via `--include-personal`):
  `gw/rtk/slack-cli`, cloud CLIs, `brew`, `docker` read ops.
- **Deliberately OUT** (stay prompting): `curl`, `wget`. They remain subject to native explicit
  approval, while D11 denies only request-data, form, upload, configuration, query-expansion, and
  POST/body-data forms (ADR-0015). Ordinary HTTP reads and authentication probes are not denied.

### 11.5 OS sandbox (both agents; FS isolation, network ON)

Claude (`sandbox.enabled` + per-path `denyRead` of `~/.ssh`, `~/.aws`, `~/.gnupg`) and Codex
(`sandbox_mode = "workspace-write"`, `network_access = true`). **Network is ON** — network-off
breaks day-one `git push`/`gh repo create`/`bd dolt push`/dependency installs. D11 blocks raw
transfer channels and structurally explicit HTTP uploads; it is not a complete egress firewall
(ADR-0015). This accepts that OS-level exfil protection is traded for zero network friction.
Asymmetry: Codex lacks per-path `denyRead` and a domain allowlist (ADR-0002).

```gherkin
Scenario: Guard is deny-only and terminal
  Given auto mode and a command matching a deny rule
  When the PreToolUse guard runs
  Then it exits 2 with a reason on stderr And the command never runs
       And the guard never returns an allow-decision for any command

Scenario: A vetted compound auto-runs without the guard approving it
  Given `git status:*` and `grep:*` are on the allowlist
  When the agent emits `git status && grep foo .`
  Then the native matcher auto-approves both constituents And the guard does not block
```

---

## 12. Shared secret-scan

*(Authoritative: secret-scan spec; cites ADR-0003. Note: the spec file's name matches a D9 token,
so read it with `cat`, not the Read tool.)*

One `.claude/hooks/secret-scan.sh` (POSIX bash, verbatim, mode 0755) implements seed rules **D9**
(path-token scan) + **D10** (env-dump verbs) and nothing else — D1–D8 and D11–D16 are the guard
hook's separate concern. Two modes over a shared core:

| Mode | Caller | Input | Blocks |
|---|---|---|---|
| `scan-command` | agent guard (PreToolUse) | command line (JSON on stdin, or `--command`) | D9 path-token refs + D10 env-dump verbs |
| `scan-staged` | lefthook pre-commit | `git diff --cached --name-only` | staged files whose path matches a secret pattern |

**Shared core:** one `SECRET_PATTERNS` array, one `EXEMPT_PATTERNS` carve-out (`*.example`,
`*.sample`, `*.template` — checked first), one matcher used by both modes. **Exit contract:** `0`
clean / `2` block + reason on stderr / `64` usage. Missing stdin or not-a-git-repo → `0` (never
crash the agent). The future guard composes `secret-scan.sh scan-command`; lefthook calls
`secret-scan.sh scan-staged`. This is the shared-script half of ADR-0003's defense-in-depth.

```gherkin
Scenario: D9 blocks a secret path regardless of binary
  Given a scan-command payload whose command displays a dotenv file
  Then it exits 2 with a D9 reason naming the token
       And awk on a dotenv, `git show HEAD:.env`, a .pem display, and a .aws/credentials
           reference all block

Scenario: D10 blocks bare env dumps but allows filtered ones
  Given bare `env`, `printenv`, `set`
  Then each blocks And `env | grep -v SECRET` is allowed

Scenario: scan-staged blocks staged secrets, allows examples
  Given a staged `.env` (and separately a staged `.env.example`)
  Then committing the `.env` is blocked And the `.env.example` commits clean
       And no staged files / outside a git repo exits 0
```

---

## 13. Reconciler & SessionStart wiring

*(Authoritative: CLI/render contract §6; init-lifecycle §4; cites ADR-0001.)*

**Staleness detection — monotonic integer.** The binary embeds `ALLOW_VERSION`; the managed block
carries `v:N`. `embedded > block` → stale. A guard test asserts "if the seed file changed in a
commit, `ALLOW_VERSION` changed too" (forgot-to-bump backstop).

**SessionStart hooks — both agents, same three in order, always exit 0:**

1. `bd prime` (beads context)
2. `instill sync` (APM install + compile — regenerates `.apm/` skill content)
3. `forge sync-allowlist --check` (staleness notice — advisory, last so most visible)

Order is for readability; no hook depends on another's output. Every forge-authored hook **exits
0 always** and **no-ops silently if `forge` is missing** (`command -v forge || exit 0`) — a
scaffolded repo opens cleanly on a machine without forge. The mutating `forge sync-allowlist`
(no `--check`) stays human-invoked; the `--check` hook never mutates and never blocks.

```gherkin
Scenario: Stale allowlist notifies without blocking
  Given a managed block at v:5 and embedded ALLOW_VERSION = 7
  When the SessionStart hook runs `forge sync-allowlist --check`
  Then it prints a "2 versions behind" notice, exits 0, and the managed block is unchanged

Scenario: Missing forge binary never breaks session start and is silent
  Given a collaborator's machine without forge on PATH
  When SessionStart fires the sync-allowlist --check hook
  Then the hook exits 0 and prints nothing And bd prime + instill sync still ran
```

---

## 14. Agentic baseline — every repo, day one

*(Authoritative: system-design §8.)*

Beyond permissions, every scaffolded repo ships: an **ADR scaffold** (`docs/adr/` + MADR template);
a **`CONTEXT.md` glossary stub**; **Codex full parity** (shared guard, allowlist equivalent, skill
access — not just `bd prime`); a **`FORGE CONVENTIONS` block** in `AGENTS.md` (Co-Authored-By
footer, conventional-comments, <300-line PR norm); and **gitignored APM artifacts**
(`.apm/` compiled content machine-regenerable; `apm.yml` + `apm.lock.yaml` committed).

---

## 15. Maintainer update path

*(Authoritative: CLI/render contract §8; cites ADR-0005, ADR-0006. Maintainer-only, online.)*

`forge update` refreshes all stacks by default; `--stack <key>` scopes to one. **Refresh
currently supports Go stacks only** — the python/csharp/typescript rows in `sources.yaml` are
authoritative recipes, but their vanilla layers are captured manually (run the recipe steps by
hand, apply the normalize rules) until the interpreter gains `strip_paths` plus npm/dotnet
placeholder mappings; `forge update` on those stacks fails loudly naming this section. Each run: execute
the recipe's ordered steps (`checkout`/`run`, with per-step `strip:` on a `checkout`) in a temp dir (`369`) → run the per-stack
normalization pass over the **vanilla layer only** → write the snapshot into
`templates/golden/<key>/` → re-pin `sources.yaml`. It **regenerates vanilla only — never
`.forge-overlay/`** — and **fails loudly** naming any missing native scaffolder. Rebuilding the
binary is a separate manual step after the maintainer reviews the diff.

**Determinism (ADR-0006):** contracted by an **idempotence test**, not an exhaustive up-front rule
list. Normalization rules are per-stack `normalize:` blocks in the `sources.yaml` row (variance is
tool-specific). `captured:` means last-*changed*, not last-*run*. **Refresh seam:** after
regenerating vanilla, check committed overlay paths against the new vanilla tree — **orphan** (overlay
parent dir gone) = hard fail, write nothing, name paths; **collision** (new vanilla file at an
overlay path) = loud warn, complete the write (overlay-wins still correct).

```gherkin
Scenario: Update refreshes the snapshot but preserves the overlay
  Given the maintainer repo with a pinned go-cli-cobra snapshot and overlay
  When `forge update --stack go-cli-cobra` runs
  Then templates/golden/go-cli-cobra/ reflects new output
       And .../go-cli-cobra/.forge-overlay/ is byte-identical And sources.yaml records the ref/SHA

Scenario: Update is idempotent when upstream is unchanged
  When the maintainer runs `forge update --stack <key>` twice with no upstream change
  Then templates/golden/<key>/ is byte-identical And .forge-overlay/ is untouched
       And git diff is empty

Scenario: Refresh seam fails loud on an orphaned overlay file
  Given an upstream refresh removes the vanilla parent dir of a committed overlay file
  When update runs
  Then it exits non-zero naming each orphaned path And writes nothing
```

---

## 16. Verification & smoke tiers

*(Authoritative: init-lifecycle §7; smoke-cleanup contract added 2026-06-20 grill-with-docs.)*

Two tiers prove the "indistinguishable from hand-built" promise. Because `mise run ci` includes
`test` (ADR-0003), green CI inherently proves the snapshot's starter test passes — therefore
**every shipped golden snapshot MUST ship at least one real passing test** (owned by `3tt`) so
`mise run test` is never vacuously green.

### 16.1 Local smoke (every run, hermetic, offline)

`forge init --stack <key> --remote none` (the primary consumer of `--remote none`) → `mise
install` → `mise run ci` exits 0 → clean initial commit through pre-commit gates → **assert no
network access occurred**.

**JS-stack amendment (ADR-0014):** stacks whose init runs `npm install` cannot install
offline (the lockfile is deliberately not vendored). Their hermetic tier stubs `npm` — proving
composition, gate-task shape, and lifecycle wiring, with the non-JS half running for real —
and the real JS toolchain proof moves to a **network-gated tier** (`FORGE_SMOKE_NETWORK=1`,
`cmd/forge/walking_skeleton_network_test.go`), mirroring how §16.2 gates on gh credentials.
Once installed, `mise run ci` is local except `npm audit` (kept in ci deliberately, like
`pip-audit`/`govulncheck`).

### 16.2 Full golden path (gated behind gh credentials; nightly/opt-in)

`--remote gh` end to end → remote created → first push passes pre-push → CI green on the remote.

### 16.3 Smoke teardown — ephemeral by construction (no leaked artifacts)

The full-tier smoke **creates its repo under a dedicated throwaway namespace** (e.g.
`forge-smoke-<runid>`), runs its assertions, then **deletes the repo in a `trap`/`defer` so
teardown fires even on mid-test failure**, and finally **asserts the repo no longer exists**
(cleanup is verified, not assumed).

> **Hard invariant — the test's delete-remote is a separate code path from the product's
> never-delete-remote (ADR-0009).** The product (`wer`) MUST NEVER delete a user's repo; the smoke
> harness MUST always delete its own. These two paths must never be conflated — wiring the test's
> teardown into product code would risk deleting user data.

```gherkin
Scenario: Local smoke proves a scaffolded stack works offline
  Given an empty dir
  When `forge init --stack <key> --remote none`, then `mise install`, then `mise run ci`
  Then the repo contains mise.toml, lefthook.yml, and ci.yml
       And `mise run ci` exits 0 (running >=1 real test)
       And an initial commit succeeds through the pre-commit gates
       And no network/GitHub access occurred

Scenario: Snapshot tests are not vacuous
  Given any shipped v1 stack
  When its starter test is removed and `mise run test` is run
  Then `mise run test` fails

Scenario: Full-tier smoke leaves no artifacts, even on failure
  Given gh credentials are available
  When the full golden-path smoke runs and an assertion fails mid-test
  Then the throwaway remote is still deleted by the teardown trap
       And the harness asserts the repo no longer exists
       And no product code path performed the deletion
```

---

## 17. File manifest

*(Authoritative: system-design §7.)*

Roles: **[embed]** compiled in · **[render]** templated at init · **[verbatim]** copied as-is ·
**[link]** symlinked · **[delegate]** produced by `bd`/`instill`/native tooling.

```
forge/                              # source repo
├── cmd/forge/main.go               # CLI entrypoint: init (default), update, sync-allowlist
├── internal/
│   ├── scaffold/                    # Phase 1: render, copy, symlink, settings, guard
│   ├── delegate/                    # Phase 2: shell out to bd + instill + lefthook install
│   ├── remote/                      # Phase 3: gh repo create / git remote add
│   ├── prompt/                      # interactive picker (language → type → stack)
│   ├── catalog/                     # stack-key matrix + resolution (01b — exists)
│   └── update/                      # maintainer recipe interpreter + normalization (369, cjl)
├── sources.yaml                     # [maintainer] pinned upstreams (recipe rows; §9.2)
├── templates/                       # ── all embed.FS ──
│   ├── common/
│   │   ├── gitignore.base           # [verbatim] multi-language base .gitignore
│   │   ├── AGENTS.md.tmpl           # [render]
│   │   ├── claude/{settings.json, settings.local.json.tmpl, hooks/{guard, secret-scan.sh}}
│   │   ├── codex/hooks.json         # [verbatim]
│   │   ├── mise/conf.d/otel.toml    # [verbatim] → .config/mise/conf.d/otel.toml (§9.5)
│   │   └── otel/{compose.yaml, collector.yaml}  # [verbatim] → .otel/ (§9.5)
│   ├── seed/skills.json.tmpl        # [embed] default skill list, rendered in memory at init
│   ├── gitignore/                   # [embed] vendored github/gitignore per language (Go, Python, VisualStudio, Node)
│   └── golden/<key>/                # [embed] pinned snapshots + .forge-overlay/ per v1 stack
│                                    #   typescript keys double as fullstack web/ fragments (ADR-0013)
└── docs/                            # PRD.md, SPEC.md, adr/, handoffs/, superpowers/

# Scaffolded output (what the verification test inspects):
myproject/
├── .git/                  [delegate]   ├── AGENTS.md          [render] + beads block
├── .gitignore  base+lang merged        ├── CLAUDE.md → AGENTS.md  [link]
├── .claude/{settings.json, settings.local.json, hooks/{guard, secret-scan.sh}}
├── apm.yml, apm.lock.yaml [delegate]   ├── .apm/              [delegate, gitignored]
├── .codex/hooks.json                   ├── .beads/            [delegate]
├── .forge/manifest.json   [render]     ├── .forge-infra-version   [render, legacy fallback]
├── mise.toml, lefthook.yml, .github/workflows/ci.yml
├── .config/mise/conf.d/otel.toml       ├── .otel/{compose.yaml, collector.yaml}
└── <composed golden tree>  vanilla + overlay, rendered
```

---

## 18. Seam Inventory — cross-component couplings & their owning tests

*(New in this spec. Zero-silos is structural: every coupling between work items names the single
test that owns the boundary. A seam without a named owning test is a defect, surfaced like a
contradiction. Scheduling is enforced via the Epic → Feature → Story structure in beads, where a
Feature's `all-children` gate IS the seam test.)*

| Seam | Components | Owning test (where it lives) | Scheduling |
|---|---|---|---|
| **Composition** | `83n` (contract) × `iha` (Phase-1 writer) × `3tt` (catalog assets) × `yuw` (common assets) × `s5x` (init entrypoint that invokes composition end-to-end) | **Walking-skeleton Feature gate**: `forge init --stack <key> --remote none` composes correctly → `mise run ci` green → ≥1 real test passes, offline. MAY prove with one stack first; MUST cover all six to close. | Feature `all-children` gate; pulled forward as the first user-visible milestone. |
| **CI resolves** | `ud1` (CI workflow) × `x2k` (mise `ci` task) | **Gate-pipeline Feature gate**: every shipped overlay's `mise.toml` defines a `ci` task that `mise run ci` resolves to, across all v1 stacks. | Feature `all-children` gate. |
| **Update idempotence** | `369` (steps interpreter) × `cjl` (normalization/refresh) | **Maintainer-refresh Feature gate**: `forge update --stack <key>` run twice with no upstream change → `templates/golden/<key>/` byte-identical, overlay untouched, git diff empty. | Feature `all-children` gate. |
| **Conformance fixture** | `zz8` (publish guidelines) → `ebp` (conformance test) | Precondition, not a composition seam: `ebp` reads the guideline files from the in-repo vendored snapshots (`test/testdata/guidelines/`, **ADR-0018**), refreshed from the canonical path by `cp -f` after `zz8` publishes. | Plain `blockedBy` edge (`ebp` blocked-by `zz8`). |
| **Chain-mode hooks** | `485` (verify beads hooks survive `lefthook install`) → `x2k` (real multi-job lefthook) | `485` proves chain mode in isolation; the local smoke (§16.1) exercises pre-commit/pre-push at runtime without duplicating the `485` proof. | `x2k` blocked-by `485` (existing edge). |
| **Telemetry bootstrap** | overlay telemetry module + test × stack manifest OpenTelemetry dependency (`agentic_template_start-6cf`, ADR-0020) | `TestGoldenStacksShipTelemetryBootstrap` (`test/golden_assets_test.go`): for every v1 stack, the manifest pins the OpenTelemetry dependency **and** the overlay ships the telemetry module and its test; `TestPythonStacksRunUnderOpentelemetryInstrument` pins the `serve`/`run-cli` wrapper task. | Epic `all-children` gate; each platform child (`.3`–`.6`) turns its rows green. |

### 18.1 Work hierarchy (Epic → Feature → Story)

```mermaid
flowchart TD
    EPIC["EPIC: forge v1 — one command → a complete AI-native project"]

    subgraph F1["FEATURE: Walking-skeleton scaffold (gate = composition seam test)"]
        S83["83n contract"] & SIHA["iha Phase-1 writer"] & S3TT["3tt catalog assets"] & SYUW["yuw common assets"] & SS5X["s5x init entrypoint"]
    end
    subgraph F2["FEATURE: Enforced gate pipeline (gate = CI-resolves seam test)"]
        SUD1["ud1 CI workflow"] & SX2K["x2k mise tasks"]
    end
    subgraph F3["FEATURE: Maintainer stack refresh (gate = update-idempotence seam test)"]
        S369["369 steps interpreter"] & SCJL["cjl normalization/refresh"]
    end

    FLAT["Flat under EPIC: 7sn guard · 79r secret-core · 02o scan-staged · 485 chain-mode ·
          rom reconciler · wer remote · uuw smoke · ebp conformance · zz8 publish · 7eu post-v1
          (edges: ebp ⊣ zz8 · uuw ⊣ walking-skeleton Feature · x2k ⊣ 485)"]

    EPIC --> F1 & F2 & F3 & FLAT
```

---

## 19. Infrastructure upgrade path

`forge upgrade` propagates managed static infrastructure files from the embedded template into an
existing forge-scaffolded repository when the on-disk infrastructure version is behind the
embedded version. `.claude/hooks/guard`, `.claude/hooks/secret-scan.sh`,
`.opencode/plugins/forge-hooks.js`, `.config/mise/conf.d/otel.toml`, `.otel/compose.yaml`, and
`.otel/collector.yaml` (§9.5, ADR-0021) are wholly forge-owned: nothing else writes to them, so
they are overwritten unconditionally (blind byte-copy).

`.claude/settings.json` and `.codex/hooks.json` are **co-owned**: other tools (bd, notably) append
their own entries into the same top-level `hooks` object. These two files are never blind-
overwritten. When the destination does not exist yet, the embedded template is written directly.
When it exists, `internal/hookcfg.Reconcile` (ADR-0016) merges it against the canonical template by
per-entry command-string identity: every owned entry converges to its canonical form exactly once;
every third-party matcher group, entry, and unrelated top-level key survives untouched; a
historical registry (`bd prime || true` ← `bd prime`, etc.) converges old shipped forms instead of
leaving them as stale duplicates. A malformed on-disk co-owned file fails the upgrade loudly, names
the file in the error, and is left unmodified — it is not overwritten, and no marker or manifest is
stamped for that run, so a fixed repo re-applies cleanly on the next `forge upgrade`.

`opencode.jsonc` is co-owned differently: it is rendered from a `.tmpl` using project-specific
parameters, so blind-copying the raw template would write unrendered `{{ }}` syntax to disk.
`forge upgrade` renders it **only when the file is missing** on disk, using
`.forge/manifest.json`'s persisted `language`/`frontend`/`includePersonal` params (`text/template`,
`missingkey=error`); once it exists, upgrade never touches it again — hand edits and its managed
allow-block (kept in sync via `forge sync-allowlist`) both survive. A repo with no manifest yet
(uninferable params) skips the render silently rather than guessing.

Version state lives in `.forge-infra-version` (repo root). Missing file → version 0 → always stale.
Managed writes happen first; the version marker and manifest are written last so interrupted
upgrades re-apply.

`forge init` stamps both `.forge/manifest.json` and `.forge-infra-version` during Phase 1, right
after the scaffold writer runs and before Phase 3 (`git add`/`commit`), so a freshly scaffolded
repo is born at the current infrastructure version — never v0 — and both files ride the initial
scaffold commit. `.forge/manifest.json` is a committed JSON record (`schemaVersion`,
`infraVersion`, and the init params `language`, `frontend`, `includePersonal`, `stack`) that lets
`forge upgrade` re-render templated files using the params the repo was actually scaffolded with,
instead of only knowing a bare version integer. `.forge-infra-version` remains the legacy fallback
marker for repos scaffolded before the manifest existed and for v3-and-earlier `forge` binaries
that don't know about it. When both are present, the manifest's `infraVersion` is authoritative;
`forge upgrade` on a repo with a manifest preserves its params while advancing `infraVersion`.

On a legacy repo with no manifest, `forge upgrade` attempts a best-effort backfill: it infers
`language` and `frontend` from the managed allowlist block in `.claude/settings.local.json`
(`allowlist.InferLanguage`/`InferFrontend`; `includePersonal` always defaults `false`, since it is
never inferable). When inference succeeds, `.forge/manifest.json` is written at the current
`infraVersion` with the inferred params. When it fails (no `settings.local.json`, or an ambiguous
managed block), the upgrade still succeeds — infra files were already reconciled above — but writes
only the bare legacy marker and prints a one-line stderr notice suggesting the user create
`.forge/manifest.json`; it never fabricates params (ADR-0017 Consequences).

`forge upgrade` also repairs `.beads` directory permissions to `0700` if drifted
(`EnsureBeadsDirPerms`), and reports the repair on stdout when one happened.

`forge upgrade --check` reports staleness and `.beads` permission drift (exit 1 if either) without
mutation — it uses `os.Stat` only, never `os.Chmod`. The SessionStart hooks in both
`settings.json` and `codex/hooks.json` run this advisory on every session open.

```gherkin
Scenario: First upgrade on a legacy repo
  Given a forge-scaffolded repo with no .forge-infra-version file
  When the user runs `forge upgrade`
  Then all managed files are overwritten from the embedded template
  And .forge-infra-version is created with the current version

Scenario: Upgrade is idempotent
  Given a repo at infrastructure version N with embedded version N
  When the user runs `forge upgrade`
  Then no files are written
  And output reports "infrastructure is current (vN)"

Scenario: Check mode advisory on session start
  Given a repo at infrastructure version 1 and embedded version 2
  When SessionStart fires `forge upgrade --check`
  Then the hook prints the staleness advisory and exits 1
  And no files are modified

Scenario: Freshly scaffolded repo is born current
  Given a repo just created by forge init
  When SessionStart fires `forge upgrade --check`
  Then the check exits 0 with no advisory

Scenario: Upgrade preserves third-party hook entries
  Given .claude/settings.json contains a beads-installed SessionStart hook
  When the user runs forge upgrade
  Then forge-managed entries match the template
  And the beads-installed entry is still present

Scenario: Clone of a scaffolded repo is not stale
  Given a repo scaffolded by forge init, with .forge/manifest.json and
    .forge-infra-version committed in the initial scaffold commit
  When another machine clones the repo and SessionStart fires `forge upgrade --check`
  Then the check exits 0 with no advisory
  And no files are modified

Scenario: Beads permission drift is reported and repaired
  Given a repo whose .beads directory mode has drifted away from 0700
  When the user runs `forge upgrade --check`
  Then the command reports the .beads permission drift and exits 1
  And the .beads directory mode is left unchanged
  When the user then runs `forge upgrade`
  Then the .beads directory mode is repaired to 0700
  And the repair is reported on stdout

Scenario: Upgrade never writes template syntax to disk
  Given a repo with no opencode.jsonc and a .forge/manifest.json carrying init params
  When the user runs `forge upgrade`
  Then opencode.jsonc is rendered from the .tmpl using text/template, missingkey=error
  And no file written by upgrade contains unrendered "{{" template syntax
```

---

*Authored By Peter O'Connor with Assistance from Claude Code (databricks-claude-opus-4-8) · 2026-06-20 · forge definitive specification*

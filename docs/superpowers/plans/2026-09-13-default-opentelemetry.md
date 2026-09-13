# Default OpenTelemetry Bootstrap Execution Plan

## Summary
Execution plan for Beads epic `agentic_template_start-6cf`: every one of the twelve golden stacks ships an env-gated OpenTelemetry bootstrap (traces + metrics) in its `.forge-overlay/` with a real in-memory-exporter test, recorded in ADR-0020 and aligned across SPEC, CONTEXT, and the generated `AGENTS.md` / `README.md`. Design: `docs/superpowers/specs/2026-09-13-default-opentelemetry-design.md`.

Commit policy: **one commit per task** on `claude/default-opentelemetry-integration-sg3icg`, made by the orchestrator after the task's Test Command is confirmed green. Phase workers do not commit.

---

## Task 1: Cross-Stack Contract Test (`agentic_template_start-6cf.1`)

### Contract
**Given** the 12 shipped golden stacks,
**When** `go test ./test -run TestGoldenStacksShipTelemetryBootstrap` runs,
**Then** it fails for every stack because no manifest pins an OpenTelemetry dependency and no overlay ships a `telemetry` module + test (RED).

### Target Files
- `test/golden_assets_test.go` (new table test, extended per-stack file lists, `TestPythonStacksRunUnderOpentelemetryInstrument`)

### Test Command
`GOCACHE=$PWD/.cache/go-build go test ./test -run 'TestGoldenStacksShipTelemetryBootstrap|TestPythonStacksRunUnder|TestGoldenCatalogPackages' -v` (expected RED)

---

## Task 2: Decision Record and Docs (`agentic_template_start-6cf.2`)

### Contract
**Given** ADR-0010's vetted-extra rule,
**When** ADR-0020, SPEC §9.4 + §10.1 Telemetry column + §18 seam row, the CONTEXT.md term, and the `templates/common/AGENTS.md.tmpl` / `README.md.tmpl` Observability sections exist,
**Then** `go test ./test -run 'TestReadme|TestMakefile'` and `go test ./internal/scaffold` stay green.

### Target Files
- `docs/adr/0020-default-opentelemetry-bootstrap.md`
- `docs/SPEC.md`
- `CONTEXT.md`
- `templates/common/AGENTS.md.tmpl`
- `templates/common/README.md.tmpl`
- `README.md`
- `docs/superpowers/specs/2026-09-13-default-opentelemetry-design.md`
- `docs/superpowers/plans/2026-09-13-default-opentelemetry.md`

### Test Command
`GOCACHE=$PWD/.cache/go-build go test ./test -run 'TestReadme|TestMakefile|TestVerifyTemplates' -count=1 && GOCACHE=$PWD/.cache/go-build go test ./internal/scaffold -count=1`

---

## Task 3: Go Worker (`agentic_template_start-6cf.3`)

### Contract
**Given** a rendered Go stack,
**When** `go test ./...` runs in it,
**Then** `internal/telemetry` (cobra: `cmd/telemetry.go`) exports one span and one metric through in-memory exporters, HTTP stacks record `GET /health`, and `Setup` returns a nil-error shutdown with no endpoint set.

### Target Files
- `templates/golden/go-cli-cobra/go.mod.tmpl`, `templates/golden/go-api-chi/go.mod.tmpl`, `templates/golden/go-web-templ/go.mod.tmpl`
- overlay `internal/telemetry/telemetry.go.tmpl` + `internal/telemetry/telemetry_test.go.tmpl` (chi, templ)
- `templates/golden/go-api-chi/.forge-overlay/cmd/api/main.go.tmpl`, `.forge-overlay/internal/httpapi/router.go.tmpl`, `.forge-overlay/internal/httpapi/health_test.go.tmpl`
- `templates/golden/go-web-templ/.forge-overlay/cmd/web/main.go.tmpl`, `.forge-overlay/internal/web/server.go`, `.forge-overlay/internal/web/server_test.go`
- `templates/golden/go-cli-cobra/.forge-overlay/cmd/telemetry.go.tmpl`, `cmd/telemetry_test.go.tmpl`, `cmd/serve.go.tmpl`, `cmd/config.go.tmpl`

### Test Command
Task 1 rows for `go-*` green, plus `GOCACHE=$PWD/.cache/go-build go test ./cmd/forge -run TestWalkingSkeletonGoCLIInitRunsThroughMiseCI`, plus a rendered `go-api-chi` / `go-web-templ` → `go mod tidy && go vet ./... && go test ./...`

---

## Task 4: Python Worker (`agentic_template_start-6cf.4`)

### Contract
**Given** a rendered Python stack,
**When** `pytest -q` runs,
**Then** `build_providers` exports a span and collects a counter in memory, the app path records `GET /health` (typer: `hello`), the `opentelemetry-instrument` subprocess prints the span with the console exporter, and `ruff check . && pyright` pass.

### Target Files
- `templates/golden/python-cli-typer/pyproject.toml.tmpl`, `templates/golden/python-fastapi/pyproject.toml.tmpl`, `templates/golden/python-web-jinja/pyproject.toml.tmpl`
- overlay `{{.PythonPackage}}/telemetry.py.tmpl` (typer: `src/{{.PythonPackage}}/telemetry.py.tmpl`)
- the three `main.py` entrypoints
- overlay `tests/test_telemetry.py.tmpl`
- `.forge-overlay/mise.toml(.tmpl)` `serve` / `run-cli` tasks (typer and web-jinja renamed to `.tmpl`)

### Test Command
Task 1 rows for `python-*` green, plus rendered `uv venv && uv pip install -e .[dev] && pytest -q && ruff check . && pyright`

---

## Task 5: C# Worker (`agentic_template_start-6cf.5`)

### Contract
**Given** a rendered C# stack,
**When** `dotnet test tests/Project.Tests` runs with `-warnaserror`,
**Then** `AddDefaultOpenTelemetry` exports one `Activity` and one metric point through `OpenTelemetry.Exporter.InMemory`, and `dotnet format --verify-no-changes` passes.

### Target Files
- three `Project.csproj.tmpl`, three `tests/Project.Tests/Project.Tests.csproj.tmpl`
- overlay `Telemetry.cs.tmpl` + `TelemetryServiceCollectionExtensions.cs.tmpl`
- overlay `tests/Project.Tests/TelemetryTests.cs.tmpl`
- `templates/golden/csharp-webapi/Program.cs`, `templates/golden/csharp-cli/Program.cs`, `templates/golden/csharp-blazor/.forge-overlay/Program.cs.tmpl`

### Test Command
Task 1 rows for `csharp-*` green, plus `make verify-fast` (csharp-cli end to end) if `mise` + `bd` can be installed, else `workflow_dispatch` of `slow-gate`

---

## Task 6: TypeScript Worker (`agentic_template_start-6cf.6`)

### Contract
**Given** a rendered TS stack,
**When** `npm run lint && npm run typecheck && npm test` run,
**Then** `startTelemetry` exports one span and one metric with in-memory exporters under vitest, `hooks.client.ts` / `main.ts` / `app.config.ts` start it in the browser only, and `svelte-check` passes.

### Target Files
- three overlay `package.json.tmpl`
- `src/lib/telemetry.ts.tmpl` (+ `telemetry.test.ts`) for vite-ts and sveltekit
- `templates/golden/sveltekit/.forge-overlay/src/hooks.client.ts`
- `templates/golden/vite-ts/.forge-overlay/src/main.ts`
- `templates/golden/angular/.forge-overlay/src/app/telemetry.ts.tmpl` (+ `telemetry.spec.ts`), `src/app/app.config.ts`, `.forge-overlay/src/environments/environment.ts.tmpl`

### Test Command
Task 1 rows for TS green, plus rendered `npm install && npm run lint && npm run typecheck && npm test`, then `FORGE_SMOKE_NETWORK=1 GOCACHE=$PWD/.cache/go-build go test ./cmd/forge -run TestNetworkSmoke`

---

## Task 7: Verification Workers (`agentic_template_start-6cf.7`)

### Contract
**Given** all Phase 2 tasks green,
**When** the full suite and native toolchains run,
**Then** `GOCACHE=$PWD/.cache/go-build go test ./... -count=1` is green and each platform's native run is recorded (or its unavailability stated) for the PR body, including the no-collector noise check.

### Target Files
- none (verification only; findings feed Task 8 and the PR body)

### Test Command
`go build ./cmd/forge && GOCACHE=$PWD/.cache/go-build go test ./... -count=1`, plus the per-platform native commands from Tasks 3–6

---

## Task 8: Review and Close (`agentic_template_start-6cf.8`)

### Contract
**Given** the cumulative diff,
**When** a review subagent (`/code-review` on the branch) runs,
**Then** all confirmed findings are fixed, the Beads epic and children are closed, follow-up issues are filed (logs bridges, `TestLocalRelease` coverage for the three fullstack stacks, chi route-pattern span names), and the branch is pushed with a draft PR opened and subscribed.

### Target Files
- whatever the review names, plus `.beads/` issue state

### Test Command
`GOCACHE=$PWD/.cache/go-build go test ./... -count=1` green before push

---

*Authored By Peter O'Connor with Assistance from Claude Code · 2026-09-13*

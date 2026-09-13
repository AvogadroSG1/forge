# Forge Bug Bash 20260912 Execution Plan

## Summary
Execution plan for Beads epic `agentic_template_start-sgz` resolving 9 reported issues in Forge and scaffolded repository configurations.

Commit policy: `none` (no commits made during phase worker execution; final review verifies cumulative diff).

---

## Task 1: Update Generated OpenCode Permissions (`agentic_template_start-sgz.1`)

### Contract
**Given** a generated `opencode.jsonc` rendered from `templates/common/opencode.jsonc.tmpl`,
**When** configuration is parsed,
**Then** `permission.external_directory` contains allow entries for `"~/ObsidianNotes/**"`, `"~/peter_code/scratch_work/**"`, `"~/peter_code/ai_support/**"`, and `"~/.plannotator/plans/**"`, `permission.write` is set to `"allow"`, permissions `read`, `edit`, `write`, `bash`, `external_directory`, `grep`, and `glob` evaluate to allow, and `external_directory` is outside the managed allowlist markers.

### Target Files
- `templates/common/opencode.jsonc.tmpl`
- `internal/allowlist/sync_test.go`
- `test/security_hooks_test.go`

### Test Command
`GOCACHE=$PWD/.cache/go-build go test ./internal/allowlist/... ./test/security_hooks_test.go -v`

---

## Task 2: Replace Installed Skill Set and Refresh Instill Library References (`agentic_template_start-sgz.3` & `agentic_template_start-sgz.9`)

### Contract
**Given** the current Instill catalog in `ai_support/skills/catalog.csv`,
**When** Forge loads seed skills or runs `instill init`,
**Then** the seed skills list contains:
- `ai-workflow/mpocock/code-rewview` (exact spelling preserved)
- `ai-workflow/mpocock/codebase-design`
- `ai-workflow/mpocock/domain-modeling`
- `ai-workflow/mpocock/grill-me`
- `ai-workflow/mpocock/grill-with-docs`
- `ai-workflow/mpocock/grilling`
- `ai-workflow/mpocock/handoff`
- `ai-workflow/mpocock/implement`
- `ai-workflow/mpocock/research`
- `ai-workflow/mpocock/tdd`
- `ai-workflow/mpocock/to-questionnaire`
- `ai-workflow/mpocock/to-spec`
- `ai-workflow/mpocock/to-tickets`
- `ai-workflow/mpocock/wait-what`
- `ai-workflow/mpocock/wayfinder`
- `ai-workflow/mpocock/wizard`
- `ai-workflow/mpocock/writing-for-agents`
- `ai-workflow/plannotator-compound`
- `ai-workflow/plannotator-setup-goal`
- `ai-workflow/plannotator-visual-explainer`
- `ai-workflow/herdr`
- `Devops/git/git-pushing`
- `productivity/mermaid`
- `productivity/obsidian/obsidian-bases`
- `productivity/obsidian/obsidian-cli`
- `productivity/obsidian/obsidian-markdown`
along with language slices, and obsolete skills are removed.

### Target Files
- `templates/seed/skills.json.tmpl`
- `internal/init/init_test.go`
- `apm.yml`
- `apm.lock.yaml`

### Test Command
`GOCACHE=$PWD/.cache/go-build go test ./internal/init -run TestReadSeedSkills -v`

---

## Task 3: Include `dcg` in Template Load (`agentic_template_start-sgz.4`)

### Contract
**Given** the shipped golden stack overlays,
**When** a repository is scaffolded,
**Then** `"github:Dicklesworthstone/destructive_command_guard" = "latest"` is present in `.forge-overlay/mise.toml` under `[tools]` across all golden stack overlays.

### Target Files
- `templates/golden/go-cli-cobra/.forge-overlay/mise.toml`
- `templates/golden/go-api-chi/.forge-overlay/mise.toml.tmpl`
- `templates/golden/go-web-templ/.forge-overlay/mise.toml`
- `templates/golden/python-cli-typer/.forge-overlay/mise.toml`
- `templates/golden/python-fastapi/.forge-overlay/mise.toml.tmpl`
- `templates/golden/python-web-jinja/.forge-overlay/mise.toml`
- `templates/golden/csharp-cli/.forge-overlay/mise.toml`
- `templates/golden/csharp-webapi/.forge-overlay/mise.toml.tmpl`
- `templates/golden/csharp-blazor/.forge-overlay/mise.toml`
- `templates/golden/vite-ts/.forge-overlay/mise.toml`
- `templates/golden/sveltekit/.forge-overlay/mise.toml`
- `templates/golden/angular/.forge-overlay/mise.toml`
- `test/guideline_conformance_test.go`

### Test Command
`GOCACHE=$PWD/.cache/go-build go test ./test/... -run TestGuideline -v`

---

## Task 4: Remove Empty Tool Directories After Install (`agentic_template_start-sgz.5`)

### Contract
**Given** scaffold output mapping and post-init completion,
**When** `mapOutputPath` processes `"claude"`, `"codex"`, `"opencode"` or final cleanup runs,
**Then** bare unhidden directories `claude`, `codex`, `opencode` are not created if unneeded, and any empty `claude`, `codex`, or `opencode` directory in the target is removed while directories containing files are preserved.

### Target Files
- `internal/scaffold/writer.go`
- `internal/scaffold/writer_test.go`
- `internal/init/init.go`
- `internal/init/init_test.go`

### Test Command
`GOCACHE=$PWD/.cache/go-build go test ./internal/scaffold -run TestMapOutputPath -v && GOCACHE=$PWD/.cache/go-build go test ./internal/init -run TestCleanupEmptyToolDirs -v`

---

## Task 5: Install `peters-bdd-orchestrator` Instill Plugin (`agentic_template_start-sgz.6`)

### Contract
**Given** Phase 2 of `forge init`,
**When** skills and plugins are provisioned,
**Then** `instill pick --type plugin peters-bdd-orchestrator` is executed after `runInstillInit` and before `instill sync`.

### Target Files
- `internal/init/init.go`
- `internal/init/init_test.go`

### Test Command
`GOCACHE=$PWD/.cache/go-build go test ./internal/init -run TestInitializeInstillPlugin -v`

---

## Task 6: Onboard and Verify `agent-fitness-functions` (`agentic_template_start-sgz.2`)

### Contract
**Given** a freshly scaffolded repository after hook chaining,
**When** Phase 2 runs,
**Then** `agent-fitness-functions client onboard --enforcement advisory` is run in advisory mode (logging a warning without failure if the tool is missing).

### Target Files
- `internal/init/init.go`
- `internal/init/init_test.go`

### Test Command
`GOCACHE=$PWD/.cache/go-build go test ./internal/init -run TestInitializeFitnessFunctions -v`

---

## Task 7: Avoid Unnecessary GitHub Username Prompt (`agentic_template_start-sgz.7`)

### Contract
**Given** a configured GitHub username from git config, environment, or flag,
**When** `forge init` runs with `--remote gh`,
**Then** it uses the configured username without prompting; if unconfigured in an interactive TTY, it prompts the user.

### Target Files
- `cmd/forge/main.go`
- `internal/prompt/prompt.go`
- `internal/prompt/prompt_test.go`

### Test Command
`GOCACHE=$PWD/.cache/go-build go test ./internal/prompt -run TestGitHubUserPrompt -v`

---

## Task 8: Fix StackEng GitHub Organization Selection (`agentic_template_start-sgz.8`)

### Contract
**Given** `--remote gh` and organization selection,
**When** the user provides an organization such as `StackEng` via `--github-org`/`--org` or interactive selection,
**Then** `StackEng` is set as `GitHubOrg`, `ModulePath` derives as `github.com/StackEng/<slug>`, and `remote.Publish` runs `gh repo create StackEng/<slug> --source=. --remote=origin --private --push`. When personal is selected, it uses `<gh-user>`.

### Target Files
- `cmd/forge/main.go`
- `internal/prompt/prompt.go`
- `internal/project/project.go`
- `internal/remote/remote.go`
- `internal/init/init.go`
- `internal/prompt/prompt_test.go`
- `internal/remote/remote_test.go`
- `internal/project/project_test.go`
- `docs/SPEC.md`

### Test Command
`GOCACHE=$PWD/.cache/go-build go test ./internal/prompt ./internal/project ./internal/remote -v`

---

*Authored By Peter O'Connor with Assistance from OpenCode (google-vertex/gemini-3.8-flash) · 2026-09-12 · Forge Bug Bash 20260912*

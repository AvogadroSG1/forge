package test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// pythonContainerStacks lists the stacks that ship the Python container image
// pattern (ADR-0026) and the uv-locked dependency set (ADR-0025).
var pythonContainerStacks = []struct {
	stack     string
	server    bool
	pkgPrefix string // directory prefix of the package inside the stack
}{
	{stack: "python-cli-typer", server: false, pkgPrefix: "src/"},
	{stack: "python-fastapi", server: true},
	{stack: "python-web-jinja", server: true},
}

const pythonMiseFile = "mise.toml.tmpl"

var (
	pyrightArg        = regexp.MustCompile(`(?m)^ARG PYRIGHT_VERSION=([0-9]+\.[0-9]+\.[0-9]+)$`)
	misePyrightPin    = regexp.MustCompile(`(?m)^"npm:pyright" = "([^"]+)"$`)
	miseUvExactPin    = regexp.MustCompile(`(?m)^uv = "[0-9]+\.[0-9]+\.[0-9]+"$`)
	pythonRuntimeLine = `ENTRYPOINT ["/app/.venv/bin/app"]`
)

func readPythonStackFile(t *testing.T, stack string, rel ...string) string {
	t.Helper()

	parts := append([]string{repoRoot(t), "templates", "golden", stack}, rel...)
	path := filepath.Join(parts...)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func readPythonOverlay(t *testing.T, stack string, rel ...string) string {
	t.Helper()
	return readPythonStackFile(t, stack, append([]string{".forge-overlay"}, rel...)...)
}

func pythonMiseTool(t *testing.T, mise, key string) string {
	t.Helper()

	m := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + ` = "([^"]+)"$`).FindStringSubmatch(mise)
	if m == nil {
		t.Fatalf("%s does not pin %s", pythonMiseFile, key)
	}
	return m[1]
}

func TestPythonStacksShipPinnedMultiStageDockerfile(t *testing.T) {
	for _, tc := range pythonContainerStacks {
		t.Run(tc.stack, func(t *testing.T) {
			dockerfile := readPythonOverlay(t, tc.stack, "Dockerfile")

			syntax := dockerfileSyntax.FindStringSubmatch(dockerfile)
			if syntax == nil || !pinnedImageRef.MatchString(syntax[1]) {
				t.Fatalf("Dockerfile must start with a tag@digest pinned # syntax= line, got %v", syntax)
			}

			images := map[string]string{}
			for _, match := range dockerfileImage.FindAllStringSubmatch(dockerfile, -1) {
				if !pinnedImageRef.MatchString(match[2]) {
					t.Errorf("%s=%s is not pinned as tag@sha256:<digest>", match[1], match[2])
				}
				images[match[1]] = match[2]
			}
			for _, want := range []string{"PYTHON_IMAGE", "UV_IMAGE", "RUFF_IMAGE", "NODE_IMAGE"} {
				if images[want] == "" {
					t.Errorf("Dockerfile missing pinned ARG %s", want)
				}
			}

			stages := map[string]string{}
			for _, match := range dockerfileFrom.FindAllStringSubmatch(dockerfile, -1) {
				base := strings.Trim(match[1], "${}")
				if images[base] == "" {
					t.Errorf("stage %s uses %s, which is not one of the pinned image ARGs", match[2], match[1])
				}
				stages[match[2]] = base
			}
			if got := len(dockerfileStage.FindAllString(dockerfile, -1)); got != len(stages) {
				t.Errorf("found %d FROM lines but parsed %d named stages; every stage must be `FROM ${X_IMAGE} AS name`", got, len(stages))
			}
			for _, stage := range []string{"sandbox", "build", "runtime"} {
				if stages[stage] != "PYTHON_IMAGE" {
					t.Errorf("stage %s base = %q, want PYTHON_IMAGE", stage, stages[stage])
				}
			}
			if last := strings.LastIndex(dockerfile, "\nFROM "); last < 0 ||
				!strings.HasPrefix(strings.TrimSpace(dockerfile[last:]), "FROM ${PYTHON_IMAGE} AS runtime") ||
				!strings.HasSuffix(strings.TrimSpace(dockerfile[last:]), pythonRuntimeLine) {
				t.Errorf("runtime must be the last (default) stage and end with %s", pythonRuntimeLine)
			}

			for _, forbidden := range []string{"latest", "ADD ", "--platform=linux", "pip install", "COPY --from=ghcr.io", "COPY --from=python", "{{"} {
				if strings.Contains(dockerfile, forbidden) {
					t.Errorf("Dockerfile must not contain %q", forbidden)
				}
			}

			build := stageBody(t, dockerfile, "build")
			for _, want := range []string{
				"uv sync --locked --no-dev --no-install-project",
				"uv sync --locked --no-dev --no-editable",
				"UV_PYTHON_DOWNLOADS=never",
				"UV_COMPILE_BYTECODE=1",
			} {
				if !strings.Contains(build, want) {
					t.Errorf("build stage missing %q", want)
				}
			}
			depsCopy := strings.Index(build, "COPY pyproject.toml uv.lock ./")
			srcCopy := strings.Index(build, "COPY . .")
			if depsCopy < 0 || srcCopy < 0 || depsCopy > srcCopy {
				t.Errorf("build stage must `COPY pyproject.toml uv.lock ./` before `COPY . .` so the dependency layer caches (got %d, %d)", depsCopy, srcCopy)
			}

			for stage, wantUID := range map[string]string{"sandbox": "10001", "runtime": "65532"} {
				user := numericUser.FindStringSubmatch(stageBody(t, dockerfile, stage))
				if user == nil || user[1] != wantUID || user[1] == "0" {
					t.Errorf("stage %s must run as numeric non-root USER %s, got %v", stage, wantUID, user)
				}
			}

			runtime := stageBody(t, dockerfile, "runtime")
			for _, want := range []string{"COPY --from=build /app/.venv /app/.venv", "PYTHONDONTWRITEBYTECODE=1", "PYTHONUNBUFFERED=1"} {
				if !strings.Contains(runtime, want) {
					t.Errorf("runtime stage missing %q", want)
				}
			}
			for _, forbidden := range []string{"HEALTHCHECK", "uv ", "COPY --from=uv"} {
				if strings.Contains(runtime, forbidden) {
					t.Errorf("runtime stage must not contain %q", forbidden)
				}
			}
			for _, want := range []string{"HOST=0.0.0.0", "PORT=8080", "EXPOSE 8080"} {
				if has := strings.Contains(runtime, want); has != tc.server {
					t.Errorf("runtime stage contains %q = %v, want %v (server=%v)", want, has, tc.server, tc.server)
				}
			}

			sandbox := stageBody(t, dockerfile, "sandbox")
			for _, want := range []string{"WORKDIR /workspace", "UV_PYTHON_DOWNLOADS=never", "COPY --from=uv ", "COPY --from=ruff "} {
				if !strings.Contains(sandbox, want) {
					t.Errorf("sandbox stage missing %q", want)
				}
			}
			if !pyrightArg.MatchString(dockerfile) {
				t.Errorf("Dockerfile must declare ARG PYRIGHT_VERSION=<exact x.y.z>")
			}
		})
	}
}

// TestPythonStackDockerfileToolVersionsMatchMise keeps the image toolchain and
// the local toolchain from drifting apart.
func TestPythonStackDockerfileToolVersionsMatchMise(t *testing.T) {
	for _, tc := range pythonContainerStacks {
		t.Run(tc.stack, func(t *testing.T) {
			dockerfile := readPythonOverlay(t, tc.stack, "Dockerfile")
			mise := readPythonOverlay(t, tc.stack, pythonMiseFile)

			for _, c := range []struct{ tool, arg, format string }{
				{"python", "PYTHON_IMAGE", "python:%s-slim"},
				{"uv", "UV_IMAGE", "uv:%s@"},
				{"ruff", "RUFF_IMAGE", "ruff:%s@"},
			} {
				version := pythonMiseTool(t, mise, c.tool)
				want := strings.Replace(c.format, "%s", version, 1)
				line := regexp.MustCompile(`(?m)^ARG ` + c.arg + `=(\S+)$`).FindStringSubmatch(dockerfile)
				if line == nil || !strings.Contains(line[1], want) {
					t.Errorf("ARG %s must contain %q to match %s %s in %s, got %v", c.arg, want, c.tool, version, pythonMiseFile, line)
				}
			}

			pyright := misePyrightPin.FindStringSubmatch(mise)
			arg := pyrightArg.FindStringSubmatch(dockerfile)
			if pyright == nil {
				t.Errorf("%s does not pin \"npm:pyright\"", pythonMiseFile)
			} else if arg == nil || arg[1] != pyright[1] {
				t.Errorf("ARG PYRIGHT_VERSION must equal mise npm:pyright %s, got %v", pyright[1], arg)
			}

			if node := regexp.MustCompile(`(?m)^node = "([^"]+)"$`).FindStringSubmatch(mise); node != nil {
				line := regexp.MustCompile(`(?m)^ARG NODE_IMAGE=(\S+)$`).FindStringSubmatch(dockerfile)
				if line == nil || !strings.Contains(line[1], "node:"+node[1]+"-") {
					t.Errorf("ARG NODE_IMAGE must contain %q to match mise node %s, got %v", "node:"+node[1]+"-", node[1], line)
				}
			}
		})
	}
}

func TestPythonStacksShipAllowlistDockerignore(t *testing.T) {
	for _, tc := range pythonContainerStacks {
		t.Run(tc.stack, func(t *testing.T) {
			ignore := readPythonOverlay(t, tc.stack, ".dockerignore")

			var rules []string
			lastInclude := -1
			for _, line := range strings.Split(ignore, "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				rules = append(rules, line)
				if strings.HasPrefix(line, "!") {
					lastInclude = len(rules) - 1
				}
			}
			if len(rules) == 0 || rules[0] != "*" {
				t.Fatalf(".dockerignore must start by excluding everything, got rules %v", rules)
			}

			has := func(rule string) bool {
				for _, r := range rules {
					if r == rule {
						return true
					}
				}
				return false
			}
			for _, want := range []string{"!pyproject.toml", "!uv.lock", "!README.md", "!**/*.py"} {
				if !has(want) {
					t.Errorf(".dockerignore missing %q", want)
				}
			}

			excludes := []string{".venv/", "tests/"}
			if tc.stack == "python-fastapi" {
				excludes = append(excludes, "web/")
			}
			for _, want := range excludes {
				last := -1
				for i, r := range rules {
					if r == want {
						last = i
					}
				}
				if last < 0 {
					t.Errorf(".dockerignore missing exclusion %q", want)
				} else if last < lastInclude {
					t.Errorf(".dockerignore exclusion %q must come after the last `!` re-include (re-includes would override it)", want)
				}
			}

			for _, extra := range []string{"!**/templates/**", "!**/static/**"} {
				if got, want := has(extra), tc.stack == "python-web-jinja"; got != want {
					t.Errorf(".dockerignore contains %q = %v, want %v", extra, got, want)
				}
			}
		})
	}
}

// TestPythonStacksDocumentTheContainerPattern keeps CONTAINER.md honest about
// the targets and tasks the stack actually ships.
func TestPythonStacksDocumentTheContainerPattern(t *testing.T) {
	factors := []string{
		"Codebase", "Dependencies", "Config", "Backing services", "Build, release, run", "Processes",
		"Port binding", "Concurrency", "Disposability", "Dev/prod parity", "Logs", "Admin processes",
	}

	for _, tc := range pythonContainerStacks {
		t.Run(tc.stack, func(t *testing.T) {
			doc := readPythonOverlay(t, tc.stack, "CONTAINER.md")

			wants := []string{"`runtime`", "`sandbox`", "mise run image:lint", "mise run image`", "mise run image:scan", "imagetools inspect", "uv.lock", "uv lock"}
			if tc.server {
				wants = append(wants, "HOST", "PORT")
			}
			for _, want := range wants {
				if !strings.Contains(doc, want) {
					t.Errorf("CONTAINER.md missing %q", want)
				}
			}
			for _, factor := range factors {
				if !strings.Contains(doc, "| "+factor+" |") {
					t.Errorf("CONTAINER.md twelve-factor table missing %q", factor)
				}
			}
		})
	}
}

// TestPythonStacksLockDependenciesWithUv pins the ADR-0025 contract.
func TestPythonStacksLockDependenciesWithUv(t *testing.T) {
	for _, tc := range pythonContainerStacks {
		t.Run(tc.stack, func(t *testing.T) {
			pyproject := readPythonStackFile(t, tc.stack, "pyproject.toml.tmpl")
			if !strings.Contains(pyproject, "[dependency-groups]") {
				t.Errorf("pyproject.toml.tmpl must declare dev dependencies under [dependency-groups]")
			}
			if strings.Contains(pyproject, "[project.optional-dependencies]") {
				t.Errorf("pyproject.toml.tmpl must not use [project.optional-dependencies]")
			}

			scripts := strings.Index(pyproject, "[project.scripts]")
			if scripts < 0 {
				t.Fatalf("pyproject.toml.tmpl must declare [project.scripts]")
			}
			table := pyproject[scripts+len("[project.scripts]"):]
			if next := strings.Index(table, "\n["); next >= 0 {
				table = table[:next]
			}
			wantApp := `app = "{{.PythonPackage}}.serve:main"`
			if !tc.server {
				wantApp = `app = "{{.PythonPackage}}.main:main"`
			}
			foundApp := false
			for _, line := range strings.Split(table, "\n") {
				if strings.HasPrefix(line, `app = "{{.PythonPackage}}.`) {
					foundApp = true
				}
			}
			if !foundApp || !strings.Contains(table, wantApp) {
				t.Errorf("[project.scripts] must contain %s, got:\n%s", wantApp, table)
			}
			if !tc.server && !strings.Contains(pyproject, `{{- if ne .PythonPackage "app" }}`) {
				t.Errorf("CLI pyproject must guard its package-named script with `{{- if ne .PythonPackage \"app\" }}`")
			}

			mise := readPythonOverlay(t, tc.stack, pythonMiseFile)
			if !miseUvExactPin.MatchString(mise) {
				t.Errorf("%s must pin uv to an exact x.y.z version", pythonMiseFile)
			}
			for _, want := range []string{"uv run pytest", "uv run pyright", "uv sync --locked", "pip-audit", "uv export"} {
				if !strings.Contains(mise, want) {
					t.Errorf("%s missing %q", pythonMiseFile, want)
				}
			}
			if strings.Contains(mise, "pip install") {
				t.Errorf("%s must not contain `pip install`", pythonMiseFile)
			}
		})
	}
}

// TestPythonServerStacksReadHostPortFromEnvironmentAndLogJSON pins the two
// code-level twelve-factor properties of the server stacks (config and logs).
func TestPythonServerStacksReadHostPortFromEnvironmentAndLogJSON(t *testing.T) {
	for _, tc := range pythonContainerStacks {
		if !tc.server {
			continue
		}
		t.Run(tc.stack, func(t *testing.T) {
			servePath := filepath.Join(".forge-overlay", "{{.PythonPackage}}", "serve.py")
			serve := readPythonStackFile(t, tc.stack, servePath)

			for _, want := range []string{`"PORT"`, `"HOST"`, `"8000"`, `"127.0.0.1"`, "uvicorn.run(", "log_config", "json.dumps", "from .main import app"} {
				if !strings.Contains(serve, want) {
					t.Errorf("%s missing %q", servePath, want)
				}
			}
			for _, forbidden := range []string{"0.0.0.0", "{{"} {
				if strings.Contains(serve, forbidden) {
					t.Errorf("%s must not contain %q", servePath, forbidden)
				}
			}

			serveTest := readPythonOverlay(t, tc.stack, "tests", "test_serve.py.tmpl")
			if strings.TrimSpace(serveTest) == "" {
				t.Errorf("tests/test_serve.py.tmpl must not be empty")
			}

			mise := readPythonOverlay(t, tc.stack, pythonMiseFile)
			start := strings.Index(mise, "[tasks.serve]")
			if start < 0 {
				t.Fatalf("%s has no [tasks.serve]", pythonMiseFile)
			}
			section := mise[start+len("[tasks.serve]"):]
			if next := strings.Index(section, "\n["); next >= 0 {
				section = section[:next]
			}
			var run string
			for _, line := range strings.Split(section, "\n") {
				if strings.HasPrefix(line, "run = ") {
					run = line
				}
			}
			if !strings.HasSuffix(run, `opentelemetry-instrument app"`) {
				t.Errorf("serve task run line must end with `opentelemetry-instrument app\"`, got %q", run)
			}
			if strings.Contains(section, "--port 8000") {
				t.Errorf("serve task must not pass --port 8000; the app reads PORT")
			}
		})
	}
}

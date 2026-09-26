package test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"forge"
	"forge/internal/catalog"
	"forge/internal/project"
	"forge/internal/scaffold"
)

// Angular cannot read process env in the browser, so the scaffold bridges
// OTEL_EXPORTER_OTLP_ENDPOINT into the bundle at build time via
// `ng build|serve --define FORGE_OTLP_ENDPOINT="<endpoint>"`, and
// environment.ts reads it through a `typeof` guard so plain `ng build`
// (outside mise, no define) still compiles and falls back to ''.

// otelAngularBackends are the api stacks that can host an Angular fragment
// under web/. Each repeats the Angular web-dev/web-build block in its own
// mise.toml.tmpl because text/template has no cross-file partials.
var otelAngularBackends = []string{"go-api-chi", "csharp-webapi", "python-fastapi"}

// otelAngularSentinel is a collector base URL that never occurs in an
// Angular bundle by accident. The OTel exporter always ships its own
// `http://localhost:4318/` default, so the real default cannot be grepped.
const otelAngularSentinel = "http://forge-sentinel.invalid:4318"

// otelAngularEndpointSource matches a shell expansion of the collector
// endpoint (`$OTEL_EXPORTER_OTLP_ENDPOINT` or `${OTEL_EXPORTER_OTLP_ENDPOINT…}`).
var otelAngularEndpointSource = regexp.MustCompile(`\$\{?OTEL_EXPORTER_OTLP_ENDPOINT\b`)

// otelAngularTaskSpec names one Angular task and the npm invocation its body
// MUST contain. The structural check is deliberately loose; the exact argv
// shape is pinned by the stub-npm behavioral tests.
type otelAngularTaskSpec struct {
	table string
	npm   *regexp.Regexp
}

var (
	otelAngularStandaloneNpm = regexp.MustCompile(`(?m)^\s*(exec\s+)?npm\s+run\b`)
	otelAngularFullstackNpm  = regexp.MustCompile(`(?m)^\s*(exec\s+)?npm\s+--prefix\s+web\s+run\b`)

	otelAngularStandaloneTasks = []otelAngularTaskSpec{
		{table: "tasks.dev", npm: otelAngularStandaloneNpm},
		{table: "tasks.build", npm: otelAngularStandaloneNpm},
	}
	otelAngularFullstackTasks = []otelAngularTaskSpec{
		{table: "tasks.web-dev", npm: otelAngularFullstackNpm},
		{table: "tasks.web-build", npm: otelAngularFullstackNpm},
	}
)

// otelAngularStubTarget is one Angular scaffold shape the stub-npm
// behavioral tests drive: which mise tasks build/serve it, and whether npm
// must be pointed at web/.
type otelAngularStubTarget struct {
	name      string
	input     func(t *testing.T) project.Input
	buildTask string
	devTask   string
	webPrefix bool
}

// otelAngularStubTargets covers standalone and one fullstack backend; the
// other two backends ship a byte-identical block (TestOtelAngularBackendTaskBlocksIdentical).
var otelAngularStubTargets = []otelAngularStubTarget{
	{
		name:      "standalone",
		input:     func(*testing.T) project.Input { return otelAngularStandaloneInput() },
		buildTask: "build",
		devTask:   "dev",
	},
	{
		name:      "go-api-chi",
		input:     func(t *testing.T) project.Input { return otelAngularFullstackInput(t, "go-api-chi", "angular") },
		buildTask: "web-build",
		devTask:   "web-dev",
		webPrefix: true,
	},
}

// S1: every Angular scaffold (standalone and each fullstack backend) ships
// mise tasks that inline OTEL_EXPORTER_OTLP_ENDPOINT via --define.
func TestOtelAngularTasksBridgeEndpointToDefine(t *testing.T) {
	t.Run("standalone", func(t *testing.T) {
		repoDir := scaffoldOtelAngularRepo(t, t.TempDir(), otelAngularStandaloneInput())
		assertOtelAngularDefineTasks(t, repoDir, otelAngularStandaloneTasks)
	})
	for _, backend := range otelAngularBackends {
		t.Run(backend, func(t *testing.T) {
			repoDir := scaffoldOtelAngularRepo(t, t.TempDir(), otelAngularFullstackInput(t, backend, "angular"))
			assertOtelAngularDefineTasks(t, repoDir, otelAngularFullstackTasks)
		})
	}
}

// S2: environment.ts reads the define through a typeof guard and no longer
// claims fileReplacements is the override mechanism.
func TestOtelAngularEnvironmentReadsDefineWithTypeofGuard(t *testing.T) {
	t.Run("standalone", func(t *testing.T) {
		repoDir := scaffoldOtelAngularRepo(t, t.TempDir(), otelAngularStandaloneInput())
		assertOtelAngularEnvironmentGuard(t, repoDir)
	})
	for _, backend := range otelAngularBackends {
		t.Run(backend, func(t *testing.T) {
			repoDir := scaffoldOtelAngularRepo(t, t.TempDir(), otelAngularFullstackInput(t, backend, "angular"))
			assertOtelAngularEnvironmentGuard(t, filepath.Join(repoDir, "web"))
		})
	}
}

// S3: the Angular block is copied into three backend templates; the copies
// MUST stay byte-identical so a fix to one is a fix to all.
func TestOtelAngularBackendTaskBlocksIdentical(t *testing.T) {
	blocks := map[string]string{}
	for _, backend := range otelAngularBackends {
		path := "templates/golden/" + backend + "/.forge-overlay/mise.toml.tmpl"
		data, err := fs.ReadFile(forge.Assets(), path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		block, err := extractOtelAngularTemplateBlock(string(data))
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		for _, table := range []string{"[tasks.web-dev]", "[tasks.web-build]"} {
			if !strings.Contains(block, table) {
				t.Errorf("%s: Angular block missing %s:\n%s", path, table, block)
			}
		}
		blocks[backend] = block
	}
	if len(blocks) != len(otelAngularBackends) {
		return
	}

	reference := otelAngularBackends[0]
	for _, backend := range otelAngularBackends[1:] {
		if blocks[backend] != blocks[reference] {
			t.Errorf("Angular block in %s differs from %s:\n--- %s ---\n%s\n--- %s ---\n%s",
				backend, reference, reference, blocks[reference], backend, blocks[backend])
		}
	}
}

// S4: non-Angular fragments keep their Vite env path; the Angular-only
// define tasks MUST NOT leak into their mise.toml. This is a regression
// guard and is expected to pass before and after the feature lands.
func TestOtelAngularTasksAbsentForOtherFrontends(t *testing.T) {
	for _, backend := range otelAngularBackends {
		for _, frontend := range catalog.FrontendStacks() {
			if frontend.Key == "angular" {
				continue
			}
			t.Run(backend+"/"+frontend.Key, func(t *testing.T) {
				repoDir := scaffoldOtelAngularRepo(t, t.TempDir(), otelAngularFullstackInput(t, backend, frontend.Key))
				misePath := filepath.Join(repoDir, "mise.toml")
				data, err := os.ReadFile(misePath)
				if err != nil {
					t.Fatalf("read %s: %v", misePath, err)
				}
				bodies, err := parseTomlRunBodies(string(data))
				if err != nil {
					t.Fatalf("parse %s: %v", misePath, err)
				}
				for _, spec := range otelAngularFullstackTasks {
					if _, ok := bodies[spec.table]; ok {
						t.Errorf("%s: [%s] present for frontend %q; the Angular define tasks MUST only ship with --frontend angular", misePath, spec.table, frontend.Key)
					}
				}
				if strings.Contains(string(data), "FORGE_OTLP_ENDPOINT") {
					t.Errorf("%s mentions FORGE_OTLP_ENDPOINT for frontend %q:\n%s", misePath, frontend.Key, data)
				}
			})
		}
	}
}

// Behavioral: hostile endpoint values are rejected by the task body before
// npm runs. npm is a recording stub, so no network or node is needed; mise
// tolerates the uninstalled [tools] entries (it warns and runs the task).
func TestOtelAngularTasksRejectUnsafeEndpoint(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{name: "double-quote", value: `http://collector.example:4318/"x`},
		{name: "backslash", value: `http://collector.example:4318/\x`},
		{name: "tab", value: "http://collector.example:4318/\tx"},
		{name: "newline", value: "http://collector.example:4318/\nx"},
		{name: "carriage-return", value: "http://collector.example:4318/\rx"},
		{name: "soh-0x01", value: "http://collector.example:4318/\x01x"},
		{name: "del-0x7f", value: "http://collector.example:4318/\x7fx"},
	}
	for _, target := range otelAngularStubTargets {
		t.Run(target.name, func(t *testing.T) {
			h := newOtelAngularStubHarness(t, target.input(t))
			for _, tc := range cases {
				for _, task := range []string{target.buildTask, target.devTask} {
					t.Run(tc.name+"/"+task, func(t *testing.T) {
						h.resetRecord(t)
						h.writeMiseLocalEndpoint(t, tomlBasicString(tc.value))

						result := h.run(t, task)

						if result.exitCode != 1 {
							t.Errorf("mise run %s with endpoint %q: exit = %d, want 1\n%s", task, tc.value, result.exitCode, result.output)
						}
						if !otelAngularNamesEndpointOutsideEcho(result.output) {
							t.Errorf("mise run %s with endpoint %q: no output line (other than mise's own `[task] $ …` command echo) names OTEL_EXPORTER_OTLP_ENDPOINT; the rejection MUST say which variable is bad\n%s", task, tc.value, result.output)
						}
						for _, argv := range h.stubInvocations(t) {
							if argv[0] == "npm" {
								t.Errorf("mise run %s with endpoint %q invoked npm %q; the value MUST be rejected before npm runs", task, tc.value, argv[1:])
							}
						}
					})
				}
			}
		})
	}
}

// miseCommandEcho matches the line mise prints before running each task
// command (e.g. `[build] $ endpoint="${OTEL_EXPORTER_OTLP_ENDPOINT:-}"`).
// Those echo the task source, so they cannot count as a rejection message.
var miseCommandEcho = regexp.MustCompile(`^\[[^\]]+\] \$ `)

// otelAngularNamesEndpointOutsideEcho is the minimal contract for the
// rejection text: some output line names the variable, and it is not mise
// echoing the task body. Wording is left to the implementation. Mise's own
// `no task <name> found` error never names the variable.
func otelAngularNamesEndpointOutsideEcho(output string) bool {
	for line := range strings.SplitSeq(output, "\n") {
		if strings.Contains(line, "OTEL_EXPORTER_OTLP_ENDPOINT") && !miseCommandEcho.MatchString(line) {
			return true
		}
	}
	return false
}

// Behavioral: safe values reach npm as exactly one argv entry
// `FORGE_OTLP_ENDPOINT="<value>"` following `--define`, with shell
// metacharacters inert.
func TestOtelAngularTasksPassEndpointAsDefine(t *testing.T) {
	const injectionValue = "http://collector.example:4318/a b $(touch INJECTED) `touch INJECTED`"
	cases := []struct {
		name string
		// tomlValue is written verbatim as the mise.local.toml value; empty
		// means no mise.local.toml, so conf.d/otel.toml's default applies.
		tomlValue string
		want      string
	}{
		{name: "conf.d-default", tomlValue: "", want: `FORGE_OTLP_ENDPOINT="http://localhost:4318"`},
		{name: "spaces-and-substitution", tomlValue: tomlBasicString(injectionValue), want: `FORGE_OTLP_ENDPOINT="` + injectionValue + `"`},
		{name: "empty", tomlValue: `""`, want: `FORGE_OTLP_ENDPOINT=""`},
		// `false` makes mise unset the variable (verified on mise 2026.9.12).
		{name: "unset", tomlValue: "false", want: `FORGE_OTLP_ENDPOINT=""`},
	}
	for _, target := range otelAngularStubTargets {
		t.Run(target.name, func(t *testing.T) {
			h := newOtelAngularStubHarness(t, target.input(t))
			wantScripts := map[string][]string{target.buildTask: {"build"}, target.devTask: {"start", "serve"}}
			for _, tc := range cases {
				for _, task := range []string{target.buildTask, target.devTask} {
					t.Run(tc.name+"/"+task, func(t *testing.T) {
						h.resetRecord(t)
						if tc.tomlValue == "" {
							h.removeMiseLocal(t)
						} else {
							h.writeMiseLocalEndpoint(t, tc.tomlValue)
						}

						result := h.run(t, task)
						if result.exitCode != 0 {
							t.Fatalf("mise run %s exit = %d, want 0\n%s", task, result.exitCode, result.output)
						}

						var npmCalls [][]string
						for _, argv := range h.stubInvocations(t) {
							if argv[0] == "npm" {
								npmCalls = append(npmCalls, argv[1:])
							}
						}
						if len(npmCalls) != 1 {
							t.Fatalf("mise run %s: npm invocations = %q, want exactly one\n%s", task, npmCalls, result.output)
						}
						argv := npmCalls[0]
						idx := slices.Index(argv, "--define")
						if idx < 0 || idx+1 >= len(argv) {
							t.Fatalf("mise run %s: npm argv %q has no `--define <value>`", task, argv)
						}
						if got := argv[idx+1]; got != tc.want {
							t.Errorf("mise run %s: --define value = %q, want %q (npm argv %q)", task, got, tc.want, argv)
						}
						if !slices.ContainsFunc(argv[:idx], func(a string) bool { return slices.Contains(wantScripts[task], a) }) {
							t.Errorf("mise run %s: npm argv %q does not run any of %q before --define", task, argv, wantScripts[task])
						}
						prefix := slices.Index(argv, "--prefix")
						switch {
						case target.webPrefix && (prefix < 0 || prefix+1 >= len(argv) || argv[prefix+1] != "web" || prefix > idx):
							t.Errorf("mise run %s: npm argv %q lacks `--prefix web` before --define; the fullstack app lives under web/", task, argv)
						case !target.webPrefix && prefix >= 0:
							t.Errorf("mise run %s: npm argv %q uses --prefix; the standalone app lives at the repo root", task, argv)
						}

						assertNoInjectedFile(t, h.root)
					})
				}
			}
		})
	}
}

// H1+H2: a real `ng build` inlines the endpoint under mise, and a plain
// `npx ng build` outside mise ignores the shell env (no define) while
// still compiling through the typeof guard.
func TestOtelAngularStandaloneBundleInlinesEndpoint(t *testing.T) {
	requireOtelAngularNetworkSmoke(t)
	h := newOtelAngularRealHarness(t, otelAngularStandaloneInput())
	distDir := filepath.Join(h.repoDir, "dist")

	runNpmInstall(t, h.repoDir)

	t.Run("mise-run-build-inlines-sentinel", func(t *testing.T) {
		if err := os.RemoveAll(distDir); err != nil {
			t.Fatalf("remove %s: %v", distDir, err)
		}
		h.writeMiseLocalEndpoint(t, tomlBasicString(otelAngularSentinel))

		result := h.run(t, "build")
		if result.exitCode != 0 {
			t.Fatalf("mise run build exit = %d, want 0\n%s", result.exitCode, result.output)
		}
		if hits := jsFilesContaining(t, distDir, `"`+otelAngularSentinel+`"`); len(hits) == 0 {
			t.Errorf("no dist/**/*.js contains the quoted sentinel %q; mise run build did not inline OTEL_EXPORTER_OTLP_ENDPOINT\n%s", otelAngularSentinel, result.output)
		}
	})

	t.Run("plain-ng-build-ignores-shell-env", func(t *testing.T) {
		if err := os.RemoveAll(distDir); err != nil {
			t.Fatalf("remove %s: %v", distDir, err)
		}
		h.removeMiseLocal(t)

		// Run with the developer's own env (real PATH, real npm cache) but
		// never through mise, so no conf.d env and no --define apply.
		env := setEnvValue(os.Environ(), "OTEL_EXPORTER_OTLP_ENDPOINT", otelAngularSentinel)
		env = setEnvValue(env, "NG_CLI_ANALYTICS", "false")
		output, err := runCommandWithin(h.repoDir, env, otelAngularBuildTimeout, "npx", "ng", "build")
		if err != nil {
			t.Fatalf("npx ng build: %v\n%s", err, output)
		}
		if hits := jsFilesContaining(t, distDir, "forge-sentinel.invalid"); len(hits) > 0 {
			t.Errorf("plain ng build inlined the shell env endpoint into %v; only the mise task may bridge it", hits)
		}
		// Without a define the guard survives minification verbatim, which
		// proves environment.ts reads FORGE_OTLP_ENDPOINT and not the env.
		if hits := jsFilesContaining(t, distDir, "FORGE_OTLP_ENDPOINT"); len(hits) == 0 {
			t.Errorf("no dist/**/*.js references FORGE_OTLP_ENDPOINT; environment.ts does not read the define through a typeof guard\n%s", output)
		}
	})
}

// H3: the fullstack web-build task inlines the endpoint into web/dist.
func TestOtelAngularFullstackBundleInlinesEndpoint(t *testing.T) {
	requireOtelAngularNetworkSmoke(t)
	h := newOtelAngularRealHarness(t, otelAngularFullstackInput(t, "go-api-chi", "angular"))
	webDir := filepath.Join(h.repoDir, "web")

	runNpmInstall(t, webDir)
	h.writeMiseLocalEndpoint(t, tomlBasicString(otelAngularSentinel))

	result := h.run(t, "web-build")
	if result.exitCode != 0 {
		t.Fatalf("mise run web-build exit = %d, want 0\n%s", result.exitCode, result.output)
	}
	if hits := jsFilesContaining(t, filepath.Join(webDir, "dist"), `"`+otelAngularSentinel+`"`); len(hits) == 0 {
		t.Errorf("no web/dist/**/*.js contains the quoted sentinel %q; mise run web-build did not inline OTEL_EXPORTER_OTLP_ENDPOINT\n%s", otelAngularSentinel, result.output)
	}
}

// assertOtelAngularBridge is the Angular-specific counterpart to the Vite
// env check in assertOtelCollectorAssets: tasks inline the endpoint and
// environment.ts reads it.
func assertOtelAngularBridge(t *testing.T, repoDir string, vars project.Variables) {
	t.Helper()

	var appDir string
	var specs []otelAngularTaskSpec
	switch {
	case vars.Stack == "angular":
		appDir, specs = repoDir, otelAngularStandaloneTasks
	case vars.Frontend == "angular":
		appDir, specs = filepath.Join(repoDir, "web"), otelAngularFullstackTasks
	default:
		return
	}
	// Presence is keyed on the requested stack, not on files, so a scaffold
	// that silently drops the Angular app cannot skip these checks.
	if manifest := filepath.Join(appDir, "angular.json"); !fileExists(manifest) {
		t.Fatalf("stack %q frontend %q: %s not found; the Angular app was not scaffolded where expected", vars.Stack, vars.Frontend, manifest)
	}
	assertOtelAngularDefineTasks(t, repoDir, specs)
	assertOtelAngularEnvironmentGuard(t, appDir)
}

func assertOtelAngularDefineTasks(t *testing.T, repoDir string, specs []otelAngularTaskSpec) {
	t.Helper()

	misePath := filepath.Join(repoDir, "mise.toml")
	data, err := os.ReadFile(misePath)
	if err != nil {
		t.Fatalf("read %s: %v", misePath, err)
	}
	bodies, err := parseTomlRunBodies(string(data))
	if err != nil {
		t.Fatalf("parse %s: %v", misePath, err)
	}

	for _, spec := range specs {
		body, ok := bodies[spec.table]
		if !ok {
			t.Errorf("%s: no [%s] task; Angular needs it to inline OTEL_EXPORTER_OTLP_ENDPOINT via --define (found %v)", misePath, spec.table, mapKeys(bodies))
			continue
		}
		if !strings.HasPrefix(strings.TrimLeft(body, " \t\n"), "set -eu") {
			t.Errorf("%s: [%s] run body does not start with `set -eu`:\n%s", misePath, spec.table, body)
		}
		if m := teraTemplateOpener.FindString(body); m != "" {
			t.Errorf("%s: [%s] run body contains Tera template opener %q:\n%s", misePath, spec.table, m, body)
		}

		for _, snippet := range []string{"--define", "FORGE_OTLP_ENDPOINT"} {
			if !strings.Contains(body, snippet) {
				t.Errorf("%s: [%s] run body lacks %q; it MUST inline the endpoint via `ng --define FORGE_OTLP_ENDPOINT=…`:\n%s", misePath, spec.table, snippet, body)
			}
		}
		if !otelAngularEndpointSource.MatchString(body) {
			t.Errorf("%s: [%s] run body never expands $OTEL_EXPORTER_OTLP_ENDPOINT:\n%s", misePath, spec.table, body)
		}
		if !spec.npm.MatchString(body) {
			t.Errorf("%s: [%s] run body has no npm invocation matching %s:\n%s", misePath, spec.table, spec.npm, body)
		}
	}
}

func assertOtelAngularEnvironmentGuard(t *testing.T, appDir string) {
	t.Helper()

	path := filepath.Join(appDir, "src", "environments", "environment.ts")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	content := string(data)
	for _, snippet := range []string{
		"declare const FORGE_OTLP_ENDPOINT: string | undefined;",
		"typeof FORGE_OTLP_ENDPOINT === 'string' ? FORGE_OTLP_ENDPOINT : ''",
	} {
		if !strings.Contains(content, snippet) {
			t.Errorf("%s missing %q:\n%s", path, snippet, content)
		}
	}
	if strings.Contains(content, "fileReplacements") {
		t.Errorf("%s still mentions fileReplacements, which the scaffold does not configure:\n%s", path, content)
	}
}

var otelAngularTemplateOpener = regexp.MustCompile(`\{\{-?\s*if\s+eq\s+\.Frontend\s+"angular"\s*-?\}\}`)

// extractOtelAngularTemplateBlock returns the text between
// `{{- if eq .Frontend "angular" }}` and its `{{- end }}`. The block MUST be
// plain TOML (no nested actions), so the first following action closes it.
func extractOtelAngularTemplateBlock(src string) (string, error) {
	loc := otelAngularTemplateOpener.FindStringIndex(src)
	if loc == nil {
		return "", errors.New(`no {{- if eq .Frontend "angular" }} block`)
	}
	rest := src[loc[1]:]
	next := strings.Index(rest, "{{")
	if next < 0 {
		return "", errors.New(`unterminated {{- if eq .Frontend "angular" }} block`)
	}
	if !regexp.MustCompile(`^\{\{-?\s*end\s*-?\}\}`).MatchString(rest[next:]) {
		return "", fmt.Errorf("Angular block contains a nested template action at %q; it MUST be plain TOML", rest[next:min(len(rest), next+40)])
	}
	return rest[:next], nil
}

func otelAngularStandaloneInput() project.Input {
	return project.Input{
		ProjectName: "Otel Angular",
		Language:    "typescript",
		ProjectType: "frontend",
		Stack:       "angular",
		AuthorName:  "CI Bot",
		AuthorEmail: "ci@example.com",
		Remote:      project.RemoteNone,
	}
}

func otelAngularFullstackInput(t *testing.T, backend, frontend string) project.Input {
	t.Helper()

	stack, ok := catalog.Get(backend)
	if !ok {
		t.Fatalf("catalog has no stack %q", backend)
	}
	return project.Input{
		ProjectName: "Otel Angular " + backend,
		Language:    stack.Language,
		ProjectType: "fullstack",
		Stack:       backend,
		Frontend:    frontend,
		AuthorName:  "CI Bot",
		AuthorEmail: "ci@example.com",
		Remote:      project.RemoteNone,
	}
}

func scaffoldOtelAngularRepo(t *testing.T, repoDir string, input project.Input) string {
	t.Helper()

	vars, err := project.ResolveVariables(input)
	if err != nil {
		t.Fatalf("ResolveVariables() error = %v", err)
	}
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", repoDir, err)
	}
	if err := (scaffold.Writer{Assets: forge.Assets()}).Write(repoDir, vars); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	return repoDir
}

// otelAngularHarness wraps the shared otel task harness with a scaffolded
// Angular repo and mise.local.toml control.
type otelAngularHarness struct {
	*otelTaskHarness
}

// newOtelAngularBaseHarness scaffolds input into <root>/repo and builds the
// same isolated HOME/XDG/mise env as newOtelRepoHarness (which is hard-wired
// to go-cli-cobra), minus PATH. mise is exposed through a private bin dir so
// PATH never has to include mise's install dir (e.g. /opt/homebrew/bin),
// which would also leak the developer's real npm into the stub tests.
func newOtelAngularBaseHarness(t *testing.T, input project.Input) (*otelAngularHarness, string) {
	t.Helper()

	misePath := resolveCommandPath("mise")
	if _, err := os.Stat(misePath); err != nil {
		t.Skip("mise not installed; skipping Angular otel task execution test")
	}

	root := t.TempDir()
	repoDir := scaffoldOtelAngularRepo(t, filepath.Join(root, "repo"), input)

	home := filepath.Join(root, "home")
	dataHome := filepath.Join(home, ".local", "share")
	xdg := map[string]string{
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"XDG_DATA_HOME":   dataHome,
		"XDG_STATE_HOME":  filepath.Join(home, ".local", "state"),
		"XDG_CACHE_HOME":  filepath.Join(home, ".cache"),
	}
	for _, dir := range xdg {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	miseBin := filepath.Join(root, "mise-bin")
	if err := os.MkdirAll(miseBin, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", miseBin, err)
	}
	if err := os.Symlink(misePath, filepath.Join(miseBin, "mise")); err != nil {
		t.Fatalf("symlink mise: %v", err)
	}

	env := []string{
		"HOME=" + home,
		"MISE_CEILING_PATHS=" + root,
		"MISE_TRUSTED_CONFIG_PATHS=" + repoDir,
		"MISE_YES=1",
		// The [tools] (node 24, lefthook, dcg, backend toolchains) are never
		// installed in this isolated data dir. With auto-install and network
		// off, mise warns and still runs the task with PATH as given.
		"MISE_AUTO_INSTALL=0",
		"MISE_TASK_RUN_AUTO_INSTALL=0",
		"MISE_NOT_FOUND_AUTO_INSTALL=0",
		"MISE_OFFLINE=1",
		"NG_CLI_ANALYTICS=false",
	}
	for key, value := range xdg {
		env = append(env, key+"="+value)
	}
	if dash, err := exec.LookPath("dash"); err == nil {
		// Task bodies MUST be POSIX; run them under dash like Debian CI.
		env = append(env, "MISE_UNIX_DEFAULT_INLINE_SHELL_ARGS="+dash+" -c")
	}

	return &otelAngularHarness{&otelTaskHarness{
		root:       root,
		repoDir:    repoDir,
		dataHome:   dataHome,
		recordPath: filepath.Join(root, "stub-argv.log"),
		env:        env,
		mise:       misePath,
		timeout:    otelTaskTimeout,
	}}, miseBin
}

// newOtelAngularStubHarness scaffolds input (standalone or fullstack) with a
// recording `npm` stub first on PATH and no real node/npm reachable.
func newOtelAngularStubHarness(t *testing.T, input project.Input) *otelAngularHarness {
	t.Helper()

	h, miseBin := newOtelAngularBaseHarness(t, input)
	stubDir := filepath.Join(h.root, "stubs")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", stubDir, err)
	}
	if err := os.WriteFile(filepath.Join(stubDir, "npm"), []byte(otelStubScript), 0o755); err != nil {
		t.Fatalf("write npm stub: %v", err)
	}
	h.env = append(h.env,
		"PATH="+strings.Join([]string{stubDir, miseBin, "/usr/bin", "/bin"}, string(os.PathListSeparator)),
		"FORGE_OTEL_STUB_RECORD="+h.recordPath,
	)
	return h
}

// otelAngularBuildTimeout bounds one real `ng build`.
const otelAngularBuildTimeout = 5 * time.Minute

// newOtelAngularRealHarness uses the developer's real node/npm. mise's own
// node@24 is not installed in the isolated data dir and MISE_OFFLINE keeps
// it from downloading one, so the task resolves npm from PATH instead;
// Angular 21 accepts any node >= 20.19, so the host node is sufficient.
func newOtelAngularRealHarness(t *testing.T, input project.Input) *otelAngularHarness {
	t.Helper()

	h, miseBin := newOtelAngularBaseHarness(t, input)
	npm, err := exec.LookPath("npm")
	if err != nil {
		t.Skip("npm is not installed")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	path := []string{miseBin, filepath.Dir(npm)}
	if dir := filepath.Dir(node); dir != filepath.Dir(npm) {
		path = append(path, dir)
	}
	h.env = append(h.env, "PATH="+strings.Join(append(path, "/usr/bin", "/bin"), string(os.PathListSeparator)))
	h.timeout = otelAngularBuildTimeout
	return h
}

func requireOtelAngularNetworkSmoke(t *testing.T) {
	t.Helper()

	if os.Getenv("FORGE_SMOKE_NETWORK") != "1" {
		t.Skip("set FORGE_SMOKE_NETWORK=1 to run the networked Angular bundle checks")
	}
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm is not installed")
	}
}

// runNpmInstall installs dependencies with the developer's own env so the
// real npm cache is reused; only the build under test is isolated.
func runNpmInstall(t *testing.T, dir string) {
	t.Helper()

	env := setEnvValue(os.Environ(), "NG_CLI_ANALYTICS", "false")
	output, err := runCommandWithin(dir, env, 10*time.Minute, "npm", "install", "--no-audit", "--no-fund")
	if err != nil {
		t.Fatalf("npm install in %s: %v\n%s", dir, err, output)
	}
}

func runCommandWithin(dir string, env []string, timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return string(output), fmt.Errorf("%s timed out after %s: %w", name, timeout, ctx.Err())
	}
	return string(output), err
}

func (h *otelAngularHarness) writeMiseLocalEndpoint(t *testing.T, tomlValue string) {
	t.Helper()

	path := filepath.Join(h.repoDir, "mise.local.toml")
	content := "[env]\nOTEL_EXPORTER_OTLP_ENDPOINT = " + tomlValue + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func (h *otelAngularHarness) removeMiseLocal(t *testing.T) {
	t.Helper()

	path := filepath.Join(h.repoDir, "mise.local.toml")
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("remove %s: %v", path, err)
	}
}

func (h *otelAngularHarness) resetRecord(t *testing.T) {
	t.Helper()

	if err := os.Remove(h.recordPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("remove %s: %v", h.recordPath, err)
	}
}

// tomlBasicString encodes s as a TOML basic string, escaping quotes,
// backslashes and control characters so the decoded value is exactly s.
func tomlBasicString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func jsFilesContaining(t *testing.T, dir, needle string) []string {
	t.Helper()

	var hits []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".js" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), needle) {
			hits = append(hits, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return hits
}

func assertNoInjectedFile(t *testing.T, root string) {
	t.Helper()

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == "INJECTED" {
			t.Errorf("endpoint value was evaluated as shell source; found %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

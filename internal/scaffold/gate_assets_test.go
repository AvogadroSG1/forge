package scaffold

import (
	"bytes"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"forge"
)

var shippedStacks = []string{
	"go-cli-cobra",
	"go-api-chi",
	"python-cli-typer",
	"python-fastapi",
	"csharp-cli",
	"csharp-webapi",
	"go-web-templ",
	"python-web-jinja",
	"csharp-blazor",
	"vite-ts",
	"sveltekit",
	"angular",
}

// readMiseOverlay reads a stack's overlay mise.toml, accepting the rendered
// (.tmpl) form used by fullstack-capable backends.
func readMiseOverlay(t *testing.T, assets fs.FS, stack string) (string, string) {
	t.Helper()

	base := filepath.ToSlash(filepath.Join("templates/golden", stack, ".forge-overlay", "mise.toml"))
	for _, candidate := range []string{base, base + ".tmpl"} {
		data, err := fs.ReadFile(assets, candidate)
		if err == nil {
			return candidate, string(data)
		}
	}

	t.Fatalf("no mise.toml or mise.toml.tmpl overlay for stack %s", stack)
	return "", ""
}

func TestShippedStacksCarryGatePipelineAssets(t *testing.T) {
	t.Parallel()

	assets := forge.Assets()
	for _, stack := range shippedStacks {
		stack := stack
		t.Run(stack, func(t *testing.T) {
			t.Parallel()

			readMiseOverlay(t, assets, stack)
			for _, rel := range []string{
				".forge-overlay/lefthook.yml",
				".forge-overlay/.github/workflows/ci.yml",
			} {
				assetPath := filepath.ToSlash(filepath.Join("templates/golden", stack, rel))
				if _, err := fs.Stat(assets, assetPath); err != nil {
					t.Fatalf("fs.Stat(%s) error = %v", assetPath, err)
				}
			}
		})
	}
}

func TestShippedStacksDefineGateTasksAndHookCallers(t *testing.T) {
	t.Parallel()

	assets := forge.Assets()
	for _, stack := range shippedStacks {
		stack := stack
		t.Run(stack, func(t *testing.T) {
			t.Parallel()

			misePath, miseText := readMiseOverlay(t, assets, stack)
			for _, section := range []string{"[tasks.fmt]", "[tasks.lint]", "[tasks.test]", "[tasks.ci]"} {
				if !strings.Contains(miseText, section) {
					t.Fatalf("%s missing %s", misePath, section)
				}
			}

			hookPath := filepath.ToSlash(filepath.Join("templates/golden", stack, ".forge-overlay", "lefthook.yml"))
			hookData, err := fs.ReadFile(assets, hookPath)
			if err != nil {
				t.Fatalf("ReadFile(%s) error = %v", hookPath, err)
			}
			hookText := string(hookData)
			for _, snippet := range []string{"scan-staged", "mise run lint", "mise run fmt", "mise run test"} {
				if !strings.Contains(hookText, snippet) {
					t.Fatalf("%s missing %q", hookPath, snippet)
				}
			}
		})
	}
}

func TestSharedCIWorkflowOnlyDelegatesToMise(t *testing.T) {
	t.Parallel()

	assets := forge.Assets()
	for _, stack := range shippedStacks {
		stack := stack
		t.Run(stack, func(t *testing.T) {
			t.Parallel()

			ciPath := filepath.ToSlash(filepath.Join("templates/golden", stack, ".forge-overlay", ".github", "workflows", "ci.yml"))
			ciData, err := fs.ReadFile(assets, ciPath)
			if err != nil {
				t.Fatalf("ReadFile(%s) error = %v", ciPath, err)
			}
			ciText := string(ciData)
			for _, snippet := range []string{"actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1", "jdx/mise-action@236a21c3b91f5db6d4e174b348674a0f0caac06d # v5.2.9", "mise install", "mise run ci"} {
				if !strings.Contains(ciText, snippet) {
					t.Fatalf("%s missing %q", ciPath, snippet)
				}
			}
			for _, forbidden := range []string{"go test", "pytest", "dotnet test", "ruff", "golangci-lint", "vitest", "eslint", "npm run"} {
				if strings.Contains(ciText, forbidden) {
					t.Fatalf("%s should not inline %q", ciPath, forbidden)
				}
			}
		})
	}
}

func TestFullstackBackendMiseTemplatesCarryWebGates(t *testing.T) {
	t.Parallel()

	assets := forge.Assets()
	for _, stack := range []string{"go-api-chi", "python-fastapi", "csharp-webapi"} {
		stack := stack
		t.Run(stack, func(t *testing.T) {
			t.Parallel()

			misePath, miseText := readMiseOverlay(t, assets, stack)
			if !strings.HasSuffix(misePath, ".tmpl") {
				t.Fatalf("%s must be a template so fullstack repos gain web gates", misePath)
			}
			for _, snippet := range []string{
				"{{- if .Frontend }}",
				"[tasks.web-fmt]",
				"[tasks.web-lint]",
				"[tasks.web-test]",
				"npm --prefix web audit --audit-level=high",
			} {
				if !strings.Contains(miseText, snippet) {
					t.Fatalf("%s missing %q", misePath, snippet)
				}
			}
		})
	}
}

// goContainerGateStacks ship the container image pattern (ADR-0023).
var goContainerGateStacks = []string{"go-cli-cobra", "go-api-chi", "go-web-templ"}

// TestGoStacksDefineContainerImageGates owns the container gate seam: the
// Dockerfile lint is part of the Docker-free `ci` task, while build and scan
// are their own mise tasks that the CI workflow reaches only through mise.
func TestGoStacksDefineContainerImageGates(t *testing.T) {
	t.Parallel()

	assets := forge.Assets()
	for _, stack := range goContainerGateStacks {
		stack := stack
		t.Run(stack, func(t *testing.T) {
			t.Parallel()

			misePath, miseText := readMiseOverlay(t, assets, stack)
			for _, snippet := range []string{
				`hadolint = "`,
				`trivy = "`,
				`[tasks."image:lint"]`,
				`run = "hadolint Dockerfile"`,
				"[tasks.image]",
				"--target \"$target\"",
				`[tasks."image:scan"]`,
				`depends = ["image"]`,
				"trivy image --exit-code 1 --severity HIGH,CRITICAL",
			} {
				if !strings.Contains(miseText, snippet) {
					t.Fatalf("%s missing %q", misePath, snippet)
				}
			}

			ciTasks := 0
			for _, line := range strings.Split(miseText, "\n") {
				if !strings.HasPrefix(line, `run = "mise run fmt && `) {
					continue
				}
				ciTasks++
				if !strings.Contains(line, "mise run image:lint") {
					t.Fatalf("%s ci task must run image:lint: %s", misePath, line)
				}
				// ci must stay runnable without a Docker daemon.
				for _, forbidden := range []string{"mise run image &&", "mise run image:scan", "docker ", "trivy"} {
					if strings.Contains(line, forbidden) {
						t.Fatalf("%s ci task must not need Docker (%q): %s", misePath, forbidden, line)
					}
				}
			}
			if ciTasks == 0 {
				t.Fatalf("%s has no ci task line to check", misePath)
			}

			ciPath := filepath.ToSlash(filepath.Join("templates/golden", stack, ".forge-overlay", ".github", "workflows", "ci.yml"))
			ciData, err := fs.ReadFile(assets, ciPath)
			if err != nil {
				t.Fatalf("ReadFile(%s) error = %v", ciPath, err)
			}
			ciText := string(ciData)
			if !strings.Contains(ciText, "- run: mise run image:scan") {
				t.Fatalf("%s must run the image scan through mise", ciPath)
			}
			for _, forbidden := range []string{"docker build", "trivy", "hadolint"} {
				if strings.Contains(ciText, forbidden) {
					t.Fatalf("%s should not inline %q", ciPath, forbidden)
				}
			}
		})
	}
}

// pythonContainerGateStacks ship the Python container image pattern (ADR-0026).
var pythonContainerGateStacks = []string{"python-cli-typer", "python-fastapi", "python-web-jinja"}

// TestPythonStacksDefineContainerImageGates is the Python counterpart of the Go
// gate test: image:lint is part of the Docker-free ci task, build and scan are
// separate tasks reached from CI only through mise.
func TestPythonStacksDefineContainerImageGates(t *testing.T) {
	t.Parallel()

	assets := forge.Assets()
	for _, stack := range pythonContainerGateStacks {
		stack := stack
		t.Run(stack, func(t *testing.T) {
			t.Parallel()

			misePath, miseText := readMiseOverlay(t, assets, stack)
			for _, snippet := range []string{
				`hadolint = "2.15.1"`,
				`trivy = "0.76.2"`,
				`[tasks."image:lint"]`,
				`run = "hadolint Dockerfile"`,
				"[tasks.image]",
				"--target \"$target\"",
				`[tasks."image:scan"]`,
				`depends = ["image"]`,
				"trivy image --exit-code 1 --severity HIGH,CRITICAL",
			} {
				if !strings.Contains(miseText, snippet) {
					t.Fatalf("%s missing %q", misePath, snippet)
				}
			}

			// The run line that follows each [tasks.ci] header is the ci task.
			ciTasks := 0
			lines := strings.Split(miseText, "\n")
			for i, line := range lines {
				if strings.TrimSpace(line) != "[tasks.ci]" {
					continue
				}
				var run string
				for _, next := range lines[i+1:] {
					if strings.HasPrefix(next, "[") {
						break
					}
					if strings.HasPrefix(next, "run = ") {
						run = next
						break
					}
				}
				if run == "" {
					t.Fatalf("%s [tasks.ci] has no run line", misePath)
				}
				ciTasks++
				for _, required := range []string{"mise run image:lint", "uv sync --locked"} {
					if !strings.Contains(run, required) {
						t.Fatalf("%s ci task must contain %q: %s", misePath, required, run)
					}
				}
				// ci must stay runnable without a Docker daemon.
				for _, forbidden := range []string{"mise run image &&", "mise run image:scan", "docker ", "trivy"} {
					if strings.Contains(run, forbidden) {
						t.Fatalf("%s ci task must not need Docker (%q): %s", misePath, forbidden, run)
					}
				}
			}
			wantCI := 1
			if stack == "python-fastapi" {
				wantCI = 2 // frontend and non-frontend variants
			}
			if ciTasks != wantCI {
				t.Fatalf("%s has %d [tasks.ci] tables, want %d", misePath, ciTasks, wantCI)
			}

			ciPath := filepath.ToSlash(filepath.Join("templates/golden", stack, ".forge-overlay", ".github", "workflows", "ci.yml"))
			ciData, err := fs.ReadFile(assets, ciPath)
			if err != nil {
				t.Fatalf("ReadFile(%s) error = %v", ciPath, err)
			}
			ciText := string(ciData)
			if !strings.Contains(ciText, "- run: mise run image:scan") {
				t.Fatalf("%s must run the image scan through mise", ciPath)
			}
			for _, forbidden := range []string{"docker build", "trivy", "hadolint"} {
				if strings.Contains(ciText, forbidden) {
					t.Fatalf("%s should not inline %q", ciPath, forbidden)
				}
			}
		})
	}
}

// ADR-0024: Lefthook owns .git/hooks and Beads runs as a Lefthook command in
// every hook it supports. All twelve overlays MUST stay byte-identical.
var (
	hookArgPlaceholder     = regexp.MustCompile(`\{\d+\}`)
	hookArgPlaceholderLine = regexp.MustCompile(`^\{[1-9]\}$`)
	hookArgHeredocOpen     = regexp.MustCompile(`<<'([A-Za-z0-9_]+)'$`)
)

func TestShippedLefthookConfigsRunBeadsHooksAndAreIdentical(t *testing.T) {
	t.Parallel()

	assets := forge.Assets()
	beadsHooks := []string{"pre-commit", "pre-push", "post-merge", "post-checkout", "prepare-commit-msg"}

	var reference []byte
	for _, stack := range shippedStacks {
		path := filepath.ToSlash(filepath.Join("templates/golden", stack, ".forge-overlay/lefthook.yml"))
		data, err := fs.ReadFile(assets, path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}

		var config map[string]struct {
			Commands map[string]struct {
				Run string `yaml:"run"`
			} `yaml:"commands"`
		}
		if err := yaml.Unmarshal(data, &config); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, hook := range beadsHooks {
			run := config[hook].Commands["beads"].Run
			if !strings.Contains(run, "command -v bd") {
				t.Errorf("%s: %s.beads must be guarded by `command -v bd`; got %q", path, hook, run)
			}
			if want := "bd hooks run " + hook; !strings.Contains(run, want) {
				t.Errorf("%s: %s.beads run = %q, want it to contain %q", path, hook, run, want)
			}
			// Lefthook splices {0}/{N} into shell source unescaped (ADR-0024):
			// {0} is banned, and a {N} may only be the whole body of a quoted
			// here-document.
			if strings.Contains(run, "{0}") {
				t.Errorf("%s: %s.beads uses {0}, which loses argument boundaries and executes shell syntax in arguments", path, hook)
			}
			lines := strings.Split(run, "\n")
			for i, line := range lines {
				if !hookArgPlaceholder.MatchString(line) {
					continue
				}
				if !hookArgPlaceholderLine.MatchString(line) || i == 0 || i+1 >= len(lines) {
					t.Errorf("%s: %s.beads line %q: a hook argument placeholder must be alone on its line", path, hook, line)
					continue
				}
				open := hookArgHeredocOpen.FindStringSubmatch(lines[i-1])
				if open == nil || lines[i+1] != open[1] {
					t.Errorf("%s: %s.beads placeholder %q is not the sole body of a quoted here-document (previous %q, next %q)", path, hook, line, lines[i-1], lines[i+1])
				}
			}
		}
		if _, ok := config["pre-commit"].Commands["secret-scan"]; !ok {
			t.Errorf("%s: pre-commit lost the secret-scan command", path)
		}
		if _, ok := config["pre-push"].Commands["test"]; !ok {
			t.Errorf("%s: pre-push lost the test command", path)
		}

		if reference == nil {
			reference = data
			continue
		}
		if !bytes.Equal(data, reference) {
			t.Errorf("%s differs from %s; the lefthook.yml overlays MUST stay byte-identical", path, shippedStacks[0])
		}
	}
}

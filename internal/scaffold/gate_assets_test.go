package scaffold

import (
	"bytes"
	"io/fs"
	"path/filepath"
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

// ADR-0024: Lefthook owns .git/hooks and Beads runs as a Lefthook command in
// every hook it supports. All twelve overlays MUST stay byte-identical.
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
			if want := "bd hooks run " + hook + " {0}"; !strings.Contains(run, want) {
				t.Errorf("%s: %s.beads run = %q, want it to contain %q", path, hook, run, want)
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

package scaffold

import (
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

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
			for _, snippet := range []string{"mise install", "mise run ci"} {
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

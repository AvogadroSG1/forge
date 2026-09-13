package test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCanonicalGuidelineFilesReachStablePaths(t *testing.T) {
	checker := newGuidelineChecker(t)

	for _, language := range []string{"golang", "python", "csharp", "typescript"} {
		path, err := checker.guidelinePath(language)
		if err != nil {
			t.Fatalf("guideline path for %s: %v", language, err)
		}

		if _, err := os.Stat(path); err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}

		required, err := checker.requiredTools(language)
		if err != nil {
			t.Fatalf("required tools for %s: %v", language, err)
		}
		if len(required) == 0 {
			t.Fatalf("%s guideline produced no normalized tool requirements", language)
		}
	}
}

func TestShippedV1StacksSatisfyGuidelineFloor(t *testing.T) {
	checker := newGuidelineChecker(t)

	tests := []struct {
		name     string
		language string
		files    []string
	}{
		{
			name:     "go-cli-cobra",
			language: "golang",
			files: []string{
				"templates/golden/go-cli-cobra/go.mod.tmpl",
				"templates/golden/go-cli-cobra/.forge-overlay/mise.toml",
				"templates/golden/go-cli-cobra/.forge-overlay/cmd/root_test.go.tmpl",
			},
		},
		{
			name:     "go-api-chi",
			language: "golang",
			files: []string{
				"templates/golden/go-api-chi/go.mod.tmpl",
				"templates/golden/go-api-chi/.forge-overlay/mise.toml.tmpl",
				"templates/golden/go-api-chi/.forge-overlay/internal/httpapi/health_test.go.tmpl",
			},
		},
		{
			name:     "python-cli-typer",
			language: "python",
			files: []string{
				"templates/golden/python-cli-typer/pyproject.toml.tmpl",
				"templates/golden/python-cli-typer/.forge-overlay/mise.toml.tmpl",
				"templates/golden/python-cli-typer/.forge-overlay/tests/test_cli.py.tmpl",
			},
		},
		{
			name:     "python-fastapi",
			language: "python",
			files: []string{
				"templates/golden/python-fastapi/pyproject.toml.tmpl",
				"templates/golden/python-fastapi/.forge-overlay/mise.toml.tmpl",
				"templates/golden/python-fastapi/.forge-overlay/tests/test_health.py.tmpl",
			},
		},
		{
			name:     "csharp-cli",
			language: "csharp",
			files: []string{
				"templates/golden/csharp-cli/Project.csproj.tmpl",
				"templates/golden/csharp-cli/.forge-overlay/mise.toml",
				"templates/golden/csharp-cli/.forge-overlay/tests/Project.Tests/Project.Tests.csproj.tmpl",
				"templates/golden/csharp-cli/.forge-overlay/tests/Project.Tests/ProgramTests.cs",
			},
		},
		{
			name:     "csharp-webapi",
			language: "csharp",
			files: []string{
				"templates/golden/csharp-webapi/Project.csproj.tmpl",
				"templates/golden/csharp-webapi/.forge-overlay/mise.toml.tmpl",
				"templates/golden/csharp-webapi/WeatherForecast.cs.tmpl",
				"templates/golden/csharp-webapi/Controllers/WeatherForecastController.cs.tmpl",
				"templates/golden/csharp-webapi/.forge-overlay/tests/Project.Tests/Project.Tests.csproj.tmpl",
				"templates/golden/csharp-webapi/.forge-overlay/tests/Project.Tests/WeatherForecastEndpointTests.cs",
			},
		},
		{
			name:     "go-web-templ",
			language: "golang",
			files: []string{
				"templates/golden/go-web-templ/go.mod.tmpl",
				"templates/golden/go-web-templ/.forge-overlay/mise.toml",
				"templates/golden/go-web-templ/.forge-overlay/internal/web/server_test.go",
			},
		},
		{
			name:     "python-web-jinja",
			language: "python",
			files: []string{
				"templates/golden/python-web-jinja/pyproject.toml.tmpl",
				"templates/golden/python-web-jinja/.forge-overlay/mise.toml.tmpl",
				"templates/golden/python-web-jinja/.forge-overlay/tests/test_health.py.tmpl",
			},
		},
		{
			name:     "csharp-blazor",
			language: "csharp",
			files: []string{
				"templates/golden/csharp-blazor/Project.csproj.tmpl",
				"templates/golden/csharp-blazor/.forge-overlay/mise.toml",
				"templates/golden/csharp-blazor/.forge-overlay/tests/Project.Tests/Project.Tests.csproj.tmpl",
				"templates/golden/csharp-blazor/.forge-overlay/tests/Project.Tests/HealthReporterTests.cs.tmpl",
			},
		},
		{
			name:     "vite-ts",
			language: "typescript",
			files: []string{
				"templates/golden/vite-ts/.forge-overlay/package.json.tmpl",
				"templates/golden/vite-ts/.forge-overlay/mise.toml",
				"templates/golden/vite-ts/.forge-overlay/src/api/client.test.ts",
			},
		},
		{
			name:     "sveltekit",
			language: "typescript",
			files: []string{
				"templates/golden/sveltekit/.forge-overlay/package.json.tmpl",
				"templates/golden/sveltekit/.forge-overlay/mise.toml",
				"templates/golden/sveltekit/.forge-overlay/src/lib/api/client.test.ts",
			},
		},
		{
			name:     "angular",
			language: "typescript",
			files: []string{
				"templates/golden/angular/.forge-overlay/package.json.tmpl",
				"templates/golden/angular/.forge-overlay/mise.toml",
				"templates/golden/angular/.forge-overlay/src/app/api/client.spec.ts",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := checker.checkStack(tt.language, tt.files); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConformanceIgnoresVettedExtrasOutsideTheGuidelineFloor(t *testing.T) {
	checker := newGuidelineChecker(t)

	err := checker.checkEvidence("python", []evidenceFile{
		{
			path: "synthetic-python-overlay",
			content: strings.Join([]string{
				"ruff format .",
				"ruff check . && pyright",
				"pytest -q",
				"pytest-cov",
				"pytest-mock",
				"pip-audit",
				"bonus-tool",
			}, "\n"),
		},
	})
	if err != nil {
		t.Fatalf("extras should not fail floor-only conformance: %v", err)
	}
}

func TestPostV1GuidelineFilesExistInVendoredSnapshots(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)

	for _, tc := range []struct {
		language string
		file     string
	}{
		{"rust", "rust.md"},
		{"bash", "bash.md"},
	} {
		t.Run(tc.language, func(t *testing.T) {
			path := filepath.Join(root, "test", "testdata", "guidelines", tc.file)
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("guideline file missing: %s", path)
			}
			if info.Size() < 100 {
				t.Fatalf("guideline file %s is suspiciously small (%d bytes)", path, info.Size())
			}
		})
	}
}

func TestShippedTemplateWithoutGuidelineBackedLanguageFails(t *testing.T) {
	checker := newGuidelineChecker(t)

	err := checker.checkEvidence("zig", []evidenceFile{{path: "synthetic-zig-overlay", content: "zig build test"}})
	if err == nil {
		t.Fatal("expected missing canonical guideline error")
	}
	if !strings.Contains(err.Error(), "no canonical guideline file") {
		t.Fatalf("error %q does not name the missing canonical guideline", err)
	}
}

type guidelineChecker struct {
	repoRoot string
}

type toolRequirement struct {
	key             string
	guidelineNeedle string
	evidenceAll     []string
}

type evidenceFile struct {
	path    string
	content string
}

func newGuidelineChecker(t *testing.T) guidelineChecker {
	t.Helper()

	return guidelineChecker{
		repoRoot: repoRoot(t),
	}
}

func (c guidelineChecker) checkStack(language string, relPaths []string) error {
	files := make([]evidenceFile, 0, len(relPaths))
	for _, relPath := range relPaths {
		fullPath := filepath.Join(c.repoRoot, relPath)
		content, err := os.ReadFile(fullPath)
		if err != nil {
			return fmt.Errorf("read %s: %w", fullPath, err)
		}
		files = append(files, evidenceFile{
			path:    relPath,
			content: string(content),
		})
	}

	return c.checkEvidence(language, files)
}

func (c guidelineChecker) checkEvidence(language string, files []evidenceFile) error {
	required, err := c.requiredTools(language)
	if err != nil {
		return err
	}

	var missing []string
	for _, requirement := range required {
		if !filesContainAll(files, requirement.evidenceAll) {
			missing = append(missing, requirement.key)
		}
	}

	if len(missing) == 0 {
		return nil
	}

	slices.Sort(missing)
	return fmt.Errorf("%s overlay is missing guideline floor tools: %s", language, strings.Join(missing, ", "))
}

func (c guidelineChecker) requiredTools(language string) ([]toolRequirement, error) {
	path, err := c.guidelinePath(language)
	if err != nil {
		return nil, err
	}

	contentBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	content := string(contentBytes)
	lines := normativeGuidelineLines(content)
	specs, ok := languageRequirementSpecs[language]
	if !ok {
		return nil, fmt.Errorf("no canonical guideline file for %s", language)
	}

	required := make([]toolRequirement, 0, len(specs))
	for _, spec := range specs {
		if !linesContain(lines, spec.guidelineNeedle) {
			return nil, fmt.Errorf("%s guideline is missing required MUST/SHOULD coverage for %s", language, spec.key)
		}
		required = append(required, spec)
	}

	return required, nil
}

func (c guidelineChecker) guidelinePath(language string) (string, error) {
	fileName, ok := map[string]string{
		"golang":     "golang.md",
		"python":     "python.md",
		"csharp":     "csharp.md",
		"typescript": "typescript.md",
		"rust":       "rust.md",
		"bash":       "bash.md",
	}[language]
	if !ok {
		return "", fmt.Errorf("no canonical guideline file for %s", language)
	}

	return filepath.Join(c.repoRoot, "test", "testdata", "guidelines", fileName), nil
}

func normativeGuidelineLines(content string) []string {
	var lines []string

	for _, line := range strings.Split(content, "\n") {
		if !strings.Contains(line, "MUST") && !strings.Contains(line, "SHOULD") && !strings.Contains(line, "PREFER") {
			continue
		}
		lines = append(lines, line)
	}

	return lines
}

func linesContain(lines []string, needle string) bool {
	for _, line := range lines {
		if strings.Contains(line, needle) {
			return true
		}
	}

	return false
}

func filesContainAll(files []evidenceFile, needles []string) bool {
	for _, needle := range needles {
		found := false
		for _, file := range files {
			if strings.Contains(file.content, needle) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	return true
}

var languageRequirementSpecs = map[string][]toolRequirement{
	"golang": {
		{key: "gofmt", guidelineNeedle: "`gofmt`", evidenceAll: []string{"gofmt -w"}},
		{key: "golangci-lint", guidelineNeedle: "`golangci-lint`", evidenceAll: []string{"golangci-lint run ./..."}},
		{key: "testing", guidelineNeedle: "`testing` package", evidenceAll: []string{"\"testing\""}},
		{key: "go-cmp", guidelineNeedle: "github.com/google/go-cmp", evidenceAll: []string{"github.com/google/go-cmp"}},
		{key: "go test -cover", guidelineNeedle: "`go test -cover`", evidenceAll: []string{"go test -cover ./..."}},
		{key: "govulncheck", guidelineNeedle: "`govulncheck`", evidenceAll: []string{"govulncheck ./..."}},
	},
	"python": {
		{key: "ruff format", guidelineNeedle: "`ruff` for linting and formatting", evidenceAll: []string{"ruff format ."}},
		{key: "ruff", guidelineNeedle: "`ruff` for linting and formatting", evidenceAll: []string{"ruff check ."}},
		{key: "pytest", guidelineNeedle: "`pytest` as the testing framework", evidenceAll: []string{"pytest -q"}},
		{key: "pytest-cov", guidelineNeedle: "`pytest-cov`", evidenceAll: []string{"pytest-cov"}},
		{key: "pytest-mock", guidelineNeedle: "`pytest-mock`", evidenceAll: []string{"pytest-mock"}},
		{key: "pyright", guidelineNeedle: "`pyright`", evidenceAll: []string{"pyright"}},
		{key: "pip-audit", guidelineNeedle: "`pip-audit`", evidenceAll: []string{"pip-audit"}},
	},
	"typescript": {
		{key: "prettier", guidelineNeedle: "`prettier`", evidenceAll: []string{"prettier --write"}},
		{key: "eslint", guidelineNeedle: "`eslint`", evidenceAll: []string{"eslint ."}},
		{key: "vitest", guidelineNeedle: "`vitest`", evidenceAll: []string{"vitest"}},
		{key: "typecheck", guidelineNeedle: "`typecheck` script", evidenceAll: []string{"\"typecheck\":"}},
		{key: "npm audit", guidelineNeedle: "`npm audit`", evidenceAll: []string{"npm audit --audit-level=high"}},
	},
	"csharp": {
		{key: "dotnet format", guidelineNeedle: "`dotnet format`", evidenceAll: []string{"dotnet format"}},
		{key: "StyleCop.Analyzers", guidelineNeedle: "`StyleCop.Analyzers`", evidenceAll: []string{"Include=\"StyleCop.Analyzers\""}},
		{key: "xUnit", guidelineNeedle: "xUnit", evidenceAll: []string{"Include=\"xunit\""}},
		{key: "NSubstitute", guidelineNeedle: "NSubstitute", evidenceAll: []string{"Include=\"NSubstitute\""}},
		{key: "coverlet", guidelineNeedle: "`coverlet`", evidenceAll: []string{"Include=\"coverlet.collector\""}},
		{key: "nullable refs", guidelineNeedle: "<Nullable>enable</Nullable>", evidenceAll: []string{"<Nullable>enable</Nullable>"}},
		{key: "dotnet list package --vulnerable", guidelineNeedle: "`dotnet list package --vulnerable`", evidenceAll: []string{"dotnet list", "--vulnerable"}},
	},
}

package scaffold

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"forge"
	"forge/internal/project"
)

func TestWriterComposesCommonVanillaAndOverlayAssets(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	assets := fstest.MapFS{
		"templates/common/AGENTS.md.tmpl":                             {Data: []byte("Project {{.ProjectName}}\n")},
		"templates/common/gitignore.base":                             {Data: []byte(".DS_Store\n")},
		"templates/common/claude/settings.local.json.tmpl":            {Data: []byte("{\"project\":\"{{.ProjectName}}\"}\n")},
		"templates/common/claude/hooks/secret-scan.sh":                {Data: []byte("#!/usr/bin/env bash\n")},
		"templates/common/codex/hooks.json":                           {Data: []byte("{\"hooks\":{}}\n")},
		"templates/gitignore/Go.gitignore":                            {Data: []byte("bin/\n")},
		"templates/golden/go-cli-cobra/main.go.tmpl":                  {Data: []byte("package main\n\nconst Name = \"{{.ProjectName}}\"\n")},
		"templates/golden/go-cli-cobra/.forge-overlay/main.go":        {Data: []byte("package main\n\nconst Overlay = true\n")},
		"templates/golden/go-cli-cobra/.forge-overlay/README.md.tmpl": {Data: []byte("# {{.ProjectName}}\n")},
	}

	vars, err := project.ResolveVariables(project.Input{
		ProjectName: "Sample App",
		Language:    "go",
		ProjectType: "cli",
		Stack:       "go-cli-cobra",
		AuthorName:  "Ada Lovelace",
		AuthorEmail: "ada@example.com",
		Remote:      project.RemoteNone,
	})
	if err != nil {
		t.Fatalf("ResolveVariables() error = %v", err)
	}

	writer := Writer{Assets: assets}
	if err := writer.Write(tempDir, vars); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	assertFileContains(t, filepath.Join(tempDir, "AGENTS.md"), "Project Sample App")
	assertFileContains(t, filepath.Join(tempDir, "README.md"), "# Sample App")
	assertFileContains(t, filepath.Join(tempDir, "main.go"), "const Overlay = true")
	assertFileContains(t, filepath.Join(tempDir, ".gitignore"), ".DS_Store")
	assertFileContains(t, filepath.Join(tempDir, ".gitignore"), "# ===== go gitignore =====")
	assertFileContains(t, filepath.Join(tempDir, ".gitignore"), "bin/")

	linkTarget, err := os.Readlink(filepath.Join(tempDir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("Readlink() error = %v", err)
	}
	if linkTarget != "AGENTS.md" {
		t.Fatalf("CLAUDE.md link target = %q, want %q", linkTarget, "AGENTS.md")
	}

	info, err := os.Stat(filepath.Join(tempDir, ".claude", "hooks", "secret-scan.sh"))
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("secret-scan.sh mode = %#o, want %#o", info.Mode().Perm(), fs.FileMode(0o755))
	}
}

func TestWriterFailsBeforeWritingPartialTemplatesOnMissingVariables(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	assets := fstest.MapFS{
		"templates/common/gitignore.base":              {Data: []byte(".DS_Store\n")},
		"templates/common/AGENTS.md.tmpl":              {Data: []byte("{{.Undefined}}\n")},
		"templates/gitignore/Go.gitignore":             {Data: []byte("bin/\n")},
		"templates/golden/go-cli-cobra/main.go":        {Data: []byte("package main\n")},
		"templates/common/claude/hooks/secret-scan.sh": {Data: []byte("#!/usr/bin/env bash\n")},
		"templates/common/codex/hooks.json":            {Data: []byte("{\"hooks\":{}}\n")},
	}

	writer := Writer{Assets: assets}
	err := writer.Write(tempDir, project.Variables{
		ProjectName: "Broken",
		Language:    "go",
		Stack:       "go-cli-cobra",
	})
	if err == nil || !strings.Contains(err.Error(), "Undefined") {
		t.Fatalf("Write() error = %v, want missing key error", err)
	}

	if _, statErr := os.Stat(filepath.Join(tempDir, "AGENTS.md")); !os.IsNotExist(statErr) {
		t.Fatalf("AGENTS.md stat error = %v, want not exists", statErr)
	}
}

func TestWriterRendersPythonPackageFromEmbeddedTemplates(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	vars, err := project.ResolveVariables(project.Input{
		ProjectName: "My Cool API",
		Language:    "python",
		ProjectType: "cli",
		Stack:       "python-cli-typer",
		AuthorName:  "Ada Lovelace",
		AuthorEmail: "ada@example.com",
		Remote:      project.RemoteNone,
	})
	if err != nil {
		t.Fatalf("ResolveVariables() error = %v", err)
	}

	writer := Writer{Assets: forge.Assets()}
	if err := writer.Write(tempDir, vars); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	assertFileContains(t, filepath.Join(tempDir, "pyproject.toml"), `name = "my_cool_api"`)
	assertFileContains(t, filepath.Join(tempDir, "README.md"), "# My Cool API")
	assertFileContains(t, filepath.Join(tempDir, "CONTEXT.md"), "# Context")
	assertFileContains(t, filepath.Join(tempDir, "docs", "adr", "0000-template.md"), "# ADR 0000")

	manifestPath := filepath.Join(tempDir, ".claude", "skill-manifest.json")
	if _, statErr := os.Stat(manifestPath); !os.IsNotExist(statErr) {
		t.Fatalf("skill-manifest.json stat error = %v, want not exists (seed skills are rendered in memory, not scaffolded)", statErr)
	}

	// Verify package directory is rendered (not literal app/ or {{.PythonPackage}}/)
	pkgDir := filepath.Join(tempDir, "src", "my_cool_api")
	if _, err := os.Stat(pkgDir); err != nil {
		t.Fatalf("expected rendered package dir %q to exist: %v", pkgDir, err)
	}
	initFile := filepath.Join(pkgDir, "__init__.py")
	if _, err := os.Stat(initFile); err != nil {
		t.Fatalf("expected %q to exist: %v", initFile, err)
	}

	// Verify literal app/ does NOT exist
	literalApp := filepath.Join(tempDir, "src", "app")
	if _, err := os.Stat(literalApp); err == nil {
		t.Fatal("literal src/app/ directory should not exist after rendering")
	}

	// Verify literal template expression does NOT exist in output
	templateLiteral := filepath.Join(tempDir, "src", "{{.PythonPackage}}")
	if _, err := os.Stat(templateLiteral); err == nil {
		t.Fatal("literal {{.PythonPackage}} directory should not exist in output")
	}
}

func TestWriterRendersPythonFastAPIPackageDirectory(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	vars, err := project.ResolveVariables(project.Input{
		ProjectName: "Cost Investigator",
		Language:    "python",
		ProjectType: "service",
		Stack:       "python-fastapi",
		AuthorName:  "Ada Lovelace",
		AuthorEmail: "ada@example.com",
		Remote:      project.RemoteNone,
	})
	if err != nil {
		t.Fatalf("ResolveVariables() error = %v", err)
	}

	writer := Writer{Assets: forge.Assets()}
	if err := writer.Write(tempDir, vars); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	// Package directory uses derived name
	pkgDir := filepath.Join(tempDir, "cost_investigator")
	if _, err := os.Stat(pkgDir); err != nil {
		t.Fatalf("expected package dir %q: %v", pkgDir, err)
	}
	assertFileContains(t, filepath.Join(pkgDir, "__init__.py"), "")

	// No literal app/ directory
	if _, err := os.Stat(filepath.Join(tempDir, "app")); err == nil {
		t.Fatal("literal app/ should not exist")
	}

	// pyproject.toml references the package name
	assertFileContains(t, filepath.Join(tempDir, "pyproject.toml"), `packages = ["cost_investigator"]`)

	// test file has correct import
	assertFileContains(t, filepath.Join(tempDir, "tests", "test_health.py"), "from cost_investigator.main import app")
}

func TestWriterRendersCSharpNamespaceIntoProjectFile(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	vars, err := project.ResolveVariables(project.Input{
		ProjectName: "My Cool API",
		Language:    "csharp",
		ProjectType: "cli",
		Stack:       "csharp-cli",
		AuthorName:  "Ada Lovelace",
		AuthorEmail: "ada@example.com",
		Remote:      project.RemoteNone,
	})
	if err != nil {
		t.Fatalf("ResolveVariables() error = %v", err)
	}

	writer := Writer{Assets: forge.Assets()}
	if err := writer.Write(tempDir, vars); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	projectFile := filepath.Join(tempDir, "Project.csproj")
	assertFileContains(t, projectFile, "<AssemblyName>MyCoolApi</AssemblyName>")
	assertFileContains(t, projectFile, "<RootNamespace>MyCoolApi</RootNamespace>")
}

func TestWriterAllowsGitDirectoryFromPreinitializedRepo(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tempDir, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.git) error = %v", err)
	}

	assets := fstest.MapFS{
		"templates/common/AGENTS.md.tmpl":              {Data: []byte("Project {{.ProjectName}}\n")},
		"templates/common/gitignore.base":              {Data: []byte(".DS_Store\n")},
		"templates/common/claude/hooks/secret-scan.sh": {Data: []byte("#!/usr/bin/env bash\n")},
		"templates/common/codex/hooks.json":            {Data: []byte("{\"hooks\":{}}\n")},
		"templates/gitignore/Go.gitignore":             {Data: []byte("bin/\n")},
		"templates/golden/go-cli-cobra/main.go.tmpl":   {Data: []byte("package main\n")},
	}

	vars, err := project.ResolveVariables(project.Input{
		ProjectName: "Sample App",
		Language:    "go",
		ProjectType: "cli",
		Stack:       "go-cli-cobra",
		AuthorName:  "Ada Lovelace",
		AuthorEmail: "ada@example.com",
		Remote:      project.RemoteNone,
	})
	if err != nil {
		t.Fatalf("ResolveVariables() error = %v", err)
	}

	writer := Writer{Assets: assets}
	if err := writer.Write(tempDir, vars); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	assertFileContains(t, filepath.Join(tempDir, "AGENTS.md"), "Project Sample App")
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}

	return string(data)
}

func assertFileContains(t *testing.T, path string, want string) {
	t.Helper()

	data := readFile(t, path)
	if !strings.Contains(data, want) {
		t.Fatalf("%s does not contain %q:\n%s", path, want, data)
	}
}

func TestTemplateSecretScanMatchesSharedScanner(t *testing.T) {
	t.Parallel()

	shared := readFile(t, filepath.Join("..", "..", ".claude", "hooks", "secret-scan.sh"))
	template := readFile(t, filepath.Join("..", "..", "templates", "common", "claude", "hooks", "secret-scan.sh"))
	if template != shared {
		t.Fatalf("template secret scanner drifted from shared scanner")
	}
}

func TestTemplateGuardMatchesSharedGuard(t *testing.T) {
	t.Parallel()

	shared := readFile(t, filepath.Join("..", "..", ".claude", "hooks", "guard"))
	template := readFile(t, filepath.Join("..", "..", "templates", "common", "claude", "hooks", "guard"))
	if template != shared {
		t.Fatalf("template guard drifted from shared guard")
	}
}

func TestWriterComposesFrontendFragmentUnderWeb(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	assets := fstest.MapFS{
		"templates/common/AGENTS.md.tmpl":  {Data: []byte("Project {{.ProjectName}}\n")},
		"templates/common/gitignore.base":  {Data: []byte(".DS_Store\n")},
		"templates/gitignore/Go.gitignore": {Data: []byte("bin/\n")},
		"templates/gitignore/Node.gitignore": {
			Data: []byte("node_modules/\n"),
		},
		"templates/golden/go-api-chi/go.mod.tmpl": {Data: []byte("module {{.ModulePath}}\n")},
		"templates/golden/go-api-chi/.forge-overlay/mise.toml.tmpl": {
			Data: []byte("[tools]\ngo = \"1.26.4\"\n{{- if .Frontend }}\nnode = \"24\"\n\n[tasks.web-test]\nrun = \"npm --prefix web test\"\n{{- end }}\n"),
		},
		"templates/golden/vite-ts/package.json.tmpl": {Data: []byte("{\"name\": \"{{.NpmPackage}}\"}\n")},
		"templates/golden/vite-ts/src/counter.ts":    {Data: []byte("export {};\n")},
		"templates/golden/vite-ts/.forge-overlay/src/api/client.ts.tmpl": {
			Data: []byte("const base = '{{.APIBaseURL}}';\nexport default base;\n"),
		},
		"templates/golden/vite-ts/.forge-overlay/src/main.ts": {Data: []byte("export const overlay = true;\n")},
		"templates/golden/vite-ts/.forge-overlay/mise.toml":   {Data: []byte("[tools]\nnode = \"24\"\n")},
		"templates/golden/vite-ts/.forge-overlay/lefthook.yml": {
			Data: []byte("pre-commit:\n"),
		},
		"templates/golden/vite-ts/.forge-overlay/.github/workflows/ci.yml": {
			Data: []byte("name: ci\n"),
		},
	}

	vars, err := project.ResolveVariables(project.Input{
		ProjectName: "Full App",
		Language:    "go",
		ProjectType: "fullstack",
		Stack:       "go-api-chi",
		Frontend:    "vite-ts",
		AuthorName:  "Ada Lovelace",
		AuthorEmail: "ada@example.com",
		Remote:      project.RemoteNone,
	})
	if err != nil {
		t.Fatalf("ResolveVariables() error = %v", err)
	}

	writer := Writer{Assets: assets}
	if err := writer.Write(tempDir, vars); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	assertFileContains(t, filepath.Join(tempDir, "web", "package.json"), "\"name\": \"full-app\"")
	assertFileContains(t, filepath.Join(tempDir, "web", "src", "counter.ts"), "export {};")
	assertFileContains(t, filepath.Join(tempDir, "web", "src", "api", "client.ts"), "const base = 'http://localhost:8080';")
	assertFileContains(t, filepath.Join(tempDir, "web", "src", "main.ts"), "overlay = true")

	assertFileContains(t, filepath.Join(tempDir, "mise.toml"), "[tasks.web-test]")
	assertFileContains(t, filepath.Join(tempDir, "mise.toml"), "node = \"24\"")

	for _, forbidden := range []string{
		filepath.Join(tempDir, "web", "mise.toml"),
		filepath.Join(tempDir, "web", "lefthook.yml"),
		filepath.Join(tempDir, "web", ".github"),
	} {
		if _, err := os.Stat(forbidden); !os.IsNotExist(err) {
			t.Fatalf("fragment gate file %s should not exist (err = %v)", forbidden, err)
		}
	}

	gitignore, err := os.ReadFile(filepath.Join(tempDir, ".gitignore"))
	if err != nil {
		t.Fatalf("ReadFile(.gitignore) error = %v", err)
	}
	text := string(gitignore)
	goBanner := strings.Index(text, "# ===== go gitignore =====")
	nodeBanner := strings.Index(text, "# ===== node gitignore =====")
	if goBanner == -1 || nodeBanner == -1 || nodeBanner < goBanner {
		t.Fatalf(".gitignore sections out of order:\n%s", text)
	}
	if !strings.Contains(text, "node_modules/") {
		t.Fatalf(".gitignore missing node section:\n%s", text)
	}
	if strings.Contains(text, "<no value>") || strings.Contains(text, "{{") {
		t.Fatalf(".gitignore has template residue:\n%s", text)
	}
}

func TestWriterRendersStandaloneTypescriptStackWithNodeGitignore(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	assets := fstest.MapFS{
		"templates/common/AGENTS.md.tmpl":            {Data: []byte("Project {{.ProjectName}}\n")},
		"templates/common/gitignore.base":            {Data: []byte(".DS_Store\n")},
		"templates/gitignore/Node.gitignore":         {Data: []byte("node_modules/\n")},
		"templates/golden/vite-ts/package.json.tmpl": {Data: []byte("{\"name\": \"{{.NpmPackage}}\"}\n")},
		"templates/golden/vite-ts/.forge-overlay/src/api/client.ts.tmpl": {
			Data: []byte("const base = '{{.APIBaseURL}}';\nexport default base;\n"),
		},
		"templates/golden/vite-ts/.forge-overlay/mise.toml": {Data: []byte("[tools]\nnode = \"24\"\n")},
	}

	vars, err := project.ResolveVariables(project.Input{
		ProjectName: "Web App",
		Language:    "typescript",
		ProjectType: "frontend",
		Stack:       "vite-ts",
		APIBaseURL:  "https://api.example.com",
		AuthorName:  "Ada Lovelace",
		AuthorEmail: "ada@example.com",
		Remote:      project.RemoteNone,
	})
	if err != nil {
		t.Fatalf("ResolveVariables() error = %v", err)
	}

	writer := Writer{Assets: assets}
	if err := writer.Write(tempDir, vars); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	assertFileContains(t, filepath.Join(tempDir, "package.json"), "\"name\": \"web-app\"")
	assertFileContains(t, filepath.Join(tempDir, "src", "api", "client.ts"), "https://api.example.com")
	assertFileContains(t, filepath.Join(tempDir, "mise.toml"), "node = \"24\"")
	assertFileContains(t, filepath.Join(tempDir, ".gitignore"), "# ===== node gitignore =====")
}

func TestRenderPathExpandsTemplateExpressions(t *testing.T) {
	t.Parallel()

	vars := project.Variables{PythonPackage: "my_cool_api"}

	got, err := renderPath("{{.PythonPackage}}/__init__.py", vars)
	if err != nil {
		t.Fatalf("renderPath() error = %v", err)
	}
	if got != "my_cool_api/__init__.py" {
		t.Fatalf("renderPath() = %q, want %q", got, "my_cool_api/__init__.py")
	}
}

func TestRenderPathPassesThroughPlainPaths(t *testing.T) {
	t.Parallel()

	vars := project.Variables{PythonPackage: "my_cool_api"}

	got, err := renderPath("claude/settings.json", vars)
	if err != nil {
		t.Fatalf("renderPath() error = %v", err)
	}
	if got != "claude/settings.json" {
		t.Fatalf("renderPath() = %q, want %q", got, "claude/settings.json")
	}
}

func TestRenderPathRejectsTraversalAttempts(t *testing.T) {
	t.Parallel()

	vars := project.Variables{PythonPackage: ".."}

	_, err := renderPath("{{.PythonPackage}}/main.py", vars)
	if err == nil {
		t.Fatal("renderPath() expected error for traversal, got nil")
	}
}

func TestRenderPathAllowsDoubleDotWithinSegment(t *testing.T) {
	t.Parallel()

	vars := project.Variables{CSharpNamespace: "My..Ns"}

	got, err := renderPath("{{.CSharpNamespace}}/Program.cs", vars)
	if err != nil {
		t.Fatalf("renderPath() unexpected error = %v", err)
	}
	if got != "My..Ns/Program.cs" {
		t.Fatalf("renderPath() = %q, want %q", got, "My..Ns/Program.cs")
	}
}

func TestMapOutputPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{input: "claude", want: ".claude"},
		{input: "claude/settings.json", want: filepath.Join(".claude", "settings.json")},
		{input: "codex", want: ".codex"},
		{input: "codex/hooks.json", want: filepath.Join(".codex", "hooks.json")},
		{input: "opencode", want: ".opencode"},
		{input: "opencode/plugins/foo.js", want: filepath.Join(".opencode", "plugins/foo.js")},
		{input: "other/path", want: "other/path"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			got := mapOutputPath(tc.input)
			if got != tc.want {
				t.Fatalf("mapOutputPath(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}


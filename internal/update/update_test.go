package update

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"forge/internal/project"
)

type commandCall struct {
	Dir     string
	Step    string
	Command string
	Args    []string
}

type recordingCommandRunner struct {
	calls   []commandCall
	runFunc func(dir string, step string, command string, args ...string) error
}

func (r *recordingCommandRunner) Run(_ context.Context, dir string, step string, command string, args ...string) error {
	r.calls = append(r.calls, commandCall{Dir: dir, Step: step, Command: command, Args: append([]string(nil), args...)})
	if r.runFunc != nil {
		return r.runFunc(dir, step, command, args...)
	}
	return nil
}

type recordingGitRunner struct {
	operations []string
	cloneFunc  func(dir string)
}

var workingDirMu sync.Mutex
var stderrMu sync.Mutex

func (r *recordingGitRunner) Clone(_ context.Context, repo string, dir string) error {
	r.operations = append(r.operations, "clone:"+repo)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if r.cloneFunc != nil {
		r.cloneFunc(dir)
	}
	return nil
}

func (r *recordingGitRunner) Checkout(_ context.Context, dir string, ref string) error {
	r.operations = append(r.operations, "checkout:"+ref)
	return nil
}

func TestRunExecutesSingleStepRecipeInOrder(t *testing.T) {
	t.Parallel()

	assets := fstest.MapFS{
		"sources.yaml": {Data: []byte(`single-step:
  kind: scaffolder
  steps:
    - run: "cobra-cli init --pkg-name example.com/sample"
  gitignore: Go
  normalize: []
  resolved:
    ref: "v1.3.0"
    captured: "2026-06-20"
`)},
	}
	runner := &recordingCommandRunner{}
	git := &recordingGitRunner{}
	repoRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(repoRoot, "sources.yaml"), `single-step:
  kind: scaffolder
  steps:
    - run: "cobra-cli init --pkg-name example.com/sample"
  gitignore: Go
  normalize: []
  resolved:
    ref: "v1.3.0"
    captured: "2026-06-20"
`)

	withWorkingDir(t, repoRoot, func() {
		if err := Run(context.Background(), assets, "single-step", runner, git); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	})

	if len(git.operations) != 0 {
		t.Fatalf("git operations = %#v, want none", git.operations)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %#v, want 1 call", runner.calls)
	}
	call := runner.calls[0]
	if call.Command != "cobra-cli" {
		t.Fatalf("command = %q, want cobra-cli", call.Command)
	}
	if got := strings.Join(call.Args, " "); got != "init --pkg-name example.com/sample" {
		t.Fatalf("args = %q, want %q", got, "init --pkg-name example.com/sample")
	}
	if call.Dir == "" {
		t.Fatal("call dir is empty")
	}
}

func TestRunExecutesCheckoutStripAndRunRecipe(t *testing.T) {
	t.Parallel()

	assets := fstest.MapFS{
		"sources.yaml": {Data: []byte(`recipe-stack:
  kind: recipe
  steps:
    - checkout: "github.com/example/project-layout"
      ref: "abc123"
      strip: [".git", "README.md", "docs"]
    - run: "go mod init example.com/sample"
  gitignore: Go
  normalize: []
  resolved:
    ref: "abc123"
    captured: "2026-06-20"
`)},
	}
	git := &recordingGitRunner{cloneFunc: func(dir string) {
		mustWriteFile(t, filepath.Join(dir, "README.md"), "remove me")
		mustWriteFile(t, filepath.Join(dir, "docs", "guide.md"), "remove me")
		mustWriteFile(t, filepath.Join(dir, ".git", "HEAD"), "ref: refs/heads/main")
		mustWriteFile(t, filepath.Join(dir, "keep.txt"), "keep me")
	}}
	runner := &recordingCommandRunner{runFunc: func(dir string, _ string, command string, args ...string) error {
		if command != "go" {
			return errors.New("unexpected command")
		}
		assertMissingPath(t, filepath.Join(dir, "README.md"))
		assertMissingPath(t, filepath.Join(dir, "docs"))
		assertMissingPath(t, filepath.Join(dir, ".git"))
		if _, err := os.Stat(filepath.Join(dir, "keep.txt")); err != nil {
			t.Fatalf("keep.txt missing before run: %v", err)
		}
		return nil
	}}

	repoRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(repoRoot, "sources.yaml"), `recipe-stack:
  kind: recipe
  steps:
    - checkout: "github.com/example/project-layout"
      ref: "abc123"
      strip: [".git", "README.md", "docs"]
    - run: "go mod init example.com/sample"
  gitignore: Go
  normalize: []
  resolved:
    ref: "abc123"
    captured: "2026-06-20"
`)

	withWorkingDir(t, repoRoot, func() {
		if err := Run(context.Background(), assets, "recipe-stack", runner, git); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	})

	if got := strings.Join(git.operations, ","); got != "clone:github.com/example/project-layout,checkout:abc123" {
		t.Fatalf("git operations = %q", got)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %#v, want 1 call", runner.calls)
	}
	if runner.calls[0].Command != "go" {
		t.Fatalf("command = %q, want go", runner.calls[0].Command)
	}
	if got := strings.Join(runner.calls[0].Args, " "); got != "mod init example.com/sample" {
		t.Fatalf("args = %q, want %q", got, "mod init example.com/sample")
	}
}

func TestRunRefreshesCheckoutStackAndPreservesVendoredSentinel(t *testing.T) {
	t.Parallel()

	const checkoutSources = `recipe-stack:
  kind: recipe
  steps:
    - checkout: "github.com/example/project-layout"
      ref: "abc123"
      strip: [".git", "README.md"]
  gitignore: Go
  normalize:
    - type: line_endings
    - type: trailing_newline
  resolved:
    ref: "abc123"
    captured: "2026-06-20"
`

	assets := fstest.MapFS{
		"sources.yaml": {Data: []byte(checkoutSources)},
	}
	repoRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(repoRoot, "sources.yaml"), checkoutSources)
	mustWriteFile(t, filepath.Join(repoRoot, "templates", "golden", "recipe-stack", "cmd", "main.go"), "old\n")
	mustWriteFile(t, filepath.Join(repoRoot, "templates", "golden", "recipe-stack", "pkg", "skeleton", ".keep"), "")
	mustWriteFile(t, filepath.Join(repoRoot, "templates", "golden", "recipe-stack", "stale.txt"), "stale\n")

	git := &recordingGitRunner{cloneFunc: func(dir string) {
		mustWriteFile(t, filepath.Join(dir, ".git", "HEAD"), "ref: refs/heads/main")
		mustWriteFile(t, filepath.Join(dir, "README.md"), "remove me\n")
		mustWriteFile(t, filepath.Join(dir, "cmd", "main.go"), "package main\n")
		if err := os.MkdirAll(filepath.Join(dir, "pkg", "skeleton"), 0o755); err != nil {
			t.Fatalf("MkdirAll(empty vendored dir) error = %v", err)
		}
	}}
	runner := &recordingCommandRunner{}

	withWorkingDir(t, repoRoot, func() {
		if err := Run(context.Background(), assets, "recipe-stack", runner, git); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	})

	if got := strings.Join(git.operations, ","); got != "clone:github.com/example/project-layout,checkout:abc123" {
		t.Fatalf("git operations = %q", got)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner calls = %#v, want none", runner.calls)
	}
	if got := mustReadFile(t, filepath.Join(repoRoot, "templates", "golden", "recipe-stack", "cmd", "main.go")); got != "package main\n" {
		t.Fatalf("main.go = %q", got)
	}
	if got := mustReadFile(t, filepath.Join(repoRoot, "templates", "golden", "recipe-stack", "pkg", "skeleton", ".keep")); got != "" {
		t.Fatalf(".keep = %q, want empty sentinel", got)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "templates", "golden", "recipe-stack", "stale.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stale.txt should be removed, err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "templates", "golden", "recipe-stack", "README.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("README.md should be stripped, err = %v", err)
	}
}

func TestRunReturnsHelpfulErrorWhenToolIsMissing(t *testing.T) {
	t.Parallel()

	assets := fstest.MapFS{
		"sources.yaml": {Data: []byte(`single-step:
  kind: scaffolder
  steps:
    - run: "cobra-cli init --pkg-name example.com/sample"
  gitignore: Go
  normalize: []
  resolved:
    ref: "v1.3.0"
    captured: "2026-06-20"
`)},
	}
	runner := &recordingCommandRunner{runFunc: func(_ string, _ string, _ string, _ ...string) error {
		return exec.ErrNotFound
	}}
	repoRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(repoRoot, "sources.yaml"), `single-step:
  kind: scaffolder
  steps:
    - run: "cobra-cli init --pkg-name example.com/sample"
  gitignore: Go
  normalize: []
  resolved:
    ref: "v1.3.0"
    captured: "2026-06-20"
`)

	var err error
	withWorkingDir(t, repoRoot, func() {
		err = Run(context.Background(), assets, "single-step", runner, &recordingGitRunner{})
	})
	if err == nil {
		t.Fatal("Run() error = nil, want missing tool error")
	}
	for _, snippet := range []string{"single-step", "cobra-cli"} {
		if !strings.Contains(err.Error(), snippet) {
			t.Fatalf("Run() error = %q, want snippet %q", err, snippet)
		}
	}
}

func TestRunPreservesQuotedArguments(t *testing.T) {
	t.Parallel()

	assets := fstest.MapFS{
		"sources.yaml": {Data: []byte(`quoted-step:
  kind: scaffolder
  steps:
    - run: "custom-tool add 'quoted value'"
  gitignore: Go
  normalize: []
  resolved:
    ref: "custom-tool 1.0.0"
    captured: "2026-06-20"
`)},
	}
	runner := &recordingCommandRunner{}
	repoRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(repoRoot, "sources.yaml"), `quoted-step:
  kind: scaffolder
  steps:
    - run: "custom-tool add 'quoted value'"
  gitignore: Go
  normalize: []
  resolved:
    ref: "custom-tool 1.0.0"
    captured: "2026-06-20"
`)

	withWorkingDir(t, repoRoot, func() {
		if err := Run(context.Background(), assets, "quoted-step", runner, &recordingGitRunner{}); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	})
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %#v, want 1 call", runner.calls)
	}
	if got := strings.Join(runner.calls[0].Args, "|"); got != "add|quoted value" {
		t.Fatalf("args = %q, want %q", got, "add|quoted value")
	}
}

func TestRunFailsClearlyForUnsupportedNonGoStackBeforeWrites(t *testing.T) {
	t.Parallel()

	const pythonSources = `python-cli-typer:
  kind: scaffolder
  steps:
    - run: "uv init"
    - run: "uv add typer"
  gitignore: Python
  normalize:
    - type: line_endings
    - type: trailing_newline
  resolved:
    ref: "uv 0.11.8"
    captured: "2026-06-20"
`

	assets := fstest.MapFS{
		"sources.yaml": {Data: []byte(pythonSources)},
	}
	repoRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(repoRoot, "sources.yaml"), pythonSources)
	stackRoot := filepath.Join(repoRoot, "templates", "golden", "python-cli-typer")
	mustWriteFile(t, filepath.Join(stackRoot, "pyproject.toml.tmpl"), "name = \"{{.PythonPackage}}\"\n")
	beforeTree := captureTree(t, stackRoot)
	beforeSources := mustReadFile(t, filepath.Join(repoRoot, "sources.yaml"))
	runner := &recordingCommandRunner{}
	git := &recordingGitRunner{}

	var err error
	withWorkingDir(t, repoRoot, func() {
		err = Run(context.Background(), assets, "python-cli-typer", runner, git)
	})

	if err == nil {
		t.Fatal("Run() error = nil, want unsupported language failure")
	}
	for _, snippet := range []string{"Go stacks only", "python-cli-typer", "python"} {
		if !strings.Contains(err.Error(), snippet) {
			t.Fatalf("Run() error = %q, want snippet %q", err, snippet)
		}
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner calls = %#v, want none", runner.calls)
	}
	if len(git.operations) != 0 {
		t.Fatalf("git operations = %#v, want none", git.operations)
	}
	if afterTree := captureTree(t, stackRoot); !reflect.DeepEqual(afterTree, beforeTree) {
		t.Fatalf("python stack changed on unsupported refresh\nbefore=%#v\nafter=%#v", beforeTree, afterTree)
	}
	if afterSources := mustReadFile(t, filepath.Join(repoRoot, "sources.yaml")); afterSources != beforeSources {
		t.Fatalf("sources.yaml changed on unsupported refresh\nbefore=%s\nafter=%s", beforeSources, afterSources)
	}
}

func TestRunRefreshesRepresentativeStackAndPreservesOverlay(t *testing.T) {
	t.Parallel()

	assets := representativeStackAssets()
	repoRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(repoRoot, "sources.yaml"), embeddedRepresentativeSourcesYAML)
	seedRepresentativeTemplateContract(t, repoRoot)
	mustWriteFile(t, filepath.Join(repoRoot, "templates", "golden", "go-cli-cobra", "stale.txt"), "stale\n")
	overlayPath := filepath.Join(repoRoot, "templates", "golden", "go-cli-cobra", ".forge-overlay", "cmd", "root_test.go.tmpl")
	mustWriteFile(t, overlayPath, "package cmd\n")

	runner := newRepresentativeStackRunner(t)
	withWorkingDir(t, repoRoot, func() {
		if err := Run(context.Background(), assets, "go-cli-cobra", runner, &recordingGitRunner{}); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	})

	for _, relPath := range []string{
		"templates/golden/go-cli-cobra/go.mod.tmpl",
		"templates/golden/go-cli-cobra/main.go.tmpl",
		"templates/golden/go-cli-cobra/cmd/root.go.tmpl",
		"templates/golden/go-cli-cobra/cmd/serve.go.tmpl",
		"templates/golden/go-cli-cobra/cmd/config.go.tmpl",
	} {
		if _, err := os.Stat(filepath.Join(repoRoot, relPath)); err != nil {
			t.Fatalf("Stat(%s) error = %v", relPath, err)
		}
	}

	goMod := mustReadFile(t, filepath.Join(repoRoot, "templates", "golden", "go-cli-cobra", "go.mod.tmpl"))
	if !strings.Contains(goMod, "module {{.ModulePath}}") {
		t.Fatalf("go.mod.tmpl = %q, want ModulePath template", goMod)
	}

	mainGo := mustReadFile(t, filepath.Join(repoRoot, "templates", "golden", "go-cli-cobra", "main.go.tmpl"))
	if !strings.Contains(mainGo, `"{{.ModulePath}}/cmd"`) {
		t.Fatalf("main.go.tmpl = %q, want ModulePath import template", mainGo)
	}

	if got := mustReadFile(t, overlayPath); got != "package cmd\n" {
		t.Fatalf("overlay changed to %q", got)
	}

	if _, err := os.Stat(filepath.Join(repoRoot, "templates", "golden", "go-cli-cobra", "stale.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stale vanilla file should be removed, err = %v", err)
	}
}

func TestRunPreservesCustomStyledSourcesOnNoOpRefresh(t *testing.T) {
	t.Parallel()

	assets := representativeStackAssets()
	repoRoot := t.TempDir()
	beforeSources := `go-cli-cobra:
  kind: scaffolder
  steps:
    - run: 'cobra-cli init --pkg-name {{.ModulePath}}'
    - run: 'cobra-cli add serve'
    - run: 'cobra-cli add config'
  gitignore: Go
  normalize:
    - { type: line_endings }
    - { type: trailing_newline }
    - { type: sort_files }
  resolved:
    ref: 'v1.3.0'
    captured: '2026-06-20'
`
	mustWriteFile(t, filepath.Join(repoRoot, "sources.yaml"), beforeSources)
	seedRepresentativeNoOpVanilla(t, repoRoot)

	runner := newRepresentativeStackRunner(t)
	withWorkingDir(t, repoRoot, func() {
		if err := Run(context.Background(), assets, "go-cli-cobra", runner, &recordingGitRunner{}); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	})

	afterSources := mustReadFile(t, filepath.Join(repoRoot, "sources.yaml"))
	if afterSources != beforeSources {
		t.Fatalf("sources.yaml changed on no-op refresh\nbefore=%s\nafter=%s", beforeSources, afterSources)
	}
}

func TestCurrentDateStringUsesUTC(t *testing.T) {
	if got := currentDateStringAt(time.Date(2026, 6, 23, 23, 30, 0, 0, time.FixedZone("utc-minus-11", -11*60*60))); got != "2026-06-24" {
		t.Fatalf("currentDateStringAt() = %q, want UTC date 2026-06-24", got)
	}
}

func TestRunRepinsMutableSourcesWhenRepresentativeRefreshChanges(t *testing.T) {
	t.Parallel()

	assets := representativeStackAssets()
	repoRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(repoRoot, "sources.yaml"), `go-cli-cobra:
  kind: scaffolder
  steps:
    - run: "cobra-cli init --pkg-name {{.ModulePath}}"
    - run: "cobra-cli add serve"
    - run: "cobra-cli add config"
  gitignore: Go
  normalize:
    - type: line_endings
    - type: trailing_newline
    - type: sort_files
  resolved:
    ref: "stale-ref"
    captured: "2000-01-01"
`)
	seedRepresentativeTemplateContract(t, repoRoot)
	mustWriteFile(t, filepath.Join(repoRoot, "templates", "golden", "go-cli-cobra", "stale.txt"), "stale\n")

	runner := newRepresentativeStackRunner(t)
	withWorkingDir(t, repoRoot, func() {
		if err := Run(context.Background(), assets, "go-cli-cobra", runner, &recordingGitRunner{}); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	})

	sources := mustReadFile(t, filepath.Join(repoRoot, "sources.yaml"))
	if !strings.Contains(sources, "ref: \"v1.3.0\"") {
		t.Fatalf("sources.yaml = %q, want refreshed ref", sources)
	}
	if strings.Contains(sources, "ref: \"stale-ref\"") {
		t.Fatalf("sources.yaml = %q, want stale ref removed", sources)
	}
	if strings.Contains(sources, "captured: \"2000-01-01\"") {
		t.Fatalf("sources.yaml = %q, want updated captured date", sources)
	}
}

func TestRunRepinsInlineFlowResolvedRowWhenRepresentativeRefreshChanges(t *testing.T) {
	t.Parallel()

	assets := representativeStackAssets()
	repoRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(repoRoot, "sources.yaml"), `go-cli-cobra:
  kind: scaffolder
  steps:
    - run: "cobra-cli init --pkg-name {{.ModulePath}}"
    - run: "cobra-cli add serve"
    - run: "cobra-cli add config"
  gitignore: Go
  normalize:
    - type: line_endings
    - type: trailing_newline
    - type: sort_files
  resolved: { ref: "stale-ref", captured: "2000-01-01" }
`)
	seedRepresentativeTemplateContract(t, repoRoot)
	mustWriteFile(t, filepath.Join(repoRoot, "templates", "golden", "go-cli-cobra", "stale.txt"), "stale\n")

	runner := newRepresentativeStackRunner(t)
	withWorkingDir(t, repoRoot, func() {
		if err := Run(context.Background(), assets, "go-cli-cobra", runner, &recordingGitRunner{}); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	})

	sources := mustReadFile(t, filepath.Join(repoRoot, "sources.yaml"))
	if !strings.Contains(sources, `resolved: { ref: "v1.3.0", captured: "`) {
		t.Fatalf("sources.yaml = %q, want inline flow-style repin", sources)
	}
	if strings.Contains(sources, `resolved:\n`) || strings.Contains(sources, `resolved:\r\n`) {
		t.Fatalf("sources.yaml = %q, want flow-style resolved preserved", sources)
	}
	if strings.Contains(sources, "stale-ref") || strings.Contains(sources, "2000-01-01") {
		t.Fatalf("sources.yaml = %q, want stale inline resolved values removed", sources)
	}
}

func TestRunRepinsMutableRefWhenVanillaSnapshotIsUnchanged(t *testing.T) {
	t.Parallel()

	assets := representativeStackAssets()
	repoRoot := t.TempDir()
	beforeSources := `go-cli-cobra:
  kind: scaffolder
  steps:
    - run: "cobra-cli init --pkg-name {{.ModulePath}}"
    - run: "cobra-cli add serve"
    - run: "cobra-cli add config"
  gitignore: Go
  normalize:
    - type: line_endings
    - type: trailing_newline
    - type: sort_files
  resolved:
    ref: "stale-ref"
    captured: "2000-01-01"
`
	mustWriteFile(t, filepath.Join(repoRoot, "sources.yaml"), beforeSources)
	seedRepresentativeNoOpVanilla(t, repoRoot)

	runner := newRepresentativeStackRunner(t)
	withWorkingDir(t, repoRoot, func() {
		if err := Run(context.Background(), assets, "go-cli-cobra", runner, &recordingGitRunner{}); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	})

	afterSources := mustReadFile(t, filepath.Join(repoRoot, "sources.yaml"))
	if !strings.Contains(afterSources, `ref: "v1.3.0"`) {
		t.Fatalf("sources.yaml = %q, want embedded resolved.ref", afterSources)
	}
	if strings.Contains(afterSources, `ref: "stale-ref"`) {
		t.Fatalf("sources.yaml = %q, want stale ref removed", afterSources)
	}
	if !strings.Contains(afterSources, `captured: "2000-01-01"`) {
		t.Fatalf("sources.yaml = %q, want captured preserved on unchanged vanilla", afterSources)
	}
}

func TestRunIsIdempotentWhenRepresentativeStackIsUnchanged(t *testing.T) {
	t.Parallel()

	assets := representativeStackAssets()
	repoRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(repoRoot, "sources.yaml"), embeddedRepresentativeSourcesYAML)
	seedRepresentativeTemplateContract(t, repoRoot)
	mustWriteFile(t, filepath.Join(repoRoot, "templates", "golden", "go-cli-cobra", ".forge-overlay", "cmd", "root_test.go.tmpl"), "package cmd\n")

	runner := newRepresentativeStackRunner(t)
	withWorkingDir(t, repoRoot, func() {
		if err := Run(context.Background(), assets, "go-cli-cobra", runner, &recordingGitRunner{}); err != nil {
			t.Fatalf("first Run() error = %v", err)
		}
	})

	beforeTree := captureTree(t, filepath.Join(repoRoot, "templates", "golden", "go-cli-cobra"))
	beforeSources := mustReadFile(t, filepath.Join(repoRoot, "sources.yaml"))

	withWorkingDir(t, repoRoot, func() {
		if err := Run(context.Background(), assets, "go-cli-cobra", runner, &recordingGitRunner{}); err != nil {
			t.Fatalf("second Run() error = %v", err)
		}
	})

	afterTree := captureTree(t, filepath.Join(repoRoot, "templates", "golden", "go-cli-cobra"))
	afterSources := mustReadFile(t, filepath.Join(repoRoot, "sources.yaml"))
	if !reflect.DeepEqual(afterTree, beforeTree) {
		t.Fatalf("golden tree changed on second run\nbefore=%#v\nafter=%#v", beforeTree, afterTree)
	}
	if afterSources != beforeSources {
		t.Fatalf("sources.yaml changed on second run\nbefore=%s\nafter=%s", beforeSources, afterSources)
	}
}

func TestRunFailsLoudOnOrphanedOverlayPathAndWritesNothing(t *testing.T) {
	t.Parallel()

	assets := representativeStackAssets()
	repoRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(repoRoot, "sources.yaml"), embeddedRepresentativeSourcesYAML)
	beforeSources := mustReadFile(t, filepath.Join(repoRoot, "sources.yaml"))
	seedRepresentativeTemplateContract(t, repoRoot)
	rootGoPath := filepath.Join(repoRoot, "templates", "golden", "go-cli-cobra", "cmd", "root.go.tmpl")
	mustWriteFile(t, rootGoPath, "before\n")
	overlayPath := filepath.Join(repoRoot, "templates", "golden", "go-cli-cobra", ".forge-overlay", "cmd", "root_test.go.tmpl")
	mustWriteFile(t, overlayPath, "package cmd\n")

	runner := &recordingCommandRunner{runFunc: func(dir string, step string, command string, args ...string) error {
		if err := assertRenderedRepresentativeCommand(step, command, args...); err != nil {
			return err
		}
		switch {
		case len(args) >= 1 && args[0] == "init":
			modulePath := lastArgValue(args, "--pkg-name")
			mustWriteFile(t, filepath.Join(dir, "go.mod"), "module "+modulePath+"\n")
			mustWriteFile(t, filepath.Join(dir, "main.go"), "package main\n")
		case len(args) >= 2 && args[0] == "add" && args[1] == "serve":
			return nil
		case len(args) >= 2 && args[0] == "add" && args[1] == "config":
			return nil
		default:
			return errors.New("unexpected command")
		}
		return nil
	}}

	var err error
	withWorkingDir(t, repoRoot, func() {
		err = Run(context.Background(), assets, "go-cli-cobra", runner, &recordingGitRunner{})
	})

	if err == nil {
		t.Fatal("Run() error = nil, want orphan failure")
	}
	if !strings.Contains(err.Error(), "orphan") || !strings.Contains(err.Error(), "cmd/root_test.go.tmpl") {
		t.Fatalf("Run() error = %q, want orphan path details", err)
	}
	if got := mustReadFile(t, rootGoPath); got != "before\n" {
		t.Fatalf("root.go.tmpl changed to %q", got)
	}
	afterSources := mustReadFile(t, filepath.Join(repoRoot, "sources.yaml"))
	if afterSources != beforeSources {
		t.Fatalf("sources.yaml changed on orphan failure\nbefore=%s\nafter=%s", beforeSources, afterSources)
	}
}

func TestRunLeavesCommittedVanillaUntouchedWhenStagedReplaceFails(t *testing.T) {
	assets := representativeStackAssets()
	repoRoot := t.TempDir()
	sourcesPath := filepath.Join(repoRoot, "sources.yaml")
	mustWriteFile(t, sourcesPath, embeddedRepresentativeSourcesYAML)
	beforeSources := mustReadFile(t, sourcesPath)
	seedRepresentativeTemplateContract(t, repoRoot)
	rootGoPath := filepath.Join(repoRoot, "templates", "golden", "go-cli-cobra", "cmd", "root.go.tmpl")
	mustWriteFile(t, rootGoPath, "before\n")
	stalePath := filepath.Join(repoRoot, "templates", "golden", "go-cli-cobra", "stale.txt")
	mustWriteFile(t, stalePath, "stale\n")
	overlayPath := filepath.Join(repoRoot, "templates", "golden", "go-cli-cobra", ".forge-overlay", "cmd", "root_test.go.tmpl")
	mustWriteFile(t, overlayPath, "package cmd\n")
	beforeTree := captureTree(t, filepath.Join(repoRoot, "templates", "golden", "go-cli-cobra"))

	originalWriter := replaceVanillaWriter
	replaceVanillaWriter = func(name string, data []byte, perm os.FileMode) error {
		if strings.HasSuffix(filepath.ToSlash(name), "/main.go.tmpl") {
			return errors.New("boom")
		}
		return os.WriteFile(name, data, perm)
	}
	defer func() {
		replaceVanillaWriter = originalWriter
	}()

	runner := newRepresentativeStackRunner(t)
	var err error
	withWorkingDir(t, repoRoot, func() {
		err = Run(context.Background(), assets, "go-cli-cobra", runner, &recordingGitRunner{})
	})

	if err == nil {
		t.Fatal("Run() error = nil, want staged replacement failure")
	}
	if !strings.Contains(err.Error(), "write staged path \"main.go.tmpl\"") {
		t.Fatalf("Run() error = %q, want staged path failure", err)
	}
	if afterTree := captureTree(t, filepath.Join(repoRoot, "templates", "golden", "go-cli-cobra")); !reflect.DeepEqual(afterTree, beforeTree) {
		t.Fatalf("go vanilla tree changed on staged replace failure\nbefore=%#v\nafter=%#v", beforeTree, afterTree)
	}
	if afterSources := mustReadFile(t, sourcesPath); afterSources != beforeSources {
		t.Fatalf("sources.yaml changed on staged replace failure\nbefore=%s\nafter=%s", beforeSources, afterSources)
	}
	if got := mustReadFile(t, overlayPath); got != "package cmd\n" {
		t.Fatalf("overlay changed to %q", got)
	}
}

func TestRunLeavesVanillaUntouchedWhenSourcesRepinFails(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root bypasses the file permission bits this test uses to force a write failure")
	}

	assets := representativeStackAssets()
	repoRoot := t.TempDir()
	sourcesPath := filepath.Join(repoRoot, "sources.yaml")
	mustWriteFile(t, sourcesPath, embeddedRepresentativeSourcesYAML)
	beforeSources := mustReadFile(t, sourcesPath)
	seedRepresentativeTemplateContract(t, repoRoot)
	rootGoPath := filepath.Join(repoRoot, "templates", "golden", "go-cli-cobra", "cmd", "root.go.tmpl")
	mustWriteFile(t, rootGoPath, "before\n")
	stalePath := filepath.Join(repoRoot, "templates", "golden", "go-cli-cobra", "stale.txt")
	mustWriteFile(t, stalePath, "stale\n")
	if err := os.Chmod(sourcesPath, 0o444); err != nil {
		t.Fatalf("Chmod(%s) error = %v", sourcesPath, err)
	}
	if err := os.Chmod(repoRoot, 0o555); err != nil {
		t.Fatalf("Chmod(%s) error = %v", repoRoot, err)
	}
	defer func() {
		_ = os.Chmod(repoRoot, 0o755)
		_ = os.Chmod(sourcesPath, 0o644)
	}()

	runner := newRepresentativeStackRunner(t)
	var err error
	withWorkingDir(t, repoRoot, func() {
		err = Run(context.Background(), assets, "go-cli-cobra", runner, &recordingGitRunner{})
	})

	if err == nil {
		t.Fatal("Run() error = nil, want sources repin failure")
	}
	if !strings.Contains(err.Error(), "write mutable sources.yaml") {
		t.Fatalf("Run() error = %q, want mutable sources write failure", err)
	}
	if got := mustReadFile(t, rootGoPath); got != "before\n" {
		t.Fatalf("root.go.tmpl changed to %q", got)
	}
	if got := mustReadFile(t, sourcesPath); got != beforeSources {
		t.Fatalf("sources.yaml changed on repin failure\nbefore=%s\nafter=%s", beforeSources, got)
	}
	if got := mustReadFile(t, stalePath); got != "stale\n" {
		t.Fatalf("stale.txt changed to %q", got)
	}
}

func TestRunAllowsCollisionWhilePreservingOverlay(t *testing.T) {
	t.Parallel()

	assets := representativeStackAssets()
	repoRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(repoRoot, "sources.yaml"), embeddedRepresentativeSourcesYAML)
	seedRepresentativeTemplateContract(t, repoRoot)
	overlayPath := filepath.Join(repoRoot, "templates", "golden", "go-cli-cobra", ".forge-overlay", "cmd", "root.go.tmpl")
	mustWriteFile(t, overlayPath, "package cmd\n\nconst OverlayWins = true\n")

	runner := newRepresentativeStackRunner(t)
	var stderr string
	withWorkingDir(t, repoRoot, func() {
		stderr = withCapturedStderr(t, func() {
			if err := Run(context.Background(), assets, "go-cli-cobra", runner, &recordingGitRunner{}); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
		})
	})

	if !strings.Contains(stderr, "warning: overlay collision(s) for stack go-cli-cobra: cmd/root.go.tmpl") {
		t.Fatalf("stderr = %q, want collision warning", stderr)
	}

	rootGo := mustReadFile(t, filepath.Join(repoRoot, "templates", "golden", "go-cli-cobra", "cmd", "root.go.tmpl"))
	if !strings.Contains(rootGo, "var rootCmd") {
		t.Fatalf("root.go.tmpl = %q, want refreshed vanilla root command", rootGo)
	}
	if got := mustReadFile(t, overlayPath); got != "package cmd\n\nconst OverlayWins = true\n" {
		t.Fatalf("overlay changed to %q", got)
	}
}

const embeddedRepresentativeSourcesYAML = `go-cli-cobra:
  kind: scaffolder
  steps:
    - run: "cobra-cli init --pkg-name {{.ModulePath}}"
    - run: "cobra-cli add serve"
    - run: "cobra-cli add config"
  gitignore: Go
  normalize:
    - type: line_endings
    - type: trailing_newline
    - type: sort_files
  resolved:
    ref: "v1.3.0"
    captured: "2026-06-20"
`

func representativeStackAssets() fs.FS {
	return fstest.MapFS{
		"sources.yaml": {Data: []byte(embeddedRepresentativeSourcesYAML)},
	}
}

func seedRepresentativeTemplateContract(t *testing.T, repoRoot string) {
	t.Helper()

	for relPath, contents := range map[string]string{
		"templates/golden/go-cli-cobra/go.mod.tmpl":        "module {{.ModulePath}}\n",
		"templates/golden/go-cli-cobra/main.go.tmpl":       "package main\n",
		"templates/golden/go-cli-cobra/cmd/root.go.tmpl":   "package cmd\n",
		"templates/golden/go-cli-cobra/cmd/serve.go.tmpl":  "package cmd\n",
		"templates/golden/go-cli-cobra/cmd/config.go.tmpl": "package cmd\n",
	} {
		mustWriteFile(t, filepath.Join(repoRoot, relPath), contents)
	}
}

func seedRepresentativeNoOpVanilla(t *testing.T, repoRoot string) {
	t.Helper()

	for relPath, contents := range map[string]string{
		"templates/golden/go-cli-cobra/go.mod.tmpl": "module {{.ModulePath}}\n\ngo 1.26.2\n",
		"templates/golden/go-cli-cobra/main.go.tmpl": `package main

import (
	"fmt"
	"os"

	"{{.ModulePath}}/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
`,
		"templates/golden/go-cli-cobra/cmd/root.go.tmpl":   "package cmd\n\nvar rootCmd = struct{}{}\n",
		"templates/golden/go-cli-cobra/cmd/serve.go.tmpl":  "package cmd\n\nfunc serve() {}\n",
		"templates/golden/go-cli-cobra/cmd/config.go.tmpl": "package cmd\n\nfunc config() {}\n",
	} {
		mustWriteFile(t, filepath.Join(repoRoot, relPath), contents)
	}
}

func newRepresentativeStackRunner(t *testing.T) *recordingCommandRunner {
	t.Helper()

	return &recordingCommandRunner{runFunc: func(dir string, step string, command string, args ...string) error {
		if err := assertRenderedRepresentativeCommand(step, command, args...); err != nil {
			return err
		}

		switch {
		case len(args) >= 1 && args[0] == "init":
			modulePath := lastArgValue(args, "--pkg-name")
			if modulePath == "" {
				return errors.New("missing --pkg-name")
			}
			mustWriteFile(t, filepath.Join(dir, "go.mod"), "module "+modulePath+"\n\ngo 1.26.2\n")
			mustWriteFile(t, filepath.Join(dir, "main.go"), "package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n\n\t\""+modulePath+"/cmd\"\n)\n\nfunc main() {\n\tif err := cmd.Execute(); err != nil {\n\t\tfmt.Fprintln(os.Stderr, err)\n\t\tos.Exit(1)\n\t}\n}\n")
			mustWriteFile(t, filepath.Join(dir, "cmd", "root.go"), "package cmd\n\nvar rootCmd = struct{}{}\n")
		case len(args) >= 2 && args[0] == "add" && args[1] == "serve":
			mustWriteFile(t, filepath.Join(dir, "cmd", "serve.go"), "package cmd\n\nfunc serve() {}\n")
		case len(args) >= 2 && args[0] == "add" && args[1] == "config":
			mustWriteFile(t, filepath.Join(dir, "cmd", "config.go"), "package cmd\n\nfunc config() {}\n")
		default:
			return errors.New("unexpected representative stack command")
		}

		return nil
	}}
}

func assertRenderedRepresentativeCommand(step string, command string, args ...string) error {
	if command != "cobra-cli" {
		return errors.New("unexpected command")
	}

	joined := strings.Join(append([]string{command}, args...), " ")
	if strings.Contains(joined, "{{.") {
		return errors.New("unrendered placeholder in command")
	}

	switch {
	case strings.Contains(step, "step 1"):
		if len(args) < 3 || args[0] != "init" || args[1] != "--pkg-name" {
			return errors.New("unexpected init arguments")
		}
	case strings.Contains(step, "step 2"):
		if len(args) < 2 || args[0] != "add" || args[1] != "serve" {
			return errors.New("unexpected serve arguments")
		}
	case strings.Contains(step, "step 3"):
		if len(args) < 2 || args[0] != "add" || args[1] != "config" {
			return errors.New("unexpected config arguments")
		}
	}

	return nil
}

func lastArgValue(args []string, flag string) string {
	for index := 0; index < len(args)-1; index++ {
		if args[index] == flag {
			return args[index+1]
		}
	}
	return ""
}

func mustWriteFile(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", path, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}

	return string(data)
}

func assertMissingPath(t *testing.T, path string) {
	t.Helper()
	_, err := os.Stat(path)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Stat(%s) error = %v, want not exists", path, err)
	}
}

func captureTree(t *testing.T, root string) map[string]string {
	t.Helper()

	files := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relPath)] = mustReadFile(t, path)
		return nil
	}); err != nil {
		t.Fatalf("WalkDir(%s) error = %v", root, err)
	}

	return files
}

func withCapturedStderr(t *testing.T, run func()) string {
	t.Helper()

	stderrMu.Lock()
	defer stderrMu.Unlock()

	original := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	os.Stderr = writer
	defer func() {
		os.Stderr = original
	}()

	run()
	if err := writer.Close(); err != nil {
		t.Fatalf("stderr writer close error = %v", err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("stderr read error = %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("stderr reader close error = %v", err)
	}
	return string(data)
}

func withWorkingDir(t *testing.T, dir string, run func()) {
	t.Helper()

	workingDirMu.Lock()
	defer workingDirMu.Unlock()

	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir(%s) error = %v", dir, err)
	}
	defer func() {
		if err := os.Chdir(originalDir); err != nil {
			t.Fatalf("restore dir error = %v", err)
		}
	}()

	run()
}

func TestApplyTemplatePlaceholdersToPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		vars project.Variables
		want string
	}{
		{
			name: "replaces python package segment",
			path: "my_cool_api/__init__.py",
			vars: project.Variables{PythonPackage: "my_cool_api"},
			want: "{{.PythonPackage}}/__init__.py",
		},
		{
			name: "replaces nested python package segment",
			path: "src/my_cool_api/main.py",
			vars: project.Variables{PythonPackage: "my_cool_api"},
			want: "src/{{.PythonPackage}}/main.py",
		},
		{
			name: "replaces csharp namespace segment",
			path: "MyCoolApi/Program.cs",
			vars: project.Variables{CSharpNamespace: "MyCoolApi"},
			want: "{{.CSharpNamespace}}/Program.cs",
		},
		{
			name: "does not replace substring matches",
			path: "my_cool_api_extra/main.py",
			vars: project.Variables{PythonPackage: "my_cool_api"},
			want: "my_cool_api_extra/main.py",
		},
		{
			name: "leaves non-matching paths unchanged",
			path: "tests/test_health.py",
			vars: project.Variables{PythonPackage: "my_cool_api"},
			want: "tests/test_health.py",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := applyTemplatePlaceholdersToPath(tt.path, tt.vars)
			if got != tt.want {
				t.Errorf("applyTemplatePlaceholdersToPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

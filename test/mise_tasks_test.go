package test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// miseAllTaskNames lists the root mise.toml's contributor tasks (ADR-0022),
// mirrored here so a task silently dropped from mise.toml fails this test
// rather than the discrepancy going unnoticed.
var miseAllTaskNames = []string{
	"build",
	"test",
	"run",
	"install",
	"install:completions",
	"uninstall",
	"uninstall:completions",
	"clean",
}

func TestMiseTasksListAllEight(t *testing.T) {
	t.Parallel()

	lockMakefileTest(t)

	h := newMiseTaskHarness(t)
	output, err := h.runRaw(t, "tasks", "ls")
	if err != nil {
		t.Fatalf("mise tasks ls error = %v\n%s", err, output)
	}

	for _, name := range miseAllTaskNames {
		if !strings.Contains(output, name) {
			t.Errorf("mise tasks ls output missing task %q\n%s", name, output)
		}
	}
}

func TestMiseRunRunPassesArgsThrough(t *testing.T) {
	t.Parallel()

	lockMakefileTest(t)

	h := newMiseTaskHarness(t)
	output, err := h.runRaw(t, "run", "run", "--", "help")
	if err != nil {
		t.Fatalf("mise run run -- help error = %v\n%s", err, output)
	}

	for _, snippet := range []string{"Usage:", "forge [command]"} {
		if !strings.Contains(output, snippet) {
			t.Errorf("mise run run -- help output missing %q\n%s", snippet, output)
		}
	}
}

func TestMiseRunInstallLifecycle(t *testing.T) {
	t.Parallel()

	lockMakefileTest(t)

	repoRoot := repoRoot(t)
	h := newMiseTaskHarness(t)

	if output, err := h.runRaw(t, "run", "clean"); err != nil {
		t.Fatalf("mise run clean error = %v\n%s", err, output)
	}
	defer func() {
		if output, err := h.runRaw(t, "run", "clean"); err != nil {
			t.Fatalf("deferred mise run clean error = %v\n%s", err, output)
		}
	}()

	bindir := filepath.Join(t.TempDir(), "bin")
	completionsDir := filepath.Join(t.TempDir(), "zsh-completions")
	installedBinary := filepath.Join(bindir, "forge")
	installedCompletion := filepath.Join(completionsDir, "_forge")

	installEnv := append(append([]string{}, h.env...),
		"BINDIR="+bindir,
	)
	if output, err := h.runWithEnv(t, installEnv, "run", "install"); err != nil {
		t.Fatalf("mise run install error = %v\n%s", err, output)
	}

	info, err := os.Stat(installedBinary)
	if err != nil {
		t.Fatalf("Stat(%s) error = %v", installedBinary, err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("%s mode = %v, want executable bit", installedBinary, info.Mode())
	}

	completionsEnv := append(append([]string{}, h.env...),
		"FORGE_ZSH_COMPLETIONS_DIR="+completionsDir,
	)
	if output, err := h.runWithEnv(t, completionsEnv, "run", "install:completions"); err != nil {
		t.Fatalf("mise run install:completions error = %v\n%s", err, output)
	}

	completionData, err := os.ReadFile(installedCompletion)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", installedCompletion, err)
	}
	if !strings.HasPrefix(string(completionData), "#compdef forge") {
		t.Fatalf("%s does not start with %q:\n%s", installedCompletion, "#compdef forge", completionData)
	}

	if output, err := h.runWithEnv(t, installEnv, "run", "uninstall"); err != nil {
		t.Fatalf("mise run uninstall error = %v\n%s", err, output)
	}
	if _, err := os.Stat(installedBinary); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(%s) error = %v, want not exists", installedBinary, err)
	}

	if output, err := h.runWithEnv(t, completionsEnv, "run", "uninstall:completions"); err != nil {
		t.Fatalf("mise run uninstall:completions error = %v\n%s", err, output)
	}
	if _, err := os.Stat(installedCompletion); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(%s) error = %v, want not exists", installedCompletion, err)
	}

	// build's own bin/forge (a side effect of install's `depends = ["build"]`)
	// lives in the repo itself, like the Makefile's build target; the
	// deferred `mise run clean` above removes it.
	if _, err := os.Stat(filepath.Join(repoRoot, "bin", "forge")); err != nil {
		t.Fatalf("Stat(repo bin/forge) error = %v, want the install task's build dependency to have built it", err)
	}
}

// miseTaskHarnessLeakedEnvKeys lists caller-environment keys that must not
// leak into task bodies from os.Environ(): each has a default that only
// resolves correctly relative to the fake HOME set up below, so an inherited
// value from the developer's real shell would silently redirect installs
// or completions outside the isolated harness. Individual tests add these
// back deliberately (see TestMiseRunInstallLifecycle).
var miseTaskHarnessLeakedEnvKeys = []string{
	"BINDIR",
	"FORGE_ZSH_COMPLETIONS_DIR",
	"XDG_DATA_HOME",
}

// newMiseTaskHarness returns an isolated environment for exercising the root
// mise.toml's tasks directly against the real repository checkout (mirroring
// how the Makefile tests run `make` against repoRoot). It skips the test
// when mise is not on PATH. HOME and every mise state/cache/config directory
// are redirected under a fresh t.TempDir() so the tests never read or write
// the real $HOME; PATH is inherited so `go build`/`go test` inside task
// bodies keep working, but GOPATH and GOMODCACHE are resolved from the real
// `go env` (before HOME is swapped) and set explicitly, rather than left
// unset: an unset GOPATH/GOMODCACHE makes Go derive the module cache from
// HOME, which under the fake HOME here would force a fresh, network-dependent
// module download on every test run instead of reusing the real cache.
func newMiseTaskHarness(t *testing.T) *miseTaskHarness {
	t.Helper()

	misePath, err := exec.LookPath("mise")
	if err != nil {
		t.Skip("mise not installed; skipping mise task test")
	}

	goEnvOutput, err := exec.Command("go", "env", "GOPATH", "GOMODCACHE").Output()
	if err != nil {
		t.Skipf("go env GOPATH GOMODCACHE failed: %v; skipping mise task test", err)
	}
	goEnvLines := strings.Split(strings.TrimRight(string(goEnvOutput), "\n"), "\n")
	if len(goEnvLines) != 2 {
		t.Skipf("go env GOPATH GOMODCACHE: unexpected output %q; skipping mise task test", goEnvOutput)
	}
	goPath, goModCache := goEnvLines[0], goEnvLines[1]

	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", home, err)
	}

	repoRoot := repoRoot(t)
	env := os.Environ()
	for _, key := range miseTaskHarnessLeakedEnvKeys {
		env = filterEnvKey(env, key)
	}
	env = append(env,
		"HOME="+home,
		"GOPATH="+goPath,
		"GOMODCACHE="+goModCache,
		"MISE_DATA_DIR="+filepath.Join(root, "mise-data"),
		"MISE_CONFIG_DIR="+filepath.Join(root, "mise-config"),
		"MISE_CACHE_DIR="+filepath.Join(root, "mise-cache"),
		"MISE_STATE_DIR="+filepath.Join(root, "mise-state"),
		"MISE_TRUSTED_CONFIG_PATHS="+repoRoot,
		"MISE_YES=1",
		// bats ([tools] bats = "latest") requires a network fetch to resolve
		// "latest" and is not needed to run any task below; keep these tests
		// offline and never install it.
		"MISE_OFFLINE=1",
		"MISE_AUTO_INSTALL=0",
		"MISE_TASK_RUN_AUTO_INSTALL=0",
		"MISE_NOT_FOUND_AUTO_INSTALL=0",
	)

	return &miseTaskHarness{mise: misePath, repoRoot: repoRoot, env: env}
}

// filterEnvKey returns env with every entry for key removed.
func filterEnvKey(env []string, key string) []string {
	prefix := key + "="
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

type miseTaskHarness struct {
	mise     string
	repoRoot string
	env      []string
}

// runRaw runs `mise <args...>` from the repo root under the harness's
// isolated environment.
func (h *miseTaskHarness) runRaw(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return h.runWithEnv(t, h.env, args...)
}

// runWithEnv is runRaw with a caller-supplied environment (e.g. to add
// BINDIR or FORGE_ZSH_COMPLETIONS_DIR for one invocation).
func (h *miseTaskHarness) runWithEnv(t *testing.T, env []string, args ...string) (string, error) {
	t.Helper()

	cmd := exec.Command(h.mise, args...)
	cmd.Dir = h.repoRoot
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	return string(output), err
}

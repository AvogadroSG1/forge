package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// buildForgeBinary compiles the CLI once per test into its own temp dir.
func buildForgeBinary(t *testing.T, buildDir string) string {
	t.Helper()

	binaryPath := filepath.Join(buildDir, "forge")
	if runtime.GOOS == "windows" {
		binaryPath += ".exe"
	}

	build := exec.Command("go", "build", "-o", binaryPath, ".")
	build.Dir = "."
	build.Env = append(os.Environ(), "GOCACHE="+filepath.Join(buildDir, "go-build-cache"))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build error = %v\n%s", err, output)
	}

	return binaryPath
}

const miseStubScript = `#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  trust)
    exit 0
    ;;
  install)
    exit 0
    ;;
  exec)
    shift
    if [ "$1" = "--" ]; then shift; fi
    exec "$@"
    ;;
  run)
    task="${2:-}"
    if [ -z "$task" ]; then
      echo "missing task" >&2
      exit 64
    fi
    command_line="$(python3 - "$task" <<'PY'
import pathlib
import re
import sys

task = sys.argv[1]
text = pathlib.Path("mise.toml").read_text()
pattern = rf"(?ms)^\[tasks\.{re.escape(task)}\]\s*\nrun = \"([^\"]+)\""
match = re.search(pattern, text)
if not match:
    raise SystemExit(1)
print(match.group(1))
PY
)"
    bash -c "$command_line"
    ;;
  *)
    echo "unsupported mise command: $*" >&2
    exit 64
    ;;
esac
`

// writeFrontendStubs writes the delegate stubs plus an npm stub that logs
// every invocation, so the hermetic tier proves the wiring without network.
func writeFrontendStubs(t *testing.T, stubDir string, npmLogPath string) {
	t.Helper()

	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	writeExecutable(t, filepath.Join(stubDir, "bd"), "#!/usr/bin/env bash\nset -euo pipefail\nexit 0\n")
	writeExecutable(t, filepath.Join(stubDir, "instill"), "#!/usr/bin/env bash\nset -euo pipefail\nexit 0\n")
	writeExecutable(t, filepath.Join(stubDir, "lefthook"), "#!/usr/bin/env bash\nset -euo pipefail\nexit 0\n")
	writeExecutable(t, filepath.Join(stubDir, "golangci-lint"), "#!/usr/bin/env bash\nset -euo pipefail\nexit 0\n")
	writeExecutable(t, filepath.Join(stubDir, "govulncheck"), "#!/usr/bin/env bash\nset -euo pipefail\nexit 0\n")
	writeExecutable(t, filepath.Join(stubDir, "npm"), "#!/usr/bin/env bash\nset -euo pipefail\necho \"npm $*\" >> \""+npmLogPath+"\"\nexit 0\n")
	writeExecutable(t, filepath.Join(stubDir, "mise"), miseStubScript)
}

func TestWalkingSkeletonFullstackGoViteComposesAndRunsCI(t *testing.T) {
	t.Parallel()

	buildDir := t.TempDir()
	binaryPath := buildForgeBinary(t, buildDir)
	stubDir := filepath.Join(buildDir, "bin")
	npmLog := filepath.Join(buildDir, "npm.log")
	writeFrontendStubs(t, stubDir, npmLog)

	targetDir := filepath.Join(buildDir, "full-app")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	pathEnv := stubDir + string(os.PathListSeparator) + os.Getenv("PATH")

	initCmd := exec.Command(binaryPath,
		"init",
		"--project-name", "Full App",
		"--language", "go",
		"--project-type", "fullstack",
		"--stack", "go-api-chi",
		"--frontend", "vite-ts",
		"--author-name", "Ada Lovelace",
		"--author-email", "ada@example.com",
		"--remote", "none",
	)
	initCmd.Dir = targetDir
	initCmd.Env = append(os.Environ(), "PATH="+pathEnv)
	if output, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("forge init error = %v\n%s", err, output)
	}

	for _, rel := range []string{
		"mise.toml",
		"lefthook.yml",
		".github/workflows/ci.yml",
		"go.mod",
		"cmd/api/main.go",
		"web/package.json",
		"web/src/api/client.ts",
		"web/src/api/client.test.ts",
		"web/vitest.config.ts",
	} {
		if _, err := os.Stat(filepath.Join(targetDir, rel)); err != nil {
			t.Fatalf("Stat(%s) error = %v", rel, err)
		}
	}
	for _, forbidden := range []string{"web/mise.toml", "web/lefthook.yml", "web/.github"} {
		if _, err := os.Stat(filepath.Join(targetDir, forbidden)); !os.IsNotExist(err) {
			t.Fatalf("fragment gate file %s should not exist (err = %v)", forbidden, err)
		}
	}

	miseData, err := os.ReadFile(filepath.Join(targetDir, "mise.toml"))
	if err != nil {
		t.Fatalf("ReadFile(mise.toml) error = %v", err)
	}
	miseText := string(miseData)
	for _, snippet := range []string{"[tasks.web-fmt]", "[tasks.web-lint]", "[tasks.web-test]"} {
		if !strings.Contains(miseText, snippet) {
			t.Fatalf("root mise.toml missing %q:\n%s", snippet, miseText)
		}
	}

	clientData, err := os.ReadFile(filepath.Join(targetDir, "web", "src", "api", "client.ts"))
	if err != nil {
		t.Fatalf("ReadFile(client.ts) error = %v", err)
	}
	if !strings.Contains(string(clientData), "http://localhost:8080") {
		t.Fatalf("client.ts missing derived API base URL:\n%s", clientData)
	}

	npmLogData, err := os.ReadFile(npmLog)
	if err != nil {
		t.Fatalf("ReadFile(npm log) error = %v", err)
	}
	if !strings.Contains(string(npmLogData), "npm --prefix web install") {
		t.Fatalf("init did not npm install the web fragment:\n%s", npmLogData)
	}

	ciCmd := exec.Command(filepath.Join(stubDir, "mise"), "run", "ci")
	ciCmd.Dir = targetDir
	ciCmd.Env = append(os.Environ(), "PATH="+pathEnv)
	if output, err := ciCmd.CombinedOutput(); err != nil {
		t.Fatalf("mise run ci error = %v\n%s", err, output)
	}
}

func TestWalkingSkeletonStandaloneViteComposesAndRunsCI(t *testing.T) {
	t.Parallel()

	buildDir := t.TempDir()
	binaryPath := buildForgeBinary(t, buildDir)
	stubDir := filepath.Join(buildDir, "bin")
	npmLog := filepath.Join(buildDir, "npm.log")
	writeFrontendStubs(t, stubDir, npmLog)

	targetDir := filepath.Join(buildDir, "web-app")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	pathEnv := stubDir + string(os.PathListSeparator) + os.Getenv("PATH")

	// --project-type is deliberately omitted: typescript has exactly one
	// project type and must auto-select it, even without a TTY.
	initCmd := exec.Command(binaryPath,
		"init",
		"--project-name", "Web App",
		"--language", "typescript",
		"--stack", "vite-ts",
		"--api-base-url", "https://api.example.com",
		"--author-name", "Ada Lovelace",
		"--author-email", "ada@example.com",
		"--remote", "none",
	)
	initCmd.Dir = targetDir
	initCmd.Env = append(os.Environ(), "PATH="+pathEnv)
	if output, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("forge init error = %v\n%s", err, output)
	}

	for _, rel := range []string{
		"mise.toml",
		"lefthook.yml",
		".github/workflows/ci.yml",
		"package.json",
		"src/api/client.ts",
		"src/api/client.test.ts",
		"src/pages/home.ts",
		"eslint.config.js",
		"vitest.config.ts",
	} {
		if _, err := os.Stat(filepath.Join(targetDir, rel)); err != nil {
			t.Fatalf("Stat(%s) error = %v", rel, err)
		}
	}

	clientData, err := os.ReadFile(filepath.Join(targetDir, "src", "api", "client.ts"))
	if err != nil {
		t.Fatalf("ReadFile(client.ts) error = %v", err)
	}
	if !strings.Contains(string(clientData), "https://api.example.com") {
		t.Fatalf("client.ts missing configured API base URL:\n%s", clientData)
	}

	gitignoreData, err := os.ReadFile(filepath.Join(targetDir, ".gitignore"))
	if err != nil {
		t.Fatalf("ReadFile(.gitignore) error = %v", err)
	}
	if !strings.Contains(string(gitignoreData), "# ===== node gitignore =====") {
		t.Fatalf(".gitignore missing node section:\n%s", gitignoreData)
	}

	ciCmd := exec.Command(filepath.Join(stubDir, "mise"), "run", "ci")
	ciCmd.Dir = targetDir
	ciCmd.Env = append(os.Environ(), "PATH="+pathEnv)
	if output, err := ciCmd.CombinedOutput(); err != nil {
		t.Fatalf("mise run ci error = %v\n%s", err, output)
	}
}

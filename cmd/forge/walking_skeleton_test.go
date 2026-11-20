package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWalkingSkeletonGoCLIInitRunsThroughMiseCI(t *testing.T) {
	t.Parallel()

	buildDir := t.TempDir()
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

	stubDir := filepath.Join(buildDir, "bin")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	writeExecutable(t, filepath.Join(stubDir, "bd"), "#!/usr/bin/env bash\nset -euo pipefail\nexit 0\n")
	writeExecutable(t, filepath.Join(stubDir, "instill"), "#!/usr/bin/env bash\nset -euo pipefail\nexit 0\n")
	writeExecutable(t, filepath.Join(stubDir, "lefthook"), "#!/usr/bin/env bash\nset -euo pipefail\nexit 0\n")
	writeExecutable(t, filepath.Join(stubDir, "golangci-lint"), "#!/usr/bin/env bash\nset -euo pipefail\nexit 0\n")
	writeExecutable(t, filepath.Join(stubDir, "govulncheck"), "#!/usr/bin/env bash\nset -euo pipefail\nexit 0\n")
	writeExecutable(t, filepath.Join(stubDir, "hadolint"), "#!/usr/bin/env bash\nset -euo pipefail\nexit 0\n")
	writeExecutable(t, filepath.Join(stubDir, "mise"), `#!/usr/bin/env bash
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
pattern = rf"(?m)^\[tasks\.\"?{re.escape(task)}\"?\]\s*\n(?:description = .*\n)?run = \"([^\"]+)\""
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
`)

	targetDir := filepath.Join(buildDir, "sample-app")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	pathEnv := stubDir + string(os.PathListSeparator) + os.Getenv("PATH")

	initCmd := exec.Command(binaryPath,
		"init",
		"--project-name", "Sample App",
		"--language", "go",
		"--project-type", "cli",
		"--stack", "go-cli-cobra",
		"--author-name", "Ada Lovelace",
		"--author-email", "ada@example.com",
		"--remote", "none",
	)
	initCmd.Dir = targetDir
	initCmd.Env = append(os.Environ(), "PATH="+pathEnv)
	if output, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("forge init error = %v\n%s", err, output)
	}

	for _, rel := range []string{"mise.toml", "lefthook.yml", ".github/workflows/ci.yml", "cmd/root_test.go"} {
		if _, err := os.Stat(filepath.Join(targetDir, rel)); err != nil {
			t.Fatalf("Stat(%s) error = %v", rel, err)
		}
	}

	installCmd := exec.Command(filepath.Join(stubDir, "mise"), "install")
	installCmd.Dir = targetDir
	installCmd.Env = append(os.Environ(), "PATH="+pathEnv)
	if output, err := installCmd.CombinedOutput(); err != nil {
		t.Fatalf("mise install error = %v\n%s", err, output)
	}

	ciCmd := exec.Command(filepath.Join(stubDir, "mise"), "run", "ci")
	ciCmd.Dir = targetDir
	ciCmd.Env = append(os.Environ(), "PATH="+pathEnv)
	if output, err := ciCmd.CombinedOutput(); err != nil {
		t.Fatalf("mise run ci error = %v\n%s", err, output)
	}
}

func writeExecutable(t *testing.T, path string, contents string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}

func TestWalkingSkeletonFixtureDefinesGoGateTasks(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(filepath.Join("..", "..", "templates", "golden", "go-cli-cobra", ".forge-overlay", "mise.toml"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	text := string(data)
	for _, snippet := range []string{"[tasks.fmt]", "[tasks.lint]", "[tasks.test]", "[tasks.ci]", "govulncheck ./...", "mise run image:lint"} {
		if !strings.Contains(text, snippet) {
			t.Fatalf("go-cli-cobra mise.toml missing %q", snippet)
		}
	}
}

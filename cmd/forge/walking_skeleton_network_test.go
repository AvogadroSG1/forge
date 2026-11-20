package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The network tier proves the real JS toolchain (npm install, vitest,
// eslint, tsc/svelte-check, prettier) goes green on a scaffolded repo. It
// mirrors how the full golden-path smoke gates on gh credentials (SPEC
// §16.2): opt in with FORGE_SMOKE_NETWORK=1.
func requireNetworkSmoke(t *testing.T) {
	t.Helper()

	if os.Getenv("FORGE_SMOKE_NETWORK") != "1" {
		t.Skip("set FORGE_SMOKE_NETWORK=1 to run the networked JS toolchain smoke")
	}
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm is not installed")
	}
}

// writeNetworkStubs stubs only the delegate and gate tools that are not
// under test; npm and node stay real.
func writeNetworkStubs(t *testing.T, stubDir string) {
	t.Helper()

	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	writeExecutable(t, filepath.Join(stubDir, "bd"), "#!/usr/bin/env bash\nset -euo pipefail\nexit 0\n")
	writeExecutable(t, filepath.Join(stubDir, "instill"), "#!/usr/bin/env bash\nset -euo pipefail\nexit 0\n")
	writeExecutable(t, filepath.Join(stubDir, "lefthook"), "#!/usr/bin/env bash\nset -euo pipefail\nexit 0\n")
	writeExecutable(t, filepath.Join(stubDir, "golangci-lint"), "#!/usr/bin/env bash\nset -euo pipefail\nexit 0\n")
	writeExecutable(t, filepath.Join(stubDir, "govulncheck"), "#!/usr/bin/env bash\nset -euo pipefail\nexit 0\n")
	writeExecutable(t, filepath.Join(stubDir, "hadolint"), "#!/usr/bin/env bash\nset -euo pipefail\nexit 0\n")
	writeExecutable(t, filepath.Join(stubDir, "mise"), miseStubScript)
}

func runForgeInit(t *testing.T, binaryPath string, targetDir string, pathEnv string, args ...string) {
	t.Helper()

	initCmd := exec.Command(binaryPath, append([]string{"init"}, args...)...)
	initCmd.Dir = targetDir
	initCmd.Env = append(os.Environ(), "PATH="+pathEnv)
	if output, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("forge init error = %v\n%s", err, output)
	}
}

func runMiseTask(t *testing.T, stubDir string, targetDir string, pathEnv string, task string) {
	t.Helper()

	cmd := exec.Command(filepath.Join(stubDir, "mise"), "run", task)
	cmd.Dir = targetDir
	cmd.Env = append(os.Environ(), "PATH="+pathEnv)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mise run %s error = %v\n%s", task, err, output)
	}
	if strings.Contains(string(output), "<no value>") {
		t.Fatalf("mise run %s output has template residue:\n%s", task, output)
	}
}

func TestNetworkSmokeFullstackGoViteRunsRealJSGates(t *testing.T) {
	requireNetworkSmoke(t)
	t.Parallel()

	buildDir := t.TempDir()
	binaryPath := buildForgeBinary(t, buildDir)
	stubDir := filepath.Join(buildDir, "bin")
	writeNetworkStubs(t, stubDir)

	targetDir := filepath.Join(buildDir, "full-app")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	pathEnv := stubDir + string(os.PathListSeparator) + os.Getenv("PATH")

	runForgeInit(t, binaryPath, targetDir, pathEnv,
		"--project-name", "Full App",
		"--language", "go",
		"--project-type", "fullstack",
		"--stack", "go-api-chi",
		"--frontend", "vite-ts",
		"--author-name", "Ada Lovelace",
		"--author-email", "ada@example.com",
		"--remote", "none",
	)

	if _, err := os.Stat(filepath.Join(targetDir, "web", "node_modules")); err != nil {
		t.Fatalf("web/node_modules missing after init (npm install did not run): %v", err)
	}

	for _, task := range []string{"web-fmt", "web-lint", "web-test", "test"} {
		runMiseTask(t, stubDir, targetDir, pathEnv, task)
	}
}

func TestNetworkSmokeStandaloneSvelteKitRunsRealJSGates(t *testing.T) {
	requireNetworkSmoke(t)
	t.Parallel()

	buildDir := t.TempDir()
	binaryPath := buildForgeBinary(t, buildDir)
	stubDir := filepath.Join(buildDir, "bin")
	writeNetworkStubs(t, stubDir)

	targetDir := filepath.Join(buildDir, "svelte-app")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	pathEnv := stubDir + string(os.PathListSeparator) + os.Getenv("PATH")

	runForgeInit(t, binaryPath, targetDir, pathEnv,
		"--project-name", "Svelte App",
		"--language", "typescript",
		"--stack", "sveltekit",
		"--api-base-url", "https://api.example.com",
		"--author-name", "Ada Lovelace",
		"--author-email", "ada@example.com",
		"--remote", "none",
	)

	for _, task := range []string{"fmt", "lint", "test"} {
		runMiseTask(t, stubDir, targetDir, pathEnv, task)
	}
}

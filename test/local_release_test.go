//go:build integration

package test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

const stepTimeout = 5 * time.Minute

var forgeBinary string
var runtimeWorkspaceRoot string

func TestMain(m *testing.M) {
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "getwd: %v\n", err)
		os.Exit(1)
	}

	repoRoot := filepath.Clean(filepath.Join(wd, ".."))
	runtimeWorkspaceRoot = repoRoot
	tmpDir, err := newWorkspaceRuntimeDir(repoRoot, "forge-verify")
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating temp dir: %v\n", err)
		os.Exit(1)
	}
	if _, err := stageToolBinary(filepath.Join(tmpDir, "bin"), "mise"); err != nil {
		fmt.Fprintf(os.Stderr, "staging mise binary: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("PATH", filepath.Join(tmpDir, "bin")+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
		fmt.Fprintf(os.Stderr, "setting PATH: %v\n", err)
		os.Exit(1)
	}
	ensureProcessToolPath("dotnet")

	binaryPath := filepath.Join(tmpDir, "forge")
	if runtime.GOOS == "windows" {
		binaryPath += ".exe"
	}

	build := exec.Command("go", "build", "-o", binaryPath, "./cmd/forge")
	build.Dir = repoRoot
	build.Env = append(os.Environ(), "GOCACHE="+filepath.Join(tmpDir, "go-build-cache"))
	if output, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building forge: %v\n%s\n", err, output)
		os.Exit(1)
	}

	forgeBinary = binaryPath

	code := m.Run()
	if err := os.RemoveAll(tmpDir); err != nil {
		fmt.Fprintf(os.Stderr, "removing temp dir: %v\n", err)
	}
	os.Exit(code)
}

func TestLocalRelease(t *testing.T) {
	t.Parallel()

	type stackCase struct {
		name        string
		language    string
		projectType string
		stack       string
	}

	stacks := []stackCase{
		{
			name:        "go-cli-cobra",
			language:    "go",
			projectType: "cli",
			stack:       "go-cli-cobra",
		},
		{
			name:        "python-cli-typer",
			language:    "python",
			projectType: "cli",
			stack:       "python-cli-typer",
		},
		{
			name:        "csharp-cli",
			language:    "csharp",
			projectType: "cli",
			stack:       "csharp-cli",
		},
		{
			name:        "go-api-chi",
			language:    "go",
			projectType: "api",
			stack:       "go-api-chi",
		},
		{
			name:        "python-fastapi",
			language:    "python",
			projectType: "api",
			stack:       "python-fastapi",
		},
		{
			name:        "csharp-webapi",
			language:    "csharp",
			projectType: "api",
			stack:       "csharp-webapi",
		},
	}

	for _, stack := range stacks {
		stack := stack
		t.Run(stack.name, func(t *testing.T) {
			t.Parallel()

			// Outside the forge checkout: bd walks up to the nearest
			// .beads/config.yaml, and forge's own would make "bd init"
			// clone forge's issue database into the generated repo.
			dir := tempRuntimeDirIn(t, "")
			envRoot := tempRuntimeDirIn(t, "")

			runStep(
				t,
				"forge init",
				dir,
				envRoot,
				forgeBinary,
				"init",
				"--project-name", "Verify "+stack.name,
				"--language", stack.language,
				"--project-type", stack.projectType,
				"--stack", stack.stack,
				"--author-name", "CI Bot",
				"--author-email", "ci@example.com",
				"--remote", "none",
			)
			assertEmptyIssueDatabase(t, dir)
			runStep(t, "mise install", dir, envRoot, "mise", "install")
			runStep(t, "mise run ci", dir, envRoot, "mise", "run", "ci")

			if stack.stack == "csharp-webapi" {
				verifyCSharpWebAPIRuntime(t, dir, envRoot)
			}
		})
	}
}

func assertEmptyIssueDatabase(t *testing.T, dir string) {
	t.Helper()

	cmd := exec.Command("bd", "list", "--all", "--json")
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("bd list --all --json: %v\n--- stdout ---\n%s\n--- stderr ---\n%s", err, stdout.String(), stderr.String())
	}

	var issues []json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &issues); err != nil {
		t.Fatalf("parse bd list --all --json output: %v\n--- stdout ---\n%s", err, stdout.String())
	}
	if len(issues) != 0 {
		t.Fatalf("generated repo starts with %d issues, want an empty issue database\n%s", len(issues), stdout.String())
	}
}

func tempRuntimeDir(t *testing.T) string {
	t.Helper()

	return tempRuntimeDirIn(t, runtimeWorkspaceRoot)
}

func tempRuntimeDirIn(t *testing.T, workspaceRoot string) string {
	t.Helper()

	var (
		dir string
		err error
	)
	if workspaceRoot != "" {
		dir, err = newWorkspaceRuntimeDir(workspaceRoot, "forge-runtime")
	} else {
		dir, err = os.MkdirTemp("", "forge-runtime-*")
	}
	if err != nil {
		t.Fatalf("creating runtime dir: %v", err)
	}

	t.Cleanup(func() {
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if d.Type()&fs.ModeSymlink != 0 {
				return nil
			}
			mode := os.FileMode(0o644)
			if d.IsDir() {
				mode = 0o755
			}
			_ = os.Chmod(path, mode)
			return nil
		})
		_ = os.RemoveAll(dir)
	})

	return dir
}

func runStep(t *testing.T, name string, dir string, envRoot string, command string, args ...string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), stepTimeout)
	defer cancel()

	if err := prepareCommandEnv(envRoot, command); err != nil {
		t.Fatalf("prepare command env: %v", err)
	}

	cmdEnv := commandEnv(envRoot, command)
	cmd := exec.CommandContext(ctx, resolveCommandPathForEnv(command, cmdEnv), args...)
	cmd.Dir = dir
	cmd.Env = cmdEnv
	output, err := cmd.CombinedOutput()
	if err == nil {
		return
	}

	exitCode := -1
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exitCode = exitErr.ExitCode()
	}

	treeOutput, treeErr := exec.Command(
		"find",
		dir,
		"-path",
		filepath.Join(dir, ".cache"),
		"-prune",
		"-o",
		"-type",
		"f",
		"-print",
	).CombinedOutput()
	if treeErr != nil {
		treeOutput = append(treeOutput, []byte(fmt.Sprintf("\nfind error: %v\n", treeErr))...)
	}

	t.Fatalf(
		"%s: exit %d (cmd: %s)\n--- output ---\n%s\n--- directory tree ---\n%s",
		name,
		exitCode,
		cmd.String(),
		output,
		treeOutput,
	)
}

func verifyCSharpWebAPIRuntime(t *testing.T, dir string, envRoot string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), stepTimeout)
	defer cancel()

	contract := csharpWebAPIRuntimeSmokeContract()
	if err := prepareCommandEnv(envRoot, contract.command); err != nil {
		t.Fatalf("prepare csharp-webapi runtime env: %v", err)
	}

	cmdEnv := append(commandEnv(envRoot, contract.command), contract.envOverrides...)
	cmd := exec.CommandContext(ctx, resolveCommandPathForEnv(contract.command, cmdEnv), contract.args...)
	cmd.Dir = dir
	cmd.Env = cmdEnv

	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	if err := cmd.Start(); err != nil {
		t.Fatalf("start csharp-webapi runtime smoke: %v", err)
	}

	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			Proxy: nil,
		},
	}

	deadline := time.Now().Add(30 * time.Second)
	var lastProbe string

	for time.Now().Before(deadline) {
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			t.Fatalf("csharp-webapi runtime smoke exited early\n--- output ---\n%s", output.String())
		}

		for _, url := range csharpWebAPIReadinessURLs(output.String(), contract) {
			response, probeErr := client.Get(url)
			if probeErr == nil {
				bodyBytes, readErr := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if readErr != nil {
					t.Fatalf("read csharp-webapi runtime smoke body: %v", readErr)
				}
				if response.StatusCode == http.StatusOK {
					if !bytes.Contains(bytes.ToLower(bodyBytes), []byte(contract.responseToken)) {
						t.Fatalf("csharp-webapi runtime smoke returned HTTP 200 without weather payload\n--- body ---\n%s\n--- output ---\n%s", bodyBytes, output.String())
					}
					return
				}
				lastProbe = fmt.Sprintf("%s -> status %d body=%s", url, response.StatusCode, bodyBytes)
				break
			}
			lastProbe = fmt.Sprintf("%s -> %s", url, probeErr)
		}

		time.Sleep(500 * time.Millisecond)
	}

	t.Fatalf("csharp-webapi runtime smoke did not become ready\nlast probe: %s\n--- output ---\n%s", lastProbe, output.String())
}

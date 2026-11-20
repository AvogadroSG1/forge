package test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forge"
	"forge/internal/project"
	"forge/internal/scaffold"
)

const (
	goImageBuildTimeout = 15 * time.Minute
	goImageRunTimeout   = 60 * time.Second
	goImageProbePort    = "9090"
)

// requireGoImageDocker skips unless the live image build was opted into, the
// same way the OTel lifecycle test is gated.
func requireGoImageDocker(t *testing.T) {
	t.Helper()

	if os.Getenv("FORGE_DOCKER_TESTS") != "1" {
		t.Skip("set FORGE_DOCKER_TESTS=1 to build the Go stack container images")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH; skipping Go stack image build")
	}
	if out, err := otelHostDocker("info", "--format", "{{.ServerVersion}}"); err != nil {
		t.Skipf("docker daemon not reachable (docker info: %v); skipping Go stack image build\n%s", err, out)
	}
}

func goImageCommand(t *testing.T, timeout time.Duration, dir, name string, args ...string) (string, error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mustGoImageCommand(t *testing.T, timeout time.Duration, dir, name string, args ...string) string {
	t.Helper()

	out, err := goImageCommand(t, timeout, dir, name, args...)
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return out
}

// TestGoStackContainerImagesBuildAndRun builds both targets of the shipped
// Dockerfile for every Go stack from a freshly scaffolded repo and checks the
// properties ADR-0023 promises: non-root, PORT from the environment, JSON
// logs, clean SIGTERM exit, and a usable toolchain in the sandbox.
//
// FORGE_DOCKER_BUILD_ARGS adds space-separated arguments to every
// `docker build`, for networks that need a registry mirror or a proxy.
func TestGoStackContainerImagesBuildAndRun(t *testing.T) {
	requireGoImageDocker(t)

	writer := scaffold.Writer{Assets: forge.Assets()}
	extraBuildArgs := strings.Fields(os.Getenv("FORGE_DOCKER_BUILD_ARGS"))

	for _, tc := range goContainerStacks {
		t.Run(tc.stack, func(t *testing.T) {
			dir := t.TempDir()
			projectType := map[string]string{"go-cli-cobra": "cli", "go-api-chi": "api", "go-web-templ": "fullstack"}[tc.stack]
			vars, err := project.ResolveVariables(project.Input{
				ProjectName: "Image Check " + tc.stack,
				Language:    "go",
				ProjectType: projectType,
				Stack:       tc.stack,
				AuthorName:  "CI Bot",
				AuthorEmail: "ci@example.com",
				Remote:      project.RemoteNone,
			})
			if err != nil {
				t.Fatalf("ResolveVariables() error = %v", err)
			}
			if err := writer.Write(dir, vars); err != nil {
				t.Fatalf("Write() error = %v", err)
			}
			// forge init runs this to produce go.sum, which the image build needs.
			mustGoImageCommand(t, goImageBuildTimeout, dir, "go", "mod", "tidy")

			images := map[string]string{}
			for _, target := range []string{"runtime", "sandbox"} {
				tag := fmt.Sprintf("forge-image-test-%s:%s-%d", tc.stack, target, os.Getpid())
				args := append([]string{"build"}, extraBuildArgs...)
				args = append(args, "--target", target, "--build-arg", "REVISION=test-revision", "--tag", tag, ".")
				mustGoImageCommand(t, goImageBuildTimeout, dir, "docker", args...)
				images[target] = tag
				t.Cleanup(func() { _, _ = otelHostDocker("image", "rm", "--force", tag) })
			}

			config := mustGoImageCommand(t, goImageRunTimeout, dir, "docker", "image", "inspect", images["runtime"],
				"--format", `{{.Config.User}}|{{json .Config.Entrypoint}}|{{index .Config.Labels "org.opencontainers.image.revision"}}`)
			if got, want := strings.TrimSpace(config), `65532:65532|["/app"]|test-revision`; got != want {
				t.Errorf("runtime image config = %q, want %q", got, want)
			}

			if tc.server {
				assertGoServerImageRuns(t, dir, images["runtime"])
			} else {
				out := mustGoImageCommand(t, goImageRunTimeout, dir, "docker", "run", "--rm", "--read-only", "--cap-drop", "ALL", images["runtime"], "--help")
				if !strings.Contains(out, "Usage:") {
					t.Errorf("CLI runtime image --help output missing usage:\n%s", out)
				}
			}

			sandbox := mustGoImageCommand(t, goImageRunTimeout, dir, "docker", "run", "--rm", images["sandbox"],
				"bash", "-c", "id -u && pwd && go version && gopls version && golangci-lint version && git --version && touch /workspace/.write-check")
			for _, want := range []string{"10001\n/workspace\n", "go version go", "golang.org/x/tools/gopls v", "golangci-lint has version", "git version"} {
				if !strings.Contains(sandbox, want) {
					t.Errorf("sandbox output missing %q:\n%s", want, sandbox)
				}
			}
			if tc.stack == "go-web-templ" {
				out := mustGoImageCommand(t, goImageRunTimeout, dir, "docker", "run", "--rm", images["sandbox"], "templ", "version")
				if !strings.HasPrefix(strings.TrimSpace(out), "v") {
					t.Errorf("sandbox templ version output = %q", out)
				}
			}
		})
	}
}

// assertGoServerImageRuns starts the runtime image on a non-default PORT with
// a read-only root filesystem and no capabilities, probes /health, and checks
// the process logs JSON and exits 0 on SIGTERM.
func assertGoServerImageRuns(t *testing.T, dir, image string) {
	t.Helper()

	id, baseURL := startGoServerImage(t, dir, image)
	if body := goImageGetOK(t, id, baseURL+"/health"); body != `{"status":"ok"}` {
		t.Errorf("health body = %q", body)
	}

	mustGoImageCommand(t, goImageRunTimeout, dir, "docker", "stop", id)
	exitCode := strings.TrimSpace(mustGoImageCommand(t, goImageRunTimeout, dir, "docker", "inspect", id, "--format", "{{.State.ExitCode}}"))
	if exitCode != "0" {
		t.Errorf("exit code after SIGTERM = %s, want 0", exitCode)
	}

	logs := mustGoImageCommand(t, goImageRunTimeout, dir, "docker", "logs", id)
	sawListening := false
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Errorf("log line is not JSON: %q", line)
			continue
		}
		if entry["msg"] == "listening" && entry["addr"] == ":"+goImageProbePort {
			sawListening = true
		}
	}
	if !sawListening {
		t.Errorf("logs never reported listening on :%s:\n%s", goImageProbePort, logs)
	}
}

// startGoServerImage runs a server runtime image on a non-default PORT with a
// read-only root filesystem and no capabilities, and returns the container id
// and its loopback base URL.
func startGoServerImage(t *testing.T, dir, image string) (string, string) {
	t.Helper()

	id := strings.TrimSpace(mustGoImageCommand(t, goImageRunTimeout, dir, "docker", "run", "--detach",
		"--read-only", "--cap-drop", "ALL",
		"--env", "PORT="+goImageProbePort,
		"--publish", "127.0.0.1::"+goImageProbePort,
		image))
	t.Cleanup(func() { _, _ = otelHostDocker("container", "rm", "--force", id) })

	hostPort := strings.TrimSpace(mustGoImageCommand(t, goImageRunTimeout, dir, "docker", "port", id, goImageProbePort+"/tcp"))
	return id, "http://" + strings.Split(hostPort, "\n")[0]
}

// goImageGetOK polls url until it answers 200 and returns the body.
func goImageGetOK(t *testing.T, id, url string) string {
	t.Helper()

	deadline := time.Now().Add(goImageRunTimeout)
	for {
		resp, err := http.Get(url) //nolint:gosec // loopback URL built from docker's own port mapping
		if err == nil {
			data, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return string(data)
			}
		}
		if time.Now().After(deadline) {
			logs, _ := otelHostDocker("logs", id)
			t.Fatalf("GET %s never returned 200 (last error: %v)\n%s", url, err, logs)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestGoWebTemplImageServesTemplateOnlyEdits proves the image is built from the
// .templ sources, not from the checked-in generated *_templ.go: after a first
// build, editing only a template and rebuilding must change the served page.
// A build that skips `templ generate`, or a build context without .templ files
// (which also leaves the layer cache valid), would keep serving the old page.
func TestGoWebTemplImageServesTemplateOnlyEdits(t *testing.T) {
	requireGoImageDocker(t)

	const (
		original = "API health:"
		marker   = "TEMPLATE_ONLY_EDIT_MARKER:"
	)

	dir := t.TempDir()
	vars, err := project.ResolveVariables(project.Input{
		ProjectName: "Templ Image Check",
		Language:    "go",
		ProjectType: "fullstack",
		Stack:       "go-web-templ",
		AuthorName:  "CI Bot",
		AuthorEmail: "ci@example.com",
		Remote:      project.RemoteNone,
	})
	if err != nil {
		t.Fatalf("ResolveVariables() error = %v", err)
	}
	if err := (scaffold.Writer{Assets: forge.Assets()}).Write(dir, vars); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	mustGoImageCommand(t, goImageBuildTimeout, dir, "go", "mod", "tidy")

	tag := fmt.Sprintf("forge-image-test-templ-edit:%d", os.Getpid())
	t.Cleanup(func() { _, _ = otelHostDocker("image", "rm", "--force", tag) })
	build := func() {
		args := append([]string{"build"}, strings.Fields(os.Getenv("FORGE_DOCKER_BUILD_ARGS"))...)
		mustGoImageCommand(t, goImageBuildTimeout, dir, "docker", append(args, "--target", "runtime", "--tag", tag, ".")...)
	}
	servedPage := func() string {
		id, baseURL := startGoServerImage(t, dir, tag)
		page := goImageGetOK(t, id, baseURL+"/")
		mustGoImageCommand(t, goImageRunTimeout, dir, "docker", "rm", "--force", id)
		return page
	}

	build()
	if page := servedPage(); !strings.Contains(page, original) || strings.Contains(page, marker) {
		t.Fatalf("page before the edit should contain %q and not %q:\n%s", original, marker, page)
	}

	templPath := filepath.Join(dir, "internal", "web", "index.templ")
	source, err := os.ReadFile(templPath)
	if err != nil {
		t.Fatalf("read %s: %v", templPath, err)
	}
	if !strings.Contains(string(source), original) {
		t.Fatalf("%s no longer contains %q; update this test's marker", templPath, original)
	}
	if err := os.WriteFile(templPath, []byte(strings.Replace(string(source), original, marker, 1)), 0o644); err != nil {
		t.Fatalf("write %s: %v", templPath, err)
	}

	build()
	if page := servedPage(); !strings.Contains(page, marker) || strings.Contains(page, original) {
		t.Fatalf("template-only edit did not reach the served page; want %q instead of %q:\n%s", marker, original, page)
	}
}

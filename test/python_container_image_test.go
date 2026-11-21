package test

import (
	"encoding/json"
	"fmt"
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

const pythonImagePackage = "demo_app"

var pythonImageStacks = []struct {
	stack       string
	projectType string
	server      bool
	web         bool
}{
	{stack: "python-cli-typer", projectType: "cli"},
	{stack: "python-fastapi", projectType: "api", server: true},
	{stack: "python-web-jinja", projectType: "fullstack", server: true, web: true},
}

// requirePythonImageDocker applies the Go image tests' gate and also needs uv,
// which generates the uv.lock the image build requires (forge init does this).
func requirePythonImageDocker(t *testing.T) {
	t.Helper()

	requireGoImageDocker(t)
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not on PATH; it is needed to generate uv.lock for the Python image build")
	}
}

// scaffoldPythonImageRepo writes a stack into a temp dir, locks it with the
// real uv, and applies the optional FORGE_DOCKER_DOCKERFILE_PATCH hook.
func scaffoldPythonImageRepo(t *testing.T, stack, projectType string) string {
	t.Helper()

	dir := t.TempDir()
	vars, err := project.ResolveVariables(project.Input{
		ProjectName:           "Image Check " + stack,
		Language:              "python",
		ProjectType:           projectType,
		Stack:                 stack,
		AuthorName:            "CI Bot",
		AuthorEmail:           "ci@example.com",
		Remote:                project.RemoteNone,
		PythonPackageOverride: pythonImagePackage,
	})
	if err != nil {
		t.Fatalf("ResolveVariables() error = %v", err)
	}
	if err := (scaffold.Writer{Assets: forge.Assets()}).Write(dir, vars); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	mustGoImageCommand(t, goImageBuildTimeout, dir, "uv", "lock")

	// FORGE_DOCKER_DOCKERFILE_PATCH names an executable that receives the
	// scaffolded directory and may rewrite its Dockerfile or .dockerignore, for
	// networks that need a custom CA or package mirror inside the build. Off by
	// default; the shipped Dockerfile is what runs otherwise.
	if patch := os.Getenv("FORGE_DOCKER_DOCKERFILE_PATCH"); patch != "" {
		mustGoImageCommand(t, goImageBuildTimeout, dir, patch, dir)
	}
	return dir
}

func buildPythonImage(t *testing.T, dir, target, tag string) {
	t.Helper()

	args := append([]string{"build"}, strings.Fields(os.Getenv("FORGE_DOCKER_BUILD_ARGS"))...)
	args = append(args, "--target", target, "--build-arg", "REVISION=test-revision", "--tag", tag, ".")
	mustGoImageCommand(t, goImageBuildTimeout, dir, "docker", args...)
	t.Cleanup(func() { _, _ = otelHostDocker("image", "rm", "--force", tag) })
}

// TestPythonStackContainerImagesBuildAndRun builds both targets of the shipped
// Dockerfile for every Python stack from a freshly scaffolded and locked repo
// and checks the properties ADR-0026 promises: non-root, HOST/PORT from the
// environment, JSON logs, clean SIGTERM exit, no pip in the runtime, and a
// usable toolchain in the sandbox.
//
// FORGE_DOCKER_BUILD_ARGS adds space-separated arguments to every
// `docker build`. FORGE_DOCKER_DOCKERFILE_PATCH optionally names an executable
// run with the scaffolded directory before building (see scaffoldPythonImageRepo).
func TestPythonStackContainerImagesBuildAndRun(t *testing.T) {
	requirePythonImageDocker(t)

	for _, tc := range pythonImageStacks {
		t.Run(tc.stack, func(t *testing.T) {
			dir := scaffoldPythonImageRepo(t, tc.stack, tc.projectType)

			images := map[string]string{}
			for _, target := range []string{"runtime", "sandbox"} {
				tag := fmt.Sprintf("forge-image-test-%s:%s-%d", tc.stack, target, os.Getpid())
				buildPythonImage(t, dir, target, tag)
				images[target] = tag
			}
			runtime := images["runtime"]

			config := mustGoImageCommand(t, goImageRunTimeout, dir, "docker", "image", "inspect", runtime,
				"--format", `{{.Config.User}}|{{json .Config.Entrypoint}}|{{index .Config.Labels "org.opencontainers.image.revision"}}`)
			if got, want := strings.TrimSpace(config), `65532:65532|["/app/.venv/bin/app"]|test-revision`; got != want {
				t.Errorf("runtime image config = %q, want %q", got, want)
			}

			if tc.server {
				env := mustGoImageCommand(t, goImageRunTimeout, dir, "docker", "image", "inspect", runtime, "--format", "{{json .Config.Env}}")
				var vars []string
				if err := json.Unmarshal([]byte(strings.TrimSpace(env)), &vars); err != nil {
					t.Fatalf("parse image env %q: %v", env, err)
				}
				for _, want := range []string{"HOST=0.0.0.0", "PORT=8080"} {
					if !containsString(vars, want) {
						t.Errorf("runtime image env missing %q: %v", want, vars)
					}
				}
				assertPythonServerImageRuns(t, dir, runtime, tc.web)
			} else {
				out := mustGoImageCommand(t, goImageRunTimeout, dir, "docker", "run", "--rm", "--read-only", "--cap-drop", "ALL", runtime, "--help")
				if !strings.Contains(out, "Usage") {
					t.Errorf("CLI runtime image --help output missing usage:\n%s", out)
				}
				out = mustGoImageCommand(t, goImageRunTimeout, dir, "docker", "run", "--rm", "--read-only", "--cap-drop", "ALL", runtime, "--name", "forge")
				if strings.TrimSpace(out) != "hello, forge!" {
					t.Errorf("CLI hello output = %q, want %q", out, "hello, forge!")
				}
			}

			// The runtime never installs anything, so pip must be gone.
			if out, err := goImageCommand(t, goImageRunTimeout, dir, "docker", "run", "--rm", "--entrypoint", "python", runtime, "-m", "pip", "--version"); err == nil {
				t.Errorf("pip is still present in the runtime image:\n%s", out)
			}

			sandbox := mustGoImageCommand(t, goImageRunTimeout, dir, "docker", "run", "--rm", images["sandbox"],
				"sh", "-c", "id -u && pwd && python --version && uv --version && ruff --version && pyright --version && git --version && touch /workspace/.write-check")
			for _, want := range []string{"10001\n/workspace\n", "Python 3.", "uv ", "ruff ", "pyright ", "git version"} {
				if !strings.Contains(sandbox, want) {
					t.Errorf("sandbox output missing %q:\n%s", want, sandbox)
				}
			}
		})
	}
}

// assertPythonServerImageRuns starts the runtime image on a non-default PORT
// with a read-only root filesystem and no capabilities, probes /health, and
// checks the process logs JSON and exits 0 on SIGTERM.
func assertPythonServerImageRuns(t *testing.T, dir, image string, web bool) {
	t.Helper()

	id, baseURL := startGoServerImage(t, dir, image)
	if body := goImageGetOK(t, id, baseURL+"/health"); strings.TrimSpace(body) != `{"status":"ok"}` {
		t.Errorf("health body = %q", body)
	}
	if web {
		if page := goImageGetOK(t, id, baseURL+"/"); !strings.Contains(strings.ToLower(page), "<html") {
			t.Errorf("GET / did not return HTML:\n%s", page)
		}
		if script := goImageGetOK(t, id, baseURL+"/static/htmx.min.js"); script == "" {
			t.Errorf("GET /static/htmx.min.js returned an empty body")
		}
	}

	mustGoImageCommand(t, goImageRunTimeout, dir, "docker", "stop", id)
	exitCode := strings.TrimSpace(mustGoImageCommand(t, goImageRunTimeout, dir, "docker", "inspect", id, "--format", "{{.State.ExitCode}}"))
	if exitCode != "0" {
		t.Errorf("exit code after SIGTERM = %s, want 0", exitCode)
	}

	logs := mustGoImageCommand(t, goImageRunTimeout, dir, "docker", "logs", id)
	sawStartup := false
	for _, line := range strings.Split(logs, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Errorf("log line is not a JSON object: %q", line)
			continue
		}
		lower := strings.ToLower(line)
		if strings.Contains(lower, "uvicorn running") || strings.Contains(lower, "application startup complete") {
			sawStartup = true
		}
	}
	if !sawStartup {
		t.Errorf("logs never reported the uvicorn startup:\n%s", logs)
	}
}

// TestPythonWebJinjaImageServesTemplateOnlyEdits proves the image is built
// from the package's templates: after a first build, editing only
// templates/index.html and rebuilding must change the served page.
func TestPythonWebJinjaImageServesTemplateOnlyEdits(t *testing.T) {
	requirePythonImageDocker(t)

	marker := fmt.Sprintf("TEMPLATE_ONLY_EDIT_MARKER_%d", time.Now().UnixNano())
	dir := scaffoldPythonImageRepo(t, "python-web-jinja", "fullstack")

	tag := fmt.Sprintf("forge-image-test-jinja-edit:%d", os.Getpid())
	servedPage := func() string {
		buildPythonImage(t, dir, "runtime", tag)
		id, baseURL := startGoServerImage(t, dir, tag)
		page := goImageGetOK(t, id, baseURL+"/")
		mustGoImageCommand(t, goImageRunTimeout, dir, "docker", "stop", id)
		return page
	}

	if page := servedPage(); strings.Contains(page, marker) {
		t.Fatalf("page before the edit already contains %q:\n%s", marker, page)
	}

	path := filepath.Join(dir, pythonImagePackage, "templates", "index.html")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	const anchor = "<body>"
	if !strings.Contains(string(source), anchor) {
		t.Fatalf("%s no longer contains %q; update this test's anchor", path, anchor)
	}
	edited := strings.Replace(string(source), anchor, anchor+"<!-- "+marker+" -->", 1)
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}

	if page := servedPage(); !strings.Contains(page, marker) {
		t.Fatalf("template-only edit did not reach the served page; want %q:\n%s", marker, page)
	}
}

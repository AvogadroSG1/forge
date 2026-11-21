package test

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestPiAddonImagesBuildAndRun builds the stack images and then the two
// Dockerfile.agent targets from a freshly scaffolded repo, and checks what
// ADR-0027 promises: both run as the non-root agent user in /workspace with a
// working pinned Pi, the agent image runs the application binary, and the
// agent-sandbox image keeps the sandbox toolchain.
//
// Gated by FORGE_DOCKER_TESTS=1 like the other image tests;
// FORGE_DOCKER_BUILD_ARGS adds arguments to every `docker build`.
func TestPiAddonImagesBuildAndRun(t *testing.T) {
	requireGoImageDocker(t)

	extraBuildArgs := strings.Fields(os.Getenv("FORGE_DOCKER_BUILD_ARGS"))
	piVersion := strings.TrimPrefix(piVersionPin.FindString(readPiAddonAsset(t, "Dockerfile.agent")), "ARG PI_VERSION=")
	if piVersion == "" {
		t.Fatal("Dockerfile.agent has no PI_VERSION pin")
	}

	for _, tc := range piAddonStacks {
		t.Run(tc.stack, func(t *testing.T) {
			dir := scaffoldPiAddonStack(t, tc.stack, tc.projectType, "pi")
			// forge init runs this to produce go.sum, which the image build needs.
			mustGoImageCommand(t, goImageBuildTimeout, dir, "go", "mod", "tidy")

			name := fmt.Sprintf("forge-agent-test-%s-%d", tc.stack, os.Getpid())
			build := func(file, target string, buildArgs ...string) string {
				tag := name + ":" + target
				args := append([]string{"build"}, extraBuildArgs...)
				args = append(args, "--file", file, "--target", target)
				for _, buildArg := range buildArgs {
					args = append(args, "--build-arg", buildArg)
				}
				args = append(args, "--tag", tag, ".")
				mustGoImageCommand(t, goImageBuildTimeout, dir, "docker", args...)
				t.Cleanup(func() { _, _ = otelHostDocker("image", "rm", "--force", tag) })
				return tag
			}

			runtime := build("Dockerfile", "runtime", "REVISION=test-revision")
			sandbox := build("Dockerfile", "sandbox", "REVISION=test-revision")
			bases := []string{"APP_IMAGE=" + runtime, "SANDBOX_IMAGE=" + sandbox}
			agent := build("Dockerfile.agent", "agent", bases...)
			agentSandbox := build("Dockerfile.agent", "agent-sandbox", bases...)

			config := mustGoImageCommand(t, goImageRunTimeout, dir, "docker", "image", "inspect", agent,
				"--format", `{{.Config.User}}|{{json .Config.Entrypoint}}|{{.Config.WorkingDir}}`)
			if got, want := strings.TrimSpace(config), `10001:10001|["/app"]|/workspace`; got != want {
				t.Errorf("agent image config = %q, want %q", got, want)
			}

			// Pi must start and answer over RPC without a provider key:
			// get_state needs no model call.
			const probe = `id -u && pwd && pi --version && echo '{"id":"a","type":"get_state"}' | PI_OFFLINE=1 pi --mode rpc --no-session && touch /workspace/.write-check`
			for image, extra := range map[string]string{agent: "", agentSandbox: " && go version && golangci-lint version"} {
				out := mustGoImageCommand(t, goImageRunTimeout, dir, "docker", "run", "--rm", "--entrypoint", "bash", image, "-c", probe+extra)
				wants := []string{"10001\n/workspace\n", piVersion, `"command":"get_state","success":true`}
				if extra != "" {
					wants = append(wants, "go version go", "golangci-lint has version")
				}
				for _, want := range wants {
					if !strings.Contains(out, want) {
						t.Errorf("%s output missing %q:\n%s", image, want, out)
					}
				}
			}

			if tc.stack == "go-cli-cobra" {
				out := mustGoImageCommand(t, goImageRunTimeout, dir, "docker", "run", "--rm", agent, "agent", "--help")
				if !strings.Contains(out, "Send a prompt to the Pi agent") {
					t.Errorf("agent image `agent --help` output:\n%s", out)
				}
			}
		})
	}
}

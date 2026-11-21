package test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	forge "forge"
	"forge/internal/catalog"
	"forge/internal/project"
	"forge/internal/scaffold"
)

// piAddonStacks are the stacks the Pi agent add-on (ADR-0027) is offered on,
// with the stack-specific wiring each one gets.
var piAddonStacks = []struct {
	stack       string
	projectType string
	wiring      []string
}{
	{stack: "go-cli-cobra", projectType: "cli", wiring: []string{"cmd/agent.go", "cmd/agent_test.go"}},
	{stack: "go-api-chi", projectType: "api", wiring: []string{"internal/httpapi/agent.go", "internal/httpapi/agent_test.go"}},
}

// piAddonCommonFiles is what every stack with the add-on receives.
var piAddonCommonFiles = []string{
	"AGENT.md",
	"Dockerfile.agent",
	".config/mise/conf.d/pi.toml",
	"internal/piagent/client.go",
	"internal/piagent/client_test.go",
}

var piVersionPin = regexp.MustCompile(`(?m)^ARG PI_VERSION=\d+\.\d+\.\d+$`)

func readPiAddonAsset(t *testing.T, name string) string {
	t.Helper()

	path := filepath.Join(repoRoot(t), "templates", "addons", "pi", "common", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func scaffoldPiAddonStack(t *testing.T, stack, projectType, agent string) string {
	t.Helper()

	vars, err := project.ResolveVariables(project.Input{
		ProjectName: "Agent Check " + stack,
		Language:    "go",
		ProjectType: projectType,
		Stack:       stack,
		Agent:       agent,
		AuthorName:  "CI Bot",
		AuthorEmail: "ci@example.com",
		Remote:      project.RemoteNone,
	})
	if err != nil {
		t.Fatalf("ResolveVariables() error = %v", err)
	}
	dir := t.TempDir()
	if err := (scaffold.Writer{Assets: forge.Assets()}).Write(dir, vars); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	return dir
}

// The catalog and this table must agree, so a stack cannot offer the add-on
// without the tests below covering it.
func TestPiAddonStacksMatchTheCatalog(t *testing.T) {
	var want []string
	for _, tc := range piAddonStacks {
		want = append(want, tc.stack)
	}
	if got := catalog.AgentAddonStacks("pi"); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("catalog offers pi on %v, tests cover %v", got, want)
	}
}

// TestPiAddonDockerfilePinsEverythingItPulls holds Dockerfile.agent to the
// pinning rules of the stack Dockerfile (ADR-0023): registry images are
// tag@digest, Pi is an exact version installed without lifecycle scripts,
// and both targets run as the non-root agent user.
func TestPiAddonDockerfilePinsEverythingItPulls(t *testing.T) {
	dockerfile := readPiAddonAsset(t, "Dockerfile.agent")

	if !dockerfileSyntax.MatchString(dockerfile) {
		t.Error("Dockerfile.agent must start with a # syntax= line")
	}

	// APP_IMAGE and SANDBOX_IMAGE name this repository's own locally built
	// images, so they are tags; everything else comes from a registry.
	local := map[string]string{"APP_IMAGE": "app:runtime", "SANDBOX_IMAGE": "app:sandbox"}
	images := map[string]bool{}
	for _, match := range dockerfileImage.FindAllStringSubmatch(dockerfile, -1) {
		name, ref := match[1], match[2]
		images[name] = true
		if want, ok := local[name]; ok {
			if ref != want {
				t.Errorf("ARG %s=%s, want the local default %s", name, ref, want)
			}
			continue
		}
		if !pinnedImageRef.MatchString(ref) {
			t.Errorf("ARG %s=%s is not pinned as tag@sha256:digest", name, ref)
		}
	}
	for _, name := range []string{"NODE_IMAGE", "APP_IMAGE", "SANDBOX_IMAGE"} {
		if !images[name] {
			t.Errorf("Dockerfile.agent is missing ARG %s", name)
		}
	}

	stages := map[string]string{}
	for _, match := range dockerfileFrom.FindAllStringSubmatch(dockerfile, -1) {
		base, stage := match[1], match[2]
		stages[stage] = base
		if !strings.HasPrefix(base, "${") || !strings.HasSuffix(base, "_IMAGE}") {
			t.Errorf("stage %s is FROM %s, want a pinned *_IMAGE ARG", stage, base)
		}
	}
	if got := len(dockerfileStage.FindAllString(dockerfile, -1)); got != len(stages) {
		t.Errorf("Dockerfile.agent has %d FROM lines but %d parsed stages; every FROM must be `FROM ${X_IMAGE} AS name`", got, len(stages))
	}
	for stage, base := range map[string]string{"app": "${APP_IMAGE}", "agent-sandbox": "${SANDBOX_IMAGE}", "agent": "${NODE_IMAGE}", "pi": "${NODE_IMAGE}"} {
		if stages[stage] != base {
			t.Errorf("stage %s is FROM %q, want %s", stage, stages[stage], base)
		}
	}

	if !piVersionPin.MatchString(dockerfile) {
		t.Error("Dockerfile.agent must pin Pi with ARG PI_VERSION=<exact semver>")
	}
	pi := stageBody(t, dockerfile, "pi")
	for _, want := range []string{"--ignore-scripts", `"@earendil-works/pi-coding-agent@${PI_VERSION}"`} {
		if !strings.Contains(pi, want) {
			t.Errorf("pi stage must contain %s", want)
		}
	}

	for _, stage := range []string{"agent", "agent-sandbox"} {
		body := stageBody(t, dockerfile, stage)
		users := numericUser.FindAllStringSubmatch(body, -1)
		if len(users) == 0 || users[len(users)-1][1] != "10001" {
			t.Errorf("stage %s must end as USER 10001:10001", stage)
		}
		if !strings.Contains(body, "COPY --from=pi /out/ /usr/local/pi/") {
			t.Errorf("stage %s must copy Pi from the pi stage", stage)
		}
		if !strings.Contains(body, "WORKDIR /workspace") {
			t.Errorf("stage %s must work in /workspace", stage)
		}
	}
	if agent := stageBody(t, dockerfile, "agent"); !strings.Contains(agent, "COPY --from=app /app /app") || !strings.Contains(agent, `ENTRYPOINT ["/app"]`) {
		t.Error("agent stage must run the application binary copied from the runtime image")
	}

	for _, forbidden := range []string{":latest", "\nADD "} {
		if strings.Contains(dockerfile, forbidden) {
			t.Errorf("Dockerfile.agent must not contain %q", strings.TrimSpace(forbidden))
		}
	}
}

// The agent images sit on the images the stack Dockerfile builds, and the
// shared lint gate has to see Dockerfile.agent without a new CI step.
func TestPiAddonImageTasksBuildOnTheStackImagesAndStayLinted(t *testing.T) {
	tasks := readPiAddonAsset(t, filepath.Join("mise", "conf.d", "pi.toml"))
	for _, want := range []string{
		`[tasks."image:agent"]`,
		`depends = ["image"]`,
		"--file Dockerfile.agent",
		`--build-arg "APP_IMAGE=$image:runtime"`,
		`--build-arg "SANDBOX_IMAGE=$image:sandbox"`,
		"for target in agent agent-sandbox; do",
		`[tasks."image:agent:scan"]`,
	} {
		if !strings.Contains(tasks, want) {
			t.Errorf("pi.toml missing %q", want)
		}
	}

	for _, tc := range piAddonStacks {
		dir := scaffoldPiAddonStack(t, tc.stack, tc.projectType, "pi")
		mise, err := os.ReadFile(filepath.Join(dir, "mise.toml"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(mise), `run = "hadolint Dockerfile*"`) {
			t.Errorf("%s image:lint must lint Dockerfile* so `mise run ci` covers Dockerfile.agent", tc.stack)
		}
	}
}

func TestPiAddonIsScaffoldedOnlyWhenRequested(t *testing.T) {
	for _, tc := range piAddonStacks {
		t.Run(tc.stack, func(t *testing.T) {
			files := append(append([]string{}, piAddonCommonFiles...), tc.wiring...)

			with := scaffoldPiAddonStack(t, tc.stack, tc.projectType, "pi")
			for _, name := range files {
				if _, err := os.Stat(filepath.Join(with, name)); err != nil {
					t.Errorf("--agent pi: %s missing: %v", name, err)
				}
			}

			without := scaffoldPiAddonStack(t, tc.stack, tc.projectType, "")
			for _, name := range files {
				if _, err := os.Stat(filepath.Join(without, name)); !os.IsNotExist(err) {
					t.Errorf("no --agent: %s must not be written (err = %v)", name, err)
				}
			}

			// Nothing in a repo without the add-on may mention it.
			err := filepath.WalkDir(without, func(path string, d os.DirEntry, walkErr error) error {
				if walkErr != nil || d.IsDir() {
					return walkErr
				}
				if info, err := d.Info(); err != nil || !info.Mode().IsRegular() {
					return err
				}
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				for _, leak := range []string{"piagent", "mountAgent", "Dockerfile.agent"} {
					if strings.Contains(string(data), leak) {
						t.Errorf("no --agent: %s mentions %q", strings.TrimPrefix(path, without), leak)
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// The api route is wired through the overlay's router template; the wiring
// must appear with the add-on and the generated module path must be used.
func TestPiAddonWiresIntoEachStack(t *testing.T) {
	api := scaffoldPiAddonStack(t, "go-api-chi", "api", "pi")
	router, err := os.ReadFile(filepath.Join(api, "internal", "httpapi", "router.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(router), "\tmountAgent(router, logger)\n") {
		t.Errorf("go-api-chi router does not mount the agent route:\n%s", router)
	}
	handler, err := os.ReadFile(filepath.Join(api, "internal", "httpapi", "agent.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"github.com/your-org/agent-check-go-api-chi/internal/piagent"`, `os.Getenv(agentEnabledEnv) != "1"`} {
		if !strings.Contains(string(handler), want) {
			t.Errorf("go-api-chi agent.go missing %s", want)
		}
	}

	cli := scaffoldPiAddonStack(t, "go-cli-cobra", "cli", "pi")
	command, err := os.ReadFile(filepath.Join(cli, "cmd", "agent.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"github.com/your-org/agent-check-go-cli-cobra/internal/piagent"`, "rootCmd.AddCommand(agentCmd)"} {
		if !strings.Contains(string(command), want) {
			t.Errorf("go-cli-cobra agent.go missing %s", want)
		}
	}

	for _, dir := range []string{api, cli} {
		doc, err := os.ReadFile(filepath.Join(dir, "AGENT.md"))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"mise run image:agent", "`agent-sandbox`", "ANTHROPIC_API_KEY", "PI_VERSION", "--format '{{.Manifest.Digest}}'"} {
			if !strings.Contains(string(doc), want) {
				t.Errorf("AGENT.md missing %q", want)
			}
		}
	}
}

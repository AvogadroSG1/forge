package test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// goContainerStacks lists the stacks that ship the container image pattern
// (ADR-0023), with the main package each Dockerfile builds and whether the
// stack is an HTTP server.
var goContainerStacks = []struct {
	stack       string
	mainPackage string
	server      bool
	miseFile    string
}{
	{stack: "go-cli-cobra", mainPackage: ".", server: false, miseFile: "mise.toml"},
	{stack: "go-api-chi", mainPackage: "./cmd/api", server: true, miseFile: "mise.toml.tmpl"},
	{stack: "go-web-templ", mainPackage: "./cmd/web", server: true, miseFile: "mise.toml"},
}

var (
	// pinnedImageRef is tag@digest: a version tag for people, a digest for the pull.
	pinnedImageRef   = regexp.MustCompile(`^[a-z0-9./_-]+:[A-Za-z0-9._-]+@sha256:[0-9a-f]{64}$`)
	dockerfileImage  = regexp.MustCompile(`(?m)^ARG ([A-Z_]+_IMAGE)=(\S+)$`)
	dockerfileFrom   = regexp.MustCompile(`(?m)^FROM (?:--platform=\S+ )?(\S+) AS (\S+)$`)
	dockerfileSyntax = regexp.MustCompile(`(?m)\A# syntax=(\S+)$`)
	dockerfileStage  = regexp.MustCompile(`(?m)^FROM `)
	hardcodedArch    = regexp.MustCompile(`GOARCH=[a-z0-9]`)
	numericUser      = regexp.MustCompile(`(?m)^USER ([0-9]+):([0-9]+)$`)
	miseToolVersion  = `(?m)^%s = "([^"]+)"$`
)

func readGoContainerAsset(t *testing.T, stack, name string) string {
	t.Helper()

	path := filepath.Join(repoRoot(t), "templates", "golden", stack, ".forge-overlay", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// stageBody returns the Dockerfile text of one named stage.
func stageBody(t *testing.T, dockerfile, stage string) string {
	t.Helper()

	marker := " AS " + stage + "\n"
	start := strings.Index(dockerfile, marker)
	if start < 0 {
		t.Fatalf("Dockerfile has no stage %q", stage)
	}
	body := dockerfile[start+len(marker):]
	if next := dockerfileStage.FindStringIndex(body); next != nil {
		body = body[:next[0]]
	}
	return body
}

func TestGoStacksShipPinnedMultiStageDockerfile(t *testing.T) {
	for _, tc := range goContainerStacks {
		t.Run(tc.stack, func(t *testing.T) {
			dockerfile := readGoContainerAsset(t, tc.stack, "Dockerfile")

			syntax := dockerfileSyntax.FindStringSubmatch(dockerfile)
			if syntax == nil || !pinnedImageRef.MatchString(syntax[1]) {
				t.Fatalf("Dockerfile must start with a tag@digest pinned # syntax= line, got %v", syntax)
			}

			images := map[string]string{}
			for _, match := range dockerfileImage.FindAllStringSubmatch(dockerfile, -1) {
				if !pinnedImageRef.MatchString(match[2]) {
					t.Errorf("%s=%s is not pinned as tag@sha256:<digest>", match[1], match[2])
				}
				images[match[1]] = match[2]
			}
			for _, want := range []string{"GO_IMAGE", "LINT_IMAGE", "RUNTIME_IMAGE"} {
				if images[want] == "" {
					t.Errorf("Dockerfile missing pinned ARG %s", want)
				}
			}

			// Every stage must come from a pinned ARG, so no FROM can
			// introduce an unpinned image.
			stages := map[string]string{}
			for _, match := range dockerfileFrom.FindAllStringSubmatch(dockerfile, -1) {
				base := strings.Trim(match[1], "${}")
				if images[base] == "" {
					t.Errorf("stage %s uses %s, which is not one of the pinned image ARGs", match[2], match[1])
				}
				stages[match[2]] = base
			}
			if got := len(dockerfileStage.FindAllString(dockerfile, -1)); got != len(stages) {
				t.Errorf("found %d FROM lines but parsed %d named stages; every stage must be `FROM ${X_IMAGE} AS name`", got, len(stages))
			}
			for stage, base := range map[string]string{"sandbox": "GO_IMAGE", "build": "GO_IMAGE", "runtime": "RUNTIME_IMAGE"} {
				if stages[stage] != base {
					t.Errorf("stage %s base = %q, want %q", stage, stages[stage], base)
				}
			}
			if !strings.HasSuffix(strings.TrimSpace(dockerfile[strings.LastIndex(dockerfile, "\nFROM "):]), `ENTRYPOINT ["/app"]`) {
				t.Errorf("runtime must be the last (default) stage and end with an exec-form ENTRYPOINT")
			}

			for _, forbidden := range []string{"latest", "COPY --from=golang", "COPY --from=docker.io", "ADD "} {
				if strings.Contains(dockerfile, forbidden) {
					t.Errorf("Dockerfile must not contain %q", forbidden)
				}
			}
			if hardcodedArch.MatchString(dockerfile) {
				t.Errorf("Dockerfile must not hardcode GOARCH; use TARGETARCH")
			}

			build := stageBody(t, dockerfile, "build")
			for _, want := range []string{
				"FROM --platform=${BUILDPLATFORM} ${GO_IMAGE} AS build",
				"go mod download && go mod verify",
				"CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH}",
				"-trimpath",
				"-o /out/app " + tc.mainPackage + "\n",
			} {
				if !strings.Contains(dockerfile, want) {
					t.Errorf("Dockerfile missing %q", want)
				}
			}
			if strings.Index(build, "COPY go.mod go.sum ./") > strings.Index(build, "COPY . .") {
				t.Errorf("build stage must copy go.mod/go.sum before the source so the module layer caches")
			}

			for stage, wantUID := range map[string]string{"sandbox": "10001", "runtime": "65532"} {
				user := numericUser.FindStringSubmatch(stageBody(t, dockerfile, stage))
				if user == nil || user[1] != wantUID || user[1] == "0" {
					t.Errorf("stage %s must run as numeric non-root USER %s, got %v", stage, wantUID, user)
				}
			}

			runtime := stageBody(t, dockerfile, "runtime")
			for _, forbidden := range []string{"RUN ", "HEALTHCHECK"} {
				if strings.Contains(runtime, forbidden) {
					t.Errorf("runtime stage must not contain %q: the base image has no shell", forbidden)
				}
			}
			hasPort := strings.Contains(runtime, "ENV PORT=8080") && strings.Contains(runtime, "EXPOSE 8080")
			if hasPort != tc.server {
				t.Errorf("runtime stage PORT/EXPOSE present = %v, want %v", hasPort, tc.server)
			}

			sandbox := stageBody(t, dockerfile, "sandbox")
			for _, want := range []string{"WORKDIR /workspace", "CGO_ENABLED=0", "COPY --from=tools /out/gopls", "COPY --from=lint /usr/bin/golangci-lint"} {
				if !strings.Contains(sandbox, want) {
					t.Errorf("sandbox stage missing %q", want)
				}
			}
			if !regexp.MustCompile(`(?m)^ARG GOPLS_VERSION=v[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(dockerfile) {
				t.Errorf("gopls must be pinned to an exact version")
			}
		})
	}
}

// TestGoStackDockerfileToolVersionsMatchMise keeps the image toolchain and the
// local toolchain from drifting apart: dev/prod parity depends on it.
func TestGoStackDockerfileToolVersionsMatchMise(t *testing.T) {
	for _, tc := range goContainerStacks {
		t.Run(tc.stack, func(t *testing.T) {
			dockerfile := readGoContainerAsset(t, tc.stack, "Dockerfile")
			mise := readGoContainerAsset(t, tc.stack, tc.miseFile)

			for tool, imagePrefix := range map[string]string{
				"go":            "GO_IMAGE=golang:%s-alpine",
				"golangci-lint": "LINT_IMAGE=golangci/golangci-lint:v%s-alpine@",
			} {
				version := regexp.MustCompile(strings.Replace(miseToolVersion, "%s", regexp.QuoteMeta(tool), 1)).FindStringSubmatch(mise)
				if version == nil {
					t.Fatalf("%s does not pin %s", tc.miseFile, tool)
				}
				want := strings.Replace(imagePrefix, "%s", version[1], 1)
				if !strings.Contains(dockerfile, want) {
					t.Errorf("Dockerfile must contain %q to match %s %s in %s", want, tool, version[1], tc.miseFile)
				}
			}
		})
	}
}

func TestGoStacksShipAllowlistDockerignore(t *testing.T) {
	for _, tc := range goContainerStacks {
		t.Run(tc.stack, func(t *testing.T) {
			ignore := readGoContainerAsset(t, tc.stack, ".dockerignore")

			var rules []string
			for _, line := range strings.Split(ignore, "\n") {
				line = strings.TrimSpace(line)
				if line != "" && !strings.HasPrefix(line, "#") {
					rules = append(rules, line)
				}
			}
			if len(rules) == 0 || rules[0] != "*" {
				t.Fatalf(".dockerignore must start by excluding everything, got rules %v", rules)
			}
			for _, want := range []string{"!go.mod", "!go.sum", "!**/*.go"} {
				if !strings.Contains(ignore, want+"\n") {
					t.Errorf(".dockerignore missing %q", want)
				}
			}
		})
	}
}

// TestGoStacksDocumentTheContainerPattern keeps CONTAINER.md honest about the
// targets and tasks the stack actually ships.
func TestGoStacksDocumentTheContainerPattern(t *testing.T) {
	factors := []string{
		"Codebase", "Dependencies", "Config", "Backing services", "Build, release, run", "Processes",
		"Port binding", "Concurrency", "Disposability", "Dev/prod parity", "Logs", "Admin processes",
	}

	for _, tc := range goContainerStacks {
		t.Run(tc.stack, func(t *testing.T) {
			doc := readGoContainerAsset(t, tc.stack, "CONTAINER.md")

			for _, want := range []string{"`runtime`", "`sandbox`", "mise run image:lint", "mise run image`", "mise run image:scan", "imagetools inspect"} {
				if !strings.Contains(doc, want) {
					t.Errorf("CONTAINER.md missing %q", want)
				}
			}
			for _, factor := range factors {
				if !strings.Contains(doc, "| "+factor+" |") {
					t.Errorf("CONTAINER.md twelve-factor table missing %q", factor)
				}
			}
		})
	}
}

// TestGoServerStacksReadPortFromEnvironmentAndLogJSON pins the two code-level
// twelve-factor properties of the server stacks (config and logs).
func TestGoServerStacksReadPortFromEnvironmentAndLogJSON(t *testing.T) {
	mains := map[string]string{
		"go-api-chi":   filepath.Join("cmd", "api", "main.go.tmpl"),
		"go-web-templ": filepath.Join("cmd", "web", "main.go.tmpl"),
	}

	for stack, mainPath := range mains {
		t.Run(stack, func(t *testing.T) {
			main := readGoContainerAsset(t, stack, mainPath)

			for _, want := range []string{`os.Getenv("PORT")`, "Addr:              listenAddr(),", "slog.NewJSONHandler(os.Stdout, nil)", "syscall.SIGTERM"} {
				if !strings.Contains(main, want) {
					t.Errorf("%s missing %q", mainPath, want)
				}
			}
			for _, forbidden := range []string{`Addr:              ":8080"`, " log.New(", "\t\"log\"\n"} {
				if strings.Contains(main, forbidden) {
					t.Errorf("%s must not contain %q", mainPath, forbidden)
				}
			}
		})
	}
}

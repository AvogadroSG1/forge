package test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"forge"
	"forge/internal/project"
	"forge/internal/scaffold"
)

// otelTaskTimeout bounds each `mise run otel*` invocation. The stubbed docker
// and curl return immediately, so a healthy task finishes in well under this.
const otelTaskTimeout = 60 * time.Second

// otelInjectionSegment is a directory name that runs `touch INJECTED` if a
// shell ever evaluates the checkout path as source code instead of data.
const otelInjectionSegment = "evil $(touch${IFS}INJECTED) dir"

// otelStubArgSeparator separates argv entries on one recorded stub line. A
// unit separator never appears in the task's docker/curl arguments.
const otelStubArgSeparator = "\x1f"

// otelStubScript records its own name and argv (one invocation per line)
// to $FORGE_OTEL_STUB_RECORD and exits 0, so `docker compose version`
// probes, `docker compose up`, and `curl` health checks all succeed.
const otelStubScript = `#!/bin/sh
{
  printf '%s' "${0##*/}"
  for arg in "$@"; do
    printf '\037%s' "$arg"
  done
  printf '\n'
} >>"$FORGE_OTEL_STUB_RECORD"
exit 0
`

// otelTaskHarness is a scaffolded repo wired to run the forge-managed otel
// mise tasks against stubbed docker/curl in a fully isolated environment.
type otelTaskHarness struct {
	root       string
	repoDir    string
	dataHome   string
	recordPath string
	env        []string
	mise       string
	// timeout bounds each `mise run`; stopBy, when non-zero, is a hard
	// wall-clock limit that leaves the rest of the test deadline for cleanup.
	timeout time.Duration
	stopBy  time.Time
}

// otelTaskResult is the observable outcome of one `mise run <task>`.
type otelTaskResult struct {
	task     string
	exitCode int
	output   string
}

func TestOtelTasksRunUnderDash(t *testing.T) {
	h := newOtelTaskHarness(t, "repo")
	if dash, err := exec.LookPath("dash"); err == nil {
		h.env = setEnvValue(h.env, "MISE_UNIX_DEFAULT_INLINE_SHELL_ARGS", dash+" -c")
	} else {
		t.Logf("dash not on PATH; running under mise's default inline shell (dash on Debian/Ubuntu)")
	}

	for _, task := range []string{"otel", "otel:status", "otel:logs", "otel:down"} {
		result := h.run(t, task)
		if result.exitCode != 0 {
			t.Errorf("mise run %s exit = %d, want 0\n%s", task, result.exitCode, result.output)
		}
		if strings.Contains(result.output, "Illegal option -o pipefail") {
			t.Errorf("mise run %s printed %q; tasks MUST run under a POSIX shell\n%s", task, "Illegal option -o pipefail", result.output)
		}
	}

	// Guard against a vacuous pass: the tasks must have reached the stubs
	// on PATH rather than a real docker or an early exit.
	sawDocker := false
	for _, argv := range h.stubInvocations(t) {
		if argv[0] == "docker" {
			sawDocker = true
		}
	}
	if !sawDocker {
		t.Errorf("no docker stub invocation recorded in %s; tasks never reached docker", h.recordPath)
	}
}

func TestOtelTasksPathMetacharactersInert(t *testing.T) {
	h := newOtelTaskHarness(t, filepath.Join(otelInjectionSegment, "repo"))

	result := h.run(t, "otel")

	var injected []string
	err := filepath.WalkDir(h.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == "INJECTED" {
			injected = append(injected, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", h.root, err)
	}
	if len(injected) > 0 {
		t.Errorf("checkout path was evaluated as shell source; found INJECTED at %v\nmise run otel exit = %d\n%s", injected, result.exitCode, result.output)
	}

	for _, name := range []string{"compose.yaml", "collector.yaml"} {
		want, err := os.ReadFile(filepath.Join(h.repoDir, ".otel", name))
		if err != nil {
			t.Fatalf("read repo .otel/%s: %v", name, err)
		}
		copied := filepath.Join(h.dataHome, "forge-otel", name)
		got, err := os.ReadFile(copied)
		if err != nil {
			t.Errorf("read %s: %v (mise run otel exit = %d)\n%s", copied, err, result.exitCode, result.output)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s does not match repo .otel/%s byte-for-byte", copied, name)
		}
	}
}

func TestOtelTasksNoConfigRootInRunBodies(t *testing.T) {
	const templatePath = "templates/common/mise/conf.d/otel.toml"
	data, err := fs.ReadFile(forge.Assets(), templatePath)
	if err != nil {
		t.Fatalf("read %s: %v", templatePath, err)
	}

	bodies, err := parseTomlRunBodies(string(data))
	if err != nil {
		t.Fatalf("parse %s: %v", templatePath, err)
	}
	for _, task := range []string{"tasks.otel", `tasks."otel:down"`, `tasks."otel:status"`, `tasks."otel:logs"`} {
		if _, ok := bodies[task]; !ok {
			t.Errorf("%s: no run body found for [%s] (found %v)", templatePath, task, mapKeys(bodies))
		}
	}
	for table, body := range bodies {
		if strings.Contains(body, "{{config_root}}") {
			t.Errorf("%s: [%s] run body interpolates %q into shell source", templatePath, table, "{{config_root}}")
		}
	}
}

func TestOtelTasksUpForceRecreates(t *testing.T) {
	h := newOtelTaskHarness(t, "repo")

	result := h.run(t, "otel")
	if result.exitCode != 0 {
		t.Fatalf("mise run otel exit = %d, want 0\n%s", result.exitCode, result.output)
	}

	// A bind-mounted collector.yaml edit does not change the container's
	// compose config hash, so a plain `up` leaves the running collector on the
	// old pipeline; `otel` MUST force the collector to be recreated.
	var ups [][]string
	for _, argv := range h.stubInvocations(t) {
		if argv[0] == "docker" && slices.Contains(argv, "compose") && slices.Contains(argv, "up") {
			ups = append(ups, argv)
		}
	}
	if len(ups) == 0 {
		t.Fatalf("no `docker compose ... up` invocation recorded in %s\n%s", h.recordPath, result.output)
	}
	for _, up := range ups {
		for _, flag := range []string{"-d", "--wait", "--force-recreate"} {
			if !slices.Contains(up, flag) {
				t.Errorf("docker compose up argv = %q, want it to contain %q", up, flag)
			}
		}
	}
}

// newOtelTaskHarness scaffolds a go-cli-cobra repo at <tempRoot>/<repoRel>,
// installs recording docker/curl stubs, and builds a minimal isolated env for
// mise. It skips the test when mise is not installed.
func newOtelTaskHarness(t *testing.T, repoRel string) *otelTaskHarness {
	t.Helper()

	h := newOtelRepoHarness(t, repoRel)

	stubDir := filepath.Join(h.root, "stubs")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", stubDir, err)
	}
	for _, name := range []string{"docker", "curl"} {
		if err := os.WriteFile(filepath.Join(stubDir, name), []byte(otelStubScript), 0o755); err != nil {
			t.Fatalf("write stub %s: %v", name, err)
		}
	}

	h.env = append(h.env,
		"PATH="+strings.Join([]string{stubDir, filepath.Dir(h.mise), "/usr/bin", "/bin"}, string(os.PathListSeparator)),
		"FORGE_OTEL_STUB_RECORD="+h.recordPath,
	)

	return h
}

// newOtelRepoHarness scaffolds a go-cli-cobra repo at <tempRoot>/<repoRel>
// and builds an isolated HOME/XDG/mise env with no PATH; callers add the PATH
// (stubbed or real tools) they need. It skips the test when mise is not
// installed.
func newOtelRepoHarness(t *testing.T, repoRel string) *otelTaskHarness {
	t.Helper()

	misePath := resolveCommandPath("mise")
	if _, err := os.Stat(misePath); err != nil {
		t.Skip("mise not installed; skipping otel task execution test")
	}

	root := t.TempDir()
	repoDir := filepath.Join(root, repoRel)
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", repoDir, err)
	}

	vars, err := project.ResolveVariables(project.Input{
		ProjectName: "Otel Tasks",
		Language:    "go",
		ProjectType: "cli",
		Stack:       "go-cli-cobra",
		AuthorName:  "CI Bot",
		AuthorEmail: "ci@example.com",
		Remote:      project.RemoteNone,
	})
	if err != nil {
		t.Fatalf("ResolveVariables() error = %v", err)
	}
	if err := (scaffold.Writer{Assets: forge.Assets()}).Write(repoDir, vars); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	home := filepath.Join(root, "home")
	dataHome := filepath.Join(home, ".local", "share")
	xdg := map[string]string{
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"XDG_DATA_HOME":   dataHome,
		"XDG_STATE_HOME":  filepath.Join(home, ".local", "state"),
		"XDG_CACHE_HOME":  filepath.Join(home, ".cache"),
	}
	for _, dir := range xdg {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	recordPath := filepath.Join(root, "stub-argv.log")
	env := []string{
		"HOME=" + home,
		// Without a ceiling, mise walks above the temp root and loads the
		// developer's own config as project config.
		"MISE_CEILING_PATHS=" + root,
		"MISE_TRUSTED_CONFIG_PATHS=" + repoDir,
		"MISE_YES=1",
		"MISE_AUTO_INSTALL=0",
		"MISE_TASK_RUN_AUTO_INSTALL=0",
		"MISE_NOT_FOUND_AUTO_INSTALL=0",
		// The stack's mise.toml pins tools at "latest"; offline mode stops
		// mise resolving them over the network on every task run.
		"MISE_OFFLINE=1",
	}
	for key, value := range xdg {
		env = append(env, key+"="+value)
	}

	return &otelTaskHarness{
		root:       root,
		repoDir:    repoDir,
		dataHome:   dataHome,
		recordPath: recordPath,
		env:        env,
		mise:       misePath,
		timeout:    otelTaskTimeout,
	}
}

// run executes `mise run <task>` from the repo root and returns its outcome.
func (h *otelTaskHarness) run(t *testing.T, task string) otelTaskResult {
	t.Helper()

	result, err := h.tryRun(task)
	if err != nil {
		t.Fatalf("%v\n%s", err, result.output)
	}

	return result
}

// tryRun executes `mise run <task>` like run but reports harness failures
// (timeout, mise not startable) as an error instead of failing the test.
// The task is bounded by h.timeout and, when set, by h.stopBy.
func (h *otelTaskHarness) tryRun(task string) (otelTaskResult, error) {
	timeout := h.timeout
	if !h.stopBy.IsZero() {
		remaining := time.Until(h.stopBy)
		if remaining <= 0 {
			return otelTaskResult{task: task}, fmt.Errorf("mise run %s: test time budget exhausted; %w", task, context.DeadlineExceeded)
		}
		timeout = min(timeout, remaining)
	}
	return h.tryRunWithin(task, timeout)
}

// tryRunWithin executes `mise run <task>` bounded only by timeout. It never
// fails the test, so t.Cleanup can use it with its own reserved budget.
func (h *otelTaskHarness) tryRunWithin(task string, timeout time.Duration) (otelTaskResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, h.mise, "run", task)
	cmd.Dir = h.repoDir
	cmd.Env = h.env
	cmd.Stdin = nil
	output, err := cmd.CombinedOutput()
	result := otelTaskResult{task: task, output: string(output)}
	if ctx.Err() != nil {
		return result, fmt.Errorf("mise run %s timed out after %s: %w", task, timeout, ctx.Err())
	}
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return result, fmt.Errorf("mise run %s: %w", task, err)
		}
		result.exitCode = exitErr.ExitCode()
	}

	return result, nil
}

// stubInvocations returns every recorded docker/curl invocation in order.
// Each entry is the stub name followed by its argv, e.g.
// ["docker", "compose", "-p", "forge-otel", "-f", "...", "up", "-d", "--wait"].
func (h *otelTaskHarness) stubInvocations(t *testing.T) [][]string {
	t.Helper()

	data, err := os.ReadFile(h.recordPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read %s: %v", h.recordPath, err)
	}

	var invocations [][]string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		invocations = append(invocations, strings.Split(scanner.Text(), otelStubArgSeparator))
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", h.recordPath, err)
	}

	return invocations
}

var (
	tomlTableHeader = regexp.MustCompile(`^\s*\[([^\[\]]+)\]\s*(#.*)?$`)
	tomlRunKey      = regexp.MustCompile(`^\s*run\s*=\s*(.*)$`)
)

// parseTomlRunBodies extracts every `run` string value keyed by its table
// header (e.g. `tasks."otel:down"`). The module has no TOML dependency, so
// this handles the string forms a task body can take (multiline literal and
// basic strings, single-line literal and basic strings) and rejects anything
// else, such as arrays, so an unparsed body can never pass silently.
func parseTomlRunBodies(src string) (map[string]string, error) {
	bodies := map[string]string{}
	lines := strings.Split(src, "\n")
	table := ""

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if m := tomlTableHeader.FindStringSubmatch(line); m != nil {
			table = strings.TrimSpace(m[1])
			continue
		}
		m := tomlRunKey.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		value := strings.TrimSpace(m[1])

		switch {
		case strings.HasPrefix(value, "'''") || strings.HasPrefix(value, `"""`):
			delim := value[:3]
			rest := value[3:]
			if end := strings.Index(rest, delim); end >= 0 {
				bodies[table] = rest[:end]
				continue
			}
			var body strings.Builder
			body.WriteString(rest)
			closed := false
			for i++; i < len(lines); i++ {
				if end := strings.Index(lines[i], delim); end >= 0 {
					body.WriteString("\n" + lines[i][:end])
					closed = true
					break
				}
				body.WriteString("\n" + lines[i])
			}
			if !closed {
				return nil, &tomlParseError{table: table, reason: "unterminated " + delim + " string"}
			}
			bodies[table] = body.String()
		case strings.HasPrefix(value, "'") || strings.HasPrefix(value, `"`):
			quote := value[:1]
			end := strings.Index(value[1:], quote)
			if end < 0 {
				return nil, &tomlParseError{table: table, reason: "unterminated string"}
			}
			bodies[table] = value[1 : 1+end]
		default:
			return nil, &tomlParseError{table: table, reason: "unsupported run value form: " + value}
		}
	}

	return bodies, nil
}

type tomlParseError struct {
	table  string
	reason string
}

func (e *tomlParseError) Error() string {
	return "[" + e.table + "] run: " + e.reason
}

func mapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

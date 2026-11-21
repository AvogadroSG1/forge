package upgrade

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"forge"
	"forge/internal/catalog"
	"forge/internal/project"
	"forge/internal/scaffold"
)

const testModule = "example.com/acme/app"

// addonOwnedDests are the four wholly-owned add-on files of the "pi" add-on.
var addonOwnedDests = []string{
	"Dockerfile.agent",
	".config/mise/conf.d/pi.toml",
	"internal/piagent/client.go",
	"internal/piagent/client_test.go",
}

// addonWiring is the add-if-missing output of the fake add-on per stack, with
// AGENT.md common to both.
var addonWiring = map[string][]string{
	"go-api-chi":   {"internal/httpapi/agent.go", "internal/httpapi/agent_test.go"},
	"go-cli-cobra": {"cmd/agent.go", "cmd/agent_test.go"},
}

// addonAssets extends containerPatternAssets with a small fake pi add-on tree.
func addonAssets() fstest.MapFS {
	assets := containerPatternAssets()
	set := func(path, data string) { assets[path] = &fstest.MapFile{Data: []byte(data)} }
	set("templates/addons/pi/common/Dockerfile.agent", "# Dockerfile.agent v1\n")
	set("templates/addons/pi/common/mise/conf.d/pi.toml", "# pi.toml v1\n")
	set("templates/addons/pi/common/internal/piagent/client.go", "package piagent // v1\n")
	set("templates/addons/pi/common/internal/piagent/client_test.go", "package piagent // test v1\n")
	set("templates/addons/pi/common/AGENT.md.tmpl", "# AGENT {{.Stack}}\n")
	set("templates/addons/pi/go-api-chi/internal/httpapi/agent.go.tmpl", "package x\n\nimport _ \"{{.ModulePath}}/internal/piagent\"\n")
	set("templates/addons/pi/go-api-chi/internal/httpapi/agent_test.go.tmpl", "// test for {{.ModulePath}}\n")
	set("templates/addons/pi/go-cli-cobra/cmd/agent.go.tmpl", "package x\n\nimport _ \"{{.ModulePath}}/internal/piagent\"\n")
	set("templates/addons/pi/go-cli-cobra/cmd/agent_test.go.tmpl", "// test for {{.ModulePath}}\n")
	return assets
}

// stackFixtures are the minimal files that satisfy each stack's requirement.
var stackFixtures = map[string]struct{ path, data string }{
	"go-api-chi":   {"internal/httpapi/router.go", "package httpapi\n\nimport \"log/slog\"\n\nfunc NewRouter(logger *slog.Logger) {}\n"},
	"go-cli-cobra": {"cmd/root.go", "package cmd\n\nvar rootCmd = 1\n"},
}

// addonRepo returns a forge repo whose manifest records stack and agent, at
// the current infra version or (stale) version 0, with a go.mod for testModule.
func addonRepo(t *testing.T, stack, agent string, stale bool) string {
	t.Helper()

	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, ".claude", "hooks", "guard"), "sentinel guard\n", 0o755)
	mustWriteFile(t, filepath.Join(dir, "go.mod"), "module "+testModule+"\n\ngo 1.27\n", 0o644)
	// The stack API forge's wiring needs (addonRequirements).
	if fixture, ok := stackFixtures[stack]; ok {
		mustWriteFile(t, filepath.Join(dir, fixture.path), fixture.data, 0o644)
	}
	if err := Stamp(dir, Manifest{Language: "go", Stack: stack, Agent: agent}); err != nil {
		t.Fatalf("Stamp() error = %v", err)
	}
	if stale {
		m, err := ReadManifest(dir)
		if err != nil {
			t.Fatal(err)
		}
		m.InfraVersion = 0
		writeManifest(t, dir, m)
	}
	return dir
}

func writeManifest(t *testing.T, dir string, m Manifest) {
	t.Helper()

	// Stamp forces the current version, so write the file by hand to pin 0.
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(dir, ".forge", "manifest.json"), string(data)+"\n", 0o644)
}

func mustWriteFile(t *testing.T, path, data string, mode os.FileMode) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, dir, rel string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

func exists(t *testing.T, dir, rel string) bool {
	t.Helper()

	_, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rel)))
	if err == nil {
		return true
	}
	if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return false
}

// snapshot records every path under dir with its mode and, for regular
// files, its content, so a refused run can be proven to have written nothing.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()

	snap := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		info, err := d.Info()
		if err != nil {
			return err
		}
		entry := info.Mode().String()
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entry += " " + string(data)
		}
		snap[rel] = entry
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func assertSnapshotsEqual(t *testing.T, before, after map[string]string) {
	t.Helper()

	for path, entry := range before {
		got, ok := after[path]
		if !ok {
			t.Errorf("%s was removed", path)
		} else if got != entry {
			t.Errorf("%s changed:\n  before %q\n  after  %q", path, entry, got)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			t.Errorf("%s was created", path)
		}
	}
}

// runCapturingStderr runs RunWith and returns what it printed on stderr.
// Tests using it must not be parallel.
func runCapturingStderr(t *testing.T, assets fs.FS, dir string, opts Options) (Status, string, error) {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	status, runErr := RunWith(assets, dir, opts)
	os.Stderr = orig
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return status, string(out), runErr
}

// assertNoAddonNotice allows other notices (the container notice is printed
// on a stale upgrade) but not the add-on's.
func assertNoAddonNotice(t *testing.T, stderr string) {
	t.Helper()

	if strings.Contains(stderr, "Pi agent add-on") || strings.Contains(stderr, "image:agent:scan") {
		t.Errorf("unexpected add-on notice:\n%s", stderr)
	}
}

func addonPaths(stack string) []string {
	return append(append([]string{"AGENT.md"}, addonWiring[stack]...), addonOwnedDests...)
}

func assertNoStrayTempFiles(t *testing.T, dir string) {
	t.Helper()

	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), ".forge-upgrade-") {
			t.Errorf("leftover temp file %s", path)
		}
		return nil
	})
}

func TestRunWithRefreshesOwnedAddonFilesOnStaleUpgradeOfAddonRepo(t *testing.T) {
	for _, stack := range []string{"go-api-chi", "go-cli-cobra"} {
		t.Run(stack, func(t *testing.T) {
			dir := addonRepo(t, stack, "pi", true)
			mustWriteFile(t, filepath.Join(dir, "internal/piagent/client.go"), "locally edited\n", 0o644)

			status, stderr, err := runCapturingStderr(t, addonAssets(), dir, Options{})
			if err != nil {
				t.Fatalf("RunWith() error = %v", err)
			}
			assertNoAddonNotice(t, stderr)
			for _, dest := range addonOwnedDests {
				if !exists(t, dir, dest) {
					t.Errorf("%s missing after refresh", dest)
				}
				if !slices.Contains(status.Updated, dest) {
					t.Errorf("Updated %v lacks %s", status.Updated, dest)
				}
			}
			if got := readFile(t, dir, "internal/piagent/client.go"); got != "package piagent // v1\n" {
				t.Errorf("client.go = %q, want the embedded bytes", got)
			}
			for _, rel := range append([]string{"AGENT.md"}, addonWiring[stack]...) {
				if exists(t, dir, rel) {
					t.Errorf("%s written by a plain upgrade", rel)
				}
			}
			m, err := ReadManifest(dir)
			if err != nil {
				t.Fatal(err)
			}
			if m.Agent != "pi" || m.InfraVersion != Version {
				t.Errorf("manifest = %+v, want agent pi at v%d", m, Version)
			}
		})
	}
}

func TestRunWithRestoresDeletedOwnedAddonFile(t *testing.T) {
	t.Parallel()

	dir := addonRepo(t, "go-cli-cobra", "pi", true)
	if _, err := RunWith(addonAssets(), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "Dockerfile.agent")); err != nil {
		t.Fatal(err)
	}
	// Stale again, as after a forge release that bumps Version.
	m, _ := ReadManifest(dir)
	m.InfraVersion = 0
	writeManifest(t, dir, m)

	if _, err := RunWith(addonAssets(), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, dir, "Dockerfile.agent"); got != "# Dockerfile.agent v1\n" {
		t.Errorf("Dockerfile.agent = %q after restore", got)
	}
}

func TestRunWithoutAgentWritesNoAddonFiles(t *testing.T) {
	t.Parallel()

	for _, stack := range []string{"go-api-chi", "go-cli-cobra", "go-web-templ"} {
		t.Run(stack, func(t *testing.T) {
			t.Parallel()

			dir := addonRepo(t, stack, "", true)
			if _, err := RunWith(addonAssets(), dir, Options{}); err != nil {
				t.Fatalf("RunWith() error = %v", err)
			}
			for _, rel := range append(addonPaths("go-api-chi"), addonPaths("go-cli-cobra")...) {
				if exists(t, dir, rel) {
					t.Errorf("%s written for a repo without the add-on", rel)
				}
			}
		})
	}
}

func TestRunWithSkipsSilentlyWhenRecordedAddonIsUnavailable(t *testing.T) {
	cases := []struct{ name, stack, agent string }{
		{"stack no longer offers it", "go-web-templ", "pi"},
		{"unknown to this forge", "go-api-chi", "future-agent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := addonRepo(t, tc.stack, tc.agent, true)
			_, stderr, err := runCapturingStderr(t, addonAssets(), dir, Options{})
			if err != nil {
				t.Fatalf("RunWith() error = %v", err)
			}
			assertNoAddonNotice(t, stderr)
			for _, rel := range addonOwnedDests {
				if exists(t, dir, rel) {
					t.Errorf("%s written", rel)
				}
			}
			m, _ := ReadManifest(dir)
			if m.Agent != tc.agent || m.InfraVersion != Version {
				t.Errorf("manifest = %+v, want agent %q kept and stamped", m, tc.agent)
			}
		})
	}
}

func TestRunWithOptInOnCurrentRepo(t *testing.T) {
	for _, stack := range []string{"go-api-chi", "go-cli-cobra"} {
		t.Run(stack, func(t *testing.T) {
			dir := addonRepo(t, stack, "", false)
			status, _, err := runCapturingStderr(t, addonAssets(), dir, Options{Agent: "pi"})
			if err != nil {
				t.Fatalf("RunWith() error = %v", err)
			}

			want := addonPaths(stack)
			got := slices.Clone(status.Updated)
			slices.Sort(want)
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Errorf("Updated = %v, want exactly %v", got, want)
			}
			if a := readFile(t, dir, "AGENT.md"); a != "# AGENT "+stack+"\n" {
				t.Errorf("AGENT.md = %q", a)
			}
			for _, rel := range addonWiring[stack] {
				if !strings.Contains(readFile(t, dir, rel), testModule) {
					t.Errorf("%s does not carry the go.mod module path", rel)
				}
			}
			m, err := ReadManifest(dir)
			if err != nil {
				t.Fatal(err)
			}
			if m.Agent != "pi" || m.InfraVersion != Version {
				t.Errorf("manifest = %+v, want agent pi at v%d", m, Version)
			}
			if got := readFile(t, dir, ".claude/hooks/guard"); got != "sentinel guard\n" {
				t.Errorf("guard = %q: ordinary managed files were rewritten on a current repo", got)
			}
		})
	}
}

func TestRunWithOptInOnStaleRepoAlsoRunsManagedSteps(t *testing.T) {
	t.Parallel()

	dir := addonRepo(t, "go-api-chi", "", true)
	status, err := RunWith(addonAssets(), dir, Options{Agent: "pi"})
	if err != nil {
		t.Fatalf("RunWith() error = %v", err)
	}
	if got := readFile(t, dir, ".claude/hooks/guard"); got != "#!/usr/bin/env bash\n# guard v1\n" {
		t.Errorf("guard = %q, want the embedded bytes", got)
	}
	for _, rel := range addonPaths("go-api-chi") {
		if !exists(t, dir, rel) {
			t.Errorf("%s missing", rel)
		}
		if !slices.Contains(status.Updated, rel) {
			t.Errorf("Updated %v lacks %s", status.Updated, rel)
		}
	}
	if !slices.Contains(status.Updated, ".claude/hooks/guard") {
		t.Errorf("Updated %v lacks the managed guard", status.Updated)
	}
}

func TestRunWithOptInKeepsExistingWiringAndAgentMD(t *testing.T) {
	t.Parallel()

	dir := addonRepo(t, "go-api-chi", "", false)
	mine := map[string]string{
		"AGENT.md":                       "mine\n",
		"internal/httpapi/agent.go":      "mine\n",
		"internal/httpapi/agent_test.go": "mine\n",
	}
	for rel, data := range mine {
		mustWriteFile(t, filepath.Join(dir, rel), data, 0o644)
	}
	mustWriteFile(t, filepath.Join(dir, "internal/piagent/client.go"), "edited\n", 0o644)

	status, err := RunWith(addonAssets(), dir, Options{Agent: "pi"})
	if err != nil {
		t.Fatalf("RunWith() error = %v", err)
	}
	for rel, data := range mine {
		if got := readFile(t, dir, rel); got != data {
			t.Errorf("%s = %q, want it kept", rel, got)
		}
		if slices.Contains(status.Updated, rel) {
			t.Errorf("Updated lists kept file %s", rel)
		}
	}
	if got := readFile(t, dir, "internal/piagent/client.go"); got != "package piagent // v1\n" {
		t.Errorf("owned client.go = %q, want overwritten", got)
	}
}

func TestRunWithOptInKeepsSymlinkAndDirectoryAtAddOnPaths(t *testing.T) {
	t.Parallel()

	dir := addonRepo(t, "go-cli-cobra", "", false)
	if err := os.MkdirAll(filepath.Join(dir, "AGENT.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nowhere", filepath.Join(dir, "cmd", "agent.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := RunWith(addonAssets(), dir, Options{Agent: "pi"}); err != nil {
		t.Fatalf("RunWith() error = %v", err)
	}
	if info, err := os.Lstat(filepath.Join(dir, "AGENT.md")); err != nil || !info.IsDir() {
		t.Errorf("AGENT.md directory replaced: %v %v", info, err)
	}
	if info, err := os.Lstat(filepath.Join(dir, "cmd", "agent.go")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("symlink replaced: %v %v", info, err)
	}
}

func TestRunWithOptInRefusalsWriteNothing(t *testing.T) {
	cases := []struct {
		name  string
		stack string
		agent string // manifest agent
		opts  Options
		setup func(t *testing.T, dir string)
	}{
		{name: "with --check", stack: "go-api-chi", opts: Options{Agent: "pi", CheckOnly: true}},
		{name: "none", stack: "go-api-chi", opts: Options{Agent: "none"}},
		{name: "none uppercase", stack: "go-api-chi", opts: Options{Agent: " NONE "}},
		{name: "unknown add-on", stack: "go-api-chi", opts: Options{Agent: "claude"}},
		{name: "no manifest", stack: "go-api-chi", opts: Options{Agent: "pi"}, setup: func(t *testing.T, dir string) {
			mustRemove(t, filepath.Join(dir, ".forge", "manifest.json"))
			mustWriteFile(t, filepath.Join(dir, ".forge-infra-version"), "0\n", 0o644)
		}},
		{name: "stack without the add-on", stack: "go-web-templ", opts: Options{Agent: "pi"}},
		{name: "go.mod missing", stack: "go-api-chi", opts: Options{Agent: "pi"}, setup: func(t *testing.T, dir string) {
			mustRemove(t, filepath.Join(dir, "go.mod"))
		}},
		{name: "go.mod without module line", stack: "go-api-chi", opts: Options{Agent: "pi"}, setup: func(t *testing.T, dir string) {
			mustWriteFile(t, filepath.Join(dir, "go.mod"), "// no module\ngo 1.27\n", 0o644)
		}},
		{name: "different add-on recorded", stack: "go-api-chi", agent: "other", opts: Options{Agent: "pi"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := addonRepo(t, tc.stack, tc.agent, true)
			if tc.setup != nil {
				tc.setup(t, dir)
			}
			before := snapshot(t, dir)

			status, err := RunWith(addonAssets(), dir, tc.opts)
			if err == nil {
				t.Fatalf("RunWith() = %+v, want an error", status)
			}
			assertSnapshotsEqual(t, before, snapshot(t, dir))
		})
	}
}

func mustRemove(t *testing.T, path string) {
	t.Helper()

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func TestRunWithOptInWhenManifestAlreadyRecordsAddonIsAPlainUpgrade(t *testing.T) {
	dir := addonRepo(t, "go-api-chi", "pi", false)
	before := snapshot(t, dir)

	status, stderr, err := runCapturingStderr(t, addonAssets(), dir, Options{Agent: "pi"})
	if err != nil {
		t.Fatalf("RunWith() error = %v", err)
	}
	if len(status.Updated) != 0 || stderr != "" {
		t.Errorf("Updated = %v, stderr = %q, want a silent no-op", status.Updated, stderr)
	}
	assertSnapshotsEqual(t, before, snapshot(t, dir))
}

func TestRunWithOptInWhenManifestAlreadyRecordsAddonRefreshesWhenStale(t *testing.T) {
	dir := addonRepo(t, "go-api-chi", "pi", true)
	_, stderr, err := runCapturingStderr(t, addonAssets(), dir, Options{Agent: "pi"})
	if err != nil {
		t.Fatalf("RunWith() error = %v", err)
	}
	assertNoAddonNotice(t, stderr)
	if exists(t, dir, "AGENT.md") {
		t.Error("AGENT.md delivered although the add-on was already recorded")
	}
	if !exists(t, dir, "Dockerfile.agent") {
		t.Error("owned files not refreshed on a stale upgrade")
	}
}

func TestRunWithInterruptedOptInIsFinishedByRerun(t *testing.T) {
	// The fake add-on also renders docs/agent.md, so the second case fails
	// after the owned files and the wiring are already written; the re-run
	// treats those as delivered ("ours") and finishes the rest.
	cases := []struct{ name, blocker string }{
		{"owned stage", "internal/piagent"},
		{"later write stage", "docs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := addonRepo(t, "go-api-chi", "", false)
			blocker := filepath.Join(dir, filepath.FromSlash(tc.blocker))
			mustWriteFile(t, blocker, "a file where a directory must go\n", 0o644)
			assets := addonAssets()
			assets["templates/addons/pi/common/docs/agent.md"] = &fstest.MapFile{Data: []byte("# docs\n")}

			if _, err := RunWith(assets, dir, Options{Agent: "pi"}); err == nil {
				t.Fatal("RunWith() succeeded despite the blocked directory")
			}
			if m, _ := ReadManifest(dir); m.Agent != "" {
				t.Errorf("manifest records agent %q after a failed opt-in", m.Agent)
			}

			mustRemove(t, blocker)
			if _, err := RunWith(assets, dir, Options{Agent: "pi"}); err != nil {
				t.Fatalf("re-run error = %v", err)
			}
			for _, rel := range append(addonPaths("go-api-chi"), "docs/agent.md") {
				if !exists(t, dir, rel) {
					t.Errorf("%s missing after re-run", rel)
				}
			}
			if m, _ := ReadManifest(dir); m.Agent != "pi" {
				t.Errorf("manifest agent = %q after re-run, want pi", m.Agent)
			}
			assertNoStrayTempFiles(t, dir)
		})
	}
}

func TestRunWithMissingEmbeddedAddonFileFailsBeforeAnyWrite(t *testing.T) {
	assets := addonAssets()
	delete(assets, "templates/addons/pi/common/internal/piagent/client_test.go")
	dir := addonRepo(t, "go-cli-cobra", "", false)
	before := snapshot(t, dir)

	if _, err := RunWith(assets, dir, Options{Agent: "pi"}); err == nil {
		t.Fatal("RunWith() succeeded with a missing embedded add-on file")
	}
	assertSnapshotsEqual(t, before, snapshot(t, dir))
}

func TestRunWithOptInPrintsNotice(t *testing.T) {
	cases := []struct {
		stack     string
		wantMount bool
	}{
		{"go-api-chi", true},
		{"go-cli-cobra", false},
	}
	for _, tc := range cases {
		t.Run(tc.stack, func(t *testing.T) {
			dir := addonRepo(t, tc.stack, "", false)
			_, stderr, err := runCapturingStderr(t, addonAssets(), dir, Options{Agent: "pi"})
			if err != nil {
				t.Fatalf("RunWith() error = %v", err)
			}
			for _, want := range []string{"hadolint Dockerfile*", "image:agent:scan", "AGENT.md", "mise.toml", "ci.yml"} {
				if !strings.Contains(stderr, want) {
					t.Errorf("notice missing %q:\n%s", want, stderr)
				}
			}
			if got := strings.Contains(stderr, "mountAgent"); got != tc.wantMount {
				t.Errorf("notice mentions mountAgent = %v, want %v:\n%s", got, tc.wantMount, stderr)
			}
		})
	}
}

func TestModulePath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"plain", "module foo\n", "foo", true},
		{"with go directive", "module example.com/a/b\n\ngo 1.27\n", "example.com/a/b", true},
		{"double quoted", "module \"example.com/a\"\n", "example.com/a", true},
		{"raw quoted", "module `example.com/a`\n", "example.com/a", true},
		{"trailing comment", "module foo // the app\n", "foo", true},
		{"leading comments and blanks", "// header\n\nmodule foo\n", "foo", true},
		{"indented", "\tmodule foo\n", "foo", true},
		{"crlf", "module foo\r\ngo 1.27\r\n", "foo", true},
		{"no trailing newline", "module foo", "foo", true},
		{"first directive wins", "module a\nmodule b\n", "a", true},
		{"empty", "", "", false},
		{"no module", "go 1.27\n", "", false},
		{"commented out", "// module foo\n", "", false},
		{"bare keyword", "module\n", "", false},
		{"empty quotes", "module \"\"\n", "", false},
		{"prefix only", "modulefoo\n", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := modulePath([]byte(tc.in))
			if got != tc.want || ok != tc.ok {
				t.Errorf("modulePath(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestAddonOwnedFilesMatchRenderedAddon keeps the owned-file declaration from
// drifting from the real template tree: every declared dest must be produced
// by scaffold.RenderAddon, byte for byte, for every stack offering the add-on.
func TestAddonOwnedFilesMatchRenderedAddon(t *testing.T) {
	t.Parallel()

	for addon, owned := range addonOwnedFiles {
		stacks := catalog.AgentAddonStacks(addon)
		if len(stacks) == 0 {
			t.Fatalf("no stack offers add-on %q", addon)
		}
		for _, stack := range stacks {
			t.Run(addon+"/"+stack, func(t *testing.T) {
				t.Parallel()

				files, err := scaffold.RenderAddon(forge.Assets(), project.Variables{
					Agent: addon, Stack: stack, Language: "go", ModulePath: testModule, GoModule: testModule,
				})
				if err != nil {
					t.Fatalf("RenderAddon() error = %v", err)
				}
				byPath := map[string]scaffold.File{}
				for _, f := range files {
					byPath[f.Path] = f
				}
				for _, mf := range owned {
					rendered, ok := byPath[mf.dest]
					if !ok {
						t.Errorf("%s is declared owned but RenderAddon does not produce it", mf.dest)
						continue
					}
					embedded, err := fs.ReadFile(forge.Assets(), mf.src)
					if err != nil {
						t.Errorf("read %s: %v", mf.src, err)
						continue
					}
					if !bytes.Equal(rendered.Data, embedded) {
						t.Errorf("%s: RenderAddon bytes differ from %s", mf.dest, mf.src)
					}
					if rendered.Mode != mf.mode {
						t.Errorf("%s: RenderAddon mode %o, declared %o", mf.dest, rendered.Mode, mf.mode)
					}
				}
			})
		}
	}
}

// renderedWiring returns what the fake add-on renders for stack, by path.
func renderedWiring(t *testing.T, stack string) map[string]string {
	t.Helper()

	files, err := scaffold.RenderAddon(addonAssets(), project.Variables{
		Agent: "pi", Stack: stack, Language: "go", ModulePath: testModule, GoModule: testModule,
	})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range files {
		out[f.Path] = string(f.Data)
	}
	return out
}

func TestRunWithOptInKeepsCustomSourceAndItsTestIsNotInstalled(t *testing.T) {
	for _, stack := range []string{"go-api-chi", "go-cli-cobra"} {
		t.Run(stack, func(t *testing.T) {
			dir := addonRepo(t, stack, "", false)
			src, test := addonWiring[stack][0], addonWiring[stack][1]
			mustWriteFile(t, filepath.Join(dir, src), "package mine\n", 0o644)

			status, stderr, err := runCapturingStderr(t, addonAssets(), dir, Options{Agent: "pi"})
			if err != nil {
				t.Fatalf("RunWith() error = %v", err)
			}
			if got := readFile(t, dir, src); got != "package mine\n" {
				t.Errorf("%s = %q, want it kept", src, got)
			}
			if exists(t, dir, test) {
				t.Errorf("%s installed although it tests forge's implementation of a file the repo replaced", test)
			}
			for _, p := range []string{src, test} {
				if slices.Contains(status.Updated, p) {
					t.Errorf("Updated lists %s", p)
				}
			}
			want := "kept your `" + src + "`; `" + test + "` was not installed because it tests forge's implementation"
			if !strings.Contains(stderr, want) {
				t.Errorf("notice lacks %q:\n%s", want, stderr)
			}
			if !exists(t, dir, "AGENT.md") {
				t.Error("AGENT.md not written: a kept group must not hold back other groups")
			}
			if m, _ := ReadManifest(dir); m.Agent != "pi" {
				t.Errorf("manifest agent = %q, want pi", m.Agent)
			}
		})
	}
}

func TestRunWithOptInKeepsCustomTestAndInstallsNeitherFile(t *testing.T) {
	dir := addonRepo(t, "go-cli-cobra", "", false)
	mustWriteFile(t, filepath.Join(dir, "cmd/agent_test.go"), "package mine\n", 0o644)

	_, stderr, err := runCapturingStderr(t, addonAssets(), dir, Options{Agent: "pi"})
	if err != nil {
		t.Fatalf("RunWith() error = %v", err)
	}
	if exists(t, dir, "cmd/agent.go") {
		t.Error("cmd/agent.go installed next to a custom test")
	}
	if got := readFile(t, dir, "cmd/agent_test.go"); got != "package mine\n" {
		t.Errorf("agent_test.go = %q, want it kept", got)
	}
	if !strings.Contains(stderr, "kept your `cmd/agent_test.go`; `cmd/agent.go` was not installed") {
		t.Errorf("notice lacks the kept line:\n%s", stderr)
	}
}

func TestRunWithOptInCompletesAnInterruptedWiringGroup(t *testing.T) {
	dir := addonRepo(t, "go-cli-cobra", "", false)
	want := renderedWiring(t, "go-cli-cobra")
	mustWriteFile(t, filepath.Join(dir, "cmd/agent.go"), want["cmd/agent.go"], 0o644)

	status, stderr, err := runCapturingStderr(t, addonAssets(), dir, Options{Agent: "pi"})
	if err != nil {
		t.Fatalf("RunWith() error = %v", err)
	}
	if got := readFile(t, dir, "cmd/agent_test.go"); got != want["cmd/agent_test.go"] {
		t.Errorf("agent_test.go = %q, want forge's bytes", got)
	}
	if !slices.Contains(status.Updated, "cmd/agent_test.go") || slices.Contains(status.Updated, "cmd/agent.go") {
		t.Errorf("Updated = %v, want only the missing member of the group", status.Updated)
	}
	if strings.Contains(stderr, "kept your") {
		t.Errorf("unexpected kept line:\n%s", stderr)
	}
}

func TestRunWithOptInWritesNothingFromAGroupAlreadyDelivered(t *testing.T) {
	dir := addonRepo(t, "go-cli-cobra", "", false)
	want := renderedWiring(t, "go-cli-cobra")
	for _, rel := range addonWiring["go-cli-cobra"] {
		mustWriteFile(t, filepath.Join(dir, rel), want[rel], 0o644)
	}

	status, stderr, err := runCapturingStderr(t, addonAssets(), dir, Options{Agent: "pi"})
	if err != nil {
		t.Fatalf("RunWith() error = %v", err)
	}
	for _, rel := range addonWiring["go-cli-cobra"] {
		if slices.Contains(status.Updated, rel) {
			t.Errorf("Updated lists %s although it was already forge's", rel)
		}
	}
	if strings.Contains(stderr, "kept your") {
		t.Errorf("unexpected kept line:\n%s", stderr)
	}
}

func TestRunWithOptInKeepsCustomAgentMDAndStillWritesWiring(t *testing.T) {
	dir := addonRepo(t, "go-cli-cobra", "", false)
	mustWriteFile(t, filepath.Join(dir, "AGENT.md"), "mine\n", 0o644)

	_, stderr, err := runCapturingStderr(t, addonAssets(), dir, Options{Agent: "pi"})
	if err != nil {
		t.Fatalf("RunWith() error = %v", err)
	}
	if got := readFile(t, dir, "AGENT.md"); got != "mine\n" {
		t.Errorf("AGENT.md = %q, want it kept", got)
	}
	for _, rel := range addonWiring["go-cli-cobra"] {
		if !exists(t, dir, rel) {
			t.Errorf("%s not written", rel)
		}
	}
	if !strings.Contains(stderr, "kept your `AGENT.md`") {
		t.Errorf("notice lacks the kept line:\n%s", stderr)
	}
}

func TestRunWithOptInNoticeSkipsMountAgentWhenAgentGoIsTheUsers(t *testing.T) {
	dir := addonRepo(t, "go-api-chi", "", false)
	mustWriteFile(t, filepath.Join(dir, "internal/httpapi/agent.go"), "package mine\n", 0o644)

	_, stderr, err := runCapturingStderr(t, addonAssets(), dir, Options{Agent: "pi"})
	if err != nil {
		t.Fatalf("RunWith() error = %v", err)
	}
	if strings.Contains(stderr, "mountAgent") {
		t.Errorf("notice prescribes mountAgent for a repo with its own agent.go:\n%s", stderr)
	}
	if !strings.Contains(stderr, "hadolint Dockerfile*") {
		t.Errorf("notice lost the other manual steps:\n%s", stderr)
	}
}

func TestRunWithOptInRequirementFailureWritesNothing(t *testing.T) {
	cases := []struct {
		name   string
		stack  string
		setup  func(t *testing.T, dir string)
		wanted []string
	}{
		{
			name: "api router takes *log.Logger", stack: "go-api-chi",
			setup: func(t *testing.T, dir string) {
				mustWriteFile(t, filepath.Join(dir, "internal/httpapi/router.go"),
					"package httpapi\n\nimport \"log\"\n\nfunc NewRouter(logger *log.Logger) {}\n", 0o644)
			},
			wanted: []string{"NewRouter", "*slog.Logger", "log/slog", "forge upgrade --agent pi"},
		},
		{
			name: "cobra has no rootCmd", stack: "go-cli-cobra",
			setup: func(t *testing.T, dir string) {
				mustWriteFile(t, filepath.Join(dir, "cmd/root.go"), "package cmd\n\nvar root = 1\n", 0o644)
			},
			wanted: []string{"rootCmd", "write your own cmd/agent.go"},
		},
	}
	for _, tc := range cases {
		for _, stale := range []bool{false, true} {
			name := tc.name + map[bool]string{false: "", true: " on a stale repo"}[stale]
			t.Run(name, func(t *testing.T) {
				dir := addonRepo(t, tc.stack, "", stale)
				tc.setup(t, dir)
				before := snapshot(t, dir)

				_, err := RunWith(addonAssets(), dir, Options{Agent: "pi"})
				if err == nil {
					t.Fatal("RunWith() succeeded although the wiring cannot compile in this repo")
				}
				for _, want := range tc.wanted {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error lacks %q: %v", want, err)
					}
				}
				assertSnapshotsEqual(t, before, snapshot(t, dir))
				if m, _ := ReadManifest(dir); m.Agent != "" {
					t.Errorf("manifest records agent %q after a refused opt-in", m.Agent)
				}
			})
		}
	}
}

func TestRunWithOptInDoesNotCheckRequirementsForKeptOrDeliveredWiring(t *testing.T) {
	brokenRouter := "package httpapi\n\nimport \"log\"\n\nfunc NewRouter(logger *log.Logger) {}\n"
	t.Run("custom wiring", func(t *testing.T) {
		dir := addonRepo(t, "go-api-chi", "", false)
		mustWriteFile(t, filepath.Join(dir, "internal/httpapi/router.go"), brokenRouter, 0o644)
		mustWriteFile(t, filepath.Join(dir, "internal/httpapi/agent.go"), "package mine\n", 0o644)
		if _, err := RunWith(addonAssets(), dir, Options{Agent: "pi"}); err != nil {
			t.Fatalf("RunWith() error = %v", err)
		}
	})
	t.Run("already forge's bytes", func(t *testing.T) {
		dir := addonRepo(t, "go-api-chi", "", false)
		mustWriteFile(t, filepath.Join(dir, "internal/httpapi/router.go"), brokenRouter, 0o644)
		for rel, data := range renderedWiring(t, "go-api-chi") {
			if slices.Contains(addonWiring["go-api-chi"], rel) {
				mustWriteFile(t, filepath.Join(dir, rel), data, 0o644)
			}
		}
		if _, err := RunWith(addonAssets(), dir, Options{Agent: "pi"}); err != nil {
			t.Fatalf("RunWith() error = %v", err)
		}
	})
}

func TestRunWithOptInRequirementChecks(t *testing.T) {
	const (
		slogRouter = "package httpapi\n\nimport \"log/slog\"\n\nfunc NewRouter(l *slog.Logger, extra int) {}\n"
		apiPath    = "internal/httpapi/router.go"
	)
	cases := []struct {
		name    string
		stack   string
		files   map[string]string // replaces the fixture
		wantErr string            // "" means the opt-in must succeed
	}{
		{"aliased log/slog passes", "go-api-chi", map[string]string{apiPath: "package httpapi\n\nimport sl \"log/slog\"\n\nfunc NewRouter(l *sl.Logger) {}\n"}, ""},
		{"first parameter among several", "go-api-chi", map[string]string{apiPath: slogRouter}, ""},
		{"declared in another file", "go-api-chi", map[string]string{apiPath: "package httpapi\n", "internal/httpapi/other.go": slogRouter}, ""},
		{"*log.Logger fails", "go-api-chi", map[string]string{apiPath: "package httpapi\n\nimport \"log\"\n\nfunc NewRouter(l *log.Logger) {}\n"}, "*slog.Logger"},
		{"another package named slog fails", "go-api-chi", map[string]string{apiPath: "package httpapi\n\nimport \"example.com/slog\"\n\nfunc NewRouter(l *slog.Logger) {}\n"}, "*slog.Logger"},
		{"slog without the import fails", "go-api-chi", map[string]string{apiPath: "package httpapi\n\nfunc NewRouter(l *slog.Logger) {}\n"}, "*slog.Logger"},
		{"value slog.Logger fails", "go-api-chi", map[string]string{apiPath: "package httpapi\n\nimport \"log/slog\"\n\nfunc NewRouter(l slog.Logger) {}\n"}, "*slog.Logger"},
		{"NewRouter missing fails", "go-api-chi", map[string]string{apiPath: "package httpapi\n"}, "NewRouter"},
		{"NewRouter as a method fails", "go-api-chi", map[string]string{apiPath: "package httpapi\n\nimport \"log/slog\"\n\ntype T int\n\nfunc (T) NewRouter(l *slog.Logger) {}\n"}, "NewRouter"},
		{"unparsable file is reported", "go-api-chi", map[string]string{"internal/httpapi/broken.go": "package httpapi\nfunc {\n"}, "broken.go"},
		{"test files are ignored", "go-api-chi", map[string]string{apiPath: "package httpapi\n", "internal/httpapi/x_test.go": slogRouter}, "NewRouter"},
		{"rootCmd var passes", "go-cli-cobra", map[string]string{"cmd/root.go": "package cmd\n\nvar (\n\ta       = 1\n\trootCmd = 2\n)\n"}, ""},
		{"rootCmd missing fails", "go-cli-cobra", map[string]string{"cmd/root.go": "package cmd\n\nvar root = 1\n"}, "rootCmd"},
		{"rootCmd as a func fails", "go-cli-cobra", map[string]string{"cmd/root.go": "package cmd\n\nfunc rootCmd() {}\n"}, "rootCmd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := addonRepo(t, tc.stack, "", false)
			if _, ok := tc.files[stackFixtures[tc.stack].path]; ok {
				mustWriteFile(t, filepath.Join(dir, stackFixtures[tc.stack].path), "", 0o644)
			}
			for rel, data := range tc.files {
				mustWriteFile(t, filepath.Join(dir, rel), data, 0o644)
			}
			_, err := RunWith(addonAssets(), dir, Options{Agent: "pi"})
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("RunWith() error = %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("RunWith() error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}

	t.Run("package directory missing fails", func(t *testing.T) {
		dir := addonRepo(t, "go-cli-cobra", "", false)
		mustRemove(t, filepath.Join(dir, "cmd/root.go"))
		mustRemove(t, filepath.Join(dir, "cmd"))
		if _, err := RunWith(addonAssets(), dir, Options{Agent: "pi"}); err == nil || !strings.Contains(err.Error(), "cmd") {
			t.Fatalf("RunWith() error = %v, want a missing-directory failure", err)
		}
	})
}

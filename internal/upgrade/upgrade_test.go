package upgrade

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"forge"
)

func testAssets() fstest.MapFS {
	return fstest.MapFS{
		"templates/common/claude/hooks/guard":              &fstest.MapFile{Data: []byte("#!/usr/bin/env bash\n# guard v1\n")},
		"templates/common/claude/hooks/secret-scan.sh":     &fstest.MapFile{Data: []byte("#!/usr/bin/env bash\n# secret-scan v1\n")},
		"templates/common/claude/settings.json":            &fstest.MapFile{Data: []byte(`{"hooks":{}}` + "\n")},
		"templates/common/codex/hooks.json":                &fstest.MapFile{Data: []byte(`{"hooks":{}}` + "\n")},
		"templates/common/opencode.jsonc.tmpl":             &fstest.MapFile{Data: []byte(`{"lsp":{},"permission":{"bash":{"// BEGIN FORGE ALLOW v:2","// END FORGE ALLOW"}}}` + "\n")},
		"templates/common/opencode/plugins/forge-hooks.js": &fstest.MapFile{Data: []byte(`export const ForgeHooks = async () => {};` + "\n")},
		"templates/common/mise/conf.d/otel.toml":           &fstest.MapFile{Data: []byte("[env]\nOTEL_EXPORTER_OTLP_ENDPOINT = \"http://localhost:4318\"\n")},
		"templates/common/otel/compose.yaml":               &fstest.MapFile{Data: []byte("name: forge-otel\n")},
		"templates/common/otel/collector.yaml":             &fstest.MapFile{Data: []byte("receivers:\n  otlp: {}\n")},
	}
}

func TestCheckReportsStaleWhenVersionFileMissing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Create minimal forge repo marker
	if err := os.MkdirAll(filepath.Join(dir, ".claude", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude", "hooks", "guard"), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	status, err := Run(testAssets(), dir, true)
	if err != nil {
		t.Fatalf("Run(check) error = %v", err)
	}
	if !status.Stale {
		t.Fatal("Run(check) stale = false, want true")
	}
	if status.OnDisk != 0 {
		t.Fatalf("Run(check) OnDisk = %d, want 0", status.OnDisk)
	}
	if status.Version != Version {
		t.Fatalf("Run(check) Version = %d, want %d", status.Version, Version)
	}
}

func TestCheckReportsCurrentWhenVersionMatches(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude", "hooks", "guard"), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".forge-infra-version"), []byte(fmt.Sprintf("%d\n", Version)), 0o644); err != nil {
		t.Fatal(err)
	}

	status, err := Run(testAssets(), dir, true)
	if err != nil {
		t.Fatalf("Run(check) error = %v", err)
	}
	if status.Stale {
		t.Fatal("Run(check) stale = true, want false")
	}
}

func TestCheckDoesNotMutateFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	guardPath := filepath.Join(dir, ".claude", "hooks", "guard")
	if err := os.WriteFile(guardPath, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := Run(testAssets(), dir, true)
	if err != nil {
		t.Fatalf("Run(check) error = %v", err)
	}

	data, err := os.ReadFile(guardPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old" {
		t.Fatalf("Run(check) mutated guard: got %q", string(data))
	}
}

func TestRunOverwritesManagedFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude", "hooks", "guard"), []byte("old-guard"), 0o755); err != nil {
		t.Fatal(err)
	}

	assets := testAssets()
	status, err := Run(assets, dir, false)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(status.Updated) != 8 {
		t.Fatalf("Run() updated %d files, want 8", len(status.Updated))
	}

	// Verify guard was overwritten
	data, err := os.ReadFile(filepath.Join(dir, ".claude", "hooks", "guard"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "#!/usr/bin/env bash\n# guard v1\n" {
		t.Fatalf("guard not overwritten: got %q", string(data))
	}

	// Verify opencode.jsonc was NOT created — it is no longer upgrade-managed.
	if _, err := os.Stat(filepath.Join(dir, "opencode.jsonc")); !os.IsNotExist(err) {
		t.Fatalf("opencode.jsonc created by upgrade (err=%v); it must not be upgrade-managed", err)
	}

	// Verify vendor plugin was created
	if _, err := os.Stat(filepath.Join(dir, ".opencode", "plugins", "forge-hooks.js")); err != nil {
		t.Fatalf(".opencode/plugins/forge-hooks.js not created: %v", err)
	}

	// Verify version file was written
	vData, err := os.ReadFile(filepath.Join(dir, ".forge-infra-version"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(vData)) != strconv.Itoa(Version) {
		t.Fatalf("version file = %q, want %q", string(vData), strconv.Itoa(Version))
	}
}

func TestRunIsIdempotent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude", "hooks", "guard"), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	assets := testAssets()

	// First run: should upgrade
	status1, err := Run(assets, dir, false)
	if err != nil {
		t.Fatalf("Run(1) error = %v", err)
	}
	if len(status1.Updated) == 0 {
		t.Fatal("Run(1) updated nothing")
	}

	// Second run: should be current
	status2, err := Run(assets, dir, false)
	if err != nil {
		t.Fatalf("Run(2) error = %v", err)
	}
	if status2.Stale {
		t.Fatal("Run(2) stale = true after upgrade")
	}
	if len(status2.Updated) != 0 {
		t.Fatalf("Run(2) updated %d files, want 0", len(status2.Updated))
	}
}

func TestRunCreatesDirectoriesForMissingFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Only create the version file as a marker — no .claude/ dir at all
	if err := os.WriteFile(filepath.Join(dir, ".forge-infra-version"), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	status, err := Run(testAssets(), dir, false)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(status.Updated) != 8 {
		t.Fatalf("Run() updated %d files, want 8", len(status.Updated))
	}

	// Verify .codex/hooks.json was created
	if _, err := os.Stat(filepath.Join(dir, ".codex", "hooks.json")); err != nil {
		t.Fatalf(".codex/hooks.json not created: %v", err)
	}

	// Verify .opencode/plugins/forge-hooks.js was created
	if _, err := os.Stat(filepath.Join(dir, ".opencode", "plugins", "forge-hooks.js")); err != nil {
		t.Fatalf(".opencode/plugins/forge-hooks.js not created: %v", err)
	}

	// Verify opencode.jsonc was NOT created — it is no longer upgrade-managed.
	if _, err := os.Stat(filepath.Join(dir, "opencode.jsonc")); !os.IsNotExist(err) {
		t.Fatalf("opencode.jsonc created by upgrade (err=%v); it must not be upgrade-managed", err)
	}
}

func TestRunRejectsNonForgeDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	_, err := Run(testAssets(), dir, false)
	if err == nil {
		t.Fatal("Run() on non-forge dir should fail")
	}
	if !strings.Contains(err.Error(), "not a forge-managed repository") {
		t.Fatalf("Run() error = %q, want 'not a forge-managed repository'", err.Error())
	}
}

func TestRunSetsExecutablePermissionOnHooks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude", "hooks", "guard"), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := Run(testAssets(), dir, false)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, ".claude", "hooks", "guard"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("guard mode = %o, want 755", info.Mode().Perm())
	}
}

func TestStampWritesManifestAndLegacyMarker(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := Stamp(dir, Manifest{Language: "go", Stack: "go-cli-cobra", IncludePersonal: true}); err != nil {
		t.Fatalf("Stamp() error = %v", err)
	}

	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatalf("ReadManifest() error = %v", err)
	}
	if m.SchemaVersion != 1 {
		t.Fatalf("manifest schemaVersion = %d, want 1", m.SchemaVersion)
	}
	if m.InfraVersion != Version {
		t.Fatalf("manifest infraVersion = %d, want %d", m.InfraVersion, Version)
	}
	if m.Language != "go" || m.Stack != "go-cli-cobra" || !m.IncludePersonal {
		t.Fatalf("manifest params not preserved: %+v", m)
	}

	legacy, err := os.ReadFile(filepath.Join(dir, ".forge-infra-version"))
	if err != nil {
		t.Fatalf("legacy marker not written: %v", err)
	}
	if string(legacy) != fmt.Sprintf("%d\n", Version) {
		t.Fatalf("legacy marker = %q, want %q", string(legacy), fmt.Sprintf("%d\n", Version))
	}
}

func TestReadOnDiskVersionPrefersManifestOverLegacyMarker(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Stale legacy marker alongside a current manifest: the manifest wins.
	if err := os.WriteFile(filepath.Join(dir, ".forge-infra-version"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Stamp(dir, Manifest{Language: "go"}); err != nil {
		t.Fatalf("Stamp() error = %v", err)
	}
	if got := readOnDiskVersion(dir); got != Version {
		t.Fatalf("readOnDiskVersion = %d, want %d (manifest must win)", got, Version)
	}
}

func TestReadOnDiskVersionFallsBackToLegacyMarker(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".forge-infra-version"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readOnDiskVersion(dir); got != 2 {
		t.Fatalf("readOnDiskVersion = %d, want 2 (legacy fallback)", got)
	}
}

func TestRunPreservesManifestParamsWhileUpdatingInfraVersion(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude", "hooks", "guard"), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Repo stamped at an older infra version but with a manifest carrying params.
	if err := Stamp(dir, Manifest{Language: "csharp", Stack: "csharp-cli"}); err != nil {
		t.Fatalf("Stamp() error = %v", err)
	}
	stale := filepath.Join(dir, ".forge-infra-version")
	if err := os.WriteFile(stale, []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	forceStale, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	forceStale.InfraVersion = 0
	data, err := json.Marshal(forceStale)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".forge", "manifest.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Run(testAssets(), dir, false); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatalf("ReadManifest() after Run error = %v", err)
	}
	if m.InfraVersion != Version {
		t.Fatalf("manifest infraVersion after Run = %d, want %d", m.InfraVersion, Version)
	}
	if m.Language != "csharp" || m.Stack != "csharp-cli" {
		t.Fatalf("Run() lost manifest params: %+v", m)
	}
}

func TestRunLeavesOpencodeJsoncUntouched(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude", "hooks", "guard"), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	// opencode.jsonc is co-owned and parameterized; upgrade must never overwrite it.
	sentinel := `{"lsp":{"gopls":{}},"hand":"edited"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "opencode.jsonc"), []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}

	status, err := Run(testAssets(), dir, false)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "opencode.jsonc"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != sentinel {
		t.Fatalf("opencode.jsonc mutated by upgrade:\ngot  %q\nwant %q", string(data), sentinel)
	}
	for _, updated := range status.Updated {
		if updated == "opencode.jsonc" {
			t.Fatal("Run() reported opencode.jsonc as updated; it must not be upgrade-managed")
		}
	}
}

func TestRunNeverWritesTemplateSyntaxToDisk(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude", "hooks", "guard"), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Mirror the real repo: the opencode template carries Go-template expressions
	// that are only valid after rendering with init parameters.
	assets := testAssets()
	assets["templates/common/opencode.jsonc.tmpl"] = &fstest.MapFile{
		Data: []byte("{\n{{- if eq .Language \"go\" }}\n  \"lsp\": {\"gopls\": {}}\n{{- end }}\n}\n"),
	}

	status, err := Run(assets, dir, false)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	for _, updated := range status.Updated {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(updated)))
		if err != nil {
			t.Fatalf("read %s: %v", updated, err)
		}
		if strings.Contains(string(data), "{{") {
			t.Fatalf("%s contains unrendered template syntax:\n%s", updated, string(data))
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "opencode.jsonc")); !os.IsNotExist(err) {
		t.Fatalf("opencode.jsonc written by upgrade (err=%v); the raw template must never be copied", err)
	}
}

func TestRunFixesWrongPermissionsOnExistingHooks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-existing guard with WRONG permissions (0644 instead of 0755)
	if err := os.WriteFile(filepath.Join(dir, ".claude", "hooks", "guard"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Run(testAssets(), dir, false)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, ".claude", "hooks", "guard"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("guard mode after upgrade = %o, want 755", info.Mode().Perm())
	}
}

// TestCheckReportsStaleForLegacyRepoMissingOtelAssets proves that a repo
// scaffolded before the OTel collector assets existed (no
// .forge-infra-version at all, so it predates every infra version) is
// reported stale by --check without any mutation.
func TestCheckReportsStaleForLegacyRepoMissingOtelAssets(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude", "hooks", "guard"), []byte("old-guard"), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, rel := range []string{
		filepath.Join(".config", "mise", "conf.d", "otel.toml"),
		filepath.Join(".otel", "compose.yaml"),
		filepath.Join(".otel", "collector.yaml"),
	} {
		if _, err := os.Stat(filepath.Join(dir, rel)); !os.IsNotExist(err) {
			t.Fatalf("precondition: %s should not exist yet (err = %v)", rel, err)
		}
	}

	status, err := Run(testAssets(), dir, true)
	if err != nil {
		t.Fatalf("Run(check) error = %v", err)
	}
	if !status.Stale {
		t.Fatal("Run(check) stale = false, want true for a legacy repo missing the OTel collector assets")
	}

	for _, rel := range []string{
		filepath.Join(".config", "mise", "conf.d", "otel.toml"),
		filepath.Join(".otel", "compose.yaml"),
		filepath.Join(".otel", "collector.yaml"),
	} {
		if _, err := os.Stat(filepath.Join(dir, rel)); !os.IsNotExist(err) {
			t.Fatalf("Run(check) must not mutate: %s exists (err = %v)", rel, err)
		}
	}
}

// TestRunAddsOtelCollectorAssetsToLegacyRepo proves that a stale repo
// lacking the OTel collector assets entirely (predates their introduction)
// gains all three, at the nested destinations, with the parent directories
// created, after a mutating `forge upgrade`.
func TestRunAddsOtelCollectorAssetsToLegacyRepo(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude", "hooks", "guard"), []byte("old-guard"), 0o755); err != nil {
		t.Fatal(err)
	}

	assets := testAssets()
	status, err := Run(assets, dir, false)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	wantUpdated := map[string]bool{
		".config/mise/conf.d/otel.toml": false,
		".otel/compose.yaml":            false,
		".otel/collector.yaml":          false,
	}
	for _, updated := range status.Updated {
		if _, ok := wantUpdated[updated]; ok {
			wantUpdated[updated] = true
		}
	}
	for dest, ok := range wantUpdated {
		if !ok {
			t.Errorf("Run() did not report %s as updated: %v", dest, status.Updated)
		}
	}

	otelToml, err := os.ReadFile(filepath.Join(dir, ".config", "mise", "conf.d", "otel.toml"))
	if err != nil {
		t.Fatalf(".config/mise/conf.d/otel.toml not created: %v", err)
	}
	if !strings.Contains(string(otelToml), "OTEL_EXPORTER_OTLP_ENDPOINT") {
		t.Fatalf("otel.toml missing expected content: %q", otelToml)
	}
	if info, err := os.Stat(filepath.Join(dir, ".config", "mise", "conf.d", "otel.toml")); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o644 {
		t.Fatalf("otel.toml mode = %o, want 644", info.Mode().Perm())
	}

	compose, err := os.ReadFile(filepath.Join(dir, ".otel", "compose.yaml"))
	if err != nil {
		t.Fatalf(".otel/compose.yaml not created: %v", err)
	}
	if !strings.Contains(string(compose), "forge-otel") {
		t.Fatalf("compose.yaml missing expected content: %q", compose)
	}

	if _, err := os.ReadFile(filepath.Join(dir, ".otel", "collector.yaml")); err != nil {
		t.Fatalf(".otel/collector.yaml not created: %v", err)
	}

	// A repeat run is idempotent: nothing is reported as updated.
	status2, err := Run(assets, dir, false)
	if err != nil {
		t.Fatalf("Run(2) error = %v", err)
	}
	if len(status2.Updated) != 0 {
		t.Fatalf("Run(2) updated %v, want none (idempotent)", status2.Updated)
	}
}

// containerPatternAssets extends testAssets with the container overlay files of
// every stack that has the container image pattern (Go, ADR-0023; Python,
// ADR-0026), each stack carrying distinct content.
var (
	goContainerStacks     = []string{"go-cli-cobra", "go-api-chi", "go-web-templ"}
	pythonContainerStacks = []string{"python-cli-typer", "python-fastapi", "python-web-jinja"}
	allContainerStacks    = append(append([]string{}, goContainerStacks...), pythonContainerStacks...)
)

func containerPatternAssets() fstest.MapFS {
	assets := testAssets()
	for _, stack := range allContainerStacks {
		base := "templates/golden/" + stack + "/.forge-overlay/"
		assets[base+"Dockerfile"] = &fstest.MapFile{Data: []byte("# Dockerfile " + stack + "\n")}
		assets[base+".dockerignore"] = &fstest.MapFile{Data: []byte("*\n# dockerignore " + stack + "\n")}
		assets[base+"CONTAINER.md"] = &fstest.MapFile{Data: []byte("# CONTAINER " + stack + "\n")}
	}
	return assets
}

// staleStackedRepo returns a forge repo at infra version 0 whose manifest
// records stack ("" writes a manifest with no stack).
func staleStackedRepo(t *testing.T, stack string) string {
	t.Helper()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude", "hooks", "guard"), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Stamp(dir, Manifest{Language: "go", Stack: stack}); err != nil {
		t.Fatalf("Stamp() error = %v", err)
	}
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	m.InfraVersion = 0
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".forge", "manifest.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func containerFilePresent(t *testing.T, dir, name string) bool {
	t.Helper()

	_, err := os.Lstat(filepath.Join(dir, name))
	if err == nil {
		return true
	}
	if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return false
}

func TestRunAddsContainerFilesForEachContainerStack(t *testing.T) {
	t.Parallel()

	for _, stack := range allContainerStacks {
		t.Run(stack, func(t *testing.T) {
			t.Parallel()

			dir := staleStackedRepo(t, stack)
			status, err := Run(containerPatternAssets(), dir, false)
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			for _, name := range containerFiles {
				data, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil {
					t.Fatalf("%s not added: %v", name, err)
				}
				if !strings.Contains(string(data), stack) {
					t.Errorf("%s = %q, want content from the %s overlay", name, data, stack)
				}
				info, err := os.Stat(filepath.Join(dir, name))
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != 0o644 {
					t.Errorf("%s mode = %o, want 644", name, info.Mode().Perm())
				}
				found := false
				for _, updated := range status.Updated {
					found = found || updated == name
				}
				if !found {
					t.Errorf("Run() did not report %s as updated: %v", name, status.Updated)
				}
			}
		})
	}
}

func TestRunAddsRealContainerFilesFromEmbeddedAssets(t *testing.T) {
	t.Parallel()

	for _, stack := range allContainerStacks {
		t.Run(stack, func(t *testing.T) {
			t.Parallel()

			dir := staleStackedRepo(t, stack)
			if _, err := Run(forge.Assets(), dir, false); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			for _, name := range containerFiles {
				want, err := fs.ReadFile(forge.Assets(), "templates/golden/"+stack+"/.forge-overlay/"+name)
				if err != nil {
					t.Fatalf("read embedded %s overlay %s: %v", stack, name, err)
				}
				got, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil {
					t.Fatalf("%s not added: %v", name, err)
				}
				if string(got) != string(want) {
					t.Errorf("%s differs from the embedded %s overlay", name, stack)
				}
			}
		})
	}
}

func TestRunSkipsContainerFilesForStackWithoutContainerPattern(t *testing.T) {
	t.Parallel()

	for _, stack := range []string{"csharp-cli", "angular", ""} {
		t.Run("stack="+stack, func(t *testing.T) {
			t.Parallel()

			dir := staleStackedRepo(t, stack)
			if _, err := Run(containerPatternAssets(), dir, false); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			for _, name := range containerFiles {
				if containerFilePresent(t, dir, name) {
					t.Errorf("%s added to a %q repo", name, stack)
				}
			}
		})
	}
}

func TestRunSkipsGoContainerFilesWhenBuildPairHasOneFile(t *testing.T) {
	t.Parallel()

	for _, existing := range []string{"Dockerfile", ".dockerignore"} {
		t.Run(existing, func(t *testing.T) {
			t.Parallel()

			dir := staleStackedRepo(t, "go-api-chi")
			if err := os.WriteFile(filepath.Join(dir, existing), []byte("hand written\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			status, err := Run(containerPatternAssets(), dir, false)
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			for _, name := range containerFiles {
				if name == existing {
					continue
				}
				if containerFilePresent(t, dir, name) {
					t.Errorf("%s added although %s already exists", name, existing)
				}
			}
			data, err := os.ReadFile(filepath.Join(dir, existing))
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != "hand written\n" {
				t.Errorf("%s was modified: %q", existing, data)
			}
			for _, updated := range status.Updated {
				for _, name := range containerFiles {
					if updated == name {
						t.Errorf("Run() reported %s as updated", updated)
					}
				}
			}
		})
	}
}

func TestRunSkipsPythonContainerFilesWhenDockerfileIsCustom(t *testing.T) {
	t.Parallel()

	for _, stack := range pythonContainerStacks {
		t.Run(stack, func(t *testing.T) {
			t.Parallel()

			dir := staleStackedRepo(t, stack)
			if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("hand written\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Run(containerPatternAssets(), dir, false); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			for _, name := range []string{".dockerignore", "CONTAINER.md"} {
				if containerFilePresent(t, dir, name) {
					t.Errorf("%s added although a custom Dockerfile exists", name)
				}
			}
		})
	}
}

func TestRunPythonContainerFilesSecondRunIsNoOp(t *testing.T) {
	t.Parallel()

	dir := staleStackedRepo(t, "python-fastapi")
	if _, err := Run(containerPatternAssets(), dir, false); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("# my edits\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, err := Run(containerPatternAssets(), dir, false)
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if len(status.Updated) != 0 {
		t.Errorf("second Run() updated %v, want nothing", status.Updated)
	}
}

func TestRunKeepsExistingContainerMd(t *testing.T) {
	t.Parallel()

	dir := staleStackedRepo(t, "go-cli-cobra")
	if err := os.WriteFile(filepath.Join(dir, "CONTAINER.md"), []byte("my notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, err := Run(containerPatternAssets(), dir, false)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "CONTAINER.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "my notes\n" {
		t.Errorf("CONTAINER.md overwritten: %q", data)
	}
	for _, name := range []string{"Dockerfile", ".dockerignore"} {
		if !containerFilePresent(t, dir, name) {
			t.Errorf("%s not added", name)
		}
	}
	for _, updated := range status.Updated {
		if updated == "CONTAINER.md" {
			t.Error("Run() reported the untouched CONTAINER.md as updated")
		}
	}
}

func TestRunSkipsGoContainerFilesWithoutManifest(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude", "hooks", "guard"), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(containerPatternAssets(), dir, false); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, name := range containerFiles {
		if containerFilePresent(t, dir, name) {
			t.Errorf("%s added to a legacy repo with no manifest", name)
		}
	}
}

func TestRunGoContainerFilesSecondRunIsNoOp(t *testing.T) {
	t.Parallel()

	dir := staleStackedRepo(t, "go-web-templ")
	if _, err := Run(containerPatternAssets(), dir, false); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	// The user then edits the Dockerfile; a second upgrade must not touch it.
	edited := []byte("# my edits\n")
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), edited, 0o644); err != nil {
		t.Fatal(err)
	}
	status, err := Run(containerPatternAssets(), dir, false)
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if len(status.Updated) != 0 {
		t.Errorf("second Run() updated %v, want nothing", status.Updated)
	}
	data, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(edited) {
		t.Errorf("Dockerfile changed on the second run: %q", data)
	}
}

// Not parallel: it swaps os.Stderr to capture the notice.
func TestRunPrintsContainerNoticeOnlyWhenFilesAdded(t *testing.T) {
	capture := func(t *testing.T, dir string) string {
		t.Helper()

		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		orig := os.Stderr
		os.Stderr = w
		_, runErr := Run(containerPatternAssets(), dir, false)
		os.Stderr = orig
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		out, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if runErr != nil {
			t.Fatalf("Run() error = %v", runErr)
		}
		return string(out)
	}

	added := capture(t, staleStackedRepo(t, "go-api-chi"))
	for _, want := range []string{"image:lint", "image:scan", "hadolint", "trivy", "PORT", "slog", "mise.toml", "CONTAINER.md"} {
		if !strings.Contains(added, want) {
			t.Errorf("notice missing %q:\n%s", want, added)
		}
	}

	if strings.Contains(added, "uv.lock") {
		t.Errorf("Go notice mentions uv.lock:\n%s", added)
	}

	for _, stack := range pythonContainerStacks {
		py := capture(t, staleStackedRepo(t, stack))
		for _, want := range []string{"container image", "uv.lock", "uv lock", "ADR-0026", "CONTAINER.md", "mise.toml", "pyproject.toml"} {
			if !strings.Contains(py, want) {
				t.Errorf("%s notice missing %q:\n%s", stack, want, py)
			}
		}
		if strings.Contains(py, "slog") {
			t.Errorf("%s notice mentions slog:\n%s", stack, py)
		}
	}

	if out := capture(t, staleStackedRepo(t, "csharp-cli")); strings.Contains(out, "container image") {
		t.Errorf("notice printed for a stack without the container pattern:\n%s", out)
	}
}

// An upgrade interrupted partway through the container files must complete on
// retry. Each case seeds the exact on-disk state after some of forge's own
// writes landed (byte-identical to the embedded files) and then runs upgrade:
// the full set must exist, with forge's content, before the version advances.
func TestRunCompletesInterruptedGoContainerInstall(t *testing.T) {
	t.Parallel()

	const stack = "go-api-chi"
	cases := map[string][]string{
		"after .dockerignore":            {".dockerignore"},
		"after .dockerignore and README": {".dockerignore", "CONTAINER.md"},
		"after Dockerfile only":          {"Dockerfile"},
		"after Dockerfile and README":    {"Dockerfile", "CONTAINER.md"},
		"after CONTAINER.md only":        {"CONTAINER.md"},
		"after both build files":         {".dockerignore", "Dockerfile"},
	}
	for name, present := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assets := containerPatternAssets()
			embedded := func(file string) []byte {
				return assets["templates/golden/"+stack+"/.forge-overlay/"+file].Data
			}
			dir := staleStackedRepo(t, stack)
			for _, file := range present {
				if err := os.WriteFile(filepath.Join(dir, file), embedded(file), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			status, err := Run(assets, dir, false)
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			for _, file := range containerFiles {
				data, err := os.ReadFile(filepath.Join(dir, file))
				if err != nil {
					t.Fatalf("%s missing after the retry: %v", file, err)
				}
				if string(data) != string(embedded(file)) {
					t.Errorf("%s = %q, want the embedded content", file, data)
				}
			}
			wantAdded := len(containerFiles) - len(present)
			gotAdded := 0
			for _, updated := range status.Updated {
				for _, file := range containerFiles {
					if updated == file {
						gotAdded++
						for _, already := range present {
							if already == file {
								t.Errorf("Run() reported %s as added although it was already in place", file)
							}
						}
					}
				}
			}
			if gotAdded != wantAdded {
				t.Errorf("Run() added %d container files, want %d (updated: %v)", gotAdded, wantAdded, status.Updated)
			}
			if got := readOnDiskVersion(dir); got != Version {
				t.Errorf("infra version = %d after a completed retry, want %d", got, Version)
			}
			leftovers, err := filepath.Glob(filepath.Join(dir, ".forge-upgrade-*"))
			if err != nil {
				t.Fatal(err)
			}
			if len(leftovers) != 0 {
				t.Errorf("temporary files left behind: %v", leftovers)
			}
		})
	}
}

// A failure while delivering the container files must leave the repo stale, so
// the next upgrade retries instead of returning early as "current".
func TestRunDoesNotStampVersionWhenGoContainerWriteFails(t *testing.T) {
	t.Parallel()

	assets := containerPatternAssets()
	dir := staleStackedRepo(t, "go-cli-cobra")
	// Force the failure through a missing embedded source.
	delete(assets, "templates/golden/go-cli-cobra/.forge-overlay/CONTAINER.md")

	if _, err := Run(assets, dir, false); err == nil {
		t.Fatal("Run() error = nil, want a failure reading the embedded CONTAINER.md")
	}
	if got := readOnDiskVersion(dir); got == Version {
		t.Errorf("infra version advanced to %d although the container files were not delivered", got)
	}
	for _, file := range containerFiles {
		if containerFilePresent(t, dir, file) {
			t.Errorf("%s written although the set could not be completed", file)
		}
	}

	// With the asset restored the same repo upgrades fully.
	if _, err := Run(containerPatternAssets(), dir, false); err != nil {
		t.Fatalf("retry Run() error = %v", err)
	}
	for _, file := range containerFiles {
		if !containerFilePresent(t, dir, file) {
			t.Errorf("%s missing after the retry", file)
		}
	}
	if got := readOnDiskVersion(dir); got != Version {
		t.Errorf("infra version = %d after the retry, want %d", got, Version)
	}
}

package upgrade

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
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
	if err := Stamp(dir, Manifest{Language: "python", Stack: "python-cli-typer"}); err != nil {
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
	if m.Language != "python" || m.Stack != "python-cli-typer" {
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
